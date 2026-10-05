package kernel

import "testing"

// AT-KRN-47 (global events; the resume variant is in pause_test.go)
func TestAtFront(t *testing.T) {
	schedule := func(s *Sim, log *orderLog) {
		s.At(0, "a", log.add("a"))
		s.AtFront(0, "f1", log.add("f1"))
		s.At(0, "b", log.add("b"))
		s.AtFront(0, "f2", log.add("f2"))
		s.At(0, "c", log.add("c"))
	}

	s := fullSim(1)
	var log orderLog
	schedule(s, &log)
	if r := s.Run(); r != StopIdle || s.Executed() != 5 {
		t.Fatalf("Run = %v, Executed %d", r, s.Executed())
	}
	if got := log.String(); got != "f1 f2 c a b" {
		t.Errorf("seeded order %q, want %q", got, "f1 f2 c a b")
	}
	checkRecords(t, s, `
1 0.000000000s 0/0 kernel.start cause=0 "start" [seed=0x0000000000000001 tie_break=seeded]
2 0.000000000s 0/0 kernel.event cause=1 "f1" [id=2]
3 0.000000000s 0/0 kernel.event cause=1 "f2" [id=4]
4 0.000000000s 0/0 kernel.event cause=1 "c" [id=5]
5 0.000000000s 0/0 kernel.event cause=1 "a" [id=1]
6 0.000000000s 0/0 kernel.event cause=1 "b" [id=3]
`)
	if got := s.TraceHash(); got != 0x071010cb931c8519 {
		t.Errorf("seeded TraceHash = %#016x, want 0x071010cb931c8519", got)
	}

	fifo := New(Config{Seed: 1, TieBreak: TieBreakFIFO, Trace: TraceConfig{Level: TraceFull}})
	var flog orderLog
	schedule(fifo, &flog)
	fifo.Run()
	if got := flog.String(); got != "a f1 b f2 c" {
		t.Errorf("fifo order %q, want %q", got, "a f1 b f2 c")
	}
	for i, r := range fifo.Records()[1:] {
		if want := []string{"1", "2", "3", "4", "5"}[i]; r.Attrs[0].Value != want {
			t.Errorf("fifo record %d id %s, want %s", r.Seq, r.Attrs[0].Value, want)
		}
	}
	if got := fifo.TraceHash(); got != 0xbe278ef19a72e1eb {
		t.Errorf("fifo TraceHash = %#016x, want 0xbe278ef19a72e1eb", got)
	}

	m := New(Config{Seed: 1})
	m.RunUntil(sec(1))
	f := func() {}
	mustPanic(t, `kernel: AtFront(0.500000000s, "x") is before Now() 1.000000000s`, func() { m.AtFront(msec(500), "x", f) })
	mustPanic(t, "kernel: empty event label", func() { m.AtFront(sec(1), "", f) })
	mustPanic(t, `kernel: nil callback for event "x"`, func() { m.AtFront(sec(1), "x", nil) })
	ran := false
	id := m.AtFront(sec(2), "x", func() { ran = true })
	if !m.Cancel(id) {
		t.Fatal("Cancel(AtFront) = false")
	}
	m.Run()
	if ran {
		t.Error("cancelled AtFront event ran")
	}
}

// KRN-026: AtFront takes tie-break 0, draws nothing from kernel/sched, and accepts a time after
// MaxTime (the event never runs).
func TestAtFrontDrawsNothing(t *testing.T) {
	s := New(Config{Seed: 1, MaxTime: sec(1)})
	ran := false
	late := s.AtFront(sec(5), "late", func() { ran = true })
	x := s.At(0, "x", func() {})
	sched := newStream(1, "kernel/sched")
	if tb := s.pending[late].tb; tb != 0 {
		t.Errorf("AtFront tie-break = %#x, want 0", tb)
	}
	if tb, want := s.pending[x].tb, sched.Uint64(); tb != want {
		t.Errorf("tie-break after AtFront = %#x, want the first kernel/sched value %#x", tb, want)
	}
	if r := s.Run(); r != StopMaxTime || ran {
		t.Fatalf("Run = %v, late ran = %v", r, ran)
	}
}
