"""screen, pilot and run: a set of runs in a fixed order, resumable, within the set's cap."""
import glob
import os

import common
import measures
import report
import runone


def preflight(cfg, arms=None):
    """Problems that stop a set of these arms (default: the tier's three) before it spends anything."""
    arms = cfg["arms"] if arms is None else arms
    svc, probs = cfg["paths"]["service"], []
    for f in ("prepare.sh", "hidden.sh"):
        if not os.access(os.path.join(svc, f), os.X_OK):
            probs.append("missing or not executable: %s" % os.path.join(svc, f))
    for arm in arms:
        v = common.ARM_VERSION[arm]
        for bug in cfg["bugs"]:
            for p in (common.variant_dir(cfg, v, bug), os.path.join(svc, "logs", "%s-%s.txt" % (v, bug))):
                if not os.path.exists(p):
                    probs.append("missing: " + p)
    if "plugin" in arms and not os.path.isfile(os.path.join(cfg["paths"]["plugin"], ".claude-plugin", "plugin.json")):
        probs.append("missing plugin: %s (run arms/build.sh)" % cfg["paths"]["plugin"])
    rc, out = common.sh([cfg["claude"], "--version"], env=common.child_env(cfg), timeout=60)
    if rc != 0:
        probs.append("claude --version failed: " + out.strip()[:200])
    for p in common.leftover_project_folders(cfg):
        probs.append("transcripts of an earlier workspace, readable by agents: %s (move it out)" % p)
    probs += ["forbidden path in %s" % h for h in input_hits(cfg, arms)]
    return sorted(set(probs))


def input_hits(cfg, arms=None):
    """Paths of the repository, bench/agents or the transcripts in what agents see: the CI logs, the
    prompts, the docs arm's files and the variants prepare.sh copies (each run also checks its
    prepared workspace)."""
    svc, files = cfg["paths"]["service"], []
    files += glob.glob(os.path.join(svc, "logs", "*.txt")) + glob.glob(os.path.join(cfg["paths"]["prompts"], "*"))
    files += measures.tree_files(os.path.join(cfg["paths"]["arms"], "docs"))
    for v in sorted({common.ARM_VERSION[a] for a in (cfg["arms"] if arms is None else arms)}):
        for bug in cfg["bugs"]:
            files += measures.tree_files(common.variant_dir(cfg, v, bug))
    pats = common.forbidden_patterns(cfg)
    return [os.path.relpath(f, cfg["bench"]) + ": " + h.split(": ", 1)[1]
            for f in files for h in measures.forbidden_hits([f], pats)]


def plan(cfg, n_runs, arms=None):
    arms = cfg["arms"] if arms is None else arms
    return [(n, bug, arm) for n in range(1, n_runs + 1) for bug in cfg["bugs"] for arm in arms]


def run_set(cfg, set_name, n_runs, log=print, user=None):
    """Runs every (n, bug, arm) not yet done. Stops on an error or a refused run, and before a run
    that could pass the set cap; the spend counts retired attempts. Returns 0 when every planned run
    is done."""
    arms = common.set_arms(cfg, set_name)
    probs = preflight(cfg, arms)
    if probs:
        for p in probs:
            log("preflight: " + p)
        return 2
    run_cap = common.run_cap(cfg, set_name)
    cap = cfg["set_cap_usd"].get(set_name, run_cap * len(plan(cfg, n_runs, arms)))
    status = 0
    for n, bug, arm in plan(cfg, n_runs, arms):
        rid = runone.run_id(set_name, bug, arm, n)
        prev = common.read_json(os.path.join(cfg["paths"]["runs"], rid, "run.json"), {})
        if prev.get("status") == "done":
            log("skip %s (done)" % rid)
            continue
        spent = sum(r.get("cost_usd") or 0 for r in report.load(cfg) + report.load_retired(cfg)
                    if r.get("set") == set_name)
        if spent + run_cap > cap:
            log("stop: spent $%.2f, one more run could pass the $%.2f cap of set %s" % (spent, cap, set_name))
            status = 3
            break
        log("run %s" % rid)
        rec = runone.do_run(cfg, set_name, arm, bug, n, log=log, user=user)
        log("  %s fixed=%s rounds=%s cost=$%.2f isolation=%s%s" % (
            rec["status"], rec.get("fixed"), rec.get("rounds"), rec.get("cost_usd") or 0, rec.get("isolation"),
            (" error=" + rec["error"]) if rec.get("error") else ""))
        if rec["status"] != "done":
            log("stop: run %s is %s" % (rid, rec["status"]))
            status = 1
            break
    for p in report.write(cfg):
        log("wrote " + p)
    return status
