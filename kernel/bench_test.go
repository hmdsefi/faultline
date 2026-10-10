// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package kernel

import (
	"os"
	"strconv"
	"testing"
	"time"
)

// loopSim is the BenchmarkLoop setup: 1024 global timers, timer i rescheduling itself every
// 1000+i ns.
func loopSim(maxEvents uint64, level TraceLevel) *Sim {
	s := New(Config{Seed: 1, MaxEvents: maxEvents, Trace: TraceConfig{Level: level}})
	for i := 0; i < 1024; i++ {
		d := time.Duration(1000 + i)
		var self func()
		self = func() { s.After(d, "tick", self) }
		s.After(d, "tick", self)
	}
	return s
}

// reportEvents reports b.N per second of timed run as the "events/s" metric of KRN §9.
func reportEvents(b *testing.B) {
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "events/s")
}

// benchLoop runs b.N events of loopSim at level in the timed region.
func benchLoop(b *testing.B, level TraceLevel) {
	b.ReportAllocs()
	s := loopSim(uint64(b.N), level) //nolint:gosec // b.N is positive
	b.ResetTimer()
	s.Run()
	reportEvents(b)
	if got := s.Executed(); got != uint64(b.N) { //nolint:gosec // b.N is positive
		b.Fatalf("executed %d events, want %d", got, b.N)
	}
}

func BenchmarkLoop(b *testing.B) {
	b.Run("hash", func(b *testing.B) { benchLoop(b, TraceHash) })
	b.Run("full", func(b *testing.B) { benchLoop(b, TraceFull) })
}

func BenchmarkNodeTimers(b *testing.B) {
	b.ReportAllocs()
	const nodes = 16
	s := New(Config{Seed: 1, MaxEvents: uint64(b.N) + nodes}) //nolint:gosec // b.N is positive; the boot events are not timed
	for i := 0; i < nodes; i++ {
		s.AddNode("n"+strconv.Itoa(i), func(n *Node) {
			for j := 0; j < 64; j++ {
				d := time.Duration(1000 + j)
				var self func()
				self = func() { n.After(d, "timer", self) }
				n.After(d, "timer", self)
			}
		}, WithClock(0, 100))
	}
	s.RunFor(0) // boot the nodes
	b.ResetTimer()
	s.Run()
	reportEvents(b)
}

func BenchmarkCancel(b *testing.B) {
	b.ReportAllocs()
	s := loopSim(0, TraceHash)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Cancel(s.After(time.Millisecond, "c", func() {}))
	}
	reportEvents(b)
}

func BenchmarkEmit(b *testing.B) {
	b.ReportAllocs()
	s := New(Config{Seed: 1})
	r := Record{Kind: "bench.x", Text: "0123456789abcdef", Attrs: []Attr{{Key: "a", Value: "1"}, {Key: "b", Value: "2"}}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Emit(r)
	}
	reportEvents(b)
}

func BenchmarkRandStream(b *testing.B) {
	b.ReportAllocs()
	s := New(Config{Seed: 1})
	labels := make([]string, 1024)
	for i := range labels {
		labels[i] = "bench/" + strconv.Itoa(i)
		s.Rand(labels[i])
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Rand(labels[i%len(labels)])
	}
	reportEvents(b)
}

// AT-KRN-44, KRN-120: TraceHash mode does not allocate per event.
func TestLoopAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts are not representative under the race detector")
	}
	s := loopSim(0, TraceHash)
	for i := 0; i < 10_000; i++ {
		s.Step()
	}
	// AllocsPerRun divides by its run count as integers, so measure all steps in one run.
	const steps = 100_000
	total := testing.AllocsPerRun(1, func() {
		for i := 0; i < steps; i++ {
			if !s.Step() {
				t.Fatalf("Step %d executed no event", i)
			}
		}
	})
	if avg := total / steps; avg >= 0.01 {
		t.Fatalf("%.4f allocations per event, want < 0.01", avg)
	}
}

// AT-KRN-44: throughput targets of KRN §9 (FAULTLINE_PERF=1 only; shared runners are noisy).
func TestThroughput(t *testing.T) {
	if os.Getenv("FAULTLINE_PERF") != "1" {
		t.Skip("set FAULTLINE_PERF=1 to check the throughput targets")
	}
	if raceEnabled {
		t.Skip("throughput is not representative under the race detector")
	}
	for _, c := range []struct {
		level TraceLevel
		min   float64
	}{{TraceHash, 1_000_000}, {TraceFull, 300_000}} {
		r := testing.Benchmark(func(b *testing.B) { benchLoop(b, c.level) })
		// testing.Benchmark discards a failure and returns a zero result.
		if r.N == 0 {
			t.Fatalf("%v: benchLoop failed (go test -bench Loop shows why)", c.level)
		}
		if got := float64(r.N) / r.T.Seconds(); got < c.min {
			t.Errorf("%v: %.0f events/s, want >= %.0f", c.level, got, c.min)
		} else {
			t.Logf("%v: %.0f events/s", c.level, got)
		}
	}
}
