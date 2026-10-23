// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// stubCommand replaces the procedure of cmd for the test; it returns err and stores the config it
// was called with in the returned value.
func stubCommand(t *testing.T, cmd string, err error) *config {
	t.Helper()
	saved := commands[cmd]
	t.Cleanup(func() { commands[cmd] = saved })
	got := &config{}
	commands[cmd] = func(c *config) error {
		*got = *c
		return err
	}
	return got
}

// EXB §4.3: exit code 0 with "<cmd>: PASS" when the requirement is met, 1 with "<cmd>: FAIL" when
// it is not, and 2 on a tool error.
func TestRunExitCodes(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name   string
		err    error
		code   int
		stdout string
	}{
		{"met", nil, 0, "deps: PASS\n"},
		{"not met", errRequirement, 1, "deps: FAIL\n"},
		{"not met, wrapped", fmt.Errorf("deps: %w", errRequirement), 1, "deps: FAIL\n"},
		{"tool error", errors.New("go list broke"), 2, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubCommand(t, "deps", tc.err)
			var stdout, stderr strings.Builder
			code := run([]string{"deps", "-root", root, "-out", filepath.Join(root, "deps.txt")}, &stdout, &stderr)
			if code != tc.code || stdout.String() != tc.stdout {
				t.Fatalf("exit %d, stdout %q; want %d, %q", code, stdout.String(), tc.code, tc.stdout)
			}
			if tc.code == 2 && stderr.String() != "exbmeasure: go list broke\n" {
				t.Fatalf("stderr %q", stderr.String())
			}
		})
	}
}

// A usage error is exit code 2 and runs nothing.
func TestRunUsageErrors(t *testing.T) {
	for cmd := range commands {
		saved := commands[cmd]
		t.Cleanup(func() { commands[cmd] = saved })
		commands[cmd] = func(*config) error {
			t.Errorf("%s ran", cmd)
			return nil
		}
	}
	for _, args := range [][]string{
		nil,
		{"measure", "-out", "f"},
		{"deps"},
		{"deps", "-out", "f", "extra"},
		{"deps", "-bogus", "-out", "f"},
		{"deps", "-h"},
		{"slowdown", "-reps", "three", "-out", "f"},
	} {
		var stdout, stderr strings.Builder
		if code := run(args, &stdout, &stderr); code != 2 || stdout.String() != "" || !strings.Contains(stderr.String(), "usage") {
			t.Errorf("run(%q) = %d, stdout %q, stderr %q; want 2 and a usage message", args, code, stdout.String(), stderr.String())
		}
	}
}

// The flags reach the procedure; paths become absolute, so that a run's FAULTLINE_RESULTS and
// the -wasm and -toolexec binaries name the same files from the package directory a test binary
// runs in. A runner given by name (no separator) stays a PATH lookup.
func TestRunFlags(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got := stubCommand(t, "slowdown", nil)
	args := []string{"slowdown", "-backend", "wasm", "-corpus", "C2", "-seeds", "100", "-reps", "3", "-runs", "4",
		"-parallel", "2", "-reuse", "-root", "r", "-work", "w", "-out", "o.csv", "-wasm", "bin/faultline-wasm", "-toolexec", "bin/tx"}
	if code := run(args, &strings.Builder{}, &strings.Builder{}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	want := config{backend: "wasm", corpus: "C2", seeds: 100, reps: 3, runs: 4, parallel: 2, reuse: true,
		root: filepath.Join(wd, "r"), work: filepath.Join(wd, "w"), out: filepath.Join(wd, "o.csv"),
		wasmRunner: filepath.Join(wd, "bin", "faultline-wasm"), toolexec: filepath.Join(wd, "bin", "tx")}
	if *got != want {
		t.Fatalf("config\n%+v\nwant\n%+v", *got, want)
	}
	if code := run([]string{"slowdown", "-root", dir, "-out", "o.csv"}, &strings.Builder{}, &strings.Builder{}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	want = config{runs: 2, reps: 3, parallel: runtime.NumCPU(), root: dir, out: filepath.Join(wd, "o.csv"),
		work: filepath.Join(os.TempDir(), "exbmeasure"), wasmRunner: "faultline-wasm"}
	if *got != want {
		t.Fatalf("defaults\n%+v\nwant\n%+v", *got, want)
	}
}
