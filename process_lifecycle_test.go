// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"runtime/pprof"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
)

func vt(d time.Duration) kernel.Time { return kernel.Time(d) }

func init() { //nolint:gochecknoinits // registers the child-process scenarios
	gorScenarios["gor-09"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 10 * time.Second}, func(w *World) {
			w.AddProcess("p1", func(ctx context.Context, p *Process) error {
				time.Sleep(time.Second)
				p.Go(func() { panic("x") })
				<-ctx.Done()
				return nil
			})
		})
	}
	gorScenarios["gor-10"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 3 * time.Second}, func(w *World) {
			w.AddProcess("p1", func(ctx context.Context, p *Process) error {
				p.Go(func() {
					time.Sleep(1500 * time.Millisecond)
					panic("boom")
				})
				<-ctx.Done()
				return nil
			})
			w.Plan(fault.Script(fault.Event{At: vt(time.Second), Kind: fault.KindCrash, Node: "p1"}))
		})
	}
	gorScenarios["gor-15"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: time.Second}, func(w *World) {
			n := w.AddProcess("p1", func(ctx context.Context, p *Process) error { <-ctx.Done(); return nil })
			w.RunFor(time.Millisecond)
			_ = w.Process(n.ID()).Do("x", func() {})
		})
	}
	gorScenarios["waitunpaused-on-driver"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 2 * time.Second}, func(w *World) {
			n := w.AddProcess("p1", func(ctx context.Context, p *Process) error { <-ctx.Done(); return nil })
			w.Plan(fault.Script(fault.Event{At: vt(time.Second), Kind: fault.KindPause, Node: "p1"}))
			w.Final("read paused", func() error {
				w.drv.Lock()
				w.drv.WaitUnpaused(n.ID(), n.Incarnation())
				w.drv.Unlock()
				return nil
			})
		})
	}
	gorScenarios["do-panic"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 10 * time.Second}, func(w *World) {
			w.AddProcess("p1", func(ctx context.Context, p *Process) error {
				time.Sleep(time.Second)
				err := p.Do("boom", func() { panic("y") })
				fmt.Printf("Do returned %v\n", err)
				<-ctx.Done()
				return nil
			})
		})
	}
	gorScenarios["go-fatal"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 3 * time.Second}, func(w *World) {
			w.AddProcess("p1", func(ctx context.Context, p *Process) error {
				time.Sleep(time.Second)
				p.Go(func() { w.T().Fatal("fatal in Go") })
				<-ctx.Done()
				fmt.Printf("cause: %v\n", context.Cause(ctx))
				return nil
			})
			w.Final("final", func() error {
				fmt.Println("final check ran")
				return nil
			})
		})
	}
	gorScenarios["ghost-fatal"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 3 * time.Second}, func(w *World) {
			w.AddProcess("p1", func(ctx context.Context, p *Process) error {
				p.Go(func() {
					time.Sleep(1500 * time.Millisecond)
					w.T().Fatal("fatal in a ghost")
				})
				<-ctx.Done()
				return nil
			})
			w.Plan(fault.Script(fault.Event{At: vt(time.Second), Kind: fault.KindCrash, Node: "p1"}))
			w.Final("final", func() error {
				fmt.Println("final check ran")
				return nil
			})
		})
	}
	gorScenarios["ghost-after-restart"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 5 * time.Second}, func(w *World) {
			w.AddProcess("p1", func(ctx context.Context, p *Process) error {
				if p.Incarnation() == 1 {
					p.Go(func() {
						time.Sleep(4 * time.Second)
						panic("late ghost")
					})
				}
				<-ctx.Done()
				return nil
			})
			w.Plan(fault.Script(
				fault.Event{At: vt(time.Second), Kind: fault.KindCrash, Node: "p1"},
				fault.Event{At: vt(2 * time.Second), Kind: fault.KindRestart, Node: "p1"},
			))
		})
	}
}

func TestATGOR02_DoRunsAtVirtualTime(t *testing.T) {
	var rec [][2]int64
	gw(t, Options{Duration: time.Second}, func(w *World) {
		rec = nil
		w.AddProcess("p1", func(ctx context.Context, p *Process) error {
			tk := time.NewTicker(100 * time.Millisecond)
			defer tk.Stop()
			for {
				select {
				case <-ctx.Done():
					return nil
				case <-tk.C:
					_ = p.Do("t", func() {
						rec = append(rec, [2]int64{int64(w.Sim.Now()), int64(time.Now().Sub(epoch))}) //nolint:staticcheck // the AT reads time.Now itself
					})
				}
			}
		})
	})
	if len(rec) != 10 {
		t.Fatalf("%d entries, want 10: %v", len(rec), rec)
	}
	for i, e := range rec {
		want := int64(time.Duration(i+1) * 100 * time.Millisecond)
		if e[0] != want || e[1] != want {
			t.Fatalf("entry %d = %v, want both %d", i, e, want)
		}
	}
}

// AT-GOR-04: the requests of one tick are numbered in node order; their kernel events run in the
// kernel's seeded tie-break order (GOR-025), the same in both runs of a seed.
func TestATGOR04_LogOrderAndDeterminism(t *testing.T) {
	sink := newRecordSink()
	gw(t, fullTrace(Options{Seeds: 10, CheckDeterminism: true, Duration: time.Second}), func(w *World) {
		for _, name := range []string{"a", "b", "c"} {
			w.AddProcess(name, func(ctx context.Context, p *Process) error {
				tk := time.NewTicker(100 * time.Millisecond)
				defer tk.Stop()
				for {
					select {
					case <-ctx.Done():
						return nil
					case <-tk.C:
						p.Logf("tick %s", p.Node.Name())
					}
				}
			})
		}
		sink.capture(w)
	})
	if n := len(sink.seeds()); n != 10 {
		t.Fatalf("%d seeds captured, want 10", n)
	}
	ticks, inNodeOrder := 0, 0
	want := []string{"tick a", "tick b", "tick c"}
	for _, seed := range sink.seeds() {
		rs := sink.get(seed)
		var times []kernel.Time
		logs, reqs := map[kernel.Time][]string{}, map[kernel.Time][]string{}
		for _, r := range ofKind(rs, "kernel.log") {
			if _, ok := logs[r.At]; !ok {
				times = append(times, r.At)
			}
			logs[r.At] = append(logs[r.At], r.Text)
		}
		for _, r := range ofKind(rs, "driver.request") {
			reqs[r.At] = append(reqs[r.At], attr(r, "key"))
		}
		if len(times) != 10 {
			t.Fatalf("seed %#x: kernel.log at %d instants, want 10", seed, len(times))
		}
		for _, tm := range times {
			if !slices.Equal(reqs[tm], []string{"log:tick a", "log:tick b", "log:tick c"}) {
				t.Fatalf("seed %#x at %v: driver.request keys %q, want node order a, b, c", seed, tm, reqs[tm])
			}
			if got := slices.Sorted(slices.Values(logs[tm])); !slices.Equal(got, want) {
				t.Fatalf("seed %#x at %v: kernel.log %q, want one per node", seed, tm, logs[tm])
			}
			ticks++
			if slices.Equal(logs[tm], want) {
				inNodeOrder++
			}
		}
	}
	if inNodeOrder == ticks {
		t.Fatalf("kernel.log in node order at all %d ticks; the seeded tie-break orders the nodes' events", ticks)
	}
}

func TestATGOR09_GoPanicFailsRun(t *testing.T) {
	res := runGorScenario(t, "gor-09", "FAULTLINE_SEED=0x1")
	a := res.artifact(t, 1)
	if res.code != 1 || a.Report.Failure == nil || a.Report.Failure.Kind != "panic" {
		t.Fatalf("exit %d failure %+v\n%s", res.code, a.Report.Failure, res.out)
	}
	panics := ofKind(a.Trace.Records, "driver.process.panic")
	if len(panics) != 1 || attr(panics[0], "error") != "x" || panics[0].Text != "process goroutine panic" {
		t.Fatalf("driver.process.panic %+v", panics)
	}
	fails := ofKind(a.Trace.Records, "kernel.fail")
	if len(fails) != 1 || !strings.Contains(fails[0].Text, `in event 0 "process.Go" on node "p1" (inc 1)`) {
		t.Fatalf("kernel.fail %+v, want PanicError.Label process.Go", fails)
	}
}

func TestATGOR10_GhostPanicIsLogged(t *testing.T) {
	res := runGorScenario(t, "gor-10", "FAULTLINE_SEED=0x1")
	if res.code != 0 || !strings.Contains(res.out, "ghost of p1 (incarnation 1) panicked after it stopped: boom") {
		t.Fatalf("exit %d\n%s", res.code, res.out)
	}
}

func TestATGOR15_DoOnDriverIsMisuse(t *testing.T) {
	res := runGorScenario(t, "gor-15", "FAULTLINE_RESULTS=$ARTIFACTS/results.jsonl")
	want := "faultline: Process.Do called on the driver goroutine (body, a check or a kernel callback); call fn directly"
	if res.code != 1 || !strings.Contains(res.out, want) {
		t.Fatalf("exit %d\n%s", res.code, res.out)
	}
	lines := res.results(t)
	if len(lines) != 1 || lines[0]["kind"] != "setup" || lines[0]["message"] != want {
		t.Fatalf("results lines %v, want one setup error with GOR-056's message\n%s", lines, res.out)
	}
}

func TestATGOR28_DoCancelledByCrash(t *testing.T) {
	sink := newRecordSink()
	var doErr atomic.Value
	var ran atomic.Bool
	gw(t, fullTrace(Options{Duration: 2 * time.Second}), func(w *World) {
		w.AddProcess("p1", func(ctx context.Context, p *Process) error {
			time.Sleep(600 * time.Millisecond)
			doErr.Store(errBox{p.Do("slow", func() { ran.Store(true) })})
			<-ctx.Done()
			return nil
		})
		w.Plan(fault.Script(
			fault.Event{At: vt(500 * time.Millisecond), Kind: fault.KindPause, Node: "p1"},
			fault.Event{At: vt(time.Second), Kind: fault.KindCrash, Node: "p1"},
		))
		sink.capture(w)
	})
	box, _ := doErr.Load().(errBox)
	if !errors.Is(box.err, ErrCrashed) || ran.Load() {
		t.Fatalf("Do returned %v, fn ran %v; want ErrCrashed and false", box.err, ran.Load())
	}
	cancels := ofKind(sink.only(t), "driver.cancel")
	if len(cancels) != 1 || cancels[0].Text != "cancel do do:slow: crashed" || attr(cancels[0], "reason") != "crashed" ||
		cancels[0].At != vt(time.Second) {
		t.Fatalf("driver.cancel %+v", cancels)
	}
}

func TestATGOR29_DoKeyOrder(t *testing.T) {
	var mu sync.Mutex
	orders := map[uint64]string{}
	seeds := 0
	gw(t, Options{Seeds: 20, Duration: time.Second}, func(w *World) {
		seeds = w.Options().Seeds
		order := ""
		w.AddProcess("p1", func(ctx context.Context, p *Process) error {
			done := make(chan struct{})
			p.Go(func() {
				_ = p.Do("a", func() { order += "a" })
				close(done)
			})
			_ = p.Do("b", func() { order += "b" })
			<-done
			mu.Lock()
			orders[w.Seed()] = order
			mu.Unlock()
			<-ctx.Done()
			return nil
		})
		w.AddProcess("p2", func(ctx context.Context, p *Process) error {
			_ = p.Do("z", func() {})
			<-ctx.Done()
			return nil
		})
	})
	if len(orders) != seeds {
		t.Fatalf("%d of %d seeds recorded", len(orders), seeds)
	}
	for seed, o := range orders {
		if o != "ab" {
			t.Fatalf("seed %#x: order %q, want ab (key do:a < do:b)", seed, o)
		}
	}
}

// AT-GOR-05 without the network part (Listen and Accept need the netsim shims): contexts, ghost Do,
// incarnations and per-incarnation streams across a crash and restart, twice with one seed.
func TestGoroutineCrashRestartGhost(t *testing.T) {
	type obs struct {
		inc         uint32
		rand        uint64
		start       kernel.Time
		cause, late error
		lateRan     bool
	}
	run := func(t *testing.T) []obs {
		var mu sync.Mutex
		var out []obs
		gw(t, Options{BaseSeed: 7, Duration: 5 * time.Second}, func(w *World) {
			out = nil
			w.AddProcess("p1", func(ctx context.Context, p *Process) error {
				o := obs{inc: p.Incarnation(), rand: p.Rand().Uint64()}
				_ = p.Do("start", func() { o.start = w.Sim.Now() })
				<-ctx.Done()
				o.cause = context.Cause(ctx)
				o.late = p.Do("late", func() { o.lateRan = true })
				mu.Lock()
				out = append(out, o)
				mu.Unlock()
				return nil
			})
			w.Plan(fault.Script(
				fault.Event{At: vt(2 * time.Second), Kind: fault.KindCrash, Node: "p1"},
				fault.Event{At: vt(3 * time.Second), Kind: fault.KindRestart, Node: "p1"},
			))
		})
		mu.Lock()
		defer mu.Unlock()
		return append([]obs(nil), out...)
	}
	var first, second []obs
	t.Run("run1", func(t *testing.T) { first = run(t) })
	t.Run("run2", func(t *testing.T) { second = run(t) })
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("observations %v / %v, want 2 each", first, second)
	}
	i1, i2 := first[0], first[1]
	if i1.inc != 1 || !errors.Is(i1.cause, ErrCrashed) || !errors.Is(i1.late, ErrCrashed) || i1.lateRan || i1.start != 0 {
		t.Fatalf("incarnation 1: %+v", i1)
	}
	if i2.inc != 2 || i2.start != vt(3*time.Second) || !errors.Is(i2.cause, ErrRunEnded) || !errors.Is(i2.late, ErrRunEnded) || i2.lateRan {
		t.Fatalf("incarnation 2: %+v", i2)
	}
	if i1.rand == i2.rand || i1.rand != second[0].rand || i2.rand != second[1].rand {
		t.Fatalf("rand values %d %d / %d %d", i1.rand, i2.rand, second[0].rand, second[1].rand)
	}
}

func TestGoroutineLogfOrderWithinNode(t *testing.T) {
	sink := newRecordSink()
	gw(t, fullTrace(Options{Duration: time.Second}), func(w *World) {
		w.AddProcess("p1", func(ctx context.Context, p *Process) error {
			p.Go(func() { p.Logf("a") })
			p.Logf("b")
			<-ctx.Done()
			return nil
		})
		sink.capture(w)
	})
	var got []string
	for _, r := range ofKind(sink.only(t), "kernel.log") {
		got = append(got, r.Text)
	}
	if !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("kernel.log %q, want a, b (key order)", got)
	}
}

func TestGoroutineGoNilIsMisuse(t *testing.T) {
	var msg atomic.Value
	gw(t, Options{Duration: time.Second}, func(w *World) {
		w.AddProcess("p1", func(ctx context.Context, p *Process) error {
			defer func() { msg.Store(fmt.Sprint(recover())) }()
			p.Go(nil)
			return nil
		})
	})
	if got, _ := msg.Load().(string); got != "faultline: Process.Go: nil function" {
		t.Fatalf("recovered %q", got)
	}
}

func TestGoroutineWaitUnpaused(t *testing.T) {
	var resumedAt, resumedGoAt atomic.Int64
	var resumed, resumedGo atomic.Bool
	var crashed atomic.Int32 // 1 true, 2 false
	gw(t, Options{Duration: 4 * time.Second}, func(w *World) {
		wait := func(p *Process) bool {
			p.d.Lock()
			defer p.d.Unlock()
			return p.d.WaitUnpaused(p.Node.ID(), p.Incarnation())
		}
		w.AddProcess("p1", func(ctx context.Context, p *Process) error {
			time.Sleep(1500 * time.Millisecond)
			done := make(chan struct{})
			p.Go(func() { // a second waiter on the same node
				resumedGo.Store(wait(p))
				resumedGoAt.Store(int64(time.Since(epoch)))
				close(done)
			})
			resumed.Store(wait(p))
			resumedAt.Store(int64(time.Since(epoch)))
			<-done
			<-ctx.Done()
			return nil
		})
		w.AddProcess("p2", func(ctx context.Context, p *Process) error {
			time.Sleep(1500 * time.Millisecond)
			if wait(p) {
				crashed.Store(1)
			} else {
				crashed.Store(2)
			}
			return nil
		})
		w.Plan(fault.Script(
			fault.Event{At: vt(time.Second), Kind: fault.KindPause, Node: "p1"},
			fault.Event{At: vt(time.Second), Kind: fault.KindPause, Node: "p2"},
			fault.Event{At: vt(2 * time.Second), Kind: fault.KindResume, Node: "p1"},
			fault.Event{At: vt(2500 * time.Millisecond), Kind: fault.KindCrash, Node: "p2"},
		))
	})
	if !resumed.Load() || time.Duration(resumedAt.Load()) != 2*time.Second ||
		!resumedGo.Load() || time.Duration(resumedGoAt.Load()) != 2*time.Second {
		t.Fatalf("p1: WaitUnpaused %v at %v and %v at %v, want both true at 2s", resumed.Load(),
			time.Duration(resumedAt.Load()), resumedGo.Load(), time.Duration(resumedGoAt.Load()))
	}
	if crashed.Load() != 2 {
		t.Fatalf("p2: WaitUnpaused returned %d, want false (2) after the crash", crashed.Load())
	}
}

func TestGoroutineWaitUnpausedOnDriverIsMisuse(t *testing.T) {
	res := runGorScenario(t, "waitunpaused-on-driver", "FAULTLINE_SEED=0x1")
	want := "faultline: fsx call on paused node p1 from the driver goroutine; read its files with w.Disk.Volume(n) instead"
	if res.code != 1 || !strings.Contains(res.out, want) || !strings.Contains(res.out, `panic in final check "read paused"`) {
		t.Fatalf("exit %d\n%s", res.code, res.out)
	}
}

// GOR-041, GOR-051 and GOR-048: a waiter woken at T returns from WaitUnpaused before the next
// kernel event at T runs, whether a resume event, a crash made by body or an exit woke it.
func TestGoroutineWaitUnpausedBeforeNextEvent(t *testing.T) {
	var mu sync.Mutex
	returned := map[string]bool{}
	seen := map[string]bool{}
	gw(t, Options{Duration: 3 * time.Second}, func(w *World) {
		d := w.drv
		main := func(ctx context.Context, p *Process) error {
			time.Sleep(1500 * time.Millisecond)
			d.Lock()
			d.WaitUnpaused(p.Node.ID(), p.Incarnation())
			d.Unlock()
			mu.Lock()
			returned[p.Node.Name()] = true
			mu.Unlock()
			<-ctx.Done()
			return nil
		}
		check := func(name string) func() {
			return func() {
				mu.Lock()
				seen[name] = returned[name]
				mu.Unlock()
			}
		}
		w.AddProcess("resumed", main)
		crashed := w.AddProcess("crashed", main)
		exited := w.AddProcess("exited", main)
		var faults []fault.Event
		for _, name := range []string{"resumed", "crashed", "exited"} {
			faults = append(faults, fault.Event{At: vt(time.Second), Kind: fault.KindPause, Node: name})
		}
		faults = append(faults, fault.Event{At: vt(2 * time.Second), Kind: fault.KindResume, Node: "resumed"})
		w.Plan(fault.Script(faults...))
		w.Sim.At(vt(2*time.Second), "check resumed", check("resumed")) // after the fault event
		w.RunFor(1600 * time.Millisecond)                              // the waiters wait
		w.Sim.At(w.Sim.Now(), "check crashed", check("crashed"))       // the only event at 1.6s
		crashed.Crash()
		w.RunFor(100 * time.Millisecond)
		w.Sim.At(w.Sim.Now(), "check exited", check("exited")) // the only event at 1.7s
		d.endIncarnation(d.proc(exited.ID(), 1), incExited, ErrExited, "exited")
	})
	for _, name := range []string{"resumed", "crashed", "exited"} {
		if !seen[name] {
			t.Errorf("%s: WaitUnpaused had not returned when the next event at that instant ran", name)
		}
	}
}

// GOR-051: a crash after an exit leaves the exited incarnation's status and end time alone.
func TestGoroutineCrashAfterExitKeepsStatus(t *testing.T) {
	var status incStatus
	var endAt kernel.Time
	gw(t, Options{Duration: 3 * time.Second}, func(w *World) {
		d := w.drv
		n := w.AddProcess("p1", func(ctx context.Context, p *Process) error {
			time.Sleep(time.Second)
			return nil // exits at 1s; the node stays up
		})
		w.Plan(fault.Script(fault.Event{At: vt(2 * time.Second), Kind: fault.KindCrash, Node: "p1"}))
		w.Final("status", func() error {
			d.Lock()
			defer d.Unlock()
			p := d.proc(n.ID(), 1)
			status, endAt = p.status, p.endAt
			return nil
		})
	})
	if status != incExited || endAt != vt(time.Second) {
		t.Fatalf("status %d end %v, want exited (%d) at 1s", status, endAt, incExited)
	}
}

// GOR-041 and E1: a resume made by body wakes the waiter at the start of the next loop pass, an
// exit wakes it with false, and the end of the run wakes a waiter whose node stays paused.
func TestGoroutineWaitUnpausedWakeUps(t *testing.T) {
	type result struct {
		ok bool
		at time.Duration
	}
	var mu sync.Mutex
	results := map[string]result{}
	gw(t, Options{Duration: 3 * time.Second}, func(w *World) {
		d := w.drv
		main := func(ctx context.Context, p *Process) error {
			time.Sleep(100 * time.Millisecond)
			d.Lock()
			ok := d.WaitUnpaused(p.Node.ID(), p.Incarnation())
			d.Unlock()
			mu.Lock()
			results[p.Node.Name()] = result{ok, time.Since(epoch)}
			mu.Unlock()
			<-ctx.Done()
			return nil
		}
		resumed := w.AddProcess("resumed", main)
		exited := w.AddProcess("exited", main)
		paused := w.AddProcess("paused", main)
		w.RunFor(time.Millisecond) // the boots ran
		resumed.Pause()
		exited.Pause()
		paused.Pause()
		w.Sim.AtFront(vt(2*time.Second), "exit", func() {
			d.endIncarnation(d.proc(exited.ID(), 1), incExited, ErrExited, "exited")
		})
		w.RunFor(time.Second)
		resumed.Resume() // body: no kernel event follows before 2s
	})
	want := map[string]result{
		"resumed": {true, 1001 * time.Millisecond},
		"exited":  {false, 2 * time.Second},
		"paused":  {false, 3 * time.Second},
	}
	for _, name := range []string{"resumed", "exited", "paused"} {
		if got, ok := results[name]; !ok || got != want[name] {
			t.Errorf("%s: WaitUnpaused %+v (returned %v), want %+v", name, got, ok, want[name])
		}
	}
}

// GOR-031 to GOR-033 with pauses and a restart: requests queued while their node is paused run
// after the resume in req order (x1 before x2, key order), a later window's after an earlier
// one's; a crash cancels the queued ones, and the next incarnation's requests run. A crash of a
// node that is not a process changes nothing here.
func TestGoroutineRequestsAcrossPauseAndRestart(t *testing.T) {
	sink := newRecordSink()
	gw(t, fullTrace(Options{Duration: 4 * time.Second}), func(w *World) {
		w.AddProcess("p1", func(ctx context.Context, p *Process) error {
			if p.Incarnation() == 2 {
				p.Logf("inc 2")
				<-ctx.Done()
				return nil
			}
			time.Sleep(600 * time.Millisecond)
			p.Logf("x2")
			p.Logf("x1")
			time.Sleep(100 * time.Millisecond)
			p.Logf("y")
			time.Sleep(1400 * time.Millisecond)
			p.Logf("z")
			<-ctx.Done()
			return nil
		})
		w.Sim.AddNode("plain", func(*kernel.Node) {})
		w.Plan(fault.Script(
			fault.Event{At: vt(500 * time.Millisecond), Kind: fault.KindPause, Node: "p1"},
			fault.Event{At: vt(time.Second), Kind: fault.KindResume, Node: "p1"},
			fault.Event{At: vt(2 * time.Second), Kind: fault.KindPause, Node: "p1"},
			fault.Event{At: vt(2500 * time.Millisecond), Kind: fault.KindCrash, Node: "plain"},
			fault.Event{At: vt(2500 * time.Millisecond), Kind: fault.KindCrash, Node: "p1"},
			fault.Event{At: vt(3 * time.Second), Kind: fault.KindRestart, Node: "p1"},
		))
		sink.capture(w)
	})
	rs := sink.only(t)
	var got []string
	for _, r := range rs {
		switch r.Kind {
		case "kernel.log", "driver.cancel":
			got = append(got, fmt.Sprintf("%v %d %s", r.At, r.Inc, r.Text))
		}
	}
	want := []string{
		"1.000000000s 1 x1",
		"1.000000000s 1 x2",
		"1.000000000s 1 y",
		"2.500000000s 1 cancel log log:z: crashed",
		"3.000000000s 2 inc 2",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("records\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// GOR-056: a panic in fn is a kernel event panic of the request's event, and Do returns nil.
func TestGoroutineDoPanicFailsRun(t *testing.T) {
	res := runGorScenario(t, "do-panic", "FAULTLINE_SEED=0x1")
	a := res.artifact(t, 1)
	if res.code != 1 || a.Report.Failure == nil || a.Report.Failure.Kind != "panic" || !strings.Contains(res.out, "Do returned <nil>") {
		t.Fatalf("exit %d failure %+v\n%s", res.code, a.Report.Failure, res.out)
	}
	panics := ofKind(a.Trace.Records, "kernel.panic")
	if len(panics) != 1 || panics[0].Text != "y" || attr(panics[0], "label") != "req do" || panics[0].At != vt(time.Second) ||
		panics[0].Node != 1 || panics[0].Inc != 1 {
		t.Fatalf("kernel.panic %+v, want the req do event of p1 at 1s", panics)
	}
}

// GOR-016 and GOR-057: Logf on the driver while it holds the world lock panics with the message
// that names Process.Logf; a check runs without the lock, so Logf works there.
func TestGoroutineLogfOnDriver(t *testing.T) {
	var got string
	var fromCheck atomic.Bool
	gw(t, Options{Duration: time.Second}, func(w *World) {
		n := w.AddProcess("p1", func(ctx context.Context, p *Process) error { <-ctx.Done(); return nil })
		w.RunFor(time.Millisecond)
		p := w.Process(n.ID())
		func() {
			defer func() { got = fmt.Sprint(recover()) }()
			p.Logf("from body")
		}()
		w.Final("logf", func() error {
			p.Logf("from a check")
			fromCheck.Store(true)
			return nil
		})
	})
	want := "faultline: Process.Logf called on the driver goroutine while it holds the world lock (body, a kernel event, a boot function, a hook or a Process.Do function); use the node's kernel methods (Node.Logf, Node.Now, Node.LocalTime, Node.Drift)"
	if got != want || !fromCheck.Load() {
		t.Fatalf("body: recovered %q, want %q; Logf from a check returned %v", got, want, fromCheck.Load())
	}
}

// GOR-058: a goroutine started by Go on the driver carries the process's pprof labels, not the
// driver's.
func TestGoroutineGoFromDriverLabels(t *testing.T) {
	var profile string
	gw(t, Options{Duration: time.Second}, func(w *World) {
		n := w.AddProcess("p1", func(ctx context.Context, p *Process) error { <-ctx.Done(); return nil })
		w.RunFor(time.Millisecond)
		release := make(chan struct{})
		w.Process(n.ID()).Go(func() { goLabelsMarker(release) })
		w.RunFor(time.Millisecond) // the goroutine runs and blocks
		var b bytes.Buffer
		if err := pprof.Lookup("goroutine").WriteTo(&b, 1); err != nil {
			t.Error(err)
		}
		profile = b.String()
		close(release)
	})
	for _, group := range strings.Split(profile, "\n\n") {
		if strings.Contains(group, "goLabelsMarker") {
			if !strings.Contains(group, `"faultline_node":"1"`) || !strings.Contains(group, `"faultline_inc":"1"`) {
				t.Fatalf("goroutine labels of the Go goroutine:\n%s", group)
			}
			return
		}
	}
	t.Fatalf("goLabelsMarker not in the goroutine profile:\n%s", profile)
}

//go:noinline
func goLabelsMarker(release chan struct{}) { <-release }

// GOR-058: a goroutine started by Go carries the labels of Go's incarnation, also when a goroutine
// of another process calls Go.
func TestGoroutineGoFromOtherProcessLabels(t *testing.T) {
	var profile string
	gw(t, Options{Duration: time.Second}, func(w *World) {
		var p1 atomic.Pointer[Process]
		release := make(chan struct{})
		w.AddProcess("p1", func(ctx context.Context, p *Process) error {
			p1.Store(p)
			<-ctx.Done()
			return nil
		})
		w.AddProcess("p2", func(ctx context.Context, p *Process) error {
			time.Sleep(100 * time.Millisecond)
			p1.Load().Go(func() { goLabelsMarker(release) })
			<-ctx.Done()
			return nil
		})
		w.RunFor(200 * time.Millisecond) // p2 called Go, and the goroutine blocks
		var b bytes.Buffer
		if err := pprof.Lookup("goroutine").WriteTo(&b, 1); err != nil {
			t.Error(err)
		}
		profile = b.String()
		close(release)
	})
	for _, group := range strings.Split(profile, "\n\n") {
		if strings.Contains(group, "goLabelsMarker") {
			if !strings.Contains(group, `"faultline_node":"1"`) || !strings.Contains(group, `"faultline_inc":"1"`) {
				t.Fatalf("goroutine labels of p1's Go goroutine started by p2:\n%s", group)
			}
			return
		}
	}
	t.Fatalf("goLabelsMarker not in the goroutine profile:\n%s", profile)
}

// GOR-058's Goexit row: t.Fatal in a Process.Go function stops the run at that instant, as it does
// in main; the final check does not run, and the incarnation stays running until the run ends.
func TestGoroutineGoFatalStopsRun(t *testing.T) {
	res := runGorScenario(t, "go-fatal", "FAULTLINE_SEED=0x1")
	want := "faultline: seed 0x0000000000000001 stopped by t.FailNow or t.Fatal at t=1.000000000s (event 1); no artifacts were written"
	if res.code != 1 || !strings.Contains(res.out, want) || strings.Contains(res.out, "final check ran") ||
		!strings.Contains(res.out, "cause: faultline: run ended\n") {
		t.Fatalf("exit %d, want 1, %q, no final check and the cause ErrRunEnded\n%s", res.code, want, res.out)
	}
}

// GOR-058's Goexit row, not alive: t.Fatal in a goroutine of a crashed incarnation fails the test
// but does not stop the run; the final check runs, and the seed ends at the end of the run.
func TestGoroutineGhostFatalDoesNotStopRun(t *testing.T) {
	res := runGorScenario(t, "ghost-fatal", "FAULTLINE_SEED=0x1")
	if res.code != 1 || !strings.Contains(res.out, "fatal in a ghost") || !strings.Contains(res.out, "final check ran") ||
		!strings.Contains(res.out, "stopped by t.FailNow or t.Fatal at t=3.000000000s") {
		t.Fatalf("exit %d, want 1, the final check and a stop at the end of the run (3s)\n%s", res.code, res.out)
	}
}

// GOR-058: a bare runtime.Goexit in a Process.Go function ends that goroutine only, as a return
// does: the run goes on, the incarnation stays alive, and nothing is recorded for it.
func TestGoroutineGoexitInGo(t *testing.T) {
	sink := newRecordSink()
	var alive, deferred atomic.Bool
	gw(t, fullTrace(Options{Duration: 2 * time.Second}), func(w *World) {
		n := w.AddProcess("p1", func(ctx context.Context, p *Process) error {
			time.Sleep(time.Second)
			p.Go(func() {
				defer deferred.Store(true)
				runtime.Goexit()
			})
			<-ctx.Done()
			return nil
		})
		w.Final("alive", func() error {
			alive.Store(w.Process(n.ID()) != nil)
			return nil
		})
		sink.capture(w)
	})
	var kinds []string
	for _, r := range sink.only(t) {
		if strings.HasPrefix(r.Kind, "driver.process.") {
			kinds = append(kinds, r.Kind)
		}
	}
	if !alive.Load() || !deferred.Load() || !slices.Equal(kinds, []string{"driver.process.start"}) {
		t.Fatalf("alive %v, fn's defer ran %v, records %q; want true, true and only driver.process.start",
			alive.Load(), deferred.Load(), kinds)
	}
}

// GOR-052: a panic of incarnation 1's goroutine after the restart into incarnation 2 is a ghost
// panic: logged, and the seed passes.
func TestGoroutineGhostPanicAfterRestart(t *testing.T) {
	res := runGorScenario(t, "ghost-after-restart", "FAULTLINE_SEED=0x1")
	if res.code != 0 || !strings.Contains(res.out, "ghost of p1 (incarnation 1) panicked after it stopped: late ghost") {
		t.Fatalf("exit %d\n%s", res.code, res.out)
	}
}

// GOR-051 for a later incarnation: a crash of incarnation 2 cancels its context with ErrCrashed,
// and each crash sets its incarnation's status and end time.
func TestGoroutineCrashSecondIncarnation(t *testing.T) {
	var mu sync.Mutex
	causes := map[uint32]error{}
	var ends [2]kernel.Time
	var statuses [2]incStatus
	gw(t, Options{Duration: 5 * time.Second}, func(w *World) {
		n := w.AddProcess("p1", func(ctx context.Context, p *Process) error {
			<-ctx.Done()
			mu.Lock()
			causes[p.Incarnation()] = context.Cause(ctx)
			mu.Unlock()
			return nil
		})
		w.Plan(fault.Script(
			fault.Event{At: vt(time.Second), Kind: fault.KindCrash, Node: "p1"},
			fault.Event{At: vt(2 * time.Second), Kind: fault.KindRestart, Node: "p1"},
			fault.Event{At: vt(3 * time.Second), Kind: fault.KindCrash, Node: "p1"},
		))
		w.Final("incarnations", func() error {
			defer w.drv.acquire()()
			for i := range ends {
				p := w.drv.proc(n.ID(), uint32(i+1))
				ends[i], statuses[i] = p.endAt, p.status
			}
			return nil
		})
	})
	mu.Lock()
	defer mu.Unlock()
	if !errors.Is(causes[1], ErrCrashed) || !errors.Is(causes[2], ErrCrashed) {
		t.Fatalf("causes %v, want ErrCrashed for incarnations 1 and 2", causes)
	}
	if ends != [2]kernel.Time{vt(time.Second), vt(3 * time.Second)} || statuses != [2]incStatus{incCrashed, incCrashed} {
		t.Fatalf("end times %v, statuses %v; want 1s and 3s, both crashed (%d)", ends, statuses, incCrashed)
	}
}

// GOR-056: Do from a check, which runs on the driver goroutine without the world lock, panics with
// the misuse message too.
func TestGoroutineDoFromCheck(t *testing.T) {
	var got string
	gw(t, Options{Duration: time.Second}, func(w *World) {
		n := w.AddProcess("p1", func(ctx context.Context, p *Process) error { <-ctx.Done(); return nil })
		w.Final("do", func() error {
			defer func() { got = fmt.Sprint(recover()) }()
			_ = w.Process(n.ID()).Do("x", func() {})
			return nil
		})
	})
	if want := "faultline: Process.Do called on the driver goroutine (body, a check or a kernel callback); call fn directly"; got != want {
		t.Fatalf("recovered %q, want %q", got, want)
	}
}

// GOR-016 with goroutines running in parallel (Procs 4): a process goroutine that calls Logf while
// the driver holds the world lock waits for the lock; only the driver itself gets the misuse panic.
func TestGoroutineProcsLogfWaitsForLock(t *testing.T) {
	gw(t, Options{Procs: 4, Seeds: 3, Duration: 500 * time.Millisecond}, func(w *World) {
		for _, name := range []string{"a", "b", "c", "d"} {
			w.AddProcess(name, func(ctx context.Context, p *Process) error {
				for ctx.Err() == nil {
					_ = p.Do("x", func() {})
					for i := range 20 {
						p.Logf("after do %d", i)
					}
					time.Sleep(time.Millisecond)
				}
				return nil
			})
		}
	})
}

// GOR-031 and GOR-032: one node's requests of one window run one kernel event at a time in req
// order (byte order of the messages here, not call order), and the cause of each request's
// kernel.event is its own driver.request record.
func TestGoroutineRequestOrderAndCause(t *testing.T) {
	sink := newRecordSink()
	gw(t, fullTrace(Options{Duration: time.Second}), func(w *World) {
		for _, name := range []string{"p1", "p2"} {
			w.AddProcess(name, func(ctx context.Context, p *Process) error {
				time.Sleep(100 * time.Millisecond)
				p.Logf("m3")
				p.Logf("m1")
				p.Logf("m2")
				<-ctx.Done()
				return nil
			})
		}
		sink.capture(w)
	})
	rs := sink.only(t)
	bySeq := map[uint64]kernel.Record{}
	for _, r := range rs {
		bySeq[r.Seq] = r
	}
	logs := map[kernel.NodeID][]string{}
	events := 0
	for i, r := range rs {
		switch {
		case r.Kind == "kernel.log":
			logs[r.Node] = append(logs[r.Node], r.Text)
		case r.Kind == "kernel.event" && r.Text == "req log":
			events++
			c, next := bySeq[r.Cause], rs[i+1]
			if c.Kind != "driver.request" || c.Node != r.Node || next.Kind != "kernel.log" || attr(c, "key") != "log:"+next.Text {
				t.Fatalf("event %+v: cause %+v, first record %+v; want its own driver.request", r, c, next)
			}
		}
	}
	want := []string{"m1", "m2", "m3"}
	if events != 6 || !slices.Equal(logs[1], want) || !slices.Equal(logs[2], want) {
		t.Fatalf("%d request events, kernel.log %q; want 6 and m1, m2, m3 on each node", events, logs)
	}
}

// postNext reports a request event that the kernel would not schedule, on a down node, as an
// internal error instead of leaving the node's later requests waiting.
func TestGoroutinePostNextDownNode(t *testing.T) {
	var got string
	gw(t, Options{Duration: time.Second}, func(w *World) {
		d := w.drv
		n := w.AddProcess("p1", func(ctx context.Context, p *Process) error { <-ctx.Done(); return nil })
		w.RunFor(time.Millisecond)
		n.Crash()
		np := d.node(n.ID())
		np.pending = []*request{{node: n.ID(), inc: 1, kind: "test", key: "k", run: func() {}, cancel: func() {}}}
		func() {
			defer func() { got = fmt.Sprint(recover()) }()
			d.postNext(n.ID(), np)
		}()
		np.pending, np.posted = nil, nil
	})
	if want := "faultline: internal error: request event for a down node"; got != want {
		t.Fatalf("recovered %q, want %q", got, want)
	}
}
