// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"encoding/csv"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/hmdsefi/faultline"
)

// exactnessRow is one (test, seed) of EXB-061.
type exactnessRow struct {
	test     string
	seed     uint64
	hashes   []string
	statuses []string
	exact    bool
}

// exactnessRows reads the R runs of every seed of every test of the corpus (running them first
// unless -reuse finds them) and compares trace_hash and status per results-line test name. A row
// is exact when every run has at least one line for the name, every such line carries a trace
// hash and the run's seed, and all runs have the same trace hashes and statuses, line by line.
// The row shows a run's hashes and statuses joined with ";" (one each in a normal run), and
// "missing" as the status of a run without a line.
func exactnessRows(c *config) ([]exactnessRow, error) {
	tcs := corpora[c.corpus]
	var jobs []job
	for _, tc := range tcs {
		for i := range c.seeds {
			for r := range c.runs {
				jobs = append(jobs, job{tc, faultline.DeriveSeed(base, i), r + 1})
			}
		}
	}
	if err := c.ensureAll(jobs); err != nil {
		return nil, err
	}
	var rows []exactnessRow
	for _, tc := range tcs {
		for i := range c.seeds {
			s := faultline.DeriveSeed(base, i)
			seed := fmt.Sprintf("0x%016x", s)
			perRun := make([]map[string][]map[string]any, c.runs)
			var names []string
			for r := range c.runs {
				lines, err := readResults(c.rawPath(tc, s, r+1, "jsonl"))
				if err != nil {
					return nil, err
				}
				perRun[r] = map[string][]map[string]any{}
				for _, l := range lines {
					name := str(l, "test")
					if !slices.Contains(names, name) {
						names = append(names, name)
					}
					perRun[r][name] = append(perRun[r][name], l)
				}
			}
			if len(names) == 0 {
				names = []string{tc.Name} // no results line in any run: not exact
			}
			for _, name := range names {
				row := exactnessRow{test: name, seed: s, exact: true}
				for r := range c.runs {
					ls := perRun[r][name]
					ok := len(ls) > 0
					h, st := "", "missing"
					if ok {
						hs, sts := make([]string, len(ls)), make([]string, len(ls))
						for k, l := range ls {
							hs[k], sts[k] = str(l, "trace_hash"), str(l, "status")
							if hs[k] == "" || str(l, "seed") != seed {
								ok = false
							}
						}
						h, st = strings.Join(hs, ";"), strings.Join(sts, ";")
					}
					row.hashes, row.statuses = append(row.hashes, h), append(row.statuses, st)
					if !ok || h != row.hashes[0] || st != row.statuses[0] {
						row.exact = false
					}
				}
				rows = append(rows, row)
			}
		}
	}
	return rows, nil
}

// exactness implements EXB-061: exactness-<backend>-<corpus>.csv.
func exactness(c *config) error {
	if c.seeds < 1 || c.runs < 2 {
		return fmt.Errorf("exactness needs -seeds >= 1 and -runs >= 2")
	}
	if !slices.Contains([]string{"C1", "C2", "C3", "C4", "C5"}, c.corpus) {
		return fmt.Errorf("exactness applies to C1 to C5, not %q", c.corpus)
	}
	rows, err := exactnessRows(c)
	if err != nil {
		return err
	}
	f, err := os.Create(c.out)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	head := []string{"test", "seed"}
	for r := range c.runs {
		head = append(head, fmt.Sprintf("run%d_hash", r+1))
	}
	for r := range c.runs {
		head = append(head, fmt.Sprintf("run%d_status", r+1))
	}
	_ = w.Write(append(head, "exact")) // a write error shows in w.Error below
	exact := 0
	for _, row := range rows {
		rec := []string{row.test, fmt.Sprintf("0x%016x", row.seed)}
		rec = append(append(rec, row.hashes...), row.statuses...)
		_ = w.Write(append(rec, strconv.FormatBool(row.exact)))
		if row.exact {
			exact++
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	fmt.Printf("exactness %s %s: %d/%d (test, seed) rows exact\n", c.backend, c.corpus, exact, len(rows))
	if exact != len(rows) || len(rows) == 0 {
		return errRequirement
	}
	return nil
}
