// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// section returns the text of the markdown section starting with heading, up to the next
// heading of the same or a higher level.
func section(doc, heading string) string {
	i := strings.Index(doc, heading+"\n")
	if i < 0 {
		return ""
	}
	level := strings.Count(strings.Fields(heading)[0], "#")
	rest := doc[i+len(heading)+1:]
	re := regexp.MustCompile(fmt.Sprintf(`(?m)^#{1,%d} `, level))
	if loc := re.FindStringIndex(rest); loc != nil {
		rest = rest[:loc[0]]
	}
	return strings.TrimSpace(rest)
}

// decisionCriteria are the rows of EXB-090's Results table, each with its Requirement cell.
var decisionCriteria = []struct{ name, requirement string }{
	{"Exactness C1", "100% (20 × 2 per test)"},
	{"Exactness C2, C3, C4, C5", "100% (1,000 × 2 each)"},
	{"Seed sensitivity C4 / C5", "≥ 4 / ≥ 95 distinct of 100"},
	{"Go 1.26 and 1.27 unmodified", "all criteria on both"},
	{"Slowdown C1 / C2 / C3", "≤ 5× (median of 3)"},
	{"Compatibility", "all C1 and C6 tests pass"},
	{"Size", "≤ 3,000 lines extrapolated"},
	{"Dependency hygiene", "third-party code only in nested modules"},
	{"In-process goroutine order C7", "none: recorded, not scored (EXB-068)"},
}

// TestDecisionRecord is the automated part of AT-EXB-12: RFC-0002 §4.5 follows EXB-090.
func TestDecisionRecord(t *testing.T) {
	rfc, dir := os.Getenv("EXB_RFC"), os.Getenv("EXB_DATA")
	run, err := decisionEnv(rfc, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !run {
		t.Skip("set EXB_RFC to .helper/docs/rfcs/0002-goroutine-determinism.md and EXB_DATA to its data directory")
	}
	for _, p := range decisionProblems(rfc, dir) {
		t.Error(p)
	}
}

// decisionEnv reports whether TestDecisionRecord runs for these values of EXB_RFC and EXB_DATA:
// it skips without EXB_RFC, and EXB_RFC without EXB_DATA is an error, not a skip.
func decisionEnv(rfc, dir string) (bool, error) {
	switch {
	case rfc == "":
		return false, nil
	case dir == "":
		return false, errors.New("EXB_RFC is set but EXB_DATA is not: set it to the record's data directory, .helper/docs/rfcs/0002-data")
	}
	return true, nil
}

// decisionProblems checks the RFC-0002 text in the file rfc, and the data directory dir, against
// EXB-090. It returns one message per problem, or nil.
func decisionProblems(rfc, dir string) []string {
	b, err := os.ReadFile(rfc) //nolint:gosec // G703: EXB_RFC names the file this test checks
	if err != nil {
		return []string{fmt.Sprintf("EXB_RFC: %v", err)}
	}
	doc := string(b)
	dec := section(doc, "### 4.5 Decision")
	if dec == "" {
		return []string{"no §4.5 Decision section"}
	}
	if strings.Contains(dec, "_To be recorded") {
		return []string{"the §4.5 placeholder is still there"}
	}
	if m := regexp.MustCompile(`<[^>\n]*>`).FindAllString(dec, -1); m != nil {
		return []string{fmt.Sprintf("unfilled <…> fields remain: %q", m)}
	}
	status := regexp.MustCompile(`(?m)^\| \*\*Status\*\* \| Part A accepted \(2026-10-04\)\. Part B decided \((\d{4}-\d{2}-\d{2})\): (wasip1|toolexec|Part A only) \|$`)
	st := status.FindStringSubmatch(doc)
	if st == nil {
		return []string{"the Status row does not follow EXB-090"}
	}
	var problems []string
	fields := map[string]string{}
	for _, field := range []string{"Decided", "Outcome", "Spike", "Toolchains", "Machines"} {
		re := regexp.MustCompile(`(?m)^\| \*\*` + field + `\*\* \| (.+) \|$`)
		m := re.FindStringSubmatch(dec)
		if m == nil || strings.TrimSpace(m[1]) == "" {
			problems = append(problems, fmt.Sprintf("field %s missing or empty", field))
			continue
		}
		fields[field] = strings.TrimSpace(m[1])
	}
	if d, ok := fields["Decided"]; ok && d != st[1] {
		problems = append(problems, fmt.Sprintf("the Status row's date %s is not the Decided cell's %s", st[1], d))
	}
	outcome := regexp.MustCompile("(?m)^\\| \\*\\*Outcome\\*\\* \\| (`wasip1`|`toolexec`|Part A only) \\|$")
	if m := outcome.FindStringSubmatch(dec); m == nil {
		problems = append(problems, "Outcome is not exactly one of `wasip1`, `toolexec`, Part A only")
	} else if o := strings.Trim(m[1], "`"); o != st[2] {
		problems = append(problems, fmt.Sprintf("the Status row's outcome %q is not the Outcome cell's %q", st[2], o))
	}
	results := section(dec, "#### Results")
	for _, c := range decisionCriteria {
		re := regexp.MustCompile(`(?m)^\| ` + regexp.QuoteMeta(c.name) + ` \|(.*)\|$`)
		m := re.FindStringSubmatch(results)
		if m == nil {
			problems = append(problems, fmt.Sprintf("results row %q missing", c.name))
			continue
		}
		cells := strings.Split(m[1], "|")
		if len(cells) != 4 {
			problems = append(problems, fmt.Sprintf("results row %q has %d cells after the name, want 4", c.name, len(cells)))
			continue
		}
		for i, cell := range cells {
			if strings.TrimSpace(cell) == "" {
				problems = append(problems, fmt.Sprintf("results row %q cell %d is empty", c.name, i+2))
			}
		}
		if r := strings.TrimSpace(cells[0]); r != "" && r != c.requirement {
			problems = append(problems, fmt.Sprintf("results row %q: Requirement %q, EXB-090 has %q", c.name, r, c.requirement))
		}
	}
	listed := map[string]string{}
	for _, m := range regexp.MustCompile("`([^`]+)`: ([0-9a-f]{64})").FindAllStringSubmatch(results, -1) {
		listed[m[1]] = m[2]
	}
	problems = append(problems, dataProblems(dir, listed)...)
	for _, h := range []string{"#### Findings", "#### Rationale", "#### Integration plan", "#### What would change this decision"} {
		if section(dec, h) == "" {
			problems = append(problems, fmt.Sprintf("section %q missing or empty", h))
		}
	}
	return problems
}

// dataProblems compares the files under dir with listed, the record's path to SHA-256 list.
func dataProblems(dir string, listed map[string]string) []string {
	info, err := os.Stat(dir) //nolint:gosec // G703: EXB_DATA names the directory this test reads
	switch {
	case err != nil:
		return []string{fmt.Sprintf("EXB_DATA: %v", err)}
	case !info.IsDir():
		return []string{fmt.Sprintf("EXB_DATA: %s is not a directory", dir)}
	}
	var onDisk []string
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error { //nolint:gosec // G703: EXB_DATA names the directory this test reads
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		onDisk = append(onDisk, filepath.ToSlash(rel))
		return err
	})
	switch {
	case err != nil:
		return []string{fmt.Sprintf("EXB_DATA: %v", err)}
	case len(onDisk) == 0:
		return []string{fmt.Sprintf("EXB_DATA: %s holds no file", dir)}
	}
	slices.Sort(onDisk)
	var problems []string
	for _, f := range onDisk {
		data, err := os.ReadFile(filepath.Join(dir, f)) //nolint:gosec // G703: EXB_DATA names the directory this test reads
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		sum := sha256.Sum256(data)
		if got, ok := listed[f]; !ok {
			problems = append(problems, fmt.Sprintf("data file %s is not listed with its SHA-256", f))
		} else if got != hex.EncodeToString(sum[:]) {
			problems = append(problems, fmt.Sprintf("data file %s: listed SHA-256 %s, actual %s", f, got, hex.EncodeToString(sum[:])))
		}
	}
	for _, f := range slices.Sorted(maps.Keys(listed)) {
		if !slices.Contains(onDisk, f) {
			problems = append(problems, fmt.Sprintf("listed file %s is not in the data directory", f))
		}
	}
	return problems
}

// decisionFixture is a short record that follows EXB-090, with ' for a backtick. Its data
// directory holds a.csv ("a\n") and logs/b.log ("b\n"); SUM_A and SUM_B stand for their SHA-256.
const decisionFixture = `# RFC-0002

| | |
|---|---|
| **Status** | Part A accepted (2026-10-04). Part B decided (2026-10-24): toolexec |

### 4.5 Decision

| | |
|---|---|
| **Decided** | 2026-10-24 |
| **Outcome** | 'toolexec' |
| **Spike** | branch 'spike/exb', commit 'e4b8e11' |
| **Toolchains** | go1.26.10, go1.27.3; wazero v1.12.0; gRPC v1.84.0 |
| **Machines** | slowdown: Apple M4 Max, macOS 27.0.1, runner 'local'; other criteria: runner 'local' |

#### Results

| Criterion | Requirement | Part A (reference) | wasip1 | toolexec |
|---|---|---|---|---|
| Exactness C1 | 100% (20 × 2 per test) | 1060/1060 | 1060/1060 | 1060/1060 |
| Exactness C2, C3, C4, C5 | 100% (1,000 × 2 each) | C2 1000/1000 | C2 1000/1000 | C2 1000/1000 |
| Seed sensitivity C4 / C5 | ≥ 4 / ≥ 95 distinct of 100 | 8 / 98 distinct | 8 / 99 distinct | 100 / 100 distinct |
| Go 1.26 and 1.27 unmodified | all criteria on both | not measured | no | yes |
| Slowdown C1 / C2 / C3 | ≤ 5× (median of 3) | 1× | C1 5.00× | C1 1.52× |
| Compatibility | all C1 and C6 tests pass | all 55 pass | 2 fail | all 55 pass |
| Size | ≤ 3,000 lines extrapolated | — | 439 + 640 = 1079 | 1624 + 1260 = 2884 |
| Dependency hygiene | third-party code only in nested modules | — | yes | yes |
| In-process goroutine order C7 | none: recorded, not scored (EXB-068) | 1 distinct, 100/100 same | 1 distinct, 100/100 same | 100 distinct, 100/100 same |

Raw data: 'data/', 2 files, each as path: SHA-256:
'a.csv': SUM_A;
'logs/b.log': SUM_B.

#### Findings

- A finding.

#### Rationale

A rationale.

#### Integration plan

A plan.

#### What would change this decision

- A trigger.

## 5. Next section
`

// TestDecisionChecks breaks a valid record one rule at a time and checks that decisionProblems
// reports each break. It runs without EXB_RFC and EXB_DATA.
func TestDecisionChecks(t *testing.T) {
	sum := func(s string) string {
		h := sha256.Sum256([]byte(s))
		return hex.EncodeToString(h[:])
	}
	zeros := strings.Repeat("0", 64)
	valid := strings.NewReplacer("'", "`", "SUM_A", sum("a\n"), "SUM_B", sum("b\n")).Replace(decisionFixture)
	setup := func(t *testing.T, doc string) (rfc, data string) {
		t.Helper()
		dir := t.TempDir()
		rfc, data = filepath.Join(dir, "rfc.md"), filepath.Join(dir, "data")
		for name, text := range map[string]string{rfc: doc, filepath.Join(data, "a.csv"): "a\n", filepath.Join(data, "logs", "b.log"): "b\n"} {
			if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(name, []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return rfc, data
	}
	if got := decisionProblems(setup(t, valid)); got != nil {
		t.Fatalf("valid record: %q", got)
	}
	tests := []struct {
		name     string
		old, new string                                                // the edit of the record; none when old is empty
		paths    func(t *testing.T, rfc, data string) (string, string) // replaces the two paths, if set
		want     string                                                // a reported problem contains it
	}{
		{name: "section heading", old: "### 4.5 Decision\n", new: "### 4.5 Decision record\n", want: "no §4.5 Decision section"},
		{name: "placeholder", old: "### 4.5 Decision\n", new: "### 4.5 Decision\n\n_To be recorded after the spike._\n", want: "the §4.5 placeholder is still there"},
		{name: "unfilled field", old: "| **Decided** | 2026-10-24 |", new: "| **Decided** | <YYYY-MM-DD> |", want: "unfilled <…> fields remain"},
		{name: "status row", old: "Part B decided (2026-10-24): toolexec |", new: "Part B decided (2026-10-24): prototype B |", want: "the Status row does not follow EXB-090"},
		{name: "status outcome", old: "Part B decided (2026-10-24): toolexec |", new: "Part B decided (2026-10-24): wasip1 |", want: `the Status row's outcome "wasip1" is not the Outcome cell's "toolexec"`},
		{name: "status date", old: "| **Decided** | 2026-10-24 |", new: "| **Decided** | 2026-10-23 |", want: `the Status row's date 2026-10-24 is not the Decided cell's 2026-10-23`},
		{name: "empty field", old: "| **Toolchains** | go1.26.10, go1.27.3; wazero v1.12.0; gRPC v1.84.0 |", new: "| **Toolchains** |   |", want: "field Toolchains missing or empty"},
		{name: "missing field", old: "**Machines**", new: "**Machine**", want: "field Machines missing or empty"},
		{name: "outcome", old: "| **Outcome** | `toolexec` |", new: "| **Outcome** | `toolexec` (prototype B) |", want: "Outcome is not exactly one of"},
		{name: "results row", old: "| Dependency hygiene | third-party code only in nested modules | — | yes | yes |\n", new: "", want: `results row "Dependency hygiene" missing`},
		{name: "cell count", old: "| Compatibility | all C1 and C6 tests pass | all 55 pass |", new: "| Compatibility | all C1 and C6 tests pass |", want: `results row "Compatibility" has 3 cells after the name, want 4`},
		{name: "empty cell", old: "| 439 + 640 = 1079 | 1624 + 1260 = 2884 |", new: "| 439 + 640 = 1079 |   |", want: `results row "Size" cell 5 is empty`},
		{name: "requirement", old: "| ≤ 5× (median of 3) |", new: "| ≤ 6× (median of 3) |", want: `results row "Slowdown C1 / C2 / C3": Requirement "≤ 6× (median of 3)", EXB-090 has "≤ 5× (median of 3)"`},
		{name: "sha-256", old: "`a.csv`: " + sum("a\n"), new: "`a.csv`: " + zeros, want: "data file a.csv: listed SHA-256 " + zeros},
		{name: "file not listed", old: "`logs/b.log`: " + sum("b\n") + ".", new: "", want: "data file logs/b.log is not listed with its SHA-256"},
		{name: "listed file missing", old: "`a.csv`: ", new: "`ghost.csv`: " + zeros + ";\n`a.csv`: ", want: "listed file ghost.csv is not in the data directory"},
		{name: "section missing", old: "#### Rationale\n", new: "#### Reasoning\n", want: `section "#### Rationale" missing or empty`},
		{name: "section empty", old: "#### What would change this decision\n", new: "#### What would change this decision\n\n#### Spare\n", want: `section "#### What would change this decision" missing or empty`},
		{name: "EXB_RFC missing", paths: func(_ *testing.T, rfc, data string) (string, string) { return rfc + ".missing", data }, want: "EXB_RFC: "},
		{name: "EXB_DATA missing", paths: func(_ *testing.T, rfc, data string) (string, string) { return rfc, data + ".missing" }, want: "EXB_DATA: "},
		{name: "EXB_DATA a file", paths: func(_ *testing.T, rfc, _ string) (string, string) { return rfc, rfc }, want: "is not a directory"},
		{name: "EXB_DATA empty", paths: func(t *testing.T, rfc, _ string) (string, string) { return rfc, t.TempDir() }, want: "holds no file"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := valid
			if tc.old != "" {
				if n := strings.Count(doc, tc.old); n != 1 {
					t.Fatalf("%q occurs %d times in the record, want 1", tc.old, n)
				}
				doc = strings.Replace(doc, tc.old, tc.new, 1)
			}
			rfc, data := setup(t, doc)
			if tc.paths != nil {
				rfc, data = tc.paths(t, rfc, data)
			}
			got := decisionProblems(rfc, data)
			if !slices.ContainsFunc(got, func(p string) bool { return strings.Contains(p, tc.want) }) {
				t.Errorf("problems %q, want one that contains %q", got, tc.want)
			}
		})
	}
}

// TestDecisionEnv checks when TestDecisionRecord runs: it skips without EXB_RFC, and EXB_RFC
// without EXB_DATA fails instead of skipping.
func TestDecisionEnv(t *testing.T) {
	tests := []struct {
		rfc, data string
		run, err  bool
	}{
		{"", "", false, false},
		{"", "data", false, false},
		{"rfc.md", "", false, true},
		{"rfc.md", "data", true, false},
	}
	for _, tc := range tests {
		run, err := decisionEnv(tc.rfc, tc.data)
		if run != tc.run || (err != nil) != tc.err {
			t.Errorf("decisionEnv(%q, %q) = %v, %v; want run %v, error %v", tc.rfc, tc.data, run, err, tc.run, tc.err)
		}
	}
}
