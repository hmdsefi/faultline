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
	corpusModule  = faultlineRoot + "/internal/spike/exb"
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

// rewriteFiles returns the files of a compile that are rewritten (EXB-033): none for the standard
// library (unless FAULTLINE_TOOLEXEC_STD lists the package), module cache code, test mains, sched
// and gosched; only the _test.go files of faultline's own modules (except the corpus module);
// every file of any other local module.
func rewriteFiles(ip string, files []string, roots map[string]string) map[string]bool {
	pkg, _, _ := strings.Cut(ip, " ")
	if len(files) == 0 || strings.HasSuffix(pkg, ".test") || pkg == schedPath || pkg == goschedPath {
		return nil
	}
	sel := map[string]bool{}
	all := func() map[string]bool {
		for _, f := range files {
			sel[f] = true
		}
		return sel
	}
	first, _ := filepath.Abs(files[0])
	if under(first, filepath.Join(roots["GOROOT"], "src")) {
		for _, s := range strings.Split(os.Getenv("FAULTLINE_TOOLEXEC_STD"), ",") {
			if s != "" && s == pkg {
				return all()
			}
		}
		return nil
	}
	if under(first, roots["GOMODCACHE"]) {
		return nil
	}
	m := moduleOf(filepath.Dir(first))
	if (m == faultlineRoot || strings.HasPrefix(m, faultlineRoot+"/")) && !strings.HasPrefix(m, corpusModule) {
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				sel[f] = true
			}
		}
		return sel
	}
	return all()
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
