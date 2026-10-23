// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
)

// helperRun is TestHelperProcess's "run" mode. It appends a line with its id, arguments and
// FAULTLINE_* environment to EXBMEASURE_HELPER_FILE, sleeps EXBMEASURE_HELPER_NAP milliseconds,
// writes one results line per status of EXBMEASURE_HELPER_STATUSES to FAULTLINE_RESULTS, appends
// a passing results line for each of the first EXBMEASURE_HELPER_SEEDS seeds it was given, then
// EXBMEASURE_HELPER_RESULTS as it is, prints EXBMEASURE_HELPER_PRINT and exits with
// EXBMEASURE_HELPER_EXIT (each optional). With
// EXBMEASURE_HELPER_CALLS set to a file that counts the calls, NAP, SEEDS and EXIT may be
// comma-separated lists: call i takes value i, and the last value repeats.
func helperRun(t *testing.T) {
	call := 0
	if f := os.Getenv("EXBMEASURE_HELPER_CALLS"); f != "" {
		b, _ := os.ReadFile(f) //nolint:gosec // G304: the parent test names the file
		call = strings.Count(string(b), "\n")
		appendLine(t, f, "call")
	}
	value := func(key string) string {
		vs := strings.Split(os.Getenv(key), ",")
		return vs[min(call, len(vs)-1)]
	}
	if f := os.Getenv("EXBMEASURE_HELPER_FILE"); f != "" {
		var env []string
		for _, kv := range os.Environ() {
			if strings.HasPrefix(kv, "FAULTLINE_") || strings.HasPrefix(kv, "EXB_OUT=") {
				env = append(env, kv)
			}
		}
		slices.Sort(env)
		appendLine(t, f, "run "+os.Getenv("EXBMEASURE_HELPER_ID")+"\t"+strings.Join(os.Args[1:], " ")+"\t"+strings.Join(env, " "))
	}
	if ms, err := strconv.Atoi(value("EXBMEASURE_HELPER_NAP")); err == nil {
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
	if st := os.Getenv("EXBMEASURE_HELPER_STATUSES"); st != "" {
		var b strings.Builder
		for i, s := range strings.Split(st, ",") {
			b.WriteString(resultLine("TestHelper", fmt.Sprintf("0x%016x", i), s, "0x0000000000000001") + "\n")
		}
		if err := os.WriteFile(os.Getenv("FAULTLINE_RESULTS"), []byte(b.String()), 0o600); err != nil { //nolint:gosec // G703: the parent test names the file
			t.Fatal(err)
		}
	}
	if n, err := strconv.Atoi(value("EXBMEASURE_HELPER_SEEDS")); err == nil {
		var b strings.Builder
		for _, s := range helperSeeds(t)[:n] {
			b.WriteString(resultLine("TestHelper", s, "pass", s) + "\n")
		}
		appendText(t, os.Getenv("FAULTLINE_RESULTS"), b.String())
	}
	if text := os.Getenv("EXBMEASURE_HELPER_RESULTS"); text != "" {
		appendText(t, os.Getenv("FAULTLINE_RESULTS"), text)
	}
	fmt.Print(os.Getenv("EXBMEASURE_HELPER_PRINT"))
	if code, err := strconv.Atoi(value("EXBMEASURE_HELPER_EXIT")); err == nil {
		os.Exit(code)
	}
}

// helperSeeds returns the seeds of a slowdown process: FAULTLINE_SEED_LIST (parta and toolexec),
// or seeds 0 to FAULTLINE_SEEDS-1 of FAULTLINE_BASE_SEED, the plan faultline-wasm runs.
func helperSeeds(t *testing.T) []string {
	if list := os.Getenv("FAULTLINE_SEED_LIST"); list != "" {
		return strings.Split(list, ",")
	}
	b, err1 := strconv.ParseUint(strings.TrimPrefix(os.Getenv("FAULTLINE_BASE_SEED"), "0x"), 16, 64)
	n, err2 := strconv.Atoi(os.Getenv("FAULTLINE_SEEDS"))
	if err1 != nil || err2 != nil {
		t.Fatalf("no seeds in the environment: %v, %v", err1, err2)
	}
	var seeds []string
	for i := range n {
		seeds = append(seeds, fmt.Sprintf("0x%016x", faultline.DeriveSeed(b, i)))
	}
	return seeds
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // G304: the parent test names the file
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// measureCase is a test case named name whose binary is this test binary in mode "run", with the
// helper variables in env and EXBMEASURE_HELPER_ID=<name>. For the wasm backend the binary is an
// empty placeholder module, which the wasmWrapper runner ignores. Every case shares module m,
// package p, so every case has the same binary. GORACE=atexit_sleep_ms=0 keeps a -race helper
// that exits 0 from waiting a second before it ends, so the tests can time short naps.
func measureCase(t *testing.T, c *config, name string, env ...string) testCase {
	t.Helper()
	tc := testCase{Name: name, Module: "m", Pkg: "p", Run: "^TestHelperProcess$", Gated: true,
		Env: append([]string{"EXBMEASURE_HELPER=run", "EXBMEASURE_HELPER_ID=" + name, "GORACE=atexit_sleep_ms=0"}, env...)}
	if err := os.MkdirAll(filepath.Join(c.root, "m", "p"), 0o750); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(c.work, "bin", c.backend)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "m_p.test")
	if c.backend == "wasm" {
		bin = filepath.Join(dir, "m_p.wasm")
		if err := os.WriteFile(bin, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	} else if _, err := os.Lstat(bin); err != nil {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
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

// wasmWrapper writes a stand-in for faultline-wasm: it logs "wasm <arguments>" to
// EXBMEASURE_HELPER_FILE, drops the module argument and runs this test binary with the rest.
func wasmWrapper(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "faultline-wasm")
	script := "#!/bin/sh\necho \"wasm $*\" >> \"$EXBMEASURE_HELPER_FILE\"\nshift\nexec '" + exe + "' \"$@\"\n"
	if err := os.WriteFile(p, []byte(script), 0o700); err != nil { //nolint:gosec // G306: the runner must be executable
		t.Fatal(err)
	}
	return p
}

// setCorpus replaces corpora[name] for the test.
func setCorpus(t *testing.T, name string, tcs ...testCase) {
	t.Helper()
	saved, ok := corpora[name]
	t.Cleanup(func() {
		if ok {
			corpora[name] = saved
		} else {
			delete(corpora, name)
		}
	})
	corpora[name] = tcs
}

func readLog(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// slowdownCSV reads slowdown.csv as records, header included.
func slowdownCSV(t *testing.T, path string) [][]string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(strings.NewReader(string(b))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil { //nolint:gosec // G703: the test names the file
		t.Fatal(err)
	}
}

func slowdownConfig(t *testing.T, backend, corpus string) *config {
	t.Helper()
	work := t.TempDir()
	return &config{backend: backend, corpus: corpus, seeds: 100, reps: 3, parallel: 2, work: work, root: work,
		out: filepath.Join(t.TempDir(), "slowdown.csv")}
}

// seedList100 is the FAULTLINE_SEED_LIST of EXB-064: seeds 0 to 99 of the base seed.
func seedList100(t *testing.T) string {
	t.Helper()
	var list []string
	for i := range 100 {
		list = append(list, fmt.Sprintf("0x%016x", faultline.DeriveSeed(base, i)))
	}
	if !strings.HasPrefix(strings.Join(list, ","), "0xafb28507f36457eb,0x75a9d0b00437271e,0x43ecfcf56902d0b3,") {
		t.Fatalf("seed list starts %v", list[:3])
	}
	return strings.Join(list, ",")
}

// partaRows are slowdown.csv's header and three parta rows of an hour each for corpus.
func partaRows(corpus string) string {
	return "backend,corpus,rep,wall_ns\nparta," + corpus + ",1,3600000000000\nparta," + corpus + ",2,3600000000000\nparta," +
		corpus + ",3,3600000000000\n"
}

// partaRefs writes the parta reference of corpus into c's work directory as a parta measurement
// leaves it when each named test exited 0 in its three repetitions and wrote no results line
// (the helper writes none unless EXBMEASURE_HELPER_SEEDS is set).
func partaRefs(t *testing.T, c *config, corpus string, tests ...string) {
	t.Helper()
	refs := map[string]slowdownRef{}
	for _, n := range tests {
		refs[n] = slowdownRef{Codes: []int{0, 0, 0}}
	}
	if err := writeRefs((&config{work: c.work, corpus: corpus}).refPath(), refs); err != nil {
		t.Fatal(err)
	}
}

// walltimes returns the wall times of backend's rows in slowdown.csv, in order.
func walltimes(t *testing.T, path, backend string) []time.Duration {
	t.Helper()
	var w []time.Duration
	for _, r := range slowdownCSV(t, path)[1:] {
		if r[0] == backend {
			ns, err := strconv.ParseInt(r[3], 10, 64)
			if err != nil {
				t.Fatalf("row %q: %v", r, err)
			}
			w = append(w, time.Duration(ns))
		}
	}
	return w
}

// EXB-064 for parta (and toolexec, which runs the same way): K = 3 repetitions, each one process
// per corpus test, in corpus order, with FAULTLINE_SEED_LIST of seeds 0 to 99,
// FAULTLINE_TRACE=full, FAULTLINE_ARTIFACTS=off and a results file per run, and nothing else; a
// repetition's wall time is the sum over the corpus tests (C1): TestA's nap counts although TestB
// runs last. parta's measurement leaves the reference the other backends are checked against.
func TestSlowdownRunsSeedListProcesses(t *testing.T) {
	for _, backend := range []string{"parta", "toolexec"} {
		t.Run(backend, func(t *testing.T) {
			c := slowdownConfig(t, backend, "C1")
			log := filepath.Join(t.TempDir(), "runs.log")
			a := measureCase(t, c, "TestA", "EXBMEASURE_HELPER_FILE="+log, "EXBMEASURE_HELPER_NAP=800")
			b := measureCase(t, c, "TestB", "EXBMEASURE_HELPER_FILE="+log)
			setCorpus(t, "C1", a, b)
			if backend == "toolexec" {
				writeFile(t, c.out, partaRows("C1"))
				partaRefs(t, c, "C1", "TestA", "TestB")
			}
			if err := slowdown(c); err != nil {
				t.Fatalf("slowdown = %v, want nil", err)
			}
			dir := filepath.Join(c.work, backend, "slowdown", "C1")
			var want []string
			for rep := 1; rep <= 3; rep++ {
				for _, id := range []string{"A", "B"} {
					env := "FAULTLINE_ARTIFACTS=off FAULTLINE_RESULTS=" + filepath.Join(dir, fmt.Sprintf("Test%s-rep%d.jsonl", id, rep)) +
						" FAULTLINE_SEED_LIST=" + seedList100(t) + " FAULTLINE_TRACE=full"
					want = append(want, "run Test"+id+"\t-test.run ^TestHelperProcess$ -test.count=1\t"+env)
				}
			}
			if got := readLog(t, log); !slices.Equal(got, want) {
				t.Fatalf("runs\n%q\nwant\n%q", got, want)
			}
			recs := slowdownCSV(t, c.out)
			var mine [][]string
			for _, r := range recs[1:] {
				if r[0] == backend {
					mine = append(mine, r)
				}
			}
			if !slices.Equal(recs[0], []string{"backend", "corpus", "rep", "wall_ns"}) || len(mine) != 3 {
				t.Fatalf("slowdown.csv %q", recs)
			}
			for i, r := range mine {
				ns, err := strconv.ParseInt(r[3], 10, 64)
				if r[1] != "C1" || r[2] != strconv.Itoa(i+1) || err != nil || time.Duration(ns) < 800*time.Millisecond {
					t.Fatalf("row %q: want rep %d of C1 with the wall time of both tests (>= 800ms)", r, i+1)
				}
			}
			if backend == "parta" {
				refs, err := readRefs(c.refPath(), corpora["C1"])
				want := map[string]slowdownRef{"TestA": {Codes: []int{0, 0, 0}}, "TestB": {Codes: []int{0, 0, 0}}}
				if err != nil || len(refs) != 2 || !slices.Equal(refs["TestA"].Codes, want["TestA"].Codes) ||
					!slices.Equal(refs["TestB"].Codes, want["TestB"].Codes) || len(refs["TestA"].SeedRuns) != 0 {
					t.Fatalf("parta reference %v, %v; want %v", refs, err, want)
				}
			}
		})
	}
}

// EXB-064 for wasm: before timing, one untimed run of an empty test per test binary warms the
// compilation cache and a second one measures the per-seed instance overhead (EXB §9); every
// timed run is faultline-wasm in plan/run mode with FAULTLINE_BASE_SEED=base and
// FAULTLINE_SEEDS=100 (no FAULTLINE_SEED, no FAULTLINE_SEED_LIST).
func TestSlowdownWasmRunsPlanRunMode(t *testing.T) {
	c := slowdownConfig(t, "wasm", "C2")
	c.wasmRunner = wasmWrapper(t)
	log := filepath.Join(t.TempDir(), "runs.log")
	a := measureCase(t, c, "TestA", "EXBMEASURE_HELPER_FILE="+log)
	b := measureCase(t, c, "TestB", "EXBMEASURE_HELPER_FILE="+log)
	setCorpus(t, "C2", a, b)
	writeFile(t, c.out, partaRows("C2"))
	partaRefs(t, c, "C2", "TestA", "TestB")
	if err := slowdown(c); err != nil {
		t.Fatalf("slowdown = %v, want nil", err)
	}
	bin := filepath.Join(c.work, "bin", "wasm", "m_p.wasm")
	want := []string{"wasm " + bin + " -test.run ^$ -test.count=1", "wasm " + bin + " -test.run ^$ -test.count=1"}
	for rep := 1; rep <= 3; rep++ {
		for _, id := range []string{"A", "B"} {
			res := filepath.Join(c.work, "wasm", "slowdown", "C2", fmt.Sprintf("Test%s-rep%d.jsonl", id, rep))
			env := "FAULTLINE_ARTIFACTS=off FAULTLINE_BASE_SEED=0x0e0b5eed00000001 FAULTLINE_RESULTS=" + res + " FAULTLINE_SEEDS=100 FAULTLINE_TRACE=full"
			want = append(want, "wasm "+bin+" -test.run ^TestHelperProcess$ -test.count=1",
				"run Test"+id+"\t-test.run ^TestHelperProcess$ -test.count=1\t"+env)
		}
	}
	if got := readLog(t, log); !slices.Equal(got, want) {
		t.Fatalf("runs\n%q\nwant\n%q", got, want)
	}
	recs := slowdownCSV(t, c.out)
	if len(recs) != 7 || !slices.Equal(recs[1], []string{"parta", "C2", "1", "3600000000000"}) || recs[4][0] != "wasm" || recs[6][2] != "3" {
		t.Fatalf("slowdown.csv %q, want the parta rows kept and three wasm rows", recs)
	}
}

// EXB-064's requirement: median(backend) <= 5 x median(parta), compared exactly.
func TestSlowdownRequirement(t *testing.T) {
	for _, tc := range []struct {
		med, ref time.Duration
		ok       bool
	}{
		{5 * time.Second, time.Second, true},
		{5*time.Second + 1, time.Second, false},
		{time.Second, time.Second, true},
		{time.Millisecond, time.Hour, true},
	} {
		if got := withinSlowdown(tc.med, tc.ref); got != tc.ok {
			t.Errorf("withinSlowdown(%v, %v) = %v, want %v", tc.med, tc.ref, got, tc.ok)
		}
	}
	// End to end: against parta rows of 1 ns any real run is more than 5 times slower. The
	// measurement is still written.
	c := slowdownConfig(t, "toolexec", "C3")
	setCorpus(t, "C3", measureCase(t, c, "TestA"))
	writeFile(t, c.out, "backend,corpus,rep,wall_ns\nparta,C3,1,1\nparta,C3,2,1\nparta,C3,3,1\n")
	partaRefs(t, c, "C3", "TestA")
	if err := slowdown(c); !errors.Is(err, errRequirement) {
		t.Fatalf("slowdown = %v, want errRequirement", err)
	}
	if recs := slowdownCSV(t, c.out); len(recs) != 7 || recs[6][0] != "toolexec" {
		t.Fatalf("slowdown.csv %q, want the toolexec rows", recs)
	}
}

func TestMedian(t *testing.T) {
	d := []time.Duration{30, 10, 20}
	if got := median(d); got != 20 {
		t.Fatalf("median(30, 10, 20) = %v, want 20", got)
	}
	if !slices.Equal(d, []time.Duration{30, 10, 20}) {
		t.Fatalf("median sorted its argument: %v", d)
	}
	if got := median([]time.Duration{40, 10, 30, 20}); got != 25 {
		t.Fatalf("median(40, 10, 30, 20) = %v, want 25", got)
	}
	if got := median([]time.Duration{7}); got != 7 {
		t.Fatalf("median(7) = %v, want 7", got)
	}
}

// A process that exits 1 is timed like any other (EXB-064 times the process, and C1 tests that
// check results across seeds after Run fail under a seed list they did not choose): its wall time
// counts, its output is kept in the work directory, and parta's reference records the code.
func TestSlowdownTimesFailingRuns(t *testing.T) {
	c := slowdownConfig(t, "parta", "C2")
	setCorpus(t, "C2", measureCase(t, c, "TestA", "EXBMEASURE_HELPER_NAP=300", "EXBMEASURE_HELPER_EXIT=1", "EXBMEASURE_HELPER_PRINT=after Run"))
	if err := slowdown(c); err != nil {
		t.Fatalf("slowdown = %v, want nil", err)
	}
	if w := walltimes(t, c.out, "parta"); len(w) != 3 || slices.Min(w) < 300*time.Millisecond {
		t.Fatalf("parta rows %v, want three with the 300ms of the failing run", w)
	}
	b, err := os.ReadFile(filepath.Join(c.work, "parta", "slowdown", "C2", "TestA-rep2.log"))
	if err != nil || !strings.HasPrefix(string(b), "exit 1\n") || !strings.Contains(string(b), "after Run") {
		t.Fatalf("log %q, %v", b, err)
	}
	refs, err := readRefs(c.refPath(), corpora["C2"])
	if err != nil || !slices.Equal(refs["TestA"].Codes, []int{1, 1, 1}) {
		t.Fatalf("parta reference %v, %v; want codes 1, 1, 1", refs, err)
	}
}

// parta is the reference, so a parta process that did not run its seeds is a tool error that
// leaves slowdown.csv and the reference as they were: one a signal or its limit ended (the run
// limit once per seed, EXB §11), one that exits 2 or more (a panic, a deadlock), repetitions
// whose results lines differ, and a results file that cannot be read.
func TestSlowdownPartaToolErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit time.Duration // runTimeout
		env   []string
		log   string // first line of TestA-rep1.log
	}{
		{"killed", 3 * time.Millisecond, []string{"EXBMEASURE_HELPER_NAP=20000"}, "exit -1 (timeout after 300ms)"},
		{"exit 2", runTimeout, []string{"EXBMEASURE_HELPER_EXIT=2"}, "exit 2"},
		{"repetitions differ", runTimeout, []string{"EXBMEASURE_HELPER_SEEDS=100,50"}, ""},
		{"unreadable results", runTimeout, []string{"EXBMEASURE_HELPER_SEEDS=100", "EXBMEASURE_HELPER_RESULTS=not a results line\n"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := runTimeout
			runTimeout = tc.limit
			t.Cleanup(func() { runTimeout = old })
			c := slowdownConfig(t, "parta", "C2")
			env := append([]string{"EXBMEASURE_HELPER_CALLS=" + filepath.Join(t.TempDir(), "calls")}, tc.env...)
			setCorpus(t, "C2", measureCase(t, c, "TestA", env...))
			before := "backend,corpus,rep,wall_ns\nwasm,C2,1,5\nwasm,C2,2,5\nwasm,C2,3,5\n"
			writeFile(t, c.out, before)
			partaRefs(t, c, "C2", "TestOld")
			ref, err := os.ReadFile(c.refPath())
			if err != nil {
				t.Fatal(err)
			}
			err = slowdown(c)
			if err == nil || errors.Is(err, errRequirement) || !strings.Contains(err.Error(), "TestA") {
				t.Fatalf("slowdown = %v, want a tool error naming TestA", err)
			}
			if b, _ := os.ReadFile(c.out); string(b) != before {
				t.Fatalf("slowdown.csv changed:\n%s", b)
			}
			if b, _ := os.ReadFile(c.refPath()); string(b) != string(ref) {
				t.Fatalf("reference changed:\n%s", b)
			}
			if tc.log != "" {
				if b, err := os.ReadFile(filepath.Join(c.work, "parta", "slowdown", "C2", "TestA-rep1.log")); err != nil ||
					!strings.HasPrefix(string(b), tc.log+"\n") {
					t.Fatalf("log %q, %v; want it to start with %q", b, err, tc.log)
				}
			}
		})
	}
}

// EXB-064 times the same work on every backend: each process of another backend must write the
// seed runs of parta's process of the test and end with exit 0 or 1. A process that aborts at
// once (a guest trap makes faultline-wasm exit 2 and skip the remaining seeds) or stops early
// measured something else: slowdown fails, still writes its rows, and the run's log names the
// difference on its second line. Whether the seeds passed belongs to compat and exactness, so a
// run that exits 1 where parta's exited 0 is timed, and its log keeps the code. A rerun in the
// same -work does not count the earlier run's results lines.
func TestSlowdownComparesWithParta(t *testing.T) {
	for _, tc := range []struct {
		name, backend string
		env           []string
		ok            bool
		head          string // first line of TestA-rep1.log; "" when there is none
	}{
		{"exit 2 at once", "toolexec", []string{"EXBMEASURE_HELPER_EXIT=2"}, false, "exit 2"},
		{"exit 1", "toolexec", []string{"EXBMEASURE_HELPER_SEEDS=100", "EXBMEASURE_HELPER_EXIT=1"}, true, "exit 1"},
		{"37 seeds", "toolexec", []string{"EXBMEASURE_HELPER_SEEDS=37"}, false, "exit 0"},
		{"same work", "toolexec", []string{"EXBMEASURE_HELPER_SEEDS=100"}, true, ""},
		{"wasm exit 2 at once", "wasm", []string{"EXBMEASURE_HELPER_EXIT=2"}, false, "exit 2"},
		{"wasm same work", "wasm", []string{"EXBMEASURE_HELPER_SEEDS=100"}, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := slowdownConfig(t, "parta", "C2")
			setCorpus(t, "C2", measureCase(t, p, "TestA", "EXBMEASURE_HELPER_SEEDS=100"))
			if err := slowdown(p); err != nil {
				t.Fatalf("parta: slowdown = %v", err)
			}
			writeFile(t, p.out, partaRows("C2")) // only the work decides here, not the time
			c := *p
			c.backend = tc.backend
			c.wasmRunner = wasmWrapper(t)
			env := append([]string{"EXBMEASURE_HELPER_FILE=" + filepath.Join(t.TempDir(), "runs.log")}, tc.env...)
			setCorpus(t, "C2", measureCase(t, &c, "TestA", env...))
			for range 2 { // the second measurement finds the first one's files in -work
				err := slowdown(&c)
				if tc.ok && err != nil || !tc.ok && !errors.Is(err, errRequirement) {
					t.Fatalf("slowdown = %v, want ok %v", err, tc.ok)
				}
			}
			if w := walltimes(t, c.out, tc.backend); len(w) != 3 {
				t.Fatalf("%s rows %v, want 3", tc.backend, w)
			}
			b, err := os.ReadFile(filepath.Join(c.work, tc.backend, "slowdown", "C2", "TestA-rep1.log"))
			head, rest, _ := strings.Cut(string(b), "\n")
			switch {
			case tc.head == "" && err == nil:
				t.Fatalf("log %q, want none for a run that exited 0 and repeated parta's work", b)
			case tc.head != "" && (err != nil || head != tc.head || strings.HasPrefix(rest, "differs from parta: ") == tc.ok):
				t.Fatalf("log %q, %v; want first line %q, and a second line starting %q only if the run differs", b, err, tc.head, "differs from parta: ")
			}
		})
	}
}

// slowdownRef.differs names what a backend's run did differently from parta's. Exit 0 and 1 both
// ran the seeds, whatever parta's codes were: pass or fail belongs to compat and exactness.
func TestSlowdownRefDiffers(t *testing.T) {
	runs := []string{"T 0x1", "T 0x2"}
	for _, tc := range []struct {
		codes []int // parta's
		code  int
		runs  []string
		err   error
		want  string
	}{
		{[]int{0, 1, 0}, 0, runs, nil, ""},
		{[]int{0, 1, 0}, 1, runs, nil, ""},
		{[]int{1, 1, 1}, 0, runs, nil, ""},
		{[]int{0, 1, 0}, 2, runs, nil, "exit 2, where parta exited 0 or 1"},
		{[]int{0, 1, 0}, 0, []string{"T 0x1"}, nil, "1 seed runs, where parta ran 2"},
		{[]int{0, 1, 0}, 0, []string{"T 0x1", "T 0x3"}, nil, `seed run "T 0x3", where parta ran "T 0x2"`},
		{[]int{0, 1, 0}, -1, nil, nil, "exit -1, where parta exited 0 or 1; 0 seed runs, where parta ran 2"},
		{[]int{0, 1, 0}, 0, nil, errors.New("bad line"), "results file: bad line"},
	} {
		ref := slowdownRef{Codes: tc.codes, SeedRuns: runs}
		if got := ref.differs(tc.code, tc.runs, tc.err); got != tc.want {
			t.Errorf("differs(%d, %q, %v) against parta's codes %v = %q, want %q", tc.code, tc.runs, tc.err, tc.codes, got, tc.want)
		}
	}
	// parta writes all of TestA's seeds, then TestB's, from one process; wasm writes TestA's and
	// TestB's line of one seed per instance. Sorted, the two orders are the same seed runs.
	lines := []map[string]any{{"test": "TestB", "seed": "0x1"}, {"test": "TestA", "seed": "0x2"}, {"test": "TestA", "seed": "0x1"}}
	if got, want := seedRuns(lines), []string{"TestA 0x1", "TestA 0x2", "TestB 0x1"}; !slices.Equal(got, want) {
		t.Errorf("seedRuns = %q, want %q", got, want)
	}
}

// A backend's repetition gets 5 x median(parta) in all, not the run limit (EXB §11: a timeout is
// a failed run, never a tool error): a run still going when its repetition passes that is
// stopped, the rest of the repetition is skipped, and the repetition is slower than 5x. Two of
// three such repetitions put the median past it, so slowdown fails; one does not, since the
// median of the other two is then exact.
func TestSlowdownStopsAtFiveTimesParta(t *testing.T) {
	old := runTimeout
	runTimeout = time.Millisecond // a backend's slowdown runs never use it
	t.Cleanup(func() { runTimeout = old })
	parta := "backend,corpus,rep,wall_ns\nparta,C2,1,200000000\nparta,C2,2,200000000\nparta,C2,3,200000000\n" // 5x is 1s
	t.Run("every repetition", func(t *testing.T) {
		c := slowdownConfig(t, "toolexec", "C2")
		log := filepath.Join(t.TempDir(), "runs.log")
		setCorpus(t, "C2",
			measureCase(t, c, "TestA", "EXBMEASURE_HELPER_NAP=300"),
			measureCase(t, c, "TestB", "EXBMEASURE_HELPER_NAP=5000"),
			measureCase(t, c, "TestC", "EXBMEASURE_HELPER_FILE="+log))
		writeFile(t, c.out, parta)
		partaRefs(t, c, "C2", "TestA", "TestB", "TestC")
		if err := slowdown(c); !errors.Is(err, errRequirement) {
			t.Fatalf("slowdown = %v, want errRequirement", err)
		}
		if w := walltimes(t, c.out, "toolexec"); len(w) != 3 || slices.Min(w) < time.Second || slices.Max(w) >= 5*time.Second {
			t.Fatalf("toolexec rows %v, want three of at least 1s, each stopped", w)
		}
		for rep := 1; rep <= 3; rep++ {
			b, err := os.ReadFile(filepath.Join(c.work, "toolexec", "slowdown", "C2", fmt.Sprintf("TestB-rep%d.log", rep)))
			head, _, _ := strings.Cut(string(b), "\n")
			after, ok := strings.CutPrefix(head, "exit -1 (timeout after ")
			d, perr := time.ParseDuration(strings.TrimSuffix(after, ")"))
			if err != nil || !ok || perr != nil || d >= time.Second {
				t.Fatalf("TestB rep %d log starts %q (%v): want a timeout of the time left of the 1s", rep, head, err)
			}
		}
		if _, err := os.Stat(log); err == nil {
			t.Fatal("TestC ran in a stopped repetition")
		}
	})
	t.Run("one repetition", func(t *testing.T) {
		c := slowdownConfig(t, "toolexec", "C2")
		calls := filepath.Join(t.TempDir(), "calls")
		setCorpus(t, "C2", measureCase(t, c, "TestA", "EXBMEASURE_HELPER_CALLS="+calls, "EXBMEASURE_HELPER_NAP=5000,100"))
		writeFile(t, c.out, parta)
		partaRefs(t, c, "C2", "TestA")
		if err := slowdown(c); err != nil {
			t.Fatalf("slowdown = %v, want nil", err)
		}
		w := walltimes(t, c.out, "toolexec")
		if len(w) != 3 || w[0] < time.Second || w[0] >= 5*time.Second || w[1] < 100*time.Millisecond || w[1] >= time.Second ||
			w[2] < 100*time.Millisecond || w[2] >= time.Second {
			t.Fatalf("toolexec rows %v, want the first stopped at 1s and two of 100ms to 1s", w)
		}
	})
}

// slowdown refuses, before running anything, what EXB-064 does not measure: another corpus or
// backend, other seeds or repetitions, a backend before parta (rows in -out, reference in
// -work), and an -out file that is not a slowdown.csv.
func TestSlowdownRejects(t *testing.T) {
	rows := "backend,corpus,rep,wall_ns\nparta,C2,1,5\n"
	for _, tc := range []struct {
		name, backend, corpus string
		seeds, reps           int
		csv, want             string
		refs                  []string // tests in a parta reference of C2, if any
	}{
		{"C4", "parta", "C4", 100, 3, "", `slowdown applies to C1, C2 and C3, not "C4"`, nil},
		{"C6", "parta", "C6", 100, 3, "", `not "C6"`, nil},
		{"C7", "parta", "C7", 100, 3, "", `not "C7"`, nil},
		{"backend", "native", "C2", 100, 3, "", `unknown backend "native"`, nil},
		{"seeds", "parta", "C2", 20, 3, "", "-seeds 100 -reps 3", nil},
		{"reps", "parta", "C2", 100, 1, "", "-seeds 100 -reps 3", nil},
		{"no parta rows", "wasm", "C2", 100, 3, "backend,corpus,rep,wall_ns\nparta,C1,1,5\n", "measure parta first", []string{"TestA"}},
		{"no file", "toolexec", "C2", 100, 3, "", "measure parta first", []string{"TestA"}},
		{"no parta reference", "toolexec", "C2", 100, 3, rows, "measure parta first", nil},
		{"reference lacks the test", "wasm", "C2", 100, 3, rows, "parta reference for TestA", []string{"TestOther"}},
		{"header", "parta", "C2", 100, 3, "test,result,seconds\nTestA,pass,1.0\n", "not a slowdown.csv", nil},
		{"short row", "parta", "C2", 100, 3, "backend,corpus,rep,wall_ns\nparta,C2,1\n", "not a slowdown.csv", nil},
		{"bad wall", "parta", "C2", 100, 3, "backend,corpus,rep,wall_ns\nparta,C2,1,x\n", "not a slowdown.csv", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := slowdownConfig(t, tc.backend, tc.corpus)
			c.seeds, c.reps = tc.seeds, tc.reps
			log := filepath.Join(t.TempDir(), "runs.log")
			for _, corpus := range []string{"C1", "C2", "C3", "C4", "C6", "C7"} {
				setCorpus(t, corpus, measureCase(t, &config{backend: "parta", root: c.root, work: c.work}, "TestA", "EXBMEASURE_HELPER_FILE="+log))
			}
			if tc.csv != "" {
				writeFile(t, c.out, tc.csv)
			}
			if tc.refs != nil {
				partaRefs(t, c, "C2", tc.refs...)
			}
			err := slowdown(c)
			if err == nil || errors.Is(err, errRequirement) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("slowdown = %v, want a tool error containing %q", err, tc.want)
			}
			if _, err := os.Stat(log); err == nil {
				t.Fatal("slowdown ran a test before refusing")
			}
			if b, _ := os.ReadFile(c.out); string(b) != tc.csv {
				t.Fatalf("slowdown.csv changed:\n%s", b)
			}
		})
	}
}

// slowdown.csv is shared: a measurement replaces the rows of its backend and corpus, keeps the
// others, and the file is sorted by backend, corpus and repetition.
func TestMergeSlowdown(t *testing.T) {
	rows := []slowdownRow{
		{"wasm", "C2", 1, 9}, {"parta", "C2", 2, 8}, {"parta", "C2", 1, 7}, {"parta", "C1", 1, 6},
		{"wasm", "C2", 2, 9}, {"parta", "C2", 3, 8},
	}
	got := mergeSlowdown(rows, "parta", "C2", []time.Duration{3, 1, 2})
	want := []slowdownRow{
		{"parta", "C1", 1, 6}, {"parta", "C2", 1, 3}, {"parta", "C2", 2, 1}, {"parta", "C2", 3, 2},
		{"wasm", "C2", 1, 9}, {"wasm", "C2", 2, 9},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("merged\n%v\nwant\n%v", got, want)
	}
	out := filepath.Join(t.TempDir(), "slowdown.csv")
	if err := writeSlowdown(out, got); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	text := "backend,corpus,rep,wall_ns\nparta,C1,1,6\nparta,C2,1,3\nparta,C2,2,1\nparta,C2,3,2\nwasm,C2,1,9\nwasm,C2,2,9\n"
	if err != nil || string(b) != text {
		t.Fatalf("slowdown.csv %q, %v; want %q", b, err, text)
	}
	back, err := readSlowdown(out)
	if err != nil || !slices.Equal(back, got) {
		t.Fatalf("readSlowdown = %v, %v; want %v", back, err, got)
	}
	// rep 10 sorts after rep 9
	got = mergeSlowdown(nil, "parta", "C3", []time.Duration{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
	if got[9].rep != 10 || got[8].rep != 9 {
		t.Fatalf("reps sorted as %v", got)
	}
}
