// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"context"
	"math"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
)

func init() { //nolint:gochecknoinits // registers the child-process scenarios
	gorScenarios["attempt-overhead"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 200, Duration: time.Millisecond, Drain: time.Millisecond},
			func(w *World) {
				w.AddProcess("p1", func(ctx context.Context, p *Process) error { return nil })
			})
	}
}

func TestGoroutineThroughput(t *testing.T) {
	if os.Getenv("FAULTLINE_PERF") != "1" {
		t.Skip("set FAULTLINE_PERF=1 to measure GOR §9 targets (without -race)")
	}
	if readBuild().race {
		t.Skip("GOR §9 targets are measured without -race")
	}
	atLeast := func(t *testing.T, name string, got, target float64, unit string) {
		t.Logf("%s: %.0f %s (target >= %.0f)", name, got, unit, target)
		if got < target {
			t.Errorf("%s: %.0f %s, want >= %.0f", name, got, unit, target)
		}
	}
	t.Run("DriverEvents", func(t *testing.T) {
		var events uint64
		start := time.Now()
		gw(t, Options{Duration: 500 * time.Millisecond, MaxEvents: math.MaxUint64, Drain: time.Millisecond}, func(w *World) {
			w.AddProcess("p1", func(ctx context.Context, p *Process) error { <-ctx.Done(); return nil })
			w.AddServer("e1", func(n *kernel.Node) {
				var f func()
				f = func() { n.After(time.Microsecond, "t", f) }
				n.After(time.Microsecond, "t", f)
			})
			w.Final("count", func() error { events = w.Sim.Executed(); return nil })
		})
		wall := time.Since(start)
		if events < 500_000 {
			t.Fatalf("BenchmarkDriverEvents: %d events ran, want >= 500000 (Duration / 1µs)", events)
		}
		atLeast(t, "BenchmarkDriverEvents", float64(events)/wall.Seconds(), 200_000, "events/s")
	})
	t.Run("DriverDo", func(t *testing.T) {
		const ops = 200_000
		var ran atomic.Int64
		start := time.Now()
		gw(t, Options{Duration: time.Millisecond, MaxEvents: math.MaxUint64, Drain: time.Millisecond}, func(w *World) {
			w.AddProcess("p1", func(ctx context.Context, p *Process) error {
				for range ops {
					if err := p.Do("x", func() { ran.Add(1) }); err != nil {
						return err
					}
				}
				<-ctx.Done()
				return nil
			})
		})
		wall := time.Since(start)
		n := ran.Load()
		if n != ops {
			t.Fatalf("BenchmarkDriverDo: fn ran %d times, want %d", n, ops)
		}
		atLeast(t, "BenchmarkDriverDo", float64(n)/wall.Seconds(), 100_000, "ops/s")
	})
	t.Run("DriverLogf", func(t *testing.T) {
		var (
			records  atomic.Uint64 // Logf calls
			executed uint64        // kernel events; each log request the driver runs is one
		)
		start := time.Now()
		gw(t, Options{Duration: 100 * time.Millisecond, MaxEvents: math.MaxUint64, Drain: time.Millisecond}, func(w *World) {
			for _, name := range []string{"a", "b", "c"} {
				w.AddProcess(name, func(ctx context.Context, p *Process) error {
					for ctx.Err() == nil {
						p.Logf("x")
						records.Add(1)
						time.Sleep(time.Microsecond)
					}
					return nil
				})
			}
			// Final checks run before the drain. The run's end cancels every ctx before any
			// time.Sleep returns, so every Logf call comes before this check.
			w.Final("count", func() error { executed = w.Sim.Executed(); return nil })
		})
		wall := time.Since(start)
		n := records.Load()
		if executed < n {
			t.Fatalf("BenchmarkDriverLogf: %d events ran for %d Logf calls, want a log record per call", executed, n)
		}
		atLeast(t, "BenchmarkDriverLogf", float64(n)/wall.Seconds(), 100_000, "records/s")
	})
	t.Run("AttemptOverhead", func(t *testing.T) {
		// GOR §9: per-seed wall time from the results lines of a child process (wall_ns, API-087).
		res := runGorScenario(t, "attempt-overhead", "FAULTLINE_RESULTS=$ARTIFACTS/results.jsonl")
		lines := res.results(t)
		if res.code != 0 || len(lines) != 200 {
			t.Fatalf("exit %d, %d results lines, want 0 and 200\n%s", res.code, len(lines), res.out)
		}
		var total float64
		for _, l := range lines {
			ns, ok := l["wall_ns"].(float64)
			if !ok {
				t.Fatalf("results line without wall_ns: %v", l)
			}
			total += ns
		}
		perAttempt := time.Duration(total / float64(len(lines)))
		t.Logf("BenchmarkAttemptOverhead: %v per attempt (target <= 2ms)", perAttempt)
		if perAttempt > 2*time.Millisecond {
			t.Errorf("BenchmarkAttemptOverhead: %v per attempt, want <= 2ms", perAttempt)
		}
	})
}
