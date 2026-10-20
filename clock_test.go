// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
)

func init() { //nolint:gochecknoinits // registers the child-process scenarios
	gorScenarios["gor-32"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: time.Second}, func(w *World) {
			n := w.AddProcess("p1", func(ctx context.Context, p *Process) error { <-ctx.Done(); return nil })
			w.RunFor(time.Millisecond)
			if os.Getenv("GOR32_CLOCK") == "1" {
				_ = w.Process(n.ID()).Clock().Now()
				return
			}
			w.Process(n.ID()).Logf("x")
		})
	}
}

// AT-GOR-32: Process.Logf and the LocalClock methods called while the driver holds the world lock
// panic with GOR-016's Process message naming the method. In body that is a setup error, which
// writes no artifact (a panic in body would write one).
func TestATGOR32_ProcessMethodsOnDriverAreMisuse(t *testing.T) {
	for _, tc := range []struct{ env, method string }{{"GOR32_CLOCK=0", "Logf"}, {"GOR32_CLOCK=1", "Clock().Now"}} {
		res := runGorScenario(t, "gor-32", tc.env)
		if want := fmt.Sprintf(gor016ProcMsg, tc.method); res.code != 1 || !strings.Contains(res.out, want) {
			t.Fatalf("%s: exit %d, want 1 and %q\n%s", tc.method, res.code, want, res.out)
		}
		entries, err := os.ReadDir(res.artifacts)
		if err != nil || len(entries) != 0 || strings.Contains(res.out, "artifacts:") {
			t.Fatalf("%s: %d entries in the artifacts folder (%v), want a setup error and no artifact\n%s",
				tc.method, len(entries), err, res.out)
		}
	}
}

func TestLocalClockArithmetic(t *testing.T) {
	for _, tc := range []struct {
		delta time.Duration
		ppm   int32
		want  time.Duration
	}{
		{time.Second, 100_000, 1100 * time.Millisecond},
		{time.Second, -500_000, 500 * time.Millisecond},
		{time.Second, 0, time.Second},
		{3, 333_333, 3},
		{1_000_001, 1_000_000, 2_000_002},
	} {
		if got := localElapsed(tc.delta, tc.ppm); got != tc.want {
			t.Errorf("localElapsed(%d, %d) = %d, want %d", tc.delta, tc.ppm, got, tc.want)
		}
	}
	for _, tc := range []struct {
		d    time.Duration
		ppm  int32
		want time.Duration
	}{
		{110 * time.Millisecond, 100_000, 100 * time.Millisecond},
		{1, 100_000, 1},
		{0, 100_000, 0},
		{-5, 0, 0},
		{time.Second, -500_000, 2 * time.Second},
	} {
		if got := localToGlobal(tc.d, tc.ppm); got != tc.want {
			t.Errorf("localToGlobal(%d, %d) = %d, want %d", tc.d, tc.ppm, got, tc.want)
		}
	}
	if floorDiv(-1, 1_000_000) != -1 || floorDiv(1, 1_000_000) != 0 || floorDiv(-1_000_000, 1_000_000) != -1 {
		t.Fatal("floorDiv does not round toward negative infinity")
	}
}

func TestATGOR19_ClockJumpExcludedFromMonotonic(t *testing.T) {
	var off05, mono05, off15, mono15 atomic.Int64
	gw(t, Options{Duration: 3 * time.Second}, func(w *World) {
		w.AddProcess("p1", func(ctx context.Context, p *Process) error {
			time.Sleep(500 * time.Millisecond)
			off05.Store(int64(p.Clock().Now().Sub(time.Now()))) //nolint:staticcheck // S1024: the AT's expression
			mono05.Store(int64(p.Clock().Monotonic()))
			time.Sleep(time.Second)
			off15.Store(int64(p.Clock().Now().Sub(time.Now()))) //nolint:staticcheck // S1024: the AT's expression
			mono15.Store(int64(p.Clock().Monotonic()))
			<-ctx.Done()
			return nil
		}, kernel.WithClock(5*time.Second, 0))
		w.Plan(fault.Script(fault.Event{At: kernel.Time(time.Second), Kind: fault.KindClockJump, Node: "p1", N: int64(-2 * time.Second)}))
	})
	got := [4]time.Duration{time.Duration(off05.Load()), time.Duration(mono05.Load()), time.Duration(off15.Load()), time.Duration(mono15.Load())}
	want := [4]time.Duration{5 * time.Second, 500 * time.Millisecond, 3 * time.Second, 1500 * time.Millisecond}
	if got != want {
		t.Fatalf("offset/monotonic at 0.5s and 1.5s = %v, want %v", got, want)
	}
}

func TestATGOR20_DriftAndToGlobal(t *testing.T) {
	var mono, global atomic.Int64
	gw(t, Options{Duration: 2 * time.Second}, func(w *World) {
		w.AddProcess("p1", func(ctx context.Context, p *Process) error {
			time.Sleep(time.Second)
			mono.Store(int64(p.Clock().Monotonic()))
			global.Store(int64(p.Clock().ToGlobal(110 * time.Millisecond)))
			<-ctx.Done()
			return nil
		}, kernel.WithClock(0, 100_000))
	})
	if time.Duration(mono.Load()) != 1100*time.Millisecond || time.Duration(global.Load()) != 100*time.Millisecond {
		t.Fatalf("Monotonic %v ToGlobal %v, want 1.1s and 100ms", time.Duration(mono.Load()), time.Duration(global.Load()))
	}
}

// A drift change by a fault at 1s: Monotonic is continuous and uses the new rate afterwards
// (GOR-055: m0 = 1s at 1s, then localElapsed(1s, 100000) = 1.1s).
func TestGoroutineMonotonicAcrossDriftChange(t *testing.T) {
	var at05, at2 atomic.Int64
	gw(t, Options{Duration: 3 * time.Second}, func(w *World) {
		w.AddProcess("p1", func(ctx context.Context, p *Process) error {
			time.Sleep(500 * time.Millisecond)
			at05.Store(int64(p.Clock().Monotonic()))
			time.Sleep(1500 * time.Millisecond)
			at2.Store(int64(p.Clock().Monotonic()))
			<-ctx.Done()
			return nil
		})
		w.Plan(fault.Script(fault.Event{At: kernel.Time(time.Second), Kind: fault.KindClockDrift, Node: "p1", N: 100_000}))
	})
	if time.Duration(at05.Load()) != 500*time.Millisecond || time.Duration(at2.Load()) != 2100*time.Millisecond {
		t.Fatalf("Monotonic at 0.5s %v, at 2s %v; want 0.5s and 2.1s", time.Duration(at05.Load()), time.Duration(at2.Load()))
	}
}

// The edges of the clock arithmetic: KRN-072 rounds up a remainder of 1 and saturates a quotient of
// 2^63, and with KRN-070's saturating adds localElapsed, Monotonic and the bookkeeping of
// clocksAfter and refreshClocks stop at math.MaxInt64 instead of wrapping. The second part is built
// without a bubble: with the lock free and kernel time ahead of bubble time, the LocalClock methods
// only read.
func TestLocalClockEdges(t *testing.T) {
	if got := localElapsed(math.MaxInt64, 1_000_000); got != math.MaxInt64 {
		t.Errorf("localElapsed(MaxInt64, 1000000) = %d, want MaxInt64", got)
	}
	if got := localElapsed(math.MaxInt64, -500_000); got != 4_611_686_018_427_387_903 {
		t.Errorf("localElapsed(MaxInt64, -500000) = %d, want 4611686018427387903", got)
	}
	for _, tc := range []struct {
		d    time.Duration
		ppm  int32
		want time.Duration
	}{
		{2, 999_999, 2},                          // 2e6 / 1999999: remainder 1, rounded up
		{1 << 62, -500_000, math.MaxInt64},       // quotient 2^63
		{math.MaxInt64, -500_000, math.MaxInt64}, // quotient 2^64 - 2
	} {
		if got := localToGlobal(tc.d, tc.ppm); got != tc.want {
			t.Errorf("localToGlobal(%d, %d) = %d, want %d", tc.d, tc.ppm, got, tc.want)
		}
	}
	s := kernel.New(kernel.Config{Seed: 1})
	n := s.AddNode("n", func(*kernel.Node) {}, kernel.WithClock(0, 1_000_000))
	n2 := s.AddNode("n2", func(*kernel.Node) {}, kernel.WithClock(0, 1_000_000))
	s.Step() // the boot events
	s.Step()
	d := &driver{w: &World{Sim: s}}
	// Node n: a crashed earlier incarnation and the running one. Node n2: an exited incarnation.
	// Only the running one is alive, so only its state may change (GOR-055).
	old := &Process{Node: n, d: d, status: incCrashed, ppm: 1_000_000}
	p := &Process{Node: n, d: d, inc: n.Incarnation(), status: incRunning, ppm: 1_000_000}
	exited := &Process{Node: n2, d: d, inc: n2.Incarnation(), status: incExited, ppm: 1_000_000}
	d.node(n.ID()).procs = []*Process{old, p}
	d.node(n2.ID()).procs = []*Process{exited}
	half := kernel.Time(math.MaxInt64/2 + 1)
	setDrift := func(ppm int32) {
		n.SetDrift(ppm)
		n2.SetDrift(ppm)
	}
	check := func(step string, g0 kernel.Time, ppm int32) {
		t.Helper()
		if p.m0 != math.MaxInt64 || p.g0 != g0 || p.ppm != ppm {
			t.Fatalf("after %s: (g0, m0, ppm) = (%d, %d, %d), want (%d, MaxInt64, %d)", step, p.g0, p.m0, p.ppm, g0, ppm)
		}
		for _, q := range []*Process{old, exited} {
			if q.m0 != 0 || q.g0 != 0 || q.ppm != 1_000_000 {
				t.Fatalf("after %s: a stopped incarnation's state changed to (%d, %d, %d)", step, q.g0, q.m0, q.ppm)
			}
		}
	}
	s.AdvanceTo(half)
	setDrift(0)
	d.refreshClocks(s.Now()) // m0 = localElapsed(half, 1000000), saturated
	check("refreshClocks", half, 0)
	s.AdvanceTo(half + 1)
	snap := d.snapshotLocal()
	setDrift(5)
	d.clocksAfter(snap) // m0 = MaxInt64 + 1, saturated
	check("clocksAfter", half+1, 5)
	s.AdvanceTo(half + 2)
	if got := p.Clock().Monotonic(); got != math.MaxInt64 {
		t.Fatalf("Monotonic = %d, want MaxInt64", got)
	}
	setDrift(0)
	d.refreshClocks(s.Now()) // m0 = MaxInt64 + 1, saturated
	check("second refreshClocks", half+2, 0)
}

// GOR-016: every LocalClock method called from body while the driver holds the world lock panics
// with the Process message naming the method. From a check, which runs without the lock (GOR-015),
// they read the clock at the check's time.
func TestGoroutineClockOnDriver(t *testing.T) {
	var got [3]string
	var local, mono, global atomic.Int64
	gw(t, Options{Duration: 2 * time.Second}, func(w *World) {
		n := w.AddProcess("p1", func(ctx context.Context, p *Process) error { <-ctx.Done(); return nil },
			kernel.WithClock(2*time.Second, 100_000))
		w.RunFor(time.Millisecond)
		c := w.Process(n.ID()).Clock()
		for i, call := range []func(){
			func() { _ = c.Now() },
			func() { _ = c.Monotonic() },
			func() { _ = c.ToGlobal(time.Second) },
		} {
			func() {
				defer func() { got[i] = fmt.Sprint(recover()) }()
				call()
			}()
		}
		w.Final("clock", func() error {
			local.Store(int64(c.Now().Sub(epoch)))
			mono.Store(int64(c.Monotonic()))
			global.Store(int64(c.ToGlobal(110 * time.Millisecond)))
			return nil
		})
	})
	for i, method := range []string{"Clock().Now", "Clock().Monotonic", "Clock().ToGlobal"} {
		if want := fmt.Sprintf(gor016ProcMsg, method); got[i] != want {
			t.Errorf("%s from body: recovered %q, want %q", method, got[i], want)
		}
	}
	// At 2 s: local time 2 s + 2.2 s, Monotonic 2.2 s, ToGlobal(110ms) at 100000 ppm 100ms.
	gotFinal := [3]time.Duration{time.Duration(local.Load()), time.Duration(mono.Load()), time.Duration(global.Load())}
	if want := [3]time.Duration{4200 * time.Millisecond, 2200 * time.Millisecond, 100 * time.Millisecond}; gotFinal != want {
		t.Fatalf("from the final check: local, Monotonic, ToGlobal = %v, want %v", gotFinal, want)
	}
}

// GOR-055: a drift change that body makes outside events counts for Monotonic from the instant of
// the change (refreshClocks): 1 s at 0 ppm, then 1 s at 100000 ppm.
func TestGoroutineMonotonicBodyDriftChange(t *testing.T) {
	var at2 atomic.Int64
	gw(t, Options{Duration: 3 * time.Second}, func(w *World) {
		n := w.AddProcess("p1", func(ctx context.Context, p *Process) error {
			time.Sleep(2 * time.Second)
			at2.Store(int64(p.Clock().Monotonic()))
			<-ctx.Done()
			return nil
		})
		w.RunFor(time.Second)
		n.SetDrift(100_000)
	})
	if got := time.Duration(at2.Load()); got != 2100*time.Millisecond {
		t.Fatalf("Monotonic at 2s = %v, want 2.1s", got)
	}
}

// GOR-055: a clock jump rebases the Monotonic state at the jump, so floor rounding restarts there.
// At 500000 ppm, 1 ns before and 1 ns after a jump at 1 ns count 1 + 1 = 2 ns; one 2 ns interval
// would count 2 + floor(1) = 3 ns.
func TestGoroutineMonotonicRebaseAtJump(t *testing.T) {
	var at2 atomic.Int64
	gw(t, Options{Duration: time.Second}, func(w *World) {
		w.AddProcess("p1", func(ctx context.Context, p *Process) error {
			time.Sleep(2 * time.Nanosecond)
			at2.Store(int64(p.Clock().Monotonic()))
			<-ctx.Done()
			return nil
		}, kernel.WithClock(0, 500_000))
		w.Plan(fault.Script(fault.Event{At: 1, Kind: fault.KindClockJump, Node: "p1", N: int64(time.Second)}))
	})
	if got := time.Duration(at2.Load()); got != 2 {
		t.Fatalf("Monotonic at 2ns = %d, want 2", got)
	}
}

// GOR-046 step 5, GOR-055: a restarted incarnation's Monotonic starts at 0 at its boot and runs at
// the node's drift, which a crash keeps (KRN-073); later drift changes, by a fault at 1.75 s and by
// body at 2 s, apply to the new incarnation.
func TestGoroutineMonotonicAfterRestart(t *testing.T) {
	var at2, at25 atomic.Int64
	gw(t, Options{Duration: 3 * time.Second}, func(w *World) {
		n := w.AddProcess("p1", func(ctx context.Context, p *Process) error {
			if p.Incarnation() == 2 {
				time.Sleep(500 * time.Millisecond)
				at2.Store(int64(p.Clock().Monotonic()))
				time.Sleep(500 * time.Millisecond)
				at25.Store(int64(p.Clock().Monotonic()))
			}
			<-ctx.Done()
			return nil
		}, kernel.WithClock(0, 100_000))
		w.Plan(fault.Script(
			fault.Event{At: kernel.Time(time.Second), Kind: fault.KindCrash, Node: "p1"},
			fault.Event{At: kernel.Time(1500 * time.Millisecond), Kind: fault.KindRestart, Node: "p1"},
			fault.Event{At: kernel.Time(1750 * time.Millisecond), Kind: fault.KindClockDrift, Node: "p1", N: 0},
		))
		w.RunFor(2 * time.Second)
		n.SetDrift(200_000)
	})
	// 2 s: 250ms at 100000 ppm, then 250ms at 0 ppm; 2.5 s: then 500ms at 200000 ppm.
	got := [2]time.Duration{time.Duration(at2.Load()), time.Duration(at25.Load())}
	if want := [2]time.Duration{525 * time.Millisecond, 1125 * time.Millisecond}; got != want {
		t.Fatalf("Monotonic of incarnation 2 at 2s and 2.5s = %v, want %v", got, want)
	}
}

// GOR-055 across process nodes, in node ID order. a (500000 ppm) is not rebased by c's jump. b
// exits at once, so it is not alive. c (500000 ppm) is rebased by a backward jump at 1ns. Body's
// drift changes at 1s apply to both a and c. a reads its clock right after body's change, before
// the next loop pass applies it: Monotonic still uses the incarnation's drift, ToGlobal the node's.
func TestGoroutineMonotonicSeveralNodes(t *testing.T) {
	var a2ns, a1s, aToGlobal, c2ns, c2s atomic.Int64
	gw(t, Options{Duration: 3 * time.Second}, func(w *World) {
		changed := make(chan struct{})
		a := w.AddProcess("a", func(ctx context.Context, p *Process) error {
			time.Sleep(2 * time.Nanosecond)
			a2ns.Store(int64(p.Clock().Monotonic()))
			<-changed
			a1s.Store(int64(p.Clock().Monotonic()))
			aToGlobal.Store(int64(p.Clock().ToGlobal(110 * time.Millisecond)))
			<-ctx.Done()
			return nil
		}, kernel.WithClock(0, 500_000))
		w.AddProcess("b", func(ctx context.Context, p *Process) error { return nil })
		c := w.AddProcess("c", func(ctx context.Context, p *Process) error {
			time.Sleep(2 * time.Nanosecond)
			c2ns.Store(int64(p.Clock().Monotonic()))
			time.Sleep(2*time.Second - 2*time.Nanosecond)
			c2s.Store(int64(p.Clock().Monotonic()))
			<-ctx.Done()
			return nil
		}, kernel.WithClock(5*time.Second, 500_000))
		w.Plan(fault.Script(fault.Event{At: 1, Kind: fault.KindClockJump, Node: "c", N: int64(-2 * time.Second)}))
		w.RunFor(time.Second)
		a.SetDrift(100_000)
		c.SetDrift(100_000)
		close(changed)
	})
	// a: one segment, 2 + floor(1) = 3 at 2ns and 1s + floor(0.5s) = 1.5s at 1s; ToGlobal(110ms) at
	// 100000 ppm is 100ms. c: 1 + 1 = 2 at 2ns (rebased at 1ns); at 2s
	// 1 + localElapsed(1s-1ns, 500000) + localElapsed(1s, 100000) = 1 + 1_499_999_998 + 1_100_000_000.
	got := [5]int64{a2ns.Load(), a1s.Load(), aToGlobal.Load(), c2ns.Load(), c2s.Load()}
	if want := [5]int64{3, 1_500_000_000, 100_000_000, 2, 2_599_999_999}; got != want {
		t.Fatalf("a: Monotonic at 2ns and 1s, ToGlobal(110ms) at 1s; c: Monotonic at 2ns and 2s = %v, want %v", got, want)
	}
}

// The clock bookkeeping around a Step allocates nothing: snapshotLocal reuses its buffer.
func TestLocalClockBookkeepingAllocs(t *testing.T) {
	s := kernel.New(kernel.Config{Seed: 1})
	d := &driver{w: &World{Sim: s}}
	for _, name := range []string{"a", "b", "c"} {
		n := s.AddNode(name, func(*kernel.Node) {})
		d.node(n.ID()).procs = []*Process{{Node: n, d: d, status: incRunning}}
	}
	if allocs := testing.AllocsPerRun(10, func() { d.clocksAfter(d.snapshotLocal()) }); allocs != 0 {
		t.Fatalf("snapshotLocal and clocksAfter: %v allocations per Step, want 0", allocs)
	}
}
