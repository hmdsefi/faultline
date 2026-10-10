// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package detlint

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// Allow lists what the non-test files of one package may import (DET-020).
type Allow struct {
	Stdlib   bool     // standard library except "testing" and "testing/..."
	Testing  []string // allowed packages from the testing tree, e.g. "testing", "testing/synctest"
	Gograph  bool     // "github.com/hmdsefi/gograph" and its subpackages
	Packages []string // module packages, as paths relative to Module.Path ("" = root package)
}

// Module describes a module to check.
type Module struct {
	Root    string           // absolute path of the directory containing go.mod
	Path    string           // module path
	Classes map[string]Class // relative package path → class
	Imports map[string]Allow // relative package path → allowed imports
}

// Faultline returns the root module of faultline at root with the tables of DET-011 and DET-020.
// Rows for packages that do not exist yet are harmless.
func Faultline(root string) Module {
	classes := map[string]Class{}
	for _, row := range []struct {
		class Class
		pkgs  []string
	}{
		{ClassCore, []string{"kernel", "kernel/simnet", "kernel/simdisk", "kernel/fault", "check/history", "assert", "internal/toys", "internal/gosched"}},
		{ClassTool, []string{"check/linearizability", "check/isolation", "check/liveness", "minimize", "explore"}},
		{ClassEntry, []string{""}},
		{ClassShim, []string{"shims/netsim", "shims/fsx"}},
		{ClassOutput, []string{"artifact", "ui"}},
		{ClassExempt, []string{"cmd/faultline", "internal/detlint", "internal/golden"}},
	} {
		for _, p := range row.pkgs {
			classes[p] = row.class
		}
	}
	kernel := []string{"kernel"}
	net := []string{"kernel", "kernel/simnet", "kernel/simdisk"}
	checks := []string{"kernel", "check/history"}
	imports := map[string]Allow{
		"kernel":                {Stdlib: true, Gograph: true},
		"kernel/simnet":         {Stdlib: true, Gograph: true, Packages: kernel},
		"kernel/simdisk":        {Stdlib: true, Gograph: true, Packages: kernel},
		"kernel/fault":          {Stdlib: true, Gograph: true, Packages: net},
		"check/history":         {Stdlib: true, Packages: kernel},
		"check/linearizability": {Stdlib: true, Gograph: true, Packages: checks},
		"check/isolation":       {Stdlib: true, Gograph: true, Packages: checks},
		"check/liveness":        {Stdlib: true, Gograph: true, Packages: checks},
		"artifact":              {Stdlib: true, Gograph: true, Packages: []string{"kernel", "kernel/fault", "check/history", "ui"}},
		"ui":                    {Stdlib: true},
		"assert":                {Stdlib: true, Packages: kernel},
		"minimize":              {Stdlib: true, Packages: []string{"kernel", "kernel/fault"}},
		"explore":               {Stdlib: true, Packages: []string{"kernel", "kernel/fault", "kernel/simnet"}},
		"shims/netsim":          {Stdlib: true, Packages: net},
		"shims/fsx":             {Stdlib: true, Packages: net},
		"internal/gosched":      {Stdlib: true},
		"": {Stdlib: true, Testing: []string{"testing", "testing/cryptotest", "testing/synctest"}, Gograph: true,
			Packages: []string{"kernel", "kernel/simnet", "kernel/simdisk", "kernel/fault", "check/history",
				"check/linearizability", "check/isolation", "check/liveness", "artifact", "ui", "assert",
				"minimize", "explore", "shims/netsim", "shims/fsx", "internal/gosched"}},
		"cmd/faultline":    {Stdlib: true, Packages: []string{"artifact", "ui", "explore", "minimize"}},
		"internal/detlint": {Stdlib: true},
		"internal/golden":  {Stdlib: true, Testing: []string{"testing"}, Packages: []string{"kernel", "internal/toys"}},
		"internal/toys":    {Stdlib: true, Packages: kernel},
	}
	return Module{Root: root, Path: "github.com/hmdsefi/faultline", Classes: classes, Imports: imports}
}

// FindRoot walks up from dir to the first directory whose go.mod declares module path.
func FindRoot(dir, path string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for d := abs; ; d = filepath.Dir(d) {
		data, err := os.ReadFile(filepath.Join(d, "go.mod"))
		if err == nil && modulePath(string(data)) == path {
			return d, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if filepath.Dir(d) == d {
			return "", fmt.Errorf("detlint: no go.mod for module %s above %s", path, abs)
		}
	}
}

// modulePath returns the module path declared by go.mod content, or "".
func modulePath(gomod string) string {
	for _, line := range strings.Split(gomod, "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "module")
		if !ok || rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
			continue
		}
		rest, _, _ = strings.Cut(rest, "//")
		rest = strings.TrimSpace(rest)
		if p, err := strconv.Unquote(rest); err == nil {
			return p
		}
		return rest
	}
	return ""
}

// packageDirs returns the package directories of the module at root, as slash-separated paths
// relative to root ("" for root itself), in walk order (DET-015, DET §3).
func packageDirs(root string) ([]string, error) {
	var out []string
	var walk func(dir, rel string) error
	walk = func(dir, rel string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			n, err := contextMatches(dir, name)
			if err != nil {
				return err
			}
			if n > 0 {
				out = append(out, rel)
				break
			}
		}
		for _, e := range entries {
			name := e.Name()
			if !e.IsDir() || name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				continue
			}
			sub := filepath.Join(dir, name)
			if _, err := os.Stat(filepath.Join(sub, "go.mod")); err == nil {
				continue // nested module
			}
			if err := walk(sub, path.Join(rel, name)); err != nil {
				return err
			}
		}
		return nil
	}
	return out, walk(root, "")
}

// importPath returns the import path of the package at rel in module m.
func importPath(m Module, rel string) string {
	if rel == "" {
		return m.Path
	}
	return m.Path + "/" + rel
}

// Lint type-checks and lints every package directory of m whose class is not ClassExempt and
// returns the findings sorted by (File, Line, Col, Rule, Msg) (DET-015).
func Lint(m Module) ([]Finding, error) {
	dirs, err := packageDirs(m.Root)
	if err != nil {
		return nil, err
	}
	l := newLinter()
	var out []Finding
	for _, rel := range dirs {
		c, ok := m.Classes[rel]
		if !ok {
			out = append(out, Finding{File: rel, Rule: RuleMeta, Msg: fmt.Sprintf(
				"package %s has no determinism class; add it to DET §5.1.3 and internal/detlint", importPath(m, rel))})
			continue
		}
		if c == ClassExempt {
			continue
		}
		fs, err := l.lintPackage(m.Root, filepath.Join(m.Root, filepath.FromSlash(rel)), importPath(m, rel), c)
		if err != nil {
			return nil, err
		}
		out = append(out, fs...)
	}
	sortFindings(out)
	return out, nil
}
