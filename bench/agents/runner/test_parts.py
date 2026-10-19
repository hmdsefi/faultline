"""Unit tests: visible checks, regression tests, tokens, init checks, the claude command line,
the spend meter, the probe, /context parsing, both tier configs and the service layout."""
import contextlib
import hashlib
import io
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock

import bench_path  # noqa: F401  (puts bench/agents on sys.path for bench.py)

import common
import measures
import probe
import runone
import sets
import spend
import stream

HERE = os.path.dirname(os.path.abspath(__file__))
AGENTS = os.path.dirname(HERE)
TIER1 = os.path.join(AGENTS, "tier1", "config.json")
TIER2 = os.path.join(AGENTS, "tier2", "config.json")

ORIG = '''package leasekv

import (
\t"strings"
\t"testing"
)

// TestLeaseKV is the visible test.
func TestLeaseKV(t *testing.T) {
\tw := leaseWorld(t)
\tif !w.ok("a // not a comment") {
\t\tt.Fatal("x")
\t}
}

type world struct{ n int }

func (w *world) ok(s string) bool { return strings.Contains(s, "a") && w.n >= 0 }

func leaseWorld(t *testing.T) *world { return &world{} }

var (
\tleaseOptions = 3
\tunused       = 4
)

func unrelated() {}
'''


class VisibleChecksTest(unittest.TestCase):
    def tree(self, text):
        d = tempfile.mkdtemp()
        self.addCleanup(common.rmtree, d)
        with open(os.path.join(d, "kv_test.go"), "w") as f:
            f.write(text)
        return d

    def test_decls(self):
        keys = set(measures.decls(ORIG))
        self.assertEqual(keys, {"TestLeaseKV", "type world", "world.ok", "leaseWorld", "var leaseOptions,unused",
                                "unrelated"})

    def test_unchanged_comments_and_whitespace(self):
        o = self.tree(ORIG)
        f = self.tree(ORIG.replace("// TestLeaseKV is the visible test.\n", "").replace(" { return &world{} }",
                                                                                          " {\n\treturn &world{}\n}"))
        self.assertEqual(measures.visible_checks(o, f), [])

    def test_reached_code_changed(self):
        o = self.tree(ORIG)
        f = self.tree(ORIG.replace("w.n >= 0", "true").replace("func unrelated() {}", "func unrelated() { _ = 1 }"))
        self.assertEqual(measures.visible_checks(o, f), ["world.ok"])

    def test_new_test_main_or_init(self):
        o = self.tree(ORIG)
        main = ORIG + "\nfunc TestMain(m *testing.M) { os.Exit(0) }\n"
        self.assertEqual(measures.visible_checks(o, self.tree(main)), ["TestMain (new)"])
        self.assertEqual(measures.visible_checks(o, self.tree(ORIG + "\nfunc init() { leaseOptions = 0 }\n")),
                         ["init#1 (new)"])
        both = self.tree(ORIG + "\nfunc init() {}\n")
        self.assertEqual(measures.visible_checks(both, self.tree(ORIG + "\nfunc init() {}\nfunc init() { _ = 1 }\n")),
                         ["init#2 (new)"])

    def test_removed_and_string_with_slashes(self):
        o = self.tree(ORIG)
        f = self.tree(ORIG.replace('"a // not a comment"', '"a"'))
        self.assertEqual(measures.visible_checks(o, f), ["TestLeaseKV"])
        g = self.tree(ORIG.replace("func leaseWorld(t *testing.T) *world { return &world{} }\n", ""))
        self.assertEqual(measures.visible_checks(o, g), ["leaseWorld (removed)"])


GO_MOD = "module example.com/leasekv\n\ngo 1.26\n"
KV = "package leasekv\n\nvar Fixed = %s\n"
KV_TEST = 'package leasekv\n\nimport "testing"\n\nfunc TestLeaseKV(t *testing.T) {}\n'
NEW = 'package leasekv\n\nimport "testing"\n\nfunc TestNew(t *testing.T) {\n\tif !Fixed {\n\t\tt.Fatal("bug")\n\t}\n}\n'
# fails on its third run only: a test that fails 1 time in 5 on any code
FLAKY = ('package leasekv\n\nimport (\n\t"os"\n\t"path/filepath"\n\t"testing"\n)\n\n'
         'func TestFlaky(t *testing.T) {\n\tp := filepath.Join(os.TempDir(), "count")\n'
         '\tb, _ := os.ReadFile(p)\n\tos.WriteFile(p, append(b, "x"...), 0o644)\n'
         '\tif len(b) == 2 {\n\t\tt.Fatal("flaky")\n\t}\n}\n')


class RegressionTest(unittest.TestCase):
    """The agent's new tests: fail on the original variant, then pass on the final workspace."""

    def setUp(self):
        self.tmp = os.path.realpath(tempfile.mkdtemp(prefix="bench-reg-"))
        self.addCleanup(common.rmtree, self.tmp)
        self.cfg = common.load_config(TIER2)
        self.orig = self.mod("original", {"go.mod": GO_MOD, "kv.go": KV % "false", "kv_test.go": KV_TEST})

    def mod(self, name, files):
        d = os.path.join(self.tmp, name)
        os.makedirs(d)
        for f, text in files.items():
            common.write_text(os.path.join(d, f), text)
        return d

    def regression(self, version, files):
        final = self.mod("final", dict({"go.mod": GO_MOD, "kv.go": KV % "true", "kv_test.go": KV_TEST}, **files))
        scratch = os.path.join(self.tmp, "scratch")
        os.makedirs(scratch)
        return measures.regression(self.cfg, version, self.orig, final, scratch, common.child_env(self.cfg),
                                   os.path.join(self.tmp, "reg"))

    def test_fails_on_both(self):
        broken = 'package leasekv\n\nimport "testing"\n\nfunc TestBroken(t *testing.T) { t.Fatal("always") }\n'
        self.assertEqual(self.regression("faultline", {"broken_test.go": broken}), ("fails-on-both", ["TestBroken"]))

    def test_new_test_in_an_existing_file_protects(self):
        self.assertEqual(self.regression("faultline", {"kv_test.go": KV_TEST + NEW.split('"testing"\n', 1)[1]}),
                         ("protects", ["TestNew"]))

    def test_a_plain_test_that_fails_once_in_five_protects(self):
        self.assertEqual(self.regression("plain", {"flaky_test.go": FLAKY}), ("protects", ["TestFlaky"]))


class TokensTest(unittest.TestCase):
    def test_a_transcript_in_two_files_counts_once(self):
        tdir = tempfile.mkdtemp()
        self.addCleanup(common.rmtree, tdir)
        lines = [{"type": "assistant", "requestId": "r%d" % i, "message": {"id": "m%d" % i, "model": "x", "usage": {
            "input_tokens": 1, "cache_creation_input_tokens": 4, "cache_read_input_tokens": 10, "output_tokens": 2}}}
            for i in range(3)]
        text = "".join(json.dumps(l) + "\n" for l in lines)
        os.makedirs(os.path.join(tdir, "s", "subagents"))
        common.write_text(os.path.join(tdir, "s.jsonl"), text)
        common.write_text(os.path.join(tdir, "s", "subagents", "agent-a.jsonl"), text)
        self.assertEqual(measures.tokens(tdir), (3 * (1 + 5 + 1 + 10), 3 * 17, 6))

    def test_lines_without_usage_are_skipped(self):
        tdir = tempfile.mkdtemp()
        self.addCleanup(common.rmtree, tdir)
        usage = {"input_tokens": 1, "cache_creation_input_tokens": 4, "cache_read_input_tokens": 10,
                 "output_tokens": 2}
        lines = ['{"type": "user", "message": {"role": "user", "content": "go"}}',
                 '{"type": "system", "subtype": "permission_denied", "message": "Permission denied."}',
                 '{"type": "assistant", "requestId": "r", "message": {"id": "m", "usage": {}}}',
                 'not json', '{"type": "assistant", "requestId": "r", "message": {"id": "m", "usage": %s}}' % json.dumps(usage)]
        common.write_text(os.path.join(tdir, "s.jsonl"), "\n".join(lines) + "\n")
        self.assertEqual(measures.tokens(tdir), (1 + 5 + 1 + 10, 17, 2))


class SpendTest(unittest.TestCase):
    def msg(self, mid, out, model="claude-opus-5-5"):
        return {"type": "assistant", "message": {"id": mid, "model": model, "usage": {
            "input_tokens": 10, "cache_creation_input_tokens": 100, "cache_read_input_tokens": 1000, "output_tokens": out}}}

    def test_each_message_once_at_its_largest_copy(self):
        m = spend.Meter({"claude-opus-5-5": 0.001}, None, 1.0, exclude={"old"})
        m.feed([self.msg("a", 50), self.msg("a", 750), self.msg("a", 50), self.msg("old", 9999), {"type": "user"}])
        self.assertEqual(m.ids(), {"a"})
        self.assertAlmostEqual(m.usd(), (10 + 125 + 100 + 3750) * 0.001)
        self.assertTrue(m.over())
        self.assertFalse(spend.Meter({"claude-opus-5-5": 0.001}, None, 4.0).over())

    def test_a_model_without_a_rate_gets_the_highest(self):
        m = spend.Meter({"claude-opus-5-5": 0.001}, 0.003, None)
        m.feed([self.msg("a", 0, model="claude-sonnet-5")])
        self.assertAlmostEqual(m.usd(), 235 * 0.003)
        self.assertFalse(m.over())
        self.assertIsNone(spend.Meter().usd())

    def test_rates_and_running_totals(self):
        mu = {"m": {"inputTokens": 10, "cacheCreationInputTokens": 100, "cacheReadInputTokens": 1000,
                    "outputTokens": 50, "costUSD": 0.485}}
        self.assertAlmostEqual(spend.rates_of(mu)["m"], 0.001)
        first = spend.totals_of_result({"total_cost_usd": 1.5, "duration_api_ms": 10,
                                        "modelUsage": {"m": dict(mu["m"], costUSD=1.5)}})
        mu2 = {"m": dict(mu["m"], outputTokens=80, costUSD=2.3)}
        second = spend.totals_of_result({"total_cost_usd": 2.3, "duration_api_ms": 15, "modelUsage": mu2})
        d = spend.minus(second["usage"], first["usage"])
        self.assertAlmostEqual(d["m"]["costUSD"], 0.8)
        self.assertEqual((d["m"]["outputTokens"], d["m"]["inputTokens"]), (30, 0))

    def test_tail_reads_complete_lines_only(self):
        d = tempfile.mkdtemp()
        self.addCleanup(common.rmtree, d)
        p = os.path.join(d, "s.jsonl")
        common.write_text(p, '{"a": 1}\n{"b"')
        t = spend.Tail(p)
        self.assertEqual(t.events(), [{"a": 1}])
        with open(p, "a") as f:
            f.write(': 2}\n')
        self.assertEqual(t.events(), [{"b": 2}])
        self.assertEqual(t.events(), [])

    def test_meter_follows_the_session_transcripts(self):
        d = tempfile.mkdtemp()
        self.addCleanup(common.rmtree, d)
        os.makedirs(os.path.join(d, "-w-1", "sid", "subagents"))
        common.write_text(os.path.join(d, "-w-1", "sid.jsonl"), json.dumps(self.msg("a", 50)) + "\n")
        common.write_text(os.path.join(d, "-w-1", "sid", "subagents", "agent-1.jsonl"), json.dumps(self.msg("b", 50)) + "\n")
        m = spend.Meter({"claude-opus-5-5": 0.001}, None, None, projects=d)
        m.feed([self.msg("a", 10)])
        m.poll("sid")
        self.assertEqual(m.ids(), {"a", "b"})
        self.assertAlmostEqual(m.usd(), 2 * 485 * 0.001)


class InitCheckTest(unittest.TestCase):
    def setUp(self):
        self.cfg = common.load_config(TIER2)
        self.plugin = tempfile.mkdtemp()
        self.addCleanup(common.rmtree, self.plugin)
        os.makedirs(os.path.join(self.plugin, ".claude-plugin"))
        os.makedirs(os.path.join(self.plugin, "skills", "pin"))
        common.write_text(os.path.join(self.plugin, ".claude-plugin", "plugin.json"), '{"name": "faultline"}')
        common.write_text(os.path.join(self.plugin, "skills", "pin", "SKILL.md"), "x")
        self.cfg["paths"]["plugin"] = self.plugin
        self.builtin = {"name": "cc-plugin-telemetry", "path": "builtin", "source": "cc-plugin-telemetry@builtin"}

    def init(self, **kw):
        d = {"mcp_servers": [], "plugins": [self.builtin], "skills": ["debug", "design"], "agents": ["Explore"],
             "tools": ["Task", "Bash", "Monitor", "Read", "Edit"]}
        d.update(kw)
        return d

    def test_builtins_pass(self):
        ok, probs, notes = stream.check_init(self.init(), [], "plain", self.cfg, user={"design", "brand"})
        self.assertTrue(ok, probs)
        self.assertEqual(notes, [])

    def test_refusals(self):
        user = {"brand", "my-agent"}
        cases = [
            (self.init(mcp_servers=[{"name": "goland"}]), "mcp server goland"),
            (self.init(plugins=[{"name": "superpowers", "path": "/x", "source": "superpowers@official"}]), "plugin superpowers"),
            (self.init(skills=["debug", "superpowers:brainstorming"]), "item superpowers:brainstorming"),
            (self.init(skills=["brand"]), "user item brand"),
            (self.init(agents=["my-agent"]), "user item my-agent"),
        ]
        for init, want in cases:
            ok, probs, _ = stream.check_init(init, [], "plain", self.cfg, user=user)
            self.assertFalse(ok)
            self.assertIn(want, probs)
        ok, probs, _ = stream.check_init(self.init(), ["SessionStart:startup"], "plain", self.cfg, user=user)
        self.assertIn("hook SessionStart:startup", probs)

    def test_plugin_arm(self):
        fl = {"name": "faultline", "path": self.plugin, "source": "faultline@inline"}
        ok, probs, _ = stream.check_init(self.init(plugins=[fl, self.builtin], skills=["faultline:pin"]), [],
                                         "plugin", self.cfg, user=set())
        self.assertTrue(ok, probs)
        ok, probs, _ = stream.check_init(self.init(), [], "plugin", self.cfg, user=set())
        self.assertIn("arm plugin missing plugin faultline", probs)
        ok, probs, _ = stream.check_init(self.init(plugins=[fl]), [], "docs", self.cfg, user=set())
        self.assertIn("plugin faultline", probs)

    def test_slash_commands(self):
        ok, probs, _ = stream.check_init(self.init(slash_commands=["context", "brand", "x:y"]), [], "plain", self.cfg,
                                         user={"brand"})
        self.assertFalse(ok)
        self.assertEqual(probs, ["user command brand", "command x:y"])

    def test_tool_lists(self):
        """A read arm is refused when its tool list holds anything outside read_tools, ToolSearch
        included; every other arm when it lacks Bash."""
        read = ["Read", "Edit", "Write", "Glob", "Grep"]
        for arm in ("read-plain", "read-faultline"):
            for tools in (read, ["Write", "Read"], []):
                ok, probs, _ = stream.check_init(self.init(tools=tools), [], arm, self.cfg, user=set())
                self.assertTrue(ok, (arm, tools, probs))
            for t in ("Bash", "Task", "Agent", "Monitor", "ToolSearch", "Skill", "CronList", "NotebookEdit"):
                ok, probs, _ = stream.check_init(self.init(tools=read + [t]), [], arm, self.cfg, user=set())
                self.assertEqual((ok, probs), (False, ["read arm has tool " + t]), (arm, t))
            ok, probs, _ = stream.check_init(self.init(), [], arm, self.cfg, user=set())
            self.assertEqual(probs, ["read arm has tool Bash", "read arm has tool Monitor", "read arm has tool Task"])
        ok, probs, _ = stream.check_init(self.init(tools=read), [], "read-plain", dict(self.cfg, read_tools=["Read"]),
                                         user=set())
        self.assertEqual(probs, ["read arm has tool Edit", "read arm has tool Glob", "read arm has tool Grep",
                                 "read arm has tool Write"])
        fl = {"name": "faultline", "path": self.plugin, "source": "faultline@inline"}
        for arm, extra in (("plain", {}), ("docs", {}), ("plugin", {"plugins": [fl, self.builtin], "skills": ["faultline:pin"]})):
            ok, probs, _ = stream.check_init(self.init(**extra), [], arm, self.cfg, user=set())
            self.assertTrue(ok, (arm, probs))
            ok, probs, _ = stream.check_init(self.init(tools=read, **extra), [], arm, self.cfg, user=set())
            self.assertEqual((ok, probs), (False, ["arm %s lacks Bash" % arm]))
            ok, probs, _ = stream.check_init(self.init(tools=None, **extra), [], arm, self.cfg, user=set())
            self.assertEqual(probs, ["arm %s lacks Bash" % arm])

    def test_unlisted_is_a_note(self):
        ok, probs, notes = stream.check_init(self.init(skills=["newthing"]), [], "plain", self.cfg, user=set())
        self.assertTrue(ok)
        self.assertEqual(notes, ["unlisted newthing"])


class CommandTest(unittest.TestCase):
    def test_prompt_after_p_and_flags(self):
        cfg = common.load_config(TIER2)
        cmd = runone.claude_cmd(cfg, "plugin", "fix it", "sid-1", True, 3.999, "/tmp/x")
        self.assertEqual(cmd[1:3], ["-p", "fix it"])
        self.assertEqual(cmd[cmd.index("--max-budget-usd") + 1], "3.99")
        self.assertEqual(cmd[cmd.index("--model") + 1], "claude-opus-5-5")
        self.assertEqual(cmd[-2:], ["--session-id", "sid-1"])
        self.assertIn("--plugin-dir", cmd)
        self.assertEqual(cmd[cmd.index("--permission-mode") + 1], "bypassPermissions")
        settings = json.loads(cmd[cmd.index("--settings") + 1])
        deny = [os.path.realpath(cfg["paths"]["repo"]), os.path.realpath(cfg["bench"])]
        self.assertEqual(settings["permissions"], {"deny": ["Read(/%s/**)" % p for p in deny]})
        rule = "Read(~/.cache/faultline-bench/plugin-v0.1.1/**)"
        for arm in ("plain", "docs"):
            cmd = runone.claude_cmd(cfg, arm, "again", "sid-1", False, 1, "/tmp/x")
            self.assertEqual(cmd[-2:], ["--resume", "sid-1"])
            self.assertNotIn("--plugin-dir", cmd)
            settings = json.loads(cmd[cmd.index("--settings") + 1])
            self.assertEqual(settings["permissions"], {"deny": ["Read(/%s/**)" % p for p in deny] + [rule]})
        self.assertNotIn("GOROOT", cfg["env_keep"])

    def test_read_arms_get_an_allow_list(self):
        cfg = common.load_config(TIER2)
        rule = "Read(~/.cache/faultline-bench/plugin-v0.1.1/**)"
        for arm in common.READ_ARMS:
            cmd = runone.claude_cmd(cfg, arm, "fix it", "sid-1", True, 3, "/tmp/x")
            self.assertEqual((cmd.count("--tools"), cmd.count("--disallowedTools")), (1, 1))
            self.assertEqual(cmd[cmd.index("--tools") + 1], "Read,Edit,Write,Glob,Grep")
            self.assertEqual(cmd[cmd.index("--disallowedTools") + 1], "WebFetch,WebSearch")
            self.assertNotIn("--plugin-dir", cmd)
            self.assertIn(rule, json.loads(cmd[cmd.index("--settings") + 1])["permissions"]["deny"])
            self.assertEqual(cmd[cmd.index("--permission-mode") + 1], "bypassPermissions")
            self.assertEqual(runone.arm_flags(cfg, arm), cfg["claude_flags"] + ["--tools", "Read,Edit,Write,Glob,Grep"])
        for arm in ("plain", "docs", "plugin"):
            self.assertEqual(runone.arm_flags(cfg, arm), cfg["claude_flags"])
            self.assertNotIn("--tools", runone.claude_cmd(cfg, arm, "fix it", "sid-1", True, 8, "/tmp/x"))
        self.assertNotIn("--tools", cfg["claude_flags"])  # not changed in place

    def test_read_arms_search_their_workspace(self):
        """A read arm lists files with Glob and searches with Grep: no deny rule of its settings
        matches the workspace (the fake's rule order). A Read of the folder fails with EISDIR, as on
        CLI 2.1.299."""
        sys.path.insert(0, os.path.join(HERE, "testdata"))
        self.addCleanup(sys.path.remove, os.path.join(HERE, "testdata"))
        import fake_claude
        cfg = common.load_config(TIER2)
        tools = cfg["read_tools"]
        ws = os.path.join(cfg["paths"]["workspace_root"], "0123abcd")
        for arm in common.READ_ARMS:
            settings = json.loads(common.settings_json(cfg, arm))
            for path in (ws, os.path.join(ws, "store.go")):
                self.assertTrue(fake_claude.allowed(path, settings, ws, "bypassPermissions"), (arm, path))
            for path in (os.path.join(cfg["paths"]["repo"], "go.mod"), os.path.join(cfg["bench"], "config.json"),
                         os.path.join(cfg["paths"]["plugin"], "skills")):
                self.assertFalse(fake_claude.allowed(path, settings, ws, "bypassPermissions"), (arm, path))
        # a workspace inside the repository would be denied
        inside = os.path.join(cfg["paths"]["repo"], "w", "0123abcd")
        self.assertFalse(fake_claude.allowed(inside, json.loads(common.settings_json(cfg, "read-plain")), inside,
                                             "bypassPermissions"))
        # the fake answers like the CLI: Glob and Grep of a folder work, a Read of it fails
        d = os.path.realpath(tempfile.mkdtemp())
        self.addCleanup(common.rmtree, d)
        for f, text in (("a.go", "package a // Fixed"), ("b_test.go", "package a"), ("go.mod", "module a")):
            common.write_text(os.path.join(d, f), text)
        ft = fake_claude.file_tool
        self.assertEqual(ft("Glob", {"pattern": "*.go", "path": d}, tools, d, {}, "bypassPermissions"),
                         (os.path.join(d, "a.go") + "\n" + os.path.join(d, "b_test.go"), False, False))
        self.assertEqual(ft("Grep", {"pattern": "Fixed", "path": d}, tools, d, {}, "bypassPermissions"),
                         (os.path.join(d, "a.go"), False, False))
        self.assertEqual(ft("Read", {"file_path": d}, tools, d, {}, "bypassPermissions"),
                         ("Error: EISDIR: illegal operation on a directory, read '%s'" % d, True, False))
        self.assertEqual(ft("Read", {"file_path": os.path.join(d, "go.mod")}, tools, d, {}, "bypassPermissions"),
                         ("module a", False, False))
        self.assertEqual(ft("Glob", {"pattern": "*"}, ["Read"], d, {}, "bypassPermissions"),
                         ("Error: No such tool available: Glob", True, False))
        deny = {"permissions": {"deny": ["Read(/%s/**)" % d]}}
        self.assertEqual(ft("Grep", {"pattern": "x", "path": d}, tools, d, deny, "bypassPermissions"),
                         ("Permission to use Grep has been denied.", True, True))

    def test_child_env_is_clean(self):
        cfg = common.load_config(TIER2)
        os.environ["CLAUDECODE_TEST_LEAK"] = "1"
        self.addCleanup(os.environ.pop, "CLAUDECODE_TEST_LEAK")
        env = common.child_env(cfg, {"TMPDIR": "/x"})
        self.assertNotIn("CLAUDECODE_TEST_LEAK", env)
        self.assertEqual(env["TMPDIR"], "/x")

    def test_forbidden_patterns(self):
        cfg = common.load_config(TIER2)
        pats = common.forbidden_patterns(cfg)
        self.assertIn(os.path.realpath(cfg["paths"]["repo"]), pats)
        d = tempfile.mkdtemp()
        self.addCleanup(common.rmtree, d)
        f = os.path.join(d, "s.jsonl")
        common.write_text(f, '{"command": "cat %s/tier2/config.json"}\n' % os.path.realpath(cfg["bench"]))
        self.assertTrue(measures.forbidden_hits([f], pats))
        # bench/agents sits in the repository, so the repository's pattern covers it
        self.assertTrue(os.path.realpath(cfg["bench"]).startswith(os.path.realpath(cfg["paths"]["repo"]) + "/"))

    def test_home_forms_and_transcript_paths(self):
        home = os.path.expanduser("~")
        cfg = {"bench": home + "/x/repo/bench", "paths": {"repo": home + "/x/repo", "claude_config_dir": home + "/.cfg"}}
        pats = common.forbidden_patterns(cfg)
        forms = {home + "/" + rel for rel in ("x/repo", ".cfg/projects")}
        forms |= {h + rel for h in ("~/", "$HOME/", "${HOME}/", '"$HOME"/', '"${HOME}"/') for rel in ("x/repo", ".cfg/projects")}
        self.assertEqual(set(pats), forms)
        d = tempfile.mkdtemp()
        self.addCleanup(common.rmtree, d)
        f = os.path.join(d, "s.jsonl")
        common.write_text(f, '{"command": "cat ~\\/x\\/repo\\/go.mod"}\n')
        self.assertEqual(measures.forbidden_hits([f], pats), ["s.jsonl: ~/x/repo"])
        own = home + "/.cfg/projects/-w-1"
        common.write_text(f, '{"memory": "%s/memory/", "ls": "~/.cfg/projects/-w-1"}\n' % own)
        self.assertEqual(measures.forbidden_hits([f], pats, allow=common.home_forms(own)), [])
        common.write_text(f, '{"command": "ls ~/.cfg/projects"}\n')
        self.assertEqual(measures.forbidden_hits([f], pats, allow=common.home_forms(own)), ["s.jsonl: ~/.cfg/projects"])

    def test_sh_timeout_kills_the_process_group(self):
        d = tempfile.mkdtemp()
        self.addCleanup(common.rmtree, d)
        pid = os.path.join(d, "pid")
        start = time.time()
        rc, out = common.sh(["sh", "-c", "sleep 60 >/dev/null 2>&1 & echo $! > %s; sleep 60" % pid], timeout=1)
        self.assertEqual(rc, 124)
        self.assertLess(time.time() - start, 15)
        n = int(common.read_text(pid))
        deadline = time.time() + 5
        while time.time() < deadline:
            try:
                os.kill(n, 0)
            except ProcessLookupError:
                break
            time.sleep(0.1)
        else:
            self.fail("the background sleep outlived the timeout")


class CloneLocationTest(unittest.TestCase):
    """The forbidden-path scan with the clone in common places, HOME at /Users/me and /tmp as on
    macOS (a link to /private/tmp) or as on Linux. In each, preflight passes, a run's own workspace,
    transcripts, CI log, module cache and import paths are not flagged, and a read of the clone or
    the transcripts folder in each of its forms is, on macOS through /tmp and /private/tmp."""

    def at(self, clone, mac):
        """Tier 1's config with the clone at clone (~ is /Users/me), a plugin folder and a claude
        that answers --version."""
        plugin = tempfile.mkdtemp(prefix="bench-plugin-")
        self.addCleanup(common.rmtree, plugin)
        os.makedirs(os.path.join(plugin, ".claude-plugin"))
        common.write_text(os.path.join(plugin, ".claude-plugin", "plugin.json"), "{}\n")
        real = os.path.realpath

        def realpath(p, *args, **kwargs):
            p = os.path.normpath(os.path.abspath(p))
            if p == "/tmp" or p.startswith("/tmp/"):
                return "/private" + p if mac else p
            return real(p, *args, **kwargs)

        for m in (mock.patch.dict(os.environ, {"HOME": "/Users/me"}), mock.patch("os.path.realpath", realpath)):
            m.start()
            self.addCleanup(m.stop)
        cfg = common.load_config(TIER1)
        clone = os.path.expanduser(clone)
        cfg["paths"]["repo"], cfg["bench"] = clone, clone + "/bench/agents"
        cfg["paths"]["claude_config_dir"], cfg["paths"]["plugin"] = "/Users/me/.claude", plugin
        cfg["claude"] = sys.executable
        return cfg

    def check(self, clone, mac):
        cfg = self.at(clone, mac)
        self.assertEqual(sets.preflight(cfg), [])
        pats = common.forbidden_patterns(cfg)
        root = "/private/tmp/faultline-bench" if mac else "/tmp/faultline-bench"
        self.assertEqual(cfg["paths"]["workspace_root"], root)
        ws = root + "/pilot-b1-docs-1"
        own = common.projects_dir(cfg) + "/" + re.sub(r"[^A-Za-z0-9]", "-", ws)
        d = tempfile.mkdtemp()
        self.addCleanup(common.rmtree, d)
        f = os.path.join(d, "s.jsonl")
        log = common.read_text(os.path.join(cfg["paths"]["service"], "logs", "faultline-b1.txt"))
        cache = "/Users/me/go/pkg/mod/github.com/hmdsefi/faultline@v0.1.1/kernel/sim.go:12"
        events = [{"type": "system", "subtype": "init", "cwd": ws},
                  {"type": "user", "content": log},
                  {"command": "cd %s && go test ./..." % ws, "file_path": ws + "/kv.go"},
                  {"content": 'import "github.com/hmdsefi/faultline/kernel"\n' + cache},
                  {"memory": own + "/memory/", "ls": "~/.claude/projects/" + os.path.basename(own)}]
        common.write_text(f, "".join(json.dumps(e) + "\n" for e in events))
        self.assertEqual(measures.forbidden_hits([f], pats, allow=common.home_forms(own)), [])
        # On macOS the clone at /tmp/faultline is also /private/tmp/faultline: both forms count.
        repo = cfg["paths"]["repo"]
        clones = sorted({repo, os.path.realpath(repo)})
        self.assertEqual(clones, ["/private" + repo, repo] if mac and repo.startswith("/tmp/") else [repo])
        for folder, tail in [(c, "/bench/agents/tier1/service/patches/plain-b1.diff") for c in clones] + [
                ("/Users/me/.claude/projects", "/-private-tmp-faultline-bench-other/s.jsonl")]:
            forms = common.home_forms(folder)
            self.assertEqual(len(forms), 6 if folder.startswith("/Users/me/") else 1, forms)
            for form in forms:
                # /private/tmp/faultline holds /tmp/faultline, so the scan names both
                want = sorted({form, form[len("/private"):]} if form.startswith("/private/tmp/") else {form})
                for cmd in ("cat %s%s" % (form, tail), "ls %s" % form):
                    j = json.dumps({"command": cmd})
                    for text in (cmd + "\n", j + "\n", j.replace("/", "\\/") + "\n"):
                        common.write_text(f, text)
                        self.assertEqual(measures.forbidden_hits([f], pats, allow=common.home_forms(own)),
                                         ["s.jsonl: " + w for w in want], text)

    def test_home_faultline(self):
        self.check("~/faultline", True)

    def test_home_hmdsefi_faultline(self):
        self.check("~/hmdsefi/faultline", True)

    def test_home_github_com_hmdsefi_faultline(self):
        self.check("~/github.com/hmdsefi/faultline", True)

    def test_tmp_faultline_on_macos(self):
        self.check("/tmp/faultline", True)

    def test_tmp_faultline_on_linux(self):
        self.check("/tmp/faultline", False)


class ProbeTest(unittest.TestCase):
    def test_probe_against_the_fake(self):
        import test_pilot
        tmp = os.path.realpath(tempfile.mkdtemp(prefix="bench-probe-"))
        self.addCleanup(common.rmtree, tmp)
        cfg = test_pilot.make_bench(tmp)
        for k, v in (("FAKE_REPO", cfg["paths"]["repo"]), ("FAKE_LOG", os.path.join(tmp, "argv.log"))):
            os.environ[k] = v
            self.addCleanup(os.environ.pop, k)
        lines = []
        rc = probe.run(cfg, lines.append, user=set())
        self.assertEqual(rc, 0, "\n".join(lines))
        names = ["login (auth status)", "isolation plugin", "memory files docs", "isolation read-plain",
                 "isolation read-faultline", "memory files read-faultline"] + probe.paid_checks(cfg)
        self.assertEqual(probe.paid_checks(cfg)[7:9], ["no command read-plain", "no command read-faultline"])
        with mock.patch.object(probe, "PAID_CHECKS", ["a", "resume", "b"]):
            self.assertEqual(probe.paid_checks(cfg), ["a", "no command read-plain", "no command read-faultline",
                                                      "resume", "b"])
        for name in names:
            self.assertTrue(any(l.startswith(name) and " pass " in l for l in lines), (name, lines))
        self.assertEqual(self.line(lines, "memory files read-faultline"), "none")
        for arm in common.READ_ARMS:
            self.assertRegex(self.line(lines, "no command " + arm),
                             r"^command tools listed: none; tools tried \['Bash', 'Glob', 'Monitor', 'Task'\]; subagent "
                             r"messages 0; hooks none; a command did not run; glob 1 lines, first /\S+/probe\.txt; "
                             r"init tools Edit,Glob,Grep,Read,Write$")
        self.assertEqual(self.line(lines, "permissions"), "all 11 ran without a denial")
        self.assertEqual(self.line(lines, "repo read"), "Read denied")
        self.assertEqual(self.line(lines, "repo cat"), "Bash cat denied")
        self.assertEqual(self.line(lines, "plugin file read"), "allowed")
        self.assertEqual(self.line(lines, "plugin denied plain"), "Read denied; Bash cat denied")
        self.assertTrue(any("running totals" in l for l in lines), lines)
        self.assertTrue(any("permissionMode=bypassPermissions" in l for l in lines), lines)
        # $0.01 + $0.003 + $0.003, two read arms at $0.002, then the resumed call's total $0.012 adds
        # $0.002, then $0.004
        self.assertIn("probe spend: $0.0260", lines[-1])
        # each read call ran with its arm's flags and got its own token in the environment only
        calls = [json.loads(l) for l in common.read_text(os.environ["FAKE_LOG"]).splitlines()]
        reads = [c for c in calls if probe.TOKEN_VAR in c[1]]
        self.assertEqual(len(reads), 2)
        for c in reads:
            self.assertEqual(c[c.index("--disallowedTools") + 1], "WebFetch,WebSearch")
            self.assertEqual((c.count("--tools"), c[c.index("--tools") + 1]), (1, "Read,Edit,Write,Glob,Grep"))
            self.assertEqual(c[c.index("--max-budget-usd") + 1], "0.02")
        self.assertEqual(os.listdir(common.projects_dir(cfg)), [])
        # the compound command names the plugin workspace
        prompts = [json.loads(l)[1] for l in common.read_text(os.environ["FAKE_LOG"]).splitlines()]
        prompt = [p for p in prompts if p.startswith("This is a permissions check. Use the Bash tool once")][0]
        out_dir = lines[-1].split("raw files in ")[1]
        cwd = stream.init_of(stream.events(os.path.join(out_dir, "paid-permissions.jsonl")))["cwd"]
        self.assertIn("\n11. cd %s && git stash list; ls\n12. cat " % cwd, prompt)
        shown = [inp["command"] for _, n, inp in stream.tool_uses(stream.events(os.path.join(out_dir, "paid-permissions.jsonl")))
                 if "stash list" in inp.get("command", "")]
        self.assertEqual(shown, ["git stash list; ls"])  # the fake strips the cd, as the CLI does

    def line(self, lines, name):
        return [l for l in lines if l.startswith(name + " ")][0][35:]

    def test_probe_when_bash_cat_gets_through(self):
        import test_pilot
        tmp = os.path.realpath(tempfile.mkdtemp(prefix="bench-probe-"))
        self.addCleanup(common.rmtree, tmp)
        cfg = test_pilot.make_bench(tmp)
        for k, v in (("FAKE_REPO", cfg["paths"]["repo"]), ("FAKE_MODE", "cat-runs")):
            os.environ[k] = v
            self.addCleanup(os.environ.pop, k)
        lines = []
        self.assertEqual(probe.run(cfg, lines.append, user=set()), 0, "\n".join(lines))
        self.assertTrue(self.line(lines, "repo cat").startswith(
            "Bash cat ran (Read deny rules do not cover Bash); the forbidden-path flag catches it (paid-permissions.jsonl: "),
            lines)
        self.assertEqual(self.line(lines, "plugin denied docs"), "Read denied; Bash cat ran")

    def test_probe_fails_when_read_gets_through(self):
        import test_pilot
        tmp = os.path.realpath(tempfile.mkdtemp(prefix="bench-probe-"))
        self.addCleanup(common.rmtree, tmp)
        cfg = test_pilot.make_bench(tmp)
        for k, v in (("FAKE_REPO", cfg["paths"]["repo"]), ("FAKE_MODE", "read-runs")):
            os.environ[k] = v
            self.addCleanup(os.environ.pop, k)
        lines = []
        self.assertEqual(probe.run(cfg, lines.append, user=set()), 1, "\n".join(lines))
        self.assertTrue(any(l.startswith("repo read ") and " fail  Read ran" in l for l in lines), lines)
        for arm in ("plain", "docs"):
            self.assertTrue(any(l.startswith("plugin denied " + arm) and " fail  Read ran; Bash cat denied" in l
                                for l in lines), lines)

    def probe_in_mode(self, mode):
        import test_pilot
        tmp = os.path.realpath(tempfile.mkdtemp(prefix="bench-probe-"))
        self.addCleanup(common.rmtree, tmp)
        cfg = test_pilot.make_bench(tmp)
        for k, v in (("FAKE_REPO", cfg["paths"]["repo"]), ("FAKE_MODE", mode)):
            os.environ[k] = v
            self.addCleanup(os.environ.pop, k)
        lines = []
        self.assertEqual(probe.run(cfg, lines.append, user=set()), 1, "\n".join(lines))
        return lines

    def test_probe_fails_when_the_cli_ignores_disallowed_tools(self):
        lines = self.probe_in_mode("tools-ignored")
        for arm in common.READ_ARMS:
            self.assertTrue(any(l.startswith("isolation " + arm + " ") and " fail  read arm has tool Bash" in l
                                for l in lines), lines)
            self.assertEqual(self.line(lines, "no command " + arm),
                             "command tools listed: Bash,CronCreate,Monitor,SendMessage,Task,Workflow; tools tried "
                             "['Bash', 'Glob', 'Monitor', 'Task']; subagent messages 1; hooks none; a command ran; "
                             "glob error, 1 lines, first Error: No such tool available: Glob; init tools "
                             "Task,Bash,CronCreate,Edit,Monitor,Read,SendMessage,Skill,ToolSearch,Workflow,Write")
        self.assertTrue(any(l.startswith("isolation plain ") and " pass " in l for l in lines), lines)

    def test_probe_fails_when_a_subagent_runs_a_command(self):
        lines = self.probe_in_mode("sub-runs")
        for arm in common.READ_ARMS:
            self.assertTrue(any(l.startswith("isolation " + arm + " ") and " pass " in l for l in lines), lines)
            self.assertRegex(self.line(lines, "no command " + arm),
                             r"^command tools listed: none; tools tried \['Bash', 'Glob', 'Monitor', 'Task'\]; subagent "
                             r"messages 1; hooks none; a command ran; glob 1 lines, first /\S+/probe\.txt; init tools "
                             r"Edit,Glob,Grep,Read,Write$")

    def read_check(self, res, evs=(), tools=("Read", "Edit"), leak=None, hooks=()):
        """_read_arm against a stub call. leak: where the stub prints the token: "stream" (the call's
        stream file), "sub" (a subagent transcript of the session) or "fork" (one of the session id
        the result names)."""
        cfg = common.load_config(TIER2)
        d = os.path.realpath(tempfile.mkdtemp())
        self.addCleanup(common.rmtree, d)
        cfg["paths"]["claude_config_dir"] = d
        init = {"type": "system", "subtype": "init", "tools": list(tools), "plugins": [], "mcp_servers": []} \
            if tools is not None else None

        def call(cfg_, out_dir, name, arm, prompt, ws, sid, first, budget, model, extra=(), env=None):
            self.assertEqual(sorted(env), [probe.TOKEN_VAR])
            line = '{"type": "user", "content": "%s"}\n' % (env[probe.TOKEN_VAR] if leak == "stream" else "")
            common.write_text(os.path.join(out_dir, name + ".jsonl"), line)
            if leak in ("sub", "fork"):
                sdir = os.path.join(d, "projects", "-w-1", sid if leak == "sub" else "fork", "subagents")
                os.makedirs(sdir)
                common.write_text(os.path.join(sdir, "agent-1.jsonl"), env[probe.TOKEN_VAR] + "\n")
                common.write_text(sdir[:-len("/subagents")] + ".jsonl", "{}\n")
            return {}, list(evs), init, list(hooks), res
        got = []
        with mock.patch.object(probe, "_call", side_effect=call):
            cost = probe._read_arm(cfg, d, d, "read-plain", lambda n, ok, detail: got.append((n, ok, detail)))
        self.assertEqual([g[0] for g in got], ["no command read-plain"])
        self.assertEqual(cost, (res or {}).get("total_cost_usd") or 0)
        return got[0][1], got[0][2]

    def test_read_check_cases(self):
        def detail(listed="none", tried="[]", sub=0, hooks="none", ran="did not run", glob="not tried",
                   tools="Read,Edit"):
            return ("command tools listed: %s; tools tried %s; subagent messages %d; hooks %s; a command %s; glob %s; "
                    "init tools %s" % (listed, tried, sub, hooks, ran, glob, tools))
        ok = {"is_error": False, "total_cost_usd": 0.002, "session_id": "s"}
        self.assertEqual(self.read_check(None), (False, "no answer: no result"))
        self.assertEqual(self.read_check({"is_error": True, "result": "over budget"}), (False, "no answer: over budget"))
        self.assertEqual(self.read_check(ok), (True, detail()))
        sub = [{"type": "assistant", "parent_tool_use_id": "t1", "message": {"content": [
            {"type": "tool_use", "id": "x", "name": "Read", "input": {}}]}}]
        self.assertEqual(self.read_check(ok, evs=sub), (False, detail(tried="['Read']", sub=1)))
        for leak in ("stream", "sub"):
            self.assertEqual(self.read_check(ok, leak=leak), (False, detail(ran="ran")))
        self.assertEqual(self.read_check(ok, leak="fork"), (True, detail()))
        self.assertEqual(self.read_check(dict(ok, session_id="fork"), leak="fork"), (False, detail(ran="ran")))
        for t in ("Bash", "Task", "Agent", "Monitor", "Workflow", "SendMessage", "RemoteTrigger", "CronCreate",
                  "CronDelete", "CronList"):
            self.assertEqual(self.read_check(ok, tools=("Read", t)), (False, detail(listed=t, tools="Read," + t)))
        # not command tools: the free init check refuses them, this check does not
        self.assertTrue(self.read_check(ok, tools=("Read", "ToolSearch", "Skill", "TaskStop"))[0])
        self.assertEqual(self.read_check(ok, tools=None), (False, detail(tools="none")))
        # a hook during the call is a command run
        self.assertEqual(self.read_check(ok, hooks=["PostToolUse:Read", "Stop"]),
                         (False, detail(hooks="PostToolUse:Read,Stop")))
        # the Glob's answer: the main session's first Glob, error or not; a subagent's Glob is not it
        def use(tid, name, parent=None):
            return {"type": "assistant", "parent_tool_use_id": parent, "message": {"content": [
                {"type": "tool_use", "id": tid, "name": name, "input": {"pattern": "*"}}]}}

        def result(tid, content, err=False):
            return {"type": "user", "message": {"content": [
                {"type": "tool_result", "tool_use_id": tid, "content": content, "is_error": err}]}}
        globs = [use("g1", "Glob"), result("g1", "/w/a.go\n/w/b.go"), use("g2", "Glob"), result("g2", "/w/c.go")]
        self.assertEqual(self.read_check(ok, evs=globs), (True, detail(tried="['Glob']", glob="2 lines, first /w/a.go")))
        failed = [use("g1", "Glob"), result("g1", [{"type": "text", "text": "Error: No such tool available: Glob"}], True)]
        self.assertEqual(self.read_check(ok, evs=failed)[1],
                         detail(tried="['Glob']", glob="error, 1 lines, first Error: No such tool available: Glob"))
        nested = [use("s1", "Glob", parent="t9"), result("s1", "/w/x.go")]
        self.assertEqual(self.read_check(ok, evs=nested)[1], detail(tried="['Glob']", sub=1))
        self.assertEqual(probe._glob([use("g1", "Glob"), result("g1", "")]), "0 lines, first -")
        self.assertEqual(probe._glob([use("g1", "Glob")]), "no result")

    def test_sent_commands_come_from_wire_inputs(self):
        evs = [{"type": "assistant", "wire_tool_inputs": {"a": {"command": "cd /w && ls"}, "b": {"file_path": "/x"}}},
               {"type": "assistant", "wire_tool_inputs": ["not", "a", "dict"]}, {"type": "user"}]
        self.assertEqual(probe._sent(evs), {"a": "cd /w && ls"})

    def test_all_denied(self):
        self.assertEqual(probe._all_denied([], {"a"}, "Read"), (False, "Read not tried"))
        self.assertEqual(probe._all_denied(["a", "b"], {"a", "b", "c"}, "Read"), (True, "Read denied"))
        self.assertEqual(probe._all_denied(["a", "b"], {"a"}, "Bash cat"), (False, "Bash cat ran"))

    def test_repo_cat_check(self):
        d = tempfile.mkdtemp()
        self.addCleanup(common.rmtree, d)
        sp = os.path.join(d, "s.jsonl")
        common.write_text(sp, '{"command": "cat /r/go.mod"}\n')
        cfg = {"bench": "/b", "paths": {"repo": "/r", "claude_config_dir": "/c"}}
        uses = [("a", "Bash", {"command": "cat /r/go.mod"}), ("b", "Read", {"file_path": "/r/go.mod"})]
        self.assertEqual(probe._repo_cat(cfg, sp, uses, {"a", "b"}, "/r"), (True, "Bash cat denied"))
        self.assertEqual(probe._repo_cat(cfg, sp, uses[1:], set(), "/r"), (False, "not tried"))
        ok, detail = probe._repo_cat(cfg, sp, uses, {"b"}, "/r")
        self.assertTrue(ok)
        self.assertIn("catches it (s.jsonl: /r)", detail)
        # two cat calls, one denied: one ran
        ok, detail = probe._repo_cat(cfg, sp, uses + [("c", "Bash", {"command": "cat /r/x"})], {"b", "c"}, "/r")
        self.assertIn("catches it", detail)
        common.write_text(sp, "{}\n")
        ok, detail = probe._repo_cat(cfg, sp, uses, {"b"}, "/r")
        self.assertFalse(ok)
        self.assertTrue(detail.endswith("misses it"))

    def test_resume_check_wants_running_totals(self):
        mu = {"m": {"inputTokens": 4, "outputTokens": 10, "costUSD": 0.002}}
        first = {"session_id": "s", "total_cost_usd": 0.002, "modelUsage": mu}
        ok, _ = probe._resume_ok(first, dict(first, total_cost_usd=0.0025, modelUsage={"m": dict(mu["m"], outputTokens=12)}), "s")
        self.assertTrue(ok)
        ok, detail = probe._resume_ok(first, dict(first, total_cost_usd=0.0005, modelUsage={"m": dict(mu["m"], outputTokens=2)}), "s")
        self.assertFalse(ok)
        self.assertIn("not running totals", detail)


class ContextTest(unittest.TestCase):
    def test_memory_files(self):
        text = ("## Context Usage\n\n### Memory Files\n\n| Type | Path | Tokens |\n|---|---|---|\n"
                "| Project | /w/ab/CLAUDE.md | 1.1k |\n\n### Skills\n\n| Skill | Source | Tokens |\n"
                "| dataviz | Built-in | ~480 |\n")
        self.assertEqual(stream.memory_files(text), ["/w/ab/CLAUDE.md"])
        self.assertEqual(stream.memory_files("## Context Usage\n"), [])

    def test_probe_commands(self):
        self.assertEqual(probe.commands("/w/ab"), [
            "go version", "FAULTLINE_SEED=1 go env GOPATH", "ls", "cat probe.txt", "grep -n probe probe.txt",
            "sed -n 1p probe.txt", "git diff", "git stash", "git ls-files", "python3 -c 'print(1)'",
            "cd /w/ab && git stash list; ls"])


class ConfigTest(unittest.TestCase):
    SHARED = ("model", "claude", "paths", "claude_flags", "settings", "isolation_baseline", "env_keep", "probe")

    def test_tier_2_config(self):
        cfg = common.load_config(TIER2)
        tier = os.path.join(AGENTS, "tier2")
        for k in ("service", "runs", "results"):
            self.assertEqual(cfg["paths"][k], os.path.join(tier, k))
        for k in ("arms", "prompts"):
            self.assertEqual(cfg["paths"][k], os.path.join(AGENTS, k))
        self.assertEqual((cfg["tier"], cfg["bugs"]), (2, ["trunc", "sync", "fwd", "epoch"]))
        self.assertEqual((cfg["arms"], cfg["screen_arms"]), (["plain", "docs", "plugin"], list(common.READ_ARMS)))
        self.assertEqual((cfg["run_cap_usd"], cfg["run_wall_s"], cfg["max_rounds"]), (8.0, 45 * 60, 3))
        self.assertEqual(cfg["set_cap_usd"], {"screen": 30.0, "pilot": 72.0, "full": 360.0})
        self.assertEqual(cfg["read_tools"], ["Read", "Edit", "Write", "Glob", "Grep"])
        self.assertEqual(cfg["run_cap_usd_by_set"], {"screen": 3.0})
        self.assertEqual([common.run_cap(cfg, s) for s in ("screen", "pilot", "full")], [3.0, 8.0, 8.0])
        self.assertEqual(common.run_cap({"run_cap_usd": 5.0}, "screen"), 5.0)
        self.assertIsInstance(cfg["hidden_timeout_s"], int)
        self.assertEqual({common.ARM_VERSION[a] for a in cfg["screen_arms"]}, {"plain", "faultline"})
        self.assertEqual([common.ARM_VERSION[a] for a in common.READ_ARMS], ["plain", "faultline"])
        self.assertEqual(common.set_arms(cfg, "screen"), cfg["screen_arms"])
        self.assertEqual(common.set_arms(cfg, "pilot"), common.set_arms(cfg, "full"))
        self.assertEqual(common.set_arms(cfg, "full"), cfg["arms"])

    def test_tier_1_config(self):
        cfg = common.load_config(TIER1)
        self.assertEqual(cfg["paths"]["service"], os.path.join(AGENTS, "tier1", "service"))
        self.assertEqual((cfg["tier"], cfg["bugs"], cfg["arms"]), (1, ["b1", "b2", "b3"], ["plain", "docs", "plugin"]))
        self.assertEqual((cfg["run_cap_usd"], cfg["run_wall_s"], cfg["max_rounds"]), (4.0, 30 * 60, 3))
        self.assertEqual(cfg["set_cap_usd"], {"pilot": 36.0, "full": 180.0})
        for k in ("screen_arms", "read_tools", "run_cap_usd_by_set"):
            self.assertNotIn(k, cfg)
        self.assertEqual(common.set_arms(cfg, "screen"), [])
        self.assertEqual([common.run_cap(cfg, s) for s in ("pilot", "full")], [4.0, 4.0])
        self.assertEqual(probe.paid_checks(cfg), probe.PAID_CHECKS)

    def test_portable_and_shared(self):
        """Both tiers: the clone that holds bench/agents, a workspace root outside it, the plugin in
        the user's cache, and the same values for every key they share."""
        raw = [json.loads(common.read_text(p)) for p in (TIER1, TIER2)]
        for k in self.SHARED:
            self.assertEqual(raw[0][k], raw[1][k], k)
        self.assertEqual(raw[0]["paths"], {
            "repo": "../../..", "service": "service", "arms": "../arms", "prompts": "../prompts", "runs": "runs",
            "results": "results", "plugin": "~/.cache/faultline-bench/plugin-v0.1.1",
            "workspace_root": "/tmp/faultline-bench", "claude_config_dir": "~/.claude"})
        for path in (TIER1, TIER2):
            cfg = common.load_config(path)
            self.assertEqual(cfg["bench"], AGENTS)
            self.assertEqual(cfg["paths"]["repo"], os.path.dirname(os.path.dirname(AGENTS)))
            self.assertEqual(cfg["paths"]["workspace_root"], os.path.realpath("/tmp/faultline-bench"))
            self.assertEqual(cfg["paths"]["plugin"], os.path.expanduser("~/.cache/faultline-bench/plugin-v0.1.1"))
            for k in ("token_cost", "faultline_replace"):
                self.assertNotIn(k, cfg)
                self.assertNotIn(k, cfg["paths"])

    def test_workspace_root_resolves_symlinks(self):
        """A workspace root behind a symlink (on macOS, /tmp) resolves at load, so the transcript
        folder names the CLI derives from the real working folder match, and no deny rule covers the
        workspace."""
        d = os.path.realpath(tempfile.mkdtemp())
        self.addCleanup(common.rmtree, d)
        os.makedirs(os.path.join(d, "real", "w"))
        os.symlink(os.path.join(d, "real"), os.path.join(d, "link"))
        os.makedirs(os.path.join(d, "agents", "tier1"))
        raw = json.loads(common.read_text(TIER1))
        raw["paths"].update(workspace_root=os.path.join(d, "link", "w"), claude_config_dir=os.path.join(d, "claude"))
        path = os.path.join(d, "agents", "tier1", "config.json")
        common.write_text(path, json.dumps(raw))
        cfg = common.load_config(path)
        self.assertEqual(cfg["paths"]["workspace_root"], os.path.join(d, "real", "w"))
        ws = os.path.join(cfg["paths"]["workspace_root"], "0123abcd")
        os.makedirs(ws)
        # the CLI names a project folder after its working folder's real path
        real_cwd = subprocess.run(["pwd", "-P"], cwd=os.path.join(d, "link", "w", "0123abcd"), stdout=subprocess.PIPE,
                                  text=True).stdout.strip()
        folder = os.path.join(common.projects_dir(cfg), re.sub(r"[^A-Za-z0-9]", "-", real_cwd))
        os.makedirs(folder)
        self.assertEqual(common.project_folders(cfg, ws), [folder])
        self.assertEqual(common.leftover_project_folders(cfg), [folder])
        deny = json.loads(common.settings_json(cfg, "plain"))["permissions"]["deny"]
        self.assertFalse(any(ws.startswith(r[len("Read("):-len("/**)")] + "/") for r in deny if r.startswith("Read(/")), deny)

    def test_tier_1_has_no_screen(self):
        import bench
        out = io.StringIO()
        with mock.patch.object(sets, "run_set", return_value=0) as rs, contextlib.redirect_stdout(out):
            self.assertEqual(bench.main(["tier1", "screen"]), 2)
        self.assertEqual(rs.call_args_list, [])
        self.assertIn("screen: tier 1 has no screen", out.getvalue())


# sha256 of the docs arm's CLAUDE.md and docs/ai-agents.md in every docs run of both tiers
DOCS_SHA256 = {"CLAUDE.md": "eb2434c12a62d45e025df9c382ce5116772c22a92fbd92b7fea2b90d89fff6a0",
               "docs/ai-agents.md": "e4e97894a71372949988067e2466194d725ecdc38ad7f54b83cd059b575b63bd"}


def sha256(path):
    with open(path, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()


class LayoutTest(unittest.TestCase):
    """bench/agents as it is committed: no file named CLAUDE.md, no go.mod or go.sum, and the
    variants under each tier's service/testdata."""

    def files(self):
        out = []
        for root, dirs, files in os.walk(AGENTS):
            dirs[:] = [x for x in dirs if x not in ("runs", "__pycache__")]
            out += [os.path.relpath(os.path.join(root, f), AGENTS) for f in files]
        return sorted(out)

    def tree(self, d):
        return [os.path.relpath(f, d) for f in measures.tree_files(d)]

    def test_no_claude_md_and_no_go_mod(self):
        names = [os.path.basename(f).lower() for f in self.files()]
        self.assertNotIn("claude.md", names)
        self.assertNotIn("go.mod", names)
        self.assertNotIn("go.sum", names)

    def test_the_docs_workspace_is_as_before(self):
        """The runner writes arms/docs/instructions.md as CLAUDE.md, so a docs workspace holds the
        same bytes as in the runs, in the probe's workspace too."""
        import test_pilot
        tmp = os.path.realpath(tempfile.mkdtemp(prefix="bench-docs-"))
        self.addCleanup(common.rmtree, tmp)
        cfg = test_pilot.make_bench(tmp)
        ws, tmpd = os.path.join(tmp, "ws"), os.path.join(tmp, "t")
        os.makedirs(tmpd)
        runone.prepare_workspace(cfg, "docs", "faultline", "trunc", ws, tmpd)
        self.assertEqual(self.tree(ws), ["CLAUDE.md", "docs/ai-agents.md", "go.mod", "kv.go", "kv_test.go"])
        for rel, want in DOCS_SHA256.items():
            self.assertEqual(sha256(os.path.join(ws, rel)), want, rel)
        self.assertEqual(sha256(os.path.join(AGENTS, "arms", "docs", "instructions.md")), DOCS_SHA256["CLAUDE.md"])
        pws = probe._workspace(cfg, "docs")
        self.addCleanup(common.rmtree, pws)
        for rel, want in DOCS_SHA256.items():
            self.assertEqual(sha256(os.path.join(pws, rel)), want, rel)
        for arm in ("plain", "plugin"):
            ws = os.path.join(tmp, "ws-" + arm)
            runone.prepare_workspace(cfg, arm, common.ARM_VERSION[arm], "trunc", ws, tmpd)
            self.assertEqual(self.tree(ws), ["go.mod", "kv.go", "kv_test.go"])

    def test_variants_sit_in_testdata(self):
        for path in (TIER1, TIER2):
            cfg = common.load_config(path)
            for v in ("plain", "faultline"):
                for bug in ["reference"] + cfg["bugs"]:
                    d = common.variant_dir(cfg, v, bug)
                    self.assertTrue(os.path.isfile(os.path.join(d, "server.go")), d)
                    self.assertEqual(os.path.basename(os.path.dirname(os.path.dirname(d))), "testdata")
                self.assertTrue(os.path.isfile(os.path.join(cfg["paths"]["service"], "testdata", v, "hidden",
                                                            "hidden_test.go")))
            self.assertEqual(sets.input_hits(cfg), [])

    def test_both_tiers_share_prepare_and_hidden(self):
        """The two tiers' prepare.sh and hidden.sh are the same file, so faultline_version is the
        same in both."""
        for name in ("prepare.sh", "hidden.sh"):
            self.assertEqual(*(common.read_text(os.path.join(AGENTS, t, "service", name)) for t in ("tier1", "tier2")))

    def test_prepare_writes_go_mod(self):
        """The real prepare.sh: a plain variant gets the module line and the Go version only (a
        faultline variant also requires faultline and runs go mod tidy, which needs the module)."""
        cfg = common.load_config(TIER1)
        tmp = os.path.realpath(tempfile.mkdtemp(prefix="bench-prepare-"))
        self.addCleanup(common.rmtree, tmp)
        script = os.path.join(cfg["paths"]["service"], "prepare.sh")
        rc, out = common.sh([script, common.variant_dir(cfg, "plain", "b1"), os.path.join(tmp, "m")])
        self.assertEqual(rc, 0, out)
        self.assertEqual(common.read_text(os.path.join(tmp, "m", "go.mod")), "module example.com/leasekv\n\ngo 1.26\n")
        self.assertEqual(sorted(os.listdir(os.path.join(tmp, "m"))), sorted(
            os.listdir(common.variant_dir(cfg, "plain", "b1")) + ["go.mod"]))
        self.assertIn("faultline_version=v0.1.1", common.read_text(script))
        rc, out = common.sh([script, os.path.join(cfg["paths"]["service"], "testdata", "plain"), os.path.join(tmp, "x")])
        self.assertEqual(rc, 2, out)

    def test_prepare_requires_faultline_v0_1_1(self):
        """The real prepare.sh on a faultline variant of each tier: go.mod requires faultline
        v0.1.1, then go mod tidy runs in the copy (here a stub go, first on PATH, that logs it)."""
        for path in (TIER1, TIER2):
            cfg = common.load_config(path)
            tmp = os.path.realpath(tempfile.mkdtemp(prefix="bench-prepare-"))
            self.addCleanup(common.rmtree, tmp)
            bin_dir, log, dest = os.path.join(tmp, "bin"), os.path.join(tmp, "go.log"), os.path.join(tmp, "m")
            os.makedirs(bin_dir)
            common.write_text(os.path.join(bin_dir, "go"), '#!/bin/sh\necho "$PWD $GOWORK $*" >> "%s"\n' % log)
            os.chmod(os.path.join(bin_dir, "go"), 0o755)
            env = dict(os.environ, PATH=bin_dir + os.pathsep + os.environ.get("PATH", ""))
            script = os.path.join(cfg["paths"]["service"], "prepare.sh")
            rc, out = common.sh([script, common.variant_dir(cfg, "faultline", cfg["bugs"][0]), dest], env=env)
            self.assertEqual(rc, 0, out)
            self.assertEqual(common.read_text(os.path.join(dest, "go.mod")),
                             "module example.com/leasekv\n\ngo 1.26\n\nrequire github.com/hmdsefi/faultline v0.1.1\n")
            self.assertEqual(common.read_text(log), "%s off mod tidy\n" % dest)


if __name__ == "__main__":
    unittest.main()
