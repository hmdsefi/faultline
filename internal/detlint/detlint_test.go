package detlint

import (
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
