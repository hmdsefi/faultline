// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package detlint

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeFiles creates the files (slash-separated relative path → content) under root.
func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// AT-DET-06
func TestLintUnclassifiedPackage(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"go.mod":           "module example.com/m\n\ngo 1.26\n",
		"kernel/k.go":      "package kernel\n\nfunc K() int { return 1 }\n",
		"extra/e.go":       "package extra\n\nfunc E() int { return 2 }\n",
		"testdata/t/t.go":  "package t\n\nfunc T() { go T() }\n",
		"_old/o.go":        "package old\n\nfunc O() { go O() }\n",
		".git/g/g.go":      "package g\n\nfunc G() { go G() }\n",
		"sub/go.mod":       "module example.com/sub\n\ngo 1.26\n",
		"sub/s.go":         "package sub\n\nfunc S() { go S() }\n",
		"kernel/k_test.go": "package kernel\n\nfunc init() { go K() }\n",
		"vendor/v/v.go":    "package v\n\nfunc V() { go V() }\n",
		"tonly/x_test.go":  "package tonly\n",
	})
	fs, err := Lint(Module{Root: root, Path: "example.com/m", Classes: map[string]Class{"kernel": ClassCore}})
	if err != nil {
		t.Fatal(err)
	}
	want := Finding{File: "extra", Line: 0, Col: 0, Rule: RuleMeta,
		Msg: "package example.com/m/extra has no determinism class; add it to DET §5.1.3 and internal/detlint"}
	if len(fs) != 1 || fs[0] != want {
		t.Fatalf("findings %v, want [%v]", fs, want)
	}
	dirs, err := packageDirs(root)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(dirs, []string{"extra", "kernel"}) {
		t.Fatalf("package directories %v", dirs)
	}
}

// DET-015: Lint returns its findings sorted, across files and directories.
func TestLintSorted(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"go.mod":      "module example.com/m\n\ngo 1.26\n",
		"kernel/a.go": "package kernel\n\nvar F float64\n",
		"kernel/b.go": "package kernel\n\nfunc G() { go G() }\n",
		"a-b/x.go":    "package x\n\nfunc X() { go X() }\n",
		"a/y/y.go":    "package y\n\nfunc Y() { go Y() }\n",
	})
	fs, err := Lint(Module{Root: root, Path: "example.com/m",
		Classes: map[string]Class{"kernel": ClassCore, "a-b": ClassCore, "a/y": ClassCore}})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range fs {
		got = append(got, fmt.Sprintf("%s:%d %s", f.File, f.Line, f.Rule))
	}
	want := []string{"a-b/x.go:3 DL004", "a/y/y.go:3 DL004", "kernel/a.go:3 DL007", "kernel/b.go:3 DL004"}
	if !slices.Equal(got, want) {
		t.Fatalf("findings %q, want %q", got, want)
	}
}

// DET-015: a type-checking error is an error, not a finding.
func TestLintTypeError(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"go.mod":      "module example.com/m\n\ngo 1.26\n",
		"kernel/k.go": "package kernel\n\nfunc K() int { return \"x\" }\n",
	})
	_, err := Lint(Module{Root: root, Path: "example.com/m", Classes: map[string]Class{"kernel": ClassCore}})
	if err == nil || !strings.Contains(err.Error(), "type-checking example.com/m/kernel") {
		t.Fatalf("err = %v", err)
	}
}

// DET-016, DET §7: FindRoot.
func TestFindRoot(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"go.mod":         "// comment\nmodule \"example.com/m\" // trailing\n\ngo 1.26\n",
		"a/b/go.mod":     "module example.com/m/a/b\n",
		"a/b/c/keep.txt": "",
	})
	start := filepath.Join(root, "a", "b", "c")
	got, err := FindRoot(start, "example.com/m")
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(root); got != root && got != want {
		t.Fatalf("FindRoot = %q, want %q", got, root)
	}
	if got, err := FindRoot(start, "example.com/m/a/b"); err != nil || filepath.Base(got) != "b" {
		t.Fatalf("FindRoot(nested) = %q, %v", got, err)
	}
	_, err = FindRoot(start, "example.com/none")
	if err == nil || err.Error() != "detlint: no go.mod for module example.com/none above "+start {
		t.Fatalf("err = %v", err)
	}
	t.Chdir(start)
	abs, _ := filepath.Abs(".")
	_, err = FindRoot(".", "example.com/none")
	if err == nil || err.Error() != "detlint: no go.mod for module example.com/none above "+abs {
		t.Fatalf("err from . = %v", err)
	}
}
