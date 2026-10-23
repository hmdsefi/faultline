// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"bufio"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
)

// writeResults writes one synthetic FAULTLINE_RESULTS line for TestMapOrder.
func writeResults(t *testing.T, c *config, s uint64, run int, hash, status string) {
	t.Helper()
	tc := corpora["C4"][0]
	p := c.rawPath(tc, s, run, "jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf(`{"faultline_result":1,"package":"x","test":"TestMapOrder","seed":"0x%016x","index":0,"status":%q,"trace_hash":%q,"wall_ns":1}`+"\n", s, status, hash)
	if err := os.WriteFile(p, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

// AT-EXB-10
func TestATEXB10_ExactnessFromSyntheticResults(t *testing.T) {
	work := t.TempDir()
	c := &config{backend: "parta", corpus: "C4", seeds: 3, runs: 2, reuse: true, work: work, root: work,
		out: filepath.Join(work, "exactness-parta-C4.csv")}
	s0, s1, s2 := faultline.DeriveSeed(base, 0), faultline.DeriveSeed(base, 1), faultline.DeriveSeed(base, 2)
	writeResults(t, c, s0, 1, "0x00000000000000aa", "pass")
	writeResults(t, c, s0, 2, "0x00000000000000aa", "pass")
	writeResults(t, c, s1, 1, "0x00000000000000aa", "pass")
	writeResults(t, c, s1, 2, "0x00000000000000bb", "pass")
	writeResults(t, c, s2, 1, "0x00000000000000cc", "pass")
	writeResults(t, c, s2, 2, "0x00000000000000cc", "fail")
	err := exactness(c)
	if err != errRequirement {
		t.Fatalf("exactness = %v, want errRequirement (1 of 3 exact)", err)
	}
	b, err := os.ReadFile(c.out)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"test,seed,run1_hash,run2_hash,run1_status,run2_status,exact",
		"TestMapOrder,0xafb28507f36457eb,0x00000000000000aa,0x00000000000000aa,pass,pass,true",
		"TestMapOrder,0x75a9d0b00437271e,0x00000000000000aa,0x00000000000000bb,pass,pass,false",
		"TestMapOrder,0x43ecfcf56902d0b3,0x00000000000000cc,0x00000000000000cc,pass,fail,false",
	}, "\n") + "\n"
	if string(b) != want {
		t.Fatalf("csv:\n%s\nwant:\n%s", b, want)
	}
}

func TestExactnessMissingRunIsNotExact(t *testing.T) {
	work := t.TempDir()
	c := &config{backend: "wasm", corpus: "C4", seeds: 1, runs: 2, reuse: true, work: work, root: work,
		out: filepath.Join(work, "e.csv")}
	s0 := faultline.DeriveSeed(base, 0)
	writeResults(t, c, s0, 1, "0x00000000000000aa", "pass")
	tc := corpora["C4"][0]
	if err := os.WriteFile(c.rawPath(tc, s0, 2, "jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := exactness(c); err != errRequirement {
		t.Fatalf("exactness = %v", err)
	}
	b, _ := os.ReadFile(c.out)
	if !strings.Contains(string(b), "0x00000000000000aa,,pass,missing,false") {
		t.Fatalf("csv:\n%s", b)
	}
}

// writeOrder writes the synthetic results and EXB_OUT files of one C7 run.
func writeOrder(t *testing.T, c *config, s uint64, run int, order string) {
	t.Helper()
	tc := corpora["C7"][0]
	if err := os.MkdirAll(filepath.Dir(c.rawPath(tc, s, run, "jsonl")), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.rawPath(tc, s, run, "jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf("TestGoroutineOrder\t0x%016x\t%s\n", s, order)
	if err := os.WriteFile(c.rawPath(tc, s, run, "out"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

// EXB-068: C7 is recorded, never gated, even when runs differ.
func TestSensitivityC7IsInformational(t *testing.T) {
	work := t.TempDir()
	c := &config{backend: "parta", corpus: "C7", seeds: 3, reuse: true, work: work, root: work,
		out: filepath.Join(work, "goroutine-order-parta.csv")}
	s0, s1, s2 := faultline.DeriveSeed(base, 0), faultline.DeriveSeed(base, 1), faultline.DeriveSeed(base, 2)
	writeOrder(t, c, s0, 1, "abab")
	writeOrder(t, c, s0, 2, "abab")
	writeOrder(t, c, s1, 1, "aabb")
	writeOrder(t, c, s1, 2, "abba")
	writeOrder(t, c, s2, 1, "abab")
	writeOrder(t, c, s2, 2, "abab")
	if err := sensitivity(c); err != nil {
		t.Fatalf("sensitivity C7 = %v, want nil (informational)", err)
	}
	b, err := os.ReadFile(c.out)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"seed,outcome_run1,outcome_run2",
		"0xafb28507f36457eb,abab,abab",
		"0x75a9d0b00437271e,aabb,abba",
		"0x43ecfcf56902d0b3,abab,abab",
	}, "\n") + "\n"
	if string(b) != want {
		t.Fatalf("csv:\n%s\nwant:\n%s", b, want)
	}
	c.corpus = "C6"
	if err := sensitivity(c); err == nil || !strings.Contains(err.Error(), "C4, C5 and C7") {
		t.Fatalf("sensitivity C6 = %v, want a usage error", err)
	}
}

// resultLine formats one synthetic FAULTLINE_RESULTS line; an empty hash leaves trace_hash out,
// as Part A does for a setup failure.
func resultLine(test, seed, status, hash string) string {
	if hash == "" {
		return fmt.Sprintf(`{"faultline_result":1,"package":"x","test":%q,"seed":%q,"index":-1,"status":%q,"kind":"setup","wall_ns":1}`, test, seed, status)
	}
	return fmt.Sprintf(`{"faultline_result":1,"package":"x","test":%q,"seed":%q,"index":0,"status":%q,"trace_hash":%q,"wall_ns":1}`, test, seed, status, hash)
}

// writeLines writes the results file of one run of tc with the given lines (none: an empty file).
func writeLines(t *testing.T, c *config, tc testCase, s uint64, run int, lines ...string) {
	t.Helper()
	p := c.rawPath(tc, s, run, "jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestExactnessRules pins what makes a (test, seed) row exact (EXB-061): every run has a results
// line for the test, every line carries a trace hash and the run's seed, and all runs agree on
// every line's trace_hash and status. Each case is seed 0 of C4 with two runs.
func TestExactnessRules(t *testing.T) {
	s0 := fmt.Sprintf("0x%016x", faultline.DeriveSeed(base, 0))
	s1 := fmt.Sprintf("0x%016x", faultline.DeriveSeed(base, 1))
	const aa, bb, cc = "0x00000000000000aa", "0x00000000000000bb", "0x00000000000000cc"
	m := func(seed, status, hash string) string { return resultLine("TestMapOrder", seed, status, hash) }
	for _, tt := range []struct {
		name       string
		run1, run2 []string
		rows       []string // CSV rows after the header
	}{
		{"equal hash and status", []string{m(s0, "pass", aa)}, []string{m(s0, "pass", aa)},
			[]string{"TestMapOrder," + s0 + "," + aa + "," + aa + ",pass,pass,true"}},
		{"hashes differ", []string{m(s0, "pass", aa)}, []string{m(s0, "pass", bb)},
			[]string{"TestMapOrder," + s0 + "," + aa + "," + bb + ",pass,pass,false"}},
		{"statuses differ", []string{m(s0, "pass", aa)}, []string{m(s0, "fail", aa)},
			[]string{"TestMapOrder," + s0 + "," + aa + "," + aa + ",pass,fail,false"}},
		{"no line in either run", nil, nil,
			[]string{"TestMapOrder," + s0 + ",,,missing,missing,false"}},
		{"no trace hash in either run", []string{m(s0, "fail", "")}, []string{m(s0, "fail", "")},
			[]string{"TestMapOrder," + s0 + ",,,fail,fail,false"}},
		{"both runs report another seed", []string{m(s1, "pass", aa)}, []string{m(s1, "pass", aa)},
			[]string{"TestMapOrder," + s0 + "," + aa + "," + aa + ",pass,pass,false"}},
		{"two equal lines in both runs", []string{m(s0, "pass", aa), m(s0, "pass", bb)}, []string{m(s0, "pass", aa), m(s0, "pass", bb)},
			[]string{"TestMapOrder," + s0 + "," + aa + ";" + bb + "," + aa + ";" + bb + ",pass;pass,pass;pass,true"}},
		{"first of two lines differs", []string{m(s0, "pass", aa), m(s0, "pass", bb)}, []string{m(s0, "pass", cc), m(s0, "pass", bb)},
			[]string{"TestMapOrder," + s0 + "," + aa + ";" + bb + "," + cc + ";" + bb + ",pass;pass,pass;pass,false"}},
		{"a second line in one run", []string{m(s0, "pass", aa)}, []string{m(s0, "pass", aa), m(s0, "pass", aa)},
			[]string{"TestMapOrder," + s0 + "," + aa + "," + aa + ";" + aa + ",pass,pass;pass,false"}},
		{"each subtest is a row, in order of first appearance",
			[]string{resultLine("TestMapOrder/b", s0, "pass", bb), resultLine("TestMapOrder/a", s0, "pass", aa)},
			[]string{resultLine("TestMapOrder/a", s0, "pass", aa), resultLine("TestMapOrder/b", s0, "pass", bb), resultLine("TestMapOrder/c", s0, "pass", cc)},
			[]string{
				"TestMapOrder/b," + s0 + "," + bb + "," + bb + ",pass,pass,true",
				"TestMapOrder/a," + s0 + "," + aa + "," + aa + ",pass,pass,true",
				"TestMapOrder/c," + s0 + ",," + cc + ",missing,pass,false",
			}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			work := t.TempDir()
			c := &config{backend: "parta", corpus: "C4", seeds: 1, runs: 2, reuse: true, work: work, root: work,
				out: filepath.Join(work, "e.csv")}
			tc := corpora["C4"][0]
			writeLines(t, c, tc, faultline.DeriveSeed(base, 0), 1, tt.run1...)
			writeLines(t, c, tc, faultline.DeriveSeed(base, 0), 2, tt.run2...)
			err := exactness(c)
			b, rerr := os.ReadFile(c.out)
			if rerr != nil {
				t.Fatal(rerr)
			}
			want := "test,seed,run1_hash,run2_hash,run1_status,run2_status,exact\n" + strings.Join(tt.rows, "\n") + "\n"
			if string(b) != want {
				t.Fatalf("csv:\n%s\nwant:\n%s", b, want)
			}
			allExact := !slices.ContainsFunc(tt.rows, func(r string) bool { return strings.HasSuffix(r, ",false") })
			if allExact && err != nil || !allExact && err != errRequirement {
				t.Fatalf("exactness = %v, want nil only when every row is exact", err)
			}
		})
	}
}

// EXB §4.3: exactness measures C1 to C5 only; C6 is compat's and C7 is sensitivity's (EXB-068).
func TestExactnessCorpora(t *testing.T) {
	for _, corpus := range []string{"C6", "C7", "C8"} {
		work := t.TempDir()
		c := &config{backend: "parta", corpus: corpus, seeds: 1, runs: 2, reuse: true, work: work, root: work,
			out: filepath.Join(work, "e.csv")}
		if err := exactness(c); err == nil || err == errRequirement || !strings.Contains(err.Error(), "C1 to C5") {
			t.Fatalf("exactness %s = %v, want a usage error", corpus, err)
		}
	}
	c := &config{backend: "parta", corpus: "C4", seeds: 1, runs: 1}
	if err := exactness(c); err == nil || err == errRequirement {
		t.Fatalf("exactness with -runs 1 = %v, want a usage error", err)
	}
}

// writeOutcome writes the results file and, for a non-empty outcome, the EXB_OUT line of one
// run of the first test of c.corpus.
func writeOutcome(t *testing.T, c *config, s uint64, run int, outcome string) {
	t.Helper()
	tc := corpora[c.corpus][0]
	writeLines(t, c, tc, s, run)
	if outcome == "" {
		return
	}
	line := fmt.Sprintf("%s\t0x%016x\t%s\n", tc.Name, s, outcome)
	if err := os.WriteFile(c.rawPath(tc, s, run, "out"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestCountOutcomes pins EXB-062's and EXB-068's counting: distinct non-empty run-1 outcomes, and
// seeds whose two runs have the same non-empty outcome.
func TestCountOutcomes(t *testing.T) {
	distinct, stable := countOutcomes([][2]string{
		{"01234567", "01234567"}, {"12345670", "12345670"}, {"01234567", "01234567"},
		{"", ""}, {"", "23456701"}, {"34567012", "45670123"},
	})
	if distinct != 3 || stable != 3 {
		t.Fatalf("countOutcomes = %d distinct, %d stable; want 3, 3", distinct, stable)
	}
}

// EXB-062: C4 needs at least 4 distinct key orders and C5 at least 95 distinct masks, and every
// seed's two runs must have the same outcome.
func TestSensitivityRequirement(t *testing.T) {
	orders := func(n int) []string {
		var o []string
		for i := range n {
			o = append(o, fmt.Sprintf("order%d", i))
		}
		return o
	}
	for _, tt := range []struct {
		name       string
		corpus     string
		run1, run2 []string
		ok         bool
	}{
		{"C4 with 4 orders", "C4", orders(4), orders(4), true},
		{"C4 with 3 orders", "C4", []string{"order0", "order1", "order2", "order0"}, []string{"order0", "order1", "order2", "order0"}, false},
		{"C4 with a seed whose runs differ", "C4", orders(5), append(orders(4), "order0"), false},
		{"C4 with an empty outcome", "C4", append(orders(4), ""), append(orders(4), ""), false},
		{"C5 with 95 masks", "C5", append(orders(95), orders(5)...), append(orders(95), orders(5)...), true},
		{"C5 with 94 masks", "C5", append(orders(94), orders(6)...), append(orders(94), orders(6)...), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			work := t.TempDir()
			c := &config{backend: "parta", corpus: tt.corpus, seeds: len(tt.run1), reuse: true, work: work, root: work,
				out: filepath.Join(work, "s.csv")}
			for i := range c.seeds {
				writeOutcome(t, c, faultline.DeriveSeed(base, i), 1, tt.run1[i])
				writeOutcome(t, c, faultline.DeriveSeed(base, i), 2, tt.run2[i])
			}
			err := sensitivity(c)
			if tt.ok && err != nil || !tt.ok && err != errRequirement {
				t.Fatalf("sensitivity = %v, want ok=%v", err, tt.ok)
			}
			b, err := os.ReadFile(c.out)
			if err != nil {
				t.Fatal(err)
			}
			rows := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
			if len(rows) != c.seeds+1 || rows[0] != "seed,outcome_run1,outcome_run2" {
				t.Fatalf("csv:\n%s", b)
			}
			last := c.seeds - 1
			if want := fmt.Sprintf("0x%016x,%s,%s", faultline.DeriveSeed(base, last), tt.run1[last], tt.run2[last]); rows[c.seeds] != want {
				t.Fatalf("last row %q, want %q", rows[c.seeds], want)
			}
		})
	}
	work := t.TempDir()
	c := &config{backend: "parta", corpus: "C7", seeds: 0, reuse: true, work: work, root: work, out: filepath.Join(work, "s.csv")}
	if err := sensitivity(c); err == nil || err == errRequirement {
		t.Fatalf("sensitivity with -seeds 0 = %v, want a usage error", err)
	}
}

// readOutcome takes the first line of the named test with three fields.
func TestReadOutcome(t *testing.T) {
	p := filepath.Join(t.TempDir(), "out")
	text := "TestOther\t0x01\tzzz\nTestMapOrder\tbroken\nTestMapOrder\t0x01\t01234567\nTestMapOrder\t0x01\t76543210\n"
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readOutcome(p, "TestMapOrder"); got != "01234567" {
		t.Fatalf("readOutcome = %q", got)
	}
	if got := readOutcome(filepath.Join(t.TempDir(), "none"), "TestMapOrder"); got != "" {
		t.Fatalf("readOutcome of a missing file = %q", got)
	}
}

// findRoot finds the checkout above the working directory, past the go.mod of a nested module.
func TestFindRoot(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := findRoot()
	if want := filepath.Clean(filepath.Join(wd, "..", "..", "..", "..", "..")); err != nil || root != want {
		t.Fatalf("findRoot = %q, %v; want %q", root, err, want)
	}
	t.Chdir(t.TempDir())
	if root, err := findRoot(); err == nil {
		t.Fatalf("findRoot outside a checkout = %q, want an error", root)
	}
}

// EXB-060: a run sees only PATH, HOME, TMPDIR, GOCACHE, GOTOOLCHAIN and FAULTLINE_WASM_* from
// exbmeasure's environment.
func TestBaseEnvIsFixed(t *testing.T) {
	for _, kv := range [][2]string{
		{"PATH", "/p"}, {"HOME", "/h"}, {"TMPDIR", "/t"}, {"GOCACHE", "/c"}, {"GOTOOLCHAIN", "go1.27.1"},
		{"FAULTLINE_WASM_TEST", "1"}, {"FAULTLINE_CHECK_DETERMINISM", "1"}, {"FAULTLINE_SEED", "0x01"},
		{"GOMAXPROCS", "3"}, {"GOFLAGS", "-race"}, {"GORACE", "halt_on_error=1"},
	} {
		t.Setenv(kv[0], kv[1])
	}
	env := baseEnv()
	want := []string{"PATH=/p", "HOME=/h", "TMPDIR=/t", "GOCACHE=/c", "GOTOOLCHAIN=go1.27.1"}
	if !slices.Equal(env[:len(want)], want) {
		t.Fatalf("baseEnv = %q, want it to start with %q", env, want)
	}
	if !slices.Contains(env, "FAULTLINE_WASM_TEST=1") {
		t.Fatalf("baseEnv = %q, want FAULTLINE_WASM_TEST=1", env)
	}
	for _, kv := range env[len(want):] {
		if !strings.HasPrefix(kv, "FAULTLINE_WASM_") {
			t.Fatalf("baseEnv passes %q", kv)
		}
	}
}

// TestHelperProcess stands in for a corpus test binary in the run tests below; it does nothing
// unless EXBMEASURE_HELPER selects a mode.
func TestHelperProcess(t *testing.T) {
	switch os.Getenv("EXBMEASURE_HELPER") {
	case "":
		t.Skip("runs only as the child of a run test")
	case "record": // writes its arguments, directory and environment to EXB_OUT, then exits 3
		dir, _ := os.Getwd()
		env := os.Environ()
		slices.Sort(env)
		text := "args\t" + strings.Join(os.Args[1:], " ") + "\ndir\t" + dir + "\n"
		for _, kv := range env {
			text += "env\t" + kv + "\n"
		}
		if err := os.WriteFile(os.Getenv("EXB_OUT"), []byte(text), 0o600); err != nil { //nolint:gosec // G703: the parent test names the file
			t.Fatal(err)
		}
		fmt.Println("helper output")
		os.Exit(3)
	case "result": // appends one passing results line whose trace hash is the seed, as Part A appends
		s := os.Getenv("FAULTLINE_SEED")
		appendText(t, os.Getenv("FAULTLINE_RESULTS"), resultLine("TestHelper", s, "pass", s)+"\n")
	case "outcome": // appends TestHelper's EXB_OUT line with the outcome o<seed>, as exbout appends
		s := os.Getenv("FAULTLINE_SEED")
		appendText(t, os.Getenv("EXB_OUT"), "TestHelper\t"+s+"\to"+s+"\n")
	case "kill": // ends by a signal
		p, err := os.FindProcess(os.Getpid())
		if err == nil {
			err = p.Kill()
		}
		t.Fatalf("still running after Kill: %v", err)
	case "linger": // leaves a process holding the run's output open, then exits, or waits with "wait"
		cmd := exec.Command("sleep", "10")
		cmd.Stdout = os.Stdout
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		appendText(t, os.Getenv("EXB_OUT"), "started\n")
		if os.Getenv("EXBMEASURE_LINGER") == "wait" {
			time.Sleep(time.Minute)
		}
	case "sleep":
		time.Sleep(time.Minute)
	case "slow": // a passing run that takes a second
		time.Sleep(time.Second)
	}
}

// appendText appends text to the file at path, creating it.
func appendText(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // G304: the parent test names the file
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// stampBinary writes the stamp that a build of tc's binary bin would leave next to it, so that
// binary uses the file a test put at bin instead of building one.
func stampBinary(t *testing.T, c *config, tc testCase, bin string) {
	t.Helper()
	_, stamp, err := c.build(tc, bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin+".stamp", []byte(stamp), 0o600); err != nil {
		t.Fatal(err)
	}
}

// helperCase is a test case whose binary is this test binary in the given mode; with mkdir
// unset its package directory does not exist, so starting it fails.
func helperCase(t *testing.T, c *config, name, module, mode string, mkdir bool) testCase {
	t.Helper()
	tc := testCase{Name: name, Module: module, Pkg: "p", Run: "^TestHelperProcess$", Env: []string{"EXBMEASURE_HELPER=" + mode}}
	if err := os.MkdirAll(filepath.Join(c.root, module), 0o750); err != nil {
		t.Fatal(err)
	}
	if mkdir {
		if err := os.MkdirAll(filepath.Join(c.root, module, "p"), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(c.work, "bin", c.backend, module+"_p.test")
	if err := os.MkdirAll(filepath.Dir(bin), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(bin); err != nil {
		if err := os.Symlink(exe, bin); err != nil {
			t.Fatal(err)
		}
	}
	stampBinary(t, c, tc, bin)
	if got, err := c.binary(tc); err != nil || got != bin {
		t.Fatalf("binary = %q, %v; want %q", got, err, bin)
	}
	return tc
}

// EXB-060: every run is a fresh instance of the binary with the fixed environment, the run's
// variables, -test.run '^<Test>$' -test.count=1 and the case's extra flags, in the package
// directory. A run that writes no results leaves an empty file, and the log holds the exit code.
func TestEnsureRunEnvironment(t *testing.T) {
	work := t.TempDir()
	c := &config{backend: "parta", corpus: "C2", work: work, root: work}
	tc := helperCase(t, c, "TestHelper", "m", "record", true)
	tc.Args = []string{"-test.v"}
	s := faultline.DeriveSeed(base, 0)
	if err := c.ensureRun(tc, s, 2); err != nil {
		t.Fatal(err)
	}
	res, out := c.rawPath(tc, s, 2, "jsonl"), c.rawPath(tc, s, 2, "out")
	if res != filepath.Join(work, "parta", "C2", "TestHelper", "0xafb28507f36457eb-run2.jsonl") {
		t.Fatalf("results path %s", res)
	}
	if b, err := os.ReadFile(res); err != nil || len(b) != 0 {
		t.Fatalf("results file %q, %v; want an empty file", b, err)
	}
	log, err := os.ReadFile(c.rawPath(tc, s, 2, "log"))
	if err != nil || !strings.HasPrefix(string(log), "exit 3\n") || !strings.Contains(string(log), "helper output\n") {
		t.Fatalf("log %q, %v", log, err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var args, dir string
	var env []string
	for _, l := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		k, v, _ := strings.Cut(l, "\t")
		switch k {
		case "args":
			args = v
		case "dir":
			dir = v
		case "env":
			env = append(env, v)
		}
	}
	if args != "-test.run ^TestHelperProcess$ -test.count=1 -test.v" {
		t.Fatalf("args %q", args)
	}
	wantDir, _ := filepath.EvalSymlinks(filepath.Join(work, "m", "p"))
	if gotDir, _ := filepath.EvalSymlinks(dir); gotDir != wantDir {
		t.Fatalf("dir %q, want %q", dir, wantDir)
	}
	want := append(baseEnv(), "EXBMEASURE_HELPER=record", "FAULTLINE_SEED=0xafb28507f36457eb",
		"FAULTLINE_RESULTS="+res, "EXB_OUT="+out, "FAULTLINE_TRACE=full", "FAULTLINE_ARTIFACTS=off")
	slices.Sort(want)
	if !slices.Equal(env, want) {
		t.Fatalf("environment\n%q\nwant\n%q", env, want)
	}
}

// EXB §11: a run that exceeds the per-run timeout is a failed run (exit code -1), not a tool error.
func TestRunTimeoutIsAFailedRun(t *testing.T) {
	old := runTimeout
	runTimeout = 500 * time.Millisecond
	t.Cleanup(func() { runTimeout = old })
	work := t.TempDir()
	c := &config{backend: "parta", corpus: "C2", work: work, root: work}
	tc := helperCase(t, c, "TestHelper", "m", "sleep", true)
	s := faultline.DeriveSeed(base, 0)
	start := time.Now()
	if err := c.ensureRun(tc, s, 1); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Fatalf("the run took %v", d)
	}
	log, err := os.ReadFile(c.rawPath(tc, s, 1, "log"))
	if err != nil || !strings.HasPrefix(string(log), "exit -1 (timeout after 500ms)\n") {
		t.Fatalf("log %q, %v", log, err)
	}
	if b, err := os.ReadFile(c.rawPath(tc, s, 1, "jsonl")); err != nil || len(b) != 0 {
		t.Fatalf("results file %q, %v; want an empty file", b, err)
	}
}

// A run killed by a signal is a failed run too, and its log names the signal, not a timeout.
func TestRunKilledBySignal(t *testing.T) {
	work := t.TempDir()
	c := &config{backend: "parta", corpus: "C2", work: work, root: work}
	tc := helperCase(t, c, "TestHelper", "m", "kill", true)
	s := faultline.DeriveSeed(base, 0)
	if err := c.ensureRun(tc, s, 1); err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(c.rawPath(tc, s, 1, "log"))
	if err != nil || !strings.HasPrefix(string(log), "exit -1 (signal: killed)\n") {
		t.Fatalf("log %q, %v", log, err)
	}
}

// A process that a run leaves behind with the run's output open delays exbmeasure by at most
// waitDelay: after a timeout the run is a failed run, and after an exit it is a tool error.
func TestRunOutputHeldOpen(t *testing.T) {
	oldTimeout, oldDelay := runTimeout, waitDelay
	waitDelay = 200 * time.Millisecond
	t.Cleanup(func() { runTimeout, waitDelay = oldTimeout, oldDelay })
	for _, tt := range []struct {
		name, linger string
		timeout      time.Duration
		wantErr      bool
	}{
		{"killed at the timeout", "wait", 2 * time.Second, false},
		{"exited", "exit", time.Minute, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runTimeout = tt.timeout
			work := t.TempDir()
			c := &config{backend: "parta", corpus: "C2", work: work, root: work}
			tc := helperCase(t, c, "TestHelper", "m", "linger", true)
			tc.Env = append(tc.Env, "EXBMEASURE_LINGER="+tt.linger)
			s := faultline.DeriveSeed(base, 0)
			start := time.Now()
			err := c.ensureRun(tc, s, 1)
			if d := time.Since(start); d > 6*time.Second {
				t.Fatalf("the run took %v; the left process holds the output for 10s", d)
			}
			if b, rerr := os.ReadFile(c.rawPath(tc, s, 1, "out")); rerr != nil || string(b) != "started\n" {
				t.Fatalf("EXB_OUT %q, %v; want the helper to have started its process", b, rerr)
			}
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "kept its output open") {
					t.Fatalf("ensureRun = %v, want a tool error", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			log, err := os.ReadFile(c.rawPath(tc, s, 1, "log"))
			if err != nil || !strings.HasPrefix(string(log), "exit -1 (timeout after 2s)\n") {
				t.Fatalf("log %q, %v", log, err)
			}
		})
	}
}

// fakeTools puts stand-ins for go and git first on PATH and returns the build log. The go
// stand-in prints EXB_FAKE_GOVERSION and GOFLAGS for go env; for go test it appends its GOWORK,
// GOOS, GOARCH and arguments to the log and writes "fresh" to the -o file. The git stand-in
// reports the -C directory as the top of a checkout at commit EXB_FAKE_COMMIT whose status is
// EXB_FAKE_STATUS.
func fakeTools(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "builds.log")
	tools := map[string]string{
		"go": "#!/bin/sh\ncase \"$1\" in\nenv) echo \"$EXB_FAKE_GOVERSION\"; echo \"$GOFLAGS\" ;;\n" +
			"test) echo \"GOWORK=$GOWORK GOOS=$GOOS GOARCH=$GOARCH $*\" >> \"$EXB_FAKE_LOG\"\n" +
			"  while [ $# -gt 0 ]; do if [ \"$1\" = -o ]; then echo fresh > \"$2\"; fi; shift; done ;;\n" +
			"*) exit 2 ;;\nesac\n",
		"git": "#!/bin/sh\ncase \"$3\" in\nrev-parse) echo \"$2\"; echo \"$EXB_FAKE_COMMIT\" ;;\n" +
			"status) printf '%s' \"$EXB_FAKE_STATUS\" ;;\n*) exit 2 ;;\nesac\n",
	}
	for name, script := range tools {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil { //nolint:gosec // G306: the stand-ins must be executable
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, kv := range [][2]string{
		{"EXB_FAKE_LOG", log}, {"EXB_FAKE_COMMIT", "c1"}, {"EXB_FAKE_GOVERSION", "go1.27.3"},
		{"EXB_FAKE_STATUS", ""}, {"GOFLAGS", "-trimpath"}, {"GOWORK", filepath.Join(dir, "go.work")},
		{"GOOS", ""}, {"GOARCH", ""},
	} {
		t.Setenv(kv[0], kv[1])
	}
	return log
}

// forget drops bin from the builds this process has checked, as when a new command starts.
func forget(bin string) {
	builds.Lock()
	defer builds.Unlock()
	delete(builds.done, bin)
}

// EXB-060's build of each backend, outside any go.work, and the stamp it leaves next to the
// binary: the checkout's commit, the Go version and GOFLAGS, the backend, the module directory,
// the build command, and for toolexec the wrapper's SHA-256.
func TestBinaryStamp(t *testing.T) {
	wrapper := filepath.Join(t.TempDir(), "faultline-toolexec")
	if err := os.WriteFile(wrapper, []byte("v1"), 0o700); err != nil { //nolint:gosec // G306: the wrapper must be executable
		t.Fatal(err)
	}
	for _, tt := range []struct {
		backend, ext, env, flags, extra string
	}{
		{"parta", ".test", "GOOS= GOARCH=", "", ""},
		{"wasm", ".wasm", "GOOS=wasip1 GOARCH=wasm", "GOOS=wasip1 GOARCH=wasm ", ""},
		{"toolexec", ".test", "GOOS= GOARCH=", "", fmt.Sprintf("toolexec %x\n", sha256.Sum256([]byte("v1")))},
	} {
		t.Run(tt.backend, func(t *testing.T) {
			log := fakeTools(t)
			work, root := t.TempDir(), t.TempDir()
			c := &config{backend: tt.backend, work: work, root: root, toolexec: wrapper}
			tc := testCase{Name: "TestX", Module: "m", Pkg: "./p"}
			if err := os.MkdirAll(filepath.Join(root, "m"), 0o750); err != nil {
				t.Fatal(err)
			}
			bin, err := c.binary(tc)
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(work, "bin", tt.backend, "m___p"+tt.ext); bin != want {
				t.Fatalf("binary = %s, want %s", bin, want)
			}
			args := "test -c -o " + bin + " ./p"
			if tt.backend == "toolexec" {
				args = "test -c -toolexec=" + wrapper + " -o " + bin + " ./p"
			}
			if b, err := os.ReadFile(log); err != nil || string(b) != "GOWORK=off "+tt.env+" "+args+"\n" {
				t.Fatalf("build log %q, %v", b, err)
			}
			want := "commit c1\ngo go1.27.3\ngoflags -trimpath\nbackend " + tt.backend + "\ndir " +
				filepath.Join(root, "m") + "\nbuild " + tt.flags + "go " + args + "\n" + tt.extra
			if b, err := os.ReadFile(bin + ".stamp"); err != nil || string(b) != want {
				t.Fatalf("stamp\n%s\nwant\n%s", b, want)
			}
		})
	}
}

// A binary is rebuilt when its stamp is missing or differs from the build's, or when the
// checkout has uncommitted changes, and is checked once per command.
func TestBinaryRebuildsWhenItsStampChanges(t *testing.T) {
	log := fakeTools(t)
	work, root := t.TempDir(), t.TempDir()
	wrapper := filepath.Join(t.TempDir(), "faultline-toolexec")
	if err := os.WriteFile(wrapper, []byte("v1"), 0o700); err != nil { //nolint:gosec // G306: the wrapper must be executable
		t.Fatal(err)
	}
	c := &config{backend: "toolexec", work: work, root: root, toolexec: wrapper}
	tc := testCase{Name: "TestX", Module: "m", Pkg: "./p"}
	if err := os.MkdirAll(filepath.Join(root, "m"), 0o750); err != nil {
		t.Fatal(err)
	}
	bin, err := c.binary(tc)
	if err != nil {
		t.Fatal(err)
	}
	set := func(k, v string) func() { return func() { t.Setenv(k, v) } }
	for _, step := range []struct {
		name       string
		change     func()
		newCommand bool
		rebuild    bool
	}{
		{"same stamp", nil, true, false},
		{"new commit in the same command", set("EXB_FAKE_COMMIT", "c2"), false, false},
		{"new commit", nil, true, true},
		{"new Go version", set("EXB_FAKE_GOVERSION", "go1.27.4"), true, true},
		{"new GOFLAGS", set("GOFLAGS", "-race"), true, true},
		{"new wrapper", func() { _ = os.WriteFile(wrapper, []byte("v2"), 0o700) }, true, true}, //nolint:gosec // G306: the wrapper must be executable
		{"stamp missing", func() { _ = os.Remove(bin + ".stamp") }, true, true},
		{"binary missing", func() { _ = os.Remove(bin) }, true, true},
		{"same stamp again", nil, true, false},
		{"uncommitted changes", set("EXB_FAKE_STATUS", " M run.go\n"), true, true},
		{"uncommitted changes, next command", nil, true, true},
		{"uncommitted changes, same command", nil, false, false},
	} {
		if _, err := os.Stat(bin); err == nil {
			if err := os.WriteFile(bin, []byte("old\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if step.change != nil {
			step.change()
		}
		if step.newCommand {
			forget(bin)
		}
		before, _ := os.ReadFile(log)
		if got, err := c.binary(tc); err != nil || got != bin {
			t.Fatalf("%s: binary = %q, %v", step.name, got, err)
		}
		after, _ := os.ReadFile(log)
		content, _ := os.ReadFile(bin)
		if rebuilt := len(after) > len(before); rebuilt != step.rebuild || (string(content) == "fresh\n") != step.rebuild {
			t.Fatalf("%s: rebuilt %v (binary %q), want %v", step.name, rebuilt, content, step.rebuild)
		}
	}
}

// The commit is git's only for the top of a checkout with a commit: a directory inside one,
// outside any, or a repository without a commit is "unknown".
func TestCheckoutCommit(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dirs := []string{t.TempDir(), wd}
	if _, err := exec.LookPath("git"); err == nil {
		empty := t.TempDir()
		if out, err := exec.Command("git", "init", "-q", empty).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, out)
		}
		dirs = append(dirs, empty)
	}
	for _, dir := range dirs {
		if got := checkoutCommit(dir); got != "unknown" {
			t.Fatalf("checkoutCommit(%s) = %q, want unknown", dir, got)
		}
	}
}

// readResults reports a line longer than it reads instead of ending the file there.
func TestReadResultsLongLine(t *testing.T) {
	old := maxResultsLine
	maxResultsLine = 64
	t.Cleanup(func() { maxResultsLine = old })
	p := filepath.Join(t.TempDir(), "r.jsonl")
	if err := os.WriteFile(p, []byte(resultLine("TestMapOrder", "0x01", "pass", "0x02")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if lines, err := readResults(p); !errors.Is(err, bufio.ErrTooLong) || !strings.Contains(err.Error(), p) {
		t.Fatalf("readResults = %v, %v; want bufio.ErrTooLong naming the file", lines, err)
	}
}

// ensureAll returns a run's tool error only after the runs it started have ended, so no instance
// outlives exbmeasure.
func TestEnsureAllWaitsForStartedRuns(t *testing.T) {
	work := t.TempDir()
	c := &config{backend: "parta", corpus: "C2", work: work, root: work, parallel: 2}
	broken := helperCase(t, c, "TestBroken", "gone", "result", false)
	slow := helperCase(t, c, "TestSlow", "m", "slow", true)
	s := faultline.DeriveSeed(base, 0)
	err := c.ensureAll([]job{{slow, s, 1}, {broken, s, 1}})
	if err == nil {
		t.Fatal("ensureAll = nil, want the error of the run that could not start")
	}
	if _, err := os.Stat(c.rawPath(slow, s, 1, "log")); err != nil {
		t.Fatalf("the slow run had not ended when ensureAll returned: %v", err)
	}
}

// After a tool error ensureAll starts no more runs: with one worker, the job after the run that
// could not start never runs.
func TestEnsureAllStopsAfterAToolError(t *testing.T) {
	work := t.TempDir()
	c := &config{backend: "parta", corpus: "C2", work: work, root: work, parallel: 1}
	broken := helperCase(t, c, "TestBroken", "gone", "result", false)
	slow := helperCase(t, c, "TestSlow", "m", "slow", true)
	s := faultline.DeriveSeed(base, 0)
	if err := c.ensureAll([]job{{broken, s, 1}, {slow, s, 1}}); err == nil {
		t.Fatal("ensureAll = nil, want the error of the run that could not start")
	}
	if _, err := os.Stat(c.rawPath(slow, s, 1, "log")); !os.IsNotExist(err) {
		t.Fatalf("the job after the tool error ran (log: %v)", err)
	}
}

// EXB-062 and EXB-068 without -reuse: sensitivity runs every seed twice, each a fresh instance
// with its seed, and removes an EXB_OUT file left by an earlier measurement first (the corpus
// appends to it).
func TestSensitivityRunsEverySeedTwice(t *testing.T) {
	work := t.TempDir()
	c := &config{backend: "parta", corpus: "C4", seeds: 4, parallel: 2, work: work, root: work,
		out: filepath.Join(work, "sensitivity-parta-C4.csv")}
	saved := corpora["C4"]
	t.Cleanup(func() { corpora["C4"] = saved })
	tc := helperCase(t, c, "TestHelper", "m", "outcome", true)
	tc.Outcome = true
	corpora["C4"] = []testCase{tc}
	s0 := faultline.DeriveSeed(base, 0)
	stale := c.rawPath(tc, s0, 1, "out")
	if err := os.MkdirAll(filepath.Dir(stale), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte(fmt.Sprintf("TestHelper\t0x%016x\tstale\n", s0)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sensitivity(c); err != nil {
		t.Fatalf("sensitivity = %v, want nil (4 distinct outcomes, every seed stable)", err)
	}
	want := "seed,outcome_run1,outcome_run2\n"
	for i := range c.seeds {
		s := faultline.DeriveSeed(base, i)
		want += fmt.Sprintf("0x%016x,o0x%016x,o0x%016x\n", s, s, s)
		for r := 1; r <= 2; r++ {
			if _, err := os.Stat(c.rawPath(tc, s, r, "log")); err != nil {
				t.Fatalf("seed 0x%016x run %d did not run: %v", s, r, err)
			}
			if b, err := os.ReadFile(c.rawPath(tc, s, r, "out")); err != nil || strings.Count(string(b), "\n") != 1 {
				t.Fatalf("seed 0x%016x run %d: EXB_OUT %q, %v; want one line", s, r, b, err)
			}
		}
	}
	if b, err := os.ReadFile(c.out); err != nil || string(b) != want {
		t.Fatalf("csv:\n%s\nwant:\n%s", b, want)
	}
}

// EXB-061 end to end: exactness runs every seed R times, each a fresh instance with its seed, and
// without -reuse it replaces results already in the work directory.
func TestExactnessRunsEverySeed(t *testing.T) {
	work := t.TempDir()
	c := &config{backend: "parta", corpus: "C2", seeds: 2, runs: 2, parallel: 2, work: work, root: work,
		out: filepath.Join(work, "exactness-parta-C2.csv")}
	saved := corpora["C2"]
	t.Cleanup(func() { corpora["C2"] = saved })
	tc := helperCase(t, c, "TestHelper", "m", "result", true)
	corpora["C2"] = []testCase{tc}
	s0, s1 := faultline.DeriveSeed(base, 0), faultline.DeriveSeed(base, 1)
	stale := resultLine("TestHelper", fmt.Sprintf("0x%016x", s1), "pass", "0x00000000000000aa")
	writeLines(t, c, tc, s1, 2, stale)
	if err := exactness(c); err != nil {
		t.Fatalf("exactness = %v, want nil", err)
	}
	want := fmt.Sprintf("test,seed,run1_hash,run2_hash,run1_status,run2_status,exact\n"+
		"TestHelper,0x%016x,0x%016x,0x%016x,pass,pass,true\nTestHelper,0x%016x,0x%016x,0x%016x,pass,pass,true\n",
		s0, s0, s0, s1, s1, s1)
	if b, err := os.ReadFile(c.out); err != nil || string(b) != want {
		t.Fatalf("csv:\n%s\nwant:\n%s", b, want)
	}
	for _, s := range []uint64{s0, s1} {
		for r := 1; r <= 2; r++ {
			if _, err := os.Stat(c.rawPath(tc, s, r, "log")); err != nil {
				t.Fatalf("seed 0x%016x run %d: %v", s, r, err)
			}
			if b, err := os.ReadFile(c.rawPath(tc, s, r, "jsonl")); err != nil || strings.Count(string(b), "\n") != 1 {
				t.Fatalf("seed 0x%016x run %d: results %q, %v; want one line", s, r, b, err)
			}
		}
	}
	// With -reuse the stale line stays and the seed is not exact.
	writeLines(t, c, tc, s1, 2, stale)
	c.reuse = true
	if err := exactness(c); err != errRequirement {
		t.Fatalf("exactness -reuse = %v, want errRequirement", err)
	}
}
