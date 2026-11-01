// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var wrapper string // the faultline-toolexec binary under test

func TestMain(m *testing.M) {
	code, err := setup(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(code)
}

func setup(m *testing.M) (int, error) {
	dir, err := os.MkdirTemp("", "faultline-toolexec-test-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	wrapper = filepath.Join(dir, "faultline-toolexec")
	if out, err := exec.Command("go", "build", "-o", wrapper, ".").CombinedOutput(); err != nil {
		return 0, fmt.Errorf("building faultline-toolexec: %v\n%s", err, out)
	}
	if err := os.Setenv("FAULTLINE_TOOLEXEC_STATE", filepath.Join(dir, "state")); err != nil {
		return 0, err
	}
	prep := exec.Command(wrapper, "prepare")
	prep.Dir = "../.." // the module root: go list resolves sched there (EXB-031)
	// GOWORK=off as for the fixture builds (buildWithWrapper), so sched's export data and the
	// builds resolve the same module versions.
	prep.Env = append(os.Environ(), "GOWORK=off")
	if out, err := prep.CombinedOutput(); err != nil {
		return 0, fmt.Errorf("faultline-toolexec prepare: %v\n%s", err, out)
	}
	return m.Run(), nil
}

func TestMergeImportcfg(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "orig")
	if err := os.WriteFile(cfg, []byte("# import config\nimportmap x=y\npackagefile a=/a1.a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	merged, err := mergeImportcfg(cfg, []string{"packagefile a=/a2.a", "packagefile b=/b.a"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(merged)
	if string(b) != "# import config\nimportmap x=y\npackagefile a=/a1.a\npackagefile b=/b.a\n" {
		t.Fatalf("merged importcfg %q", b)
	}
}

func TestFlagValue(t *testing.T) {
	args := []string{"-o", "x.a", "-p", "example.com/u", "-importcfg=/tmp/cfg", "-goversion", "go1.27.1"}
	if v, i := flagValue(args, "-p"); v != "example.com/u" || i != 3 {
		t.Fatalf("-p: %q %d", v, i)
	}
	if v, i := flagValue(args, "-importcfg"); v != "/tmp/cfg" || i != 4 {
		t.Fatalf("-importcfg: %q %d", v, i)
	}
	if v, i := flagValue(args, "-lang"); v != "" || i != -1 {
		t.Fatalf("-lang: %q %d", v, i)
	}
}

// goEnv returns the go env values of names, in order.
func goEnv(t *testing.T, names ...string) []string {
	t.Helper()
	out, err := exec.Command("go", append([]string{"env"}, names...)...).Output() //nolint:gosec // go env with the test's names
	if err != nil {
		t.Fatalf("go env: %v", err)
	}
	v := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(v) != len(names) {
		t.Fatalf("go env %v: %q", names, out)
	}
	return v
}

// wrapperRun runs the wrapper with args and env added to the test's environment, and returns its
// combined output and exit code.
func wrapperRun(t *testing.T, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(wrapper, args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		t.Fatalf("running the wrapper: %v", err)
	}
	return string(out), cmd.ProcessState.ExitCode()
}

// toolchain is go env GOVERSION, GOOS, GOARCH and GOTOOLDIR.
type toolchain struct{ version, goos, goarch, tooldir string }

func currentToolchain(t *testing.T) toolchain {
	v := goEnv(t, "GOVERSION", "GOOS", "GOARCH", "GOTOOLDIR")
	return toolchain{v[0], v[1], v[2], v[3]}
}

// key is the base name of prepare's files for the toolchain.
func (tc toolchain) key() string { return tc.version + "-" + tc.goos + "-" + tc.goarch }

// stateCopy returns a new state directory with copies of the files TestMain's prepare wrote.
func stateCopy(t *testing.T, tc toolchain) string {
	t.Helper()
	dir := t.TempDir()
	for _, ext := range []string{".importcfg", "-race.importcfg", ".env"} {
		b, err := os.ReadFile(filepath.Join(os.Getenv("FAULTLINE_TOOLEXEC_STATE"), tc.key()+ext))
		if errors.Is(err, fs.ErrNotExist) && ext == "-race.importcfg" {
			continue // no -race support on this host
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, tc.key()+ext), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// readPrepared returns the packagefile lines of prepare's importcfg by import path.
func readPrepared(t *testing.T, name string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, l := range strings.Split(string(b), "\n") {
		if l == "" {
			continue
		}
		rest, ok := strings.CutPrefix(l, "packagefile ")
		p, f, ok2 := strings.Cut(rest, "=")
		if !ok || !ok2 {
			t.Fatalf("%s: line %q", name, l)
		}
		files[p] = f
	}
	return files
}

// linksSched reports whether go tool nm lists a symbol of package sched in the program bin.
func linksSched(t *testing.T, bin string) bool {
	t.Helper()
	out, err := exec.Command("go", "tool", "nm", bin).Output() //nolint:gosec // the program the test built
	if err != nil {
		t.Fatalf("go tool nm %s: %v", bin, err)
	}
	return strings.Contains(string(out), " "+schedPath+".")
}

// TestStdInBuildCacheKey builds a program that imports os/signal through the wrapper three times
// in one empty build cache: by default, with FAULTLINE_TOOLEXEC_STD=os/signal, and by default
// again. The go command keys cached packages on the -V=full line, so the second build must not
// reuse the unrewritten os/signal of the first, and the third must not reuse the rewritten one of
// the second.
func TestStdInBuildCacheKey(t *testing.T) {
	tc := currentToolchain(t)
	state, cache, logs := stateCopy(t, tc), t.TempDir(), t.TempDir()
	dir := userModule(t, "main.go", "package main\n\nimport (\n\t\"os\"\n\t\"os/signal\"\n)\n\n"+
		"func main() {\n\tc := make(chan os.Signal, 1)\n\tsignal.Notify(c, os.Interrupt)\n\tsignal.Stop(c)\n}\n")
	for i, c := range []struct {
		std, log string
		sched    bool
	}{
		{"", "compile os/signal unchanged", false},
		{"os/signal", "compile os/signal rewritten", true},
		{"", "", false},
	} {
		bin, log := filepath.Join(dir, fmt.Sprintf("p%d", i)), filepath.Join(logs, fmt.Sprintf("%d.log", i))
		build := exec.Command("go", "build", "-p", "2", "-toolexec", wrapper, "-o", bin, ".")
		build.Dir = dir
		build.Env = append(os.Environ(), "GOWORK=off", "GOCACHE="+cache, "FAULTLINE_TOOLEXEC_STATE="+state,
			"FAULTLINE_TOOLEXEC_STD="+c.std, "FAULTLINE_TOOLEXEC_LOG="+log)
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build %d: %v\n%s", i, err, out)
		}
		b, _ := os.ReadFile(log) //nolint:gosec // the log file the test names; a build from the cache writes none
		got := ""
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "compile os/signal ") {
				got = l
			}
		}
		if got != c.log {
			t.Errorf("build %d (FAULTLINE_TOOLEXEC_STD=%q): os/signal log line %q, want %q", i, c.std, got, c.log)
		}
		if got := linksSched(t, bin); got != c.sched {
			t.Errorf("build %d (FAULTLINE_TOOLEXEC_STD=%q): links sched %v, want %v", i, c.std, got, c.sched)
		}
	}
}

// TestOtherToolExitCode: a tool other than compile and link runs unchanged, and the wrapper exits
// with the tool's exit code.
func TestOtherToolExitCode(t *testing.T) {
	if out, code := wrapperRun(t, nil, "sh", "-c", "echo ran; exit 3"); code != 3 || out != "ran\n" {
		t.Fatalf("exit %d, %q; want exit 3, %q", code, out, "ran\n")
	}
}

// TestMissingPrepare: compile and link fail with exit 2 when prepare has not run for the
// toolchain.
func TestMissingPrepare(t *testing.T) {
	tc := currentToolchain(t)
	env := []string{"FAULTLINE_TOOLEXEC_STATE=" + t.TempDir(), "GOOS=" + tc.goos, "GOARCH=" + tc.goarch}
	want := fmt.Sprintf("faultline-toolexec: run \"faultline-toolexec prepare\" for %s %s/%s\n", tc.version, tc.goos, tc.goarch)
	for _, args := range [][]string{
		{filepath.Join(tc.tooldir, "compile"), "-p", "example.com/u", "-goversion", tc.version, "u.go"},
		{filepath.Join(tc.tooldir, "link"), "-importcfg", "importcfg.link", "-o", "x", "x.a"},
	} {
		if out, code := wrapperRun(t, env, args...); code != 2 || out != want {
			t.Errorf("%s: exit %d, %q; want exit 2, %q", filepath.Base(args[0]), code, out, want)
		}
	}
}

// TestInvalidPrepareState: a compile fails with exit 2 when one of prepare's files is missing, cut
// short or not written by prepare, instead of using it.
func TestInvalidPrepareState(t *testing.T) {
	tc := currentToolchain(t)
	state := filepath.Join(stateCopy(t, tc), tc.key())
	read := func(ext string) string {
		b, err := os.ReadFile(state + ext)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	cfg, env := read(".importcfg"), read(".env")
	lastLine := strings.LastIndex(strings.TrimSuffix(cfg, "\n"), "\n") + 1
	roots := goEnv(t, "GOROOT", "GOMODCACHE")
	again := fmt.Sprintf("run \"faultline-toolexec prepare\" for %s %s/%s\n", tc.version, tc.goos, tc.goarch)
	const absent = "\x00"
	gone := "packagefile example.com/gone=" + filepath.Join(t.TempDir(), "gone.a") // as after go clean -cache
	for _, c := range []struct{ name, cfg, env, file, why string }{
		{"no .env file", cfg, absent, "", ""},
		{"importcfg cut in a line", cfg[:len(cfg)-10], env, ".importcfg", "the file does not end with a newline"},
		{"importcfg without its last line", cfg[:lastLine], env, ".importcfg", "no export data for " + schedPath},
		{"importcfg not from prepare", "hello\n", env, ".importcfg", `line "hello" is not a packagefile line`},
		{"importcfg with a relative file", "packagefile " + schedPath + "=s.a\n", env, ".importcfg",
			`line "packagefile ` + schedPath + `=s.a" is not a packagefile line`},
		{"importcfg with an importmap line", "importmap x=/y\n" + cfg, env, ".importcfg", `line "importmap x=/y" is not a packagefile line`},
		{"importcfg naming a missing export file", gone + "\n" + cfg, env, ".importcfg",
			fmt.Sprintf("line %q: the export file is missing (the go build cache was cleaned or trimmed)", gone)},
		{".env without GOMODCACHE", cfg, "GOROOT=" + roots[0] + "\n", ".env", "want a GOROOT line and a GOMODCACHE line"},
		{".env with a third line", cfg, "GOROOT=" + roots[0] + "\nGOMODCACHE=" + roots[1] + "\nx", ".env", "want a GOROOT line and a GOMODCACHE line"},
		{".env with an empty GOROOT", cfg, "GOROOT=\nGOMODCACHE=" + roots[1] + "\n", ".env", `line "GOROOT=": want GOROOT=<absolute path>`},
	} {
		tmp := t.TempDir()
		base := filepath.Join(tmp, tc.key())
		for _, f := range []struct{ ext, content string }{{".importcfg", c.cfg}, {".env", c.env}} {
			if f.content == absent {
				continue
			}
			if err := os.WriteFile(base+f.ext, []byte(f.content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		want := "faultline-toolexec: " + again
		if c.file != "" {
			want = "faultline-toolexec: " + base + c.file + ": " + c.why + "; " + again
		}
		out, code := wrapperRun(t, []string{"FAULTLINE_TOOLEXEC_STATE=" + tmp, "GOOS=" + tc.goos, "GOARCH=" + tc.goarch},
			filepath.Join(tc.tooldir, "compile"), "-p", "example.com/u", "-goversion", tc.version, "u.go")
		if code != 2 || out != want {
			t.Errorf("%s: exit %d, %q; want exit 2, %q", c.name, code, out, want)
		}
	}
}

// userModule writes module example.com/u with the files (relative name, content) into a
// temporary directory and returns it.
func userModule(t *testing.T, files ...string) string {
	t.Helper()
	dir := t.TempDir()
	files = append([]string{"go.mod", "module example.com/u\n\ngo 1.26.0\n"}, files...)
	for i := 0; i < len(files); i += 2 {
		p := filepath.Join(dir, files[i])
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(files[i+1]), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// compileArgs returns the go command's compile arguments for package pkg, built from the files.
func compileArgs(tc toolchain, pkg, cfg, out string, files ...string) []string {
	return append([]string{filepath.Join(tc.tooldir, "compile"), "-o", out, "-p", pkg, "-lang=go1.26",
		"-goversion", tc.version, "-importcfg", cfg, "-pack"}, files...)
}

// TestTypeCheckFailure: a package of the rewrite set that does not type-check fails the compile
// with its import path and exit 2.
func TestTypeCheckFailure(t *testing.T) {
	tc := currentToolchain(t)
	dir := userModule(t, "u.go", "package u\n\nvar X int = \"s\"\n", "importcfg", "# import config\n")
	env := []string{"FAULTLINE_TOOLEXEC_STATE=" + stateCopy(t, tc), "TOOLEXEC_IMPORTPATH=example.com/u"}
	out, code := wrapperRun(t, env, compileArgs(tc, "example.com/u", filepath.Join(dir, "importcfg"),
		filepath.Join(dir, "u.a"), filepath.Join(dir, "u.go"))...)
	if code != 2 || !strings.HasPrefix(out, "faultline-toolexec: example.com/u: "+filepath.Join(dir, "u.go")+":3:") {
		t.Fatalf("exit %d, %q; want exit 2 and the type error", code, out)
	}
}

// TestCompileAndLink builds a program of a user module through the wrapper, tool by tool as the
// go command does: the package with a channel operation is rewritten and compiled against
// sched's prepared export data, a package with nothing to rewrite compiles unchanged, the link
// finds sched through the prepared lines, and the program runs. The log, the kept file and
// gaps.txt record it, and no temporary directory is left behind.
func TestCompileAndLink(t *testing.T) {
	tc := currentToolchain(t)
	state, tmp := stateCopy(t, tc), t.TempDir()
	prepared := readPrepared(t, filepath.Join(state, tc.key()+".importcfg"))
	dir := userModule(t,
		"u.go", "package u\n\nimport \"sync\"\n\nfunc F(ch chan int, m *sync.Mutex) func() {\n\tch <- 1\n\treturn m.Lock\n}\n",
		"v/v.go", "package v\n\nfunc G() int { return 1 }\n",
		"cmd/m/main.go", "package main\n\nfunc main() {\n\tch := make(chan int, 1)\n\tch <- 1\n\t<-ch\n}\n",
		"importcfg.u", "packagefile sync="+prepared["sync"]+"\n",
		"importcfg.none", "# import config\n")
	keep, log := t.TempDir(), filepath.Join(t.TempDir(), "wrapper.log")
	env := []string{"FAULTLINE_TOOLEXEC_STATE=" + state, "TMPDIR=" + tmp, "FAULTLINE_TOOLEXEC_KEEP=" + keep, "FAULTLINE_TOOLEXEC_LOG=" + log}
	in := func(rel string) string { return filepath.Join(dir, rel) }
	for _, c := range []struct{ ip, pkg, cfg, out, file string }{
		{"example.com/u", "example.com/u", "importcfg.u", "u.a", "u.go"},
		{"example.com/u/v", "example.com/u/v", "importcfg.none", "v.a", "v/v.go"},
		{"example.com/u/cmd/m", "main", "importcfg.none", "m.a", "cmd/m/main.go"},
	} {
		args := compileArgs(tc, c.pkg, in(c.cfg), in(c.out), in(c.file))
		if out, code := wrapperRun(t, append(env, "TOOLEXEC_IMPORTPATH="+c.ip), args...); code != 0 {
			t.Fatalf("compile %s: exit %d\n%s", c.ip, code, out)
		}
	}
	if err := os.WriteFile(in("importcfg.link"), []byte("packagefile example.com/u/cmd/m="+in("m.a")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := []string{filepath.Join(tc.tooldir, "link"), "-o", in("m"), "-importcfg", in("importcfg.link"), in("m.a")}
	if out, code := wrapperRun(t, append(env, "TOOLEXEC_IMPORTPATH=example.com/u/cmd/m"), link...); code != 0 {
		t.Fatalf("link: exit %d\n%s", code, out)
	}
	if out, err := exec.Command(in("m")).CombinedOutput(); err != nil { //nolint:gosec // the program the test linked
		t.Fatalf("the linked program: %v\n%s", err, out)
	}
	if !linksSched(t, in("m")) {
		t.Errorf("the linked program has no symbol of %s: compile got the original main.go", schedPath)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if want := "compile example.com/u rewritten\ncompile example.com/u/v unchanged\n" +
		"compile example.com/u/cmd/m rewritten\nlink example.com/u/cmd/m\n"; string(b) != want {
		t.Errorf("log %q, want %q", b, want)
	}
	kept, err := os.ReadFile(filepath.Join(keep, "example.com_u", "u.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(kept), "//line "+in("u.go")+":1\n") || !strings.Contains(string(kept), "__fl_sched.Point(); ch <- 1") {
		t.Errorf("kept u.go:\n%s", kept)
	}
	gaps, err := os.ReadFile(filepath.Join(state, "gaps.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if want := in("u.go") + ":7: method value Mutex.Lock\n"; string(gaps) != want {
		t.Errorf("gaps.txt %q, want %q", gaps, want)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("left behind in TMPDIR: %s", e.Name())
	}
}

// TestStandInTools runs shell stand-ins for compile and link through the wrapper, with the
// -importcfg=<file> form of the flag. The type check resolves sync through an importmap line,
// compile gets the rewritten file, both get the merged importcfg, and the wrapper exits with the
// tool's exit code.
func TestStandInTools(t *testing.T) {
	tc := currentToolchain(t)
	state := stateCopy(t, tc)
	prepared := readPrepared(t, filepath.Join(state, tc.key()+".importcfg"))
	// A stand-in prints its -V=full line, or else copies its importcfg and its .go file next to
	// itself and exits 3.
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = -V=full ]; then echo \"$(basename \"$0\") version " + tc.version + "\"; exit 0; fi\n" +
		"for a; do case $a in -importcfg=*) cp \"${a#-importcfg=}\" \"$0.importcfg\" ;; *.go) cp \"$a\" \"$0.go\" ;; esac; done\n" +
		"exit 3\n"
	tools := t.TempDir()
	for _, name := range []string{"compile", "link"} {
		if err := os.WriteFile(filepath.Join(tools, name), []byte(script), 0o700); err != nil { //nolint:gosec // a stand-in tool must be executable
			t.Fatal(err)
		}
	}
	dir := userModule(t,
		"u.go", "package u\n\nimport \"sync\"\n\nfunc F(ch chan int, m *sync.Mutex) {\n\tch <- 1\n\tm.Lock()\n}\n",
		"importcfg", "importmap sync=vendor/example.com/sync\npackagefile vendor/example.com/sync="+prepared["sync"]+"\n",
		"importcfg.link", "packagefile example.com/u=u.a\n")
	in := func(rel string) string { return filepath.Join(dir, rel) }
	for _, c := range []struct {
		tool string
		args []string
	}{
		{"compile", []string{"-o", in("u.a"), "-p", "example.com/u", "-lang=go1.26", "-goversion", tc.version,
			"-importcfg=" + in("importcfg"), "-pack", in("u.go")}},
		{"link", []string{"-o", in("m"), "-importcfg=" + in("importcfg.link"), in("u.a")}},
	} {
		tool := filepath.Join(tools, c.tool)
		out, code := wrapperRun(t, []string{"FAULTLINE_TOOLEXEC_STATE=" + state, "TOOLEXEC_IMPORTPATH=example.com/u"}, append([]string{tool}, c.args...)...)
		if code != 3 || out != "" {
			t.Errorf("%s: exit %d, %q; want exit 3 and no output", c.tool, code, out)
		}
		cfg, err := os.ReadFile(tool + ".importcfg")
		if err != nil {
			t.Fatalf("%s: %v", c.tool, err)
		}
		if !strings.Contains(string(cfg), "\npackagefile "+schedPath+"="+prepared[schedPath]+"\n") {
			t.Errorf("%s got importcfg %q, want the prepared lines added", c.tool, cfg)
		}
	}
	src, err := os.ReadFile(filepath.Join(tools, "compile.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "__fl_sched.Point(); ch <- 1") {
		t.Errorf("compile got u.go:\n%s", src)
	}
}
