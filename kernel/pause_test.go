package kernel

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// lifecycleScenario builds the AT-KRN-25 scenario and runs it with RunUntil(1s). It returns the
// Sim, the stop reason and the tick counter shared by all incarnations.
func lifecycleScenario(trace TraceConfig) (*Sim, StopReason, int) {
	s := New(Config{Seed: 1, Trace: trace})
	counter := 0
	n1 := s.AddNode("n1", func(n *Node) {
		var tick func()
		tick = func() {
			counter++
			n.Logf("tick %d", counter)
			n.After(100*time.Millisecond, "tick", tick)
		}
		n.After(100*time.Millisecond, "tick", tick)
	}, WithTags("server"))
	s.At(msec(250), "pause", n1.Pause)
	s.At(msec(500), "resume", n1.Resume)
	s.At(msec(650), "crash", n1.Crash)
	s.At(msec(700), "restart", n1.Restart)
	r := s.RunUntil(sec(1))
	return s, r, counter
}

// AT-KRN-25
func TestLifecycleScenario(t *testing.T) {
	s, r, counter := lifecycleScenario(TraceConfig{Level: TraceFull})
	if r != StopDeadline || counter != 7 || s.Executed() != 12 || s.Now() != sec(1) {
		t.Fatalf("RunUntil = %v, counter %d, Executed %d, Now %v", r, counter, s.Executed(), s.Now())
	}
	checkRecords(t, s, `
 1 0.000000000s 0/0 kernel.start cause=0 "start" [seed=0x0000000000000001 tie_break=seeded]
 2 0.000000000s 1/0 kernel.add_node cause=1 "n1" [tags=server offset_ns=0 drift_ppm=0]
 3 0.000000000s 1/0 kernel.event cause=2 "boot" [id=1]
 4 0.000000000s 1/1 kernel.boot cause=3 "boot" []
 5 0.100000000s 1/1 kernel.event cause=4 "tick" [id=6]
 6 0.100000000s 1/1 kernel.log cause=5 "tick 1" []
 7 0.200000000s 1/1 kernel.event cause=6 "tick" [id=7]
 8 0.200000000s 1/1 kernel.log cause=7 "tick 2" []
 9 0.250000000s 0/0 kernel.event cause=2 "pause" [id=2]
10 0.250000000s 1/1 kernel.pause cause=9 "pause" []
11 0.300000000s 1/1 kernel.defer cause=8 "tick" [id=8]
12 0.500000000s 0/0 kernel.event cause=2 "resume" [id=3]
13 0.500000000s 1/1 kernel.resume cause=12 "resume" [deferred=1]
14 0.500000000s 1/1 kernel.event cause=8 "tick" [id=8]
15 0.500000000s 1/1 kernel.log cause=14 "tick 3" []
16 0.600000000s 1/1 kernel.event cause=15 "tick" [id=9]
17 0.600000000s 1/1 kernel.log cause=16 "tick 4" []
18 0.650000000s 0/0 kernel.event cause=2 "crash" [id=4]
19 0.650000000s 1/1 kernel.crash cause=18 "crash" [from=up]
20 0.700000000s 0/0 kernel.event cause=2 "restart" [id=5]
21 0.700000000s 1/2 kernel.boot cause=20 "boot" []
22 0.800000000s 1/2 kernel.event cause=21 "tick" [id=11]
23 0.800000000s 1/2 kernel.log cause=22 "tick 5" []
24 0.900000000s 1/2 kernel.event cause=23 "tick" [id=12]
25 0.900000000s 1/2 kernel.log cause=24 "tick 6" []
26 1.000000000s 1/2 kernel.event cause=25 "tick" [id=13]
27 1.000000000s 1/2 kernel.log cause=26 "tick 7" []
`)
	const want = 0x42a2518011c8bc6a
	if got := s.TraceHash(); got != want {
		t.Errorf("TraceHash = %#016x, want %#016x", got, want)
	}
	if got := HashRecords(s.Records()); got != want {
		t.Errorf("HashRecords(Records()) = %#016x", got)
	}
	if h, _, _ := lifecycleScenario(TraceConfig{}); h.TraceHash() != want {
		t.Errorf("TraceHash level: %#016x", h.TraceHash())
	}
}

// pauseScenario builds AT-KRN-29: node n1 with timers t1, t2, t3 and the global events pause,
// post, [crash, restart,] resume and g. resumeExtra runs in the resume callback after Resume.
func pauseScenario(tb TieBreak, crash bool, resumeExtra func(s *Sim, log *[]string)) (*Sim, []string) {
	s := New(Config{Seed: 3, TieBreak: tb, Trace: TraceConfig{Level: TraceFull}})
	var log []string
	add := func(label string) func() {
		return func() { log = append(log, fmt.Sprintf("%s@%dms", label, time.Duration(s.Now())/time.Millisecond)) }
	}
	n1 := s.AddNode("n1", func(n *Node) {
		n.After(10*time.Millisecond, "t1", add("t1"))
		n.After(20*time.Millisecond, "t2", add("t2"))
		n.After(20*time.Millisecond, "t3", add("t3"))
	})
	s.At(msec(5), "pause", n1.Pause)
	s.At(msec(15), "post", func() { n1.Post("p1", add("p1")) })
	if crash {
		s.At(msec(25), "crash", n1.Crash)
		s.At(msec(40), "restart", n1.Restart)
	}
	s.At(msec(30), "resume", func() {
		n1.Resume()
		if resumeExtra != nil {
			resumeExtra(s, &log)
		}
	})
	s.At(msec(30), "g", add("g"))
	if r := s.Run(); r != StopIdle {
		panic(fmt.Sprintf("pause scenario: Run = %v, Err() = %v", r, s.Err()))
	}
	return s, log
}

// AT-KRN-29
func TestPauseDeferResume(t *testing.T) {
	s, log := pauseScenario(TieBreakSeeded, false, nil)
	if got := strings.Join(log, ", "); got != "g@30ms, t1@30ms, p1@30ms, t2@30ms, t3@30ms" {
		t.Fatalf("log %q", got)
	}
	if s.Executed() != 9 {
		t.Fatalf("Executed %d", s.Executed())
	}
	rs := s.Records()
	want := map[int]string{
		7:  `7 0.010000000s 1/1 kernel.defer cause=4 "t1" [id=6]`,
		9:  `9 0.015000000s 1/1 kernel.defer cause=8 "p1" [id=9]`,
		10: `10 0.020000000s 1/1 kernel.defer cause=4 "t2" [id=7]`,
		11: `11 0.020000000s 1/1 kernel.defer cause=4 "t3" [id=8]`,
		14: `14 0.030000000s 1/1 kernel.resume cause=13 "resume" [deferred=4]`,
		15: `15 0.030000000s 1/1 kernel.event cause=4 "t1" [id=6]`,
		16: `16 0.030000000s 1/1 kernel.event cause=8 "p1" [id=9]`,
		17: `17 0.030000000s 1/1 kernel.event cause=4 "t2" [id=7]`,
		18: `18 0.030000000s 1/1 kernel.event cause=4 "t3" [id=8]`,
	}
	for seq, line := range want {
		if got := recLine(rs[seq-1]); got != line {
			t.Errorf("record %d: got %s, want %s", seq, got, line)
		}
	}
	if got := s.TraceHash(); got != 0x62954c3a267730f4 {
		t.Errorf("TraceHash = %#016x, want 0x62954c3a267730f4", got)
	}

	f, flog := pauseScenario(TieBreakFIFO, false, nil)
	if got := strings.Join(flog, ", "); got != "g@30ms, t1@30ms, p1@30ms, t2@30ms, t3@30ms" {
		t.Fatalf("fifo log %q", got)
	}
	resumeSeq, gSeq := uint64(0), uint64(0)
	for _, r := range f.Records() {
		if r.Kind == "kernel.event" && r.Text == "resume" {
			resumeSeq = r.Seq
		}
		if r.Kind == "kernel.event" && r.Text == "g" {
			gSeq = r.Seq
		}
	}
	if resumeSeq == 0 || resumeSeq > gSeq {
		t.Errorf("fifo: resume (record %d) did not execute before g (record %d)", resumeSeq, gSeq)
	}
	if got := f.TraceHash(); got != 0x7e0da2e156632093 {
		t.Errorf("fifo TraceHash = %#016x, want 0x7e0da2e156632093", got)
	}

	_, vlog := pauseScenario(TieBreakSeeded, false, func(s *Sim, log *[]string) {
		s.After(0, "g2", func() { *log = append(*log, "g2") })
	})
	if slices.Index(vlog, "g2") < slices.Index(vlog, "t3@30ms") {
		t.Errorf("g2 ran before t3: %v", vlog)
	}
}

// AT-KRN-30
func TestCrashWhilePaused(t *testing.T) {
	s, log := pauseScenario(TieBreakSeeded, true, nil)
	if got := strings.Join(log, ", "); got != "g@30ms, t1@50ms, t3@60ms, t2@60ms" {
		t.Fatalf("log %q", got)
	}
	if s.Executed() != 10 {
		t.Fatalf("Executed %d", s.Executed())
	}
	var crash, resume int
	for _, r := range s.Records() {
		if r.Kind == "kernel.crash" {
			crash++
			if r.Attrs[0].Value != "paused" {
				t.Errorf("crash record %s", recLine(r))
			}
		}
		if r.Kind == "kernel.resume" {
			resume++
		}
	}
	if crash != 1 || resume != 0 {
		t.Errorf("%d crash records, %d resume records", crash, resume)
	}
	if got := s.TraceHash(); got != 0x5de08f81f4cd4e18 {
		t.Errorf("TraceHash = %#016x, want 0x5de08f81f4cd4e18", got)
	}
}

// AT-KRN-31
func TestCancelDeferred(t *testing.T) {
	s := fullSim(1)
	ran := false
	var id EventID
	n := s.AddNode("n", func(n *Node) { id = n.After(time.Second, "t", func() { ran = true }) })
	s.RunUntil(0)
	n.Pause()
	s.RunUntil(sec(2))
	if r := lastRecord(s); r.Kind != "kernel.defer" {
		t.Fatalf("timer not deferred: %s", recLine(r))
	}
	if !n.Cancel(id) {
		t.Fatal("Cancel of a deferred entry = false")
	}
	if s.Cancel(id) {
		t.Fatal("second Cancel = true")
	}
	n.Resume()
	if r := lastRecord(s); r.Kind != "kernel.resume" || r.Attrs[0].Value != "0" {
		t.Fatalf("resume record %s", recLine(r))
	}
	s.Run()
	if ran {
		t.Fatal("cancelled deferred timer ran")
	}
}

// AT-KRN-26
func TestNoOpTransitions(t *testing.T) {
	s := fullSim(1)
	n := s.AddNode("n", noBoot)
	s.Run()
	unchanged := func(what string, op func()) {
		t.Helper()
		count, h := len(s.Records()), s.TraceHash()
		op()
		if len(s.Records()) != count || s.TraceHash() != h {
			t.Errorf("%s on a %v node emitted a record", what, n.State())
		}
	}
	unchanged("Restart", n.Restart)
	unchanged("Resume", n.Resume)
	n.Pause()
	unchanged("Pause", n.Pause)
	unchanged("Restart", n.Restart)
	n.Crash()
	if r := lastRecord(s); r.Kind != "kernel.crash" || r.Attrs[0].Value != "paused" {
		t.Fatalf("crash from paused: %s", recLine(r))
	}
	unchanged("Crash", n.Crash)
	unchanged("Pause", n.Pause)
	unchanged("Resume", n.Resume)
}

// AT-KRN-17 (deferred entries do not satisfy Step)
func TestStepDefersWithoutCounting(t *testing.T) {
	s := fullSim(1)
	n := s.AddNode("n", func(n *Node) { n.After(time.Second, "timer", func() {}) })
	s.Step()
	n.Pause()
	ran := false
	s.At(sec(2), "global", func() { ran = true })
	if !s.Step() {
		t.Fatal("Step = false")
	}
	rs := s.Records()
	if !ran || s.Executed() != 2 || rs[len(rs)-2].Kind != "kernel.defer" || rs[len(rs)-1].Text != "global" {
		t.Fatalf("ran %v executed %d records %s / %s", ran, s.Executed(), recLine(rs[len(rs)-2]), recLine(rs[len(rs)-1]))
	}
}

// KRN-037: a Sim whose only remaining work is deferred returns StopIdle.
func TestOnlyDeferredWorkIsIdle(t *testing.T) {
	s := New(Config{Seed: 1})
	n := s.AddNode("n", func(n *Node) { n.After(time.Second, "t", func() {}) })
	s.RunUntil(0)
	n.Pause()
	if r := s.Run(); r != StopIdle || s.Now() != sec(1) || s.Executed() != 1 {
		t.Fatalf("Run = %v, Now %v, Executed %d", r, s.Now(), s.Executed())
	}
}

// AT-KRN-47 (resume variant)
func TestAtFrontAfterResume(t *testing.T) {
	_, log := pauseScenario(TieBreakSeeded, false, func(s *Sim, log *[]string) {
		s.AtFront(s.Now(), "f", func() { *log = append(*log, "f@30ms") })
	})
	if got := strings.Join(log, ", "); got != "g@30ms, t1@30ms, p1@30ms, t2@30ms, t3@30ms, f@30ms" {
		t.Fatalf("log %q", got)
	}
}

// KRN-063: each pause cycle defers and resumes only its own entries, also when the node pauses again
// in the instant it resumes.
func TestPauseTwice(t *testing.T) {
	s := fullSim(1)
	var log []string
	add := func(label string) func() {
		return func() { log = append(log, fmt.Sprintf("%s@%dms", label, time.Duration(s.Now())/time.Millisecond)) }
	}
	n := s.AddNode("n", func(n *Node) {
		n.After(time.Second, "a", add("a"))
		n.After(3*time.Second, "b", add("b"))
	})
	s.At(msec(500), "pause", n.Pause)
	s.At(sec(2), "resume", n.Resume)
	s.At(msec(2500), "pause", n.Pause)
	s.At(sec(4), "resume", n.Resume)
	if r := s.Run(); r != StopIdle {
		t.Fatalf("Run = %v, Err() = %v", r, s.Err())
	}
	var deferred []string
	for _, r := range s.Records() {
		if r.Kind == "kernel.resume" {
			deferred = append(deferred, r.Attrs[0].Value)
		}
	}
	if got := strings.Join(log, ", "); got != "a@2000ms, b@4000ms" || !slices.Equal(deferred, []string{"1", "1"}) {
		t.Fatalf("log %q, deferred %v", got, deferred)
	}

	_, plog := pauseScenario(TieBreakSeeded, false, func(s *Sim, _ *[]string) {
		n1 := s.Lookup("n1")
		n1.Pause()
		s.At(msec(35), "resume2", n1.Resume)
	})
	if got := strings.Join(plog, ", "); got != "g@30ms, t1@35ms, p1@35ms, t2@35ms, t3@35ms" {
		t.Fatalf("pause in the resume instant: log %q", got)
	}
}

// KRN-036: step 6 comes after the MaxTime check (step 5) and before the MaxEvents check (step 7).
func TestDeferStepOrder(t *testing.T) {
	paused := func(cfg Config) *Sim {
		cfg.Seed, cfg.Trace = 1, TraceConfig{Level: TraceFull}
		s := New(cfg)
		n := s.AddNode("n", func(n *Node) { n.After(time.Second, "t", func() {}) })
		s.RunUntil(0)
		n.Pause()
		return s
	}
	for _, global := range []bool{false, true} {
		s := paused(Config{MaxEvents: 1})
		want := StopIdle
		if global {
			s.At(sec(2), "g", func() {})
			want = StopMaxEvents
		}
		if r := s.Run(); r != want || s.Now() != sec(1) || lastRecord(s).Kind != "kernel.defer" {
			t.Errorf("MaxEvents, global %v: Run = %v, Now %v, last record %s", global, r, s.Now(), recLine(lastRecord(s)))
		}
	}
	s := paused(Config{MaxTime: msec(500)})
	count := len(s.Records())
	if r := s.Run(); r != StopMaxTime || s.Now() != 0 || len(s.Records()) != count {
		t.Errorf("MaxTime: Run = %v, Now %v, %d new records", r, s.Now(), len(s.Records())-count)
	}
}

// KRN-063: Resume draws nothing from kernel/sched.
func TestResumeDrawsNothing(t *testing.T) {
	next := func(pause bool) uint64 {
		s := New(Config{Seed: 1})
		n := s.AddNode("n", func(n *Node) { n.After(time.Second, "t", func() {}) })
		s.RunUntil(0)
		if pause {
			n.Pause()
			s.RunUntil(sec(2))
			n.Resume()
		}
		s.Run()
		return s.sched.Uint64()
	}
	if want, got := next(false), next(true); got != want {
		t.Errorf("kernel/sched after a resume: %#x, want %#x", got, want)
	}
}
