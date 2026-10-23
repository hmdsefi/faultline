// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// deps implements EXB-067 on the module at c.root and writes deps.txt: the output of the two go
// commands, the go.mod files that mention wazero, then one line per violation, or "no
// violations". The go commands run with GOWORK=off, since the rule is about the root module alone
// and a local go.work adds every nested module. A go command that fails has measured nothing:
// that is a tool error, noted at the end of deps.txt.
func deps(c *config) error {
	var b strings.Builder
	var violations []string
	allowed := map[string]bool{"github.com/hmdsefi/faultline": true, "github.com/hmdsefi/gograph": true}
	toolError := func(err error) error {
		fmt.Fprintf(&b, "\nerror: %v\n", err)
		if werr := os.WriteFile(c.out, []byte(b.String()), 0o600); werr != nil {
			return fmt.Errorf("%v (and writing %s: %v)", err, c.out, werr)
		}
		return err
	}

	fmt.Fprintf(&b, "$ go list -deps -test -f '{{if not .Standard}}{{.Module.Path}}{{end}}' ./... | sort -u\n")
	out, err := goOutput(c.root, "list", "-deps", "-test", "-f", "{{if not .Standard}}{{.Module.Path}}{{end}}", "./...")
	if err != nil {
		return toolError(err)
	}
	var mods []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" && !slices.Contains(mods, l) {
			mods = append(mods, l)
		}
	}
	slices.Sort(mods)
	for _, m := range mods {
		fmt.Fprintln(&b, m)
		if !allowed[m] {
			violations = append(violations, "root module test build depends on "+m)
		}
	}

	fmt.Fprintf(&b, "\n$ go mod graph\n")
	graph, err := goOutput(c.root, "mod", "graph")
	if err != nil {
		return toolError(err)
	}
	b.Write(graph)
	for _, l := range strings.Split(strings.TrimSpace(string(graph)), "\n") {
		for _, f := range strings.Fields(l) {
			m, _, _ := strings.Cut(f, "@")
			if !allowed[m] && m != "go" && m != "toolchain" {
				violations = append(violations, "go mod graph mentions "+m)
			}
		}
	}

	fmt.Fprintf(&b, "\n$ go.mod files mentioning github.com/tetratelabs/wazero\n")
	err = filepath.WalkDir(c.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == ".helper") {
			return filepath.SkipDir
		}
		if d.IsDir() || d.Name() != "go.mod" {
			return nil
		}
		data, err := os.ReadFile(p) //nolint:gosec // G122: reads the go.mod files of the checkout being measured
		if err != nil {
			return err
		}
		if !strings.Contains(string(data), "github.com/tetratelabs/wazero") {
			return nil
		}
		rel, err := filepath.Rel(c.root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		fmt.Fprintln(&b, rel)
		if rel != "runner/wasm/go.mod" {
			violations = append(violations, "wazero required by "+rel)
		}
		return nil
	})
	if err != nil {
		return toolError(err)
	}

	slices.Sort(violations)
	violations = slices.Compact(violations)
	fmt.Fprintln(&b)
	for _, v := range violations {
		fmt.Fprintln(&b, "violation:", v)
	}
	if len(violations) == 0 {
		fmt.Fprintln(&b, "no violations")
	}
	if err := os.WriteFile(c.out, []byte(b.String()), 0o600); err != nil {
		return err
	}
	if len(violations) > 0 {
		return errRequirement
	}
	return nil
}

// goOutput runs a go command in the module at dir with GOWORK=off and returns its standard
// output; an error carries its standard error.
func goOutput(dir string, args ...string) ([]byte, error) {
	cmd := goCmd(dir, []string{"GOWORK=off"}, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go %s: %v: %s", strings.Join(args, " "), err, bytes.TrimSpace(stderr.Bytes()))
	}
	return out, nil
}
