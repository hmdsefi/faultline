// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

var (
	runner   string // the faultline-wasm binary under test
	guest    string // testdata/guest compiled for wasip1
	exitMain string // testdata/exitmain compiled for wasip1
	cache    string // FAULTLINE_WASM_CACHE for every run
)

func TestMain(m *testing.M) {
	code, err := setup(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(code)
}

func setup(m *testing.M) (int, error) {
	dir, err := os.MkdirTemp("", "faultline-wasm-test-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	runner, cache = filepath.Join(dir, "faultline-wasm"), filepath.Join(dir, "cache")
	if out, err := exec.Command("go", "build", "-o", runner, ".").CombinedOutput(); err != nil {
		return 0, fmt.Errorf("building faultline-wasm: %v\n%s", err, out)
	}
	guest, exitMain = filepath.Join(dir, "guest.wasm"), filepath.Join(dir, "exitmain.wasm")
	for _, b := range [][2]string{{"guest", guest}, {"exitmain", exitMain}} {
		mod, err := fixtureModule(dir, b[0], false)
		if err != nil {
			return 0, err
		}
		cmd := exec.Command("go", "test", "-c", "-o", b[1], ".") //nolint:gosec // G204: the output is a file in the test's temporary directory
		cmd.Dir = mod
		cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "GOWORK=off")
		if out, err := cmd.CombinedOutput(); err != nil {
			return 0, fmt.Errorf("building %s for wasip1: %v\n%s", b[0], err, out)
		}
	}
	// Warm the compilation cache so that wall-time assertions exclude compilation (F10).
	warm := exec.Command(runner, guest, "-test.run=^$")
	warm.Env = cleanEnv("FAULTLINE_SEED=1")
	if out, err := warm.CombinedOutput(); err != nil {
		return 0, fmt.Errorf("warming the compilation cache: %v\n%s", err, out)
	}
	return m.Run(), nil
}

// fixtureModule copies testdata/<name> to dir/<name> and makes it module example.com/<name>
// there. No go.mod is committed under testdata (DET §11): CI's nested job would run the
// fixtures' deliberately failing tests. With faultline set, the module requires the checkout's
// root module through an absolute replace, starts from a copy of the root go.sum, and is tidied
// (go test needs the root module's requirements listed). Run go on the result with GOWORK=off.
func fixtureModule(dir, name string, faultline bool) (string, error) {
	dst := filepath.Join(dir, name)
	if err := os.CopyFS(dst, os.DirFS(filepath.Join("../../testdata", name))); err != nil {
		return "", err
	}
	mod := "module example.com/" + name + "\n\ngo 1.26.0\n"
	if faultline {
		root, err := filepath.Abs("../../../..")
		if err != nil {
			return "", err
		}
		sum, err := os.ReadFile(filepath.Join(root, "go.sum"))
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dst, "go.sum"), sum, 0o600); err != nil { //nolint:gosec // G703: dst is under the test's temporary directory
			return "", err
		}
		mod += "\nrequire github.com/hmdsefi/faultline v0.0.0-00010101000000-000000000000\n\n" +
			"replace github.com/hmdsefi/faultline => " + strconv.Quote(root) + "\n"
	}
	if err := os.WriteFile(filepath.Join(dst, "go.mod"), []byte(mod), 0o600); err != nil {
		return "", err
	}
	if faultline {
		tidy := exec.Command("go", "mod", "tidy")
		tidy.Dir = dst
		tidy.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
		if out, err := tidy.CombinedOutput(); err != nil {
			return "", fmt.Errorf("go mod tidy in %s: %v\n%s", dst, err, out)
		}
	}
	return dst, nil
}

// cleanEnv is the test's environment without FAULTLINE_* variables, plus the shared cache and env.
func cleanEnv(env ...string) []string {
	var out []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "FAULTLINE_") {
			out = append(out, kv)
		}
	}
	return append(append(out, "FAULTLINE_WASM_CACHE="+cache), env...)
}

// runWasm runs faultline-wasm with args and env added to cleanEnv.
func runWasm(t *testing.T, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(runner, args...)
	cmd.Env = cleanEnv(env...)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return o.String(), e.String(), ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return o.String(), e.String(), 0
}

var resultField = regexp.MustCompile(`(map|select)=(\S+)`)

// fields returns the map= and select= values of the RESULT line of TestDeterminism.
func fields(out string) map[string]string {
	m := map[string]string{}
	for _, f := range resultField.FindAllStringSubmatch(out, -1) {
		m[f[1]] = f[2]
	}
	return m
}

// AT-EXB-01
func TestATEXB01_SameSeedSameOutput(t *testing.T) {
	run := func(seed string) string {
		out, errOut, code := runWasm(t, []string{"FAULTLINE_SEED=" + seed}, guest, "-test.run", "TestDeterminism")
		if code != 0 || !strings.Contains(out, "RESULT ") {
			t.Fatalf("seed %s: exit %d\n%s%s", seed, code, out, errOut)
		}
		return out
	}
	a, b, c := run("1"), run("1"), run("2")
	if a != b {
		t.Fatalf("seed 1 outputs differ:\n%s\n%s", a, b)
	}
	fa, fc := fields(a), fields(c)
	if fa["map"] == fc["map"] && fa["select"] == fc["select"] {
		t.Fatalf("seed 2 has seed 1's map order and select mask: %v", fa)
	}
}

// AT-EXB-02
func TestATEXB02_ExitCodes(t *testing.T) {
	if out, errOut, code := runWasm(t, []string{"FAULTLINE_SEED=1"}, guest, "-test.run=^TestFail$"); code != 1 {
		t.Fatalf("t.Fatal guest: exit %d, want 1\n%s%s", code, out, errOut)
	}
	if out, errOut, code := runWasm(t, []string{"FAULTLINE_SEED=1"}, exitMain); code != 3 {
		t.Fatalf("os.Exit(3) guest: exit %d, want 3\n%s%s", code, out, errOut)
	}
}

// AT-EXB-03
func TestATEXB03_NotWasm(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.wasm")
	if err := os.WriteFile(bad, []byte("not a wasm module"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, errOut, code := runWasm(t, []string{"FAULTLINE_SEED=1"}, bad)
	if code != 2 || !strings.HasPrefix(errOut, "faultline-wasm: ") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
}

func TestRunnerErrors(t *testing.T) {
	if _, errOut, code := runWasm(t, nil); code != 2 || errOut != "faultline-wasm: usage: faultline-wasm <module> [args...]\n" {
		t.Fatalf("no module: exit %d, stderr %q", code, errOut)
	}
	if _, errOut, code := runWasm(t, []string{"FAULTLINE_SEED=1"}, filepath.Join(t.TempDir(), "missing.wasm")); code != 2 || !strings.HasPrefix(errOut, "faultline-wasm: ") {
		t.Fatalf("missing module: exit %d, stderr %q", code, errOut)
	}
	_, errOut, code := runWasm(t, []string{"FAULTLINE_SEED=1", "FAULTLINE_WASM_CLOCK_QUANTUM=-5"}, guest)
	if code != 2 || !strings.Contains(errOut, `faultline-wasm: invalid FAULTLINE_WASM_CLOCK_QUANTUM "-5"`) {
		t.Fatalf("bad quantum: exit %d, stderr %q", code, errOut)
	}
}

func TestStatsLine(t *testing.T) {
	_, errOut, code := runWasm(t, []string{"FAULTLINE_SEED=0x2a", "FAULTLINE_WASM_STATS=1"}, guest, "-test.run=^TestSleepHour$")
	re := regexp.MustCompile(`(?m)^faultline-wasm: seed=0x000000000000002a compile=\S+ run=\S+ virtual=1h0m\S* clock_reads=\d+ exit=0$`)
	if code != 0 || !re.MatchString(errOut) {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
}

// AT-EXB-06
func TestATEXB06_VirtualSleep(t *testing.T) {
	start := time.Now()
	out, errOut, code := runWasm(t, []string{"FAULTLINE_SEED=1"}, guest, "-test.run=^TestSleepHour$")
	if wall := time.Since(start); code != 0 || !strings.Contains(out, "1h0m0s\n") || wall >= time.Second {
		t.Fatalf("exit %d after %v\n%s%s", code, wall, out, errOut)
	}
}

// AT-EXB-07
func TestATEXB07_CryptoRandFollowsSeed(t *testing.T) {
	read := func(seed string) string {
		out, errOut, code := runWasm(t, []string{"FAULTLINE_SEED=" + seed}, guest, "-test.run=^TestCryptoRand$")
		if code != 0 {
			t.Fatalf("seed %s: exit %d\n%s%s", seed, code, out, errOut)
		}
		return strings.SplitN(out, "\n", 2)[0]
	}
	a, b, c := read("5"), read("5"), read("6")
	if a != b || a == c || len(a) != 32 {
		t.Fatalf("seed 5: %q, %q; seed 6: %q", a, b, c)
	}
}
