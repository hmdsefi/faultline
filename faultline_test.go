// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/artifact"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
)

// pkgPath is the import path of package faultline, as reports and results lines name it.
const pkgPath = "github.com/hmdsefi/faultline"

// runResults runs scenario name with FAULTLINE_RESULTS set and returns the output, the exit code
// and the results lines (none when the file was not written).
func runResults(t *testing.T, name string, env ...string) (string, int, []map[string]any) {
	t.Helper()
	results := filepath.Join(t.TempDir(), "r.jsonl")
	o, code := runScenario(t, name, append(env, "FAULTLINE_RESULTS="+results))
	var lines []map[string]any
	if _, err := os.Stat(results); err == nil {
		lines = resultLines(t, results)
	}
	return o, code, lines
}

// anyValue as a wanted value in checkLine accepts any value of a present field.
type anyValue struct{}

// checkLine reports each field of results line l whose value differs from the one kv gives. kv
// holds key and value pairs; a nil value means the field is absent. Numbers decode as float64.
func checkLine(t *testing.T, label string, l map[string]any, kv ...any) {
	t.Helper()
	for i := 0; i+1 < len(kv); i += 2 {
		key, _ := kv[i].(string)
		got, ok := l[key]
		want := kv[i+1]
		if _, anything := want.(anyValue); anything && ok {
			continue
		}
		if (want == nil) == ok || ok && got != want {
			t.Errorf("%s: results field %q is %#v (present: %v), want %#v", label, key, got, ok, want)
		}
	}
}

var _ = scenario("nil-body", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Seeds: 1}, nil)
	outLine(t, "after Run")
})

var _ = scenario("run-then-out", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Seeds: 1, Duration: time.Second}, func(w *faultline.World) { addTicker(w) })
	outLine(t, "after Run")
})

// API-001, API-004, API-087: a parent-level setup error is reported with t.Fatalf at the user's
// Run call, so the parent test stops there; no seed starts, and the results file gets one setup
// line. An error writing that line is reported at the Run call too.
func TestParentSetupError(t *testing.T) {
	cases := []struct {
		scenario string
		env      []string
		msg      string
	}{
		{"nil-body", nil, "faultline: Run: body is nil"},
		{"run-then-out", []string{"FAULTLINE_TRACE=x"}, `faultline: invalid FAULTLINE_TRACE value "x": want hash or full`},
	}
	for _, c := range cases {
		out := filepath.Join(t.TempDir(), "out")
		o, code, lines := runResults(t, c.scenario, append(c.env, "OUT="+out)...)
		at := regexp.MustCompile(`\sfaultline_test\.go:\d+: ` + regexp.QuoteMeta(c.msg) + "\n")
		if code != 1 || !at.MatchString(o) || strings.Contains(o, "seed=") {
			t.Errorf("%s: exit %d; want 1, the error at the Run call and no seed subtest\n%s", c.scenario, code, o)
		}
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Errorf("%s: the parent test went on after the setup error", c.scenario)
		}
		if len(lines) != 1 {
			t.Errorf("%s: results %v", c.scenario, lines)
			continue
		}
		checkLine(t, c.scenario, lines[0], "seed", "", "index", -1.0, "status", "fail", "kind", "setup",
			"signature", "setup:", "message", c.msg, "at_ns", nil, "events", nil, "wall_ns", 0.0)
	}
	dir := t.TempDir()
	o, code := runScenario(t, "nil-body", []string{"FAULTLINE_RESULTS=" + dir})
	at := regexp.MustCompile(`\sfaultline_test\.go:\d+: faultline: FAULTLINE_RESULTS: open ` + regexp.QuoteMeta(dir) + ": is a directory\n")
	if code != 1 || !at.MatchString(o) || !strings.Contains(o, "faultline: Run: body is nil\n") {
		t.Errorf("FAULTLINE_RESULTS is a directory: exit %d; want 1 and the error at the Run call\n%s", code, o)
	}
}

// API-074, API-075, API-076, API-087: the failure of the harness scenario's seed 1 (MaxEvents),
// field by field: report.json, the trace header, report.txt, the console text and both results
// lines.
func TestFailureArtifact(t *testing.T) {
	root := t.TempDir()
	results := filepath.Join(t.TempDir(), "r.jsonl")
	o, code := runScenario(t, "harness", []string{"OUT=" + filepath.Join(t.TempDir(), "out"), "FAULTLINE_ARTIFACTS=" + root, "FAULTLINE_RESULTS=" + results})
	if code != 1 || strings.Contains(o, "no artifacts were written") {
		t.Fatalf("exit %d; want 1 and no Goexit note\n%s", code, o)
	}
	s1 := fmt.Sprintf("0x%016x", seedK(1))
	dir := seedDir(root, seedK(1))
	a, err := artifact.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	rep, hdr, recs := a.Report, a.Trace.Header, a.Trace.Records
	if len(recs) == 0 {
		t.Fatal("trace.jsonl has no records")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	lines := resultLines(t, results)
	if len(lines) != 2 {
		t.Fatalf("results %v", lines)
	}
	for _, c := range []struct {
		field     string
		got, want any
	}{
		{"status", rep.Status, "fail"},
		{"package", rep.Package, pkgPath},
		{"test", rep.Test, "TestScenario"},
		{"subtest", rep.Subtest, "TestScenario/seed=" + s1},
		{"seed", rep.Seed, s1},
		{"seed_source", rep.SeedSource, "derived"},
		{"seed_index", rep.SeedIndex, 1},
		{"base_seed", rep.BaseSeed, fmt.Sprintf("0x%016x", faultline.NameBase("TestScenario"))},
		{"base_source", rep.BaseSource, "test_name"},
		{"replay.package_dir", rep.Replay.PackageDir, cwd},
		{"versions.go", rep.Versions.Go, runtime.Version()},
		{"versions.goos", rep.Versions.GOOS, runtime.GOOS},
		{"versions.goarch", rep.Versions.GOARCH, runtime.GOARCH},
		{"options.seeds", rep.Options.Seeds, 2},
		{"options.duration", rep.Options.Duration, "1s"},
		{"options.max_events", rep.Options.MaxEvents, uint64(1000)},
		{"options.mode", rep.Options.Mode, "event"},
		{"run.trace_hash", rep.Run.TraceHash, lines[1]["trace_hash"]},
		{"run.events", rep.Run.Events, uint64(1000)},
		{"run.records", rep.Run.Records, recs[len(recs)-1].Seq},
		{"run.dropped", rep.Run.Dropped, uint64(0)},
		{"run.end_ns", rep.Run.EndNS, int64(0)},
		{"run.end", rep.Run.End, "0.000000000s"},
		{"run.stop", rep.Run.Stop, "max-events"},
		{"run.attempts", rep.Run.Attempts, 2},
		{"len(nodes)", len(rep.Nodes), 1},
		{"trace header package", hdr.Package, pkgPath},
		{"trace header test", hdr.Test, "TestScenario"},
		{"trace header subtest", hdr.Subtest, rep.Subtest},
		{"trace header seed", hdr.Seed, s1},
		{"trace header trace_hash", hdr.TraceHash, rep.Run.TraceHash},
		{"trace header go_version", hdr.GoVersion, runtime.Version()},
		{"trace header faultline_version", hdr.FaultlineVersion, rep.Versions.Faultline},
		{"trace header nodes", fmt.Sprint(hdr.Nodes), fmt.Sprint(rep.Nodes)},
		{"schedule.json written", a.Schedule != nil, true},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.field, c.got, c.want)
		}
	}
	if len(rep.Nodes) == 1 && (rep.Nodes[0].ID != 1 || rep.Nodes[0].Name != "n1" || !slices.Equal(rep.Nodes[0].Tags, []string{"server"})) {
		t.Errorf("nodes %+v", rep.Nodes)
	}
	if rep.Versions.Faultline == "" || !regexp.MustCompile(`^0x[0-9a-f]{16}$`).MatchString(rep.OptionsHash) {
		t.Errorf("versions.faultline %q, options_hash %q", rep.Versions.Faultline, rep.OptionsHash)
	}
	if !strings.HasPrefix(rep.Replay.Command, "FAULTLINE_SEED="+s1+" go test") || !strings.HasSuffix(rep.Replay.Command, " -run '^TestScenario$' "+pkgPath) {
		t.Errorf("replay.command %q", rep.Replay.Command)
	}

	headline := "faultline: run exceeded MaxEvents (1000) at t=0.000000000s (event 1000)"
	message := "likely a livelock or a runaway timer; raise Options.MaxEvents or set Options.AllowLimit"
	artifacts := "artifacts: " + dir + string(os.PathSeparator)
	mustContainInOrder(t, o, headline+"\n", "  "+message+"\n", "replay:    "+rep.Replay.Command+"\n", artifacts+"\n")
	text := "--- FAIL: TestScenario/seed=" + s1 + "\n" +
		"    " + headline + "\n" +
		"      " + message + "\n" +
		"    replay:    " + rep.Replay.Command + "\n" +
		"    " + artifacts + "\n"
	if a.Text != text {
		t.Errorf("report.txt\n%q\nwant\n%q", a.Text, text)
	}

	checkLine(t, "pass line", lines[0], "status", "pass", "kind", nil, "message", nil, "at_ns", nil,
		"events", anyValue{}, "trace_hash", anyValue{}, "artifact", nil)
	checkLine(t, "fail line", lines[1], "seed", s1, "index", 1.0, "status", "fail", "kind", "limit",
		"check", "max-events", "signature", "limit:max-events", "message", message, "at_ns", 0.0,
		"at", "0.000000000s", "events", 1000.0, "trace_hash", anyValue{}, "artifact", dir, "artifact_error", nil)
	// The results file is opened with mode 0o644; the umask narrows it as it narrows ref's.
	ref, err := os.OpenFile(filepath.Join(t.TempDir(), "ref"), os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // the mode under test
	if err != nil {
		t.Fatal(err)
	}
	if err := ref.Close(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(results)
	if err != nil {
		t.Fatal(err)
	}
	refInfo, err := os.Stat(ref.Name())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != refInfo.Mode().Perm() {
		t.Errorf("results file mode %v, want %v", fi.Mode().Perm(), refInfo.Mode().Perm())
	}
}

var _ = scenario("recovery-history", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Seeds: 1, Duration: 8 * time.Second}, func(w *faultline.World) {
		addTicker(w)
		w.Plan(&fault.Random{Rules: []fault.Rule{{Kind: fault.KindPause, Every: time.Hour}}})
		w.History.Invoke("c1", "read", nil)
		w.Invariant("always", func() error { return errors.New("bad") })
	})
})

// API-074: run.planners, run.recovery, schedule.json's end and recovery, and history.jsonl come
// from the artifact attempt.
func TestArtifactRecoveryAndHistory(t *testing.T) {
	root := t.TempDir()
	o, code := runScenario(t, "recovery-history", []string{"FAULTLINE_ARTIFACTS=" + root})
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, o)
	}
	a, err := artifact.Read(seedDir(root, seedK(0)))
	if err != nil {
		t.Fatal(err)
	}
	run := a.Report.Run
	if run.Recovery != "6.000000000s" || run.RecoveryNS != int64(6*time.Second) || !slices.Equal(run.Planners, []string{"random"}) {
		t.Errorf("run %+v", run)
	}
	if a.Schedule == nil || a.Schedule.End != kernel.Time(8*time.Second) || a.Schedule.Recovery != kernel.Time(6*time.Second) {
		t.Errorf("schedule.json %+v", a.Schedule)
	}
	if len(a.History) == 0 {
		t.Error("no history.jsonl")
	}
}

// mismatchCalls counts the body calls of a child process; logging it makes each attempt's trace
// differ from the others.
var mismatchCalls int

var _ = scenario("rerun-mismatch", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Seeds: 1, Duration: time.Second}, func(w *faultline.World) {
		mismatchCalls++
		addTicker(w)
		w.Logf("call %d", mismatchCalls)
		w.Invariant("always", func() error { return errors.New("bad") })
	})
})

var _ = scenario("check-mismatch", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Seeds: 1, Duration: time.Second, CheckDeterminism: true}, func(w *faultline.World) {
		mismatchCalls++
		addTicker(w)
		w.Logf("call %d", mismatchCalls)
	})
})

var _ = scenario("bad-invariant", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Seeds: 1, Duration: time.Second}, func(w *faultline.World) {
		addTicker(w)
		w.Invariant("always", func() error { return errors.New("bad") })
	})
})

// API-070: the attempts after the primary one. A mismatch in the artifact re-run takes a third
// attempt, keeps A1's failure as the original and A2 as the artifact attempt; a CheckDeterminism
// mismatch takes two full-trace attempts, diffs them and keeps A3; and re-runs replay
// FAULTLINE_SCHEDULE too, or their trace would differ from A1's.
func TestReruns(t *testing.T) {
	sched := filepath.Join(t.TempDir(), "schedule.json")
	f, err := os.Create(sched)
	if err != nil {
		t.Fatal(err)
	}
	if err := (fault.Schedule{Version: 1}).Write(f); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		scenario string
		env      []string
		context  string    // of the determinism failure; "" when the seed keeps A1's failure
		original string    // signature of the determinism failure's original failure, if any
		attempts int       // also the number of hashes of a determinism failure
		art      int       // the artifact attempt's index in the hashes
		diff     [2]string // texts of the first differing records
	}{
		{"rerun-mismatch", nil, "artifact_rerun", "invariant:always", 3, 1, [2]string{"call 2", "call 3"}},
		{"check-mismatch", nil, "check_determinism", "", 4, 2, [2]string{"call 3", "call 4"}},
		{"bad-invariant", []string{"FAULTLINE_SCHEDULE=" + sched}, "", "", 2, 0, [2]string{}},
	}
	for _, c := range cases {
		root := t.TempDir()
		o, code := runScenario(t, c.scenario, append(c.env, "FAULTLINE_ARTIFACTS="+root))
		if code != 1 {
			t.Errorf("%s: exit %d\n%s", c.scenario, code, o)
			continue
		}
		a, err := artifact.Read(seedDir(root, seedK(0)))
		if err != nil {
			t.Fatal(err)
		}
		rf, run := a.Report.Failure, a.Report.Run
		if run.Attempts != c.attempts {
			t.Errorf("%s: run.attempts %d, want %d", c.scenario, run.Attempts, c.attempts)
		}
		if c.context == "" {
			if rf.Signature != "invariant:always" || rf.Determinism != nil {
				t.Errorf("%s: failure %+v", c.scenario, rf)
			}
			continue
		}
		d := rf.Determinism
		if rf.Signature != "determinism:" || d == nil || d.Context != c.context || len(d.Hashes) != c.attempts {
			t.Errorf("%s: failure %+v, determinism %+v", c.scenario, rf, d)
			continue
		}
		if (d.Original == nil) != (c.original == "") || d.Original != nil && d.Original.Signature != c.original {
			t.Errorf("%s: original failure %+v, want signature %q", c.scenario, d.Original, c.original)
		}
		if run.TraceHash != d.Hashes[c.art] {
			t.Errorf("%s: run.trace_hash %s, want hash %d of %v", c.scenario, run.TraceHash, c.art, d.Hashes)
		}
		if d.Diff == nil || d.Diff.A == nil || d.Diff.B == nil || d.Diff.A.Text != c.diff[0] || d.Diff.B.Text != c.diff[1] {
			t.Errorf("%s: diff %+v, want %q and %q", c.scenario, d.Diff, c.diff[0], c.diff[1])
		}
		logged := fmt.Sprintf("call %d", c.art+1)
		if !slices.ContainsFunc(a.Trace.Records, func(r kernel.Record) bool { return r.Kind == "kernel.log" && r.Text == logged }) {
			t.Errorf("%s: the artifact's trace has no %q record", c.scenario, logged)
		}
	}
}

// raceEnabled reports whether this test binary was built with -race: its build settings then hold
// -race=true (API-080).
func raceEnabled() bool {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, s := range bi.Settings {
		if s.Key == "-race" {
			return s.Value == "true"
		}
	}
	return false
}

var _ = scenario("spin-ring", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Seeds: 1, Duration: time.Second, MaxEvents: 1_000_100}, func(w *faultline.World) { addSpin(w) })
})

// API-070: the artifact re-run of a limit failure keeps its last 1,000,000 records. The child
// holds them all: about 500 MB and 2 s, or 1.8 GB and 45 s under the race detector, so the test
// does not run with -race or -short.
func TestLimitRing(t *testing.T) {
	if testing.Short() || raceEnabled() {
		t.Skip("needs about 500 MB, and 1.8 GB under the race detector")
	}
	root := t.TempDir()
	o, code := runScenario(t, "spin-ring", []string{"FAULTLINE_ARTIFACTS=" + root})
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, o)
	}
	f, err := os.Open(filepath.Join(seedDir(root, seedK(0)), artifact.FileReport))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rep, err := artifact.ReadReport(f)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Run.Dropped == 0 || rep.Run.Records-rep.Run.Dropped != 1_000_000 {
		t.Fatalf("run.records %d, run.dropped %d; want 1,000,000 records kept", rep.Run.Records, rep.Run.Dropped)
	}
}

// goexitCalls counts the body calls of each seed of a child process.
var goexitCalls = map[uint64]int{}

// Scenario goexit ends each seed through Goexit as GOEXIT says: "<how>" in the primary attempt,
// "<how>-rerun" in the artifact re-run after invariant "tick 5" failed, "<how>-check" in the
// CheckDeterminism attempt after the primary attempt passed, "<how>-diag" in the first diagnostic
// attempt after a CheckDeterminism mismatch, or "<how>-rediag" in the diagnostic attempt after an
// artifact re-run mismatch ("-diag" and "-rediag" log each call's number, so every attempt's trace
// differs). how is skip (w.T().Skip in body), setup (a misuse panic in body, a setup error), body
// (w.T().Fatalf in body, before the first event), tick (w.T().Fatalf in the 3rd tick), tickskip
// (w.T().Skip in the 3rd tick) or errorskip (w.T().Errorf, then w.T().Skip, in the 3rd tick).
var _ = scenario("goexit", func(t *testing.T) {
	how, where, _ := strings.Cut(os.Getenv("GOEXIT"), "-")
	stop := map[string]int{"": 1, "rerun": 2, "check": 2, "diag": 3, "rediag": 3}[where] // the body call that stops
	faultline.Run(t, faultline.Options{Seeds: 2, Duration: time.Second, CheckDeterminism: where == "check" || where == "diag"}, func(w *faultline.World) {
		goexitCalls[w.Seed()]++
		tk := addTicker(w)
		if where == "rerun" || where == "rediag" {
			w.Invariant("tick 5", func() error {
				if tk.count == 5 {
					return errors.New("five")
				}
				return nil
			})
		}
		if where == "diag" || where == "rediag" {
			w.Logf("call %d", goexitCalls[w.Seed()])
		}
		if goexitCalls[w.Seed()] != stop {
			return
		}
		switch how {
		case "skip":
			w.T().Skip("skip")
		case "setup":
			w.RunFor(-time.Second)
		case "body":
			w.T().Fatalf("stop")
		case "tick":
			tk.onTick = func(n *kernel.Node, c int) {
				if c == 3 {
					w.T().Fatalf("stop")
				}
			}
		case "tickskip":
			tk.onTick = func(n *kernel.Node, c int) {
				if c == 3 {
					w.T().Skip("skip")
				}
			}
		case "errorskip":
			tk.onTick = func(n *kernel.Node, c int) {
				if c == 3 {
					w.T().Errorf("soft")
					w.T().Skip("skip")
				}
			}
		}
	})
})

// API-101, API-072, API-087, API-021: every Goexit case in the primary attempt and in each later
// attempt. In the primary attempt API-101's cases apply in order, and a skip after t.Errorf is not
// a skip. A Goexit in a later attempt, a skip and a setup error included, keeps the seed's outcome:
// the primary attempt's failure after the artifact re-run, the determinism failure in a diagnostic
// attempt. In the check attempt after a passing primary attempt it is a determinism failure. The
// console then shows that failure, then a note that names the attempt. events and trace_hash are
// written only when the primary attempt completed. A failing seed stops the seed loop; a skipped
// one does not. Goexit never writes artifacts.
func TestGoexitOutcomes(t *testing.T) {
	s0 := fmt.Sprintf("0x%016x", seedK(0))
	stopped := "stopped by t.FailNow, t.Fatal or t.SkipNow"
	misuse := "faultline: World.RunFor: negative duration -1s"
	fatal, skip, setup := "t.FailNow or t.Fatal", "t.SkipNow or t.Skip", "a setup error"
	tick3, early := " at t=0.030000000s (event 4)", " before its first event"
	replay := "replay:    FAULTLINE_SEED=" + s0 + " go test"
	// primary is API-101's note.
	primary := func(how string) []string {
		return []string{"faultline: seed " + s0 + " stopped by " + how + tick3 + "; no artifacts were written",
			"  report failures with an Invariant, a Final check or w.Sim.Fail(err) to get a replayable report", replay}
	}
	// later is the seed's failure, then API-072's note.
	later := func(attempt, stop string, failure ...string) []string {
		return append(failure, "faultline: the "+attempt+" of seed "+s0+" stopped by "+stop+"; no artifacts were written",
			"  the first run did not stop, so the test depends on something outside the seed; the failure above is the seed's outcome", replay)
	}
	tick5 := []string{`faultline: invariant "tick 5" violated at t=0.050000000s (event 6)`, "  five"}
	causes := "  common causes: state kept between runs in the same process (package-level variables, sync.Once, caches), map iteration order, global math/rand, wall-clock time, goroutines"
	// check is the determinism failure of a stop in the check attempt, diag that of a stop in a
	// diagnostic attempt after a CheckDeterminism mismatch, and rediag after an artifact re-run
	// mismatch. HASH stands for any trace hash.
	check := func(stop, at string) []string {
		return later("determinism check", stop, "faultline: determinism failure at t="+at,
			"  the determinism check stopped by "+stop+"; the first run passed with trace hash HASH", causes)
	}
	diag := func(stop string) []string {
		return later("diagnostic re-run", stop, "faultline: determinism failure: two runs of seed "+s0+" gave trace hashes HASH and HASH",
			"  the diagnostic re-run stopped by "+stop+", so the first differing record was not found", causes)
	}
	rediag := func(stop string) []string {
		return later("diagnostic re-run", stop, "faultline: determinism failure: re-running seed "+s0+" gave trace hash HASH; the first run gave HASH",
			"  the first run failed: "+tick5[0][len("faultline: "):],
			"  the diagnostic re-run stopped by "+stop+", so the first differing record was not found", causes)
	}
	cases := []struct {
		goexit         string
		code, lines    int
		status         string
		signature, msg any      // nil: absent; anyValue{}: the message the console shows
		atNS           any      // nil: absent
		console        []string // consecutive lines of the seed's output; nil: no note
		completed      bool     // events and trace_hash present
	}{
		{"skip", 0, 2, "skip", nil, nil, nil, nil, false},
		{"setup", 1, 1, "fail", "setup:", misuse, nil, nil, false},
		{"body", 1, 1, "fail", "setup:", stopped, nil, nil, false},
		{"tick", 1, 1, "fail", "fail:", stopped, 3e7, primary(fatal), false},
		{"errorskip", 1, 1, "fail", "fail:", stopped, 3e7, primary(skip), false},
		{"skip-rerun", 1, 1, "fail", "invariant:tick 5", "five", 5e7, later("artifact re-run", skip+early, tick5...), true},
		{"setup-rerun", 1, 1, "fail", "invariant:tick 5", "five", 5e7, later("artifact re-run", setup+early, tick5...), true},
		{"body-rerun", 1, 1, "fail", "invariant:tick 5", "five", 5e7, later("artifact re-run", fatal+early, tick5...), true},
		{"tick-rerun", 1, 1, "fail", "invariant:tick 5", "five", 5e7, later("artifact re-run", fatal+tick3, tick5...), true},
		{"tickskip-rerun", 1, 1, "fail", "invariant:tick 5", "five", 5e7, later("artifact re-run", skip+tick3, tick5...), true},
		{"skip-check", 1, 1, "fail", "determinism:", anyValue{}, 0.0, check(skip+early, "0.000000000s"), true},
		{"setup-check", 1, 1, "fail", "determinism:", anyValue{}, 0.0, check(setup+early, "0.000000000s"), true},
		{"tick-check", 1, 1, "fail", "determinism:", anyValue{}, 3e7, check(fatal+tick3, "0.030000000s"), true},
		{"skip-diag", 1, 1, "fail", "determinism:", anyValue{}, 0.0, diag(skip + early), true},
		{"tickskip-diag", 1, 1, "fail", "determinism:", anyValue{}, 3e7, diag(skip + tick3), true},
		{"tick-rediag", 1, 1, "fail", "determinism:", anyValue{}, 3e7, rediag(fatal + tick3), true},
	}
	for _, c := range cases {
		root := t.TempDir()
		o, code, lines := runResults(t, "goexit", "GOEXIT="+c.goexit, "FAULTLINE_ARTIFACTS="+root)
		note := strings.Contains(o, "no artifacts were written")
		verdict := fmt.Sprintf("--- %s: TestScenario/seed=%s (", strings.ToUpper(c.status), s0)
		console := strings.ReplaceAll(regexp.QuoteMeta(strings.Join(c.console, "\n    ")), "HASH", "0x[0-9a-f]{16}")
		if code != c.code || len(lines) != c.lines || note != (c.console != nil) || !regexp.MustCompile(console).MatchString(o) || !strings.Contains(o, verdict) {
			t.Errorf("%s: exit %d, %d results lines, note %v; want %d, %d, these lines and %q:\n%s\n\n%s", c.goexit, code, len(lines), note, c.code, c.lines, strings.Join(c.console, "\n"), verdict, o)
			continue
		}
		var completed any
		if c.completed {
			completed = anyValue{}
		}
		for _, l := range lines {
			checkLine(t, c.goexit, l, "status", c.status, "signature", c.signature, "message", c.msg, "at_ns", c.atNS,
				"events", completed, "trace_hash", completed, "artifact", nil)
		}
		if msg, ok := lines[0]["message"].(string); ok && c.msg == (anyValue{}) && !strings.Contains(o, "\n      "+strings.ReplaceAll(msg, "\n", "\n      ")+"\n") {
			t.Errorf("%s: the console lacks the results line's message %q\n%s", c.goexit, msg, o)
		}
		if _, err := os.Stat(seedDir(root, seedK(0))); !os.IsNotExist(err) {
			t.Errorf("%s: artifacts written", c.goexit)
		}
	}
}

var _ = scenario("empty-error", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Seeds: 1, Duration: time.Second}, func(w *faultline.World) {
		addTicker(w)
		w.Invariant("empty", func() error { return errors.New("") })
	})
})

// API-087: every fail line has message, also when it is empty; an error writing the results file
// is reported on the seed.
func TestResultsFile(t *testing.T) {
	o, code, lines := runResults(t, "empty-error")
	if code != 1 || len(lines) != 1 {
		t.Fatalf("exit %d, results %v\n%s", code, lines, o)
	}
	checkLine(t, "empty error", lines[0], "status", "fail", "kind", "invariant", "check", "empty",
		"signature", "invariant:empty", "message", "")
	dir := t.TempDir()
	o, code = runScenario(t, "empty-error", []string{"FAULTLINE_RESULTS=" + dir})
	if code != 1 || !strings.Contains(o, "faultline: FAULTLINE_RESULTS: open "+dir+": is a directory\n") {
		t.Fatalf("FAULTLINE_RESULTS is a directory: exit %d\n%s", code, o)
	}
}

// API-074: where a failure artifact goes. The default root is faultline under the temporary
// directory; when the seed's folder holds another test's artifact it is AltDir, which the
// console, report.txt and the results line name; a write error is printed and goes to the
// results line's artifact_error.
func TestArtifactLocation(t *testing.T) {
	tmp, root, file := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "file")
	other := &artifact.Artifact{Report: artifact.Report{Package: pkgPath, Test: "Testscenario", Seed: fmt.Sprintf("0x%016x", seedK(0)), Status: "pass"}, Trace: &artifact.Trace{}}
	if err := artifact.Write(seedDir(root, seedK(0)), other); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		env  []string
		dir  string // "" when the write fails
	}{
		{"default root", []string{"FAULTLINE_ARTIFACTS=", "TMPDIR=" + tmp}, artifact.Dir(filepath.Join(tmp, "faultline"), pkgPath, "TestScenario", seedK(0))},
		{"folder of another test", []string{"FAULTLINE_ARTIFACTS=" + root}, artifact.AltDir(root, pkgPath, "TestScenario", seedK(0))},
		{"write error", []string{"FAULTLINE_ARTIFACTS=" + file}, ""},
	}
	for _, c := range cases {
		o, code, lines := runResults(t, "bad-invariant", c.env...)
		if code != 1 || len(lines) != 1 {
			t.Errorf("%s: exit %d, results %v\n%s", c.name, code, lines, o)
			continue
		}
		if c.dir == "" {
			if !strings.Contains(o, "artifacts: not written: artifact: ") {
				t.Errorf("%s: no artifacts: not written line\n%s", c.name, o)
			}
			checkLine(t, c.name, lines[0], "artifact", nil, "artifact_error", anyValue{})
			continue
		}
		line := "artifacts: " + c.dir + string(os.PathSeparator) + "\n"
		if !strings.Contains(o, line) {
			t.Errorf("%s: output lacks %q\n%s", c.name, line, o)
		}
		a, err := artifact.Read(c.dir)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if a.Report.Test != "TestScenario" || !strings.HasSuffix(a.Text, "    "+line) {
			t.Errorf("%s: test %q, report.txt\n%s", c.name, a.Report.Test, a.Text)
		}
		checkLine(t, c.name, lines[0], "artifact", c.dir, "artifact_error", nil)
	}
}

// panicDeep panics d calls further down, so that the stack has more than 40 lines.
func panicDeep(d int) {
	if d == 0 {
		panic("deep")
	}
	panicDeep(d - 1)
}

var _ = scenario("deep-panic", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Seeds: 1, Duration: time.Second}, func(w *faultline.World) {
		tk := addTicker(w)
		tk.onTick = func(n *kernel.Node, c int) {
			if c == 3 {
				panicDeep(30)
			}
		}
	})
})

// API-075, API-076: the console prints the first 40 stack lines and counts the rest; report.txt
// has the whole stack.
func TestStackTruncation(t *testing.T) {
	root := t.TempDir()
	o, code := runScenario(t, "deep-panic", []string{"FAULTLINE_ARTIFACTS=" + root})
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, o)
	}
	a, err := artifact.Read(seedDir(root, seedK(0)))
	if err != nil {
		t.Fatal(err)
	}
	if a.Report.Failure == nil || a.Report.Failure.Panic == nil {
		t.Fatalf("failure %+v", a.Report.Failure)
	}
	stack := strings.Split(strings.TrimSuffix(a.Report.Failure.Panic.Stack, "\n"), "\n")
	if len(stack) <= 40 {
		t.Fatalf("the stack has %d lines; the test needs more than 40", len(stack))
	}
	mustContainInOrder(t, o, "stack:\n", "  "+stack[39]+"\n", fmt.Sprintf("  ... %d more lines (full stack in report.txt)\n", len(stack)-40), "replay:    ")
	var full strings.Builder
	for _, l := range stack {
		full.WriteString("      " + l + "\n")
	}
	if strings.Contains(a.Text, "more lines") || !strings.Contains(a.Text, "    stack:\n"+full.String()+"    replay:    ") {
		t.Errorf("report.txt\n%s", a.Text)
	}
}
