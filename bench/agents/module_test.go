// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package agents_test

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoGoModUnderBench walks bench/, the module root, and requires bench/go.mod to be the only
// go.mod, with no go.sum and no go.work. A go.mod further down would cut its folder out of this
// module, and CI would vet and test it as a module of its own.
func TestNoGoModUnderBench(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if first, _, _ := strings.Cut(string(mod), "\n"); first != "module github.com/hmdsefi/faultline/bench" {
		t.Fatalf("%s starts with %q, want the bench module", filepath.Join(root, "go.mod"), first)
	}
	var found []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			// Each tier's runs/ holds the runner's output, which git ignores. Its workspace copies
			// carry the go.mod that prepare.sh writes.
			if ok, _ := path.Match("agents/*/runs", rel); ok {
				return filepath.SkipDir
			}
			return nil
		}
		switch d.Name() {
		case "go.mod":
			if rel != "go.mod" {
				found = append(found, rel)
			}
		case "go.sum", "go.work":
			found = append(found, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range found {
		t.Errorf("bench/%s: bench/go.mod must be the only go.mod in bench/, with no go.sum or go.work", f)
	}
}
