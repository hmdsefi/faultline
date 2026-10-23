// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// EXB-065: every C1 and C6 test runs once in the backend's normal mode (no FAULTLINE_SEED), with
// FAULTLINE_RESULTS and FAULTLINE_ARTIFACTS=off. A test passes when it exits 0, or, for a
// scenario entry point, when the parent test's assertion (Check) accepts the run; the gRPC test
// is reported, not gated. A failing test whose every results line passed is marked, because under
// wasm that is a test that checks several seeds after Run (EXB-065, EXB-081).
func TestCompatResults(t *testing.T) {
	work := t.TempDir()
	c := &config{backend: "parta", work: work, root: work, out: filepath.Join(t.TempDir(), "compat-parta.csv")}
	log := filepath.Join(t.TempDir(), "runs.log")
	var checked []string
	check := func(want error) func(int, string, []map[string]any) error {
		return func(code int, out string, results []map[string]any) error {
			var st []string
			for _, l := range results {
				st = append(st, str(l, "status"))
			}
			checked = append(checked, fmt.Sprintf("exit %d, output %q, statuses %v", code, out, st))
			return want
		}
	}
	pass := measureCase(t, c, "TestPass", "EXBMEASURE_HELPER_FILE="+log, "EXBMEASURE_HELPER_STATUSES=pass")
	fail := measureCase(t, c, "TestFail", "EXBMEASURE_HELPER_STATUSES=pass,fail", "EXBMEASURE_HELPER_EXIT=1")
	afterRun := measureCase(t, c, "TestAfterRun", "EXBMEASURE_HELPER_STATUSES=pass,pass", "EXBMEASURE_HELPER_EXIT=1")
	noLines := measureCase(t, c, "TestNoLines", "EXBMEASURE_HELPER_EXIT=2")
	tolerant := measureCase(t, c, "TestScenarioTolerant", "EXBMEASURE_HELPER_STATUSES=pass,fail", "EXBMEASURE_HELPER_EXIT=1",
		"EXBMEASURE_HELPER_PRINT=scenario output")
	tolerant.Check = check(nil)
	strict := measureCase(t, c, "TestScenarioStrict", "EXBMEASURE_HELPER_STATUSES=pass")
	strict.Check = check(errors.New("assertion failed"))
	setCorpus(t, "C1", pass, fail, afterRun, noLines, tolerant, strict)
	c6 := measureCase(t, c, "TestCompatSync", "EXBMEASURE_HELPER_STATUSES=pass")
	grpc := measureCase(t, c, "TestCompatGRPC", "EXBMEASURE_HELPER_EXIT=1")
	grpc.Gated = false
	setCorpus(t, "C6", c6, grpc)

	if err := compat(c); !errors.Is(err, errRequirement) {
		t.Fatalf("compat = %v, want errRequirement", err)
	}
	b, err := os.ReadFile(c.out)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		f := strings.Split(l, ",")
		if len(f) != 3 {
			t.Fatalf("compat line %q", l)
		}
		got = append(got, f[0]+","+f[1])
	}
	want := []string{"test,result",
		"TestPass,pass", "TestFail,fail", "TestAfterRun,fail (all seeds passed)", "TestNoLines,fail",
		"TestScenarioTolerant,pass", "TestScenarioStrict,fail",
		"TestCompatSync,pass", "TestCompatGRPC,fail (not gated)"}
	if !slices.Equal(got, want) {
		t.Fatalf("compat-parta.csv\n%q\nwant\n%q", got, want)
	}
	wantChecked := []string{`exit 1, output "scenario output", statuses [pass fail]`, `exit 0, output "PASS\n", statuses [pass]`}
	if !slices.Equal(checked, wantChecked) {
		t.Fatalf("Check saw %q, want %q", checked, wantChecked)
	}
	res := filepath.Join(work, "parta", "compat", "TestPass.jsonl")
	wantRun := "run TestPass\t-test.run ^TestHelperProcess$ -test.count=1\tFAULTLINE_ARTIFACTS=off FAULTLINE_RESULTS=" + res
	if runs := readLog(t, log); !slices.Equal(runs, []string{wantRun}) {
		t.Fatalf("runs %q, want %q", runs, wantRun)
	}
	for name, kept := range map[string]bool{"TestPass": false, "TestFail": true, "TestAfterRun": true, "TestCompatGRPC": true} {
		_, err := os.Stat(filepath.Join(work, "parta", "compat", name+".log"))
		if (err == nil) != kept {
			t.Errorf("%s.log kept = %v, want %v", name, err == nil, kept)
		}
	}
}

// Only gated failures count: with every gated test passing, a failing gRPC test still passes the
// requirement.
func TestCompatUngatedFailurePasses(t *testing.T) {
	work := t.TempDir()
	c := &config{backend: "toolexec", work: work, root: work, out: filepath.Join(t.TempDir(), "compat-toolexec.csv")}
	setCorpus(t, "C1", measureCase(t, c, "TestPass"))
	grpc := measureCase(t, c, "TestCompatGRPC", "EXBMEASURE_HELPER_EXIT=1")
	grpc.Gated = false
	setCorpus(t, "C6", grpc)
	if err := compat(c); err != nil {
		t.Fatalf("compat = %v, want nil", err)
	}
	c.backend = "native"
	if err := compat(c); err == nil || errors.Is(err, errRequirement) {
		t.Fatalf("compat -backend native = %v, want a tool error", err)
	}
}

// One gated failure fails the requirement. A run that the run limit ended is such a failure, and
// its log says why it ended.
func TestCompatOneGatedFailureFails(t *testing.T) {
	old := runTimeout
	runTimeout = time.Second
	t.Cleanup(func() { runTimeout = old })
	work := t.TempDir()
	c := &config{backend: "parta", work: work, root: work, out: filepath.Join(t.TempDir(), "compat-parta.csv")}
	setCorpus(t, "C1", measureCase(t, c, "TestPass"), measureCase(t, c, "TestSlow", "EXBMEASURE_HELPER_NAP=20000"))
	setCorpus(t, "C6", measureCase(t, c, "TestCompatSync"))
	if err := compat(c); !errors.Is(err, errRequirement) {
		t.Fatalf("compat = %v, want errRequirement", err)
	}
	b, err := os.ReadFile(c.out)
	if err != nil || !strings.Contains(string(b), "TestPass,pass,") || !strings.Contains(string(b), "TestSlow,fail,") ||
		!strings.Contains(string(b), "TestCompatSync,pass,") {
		t.Fatalf("compat-parta.csv %q, %v", b, err)
	}
	b, err = os.ReadFile(filepath.Join(work, "parta", "compat", "TestSlow.log"))
	if err != nil || !strings.HasPrefix(string(b), "exit -1 (timeout after 1s)\n") {
		t.Fatalf("TestSlow.log %q, %v; want it to start with the timeout", b, err)
	}
}

// EXB-050 and EXB-055: C1 is the 51 Part A tests, all gated; C6's gRPC test is the only one not
// gated.
func TestCompatCorpusGating(t *testing.T) {
	if n := len(corpora["C1"]); n != 51 {
		t.Fatalf("C1 has %d tests, want 51", n)
	}
	for _, tc := range corpora["C1"] {
		if !tc.Gated {
			t.Errorf("C1 test %s is not gated", tc.Name)
		}
	}
	for _, tc := range corpora["C6"] {
		if tc.Gated != (tc.Name != "TestCompatGRPC") {
			t.Errorf("C6 test %s: gated = %v", tc.Name, tc.Gated)
		}
	}
}

// The scenario entry points apply their parent tests' assertions: AT-GOR-30 reads only the 50
// results lines and allows one determinism failure (the child exits 1 then); AT-NSM-27 needs exit
// 0 and nsm27-ok=true without "still blocked".
func TestScenarioChecks(t *testing.T) {
	var gor30, nsm27 testCase
	for _, tc := range corpora["C1"] {
		switch tc.Name {
		case "TestATGOR30_EchoReplayRate":
			gor30 = tc
		case "TestATNSM27_RunEndUnblocks":
			nsm27 = tc
		}
	}
	if gor30.Check == nil || nsm27.Check == nil {
		t.Fatal("AT-GOR-30 or AT-NSM-27 has no Check")
	}
	lines := func(n, det, other int) []map[string]any {
		var ls []map[string]any
		for i := range n {
			switch {
			case i < det:
				ls = append(ls, map[string]any{"status": "fail", "kind": "determinism"})
			case i < det+other:
				ls = append(ls, map[string]any{"status": "fail", "kind": "invariant"})
			default:
				ls = append(ls, map[string]any{"status": "pass"})
			}
		}
		return ls
	}
	for _, tc := range []struct {
		name          string
		code          int
		n, det, other int
		ok            bool
	}{
		{"all pass", 0, 50, 0, 0, true},
		{"one determinism failure", 1, 50, 1, 0, true},
		{"two determinism failures", 1, 50, 2, 0, false},
		{"other failure", 1, 50, 0, 1, false},
		{"49 lines", 0, 49, 0, 0, false},
		{"killed", -1, 20, 0, 0, false},
	} {
		if err := gor30.Check(tc.code, "", lines(tc.n, tc.det, tc.other)); (err == nil) != tc.ok {
			t.Errorf("AT-GOR-30 %s: Check = %v, want ok %v", tc.name, err, tc.ok)
		}
	}
	for _, tc := range []struct {
		name string
		code int
		out  string
		ok   bool
	}{
		{"ok", 0, "nsm27-ok=true (x)", true},
		{"exit 1", 1, "nsm27-ok=true (x)", false},
		{"not ok", 0, "nsm27-ok=false (x)", false},
		{"still blocked", 0, "nsm27-ok=true\nstill blocked", false},
	} {
		if err := nsm27.Check(tc.code, tc.out, nil); (err == nil) != tc.ok {
			t.Errorf("AT-NSM-27 %s: Check = %v, want ok %v", tc.name, err, tc.ok)
		}
	}
}
