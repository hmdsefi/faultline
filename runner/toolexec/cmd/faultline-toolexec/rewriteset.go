// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	faultlineRoot = "github.com/hmdsefi/faultline"
	goschedPath   = faultlineRoot + "/internal/gosched"
)

// moduleOf returns the module path of the go.mod nearest above dir, or "".
func moduleOf(dir string) string {
	for {
		b, err := os.ReadFile(filepath.Join(dir, "go.mod")) //nolint:gosec // the go.mod files above the package's sources
		if err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if rest, ok := strings.CutPrefix(strings.TrimSpace(l), "module "); ok {
					return strings.Trim(strings.TrimSpace(rest), `"`)
				}
			}
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func under(path, root string) bool {
	return root != "" && (path == root || strings.HasPrefix(path, root+string(filepath.Separator)))
}

// rewriteFiles returns the files of a compile that are rewritten (XTE-030), and whether rule 2
// excludes the compile because one of its files lies outside every module.
func rewriteFiles(ip string, files []string, roots map[string]string) (sel map[string]bool, outside bool) {
	pkg, _, _ := strings.Cut(ip, " ")
	if len(files) == 0 || strings.HasSuffix(pkg, ".test") || pkg == schedPath || pkg == goschedPath {
		return nil, false
	}
	src, modcache := filepath.Join(roots["GOROOT"], "src"), roots["GOMODCACHE"]
	first, _ := filepath.Abs(files[0])
	switch {
	case under(first, src):
		if !listed(os.Getenv("FAULTLINE_TOOLEXEC_STD"), pkg) {
			return nil, false
		}
	case under(first, modcache):
		if !inListedModule(first, modcache, os.Getenv("FAULTLINE_TOOLEXEC_DEPS")) {
			return nil, false
		}
	}
	for _, f := range files {
		abs, _ := filepath.Abs(f)
		if !under(abs, src) && !under(abs, modcache) && moduleOf(filepath.Dir(abs)) == "" {
			return nil, true
		}
	}
	if under(first, src) || under(first, modcache) {
		return selectAll(files), false
	}
	if m := moduleOf(filepath.Dir(first)); m == faultlineRoot || strings.HasPrefix(m, faultlineRoot+"/") {
		sel = map[string]bool{}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				sel[f] = true
			}
		}
		return sel, false
	}
	return selectAll(files), false
}

func selectAll(files []string) map[string]bool {
	sel := map[string]bool{}
	for _, f := range files {
		sel[f] = true
	}
	return sel
}

// listed reports whether the comma-separated list holds name; entries are trimmed.
func listed(list, name string) bool {
	for _, e := range strings.Split(list, ",") {
		if strings.TrimSpace(e) == name {
			return true
		}
	}
	return false
}

// inListedModule reports whether file lies in the module-cache directory of a listed module,
// <modcache>/<escaped module path>@<version> (XTE-032).
func inListedModule(file, modcache, list string) bool {
	for _, e := range strings.Split(list, ",") {
		if e = strings.TrimSpace(e); e == "" {
			continue
		}
		if strings.HasPrefix(file, filepath.Join(modcache, filepath.FromSlash(escapePath(e)))+"@") {
			return true
		}
	}
	return false
}

// escapePath applies the module cache's case encoding: an upper-case letter X becomes !x.
func escapePath(p string) string {
	var b strings.Builder
	for _, r := range p {
		if 'A' <= r && r <= 'Z' {
			b.WriteByte('!')
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// checkStd validates FAULTLINE_TOOLEXEC_STD (XTE-031); deps are the prepared importcfg lines,
// sched and its dependencies.
func checkStd(list string, deps []string) error {
	dep := map[string]bool{}
	for _, l := range deps {
		p, _, _ := strings.Cut(strings.TrimPrefix(l, "packagefile "), "=")
		dep[p] = true
	}
	for _, e := range strings.Split(list, ",") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		first, _, _ := strings.Cut(e, "/")
		why := ""
		switch {
		case strings.Contains(first, "."):
			why = "not a standard-library package"
		case e == "unsafe" || e == "syscall" || e == "sync" || e == "sync/atomic" || e == "reflect" ||
			e == "runtime" || strings.HasPrefix(e, "runtime/") || e == "testing" || strings.HasPrefix(e, "testing/") ||
			strings.HasPrefix(e, "internal/") || strings.HasPrefix(e, "vendor/") || strings.Contains(e, "/internal/"):
			why = "the runtime, syscall, unsafe, sync, reflect, testing, internal and vendor packages are never rewritten"
		case dep[e]:
			why = "sched depends on it"
		}
		if why != "" {
			return fmt.Errorf("FAULTLINE_TOOLEXEC_STD: %s cannot be rewritten: %s", e, why)
		}
	}
	return nil
}

// checkDeps validates FAULTLINE_TOOLEXEC_DEPS (XTE-032).
func checkDeps(list string) error {
	for _, e := range strings.Split(list, ",") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		first, _, _ := strings.Cut(e, "/")
		if !strings.Contains(first, ".") || e == faultlineRoot || strings.HasPrefix(e, faultlineRoot+"/") {
			return fmt.Errorf("FAULTLINE_TOOLEXEC_DEPS: %s is not a module path outside %s", e, faultlineRoot)
		}
	}
	return nil
}
