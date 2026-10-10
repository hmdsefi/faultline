// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package detlint

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// copyrightLine matches the copyright notice of the license header.
var copyrightLine = regexp.MustCompile(`Copyright \d{4} Hamed Yousefi`)

// licensed lists the source file extensions that carry the license header. The page template
// ui/static/timeline.html has none: the page it becomes must hold no "<!--", and the scripts and
// stylesheet inlined into it carry the notice.
var licensed = []string{".go", ".js", ".mjs", ".css"}

// TestLicenseHeaders checks that every source file in the repository, nested modules included,
// names its copyright holder and the MPL-2.0 license in its first three lines. Fixtures under
// testdata are skipped: their bytes and line numbers are what the tests read.
func TestLicenseHeaders(t *testing.T) {
	root := moduleRoot(t)
	var missing []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && (strings.HasPrefix(name, ".") || name == "testdata" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(name)
		for _, e := range licensed {
			if ext == e {
				if !hasLicenseHeader(t, path) {
					rel, _ := filepath.Rel(root, path)
					missing = append(missing, filepath.ToSlash(rel))
				}
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) > 0 {
		t.Errorf("%d file(s) have no license header in their first three lines:\n  %s\n"+
			"start each one with these two lines, as comments in the file's syntax:\n"+
			"  Copyright 2026 Hamed Yousefi\n  SPDX-License-Identifier: MPL-2.0",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// hasLicenseHeader reports whether the first three lines of path hold the copyright line and the
// SPDX identifier.
func hasLicenseHeader(t *testing.T, path string) bool {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // path comes from walking the repository
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var copyright, spdx bool
	s := bufio.NewScanner(f)
	for i := 0; i < 3 && s.Scan(); i++ {
		copyright = copyright || copyrightLine.MatchString(s.Text())
		spdx = spdx || strings.Contains(s.Text(), "SPDX-License-Identifier: MPL-2.0")
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	return copyright && spdx
}
