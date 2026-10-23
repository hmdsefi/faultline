// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"fmt"
	"os"
)

// exactMode is the value of FAULTLINE_EXACT, set only by the exact-backend runner (EXB-014,
// EXB-015; spike branch).
type exactMode uint8

const (
	exactOff  exactMode = iota // unset: Part A
	exactPlan                  // plan instance: print the seed plan and skip
	exactRun                   // run instance of the wasip1 runner: one seed, exact labels
)

// exactPlanPrefix starts every plan line; faultline-wasm removes lines with this prefix.
const exactPlanPrefix = "faultline-exact-plan\t"

// exactConsoleLine replaces GOR-077's console line in run instances (EXB-015).
const exactConsoleLine = "  determinism: exact (wasip1 backend)"

// parseExact validates FAULTLINE_EXACT (EXB §7). Empty values never reach it (API-010).
func parseExact(v string) (exactMode, error) {
	switch v {
	case "plan":
		return exactPlan, nil
	case "run":
		return exactRun, nil
	}
	return exactOff, fmt.Errorf("faultline: FAULTLINE_EXACT=%q: want plan or run", v)
}

// writeExactPlan writes one plan line per seed to standard output, each with a single Write
// (EXB-014 step 1): fmt.Fprintf formats the line into a buffer and writes it once.
func writeExactPlan(test string, seeds []uint64) {
	for _, s := range seeds {
		fmt.Fprintf(os.Stdout, "%s%s\t0x%016x\n", exactPlanPrefix, test, s)
	}
}

// determinism returns report.json's and the results line's determinism_level and
// exact_backend (GOR-078, EXB-015).
func (r *runner) determinism() (level, backend string) {
	if r.plan.env.exact == exactRun {
		return "exact", "wasip1"
	}
	return determinismLevel(r.plan.opts.Mode), ""
}

// exactConsole replaces GOR-077's console line with exactConsoleLine in run instances
// (EXB-015). consoleLines adds that line only in goroutine mode, so an event-mode seed gets no
// determinism line.
func (r *runner) exactConsole(lines []string) []string {
	if r.plan.env.exact != exactRun {
		return lines
	}
	for i, l := range lines {
		if l == gorConsoleLine {
			lines[i] = exactConsoleLine
		}
	}
	return lines
}
