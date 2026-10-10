// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package kernel

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// sec returns n seconds as a Time.
func sec(n int64) Time { return Time(n * int64(time.Second)) }

// msec returns n milliseconds as a Time.
func msec(n int64) Time { return Time(n * int64(time.Millisecond)) }

// orderLog collects labels in execution order.
type orderLog []string

func (l *orderLog) add(label string) func() { return func() { *l = append(*l, label) } }
func (l orderLog) String() string           { return strings.Join(l, " ") }

// AT-KRN-06 (tie-break part): creating a stream draws nothing from kernel/sched.
func TestRandCreationDoesNotDraw(t *testing.T) {
	run := func(createStream bool) uint64 {
		s := New(Config{Seed: 1})
		s.At(0, "before", func() {})
		if createStream {
			s.Rand("z")
		}
		s.At(0, "after", func() {})
		s.Run()
		return s.TraceHash()
	}
	if a, b := run(false), run(true); a != b {
		t.Fatalf("Rand(z) changed the run: %#x vs %#x", a, b)
	}
}

// AT-KRN-08
func TestSeededTieBreak(t *testing.T) {
	for _, level := range []TraceLevel{TraceFull, TraceHash} {
		s := New(Config{Seed: 1, Trace: TraceConfig{Level: level}})
		var log orderLog
		for _, l := range []string{"a", "b", "c"} {
			s.At(0, l, log.add(l))
		}
		if r := s.Run(); r != StopIdle {
			t.Fatalf("Run = %v", r)
		}
		if log.String() != "c a b" {
			t.Errorf("order %q, want %q", log, "c a b")
		}
		if got := s.TraceHash(); got != 0xba4e9d61bf3b035e {
			t.Errorf("level %v: TraceHash = %#016x, want 0xba4e9d61bf3b035e", level, got)
		}
		if level == TraceFull {
			checkRecords(t, s, `
1 0.000000000s 0/0 kernel.start cause=0 "start" [seed=0x0000000000000001 tie_break=seeded]
2 0.000000000s 0/0 kernel.event cause=1 "c" [id=3]
3 0.000000000s 0/0 kernel.event cause=1 "a" [id=1]
4 0.000000000s 0/0 kernel.event cause=1 "b" [id=2]
`)
		}
	}
}

// AT-KRN-09
func TestFIFOTieBreak(t *testing.T) {
	s := New(Config{Seed: 1, TieBreak: TieBreakFIFO, Trace: TraceConfig{Level: TraceFull}})
	var log orderLog
	for _, l := range []string{"a", "b", "c"} {
		s.At(0, l, log.add(l))
	}
	if r := s.Run(); r != StopIdle {
		t.Fatalf("Run = %v", r)
	}
	if log.String() != "a b c" {
		t.Errorf("order %q", log)
	}
	checkRecords(t, s, `
1 0.000000000s 0/0 kernel.start cause=0 "start" [seed=0x0000000000000001 tie_break=fifo]
2 0.000000000s 0/0 kernel.event cause=1 "a" [id=1]
3 0.000000000s 0/0 kernel.event cause=1 "b" [id=2]
4 0.000000000s 0/0 kernel.event cause=1 "c" [id=3]
`)
	if got := s.TraceHash(); got != 0x840f29953fdd0102 {
		t.Errorf("TraceHash = %#016x, want 0x840f29953fdd0102", got)
	}
}

// AT-KRN-10
func TestTimeOrder(t *testing.T) {
	s := New(Config{Seed: 1})
	var got []Time
	for _, at := range []Time{sec(3), sec(1), sec(2)} {
		want := at
		s.At(at, "e", func() {
			if s.Now() != want {
				t.Errorf("Now() = %v inside the event at %v", s.Now(), want)
			}
			got = append(got, s.Now())
		})
	}
	s.Run()
	if len(got) != 3 || got[0] != sec(1) || got[1] != sec(2) || got[2] != sec(3) {
		t.Errorf("order %v", got)
	}
	if s.Now() != sec(3) || s.Executed() != 3 {
		t.Errorf("Now = %v, Executed = %d", s.Now(), s.Executed())
	}
}

// AT-KRN-11
func TestSchedulingMisuse(t *testing.T) {
	s := New(Config{Seed: 1})
	s.RunUntil(sec(1))
	f := func() {}
	mustPanic(t, `kernel: At(0.500000000s, "x") is before Now() 1.000000000s`, func() { s.At(msec(500), "x", f) })
	mustPanic(t, "kernel: empty event label", func() { s.At(sec(1), "", f) })
	mustPanic(t, `kernel: nil callback for event "x"`, func() { s.At(sec(1), "x", nil) })
	mustPanic(t, "kernel: empty event label", func() { s.After(0, "", f) })
	mustPanic(t, `kernel: nil callback for event "x"`, func() { s.After(0, "x", nil) })
	var ranAt Time = -1
	s.After(-5*time.Second, "neg", func() { ranAt = s.Now() })
	s.Run()
	if ranAt != sec(1) {
		t.Errorf("After(-5s) ran at %v, want 1s", ranAt)
	}
}

// AT-KRN-12
func TestCancel(t *testing.T) {
	s := New(Config{Seed: 1, Trace: TraceConfig{Level: TraceFull}})
	ran := false
	id := s.After(time.Second, "x", func() { ran = true })
	n := len(s.Records())
	if !s.Cancel(id) {
		t.Fatal("first Cancel = false")
	}
	if s.Cancel(id) {
		t.Fatal("second Cancel = true")
	}
	if len(s.Records()) != n {
		t.Error("Cancel emitted a record")
	}
	if r := s.Run(); r != StopIdle || s.Executed() != 0 || ran {
		t.Fatalf("Run = %v, Executed = %d, ran = %v", r, s.Executed(), ran)
	}
	done := s.After(0, "y", func() {})
	s.Run()
	if s.Cancel(done) || s.Cancel(0) || s.Cancel(EventID(999)) {
		t.Error("Cancel of an executed, zero or unknown ID returned true")
	}
}

// AT-KRN-13
func TestRunUntilAndRunFor(t *testing.T) {
	s := New(Config{Seed: 1})
	s.At(sec(1), "e", func() {})
	if r := s.RunUntil(msec(500)); r != StopDeadline || s.Now() != msec(500) || s.Executed() != 0 {
		t.Fatalf("RunUntil(500ms) = %v, Now %v, Executed %d", r, s.Now(), s.Executed())
	}
	if r := s.RunUntil(sec(2)); r != StopDeadline || s.Now() != sec(2) || s.Executed() != 1 {
		t.Fatalf("RunUntil(2s) = %v, Now %v, Executed %d", r, s.Now(), s.Executed())
	}
	if r := s.RunFor(0); r != StopDeadline || s.Now() != sec(2) {
		t.Fatalf("RunFor(0) = %v, Now %v", r, s.Now())
	}
	mustPanic(t, "kernel: RunUntil(1.000000000s) is before Now() 2.000000000s", func() { s.RunUntil(sec(1)) })
	mustPanic(t, "kernel: RunFor(-1ns): negative duration", func() { s.RunFor(-1) })

	s2 := New(Config{Seed: 1})
	inner := false
	s2.At(sec(2), "outer", func() { s2.At(sec(2), "inner", func() { inner = true }) })
	if r := s2.RunUntil(sec(2)); r != StopDeadline || !inner {
		t.Fatalf("RunUntil(2s) = %v, inner ran = %v", r, inner)
	}
}

// AT-KRN-14
func TestRunIdle(t *testing.T) {
	s := New(Config{Seed: 1})
	s.At(sec(1), "a", func() {})
	s.At(sec(4), "b", func() {})
	if r := s.Run(); r != StopIdle || s.Now() != sec(4) {
		t.Fatalf("Run = %v, Now = %v", r, s.Now())
	}
}

// AT-KRN-15
func TestMaxEvents(t *testing.T) {
	s := New(Config{Seed: 1, MaxEvents: 5})
	var tick func()
	tick = func() { s.After(time.Millisecond, "tick", tick) }
	s.After(0, "tick", tick)
	if r := s.Run(); r != StopMaxEvents || s.Executed() != 5 || s.Now() != msec(4) {
		t.Fatalf("Run = %v, Executed %d, Now %v", r, s.Executed(), s.Now())
	}
	h := s.TraceHash()
	if r := s.Run(); r != StopMaxEvents || s.Executed() != 5 || s.Now() != msec(4) || s.TraceHash() != h {
		t.Fatalf("second Run = %v, Executed %d, Now %v", r, s.Executed(), s.Now())
	}
	if s.Step() {
		t.Fatal("Step() = true after MaxEvents")
	}

	s = New(Config{Seed: 1, MaxEvents: 3})
	for i := 0; i < 3; i++ {
		s.After(time.Duration(i), "once", func() {})
	}
	if r := s.Run(); r != StopIdle {
		t.Fatalf("Run with exactly MaxEvents events = %v, want idle", r)
	}
}

// AT-KRN-16
func TestMaxTime(t *testing.T) {
	newSim := func() *Sim {
		s := New(Config{Seed: 1, MaxTime: msec(10)})
		var tick func()
		tick = func() { s.After(3*time.Millisecond, "tick", tick) }
		s.At(0, "tick", tick)
		return s
	}
	s := newSim()
	if r := s.Run(); r != StopMaxTime || s.Executed() != 4 || s.Now() != msec(9) {
		t.Fatalf("Run = %v, Executed %d, Now %v", r, s.Executed(), s.Now())
	}
	if r := s.Run(); r != StopMaxTime || s.Now() != msec(9) {
		t.Fatalf("second Run = %v, Now %v", r, s.Now())
	}
	s = newSim()
	if r := s.RunUntil(msec(10)); r != StopDeadline || s.Now() != msec(10) {
		t.Fatalf("RunUntil(10ms) = %v, Now %v", r, s.Now())
	}
	if r := s.RunUntil(msec(20)); r != StopMaxTime || s.Now() != msec(10) {
		t.Fatalf("RunUntil(20ms) = %v, Now %v", r, s.Now())
	}
	empty := New(Config{Seed: 1, MaxTime: msec(10)})
	if r := empty.RunUntil(msec(11)); r != StopMaxTime || empty.Now() != 0 {
		t.Fatalf("empty RunUntil(11ms) = %v, Now %v", r, empty.Now())
	}
}

// AT-KRN-17 (global events; the deferred-entry part is in pause_test.go)
func TestStep(t *testing.T) {
	s := New(Config{Seed: 1})
	for _, at := range []Time{sec(1), sec(2), sec(3)} {
		s.At(at, "e", func() {})
	}
	for i := 1; i <= 3; i++ {
		if !s.Step() {
			t.Fatalf("Step %d = false", i)
		}
		if s.Executed() != uint64(i) || s.Now() != sec(int64(i)) {
			t.Fatalf("after Step %d: Executed %d, Now %v", i, s.Executed(), s.Now())
		}
	}
	if s.Step() {
		t.Fatal("Step on an empty queue = true")
	}
}

// AT-KRN-18
func TestFailStopsTheLoop(t *testing.T) {
	errBad, errOther := errors.New("bad"), errors.New("other")
	s := fullSim(7)
	gRan := false
	s.After(time.Second, "check", func() { s.Fail(errBad) })
	s.After(2*time.Second, "later", func() { gRan = true })
	if r := s.Run(); r != StopFailed {
		t.Fatalf("Run = %v", r)
	}
	if s.Err() != errBad || gRan || s.Now() != sec(1) {
		t.Fatalf("Err %v, later ran %v, Now %v", s.Err(), gRan, s.Now())
	}
	checkRecords(t, s, `
1 0.000000000s 0/0 kernel.start cause=0 "start" [seed=0x0000000000000007 tie_break=seeded]
2 1.000000000s 0/0 kernel.event cause=1 "check" [id=1]
3 1.000000000s 0/0 kernel.fail cause=2 "bad" []
`)
	if got := s.TraceHash(); got != 0x7b8c3d65d23ec4db {
		t.Errorf("TraceHash = %#016x, want 0x7b8c3d65d23ec4db", got)
	}
	h := s.TraceHash()
	s.Fail(errOther)
	if s.Err() != errBad || s.TraceHash() != h {
		t.Error("a second Fail changed the Sim")
	}
	mustPanic(t, "kernel: Fail called with a nil error", func() { s.Fail(nil) })
	if r := s.Run(); r != StopFailed {
		t.Fatalf("Run after failure = %v", r)
	}

	early := New(Config{Seed: 7})
	early.After(0, "never", func() {})
	early.Fail(errBad)
	if r := early.Run(); r != StopFailed || early.Executed() != 0 {
		t.Fatalf("Run after an early Fail = %v, Executed %d", r, early.Executed())
	}
}

// AT-KRN-42 (global events; the stale node timer part is in node_test.go)
func TestNextAt(t *testing.T) {
	s := New(Config{Seed: 1})
	if at, ok := s.NextAt(); at != 0 || ok {
		t.Fatalf("empty NextAt = (%v, %v)", at, ok)
	}
	s.At(sec(5), "b", func() {})
	s.At(sec(2), "a", func() {})
	if at, ok := s.NextAt(); at != sec(2) || !ok {
		t.Fatalf("NextAt = (%v, %v), want (2s, true)", at, ok)
	}
}

// AT-KRN-43 (StopReason)
func TestStopReasonString(t *testing.T) {
	cases := map[StopReason]string{
		StopIdle: "idle", StopDeadline: "deadline", StopMaxTime: "max-time",
		StopMaxEvents: "max-events", StopFailed: "failed", StopReason(9): "StopReason(9)",
	}
	for r, want := range cases {
		if got := r.String(); got != want {
			t.Errorf("StopReason(%d).String() = %q, want %q", r, got, want)
		}
	}
}

// AT-KRN-40 (callback and nested Sims; observers and hooks are tested with them)
func TestActive(t *testing.T) {
	a := New(Config{Seed: 1})
	b := New(Config{Seed: 2})
	if Active() != nil {
		t.Fatal("Active() != nil before Run")
	}
	var inA, inB, afterB *Sim
	b.After(0, "b", func() { inB = Active() })
	a.After(0, "a", func() {
		inA = Active()
		b.Run()
		afterB = Active()
	})
	a.Run()
	if inA != a || inB != b || afterB != a {
		t.Fatalf("Active: in A %p, in B %p, after B %p; A %p B %p", inA, inB, afterB, a, b)
	}
	if Active() != nil {
		t.Fatal("Active() != nil after Run")
	}
}
