// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"go/scanner"
	"go/token"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// sizeSources lists, per backend, the prototype's own trees (all non-test .go files outside
// testdata) and the root-module files whose spike-branch changes count (added lines of
// git diff main...HEAD). Before measuring, size checks the root lists against the files that
// git diff main...HEAD changes (EXB-066).
var sizeSources = map[string]struct{ trees, diffs []string }{
	"wasm":     {trees: []string{"runner/wasm"}, diffs: []string{"exact.go", "env.go", "faultline.go", "options.go"}},
	"toolexec": {trees: []string{"runner/toolexec", "internal/gosched"}, diffs: []string{"gosched_hook.go", "driver.go", "driver_contract.go"}},
}

// codeLines returns the set of line numbers of src that are non-blank and not inside a comment
// (EXB-066): the lines a token other than a comment starts on, and the non-blank later lines of a
// raw string literal. A //line directive does not change the numbers.
func codeLines(src []byte) map[int]bool {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, src, nil, scanner.ScanComments)
	text := strings.Split(string(src), "\n")
	lines := map[int]bool{}
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.COMMENT || (tok == token.SEMICOLON && lit == "\n") {
			continue
		}
		first := file.PositionFor(pos, false).Line
		lines[first] = true
		for i := 1; i <= strings.Count(lit, "\n"); i++ {
			if strings.TrimSpace(text[first+i-1]) != "" {
				lines[first+i] = true
			}
		}
	}
	return lines
}

var hunk = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

// addedLines returns the new-file line numbers added by git diff -U0 main...HEAD -- path.
func addedLines(root, path string) (map[int]bool, error) {
	out, err := git(root, "diff", "--no-color", "--no-ext-diff", "-U0", "main...HEAD", "--", path)
	if err != nil {
		return nil, err
	}
	added := map[int]bool{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for sc.Scan() {
		m := hunk.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		start, _ := strconv.Atoi(m[1])
		n := 1
		if m[2] != "" {
			n, _ = strconv.Atoi(m[2])
		}
		for i := start; i < start+n; i++ {
			added[i] = true
		}
	}
	return added, sc.Err()
}

// git runs git in the checkout root and returns its standard output; an error carries its
// standard error.
func git(root string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...) //nolint:gosec // G204: git with exbmeasure's own arguments
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, bytes.TrimSpace(stderr.Bytes()))
	}
	return out, nil
}

// estimates reads internal/spike/exb/estimates/<backend>.tsv: "feature<TAB>lines" per line.
func estimates(root, backend string) ([][2]string, int, error) {
	b, err := os.ReadFile(filepath.Join(root, spike, "estimates", backend+".tsv"))
	if err != nil {
		return nil, 0, err
	}
	var rows [][2]string
	total := 0
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		feat, n, ok := strings.Cut(l, "\t")
		v, err := strconv.Atoi(strings.TrimSpace(n))
		if !ok || err != nil || v < 0 || strings.TrimSpace(feat) == "" {
			return nil, 0, fmt.Errorf("estimates/%s.tsv: bad line %q", backend, l)
		}
		rows, total = append(rows, [2]string{feat, strings.TrimSpace(n)}), total+v
	}
	return rows, total, nil
}

// isSource reports whether the slash-separated path is a non-test .go file outside testdata.
func isSource(p string) bool {
	return strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") &&
		!slices.Contains(strings.Split(path.Dir(p), "/"), "testdata")
}

// headFiles lists the files of HEAD, relative to the checkout root.
func headFiles(root string) ([]string, error) {
	out, err := git(root, "ls-tree", "-r", "-z", "--name-only", "HEAD")
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00"), nil
}

// checkSizeSources checks the root file lists against git diff main...HEAD: every root-module,
// non-test .go file the branch changes (nested modules excluded) is in a backend's list or under
// one of its trees, and every file in the list of backend is among them.
func checkSizeSources(root, backend string, files []string) error {
	var nested []string
	for _, p := range files {
		if path.Base(p) == "go.mod" && p != "go.mod" {
			nested = append(nested, path.Dir(p)+"/")
		}
	}
	out, err := git(root, "diff", "--no-color", "--no-ext-diff", "--name-only", "-z", "--diff-filter=d", "main...HEAD")
	if err != nil {
		return err
	}
	var changed []string
	for _, p := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if isSource(p) && !slices.ContainsFunc(nested, func(n string) bool { return strings.HasPrefix(p, n) }) {
			changed = append(changed, p)
		}
	}
	for _, p := range sizeSources[backend].diffs {
		if !slices.Contains(changed, p) {
			return fmt.Errorf("size: sizeSources lists %s for %s, but git diff main...HEAD does not change it", p, backend)
		}
	}
	for _, p := range changed {
		listed := false
		for _, b := range slices.Sorted(maps.Keys(sizeSources)) {
			src := sizeSources[b]
			listed = listed || slices.Contains(src.diffs, p) ||
				slices.ContainsFunc(src.trees, func(t string) bool { return strings.HasPrefix(p, t+"/") })
		}
		if !listed {
			return fmt.Errorf("size: git diff main...HEAD changes the root-module file %s, which no list of sizeSources names", p)
		}
	}
	return nil
}

// size implements EXB-066 and writes size-<backend>.txt. The prototype's code is read as
// committed at HEAD, the commit git diff main...HEAD ends at; the estimates file is read from the
// checkout, as it may not be committed yet.
func size(c *config) error {
	src, ok := sizeSources[c.backend]
	if !ok {
		return fmt.Errorf("size applies to wasm and toolexec, not %q", c.backend)
	}
	files, err := headFiles(c.root)
	if err != nil {
		return err
	}
	if err := checkSizeSources(c.root, c.backend, files); err != nil {
		return err
	}
	type entry struct {
		path string
		n    int
	}
	var counted []entry
	total := 0
	for _, tree := range src.trees {
		found := false
		for _, p := range files {
			if !strings.HasPrefix(p, tree+"/") || !isSource(p) {
				continue
			}
			b, err := git(c.root, "show", "HEAD:"+p)
			if err != nil {
				return err
			}
			n := len(codeLines(b))
			counted, total, found = append(counted, entry{p, n}), total+n, true
		}
		if !found {
			return fmt.Errorf("size: no non-test .go file under %s at HEAD", tree)
		}
	}
	for _, p := range src.diffs {
		b, err := git(c.root, "show", "HEAD:"+p)
		if err != nil {
			return err
		}
		added, err := addedLines(c.root, p)
		if err != nil {
			return err
		}
		n := 0
		for l := range codeLines(b) {
			if added[l] {
				n++
			}
		}
		counted, total = append(counted, entry{p + " (spike changes)", n}), total+n
	}
	slices.SortFunc(counted, func(a, b entry) int { return strings.Compare(a.path, b.path) })
	est, estTotal, err := estimates(c.root, c.backend)
	if err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "prototype lines: %d\n", total)
	for _, f := range counted {
		fmt.Fprintf(&b, "  %s: %d\n", f.path, f.n)
	}
	fmt.Fprintf(&b, "missing for production (estimate): %d\n", estTotal)
	for _, e := range est {
		fmt.Fprintf(&b, "  %s: %s\n", e[0], e[1])
	}
	verdict := "PASS"
	if total+estTotal > 3000 {
		verdict = "FAIL"
	}
	fmt.Fprintf(&b, "total: %d + %d = %d (requirement <= 3000): %s\n", total, estTotal, total+estTotal, verdict)
	if err := os.WriteFile(c.out, []byte(b.String()), 0o600); err != nil {
		return err
	}
	fmt.Print(b.String())
	if verdict == "FAIL" {
		return errRequirement
	}
	return nil
}
