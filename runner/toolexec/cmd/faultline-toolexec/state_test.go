// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// AT-XTE-01
func TestATXTE01_Prepare(t *testing.T) {
	tc := currentToolchain(t)
	state := t.TempDir()
	prep := exec.Command(wrapper, "prepare")
	prep.Dir = "../.." // the module root: go list resolves sched there
	// prepare drops -race and -toolexec from GOFLAGS (XTE-012 step 2): with -toolexec kept, go list
	// would compile sched through the wrapper, which has no state yet.
	goflags := strings.TrimSpace(os.Getenv("GOFLAGS") + " -race -toolexec=" + wrapper)
	prep.Env = append(os.Environ(), "GOWORK=off", "FAULTLINE_TOOLEXEC_STATE="+state, "GOFLAGS="+goflags)
	if out, err := prep.CombinedOutput(); err != nil {
		t.Fatalf("prepare: %v\n%s", err, out)
	}
	entries, err := os.ReadDir(state)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := []string{tc.key() + "-race.importcfg", tc.key() + ".env", tc.key() + ".importcfg"}; !slices.Equal(names, want) {
		t.Fatalf("state directory holds %q, want %q", names, want)
	}
	plain := readPrepared(t, filepath.Join(state, tc.key()+".importcfg"))
	race := readPrepared(t, filepath.Join(state, tc.key()+"-race.importcfg"))
	for _, files := range []map[string]string{plain, race} {
		for _, p := range []string{schedPath, goschedPath, "reflect", "runtime", "sync"} {
			if files[p] == "" {
				t.Errorf("a prepared importcfg lacks %s", p)
			}
		}
		for p, f := range files { //faultline:maporder every entry is checked
			if _, err := os.Stat(f); err != nil {
				t.Errorf("%s: %v", p, err)
			}
		}
	}
	if plain[schedPath] == race[schedPath] {
		t.Error("the -race variant names the plain variant's sched export file")
	}
	roots := goEnv(t, "GOROOT", "GOMODCACHE")
	b, err := os.ReadFile(filepath.Join(state, tc.key()+".env"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "GOROOT=" + roots[0] + "\nGOMODCACHE=" + roots[1] + "\n"; string(b) != want {
		t.Errorf(".env file %q, want %q", b, want)
	}
	// In a temporary directory: a prepare that accepted the relative path would write there.
	out, code := prepareIn(t, t.TempDir(), "FAULTLINE_TOOLEXEC_STATE=rel/state")
	if want := "faultline-toolexec: FAULTLINE_TOOLEXEC_STATE=\"rel/state\": want an absolute path\n"; code != 2 || out != want {
		t.Errorf("relative state: exit %d, %q; want exit 2, %q", code, out, want)
	}
}

// prepareIn runs prepare in dir with GOWORK=off and env added to the test's environment, and
// returns its combined output and exit code.
func prepareIn(t *testing.T, dir string, env ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(wrapper, "prepare")
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "GOWORK=off"), env...)
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		t.Fatalf("running prepare: %v", err)
	}
	return string(out), cmd.ProcessState.ExitCode()
}

// §7's prepare rows: a failing go env or plain go list stops prepare with go's error.
func TestPrepareFailures(t *testing.T) {
	state := "FAULTLINE_TOOLEXEC_STATE=" + t.TempDir()
	want := "faultline-toolexec: prepare: go env: exit status 1: go: invalid GOTOOLCHAIN \"bogus\"\n"
	if out, code := prepareIn(t, t.TempDir(), state, "GOTOOLCHAIN=bogus"); code != 2 || out != want {
		t.Errorf("go env fails: exit %d, %q; want exit 2, %q", code, out, want)
	}
	// Outside a module go list cannot resolve sched.
	want = "faultline-toolexec: prepare: go list " + schedPath + ": exit status 1: "
	if out, code := prepareIn(t, t.TempDir(), state); code != 2 || !strings.HasPrefix(out, want) {
		t.Errorf("go list fails: exit %d, %q; want exit 2 and the prefix %q", code, out, want)
	}
}

// XTE-012 step 2: prepare's go list runs under the caller's GOFLAGS without -race and -toolexec.
func TestPrepareFlags(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", ""},
		{"-race", ""},
		{"-race -p=2 --toolexec=x -mod=mod -race=false", "-p=2 -mod=mod"},
		{"-toolexec=/w -tags=a,b", "-tags=a,b"},
	} {
		if got := prepareFlags(c.in); got != c.want {
			t.Errorf("prepareFlags(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// §7's usage row: no arguments, or prepare with more.
func TestUsage(t *testing.T) {
	want := "faultline-toolexec: usage: faultline-toolexec prepare | faultline-toolexec <tool> [args...]\n"
	env := []string{"FAULTLINE_TOOLEXEC_STATE=" + t.TempDir()}
	for _, args := range [][]string{nil, {"prepare", "x"}} {
		if out, code := wrapperRun(t, env, args...); code != 2 || out != want {
			t.Errorf("%q: exit %d, %q; want exit 2, %q", args, code, out, want)
		}
	}
}

// XTE-011: without FAULTLINE_TOOLEXEC_STATE the state is faultline-toolexec in the user's cache
// directory, and with no cache directory the wrapper fails with §7's message. -V=full only reads
// the state, so no run writes to a cache directory.
func TestDefaultStateDir(t *testing.T) {
	tc := currentToolchain(t)
	compiler := filepath.Join(tc.tooldir, "compile")
	state := stateCopy(t, tc)
	want, code := wrapperRun(t, []string{"FAULTLINE_TOOLEXEC_STATE=" + state}, compiler, "-V=full")
	if code != 0 {
		t.Fatalf("-V=full: exit %d, %q", code, want)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cache, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(state, filepath.Join(cache, "faultline-toolexec")); err != nil {
		t.Fatal(err)
	}
	if out, code := wrapperRun(t, []string{"FAULTLINE_TOOLEXEC_STATE="}, compiler, "-V=full"); code != 0 || out != want {
		t.Errorf("state in the cache directory: exit %d, %q; want exit 0, %q", code, out, want)
	}
	out, code := wrapperRun(t, []string{"FAULTLINE_TOOLEXEC_STATE=", "HOME=", "XDG_CACHE_HOME="}, compiler, "-V=full")
	if code != 2 || !strings.HasPrefix(out, "faultline-toolexec: no state directory: ") || !strings.HasSuffix(out, "; set FAULTLINE_TOOLEXEC_STATE\n") {
		t.Errorf("no cache directory: exit %d, %q; want exit 2 and the no-state-directory message", code, out)
	}
}

// AT-XTE-02, with TestMissingPrepare and TestInvalidPrepareState: a build stops at its first
// -V=full without state, and a -race link needs the -race state.
func TestATXTE02_MissingStateStopsTheBuild(t *testing.T) {
	tc := currentToolchain(t)
	again := fmt.Sprintf(`faultline-toolexec: run "faultline-toolexec prepare" for %s %s/%s`, tc.version, tc.goos, tc.goarch)
	empty := []string{"FAULTLINE_TOOLEXEC_STATE=" + t.TempDir(), "GOOS=" + tc.goos, "GOARCH=" + tc.goarch}
	if out, code := wrapperRun(t, empty, filepath.Join(tc.tooldir, "compile"), "-V=full"); code != 2 || out != again+"\n" {
		t.Errorf("-V=full without state: exit %d, %q; want exit 2, %q", code, out, again+"\n")
	}
	state := stateCopy(t, tc)
	if err := os.Remove(filepath.Join(state, tc.key()+"-race.importcfg")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	env := []string{"FAULTLINE_TOOLEXEC_STATE=" + state, "GOOS=" + tc.goos, "GOARCH=" + tc.goarch}
	for _, args := range [][]string{
		{filepath.Join(tc.tooldir, "link"), "-race", "-importcfg", "importcfg.link", "-o", "x", "x.a"},
		{filepath.Join(tc.tooldir, "compile"), "-race", "-p", "example.com/u", "-goversion", tc.version, "u.go"},
	} {
		if out, code := wrapperRun(t, env, args...); code != 2 || out != again+" with -race support\n" {
			t.Errorf("-race %s without the -race state: exit %d, %q; want exit 2, %q", filepath.Base(args[0]), code, out, again+" with -race support\n")
		}
	}
}

// AT-XTE-03
func TestATXTE03_VersionLine(t *testing.T) {
	tc := currentToolchain(t)
	state := stateCopy(t, tc)
	compiler := filepath.Join(tc.tooldir, "compile")
	line, err := exec.Command(compiler, "-V=full").Output()
	if err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(state, tc.key()+".importcfg")
	hash := func() string {
		plain, err := os.ReadFile(cfg)
		if err != nil {
			t.Fatal(err)
		}
		race, _ := os.ReadFile(filepath.Join(state, tc.key()+"-race.importcfg"))
		sum := sha256.Sum256(append(plain, race...))
		return hex.EncodeToString(sum[:])[:12]
	}
	base := strings.TrimSpace(string(line)) + " faultline-toolexec=" + version + ",sched="
	env := func(kv ...string) []string {
		return append([]string{"FAULTLINE_TOOLEXEC_STATE=" + state, "FAULTLINE_TOOLEXEC_STD=", "FAULTLINE_TOOLEXEC_DEPS="}, kv...)
	}
	h := hash()
	for _, c := range []struct {
		env  []string
		want string
	}{
		{env(), base + h + "\n"},
		{env("FAULTLINE_TOOLEXEC_STD=net/http"), base + h + ",std=net/http\n"},
		{env("FAULTLINE_TOOLEXEC_DEPS=example.com/d"), base + h + ",deps=example.com/d\n"},
	} {
		if out, code := wrapperRun(t, c.env, compiler, "-V=full"); code != 0 || out != c.want {
			t.Errorf("%v: exit %d, %q; want exit 0, %q", c.env, code, out, c.want)
		}
	}
	exported := readPrepared(t, cfg)[schedPath] // a well-formed line names an existing file
	f, err := os.OpenFile(cfg, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("packagefile example.com/x=" + exported + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
	if h2 := hash(); h2 == h {
		t.Fatal("the hash did not change with the file")
	} else if out, _ := wrapperRun(t, env(), compiler, "-V=full"); out != base+h2+"\n" {
		t.Errorf("after the change: %q, want %q", out, base+h2+"\n")
	}
	never := "the runtime, syscall, unsafe, sync, reflect, testing, internal and vendor packages are never rewritten"
	for _, c := range []struct{ name, val, want string }{
		{"FAULTLINE_TOOLEXEC_STD", "sync", "FAULTLINE_TOOLEXEC_STD: sync cannot be rewritten: " + never},
		{"FAULTLINE_TOOLEXEC_STD", "example.com/x", "FAULTLINE_TOOLEXEC_STD: example.com/x cannot be rewritten: not a standard-library package"},
		{"FAULTLINE_TOOLEXEC_STD", "net/http, internal/poll", "FAULTLINE_TOOLEXEC_STD: internal/poll cannot be rewritten: " + never},
		{"FAULTLINE_TOOLEXEC_STD", "bytes", "FAULTLINE_TOOLEXEC_STD: bytes cannot be rewritten: sched depends on it"},
		{"FAULTLINE_TOOLEXEC_STD", "unsafe", "FAULTLINE_TOOLEXEC_STD: unsafe cannot be rewritten: " + never},
		{"FAULTLINE_TOOLEXEC_STD", "syscall", "FAULTLINE_TOOLEXEC_STD: syscall cannot be rewritten: " + never},
		{"FAULTLINE_TOOLEXEC_STD", "reflect", "FAULTLINE_TOOLEXEC_STD: reflect cannot be rewritten: " + never},
		{"FAULTLINE_TOOLEXEC_STD", "runtime/pprof", "FAULTLINE_TOOLEXEC_STD: runtime/pprof cannot be rewritten: " + never},
		{"FAULTLINE_TOOLEXEC_STD", "testing", "FAULTLINE_TOOLEXEC_STD: testing cannot be rewritten: " + never},
		{"FAULTLINE_TOOLEXEC_STD", "testing/quick", "FAULTLINE_TOOLEXEC_STD: testing/quick cannot be rewritten: " + never},
		{"FAULTLINE_TOOLEXEC_STD", "net/http/internal/ascii", "FAULTLINE_TOOLEXEC_STD: net/http/internal/ascii cannot be rewritten: " + never},
		{"FAULTLINE_TOOLEXEC_STD", "vendor/golang.org/x/net/http2/hpack", "FAULTLINE_TOOLEXEC_STD: vendor/golang.org/x/net/http2/hpack cannot be rewritten: " + never},
		{"FAULTLINE_TOOLEXEC_DEPS", "github.com/hmdsefi/faultline/x", "FAULTLINE_TOOLEXEC_DEPS: github.com/hmdsefi/faultline/x is not a module path outside github.com/hmdsefi/faultline"},
		{"FAULTLINE_TOOLEXEC_DEPS", "github.com/hmdsefi/faultline", "FAULTLINE_TOOLEXEC_DEPS: github.com/hmdsefi/faultline is not a module path outside github.com/hmdsefi/faultline"},
		{"FAULTLINE_TOOLEXEC_DEPS", "example.com/d, github.com/hmdsefi/faultline/x", "FAULTLINE_TOOLEXEC_DEPS: github.com/hmdsefi/faultline/x is not a module path outside github.com/hmdsefi/faultline"},
		{"FAULTLINE_TOOLEXEC_DEPS", "nodot/x", "FAULTLINE_TOOLEXEC_DEPS: nodot/x is not a module path outside github.com/hmdsefi/faultline"},
	} {
		out, code := wrapperRun(t, env(c.name+"="+c.val), compiler, "-V=full")
		if want := "faultline-toolexec: " + c.want + "\n"; code != 2 || out != want {
			t.Errorf("%s=%q: exit %d, %q; want exit 2, %q", c.name, c.val, code, out, want)
		}
	}
}
