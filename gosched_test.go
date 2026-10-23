// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"context"
	"math/rand/v2"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/hmdsefi/faultline/internal/gosched"
	"github.com/hmdsefi/faultline/kernel"
)

// fakeScheduler is a gosched.Scheduler test double: goroutines park in point() until Release and
// wake the driver as the gosched.Wake contract asks; Reset records whether an attempt starts
// (streams given) or ends (nil streams).
type fakeScheduler struct {
	mu       sync.Mutex
	active   bool
	waiting  []chan struct{}
	resets   []bool
	release  *rand.Rand
	user     *rand.Rand
	releases int
	seen     [][]int // per attempt: how many goroutines waited at each Release
	queued   int     // the most requests the driver had queued at a Release
}

func (f *fakeScheduler) Reset(release, user *rand.Rand) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resets = append(f.resets, release != nil && user != nil)
	f.active, f.release, f.user = release != nil, release, user
	if release != nil {
		f.seen = append(f.seen, nil)
	}
	if release == nil {
		for _, ch := range f.waiting {
			close(ch)
		}
		f.waiting = nil
	}
}

func (f *fakeScheduler) Pending() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.waiting) > 0
}

func (f *fakeScheduler) Release() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen[len(f.seen)-1] = append(f.seen[len(f.seen)-1], len(f.waiting))
	if d := activeDriver.Load(); d != nil { // Release runs on the driver with the world lock held
		f.queued = max(f.queued, len(d.queue))
	}
	close(f.waiting[0])
	f.waiting = f.waiting[1:]
	f.releases++
}

func (f *fakeScheduler) point() {
	f.mu.Lock()
	if !f.active {
		f.mu.Unlock()
		return
	}
	ch := make(chan struct{})
	f.waiting = append(f.waiting, ch)
	f.mu.Unlock()
	if gosched.Wake != nil {
		gosched.Wake()
	}
	<-ch
}

// withScheduler sets gosched.Active to s until t ends. The driver reads gosched.Active without
// synchronization, so never call it from a parallel test.
func withScheduler(t *testing.T, s gosched.Scheduler) {
	old := gosched.Active
	gosched.Active = s
	t.Cleanup(func() { gosched.Active = old })
}

func TestSchedulerHook(t *testing.T) {
	f := &fakeScheduler{}
	withScheduler(t, f)
	var sameStream, userStream, bodyOnDriver, procOnDriver atomic.Bool
	var releasedAt atomic.Int64
	gw(t, Options{Duration: time.Second}, func(w *World) {
		f.mu.Lock()
		sameStream.Store(f.release == w.Sim.Rand("toolexec/release"))
		userStream.Store(f.user == w.Sim.Rand("toolexec/user") && f.user != f.release)
		f.mu.Unlock()
		bodyOnDriver.Store(gosched.OnDriver())
		w.AddProcess("p", func(ctx context.Context, p *Process) error {
			procOnDriver.Store(gosched.OnDriver())
			for range 5 {
				f.point()
			}
			releasedAt.Store(int64(time.Since(epoch)))
			<-ctx.Done()
			return nil
		})
	})
	f.mu.Lock()
	defer f.mu.Unlock()
	if !sameStream.Load() {
		t.Error("Reset did not get the attempt's toolexec/release stream")
	}
	if !userStream.Load() {
		t.Error("Reset did not get the attempt's toolexec/user stream as a stream of its own")
	}
	if !bodyOnDriver.Load() || procOnDriver.Load() {
		t.Errorf("gosched.OnDriver: body %v (want true), process %v (want false)", bodyOnDriver.Load(), procOnDriver.Load())
	}
	if f.releases%5 != 0 || f.releases == 0 {
		t.Errorf("%d releases, want 5 per attempt", f.releases)
	}
	if releasedAt.Load() != 0 {
		t.Errorf("the points were released at %v, want 0 (releases come before time advances)", time.Duration(releasedAt.Load()))
	}
	if len(f.resets) == 0 || len(f.resets)%2 != 0 {
		t.Fatalf("resets %v, want start/end pairs", f.resets)
	}
	for i, start := range f.resets {
		if start != (i%2 == 0) {
			t.Fatalf("resets %v, want start, end, start, end, ...", f.resets)
		}
	}
	if gosched.OnDriver() {
		t.Error("gosched.OnDriver is true after the run")
	}
}

func TestNoSchedulerIsPartA(t *testing.T) {
	withScheduler(t, nil)
	var at atomic.Int64
	gw(t, Options{Duration: time.Second}, func(w *World) {
		w.AddProcess("p", func(ctx context.Context, p *Process) error {
			time.Sleep(10 * time.Millisecond)
			at.Store(int64(time.Since(epoch)))
			<-ctx.Done()
			return nil
		})
	})
	if time.Duration(at.Load()) != 10*time.Millisecond {
		t.Fatalf("process woke at %v", time.Duration(at.Load()))
	}
}

// A goroutine that a timer wakes while the driver waits for virtual time reaches a scheduling
// point. gosched.Wake makes the driver release it at that instant, not at the next kernel event
// or at the end of the run.
func TestSchedulerWakesDriver(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event bool
	}{{"no kernel event", false}, {"kernel event at 500ms", true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeScheduler{}
			withScheduler(t, f)
			var reached, released atomic.Int64
			gw(t, Options{Duration: time.Second}, func(w *World) {
				if tc.event {
					w.AddServer("s", func(nd *kernel.Node) {
						nd.After(500*time.Millisecond, "e", func() {})
					})
				}
				w.AddProcess("p", func(ctx context.Context, p *Process) error {
					time.Sleep(time.Millisecond)
					reached.Store(int64(time.Since(epoch)))
					f.point()
					released.Store(int64(time.Since(epoch)))
					<-ctx.Done()
					return nil
				})
			})
			if r, l := time.Duration(reached.Load()), time.Duration(released.Load()); r != time.Millisecond || l != time.Millisecond {
				t.Errorf("point reached at %v and released at %v, want both at 1ms", r, l)
			}
		})
	}
}

// gosched.Wake does nothing when no attempt is in progress or when the caller is outside the
// attempt's bubble: a send on the bubble's wake channel from outside it is a fatal error.
func TestWakeOutsideBubble(t *testing.T) {
	gosched.Wake()
	synctest.Test(t, func(*testing.T) { gosched.Wake() }) // a bubble, but no attempt
	start, done := make(chan struct{}), make(chan struct{})
	go func() {
		<-start
		gosched.Wake()
		close(done)
	}()
	var once sync.Once
	gw(t, Options{Duration: time.Second}, func(w *World) {
		once.Do(func() { // the determinism check runs body again
			close(start)
			<-done
		})
	})
	gosched.Wake()
}

// inBubble is true only in the bubble whose ID the attempt recorded, not outside any bubble and
// not in another bubble.
func TestInBubble(t *testing.T) {
	d := &driver{ga: &goAttempt{}}
	if d.inBubble() {
		t.Error("inBubble is true outside any bubble")
	}
	synctest.Test(t, func(t *testing.T) {
		var buf [256]byte
		line, _, _ := strings.Cut(string(buf[:runtime.Stack(buf[:], false)]), "\n")
		_, id, ok := parseStackHeader(line)
		if !ok {
			t.Fatalf("no bubble in the stack header %q", line)
		}
		d.ga.bubble.Store(id)
		if !d.inBubble() {
			t.Error("inBubble is false in the attempt's bubble")
		}
		d.ga.bubble.Store(id + 1)
		if d.inBubble() {
			t.Error("inBubble is true in another bubble")
		}
	})
}

// Requests and kernel events wait while a goroutine waits at a scheduling point. At 1 ms one
// process reaches three points and another posts a request: the request is still queued at the
// releases, and it runs as a kernel event only after the third release.
func TestSchedulerHoldsRequests(t *testing.T) {
	f := &fakeScheduler{}
	withScheduler(t, f)
	var ranAfter []int // per attempt: releases before the request ran (under f.mu)
	gw(t, Options{Duration: time.Second}, func(w *World) {
		w.AddProcess("a", func(ctx context.Context, p *Process) error {
			time.Sleep(time.Millisecond)
			for range 3 {
				f.point()
			}
			<-ctx.Done()
			return nil
		})
		w.AddProcess("b", func(ctx context.Context, p *Process) error {
			time.Sleep(time.Millisecond)
			_ = p.Do("x", func() {
				f.mu.Lock()
				ranAfter = append(ranAfter, len(f.seen[len(f.seen)-1]))
				f.mu.Unlock()
			})
			<-ctx.Done()
			return nil
		})
	})
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(ranAfter) == 0 || slices.ContainsFunc(ranAfter, func(n int) bool { return n != 3 }) {
		t.Errorf("the request ran after %v releases, want 3 per attempt (it ran while a goroutine waited at a scheduling point)", ranAfter)
	}
	if f.queued == 0 {
		t.Error("the request was drained while a goroutine waited at a scheduling point")
	}
}

// Each pass releases one goroutine. Two processes reach three points each at the same instant:
// every Release but the last sees both waiting, because the released goroutine is back at its
// next point before the next pass asks.
func TestSchedulerReleasesOnePerPass(t *testing.T) {
	f := &fakeScheduler{}
	withScheduler(t, f)
	gw(t, Options{Duration: time.Second}, func(w *World) {
		for _, name := range []string{"a", "b"} {
			w.AddProcess(name, func(ctx context.Context, p *Process) error {
				time.Sleep(time.Millisecond)
				for range 3 {
					f.point()
				}
				<-ctx.Done()
				return nil
			})
		}
	})
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.seen) == 0 {
		t.Fatal("no attempt started")
	}
	want := []int{2, 2, 2, 2, 2, 1}
	for i, got := range f.seen {
		if !slices.Equal(got, want) {
			t.Errorf("attempt %d: Release saw %v goroutines waiting, want %v", i, got, want)
		}
	}
}

// A goroutine that reaches a scheduling point after the run ended continues through
// Reset(nil, nil) at E1, before the drain, so the drain ends it and FailOnLeak finds no leak.
func TestSchedulerEndFreesParked(t *testing.T) {
	f := &fakeScheduler{}
	withScheduler(t, f)
	gw(t, Options{Duration: time.Second, FailOnLeak: true, Drain: time.Second}, func(w *World) {
		w.AddProcess("p", func(ctx context.Context, p *Process) error {
			<-ctx.Done()
			f.point()
			return nil
		})
	})
}
