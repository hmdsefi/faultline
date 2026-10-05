package detlint

import (
	"slices"
	"strings"
	"testing"
)

// AT-DET-07
func TestCheckImportsTempModule(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"go.mod":             "module example.com/m\n\ngo 1.26\n",
		"kernel/k.go":        "package kernel\n\nimport \"testing\"\n\nvar _ testing.TB\n",
		"kernel/k_test.go":   "package kernel\n\nimport _ \"example.com/m/artifact\"\n",
		"kernel/w.go":        "package kernel\n\n//line gen.y:100\nimport _ \"example.com/m/artifact\"\n",
		"kernel/y.go":        "package kernel\n\nimport _ \"testing\"\n",
		"kernel/z.go":        "package kernel\n\nimport _ \"example.com/m/artifact\"\n",
		"kernel/simnet/n.go": "package simnet\n\nimport (\n\t_ \"example.com/m/artifact\"\n\t_ \"example.com/m/kernel\"\n)\n",
		"artifact/a.go":      "package artifact\n\nimport _ \"fmt\"\n",
		"harness/x/go.mod":   "module example.com/m/harness/x\n\ngo 1.26\n",
		"harness/x/x.go":     "package x\n\nimport _ \"github.com/forbidden/dep\"\n",
		"r.go":               "package m\n\nimport _ \"github.com/root/dep\"\n",
		"ui/u.go":            "package ui\n\nimport (\n\t_ \"example.com/m/harness/x\"\n\t_ \"github.com/other/lib\"\n\t_ \"strings\"\n)\n",
		"ui/u_windows.go":    "package ui\n\nimport _ \"github.com/windows/only\"\n",
		"ui/cgo.go":          "//go:build ignore\n\npackage ui\n\nimport \"C\"\n",
	})
	m := Module{Root: root, Path: "example.com/m", Classes: map[string]Class{"ui": ClassExempt}, Imports: map[string]Allow{
		"":              {Stdlib: true},
		"kernel":        {Stdlib: true, Gograph: true},
		"kernel/simnet": {Stdlib: true, Packages: []string{"kernel"}},
		"artifact":      {Stdlib: true},
		"ui":            {Stdlib: true},
	}}
	check := func(want ...string) {
		t.Helper()
		fs, err := CheckImports(m)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, f := range fs {
			got = append(got, f.String())
		}
		if !slices.Equal(got, want) {
			t.Fatalf("findings:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
		}
	}
	base := []string{
		`kernel/k.go:3:8: DL012: import "testing" is not allowed in package example.com/m/kernel`,
		`kernel/simnet/n.go:4:4: DL012: import "example.com/m/artifact" is not allowed in package example.com/m/kernel/simnet`,
		`kernel/w.go:4:10: DL012: import "example.com/m/artifact" is not allowed in package example.com/m/kernel`,
		`kernel/y.go:3:10: DL012: import "testing" is not allowed in package example.com/m/kernel`,
		`kernel/z.go:3:10: DL012: import "example.com/m/artifact" is not allowed in package example.com/m/kernel`,
		`r.go:3:10: DL012: import "github.com/root/dep" is not allowed in package example.com/m`,
	}
	check(append(base,
		`ui/u.go:4:4: DL012: import "example.com/m/harness/x" is not allowed in package example.com/m/ui`,
		`ui/u.go:5:4: DL012: import "github.com/other/lib" is not allowed in package example.com/m/ui`,
		`ui/u_windows.go:3:10: DL012: import "github.com/windows/only" is not allowed in package example.com/m/ui`)...)

	delete(m.Imports, "ui")
	check(append(base,
		"ui:0:0: DL000: package example.com/m/ui has no import rule; add it to DET §5.2 and internal/detlint")...)
}

// A file CheckImports cannot read is an error that names its package.
func TestCheckImportsErrors(t *testing.T) {
	for _, c := range []struct{ name, src string }{
		{"build line", "//go:build linux &&\n\npackage kernel\n"},
		{"import", "package kernel\n\nimport \"fmt\n"},
	} {
		root := t.TempDir()
		writeFiles(t, root, map[string]string{
			"go.mod":      "module example.com/m\n\ngo 1.26\n",
			"kernel/a.go": "package kernel\n",
			"kernel/b.go": c.src,
		})
		_, err := CheckImports(Module{Root: root, Path: "example.com/m", Imports: map[string]Allow{"kernel": {Stdlib: true}}})
		if err == nil || !strings.HasPrefix(err.Error(), "detlint: imports of example.com/m/kernel: ") {
			t.Errorf("%s: error %v", c.name, err)
		}
	}
}

// DET-021: classification of import paths.
func TestAllowed(t *testing.T) {
	m := Module{Path: "example.com/m"}
	a := Allow{Stdlib: true, Testing: []string{"testing/synctest"}, Gograph: true, Packages: []string{"", "kernel"}}
	cases := map[string]bool{
		"fmt":                              true,
		"net/http":                         true,
		"testing":                          false,
		"testing/synctest":                 true,
		"C":                                false,
		"example.com/m":                    true,
		"example.com/m/kernel":             true,
		"example.com/m/kernel/simnet":      false,
		"example.com/mx":                   false,
		"example.com/mkernel":              false,
		"github.com/hmdsefi/gograph":       true,
		"github.com/hmdsefi/gograph/graph": true,
		"github.com/hmdsefi/gographx":      false,
		"golang.org/x/tools":               false,
		"testingx":                         true,
		"vendor/golang.org/x/net/idna":     true,
	}
	for p, want := range cases {
		if got := allowed(m, a, p); got != want {
			t.Errorf("allowed(%q) = %v, want %v", p, got, want)
		}
	}
	if allowed(m, Allow{}, "fmt") || allowed(m, Allow{}, "github.com/hmdsefi/gograph") {
		t.Error("an empty Allow allows something")
	}
	if !allowed(m, Allow{Testing: []string{"testing"}}, "testing") {
		t.Error("the testing tree depends on Stdlib")
	}
}
