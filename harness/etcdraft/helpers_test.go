// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package etcdraft

import (
	"fmt"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel"
)

// runSeed runs body for exactly one seed under faultline.Run, overriding seed-selection
// variables a developer may have exported.
func runSeed(t *testing.T, seed uint64, opts faultline.Options, body func(w *faultline.World)) {
	t.Helper()
	t.Setenv("FAULTLINE_SEED", fmt.Sprintf("0x%x", seed))
	for _, v := range []string{"FAULTLINE_SEED_LIST", "FAULTLINE_SEEDS", "FAULTLINE_BASE_SEED", "FAULTLINE_EXPLORE", "FAULTLINE_SCHEDULE", "FAULTLINE_RESULTS"} {
		t.Setenv(v, "")
	}
	faultline.Run(t, opts, body)
}

// testOptions returns the options of a d-long run (TraceFull if trace) after applying
// validateOptions, and stores the effective config (Duration set) in *cfg.
func testOptions(t *testing.T, cfg *Config, d time.Duration, trace bool) faultline.Options {
	t.Helper()
	opts := faultline.Options{Duration: d}
	if trace {
		opts.Trace = kernel.TraceConfig{Level: kernel.TraceFull}
	}
	c, o, err := validateOptions(*cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	*cfg = c
	return o
}

// recordsOf returns the records of kind in w's trace (TraceFull).
func recordsOf(w *faultline.World, kind string) []kernel.Record {
	var out []kernel.Record
	for _, r := range w.Sim.Records() {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}
