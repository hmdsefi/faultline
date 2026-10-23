// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// gitFixture creates a git repository whose branch main holds base and whose checked-out branch
// spike adds change on top (path to content). Git runs without the user's configuration.
func gitFixture(t *testing.T, base, change map[string]string) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_AUTHOR", "GIT_COMMITTER"} {
		t.Setenv(k+"_NAME", "exbmeasure test")
		t.Setenv(k+"_EMAIL", "test@example.com")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil { //nolint:gosec // G204: the test's own git commands
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(files map[string]string) {
		t.Helper()
		for _, p := range slices.Sorted(maps.Keys(files)) {
			writeFile(t, filepath.Join(dir, p), files[p])
		}
		git("add", "-A")
	}
	git("init", "-q", "-b", "main")
	write(base)
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "spike")
	write(change)
	git("commit", "-q", "-m", "spike")
	return dir
}

// with returns a copy of m with the given path and content pairs set; an empty content leaves
// the path out.
func with(m map[string]string, kv ...string) map[string]string {
	m = maps.Clone(m)
	for len(kv) >= 2 {
		k, v := kv[0], kv[1]
		kv = kv[2:]
		if v == "" {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	return m
}

var sizeBase = map[string]string{
	"go.mod":       "module github.com/hmdsefi/faultline\n\ngo 1.26.0\n",
	"env.go":       "package faultline\n\nvar a = 1\n",
	"faultline.go": "package faultline\n",
	"options.go":   "package faultline\n",
	"world.go":     "package faultline\n",
}

// sizeChange is prototype A's spike changes in the fixture: 3 code lines in the new exact.go, 2
// of env.go's 3 added lines (one is a comment), 1 each in faultline.go and options.go, and the
// runner's files outside tests and testdata.
var sizeChange = map[string]string{
	"exact.go":     "// Package doc.\npackage faultline\n\n/* block\n */\nvar x = `a\n\n`\n",
	"env.go":       "package faultline\n\n// b is new.\nvar a = 2\nvar b = 3\n",
	"faultline.go": "package faultline\n\nvar c = 1\n",
	"options.go":   "package faultline\n\nvar d = 1\n",
	"env_test.go":  "package faultline\n\nvar e = 1\n",
	"go.sum":       "x\n",

	"runner/wasm/go.mod":                     "module github.com/hmdsefi/faultline/runner/wasm\n",
	"runner/wasm/host.go":                    "package wasm\n\nfunc f() {}\n",
	"runner/wasm/host_test.go":               "package wasm\n\nfunc g() {}\n",
	"runner/wasm/testdata/guest/main.go":     "package main\n\nfunc main() {}\n",
	"runner/wasm/cmd/faultline-wasm/main.go": "package main\n\n// usage\nfunc main() {\n}\n",
	"internal/spike/exb/go.mod":              "module github.com/hmdsefi/faultline/internal/spike/exb\n",
	"internal/spike/exb/cmd/exbmeasure/m.go": "package main\n",
	"internal/spike/exb/estimates/wasm.tsv":  "feature one\t60\nfeature two\t 40\n",
}

const sizeWant = `prototype lines: 12
  env.go (spike changes): 2
  exact.go (spike changes): 3
  faultline.go (spike changes): 1
  options.go (spike changes): 1
  runner/wasm/cmd/faultline-wasm/main.go: 3
  runner/wasm/host.go: 2
missing for production (estimate): 100
  feature one: 60
  feature two: 40
total: 12 + 100 = 112 (requirement <= 3000): PASS
`

// EXB-066: prototype A's lines are the code lines of the non-test .go files under runner/wasm
// outside testdata, plus the code lines that git diff -U0 main...HEAD adds to the root files of
// its list, all as committed at HEAD; the estimates table adds its lines; requirement <= 3000.
func TestSize(t *testing.T) {
	root := gitFixture(t, sizeBase, sizeChange)
	// main moves on after the branch point. git diff main...HEAD starts at the merge base, so the
	// line main changes in faultline.go is no spike change (main..HEAD would count it).
	if _, err := git(root, "checkout", "-q", "main"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "faultline.go"), "package faultline // main moved on\n")
	for _, args := range [][]string{{"commit", "-q", "-am", "main moves on"}, {"checkout", "-q", "spike"}} {
		if _, err := git(root, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Uncommitted edits and untracked files are not part of the measured commit: on disk, env.go's
	// added lines would move down by two.
	writeFile(t, filepath.Join(root, "env.go"), "// moved\n// down\n"+sizeChange["env.go"])
	writeFile(t, filepath.Join(root, "runner", "wasm", "extra.go"), "package wasm\n\nvar u = 1\n")
	c := &config{backend: "wasm", root: root, out: filepath.Join(t.TempDir(), "size-wasm.txt")}
	if err := size(c); err != nil {
		t.Fatalf("size = %v, want nil", err)
	}
	if b, err := os.ReadFile(c.out); err != nil || string(b) != sizeWant {
		t.Fatalf("size-wasm.txt:\n%s\n%v\nwant:\n%s", b, err, sizeWant)
	}
}

// The requirement is prototype lines + estimates <= 3000: 3000 passes, 3001 fails, and both
// write the file.
func TestSizeRequirement(t *testing.T) {
	for _, tc := range []struct {
		estimate string
		err      error
		last     string
	}{
		{"2988", nil, "total: 12 + 2988 = 3000 (requirement <= 3000): PASS\n"},
		{"2989", errRequirement, "total: 12 + 2989 = 3001 (requirement <= 3000): FAIL\n"},
	} {
		root := gitFixture(t, sizeBase, with(sizeChange, "internal/spike/exb/estimates/wasm.tsv", "everything\t"+tc.estimate+"\n"))
		c := &config{backend: "wasm", root: root, out: filepath.Join(t.TempDir(), "size-wasm.txt")}
		if err := size(c); !errors.Is(err, tc.err) || (tc.err == nil && err != nil) {
			t.Fatalf("estimate %s: size = %v, want %v", tc.estimate, err, tc.err)
		}
		if b, err := os.ReadFile(c.out); err != nil || !strings.HasSuffix(string(b), tc.last) {
			t.Fatalf("estimate %s: size-wasm.txt:\n%s\n%v", tc.estimate, b, err)
		}
	}
}

// Before measuring, size checks the root file lists against git diff main...HEAD: every root
// non-test .go file the branch changes is in a backend's list or tree, and every file of the
// measured backend's list is changed. Otherwise it is a tool error and nothing is written.
func TestSizeChecksRootFiles(t *testing.T) {
	for _, tc := range []struct {
		name, backend string
		change        map[string]string
		want          string
	}{
		{"unlisted root file", "wasm", with(sizeChange, "world.go", "package faultline\n\nvar w = 1\n"), "world.go"},
		{"unchanged listed file", "wasm", with(sizeChange, "options.go", sizeBase["options.go"]), "options.go"},
		{"other prototype", "toolexec", sizeChange, "gosched_hook.go"},
		{"no estimates", "wasm", with(sizeChange, "internal/spike/exb/estimates/wasm.tsv", ""), "wasm.tsv"},
		{"backend", "parta", sizeChange, `size applies to wasm and toolexec, not "parta"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := gitFixture(t, sizeBase, tc.change)
			c := &config{backend: tc.backend, root: root, out: filepath.Join(t.TempDir(), "size.txt")}
			err := size(c)
			if err == nil || errors.Is(err, errRequirement) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("size = %v, want a tool error containing %q", err, tc.want)
			}
			if _, err := os.Stat(c.out); err == nil {
				t.Fatal("size wrote its file")
			}
		})
	}
}

// Prototype B counts its two trees, internal/gosched being in the root module.
func TestSizeToolexec(t *testing.T) {
	base := with(sizeBase, "driver.go", "package faultline\n\nvar p = 1\n", "driver_contract.go", "package faultline\n")
	change := map[string]string{
		"gosched_hook.go":                           "package faultline\n\nvar h = 1\n",
		"driver.go":                                 "package faultline\n\nvar p = 1\nvar q = 2\n",
		"driver_contract.go":                        "package faultline\n\n// none\n",
		"internal/gosched/gosched.go":               "package gosched\n\nvar Active func()\n",
		"internal/gosched/gosched_test.go":          "package gosched\n",
		"runner/toolexec/go.mod":                    "module github.com/hmdsefi/faultline/runner/toolexec\n",
		"runner/toolexec/sched/sched.go":            "package sched\n\nfunc Point() {}\n",
		"internal/spike/exb/estimates/toolexec.tsv": "rewrite table\t600\n",
	}
	root := gitFixture(t, base, change)
	c := &config{backend: "toolexec", root: root, out: filepath.Join(t.TempDir(), "size-toolexec.txt")}
	if err := size(c); err != nil {
		t.Fatalf("size = %v, want nil", err)
	}
	want := `prototype lines: 7
  driver.go (spike changes): 1
  driver_contract.go (spike changes): 0
  gosched_hook.go (spike changes): 2
  internal/gosched/gosched.go: 2
  runner/toolexec/sched/sched.go: 2
missing for production (estimate): 600
  rewrite table: 600
total: 7 + 600 = 607 (requirement <= 3000): PASS
`
	if b, err := os.ReadFile(c.out); err != nil || string(b) != want {
		t.Fatalf("size-toolexec.txt:\n%s\n%v\nwant:\n%s", b, err, want)
	}
}

// addedLines reads the new-file line numbers of git diff -U0 main...HEAD's hunks: "+c,d" adds d
// lines from c, "+c" one line, "+c,0" none.
func TestAddedLines(t *testing.T) {
	root := gitFixture(t,
		map[string]string{"f.go": "1\n2\n3\n4\n5\n6\n"},
		map[string]string{"f.go": "1\nTWO\n3\n5\n6\n7\n8\n"})
	got, err := addedLines(root, "f.go")
	if err != nil {
		t.Fatal(err)
	}
	if lines := slices.Sorted(maps.Keys(got)); !slices.Equal(lines, []int{2, 6, 7}) {
		t.Fatalf("added lines %v, want [2 6 7]", lines)
	}
}

func TestEstimates(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, spike, "estimates", "wasm.tsv")
	writeFile(t, path, "a feature\t250\nanother\t 7 \n")
	rows, total, err := estimates(root, "wasm")
	if err != nil || total != 257 || !slices.Equal(rows, [][2]string{{"a feature", "250"}, {"another", "7"}}) {
		t.Fatalf("estimates = %v, %d, %v", rows, total, err)
	}
	for _, bad := range []string{"", "no tab 5\n", "x\t-1\n", "x\tmany\n", "\t5\n", "a\t1\n\nb\t2\n"} {
		writeFile(t, path, bad)
		if _, _, err := estimates(root, "wasm"); err == nil {
			t.Errorf("estimates of %q = nil error", bad)
		}
	}
}
