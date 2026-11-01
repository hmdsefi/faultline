// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package acceptance holds the toolexec backend's acceptance tests: real builds through the
// wrapper of the fixture modules under testdata.
package acceptance

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
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
	dir, err := os.MkdirTemp("", "xte-acceptance-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	wrapper = filepath.Join(dir, "faultline-toolexec")
	b := exec.Command("go", "build", "-o", wrapper, "../cmd/faultline-toolexec")
	b.Env = append(os.Environ(), "GOWORK=off")
	if out, err := b.CombinedOutput(); err != nil {
		return 0, fmt.Errorf("building faultline-toolexec: %v\n%s", err, out)
	}
	if err := os.Setenv("FAULTLINE_TOOLEXEC_STATE", filepath.Join(dir, "state")); err != nil {
		return 0, err
	}
	prep := exec.Command(wrapper, "prepare")
	prep.Dir = ".." // the runner/toolexec module: go list resolves sched there
	prep.Env = append(os.Environ(), "GOWORK=off")
	if out, err := prep.CombinedOutput(); err != nil {
		return 0, fmt.Errorf("faultline-toolexec prepare: %v\n%s", err, out)
	}
	return m.Run(), nil
}

// checkout is the faultline checkout's root.
func checkout(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// fixture copies testdata/<name> to a temporary directory and makes it module
// example.com/<name> that requires the checkout's root module (DET §11: no go.mod is committed
// under testdata).
func fixture(t *testing.T, name string) string {
	t.Helper()
	root := checkout(t)
	dst := filepath.Join(t.TempDir(), name)
	if err := os.CopyFS(dst, os.DirFS(filepath.Join("testdata", name))); err != nil {
		t.Fatal(err)
	}
	sum, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "go.sum"), sum, 0o600); err != nil { //nolint:gosec // the copy in the test's directory
		t.Fatal(err)
	}
	mod := "module example.com/" + name + "\n\ngo 1.26.0\n\nrequire github.com/hmdsefi/faultline v0.0.0-00010101000000-000000000000\n\n" +
		"replace github.com/hmdsefi/faultline => " + strconv.Quote(root) + "\n"
	if err := os.WriteFile(filepath.Join(dst, "go.mod"), []byte(mod), 0o600); err != nil {
		t.Fatal(err)
	}
	tidy := command(t, "go", "mod", "tidy")
	tidy.Dir = dst
	tidy.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy in %s: %v\n%s", dst, err, out)
	}
	return dst
}

// cleanEnv is the environment without FAULTLINE_* variables except the wrapper's state
// directory, with GOWORK=off, plus env.
func cleanEnv(env ...string) []string {
	var out []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "FAULTLINE_") || strings.HasPrefix(kv, "FAULTLINE_TOOLEXEC_STATE=") {
			out = append(out, kv)
		}
	}
	return append(append(out, "GOWORK=off"), env...)
}

// build compiles the test binary of pkg in dir, through the wrapper when toolexec is set.
func build(t *testing.T, dir, pkg string, toolexec bool, flags []string, env ...string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "x.test")
	args := []string{"test", "-c", "-o", bin}
	if toolexec {
		args = append(args, "-toolexec="+wrapper)
	}
	cmd := command(t, "go", append(append(args, flags...), pkg)...)
	cmd.Dir = dir
	cmd.Env = cleanEnv(env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go %s in %s: %v\n%s", strings.Join(cmd.Args[1:], " "), dir, err, out)
	}
	return bin
}

// command is exec.Command for a go command or a shell that a test starts. The command is killed 30 s
// before the test binary's deadline, so a hung command fails its test with its output instead of
// outliving the binary.
func command(t *testing.T, name string, args ...string) *exec.Cmd {
	t.Helper()
	ctx := context.Background()
	if d, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, d.Add(-30*time.Second))
		t.Cleanup(cancel)
	}
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // the go command or the replay with the test's arguments
	cmd.WaitDelay = 10 * time.Second
	return cmd
}

// childTimeout is the -test.timeout of a test binary or go test command that a test starts, well
// under the acceptance package's own timeout, so that a hung child fails its test instead of
// outliving it.
const childTimeout = "3m"

// runBin runs a test binary in dir with cleanEnv(env...) and returns its output and exit code. The
// binary gets -test.timeout=childTimeout unless args sets another.
func runBin(t *testing.T, bin, dir string, env []string, args ...string) (string, int) {
	t.Helper()
	args = append([]string{"-test.timeout=" + childTimeout}, args...)
	cmd := exec.Command(bin, args...) //nolint:gosec // a binary the test built
	cmd.Dir = dir
	cmd.Env = cleanEnv(env...)
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		t.Fatalf("running %s: %v", bin, err)
	}
	return string(out), cmd.ProcessState.ExitCode()
}

// result is the part of a FAULTLINE_RESULTS line (API-087) these tests read.
type result struct {
	Test             string `json:"test"`
	Seed             string `json:"seed"`
	Status           string `json:"status"`
	TraceHash        string `json:"trace_hash"`
	DeterminismLevel string `json:"determinism_level"`
	ExactBackend     string `json:"exact_backend"`
}

// results reads the results file at path.
func results(t *testing.T, path string) []result {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // a file of the test's directory
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []result
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var r result
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("results line %q: %v", sc.Text(), err)
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// seeds returns the first n seeds of EXB-060's base, as 0x%016x.
func seeds(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("0x%016x", faultline.DeriveSeed(0x0e0b5eed00000001, i))
	}
	return out
}

// runSeeds runs test of bin for the first n seeds in one process and returns the results and the
// EXB_OUT outcomes ("<test>\t<seed>\t<outcome>" lines), both by seed.
func runSeeds(t *testing.T, bin, dir, test string, n int, env ...string) (map[string]result, map[string]string) {
	t.Helper()
	tmp := t.TempDir()
	res, out := filepath.Join(tmp, "results.jsonl"), filepath.Join(tmp, "out.txt")
	env = append([]string{"FAULTLINE_SEED_LIST=" + strings.Join(seeds(n), ","), "FAULTLINE_RESULTS=" + res,
		"EXB_OUT=" + out, "FAULTLINE_TRACE=full", "FAULTLINE_ARTIFACTS=off"}, env...)
	text, code := runBin(t, bin, dir, env, "-test.run=^"+test+"$", "-test.count=1")
	if code != 0 && code != 1 {
		t.Fatalf("%s: exit %d\n%s", test, code, text)
	}
	bySeed := map[string]result{}
	for _, r := range results(t, res) {
		bySeed[r.Seed] = r
	}
	if len(bySeed) != n {
		t.Fatalf("%s: results for %d of %d seeds\n%s", test, len(bySeed), n, text)
	}
	outcomes := map[string]string{}
	if b, err := os.ReadFile(out); err == nil { //nolint:gosec // a file of the test's directory
		for _, l := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") { // outcomes keep their spaces
			if f := strings.SplitN(l, "\t", 3); len(f) == 3 {
				outcomes[f[1]] = f[2]
			}
		}
	}
	return bySeed, outcomes
}

// linksSched reports whether go tool nm lists a symbol of package sched in the binary.
func linksSched(t *testing.T, bin string) bool {
	t.Helper()
	out, err := command(t, "go", "tool", "nm", bin).Output()
	if err != nil {
		t.Fatalf("go tool nm %s: %v", bin, err)
	}
	return strings.Contains(string(out), " github.com/hmdsefi/faultline/runner/toolexec/sched.")
}
