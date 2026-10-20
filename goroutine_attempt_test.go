// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/hmdsefi/faultline/artifact"
	"github.com/hmdsefi/faultline/kernel"
)

func init() { //nolint:gochecknoinits // registers the child-process scenarios
	gorScenarios["attempt-stderr"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 2, Duration: time.Millisecond}, func(w *World) {})
	}
	gorScenarios["attempt-skip"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: time.Second}, func(w *World) {
			mustBeInBubble()
			w.T().Skip("skip me")
		})
	}
	gorScenarios["attempt-fatal"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: time.Second}, func(w *World) {
			mustBeInBubble()
			ticks := 0
			w.AddServer("n1", func(n *kernel.Node) {
				var tick func()
				tick = func() {
					ticks++
					if ticks == 3 {
						w.T().Fatalf("stop")
					}
					n.After(10*time.Millisecond, "tick", tick)
				}
				n.After(10*time.Millisecond, "tick", tick)
			})
		})
	}
	gorScenarios["attempt-errorf"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: time.Second}, func(w *World) {
			mustBeInBubble()
			w.AddServer("n1", func(n *kernel.Node) { n.After(time.Millisecond, "x", func() {}) })
			w.RunFor(10 * time.Millisecond)
			w.T().Errorf("soft")
		})
	}
	gorScenarios["attempt-leak-errorf"] = leakErrorfScenario(false)
	gorScenarios["attempt-leak-errorf-skip"] = leakErrorfScenario(true)
	gorScenarios["attempt-deadlock"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: time.Second}, func(w *World) {
			w.AddServer("n1", func(n *kernel.Node) { n.After(time.Millisecond, "x", func() {}) })
			w.RunFor(10 * time.Millisecond)
			<-make(chan int) // nothing can wake the driver: synctest panics with its other deadlock text
		})
	}
	gorScenarios["attempt-crash"] = func(t *testing.T) {
		// GOR-005's line goes to this file; the crash below goes to file descriptor 2.
		f, err := os.Create(filepath.Join(os.Getenv("FAULTLINE_ARTIFACTS"), "stderr.txt")) //nolint:gosec // runGorScenario's temp dir
		if err != nil {
			t.Fatal(err)
		}
		os.Stderr = f
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: time.Second}, func(w *World) {
			go func() { panic("boom") }()
		})
	}
	gorScenarios["attempt-interrupt"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: time.Millisecond}, func(w *World) {})
		p, err := os.FindProcess(os.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Signal(os.Interrupt); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
		t.Log("still alive")
	}
	gorScenarios["attempt-crypto-parallel"] = func(t *testing.T) {
		t.Parallel()
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: time.Millisecond}, func(w *World) {})
	}
}

// mustBeInBubble panics unless the caller runs in a synctest bubble that has not advanced its
// clock: a bubble starts at kernel.Epoch. A scenario body calls it first, so a build that runs the
// attempt outside a bubble fails the scenario with a body panic.
func mustBeInBubble() {
	if !time.Now().Equal(kernel.Epoch) {
		panic("not in a synctest bubble")
	}
}

// leakErrorfScenario leaves a goroutine blocked in the bubble and calls t.Errorf, then t.Skip when
// skip is set. The leak makes synctest panic when the bubble ends, which pre-empts the st.FailNow
// that synctest.Test calls after a failed bubble, so GOR-006 step 4 calls it.
func leakErrorfScenario(skip bool) func(t *testing.T) {
	return func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: time.Second}, func(w *World) {
			mustBeInBubble()
			w.AddServer("n1", func(n *kernel.Node) { n.After(time.Millisecond, "x", func() {}) })
			go func() { <-make(chan int) }()
			w.RunFor(10 * time.Millisecond)
			w.T().Errorf("soft")
			if skip {
				w.T().Skip("skip after soft")
			}
		})
	}
}

func TestGoroutineAttemptRunsInBubble(t *testing.T) {
	prev := runtime.GOMAXPROCS(3) // not Procs, so a missing restore shows on a 1-CPU host too
	t.Cleanup(func() { runtime.GOMAXPROCS(prev) })
	var (
		start   time.Time
		procs   int
		mode    string
		bubbleT string
	)
	sink := newRecordSink()
	gw(t, fullTrace(Options{Duration: time.Millisecond}), func(w *World) {
		start = time.Now()
		procs = runtime.GOMAXPROCS(0)
		bubbleT = w.T().Name()
		c := make(chan os.Signal, 1) // AT-GOR-24 from the driver goroutine (GOR-004)
		signal.Notify(c, os.Interrupt)
		signal.Stop(c)
		sink.capture(w)
	})
	if !start.Equal(epoch) {
		t.Fatalf("bubble started at %v, want %v", start, epoch)
	}
	if procs != 1 {
		t.Fatalf("GOMAXPROCS inside the attempt = %d, want 1", procs)
	}
	if got := runtime.GOMAXPROCS(0); got != 3 {
		t.Fatalf("GOMAXPROCS after Run = %d, want 3", got)
	}
	if !strings.HasPrefix(bubbleT, t.Name()+"/seed=0x") {
		t.Fatalf("w.T().Name() = %q", bubbleT)
	}
	for _, r := range ofKind(sink.only(t), "run.phase") {
		if attr(r, "phase") == "setup" {
			mode = attr(r, "mode")
		}
	}
	if mode != "goroutine" {
		t.Fatalf("run.phase setup mode = %q, want goroutine", mode)
	}
}

// TestGoroutineAttemptRestoresProcsAfterSkip checks GOR-006 step 1 on a path where fn does not
// return: the seed is skipped inside the bubble.
func TestGoroutineAttemptRestoresProcsAfterSkip(t *testing.T) {
	prev := runtime.GOMAXPROCS(3)
	t.Cleanup(func() { runtime.GOMAXPROCS(prev) })
	ok := t.Run("skip", func(t *testing.T) {
		gw(t, Options{Duration: time.Millisecond}, func(w *World) { w.T().Skip("skip me") })
	})
	if !ok {
		t.Fatal("the skipped seed failed its parent")
	}
	if got := runtime.GOMAXPROCS(0); got != 3 {
		t.Fatalf("GOMAXPROCS after a skipped seed = %d, want 3", got)
	}
}

func TestATGOR25_SynctestLeakPanicText(t *testing.T) {
	var rec any
	func() {
		defer func() { rec = recover() }()
		synctest.Test(t, func(t *testing.T) {
			go func() { <-make(chan int) }()
		})
	}()
	err, ok := rec.(error)
	if !ok || !strings.HasPrefix(err.Error(), synctestLeakPrefix) {
		t.Fatalf("recovered %T %v, want an error starting with %q", rec, rec, synctestLeakPrefix)
	}
	if !isSynctestLeakPanic(rec) || isSynctestLeakPanic("x") || isSynctestLeakPanic(nil) {
		t.Fatal("isSynctestLeakPanic misclassifies")
	}
	if t.Failed() {
		t.Fatal("the recovered leak panic marked the test failed")
	}
}

// seedLine matches GOR-005's line in the child: the seed in the subtest name, the seed in the
// replay command, and the end of the line.
const seedLine = `faultline: TestGoroutineScenario/seed=0x([0-9a-f]{16}): goroutine mode; replay: ` +
	`FAULTLINE_SEED=0x([0-9a-f]{16}) go test -v[^\n]* -run '\^TestGoroutineScenario\$' github\.com/hmdsefi/faultline`

func TestATGOR27_StderrSeedLine(t *testing.T) {
	res := runGorScenario(t, "attempt-stderr")
	m := regexp.MustCompile(`(?m)^`+seedLine+`$`).FindAllStringSubmatch(res.out, -1)
	if len(m) != 2 || m[0][1] == m[1][1] {
		t.Fatalf("found %d stderr seed lines, want 2 for two seeds\n%s", len(m), res.out)
	}
	for _, l := range m {
		if l[1] != l[2] {
			t.Fatalf("line for seed 0x%s replays seed 0x%s:\n%s", l[1], l[2], l[0])
		}
	}
}

// TestGoroutineSeedLineBeforeCrash checks GOR-005's purpose: the line is on os.Stderr before the
// primary attempt starts, so it names the seed when code under test ends the binary.
func TestGoroutineSeedLineBeforeCrash(t *testing.T) {
	res := runGorScenario(t, "attempt-crash", "FAULTLINE_SEED=0x1")
	if res.code != 2 || !strings.Contains(res.out, "panic: boom") {
		t.Fatalf("exit %d\n%s", res.code, res.out)
	}
	b, err := os.ReadFile(filepath.Join(res.artifacts, "stderr.txt"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`^` + seedLine + "\n$").FindSubmatch(b)
	if m == nil || string(m[1]) != "0000000000000001" || string(m[2]) != "0000000000000001" {
		t.Fatalf("os.Stderr got %q, want GOR-005's line for seed 0x1\n%s", b, res.out)
	}
}

// TestGoroutinePreNotifyKeepsInterrupt checks that GOR-004's registration is stopped again: after
// a goroutine-mode Run, os.Interrupt still ends the test binary.
func TestGoroutinePreNotifyKeepsInterrupt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Process.Signal cannot send os.Interrupt on Windows")
	}
	res := runGorScenario(t, "attempt-interrupt")
	if res.code == 0 || strings.Contains(res.out, "still alive") || !strings.Contains(res.out, "goroutine mode; replay: ") {
		t.Fatalf("exit %d\n%s", res.code, res.out)
	}
}

// TestGoroutineAttemptSkipInBubble is AT-GOR-35's skip half (GOR-006 step 4: st.SkipNow).
func TestGoroutineAttemptSkipInBubble(t *testing.T) {
	res := runGorScenario(t, "attempt-skip", "FAULTLINE_SEED=0x1", "FAULTLINE_RESULTS=$ARTIFACTS/results.jsonl")
	if res.code != 0 || !strings.Contains(res.out, "--- SKIP: TestGoroutineScenario/seed=0x0000000000000001") {
		t.Fatalf("exit %d\n%s", res.code, res.out)
	}
	lines := res.results(t)
	if len(lines) != 1 || lines[0]["status"] != "skip" {
		t.Fatalf("results %v", lines)
	}
}

func TestGoroutineAttemptFatalInCallback(t *testing.T) {
	res := runGorScenario(t, "attempt-fatal", "FAULTLINE_SEED=0x1")
	if res.code != 1 || !strings.Contains(res.out, "stopped by t.FailNow or t.Fatal at t=0.030000000s") ||
		!strings.Contains(res.out, "no artifacts were written") {
		t.Fatalf("exit %d\n%s", res.code, res.out)
	}
}

// TestGoroutineAttemptErrorfEndsSeed is AT-GOR-35's error half (GOR-006 step 4, API-102):
// t.Errorf on the bubble's T makes synctest.Test call FailNow on the seed subtest after the
// bubble, so the seed ends through API-101.
func TestGoroutineAttemptErrorfEndsSeed(t *testing.T) {
	res := runGorScenario(t, "attempt-errorf", "FAULTLINE_SEED=0x1", "FAULTLINE_RESULTS=$ARTIFACTS/results.jsonl")
	if res.code != 1 || !strings.Contains(res.out, "soft") ||
		!strings.Contains(res.out, "stopped by t.FailNow or t.Fatal") {
		t.Fatalf("exit %d\n%s", res.code, res.out)
	}
	checkGoexitResult(t, res, "1.000000000s")
}

// TestGoroutineAttemptLeakErrorf checks GOR-006 step 4's st.FailNow after GOR-008 swallowed the
// leak panic: the seed ends through API-101 (kind fail, no artifacts, no re-run), not through
// API-061 row 8. The skip variant checks that bt.Failed() is tested before bt.Skipped(). testing
// marks st failed with bt, so the order shows only in the note: st.SkipNow would make API-101 say
// "stopped by t.SkipNow or t.Skip".
func TestGoroutineAttemptLeakErrorf(t *testing.T) {
	for _, tc := range []struct{ scenario, at string }{
		{"attempt-leak-errorf", "1.000000000s"},
		{"attempt-leak-errorf-skip", "0.010000000s"},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			res := runGorScenario(t, tc.scenario, "FAULTLINE_SEED=0x1", "FAULTLINE_RESULTS=$ARTIFACTS/results.jsonl")
			note := "faultline: seed 0x0000000000000001 stopped by t.FailNow or t.Fatal at t=" + tc.at +
				" (event 2); no artifacts were written"
			if res.code != 1 || !strings.Contains(res.out, "soft") || !strings.Contains(res.out, note) {
				t.Fatalf("exit %d\n%s", res.code, res.out)
			}
			checkGoexitResult(t, res, tc.at)
			dir := artifact.Dir(res.artifacts, "github.com/hmdsefi/faultline", "TestGoroutineScenario", 1)
			if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("artifact directory %s: %v, want none\n%s", dir, err, res.out)
			}
		})
	}
}

// checkGoexitResult checks the child's one results line: API-101's last case at virtual time at.
func checkGoexitResult(t *testing.T, res childResult, at string) {
	t.Helper()
	lines := res.results(t)
	if len(lines) != 1 || lines[0]["status"] != "fail" || lines[0]["kind"] != "fail" ||
		lines[0]["message"] != goexitMessage || lines[0]["at"] != at {
		t.Fatalf("results %v, want one fail line of kind fail at %s\n%s", lines, at, res.out)
	}
}

// TestGoroutineAttemptOtherPanicCrashes checks GOR-008's re-panic of any panic other than the
// leak panic. The binary ends, so the seed writes no API-101 note and no results line.
func TestGoroutineAttemptOtherPanicCrashes(t *testing.T) {
	res := runGorScenario(t, "attempt-deadlock", "FAULTLINE_SEED=0x1", "FAULTLINE_RESULTS=$ARTIFACTS/results.jsonl")
	if res.code != 2 || strings.Contains(res.out, "stopped by t.") ||
		!strings.Contains(res.out, "panic: deadlock: all goroutines in bubble are blocked [recovered, repanicked]") {
		t.Fatalf("exit %d\n%s", res.code, res.out)
	}
	if b, err := os.ReadFile(filepath.Join(res.artifacts, "results.jsonl")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("results.jsonl: %q, %v; want no file\n%s", b, err, res.out)
	}
}

// API-022 inside the bubble: cryptotest.SetGlobalRandom on the bubble's T gives the same
// goroutine-mode message as TestGoroutineCryptoSeedingMessage (owner decision 2). The setup error
// comes before the body runs, so the output is the same with or without the bubble: this test only
// guards the message against a regression.
func TestGoroutineAttemptCryptoParallel(t *testing.T) {
	res := runGorScenario(t, "attempt-crypto-parallel")
	want := " (goroutine mode cannot be used in a parallel test; remove t.Parallel)\n"
	if res.code != 1 || !strings.Contains(res.out, "faultline: crypto seeding: ") || !strings.Contains(res.out, want) {
		t.Fatalf("exit %d\n%s", res.code, res.out)
	}
}

func TestParseStackHeader(t *testing.T) {
	for _, tc := range []struct {
		line        string
		gid, bubble uint64
	}{
		{"goroutine 29 [running, synctest bubble 3]:", 29, 3},
		{"goroutine 1234 [running, synctest bubble 567]:", 1234, 567},
		{"goroutine 7 [chan receive (durable), 2 minutes, synctest bubble 12]:", 7, 12},
		// GODEBUG=tracebacklabels=1 prints the labels inside the brackets on go1.26.0 and after
		// them on go1.27.1.
		{`goroutine 57 [running, synctest bubble 11 labels:{"faultline_inc": "0", "faultline_node": "0", "faultline_run": "1"}]:`, 57, 11},
		{`goroutine 57 [running, synctest bubble 11] {faultline_inc: 0, faultline_node: 0, faultline_run: 1}:`, 57, 11},
	} {
		gid, bubble, ok := parseStackHeader(tc.line)
		if !ok || gid != tc.gid || bubble != tc.bubble {
			t.Errorf("parseStackHeader(%q) = %d, %d, %v; want %d, %d, true", tc.line, gid, bubble, ok, tc.gid, tc.bubble)
		}
	}
	if _, _, ok := parseStackHeader("goroutine 1 [chan receive]:"); ok {
		t.Fatal("header outside a bubble parsed as inside")
	}
	if curGoroutineID() == 0 {
		t.Fatal("curGoroutineID returned 0")
	}
}

// TestGoroutineAttemptBegin checks GOR-007 steps 1 to 3 against the runtime's own header, and the
// wake channel's capacity (GOR-024).
func TestGoroutineAttemptBegin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var a, b goAttempt
		a.begin()
		b.begin()
		var buf [256]byte
		line, _, _ := strings.Cut(string(buf[:runtime.Stack(buf[:], false)]), "\n")
		gid, bubble, ok := parseStackHeader(line)
		if !ok || bubble == 0 || gid != curGoroutineID() {
			t.Fatalf("header %q: %d, %d, %v; curGoroutineID %d", line, gid, bubble, ok, curGoroutineID())
		}
		if a.gid != gid || a.bubble.Load() != bubble {
			t.Fatalf("begin recorded goroutine %d bubble %d, want %d %d", a.gid, a.bubble.Load(), gid, bubble)
		}
		if cap(a.wake) != 1 {
			t.Fatalf("cap(wake) = %d, want 1", cap(a.wake))
		}
		ra, errA := strconv.ParseUint(a.run, 10, 64)
		rb, errB := strconv.ParseUint(b.run, 10, 64)
		if errA != nil || errB != nil || ra == 0 || rb != ra+1 {
			t.Fatalf("run labels %q then %q, want consecutive decimals from 1", a.run, b.run)
		}
		var prof bytes.Buffer
		if err := pprof.Lookup("goroutine").WriteTo(&prof, 1); err != nil {
			t.Fatal(err)
		}
		want := `{"faultline_inc":"0", "faultline_node":"0", "faultline_run":"` + b.run + `"}`
		if !strings.Contains(prof.String(), want) {
			t.Fatalf("goroutine profile has no labels %s\n%s", want, prof.String())
		}
	})
}
