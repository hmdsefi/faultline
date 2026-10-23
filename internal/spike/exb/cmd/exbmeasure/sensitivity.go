// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"encoding/csv"
	"fmt"
	"os"

	"github.com/hmdsefi/faultline"
)

// sensitivity implements EXB-062 for C4 (>= 4 distinct key orders) and C5 (>= 95 distinct
// masks) over the first -seeds seeds of the exactness runs (run 1 and run 2). For C7 it records
// EXB-068's in-process goroutine order, which has no requirement: it always passes.
func sensitivity(c *config) error {
	need, ok := map[string]int{"C4": 4, "C5": 95, "C7": 0}[c.corpus]
	if !ok {
		return fmt.Errorf("sensitivity applies to C4, C5 and C7, not %q", c.corpus)
	}
	if c.seeds < 1 {
		return fmt.Errorf("sensitivity needs -seeds >= 1")
	}
	tc := corpora[c.corpus][0]
	var jobs []job
	for i := range c.seeds {
		jobs = append(jobs, job{tc, faultline.DeriveSeed(base, i), 1}, job{tc, faultline.DeriveSeed(base, i), 2})
	}
	if err := c.ensureAll(jobs); err != nil {
		return err
	}
	f, err := os.Create(c.out)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	_ = w.Write([]string{"seed", "outcome_run1", "outcome_run2"}) // a write error shows in w.Error below
	outcomes := make([][2]string, c.seeds)
	for i := range c.seeds {
		s := faultline.DeriveSeed(base, i)
		for r := range 2 {
			outcomes[i][r] = readOutcome(c.rawPath(tc, s, r+1, "out"), tc.Name)
		}
		_ = w.Write([]string{fmt.Sprintf("0x%016x", s), outcomes[i][0], outcomes[i][1]})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	distinct, stable := countOutcomes(outcomes)
	if c.corpus == "C7" {
		fmt.Printf("sensitivity %s C7 (informational, EXB-068): %d distinct orders, %d/%d seeds with equal orders in both runs\n",
			c.backend, distinct, stable, c.seeds)
		return nil
	}
	fmt.Printf("sensitivity %s %s: %d distinct outcomes (need >= %d), %d/%d seeds with equal outcomes in both runs\n",
		c.backend, c.corpus, distinct, need, stable, c.seeds)
	if distinct < need || stable != c.seeds {
		return errRequirement
	}
	return nil
}

// countOutcomes counts the distinct non-empty run-1 outcomes, and the seeds whose two runs have
// the same non-empty outcome. An empty outcome is a run that wrote no EXB_OUT line.
func countOutcomes(outcomes [][2]string) (distinct, stable int) {
	seen := map[string]bool{}
	for _, o := range outcomes {
		if o[0] == "" {
			continue
		}
		if !seen[o[0]] {
			seen[o[0]] = true
			distinct++
		}
		if o[0] == o[1] {
			stable++
		}
	}
	return distinct, stable
}
