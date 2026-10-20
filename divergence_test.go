// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"context"
	"errors"
	"math/rand/v2"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
)

var gor22Attempts, gorStoppedAttempts atomic.Int32

func init() { //nolint:gochecknoinits // registers the child-process scenarios
	gorScenarios["gor-22"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 5 * time.Second}, func(w *World) {
			w.AddServer("e1", func(n *kernel.Node) { n.After(2*time.Second, "two", func() {}) })
			w.AddProcess("p1", func(ctx context.Context, p *Process) error {
				n := gor22Attempts.Add(1)
				time.Sleep(time.Second)
				if n == 1 {
					p.Logf("extra")
				}
				<-ctx.Done()
				return nil
			})
			w.Invariant("before two seconds", func() error {
				if w.Sim.Now() >= kernel.Time(2*time.Second) {
					return errors.New("reached two seconds")
				}
				return nil
			})
		})
	}
	gorScenarios["gor-level3"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 20, Duration: time.Second, CheckDeterminism: true, KeepGoing: true},
			func(w *World) {
				w.AddProcess("p1", func(ctx context.Context, p *Process) error {
					time.Sleep(100 * time.Millisecond)
					p.Logf("one")
					if rand.IntN(2) == 1 { //nolint:gosec // global math/rand/v2: not controlled (level 3)
						p.Logf("two")
					}
					<-ctx.Done()
					return nil
				})
			})
	}
	// The check attempt (the second attempt) stops before its first event (API-072).
	gorScenarios["gor-level3-stopped"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: time.Second, CheckDeterminism: true},
			func(w *World) {
				if gorStoppedAttempts.Add(1) == 2 {
					w.T().SkipNow()
				}
			})
	}
}

func TestATGOR22_DivergenceLocalized(t *testing.T) {
	res := runGorScenario(t, "gor-22", "FAULTLINE_SEED=0x1")
	if res.code != 1 || !strings.Contains(res.out, "determinism failure: re-running seed 0x0000000000000001") ||
		!strings.Contains(res.out, "only the first run differed") {
		t.Fatalf("exit %d\n%s", res.code, res.out)
	}
	m := regexp.MustCompile(`the first run diverged before or during event (\d+): #\d+ t=(\d+)\.(\d{9})s`).FindStringSubmatch(res.out)
	if m == nil {
		t.Fatalf("no localization line\n%s", res.out)
	}
	sec, _ := strconv.Atoi(m[2])
	if sec < 1 {
		t.Fatalf("localized at t=%s.%ss, want >= 1s", m[2], m[3])
	}
	if !strings.Contains(res.out, gorLevel3Line) {
		t.Fatalf("no level-3 line\n%s", res.out)
	}
}

// GOR-077, AT-GOR-21 (with Logf instead of UDP): in the check context the level-3 line sits right
// after API-071's line 2 and right before its line 3. Line 2 depends on the global draws: the
// "first difference" block when the two full-trace runs differ, else the "identical" line.
func TestGoroutineLevel3LineInCheckContext(t *testing.T) {
	res := runGorScenario(t, "gor-level3")
	if res.code != 1 || !strings.Contains(res.out, "determinism failure: two runs of seed") ||
		!strings.Contains(res.out, gorLevel3Line) {
		t.Fatalf("exit %d\n%s", res.code, res.out)
	}
	placed := regexp.MustCompile(`(?m)^\s*(?:first difference at record #\d+:\n\s*run 3: .*\n\s*run 4: .*|the two full-trace runs were identical .*)\n\s*` +
		regexp.QuoteMeta(gorLevel3Line) + `\n\s*common causes: `)
	if n, want := len(placed.FindAllString(res.out, -1)), strings.Count(res.out, gorLevel3Line); n != want {
		t.Fatalf("%d of %d level-3 lines in place\n%s", n, want, res.out)
	}
}

// GOR-077, API-072: the message of a check attempt that stopped (laterDeterminism) also gets the
// level-3 line, after its stop line and before the common causes.
func TestGoroutineLevel3LineInStoppedCheck(t *testing.T) {
	res := runGorScenario(t, "gor-level3-stopped", "FAULTLINE_SEED=0x1")
	before, after, ok := strings.Cut(res.out, gorLevel3Line)
	if res.code != 1 || !ok || !strings.Contains(before, "the determinism check stopped by t.SkipNow or t.Skip") ||
		!strings.Contains(after, "common causes:") {
		t.Fatalf("exit %d\n%s", res.code, res.out)
	}
}

func TestGoroutineConsoleDeterminismLine(t *testing.T) {
	res := runGorScenario(t, "gor-07", "FAULTLINE_SEED=0x1")
	re := regexp.MustCompile(`(?m)^\s*artifacts: .*\n\s*determinism: level 3 \(goroutine mode, best-effort replay\)$`)
	if !re.MatchString(res.out) {
		t.Fatalf("no determinism line after the artifacts line\n%s", res.out)
	}
	if !strings.Contains(res.artifact(t, 1).Text, gorConsoleLine) {
		t.Fatal("report.txt lacks the determinism line")
	}
}

func TestDivergenceLine(t *testing.T) {
	rs := []kernel.Record{
		{Seq: 1, Kind: "kernel.start", Text: "start"},
		{Seq: 2, Kind: "kernel.event", Text: "boot", Node: 1, Inc: 1, Attrs: []kernel.Attr{{Key: "id", Value: "1"}}},
		{Seq: 3, Kind: "kernel.event", Text: "boot", Node: 2, Inc: 1, Attrs: []kernel.Attr{{Key: "id", Value: "2"}}},
		{Seq: 4, At: kernel.Time(2 * time.Second), Kind: "kernel.event", Text: "two", Node: 1, Inc: 1, Attrs: []kernel.Attr{{Key: "id", Value: "3"}}},
	}
	want := "the first run diverged before or during event 3: " + recordLine(rs[3])
	if got := divergenceLine([]uint64{1, 2, 3, 4}, []uint64{1, 2, 9}, rs); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := divergenceLine([]uint64{1, 2}, []uint64{1, 2, 9}, rs); got != want {
		t.Fatalf("prefix case: got %q, want %q", got, want)
	}
	if got := divergenceLine([]uint64{1, 2, 3}, []uint64{1, 2, 3}, rs); got != "" {
		t.Fatalf("equal checkpoints: got %q, want empty", got)
	}
	if got := divergenceLine([]uint64{1}, []uint64{2}, rs[1:]); got != "" {
		t.Fatalf("records not starting at Seq 1: got %q, want empty", got)
	}
}

// GOR-076, GOR-077, API-071: in goroutine mode the localization line follows line 2 and the
// level-3 line comes right before line 3. The localization line needs the artifact re-run context
// with A3 equal to A2; event mode gets neither line.
func TestGoroutineDeterminismMessage(t *testing.T) {
	rs := []kernel.Record{
		{Seq: 1, Kind: "kernel.start", Text: "start"},
		{Seq: 2, Kind: "kernel.event", Text: "boot", Node: 1, Inc: 1},
		{Seq: 3, At: kernel.Time(time.Second), Kind: "kernel.event", Text: "tick", Node: 1, Inc: 1},
	}
	a1 := attemptResult{hash: 1, checkpoints: []uint64{10, 11}}
	a2 := attemptResult{hash: 2, checkpoints: []uint64{10, 12}, records: rs}
	orig := &failure{kind: "invariant", check: "i", context: "invariant", message: "x=1"}
	tail := []string{gorLevel3Line, commonCauses, nextStep}

	f := newDeterminismFailure("artifact_rerun", 1, []attemptResult{a1, a2, {hash: 2}}, orig, rs, rs, a2, true)
	want := strings.Join(append([]string{
		`the first run failed: invariant "i" violated at t=0.000000000s`,
		"  x=1",
		"the two full-trace runs were identical (trace hash 0x0000000000000002); only the first run differed",
		"the first run diverged before or during event 2: " + recordLine(rs[2]),
	}, tail...), "\n")
	if f.message != want {
		t.Fatalf("artifact re-run, A3 == A2:\n%s\nwant:\n%s", f.message, want)
	}
	if f.determinism.divergence != "the first run diverged before or during event 2: "+recordLine(rs[2]) {
		t.Errorf("divergence %q", f.determinism.divergence)
	}

	// A3 differs from A2: no localization line.
	f = newDeterminismFailure("artifact_rerun", 1, []attemptResult{a1, a2, {hash: 3}}, nil, rs, rs, a2, true)
	want = strings.Join(append([]string{
		"the two full-trace runs differ before the last 3 records they kept (trace hashes 0x0000000000000002 and 0x0000000000000003); the kept records are identical",
	}, tail...), "\n")
	if f.message != want {
		t.Fatalf("artifact re-run, A3 != A2:\n%s\nwant:\n%s", f.message, want)
	}

	// The check context: no localization line, the level-3 line after the first-difference block.
	b := slices.Clone(rs)
	b[2].Text = "tock"
	f = newDeterminismFailure("check_determinism", 1, []attemptResult{a1, a2, {hash: 3, checkpoints: []uint64{10, 11}}, {hash: 4, checkpoints: []uint64{10, 13}}}, nil, rs, b, a2, true)
	want = strings.Join(append([]string{
		"first difference at record #3:",
		"  run 3: " + recordLine(rs[2]),
		"  run 4: " + recordLine(b[2]),
	}, tail...), "\n")
	if f.message != want {
		t.Fatalf("check context:\n%s\nwant:\n%s", f.message, want)
	}

	// Event mode: neither line.
	f = newDeterminismFailure("artifact_rerun", 1, []attemptResult{a1, a2, {hash: 2}}, nil, rs, rs, a2, false)
	if strings.Contains(f.message, "diverged before or during") || strings.Contains(f.message, gorLevel3Line) {
		t.Fatalf("event mode:\n%s", f.message)
	}
}

// GOR-077, API-072: every message of a stopped check or diagnostic attempt gets the level-3 line
// right before line 3 in goroutine mode, and not in event mode.
func TestGoroutineLaterDeterminismLevel3(t *testing.T) {
	orig := &failure{kind: "invariant", check: "i", context: "invariant", message: "x=1"}
	for _, c := range []struct {
		name string
		res  seedResult
	}{
		{"determinism check", seedResult{seed: 1, attempts: 2, hashes: []uint64{5}}},
		{"diagnostic re-run", seedResult{seed: 1, attempts: 3, hashes: []uint64{5, 6}, primary: orig}},
		{"diagnostic re-run", seedResult{seed: 1, attempts: 4, hashes: []uint64{5, 6, 7}}},
	} {
		res := c.res
		lines := strings.Split(laterDeterminism(&res, c.name, "stopped by t.FailNow or t.Fatal before its first event", 0, true).message, "\n")
		if n := len(lines); n < 4 || !strings.HasPrefix(lines[n-4], "the "+c.name+" stopped by ") ||
			!slices.Equal(lines[n-3:], []string{gorLevel3Line, commonCauses, nextStep}) {
			t.Errorf("%s, attempt %d:\n%s", c.name, res.attempts, strings.Join(lines, "\n"))
		}
		if m := laterDeterminism(&res, c.name, "stopped by t.FailNow or t.Fatal before its first event", 0, false).message; strings.Contains(m, gorLevel3Line) {
			t.Errorf("event mode, %s, attempt %d:\n%s", c.name, res.attempts, m)
		}
	}
}

// GOR-077, API-075: the determinism line comes right after the artifacts line, before the
// warnings, and only in goroutine mode.
func TestGoroutineConsoleLinesOrder(t *testing.T) {
	f := &failure{kind: "fail", message: "boom"}
	got := consoleLines(f, "CMD", "/a/", []string{"w1"}, false, true, true)
	if n := len(got); n < 4 || !slices.Equal(got[n-4:], []string{"replay:    CMD", "artifacts: /a/", gorConsoleLine, "warning: w1"}) {
		t.Fatalf("goroutine mode: %q", got)
	}
	if got := consoleLines(f, "CMD", "/a/", []string{"w1"}, false, true, false); slices.Contains(got, gorConsoleLine) {
		t.Fatalf("event mode: %q", got)
	}
}
