#!/usr/bin/env python3
"""leasekv agent benchmark. python3 bench.py <tier1|tier2> <step>, steps: calibrate, probe, screen,
pilot, run, report.

  calibrate  runs <tier>/service/calibrate.sh (no model calls)
  probe      isolation and permission checks; the paid part uses the probe model only
  screen     tier 2's read-only screen: N runs per candidate and read arm, one round (set "screen")
  pilot      one run per bug and arm (set "pilot")
  run        the full set (set "full"), resumable: finished runs are skipped
  report     <tier>/results/<set>.csv and <tier>/results/<set>.md from <tier>/runs/
"""
import argparse
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, "runner"))

import common  # noqa: E402
import probe  # noqa: E402
import report  # noqa: E402
import sets  # noqa: E402

TIERS = ("tier1", "tier2")


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("tier", choices=TIERS)
    ap.add_argument("--config", help="a config file other than <tier>/config.json")
    sub = ap.add_subparsers(dest="step", required=True)
    sub.add_parser("calibrate")
    sub.add_parser("probe")
    s = sub.add_parser("screen")
    s.add_argument("--runs", type=int, default=2)
    sub.add_parser("pilot")
    r = sub.add_parser("run")
    r.add_argument("--runs", type=int, default=5)
    sub.add_parser("report")
    a = ap.parse_args(argv)
    cfg = common.load_config(a.config or os.path.join(HERE, a.tier, "config.json"))
    log = lambda s: print(s, flush=True)  # noqa: E731
    if a.step == "calibrate":
        script = os.path.join(cfg["paths"]["service"], "calibrate.sh")
        if not os.access(script, os.X_OK):
            log("missing or not executable: %s" % script)
            return 2
        rc, out = common.sh([script], cwd=cfg["paths"]["service"], env=common.child_env(cfg))
        log(out.rstrip())
        log("calibrate: %s" % ("every row holds" if rc == 0 else "exit %d" % rc))
        return rc
    if a.step == "probe":
        return probe.run(cfg, log)
    if a.step == "screen":
        if not cfg.get("screen_arms"):
            log("screen: tier %s has no screen (its config.json sets no screen_arms); run pilot and run" % cfg["tier"])
            return 2
        return sets.run_set(cfg, "screen", a.runs, log)
    if a.step in ("pilot", "run") and len(cfg["bugs"]) != 3:
        log("%s: config.json lists %d bugs (%s); set bugs to the three the screen kept" % (
            a.step, len(cfg["bugs"]), ", ".join(cfg["bugs"])))
        return 2
    if a.step == "pilot":
        return sets.run_set(cfg, "pilot", 1, log)
    if a.step == "run":
        return sets.run_set(cfg, "full", a.runs, log)
    for p in report.write(cfg):
        log("wrote " + p)
    return 0


if __name__ == "__main__":
    sys.exit(main())
