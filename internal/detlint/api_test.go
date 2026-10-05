package detlint

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const outOfDate = "api/kernel.txt is out of date (run: go test ./internal/detlint -run TestAPISnapshot -update)"

// AT-DET-08
func TestAPIFixture(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("testdata", "api"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := API(Module{Root: root, Path: "example.com"}, []string{"fix", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	const want = `pkg example.com/fix, const A = 1
pkg example.com/fix, const A ideal-int
pkg example.com/fix, const B = 2
pkg example.com/fix, const B time.Duration
pkg example.com/fix, const KX = "x"
pkg example.com/fix, const KX K
pkg example.com/fix, func G(int, int, ...string) *S
pkg example.com/fix, method (*S) Ptr(time.Duration)
pkg example.com/fix, method (S) Val() int
pkg example.com/fix, type Al = int
pkg example.com/fix, type F func(...string) bool
pkg example.com/fix, type I interface { M, unexported methods }
pkg example.com/fix, type I interface, M(int) (string, error)
pkg example.com/fix, type K string
pkg example.com/fix, type S struct
pkg example.com/fix, type S struct, X int
pkg example.com/fix, type S struct, embedded time.Time
pkg example.com/fix, var V error
`
	if got != want {
		t.Errorf("API:\n%s\nwant:\n%s", got, want)
	}
}

// DET-025 and DET-026: the kind of every untyped constant, names inside every composite type, the
// direction of a channel, type parameters, an unexported embedded field, a file the host context
// excludes, and a package listed twice.
func TestAPIKinds(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("testdata", "api"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := API(Module{Root: root, Path: "example.com"}, []string{"kinds", "kinds"})
	if err != nil {
		t.Fatal(err)
	}
	const want = `pkg example.com/kinds, const Big = 1267650600228229401496703205376
pkg example.com/kinds, const Big ideal-int
pkg example.com/kinds, const C = (0 + 2i)
pkg example.com/kinds, const C ideal-complex
pkg example.com/kinds, const Half = 3/2
pkg example.com/kinds, const Half ideal-float
pkg example.com/kinds, const R = 97
pkg example.com/kinds, const R ideal-rune
pkg example.com/kinds, const Re = 3
pkg example.com/kinds, const Re ideal-float
pkg example.com/kinds, const Str = "s"
pkg example.com/kinds, const Str ideal-string
pkg example.com/kinds, const T = true
pkg example.com/kinds, const T ideal-bool
pkg example.com/kinds, type E struct
pkg example.com/kinds, type E struct, X int
pkg example.com/kinds, type G[T any, U comparable] struct
pkg example.com/kinds, type G[T any, U comparable] struct, X T
pkg example.com/kinds, var A [2]func(int)
pkg example.com/kinds, var Ch <-chan func(int)
pkg example.com/kinds, var L []func(string)
pkg example.com/kinds, var M map[string]func(int) bool
pkg example.com/kinds, var N func(func(int))
pkg example.com/kinds, var P *func(int) error
`
	if got != want {
		t.Errorf("API:\n%s\nwant:\n%s", got, want)
	}
}

// DET-025: API returns the error of a package path that is not a directory, a bad build line, a
// syntax error and a type error.
func TestAPIErrors(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"notdir":        "",
		"badbuild/b.go": "//go:build (\n\npackage badbuild\n",
		"syntax/b.go":   "package syntax\n\nvar = 1\n",
		"typeerr/b.go":  "package typeerr\n\nvar X int = \"s\"\n",
	})
	for _, tc := range []struct{ pkg, want string }{
		{"notdir", filepath.Join(root, "notdir") + ": "},
		{"badbuild", "b.go: parsing //go:build line: "},
		{"syntax", filepath.Join(root, "syntax", "b.go") + ":3:5: "},
		{"typeerr", "detlint: type-checking example.com/typeerr: "},
	} {
		_, err := API(Module{Root: root, Path: "example.com"}, []string{tc.pkg})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want one containing %q", tc.pkg, err, tc.want)
		}
	}
}

// AT-DET-09: the snapshot diff names removed and added lines.
func TestSnapshotDiff(t *testing.T) {
	root := moduleRoot(t)
	generated, err := API(Faultline(root), APIPackages)
	if err != nil {
		t.Fatal(err)
	}
	const nowLine = "pkg github.com/hmdsefi/faultline/kernel, method (*Sim) Now() Time"
	for _, line := range []string{
		nowLine,
		"pkg github.com/hmdsefi/faultline/kernel, method (*Sim) After(time.Duration, string, func()) EventID",
		"pkg github.com/hmdsefi/faultline/kernel, func Uniform(*rand.Rand, time.Duration, time.Duration) time.Duration",
		"pkg github.com/hmdsefi/faultline/kernel, const TieBreakFIFO = 1",
	} {
		if !strings.Contains(generated, line+"\n") {
			t.Errorf("generated API lacks %q", line)
		}
	}
	dropped := strings.Replace(generated, nowLine+"\n", "", 1)
	if msg := snapshotDiff(dropped, generated); !strings.Contains(msg, "\n+"+nowLine) || !strings.HasPrefix(msg, outOfDate) {
		t.Errorf("dropped line: %q", msg)
	}
	if msg := snapshotDiff(generated+"pkg x, var Y int\n", generated); !strings.Contains(msg, "\n-pkg x, var Y int") {
		t.Errorf("extra line: %q", msg)
	}
	if msg := snapshotDiff(generated, generated); msg != "" {
		t.Errorf("identical: %q", msg)
	}
	if msg := snapshotDiff("", generated); !strings.Contains(msg, "\n+"+nowLine) {
		t.Error("a missing file is not treated as empty")
	}
	if msg, want := snapshotDiff("b\nz\nc\ny\n", "a\nb\nc\nx\n"), outOfDate+"\n-y\n-z\n+a\n+x"; msg != want {
		t.Errorf("removed and added: %q, want %q", msg, want)
	}
	for _, file := range []string{"a\na\nb\n", "b\na\n", "a\nb"} {
		if msg := snapshotDiff(file, "a\nb\n"); msg != outOfDate {
			t.Errorf("file %q: %q, want %q", file, msg, outOfDate)
		}
	}
}

// AT-DET-09: TestAPISnapshot fails on a missing or stale file, and -update writes the file, creating
// its directory.
func TestCheckSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api", "kernel.txt")
	var tb reportTB
	checkSnapshot(&tb, path, "a\nb\n", false)
	checkSnapshot(&tb, path, "a\nb\n", true)
	checkSnapshot(&tb, path, "a\nb\n", false)
	checkSnapshot(&tb, path, "a\nc\n", false)
	if want := []string{outOfDate + "\n+a\n+b", outOfDate + "\n-b\n+c"}; !slices.Equal(tb.errs, want) {
		t.Errorf("errors %q, want %q", tb.errs, want)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "a\nb\n" {
		t.Errorf("written file %q, %v", data, err)
	}
}
