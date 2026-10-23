// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/artifact"
)

// exact-plan: three seeds of the ticker; the body records each seed it runs.
var _ = scenario("exact-plan", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Seeds: 3, Duration: time.Second}, func(w *faultline.World) {
		addTicker(w)
		if os.Getenv("OUT") != "" {
			outLine(t, fmt.Sprintf("body 0x%016x", w.Seed()))
		}
	})
})

// exact-run: goroutine mode with determinism checking on; the body records every attempt.
var _ = scenario("exact-run", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Mode: faultline.ModeGoroutine, Seeds: 1, Duration: time.Second, CheckDeterminism: true},
		func(w *faultline.World) {
			addTicker(w)
			outLine(t, "body")
			if os.Getenv("ROW") == "fail" {
				w.Invariant("always fails", func() error { return errors.New("boom") })
			}
		})
})

// planLines returns the plan lines of out, in order.
func planLines(out string) []string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "faultline-exact-plan\t") {
			lines = append(lines, l)
		}
	}
	return lines
}

func TestExactPlanMode(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	o, code := runScenario(t, "exact-plan", []string{"FAULTLINE_EXACT=plan", "OUT=" + out})
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, o)
	}
	want := []string{
		"faultline-exact-plan\tTestScenario\t0x9a33dad17ee0bb7d",
		"faultline-exact-plan\tTestScenario\t0x8a216e8699751f87",
		"faultline-exact-plan\tTestScenario\t0xbfa29075015c7f9a",
	}
	if got := planLines(o); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("plan lines %q, want %q\n%s", got, want, o)
	}
	if !strings.Contains(o, "--- SKIP: TestScenario") || !strings.Contains(o, "faultline: exact backend plan instance") {
		t.Fatalf("TestScenario was not skipped\n%s", o)
	}
	if strings.Contains(o, "TestScenario/seed=") {
		t.Fatalf("a seed subtest started in the plan instance\n%s", o)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("the body ran in the plan instance (OUT exists: %v)", err)
	}
}

func TestExactPlanModeDerivedSeeds(t *testing.T) {
	o, code := runScenario(t, "exact-plan", []string{"FAULTLINE_EXACT=plan",
		"FAULTLINE_BASE_SEED=0x0e0b5eed00000001", "FAULTLINE_SEEDS=2"})
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, o)
	}
	base := uint64(0x0e0b5eed00000001)
	want := []string{
		fmt.Sprintf("faultline-exact-plan\tTestScenario\t0x%016x", faultline.DeriveSeed(base, 0)),
		fmt.Sprintf("faultline-exact-plan\tTestScenario\t0x%016x", faultline.DeriveSeed(base, 1)),
	}
	if want[0] != "faultline-exact-plan\tTestScenario\t0xafb28507f36457eb" ||
		want[1] != "faultline-exact-plan\tTestScenario\t0x75a9d0b00437271e" {
		t.Fatalf("DeriveSeed disagrees with the pinned EXB seeds: %q", want)
	}
	if got := planLines(o); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("plan lines %q, want %q\n%s", got, want, o)
	}
}

func TestExactInvalidValue(t *testing.T) {
	o, code := runScenario(t, "exact-plan", []string{"FAULTLINE_EXACT=bogus"})
	if code != 1 || !strings.Contains(o, `faultline: FAULTLINE_EXACT="bogus": want plan or run`) ||
		strings.Contains(o, "TestScenario/seed=") {
		t.Fatalf("exit %d\n%s", code, o)
	}
}

func TestExactRunModePass(t *testing.T) {
	dir := t.TempDir()
	out, results, arts := filepath.Join(dir, "out"), filepath.Join(dir, "results.jsonl"), filepath.Join(dir, "artifacts")
	o, code := runScenario(t, "exact-run", []string{"FAULTLINE_EXACT=run", "FAULTLINE_SEED=0x2a",
		"FAULTLINE_RESULTS=" + results, "FAULTLINE_TRACE=full", "FAULTLINE_ARTIFACTS=" + arts, "OUT=" + out})
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, o)
	}
	if got := readLines(t, out); len(got) != 1 {
		t.Fatalf("the body ran %d times, want 1 (run instances skip the determinism re-run)", len(got))
	}
	lines := resultLines(t, results)
	if len(lines) != 1 || lines[0]["status"] != "pass" || lines[0]["determinism_level"] != "exact" ||
		lines[0]["exact_backend"] != "wasip1" {
		t.Fatalf("results %v", lines)
	}
	rep := readReport(t, seedDir(arts, 0x2a))
	if rep.DeterminismLevel != "exact" || rep.ExactBackend != "wasip1" || rep.Run.Attempts != 1 {
		t.Fatalf("report determinism_level=%q exact_backend=%q attempts=%d", rep.DeterminismLevel, rep.ExactBackend, rep.Run.Attempts)
	}
}

func TestExactRunModeFail(t *testing.T) {
	dir := t.TempDir()
	out, arts := filepath.Join(dir, "out"), filepath.Join(dir, "artifacts")
	o, code := runScenario(t, "exact-run", []string{"FAULTLINE_EXACT=run", "FAULTLINE_SEED=0x2a",
		"FAULTLINE_ARTIFACTS=" + arts, "OUT=" + out, "ROW=fail"})
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, o)
	}
	if got := readLines(t, out); len(got) != 1 {
		t.Fatalf("the body ran %d times, want 1 (no artifact re-run)", len(got))
	}
	if !strings.Contains(o, "determinism: exact (wasip1 backend)") || strings.Contains(o, "determinism: level 3") {
		t.Fatalf("console determinism line\n%s", o)
	}
	a, err := artifact.Read(seedDir(arts, 0x2a))
	if err != nil {
		t.Fatal(err)
	}
	if a.Report.Run.Attempts != 1 || a.Report.DeterminismLevel != "exact" || a.Report.ExactBackend != "wasip1" {
		t.Fatalf("report %+v", a.Report.Run)
	}
	if len(a.Trace.Records) == 0 {
		t.Fatal("the primary attempt of a run instance must keep the full trace (EXB-015)")
	}
	if !strings.Contains(a.Text, "determinism: exact (wasip1 backend)") {
		t.Fatalf("report.txt lacks the exact line:\n%s", a.Text)
	}
}
