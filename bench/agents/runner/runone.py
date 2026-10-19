"""One run: a fresh workspace, up to max_rounds of claude -p with a hidden check after each, then
the raw files and measures in <tier>/runs/<run-id>/."""
import glob
import math
import os
import secrets
import shutil
import signal
import subprocess
import tempfile
import time
import uuid

import common
import measures
import spend
import stream

COST_NOTE = {"cost-state": "from cost-state", "estimate": "estimated from message usage", "unknown": "unknown"}


def run_id(set_name, bug, arm, n):
    return "%s-%s-%s-%d" % (set_name, bug, arm, n)


def arm_flags(cfg, arm):
    """claude_flags; a read arm also gets --tools with read_tools, an allow list of built-in tools
    (CLI 2.1.299 then lists exactly those in init). The web tools stay disallowed."""
    flags = list(cfg["claude_flags"])
    if arm in common.READ_ARMS:
        flags += ["--tools", ",".join(cfg["read_tools"])]
    return flags


def claude_cmd(cfg, arm, prompt, sid, first, budget, add_dir, model=None, extra=None):
    """The prompt sits right after -p: --allowedTools and --add-dir are variadic."""
    cmd = [cfg["claude"], "-p", prompt, "--model", model or cfg["model"],
           "--max-budget-usd", "%.2f" % (math.floor(budget * 100) / 100)]
    cmd += arm_flags(cfg, arm) + ["--settings", common.settings_json(cfg, arm)]
    if arm == "plugin":
        cmd += ["--plugin-dir", cfg["paths"]["plugin"]]
    cmd += list(extra or [])
    if add_dir:
        cmd += ["--add-dir", add_dir]
    cmd += ["--session-id", sid] if first else ["--resume", sid]
    return cmd


def _tree(pid):
    """The descendants of pid, from ps: a Bash tool child may sit in its own process group."""
    kids = {}
    for line in common.sh(["ps", "-A", "-o", "pid=", "-o", "ppid="], timeout=30)[1].splitlines():
        f = line.split()
        if len(f) == 2 and f[0].isdigit() and f[1].isdigit():
            kids.setdefault(int(f[1]), []).append(int(f[0]))
    out, todo = [], [pid]
    while todo:
        for c in kids.get(todo.pop(), []):
            out.append(c)
            todo.append(c)
    return out


def _kill(proc):
    """SIGINT, then SIGTERM, to the process group, 5 s apart until the leader exits; then SIGKILL
    to the group and to every descendant seen before the first signal."""
    tree = _tree(proc.pid)
    for sig in (signal.SIGINT, signal.SIGTERM):
        common.kill_group(proc.pid, sig)
        try:
            proc.wait(5)
            break
        except subprocess.TimeoutExpired:
            continue
    common.kill_group(proc.pid, signal.SIGKILL)
    for pid in tree:
        try:
            os.kill(pid, signal.SIGKILL)
        except OSError:
            pass
    proc.wait()


def run_claude(cmd, cwd, env, stream_path, err_path, timeout_s, on_init=None, meter=None, sid=None):
    """Runs one claude invocation, writing stdout to stream_path. on_init(init, hooks) is called
    when the init message arrives; when it returns False the process is killed (refused). With a
    meter, the process is killed once the meter's estimate passes its limit (budget_killed)."""
    start, deadline = time.time(), time.time() + timeout_s
    out, err = open(stream_path, "w"), open(err_path, "w")
    proc = subprocess.Popen(cmd, cwd=cwd, env=env, stdout=out, stderr=err, stdin=subprocess.DEVNULL,
                            start_new_session=True)
    state = {"timed_out": False, "refused": False, "budget_killed": False}
    tail, init, hooks, polled = spend.Tail(stream_path), None, [], 0.0
    try:
        while True:
            done = proc.poll() is not None
            evs = tail.events()
            for e in (evs if init is None else []):
                hooks += stream.hooks_of([e])
                if e.get("type") == "system" and e.get("subtype") == "init":
                    init = e
                    state["refused"] = on_init is not None and not on_init(e, hooks)
                    break
            if state["refused"]:
                _kill(proc)
                break
            if meter is not None:
                meter.feed(evs)
                if done or time.time() - polled >= 2:
                    meter.poll((init or {}).get("session_id") or sid)
                    polled = time.time()
                if meter.over():
                    state["budget_killed"] = True
                    _kill(proc)
                    break
            if done:
                break
            if time.time() > deadline:
                state["timed_out"] = True
                _kill(proc)
                break
            time.sleep(0.2)
    finally:
        if proc.poll() is None:
            _kill(proc)
        out.close()
        err.close()
    if meter is not None:  # what the process wrote before it was killed
        meter.feed(tail.events())
        meter.poll((init or {}).get("session_id") or sid)
    state["rc"] = proc.returncode
    state["wall_s"] = round(time.time() - start, 1)
    return state


def _copy_transcripts(cfg, sids, dest):
    os.makedirs(dest, exist_ok=True)
    pdir = common.projects_dir(cfg)
    for sid in sids:
        for f in glob.glob(os.path.join(pdir, "*", sid + ".jsonl")):
            shutil.copy2(f, os.path.join(dest, sid + ".jsonl"))
            d = f[:-len(".jsonl")]
            if os.path.isdir(d) and not os.path.exists(os.path.join(dest, sid)):
                common.copytree(d, os.path.join(dest, sid))


def _git(ws, *args):
    return common.sh(["git", "-c", "user.name=dev", "-c", "user.email=dev@example.com", "-c", "commit.gpgsign=false",
                      "-c", "core.quotepath=off"] + list(args), cwd=ws)


def _prompt(cfg, version, bug):
    with open(os.path.join(cfg["paths"]["prompts"], "task.md")) as f:
        task = f.read().rstrip("\n")
    with open(os.path.join(cfg["paths"]["prompts"], "followup.md")) as f:
        follow = f.read().strip()
    with open(os.path.join(cfg["paths"]["service"], "logs", "%s-%s.txt" % (version, bug))) as f:
        log = f.read().rstrip("\n")
    return task.replace("{{LOG}}", log), follow


def prepare_workspace(cfg, arm, version, bug, ws, tmpd):
    """Copies the variant with prepare.sh, which writes its go.mod, adds the docs arm's files, then
    git init and one commit. Returns (initial commit, faultline source): faultline comes from the
    module cache at the version prepare.sh requires."""
    src = common.variant_dir(cfg, version, bug)
    source = "module-cache" if version == "faultline" else "-"
    rc, out = common.sh([os.path.join(cfg["paths"]["service"], "prepare.sh"), src, ws],
                        env=common.child_env(cfg, {"TMPDIR": tmpd}), timeout=600)
    if rc != 0:
        raise RuntimeError("prepare.sh failed (%d): %s" % (rc, out[-2000:]))
    if arm == "docs":
        common.add_docs(cfg, ws)
    hits = measures.forbidden_hits(measures.tree_files(ws), common.forbidden_patterns(cfg))
    if hits:
        raise RuntimeError("the prepared workspace names a forbidden path: " + "; ".join(hits))
    for args in (["init", "-q", "-b", "main"], ["add", "-A"], ["commit", "-q", "-m", "Initial commit"]):
        rc, out = _git(ws, *args)
        if rc != 0:
            raise RuntimeError("git %s failed: %s" % (args[0], out))
    return _git(ws, "rev-parse", "HEAD")[1].strip(), source


def _round_totals(cfg, rec, res, before, meter, sid):
    """(running totals after the round, where they came from): the result, else the transcript's
    last cost-state record when it moved past before, else before plus the meter's estimate."""
    if res is not None:
        return spend.totals_of_result(res), "result"
    cs = spend.last_cost_state(common.projects_dir(cfg), rec["session_ids"] or [sid])
    if cs is not None and cs["cost"] > before["cost"] + 1e-9:
        return cs, "cost-state"
    if meter.usd() is not None:
        return dict(before, cost=before["cost"] + meter.usd()), "estimate"
    return before, "unknown"


def do_run(cfg, set_name, arm, bug, n, log=print, user=None):
    """Runs one (arm, bug, n) and returns its run.json record."""
    rid = run_id(set_name, bug, arm, n)
    run_dir = os.path.join(cfg["paths"]["runs"], rid)
    if os.path.exists(run_dir):
        retired = os.path.join(cfg["paths"]["runs"], "_retired")
        os.makedirs(retired, exist_ok=True)
        shutil.move(run_dir, os.path.join(retired, rid + "-" + time.strftime("%Y%m%dT%H%M%S")))
    os.makedirs(run_dir)
    version = common.ARM_VERSION[arm]
    sid = str(uuid.uuid4())
    rec = {"set": set_name, "run_id": rid, "arm": arm, "bug": bug, "run": n, "version": version,
           "model": cfg["model"], "status": "running", "fixed": False, "fixed_round": "", "rounds": 0,
           "wrong_fixes": 0, "cost_usd": 0.0, "turns": 0, "wall_s": 0.0, "api_ms": 0, "denials": 0,
           "budget_out": False, "timed_out": False, "isolation": "", "isolation_notes": [], "session_ids": [sid],
           "denied": [], "round_end": [], "hidden_exit": [], "round_cost_usd": [], "round_wall_s": [],
           "model_usage": {}, "cost_notes": [], "weighted_tokens_transcripts": None, "test_runs_after_edit": None,
           "failing_test_runs_after_edit": None, "undo_check_runs": None, "started": time.strftime("%Y-%m-%dT%H:%M:%S")}
    rec["cli_version"] = common.sh([cfg["claude"], "--version"], env=common.child_env(cfg), timeout=60)[1].strip()
    root = cfg["paths"]["workspace_root"]
    os.makedirs(root, exist_ok=True)
    ws, tmpd = os.path.join(root, secrets.token_hex(4)), os.path.join(root, secrets.token_hex(4))
    os.makedirs(tmpd)
    scratch = tempfile.mkdtemp(prefix="bench-")
    streams, run_json = [], os.path.join(run_dir, "run.json")
    try:
        prompt, follow = _prompt(cfg, version, bug)
        rec["initial_commit"], rec["faultline_source"] = prepare_workspace(cfg, arm, version, bug, ws, tmpd)
        common.copytree(ws, os.path.join(run_dir, "original"), ignore=shutil.ignore_patterns(".git"))
        common.write_json(run_json, rec)
        problems, hidden_tmp = [], os.path.join(scratch, "hidden")
        os.makedirs(hidden_tmp)
        before, rates, top, seen = spend.ZERO, {}, None, set()

        def on_init(init, hooks):
            ok, probs, notes = stream.check_init(init, hooks, arm, cfg, user)
            rec["model"] = init.get("model") or "?"
            if rec["model"] != cfg["model"]:
                ok, probs = False, probs + ["model %s, want %s" % (rec["model"], cfg["model"])]
            problems.extend(probs)
            rec["isolation_notes"] = sorted(set(rec["isolation_notes"]) | set(notes))
            return ok

        # a read arm (the screen) gets one round and no follow-up
        for r in range(1, (1 if arm in common.READ_ARMS else cfg["max_rounds"]) + 1):
            left_usd = common.run_cap(cfg, set_name) - rec["cost_usd"]
            left_s = cfg["run_wall_s"] - rec["wall_s"]  # agent time; hidden checks do not count
            if left_usd < 0.01 or left_s <= 0:
                rec["budget_out"] = rec["budget_out"] or left_usd < 0.01
                rec["timed_out"] = rec["timed_out"] or left_s <= 0
                break
            sp = os.path.join(run_dir, "stream-r%d.jsonl" % r)
            streams.append(sp)
            cmd = claude_cmd(cfg, arm, prompt if r == 1 else follow, sid, r == 1, left_usd, tmpd)
            # Round 1 has the CLI's own cap; from round 2 the runner also enforces what is left.
            meter = spend.Meter(rates, top, left_usd if rates else None, seen, common.projects_dir(cfg))
            st = run_claude(cmd, ws, common.child_env(cfg, {"TMPDIR": tmpd}), sp,
                            os.path.join(run_dir, "stderr-r%d.txt" % r), left_s, on_init, meter, sid)
            seen |= meter.ids()
            rec["rounds"] = r
            rec["round_wall_s"].append(st["wall_s"])
            rec["wall_s"] = round(rec["wall_s"] + st["wall_s"], 1)
            if st["refused"]:
                rec["status"], rec["isolation"] = "refused", "fail: " + "; ".join(problems)
                return rec
            evs = stream.events(sp)
            res = stream.result_of(evs)
            if res is None and not (st["timed_out"] or st["budget_killed"]):
                raise RuntimeError("round %d: no result message (exit %s); see stderr-r%d.txt" % (r, st["rc"], r))
            rec["round_end"].append("budget-killed" if st["budget_killed"] else "timed-out" if st["timed_out"]
                                    else str(res.get("subtype")))
            if res is not None:
                if res.get("is_error") and res.get("num_turns", 0) <= 1 and not res.get("total_cost_usd"):
                    raise RuntimeError("round %d: %s" % (r, str(res.get("result"))[:300]))
                rec["turns"] += int(res.get("num_turns") or 0)  # per call, unlike the cost
                den = res.get("permission_denials") or []
                rec["denials"] += len(den)
                rec["denied"] += [{"round": r, "tool": d.get("tool_name"), "input": d.get("tool_input")} for d in den]
                sid = res.get("session_id") or sid  # the next round resumes what this result names
                if sid not in rec["session_ids"]:
                    rec["session_ids"].append(sid)
                if "budget" in str(res.get("subtype", "")):
                    rec["budget_out"] = True
            now, src = _round_totals(cfg, rec, res, before, meter, sid)
            if res is None:
                rec["cost_notes"].append("round %d killed: cost %s" % (r, COST_NOTE[src]))
            if now["cost"] < before["cost"] - 1e-9:
                raise RuntimeError("round %d: total_cost_usd fell from %.6f to %.6f; the CLI no longer reports "
                                   "running totals on --resume (run bench.py probe)" % (r, before["cost"], now["cost"]))
            rec["round_cost_usd"].append(round(now["cost"] - before["cost"], 6))
            rec["cost_usd"] = round(rec["cost_usd"] + now["cost"] - before["cost"], 6)
            spend.add(rec["model_usage"], spend.minus(now["usage"], before["usage"]))
            rec["api_ms"] += max(0, now["api_ms"] - before["api_ms"])
            if src in ("result", "cost-state"):
                for m, rate in spend.rates_of(now["usage"]).items():
                    rates[m], top = rate, rate if top is None else max(top, rate)
            before = now
            rec["timed_out"] = rec["timed_out"] or st["timed_out"]
            if st["budget_killed"] or common.run_cap(cfg, set_name) - rec["cost_usd"] < 0.01:
                rec["budget_out"] = True
            common.write_json(run_json, rec)  # the spend so far survives a crash of the runner
            hooks = stream.hooks_of(evs)
            if hooks:
                problems.extend("hook " + h for h in hooks)
                rec["status"], rec["isolation"] = "refused", "fail: " + "; ".join(dict.fromkeys(problems))
                return rec
            # hidden.sh gets a fresh copy, so nothing it does can reach the agent's workspace
            hdir = os.path.join(scratch, "hidden-r%d" % r)
            common.copytree(ws, hdir)
            hcode, hout = common.sh([os.path.join(cfg["paths"]["service"], "hidden.sh"), version, hdir],
                                    env=common.child_env(cfg, {"TMPDIR": hidden_tmp}), timeout=cfg["hidden_timeout_s"])
            common.rmtree(hdir)
            with open(os.path.join(run_dir, "hidden-r%d.log" % r), "w") as f:
                f.write(hout)
            rec["hidden_exit"].append(hcode)
            if hcode >= 3:
                raise RuntimeError("round %d: hidden check infrastructure error (exit %d)" % (r, hcode))
            if hcode == 0:
                rec["fixed"], rec["fixed_round"] = True, r
                break
            rec["wrong_fixes"] += 1
            if rec["budget_out"] or rec["timed_out"]:
                break
        rec["isolation"] = "pass" if not problems else "fail: " + "; ".join(problems)
        _finish(cfg, rec, run_dir, ws, tmpd, scratch, streams, version, arm)
        rec["status"] = "done"
    except Exception as e:  # recorded; the set stops on an error
        rec["status"], rec["error"] = "error", str(e)
    finally:
        if os.path.isdir(ws) and not os.path.exists(os.path.join(run_dir, "workspace")):
            common.copytree(ws, os.path.join(run_dir, "workspace"))
        _move_project_folders(cfg, ws, run_dir)
        for d in (ws, tmpd, scratch):
            common.rmtree(d)
        rec["finished"] = time.strftime("%Y-%m-%dT%H:%M:%S")
        common.write_json(run_json, rec)
    return rec


def _move_project_folders(cfg, ws, dest):
    """Moves the workspace's transcript folder out of the CLI's projects folder, so later runs
    cannot read this run's transcripts."""
    for p in common.project_folders(cfg, ws):
        os.makedirs(os.path.join(dest, "claude-project"), exist_ok=True)
        shutil.move(p, os.path.join(dest, "claude-project", os.path.basename(p)))


def _finish(cfg, rec, run_dir, ws, tmpd, scratch, streams, version, arm):
    tdir = os.path.join(run_dir, "transcripts")
    _copy_transcripts(cfg, rec["session_ids"], tdir)
    final = os.path.join(run_dir, "workspace")
    common.copytree(ws, final)
    common.copytree(tmpd, os.path.join(run_dir, "tmp"), ignore=shutil.ignore_patterns("go-build*"))
    _git(final, "add", "-A")
    rc, diff = _git(final, "diff", "--cached", "--no-color", rec["initial_commit"])
    with open(os.path.join(run_dir, "final.diff"), "w") as f:
        f.write(diff)
    # tokens from the CLI's modelUsage (each round's difference of the running totals, subagents
    # included); the count over the transcripts stays as a cross-check
    rec["weighted_tokens"], rec["processed_tokens"], rec["output_tokens"] = measures.model_usage_tokens(rec["model_usage"])
    rec["weighted_tokens_transcripts"] = measures.tokens(tdir)[0]
    rec["go_test_calls"] = measures.go_test_calls(streams, tdir)
    rec["test_runs_after_edit"], rec["failing_test_runs_after_edit"] = measures.test_runs_after_edit(streams)
    rec["undo_check_runs"] = measures.undo_check_runs(streams)
    reg = os.path.join(scratch, "regression")
    os.makedirs(reg)
    env = common.child_env(cfg, {"TMPDIR": reg})
    rec["regression_test"], rec["new_tests"] = measures.regression(
        cfg, version, os.path.join(run_dir, "original"), final, reg, env, os.path.join(run_dir, "regression"))
    rec["visible_checks_changed"] = measures.visible_checks(os.path.join(run_dir, "original"), final)
    files = list(streams) + measures.transcript_files(tdir)
    own = [f for p in common.project_folders(cfg, ws) for f in common.home_forms(p)]
    rec["forbidden_hits"] = measures.forbidden_hits(files, common.forbidden_patterns(cfg), allow=own)
    rec["forbidden_path"] = bool(rec["forbidden_hits"])
