// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
)

var assertOnDriver atomic.Bool

func init() { //nolint:gochecknoinits // registers the child-process scenarios
	// AT-GOR-31's structure with a test double for package assert (plan 3.8 runs the real one).
	gorScenarios["assert-queue"] = func(t *testing.T) {
		assertOnDriver.Store(true)
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 5 * time.Second, Procs: 4}, func(w *World) {
			ga := w.drv.ga
			// record is EXP-001's recording step for one call: unless the run already failed, it
			// emits the call's record and, for a violation, fails the run.
			record := func(name string, violation error) func() {
				return func() {
					if curGoroutineID() != ga.gid {
						assertOnDriver.Store(false)
					}
					if w.Sim.Err() != nil {
						return
					}
					w.Sim.Emit(kernel.Record{Kind: "test.assert", Text: name})
					if violation != nil {
						w.Sim.Fail(violation)
					}
				}
			}
			w.AddServer("e1", func(n *kernel.Node) {
				n.After(500*time.Millisecond, "e", func() { ga.assertRun(record("e1", nil)) })
				n.After(2*time.Second, "late", func() {}) // never runs: p1's call fails the run at 1 s
			})
			w.AddProcess("p1", func(ctx context.Context, p *Process) error {
				time.Sleep(time.Second)
				ga.assertRun(record("p1", errors.New("assert p1 failed")))
				<-ctx.Done()
				return nil
			})
		})
		t.Logf("assert-on-driver=%v", assertOnDriver.Load())
	}
	// Point 2 comes before the invariants: p1's failing call, made after the event at 0.5 s woke
	// it, fails the run, so the invariants of that quiescence do not run.
	gorScenarios["assert-before-invariants"] = func(t *testing.T) {
		var ran atomic.Bool
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 2 * time.Second}, func(w *World) {
			ga := w.drv.ga
			ch := make(chan struct{})
			w.AddServer("n1", func(n *kernel.Node) {
				n.After(500*time.Millisecond, "e", func() { close(ch) })
			})
			w.AddProcess("p1", func(ctx context.Context, p *Process) error {
				<-ch
				ga.assertRun(func() { w.Sim.Fail(errors.New("assert p1 failed")) })
				<-ctx.Done()
				return nil
			})
			w.Invariant("inv", func() error {
				if w.Sim.Now() == kernel.Time(500*time.Millisecond) {
					ran.Store(true)
				}
				return nil
			})
		})
		t.Logf("invariant-ran-at-0.5s=%v", ran.Load())
	}
	// Point 4 comes before classification: a failing call made by a passing final check fails
	// the run.
	gorScenarios["assert-from-final"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: time.Second}, func(w *World) {
			ga := w.drv.ga
			w.Final("queue a failing call", func() error {
				ga.assertRun(func() { w.Sim.Fail(errors.New("assert from final failed")) })
				return nil
			})
		})
	}
	// Point 4 also runs after a final check failed, so the calls of a failing check are drained.
	gorScenarios["assert-final-failed"] = func(t *testing.T) {
		var drained atomic.Bool
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: time.Second}, func(w *World) {
			ga := w.drv.ga
			w.Final("fail", func() error {
				ga.assertRun(func() { drained.Store(true) })
				return errors.New("final failed")
			})
		})
		t.Logf("final-call-drained=%v", drained.Load())
	}
	// Point 2 comes after the failNow block (GOR-020): a call queued by a process that then calls
	// t.FailNow never runs; E1 discards it.
	gorScenarios["assert-failnow"] = func(t *testing.T) {
		var ran atomic.Bool
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 3 * time.Second}, func(w *World) {
			ga := w.drv.ga
			w.AddProcess("p1", func(ctx context.Context, p *Process) error {
				time.Sleep(time.Second)
				ga.assertRun(func() { ran.Store(true) })
				w.T().FailNow()
				return nil
			})
		})
		t.Logf("call-ran=%v", ran.Load())
	}
}

func TestGoroutineAssertQueueDrainPoints(t *testing.T) {
	res := runGorScenario(t, "assert-queue", "FAULTLINE_SEED=0x1")
	if res.code != 1 || !strings.Contains(res.out, "faultline: simulation failed at t=1.000000000s") ||
		!strings.Contains(res.out, "assert-on-driver=true") {
		t.Fatalf("exit %d\n%s", res.code, res.out)
	}
	rs := res.artifact(t, 1).Trace.Records
	e := indexWhere(rs, func(r kernel.Record) bool { return r.Kind == "kernel.event" && r.Text == "e" })
	a := indexWhere(rs, func(r kernel.Record) bool { return r.Kind == "test.assert" && r.Text == "e1" })
	if e < 0 || a != e+1 || rs[a].At != kernel.Time(500*time.Millisecond) {
		t.Fatalf("event e at %d, its assert record at %d; want directly after, at 0.5s", e, a)
	}
	for _, r := range rs {
		if r.Kind == "kernel.event" && r.At > kernel.Time(time.Second) {
			t.Fatalf("kernel.event after 1s: %+v", r)
		}
	}
}

// GOR-085's order on failing runs: point 2 before the invariants and after the failNow block,
// point 4 before classification and also after a failed final check.
func TestGoroutineAssertQueueFailingRuns(t *testing.T) {
	for _, tc := range []struct{ scenario, headline, log string }{
		{"assert-before-invariants", "faultline: simulation failed at t=0.500000000s", "invariant-ran-at-0.5s=false"},
		{"assert-from-final", "faultline: simulation failed at t=1.000000000s", ""},
		{"assert-final-failed", `faultline: final check "fail" failed after the run at t=1.000000000s`, "final-call-drained=true"},
		{"assert-failnow", "stopped by t.FailNow or t.Fatal at t=1.000000000s", "call-ran=false"},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			res := runGorScenario(t, tc.scenario, "FAULTLINE_SEED=0x1")
			if res.code != 1 || !strings.Contains(res.out, tc.headline) || !strings.Contains(res.out, tc.log) {
				t.Fatalf("exit %d, want 1, %q and %q\n%s", res.code, tc.headline, tc.log, res.out)
			}
		})
	}
}

func TestGoroutineAssertQueueOrderAndInstants(t *testing.T) {
	var mu sync.Mutex
	var got []string
	var at250 kernel.Time
	var finalDrained atomic.Bool
	gw(t, Options{Duration: time.Second}, func(w *World) {
		got, at250 = nil, 0
		finalDrained.Store(false)
		ga := w.drv.ga
		ga.assertRun(func() { mu.Lock(); got = append(got, "f1"); mu.Unlock() })
		ga.assertRun(func() { mu.Lock(); got = append(got, "f2"); mu.Unlock() })
		go func() {
			time.Sleep(250 * time.Millisecond)
			ga.assertRun(func() { at250 = w.Sim.Now() })
		}()
		w.Final("queue from a check", func() error {
			ga.assertRun(func() { finalDrained.Store(true) })
			return nil
		})
	})
	if !slices.Equal(got, []string{"f1", "f2"}) {
		t.Fatalf("order %v, want FIFO", got)
	}
	if at250 != kernel.Time(250*time.Millisecond) {
		t.Fatalf("call made at 0.25s drained at %v", at250)
	}
	if !finalDrained.Load() {
		t.Fatal("call made by a final check was not drained (GOR-085 point 4)")
	}
}

// GOR-085 point 1: calls made by an event are drained right after its Step, before the
// checkpoint, so the checkpoint covers their records. Invariants run after the next quiescence
// with no record in between, so the last checkpoint equals the trace hash there.
func TestGoroutineAssertQueueBeforeCheckpoint(t *testing.T) {
	var mismatch, checked []kernel.Time
	var drained int
	gw(t, Options{Duration: time.Second}, func(w *World) {
		mismatch, checked, drained = nil, nil, 0
		ga := w.drv.ga
		w.AddServer("n1", func(n *kernel.Node) {
			for _, at := range []time.Duration{100 * time.Millisecond, 500 * time.Millisecond} {
				n.After(at, "a", func() {
					ga.assertRun(func() {
						drained++
						w.Sim.Emit(kernel.Record{Kind: "test.assert", Text: "a"})
					})
				})
			}
		})
		w.Invariant("checkpoint covers the drain", func() error {
			checked = append(checked, w.Sim.Now())
			if cp := w.drv.checkpoints; cp[len(cp)-1] != w.Sim.TraceHash() {
				mismatch = append(mismatch, w.Sim.Now())
			}
			return nil
		})
	})
	if drained != 2 || !slices.Contains(checked, kernel.Time(100*time.Millisecond)) || !slices.Contains(checked, kernel.Time(500*time.Millisecond)) {
		t.Fatalf("%d calls drained, invariant ran at %v; want 2, and runs at 0.1s and 0.5s", drained, checked)
	}
	if len(mismatch) != 0 {
		t.Fatalf("last checkpoint differs from the trace hash after the events at %v", mismatch)
	}
}

// GOR-085's wait rule and point 3: a call made by an invariant is drained at the instant of the
// call, also when the next event is later (the drain step of GOR-020 consumed its wake-up). A call
// made by the invariant at the end of the run is drained by point 3, after drive returned at the
// horizon (GOR-020 returns before its wait rule) and before the final checks.
func TestGoroutineAssertQueueFromInvariant(t *testing.T) {
	type pair struct {
		called, drained kernel.Time
		driving         bool // drained inside drive
	}
	var pairs, atFinal []pair
	gw(t, Options{Duration: time.Second}, func(w *World) {
		pairs, atFinal = nil, nil
		ga := w.drv.ga
		w.AddServer("n1", func(n *kernel.Node) {
			n.After(100*time.Millisecond, "a", func() {})
			n.After(500*time.Millisecond, "b", func() {})
			n.After(time.Second, "c", func() {})
		})
		w.Invariant("queue a call", func() error {
			called := w.Sim.Now()
			ga.assertRun(func() { pairs = append(pairs, pair{called, w.Sim.Now(), w.driving}) })
			return nil
		})
		w.Final("copy", func() error {
			atFinal = slices.Clone(pairs)
			return nil
		})
	})
	if len(pairs) == 0 || !slices.Equal(atFinal, pairs) || pairs[len(pairs)-1].called != kernel.Time(time.Second) {
		t.Fatalf("drained %v, %v of them before the final checks; want the last call at 1s, all before", pairs, atFinal)
	}
	if pairs[len(pairs)-1].driving {
		t.Fatal("the call made at 1s was drained inside drive; want drive to return at the horizon and point 3 to drain it")
	}
	sawA := false
	for _, p := range pairs {
		if p.drained != p.called {
			t.Fatalf("call made at %v drained at %v", p.called, p.drained)
		}
		sawA = sawA || p.called == kernel.Time(100*time.Millisecond)
	}
	if !sawA {
		t.Fatalf("no call at 0.1s in %v", pairs)
	}
}

// assertRun never runs f, also on the driver, and E1 discards what is still queued when the
// attempt stops before point 3 (here body calls t.Skip).
func TestGoroutineAssertQueueDiscardedAtEnd(t *testing.T) {
	var ga *goAttempt
	var ran, ranInBody, pendingInBody atomic.Bool
	gw(t, Options{Duration: time.Second}, func(w *World) {
		ga = w.drv.ga
		ga.assertRun(func() { ran.Store(true) })
		ranInBody.Store(ran.Load())
		pendingInBody.Store(ga.asserts.pending())
		w.T().Skip("stop the attempt with a call still queued")
	})
	if ranInBody.Load() || !pendingInBody.Load() {
		t.Fatalf("assertRun on the driver: ran %v, queued %v; want queued only", ranInBody.Load(), pendingInBody.Load())
	}
	if ran.Load() || ga.asserts.pending() {
		t.Fatalf("after the run: ran %v, still queued %v; want discarded", ran.Load(), ga.asserts.pending())
	}
}

// drainAsserts runs the queue in FIFO order and repeats until it is empty, so calls queued while
// it runs are drained by the same call, after the batch and without overwriting it.
func TestDrainAssertsUntilEmpty(t *testing.T) {
	ga := &goAttempt{asserts: &assertQueue{}, wake: make(chan struct{}, 1)}
	d := &driver{ga: ga}
	var got []string
	ga.assertRun(func() {
		got = append(got, "f1")
		ga.assertRun(func() { got = append(got, "g1") })
		ga.assertRun(func() { got = append(got, "g2") })
	})
	ga.assertRun(func() { got = append(got, "f2") })
	if len(ga.wake) != 1 {
		t.Fatal("assertRun did not send on the wake channel")
	}
	d.lockDriver()
	d.drainAsserts()
	d.unlockDriver()
	if !slices.Equal(got, []string{"f1", "f2", "g1", "g2"}) || ga.asserts.pending() {
		t.Fatalf("ran %v, pending %v; want [f1 f2 g1 g2] and an empty queue", got, ga.asserts.pending())
	}
}

// GOR-085: every queued function runs on the driver with the world lock held, whoever queued it:
// body, a kernel event, a process or a final check.
func TestGoroutineAssertQueueOnDriverLocked(t *testing.T) {
	var runs, bad atomic.Int32
	gw(t, Options{Duration: time.Second}, func(w *World) {
		runs.Store(0)
		bad.Store(0)
		ga := w.drv.ga
		f := func() {
			runs.Add(1)
			if curGoroutineID() != ga.gid || !w.drv.holds.Load() {
				bad.Add(1)
			}
		}
		ga.assertRun(f)
		w.AddServer("n1", func(n *kernel.Node) {
			n.After(100*time.Millisecond, "e", func() { ga.assertRun(f) })
		})
		w.AddProcess("p1", func(ctx context.Context, p *Process) error {
			time.Sleep(200 * time.Millisecond)
			ga.assertRun(f)
			<-ctx.Done()
			return nil
		})
		w.Final("queue a call", func() error {
			ga.assertRun(f)
			return nil
		})
	})
	if runs.Load() != 4 || bad.Load() != 0 {
		t.Fatalf("%d calls ran, %d of them off the driver or without the world lock; want 4 and 0", runs.Load(), bad.Load())
	}
}
