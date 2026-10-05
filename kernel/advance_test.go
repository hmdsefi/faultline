package kernel

import (
	"errors"
	"testing"
	"time"
)

// AT-KRN-48
func TestAdvanceTo(t *testing.T) {
	s := fullSim(1)
	var log orderLog
	n1 := s.AddNode("n1", func(n *Node) { n.After(time.Second, "t", log.add("ft")) })
	s.At(sec(3), "g", log.add("fg"))
	s.RunUntil(0)
	if s.Executed() != 1 {
		t.Fatalf("Executed %d after the boot", s.Executed())
	}
	n1.Pause()

	s.AdvanceTo(sec(2))
	if s.Now() != sec(2) || s.Executed() != 1 || len(log) != 0 {
		t.Fatalf("AdvanceTo(2s): Now %v Executed %d log %v", s.Now(), s.Executed(), log)
	}
	if at, ok := s.NextAt(); at != sec(3) || !ok {
		t.Fatalf("NextAt = (%v, %v)", at, ok)
	}
	count := len(s.Records())
	s.AdvanceTo(sec(2))
	if len(s.Records()) != count {
		t.Fatal("a second AdvanceTo(2s) emitted a record")
	}
	mustPanic(t, "kernel: AdvanceTo(1.000000000s) is before Now() 2.000000000s", func() { s.AdvanceTo(sec(1)) })
	mustPanic(t, `kernel: AdvanceTo(4.000000000s): event 2 "g" at 3.000000000s is due`, func() { s.AdvanceTo(sec(4)) })
	if s.Now() != sec(2) {
		t.Fatalf("Now after the failed AdvanceTo = %v", s.Now())
	}
	s.AdvanceTo(sec(3))
	if s.Now() != sec(3) || len(log) != 0 {
		t.Fatalf("AdvanceTo(3s): Now %v log %v", s.Now(), log)
	}
	n1.Resume()
	if r := s.Run(); r != StopIdle || s.Now() != sec(3) || s.Executed() != 3 || log.String() != "ft fg" {
		t.Fatalf("Run = %v Now %v Executed %d log %q", r, s.Now(), s.Executed(), log)
	}
	checkRecords(t, s, `
1 0.000000000s 0/0 kernel.start cause=0 "start" [seed=0x0000000000000001 tie_break=seeded]
2 0.000000000s 1/0 kernel.add_node cause=1 "n1" [tags= offset_ns=0 drift_ppm=0]
3 0.000000000s 1/0 kernel.event cause=2 "boot" [id=1]
4 0.000000000s 1/1 kernel.boot cause=3 "boot" []
5 0.000000000s 1/1 kernel.pause cause=4 "pause" []
6 1.000000000s 1/1 kernel.defer cause=4 "t" [id=3]
7 3.000000000s 1/1 kernel.resume cause=6 "resume" [deferred=1]
8 3.000000000s 1/1 kernel.event cause=4 "t" [id=3]
9 3.000000000s 0/0 kernel.event cause=2 "g" [id=2]
`)
	if got := s.TraceHash(); got != 0x7195285f95a39fb3 {
		t.Errorf("TraceHash = %#016x, want 0x7195285f95a39fb3", got)
	}
}

// AT-KRN-48 (variants)
func TestAdvanceToVariants(t *testing.T) {
	s := fullSim(1)
	n1 := s.AddNode("n1", func(n *Node) { n.After(time.Second, "t", func() {}) })
	s.RunUntil(0)
	n1.Crash()
	count := len(s.Records())
	s.AdvanceTo(sec(2))
	if s.Now() != sec(2) || len(s.Records()) != count {
		t.Fatalf("crashed variant: Now %v, records +%d", s.Now(), len(s.Records())-count)
	}

	s2 := New(Config{Seed: 1})
	s2.After(time.Second, "advance", func() { s2.AdvanceTo(sec(5)) })
	if r := s2.Run(); r != StopFailed {
		t.Fatalf("Run = %v", r)
	}
	if pe := panicErr(t, s2); pe.Value != "kernel: AdvanceTo called while the simulation is running" {
		t.Fatalf("PanicError value %v", pe.Value)
	}
}

// KRN-039: AdvanceTo draws nothing, executes nothing and ignores Err, MaxTime and MaxEvents.
func TestAdvanceToIgnoresLimits(t *testing.T) {
	s := New(Config{Seed: 1, MaxTime: sec(1), MaxEvents: 1, Trace: TraceConfig{Level: TraceFull}})
	n := s.AddNode("n", func(n *Node) { n.After(2*time.Second, "t", func() {}) })
	if r := s.Run(); r != StopMaxTime || s.Executed() != 1 {
		t.Fatalf("Run = %v, Executed %d", r, s.Executed())
	}
	n.Pause()
	s.Fail(errors.New("failed"))
	s.AdvanceTo(sec(10))
	if s.Now() != sec(10) || s.Executed() != 1 || lastRecord(s).Kind != "kernel.defer" {
		t.Fatalf("Now %v, Executed %d, last record %s", s.Now(), s.Executed(), recLine(lastRecord(s)))
	}
	sched := newStream(1, "kernel/sched")
	sched.Uint64() // the boot event
	sched.Uint64() // the timer
	if tb, want := s.pending[s.At(sec(20), "x", func() {})].tb, sched.Uint64(); tb != want {
		t.Errorf("tie-break after AdvanceTo = %#x, want the third kernel/sched value %#x", tb, want)
	}
}

// KRN-039: the edges goroutine mode relies on (GOR-020, GOR-021).
func TestAdvanceToEdges(t *testing.T) {
	// A paused entry at exactly t is deferred; one after t stays queued.
	s := fullSim(1)
	p := s.AddNode("p", func(n *Node) {
		n.After(2*time.Second, "t2", func() {})
		n.After(3*time.Second, "t3", func() {})
	})
	s.RunUntil(0)
	p.Pause()
	count := len(s.Records())
	s.AdvanceTo(sec(2))
	at, ok := s.NextAt()
	if s.Now() != sec(2) || at != sec(3) || !ok || len(s.Records()) != count+1 || lastRecord(s).Text != "t2" {
		t.Fatalf("AdvanceTo(2s): Now %v, NextAt (%v, %v), records +%d", s.Now(), at, ok, len(s.Records())-count)
	}

	// A stale entry between two paused entries is discarded, and the deferrals made before an
	// "is due" panic stay done.
	s = fullSim(1)
	a := s.AddNode("a", func(n *Node) { n.After(1500*time.Millisecond, "s", func() {}) })
	b := s.AddNode("b", func(n *Node) {
		n.After(time.Second, "b1", func() {})
		n.After(2*time.Second, "b2", func() {})
	})
	s.At(sec(4), "g", func() {})
	s.RunUntil(0)
	a.Crash()
	b.Pause()
	count = len(s.Records())
	mustPanic(t, `kernel: AdvanceTo(5.000000000s): event 3 "g" at 4.000000000s is due`, func() { s.AdvanceTo(sec(5)) })
	if s.Now() != sec(2) || len(s.Records()) != count+2 || lastRecord(s).Text != "b2" {
		t.Fatalf("after the failed AdvanceTo: Now %v, records +%d", s.Now(), len(s.Records())-count)
	}

	// Node-bound entries and boot events are due too.
	s = New(Config{Seed: 1})
	s.AddNode("n", func(n *Node) { n.After(time.Second, "t", func() {}) })
	mustPanic(t, `kernel: AdvanceTo(1.000000000s): event 1 "boot" at 0.000000000s is due`, func() { s.AdvanceTo(sec(1)) })
	s.RunUntil(0)
	mustPanic(t, `kernel: AdvanceTo(2.000000000s): event 2 "t" at 1.000000000s is due`, func() { s.AdvanceTo(sec(2)) })

	// The running check comes before the past check.
	s = New(Config{Seed: 1})
	s.After(time.Second, "advance", func() { s.AdvanceTo(0) })
	s.Run()
	if pe := panicErr(t, s); pe.Value != "kernel: AdvanceTo called while the simulation is running" {
		t.Fatalf("PanicError value %v", pe.Value)
	}
}

// KRN-039: an executable entry at t stops the walk, so a paused entry behind it is deferred later
// by the loop, in the order Run gives.
func TestAdvanceToKeepsQueueOrder(t *testing.T) {
	s := New(Config{Seed: 1, TieBreak: TieBreakFIFO, Trace: TraceConfig{Level: TraceFull}})
	s.At(sec(1), "g", func() {})
	p := s.AddNode("p", func(n *Node) { n.After(time.Second, "t", func() {}) })
	s.RunUntil(0)
	p.Pause()
	count := len(s.Records())
	s.AdvanceTo(sec(1))
	if len(s.Records()) != count {
		t.Fatalf("AdvanceTo(1s) emitted %s", recLine(lastRecord(s)))
	}
	if !s.Step() || lastRecord(s).Kind != "kernel.event" || lastRecord(s).Text != "g" {
		t.Fatalf("first Step: last record %s", recLine(lastRecord(s)))
	}
	if s.Step() || lastRecord(s).Kind != "kernel.defer" || lastRecord(s).Text != "t" {
		t.Fatalf("second Step: last record %s", recLine(lastRecord(s)))
	}
}
