// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// AT-XTE-05: a link that fails with a fingerprint mismatch naming sched or internal/gosched gets
// §7's stale-state message after the linker's output; another mismatch, or another error naming
// sched, gets the linker's output only.
func TestATXTE05_StaleStateMessage(t *testing.T) {
	tc := currentToolchain(t)
	env := []string{"FAULTLINE_TOOLEXEC_STATE=" + stateCopy(t, tc), "TOOLEXEC_IMPORTPATH=example.com/u"}
	dir := userModule(t, "importcfg.link", "packagefile example.com/u=u.a\n")
	stale := "\nfaultline-toolexec: the prepared sched does not match this build (another checkout or version of faultline, or other build flags); run \"faultline-toolexec prepare\" in this module with the build's flags, or give each checkout its own FAULTLINE_TOOLEXEC_STATE\n"
	for _, c := range []struct{ line, want string }{
		{"fingerprint mismatch: " + goschedPath + " has 1, import from " + schedPath + " expecting 2", stale},
		{"fingerprint mismatch: sync has 1, import from " + schedPath + " expecting 2", stale},
		{"fingerprint mismatch: " + goschedPath + " has 1, import from example.com/u expecting 2", stale},
		{"fingerprint mismatch: example.com/a has 1, import from example.com/b expecting 2", "\n"},
		{"cannot find package " + schedPath + " (using -importcfg)", "\n"},
	} {
		tool := filepath.Join(t.TempDir(), "link")
		script := "#!/bin/sh\n" +
			"if [ \"$1\" = -V=full ]; then echo \"link version " + tc.version + "\"; exit 0; fi\n" +
			"echo '" + c.line + "' >&2\nexit 2\n"
		if err := os.WriteFile(tool, []byte(script), 0o700); err != nil { //nolint:gosec // a stand-in tool must be executable
			t.Fatal(err)
		}
		out, code := wrapperRun(t, env, tool, "-o", filepath.Join(dir, "m"), "-importcfg", filepath.Join(dir, "importcfg.link"), filepath.Join(dir, "u.a"))
		if code != 2 || out != c.line+c.want {
			t.Errorf("exit %d, %q; want exit 2, %q", code, out, c.line+c.want)
		}
	}
}

// AT-XTE-06
func TestATXTE06_RewriteSet(t *testing.T) {
	root := t.TempDir()
	file := func(rel, content string) string {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	file("fl/go.mod", "module github.com/hmdsefi/faultline\n")
	fa, fat := file("fl/a.go", "package a\n"), file("fl/a_test.go", "package a\n")
	ka, kat := file("fl/kernel/k.go", "package kernel\n"), file("fl/kernel/k_test.go", "package kernel\n")
	file("fl/shims/grpcsim/go.mod", "module github.com/hmdsefi/faultline/shims/grpcsim\n")
	ga, gat := file("fl/shims/grpcsim/g.go", "package grpcsim\n"), file("fl/shims/grpcsim/g_test.go", "package grpcsim\n")
	file("user/go.mod", "module example.com/u\n")
	ua, uat := file("user/u.go", "package u\n"), file("user/u_test.go", "package u\n")
	file("flx/go.mod", "module github.com/hmdsefi/faultline-x\n")
	xa, xat := file("flx/x.go", "package x\n"), file("flx/x_test.go", "package x\n")
	std := file("goroot/src/net/http/server.go", "package http\n")
	dd := file("modcache/example.com/!d@v1.0.0/d.go", "package d\n")
	ee := file("modcache/example.com/e@v1.0.0/e.go", "package e\n")
	dx := file("modcache/example.com/!dx@v1.0.0/x.go", "package dx\n")
	work := file("work/b001/u.cgo1.go", "package u\n")
	roots := map[string]string{"GOROOT": filepath.Join(root, "goroot"), "GOMODCACHE": filepath.Join(root, "modcache")}
	check := func(name, ip string, files []string, want string, wantOutside bool) {
		t.Helper()
		sel, outside := rewriteFiles(ip, files, roots)
		var got []string
		for f := range sel {
			got = append(got, filepath.Base(f))
		}
		slices.Sort(got)
		if g := strings.Join(got, ","); g != want || outside != wantOutside {
			t.Errorf("%s: %q, outside %v; want %q, outside %v", name, g, outside, want, wantOutside)
		}
	}
	t.Setenv("FAULTLINE_TOOLEXEC_STD", "")
	t.Setenv("FAULTLINE_TOOLEXEC_DEPS", "")
	check("faultline root: tests only", "github.com/hmdsefi/faultline [github.com/hmdsefi/faultline.test]", []string{fa, fat}, "a_test.go", false)
	check("faultline package below the go.mod: tests only", "github.com/hmdsefi/faultline/kernel", []string{ka, kat}, "k_test.go", false)
	check("nested faultline module: tests only", "github.com/hmdsefi/faultline/shims/grpcsim", []string{ga, gat}, "g_test.go", false)
	check("user module: all", "example.com/u [example.com/u.test]", []string{ua, uat}, "u.go,u_test.go", false)
	check("a file outside any module", "example.com/u", []string{ua, work}, "", true)
	check("test main: none", "example.com/u.test", []string{ua}, "", false)
	check("sched: none", schedPath, []string{ua}, "", false)
	check("gosched tests: none", goschedPath+" ["+goschedPath+".test]", []string{fa, fat}, "", false)
	check("std: none", "net/http", []string{std}, "", false)
	check("module cache: none", "example.com/D", []string{dd}, "", false)
	check("std with a $WORK file: none, no gap", "net/http", []string{std, work}, "", false)
	check("module cache with a $WORK file: none, no gap", "example.com/D", []string{dd, work}, "", false)
	check("module path extending faultline's: all", "github.com/hmdsefi/faultline-x", []string{xa, xat}, "x.go,x_test.go", false)
	t.Setenv("FAULTLINE_TOOLEXEC_STD", "fmt, net/http")
	t.Setenv("FAULTLINE_TOOLEXEC_DEPS", " example.com/D ,example.com/x")
	check("listed std: all", "net/http", []string{std}, "server.go", false)
	check("listed module, case-encoded: all", "example.com/D", []string{dd}, "d.go", false)
	check("unlisted module: none", "example.com/e", []string{ee}, "", false)
	check("module path extending a listed one: none", "example.com/Dx", []string{dx}, "", false)
	check("listed std with a $WORK file", "net/http", []string{std, work}, "", true)
	check("listed module with a $WORK file", "example.com/D", []string{dd, work}, "", true)
	// A downloaded toolchain's GOROOT lies inside GOMODCACHE; its packages are still standard-library
	// packages. The last row: it moves GOROOT.
	tcRoot := "modcache/golang.org/toolchain@v0.0.1-go1.26.0.linux-amd64"
	tcStd := file(tcRoot+"/src/net/http/server.go", "package http\n")
	roots["GOROOT"] = filepath.Join(root, tcRoot)
	check("listed std of a toolchain in the module cache: all", "net/http", []string{tcStd}, "server.go", false)
}

// XTE-023, XTE-024: a test binary whose package has nothing to rewrite still links sched, and its
// link stores the wrapper's path for replay commands.
func TestTestMainLinksSchedAndWrapperPath(t *testing.T) {
	log := filepath.Join(t.TempDir(), "wrapper.log")
	dir := userModule(t, "u_test.go", "package u\n\nimport \"testing\"\n\nfunc TestU(t *testing.T) {}\n")
	bin := filepath.Join(t.TempDir(), "u.test")
	build := exec.Command("go", "test", "-c", "-o", bin, "-toolexec="+wrapper, ".")
	build.Dir = dir
	kept := t.TempDir()
	build.Env = append(os.Environ(), "GOWORK=off", "FAULTLINE_TOOLEXEC_LOG="+log, "FAULTLINE_TOOLEXEC_KEEP="+kept)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go test -c -toolexec: %v\n%s", err, out)
	}
	if !linksSched(t, bin) {
		t.Error("the test binary does not link sched")
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "compile example.com/u.test testmain\n") {
		t.Errorf("log lacks the test main line:\n%s", b)
	}
	gen, err := os.ReadFile(filepath.Join(kept, "example.com_u.test", "__fl_testmain.go"))
	if want := "package main\n\nimport _ \"" + schedPath + "\"\n"; err != nil || string(gen) != want {
		t.Errorf("kept test main file: %q, %v; want %q", gen, err, want)
	}
	if out, err := exec.Command(bin, "-test.count=1", "-test.timeout=1m").CombinedOutput(); err != nil { //nolint:gosec // the binary the test built
		t.Fatalf("the test binary: %v\n%s", err, out)
	}
	// The linker sets a variable with -X only when the program reads it, and this binary does not
	// read gosched.Wrapper, so a stand-in link shows the argument the wrapper adds.
	exe, err := filepath.EvalSymlinks(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(t.TempDir(), "link")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = -V=full ]; then echo \"link version " + currentToolchain(t).version + "\"; exit 0; fi\n" +
		"echo \"$@\"\n"
	if err := os.WriteFile(tool, []byte(script), 0o700); err != nil { //nolint:gosec // a stand-in tool must be executable
		t.Fatal(err)
	}
	cfg := filepath.Join(t.TempDir(), "importcfg.link")
	if err := os.WriteFile(cfg, []byte("packagefile example.com/u.test=u.a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// XTE-024: the base name when PATH finds the wrapper's own file, else the resolved path.
	for _, c := range []struct{ path, w string }{
		{os.Getenv("PATH"), exe},
		{filepath.Dir(wrapper) + string(os.PathListSeparator) + os.Getenv("PATH"), "faultline-toolexec"},
	} {
		out, code := wrapperRun(t, []string{"TOOLEXEC_IMPORTPATH=example.com/u.test", "PATH=" + c.path}, tool, "-o", bin, "-importcfg", cfg, "u.a")
		if want := "-X " + goschedPath + ".Wrapper=" + c.w + " -o " + bin + " "; code != 0 || !strings.HasPrefix(out, want) {
			t.Errorf("stand-in link: exit %d, %q; want exit 0 and arguments starting %q", code, out, want)
		}
	}
}

// XTE-026: a test main's compile that fails before it runs the tool (here the importcfg is
// missing) exits 2 and removes the directory of its generated file.
func TestTestMainCompileFailureRemovesTempDir(t *testing.T) {
	tc := currentToolchain(t)
	tmp, dir := t.TempDir(), t.TempDir()
	cfg := filepath.Join(dir, "importcfg")
	env := []string{"FAULTLINE_TOOLEXEC_STATE=" + stateCopy(t, tc), "TMPDIR=" + tmp, "TOOLEXEC_IMPORTPATH=example.com/u.test"}
	out, code := wrapperRun(t, env, compileArgs(tc, "main", cfg, filepath.Join(dir, "m.a"), filepath.Join(dir, "_testmain.go"))...)
	if want := "faultline-toolexec: open " + cfg + ": no such file or directory\n"; code != 2 || out != want {
		t.Errorf("exit %d, %q; want exit 2, %q", code, out, want)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("left behind in TMPDIR: %s", e.Name())
	}
}
