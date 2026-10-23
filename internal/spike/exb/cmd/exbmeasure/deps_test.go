// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// copyModule copies the root module at root into dst: every file except those under .git,
// .helper and nested modules (directories with their own go.mod).
func copyModule(root, dst string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			if rel != "." {
				if d.Name() == ".git" || d.Name() == ".helper" {
					return filepath.SkipDir
				}
				if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
					return filepath.SkipDir
				}
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		in, err := os.Open(p) //nolint:gosec // G122: copies the test's own checkout
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(filepath.Join(dst, rel))
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

// wazeroCopy returns a temporary copy of the root module with wazero added to go.mod (and
// runner/wasm's go.sum appended), set up so that the go commands run offline.
func wazeroCopy(t *testing.T) string {
	t.Helper()
	root, err := findRoot()
	if err != nil {
		t.Fatal(err)
	}
	sumPath := filepath.Join(root, "runner", "wasm", "go.sum")
	if _, err := os.Stat(sumPath); err != nil {
		t.Skipf("runner/wasm is not on this branch yet, and this test borrows its go.sum: %v", err)
	}
	// deps runs offline below (GOPROXY=off), so first fill the module cache with what runner/wasm
	// requires: CI's nested job tests this module before runner/wasm.
	if out, err := goCmd(filepath.Join(root, "runner", "wasm"), []string{"GOWORK=off"}, "mod", "download").CombinedOutput(); err != nil {
		t.Fatalf("go mod download in runner/wasm: %v\n%s", err, out)
	}
	tmp := t.TempDir()
	if err := copyModule(root, tmp); err != nil {
		t.Fatal(err)
	}
	gomod, err := os.ReadFile(filepath.Join(tmp, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(tmp, "go.mod"), string(gomod)+"\nrequire github.com/tetratelabs/wazero v1.12.0\n")
	wasmSum, err := os.ReadFile(sumPath)
	if err != nil {
		t.Fatal(err)
	}
	sum, _ := os.ReadFile(filepath.Join(tmp, "go.sum"))
	writeFile(t, filepath.Join(tmp, "go.sum"), string(sum)+string(wasmSum))
	t.Setenv("GOFLAGS", "-mod=mod") // the copy's go.mod changed; never touch the network
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOWORK", "off")
	return tmp
}

// AT-EXB-11
func TestATEXB11_DepsReportsWazeroInRoot(t *testing.T) {
	tmp := wazeroCopy(t)
	c := &config{root: tmp, out: filepath.Join(t.TempDir(), "deps.txt")}
	if err := deps(c); !errors.Is(err, errRequirement) {
		t.Fatalf("deps = %v, want errRequirement", err)
	}
	b, err := os.ReadFile(c.out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"violation: wazero required by go.mod", "violation: go mod graph mentions github.com/tetratelabs/wazero"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("deps.txt lacks %q:\n%s", want, b)
		}
	}
}

// EXB-067's first rule: the root module's test build, test files included, depends on no
// third-party module.
func TestDepsReportsThirdPartyTestImport(t *testing.T) {
	tmp := wazeroCopy(t)
	src := "package faultline_test\n\nimport _ \"github.com/tetratelabs/wazero\"\n"
	writeFile(t, filepath.Join(tmp, "zz_wazero_test.go"), src)
	c := &config{root: tmp, out: filepath.Join(t.TempDir(), "deps.txt")}
	if err := deps(c); !errors.Is(err, errRequirement) {
		t.Fatalf("deps = %v, want errRequirement", err)
	}
	b, err := os.ReadFile(c.out)
	if err != nil {
		t.Fatal(err)
	}
	if want := "violation: root module test build depends on github.com/tetratelabs/wazero"; !strings.Contains(string(b), want) {
		t.Fatalf("deps.txt lacks %q:\n%s", want, b)
	}
}

func TestDepsOfTheCheckout(t *testing.T) {
	root, err := findRoot()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOWORK", "off")
	c := &config{root: root, out: filepath.Join(t.TempDir(), "deps.txt")}
	if err := deps(c); err != nil {
		b, _ := os.ReadFile(c.out)
		t.Fatalf("deps = %v\n%s", err, b)
	}
	b, err := os.ReadFile(c.out)
	if err != nil || !strings.HasSuffix(string(b), "\nno violations\n") {
		t.Fatalf("deps.txt does not end with no violations:\n%s\n%v", b, err)
	}
}

// EXB-067 is about the root module alone: deps runs its go commands with GOWORK=off, whatever
// the environment says (a local go.work lists every nested module).
func TestDepsIgnoresWorkspace(t *testing.T) {
	root, err := findRoot()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOWORK", filepath.Join(t.TempDir(), "missing", "go.work"))
	c := &config{root: root, out: filepath.Join(t.TempDir(), "deps.txt")}
	if err := deps(c); err != nil {
		b, _ := os.ReadFile(c.out)
		t.Fatalf("deps = %v\n%s", err, b)
	}
}

// EXB-067's third rule: wazero appears only in runner/wasm/go.mod. One other go.mod that names
// it is a violation, which alone fails the requirement; the private .helper directory (whose
// worktrees hold copies of runner/wasm) is not part of the checkout.
func TestDepsWazeroGoModFiles(t *testing.T) {
	wazero := "module example.com/x\n\ngo 1.26.0\n\nrequire github.com/tetratelabs/wazero v1.12.0\n"
	for _, tc := range []struct {
		name  string
		files map[string]string
		err   error
		want  string // the end of deps.txt
	}{
		{"runner/wasm and .helper", map[string]string{"runner/wasm/go.mod": wazero, ".helper/worktrees/a/runner/wasm/go.mod": wazero},
			nil, "runner/wasm/go.mod\n\nno violations\n"},
		{"one other go.mod", map[string]string{"runner/wasm/go.mod": wazero, "tools/go.mod": wazero},
			errRequirement, "runner/wasm/go.mod\ntools/go.mod\n\nviolation: wazero required by tools/go.mod\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			writeFile(t, filepath.Join(tmp, "go.mod"), "module github.com/hmdsefi/faultline\n\ngo 1.26.0\n")
			writeFile(t, filepath.Join(tmp, "p.go"), "package faultline\n")
			for _, p := range slices.Sorted(maps.Keys(tc.files)) {
				writeFile(t, filepath.Join(tmp, p), tc.files[p])
			}
			t.Setenv("GOPROXY", "off")
			t.Setenv("GOFLAGS", "")
			c := &config{root: tmp, out: filepath.Join(t.TempDir(), "deps.txt")}
			if err := deps(c); !errors.Is(err, tc.err) || tc.err == nil && err != nil {
				t.Fatalf("deps = %v, want %v", err, tc.err)
			}
			b, err := os.ReadFile(c.out)
			if want := "\n$ go.mod files mentioning github.com/tetratelabs/wazero\n" + tc.want; err != nil || !strings.HasSuffix(string(b), want) {
				t.Fatalf("deps.txt:\n%s\n%v\nwant it to end with:\n%s", b, err, want)
			}
		})
	}
}

// A go command that fails has measured nothing: that is a tool error, not a violation. In the
// first module only go list fails; in the second both go commands do.
func TestDepsGoFailureIsAToolError(t *testing.T) {
	for _, tc := range []struct {
		name, gomod, src string
	}{
		{"unknown import", "", "import _ \"example.com/missing\"\n"},
		{"missing module", "\nrequire example.com/missing v1.0.0\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			writeFile(t, filepath.Join(tmp, "go.mod"), "module github.com/hmdsefi/faultline\n\ngo 1.26.0\n"+tc.gomod)
			writeFile(t, filepath.Join(tmp, "p.go"), "package faultline\n\n"+tc.src)
			t.Setenv("GOPROXY", "off")
			t.Setenv("GOFLAGS", "")
			c := &config{root: tmp, out: filepath.Join(t.TempDir(), "deps.txt")}
			err := deps(c)
			if err == nil || errors.Is(err, errRequirement) || !strings.Contains(err.Error(), "example.com/missing") {
				t.Fatalf("deps = %v, want a tool error naming example.com/missing", err)
			}
		})
	}
}
