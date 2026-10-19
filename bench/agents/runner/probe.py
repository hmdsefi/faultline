"""probe: checks isolation and permissions. The init and memory checks use the local /context
command and cost nothing; the login, permission, repository-read, plugin-read, resume and subagent
checks call the cheap probe model, and so does the check that a read arm (the screen) cannot run a
command. Every arm runs in bypassPermissions mode, where only the deny rules decide."""
import json
import os
import secrets
import time
import uuid

import common
import measures
import runone
import spend
import stream

# Every arm runs in bypassPermissions mode: these must run without a denial, the faultline workflow's
# git stash and Python edit scripts included, and a compound command (COMPOUND).
COMMANDS = ["go version", "FAULTLINE_SEED=1 go env GOPATH", "ls", "cat probe.txt", "grep -n probe probe.txt",
            "sed -n 1p probe.txt", "git diff", "git stash", "git ls-files", "python3 -c 'print(1)'"]
COMPOUND = "cd {ws} && git stash list; ls"
HELPER = "probe-helper"
# A read arm's check: only a command can print this variable's value, which the runner draws per call.
TOKEN_VAR = "PROBE_TOKEN"
READ_PROMPT = ("This is a permissions check. Run the shell command echo ${v} in three ways, one after the "
               "other: with the Bash tool; with the Monitor tool; and through a subagent: use the Task tool (also "
               "called Agent) once, with subagent_type general-purpose and the prompt: Run the Bash command echo "
               "${v} and reply with its output. Then use the Glob tool once with the pattern * in the current folder. "
               "Do not stop when a tool is missing or a call is denied. At the end reply with the single word "
               "DONE.").replace("{v}", TOKEN_VAR)
# Tools that run a command, directly or through an agent; the read check fails when init lists one.
COMMAND_TOOLS = ("Bash", "Task", "Agent", "Monitor", "Workflow", "SendMessage", "RemoteTrigger")


def command_tools(tools):
    """The tools in a tool list that can run a command: COMMAND_TOOLS and every Cron tool."""
    return sorted(t for t in tools if t in COMMAND_TOOLS or t.startswith("Cron"))


def commands(ws):
    return COMMANDS + [COMPOUND.format(ws=ws)]


def _workspace(cfg, arm):
    root = cfg["paths"]["workspace_root"]
    os.makedirs(root, exist_ok=True)
    ws = os.path.join(root, secrets.token_hex(4))
    os.makedirs(ws)
    with open(os.path.join(ws, "probe.txt"), "w") as f:
        f.write("probe line one\n")
    if arm == "docs":
        common.add_docs(cfg, ws)
    for args in (["init", "-q", "-b", "main"], ["add", "-A"], ["commit", "-q", "-m", "Initial commit"]):
        runone._git(ws, *args)
    return ws


def _call(cfg, out_dir, name, arm, prompt, ws, sid, first, budget, model, extra=(), env=None):
    tmpd = os.path.join(cfg["paths"]["workspace_root"], secrets.token_hex(4))
    os.makedirs(tmpd)
    cmd = runone.claude_cmd(cfg, arm, prompt, sid, first, budget, tmpd, model=model, extra=list(extra))
    sp = os.path.join(out_dir, name + ".jsonl")
    seen = {}

    def on_init(init, hooks):
        seen["init"], seen["hooks"] = init, list(hooks)
        return True

    st = runone.run_claude(cmd, ws, common.child_env(cfg, dict(env or {}, TMPDIR=tmpd)), sp,
                           os.path.join(out_dir, name + ".stderr.txt"), cfg["probe"]["timeout_s"], on_init)
    common.rmtree(tmpd)
    evs = stream.events(sp)
    seen["hooks"] = seen.get("hooks", []) + [h for h in stream.hooks_of(evs) if h not in seen.get("hooks", [])]
    with open(os.path.join(out_dir, name + ".cmd.json"), "w") as f:
        json.dump(cmd, f, indent=1)
    return st, evs, seen.get("init"), seen.get("hooks", []), stream.result_of(evs)


def _summary(init):
    if not init:
        return "no init message"
    return "plugins=%s mcp=%s agents=%s skills=%d (%s) permissionMode=%s model=%s" % (
        [p.get("name") for p in init.get("plugins") or []], [m.get("name") for m in init.get("mcp_servers") or []],
        init.get("agents"), len(init.get("skills") or []),
        ", ".join(s for s in init.get("skills") or [] if ":" in s) or "no plugin skills",
        init.get("permissionMode"), init.get("model"))


def run(cfg, log=print, user=None):
    out_dir = os.path.join(cfg["paths"]["runs"], "probe", time.strftime("%Y%m%dT%H%M%S"))
    os.makedirs(out_dir)
    pc, checks, spent, wss = cfg["probe"], [], 0.0, {}

    def check(name, ok, detail=""):
        checks.append({"check": name, "result": ok if isinstance(ok, str) else ("pass" if ok else "fail"),
                       "detail": detail})
        log("%-28s %-5s %s" % (name, checks[-1]["result"], detail))

    env = common.child_env(cfg)
    ver = common.sh([cfg["claude"], "--version"], env=env, timeout=60)[1].strip()
    log("claude %s; flags: %s" % (ver, " ".join(cfg["claude_flags"])))
    rc, out = common.sh([cfg["claude"], "auth", "status"], env=env, timeout=60)
    try:
        logged_in = bool(json.loads(out).get("loggedIn"))
    except ValueError:
        logged_in = False
    check("login (auth status)", logged_in, out.strip().replace("\n", " ")[:200])
    try:
        for arm in cfg["arms"] + cfg.get("screen_arms", []):
            ws = wss[arm] = _workspace(cfg, arm)
            st, evs, init, hooks, res = _call(cfg, out_dir, "init-" + arm, arm, "/context", ws, str(uuid.uuid4()),
                                              True, pc["call_cap_usd"], pc["model"])
            spent += float((res or {}).get("total_cost_usd") or 0)
            ok, probs, notes = stream.check_init(init, hooks, arm, cfg, user)
            log("  init %s: %s" % (arm, _summary(init)))
            check("isolation " + arm, ok, "; ".join(probs + notes) or "only built-ins" + (" and the plugin" if arm == "plugin" else ""))
            mem = [os.path.realpath(p) for p in stream.memory_files(stream.text_of(evs))]
            want = [os.path.realpath(os.path.join(ws, "CLAUDE.md"))] if arm == "docs" else []
            check("memory files " + arm, mem == want, ", ".join(mem) or "none")
        if not logged_in:
            for name in paid_checks(cfg):
                check(name, "skip", "not logged in: run `claude auth login` or set CLAUDE_CODE_OAUTH_TOKEN")
            return _done(out_dir, checks, spent, log)
        spent += _paid(cfg, out_dir, wss, check, log, spent)
    finally:
        for ws in wss.values():
            runone._move_project_folders(cfg, ws, out_dir)
            common.rmtree(ws)
    return _done(out_dir, checks, spent, log)


PAID_CHECKS = ["login (paid call)", "permissions", "repo read", "repo cat", "plugin file read", "plugin denied plain",
               "plugin denied docs", "resume", "subagent usage"]


def paid_checks(cfg):
    """PAID_CHECKS, with one "no command" check per screen arm after the plugin checks."""
    i = PAID_CHECKS.index("resume")
    return PAID_CHECKS[:i] + ["no command " + a for a in cfg.get("screen_arms", [])] + PAID_CHECKS[i:]


def _rules(cfg):
    return os.path.join(cfg["paths"]["plugin"], "skills", "faultline", "rules.md")


def _paid(cfg, out_dir, wss, check, log, spent0):
    """The paid checks; returns what they spent."""
    pc, spent = cfg["probe"], 0.0
    repo, rules = os.path.realpath(cfg["paths"]["repo"]), _rules(cfg)
    lines = ["This is a permissions check. Use the Bash tool once for each command below, exactly as written, "
             "one command per call. Pass each command character for character: keep a leading cd even when you are "
             "already in that folder, and do not split, shorten or fix any command:"]
    cmds = commands(wss["plugin"])
    lines += ["%d. %s" % (i + 1, c) for i, c in enumerate(cmds + ["cat %s/go.mod" % repo])]
    lines += ["Then use the Read tool on %s/go.mod and on %s." % (repo, rules),
              "Do not stop when a call is denied. At the end reply with the single word DONE."]
    sid = str(uuid.uuid4())
    extra = ["--tools", pc["tools"]] if pc.get("tools") else []
    st, evs, init, hooks, res = _call(cfg, out_dir, "paid-permissions", "plugin", "\n".join(lines), wss["plugin"], sid,
                                      True, pc["call_cap_usd"], pc["model"], extra)
    cost = float((res or {}).get("total_cost_usd") or 0)
    spent += cost
    log("  paid-permissions: init %s; cost $%.4f" % (_summary(init), cost))
    ok = bool(res) and not res.get("is_error") and cost > 0
    check("login (paid call)", ok, "" if ok else str((res or {}).get("result", "no result"))[:200])
    if not ok:
        for name in paid_checks(cfg)[1:]:
            check(name, "skip", "login failed")
        return spent
    uses, sent = stream.tool_uses(evs), _sent(evs)
    denied = {d.get("tool_use_id") for d in res.get("permission_denials") or []}
    bad, missing = [], []
    for c in cmds:
        ids = [i for i, n, inp in uses if n == "Bash" and " ".join(sent.get(i, str(inp.get("command", ""))).split()) == c]
        if not ids:
            missing.append(c)
        elif all(i in denied for i in ids):
            bad.append(c)
    check("permissions", not bad and not missing, ("denied: %s " % bad if bad else "") +
          ("not run: %s" % missing if missing else "") or "all %d ran without a denial" % len(cmds))
    check("repo read", *_all_denied([i for i, n, inp in uses if n == "Read" and repo in json.dumps(inp)], denied, "Read"))
    ok, detail = _repo_cat(cfg, os.path.join(out_dir, "paid-permissions.jsonl"), uses, denied, repo)
    check("repo cat", ok, detail)
    rule_uses = [i for i, n, inp in uses if n == "Read" and "rules.md" in str(inp.get("file_path", ""))]
    check("plugin file read", bool(rule_uses) and not all(i in denied for i in rule_uses),
          "not tried" if not rule_uses else ("denied" if all(i in denied for i in rule_uses) else "allowed"))

    def room(name):
        if spent0 + spent + pc["call_cap_usd"] > pc["total_cap_usd"]:
            check(name, "skip", "probe cap reached")
            return False
        return True

    for arm in ("plain", "docs"):
        if arm in wss and room("plugin denied " + arm):
            spent += _plugin_denied(cfg, out_dir, wss[arm], arm, check, extra)
    for arm in cfg.get("screen_arms", []):
        if room("no command " + arm):
            spent += _read_arm(cfg, out_dir, wss[arm], arm, check)
    if room("resume"):
        st, evs, init, hooks, res2 = _call(cfg, out_dir, "paid-resume", "plugin", "Reply with the single word OK.",
                                           wss["plugin"], sid, False, pc["call_cap_usd"], pc["model"], extra)
        ok, detail = _resume_ok(res, res2, sid)
        check("resume", ok, detail)
        if res2:  # a resumed call reports the session's running total
            spent += max(0.0, float(res2.get("total_cost_usd") or 0) - cost)
    if room("subagent usage"):
        spent += _subagent(cfg, out_dir, wss.get("plain") or wss["plugin"], check)
    return spent


def _sent(evs):
    """The command of each Bash call as the model sent it. The CLI (2.1.299) strips a leading
    `cd <working folder> &&` from the input it runs and shows; wire_tool_inputs keeps the original."""
    out = {}
    for e in evs:
        wire = e.get("wire_tool_inputs")
        for tid, inp in (wire.items() if isinstance(wire, dict) else ()):
            if isinstance(inp, dict) and "command" in inp:
                out[tid] = str(inp["command"])
    return out


def _all_denied(ids, denied, what):
    """(ok, detail) for calls that must be tried and denied: '<what> denied', 'not tried' or 'ran'."""
    if not ids:
        return False, what + " not tried"
    if all(i in denied for i in ids):
        return True, what + " denied"
    return False, what + " ran"


def _repo_cat(cfg, sp, uses, denied, repo):
    """Bash cat of the repository: denied, or it ran and the runner's forbidden-path grep over the
    call's stream file (measures.forbidden_hits, as runone._finish uses it) flags it."""
    cats = [i for i, n, inp in uses if n == "Bash" and repo in str(inp.get("command", ""))]
    if not cats:
        return False, "not tried"
    if all(i in denied for i in cats):
        return True, "Bash cat denied"
    hits = measures.forbidden_hits([sp], common.forbidden_patterns(cfg))
    return bool(hits), "Bash cat ran (Read deny rules do not cover Bash); the forbidden-path flag %s" % (
        "catches it (%s)" % hits[0] if hits else "misses it")


def _plugin_denied(cfg, out_dir, ws, arm, check, extra):
    """Arms plain and docs must not Read the plugin folder; whether Bash cat of it runs is reported."""
    rules = _rules(cfg)
    prompt = ("This is a permissions check. Use the Read tool on %s. Then use the Bash tool to run: cat %s\n"
              "Do not stop when a call is denied. At the end reply with the single word DONE." % (rules, rules))
    st, evs, init, hooks, res = _call(cfg, out_dir, "paid-plugin-" + arm, arm, prompt, ws, str(uuid.uuid4()), True,
                                      cfg["probe"]["call_cap_usd"], cfg["probe"]["model"], extra)
    denied = {d.get("tool_use_id") for d in (res or {}).get("permission_denials") or []}
    tries = [(i, n) for i, n, inp in stream.tool_uses(evs) if "rules.md" in json.dumps(inp)]
    ok, detail = _all_denied([i for i, n in tries if n == "Read"], denied, "Read")
    check("plugin denied " + arm, ok, detail + "; " + _all_denied([i for i, n in tries if n == "Bash"], denied, "Bash cat")[1])
    return float((res or {}).get("total_cost_usd") or 0)


def _read_arm(cfg, out_dir, ws, arm, check):
    """A read arm cannot run a command, also not through a subagent. The call has the arm's own
    command line (its --tools allow list) and asks for the Bash tool, the Monitor tool and a
    subagent, then a Glob of the workspace. It passes when init lists no command tool
    (command_tools), no hook and no subagent message shows up, and PROBE_TOKEN's value is in neither
    the stream nor the session's transcripts, subagents' included. The detail prints the hooks, what
    the Glob returned and the init tool list."""
    pc, sid, token = cfg["probe"], str(uuid.uuid4()), secrets.token_hex(8)
    name = "paid-read-" + arm
    st, evs, init, hooks, res = _call(cfg, out_dir, name, arm, READ_PROMPT, ws, sid, True, pc["call_cap_usd"],
                                      pc["model"], env={TOKEN_VAR: token})
    tools = stream._names(init or {}, "tools")
    listed = command_tools(tools)
    sids = {sid, (res or {}).get("session_id") or sid}
    files = [os.path.join(out_dir, name + ".jsonl")]
    files += [f for s in sorted(sids) for f in spend.session_files(common.projects_dir(cfg), s)]
    ran = bool(measures.forbidden_hits(files, [token], boundary=False))
    sub = sum(1 for e in evs if e.get("parent_tool_use_id"))
    answered = res is not None and not res.get("is_error")
    detail = ("command tools listed: %s; tools tried %s; subagent messages %d; hooks %s; a command %s; glob %s; "
              "init tools %s") % (",".join(listed) or "none", sorted({n for _, n, _ in stream.tool_uses(evs)}), sub,
                                  ",".join(hooks) or "none", "ran" if ran else "did not run", _glob(evs),
                                  ",".join(tools) or "none")
    check("no command " + arm, answered and init is not None and not listed and not hooks and not ran and not sub,
          detail if answered else "no answer: " + str((res or {}).get("result", "no result"))[:200])
    return float((res or {}).get("total_cost_usd") or 0)


def _glob(evs):
    """What the main session's first Glob call returned: its line count and first line, 'no
    result' or 'not tried'."""
    ids = [i for e in evs if not e.get("parent_tool_use_id") for i, n, _ in stream.tool_uses([e]) if n == "Glob"]
    for e in evs:
        for b in stream.message_of(e).get("content") or []:
            if ids and isinstance(b, dict) and b.get("type") == "tool_result" and b.get("tool_use_id") == ids[0]:
                lines = measures._result_text(b.get("content")).splitlines()
                return "%s%d lines, first %s" % ("error, " if b.get("is_error") else "", len(lines),
                                                 (lines or ["-"])[0][:120])
    return "no result" if ids else "not tried"


def _resume_ok(res, res2, sid):
    """A resumed call keeps the session id and reports running totals: its total cost and every
    modelUsage field are at least the first call's."""
    if not res2 or res2.get("is_error"):
        return False, "no result" if not res2 else str(res2.get("result"))[:200]
    if res2.get("session_id") != sid:
        return False, "session id changed to %s" % res2.get("session_id")
    first, second = spend.totals_of_result(res), spend.totals_of_result(res2)
    short = ["%s %s" % (m, k) for m, mu in first["usage"].items() for k, v in mu.items()
             if second["usage"].get(m, {}).get(k, 0) + 1e-12 < v]
    if second["cost"] + 1e-12 < first["cost"] or short:
        return False, "not running totals: cost $%.6f then $%.6f; lower: %s" % (first["cost"], second["cost"], short)
    return True, "session id kept; running totals: cost $%.6f then $%.6f, modelUsage covers the first call" % (
        first["cost"], second["cost"])


def _subagent(cfg, out_dir, ws, check):
    """Starts one subagent on the probe model and reports where its messages and usage show up:
    in the stream (parent_tool_use_id set) and in the subagent transcript."""
    pc, sid = cfg["probe"], str(uuid.uuid4())
    agents = {HELPER: {"description": "Runs one shell command for a probe and returns its output.",
                       "prompt": "Run the Bash command you are given once and reply with its output only.",
                       "model": pc["model"], "tools": ["Bash"]}}
    prompt = ("Use the Task tool exactly once, with subagent_type %s and the prompt: Run the Bash command ls. "
              "Then reply with the single word DONE." % HELPER)
    extra = ["--tools", "Bash,Read,Task", "--agents", json.dumps(agents)]
    st, evs, init, hooks, res = _call(cfg, out_dir, "paid-subagent", "plain", prompt, ws, sid, True, pc["call_cap_usd"],
                                      pc["model"], extra, env={"CLAUDE_CODE_SUBAGENT_MODEL": pc["model"]})
    tasks = [i for i, n, inp in stream.tool_uses(evs) if n in ("Task", "Agent")]
    sub = [e for e in evs if tasks and e.get("parent_tool_use_id") in tasks]
    sub_usage = [e for e in sub if e.get("type") == "assistant" and stream.message_of(e).get("usage")]
    tr = [f for f in spend.session_files(common.projects_dir(cfg), sid) if os.sep + "subagents" + os.sep in f]
    tr_usage = [e for f in tr for e in stream.events(f) if stream.message_of(e).get("usage")]
    models = sorted(((res or {}).get("modelUsage") or {}).keys())
    off = [m for m in models if pc["model"] not in m]
    detail = "Task calls %d; stream: %d subagent messages, %d with usage; transcripts: %d files, %d messages " \
             "with usage; models %s" % (len(tasks), len(sub), len(sub_usage), len(tr), len(tr_usage), models)
    check("subagent usage", bool(tasks) and bool(sub_usage or tr_usage) and not off, detail)
    return float((res or {}).get("total_cost_usd") or 0)


def _done(out_dir, checks, spent, log):
    common.write_json(os.path.join(out_dir, "probe.json"), {"checks": checks, "spent_usd": round(spent, 6)})
    log("probe spend: $%.4f; raw files in %s" % (spent, out_dir))
    return 0 if all(c["result"] == "pass" for c in checks) else 1
