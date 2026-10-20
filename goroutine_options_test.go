// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/artifact"
	"github.com/hmdsefi/faultline/kernel"
)

var (
	api43Started = make(chan struct{})
	api43Release = make(chan struct{})
	api43Once    sync.Once
)

// overlapBReturned is what B of the overlap scenarios logs if its Run returns; a refused Run
// calls t.Fatalf, so it never does.
const overlapBReturned = "overlap scenario: B returned from Run"

// releaseAPI43 lets A of an overlap scenario finish. B calls it when it is done.
func releaseAPI43() { api43Once.Do(func() { close(api43Release) }) }

func init() { //nolint:gochecknoinits // registers the child-process scenarios
	gorScenarios["opts-procs-negative"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Procs: -1}, func(w *World) {})
	}
	gorScenarios["opts-drain-negative"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Drain: -time.Second}, func(w *World) {})
	}
	gorScenarios["opts-unknown-mode"] = func(t *testing.T) {
		Run(t, Options{Mode: Mode(7), Seeds: 1}, func(w *World) {})
	}
	// API-022 in goroutine mode: crypto seeding panics in a parallel test, from Run and RunSeeds.
	gorScenarios["opts-crypto-run"] = func(t *testing.T) {
		t.Parallel()
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: time.Millisecond}, func(w *World) {})
	}
	gorScenarios["opts-crypto-runseeds"] = func(t *testing.T) {
		t.Parallel()
		RunSeeds(t, Options{Mode: ModeGoroutine, Duration: time.Millisecond}, func(w *World) {}, 1)
	}
	// GOR-078 through RunSeeds, which shares Run's path (API-112).
	gorScenarios["opts-level-runseeds"] = func(t *testing.T) {
		RunSeeds(t, Options{Mode: ModeGoroutine, Duration: time.Millisecond}, func(w *World) {}, 1)
	}
	// overlapA is AT-API-43's TestA: an event-mode Run that stays active until B is done. Each
	// scenario runs in its own child process, so the scenarios below share the channels.
	overlapA := func(t *testing.T) {
		t.Parallel()
		Run(t, Options{NoCryptoSeed: true, Seeds: 1, Duration: time.Millisecond}, func(w *World) {
			close(api43Started)
			select {
			case <-api43Release:
			case <-time.After(10 * time.Second):
			}
		})
	}
	// overlapB is AT-API-43's TestB: once A's Run is active it calls run, logs overlapBReturned
	// if run returns, and releases A.
	overlapB := func(run func(t *testing.T)) func(t *testing.T) {
		return func(t *testing.T) {
			t.Parallel()
			defer releaseAPI43()
			select {
			case <-api43Started:
			case <-time.After(10 * time.Second):
				t.Fatal("A's Run did not start within 10 s")
			}
			run(t)
			t.Log(overlapBReturned)
		}
	}
	// AT-API-43: two parallel parent tests; the goroutine-mode Run overlaps the event-mode one.
	gorScenarios["api-43"] = func(t *testing.T) {
		t.Run("A", overlapA)
		t.Run("B", overlapB(func(t *testing.T) {
			Run(t, Options{Mode: ModeGoroutine, NoCryptoSeed: true, Seeds: 1}, func(w *World) {})
		}))
	}
	// API-104's order: B's overlapping RunSeeds call lists a seed twice (API-113).
	gorScenarios["api-104-order"] = func(t *testing.T) {
		t.Run("A", overlapA)
		t.Run("B", overlapB(func(t *testing.T) {
			RunSeeds(t, Options{Mode: ModeGoroutine, NoCryptoSeed: true}, func(w *World) {}, 1, 1)
		}))
	}
	// API-104's other half: an event-mode Run that starts while a goroutine-mode Run is active.
	// A's body runs in a synctest bubble, where time.After waits for virtual time, so the 10 s
	// wall-clock fallback runs outside A's Run.
	gorScenarios["api-104-reverse"] = func(t *testing.T) {
		time.AfterFunc(10*time.Second, releaseAPI43)
		t.Run("A", func(t *testing.T) {
			t.Parallel()
			Run(t, Options{Mode: ModeGoroutine, NoCryptoSeed: true, Seeds: 1, Duration: time.Millisecond}, func(w *World) {
				close(api43Started)
				<-api43Release
			})
		})
		t.Run("B", overlapB(func(t *testing.T) {
			Run(t, Options{NoCryptoSeed: true, Seeds: 1, Duration: time.Millisecond}, func(w *World) {})
		}))
	}
}

func TestGoroutineOptionsSetupErrors(t *testing.T) {
	for _, tc := range []struct{ scenario, want string }{
		{"opts-procs-negative", "faultline: Options.Procs is negative: -1"},
		{"opts-drain-negative", "faultline: Options.Drain is negative: -1s"},
		{"opts-unknown-mode", "faultline: unknown Options.Mode 7; want ModeEvent (the zero value) or ModeGoroutine"},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			res := runGorScenario(t, tc.scenario)
			if res.code != 1 {
				t.Fatalf("exit code %d, want 1\n%s", res.code, res.out)
			}
			if !strings.Contains(res.out, tc.want) {
				t.Fatalf("output lacks %q\n%s", tc.want, res.out)
			}
			if strings.Contains(res.out, "/seed=0x") {
				t.Fatalf("a seed subtest started\n%s", res.out)
			}
		})
	}
}

func TestGoroutineOptionsDefaults(t *testing.T) {
	var eff Options
	// API-004: one Run per t, so each call gets its own subtest.
	t.Run("defaults", func(t *testing.T) {
		gw(t, Options{Duration: time.Millisecond}, func(w *World) { eff = w.Options() })
	})
	if eff.Procs != 1 || eff.Drain != DefaultDrain || eff.StallTimeout != DefaultStallTimeout {
		t.Fatalf("effective Procs=%d Drain=%v StallTimeout=%v, want 1, %v, %v",
			eff.Procs, eff.Drain, eff.StallTimeout, DefaultDrain, DefaultStallTimeout)
	}
	t.Run("explicit", func(t *testing.T) {
		gw(t, Options{Duration: time.Millisecond, StallTimeout: -1, Procs: 3, Drain: time.Second}, func(w *World) { eff = w.Options() })
	})
	if eff.StallTimeout != -1 || eff.Procs != 3 || eff.Drain != time.Second {
		t.Fatalf("explicit values changed: %+v", eff)
	}
}

func TestGoroutineOptionsIgnoredInEventMode(t *testing.T) {
	var eff Options
	Run(t, Options{Seeds: 1, Duration: time.Millisecond, Procs: -1, Drain: -1, StallTimeout: -1, FailOnLeak: true},
		func(w *World) { eff = w.Options() })
	if eff.Procs != -1 || eff.Drain != -1 {
		t.Fatalf("event mode resolved goroutine-mode fields: Procs=%d Drain=%v", eff.Procs, eff.Drain)
	}
}

func TestProcessErrorText(t *testing.T) {
	inner := errors.New("boom")
	err := error(&ProcessError{Node: "p1", Inc: 2, Err: inner})
	if got, want := err.Error(), "process p1 (incarnation 2) returned an error: boom"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, inner) {
		t.Fatal("errors.Is does not reach Err")
	}
	for err, want := range map[error]string{
		ErrCrashed:  "faultline: process crashed",
		ErrExited:   "faultline: process exited",
		ErrRunEnded: "faultline: run ended",
	} {
		if err.Error() != want {
			t.Fatalf("%v != %q", err, want)
		}
	}
}

// runOverlap runs an overlap scenario and checks that A passed with one results line of level
// wantLevel, and that B failed with the parent-level setup error want (API-087's line: index -1,
// kind setup), stopped in Run (t.Fatalf) and started no seed subtest.
func runOverlap(t *testing.T, scenario, want, wantLevel string) childResult {
	t.Helper()
	res := runGorScenario(t, scenario, "FAULTLINE_RESULTS=$ARTIFACTS/results.jsonl")
	if !strings.Contains(res.out, want) {
		t.Fatalf("output lacks %q\n%s", want, res.out)
	}
	if !strings.Contains(res.out, "--- FAIL: TestGoroutineScenario/B") || !strings.Contains(res.out, "--- PASS: TestGoroutineScenario/A") {
		t.Fatalf("want B failed and A passed\n%s", res.out)
	}
	if strings.Contains(res.out, "TestGoroutineScenario/B/seed=") {
		t.Fatalf("B started a seed subtest\n%s", res.out)
	}
	if strings.Contains(res.out, overlapBReturned) {
		t.Fatalf("B went on after its Run failed\n%s", res.out)
	}
	byTest := map[string][]map[string]any{}
	for _, l := range res.results(t) {
		name, _ := l["test"].(string)
		byTest[name] = append(byTest[name], l)
	}
	a, b := byTest["TestGoroutineScenario/A"], byTest["TestGoroutineScenario/B"]
	if len(a) != 1 || a[0]["status"] != "pass" || a[0]["determinism_level"] != wantLevel {
		t.Fatalf("A's results lines %v, want one pass line with determinism_level %q", a, wantLevel)
	}
	if len(b) != 1 || b[0]["index"] != float64(-1) || b[0]["kind"] != "setup" || b[0]["message"] != want {
		t.Fatalf("B's results lines %v, want one parent-level setup line with message %q", b, want)
	}
	return res
}

func TestATAPI43_GoroutineRunCannotOverlap(t *testing.T) {
	want := "faultline: a goroutine-mode Run cannot overlap other faultline.Run calls (goroutine mode changes GOMAXPROCS and signal handling for the whole process); remove t.Parallel from goroutine-mode tests and their parents"
	runOverlap(t, "api-43", want, "exact")
}

// API-104: any Run that starts while a goroutine-mode Run is active fails too.
func TestGoroutineRunRefusesLaterRun(t *testing.T) {
	runOverlap(t, "api-104-reverse", overlapMsg, "best-effort")
}

// API-104 runs its guard after the options and seeds resolve, so an overlapping RunSeeds call
// with a seed listed twice reports API-113's error, not the overlap.
func TestGoroutineRunSeedsListErrorBeforeOverlap(t *testing.T) {
	res := runOverlap(t, "api-104-order", "faultline: RunSeeds: seed 0x0000000000000001 is listed twice; list each seed once", "exact")
	if strings.Contains(res.out, overlapMsg) {
		t.Fatalf("want no overlap error\n%s", res.out)
	}
}

// API-104's guard in process: the counters after each enterRun and leave, then concurrent Runs of
// both modes, where a goroutine-mode Run never overlaps another and -race checks the locking.
// The test is not parallel, so no other Run of this binary is active.
func TestEnterRun(t *testing.T) {
	counters := func() [2]int {
		runGuard.mu.Lock()
		defer runGuard.mu.Unlock()
		return [2]int{runGuard.active, runGuard.goroutine}
	}
	if c := counters(); c != [2]int{} {
		t.Fatalf("active, goroutine = %v before the test, want 0", c)
	}
	t.Cleanup(func() {
		runGuard.mu.Lock()
		runGuard.active, runGuard.goroutine = 0, 0
		runGuard.mu.Unlock()
	})
	var entered []func() // leave functions, in the order of the successful enterRun calls
	for i, s := range []struct {
		do   string // "event" or "goroutine" enters a Run of that mode; "leave N" leaves entered[N]
		ok   bool   // enterRun's ok
		want [2]int // active and goroutine afterwards
	}{
		{"event", true, [2]int{1, 0}},
		{"event", true, [2]int{2, 0}},      // event-mode Runs may overlap
		{"goroutine", false, [2]int{2, 0}}, // refused, and registers nothing
		{"leave 0", true, [2]int{1, 0}},
		{"goroutine", false, [2]int{1, 0}}, // one other active Run is enough
		{"leave 1", true, [2]int{0, 0}},
		{"goroutine", true, [2]int{1, 1}},
		{"event", false, [2]int{1, 1}}, // any Run while a goroutine-mode Run is active
		{"goroutine", false, [2]int{1, 1}},
		{"leave 2", true, [2]int{0, 0}},
		{"event", true, [2]int{1, 0}},
		{"leave 3", true, [2]int{0, 0}}, // an event-mode leave keeps the goroutine count
		{"goroutine", true, [2]int{1, 1}},
		{"leave 4", true, [2]int{0, 0}},
	} {
		var n int
		if _, err := fmt.Sscanf(s.do, "leave %d", &n); err == nil {
			entered[n]()
		} else {
			leave, ok := enterRun(s.do == "goroutine")
			if ok != s.ok {
				t.Fatalf("step %d (%s): ok = %v, want %v", i, s.do, ok, s.ok)
			}
			if ok {
				entered = append(entered, leave)
			}
		}
		if c := counters(); c != s.want {
			t.Fatalf("step %d (%s): active, goroutine = %v, want %v", i, s.do, c, s.want)
		}
	}

	var events, goroutines atomic.Int32 // Runs inside their enterRun and leave, per mode
	var overlap atomic.Bool
	var wg sync.WaitGroup
	for g := range 8 {
		goroutine := g%4 == 0
		wg.Go(func() {
			for range 1000 {
				leave, ok := enterRun(goroutine)
				if !ok {
					continue
				}
				if goroutine {
					if goroutines.Add(1) != 1 || events.Load() != 0 {
						overlap.Store(true)
					}
					goroutines.Add(-1)
				} else {
					events.Add(1)
					if goroutines.Load() != 0 {
						overlap.Store(true)
					}
					events.Add(-1)
				}
				leave()
			}
		})
	}
	wg.Wait()
	if overlap.Load() {
		t.Fatal("a goroutine-mode Run overlapped another Run")
	}
	if c := counters(); c != [2]int{} {
		t.Fatalf("active, goroutine = %v after concurrent Runs, want 0", c)
	}
	leave, ok := enterRun(true)
	if !ok {
		t.Fatal("a goroutine-mode Run is refused after every Run left")
	}
	leave()
}

// API-022, §7.1: in goroutine mode a crypto seeding panic gets the goroutine-mode message from
// Run and RunSeeds alike; it does not suggest NoCryptoSeed and names no spec ID.
func TestGoroutineCryptoSeedingMessage(t *testing.T) {
	const want = " (goroutine mode cannot be used in a parallel test; remove t.Parallel)\n"
	for _, scenario := range []string{"opts-crypto-run", "opts-crypto-runseeds"} {
		t.Run(scenario, func(t *testing.T) {
			res := runGorScenario(t, scenario)
			if res.code != 1 || !strings.Contains(res.out, "faultline: crypto seeding: ") || !strings.Contains(res.out, want) {
				t.Fatalf("exit %d\n%s", res.code, res.out)
			}
			if strings.Contains(res.out, "NoCryptoSeed") || strings.Contains(res.out, "API-104") {
				t.Fatalf("the message suggests NoCryptoSeed or names API-104\n%s", res.out)
			}
		})
	}
}

func TestGoroutineDeterminismLevelField(t *testing.T) {
	for _, tc := range []struct {
		mode Mode
		want string
	}{{ModeEvent, "exact"}, {ModeGoroutine, "best-effort"}} {
		t.Run(tc.mode.String(), func(t *testing.T) {
			dir := t.TempDir()
			results := filepath.Join(dir, "results.jsonl")
			t.Setenv("FAULTLINE_TRACE", "full")
			t.Setenv("FAULTLINE_ARTIFACTS", dir)
			t.Setenv("FAULTLINE_RESULTS", results)
			t.Setenv("FAULTLINE_SEED", "0x1")
			Run(t, Options{Mode: tc.mode, Seeds: 1, Duration: time.Millisecond}, func(w *World) {})
			a, err := artifact.Read(artifact.Dir(dir, "github.com/hmdsefi/faultline", t.Name(), 1))
			if err != nil {
				t.Fatal(err)
			}
			if a.Report.DeterminismLevel != tc.want || a.Report.ExactBackend != "" {
				t.Fatalf("report determinism_level=%q exact_backend=%q, want %q and empty",
					a.Report.DeterminismLevel, a.Report.ExactBackend, tc.want)
			}
			if tc.mode == ModeGoroutine {
				o := a.Report.Options
				if o.Procs != 1 || o.DrainNS != int64(DefaultDrain) || o.StallTimeoutNS != int64(DefaultStallTimeout) || o.FailOnLeak {
					t.Fatalf("report options %+v", o)
				}
			}
			b, err := os.ReadFile(results)
			if err != nil {
				t.Fatal(err)
			}
			var line map[string]any
			if err := json.Unmarshal(b, &line); err != nil {
				t.Fatal(err)
			}
			if line["determinism_level"] != tc.want {
				t.Fatalf("results determinism_level = %v, want %q", line["determinism_level"], tc.want)
			}
			if _, ok := line["exact_backend"]; ok {
				t.Fatal("results line has exact_backend")
			}
		})
	}
}

// API-082 and ART-021: options_hash and report.options carry Procs, Drain and FailOnLeak
// (report.options also StallTimeout) in goroutine mode only. Event mode keeps a user's values
// (GOR-002 ignores them there), so the event-mode case sets them all.
func TestGoroutineOptionsHash(t *testing.T) {
	base := Options{Seeds: 1, Duration: time.Second, MaxEvents: DefaultMaxEvents,
		Procs: 2, Drain: 3 * time.Second, StallTimeout: 4 * time.Second, FailOnLeak: true}
	ev := string(hashedOptionsJSON(base, ""))
	if strings.Contains(ev, "procs") || strings.Contains(ev, "drain_ns") || strings.Contains(ev, "fail_on_leak") || strings.Contains(ev, "stall") {
		t.Fatalf("event-mode hash input has Phase 2 fields: %s", ev)
	}
	if ro := runOptions(base, &environment{}); ro.Procs != 0 || ro.DrainNS != 0 || ro.StallTimeoutNS != 0 || ro.FailOnLeak {
		t.Fatalf("event-mode report options %+v have Phase 2 fields", ro)
	}
	g := base
	g.Mode, g.Procs, g.Drain, g.StallTimeout = ModeGoroutine, 3, 2*time.Second, 5*time.Second
	gs := string(hashedOptionsJSON(g, ""))
	for _, want := range []string{`"mode":"goroutine"`, `"procs":3`, `"drain_ns":2000000000`, `"fail_on_leak":true`} {
		if !strings.Contains(gs, want) {
			t.Fatalf("goroutine-mode hash input %s lacks %s", gs, want)
		}
	}
	if strings.Contains(gs, "stall") || strings.Contains(gs, "5000000000") {
		t.Fatalf("StallTimeout is hashed: %s", gs)
	}
	if ro := runOptions(g, &environment{}); ro.Procs != 3 || ro.DrainNS != 2e9 || ro.StallTimeoutNS != 5e9 || !ro.FailOnLeak {
		t.Fatalf("goroutine-mode report options %+v, want procs 3, drain_ns 2e9, stall_timeout_ns 5e9, fail_on_leak", ro)
	}
}

// GOR-078 in a child process: a goroutine-mode seed of RunSeeds says "best-effort" in its pass
// artifact and its results line, and a seed-level setup line (no attempt completed) has no level.
func TestGoroutineResultsLevel(t *testing.T) {
	for _, tc := range []struct {
		scenario string
		code     int
		want     any
	}{
		{"opts-level-runseeds", 0, "best-effort"},
		{"opts-crypto-runseeds", 1, nil},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			res := runGorScenario(t, tc.scenario, "FAULTLINE_TRACE=full", "FAULTLINE_RESULTS=$ARTIFACTS/results.jsonl")
			if res.code != tc.code {
				t.Fatalf("exit %d, want %d\n%s", res.code, tc.code, res.out)
			}
			lines := res.results(t)
			if len(lines) != 1 || lines[0]["determinism_level"] != tc.want {
				t.Fatalf("results %v, want one line with determinism_level %v", lines, tc.want)
			}
			if tc.code == 0 {
				if got := res.artifact(t, 1).Report.DeterminismLevel; got != "best-effort" {
					t.Fatalf("report determinism_level = %q, want best-effort", got)
				}
			}
		})
	}
}

// The record helpers of goroutine_helpers_test.go, on the run.phase records of a goroutine-mode run.
func TestGoroutineRecordHelpers(t *testing.T) {
	sink := newRecordSink()
	gw(t, fullTrace(Options{Duration: time.Millisecond}), func(w *World) { sink.capture(w) })
	rs := sink.only(t)
	if seeds := sink.seeds(); len(rs) == 0 || len(sink.get(seeds[0])) != len(rs) {
		t.Fatalf("seeds %v, %d records", seeds, len(rs))
	}
	setup := indexWhere(rs, func(r kernel.Record) bool { return r.Kind == "run.phase" && attr(r, "phase") == "setup" })
	if setup < 0 || attr(rs[setup], "mode") != "goroutine" || attr(rs[setup], "no such key") != "" {
		t.Fatalf("setup record at %d in %v", setup, rs)
	}
	phases := ofKind(rs, "run.phase")
	if len(phases) < 2 || phases[0].Seq != rs[setup].Seq || indexWhere(rs, func(kernel.Record) bool { return false }) != -1 {
		t.Fatalf("run.phase records %v", phases)
	}
}
