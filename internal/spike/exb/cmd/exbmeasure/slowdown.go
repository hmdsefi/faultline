// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hmdsefi/faultline"
)

// The fixed numbers of EXB-064: seeds 0 to 99, three repetitions, and the largest allowed ratio
// median(backend) / median(parta).
const (
	slowdownSeeds = 100
	slowdownReps  = 3
	slowdownLimit = 5
)

var slowdownHeader = []string{"backend", "corpus", "rep", "wall_ns"}

// slowdownRow is one row of slowdown.csv: the wall time of one repetition of a corpus.
type slowdownRow struct {
	backend, corpus string
	rep             int
	wall            time.Duration
}

// slowdown implements EXB-064. It times three repetitions of seeds 0 to 99 of the corpus in the
// backend's normal mode. A repetition runs one process per corpus test, one at a time, and its
// wall time is their sum (C1 has 51 tests). The repetitions replace the backend's earlier rows for
// the corpus in the shared slowdown.csv. A ratio compares like with like only if every row comes
// from the same seeds on one machine (machine.txt), which is why -seeds and -reps are fixed.
//
// parta is the reference: its processes must run their seeds (exit 0 or 1), and slowdown keeps
// what each did in the work directory (refPath). For wasm or toolexec the requirement is
// median(backend) <= 5 x median(parta), so parta's rows and reference must be there first. Each
// of their processes must repeat parta's work, its seed runs with exit 0 or 1
// (slowdownRef.differs), and a repetition that passes 5 x median(parta) is stopped there, since
// it is then slower than the requirement allows (EXB §11: a timeout is a failed run).
func slowdown(c *config) error {
	if !slices.Contains([]string{"C1", "C2", "C3"}, c.corpus) {
		return fmt.Errorf("slowdown applies to C1, C2 and C3, not %q", c.corpus)
	}
	if !slices.Contains(backends, c.backend) {
		return fmt.Errorf("unknown backend %q", c.backend)
	}
	if c.seeds != slowdownSeeds || c.reps != slowdownReps {
		return fmt.Errorf("slowdown times seeds 0 to 99 three times: pass -seeds 100 -reps 3, not -seeds %d -reps %d", c.seeds, c.reps)
	}
	rows, err := readSlowdown(c.out)
	if err != nil {
		return err
	}
	tcs := corpora[c.corpus]
	if len(tcs) == 0 {
		return fmt.Errorf("corpus %s has no tests", c.corpus)
	}
	s := &slowdownRun{config: c, tcs: tcs, refs: map[string]slowdownRef{}}
	var ref []time.Duration
	if c.backend != "parta" {
		ref = walls(rows, "parta", c.corpus)
		if len(ref) == 0 {
			return fmt.Errorf("%s has no parta rows for %s: measure parta first", c.out, c.corpus)
		}
		if s.refs, err = readRefs(c.refPath(), tcs); err != nil {
			return err
		}
		s.budget = slowdownLimit * median(ref)
	}
	s.env = []string{"FAULTLINE_TRACE=full", "FAULTLINE_ARTIFACTS=off"}
	if c.backend == "wasm" {
		s.env = append(s.env, fmt.Sprintf("FAULTLINE_BASE_SEED=0x%016x", uint64(base)), "FAULTLINE_SEEDS="+strconv.Itoa(c.seeds))
		overhead, err := c.warmWasm(tcs, s.env)
		if err != nil {
			return err
		}
		fmt.Printf("slowdown wasm %s: instance overhead %v (median of %d empty runs, one per test binary)\n",
			c.corpus, median(overhead), len(overhead))
	} else {
		list := make([]string, c.seeds)
		for i := range list {
			list[i] = fmt.Sprintf("0x%016x", faultline.DeriveSeed(base, i))
		}
		s.env = append(s.env, "FAULTLINE_SEED_LIST="+strings.Join(list, ","))
	}
	s.dir = filepath.Join(c.work, c.backend, "slowdown", c.corpus)
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	var reps []time.Duration
	stopped := 0
	for rep := 1; rep <= c.reps; rep++ {
		wall, stop, err := s.repetition(rep)
		if err != nil {
			return err
		}
		reps = append(reps, wall)
		if stop {
			stopped++
		}
	}
	if c.backend == "parta" {
		if err := writeRefs(c.refPath(), s.refs); err != nil {
			return err
		}
	}
	if err := writeSlowdown(c.out, mergeSlowdown(rows, c.backend, c.corpus, reps)); err != nil {
		return err
	}
	med := median(reps)
	fmt.Printf("slowdown %s %s: median %v of %d repetitions\n", c.backend, c.corpus, med, len(reps))
	if c.backend == "parta" {
		return nil
	}
	refMed := median(ref)
	fmt.Printf("slowdown %s %s: %.2fx parta (median %v against %v; requirement <= %dx)\n",
		c.backend, c.corpus, float64(med)/float64(refMed), med, refMed, slowdownLimit)
	if stopped > 0 {
		fmt.Printf("slowdown %s %s: %d of %d repetitions stopped at %v (%dx parta's median); their rows hold the time until then\n",
			c.backend, c.corpus, stopped, len(reps), s.budget, slowdownLimit)
	}
	if len(s.diffs) > 0 {
		fmt.Printf("slowdown %s %s: %d runs did not repeat parta's work, so the times are not comparable: %s (logs in %s)\n",
			c.backend, c.corpus, len(s.diffs), strings.Join(slices.Compact(slices.Sorted(slices.Values(s.diffs))), ", "), s.dir)
	}
	// A stopped repetition is slower than the limit, so when most are, so is the median.
	if len(s.diffs) > 0 || 2*stopped > len(reps) || !withinSlowdown(med, refMed) {
		return errRequirement
	}
	return nil
}

// slowdownRun is one slowdown measurement of a corpus.
type slowdownRun struct {
	*config
	tcs    []testCase
	env    []string               // the measured environment, without FAULTLINE_RESULTS
	dir    string                 // results files and logs: <work>/<backend>/slowdown/<corpus>
	budget time.Duration          // the limit of one repetition of a backend other than parta
	refs   map[string]slowdownRef // parta's work: built by parta, checked by the other backends
	diffs  []string               // the tests whose runs on another backend did not repeat parta's work
}

// repetition runs repetition rep and returns its wall time, the sum over the corpus tests. A
// parta run that does not run its seeds is a tool error. For another backend the remaining
// budget is the run limit: a run still going at its end is stopped, the rest of the repetition is
// skipped, and stopped is true.
func (s *slowdownRun) repetition(rep int) (wall time.Duration, stopped bool, err error) {
	for _, tc := range s.tcs {
		name := filepath.Join(s.dir, fmt.Sprintf("%s-rep%d", tc.Name, rep))
		res, log := name+".jsonl", name+".log"
		_ = os.Remove(res) // a run that writes no results line leaves no file
		_ = os.Remove(log)
		limit := slowdownSeeds * runTimeout // parta: the run limit once per seed
		if s.backend != "parta" {
			limit = s.budget - wall
			if limit <= 0 {
				return wall, true, nil
			}
		}
		rr, err := s.execWithin(tc, append(slices.Clone(s.env), "FAULTLINE_RESULTS="+res), limit)
		if err != nil {
			return 0, false, err
		}
		wall += rr.wall
		writeLog := func(note string) error {
			text := rr.head() + "\n"
			if note != "" {
				text += note + "\n"
			}
			return os.WriteFile(log, []byte(text+rr.out), 0o600)
		}
		if s.backend == "parta" && (rr.code < 0 || rr.code > 1) {
			if err := writeLog(""); err != nil {
				return 0, false, err
			}
			return 0, false, fmt.Errorf("slowdown parta %s: %s, repetition %d, ended with %s, but the reference must run every seed and exit 0 or 1; output in %s, %s is unchanged",
				s.corpus, tc.Name, rep, rr.head(), log, s.out)
		}
		if rr.timedOut {
			if err := writeLog(fmt.Sprintf("stopped: repetition %d passed %v, %dx parta's median", rep, s.budget, slowdownLimit)); err != nil {
				return 0, false, err
			}
			fmt.Fprintf(os.Stderr, "exbmeasure: slowdown %s %s: %s, repetition %d, stopped at %v of the repetition, %dx parta's median; output in %s\n",
				s.backend, s.corpus, tc.Name, rep, s.budget, slowdownLimit, log)
			return wall, true, nil
		}
		lines, rerr := readResults(res)
		runs := seedRuns(lines)
		note := ""
		if s.backend == "parta" {
			r, seen := s.refs[tc.Name]
			switch {
			case rerr != nil:
				return 0, false, fmt.Errorf("slowdown parta %s: %s, repetition %d: %v", s.corpus, tc.Name, rep, rerr)
			case seen && !slices.Equal(runs, r.SeedRuns):
				return 0, false, fmt.Errorf("slowdown parta %s: %s, repetition %d, wrote other seed runs than before (%d, earlier %d), so the reference is not repeatable; %s is unchanged",
					s.corpus, tc.Name, rep, len(runs), len(r.SeedRuns), s.out)
			}
			s.refs[tc.Name] = slowdownRef{Codes: append(r.Codes, rr.code), SeedRuns: runs}
		} else if why := s.refs[tc.Name].differs(rr.code, runs, rerr); why != "" {
			note = "differs from parta: " + why
			s.diffs = append(s.diffs, tc.Name)
		}
		// A C1 test that checks results across seeds after Run fails under a seed list it did not
		// choose; the process still ran every seed, so its time counts. The log keeps the code.
		if rr.code != 0 || note != "" {
			if err := writeLog(note); err != nil {
				return 0, false, err
			}
			msg := rr.head()
			if note != "" {
				msg += ", " + note
			}
			fmt.Fprintf(os.Stderr, "exbmeasure: slowdown %s %s: %s, repetition %d, %s; output in %s\n",
				s.backend, s.corpus, tc.Name, rep, msg, log)
		}
	}
	return wall, false, nil
}

// withinSlowdown reports whether med <= 5 x ref, compared in integer nanoseconds.
func withinSlowdown(med, ref time.Duration) bool {
	return med <= slowdownLimit*ref
}

// slowdownRef is what parta's process of one corpus test did in its repetitions: the exit code
// of each and the seed runs ("<test> <seed>", sorted) of its results lines, the same in each. A
// process of another backend is timed against parta only when it writes the same seed runs and
// exits 0 or 1; parta's codes are kept for the record.
type slowdownRef struct {
	Codes    []int    `json:"codes"`
	SeedRuns []string `json:"seed_runs"`
}

// refPath is the file in the work directory that holds parta's reference for c.corpus.
func (c *config) refPath() string {
	return filepath.Join(c.work, "parta", "slowdown", c.corpus, "reference.json")
}

// writeRefs writes the references of a corpus, by test name, to path.
func writeRefs(path string, refs map[string]slowdownRef) error {
	b, err := json.MarshalIndent(refs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// readRefs reads the references at path and checks that each test of tcs has one.
func readRefs(path string, tcs []testCase) (map[string]slowdownRef, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("%s does not exist: measure parta first, with the same -work", path)
	}
	if err != nil {
		return nil, err
	}
	var refs map[string]slowdownRef
	if err := json.Unmarshal(b, &refs); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	for _, tc := range tcs {
		if r, ok := refs[tc.Name]; !ok || len(r.Codes) == 0 {
			return nil, fmt.Errorf("%s has no parta reference for %s: measure parta first, with the same -work", path, tc.Name)
		}
	}
	return refs, nil
}

// seedRuns returns the sorted seed runs ("<test> <seed>") of results lines.
func seedRuns(lines []map[string]any) []string {
	runs := make([]string, 0, len(lines))
	for _, l := range lines {
		runs = append(runs, str(l, "test")+" "+str(l, "seed"))
	}
	slices.Sort(runs)
	return runs
}

// differs describes how a run that exited code with the seed runs runs (or an unreadable results
// file, err) differs from r, or returns "" when it repeats parta's work: exit 0 or 1, and the same
// seed runs. Any other code is a run its limit or a signal ended (below 0) or a crash, trap,
// panic or tool error (above 1). Whether the seeds passed is not compared: EXB-064 times the run,
// and pass or fail belongs to compat and exactness.
func (r slowdownRef) differs(code int, runs []string, err error) string {
	var why []string
	if code < 0 || code > 1 {
		codes := slices.Compact(slices.Sorted(slices.Values(r.Codes)))
		s := make([]string, len(codes))
		for i, c := range codes {
			s[i] = strconv.Itoa(c)
		}
		why = append(why, fmt.Sprintf("exit %d, where parta exited %s", code, strings.Join(s, " or ")))
	}
	switch {
	case err != nil:
		why = append(why, fmt.Sprintf("results file: %v", err))
	case len(runs) != len(r.SeedRuns):
		why = append(why, fmt.Sprintf("%d seed runs, where parta ran %d", len(runs), len(r.SeedRuns)))
	default:
		for i := range runs {
			if runs[i] != r.SeedRuns[i] {
				why = append(why, fmt.Sprintf("seed run %q, where parta ran %q", runs[i], r.SeedRuns[i]))
				break
			}
		}
	}
	return strings.Join(why, "; ")
}

// warmWasm runs an empty test (-test.run ^$) twice in each distinct test binary of tcs. The first
// run fills faultline-wasm's compilation cache; the second, timed, is the per-seed instance
// overhead: one instance that runs no test.
func (c *config) warmWasm(tcs []testCase, env []string) ([]time.Duration, error) {
	var seen []string
	var overhead []time.Duration
	for _, tc := range tcs {
		bin, err := c.binary(tc)
		if err != nil {
			return nil, err
		}
		if slices.Contains(seen, bin) {
			continue
		}
		seen = append(seen, bin)
		empty := tc
		empty.Run, empty.Args = "^$", nil
		for i := range 2 {
			rr, err := c.exec(empty, env)
			if err != nil {
				return nil, err
			}
			if rr.code != 0 {
				return nil, fmt.Errorf("an empty run of %s exited %d:\n%s", bin, rr.code, rr.out)
			}
			if i == 1 {
				overhead = append(overhead, rr.wall)
			}
		}
	}
	return overhead, nil
}

// median returns the middle value of d, or the mean of the two middle values when len(d) is
// even. d must not be empty; it is not modified.
func median(d []time.Duration) time.Duration {
	s := slices.Clone(d)
	slices.Sort(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return s[n/2-1] + (s[n/2]-s[n/2-1])/2
}

// walls returns the wall times of the rows of backend and corpus.
func walls(rows []slowdownRow, backend, corpus string) []time.Duration {
	var w []time.Duration
	for _, r := range rows {
		if r.backend == backend && r.corpus == corpus {
			w = append(w, r.wall)
		}
	}
	return w
}

// mergeSlowdown replaces the rows of backend and corpus with one row per repetition of reps, and
// sorts the rows by backend, corpus and repetition.
func mergeSlowdown(rows []slowdownRow, backend, corpus string, reps []time.Duration) []slowdownRow {
	var out []slowdownRow
	for _, r := range rows {
		if r.backend != backend || r.corpus != corpus {
			out = append(out, r)
		}
	}
	for i, w := range reps {
		out = append(out, slowdownRow{backend, corpus, i + 1, w})
	}
	slices.SortFunc(out, func(a, b slowdownRow) int {
		if c := strings.Compare(a.backend, b.backend); c != 0 {
			return c
		}
		if c := strings.Compare(a.corpus, b.corpus); c != 0 {
			return c
		}
		return a.rep - b.rep
	})
	return out
}

// readSlowdown reads slowdown.csv; a missing or empty file has no rows. Anything that is not a
// slowdown.csv is an error, so a wrong -out never loses another file's data.
func readSlowdown(path string) ([]slowdownRow, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	recs, err := csv.NewReader(bytes.NewReader(b)).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%s is not a slowdown.csv: %v", path, err)
	}
	if len(recs) == 0 {
		return nil, nil
	}
	if !slices.Equal(recs[0], slowdownHeader) {
		return nil, fmt.Errorf("%s is not a slowdown.csv: header %q", path, strings.Join(recs[0], ","))
	}
	var rows []slowdownRow
	for i, r := range recs[1:] {
		rep, err1 := strconv.Atoi(r[2])
		ns, err2 := strconv.ParseInt(r[3], 10, 64)
		if !slices.Contains(backends, r[0]) || r[1] == "" || err1 != nil || rep < 1 || err2 != nil || ns < 1 {
			return nil, fmt.Errorf("%s is not a slowdown.csv: line %d is %q", path, i+2, strings.Join(r, ","))
		}
		rows = append(rows, slowdownRow{r[0], r[1], rep, time.Duration(ns)})
	}
	return rows, nil
}

// writeSlowdown writes rows to path through a temporary file and a rename, so the shared file is
// never left half written.
func writeSlowdown(path string, rows []slowdownRow) error {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	_ = w.Write(slowdownHeader) // a write error shows in w.Error below
	for _, r := range rows {
		_ = w.Write([]string{r.backend, r.corpus, strconv.Itoa(r.rep), strconv.FormatInt(int64(r.wall), 10)})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".slowdown-*.csv")
	if err != nil {
		return err
	}
	if _, err := f.Write(b.Bytes()); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return err
	}
	return os.Rename(f.Name(), path)
}
