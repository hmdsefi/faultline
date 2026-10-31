// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package acceptance

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/artifact"
)

// AT-XTE-26
func TestATXTE26_TestMainLinksSched(t *testing.T) {
	dir := fixture(t, "plain")
	for _, flags := range [][]string{nil, {"-race"}} {
		log := filepath.Join(t.TempDir(), "wrapper.log")
		bin := build(t, dir, ".", true, flags, "FAULTLINE_TOOLEXEC_LOG="+log)
		if !linksSched(t, bin) {
			t.Errorf("%v: the binary does not link sched", flags)
		}
		b, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "compile example.com/plain.test testmain\n") {
			t.Errorf("%v: log lacks the test main line\n%s", flags, b)
		}
		res := filepath.Join(t.TempDir(), "r.jsonl")
		if out, code := runBin(t, bin, dir, []string{"FAULTLINE_SEED=0x1", "FAULTLINE_RESULTS=" + res, "FAULTLINE_ARTIFACTS=off"}, "-test.count=1"); code != 0 {
			t.Fatalf("%v: exit %d\n%s", flags, code, out)
		}
		if rs := results(t, res); len(rs) != 1 || rs[0].DeterminismLevel != "exact" || rs[0].ExactBackend != "toolexec" {
			t.Errorf("%v: results %+v, want one exact/toolexec line", flags, rs)
		}
	}
}

// AT-XTE-27
func TestATXTE27_ReplayCommandReplays(t *testing.T) {
	dir := fixture(t, "fail")
	bin := build(t, dir, ".", true, nil)
	first := filepath.Join(t.TempDir(), "first.jsonl")
	out, code := runBin(t, bin, dir, []string{"FAULTLINE_SEED=0x1", "FAULTLINE_RESULTS=" + first, "FAULTLINE_ARTIFACTS=off"}, "-test.count=1")
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s", code, out)
	}
	m := regexp.MustCompile(`(?m)^\s*replay:\s+(.*)$`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no replay line\n%s", out)
	}
	exe, err := filepath.EvalSymlinks(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m[1], "go test -v -toolexec="+exe+" -run ") { // XTE-100: right after go test -v
		t.Fatalf("replay %q lacks -toolexec=%s right after go test -v", m[1], exe)
	}
	second := filepath.Join(t.TempDir(), "second.jsonl")
	sh := command(t, "sh", "-c", m[1])
	sh.Dir = dir
	sh.Env = cleanEnv("FAULTLINE_RESULTS="+second, "FAULTLINE_ARTIFACTS=off",
		"GOFLAGS="+strings.TrimSpace(os.Getenv("GOFLAGS")+" -timeout="+childTimeout))
	replay, err := sh.CombinedOutput()
	if err == nil {
		t.Fatalf("the replay passed\n%s", replay)
	}
	if _, err := os.Stat(second); err != nil {
		t.Fatalf("the replay wrote no results: %v\n%s", err, replay)
	}
	a, b := results(t, first), results(t, second)
	if len(a) != 1 || len(b) != 1 || a[0].TraceHash == "" || a[0].TraceHash != b[0].TraceHash || a[0].Status != b[0].Status {
		t.Fatalf("first %+v, replay %+v: want one line each with the same trace hash and status\n%s", a, b, replay)
	}
}

// AT-XTE-30
func TestATXTE30_LivelockStalls(t *testing.T) {
	dir := fixture(t, "livelock")
	log := filepath.Join(t.TempDir(), "wrapper.log")
	bin := build(t, dir, ".", true, nil, "FAULTLINE_TOOLEXEC_LOG="+log)
	if b, err := os.ReadFile(log); err != nil || !strings.Contains(string(b), "compile example.com/livelock [example.com/livelock.test] rewritten\n") {
		t.Fatalf("the polling select was not rewritten: %v\n%s", err, b)
	}
	start := time.Now()
	out, code := runBin(t, bin, dir, []string{"FAULTLINE_SEED=0x1", "FAULTLINE_ARTIFACTS=off"}, "-test.count=1", "-test.timeout=60s")
	if code == 0 || time.Since(start) > 30*time.Second {
		t.Fatalf("exit %d after %v; want a stall report within 30 s\n%s", code, time.Since(start), out)
	}
	for _, want := range []string{
		"faultline: goroutine mode stalled: no driver progress for 2s of wall time\n",
		"    [running] / [runnable]: a goroutine that never blocks (busy loop)\n" + // XTE-082: right after this line
			"    [toolexec] goroutines parked in sched.Point and nothing else moving: a goroutine reaches\n" +
			"        scheduling points without ever blocking (a polling loop, for example a select with a default)\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q\n%s", want, out)
		}
	}
}

// AT-XTE-31
func TestATXTE31_FaultlineExact(t *testing.T) {
	dir := fixture(t, "plain")
	env := []string{"FAULTLINE_EXACT=1", "FAULTLINE_SEED=0x1", "FAULTLINE_ARTIFACTS=off"}
	if out, code := runBin(t, build(t, dir, ".", true, nil), dir, env, "-test.count=1"); code != 0 {
		t.Errorf("wrapper build: exit %d\n%s", code, out)
	}
	out, code := runBin(t, build(t, dir, ".", false, nil), dir, env, "-test.count=1")
	if code != 1 || !strings.Contains(out, "faultline: FAULTLINE_EXACT=1, but this test binary was built without the toolexec backend; "+
		"build it with go test -toolexec=faultline-toolexec after running faultline-toolexec prepare") {
		t.Errorf("plain build: exit %d\n%s", code, out)
	}
	out, code = runBin(t, build(t, dir, ".", true, nil), dir, append(env, "XTE_PROCS=2"), "-test.count=1")
	if code != 1 || !strings.Contains(out, "faultline: FAULTLINE_EXACT=1, but Options.Procs is 2; exact replay needs Procs 1") {
		t.Errorf("Procs 2: exit %d\n%s", code, out)
	}
}

// AT-XTE-32
func TestATXTE32_GapsInTheReport(t *testing.T) {
	dir := fixture(t, "gapfail")
	bin := build(t, dir, ".", true, nil)
	arts := t.TempDir()
	out, code := runBin(t, bin, dir, []string{"FAULTLINE_SEED=0x1", "FAULTLINE_ARTIFACTS=" + arts}, "-test.count=1")
	if code != 1 || !regexp.MustCompile(`(?m)^\s*coverage gaps: 1 in this test binary; toolexec-gaps\.txt in the artifact directory lists them$`).MatchString(out) {
		t.Fatalf("exit %d; no gaps line\n%s", code, out)
	}
	b, err := os.ReadFile(filepath.Join(artifact.Dir(arts, "example.com/gapfail", "TestGapFail", 1), "toolexec-gaps.txt"))
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join("testdata", "gapfail", "gapfail_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	line := 0
	for i, l := range strings.Split(string(src), "\n") {
		if strings.Contains(l, "// the gap") {
			line = i + 1
		}
	}
	path, rest, ok := strings.Cut(string(b), ":"+strconv.Itoa(line)+": ")
	if !ok || rest != "range over a map with unordered keys\n" || strings.Contains(path, "\n") || !filepath.IsAbs(path) {
		t.Fatalf("toolexec-gaps.txt = %q, want the one line <abs path>:%d: range over a map with unordered keys", b, line)
	}
	// On darwin the go command may name the /var/... temporary directory /private/var/...
	got, err1 := filepath.EvalSymlinks(path)
	want, err2 := filepath.EvalSymlinks(filepath.Join(dir, "gapfail_test.go"))
	if err1 != nil || err2 != nil || got != want {
		t.Fatalf("toolexec-gaps.txt names %s, want %s (%v, %v)", path, want, err1, err2)
	}
}
