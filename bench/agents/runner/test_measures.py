"""Unit tests for the tokens from modelUsage and from the transcripts, and the test runs after the
first edit, on three runs of tier 1's pilot (testdata/pilot: run.json, the round's stream and the
usage lines of the transcripts) and on small made-up streams."""
import csv
import importlib.util
import json
import os
import shutil
import tempfile
import unittest
import warnings

import common
import measures
import report
import spend
import stream

HERE = os.path.dirname(os.path.abspath(__file__))
AGENTS = os.path.dirname(HERE)
PILOT = os.path.join(HERE, "testdata", "pilot")
RUNS = ("pilot-b1-plugin-1", "pilot-b3-plain-1", "pilot-b3-plugin-1")
# Another implementation of the transcript count, to compare with: a script with a function
# scan(path, seen) that returns (first timestamp, first prompt, models, weighted, processed).
OTHER_COUNT = os.environ.get("BENCH_TOKEN_COUNT_SCRIPT", "")


def pilot_stream(run):
    return os.path.join(PILOT, run, "stream-r1.jsonl")


class ModelUsageTokensTest(unittest.TestCase):
    def test_b1_plugin_from_the_result(self):
        """The pilot's b1 plugin run: Opus plus the Sonnet triage subagent."""
        res = stream.result_of(stream.events(pilot_stream("pilot-b1-plugin-1")))
        round1 = spend.minus(spend.totals_of_result(res)["usage"], spend.ZERO["usage"])
        self.assertEqual(sorted(round1), ["claude-opus-5-5", "claude-sonnet-5-5"])
        self.assertEqual(measures.model_usage_tokens(round1), (157464, 608870, 9319))
        rec = common.read_json(os.path.join(PILOT, "pilot-b1-plugin-1", "run.json"))
        self.assertEqual(measures.model_usage_tokens(rec["model_usage"]), (157464, 608870, 9319))

    def test_each_round_is_the_difference_of_the_running_totals(self):
        res = stream.result_of(stream.events(pilot_stream("pilot-b1-plugin-1")))
        first = spend.totals_of_result(res)
        mu2 = json.loads(json.dumps(res["modelUsage"]))
        mu2["claude-opus-5-5"]["outputTokens"] += 100
        mu2["claude-opus-5-5"]["cacheReadInputTokens"] += 1000
        mu2["claude-haiku-5-5"] = {"inputTokens": 3, "outputTokens": 7}
        second = spend.totals_of_result(dict(res, modelUsage=mu2))
        r2 = spend.minus(second["usage"], first["usage"])
        self.assertEqual(measures.model_usage_tokens(r2), (500 + 100 + 3 + 35, 1100 + 10, 107))
        run = spend.add(spend.add({}, spend.minus(first["usage"], {})), r2)
        self.assertEqual(measures.model_usage_tokens(run), (157464 + 638, 608870 + 1110, 9319 + 107))

    def test_weights_and_rounding_down(self):
        tok = measures.model_usage_tokens
        self.assertEqual(tok({"m": {"inputTokens": 3}}), (3, 3, 0))
        self.assertEqual(tok({"m": {"cacheCreationInputTokens": 4.0}}), (5, 4, 0))
        self.assertEqual(tok({"m": {"cacheReadInputTokens": 10}}), (1, 10, 0))
        self.assertEqual(tok({"m": {"cacheReadInputTokens": 19}}), (1, 19, 0))
        self.assertEqual(tok({"m": {"outputTokens": 2, "costUSD": 9.0}}), (10, 2, 2))
        self.assertEqual(tok({"a": {"cacheReadInputTokens": 7}, "b": {"cacheReadInputTokens": 3}}), (1, 10, 0))
        self.assertEqual(tok({}), (0, 0, 0))
        self.assertEqual(tok(None), (0, 0, 0))


def use(tid, name, inp, parent=None):
    return {"type": "assistant", "parent_tool_use_id": parent,
            "message": {"content": [{"type": "tool_use", "id": tid, "name": name, "input": inp}]}}


def result(tid, content, is_error=False, parent=None):
    return {"type": "user", "parent_tool_use_id": parent,
            "message": {"content": [{"type": "tool_result", "tool_use_id": tid, "content": content,
                                     "is_error": is_error}]}}


def bash(tid, command, out="ok  \texample.com/leasekv\t0.1s", is_error=False, parent=None):
    return [use(tid, "Bash", {"command": command}, parent), result(tid, out, is_error, parent)]


def edit(tid, path, name="Edit", is_error=False, parent=None):
    return [use(tid, name, {"file_path": path}, parent), result(tid, "updated", is_error, parent)]


class TranscriptTokensTest(unittest.TestCase):
    """The count over the transcripts. The pilot's run.json files were written before the runner
    took its tokens from modelUsage: their weighted_tokens, processed_tokens and output_tokens are
    this count over the run's transcripts, made at the time."""

    def test_the_pilot_runs_give_the_recorded_numbers(self):
        for run in RUNS:
            rec = common.read_json(os.path.join(PILOT, run, "run.json"))
            want = (rec["weighted_tokens"], rec["processed_tokens"], rec["output_tokens"])
            self.assertEqual(measures.tokens(os.path.join(PILOT, run, "transcripts")), want, run)
        self.assertEqual(measures.tokens(os.path.join(PILOT, "pilot-b1-plugin-1", "transcripts")),
                         (145905, 606558, 7007))

    def test_the_subagent_transcript_counts(self):
        tdir = os.path.join(PILOT, "pilot-b1-plugin-1", "transcripts")
        files = measures.transcript_files(tdir)
        self.assertEqual(len(files), 2)
        self.assertIn(os.sep + "subagents" + os.sep, files[1])
        w, p, out = measures.transcript_tokens(files[1], set())
        self.assertGreater(w, 0)
        main = measures.transcript_tokens(files[0], set())
        self.assertEqual(round(main[0] + w), 145905)

    @unittest.skipUnless(os.path.isfile(OTHER_COUNT), "set BENCH_TOKEN_COUNT_SCRIPT to compare with another count")
    def test_the_same_numbers_as_the_other_count(self):
        spec = importlib.util.spec_from_file_location("other_count", OTHER_COUNT)
        other = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(other)
        for run in RUNS:
            tdir = os.path.join(PILOT, run, "transcripts")
            seen, w, p = set(), 0.0, 0.0
            with warnings.catch_warnings():
                warnings.simplefilter("ignore", ResourceWarning)
                for f in measures.transcript_files(tdir):
                    fw, fp = other.scan(f, seen)[3:5]
                    w, p = w + fw, p + fp
            self.assertEqual(measures.tokens(tdir)[:2], (round(w), round(p)), run)


class TestRunsAfterEditTest(unittest.TestCase):
    def streams(self, *rounds):
        d = tempfile.mkdtemp()
        self.addCleanup(common.rmtree, d)
        out = []
        for i, evs in enumerate(rounds):
            p = os.path.join(d, "stream-r%d.jsonl" % (i + 1))
            common.write_text(p, "".join(json.dumps(e) + "\n" for e in evs))
            out.append(p)
        return out

    def test_b3_plugin_caught_its_own_wrong_fix(self):
        runs = measures.go_test_runs_after_edit([pilot_stream("pilot-b3-plugin-1")])
        self.assertEqual(measures.test_runs_after_edit([pilot_stream("pilot-b3-plugin-1")]), (2, 1))
        self.assertEqual([f for _, f, _ in runs], [True, False])
        self.assertIn("seed=0x78eb4fa9f502ca7f", runs[0][2])
        self.assertIn('"every op completes" failed', runs[0][2])

    def test_b3_plain_never_ran_a_test(self):
        self.assertEqual(measures.test_runs_after_edit([pilot_stream("pilot-b3-plain-1")]), (0, 0))

    def test_b1_plugin_skips_denied_calls_and_the_subagent(self):
        # five go test calls ran after the first edit; the git stash one was denied
        runs = measures.go_test_runs_after_edit([pilot_stream("pilot-b1-plugin-1")])
        self.assertEqual(len(runs), 5)
        self.assertFalse(any("git stash" in c for c, _, _ in runs))
        self.assertEqual(sum(f for _, f, _ in runs), 0)

    def test_what_starts_the_count(self):
        go = bash("t9", "go test ./...")
        cases = [
            (edit("e", "/w/kv.go"), 1),
            (edit("e", "/w/kv.go", name="Write"), 1),
            (edit("e", "/w/kv.go", name="MultiEdit"), 1),
            (edit("e", "/w/kv_test.go"), 0),
            (edit("e", "/w/README.md"), 0),
            (edit("e", "/w/kv.go", name="Read"), 0),
            (edit("e", "/w/kv.go", is_error=True), 0),
            (edit("e", "/w/kv.go", parent="toolu_agent"), 0),
            (edit("e", "/w/kv.go") + [{"type": "system", "subtype": "permission_denied", "tool_use_id": "e"}], 0),
        ]
        for evs, want in cases:
            self.assertEqual(measures.test_runs_after_edit(self.streams(evs + go))[0], want, evs[0])
        # an edit whose result never came still counts (a killed round)
        self.assertEqual(measures.test_runs_after_edit(self.streams([use("e", "Edit", {"file_path": "/w/kv.go"})] + go)),
                         (1, 0))

    def test_only_runs_after_the_edit_in_any_round(self):
        r1 = bash("t1", "go test ./...", "--- FAIL: TestX", True) + edit("e", "/w/kv.go") + bash("t2", "go vet ./...")
        r2 = bash("t3", "go test -count=1 ./...") + bash("t4", "cat kv.go")
        self.assertEqual(measures.test_runs_after_edit(self.streams(r1, r2)), (1, 0))

    def test_what_counts_as_failing(self):
        fails = [("--- FAIL: TestLeaseKV (0.01s)", False), ("FAIL\texample.com/leasekv\t0.3s", False),
                 ('invariant "reads are fresh" violated at t=1s', False), ("panic: runtime error", False),
                 ("Exit code 2", True), ([{"type": "text", "text": "--- FAIL: TestX"}], False)]
        passes = [("ok  \texample.com/leasekv\t0.2s", False), ("PASS", False), ("FAIL", False),
                  ("no panic here", False), ("", False)]
        for out, err in fails + passes:
            evs = edit("e", "/w/kv.go") + bash("t", "go test ./...", out, err)
            want = (1, 1) if (out, err) in fails else (1, 0)
            self.assertEqual(measures.test_runs_after_edit(self.streams(evs)), want, (out, err))

    def test_denied_and_subagent_test_runs_do_not_count(self):
        evs = edit("e", "/w/kv.go") + bash("t1", "go test ./...", "Permission denied", True) + \
            bash("t2", "go test ./...", "--- FAIL: TestX", True, parent="toolu_agent") + bash("t3", "go test ./...")
        evs.append({"type": "result", "permission_denials": [{"tool_use_id": "t1"}]})
        self.assertEqual(measures.test_runs_after_edit(self.streams(evs)), (1, 0))

    def test_bash_writes_start_the_count(self):
        go = bash("t9", "go test ./...")
        py = "python3 - <<'EOF'\np='server.go'\ns=open(p).read()\nopen(p,'w').write(s.replace('a','b'))\nEOF"
        cases = [(py, 1), ("sed -i '' 's/a/b/' server.go", 1), ("sed -E -i 's/a/b/' kv.go", 1),
                 ("perl -pi -e 's/a/b/' kv.go", 1), ("cat new.txt > kv.go", 1), ("echo x | tee -a kv.go", 1),
                 ("sed -i 's/a/b/' kv_test.go", 0), ("cat server.go", 0), ("grep -n write server.go", 0),
                 ("sed -n 1,20p server.go", 0), ("go test ./... > out.txt", 0)]
        for command, want in cases:
            self.assertEqual(measures.test_runs_after_edit(self.streams(bash("e", command) + go))[0], want, command)
        # a write that failed does not start it; a write and a test in one command counts the test
        self.assertEqual(measures.test_runs_after_edit(self.streams(bash("e", py, "Traceback", True) + go))[0], 0)
        self.assertEqual(measures.test_runs_after_edit(self.streams(bash("e", "sed -i 's/a/b/' kv.go && go test ./..."))),
                         (1, 0))

    def test_undo_checks_are_counted_apart(self):
        fail = ("--- FAIL: TestKVSeed1", True)
        one = edit("e", "/w/kv.go") + bash("u", "git stash -q && go test -run TestKVSeed1 .; git stash pop", *fail) + \
            bash("t", "go test ./...")
        self.assertEqual(measures.test_runs_after_edit(self.streams(one)), (1, 0))
        self.assertEqual(measures.undo_check_runs(self.streams(one)), 1)
        split = edit("e", "/w/kv.go") + bash("s", "git stash push kv.go") + bash("u", "go test .", *fail) + \
            bash("p", "git stash pop") + bash("t", "go test .", *fail)
        self.assertEqual(measures.test_runs_after_edit(self.streams(split)), (1, 1))
        self.assertEqual(measures.undo_check_runs(self.streams(split)), 1)
        for start in ["git checkout -- kv.go", "git checkout HEAD -- kv.go", "git restore kv.go",
                      "git restore --source=HEAD kv.go", "git stash", "git stash save x"]:
            evs = edit("e", "/w/kv.go") + bash("s", start) + bash("u", "go test .", *fail)
            self.assertEqual(measures.undo_check_runs(self.streams(evs)), 1, start)
        # an edit after the undo puts the change back; a stash list is not an undo
        back = edit("e", "/w/kv.go") + bash("s", "git checkout -- kv.go") + edit("f", "/w/kv.go") + \
            bash("t", "go test .", *fail)
        self.assertEqual((measures.test_runs_after_edit(self.streams(back)), measures.undo_check_runs(self.streams(back))),
                         ((1, 1), 0))
        listing = edit("e", "/w/kv.go") + bash("s", "git stash list") + bash("t", "go test .", *fail)
        self.assertEqual(measures.undo_check_runs(self.streams(listing)), 0)

    def test_go_test_in_any_form(self):
        yes = ["go test ./...", "cd /w && go test .", "FAULTLINE_SEED=0x1 go test -run X .", "go  test",
               "/usr/local/go/bin/go test ./...", "go -C sub test ./...", "bash -c 'go test ./...'",
               'sh -c "go test"', "go vet . && go test .", "(go test ./...)", "x;go test", "x|go test"]
        no = ["go vet ./...", "gotestsum ./...", "go tests", "cargo test", "mongo test", "go build ./...",
              "echo ok"]
        for c in yes:
            self.assertTrue(measures.GO_TEST.search(c), c)
        for c in no:
            self.assertFalse(measures.GO_TEST.search(c), c)


class ReportUpgradeTest(unittest.TestCase):
    """report on run.json files written before the change (the pilot's): tokens from model_usage,
    test runs from the streams, the transcripts' figure as the cross-check."""

    def setUp(self):
        self.tmp = os.path.realpath(tempfile.mkdtemp(prefix="bench-report-"))
        self.addCleanup(common.rmtree, self.tmp)
        shutil.copytree(PILOT, os.path.join(self.tmp, "runs"))
        self.cfg = common.load_config(os.path.join(AGENTS, "tier1", "config.json"))
        self.cfg["paths"].update(runs=os.path.join(self.tmp, "runs"), results=os.path.join(self.tmp, "results"))

    def test_old_pilot_records(self):
        report.write(self.cfg)
        with open(os.path.join(self.tmp, "results", "pilot.csv")) as f:
            rows = {r["run_id"]: r for r in csv.DictReader(f)}
        b1 = rows["pilot-b1-plugin-1"]
        self.assertEqual((b1["weighted_tokens"], b1["processed_tokens"], b1["output_tokens"]), ("157464", "608870", "9319"))
        self.assertEqual(b1["weighted_tokens_transcripts"], "145905")
        b3 = rows["pilot-b3-plugin-1"]
        self.assertEqual((b3["test_runs_after_edit"], b3["failing_test_runs_after_edit"]), ("2", "1"))
        b3p = rows["pilot-b3-plain-1"]
        self.assertEqual((b3p["test_runs_after_edit"], b3p["failing_test_runs_after_edit"]), ("0", "0"))
        self.assertEqual(b3p["weighted_tokens_transcripts"], "73120")
        page = common.read_text(os.path.join(self.tmp, "results", "pilot.md"))
        self.assertTrue(page.startswith("# leasekv results, tier 1: pilot\n"), page[:80])
        self.assertIn("faultline: replace:local-export.", page)
        self.assertIn("| b3 | plugin | 1 of 1 | $0.81 ($0.81 to $0.81) | 0.2M (0.2M to 0.2M) | 2.6 (2.6 to 2.6) "
                      "| 1 (1 to 1) | 2 (2 to 2) | 1 (1 to 1) |", page)
        self.assertIn("| b3 | plain | 1 of 1 |", page)
        # every run: go test, tests after edit, failing, regression test, visible checks
        self.assertIn("| pilot-b3-plugin-1 | yes | 1 | 0 | $0.81 | 0.2M | 13557 | 33 | 2.6 | 4 | 2 | 1 | 0 | none | none |", page)
        self.assertIn("| 0 (0 to 0) | 0 (0 to 0) |", page)
        self.assertIn("- pilot-b1-plugin-1: modelUsage 157464, transcripts 145905 (7.9 %)", page)
        self.assertNotIn("- pilot-b3-plugin-1:", page)
        self.assertNotIn("- pilot-b3-plain-1:", page)
        # the run folders are only read
        self.assertEqual(sorted(os.listdir(os.path.join(self.tmp, "runs", "pilot-b3-plugin-1"))),
                         ["run.json", "stream-r1.jsonl", "transcripts"])

    def test_a_new_record_is_left_as_written(self):
        r = {"set": "pilot", "weighted_tokens": 100, "weighted_tokens_transcripts": None, "model_usage": {
            "m": {"outputTokens": 1000}}, "test_runs_after_edit": None, "status": "done"}
        self.assertEqual(report.upgrade(r, os.path.join(PILOT, "pilot-b3-plugin-1")), r)
        self.assertIsNone(report.token_gap(r))

    def test_an_old_record_without_usage_or_streams(self):
        r = report.upgrade({"set": "pilot", "status": "error", "weighted_tokens": 5}, self.tmp)
        self.assertEqual((r["weighted_tokens"], r["weighted_tokens_transcripts"], r.get("test_runs_after_edit")),
                         (None, 5, None))
        r = report.upgrade({"set": "pilot", "status": "error"}, os.path.join(PILOT, "pilot-b3-plugin-1"))
        self.assertIsNone(r.get("test_runs_after_edit"))

    def test_token_gap(self):
        self.assertAlmostEqual(report.token_gap({"weighted_tokens": 102, "weighted_tokens_transcripts": 100}), 2.0)
        self.assertAlmostEqual(report.token_gap({"weighted_tokens": 97, "weighted_tokens_transcripts": 100}), 3.0)
        self.assertIsNone(report.token_gap({"weighted_tokens": 97, "weighted_tokens_transcripts": 0}))
        self.assertIsNone(report.token_gap({"weighted_tokens": 97}))

    def test_the_page_lists_gaps_over_2_percent_only(self):
        rows = [{"run_id": "r-%d" % w, "bug": "b1", "arm": "plain", "status": "done", "weighted_tokens": w,
                 "weighted_tokens_transcripts": 1000} for w in (1020, 979, 1000)]
        page = report.page(self.cfg, "x", rows)
        self.assertIn("- r-979: modelUsage 979, transcripts 1000 (2.1 %)", page)
        self.assertNotIn("- r-1020:", page)
        self.assertNotIn("- r-1000:", page)
        self.assertNotIn("- none", page)
        self.assertIn("- none", report.page(self.cfg, "x", rows[2:]))

    def test_round_streams_in_round_order(self):
        for n in (10, 2, 1):
            common.write_text(os.path.join(self.tmp, "stream-r%d.jsonl" % n), "")
        self.assertEqual([os.path.basename(f) for f in report._round_streams(self.tmp)],
                         ["stream-r1.jsonl", "stream-r2.jsonl", "stream-r10.jsonl"])


if __name__ == "__main__":
    unittest.main()
