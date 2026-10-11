// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package golden holds the run-twice determinism helper for tests that drive a kernel.Sim
// directly. The golden scenarios and their pinned trace hashes live in its test files.
package golden

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/hmdsefi/faultline/kernel"
)

// RunFunc builds a fresh simulation for seed with exactly the given trace configuration, runs it,
// and returns the Sim (never nil) and the stop reason of its last Run* call. It must not share
// mutable state between calls.
type RunFunc func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason)

// Result summarizes one run.
type Result struct {
	Seed     uint64
	Hash     uint64
	Stop     kernel.StopReason
	Executed uint64
	Now      kernel.Time
	Err      string // Err().Error(), or "" if Err() is nil
}

// EnvCheckDeterminism is the environment variable read by Enabled.
const EnvCheckDeterminism = "FAULTLINE_CHECK_DETERMINISM"

// Enabled reports whether FAULTLINE_CHECK_DETERMINISM requests run-twice checks (DET-030).
func Enabled() (bool, error) {
	switch v := os.Getenv(EnvCheckDeterminism); v {
	case "", "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, fmt.Errorf("golden: invalid %s=%q: want 0 or 1", EnvCheckDeterminism, v)
	}
}

// Check runs fn once with TraceHash; if Enabled, it also runs the comparison of DET-031.
func Check(t testing.TB, seed uint64, fn RunFunc) Result {
	t.Helper()
	on, err := Enabled()
	if err != nil {
		t.Fatal(err.Error())
	}
	a := run(seed, fn, kernel.TraceConfig{Level: kernel.TraceHash})
	if on {
		compare(t, seed, fn, a)
	}
	return a.res
}

// MustBeDeterministic always runs the comparison of DET-031.
func MustBeDeterministic(t testing.TB, seed uint64, fn RunFunc) Result {
	t.Helper()
	if _, err := Enabled(); err != nil {
		t.Fatal(err.Error())
	}
	a := run(seed, fn, kernel.TraceConfig{Level: kernel.TraceHash})
	compare(t, seed, fn, a)
	return a.res
}

// FormatRecord renders r on one line (DET-032).
func FormatRecord(r kernel.Record) string {
	var b strings.Builder
	fmt.Fprintf(&b, "seq=%d at=%s node=%d inc=%d kind=%s cause=%d text=%q attrs=[",
		r.Seq, r.At, r.Node, r.Inc, r.Kind, r.Cause, r.Text)
	for i, a := range r.Attrs {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%s=%q", a.Key, a.Value)
	}
	b.WriteByte(']')
	return b.String()
}

// outcome is one run: its Sim and its Result.
type outcome struct {
	sim *kernel.Sim
	res Result
}

func run(seed uint64, fn RunFunc, trace kernel.TraceConfig) outcome {
	s, stop := fn(seed, trace)
	res := Result{Seed: seed, Hash: s.TraceHash(), Stop: stop, Executed: s.Executed(), Now: s.Now()}
	if err := s.Err(); err != nil {
		res.Err = err.Error()
	}
	return outcome{sim: s, res: res}
}

// compare is the comparison of DET-031 for the TraceHash run a.
func compare(t testing.TB, seed uint64, fn RunFunc, a outcome) {
	t.Helper()
	full := kernel.TraceConfig{Level: kernel.TraceFull, Buffer: 0}
	b := run(seed, fn, full)
	if a.res == b.res {
		return
	}
	c := run(seed, fn, full)
	if b.res == c.res {
		d := run(seed, fn, kernel.TraceConfig{Level: kernel.TraceHash})
		if a.res == d.res {
			t.Fatalf("determinism: seed 0x%016x: the trace level changes the run: TraceHash run %s, TraceFull run %s",
				seed, fmtResult(a.res), fmtResult(b.res))
		}
		t.Fatalf("determinism: seed 0x%016x: two runs differ (%s vs %s); the TraceFull runs agree, so there is no record diff",
			seed, fmtResult(a.res), fmtResult(d.res))
	}
	rb, rc := b.sim.Records(), c.sim.Records()
	i := 0
	for i < len(rb) && i < len(rc) && sameRecord(rb[i], rc[i]) {
		i++
	}
	if i == len(rb) && i == len(rc) {
		t.Fatalf("determinism: seed 0x%016x: two runs differ (%s vs %s) with the same records",
			seed, fmtResult(b.res), fmtResult(c.res))
	}
	t.Fatalf("determinism: seed 0x%016x: two runs differ (%s vs %s); first difference at record %d:\n  run 1: %s\n  run 2: %s",
		seed, fmtResult(b.res), fmtResult(c.res), i+1, recOrEnd(rb, i), recOrEnd(rc, i))
}

func sameRecord(x, y kernel.Record) bool {
	return x.Seq == y.Seq && x.At == y.At && x.Node == y.Node && x.Inc == y.Inc && x.Kind == y.Kind &&
		x.Cause == y.Cause && x.Text == y.Text && slices.Equal(x.Attrs, y.Attrs)
}

func fmtResult(r Result) string {
	return fmt.Sprintf("hash=0x%016x stop=%s executed=%d now=%s err=%q", r.Hash, r.Stop, r.Executed, r.Now, r.Err)
}

func recOrEnd(rs []kernel.Record, i int) string {
	if i < len(rs) {
		return FormatRecord(rs[i])
	}
	return "<end of trace>"
}
