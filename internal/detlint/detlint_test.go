package detlint

import (
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// wantRE matches a fixture expectation: "// want DL001 DL002", "/* want DL000 */", "// want@1 DL011",
// and the class-qualified forms "want(core) …" and "want(tool) …".
var wantRE = regexp.MustCompile(`^(?://|/\*)\s*want(?:@(\d+))?(?:\((\w+)\))?((?:\s+DL\d{3})+)\s*(?:\*/)?$`)

// fixtureWants returns the expected "line rule" pairs of the fixture package in dir for class c.
func fixtureWants(t *testing.T, dir string, c Class) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var wants []string
	fset := token.NewFileSet()
	for _, e := range entries {
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		for _, cg := range f.Comments {
			for _, cm := range cg.List {
				m := wantRE.FindStringSubmatch(cm.Text)
				if m == nil || (m[2] != "" && m[2] != c.String()) {
					continue
				}
				line := fset.Position(cm.Pos()).Line
				if m[1] != "" {
					line, _ = strconv.Atoi(m[1])
				}
				for _, r := range strings.Fields(m[3]) {
					wants = append(wants, fmt.Sprintf("%s:%d %s", e.Name(), line, r))
				}
			}
		}
	}
	slices.Sort(wants)
	return wants
}

// AT-DET-01, AT-DET-02
func TestFixtures(t *testing.T) {
	cases := []struct {
		name    string
		classes []Class
	}{
		{"dl001", []Class{ClassCore}},
		{"dl002", []Class{ClassCore}},
		{"dl003", []Class{ClassCore}},
		{"dl004", []Class{ClassCore}},
		{"dl005", []Class{ClassCore}},
		{"dl006", []Class{ClassCore}},
		{"dl007", []Class{ClassCore}},
		{"dl008", []Class{ClassCore}},
		{"dl009", []Class{ClassCore}},
		{"dl010", []Class{ClassCore}},
		{"dl011", []Class{ClassCore}},
		{"clean", []Class{ClassCore}},
		{"directives", []Class{ClassCore, ClassTool}},
	}
	for _, tc := range cases {
		for _, c := range tc.classes {
			t.Run(tc.name+"/"+c.String(), func(t *testing.T) {
				dir := filepath.Join("testdata", "src", tc.name)
				fs, err := LintDir(dir, "fixture/"+tc.name, c)
				if err != nil {
					t.Fatal(err)
				}
				var got []string
				for _, f := range fs {
					got = append(got, fmt.Sprintf("%s:%d %s", f.File, f.Line, f.Rule))
				}
				slices.Sort(got)
				if want := fixtureWants(t, dir, c); !slices.Equal(got, want) {
					t.Errorf("findings:\n  %s\nwant:\n  %s\nall findings:\n  %s",
						strings.Join(got, "\n  "), strings.Join(want, "\n  "), findingLines(fs))
				}
			})
		}
	}
}

func findingLines(fs []Finding) string {
	var b strings.Builder
	for _, f := range fs {
		b.WriteString(f.String() + "\n  ")
	}
	return b.String()
}

// moduleRoot returns the root of the faultline module.
func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := FindRoot(".", "github.com/hmdsefi/faultline")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// report fails t with one error: prefix, the number of findings, and one finding per line.
func report(t testing.TB, prefix string, fs []Finding) {
	t.Helper()
	if len(fs) == 0 {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d finding(s):", prefix, len(fs))
	for _, f := range fs {
		b.WriteString("\n" + f.String())
	}
	t.Error(b.String())
}

// AT-DET-05, AT-KRN-45 (DET-016)
func TestDeterminismLint(t *testing.T) {
	fs, err := Lint(Faultline(moduleRoot(t)))
	if err != nil {
		t.Fatal(err)
	}
	report(t, "determinism lint", fs)
}

// reportTB records the errors that report sends to it.
type reportTB struct {
	testing.TB
	errs []string
}

func (r *reportTB) Helper()           {}
func (r *reportTB) Error(args ...any) { r.errs = append(r.errs, fmt.Sprint(args...)) }

// DET-016: report fails once per call with findings, with the count and one finding per line, and
// not at all without findings.
func TestReport(t *testing.T) {
	var tb reportTB
	report(&tb, "determinism lint", nil)
	report(&tb, "determinism lint", []Finding{{File: "a.go", Line: 1, Col: 2, Rule: RuleGo, Msg: "m"}})
	report(&tb, "determinism lint", []Finding{
		{File: "a.go", Line: 1, Col: 2, Rule: RuleGo, Msg: "m"},
		{File: "b", Rule: RuleMeta, Msg: "n"},
	})
	want := []string{
		"determinism lint: 1 finding(s):\na.go:1:2: DL004: m",
		"determinism lint: 2 finding(s):\na.go:1:2: DL004: m\nb:0:0: DL000: n",
	}
	if !slices.Equal(tb.errs, want) {
		t.Fatalf("errors %q, want %q", tb.errs, want)
	}
}

// AT-KRN-45 (DET-021)
func TestImportBoundaries(t *testing.T) {
	fs, err := CheckImports(Faultline(moduleRoot(t)))
	if err != nil {
		t.Fatal(err)
	}
	report(t, "import boundaries", fs)
}

var update = flag.Bool("update", false, "rewrite api/kernel.txt")

// snapshotDiff returns "" if the snapshot file content equals generated, otherwise the failure
// message of DET-027.
func snapshotDiff(file, generated string) string {
	split := func(s string) []string {
		var out []string
		for _, l := range strings.Split(s, "\n") {
			if l != "" {
				out = append(out, l)
			}
		}
		return out
	}
	have, want := split(file), split(generated)
	var removed, added []string
	for _, l := range have {
		if !slices.Contains(want, l) {
			removed = append(removed, "-"+l)
		}
	}
	for _, l := range want {
		if !slices.Contains(have, l) {
			added = append(added, "+"+l)
		}
	}
	if len(removed) == 0 && len(added) == 0 && file == generated {
		return ""
	}
	slices.Sort(removed)
	slices.Sort(added)
	lines := append([]string{"api/kernel.txt is out of date (run: go test ./internal/detlint -run TestAPISnapshot -update)"}, removed...)
	return strings.Join(append(lines, added...), "\n")
}

// AT-DET-09 (DET-027)
func TestAPISnapshot(t *testing.T) {
	root := moduleRoot(t)
	got, err := API(Faultline(root), APIPackages)
	if err != nil {
		t.Fatal(err)
	}
	checkSnapshot(t, filepath.Join(root, "api", "kernel.txt"), got, *update)
}

// checkSnapshot fails t if the snapshot file at path differs from generated (DET-027); with write
// it writes the file instead.
func checkSnapshot(t testing.TB, path, generated string, write bool) {
	t.Helper()
	if write {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(generated), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if msg := snapshotDiff(string(data), generated); msg != "" {
		t.Error(msg)
	}
}
