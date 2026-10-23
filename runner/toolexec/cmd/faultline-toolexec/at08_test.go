// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fixtureModule copies testdata/<name> to dir/<name> and makes it module example.com/<name>
// there. No go.mod is committed under testdata (DET §11): CI's nested job would treat it as a
// nested module. With faultline set, the module requires the checkout's root module through an
// absolute replace, starts from a copy of the root go.sum, and is tidied (go test needs the root
// module's requirements listed). Run go on the result with GOWORK=off.
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
		if err := os.WriteFile(filepath.Join(dst, "go.sum"), sum, 0o600); err != nil { //nolint:gosec // the copy of the checkout's go.sum in the test's directory
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

// fixture is fixtureModule in a fresh t.TempDir().
func fixture(t *testing.T, name string, faultline bool) string {
	t.Helper()
	dir, err := fixtureModule(t.TempDir(), name, faultline)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// cleanEnv is the environment without FAULTLINE_* variables.
func cleanEnv(env ...string) []string {
	var out []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "FAULTLINE_") {
			out = append(out, kv)
		}
	}
	return append(out, env...)
}

// buildWithWrapper compiles the test binary of the fixture module in dir with -toolexec.
func buildWithWrapper(t *testing.T, dir string, all bool, env ...string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "x.test")
	args := []string{"test", "-c", "-o", bin, "-toolexec=" + wrapper}
	if all {
		args = append(args, "-a")
	}
	cmd := exec.Command("go", append(args, ".")...) //nolint:gosec // the go command with the test's arguments
	cmd.Dir = dir
	cmd.Env = cleanEnv(append([]string{"GOWORK=off"}, env...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go test -c -toolexec in %s: %v\n%s", dir, err, out)
	}
	return bin
}

// readKept returns the kept rewritten file with base name base under dir.
func readKept(t *testing.T, dir, base string) string {
	t.Helper()
	var found string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == base {
			found = p
		}
		return nil
	})
	if found == "" {
		t.Fatalf("no kept %s under %s", base, dir)
	}
	b, err := os.ReadFile(found)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// AT-EXB-08
func TestATEXB08_RewritesAndStd(t *testing.T) {
	if testing.Short() {
		t.Skip("rebuilds the standard library through the wrapper")
	}
	keep, log := t.TempDir(), filepath.Join(t.TempDir(), "wrapper.log")
	bin := buildWithWrapper(t, fixture(t, "exb08", false), true, "FAULTLINE_TOOLEXEC_KEEP="+keep, "FAULTLINE_TOOLEXEC_LOG="+log)
	src := readKept(t, keep, "exb08.go")
	for _, want := range []string{
		"//line ",
		"__fl_sched.Go(func() {",
		"__fl_sched.Point(); ch <- i",
		"__fl_sched.Select([]__fl_reflect.SelectCase{",
		"__fl_sched.Lock(&(c).Mutex)",
		"defer __fl_sched.Unlock(&(c).Mutex)",
		"__fl_sched.RLock(&(c.rw))",
		"__fl_sched.OnceDo(&(c.o), ",
		":= range __fl_sched.Keys(c.m) {",
		"__fl_sched.Point(); wg.Wait()",
		"__fl_sched.RandV1().Intn(6)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("rewritten exb08.go lacks %q", want)
		}
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"compile fmt unchanged\n",
		"compile sync unchanged\n",
		"compile example.com/exb08 [example.com/exb08.test] rewritten\n",
		"link example.com/exb08.test\n",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("wrapper log lacks %q", strings.TrimSpace(want))
		}
	}
	if out, err := exec.Command(bin, "-test.count=1").CombinedOutput(); err != nil {
		t.Fatalf("the rewritten package fails outside goroutine mode: %v\n%s", err, out)
	}
}
