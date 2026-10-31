// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package acceptance

import (
	"fmt"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// exact checks EXB-061 for two runs of the first len(a) seeds of EXB-060's base: every one of
// them has a trace hash in both runs and equal hashes and statuses.
func exact(t *testing.T, name string, a, b map[string]result) {
	t.Helper()
	for _, s := range seeds(len(a)) {
		r := a[s]
		if r.TraceHash == "" || r.TraceHash != b[s].TraceHash || r.Status != b[s].Status {
			t.Errorf("%s seed %s: %s/%s, then %s/%s", name, s, r.TraceHash, r.Status, b[s].TraceHash, b[s].Status)
		}
	}
}

// AT-XTE-33
func TestATXTE33_Exactness(t *testing.T) {
	// C3 is built with net/http rewritten, XTE-031's tested opt-in: in the default build, net/http's
	// own goroutines and timers run outside sched, and C3's trace hash differed between two runs of
	// the same seed in about 1 of 2,500 comparisons.
	std := []string{"FAULTLINE_TOOLEXEC_STD=net/http"}
	for _, c := range []struct {
		fixture, test string
		env           []string
	}{
		{"gkv", "TestGKV", nil}, {"httpcorpus", "TestHTTPCorpus", std},
		{"order", "TestMapOrder", nil}, {"order", "TestSelectOrder", nil},
	} {
		dir := fixture(t, c.fixture)
		bin := build(t, dir, ".", true, nil, c.env...)
		a, _ := runSeeds(t, bin, dir, c.test, 20)
		b, _ := runSeeds(t, bin, dir, c.test, 20)
		exact(t, c.test, a, b)
	}
}

// outcomes checks EXB-062's rule that each seed's two runs agree and returns the number of
// distinct outcomes.
func outcomes(t *testing.T, name string, a, b map[string]string, n int) int {
	t.Helper()
	distinct := map[string]bool{}
	for _, s := range seeds(n) {
		if a[s] == "" || a[s] != b[s] {
			t.Errorf("%s seed %s: outcomes %q and %q", name, s, a[s], b[s])
		}
		distinct[a[s]] = true
	}
	return len(distinct)
}

// AT-XTE-34 and AT-XTE-35
func TestATXTE34_35_SeedSensitivity(t *testing.T) {
	dir := fixture(t, "order")
	bin := build(t, dir, ".", true, nil)
	for _, c := range []struct {
		test string
		min  int
	}{{"TestMapOrder", 4}, {"TestSelectOrder", 95}, {"TestGoroutineOrder", 95}} {
		_, a := runSeeds(t, bin, dir, c.test, 100)
		_, b := runSeeds(t, bin, dir, c.test, 100)
		if n := outcomes(t, c.test, a, b, 100); n < c.min {
			t.Errorf("%s: %d distinct outcomes of 100 seeds, want at least %d", c.test, n, c.min)
		}
	}
}

// c1 is EXB-050's list by package directory (relative to the checkout) and test name.
var c1 = map[string][]string{
	".": {"TestATGOR01_SleepFollowsBubbleTime", "TestATGOR02_DoRunsAtVirtualTime", "TestATGOR03_UserTimerBeforeKernelEvent",
		"TestATGOR04_LogOrderAndDeterminism", "TestATGOR05_CrashRestartWithNetwork", "TestATGOR06_ExitedListenerRefuses",
		"TestATGOR11_UDPReaderRaceFree", "TestATGOR17_EventNodeUDPInterop", "TestATGOR18_PausedWriterDeferred",
		"TestATGOR19_ClockJumpExcludedFromMonotonic", "TestATGOR20_DriftAndToGlobal", "TestATGOR29_DoKeyOrder",
		"TestATGOR30_EchoReplayRate", "TestATFSX03_ConformanceInGoroutineMode", "TestATFSX04_StaleAfterRestart",
		"TestATFSX05_CrashLosesUnsynced", "TestATFSX06_Latency", "TestATFSX07_PausedWriteWaits",
		"TestATFSX12_MutexAcrossFileIO", "TestATFSX13_AppendSyncDeterminism"},
	"shims/netsim": {"TestATNSM01_Handshake", "TestATNSM02_Refused", "TestATNSM03_EventNodeRefuses",
		"TestATNSM04_AddressErrors", "TestATNSM05_LostSyns", "TestATNSM06_DialTimesOut", "TestATNSM07_JitterReordering",
		"TestATNSM08_Backpressure", "TestATNSM09_LossAbsorbed", "TestATNSM10_PartitionHealedBeforeTimeout",
		"TestATNSM11_ConnectionTimeout", "TestATNSM12_PeerCrashResets", "TestATNSM13_IsolatedCrashLearnedLater",
		"TestATNSM14_HalfClose", "TestATNSM15_CloseWithUnreadData", "TestATNSM16_WriteAfterPeerClosed",
		"TestATNSM17_ReadDeadlines", "TestATNSM18_WriteDeadline", "TestATNSM19_Backlog", "TestATNSM20_ListenerClose",
		"TestATNSM21_ListenErrors", "TestATNSM22_Loopback", "TestATNSM23_UDPDialAndReply", "TestATNSM24_UDPLoss",
		"TestATNSM25_UDPOverflow", "TestATNSM26_ProcessExitReleases", "TestATNSM27_RunEndUnblocks",
		"TestATNSM28_PausedNodeIO", "TestATNSM29_HTTPOverNetsim", "TestATNSM31_SoakIsDeterministic"},
	"shims/grpcsim": {"TestATNSM30_GRPCHealthAndReconnect"},
}

// AT-XTE-36
func TestATXTE36_Compatibility(t *testing.T) {
	root := checkout(t)
	n := 0
	for _, rel := range []string{".", "shims/netsim", "shims/grpcsim"} {
		dir, pkg := root, "./"+rel
		if rel == "shims/grpcsim" { // a nested module
			dir, pkg = filepath.Join(root, rel), "."
		}
		bin := build(t, dir, pkg, true, nil)
		run := "^(" + strings.Join(c1[rel], "|") + ")$"
		out, code := runBin(t, bin, filepath.Join(root, rel), []string{"FAULTLINE_EXACT=1", "FAULTLINE_CHECK_DETERMINISM=1"}, "-test.run="+run, "-test.count=1", "-test.v")
		if code != 0 {
			t.Errorf("%s: exit %d\n%s", rel, code, out)
		}
		for _, name := range c1[rel] {
			if !strings.Contains(out, "--- PASS: "+name+" ") {
				t.Errorf("%s: %s did not pass", rel, name)
			}
		}
		n += len(c1[rel])
	}
	if n != 51 {
		t.Fatalf("C1 lists %d tests, want EXB-050's 51", n)
	}
	dir := fixture(t, "compat")
	out, code := runBin(t, build(t, dir, ".", true, nil), dir, []string{"FAULTLINE_EXACT=1", "FAULTLINE_CHECK_DETERMINISM=1"}, "-test.count=1", "-test.v")
	if code != 0 {
		t.Fatalf("C6: exit %d\n%s", code, out)
	}
	for _, name := range []string{"TestCompatSync", "TestCompatEncoding", "TestCompatSynctest", "TestCompatCryptotest"} {
		if !strings.Contains(out, "--- PASS: "+name+" ") {
			t.Errorf("C6: %s did not pass", name)
		}
	}
}

// AT-XTE-37
func TestATXTE37_NetHTTPRewritten(t *testing.T) {
	dir := fixture(t, "httpcorpus")
	log := filepath.Join(t.TempDir(), "wrapper.log")
	bin := build(t, dir, ".", true, []string{"-a"}, "FAULTLINE_TOOLEXEC_STD=net/http", "FAULTLINE_TOOLEXEC_LOG="+log)
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "compile net/http rewritten\n") {
		t.Fatal("the log has no compile net/http rewritten line")
	}
	a, _ := runSeeds(t, bin, dir, "TestHTTPCorpus", 20)
	c, _ := runSeeds(t, bin, dir, "TestHTTPCorpus", 20)
	exact(t, "TestHTTPCorpus with net/http rewritten", a, c)
}

// AT-XTE-38
func TestATXTE38_Slowdown(t *testing.T) {
	dir := fixture(t, "gkv")
	// The plain and wrapped runs alternate, so a slow spell of the machine counts against both.
	bins, d := []string{build(t, dir, ".", false, nil), build(t, dir, ".", true, nil)}, [2][]time.Duration{}
	for range 3 {
		for i, bin := range bins {
			start := time.Now()
			runSeeds(t, bin, dir, "TestGKV", 20)
			d[i] = append(d[i], time.Since(start))
		}
	}
	median := func(d []time.Duration) time.Duration { slices.Sort(d); return d[1] }
	plain, wrapped := median(d[0]), median(d[1])
	t.Logf("C2, 20 seeds: Part A %v, toolexec %v, %.2fx", plain, wrapped, float64(wrapped)/float64(plain))
	if wrapped > 5*plain {
		t.Fatalf("toolexec median %v is more than 5x Part A's %v", wrapped, plain)
	}
}

// AT-XTE-39
func TestATXTE39_Dependencies(t *testing.T) {
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Path}}", "all")
	cmd.Dir = ".."
	cmd.Env = cleanEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Fields(string(out))
	slices.Sort(got)
	if want := []string{"github.com/hmdsefi/faultline", "github.com/hmdsefi/faultline/runner/toolexec", "github.com/hmdsefi/gograph"}; !slices.Equal(got, want) {
		t.Fatalf("modules %q, want %q", got, want)
	}
}

// codeLines returns the lines of src that EXB-066 counts: non-blank and outside comments
// (exbmeasure's rule on spike/exb).
func codeLines(src []byte) map[int]bool {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, src, nil, scanner.ScanComments)
	text := strings.Split(string(src), "\n")
	lines := map[int]bool{}
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.COMMENT || (tok == token.SEMICOLON && lit == "\n") {
			continue
		}
		first := file.PositionFor(pos, false).Line
		lines[first] = true
		for i := 1; i <= strings.Count(lit, "\n"); i++ {
			if strings.TrimSpace(text[first+i-1]) != "" {
				lines[first+i] = true
			}
		}
	}
	return lines
}

// AT-XTE-40
func TestATXTE40_Size(t *testing.T) {
	root := checkout(t)
	var files []string
	for _, tree := range []string{"runner/toolexec", "internal/gosched"} {
		err := filepath.WalkDir(filepath.Join(root, tree), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && d.Name() == "testdata" {
				return filepath.SkipDir
			}
			if !d.IsDir() && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	files = append(files, filepath.Join(root, "gosched_hook.go"), filepath.Join(root, "exact.go"))
	total := 0
	for _, f := range files {
		b, err := os.ReadFile(f) //nolint:gosec // a file of the checkout
		if err != nil {
			t.Fatal(err)
		}
		total += len(codeLines(b))
	}
	t.Logf("toolexec backend: %d counted lines in %d files", total, len(files))
	if total > 2900 {
		t.Fatalf("%d counted lines; XTE-003 allows 2,900 here (100 are reserved for lines in other root files)", total)
	}
}

// AT-XTE-41 (AT-EXB-08)
func TestATXTE41_RewritesAndStd(t *testing.T) {
	keep, log := t.TempDir(), filepath.Join(t.TempDir(), "wrapper.log")
	dir := fixture(t, "exb08")
	bin := build(t, dir, ".", true, []string{"-a"}, "FAULTLINE_TOOLEXEC_KEEP="+keep, "FAULTLINE_TOOLEXEC_LOG="+log)
	src, err := os.ReadFile(filepath.Join(keep, "example.com_exb08_example.com_exb08.test", "exb08.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"//line ", "__fl_sched.Go(func() {", "__fl_sched.Point(); ch <- i", "__fl_sched.Select([]__fl_reflect.SelectCase{",
		"__fl_sched.Lock(&(c).Mutex)", "defer __fl_sched.Unlock(&(c).Mutex)", "__fl_sched.RLock(&(c.rw))",
		"__fl_sched.OnceDo(&(c.o), ", " := c.m; for _, ", " := range __fl_sched.Keys(__fl_m",
		"__fl_sched.Point(); wg.Wait()", "__fl_sched.RandV1().Intn(6)",
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("rewritten exb08.go lacks %q", want)
		}
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"compile fmt unchanged\n", "compile sync unchanged\n",
		"compile example.com/exb08 [example.com/exb08.test] rewritten\n", "link example.com/exb08.test\n"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("wrapper log lacks %q", strings.TrimSpace(want))
		}
	}
	if !linksSched(t, bin) {
		t.Error("the binary does not link sched")
	}
	if out, code := runBin(t, bin, dir, nil, "-test.count=1"); code != 0 {
		t.Fatalf("the rewritten package fails outside goroutine mode: exit %d\n%s", code, out)
	}
}

// AT-XTE-42 (AT-EXB-09, with the property checked directly: the binary links sched and the
// package was rewritten)
func TestATXTE42_ExchangeIsExact(t *testing.T) {
	log := filepath.Join(t.TempDir(), "wrapper.log")
	dir := fixture(t, "exb09")
	bin := build(t, dir, ".", true, nil, "FAULTLINE_TOOLEXEC_LOG="+log)
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "compile example.com/exb09 [example.com/exb09.test] rewritten\n") || !linksSched(t, bin) {
		t.Fatalf("exb09 was not rewritten or does not link sched\n%s", b)
	}
	orders := map[string]bool{}
	for seed := 1; seed <= 10; seed++ {
		var got [2]string
		for r := range 2 {
			out := filepath.Join(t.TempDir(), "out")
			text, code := runBin(t, bin, dir, []string{fmt.Sprintf("FAULTLINE_SEED=%d", seed), "EXB_OUT=" + out, "FAULTLINE_ARTIFACTS=off"}, "-test.run=^TestExchange$", "-test.count=1")
			if code != 0 {
				t.Fatalf("seed %d run %d: exit %d\n%s", seed, r+1, code, text)
			}
			line, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			f := strings.Split(strings.TrimSpace(string(line)), "\t")
			if len(f) != 3 || len(f[2]) != 200 {
				t.Fatalf("seed %d run %d: EXB_OUT %q", seed, r+1, line)
			}
			got[r] = f[2]
		}
		if got[0] != got[1] {
			t.Fatalf("seed %d: the two runs differ\n%s\n%s", seed, got[0], got[1])
		}
		orders[got[0]] = true
	}
	if len(orders) < 2 {
		t.Fatal("all 10 seeds gave the same order")
	}
}
