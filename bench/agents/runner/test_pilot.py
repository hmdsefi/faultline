"""A whole screen (16 runs), a whole pilot (9 runs) and refused runs against a fake claude and a stub
service."""
import contextlib
import csv
import io
import json
import os
import re
import shutil
import tempfile
import time
import unittest
from unittest import mock

import bench_path  # noqa: F401  (puts bench/agents on sys.path for bench.py)
import common
import report
import runone
import sets
import stream

HERE = os.path.dirname(os.path.abspath(__file__))
AGENTS = os.path.dirname(HERE)

KV = "package leasekv\n\n// Fixed is false while the bug is in.\nvar Fixed = false\n"
KV_TEST = ('package leasekv\n\nimport "testing"\n\nfunc TestLeaseKV(t *testing.T) {\n\tif !check(1) {\n'
           '\t\tt.Fatal("bad")\n\t}\n}\n\n// check is the visible check.\nfunc check(n int) bool { return n > 0 }\n')
LOGS = {"plain-trunc": "FAIL TestLeaseKV", "plain-sync": "FAIL TestLeaseKV FAKE:SLOW", "plain-fwd": "FAIL TestLeaseKV",
        "faultline-trunc": "FAIL seed 0x1 FAKE:FORBIDDEN", "faultline-sync": "FAIL seed 0x2",
        "faultline-fwd": "FAIL seed 0x3 FAKE:WEAKEN", "plain-epoch": "FAIL TestLeaseKV epoch",
        "faultline-epoch": "FAIL seed 0x4 FAKE:SLOW"}
THREE = ["trunc", "sync", "fwd"]  # the candidates a screen keeps, for the pilot
READ_TOOLS = "Read,Edit,Write,Glob,Grep"


def put(path, text):
    with open(path, "w") as f:
        f.write(text)


def make_bench(tmp):
    """A tree like bench/agents/tier2: a stub service with eight variants (four candidates) under
    service/testdata, a fake plugin and a config for them. The real arms and prompts."""
    bench, outside = os.path.join(tmp, "agents"), os.path.join(tmp, "outside")
    svc = os.path.join(bench, "tier2", "service")
    shutil.copytree(os.path.join(HERE, "testdata", "service"), svc)
    os.makedirs(os.path.join(svc, "logs"))
    for v in ("plain", "faultline"):
        for b in ("trunc", "sync", "fwd", "epoch"):
            d = os.path.join(svc, "testdata", v, b)
            os.makedirs(d)
            put(os.path.join(d, "kv.go"), KV)
            put(os.path.join(d, "kv_test.go"), KV_TEST)
            put(os.path.join(svc, "logs", "%s-%s.txt" % (v, b)), LOGS["%s-%s" % (v, b)] + "\n")
    plugin = os.path.join(outside, "plugin")
    for p in (".claude-plugin", "skills/faultline", "skills/pin", "skills/replay", "agents"):
        os.makedirs(os.path.join(plugin, p))
    put(os.path.join(plugin, ".claude-plugin", "plugin.json"), '{"name": "faultline"}')
    for s in ("faultline", "pin", "replay"):
        put(os.path.join(plugin, "skills", s, "SKILL.md"), "---\nname: %s\n---\n" % s)
    put(os.path.join(plugin, "agents", "triage.md"), "---\nname: triage\n---\n")
    with open(os.path.join(AGENTS, "tier2", "config.json")) as f:
        cfg = json.load(f)
    cfg["claude"] = os.path.join(HERE, "testdata", "fake_claude.py")
    cfg["paths"].update({
        "repo": os.path.join(tmp, "repo"), "service": "service", "runs": "runs", "results": "results",
        "arms": os.path.join(AGENTS, "arms"), "prompts": os.path.join(AGENTS, "prompts"), "plugin": plugin,
        "workspace_root": os.path.join(outside, "w"), "claude_config_dir": os.path.join(outside, "claude")})
    cfg["env_keep"] = cfg["env_keep"] + ["FAKE_LOG", "FAKE_REPO", "FAKE_MCP", "FAKE_MODE", "FAKE_FORK", "FAKE_PIDS",
                                         "FAKE_MODEL", "FAKE_HIDDEN_SLEEP", "GOCACHE"]
    path = os.path.join(bench, "tier2", "config.json")
    put(path, json.dumps(cfg, indent=1))
    return common.load_config(path)


class BenchCase(unittest.TestCase):
    """A fresh tree, fake and environment per test."""

    def setUp(self):
        self.tmp = os.path.realpath(tempfile.mkdtemp(prefix="bench-test-"))
        self.cfg = make_bench(self.tmp)
        self.env = dict(os.environ)
        os.environ["FAKE_LOG"] = os.path.join(self.tmp, "argv.log")
        os.environ["FAKE_REPO"] = self.cfg["paths"]["repo"]
        for k in ("FAKE_MCP", "FAKE_MODE", "FAKE_FORK", "FAKE_PIDS", "FAKE_MODEL", "FAKE_HIDDEN_SLEEP"):
            os.environ.pop(k, None)

    def tearDown(self):
        os.environ.clear()
        os.environ.update(self.env)
        common.rmtree(self.tmp)


class PilotTest(BenchCase):
    def test_pilot(self):
        self.cfg["bugs"] = THREE
        lines = []
        rc = sets.run_set(self.cfg, "pilot", 1, log=lines.append, user=set())
        self.assertEqual(rc, 0, "\n".join(lines))
        with open(os.path.join(self.cfg["paths"]["results"], "pilot.csv")) as f:
            rows = list(csv.DictReader(f))
        self.assertEqual(len(rows), 9)
        self.assertEqual(list(rows[0].keys()), report.COLUMNS)
        by = {(r["bug"], r["arm"]): r for r in rows}
        for r in rows:
            self.assertEqual(r["status"], "done", r)
            self.assertEqual(r["fixed"], "yes")
            self.assertEqual(r["isolation"], "pass")
            self.assertEqual(r["denials"], str(int(r["rounds"])))
            self.assertEqual(r["regression_test"], "protects")
            self.assertEqual(r["new_tests"], "TestLeaseKVRegression")
            self.assertGreater(int(r["weighted_tokens"]), 0)
            # tokens come from modelUsage, which holds 20 output tokens a call that no transcript has
            self.assertEqual(int(r["weighted_tokens"]), int(r["weighted_tokens_transcripts"]) + 100 * int(r["rounds"]))
        slow = by[("sync", "plain")]
        self.assertEqual((slow["fixed_round"], slow["rounds"], slow["wrong_fixes"]), ("2", "2", "1"))
        # the resumed round's result says $0.50, a running total: round 2 cost $0.25
        self.assertEqual((slow["cost_usd"], slow["round_cost_usd"]), ("0.5000", "0.25; 0.25"))
        self.assertEqual((slow["round_end"], slow["hidden_exit"]), ("success; success", "1; 0"))
        self.assertEqual(slow["go_test_calls"], "6")
        # round 1's first go test after the Edit fails on the wrong change; round 2's two pass
        self.assertEqual((slow["test_runs_after_edit"], slow["failing_test_runs_after_edit"]), ("4", "1"))
        self.assertEqual(slow["turns"], "8")
        fast = by[("trunc", "plain")]
        self.assertEqual((fast["fixed_round"], fast["rounds"], fast["wrong_fixes"]), ("1", "1", "0"))
        self.assertEqual((fast["cost_usd"], fast["go_test_calls"], fast["output_tokens"]), ("0.2500", "3", "270"))
        self.assertEqual((fast["weighted_tokens"], fast["processed_tokens"], fast["weighted_tokens_transcripts"]),
                         ("2525", "5820", "2425"))
        self.assertEqual((fast["test_runs_after_edit"], fast["failing_test_runs_after_edit"]), ("2", "0"))
        # the fake runs no undo check, so every finished run records 0, not a blank
        self.assertEqual({r["undo_check_runs"] for r in by.values()}, {"0"})
        self.assertEqual(fast["budget_out"], "no")
        self.assertEqual(fast["faultline_source"], "-")
        self.assertEqual({by[("trunc", a)]["faultline_source"] for a in ("docs", "plugin")}, {"module-cache"})
        # the docs arm's workspace holds instructions.md as CLAUDE.md and docs/ai-agents.md; the others neither
        docs = os.path.join(AGENTS, "arms", "docs")
        for bug in THREE:
            for arm in ("plain", "docs", "plugin"):
                orig = os.path.join(self.cfg["paths"]["runs"], "pilot-%s-%s-1" % (bug, arm), "original")
                if arm == "docs":
                    self.assertEqual(common.read_text(os.path.join(orig, "CLAUDE.md")),
                                     common.read_text(os.path.join(docs, "instructions.md")))
                    self.assertEqual(common.read_text(os.path.join(orig, "docs", "ai-agents.md")),
                                     common.read_text(os.path.join(docs, "docs", "ai-agents.md")))
                else:
                    self.assertEqual(sorted(os.listdir(orig)), ["go.mod", "kv.go", "kv_test.go"], (bug, arm))
        for arm in ("docs", "plugin"):
            self.assertEqual(by[("trunc", arm)]["forbidden_path"], "yes")
            self.assertEqual(by[("fwd", arm)]["visible_checks_changed"], "check")
            self.assertEqual(by[("sync", arm)]["visible_checks_changed"], "")
        self.assertEqual(by[("trunc", "plain")]["forbidden_path"], "no")
        # raw files of the slow run
        rd = os.path.join(self.cfg["paths"]["runs"], "pilot-sync-plain-1")
        for f in ("stream-r1.jsonl", "stream-r2.jsonl", "hidden-r1.log", "hidden-r2.log", "final.diff",
                  "regression-5.log", "workspace/kv.go", "original/kv.go"):
            self.assertTrue(os.path.exists(os.path.join(rd, f)), f)
        self.assertIn("Fixed = true", common.read_text(os.path.join(rd, "final.diff")))
        self.assertIn("FAIL", common.read_text(os.path.join(rd, "hidden-r1.log")))
        rec = common.read_json(os.path.join(rd, "run.json"))
        sid = rec["session_ids"][0]
        self.assertTrue(os.path.exists(os.path.join(rd, "transcripts", sid + ".jsonl")))
        self.assertEqual(len(os.listdir(os.path.join(rd, "transcripts", sid, "subagents"))), 2)
        mu = rec["model_usage"]["claude-opus-5-5"]
        self.assertEqual((mu["costUSD"], mu["outputTokens"], rec["api_ms"], rec["turns"]), (0.5, 500, 2000, 8))
        # hidden.sh wrote into the folder it got (the stub breaks the contract): never the workspace
        for r in rows:
            self.assertFalse(os.path.exists(os.path.join(self.cfg["paths"]["runs"], r["run_id"], "workspace",
                                                         "hidden_leak.txt")), r["run_id"])
        # each run's transcript folder moved into its run folder; none is left for later runs
        self.assertEqual(os.listdir(common.projects_dir(self.cfg)), [])
        self.assertEqual(len(os.listdir(os.path.join(rd, "claude-project"))), 1)
        # every claude call: prompt right after -p, resume in round 2, plugin only for arm plugin
        calls = [json.loads(l) for l in common.read_text(os.environ["FAKE_LOG"]).splitlines()]
        self.assertEqual(len(calls), 10)
        self.assertTrue(all(c[0] == "-p" for c in calls))
        self.assertEqual(sum("--resume" in c for c in calls), 1)
        self.assertEqual(sum("--plugin-dir" in c for c in calls), 3)
        first = [c for c in calls if "--session-id" in c and "FAKE:SLOW" in c[1]][0]
        self.assertIn("FAIL TestLeaseKV FAKE:SLOW", first[1])
        self.assertIn("Find the bug in the service and fix it.", first[1])
        # workspaces are gone and were outside bench/agents
        self.assertEqual(os.listdir(self.cfg["paths"]["workspace_root"]), [])
        # the tier's three arms keep tier 1's flags: only the web tools are disallowed
        self.assertEqual({c[c.index("--disallowedTools") + 1] for c in calls}, {"WebFetch,WebSearch"})
        page = common.read_text(os.path.join(self.cfg["paths"]["results"], "pilot.md"))
        self.assertTrue(page.startswith("# leasekv results, tier 2: pilot\n"), page[:80])
        self.assertNotIn("## Screen", page)
        self.assertIn("## Summary per bug and arm", page)
        self.assertIn("| sync | plain | 1 of 1 | $0.50 ($0.50 to $0.50)", page)
        self.assertIn("| 2 (2 to 2) | 4 (4 to 4) | 1 (1 to 1) |", page)
        self.assertIn("more than 2 %:\n\n- pilot-fwd-plain-1: modelUsage 2525, transcripts 2425 (4.1 %)\n", page)
        self.assertIn("Spent: $2.50.", page)
        self.assertIn("Spent on retired attempts (`runs/_retired/`, 0): $0.00.", page)
        self.assertIn("| pilot-fwd-plugin-1 | yes | 1 |", page)
        self.assertIn("## Where faultline did not help", page)
        # resumable: a second pilot skips every run and spends nothing
        lines = []
        self.assertEqual(sets.run_set(self.cfg, "pilot", 1, log=lines.append, user=set()), 0)
        self.assertEqual(sum(l.startswith("skip ") for l in lines), 9)
        self.assertEqual(len(common.read_text(os.environ["FAKE_LOG"]).splitlines()), 10)

    def test_refused_run(self):
        os.environ["FAKE_MCP"] = "1"
        start = time.time()
        rec = runone.do_run(self.cfg, "pilot", "plain", "trunc", 1, user=set())
        self.assertLess(time.time() - start, 30)
        self.assertEqual(rec["status"], "refused")
        self.assertIn("mcp server stray", rec["isolation"])
        # the key marks the record as written after 2026-10-17, so report.upgrade leaves it alone
        self.assertEqual((rec["weighted_tokens_transcripts"], rec["test_runs_after_edit"]), (None, None))
        self.assertIs(report.upgrade(rec, "/nonexistent"), rec)
        lines = []
        self.assertEqual(sets.run_set(self.cfg, "pilot", 1, log=lines.append, user=set()), 1)
        self.assertTrue(any("stop: run pilot-trunc-plain-1 is refused" in l for l in lines), lines)

    def test_an_arm_run_without_bash_is_refused(self):
        i = self.cfg["claude_flags"].index("--disallowedTools") + 1
        self.cfg["claude_flags"][i] += ",Bash"
        rec = runone.do_run(self.cfg, "pilot", "plain", "trunc", 1, user=set())
        self.assertEqual(rec["status"], "refused")
        self.assertEqual(rec["isolation"], "fail: arm plain lacks Bash")

class ScreenTest(BenchCase):
    """The read-only screen: two read arms, one round, no command tools, the hidden check decides."""

    def test_screen(self):
        lines = []
        rc = sets.run_set(self.cfg, "screen", 2, log=lines.append, user=set())
        self.assertEqual(rc, 0, "\n".join(lines))
        with open(os.path.join(self.cfg["paths"]["results"], "screen.csv")) as f:
            rows = list(csv.DictReader(f))
        self.assertEqual(len(rows), 16)
        self.assertEqual([(r["bug"], r["arm"], r["run"]) for r in rows[:4]], [
            ("epoch", "read-plain", "1"), ("epoch", "read-plain", "2"), ("epoch", "read-faultline", "1"),
            ("epoch", "read-faultline", "2")])
        by = {(r["bug"], r["arm"], r["run"]): r for r in rows}
        self.assertEqual(sorted({k[0] for k in by}), ["epoch", "fwd", "sync", "trunc"])
        self.assertEqual({k[1] for k in by}, {"read-plain", "read-faultline"})
        for r in rows:
            self.assertEqual((r["status"], r["isolation"], r["rounds"], r["denials"]), ("done", "pass", "1", "0"), r)
            self.assertEqual((r["go_test_calls"], r["test_runs_after_edit"], r["forbidden_path"]), ("0", "0", "no"))
        # FAKE:SLOW fixes only in a second round, which neither read arm ever gets
        for slow in (by[("sync", "read-plain", "1")], by[("epoch", "read-faultline", "2")]):
            self.assertEqual((slow["fixed"], slow["fixed_round"], slow["wrong_fixes"]), ("no", "", "1"))
            self.assertEqual((slow["round_end"], slow["hidden_exit"], slow["cost_usd"]), ("success", "1", "0.2500"))
        self.assertEqual(by[("sync", "read-faultline", "2")]["fixed"], "yes")
        self.assertEqual(by[("trunc", "read-plain", "1")]["faultline_source"], "-")
        self.assertEqual(by[("trunc", "read-faultline", "1")]["faultline_source"], "module-cache")
        # every call: one round, the arm's own prompt and CI log, no plugin, no command tools
        calls = [json.loads(l) for l in common.read_text(os.environ["FAKE_LOG"]).splitlines()]
        self.assertEqual(len(calls), 16)
        self.assertFalse(any("--resume" in c or "--plugin-dir" in c for c in calls))
        self.assertEqual([c.count("--disallowedTools") for c in calls], [1] * 16)
        self.assertEqual({c[c.index("--disallowedTools") + 1] for c in calls}, {"WebFetch,WebSearch"})
        self.assertEqual([c.count("--tools") for c in calls], [1] * 16)
        self.assertEqual({c[c.index("--tools") + 1] for c in calls}, {READ_TOOLS})
        # the screen's run cap is $3, the set cap $30
        self.assertEqual({c[c.index("--max-budget-usd") + 1] for c in calls}, {"3.00"})
        # a read arm lists files with Glob and searches with Grep (the settings allow both: denials 0
        # above); a Read of the folder fails with EISDIR, as on the real CLI
        sp = os.path.join(self.cfg["paths"]["runs"], "screen-trunc-read-plain-1", "stream-r1.jsonl")
        evs = stream.events(sp)
        init, ws = stream.init_of(evs), stream.init_of(evs)["cwd"]
        self.assertEqual(init["tools"], ["Edit", "Glob", "Grep", "Read", "Write"])
        self.assertEqual([n for _, n, _ in stream.tool_uses(evs)], ["Glob", "Grep", "Read", "Read", "Edit"])
        results = [b for e in evs if e.get("type") == "user" for b in stream.message_of(e)["content"]]
        self.assertEqual([(b["content"], b["is_error"]) for b in results[:3]], [
            (ws + "/kv.go\n" + ws + "/kv_test.go", False), (ws + "/kv.go", False),
            ("Error: EISDIR: illegal operation on a directory, read '%s'" % ws, True)])
        rule = common.plugin_read_rule(self.cfg)
        self.assertTrue(all(rule in json.loads(c[c.index("--settings") + 1])["permissions"]["deny"] for c in calls))
        want = {runone._prompt(self.cfg, v, b)[0] for v in ("plain", "faultline") for b in self.cfg["bugs"]}
        self.assertEqual({c[1] for c in calls}, want)
        # the variant only: no CLAUDE.md and no docs folder in either version
        for b in self.cfg["bugs"]:
            for arm in common.READ_ARMS:
                orig = os.path.join(self.cfg["paths"]["runs"], "screen-%s-%s-1" % (b, arm), "original")
                self.assertEqual(sorted(os.listdir(orig)), ["go.mod", "kv.go", "kv_test.go"], (b, arm))
        page = common.read_text(os.path.join(self.cfg["paths"]["results"], "screen.md"))
        self.assertTrue(page.startswith("# leasekv results, tier 2: screen\n"), page[:80])
        self.assertIn("| Candidate | Version | Fixed | Cost | Touched the defect's function |\n|---|---|---|---|---|\n"
                      "| trunc | plain | 2 of 2 | $0.50 |  |\n| trunc | faultline | 2 of 2 | $0.50 |  |\n"
                      "| sync | plain | 0 of 2 | $0.50 |  |\n", page)
        self.assertIn("| epoch | faultline | 0 of 2 | $0.50 |  |\n\n## Summary per bug and arm", page)
        self.assertIn("| sync | read-plain | 0 of 2 | $0.25 ($0.25 to $0.25)", page)
        self.assertIn("Spent: $4.00.", page)
        # after the screen the config lists the three it kept; the screen page keeps all four
        self.cfg["bugs"] = ["fwd", "trunc", "sync"]
        report.write(self.cfg)
        page = common.read_text(os.path.join(self.cfg["paths"]["results"], "screen.md"))
        self.assertIn("| sync | faultline | 2 of 2 | $0.50 |  |\n| epoch | plain | 2 of 2 | $0.50 |  |\n", page)
        self.assertIn("| epoch | read-faultline | 0 of 2 |", page)
        # resumable
        lines = []
        self.assertEqual(sets.run_set(self.cfg, "screen", 2, log=lines.append, user=set()), 0)
        self.assertEqual(sum(l.startswith("skip ") for l in lines), 12)

    def test_a_screen_run_whose_init_lists_bash_is_refused(self):
        os.environ["FAKE_MODE"] = "tools-ignored"
        lines = []
        self.assertEqual(sets.run_set(self.cfg, "screen", 2, log=lines.append, user=set()), 1)
        self.assertIn("stop: run screen-trunc-read-plain-1 is refused", lines)
        rec = common.read_json(os.path.join(self.cfg["paths"]["runs"], "screen-trunc-read-plain-1", "run.json"))
        self.assertEqual(rec["isolation"], "fail: " + "; ".join("read arm has tool " + t for t in (
            "Bash", "CronCreate", "Monitor", "SendMessage", "Skill", "Task", "ToolSearch", "Workflow")))
        # the screen table counts finished runs only: no row for the refused run's candidate
        page = common.read_text(os.path.join(self.cfg["paths"]["results"], "screen.md"))
        table = page.split("## Screen", 1)[1].split("## Summary", 1)[0]
        self.assertTrue(table.rstrip().endswith("|---|---|---|---|---|"), table)
        self.assertIn("| screen-trunc-read-plain-1 | refused |", page)

    def test_the_screen_has_its_own_set_cap(self):
        self.cfg["set_cap_usd"]["screen"] = 3.2
        lines = []
        self.assertEqual(sets.run_set(self.cfg, "screen", 2, log=lines.append, user=set()), 3)
        self.assertEqual([l for l in lines if l.startswith("run ")], ["run screen-trunc-read-plain-1"])
        self.assertIn("stop: spent $0.25, one more run could pass the $3.20 cap of set screen", lines)

    def test_a_screen_run_uses_the_screen_run_cap(self):
        self.cfg["run_cap_usd_by_set"]["screen"] = 0.25
        rec = runone.do_run(self.cfg, "screen", "read-plain", "trunc", 1, user=set())
        self.assertEqual((rec["status"], rec["cost_usd"], rec["budget_out"]), ("done", 0.25, True))
        rec = runone.do_run(self.cfg, "pilot", "plain", "trunc", 1, user=set())
        self.assertEqual((rec["status"], rec["cost_usd"], rec["budget_out"]), ("done", 0.25, False))
        calls = [json.loads(l) for l in common.read_text(os.environ["FAKE_LOG"]).splitlines()]
        self.assertEqual([c[c.index("--max-budget-usd") + 1] for c in calls], ["0.25", "8.00"])

    def test_pilot_and_run_need_the_three_kept_bugs(self):
        import bench
        path = os.path.join(self.cfg["bench"], "tier2", "config.json")
        out = io.StringIO()
        with mock.patch.object(sets, "run_set", return_value=0) as rs, contextlib.redirect_stdout(out):
            self.assertEqual(bench.main(["tier2", "--config", path, "pilot"]), 2)
            self.assertEqual(bench.main(["tier2", "--config", path, "run"]), 2)
            self.assertEqual(bench.main(["tier2", "--config", path, "screen"]), 0)
            cfg = json.loads(common.read_text(path))
            for bugs in (THREE, THREE[:2]):
                common.write_text(path, json.dumps(dict(cfg, bugs=bugs)))
                bench.main(["tier2", "--config", path, "pilot"])
                bench.main(["tier2", "--config", path, "run", "--runs", "5"])
        self.assertEqual([c.args[1:3] for c in rs.call_args_list], [("screen", 2), ("pilot", 1), ("full", 5)])
        self.assertIn("pilot: config.json lists 4 bugs (trunc, sync, fwd, epoch); set bugs to the three the screen "
                      "kept", out.getvalue())
        self.assertIn("run: config.json lists 2 bugs (trunc, sync); set bugs", out.getvalue())

    def test_bench_screen_runs_two_per_candidate_and_arm(self):
        import bench
        path = os.path.join(self.cfg["bench"], "tier2", "config.json")
        with mock.patch.object(sets, "run_set", return_value=0) as rs, contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(bench.main(["tier2", "--config", path, "screen"]), 0)
            self.assertEqual(bench.main(["tier2", "--config", path, "screen", "--runs", "3"]), 0)
        self.assertEqual([c.args[1:3] for c in rs.call_args_list], [("screen", 2), ("screen", 3)])
        self.assertEqual(len(sets.plan(self.cfg, 2, common.set_arms(self.cfg, "screen"))), 16)



def alive(pid):
    try:
        os.kill(pid, 0)
        return True
    except ProcessLookupError:
        return False
    except PermissionError:
        return True


class LifecycleTest(BenchCase):
    """One run at a time: kills, round limits, budgets, set caps and resuming a set."""

    def argv(self):
        return [json.loads(l) for l in common.read_text(os.environ["FAKE_LOG"]).splitlines()]

    def test_wall_limit_kills_the_process_group_and_every_descendant(self):
        os.environ["FAKE_MODE"] = "hang"
        os.environ["FAKE_PIDS"] = os.path.join(self.tmp, "pids.json")
        self.cfg["run_wall_s"] = 2
        start = time.time()
        rec = runone.do_run(self.cfg, "pilot", "plain", "trunc", 1, user=set())
        self.assertLess(time.time() - start, 25)
        self.assertEqual(rec["status"], "done", rec.get("error"))
        self.assertTrue(rec["timed_out"])
        self.assertEqual((rec["rounds"], rec["round_end"], rec["fixed"]), (1, ["timed-out"], False))
        self.assertEqual(rec["cost_notes"], ["round 1 killed: cost from cost-state"])
        self.assertEqual(rec["cost_usd"], 0.1)
        pids = common.read_json(os.environ["FAKE_PIDS"])
        deadline = time.time() + 5
        while any(alive(p) for p in pids) and time.time() < deadline:
            time.sleep(0.1)
        self.assertEqual([p for p in pids if alive(p)], [], "a grandchild outlived the kill")

    def test_three_rounds_with_what_is_left_of_the_cap(self):
        os.environ["FAKE_MODE"] = "never-fix"
        os.environ["FAKE_FORK"] = "1"
        rec = runone.do_run(self.cfg, "pilot", "plain", "trunc", 1, user=set())
        self.assertEqual(rec["status"], "done", rec.get("error"))
        self.assertEqual((rec["rounds"], rec["wrong_fixes"], rec["fixed"]), (3, 3, False))
        self.assertEqual((rec["hidden_exit"], rec["round_cost_usd"], rec["cost_usd"]), ([1, 1, 1], [0.25] * 3, 0.75))
        self.assertEqual((rec["turns"], rec["api_ms"]), (12, 3000))
        self.assertEqual(rec["model_usage"]["claude-opus-5-5"]["outputTokens"], 750)
        calls = self.argv()
        self.assertEqual([c[c.index("--max-budget-usd") + 1] for c in calls], ["8.00", "7.75", "7.50"])
        # each resumed round continues the session the previous result named
        ids = rec["session_ids"]
        self.assertEqual(len(ids), 3)
        self.assertEqual([c[-2:] for c in calls], [["--session-id", ids[0]], ["--resume", ids[0]], ["--resume", ids[1]]])
        self.assertGreaterEqual(rec["wall_s"], 0.9)
        self.assertAlmostEqual(rec["wall_s"], sum(rec["round_wall_s"]), delta=0.15)

    def test_runner_stops_a_round_that_passes_the_run_cap(self):
        os.environ["FAKE_MODE"] = "spend"
        rec = runone.do_run(self.cfg, "pilot", "plain", "trunc", 1, user=set())
        self.assertEqual(rec["status"], "done", rec.get("error"))
        self.assertTrue(rec["budget_out"])
        self.assertEqual((rec["rounds"], rec["round_end"]), (2, ["success", "budget-killed"]))
        self.assertEqual(rec["cost_notes"], ["round 2 killed: cost estimated from message usage"])
        self.assertEqual([c[c.index("--max-budget-usd") + 1] for c in self.argv()], ["8.00", "7.75"])
        # round 1 cost $0.25 for 2425 weighted tokens; each round-2 message counts once, at its
        # largest copy
        top = {}
        for e in stream.events(os.path.join(self.cfg["paths"]["runs"], "pilot-trunc-plain-1", "stream-r2.jsonl")):
            m = e.get("message") or {}
            if e.get("type") == "assistant":
                top[m["id"]] = max(top.get(m["id"], 0), m["usage"]["output_tokens"])
        want = sum(10 + 125 + 100 + 5 * o for o in top.values()) * 0.25 / 2425
        self.assertGreater(want, 7.75)
        self.assertAlmostEqual(rec["round_cost_usd"][1], want, places=5)
        self.assertAlmostEqual(rec["cost_usd"], 0.25 + want, places=5)

    def test_hooks_refuse_the_run(self):
        for mode, hook in (("hook", "hook SessionStart:startup"), ("late-hook", "hook PostToolUse:Bash")):
            os.environ["FAKE_MODE"] = mode
            rec = runone.do_run(self.cfg, "pilot", "plain", "trunc", 1, user=set())
            self.assertEqual(rec["status"], "refused", mode)
            self.assertIn(hook, rec["isolation"])

    def test_the_hidden_check_timeout_comes_from_the_config(self):
        os.environ["FAKE_HIDDEN_SLEEP"] = "20"
        self.cfg["hidden_timeout_s"] = 1
        start = time.time()
        rec = runone.do_run(self.cfg, "screen", "read-plain", "trunc", 1, user=set())
        self.assertLess(time.time() - start, 15)
        self.assertEqual((rec["status"], rec["error"]), ("error", "round 1: hidden check infrastructure error (exit 124)"))

    def test_another_model_refuses_the_run(self):
        os.environ["FAKE_MODEL"] = "claude-other-1"
        rec = runone.do_run(self.cfg, "pilot", "plain", "trunc", 1, user=set())
        self.assertEqual((rec["status"], rec["model"]), ("refused", "claude-other-1"))
        self.assertIn("model claude-other-1, want claude-opus-5-5", rec["isolation"])

    def test_set_cap_counts_retired_attempts(self):
        self.cfg["set_cap_usd"]["pilot"] = 8.6
        old = os.path.join(self.cfg["paths"]["runs"], "_retired", "pilot-fwd-plain-1-20261016T000000")
        os.makedirs(old)
        common.write_json(os.path.join(old, "run.json"), {"set": "pilot", "status": "error", "cost_usd": 0.5})
        lines = []
        self.assertEqual(sets.run_set(self.cfg, "pilot", 1, log=lines.append, user=set()), 3)
        self.assertEqual([l for l in lines if l.startswith("run ")], ["run pilot-trunc-plain-1"])
        self.assertIn("stop: spent $0.75, one more run could pass the $8.60 cap of set pilot", lines)
        page = common.read_text(os.path.join(self.cfg["paths"]["results"], "pilot.md"))
        self.assertIn("Spent on retired attempts (`runs/_retired/`, 1): $0.50.", page)

    def test_rerun_retires_a_crashed_run(self):
        self.cfg["bugs"], self.cfg["arms"] = ["trunc"], ["plain"]
        rd = os.path.join(self.cfg["paths"]["runs"], "pilot-trunc-plain-1")
        os.makedirs(rd)
        common.write_json(os.path.join(rd, "run.json"), {"set": "pilot", "run_id": "pilot-trunc-plain-1", "arm": "plain",
                                                         "bug": "trunc", "run": 1, "status": "running", "cost_usd": 0.3})
        lines = []
        self.assertEqual(sets.run_set(self.cfg, "pilot", 1, log=lines.append, user=set()), 0, lines)
        self.assertIn("run pilot-trunc-plain-1", lines)
        self.assertEqual(common.read_json(os.path.join(rd, "run.json"))["status"], "done")
        retired = report.load_retired(self.cfg)
        self.assertEqual([(r["status"], r["cost_usd"]) for r in retired], [("running", 0.3)])

    def test_calibrate_runs_the_tier_script(self):
        import bench
        out = io.StringIO()
        os.environ["FAULTLINE_SEED"] = "0x1"  # not in env_keep: the script never sees it
        with contextlib.redirect_stdout(out):
            rc = bench.main(["tier2", "--config", os.path.join(self.cfg["bench"], "tier2", "config.json"), "calibrate"])
        self.assertEqual(rc, 0)
        self.assertIn("| row | holds |\nFAULTLINE_ settings: 0\ncalibrate: every row holds", out.getvalue())

    def test_preflight_flags_leftovers_and_leaked_paths(self):
        self.assertEqual(sets.preflight(self.cfg), [])
        name = re.sub(r"[^A-Za-z0-9]", "-", os.path.realpath(self.cfg["paths"]["workspace_root"])) + "-deadbeef"
        os.makedirs(os.path.join(common.projects_dir(self.cfg), name))
        with open(os.path.join(self.cfg["paths"]["service"], "logs", "plain-sync.txt"), "a") as f:
            f.write("at %s/kv.go:3\n" % self.cfg["paths"]["repo"])
        probs = sets.preflight(self.cfg)
        self.assertTrue(any(name in p and "move it out" in p for p in probs), probs)
        self.assertIn("forbidden path in tier2/service/logs/plain-sync.txt: " + self.cfg["paths"]["repo"], probs)

    def test_the_screen_needs_no_plugin(self):
        common.rmtree(self.cfg["paths"]["plugin"])
        self.assertEqual(sets.preflight(self.cfg, self.cfg["screen_arms"]), [])
        self.assertEqual(sets.preflight(self.cfg), ["missing plugin: %s (run arms/build.sh)" % self.cfg["paths"]["plugin"]])
        # each read arm needs its own version's variants only
        common.rmtree(common.variant_dir(self.cfg, "plain", "epoch"))
        self.assertEqual(sets.preflight(self.cfg, ["read-faultline"]), [])
        self.assertEqual(sets.preflight(self.cfg, ["read-plain"]),
                         ["missing: " + os.path.join(self.cfg["paths"]["service"], "testdata", "plain", "epoch")])


if __name__ == "__main__":
    unittest.main()
