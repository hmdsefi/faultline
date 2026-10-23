// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Command faultline-wasm runs a GOOS=wasip1 test binary with deterministic randomness and a
// virtual clock (prototype A of the exact backend). Use it as go test -exec faultline-wasm.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hmdsefi/faultline/runner/wasm"
)

func fail(err error) {
	fmt.Fprintf(os.Stderr, "faultline-wasm: %v\n", err)
	os.Exit(2)
}

// parseSeed parses a seed as API-010 does: 0x/0X followed by hex digits, or decimal digits.
func parseSeed(s string) (uint64, bool) {
	if h, ok := strings.CutPrefix(s, "0x"); ok {
		s = h
	} else if h, ok := strings.CutPrefix(s, "0X"); ok {
		s = h
	} else {
		if s == "" || strings.Trim(s, "0123456789") != "" {
			return 0, false
		}
		v, err := strconv.ParseUint(s, 10, 64)
		return v, err == nil
	}
	if s == "" || strings.Trim(s, "0123456789abcdefABCDEF") != "" {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 16, 64)
	return v, err == nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "faultline-wasm: usage: faultline-wasm <module> [args...]")
		os.Exit(2)
	}
	module, args := os.Args[1], os.Args[2:]
	var quantum int64
	if q := strings.TrimSpace(os.Getenv("FAULTLINE_WASM_CLOCK_QUANTUM")); q != "" {
		v, err := strconv.ParseInt(q, 10, 64)
		if err != nil || v < 0 || strings.Trim(q, "0123456789") != "" {
			fail(fmt.Errorf("invalid FAULTLINE_WASM_CLOCK_QUANTUM %q: want non-negative decimal nanoseconds", q))
		}
		quantum = v
	}
	cache := os.Getenv("FAULTLINE_WASM_CACHE")
	if cache == "" {
		cache = filepath.Join(os.TempDir(), "faultline-wasm")
	}
	stats := os.Getenv("FAULTLINE_WASM_STATS") == "1"
	ctx := context.Background()
	h, err := wasm.NewHost(ctx, module, cache, quantum)
	if err != nil {
		fail(err)
	}
	// h is never closed: every path ends in os.Exit, which releases the runtime.
	run := func(seed uint64, env map[string]string, args []string, stdout io.Writer) int {
		res, err := h.Run(ctx, wasm.Instance{Seed: seed, Args: args, Env: env, Stdout: stdout, Stderr: os.Stderr})
		if err != nil {
			fail(err)
		}
		if stats {
			fmt.Fprintf(os.Stderr, "faultline-wasm: seed=0x%016x compile=%v run=%v virtual=%v clock_reads=%d exit=%d\n",
				seed, h.Compile, res.Run, res.Virtual, res.ClockReads, res.Code)
		}
		return res.Code
	}
	env := wasm.HostEnv()
	if s := strings.TrimSpace(env["FAULTLINE_SEED"]); s != "" { // single mode (EXB-014)
		seed, ok := parseSeed(s)
		if !ok {
			fail(fmt.Errorf("invalid FAULTLINE_SEED %q", s))
		}
		os.Exit(run(seed, env, args, os.Stdout))
	}
	// Plan/run mode (EXB-014): a plan instance with seed 0, then one run instance per plan line.
	planEnv := clone(env)
	planEnv["FAULTLINE_EXACT"] = "plan"
	filter := &wasm.PlanFilter{W: os.Stdout}
	code := run(0, planEnv, args, filter)
	filter.Flush()
	if filter.Err != nil {
		fail(filter.Err)
	}
	for _, e := range filter.Entries {
		runEnv := clone(env)
		delete(runEnv, "FAULTLINE_SEED_LIST") // FAULTLINE_SEED and FAULTLINE_SEED_LIST conflict (API-011)
		runEnv["FAULTLINE_EXACT"] = "run"
		runEnv["FAULTLINE_SEED"] = fmt.Sprintf("0x%016x", e.Seed)
		if c := run(e.Seed, runEnv, wasm.RunArgs(args, e.Test), os.Stdout); code == 0 {
			code = c // EXB-016: the first non-zero code, plan instance first
		}
	}
	os.Exit(code)
}

func clone(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
