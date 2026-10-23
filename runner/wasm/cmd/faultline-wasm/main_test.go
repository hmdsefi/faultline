// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
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

// goTestExec runs GOOS=wasip1 GOARCH=wasm go test -exec <runner> args... in a fixtureModule
// copy of testdata/exact, without FAULTLINE_* variables except an artifact root and env. A hung
// guest does not outlive the test: with the -timeout below, go test kills the runner before the
// test's deadline (one minute after that timeout), and the context kills go test itself.
func goTestExec(t *testing.T, env []string, args ...string) (string, int) {
	t.Helper()
	mod, err := fixtureModule(t.TempDir(), "exact", true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, flags := t.Context(), []string{"test", "-exec", runner}
	if d, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, d.Add(-5*time.Second))
		defer cancel()
		flags = append(flags, fmt.Sprintf("-timeout=%v", max(time.Until(d)-90*time.Second, 30*time.Second).Round(time.Second)))
	}
	cmd := exec.CommandContext(ctx, "go", append(flags, args...)...) //nolint:gosec // G204: runner is the binary setup built, args are the tests' literals
	cmd.WaitDelay = 5 * time.Second
	cmd.Dir = mod
	cmd.Env = append(cleanEnv("GOOS=wasip1", "GOARCH=wasm", "GOWORK=off", "FAULTLINE_ARTIFACTS="+t.TempDir()), env...)
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), 0
}

// testASeeds are TestA's default seeds (API-015); computed with DeriveSeed and pinned:
// 0xbc886b4ba098a264, 0xe2c3c685e03b7dda, 0x6bf2f4f90164d0cf.
func testASeeds(t *testing.T) []uint64 {
	var s []uint64
	for i := range 3 {
		s = append(s, faultline.DeriveSeed(faultline.NameBase("TestA"), i))
	}
	if s[0] != 0xbc886b4ba098a264 || s[1] != 0xe2c3c685e03b7dda || s[2] != 0x6bf2f4f90164d0cf {
		t.Fatalf("TestA seeds %#x differ from the pinned values", s)
	}
	return s
}

// AT-EXB-04
func TestATEXB04_PlanRunThroughGoTest(t *testing.T) {
	out, code := goTestExec(t, nil, "-count=1", "-v", "./pass")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	b := 0
	for _, l := range strings.Split(out, "\n") {
		if l == "B" {
			b++
		}
	}
	if b != 1 {
		t.Fatalf("B printed %d times, want once\n%s", b, out)
	}
	for _, s := range testASeeds(t) {
		if n := strings.Count(out, fmt.Sprintf("--- PASS: TestA/seed=0x%016x", s)); n != 1 {
			t.Fatalf("seed 0x%016x passed %d times, want once\n%s", s, n, out)
		}
	}
	if strings.Contains(out, "faultline-exact-plan") {
		t.Fatalf("plan lines leaked into the output\n%s", out)
	}
}

// AT-EXB-05
func TestATEXB05_FailingSeedReported(t *testing.T) {
	out, code := goTestExec(t, nil, "-count=1", "-v", "./fail")
	s := testASeeds(t)
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s", code, out)
	}
	for _, want := range []string{
		fmt.Sprintf("--- FAIL: TestA/seed=0x%016x", s[1]),
		`faultline: invariant "seed two fails" violated`,
		fmt.Sprintf("replay:    FAULTLINE_SEED=0x%016x go test -v -run '^TestA$'", s[1]),
		"determinism: exact (wasip1 backend)",
		fmt.Sprintf("--- PASS: TestA/seed=0x%016x", s[0]),
		fmt.Sprintf("--- PASS: TestA/seed=0x%016x", s[2]),
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q\n%s", want, out)
		}
	}
}

func TestBadPlanLine(t *testing.T) {
	_, errOut, code := runWasm(t, []string{"BAD_PLAN=1"}, guest, "-test.run=^TestBadPlan$")
	if code != 2 || !strings.Contains(errOut, `faultline-wasm: bad plan line "faultline-exact-plan\tTestBadPlan\tnot-a-seed"`) {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
}

// A plan line that follows output without a newline still runs its seed, here the failing one,
// and the plan instance's final partial line is forwarded (EXB-014 removes plan lines wherever
// they start). Without -v, Run logs nothing before its plan lines.
func TestPlanLineAfterPartialOutput(t *testing.T) {
	out, code := goTestExec(t, nil, "-count=1", "./partial")
	if s := testASeeds(t)[0]; code != 1 || !strings.Contains(out, fmt.Sprintf("--- FAIL: TestA/seed=0x%016x", s)) {
		t.Fatalf("exit %d, want 1 with seed 0x%016x failing\n%s", code, s, out)
	}
	if n := strings.Count(out, "partial-end"); n != 4 || strings.Contains(out, "faultline-exact-plan") {
		t.Fatalf("partial-end printed %d times, want 4 (every instance), and no plan text\n%s", n, out)
	}
}

// With -count=2 and FAULTLINE_SEED_LIST, each seed runs once per repetition, each in its own run
// instance whose seed is its plan line's, in plan order after the plan instance (EXB-014).
func TestPlanRunCountAndSeedList(t *testing.T) {
	out, code := goTestExec(t, []string{"FAULTLINE_SEED_LIST=0x1,0x2", "FAULTLINE_WASM_STATS=1"}, "-count=2", "-v", "./partial")
	var seeds []string
	for _, m := range regexp.MustCompile(`faultline-wasm: seed=(0x[0-9a-f]{16}) `).FindAllStringSubmatch(out, -1) {
		seeds = append(seeds, m[1])
	}
	want := "0x0000000000000000 0x0000000000000001 0x0000000000000002 0x0000000000000001 0x0000000000000002"
	if code != 0 || strings.Join(seeds, " ") != want {
		t.Fatalf("exit %d, instance seeds %v, want exit 0 and %s\n%s", code, seeds, want, out)
	}
	for _, s := range []string{"0x0000000000000001", "0x0000000000000002"} {
		if n := strings.Count(out, "--- PASS: TestA/seed="+s); n != 2 {
			t.Fatalf("seed %s passed %d times, want twice\n%s", s, n, out)
		}
	}
}

// Both kinds of instance write to stdout, a run instance prints what single mode prints for its
// seed, and the plan instance's exit code counts (EXB-014, EXB-016).
func TestPlanRunStdoutAndSeed(t *testing.T) {
	single, _, _ := runWasm(t, []string{"FAULTLINE_SEED=5"}, guest, "-test.run=^TestCryptoRand$")
	zero, _, _ := runWasm(t, []string{"FAULTLINE_SEED=0"}, guest, "-test.run=^TestCryptoRand$")
	out, errOut, code := runWasm(t, []string{"GOOD_PLAN=1"}, guest, "-test.run=^(TestFail|TestCryptoRand|TestBadPlan)$")
	hex := regexp.MustCompile(`(?m)^[0-9a-f]{32}$`).FindAllString(out, -1)
	want := []string{strings.SplitN(zero, "\n", 2)[0], strings.SplitN(single, "\n", 2)[0]}
	if code != 1 || strings.Join(hex, " ") != strings.Join(want, " ") {
		t.Fatalf("exit %d, stdout lines %q, want exit 1 and %q (seed 0, then seed 5)\nstdout %s\nstderr %s", code, hex, want, out, errOut)
	}
}
