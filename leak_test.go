// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime/pprof"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/artifact"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
)

func init() { //nolint:gochecknoinits // registers the child-process scenarios
	gorScenarios["gor-33"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 3 * time.Second}, func(w *World) {
			w.AddProcess("p1", func(ctx context.Context, p *Process) error {
				go func() { <-make(chan int) }()
				time.Sleep(time.Second)
				return nil
			})
			w.Plan(fault.Script(fault.Event{At: vt(2 * time.Second), Kind: fault.KindCrash, Node: "p1"}))
		})
	}
	gorScenarios["gor-12"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: time.Second,
			FailOnLeak: os.Getenv("GOR12_FAIL_ON_LEAK") == "1"}, func(w *World) {
			w.AddProcess("p1", func(ctx context.Context, p *Process) error {
				go func() { <-make(chan int) }()
				<-ctx.Done()
				return nil
			})
			if os.Getenv("GOR12_FINAL_FAILS") == "1" {
				w.Final("fails", func() error { return errors.New("no") })
			}
		})
	}
	gorScenarios["gor-13"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: time.Second}, func(w *World) {
			w.AddProcess("p1", func(ctx context.Context, p *Process) error {
				for ctx.Err() == nil {
					time.Sleep(5 * time.Second)
				}
				return nil
			})
		})
	}
	// A run that stops at MaxEvents with AllowLimit (API-061 row 9) and leaks a goroutine.
	gorScenarios["leak-limited"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: time.Second, MaxEvents: 1, AllowLimit: true,
			FailOnLeak: os.Getenv("LEAK_FAIL_ON_LEAK") == "1"}, func(w *World) {
			w.AddProcess("p1", func(ctx context.Context, p *Process) error {
				go func() { <-make(chan int) }()
				<-ctx.Done()
				return nil
			})
			w.Sim.At(vt(100*time.Millisecond), "a", func() {})
			w.Sim.At(vt(200*time.Millisecond), "b", func() {})
		})
	}
	// FailOnLeak, and the attempt ends through a Goexit: p2 calls t.FailNow at 0.5 s
	// (LEAK_GOEXIT=failnow), or a final check calls t.Skip (LEAK_GOEXIT=skip).
	gorScenarios["leak-goexit"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: time.Second, FailOnLeak: true}, func(w *World) {
			w.AddProcess("p1", func(ctx context.Context, p *Process) error {
				go func() { <-make(chan int) }()
				<-ctx.Done()
				return nil
			})
			switch os.Getenv("LEAK_GOEXIT") {
			case "failnow":
				w.AddProcess("p2", func(ctx context.Context, p *Process) error {
					time.Sleep(500 * time.Millisecond)
					w.T().FailNow()
					return nil
				})
			case "skip":
				w.Final("skips", func() error {
					w.T().Skip("skipped in a final check")
					return nil
				})
			}
		})
	}
	// FailOnLeak; only the primary attempt leaks, so the artifact attempt, with the same trace
	// hash, finds no leak.
	gorScenarios["leak-first-attempt"] = func(t *testing.T) {
		var attempts atomic.Int32
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: time.Second, FailOnLeak: true}, func(w *World) {
			first := attempts.Add(1) == 1
			w.AddProcess("p1", func(ctx context.Context, p *Process) error {
				if first {
					go func() { <-make(chan int) }()
				}
				<-ctx.Done()
				return nil
			})
		})
	}
	// FailOnLeak with every goroutine dump cut: the dump cap is lowered below the driver's own
	// block, so the leaked goroutine is never counted.
	gorScenarios["leak-cut"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: time.Second, FailOnLeak: true}, func(w *World) {
			w.drv.stackCap = 1 << 8
			w.AddProcess("p1", func(ctx context.Context, p *Process) error {
				go func() { <-make(chan int) }()
				<-ctx.Done()
				return nil
			})
		})
	}
}

func TestATGOR12_LeakReported(t *testing.T) {
	res := runGorScenario(t, "gor-12", "FAULTLINE_SEED=0x1")
	for _, want := range []string{
		"1 goroutine(s) still blocked after the run (drain 1m0s)",
		"p1 incarnation 1 (running at the end): 1",
	} {
		if res.code != 0 || !strings.Contains(res.out, want) {
			t.Fatalf("exit %d, want 0 and %q\n%s", res.code, want, res.out)
		}
	}
}

// GOR-067: with GODEBUG=tracebacklabels=1 the goroutine headers carry the pprof labels (go1.26:
// "synctest bubble <id> labels:{...}]", go1.27: "synctest bubble <id>] {...}"); the leak is still
// counted and reported.
func TestATGOR12_LeakReportedTracebackLabels(t *testing.T) {
	res := runGorScenario(t, "gor-12", "FAULTLINE_SEED=0x1", "GODEBUG=tracebacklabels=1")
	for _, want := range []string{
		"1 goroutine(s) still blocked after the run (drain 1m0s)",
		"p1 incarnation 1 (running at the end): 1",
	} {
		if res.code != 0 || !strings.Contains(res.out, want) {
			t.Fatalf("exit %d, want 0 and %q\n%s", res.code, want, res.out)
		}
	}
}

func TestATGOR12_FailOnLeak(t *testing.T) {
	res := runGorScenario(t, "gor-12", "FAULTLINE_SEED=0x1", "GOR12_FAIL_ON_LEAK=1",
		"FAULTLINE_RESULTS=$ARTIFACTS/results.jsonl")
	if res.code != 1 {
		t.Fatalf("exit %d\n%s", res.code, res.out)
	}
	a := res.artifact(t, 1)
	f := a.Report.Failure
	if f == nil || f.Kind != "leak" || f.Check != "" || f.Signature != "leak:" ||
		f.Headline != "1 goroutine(s) still blocked after the run (drain 1m0s)" {
		t.Fatalf("failure %+v", f)
	}
	l := a.Report.Leaks
	if l == nil || len(l.Groups) != 1 || l.Groups[0].Node != "p1" || l.Groups[0].Inc != 1 ||
		l.Groups[0].Status != "running at the end" || l.Groups[0].Count != 1 || l.Unattributed != 0 {
		t.Fatalf("report leaks %+v", l)
	}
	dir := artifact.Dir(res.artifacts, "github.com/hmdsefi/faultline", "TestGoroutineScenario", 1)
	b, err := os.ReadFile(filepath.Join(dir, "goroutines.txt"))
	if err != nil || !strings.Contains(string(b), "[chan receive (durable), synctest bubble") {
		t.Fatalf("goroutines.txt: %v\n%s", err, b)
	}
	lines := res.results(t)
	if len(lines) != 1 || lines[0]["signature"] != "leak:" || lines[0]["leaks"] == nil {
		t.Fatalf("results %v", lines)
	}
	// API-066: the message is the report text, at is the final Now, no event and no failure record.
	if !strings.HasPrefix(f.Message, "faultline: 1 goroutine(s) still blocked after the run (drain 1m0s)\n  p1 incarnation 1 (running at the end): 1\n    1 × ") ||
		f.At != "1.000000000s" || f.AtNS != int64(time.Second) || f.Event != 0 || f.RecordSeq != 0 {
		t.Fatalf("failure message, time, event or record_seq: %+v", f)
	}
	for _, want := range []string{"--- FAIL: TestGoroutineScenario/seed=0x0000000000000001",
		"faultline: 1 goroutine(s) still blocked after the run (drain 1m0s)\n"} {
		if !strings.Contains(res.out, want) {
			t.Fatalf("output lacks %q\n%s", want, res.out)
		}
	}
	// GOR-069: the report is the failure's message, so no attempt also logs it.
	if n := strings.Count(res.out, "p1 incarnation 1 (running at the end): 1"); n != 1 {
		t.Fatalf("the report's group line appears %d times, want 1 (the failure message)\n%s", n, res.out)
	}
}

// GOR-065 and GOR-069: with FailOnLeak, an attempt that ended through a Goexit (t.FailNow in a
// process, t.Skip in a final check) is not a pass, so its leak report is logged, not dropped.
func TestGoroutineFailOnLeakAfterGoexit(t *testing.T) {
	for _, tc := range []struct {
		mode, outcome string
		code          int
	}{
		{"failnow", "--- FAIL: TestGoroutineScenario/seed=0x0000000000000001", 1},
		{"skip", "--- SKIP: TestGoroutineScenario/seed=0x0000000000000001", 0},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			res := runGorScenario(t, "leak-goexit", "FAULTLINE_SEED=0x1", "LEAK_GOEXIT="+tc.mode)
			for _, want := range []string{tc.outcome,
				"faultline: 1 goroutine(s) still blocked after the run (drain 1m0s)\n",
				"p1 incarnation 1 (running at the end): 1"} {
				if res.code != tc.code || !strings.Contains(res.out, want) {
					t.Fatalf("exit %d, want %d and %q\n%s", res.code, tc.code, want, res.out)
				}
			}
		})
	}
}

// API-074 and API-077: when only the primary attempt leaked, the artifact of the leak failure
// still carries the failure's leaks and goroutines.txt.
func TestGoroutineLeakArtifactFromPrimary(t *testing.T) {
	res := runGorScenario(t, "leak-first-attempt", "FAULTLINE_SEED=0x1")
	if res.code != 1 {
		t.Fatalf("exit %d\n%s", res.code, res.out)
	}
	a := res.artifact(t, 1)
	if f := a.Report.Failure; f == nil || f.Signature != "leak:" || a.Report.Run.Attempts != 2 {
		t.Fatalf("failure %+v after %d attempts, want leak: after 2", f, a.Report.Run.Attempts)
	}
	if l := a.Report.Leaks; l == nil || len(l.Groups) != 1 || l.Groups[0].Node != "p1" || l.Groups[0].Count != 1 {
		t.Fatalf("report leaks %+v", l)
	}
	b, err := os.ReadFile(filepath.Join(artifact.Dir(res.artifacts, "github.com/hmdsefi/faultline", "TestGoroutineScenario", 1), "goroutines.txt"))
	if err != nil || !strings.Contains(string(b), "[chan receive (durable), synctest bubble") {
		t.Fatalf("goroutines.txt: %v\n%s", err, b)
	}
}

// GOR-067: when every dump is cut, the leak goes uncounted; the attempt logs the cut warning,
// and FailOnLeak does not fail the seed, whose results line has no leaks.
func TestGoroutineLeakCutDumpLogged(t *testing.T) {
	res := runGorScenario(t, "leak-cut", "FAULTLINE_SEED=0x1", "FAULTLINE_RESULTS=$ARTIFACTS/results.jsonl")
	if res.code != 0 || !strings.Contains(res.out, "faultline: "+dumpCutWarning+"\n") || strings.Contains(res.out, "still blocked after the run (drain") {
		t.Fatalf("exit %d, want 0 and the cut warning without a leak report\n%s", res.code, res.out)
	}
	if lines := res.results(t); len(lines) != 1 || lines[0]["status"] != "pass" || lines[0]["leaks"] != nil {
		t.Fatalf("results %v", lines)
	}
}

// GOR-069: FailOnLeak changes only an outcome that is otherwise a pass. A failing final check
// stays the failure, the report is logged, and report.json still carries the leaks.
func TestGoroutineFailOnLeakKeepsFailure(t *testing.T) {
	res := runGorScenario(t, "gor-12", "FAULTLINE_SEED=0x1", "GOR12_FAIL_ON_LEAK=1", "GOR12_FINAL_FAILS=1")
	if res.code != 1 || !strings.Contains(res.out, "faultline: 1 goroutine(s) still blocked after the run (drain 1m0s)\n") {
		t.Fatalf("exit %d\n%s", res.code, res.out)
	}
	a := res.artifact(t, 1)
	if f := a.Report.Failure; f == nil || f.Signature != "final:fails" {
		t.Fatalf("failure %+v", f)
	}
	if l := a.Report.Leaks; l == nil || len(l.Groups) != 1 || l.Groups[0].Count != 1 {
		t.Fatalf("report leaks %+v", l)
	}
}

// API-074 and API-087: a passing seed with leaks carries them in its results line and, under
// FAULTLINE_TRACE=full, in its pass artifact with goroutines.txt.
func TestGoroutineLeakOnPassResults(t *testing.T) {
	res := runGorScenario(t, "gor-12", "FAULTLINE_SEED=0x1", "FAULTLINE_TRACE=full",
		"FAULTLINE_RESULTS=$ARTIFACTS/results.jsonl")
	if res.code != 0 {
		t.Fatalf("exit %d\n%s", res.code, res.out)
	}
	lines := res.results(t)
	if len(lines) != 1 || lines[0]["status"] != "pass" {
		t.Fatalf("results %v", lines)
	}
	l, _ := lines[0]["leaks"].(map[string]any)
	groups, _ := l["groups"].([]any)
	if len(groups) != 1 || l["unattributed"] != float64(0) {
		t.Fatalf("results leaks %v", lines[0]["leaks"])
	}
	if g, _ := groups[0].(map[string]any); g["node"] != "p1" || g["inc"] != float64(1) || g["count"] != float64(1) {
		t.Fatalf("results leak group %v", groups[0])
	}
	// API-087's field order: leaks comes after determinism_level and exact_backend.
	raw, err := os.ReadFile(filepath.Join(res.artifacts, "results.jsonl"))
	if i, j := bytes.Index(raw, []byte(`"determinism_level":`)), bytes.Index(raw, []byte(`"leaks":`)); err != nil || i < 0 || j < i {
		t.Fatalf("results line has leaks before determinism_level (%v):\n%s", err, raw)
	}
	a := res.artifact(t, 1)
	if a.Report.Status != "pass" || a.Report.Leaks == nil || len(a.Report.Leaks.Groups) != 1 {
		t.Fatalf("pass artifact status %q leaks %+v", a.Report.Status, a.Report.Leaks)
	}
	b, err := os.ReadFile(filepath.Join(artifact.Dir(res.artifacts, "github.com/hmdsefi/faultline", "TestGoroutineScenario", 1), "goroutines.txt"))
	if err != nil || !strings.Contains(string(b), "[chan receive (durable), synctest bubble") {
		t.Fatalf("pass artifact goroutines.txt: %v\n%s", err, b)
	}
}

// API-061: row 8a comes before row 9, so with FailOnLeak a leaking run that stopped at
// MaxEvents with AllowLimit fails with kind leak and does not log the limited pass. Without
// FailOnLeak it stays a limited pass and logs the report.
func TestGoroutineLeakBeforeLimitedPass(t *testing.T) {
	const limited = "stopped at MaxEvents (1)"
	res := runGorScenario(t, "leak-limited", "FAULTLINE_SEED=0x1", "LEAK_FAIL_ON_LEAK=1",
		"FAULTLINE_RESULTS=$ARTIFACTS/results.jsonl")
	if lines := res.results(t); res.code != 1 || len(lines) != 1 || lines[0]["kind"] != "leak" || strings.Contains(res.out, limited) {
		t.Fatalf("FailOnLeak: exit %d, results %v\n%s", res.code, lines, res.out)
	}
	res = runGorScenario(t, "leak-limited", "FAULTLINE_SEED=0x1")
	if res.code != 0 || !strings.Contains(res.out, limited) || !strings.Contains(res.out, "1 goroutine(s) still blocked after the run") {
		t.Fatalf("default: exit %d\n%s", res.code, res.out)
	}
}

// AT-GOR-33: a crash after the process exited keeps the incarnation's status exited (GOR-051).
func TestATGOR33_ExitedStatusSurvivesCrash(t *testing.T) {
	res := runGorScenario(t, "gor-33", "FAULTLINE_SEED=0x1")
	if want := "p1 incarnation 1 (exited at t=1.000000000s): 1"; res.code != 0 || !strings.Contains(res.out, want) {
		t.Fatalf("exit %d, want 0 and %q\n%s", res.code, want, res.out)
	}
}

func TestATGOR13_RetryLoopExitsDuringDrain(t *testing.T) {
	res := runGorScenario(t, "gor-13", "FAULTLINE_SEED=0x1")
	if res.code != 0 || strings.Contains(res.out, "still blocked") {
		t.Fatalf("exit %d\n%s", res.code, res.out)
	}
}

func TestSplitStacks(t *testing.T) {
	dump := strings.Join([]string{
		"goroutine 29 [running, synctest bubble 3]:",
		"main.driver()",
		"\t/src/a.go:1 +0x1",
		"",
		"goroutine 1 [chan receive]:",
		"testing.(*T).Run(0x1)",
		"\t/go/testing.go:1 +0x1",
		"",
		"goroutine 27 [synctest.Run (durable), synctest bubble 3]:",
		"internal/synctest.Run(0x1)",
		"\t/go/synctest.go:1 +0x1",
		"",
		"goroutine 28 [chan receive (durable), synctest bubble 3]:",
		"testing/synctest.testingSynctestTest(0x1, 0x2)",
		"\t/go/testing.go:2308 +0x218",
		"",
		"goroutine 40 [chan receive (durable), synctest bubble 3]:",
		"kv.leak()",
		"\t/src/kv.go:9 +0x1",
		"",
		"goroutine 33 [select (durable), synctest bubble 3, locked to thread]:",
		"kv.other()",
		"\t/src/kv.go:10 +0x1",
		"",
		"goroutine 41 [chan receive (durable), synctest bubble 13]:",
		"other.bubble()",
		"\t/src/o.go:1 +0x1",
		"",
	}, "\n")
	var ids []uint64
	for _, b := range splitStacks(dump, 3, 29, true) {
		ids = append(ids, b.id)
	}
	if !slices.Equal(ids, []uint64{33, 40}) {
		t.Fatalf("counted goroutines %v, want [33 40]", ids)
	}
	ids = nil
	for _, b := range splitStacks(dump, 3, 29, false) {
		ids = append(ids, b.id)
	}
	if !slices.Equal(ids, []uint64{27, 28, 29, 33, 40}) {
		t.Fatalf("bubble goroutines %v, want [27 28 29 33 40]", ids)
	}
	if blk := splitStacks(dump, 3, 29, true)[1]; blk.text != "goroutine 40 [chan receive (durable), synctest bubble 3]:\nkv.leak()\n\t/src/kv.go:9 +0x1" {
		t.Fatalf("block text %q", blk.text)
	}
}

// GOR-067 with GODEBUG=tracebacklabels=1: go1.26 prints the labels inside the brackets after the
// bubble ID, go1.27 after the closing bracket. A label value may hold "]" or "synctest bubble".
func TestSplitStacksTracebackLabels(t *testing.T) {
	dump := strings.Join([]string{
		`goroutine 7 [synctest.Run (durable), synctest bubble 3]:`,
		"internal/synctest.Run(0x1)",
		"",
		`goroutine 8 [chan receive (durable), synctest bubble 3]:`,
		"testing.testingSynctestTest(0x1, 0x2)",
		"",
		`goroutine 9 [running, synctest bubble 3 labels:{"faultline_inc": "0", "faultline_node": "0", "faultline_run": "4"}]:`,
		"main.driver()",
		"",
		`goroutine 10 [chan receive (durable), synctest bubble 3 labels:{"faultline_inc": "1", "faultline_node": "1", "faultline_run": "4"}]:`,
		"kv.leak()",
		"",
		`goroutine 11 [chan receive (durable), synctest bubble 3] {faultline_inc: 1, faultline_node: 1, faultline_run: 4}:`,
		"kv.leak()",
		"",
		`goroutine 12 [select (durable), synctest bubble 3 labels:{"x": "a]b"}]:`,
		"kv.other()",
		"",
		`goroutine 13 [chan receive (durable), synctest bubble 31 labels:{"x": "synctest bubble 3]"}]:`,
		"other.bubble()",
		"",
		`goroutine 14 [chan receive (durable), synctest bubble 31] {x: "synctest bubble 3]"}:`,
		"other.bubble()",
		"",
		`goroutine 15 [chan receive] {x: "synctest bubble 3"}:`,
		"other.unbubbled()",
		"",
	}, "\n")
	var ids []uint64
	for _, b := range splitStacks(dump, 3, 9, true) {
		ids = append(ids, b.id)
	}
	if !slices.Equal(ids, []uint64{10, 11, 12}) {
		t.Fatalf("counted goroutines %v, want [10 11 12]", ids)
	}
}

func TestParseGoroutineProfileAndLeakText(t *testing.T) {
	prof := strings.Join([]string{
		"goroutine profile: total 5",
		"1 @ 0x1 0x2",
		`# labels: {"faultline_inc":"0", "faultline_node":"-1", "faultline_run":"9"}`,
		"#\t0x105158\truntime/pprof.writeRuntimeProfile+0xb3\t/go/src/runtime/pprof/pprof.go:848",
		"",
		"2 @ 0x1 0x2",
		`# labels: {"faultline_inc":"1", "faultline_node":"1", "faultline_run":"9"}`,
		"#\t0x105168503\tkv.(*Server).replicate+0x43\t/src/kv/server.go:88",
		"#\t0x105168504\tkv.(*Server).Start.func1+0x10\t\t/src/kv/server.go:41",
		"",
		"1 @ 0x1",
		`# labels: {"faultline_inc":"0", "faultline_node":"0", "faultline_run":"9"}`,
		"#\t0x1\tmain.helper+0x1\t/src/main.go:7",
		"",
		"1 @ 0x1",
		`# labels: {"faultline_inc":"1", "faultline_node":"1", "faultline_run":"8"}`,
		"#\t0x1\told.run+0x1\t/src/old.go:1",
		"",
		"1 @ 0x1",
		"#\t0x1\ttesting.tRunner+0xc3\t/go/src/testing/testing.go:2193",
		"",
	}, "\n")
	recs := parseGoroutineProfile(prof)
	if len(recs) != 5 || recs[1].count != 2 || recs[1].labels["faultline_node"] != "1" ||
		!slices.Equal(recs[1].frames, []string{
			"kv.(*Server).replicate (/src/kv/server.go:88)",
			"kv.(*Server).Start.func1 (/src/kv/server.go:41)",
		}) || recs[4].labels != nil {
		t.Fatalf("parsed %+v", recs)
	}
	describe := func(node int64, inc uint64) (string, string) {
		if node == 0 {
			return "", "world"
		}
		return "n1", "crashed at t=12.400000000s"
	}
	groups, sum := groupLeaks(recs, "9", describe)
	if sum != 3 || len(groups) != 2 || groups[0].Status != "world" || groups[1].Node != "n1" || groups[1].Count != 2 {
		t.Fatalf("groups %+v sum %d", groups, sum)
	}
	want := strings.Join([]string{
		"faultline: 4 goroutine(s) still blocked after the run (drain 1m0s)",
		"  world: 1",
		"    1 × main.helper (/src/main.go:7)",
		"  n1 incarnation 1 (crashed at t=12.400000000s): 2",
		"    2 × kv.(*Server).replicate (/src/kv/server.go:88)",
		"          kv.(*Server).Start.func1 (/src/kv/server.go:41)",
		"  unattributed: 1",
	}, "\n")
	if got := leakText(4, time.Minute, groups, 4-sum); got != want {
		t.Fatalf("leakText:\n%s\nwant:\n%s", got, want)
	}
	if got := leakText(3, time.Minute, groups, 0); strings.Contains(got, "unattributed") {
		t.Fatalf("unattributed line printed for 0:\n%s", got)
	}
}

// GOR-067: allStacks grows its buffer from 1 MiB until the dump fits.
func TestAllStacksGrows(t *testing.T) {
	const n = 2000
	release := make(chan struct{})
	defer close(release)
	var deep func(depth int)
	deep = func(depth int) {
		if depth == 0 {
			<-release
			return
		}
		deep(depth - 1)
	}
	var started sync.WaitGroup
	for range n {
		started.Add(1)
		go func() {
			started.Done()
			deep(10)
		}()
	}
	started.Wait()
	dump := allStacks()
	if len(dump) <= stackBufMin || strings.Count(dump, "\ngoroutine ") < n {
		t.Fatalf("dump of %d bytes with %d goroutines, want more than %d bytes and %d goroutines", len(dump), strings.Count(dump, "\ngoroutine "), stackBufMin, n)
	}
}

// GOR-067: a dump's buffer starts at the smallest size of the doubling sequence from 1 MiB that
// held the last dump, never above the cap, and a dump that fills the buffer at the cap is cut.
func TestDumpStacksBuffer(t *testing.T) {
	for _, tc := range []struct {
		last        int64
		limit, want int
	}{
		{0, stackBufMax, 1 << 20},
		{1<<20 - 1, stackBufMax, 1 << 20},
		{1 << 20, stackBufMax, 2 << 20},
		{20 << 20, stackBufMax, 32 << 20},
		{64 << 20, stackBufMax, 64 << 20},
		{1 << 40, stackBufMax, 64 << 20},
		{0, 1 << 10, 1 << 10},
	} {
		if got := stackBufStart(tc.last, tc.limit); got != tc.want {
			t.Errorf("stackBufStart(%d, %d) = %d, want %d", tc.last, tc.limit, got, tc.want)
		}
	}
	if dump, cut := dumpStacks(64); !cut || len(dump) != 64 {
		t.Fatalf("dump with a 64-byte cap: %d bytes, cut %v; want 64 bytes, cut", len(dump), cut)
	}
	if dump, cut := dumpStacks(stackBufMax); cut || !strings.HasPrefix(dump, "goroutine ") || !strings.HasSuffix(dump, "\n") {
		t.Fatalf("dump with the 64 MiB cap: cut %v\n%s", cut, dump)
	}
	if !strings.Contains(dumpCutWarning, "passed 64 MiB,") {
		t.Fatalf("the cut warning does not name the 64 MiB cap: %s", dumpCutWarning)
	}
}

// GOR-067: with a cut dump the drain does not stop on a count of 0, the attempt gets the cut
// warning, and with FailOnLeak a cut dump that counts no goroutine does not fail the attempt.
func TestGoroutineDrainCutDump(t *testing.T) {
	for _, leak := range []bool{false, true} {
		t.Run(fmt.Sprintf("leak=%v", leak), func(t *testing.T) {
			var (
				ga      *goAttempt
				atFinal uint64
			)
			gw(t, Options{Duration: time.Second, Drain: 3 * time.Second, FailOnLeak: true}, func(w *World) {
				ga = w.drv.ga
				w.drv.stackCap = 1 << 8 // below the driver's own block: every dump is cut
				w.AddProcess("p1", func(ctx context.Context, p *Process) error {
					if leak {
						go func() { <-make(chan int) }()
					}
					<-ctx.Done()
					return nil
				})
				w.Final("progress", func() error {
					atFinal = ga.watch.progress.Load()
					return nil
				})
			})
			if got := ga.watch.progress.Load() - atFinal; got != 5 {
				t.Fatalf("drain chunks %d, want 5 (100ms, 200ms, 400ms, 800ms, then 1.5s up to Drain)", got)
			}
			if ga.res.leaks != nil || ga.res.fail != nil || !slices.Equal(ga.res.warnings, []string{dumpCutWarning}) {
				t.Fatalf("leaks %+v failure %+v warnings %q, want no leaks, no failure and the cut warning", ga.res.leaks, ga.res.fail, ga.res.warnings)
			}
		})
	}
}

// GOR-068 step 2: pprof writes label keys and values as Go's %q does, which JSON does not accept
// for every value. Text in another form gives no labels.
func TestParseGoroutineProfileQuotedLabels(t *testing.T) {
	const run, value = "quoted-labels-test", "a\x01\a\v\"}, \"faultline_run\":\"x"
	release, started := make(chan struct{}), make(chan struct{})
	defer close(release)
	go pprof.Do(t.Context(), pprof.Labels("faultline_run", run, "faultline_node", "1", "faultline_inc", "2", "x", value), func(context.Context) {
		close(started)
		<-release
	})
	<-started
	var b bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&b, 1); err != nil {
		t.Fatal(err)
	}
	var got []map[string]string
	for _, r := range parseGoroutineProfile(b.String()) {
		if r.labels["faultline_run"] == run {
			got = append(got, r.labels)
		}
	}
	want := map[string]string{"faultline_run": run, "faultline_node": "1", "faultline_inc": "2", "x": value}
	if len(got) != 1 || !maps.Equal(got[0], want) {
		t.Fatalf("labels %q, want one record with %q\n%s", got, want, b.String())
	}
	for _, s := range []string{`{}`, `{"a":"b"`, `"a":"b"}`, `{"a" "b"}`, `{"a":"b",}`, `{"a":"b", }`, `{"a":b}`, `{"a":"b"; "c":"d"}`, `{"a":"b"  "c":"d"}`} {
		recs := parseGoroutineProfile("1 @ 0x1\n# labels: " + s + "\n")
		if len(recs) != 1 || (s == "{}") != (recs[0].labels != nil) || len(recs[0].labels) != 0 {
			t.Errorf("labels %q parsed as %#v", s, recs)
		}
	}
}

// GOR-068 step 3: groups ascend by (node, inc), and records within a group by frame text. A
// record whose node or incarnation label is not a number belongs to no group.
func TestGroupLeaksOrder(t *testing.T) {
	rec := func(node, inc, fn string) profRecord {
		return profRecord{count: 1, labels: map[string]string{"faultline_run": "1", "faultline_node": node, "faultline_inc": inc},
			frames: []string{fn + " (/a.go:1)"}}
	}
	recs := []profRecord{rec("2", "1", "b"), rec("10", "1", "a"), rec("2", "1", "a"), rec("1", "2", "a"), rec("1", "1", "y"),
		rec("x", "1", "bad"), rec("1", "x", "bad")}
	var called []string
	groups, sum := groupLeaks(recs, "1", func(node int64, inc uint64) (string, string) {
		called = append(called, fmt.Sprintf("%d/%d", node, inc))
		return fmt.Sprintf("n%d", node), "running at the end"
	})
	var got []string
	for _, g := range groups {
		var fs []string
		for _, r := range g.Records {
			fs = append(fs, r.Frames[0])
		}
		got = append(got, fmt.Sprintf("%s/%d:%d:%s", g.Node, g.Inc, g.Count, strings.Join(fs, ",")))
	}
	want := []string{"n1/1:1:y (/a.go:1)", "n1/2:1:a (/a.go:1)", "n2/1:2:a (/a.go:1),b (/a.go:1)", "n10/1:1:a (/a.go:1)"}
	if !slices.Equal(got, want) || sum != 5 || !slices.Equal(called, []string{"1/1", "1/2", "2/1", "10/1"}) {
		t.Fatalf("groups %q sum %d describe calls %v, want %q sum 5", got, sum, called, want)
	}
}

// GOR-068: the groups, unattributed goroutines and stacks of a real bubble. body's goroutine is
// world, a crashed incarnation and a running one are named with their status, the goroutine whose
// labels were replaced is unattributed, and the driver (labels faultline_node=-1) is not counted.
func TestGoroutineLeakReport(t *testing.T) {
	var ga *goAttempt
	gw(t, Options{Duration: time.Second}, func(w *World) {
		ga = w.drv.ga
		go func() { <-make(chan int) }()
		w.AddProcess("p1", func(ctx context.Context, p *Process) error {
			block := make(chan int)
			for range 2 {
				go func() { <-block }()
			}
			go func() { //nolint:gosec // G118: the goroutine drops its labels on purpose
				pprof.SetGoroutineLabels(context.Background())
				<-block
			}()
			<-ctx.Done()
			return nil
		})
		w.AddProcess("p2", func(ctx context.Context, p *Process) error {
			p.Go(func() { <-make(chan int) })
			<-ctx.Done()
			return nil
		})
		w.Plan(fault.Script(fault.Event{At: vt(500 * time.Millisecond), Kind: fault.KindCrash, Node: "p2"}))
	})
	rep := ga.res.leaks
	if rep == nil {
		t.Fatal("no leak report")
	}
	var got []string
	for _, g := range rep.groups {
		got = append(got, fmt.Sprintf("%s/%d %s: %d in %d", g.Node, g.Inc, g.Status, g.Count, len(g.Records)))
	}
	want := []string{"/0 world: 1 in 1", "p1/1 running at the end: 2 in 1", "p2/1 crashed at t=0.500000000s: 1 in 1"}
	if !slices.Equal(got, want) || rep.total != 5 || rep.unattributed != 1 || rep.drain != DefaultDrain {
		t.Fatalf("groups %q total %d unattributed %d drain %v, want %q 5 1 1m0s", got, rep.total, rep.unattributed, rep.drain, want)
	}
	if !strings.HasPrefix(rep.text, "faultline: 5 goroutine(s) still blocked after the run (drain 1m0s)\n  world: 1\n") ||
		!strings.HasSuffix(rep.text, "\n  unattributed: 1") ||
		rep.headline() != "5 goroutine(s) still blocked after the run (drain 1m0s)" {
		t.Fatalf("text:\n%s\nheadline %q", rep.text, rep.headline())
	}
	if l := rep.leaks(); len(l.Groups) != 3 || l.Unattributed != 1 {
		t.Fatalf("leaks() %+v", l)
	}
	// stacks: the five blocks, in goroutine ID order, separated by one blank line.
	blocks := strings.Split(strings.TrimSuffix(rep.stacks, "\n"), "\n\n")
	var prev uint64
	for _, b := range blocks {
		var id uint64
		if _, err := fmt.Sscanf(b, "goroutine %d [", &id); err != nil || id <= prev ||
			!strings.Contains(b, fmt.Sprintf("synctest bubble %d", ga.bubble.Load())) {
			t.Fatalf("stacks block %q (err %v, previous ID %d)\n%s", b, err, prev, rep.stacks)
		}
		prev = id
	}
	if len(blocks) != 5 || !strings.HasSuffix(rep.stacks, "\n") || strings.HasSuffix(rep.stacks, "\n\n") {
		t.Fatalf("stacks has %d blocks:\n%s", len(blocks), rep.stacks)
	}
	if len(ga.res.extra) != 1 || ga.res.extra[0].name != "goroutines.txt" || string(ga.res.extra[0].data) != rep.stacks {
		t.Fatalf("extra files %+v", ga.res.extra)
	}
}

// GOR-066 and GOR-080: the drain sleeps in chunks of 100 ms doubling up to 10 s, counts each
// chunk as progress in phase drain, stops when no bubble goroutine is left or after Drain of
// bubble time, and runs no kernel event.
func TestGoroutineDrainChunks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		drain  time.Duration
		exit   time.Duration // main returns this long after the run ended, at once when <= 0; 0 also leaves a goroutine that never returns
		chunks uint64
		end    time.Duration // bubble time when the drain stopped
	}{
		{"exits during the second chunk", 0, 250 * time.Millisecond, 2, 1300 * time.Millisecond},
		{"exits at the end of the second chunk", 0, 300 * time.Millisecond, 2, 1300 * time.Millisecond},
		{"exits at once", 0, -1, 1, 1100 * time.Millisecond},
		{"Drain 3s", 3 * time.Second, 0, 5, 4 * time.Second},
		{"chunks capped at 10s", 0, 0, 12, 61 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var (
				ws              *watchState
				ga              *goAttempt
				atFinal, events uint64
				w               *World
				last            atomic.Int64
			)
			gw(t, Options{Duration: time.Second, Drain: tc.drain}, func(wd *World) {
				w = wd
				ga = w.drv.ga
				ws = ga.watch
				w.AddProcess("p1", func(ctx context.Context, p *Process) error {
					if tc.exit == 0 { // a goroutine that never returns and records bubble time
						go func() {
							for {
								time.Sleep(100 * time.Millisecond)
								last.Store(int64(time.Since(epoch)))
							}
						}()
					}
					<-ctx.Done()
					if tc.exit > 0 {
						time.Sleep(tc.exit)
					}
					return nil
				})
				w.Sim.At(vt(5*time.Second), "after the end", func() {})
				w.Final("progress", func() error {
					atFinal, events = ws.progress.Load(), w.Sim.Executed()
					return nil
				})
			})
			if got := ws.progress.Load() - atFinal; got != tc.chunks || ws.phase.Load() != watchDrain {
				t.Fatalf("drain chunks %d phase %d, want %d and %d", got, ws.phase.Load(), tc.chunks, watchDrain)
			}
			if tc.exit == 0 && last.Load() != int64(tc.end) {
				t.Fatalf("drain ended at bubble time %v, want %v", time.Duration(last.Load()), tc.end)
			}
			switch rep := ga.res.leaks; {
			case tc.exit != 0 && rep != nil:
				t.Fatalf("leak report after every goroutine exited:\n%s", rep.text)
			case tc.exit == 0 && (rep == nil || rep.total != 1 || rep.drain != tc.end-time.Second):
				t.Fatalf("leak report %+v, want 1 goroutine and drain %v", rep, tc.end-time.Second)
			}
			if w.Sim.Executed() != events || w.Sim.Now() != vt(time.Second) {
				t.Fatalf("kernel after the drain: %d events at %v, want %d at 1s", w.Sim.Executed(), w.Sim.Now(), events)
			}
		})
	}
}

// GOR-040 and GOR-066: after the run ended SyncClock leaves kernel time alone, so a process that
// reads its clock during the drain, after bubble time passed a pending kernel event, gets its
// node's time at the end of the run instead of GOR-021's internal-error panic.
func TestGoroutineClockDuringDrain(t *testing.T) {
	var (
		now, mono, global atomic.Int64
		panicked          atomic.Value
		w                 *World
	)
	gw(t, Options{Duration: time.Second}, func(wd *World) {
		w = wd
		w.AddProcess("p1", func(ctx context.Context, p *Process) error {
			<-ctx.Done()
			time.Sleep(10 * time.Second) // bubble time passes the kernel event at 5s
			defer func() {
				if v := recover(); v != nil {
					panicked.Store(fmt.Sprint(v))
				}
			}()
			c := p.Clock()
			now.Store(int64(c.Now().Sub(epoch)))
			mono.Store(int64(c.Monotonic()))
			global.Store(int64(c.ToGlobal(time.Second)))
			return nil
		}, kernel.WithClock(2*time.Second, 0))
		w.Sim.At(vt(5*time.Second), "after the end", func() {})
	})
	if v := panicked.Load(); v != nil {
		t.Fatalf("reading the clock during the drain panicked: %v", v)
	}
	if now.Load() != int64(3*time.Second) || mono.Load() != int64(time.Second) || global.Load() != int64(time.Second) {
		t.Fatalf("Now %v Monotonic %v ToGlobal %v, want 3s 1s 1s", time.Duration(now.Load()), time.Duration(mono.Load()), time.Duration(global.Load()))
	}
	if w.Sim.Now() != vt(time.Second) {
		t.Fatalf("kernel time %v after the drain, want 1s", w.Sim.Now())
	}
}

// API-061 rows 9 and 10, and row 8a.
func TestAttemptResultLeakFailure(t *testing.T) {
	for _, tc := range []struct {
		res  attemptResult
		pass bool
	}{
		{attemptResult{}, true},
		{attemptResult{limited: true}, true},
		{attemptResult{setupErr: "x"}, false},
		{attemptResult{fail: &failure{kind: "fail"}}, false},
	} {
		if got := tc.res.isPass(); got != tc.pass {
			t.Errorf("isPass(%+v) = %v, want %v", tc.res, got, tc.pass)
		}
	}
	rep := &leakReport{total: 2, drain: 3 * time.Second, text: "faultline: 2 goroutine(s) still blocked after the run (drain 3s)\n  world: 2"}
	res := attemptResult{limited: true, now: vt(1500 * time.Millisecond)}
	res.setLeakFailure(rep)
	f := res.fail
	if f == nil || res.limited || f.kind != "leak" || f.check != "" || f.signature() != "leak:" || f.message != rep.text ||
		f.at != vt(1500*time.Millisecond) || f.hasEvent || f.recordSeq != 0 || f.err != nil ||
		f.headline() != "2 goroutine(s) still blocked after the run (drain 3s)" {
		t.Fatalf("leak failure %+v (limited %v)", f, res.limited)
	}
	if rf := f.report(); rf.Kind != "leak" || rf.Signature != "leak:" || rf.Headline != f.headline() || rf.Event != 0 || rf.RecordSeq != 0 {
		t.Fatalf("report failure %+v", rf)
	}
}

type filesErr map[string][]byte

func (e filesErr) Error() string                    { return "files" }
func (e filesErr) ArtifactFiles() map[string][]byte { return e }

// API-077 item 2: goroutines.txt comes after the failure's files; when one of them already added
// the name, the leak report's file is dropped with a warning naming the first source.
func TestAddLeakFile(t *testing.T) {
	rep := &leakReport{stacks: "goroutine 9 [chan receive (durable), synctest bubble 1]:\nx()\n"}
	var res attemptResult
	addLeakFile(&res, rep)
	if len(res.extra) != 1 || res.extra[0].name != "goroutines.txt" || string(res.extra[0].data) != rep.stacks || res.warnings != nil {
		t.Fatalf("extra %+v warnings %q", res.extra, res.warnings)
	}
	for _, tc := range []struct {
		f     *failure
		first string
	}{
		{&failure{kind: "fail", err: filesErr{"goroutines.txt": []byte("mine")}}, "the seed's failure error"},
		{&failure{kind: "final", finals: []finalResult{{check: "a"}, {check: "b", err: filesErr{"goroutines.txt": []byte("mine")}}}}, `final check "b"`},
	} {
		var res attemptResult
		res.extra, res.warnings = extraFiles(tc.f)
		addLeakFile(&res, rep)
		want := `extra artifact file "goroutines.txt" from the leak report dropped: ` + tc.first + " already added it"
		if len(res.extra) != 1 || string(res.extra[0].data) != "mine" || !slices.Equal(res.warnings, []string{want}) {
			t.Fatalf("extra %+v warnings %q, want only the failure's file and %q", res.extra, res.warnings, want)
		}
	}
	// Another name is kept next to the leak report's file.
	res = attemptResult{}
	res.extra, res.warnings = extraFiles(&failure{kind: "fail", err: filesErr{"a.txt": nil}})
	addLeakFile(&res, rep)
	if len(res.extra) != 2 || res.extra[1].name != "goroutines.txt" || res.warnings != nil {
		t.Fatalf("extra %+v warnings %q", res.extra, res.warnings)
	}
}
