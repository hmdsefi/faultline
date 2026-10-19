// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package agents_test

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// notVariants are the folders in testdata/<version>/ that hold no bug: the reference service, the
// hidden checks and tier 2's extra test.
var notVariants = map[string]bool{"reference": true, "hidden": true, "extra": true}

// TestPatchesMatchVariants applies each <tier>/service/patches/<version>-<bug>.diff to
// testdata/<version>/reference/ and requires the result to equal testdata/<version>/<bug>/ byte
// for byte, with no file missing or extra. Every variant folder needs a patch.
func TestPatchesMatchVariants(t *testing.T) {
	services, err := filepath.Glob(filepath.Join("tier*", "service"))
	if err != nil {
		t.Fatal(err)
	}
	if len(services) == 0 {
		t.Fatal("found no tier*/service folder")
	}
	for _, svc := range services {
		patches, err := filepath.Glob(filepath.Join(svc, "patches", "*.diff"))
		if err != nil {
			t.Fatal(err)
		}
		if len(patches) == 0 {
			t.Errorf("%s: found no patches/*.diff", svc)
		}
		patched := map[string]bool{}
		for _, p := range patches {
			name := strings.TrimSuffix(filepath.Base(p), ".diff")
			version, bug, ok := strings.Cut(name, "-")
			if !ok || notVariants[bug] {
				t.Errorf("%s: want a name <version>-<bug>.diff", p)
				continue
			}
			patched[version+"/"+bug] = true
			t.Run(filepath.Dir(svc)+"/"+name, func(t *testing.T) {
				data := filepath.Join(svc, "testdata", version)
				diff, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				reference, err := readTree(filepath.Join(data, "reference"))
				if err != nil {
					t.Fatal(err)
				}
				variant, err := readTree(filepath.Join(data, bug))
				if err != nil {
					t.Fatal(err)
				}
				got, err := apply(reference, string(diff), bug)
				if err != nil {
					t.Fatalf("%s: %v", p, err)
				}
				for _, problem := range compareTrees(got, variant) {
					t.Error(problem)
				}
			})
		}
		dirs, err := filepath.Glob(filepath.Join(svc, "testdata", "*", "*"))
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range dirs {
			fi, err := os.Stat(d)
			if err != nil {
				t.Fatal(err)
			}
			bug, version := filepath.Base(d), filepath.Base(filepath.Dir(d))
			if fi.IsDir() && !notVariants[bug] && !patched[version+"/"+bug] {
				t.Errorf("%s has no patch %s-%s.diff in %s", d, version, bug, filepath.Join(svc, "patches"))
			}
		}
	}
}

// readTree returns every file under dir, keyed by its slash-separated path relative to dir.
func readTree(dir string) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", path)
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		files[filepath.ToSlash(rel)] = b
		return err
	})
	return files, err
}

// compareTrees returns a line for each file that got and want do not hold with the same bytes.
func compareTrees(got, want map[string][]byte) []string {
	names := map[string]bool{}
	for n := range got {
		names[n] = true
	}
	for n := range want {
		names[n] = true
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	var problems []string
	for _, n := range sorted {
		g, inGot := got[n]
		w, inWant := want[n]
		switch {
		case !inGot:
			problems = append(problems, n+": the variant has it, the patched reference does not")
		case !inWant:
			problems = append(problems, n+": the patched reference has it, the variant does not")
		case !bytes.Equal(g, w):
			problems = append(problems, fmt.Sprintf("%s: the patched reference and the variant differ from line %d",
				n, firstDiffLine(g, w)))
		}
	}
	return problems
}

// firstDiffLine returns the 1-based number of the first line where a and b differ.
func firstDiffLine(a, b []byte) int {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	return bytes.Count(a[:i], []byte("\n")) + 1
}

// apply applies a patch made by `diff -ru reference <bug>` to the files of reference and returns
// the result. Every hunk must sit where its header says, every context and removed line must equal
// the reference line it stands for, and every hunk must hold the line counts its header gives.
func apply(reference map[string][]byte, patch, bug string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(reference))
	for n, b := range reference {
		out[n] = b
	}
	lines := strings.SplitAfter(patch, "\n")
	if last := lines[len(lines)-1]; last != "" {
		return nil, fmt.Errorf("the last line %q has no newline", last)
	}
	lines = lines[:len(lines)-1]
	done := map[string]bool{}
	for i := 0; i < len(lines); {
		if strings.HasPrefix(lines[i], "diff ") {
			i++
			continue
		}
		if !strings.HasPrefix(lines[i], "--- ") || i+1 == len(lines) || !strings.HasPrefix(lines[i+1], "+++ ") {
			return nil, fmt.Errorf("line %d: want a --- and +++ file header, got %q", i+1, lines[i])
		}
		name, ok := strings.CutPrefix(headerPath(lines[i]), "reference/")
		if !ok || headerPath(lines[i+1]) != bug+"/"+name {
			return nil, fmt.Errorf("line %d: want reference/<file> and %s/<file>, got %q and %q",
				i+1, bug, headerPath(lines[i]), headerPath(lines[i+1]))
		}
		src, ok := reference[name]
		if !ok {
			return nil, fmt.Errorf("line %d: the reference has no file %s", i+1, name)
		}
		if done[name] {
			return nil, fmt.Errorf("line %d: %s is patched twice", i+1, name)
		}
		done[name] = true
		next, b, err := applyFile(lines, i+2, splitLines(string(src)))
		if err != nil {
			return nil, fmt.Errorf("%s: %v", name, err)
		}
		out[name], i = b, next
	}
	if len(done) == 0 {
		return nil, fmt.Errorf("the patch changes no file")
	}
	return out, nil
}

// applyFile applies the hunks that start at lines[i] to old, a file's lines. It returns the index
// of the first line after the hunks and the patched file.
func applyFile(lines []string, i int, old []string) (int, []byte, error) {
	var res []string
	pos := 0 // the first line of old not yet copied or consumed
	hunks := 0
	for ; i < len(lines) && strings.HasPrefix(lines[i], "@@ "); hunks++ {
		oStart, oCount, nStart, nCount, err := hunkHeader(lines[i])
		if err != nil {
			return 0, nil, fmt.Errorf("line %d: %v", i+1, err)
		}
		// An empty range names the line before it.
		start := oStart - 1
		if oCount == 0 {
			start = oStart
		}
		if start < pos || start > len(old) {
			return 0, nil, fmt.Errorf("line %d: the hunk starts at line %d, outside the file or before the last hunk's end", i+1, oStart)
		}
		res = append(res, old[pos:start]...)
		pos = start
		wantN := len(res) + 1
		if nCount == 0 {
			wantN = len(res)
		}
		if nStart != wantN {
			return 0, nil, fmt.Errorf("line %d: the new side starts at line %d, want %d", i+1, nStart, wantN)
		}
		header := i + 1
		i++
		o, n := 0, 0
		for o < oCount || n < nCount {
			if i == len(lines) {
				return 0, nil, fmt.Errorf("line %d: the patch ends inside the hunk", header)
			}
			l := lines[i]
			if l == "\n" || l == "" {
				return 0, nil, fmt.Errorf("line %d: an empty line inside a hunk", i+1)
			}
			// "\ No newline at end of file" applies to the line before it.
			text, noNewline := l[1:], i+1 < len(lines) && strings.HasPrefix(lines[i+1], `\`)
			if noNewline {
				text = strings.TrimSuffix(text, "\n")
			}
			switch l[0] {
			case ' ', '-':
				if pos == len(old) || old[pos] != text {
					got := "the end of the file"
					if pos < len(old) {
						got = strconv.Quote(old[pos])
					}
					return 0, nil, fmt.Errorf("line %d: want %q at line %d of the reference, got %s", i+1, text, pos+1, got)
				}
				pos++
				o++
				if l[0] == ' ' {
					res = append(res, text)
					n++
				}
			case '+':
				res = append(res, text)
				n++
			default:
				return 0, nil, fmt.Errorf("line %d: want a context, removed or added line, got %q", i+1, l)
			}
			if o > oCount || n > nCount {
				return 0, nil, fmt.Errorf("line %d: the hunk holds more lines than its header gives", header)
			}
			i++
			if noNewline {
				i++
			}
		}
	}
	if hunks == 0 {
		return 0, nil, fmt.Errorf("line %d: a file header with no hunk", i)
	}
	res = append(res, old[pos:]...)
	return i, []byte(strings.Join(res, "")), nil
}

// headerPath returns the path of a --- or +++ line, without the timestamp after the tab.
func headerPath(line string) string {
	p, _, _ := strings.Cut(strings.TrimSuffix(line[4:], "\n"), "\t")
	return p
}

// hunkHeader parses "@@ -a,b +c,d @@".
func hunkHeader(line string) (oStart, oCount, nStart, nCount int, err error) {
	rest, ok := strings.CutPrefix(line, "@@ -")
	ranges, _, found := strings.Cut(rest, " @@")
	oldRange, newRange, plus := strings.Cut(ranges, " +")
	if !ok || !found || !plus {
		return 0, 0, 0, 0, fmt.Errorf("want a hunk header @@ -a,b +c,d @@, got %q", line)
	}
	if oStart, oCount, err = hunkRange(oldRange); err == nil {
		nStart, nCount, err = hunkRange(newRange)
	}
	if err != nil {
		err = fmt.Errorf("%q: %v", line, err)
	}
	return oStart, oCount, nStart, nCount, err
}

// hunkRange parses "a,b" or "a" of a hunk header; a missing count is 1.
func hunkRange(s string) (start, count int, err error) {
	a, b, comma := strings.Cut(s, ",")
	if start, err = strconv.Atoi(a); err != nil {
		return 0, 0, err
	}
	count = 1
	if comma {
		if count, err = strconv.Atoi(b); err != nil {
			return 0, 0, err
		}
	}
	if start < 0 || count < 0 {
		return 0, 0, fmt.Errorf("a negative line number or count in %q", s)
	}
	return start, count, nil
}

// splitLines splits s after each newline; a last line without one is kept.
func splitLines(s string) []string {
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
