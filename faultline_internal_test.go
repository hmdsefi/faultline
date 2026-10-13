// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/artifact"
	"github.com/hmdsefi/faultline/kernel"
)

// API-010: Run reads each variable once, although resultsPath and resolve both look up
// FAULTLINE_RESULTS.
func TestOnceLookup(t *testing.T) {
	// FAULTLINE_RESULTS set, and unset (the usual case): an unset variable is read once too.
	for _, vars := range []map[string]string{{"FAULTLINE_RESULTS": "r.jsonl"}, nil} {
		calls := map[string]int{}
		env := fakeEnv(vars)
		lookup := onceLookup(func(name string) (string, bool) {
			calls[name]++
			return env(name)
		})
		results := resultsPath(lookup)
		p, err := resolve(resolveInput{testName: "TestX", lookup: lookup, exploreBase: func() uint64 { return 0 }})
		if err != nil {
			t.Fatal(err)
		}
		if (results == "") != (vars == nil) || p.env.set[envResults] != (vars != nil) {
			t.Errorf("%v: FAULTLINE_RESULTS %q, set for resolve %v", vars, results, p.env.set[envResults])
		}
		for _, name := range envOrder {
			if calls[name] != 1 {
				t.Errorf("%v: %s looked up %d times, want 1", vars, name, calls[name])
			}
		}
	}
}

// minimalArtifact returns a pass artifact of test in package p for seed 0x1.
func minimalArtifact(test string) *artifact.Artifact {
	return &artifact.Artifact{
		Report: artifact.Report{Package: "p", Test: test, Seed: "0x0000000000000001", Status: "pass"},
		Trace:  &artifact.Trace{},
	}
}

// API-083: with FAULTLINE_ARTIFACTS=off no report is read, not even at the relative path that
// artifact.Dir gives for an empty root.
func TestPreviousReportArtifactsOff(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	if err := artifact.Write(artifact.Dir("", "p", t.Name(), 1), minimalArtifact(t.Name())); err != nil {
		t.Fatal(err)
	}
	r := &runner{t: t, build: buildInfo{importPath: "p"}, plan: &runPlan{seedSource: "env", env: &environment{artifactsRoot: cwd}}}
	if rep, dir := r.previousReport(1); rep == nil || dir != artifact.Dir(cwd, "p", t.Name(), 1) {
		t.Fatalf("with artifacts on: report %+v read from %q", rep, dir)
	}
	r.plan.env = &environment{artifactsOff: true}
	if rep, dir := r.previousReport(1); rep != nil || dir != "" {
		t.Fatalf("read with artifacts off: %+v from %q", rep, dir)
	}
}

// API-074: the default artifact root is one folder per user, or faultline without user IDs.
func TestDefaultArtifactRoot(t *testing.T) {
	tmp := filepath.Join("x", "tmp")
	if got, want := defaultArtifactRoot(tmp, 1000), filepath.Join(tmp, "faultline-1000"); got != want {
		t.Errorf("uid 1000: %q, want %q", got, want)
	}
	if got, want := defaultArtifactRoot(tmp, 0), filepath.Join(tmp, "faultline-0"); got != want {
		t.Errorf("uid 0: %q, want %q", got, want)
	}
	if got, want := defaultArtifactRoot(tmp, -1), filepath.Join(tmp, "faultline"); got != want {
		t.Errorf("no user IDs: %q, want %q", got, want)
	}
}

// API-073, API-083: readReportFile opens report.json only when it is a regular file, so a symbolic
// link is not followed and gives a reason; a missing report.json, or one out of reach, gives nil
// and no reason.
func TestReadReportFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a")
	if err := artifact.Write(dir, minimalArtifact("T")); err != nil {
		t.Fatal(err)
	}
	if rep, err := readReportFile(dir); rep == nil || err != nil {
		t.Fatalf("regular report.json not read: %v", err)
	}
	link := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, artifact.FileReport), filepath.Join(link, artifact.FileReport)); err != nil {
		t.Fatal(err)
	}
	if rep, err := readReportFile(link); rep != nil || err == nil || err.Error() != "not a regular file" {
		t.Fatalf("symbolic link: %+v, %v", rep, err)
	}
	if rep, err := readReportFile(t.TempDir()); rep != nil || err != nil {
		t.Fatalf("missing report.json: %+v, %v", rep, err)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if rep, err := readReportFile(filepath.Join(file, "a")); rep != nil || err != nil {
		t.Fatalf("a path through a file: %+v, %v", rep, err)
	}
	// A report.json that cannot be opened gives the open error.
	if err := os.Chmod(filepath.Join(dir, artifact.FileReport), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(filepath.Join(dir, artifact.FileReport)); err == nil {
		t.Skip("report.json still opens without permissions (root, or a file system without them)")
	}
	if rep, err := readReportFile(dir); rep != nil || err == nil || !strings.HasPrefix(err.Error(), "open ") {
		t.Fatalf("report.json without permissions: %+v, %v", rep, err)
	}
}

// API-073, API-083: readReportFile does not open a FIFO report.json, so a FIFO cannot block the
// run. The test sets no time limit: if readReportFile opened the FIFO, go test's -timeout would
// stop the test binary and print the stack of every goroutine, this one blocked in the open.
func TestReadReportFileFIFO(t *testing.T) {
	dir := t.TempDir()
	switch ok, err := mkfifo(filepath.Join(dir, artifact.FileReport)); {
	case !ok:
		t.Skip("this platform has no FIFOs")
	case err != nil:
		t.Skipf("cannot create a FIFO here: %v", err)
	}
	if rep, err := readReportFile(dir); rep != nil || err == nil || err.Error() != "not a regular file" {
		t.Fatalf("FIFO report.json: %+v, %v", rep, err)
	}
}

// API-021: the stop line counts the seeds not run, unless -run or -skip may have excluded some.
func TestStopLine(t *testing.T) {
	const counted = "faultline: stopping after failing seed 0x0000000000000007; 2 of 3 seeds not run (set Options.KeepGoing to run all)"
	const filtered = "faultline: stopping after failing seed 0x0000000000000007; any later seeds that -run and -skip allow were not run (set Options.KeepGoing to run them)"
	cases := []struct {
		i, total int
		filtered bool
		want     string
	}{
		{0, 3, false, counted},
		{0, 3, true, filtered},
		{1, 3, false, "faultline: stopping after failing seed 0x0000000000000007; 1 of 3 seeds not run (set Options.KeepGoing to run all)"},
		{2, 3, false, ""}, // the last seed failed: none was left
		{2, 3, true, ""},
		{0, 1, false, ""},
	}
	for _, c := range cases {
		if got := stopLine(7, c.i, c.total, c.filtered); got != c.want {
			t.Errorf("stopLine(seed %d of %d, filtered %v) = %q, want %q", c.i, c.total, c.filtered, got, c.want)
		}
	}
}

// API-021: a -run or -skip pattern may exclude seed subtests only when it has a part for their
// level, one more '/' than the name of the test that calls Run.
func TestSeedFilter(t *testing.T) {
	cases := []struct {
		name, run, skip string
		want            bool
	}{
		{"TestKV", "", "", false},
		{"TestKV", "^TestKV$", "", false},
		{"TestKV", "^TestKV$/^seed=0x287372ab06f1482e$", "", true},
		{"TestKV", "", "^TestOther$", false},
		{"TestKV", "", "TestKV/seed=0x287372ab06f1482e", true},
		{"TestKV/raft", "TestKV/raft", "", false}, // selects the test that calls Run, not its seeds
		{"TestKV/raft", "TestKV/raft/seed=0x1", "", true},
		{"TestKV/raft", "", "TestKV/raft/seed=0x1", true},
	}
	for _, c := range cases {
		if got := seedFilter(c.name, c.run, c.skip); got != c.want {
			t.Errorf("seedFilter(%q, -run %q, -skip %q) = %v, want %v", c.name, c.run, c.skip, got, c.want)
		}
	}
}

// faultline.testflag is a flag that TestTestFlag sets, so that its value differs from its default
// in every run, with or without -run.
var _ = flag.String("faultline.testflag", "default", "set by TestTestFlag")

// API-021: testFlag reads the testing flags that seedFilter needs.
func TestTestFlag(t *testing.T) {
	if got, want := testFlag("test.run"), flag.Lookup("test.run").Value.String(); got != want {
		t.Errorf("testFlag(test.run) = %q, want %q", got, want)
	}
	// Not parallel: sets a flag.
	t.Cleanup(func() { _ = flag.Set("faultline.testflag", "default") })
	if err := flag.Set("faultline.testflag", "x"); err != nil {
		t.Fatal(err)
	}
	if got := testFlag("faultline.testflag"); got != "x" {
		t.Errorf("testFlag(faultline.testflag) = %q, want x (its value, not its default)", got)
	}
	if got := testFlag("test.no-such-flag"); got != "" {
		t.Errorf("testFlag of an undefined flag = %q, want \"\"", got)
	}
}

// API-073: the pass line says "1 event" for a run of one event.
func TestPassLine(t *testing.T) {
	for _, c := range []struct {
		executed uint64
		want     string
	}{
		{1, "faultline: seed 0x0000000000000007 passed: 1 event, ended at t=2.000000000s (deadline)"},
		{680, "faultline: seed 0x0000000000000007 passed: 680 events, ended at t=2.000000000s (deadline)"},
	} {
		if got := passLine(7, attemptResult{executed: c.executed, now: kernel.Time(2 * time.Second), stop: "deadline"}); got != c.want {
			t.Errorf("passLine(%d events) = %q, want %q", c.executed, got, c.want)
		}
	}
}
