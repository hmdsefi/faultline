// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
)

// ticker adds an event-style server that ticks every period and counts in *n.
func ticker(w *World, name string, period time.Duration, n *int) {
	w.AddServer(name, func(node *kernel.Node) {
		var tick func()
		tick = func() {
			*n++
			node.After(period, "tick", tick)
		}
		node.After(period, "tick", tick)
	})
}

func init() { //nolint:gochecknoinits // registers the child-process scenarios
	gorScenarios["driver-max-events"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: time.Second, MaxEvents: 1000}, func(w *World) {
			w.AddServer("n1", func(n *kernel.Node) {
				var spin func()
				spin = func() { n.Post("spin", spin) }
				n.Post("spin", spin)
			})
		})
	}
	gorScenarios["driver-lock-in-body"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: time.Second}, func(w *World) {
			w.drv.Lock() // a shim call from body (GOR-016)
		})
	}
	gorScenarios["driver-invariant-fails"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: time.Second}, func(w *World) {
			ticks := 0
			ticker(w, "n1", 100*time.Millisecond, &ticks)
			w.Invariant("three ticks", func() error {
				if ticks == 3 {
					return fmt.Errorf("ticked %d times", ticks)
				}
				return nil
			})
		})
	}
	gorScenarios["driver-failnow-in-body"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: time.Second}, func(w *World) {
			w.AddServer("n1", func(*kernel.Node) {})
			w.RunFor(10 * time.Millisecond) // past the first event, so API-101 writes its note
			d := w.drv
			go func() { // waits in Lock until the world lock is released
				d.Lock()
				_ = w.Sim.Now() // what a shim does under the lock
				d.Unlock()
			}()
			w.T().FailNow() // runtime.Goexit while the driver holds the world lock
		})
	}
	gorScenarios["driver-failnow-in-invariant"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: time.Second}, func(w *World) {
			ticks := 0
			ticker(w, "n1", 100*time.Millisecond, &ticks)
			w.Invariant("stops the test", func() error {
				if ticks == 3 {
					w.T().FailNow() // runtime.Goexit while the world lock is released (GOR-015)
				}
				return nil
			})
		})
	}
}

func TestGoroutineDriverTicker(t *testing.T) {
	var (
		ticks                   int
		nowBody, nowFinal       kernel.Time
		bubbleBody, bubbleFinal time.Duration
	)
	gw(t, Options{Duration: 2 * time.Second}, func(w *World) {
		ticks = 0
		ticker(w, "n1", 10*time.Millisecond, &ticks)
		w.RunFor(500 * time.Millisecond)
		nowBody, bubbleBody = w.Sim.Now(), time.Since(epoch)
		w.Final("times", func() error {
			nowFinal, bubbleFinal = w.Sim.Now(), time.Since(epoch)
			return nil
		})
	})
	if nowBody != kernel.Time(500*time.Millisecond) || bubbleBody != 500*time.Millisecond {
		t.Fatalf("after RunFor: kernel %v, bubble %v; want 0.5s both", nowBody, bubbleBody)
	}
	if nowFinal != kernel.Time(2*time.Second) || bubbleFinal != 2*time.Second {
		t.Fatalf("final: kernel %v, bubble %v; want 2s both (GOR-023)", nowFinal, bubbleFinal)
	}
	if ticks != 200 {
		t.Fatalf("ticks = %d, want 200", ticks)
	}
}

// GOR-025: a user timer at T runs before the kernel event at T.
func TestGoroutineDriverUserTimersRunFirst(t *testing.T) {
	var mu sync.Mutex
	seen := map[uint64]int64{}
	seeds := 0
	gw(t, Options{Seeds: 20, Duration: 2 * time.Second}, func(w *World) {
		seeds = w.Options().Seeds
		var x atomic.Int64
		go func() {
			time.Sleep(time.Second)
			x.Store(1)
		}()
		w.AddServer("e1", func(n *kernel.Node) {
			w.Sim.At(kernel.Time(time.Second), "x", func() {
				mu.Lock()
				seen[w.Seed()] = x.Load()
				mu.Unlock()
			})
		})
	})
	if len(seen) != seeds {
		t.Fatalf("%d of %d seeds recorded", len(seen), seeds)
	}
	for seed, v := range seen {
		if v != 1 {
			t.Fatalf("seed %#x: the kernel event at 1s ran before the user timer", seed)
		}
	}
}

// GOR-022: invariants run once after each executed event, after the goroutines woken by it have
// run (race-free with -race).
func TestGoroutineDriverInvariantsAfterQuiescence(t *testing.T) {
	var checks, executed uint64
	gw(t, Options{Duration: time.Second}, func(w *World) {
		checks, executed = 0, 0
		sent, got := 0, 0
		ch := make(chan int, 1000)
		go func() {
			for v := range ch {
				got = v
			}
		}()
		w.AddServer("e1", func(n *kernel.Node) {
			var tick func()
			tick = func() {
				sent++
				ch <- sent
				n.After(10*time.Millisecond, "send", tick)
			}
			n.After(10*time.Millisecond, "send", tick)
		})
		w.Invariant("receiver caught up", func() error {
			checks++
			if got != sent {
				return fmt.Errorf("got %d, sent %d", got, sent)
			}
			return nil
		})
		w.Final("stop the receiver", func() error {
			executed = w.Sim.Executed()
			close(ch)
			return nil
		})
	})
	if executed < 100 || checks != executed {
		t.Fatalf("invariant ran %d times for %d executed events; want one run per event, at least 100", checks, executed)
	}
}

func TestGoroutineDriverMaxEvents(t *testing.T) {
	res := runGorScenario(t, "driver-max-events", "FAULTLINE_SEED=0x1")
	want := "faultline: run exceeded MaxEvents (1000) at t=0.000000000s (event 1000)"
	if res.code != 1 || !strings.Contains(res.out, want) {
		t.Fatalf("exit %d, want 1 and %q\n%s", res.code, want, res.out)
	}
}

func TestGoroutineDriverShimCallFromBody(t *testing.T) {
	res := runGorScenario(t, "driver-lock-in-body")
	want := "faultline: netsim/fsx call on the driver goroutine while it holds the world lock (body, a kernel event, a boot function, a hook or a Process.Do function) would deadlock; prepare files with w.Disk.Volume(n).WriteFileDurable and use shims only from processes and checks"
	if res.code != 1 || !strings.Contains(res.out, want) {
		t.Fatalf("exit %d, want 1 and the GOR-016 message\n%s", res.code, res.out)
	}
}

// GOR-075: one checkpoint per executed event, equal to the trace hash right after it.
func TestGoroutineDriverCheckpoints(t *testing.T) {
	var n, executed uint64
	gw(t, Options{Duration: time.Second}, func(w *World) {
		ticks := 0
		ticker(w, "n1", 100*time.Millisecond, &ticks)
		w.Invariant("checkpoint is the hash after the event", func() error {
			cp := w.drv.checkpoints
			if len(cp) == 0 || cp[len(cp)-1] != w.Sim.TraceHash() {
				return fmt.Errorf("checkpoint mismatch after event %d", w.Sim.Executed())
			}
			return nil
		})
		w.Final("count", func() error {
			n, executed = uint64(len(w.drv.checkpoints)), w.Sim.Executed()
			return nil
		})
	})
	if n == 0 || n != executed {
		t.Fatalf("%d checkpoints for %d executed events", n, executed)
	}
}

func TestGoroutineDriverDriveRecords(t *testing.T) {
	sink := newRecordSink()
	gw(t, fullTrace(Options{Duration: time.Second}), func(w *World) {
		w.RunFor(100 * time.Millisecond)
		sink.capture(w)
	})
	var got []string
	for _, r := range ofKind(sink.only(t), "driver.drive") {
		got = append(got, fmt.Sprintf("%d/%d|%s|%s", r.Node, r.Inc, r.Text, attr(r, "until_ns")))
	}
	want := []string{
		"0/0|drive until 0.100000000s|100000000",
		"0/0|drive until 1.000000000s|1000000000",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("driver.drive records %q, want %q", got, want)
	}
}

func TestGoroutineDriverOnDriverAndSyncClock(t *testing.T) {
	var inBody, inGoroutine, inFinal atomic.Int32 // 1 true, 2 false
	var synced atomic.Int64
	set := func(v *atomic.Int32, b bool) {
		if b {
			v.Store(1)
		} else {
			v.Store(2)
		}
	}
	gw(t, Options{Duration: time.Second}, func(w *World) {
		d := w.drv
		set(&inBody, d.OnDriver())
		go func() {
			time.Sleep(250 * time.Millisecond)
			set(&inGoroutine, d.OnDriver())
			d.Lock()
			d.SyncClock()
			synced.Store(int64(w.Sim.Now()))
			d.Unlock()
		}()
		w.Final("on driver", func() error { set(&inFinal, d.OnDriver()); return nil })
	})
	if inBody.Load() != 1 || inGoroutine.Load() != 2 || inFinal.Load() != 1 {
		t.Fatalf("OnDriver body=%d goroutine=%d final=%d; want 1 2 1", inBody.Load(), inGoroutine.Load(), inFinal.Load())
	}
	if got := kernel.Time(synced.Load()); got != kernel.Time(250*time.Millisecond) {
		t.Fatalf("SyncClock moved kernel time to %v, want 0.25s", got)
	}
}

// GOR-015: invariant and final checks run without the world lock, so they may take it as a shim
// does. If the driver held it, Lock would panic with GOR-016's misuse and fail the run.
func TestGoroutineDriverChecksRunUnlocked(t *testing.T) {
	var invariants, finals atomic.Int32
	gw(t, Options{Duration: time.Second}, func(w *World) {
		invariants.Store(0)
		finals.Store(0)
		ticks := 0
		ticker(w, "t1", 100*time.Millisecond, &ticks)
		w.Invariant("takes the lock", func() error {
			w.drv.Lock()
			w.drv.SyncClock()
			w.drv.Unlock()
			invariants.Add(1)
			return nil
		})
		w.Final("takes the lock", func() error {
			w.drv.Lock()
			finals.Add(1)
			w.drv.Unlock()
			return nil
		})
	})
	if invariants.Load() < 10 || finals.Load() != 1 {
		t.Fatalf("invariant ran %d times, final %d times; want >= 10 and 1", invariants.Load(), finals.Load())
	}
}

// GOR-015, GOR-016: a goroutine that calls Lock while the driver holds the world lock waits until
// the driver releases it, here after body returns.
func TestGoroutineDriverLockExcludes(t *testing.T) {
	var atReturn, inFinal atomic.Int32
	gw(t, Options{Duration: time.Second}, func(w *World) {
		d := w.drv
		var started, passed atomic.Int32
		go func() {
			started.Store(1)
			d.Lock()
			passed.Store(1)
			d.Unlock()
		}()
		for started.Load() == 0 { // let the goroutine run until it blocks in Lock
			runtime.Gosched()
		}
		runtime.Gosched()
		atReturn.Store(passed.Load())
		w.Final("lock taken", func() error { inFinal.Store(passed.Load()); return nil })
	})
	if atReturn.Load() != 0 || inFinal.Load() != 1 {
		t.Fatalf("goroutine past Lock: %d when body returned, %d in the final check; want 0 then 1", atReturn.Load(), inFinal.Load())
	}
}

// GOR-080: the driver publishes the phase, progress, Now and Executed for the watchdog.
func TestGoroutineDriverWatchState(t *testing.T) {
	var (
		phaseBody, phaseInvRun, phaseFinal int32
		stale                              atomic.Int32
		progress, executed                 uint64
	)
	gw(t, Options{Duration: time.Second}, func(w *World) {
		stale.Store(0)
		phaseInvRun = -1
		ws := w.drv.ga.watch
		phaseBody = ws.phase.Load()
		ticks := 0
		ticker(w, "n1", 100*time.Millisecond, &ticks)
		w.RunFor(300 * time.Millisecond)
		w.Invariant("watch state", func() error {
			if ws.nowNS.Load() != int64(w.Sim.Now()) || ws.executed.Load() != w.Sim.Executed() {
				stale.Add(1)
			}
			phaseInvRun = ws.phase.Load()
			return nil
		})
		w.Final("watch state", func() error {
			phaseFinal = ws.phase.Load()
			progress, executed = ws.progress.Load(), w.Sim.Executed()
			return nil
		})
	})
	if phaseBody != watchBody || phaseInvRun != watchRun || phaseFinal != watchFinal {
		t.Fatalf("phases body=%d run=%d final=%d, want %d %d %d", phaseBody, phaseInvRun, phaseFinal, watchBody, watchRun, watchFinal)
	}
	if stale.Load() != 0 {
		t.Fatalf("nowNS or executed lagged the kernel in %d invariant runs", stale.Load())
	}
	if executed == 0 || progress < executed {
		t.Fatalf("progress %d for %d executed events; want at least one synctest.Wait per event", progress, executed)
	}
}

// GOR-022: a failing invariant fails the run at the event that broke it, and the loop stops with
// StopFailed.
func TestGoroutineDriverInvariantFailure(t *testing.T) {
	res := runGorScenario(t, "driver-invariant-fails", "FAULTLINE_SEED=0x1")
	want := `faultline: invariant "three ticks" violated at t=0.300000000s on n1 (event 4)`
	if res.code != 1 || !strings.Contains(res.out, want) {
		t.Fatalf("exit %d, want 1 and %q\n%s", res.code, want, res.out)
	}
	if stop := res.artifact(t, 1).Report.Run.Stop; stop != "failed" {
		t.Fatalf("report.json run.stop = %q, want %q", stop, "failed")
	}
}

// GOR-021: when bubble time has passed a pending kernel event, the loop's AdvanceTo panics with
// the internal-error message.
func TestGoroutineDriverAdvancePastEvent(t *testing.T) {
	var msg string
	gw(t, Options{Duration: time.Second}, func(w *World) {
		msg = ""
		w.RunFor(0)
		w.Sim.At(kernel.Time(50*time.Millisecond), "x", func() {})
		time.Sleep(100 * time.Millisecond) // bubble time passes the kernel event
		func() {
			defer func() { msg = fmt.Sprint(recover()) }()
			w.RunFor(time.Millisecond)
		}()
		w.Sim.RunUntil(bubbleNow()) // let the run go on
	})
	want := "faultline: internal error: bubble time 0.100000000s passed pending kernel event at 0.050000000s"
	if msg != want {
		t.Fatalf("advance panicked with %q, want %q", msg, want)
	}
}

// GOR-023: with Config.MaxTime before until, drive returns StopMaxTime when bubble time reaches
// MaxTime, also when the next kernel event is later.
func TestGoroutineDriverMaxTime(t *testing.T) {
	var (
		stop        kernel.StopReason
		now, bubble kernel.Time
	)
	gw(t, Options{Duration: time.Second}, func(w *World) {
		s := kernel.New(kernel.Config{Seed: 1, MaxTime: kernel.Time(300 * time.Millisecond)})
		s.At(kernel.Time(500*time.Millisecond), "late", func() {})
		ga := &goAttempt{watch: &watchState{}, gid: w.drv.ga.gid, asserts: &assertQueue{}} // not the attempt's own
		d := &driver{w: &World{Sim: s}, ga: ga, wake: make(chan struct{}, 1)}
		d.lockDriver()
		stop = d.drive(kernel.Time(time.Second))
		d.unlockDriver()
		now, bubble = s.Now(), bubbleNow()
	})
	want := kernel.Time(300 * time.Millisecond)
	if stop != kernel.StopMaxTime || now != want || bubble != want {
		t.Fatalf("drive = %v at kernel %v, bubble %v; want %v at %v both", stop, now, bubble, kernel.StopMaxTime, want)
	}
}

// GOR-075: at most 4,194,304 checkpoints; later events are not checkpointed.
func TestGoroutineDriverCheckpointCap(t *testing.T) {
	if maxCheckpoints != 4_194_304 {
		t.Fatalf("maxCheckpoints = %d, want 4194304", maxCheckpoints)
	}
	s := kernel.New(kernel.Config{Seed: 1})
	d := &driver{w: &World{Sim: s}, checkpoints: make([]uint64, maxCheckpoints-1, maxCheckpoints)}
	d.checkpoint()
	d.checkpoint()
	if n := len(d.checkpoints); n != maxCheckpoints || d.checkpoints[n-1] != s.TraceHash() {
		t.Fatalf("%d checkpoints after two more events, want %d ending with the trace hash", n, maxCheckpoints)
	}
}

// GOR-020, GOR-022, GOR-023: every event at exactly until runs, and the invariants run after each
// of them, as RunUntil does in event mode.
func TestGoroutineDriverAllEventsAtEnd(t *testing.T) {
	var ran, checks, executed uint64
	gw(t, Options{Duration: time.Second}, func(w *World) {
		ran, checks, executed = 0, 0, 0
		w.AddServer("e1", func(n *kernel.Node) {
			w.Sim.At(kernel.Time(time.Second), "a", func() { ran++ })
			w.Sim.At(kernel.Time(time.Second), "b", func() { ran++ })
		})
		w.Invariant("count", func() error { checks++; return nil })
		w.Final("executed", func() error { executed = w.Sim.Executed(); return nil })
	})
	if ran != 2 || executed != 3 || checks != executed {
		t.Fatalf("%d of 2 events at the end ran; invariants ran %d times for %d executed events (want 3)", ran, checks, executed)
	}
}

// GOR-065: after a runtime.Goexit, end leaves the world lock released, whether the driver held it
// (FailNow in body, with a goroutine waiting in Lock) or not (FailNow in an invariant).
func TestGoroutineDriverGoexitReleasesLock(t *testing.T) {
	for _, tc := range []struct{ scenario, at string }{
		{"driver-failnow-in-body", "0.010000000s (event 1)"},
		{"driver-failnow-in-invariant", "0.300000000s (event 4)"},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			res := runGorScenario(t, tc.scenario, "FAULTLINE_SEED=0x1")
			want := "faultline: seed 0x0000000000000001 stopped by t.FailNow or t.Fatal at t=" + tc.at
			if res.code != 1 || !strings.Contains(res.out, want) {
				t.Fatalf("exit %d, want 1 and %q\n%s", res.code, want, res.out)
			}
		})
	}
}

// GOR-040: SyncClock needs the world lock. Called by a check that did not take it, it panics
// instead of moving kernel time unguarded.
func TestGoroutineDriverSyncClockWithoutLock(t *testing.T) {
	var msg string
	gw(t, Options{Duration: time.Second}, func(w *World) {
		msg = ""
		w.Final("SyncClock without Lock", func() error {
			defer func() { msg = fmt.Sprint(recover()) }()
			w.drv.SyncClock()
			return nil
		})
	})
	want := "faultline: internal error: SyncClock called without the world lock"
	if msg != want {
		t.Fatalf("SyncClock without the lock: panic %q, want %q", msg, want)
	}
}
