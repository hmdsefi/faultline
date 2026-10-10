// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hmdsefi/faultline/artifact"
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
		if (results == "") != (vars == nil) || p.env.results != results {
			t.Errorf("%v: FAULTLINE_RESULTS %q, then %q", vars, results, p.env.results)
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
	r := &runner{t: t, build: buildInfo{importPath: "p"}, plan: &plan{seedSource: "env", env: &environment{artifactsRoot: cwd}}}
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

// API-083: readReportFile opens report.json only when it is a regular file, so a symbolic link
// is not followed; a missing report.json gives nil.
func TestReadReportFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a")
	if err := artifact.Write(dir, minimalArtifact("T")); err != nil {
		t.Fatal(err)
	}
	if readReportFile(dir) == nil {
		t.Fatal("regular report.json not read")
	}
	link := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, artifact.FileReport), filepath.Join(link, artifact.FileReport)); err != nil {
		t.Fatal(err)
	}
	if rep := readReportFile(link); rep != nil {
		t.Fatalf("symbolic link followed: %+v", rep)
	}
	if rep := readReportFile(t.TempDir()); rep != nil {
		t.Fatalf("missing report.json read: %+v", rep)
	}
}
