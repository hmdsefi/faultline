// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
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

func init() { //nolint:gochecknoinits // registers the child-process scenarios
	gorScenarios["gor-07"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 10 * time.Second}, func(w *World) {
			w.AddProcess("p1", func(ctx context.Context, p *Process) error {
				time.Sleep(time.Second)
				return errors.New("boom")
			})
		})
	}
	gorScenarios["gor-08"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 10 * time.Second}, func(w *World) {
			w.AddProcess("p1", gor08Process())
		})
	}
	gorScenarios["gor-16"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 10 * time.Second}, func(w *World) {
			w.AddProcess("p1", func(ctx context.Context, p *Process) error {
				time.Sleep(time.Second)
				w.Logf("x")
				return nil
			})
		})
	}
	gorScenarios["gor-26"] = func(t *testing.T) {
		Run(t, Options{Seeds: 1, Duration: time.Second}, func(w *World) {
			w.AddProcess("p", func(ctx context.Context, p *Process) error { return nil })
		})
	}
	gorScenarios["process-error-cause"] = causeScenario(func(*World) error { return errors.New("boom") })
	gorScenarios["process-panic-cause"] = causeScenario(func(*World) error { panic("boom") })
	gorScenarios["process-failnow"] = causeScenario(func(w *World) error { w.T().FailNow(); return nil })
	gorScenarios["process-fatal"] = causeScenario(func(w *World) error { w.T().Fatal("fatal in p1"); return nil })
	gorScenarios["process-typed-nil"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 3 * time.Second}, func(w *World) {
			w.AddProcess("p1", func(ctx context.Context, p *Process) error {
				time.Sleep(time.Second)
				var err *nilError
				return err
			})
		})
	}
	gorScenarios["process-fail-drain"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 10 * time.Second}, func(w *World) {
			w.AddProcess("p1", func(ctx context.Context, p *Process) error { time.Sleep(time.Second); return errors.New("boom") })
			w.AddProcess("p2", func(ctx context.Context, p *Process) error { time.Sleep(time.Second); return nil })
		})
	}
	gorScenarios["process-from-check"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: time.Second}, func(w *World) {
			ch := make(chan struct{})
			n := w.AddProcess("p1", func(ctx context.Context, p *Process) error {
				<-ch
				return errors.New("late")
			})
			w.Final("wake p1", func() error {
				close(ch)
				time.Sleep(time.Millisecond) // p1 returns its error meanwhile
				fmt.Printf("Process(p1) nil: %t\n", w.Process(n.ID()) == nil)
				return nil
			})
		})
	}
	gorScenarios["final-fatal"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: time.Second}, func(w *World) {
			w.AddProcess("p1", func(ctx context.Context, p *Process) error { <-ctx.Done(); return nil })
			w.Final("fatal", func() error { w.T().Fatal("fatal in a final check"); return nil })
		})
	}
	gorScenarios["process-nil-main"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: time.Second}, func(w *World) {
			w.AddProcess("p", nil)
		})
	}
}

// causeScenario returns a scenario whose process p1 prints its context's cause from another
// goroutine and ends main at 1 s with end.
func causeScenario(end func(w *World) error) func(t *testing.T) {
	return func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 3 * time.Second}, func(w *World) {
			w.AddProcess("p1", func(ctx context.Context, p *Process) error {
				go func() {
					<-ctx.Done()
					fmt.Printf("cause: %v\n", context.Cause(ctx))
				}()
				time.Sleep(time.Second)
				return end(w)
			})
		})
	}
}

// nilError's Error method panics on a nil receiver, as a typed nil error's often does.
type nilError struct{ msg string }

func (e *nilError) Error() string { return e.msg }

// gor08Process returns AT-GOR-08's main; its panic site is gor08Process.func1. It is not inlined:
// a closure of an inlined function is named after its caller.
//
//go:noinline
func gor08Process() ProcessFunc {
	return func(ctx context.Context, p *Process) error {
		time.Sleep(time.Second)
		var m map[string]int
		m["x"] = 1 //nolint:staticcheck // the nil-map write is the panic under test
		return nil
	}
}

func TestATGOR01_SleepFollowsBubbleTime(t *testing.T) {
	var since, now atomic.Int64
	sink := newRecordSink()
	gw(t, fullTrace(Options{}), func(w *World) {
		w.AddProcess("p1", func(ctx context.Context, p *Process) error {
			t0 := time.Now()
			time.Sleep(1500 * time.Millisecond)
			since.Store(int64(time.Since(t0)))
			now.Store(int64(time.Now().Sub(epoch))) //nolint:staticcheck // AT-GOR-01 reads time.Now itself
			<-ctx.Done()
			return nil
		})
		sink.capture(w)
	})
	if time.Duration(since.Load()) != 1500*time.Millisecond || time.Duration(now.Load()) != 1500*time.Millisecond {
		t.Fatalf("recorded %v and %v, want 1.5s and 1.5s", time.Duration(since.Load()), time.Duration(now.Load()))
	}
	rs := sink.only(t)
	starts := ofKind(rs, "driver.process.start")
	if len(starts) != 1 || starts[0].Node != 1 || starts[0].Inc != 1 || starts[0].Text != "process start" || len(starts[0].Attrs) != 0 {
		t.Fatalf("driver.process.start records %+v", starts)
	}
	if exits := ofKind(rs, "driver.process.exit"); len(exits) != 0 {
		t.Fatalf("unexpected driver.process.exit %+v", exits)
	}
}

func TestATGOR03_UserTimerBeforeKernelEvent(t *testing.T) {
	var mu sync.Mutex
	seen := map[uint64]int64{}
	seeds := 0
	gw(t, Options{Seeds: 20, Duration: 2 * time.Second}, func(w *World) {
		seeds = w.Options().Seeds
		var x atomic.Int64
		w.AddServer("e1", func(n *kernel.Node) {
			w.Sim.At(kernel.Time(time.Second), "x", func() { x.Store(1) })
		})
		w.AddProcess("p1", func(ctx context.Context, p *Process) error {
			time.Sleep(time.Second)
			v := x.Load()
			mu.Lock()
			seen[w.Seed()] = v
			mu.Unlock()
			<-ctx.Done()
			return nil
		})
	})
	if len(seen) != seeds {
		t.Fatalf("%d of %d seeds recorded", len(seen), seeds)
	}
	for seed, v := range seen {
		if v != 0 {
			t.Fatalf("seed %#x: seen = %d, want 0 (GOR-025)", seed, v)
		}
	}
}

func TestATGOR07_ProcessReturnsError(t *testing.T) {
	res := runGorScenario(t, "gor-07", "FAULTLINE_SEED=0x1")
	if res.code != 1 {
		t.Fatalf("exit %d, want 1\n%s", res.code, res.out)
	}
	for _, want := range []string{
		"faultline: simulation failed at t=1.000000000s on p1 (event ",
		"  process p1 (incarnation 1) returned an error: boom",
	} {
		if !strings.Contains(res.out, want) {
			t.Fatalf("output lacks %q\n%s", want, res.out)
		}
	}
	a := res.artifact(t, 1)
	if a.Report.Failure == nil || a.Report.Failure.Signature != "fail:" {
		t.Fatalf("failure %+v, want signature fail:", a.Report.Failure)
	}
	rs := a.Trace.Records
	exit := indexWhere(rs, func(r kernel.Record) bool {
		return r.Kind == "driver.process.exit" && r.Text == "process exit: error" &&
			attr(r, "status") == "error" && attr(r, "error") == "boom"
	})
	fail := indexWhere(rs, func(r kernel.Record) bool { return r.Kind == "kernel.fail" })
	if exit < 0 || fail < 0 || exit > fail {
		t.Fatalf("driver.process.exit at %d, kernel.fail at %d; want exit before fail", exit, fail)
	}
	if rs[exit].At != kernel.Time(time.Second) || rs[fail].At != kernel.Time(time.Second) {
		t.Fatalf("driver.process.exit at t=%s, kernel.fail at t=%s; want both at 1s (SyncClock)", rs[exit].At, rs[fail].At)
	}
}

// GOR-049, GOR-050 and GOR-047's Goexit row: a process that returns an error, panics, or calls
// t.FailNow or t.Fatal ends its incarnation (context cause ErrExited), and the run fails at once.
func TestGoroutineProcessEndCause(t *testing.T) {
	stopped := "faultline: seed 0x0000000000000001 stopped by t.FailNow or t.Fatal at t=1.000000000s (event 1); no artifacts were written"
	for _, tc := range []struct{ scenario, want string }{
		{"process-error-cause", "faultline: simulation failed at t=1.000000000s on p1 (event 1)"},
		{"process-panic-cause", "faultline: panic at t=1.000000000s on p1 (event 1)"},
		{"process-failnow", stopped},
		{"process-fatal", stopped},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			res := runGorScenario(t, tc.scenario, "FAULTLINE_SEED=0x1")
			if res.code != 1 || !strings.Contains(res.out, "cause: faultline: process exited\n") || !strings.Contains(res.out, tc.want) {
				t.Fatalf("exit %d, want 1, the cause ErrExited and %q\n%s", res.code, tc.want, res.out)
			}
		})
	}
}

// An error whose Error method panics, here a typed nil, is a panic of main (GOR-047): the run fails
// with kind panic instead of the test binary crashing.
func TestGoroutineProcessTypedNilError(t *testing.T) {
	res := runGorScenario(t, "process-typed-nil", "FAULTLINE_SEED=0x1")
	if res.code != 1 {
		t.Fatalf("exit %d, want 1\n%s", res.code, res.out)
	}
	a := res.artifact(t, 1)
	exits := ofKind(a.Trace.Records, "driver.process.exit")
	if f := a.Report.Failure; f == nil || f.Kind != "panic" || f.At != "1.000000000s" ||
		len(exits) != 1 || attr(exits[0], "status") != "panic" ||
		attr(exits[0], "error") != "runtime error: invalid memory address or nil pointer dereference" {
		t.Fatalf("failure %+v, driver.process.exit %+v", f, exits)
	}
}

// GOR-020's order: a process failure at 1 s does not stop the driver before it drains the
// requests queued in the same window (p2's exit).
func TestGoroutineFailureDrainsWindow(t *testing.T) {
	res := runGorScenario(t, "process-fail-drain", "FAULTLINE_SEED=0x1")
	if res.code != 1 {
		t.Fatalf("exit %d, want 1\n%s", res.code, res.out)
	}
	reqs := ofKind(res.artifact(t, 1).Trace.Records, "driver.request")
	if len(reqs) != 1 || reqs[0].Node != 2 || reqs[0].At != kernel.Time(time.Second) || reqs[0].Text != "process.exit process.exit" {
		t.Fatalf("driver.request %+v, want p2's process.exit at 1s", reqs)
	}
}

// World.Process from a final check reads the driver's state under the world lock: p1, woken by the
// check, ends its incarnation with an error meanwhile, and -race sees no race.
func TestGoroutineProcessFromCheck(t *testing.T) {
	res := runGorScenario(t, "process-from-check", "FAULTLINE_SEED=0x1")
	if res.code != 1 || strings.Contains(res.out, "DATA RACE") || !strings.Contains(res.out, "Process(p1) nil: true\n") ||
		!strings.Contains(res.out, "process p1 (incarnation 1) returned an error: late") {
		t.Fatalf("exit %d, want 1, no race, Process nil and p1's error\n%s", res.code, res.out)
	}
}

// GOR-065: E1 takes the lock again after t.Fatal in a final check, which runs without it.
func TestGoroutineFatalInFinalCheck(t *testing.T) {
	res := runGorScenario(t, "final-fatal", "FAULTLINE_SEED=0x1")
	want := "faultline: seed 0x0000000000000001 stopped by t.FailNow or t.Fatal at t=1.000000000s (event 1); no artifacts were written"
	if res.code != 1 || !strings.Contains(res.out, want) {
		t.Fatalf("exit %d, want 1 and %q\n%s", res.code, want, res.out)
	}
}

func TestATGOR08_ProcessPanics(t *testing.T) {
	res := runGorScenario(t, "gor-08", "FAULTLINE_SEED=0x8")
	if res.code != 1 {
		t.Fatalf("exit %d\n%s", res.code, res.out)
	}
	f := res.artifact(t, 8).Report.Failure
	if f == nil || f.Kind != "panic" || !strings.HasSuffix(f.Check, "gor08Process.func1") {
		t.Fatalf("failure %+v, want kind panic at gor08Process.func1", f)
	}
	if f.At != "1.000000000s" { // the wake-up of GOR-050 reports the failure at once
		t.Fatalf("failure at %s, want 1.000000000s", f.At)
	}
	rs := res.artifact(t, 8).Trace.Records
	failRec := ofKind(rs, "kernel.fail")
	want := `kernel: panic at 1.000000000s in event 0 "process" on node "p1" (inc 1)`
	if len(failRec) != 1 || !strings.HasPrefix(failRec[0].Text, want) {
		t.Fatalf("kernel.fail %+v, want text starting %q", failRec, want)
	}
	exits := ofKind(rs, "driver.process.exit")
	if len(exits) != 1 || attr(exits[0], "status") != "panic" || attr(exits[0], "error") != "assignment to entry in nil map" {
		t.Fatalf("driver.process.exit %+v", exits)
	}
	exit := indexWhere(rs, func(r kernel.Record) bool { return r.Kind == "driver.process.exit" })
	fail := indexWhere(rs, func(r kernel.Record) bool { return r.Kind == "kernel.fail" })
	if exit > fail {
		t.Fatalf("driver.process.exit at %d, kernel.fail at %d; want exit before fail", exit, fail)
	}
}

func TestATGOR16_WorldMethodFromProcess(t *testing.T) {
	res := runGorScenario(t, "gor-16", "FAULTLINE_SEED=0x1")
	f := res.artifact(t, 1).Report.Failure
	if res.code != 1 || f == nil || f.Kind != "panic" || !strings.Contains(f.Message, "World.Logf called from a process goroutine") {
		t.Fatalf("exit %d failure %+v\n%s", res.code, f, res.out)
	}
}

func TestATGOR23_Procs(t *testing.T) {
	before := runtime.GOMAXPROCS(0)
	for _, tc := range []struct{ procs, want int }{{0, 1}, {4, 4}} {
		t.Run(fmt.Sprint(tc.procs), func(t *testing.T) {
			var got atomic.Int64
			gw(t, Options{Procs: tc.procs, Duration: time.Second}, func(w *World) {
				w.AddProcess("p1", func(ctx context.Context, p *Process) error {
					got.Store(int64(runtime.GOMAXPROCS(0)))
					return nil
				})
			})
			if int(got.Load()) != tc.want {
				t.Fatalf("GOMAXPROCS in main = %d, want %d", got.Load(), tc.want)
			}
			if runtime.GOMAXPROCS(0) != before {
				t.Fatalf("GOMAXPROCS after Run = %d, want %d", runtime.GOMAXPROCS(0), before)
			}
		})
	}
}

func TestATGOR24_SignalNotifyInProcess(t *testing.T) {
	gw(t, Options{Duration: time.Second}, func(w *World) {
		w.AddProcess("p1", func(ctx context.Context, p *Process) error {
			c := make(chan os.Signal, 1)
			signal.Notify(c, os.Interrupt)
			signal.Stop(c)
			return nil
		})
	})
}

func TestATGOR26_AddProcessInEventMode(t *testing.T) {
	res := runGorScenario(t, "gor-26")
	want := "faultline: World.AddProcess requires Options.Mode == faultline.ModeGoroutine"
	if res.code != 1 || !strings.Contains(res.out, want) {
		t.Fatalf("exit %d\n%s", res.code, res.out)
	}
}

func TestGoroutineAddProcessNilMain(t *testing.T) {
	res := runGorScenario(t, "process-nil-main")
	want := `faultline: World.AddProcess "p": nil ProcessFunc`
	if res.code != 1 || !strings.Contains(res.out, want) {
		t.Fatalf("exit %d\n%s", res.code, res.out)
	}
}

// GOR-048 and GOR-047's Goexit row: a process that returns nil, or calls runtime.Goexit without
// failing the test, exits; its node stays up.
func TestGoroutineProcessExitKeepsNodeUp(t *testing.T) {
	for _, goexit := range []bool{false, true} {
		t.Run(fmt.Sprintf("goexit=%t", goexit), func(t *testing.T) { testProcessExit(t, goexit) })
	}
}

func testProcessExit(t *testing.T, goexit bool) {
	sink := newRecordSink()
	var state kernel.NodeState
	var procNil, ctxErr atomic.Bool
	gw(t, fullTrace(Options{Duration: 2 * time.Second}), func(w *World) {
		n := w.AddProcess("p1", func(ctx context.Context, p *Process) error {
			time.Sleep(time.Second)
			go func() {
				<-ctx.Done()
				ctxErr.Store(errors.Is(context.Cause(ctx), ErrExited))
			}()
			if goexit {
				runtime.Goexit()
			}
			return nil
		})
		sink.capture(w)
		w.Final("state", func() error {
			state = n.State()
			procNil.Store(w.Process(n.ID()) == nil)
			return nil
		})
	})
	rs := sink.only(t)
	reqs := ofKind(rs, "driver.request")
	if len(reqs) != 1 || reqs[0].Text != "process.exit process.exit" || attr(reqs[0], "req") != "1" ||
		attr(reqs[0], "kind") != "process.exit" || attr(reqs[0], "key") != "process.exit" {
		t.Fatalf("driver.request %+v", reqs)
	}
	exits := ofKind(rs, "driver.process.exit")
	if len(exits) != 1 || exits[0].At != kernel.Time(time.Second) || exits[0].Text != "process exit: ok" ||
		attr(exits[0], "status") != "ok" || attr(exits[0], "error") != "" {
		t.Fatalf("driver.process.exit %+v", exits)
	}
	if state != kernel.NodeUp || !procNil.Load() || !ctxErr.Load() {
		t.Fatalf("state %v, Process nil %v, cause ErrExited %v", state, procNil.Load(), ctxErr.Load())
	}
}

func TestGoroutineProcessTagsAndRoles(t *testing.T) {
	var servers, clients, tags []string
	gw(t, Options{Duration: time.Millisecond}, func(w *World) {
		servers, clients = nil, nil
		quit := func(ctx context.Context, p *Process) error { return nil }
		w.AddProcess("s1", quit)
		w.AddClientProcess("c1", quit, kernel.WithTags("x"))
		w.AddServer("e1", func(*kernel.Node) {})
		for _, n := range w.Servers() {
			servers = append(servers, n.Name())
		}
		for _, n := range w.Clients() {
			clients = append(clients, n.Name())
		}
		tags = w.Sim.Lookup("c1").Tags()
	})
	if !slices.Equal(servers, []string{"s1", "e1"}) || !slices.Equal(clients, []string{"c1"}) ||
		!slices.Equal(tags, []string{"x", "client", "process"}) {
		t.Fatalf("servers %v clients %v tags %v", servers, clients, tags)
	}
}

// GOR-017: World methods panic on process goroutines; T, Seed, Options and End do not.
func TestGoroutineWorldMethodsOffDriver(t *testing.T) {
	var mu sync.Mutex
	var got []string
	var exemptOK atomic.Bool
	gw(t, Options{Duration: 2 * time.Second}, func(w *World) {
		got = nil
		w.AddProcess("p1", func(ctx context.Context, p *Process) error {
			noop := func(context.Context, *Process) error { return nil }
			calls := []struct {
				name string
				f    func()
			}{
				{"AddServer", func() { w.AddServer("x", func(*kernel.Node) {}) }},
				{"AddClient", func() { w.AddClient("x", func(*kernel.Node) {}) }},
				{"AddProcess", func() { w.AddProcess("x", noop) }},
				{"AddClientProcess", func() { w.AddClientProcess("x", noop) }},
				{"Servers", func() { w.Servers() }},
				{"Clients", func() { w.Clients() }},
				{"Invariant", func() { w.Invariant("i", func() error { return nil }) }},
				{"Final", func() { w.Final("f", func() error { return nil }) }},
				{"Plan", func() { w.Plan(fault.None()) }},
				{"Role", func() { w.Role("r", func() []kernel.NodeID { return nil }) }},
				{"RecoveryStart", func() { w.RecoveryStart() }},
				{"Rand", func() { w.Rand("x") }},
				{"RunFor", func() { w.RunFor(time.Millisecond) }},
				{"Logf", func() { w.Logf("x") }},
				{"Process", func() { w.Process(1) }},
			}
			for _, c := range calls {
				func() {
					defer func() {
						msg := fmt.Sprint(recover())
						mu.Lock()
						got = append(got, msg)
						mu.Unlock()
					}()
					c.f()
				}()
			}
			_, _, _, _ = w.T(), w.Seed(), w.Options(), w.End()
			exemptOK.Store(true)
			<-ctx.Done()
			return nil
		})
	})
	names := []string{"AddServer", "AddClient", "AddProcess", "AddClientProcess", "Servers", "Clients",
		"Invariant", "Final", "Plan", "Role", "RecoveryStart", "Rand", "RunFor", "Logf", "Process"}
	if len(got) != len(names) || !exemptOK.Load() {
		t.Fatalf("got %d messages %q, exempt ok %v", len(got), got, exemptOK.Load())
	}
	for i, name := range names {
		want := fmt.Sprintf("faultline: World.%s called from a process goroutine; World methods run on the driver goroutine (body, checks, kernel callbacks); use Process.Do", name)
		if got[i] != want {
			t.Fatalf("World.%s panicked with %q, want %q", name, got[i], want)
		}
	}
}

// GOR-030 to GOR-033, GOR-065 E1 and GOR-080's label, through the driver's Post: drain numbers and
// schedules the requests of one window in (node, key, arrival) order (the kernel's seeded
// tie-break then orders their events), a request posted for a dead incarnation is cancelled
// without a record, drain and exit cancel with driver.cancel records, and the end of the run
// cancels queued requests in arrival order without records and cancels contexts with ErrRunEnded.
func TestGoroutineRequestQueue(t *testing.T) {
	var sim *kernel.Sim
	var ran, cancelled []string
	var label string
	var p1Nil bool
	var endCause, aliveAfterEnd atomic.Bool
	gw(t, fullTrace(Options{Duration: time.Second}), func(w *World) {
		ran, cancelled, label, p1Nil = nil, nil, "", false
		sim = w.Sim
		d := w.drv
		wait := func(ctx context.Context, p *Process) error { <-ctx.Done(); return nil }
		p1 := w.AddProcess("p1", wait)
		p2 := w.AddProcess("p2", wait)
		p3 := w.AddProcess("p3", wait)
		p4 := w.AddProcess("p4", func(ctx context.Context, p *Process) error {
			<-ctx.Done()
			endCause.Store(errors.Is(context.Cause(ctx), ErrRunEnded))
			d.Lock()
			aliveAfterEnd.Store(d.Alive(p.Node.ID(), p.Incarnation()))
			d.Unlock()
			return context.Cause(ctx) // an error after the run ended does nothing (GOR-047)
		})
		post := func(n *kernel.Node, key, name string) {
			d.Post(n.ID(), 1, "test", key, func() { ran = append(ran, name) }, func() { cancelled = append(cancelled, name) })
		}
		w.RunFor(time.Millisecond) // the boots ran

		// Window 1: node, then key, then arrival.
		post(p2, "b", "p2b")
		post(p1, "b", "p1b")
		post(p2, "a", "p2a")
		post(p1, "a", "p1a1")
		post(p1, "a", "p1a2")
		w.RunFor(time.Millisecond)

		// Window 2: an exit cancels the incarnation's scheduled requests, and the kernel event of
		// a cancelled request does nothing. The AtFront event runs before the request's event.
		post(p1, "d", "p1d")
		w.Sim.AtFront(w.Sim.Now(), "exit p1", func() { d.exitOK(d.proc(p1.ID(), 1)) })
		w.RunFor(time.Millisecond)
		p1Nil = w.Process(p1.ID()) == nil
		post(p1, "e", "p1e") // dead incarnation: cancelled at once, no record

		// Window 3: incarnations that ended after Post are cancelled by drain.
		post(p2, "x", "p2x")
		post(p3, "y", "p3y1")
		post(p3, "y", "p3y2")
		d.endIncarnation(d.proc(p2.ID(), 1), incCrashed, ErrCrashed, "crashed")
		d.exitOK(d.proc(p3.ID(), 1))
		w.RunFor(time.Millisecond)

		w.Final("label", func() error {
			if l := d.ga.watch.label.Load(); l != nil {
				label = *l
			}
			return nil
		})
		w.Final("queue at end", func() error { // queued at the end: cancelled by E1 in arrival order
			d.Lock()
			defer d.Unlock()
			post(p4, "b", "p4b")
			post(p4, "a", "p4a")
			return nil
		})
	})
	slices.Sort(ran)
	if want := []string{"p1a1", "p1a2", "p1b", "p2a", "p2b"}; !slices.Equal(ran, want) {
		t.Fatalf("ran %v, want %v", ran, want)
	}
	if want := []string{"p1d", "p1e", "p2x", "p3y1", "p3y2", "p4b", "p4a"}; !slices.Equal(cancelled, want) {
		t.Fatalf("cancelled %v, want %v", cancelled, want)
	}
	if label != "req test" || !p1Nil || !endCause.Load() || aliveAfterEnd.Load() {
		t.Fatalf("label %q, Process(p1) nil %v, p4 cause ErrRunEnded %v, alive after end %v",
			label, p1Nil, endCause.Load(), aliveAfterEnd.Load())
	}
	rs := sim.Records() // read after E1: nothing is recorded after run.phase end
	if last := rs[len(rs)-1]; last.Kind != "run.phase" || attr(last, "phase") != "end" {
		t.Fatalf("last record %+v, want run.phase end", last)
	}
	var got []string
	events := 0
	for _, r := range rs {
		if r.Kind == "driver.request" || r.Kind == "driver.cancel" {
			got = append(got, fmt.Sprintf("%s %d/%d req=%s %s", r.Kind, r.Node, r.Inc, attr(r, "req"), r.Text))
		}
		if r.Kind == "kernel.event" && r.Text == "req test" {
			events++
		}
	}
	if events != 6 { // the five requests that ran and p1d, whose event does nothing
		t.Fatalf("%d kernel.event records labelled \"req test\", want 6", events)
	}
	want := []string{
		"driver.request 1/1 req=1 test a",
		"driver.request 1/1 req=2 test a",
		"driver.request 1/1 req=3 test b",
		"driver.request 2/1 req=4 test a",
		"driver.request 2/1 req=5 test b",
		"driver.request 1/1 req=6 test d",
		"driver.cancel 1/1 req=6 cancel test d: exited",
		"driver.cancel 2/1 req=7 cancel test x: crashed",
		"driver.cancel 3/1 req=8 cancel test y: exited",
		"driver.cancel 3/1 req=9 cancel test y: exited",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("request records\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	request := ofKind(rs, "driver.request")[0]
	if want := []kernel.Attr{{Key: "req", Value: "1"}, {Key: "kind", Value: "test"}, {Key: "key", Value: "a"}}; !slices.Equal(request.Attrs, want) {
		t.Fatalf("driver.request attrs %+v, want %+v", request.Attrs, want)
	}
	cancel := ofKind(rs, "driver.cancel")[0]
	if want := []kernel.Attr{{Key: "req", Value: "6"}, {Key: "kind", Value: "test"}, {Key: "key", Value: "d"}, {Key: "reason", Value: "exited"}}; !slices.Equal(cancel.Attrs, want) {
		t.Fatalf("driver.cancel attrs %+v, want %+v", cancel.Attrs, want)
	}
}

// GOR-031: more requests with equal keys than an insertion sort handles still keep arrival order.
func TestGoroutineDrainEqualKeysKeepArrivalOrder(t *testing.T) {
	var cancelled, want []string
	gw(t, Options{Duration: time.Second}, func(w *World) {
		cancelled, want = nil, nil
		d := w.drv
		p1 := w.AddProcess("p1", func(ctx context.Context, p *Process) error { <-ctx.Done(); return nil })
		w.RunFor(time.Millisecond)
		var a, b []string
		for i := range 40 {
			key := []string{"b", "a"}[i%2]
			name := fmt.Sprintf("%s%02d", key, i)
			if key == "a" {
				a = append(a, name)
			} else {
				b = append(b, name)
			}
			d.Post(p1.ID(), 1, "test", key, func() {}, func() { cancelled = append(cancelled, name) })
		}
		want = slices.Concat(a, b)
		d.endIncarnation(d.proc(p1.ID(), 1), incExited, ErrExited, "exited") // drain cancels them in req order
		w.RunFor(time.Millisecond)
	})
	if !slices.Equal(cancelled, want) {
		t.Fatalf("cancelled %v\nwant %v", cancelled, want)
	}
}

// GOR-031 and GOR-047: two processes that return at the same instant post their exits in one
// window, which drain numbers in node order whichever goroutine ran first; -race checks that both
// posted under the lock.
func TestGoroutineTwoExitsOneWindow(t *testing.T) {
	var sim *kernel.Sim
	gw(t, fullTrace(Options{Duration: 2 * time.Second}), func(w *World) {
		sim = w.Sim
		exit := func(ctx context.Context, p *Process) error { time.Sleep(time.Second); return nil }
		w.AddProcess("p1", exit)
		w.AddProcess("p2", exit)
	})
	var got []string
	for _, r := range sim.Records() {
		switch r.Kind {
		case "driver.request":
			got = append(got, fmt.Sprintf("request %d at %s req=%s", r.Node, r.At, attr(r, "req")))
		case "driver.process.exit":
			got = append(got, fmt.Sprintf("exit %d at %s", r.Node, r.At))
		}
	}
	if len(got) == 4 {
		slices.Sort(got[2:]) // the exits run in the seeded tie-break order
	}
	want := []string{"request 1 at 1.000000000s req=1", "request 2 at 1.000000000s req=2", "exit 1 at 1.000000000s", "exit 2 at 1.000000000s"}
	if !slices.Equal(got, want) {
		t.Fatalf("records %q, want %q", got, want)
	}
}

// GOR-003: after the run ended, AddProcess panics with API §7.2's phase message.
func TestGoroutineAddProcessAfterEnd(t *testing.T) {
	var got string
	gw(t, Options{Duration: time.Second}, func(w *World) {
		w.Final("late", func() error {
			defer func() { got = fmt.Sprint(recover()) }()
			w.AddClientProcess("late", func(context.Context, *Process) error { return nil })
			return nil
		})
	})
	want := "faultline: World.AddClientProcess called after the run ended; call it from body or from a callback during the run"
	if got != want {
		t.Fatalf("panic %q, want %q", got, want)
	}
}

// GOR-033 and E1: requests still scheduled when the run ends (here deferred on paused nodes) are
// cancelled in req order, not in node or arrival order.
func TestGoroutineEndCancelsScheduled(t *testing.T) {
	var cancelled []string
	gw(t, Options{Duration: time.Second}, func(w *World) {
		cancelled = nil
		d := w.drv
		wait := func(ctx context.Context, p *Process) error { <-ctx.Done(); return nil }
		p1, p2 := w.AddProcess("p1", wait), w.AddProcess("p2", wait)
		post := func(n *kernel.Node, key string) {
			d.Post(n.ID(), 1, "test", key, func() { t.Errorf("request %s%s ran", n.Name(), key) },
				func() { cancelled = append(cancelled, n.Name()+key) })
		}
		w.RunFor(time.Millisecond) // the boots ran
		p1.Pause()
		p2.Pause()
		post(p2, "x") // req 1
		w.RunFor(time.Millisecond)
		post(p1, "b") // req 3
		post(p1, "a") // req 2
	})
	if want := []string{"p2x", "p1a", "p1b"}; !slices.Equal(cancelled, want) {
		t.Fatalf("cancelled %v, want %v", cancelled, want)
	}
}

// GOR-046 steps 4 and 6 and GOR-059: Rand is safe for concurrent use (-race), the context carries
// the pprof labels, Context and Incarnation return the incarnation's values, and World.Process
// returns the live incarnation's Process from body and from a check.
func TestGoroutineProcessAccessors(t *testing.T) {
	var got atomic.Value
	var fromBody, fromCheck string
	gw(t, Options{Duration: time.Second}, func(w *World) {
		var started atomic.Pointer[Process] // the Process main received
		live := func(id kernel.NodeID) string {
			p := w.Process(id)
			if p == nil {
				return "nil"
			}
			return fmt.Sprintf("same=%t incarnation=%d", p == started.Load(), p.Incarnation())
		}
		n := w.AddProcess("p1", func(ctx context.Context, p *Process) error {
			started.Store(p)
			var wg sync.WaitGroup
			for range 2 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for range 100 {
						p.Rand().Uint64()
					}
				}()
			}
			wg.Wait()
			run, _ := pprof.Label(ctx, "faultline_run")
			node, _ := pprof.Label(ctx, "faultline_node")
			inc, _ := pprof.Label(ctx, "faultline_inc")
			got.Store(fmt.Sprintf("run=%t node=%s inc=%s ctx=%t incarnation=%d",
				run != "", node, inc, p.Context() == ctx, p.Incarnation()))
			<-ctx.Done()
			return nil
		})
		w.RunFor(time.Millisecond)
		fromBody = live(n.ID())
		w.Final("process", func() error {
			fromCheck = live(n.ID())
			return nil
		})
	})
	if want := "run=true node=1 inc=1 ctx=true incarnation=1"; got.Load() != want {
		t.Fatalf("got %q, want %q", got.Load(), want)
	}
	if want := "same=true incarnation=1"; fromBody != want || fromCheck != want {
		t.Fatalf("World.Process from body %q, from a check %q; want %q", fromBody, fromCheck, want)
	}
}
