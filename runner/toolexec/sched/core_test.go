// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package sched

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/internal/gosched"
)

func TestSelfReadsGoroutineAndBubble(t *testing.T) {
	if id := self(); id.gid == 0 || id.bubble != 0 {
		t.Fatalf("outside a bubble: %+v", id)
	}
	synctest.Test(t, func(t *testing.T) {
		a := self()
		var b identity
		done := make(chan struct{})
		go func() {
			b = self()
			close(done)
		}()
		<-done
		if a.bubble == 0 || b.bubble != a.bubble || a.gid == b.gid || b.gid == 0 {
			t.Fatalf("in a bubble: %+v and %+v", a, b)
		}
	})
}

// AT-XTE-14: driver code wakes the holder of mu and locks mu right away. Lock waits until the
// woken holder stopped at its next point before it reads Pending, so it releases the holder
// instead of blocking on the plain mutex while the holder parks forever.
func TestATXTE14_DriverLockAfterWakingHolder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s.Reset(streams(1))
		var mu sync.Mutex
		gate, done := make(chan struct{}), make(chan struct{})
		go func() {
			Lock(&mu) // parks at Lock's point first
			<-gate    // holds mu, blocked durably
			Point()   // parks while it holds mu
			Unlock(&mu)
			close(done)
		}()
		synctest.Wait()
		s.Release() // the goroutine takes mu and waits on gate
		synctest.Wait()
		close(gate) // driver code wakes the holder; it still runs when Lock starts
		Lock(&mu)
		Unlock(&mu)
		<-done
		s.Reset(nil, nil)
	})
}

// AT-XTE-15: an invariant that locks a mutex a process holds across a sleep panics with the
// deadlock text at once, instead of hanging until the 60 s stall watchdog.
func TestATXTE15_DriverLockDeadlockFailsFast(t *testing.T) {
	if os.Getenv("SCHED_DEADLOCK_CHILD") == "1" {
		var mu sync.Mutex
		faultline.Run(t, faultline.Options{Mode: faultline.ModeGoroutine, Seeds: 1, Duration: 5 * time.Second},
			func(w *faultline.World) {
				w.AddProcess("holder", func(ctx context.Context, p *faultline.Process) error {
					Lock(&mu)
					time.Sleep(time.Second)
					Unlock(&mu)
					return nil
				})
				w.Invariant("locks mu", func() error {
					Lock(&mu)
					Unlock(&mu)
					return nil
				})
			})
		return
	}
	// The child stops itself well before this test's deadline, so a hang in it never outlives the
	// parent. It gets none of the parent's FAULTLINE_* variables but the ones set here.
	limit := 30 * time.Second
	if d, ok := t.Deadline(); ok {
		limit = min(limit, time.Until(d)/2)
	}
	start := time.Now()
	cmd := exec.Command(os.Args[0], "-test.run=^TestATXTE15_DriverLockDeadlockFailsFast$", "-test.v", "-test.count=1", "-test.timeout="+limit.String()) //nolint:gosec // this test binary again
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "FAULTLINE_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "SCHED_DEADLOCK_CHILD=1", "FAULTLINE_ARTIFACTS=off", "FAULTLINE_CHECK_DETERMINISM=0", "FAULTLINE_SEED=0x1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the child passed\n%s", out)
	}
	if el := time.Since(start); el > 20*time.Second {
		t.Errorf("the child took %v; the deadlock must fail at once", el)
	}
	if !strings.Contains(string(out), deadlock) {
		t.Fatalf("the child's output lacks the deadlock text\n%s", out)
	}
}

// AT-XTE-16: during an attempt, a goroutine outside the attempt's bubble passes through points
// and locks; it never waits for a release.
func TestATXTE16_OutsideBubblePassesThrough(t *testing.T) {
	start, done := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	go func() { // outside any bubble
		<-start
		Point()
		Lock(&mu)
		Unlock(&mu)
		close(done)
	}()
	synctest.Test(t, func(t *testing.T) {
		s.Reset(streams(1))
		close(start)
		<-done // a channel made outside the bubble: no synctest.Wait runs while it blocks
		if s.Pending() {
			t.Error("a goroutine outside the attempt's bubble waits at a scheduling point")
		}
		s.Reset(nil, nil)
	})
}

// Wake is called when a waiter can make Pending true: at a point and when Unlock moves a mutex's
// waiters, not when a goroutine parks on a mutex.
func TestWakeOnlyWhenPendingCanChange(t *testing.T) {
	old := gosched.Wake
	var wakes atomic.Int32
	gosched.Wake = func() { wakes.Add(1) }
	defer func() { gosched.Wake = old }()
	synctest.Test(t, func(t *testing.T) {
		s.Reset(streams(1))
		var mu sync.Mutex
		gate := make(chan struct{})
		go func() {
			Lock(&mu)
			<-gate
			Unlock(&mu)
		}()
		synctest.Wait()
		s.Release()
		synctest.Wait() // the holder has mu: 1 wake
		go func() {
			Lock(&mu)
			Unlock(&mu)
		}()
		synctest.Wait()
		s.Release()
		synctest.Wait() // the second goroutine parked on mu: 2 wakes, not 3
		if n := wakes.Load(); n != 2 {
			t.Fatalf("%d wakes after a point each and a mutex park, want 2", n)
		}
		close(gate)
		synctest.Wait() // Unlock moved the waiter
		if n := wakes.Load(); n != 3 {
			t.Fatalf("%d wakes after Unlock moved a waiter, want 3", n)
		}
		s.Release()
		synctest.Wait()
		s.Reset(nil, nil)
	})
}

func TestGapsRegistry(t *testing.T) {
	gaps.mu.Lock()
	saved := gaps.lines
	gaps.lines = nil
	gaps.mu.Unlock()
	defer func() {
		gaps.mu.Lock()
		gaps.lines = saved
		gaps.mu.Unlock()
	}()
	if !Gaps("/b.go:2: sync.Map.Range\n/a.go:1: reflect map iteration") || !Gaps("/a.go:1: reflect map iteration") {
		t.Fatal("Gaps returned false")
	}
	if got, want := s.Gaps(), []string{"/a.go:1: reflect map iteration", "/b.go:2: sync.Map.Range"}; !slices.Equal(got, want) {
		t.Fatalf("Gaps() = %q, want %q", got, want)
	}
}

// The deadlock panic's text is XTE §7's, written out once so that a change to the constant fails.
func TestDeadlockText(t *testing.T) {
	const want = "faultline: toolexec backend: the driver goroutine (the body, an invariant or a final check) locks a mutex that a blocked goroutine holds; do not lock in the body or a check a mutex that a process holds across a blocking call"
	if deadlock != want {
		t.Fatalf("deadlock = %q, want %q", deadlock, want)
	}
}

// An Unlock outside the attempt's bubble moves the bubble's waiter to the scheduling-point list
// but does not call Wake, which sends on a channel of the bubble.
func TestUnlockOutsideBubbleDoesNotWake(t *testing.T) {
	old := gosched.Wake
	var wakes atomic.Int32
	gosched.Wake = func() { wakes.Add(1) }
	defer func() { gosched.Wake = old }()
	var mu sync.Mutex
	mu.Lock() // held outside any bubble
	unlock, unlocked := make(chan struct{}), make(chan struct{})
	go func() { // outside any bubble
		<-unlock
		Unlock(&mu)
		close(unlocked)
	}()
	synctest.Test(t, func(t *testing.T) {
		s.Reset(streams(1))
		go func() {
			Lock(&mu)
			Unlock(&mu)
		}()
		synctest.Wait()
		s.Release() // the goroutine finds mu taken and parks on it
		synctest.Wait()
		before := wakes.Load()
		close(unlock)
		<-unlocked // a channel made outside the bubble: no synctest.Wait runs while it blocks
		if n := wakes.Load() - before; n != 0 {
			t.Errorf("%d wakes from an Unlock outside the bubble, want 0", n)
		}
		if !s.Pending() {
			t.Error("the Unlock outside the bubble did not move the waiter to the scheduling-point list")
		}
		s.Release()
		synctest.Wait()
		s.Reset(nil, nil)
	})
}

// During an attempt, the driver and a goroutine outside the bubble call o.Do directly, without
// the attempt's per-Once state.
func TestOnceDoDirectForDriverAndOutside(t *testing.T) {
	var outside sync.Once
	start, done := make(chan struct{}), make(chan struct{})
	go func() { // outside any bubble
		<-start
		OnceDo(&outside, func() {})
		close(done)
	}()
	synctest.Test(t, func(t *testing.T) {
		s.Reset(streams(1))
		var o sync.Once
		OnceDo(&o, func() {}) // the driver
		close(start)
		<-done
		s.mu.Lock()
		n := len(s.onces)
		s.mu.Unlock()
		if n != 0 {
			t.Errorf("%d Once states after calls from the driver and from outside the bubble, want 0", n)
		}
		s.Reset(nil, nil)
	})
}

// The end of an attempt forgets the attempt's per-Once states.
func TestResetClearsOnceStates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var o sync.Once
		drive(1, func() { Go(func() { OnceDo(&o, func() {}) }) }, func() bool { return true })
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := len(s.onces); n != 0 {
		t.Fatalf("%d Once states after the attempt ended, want 0", n)
	}
}

// blocked returns once n goroutines wait in state, for example "sync.Mutex.Lock".
func blocked(state string, n int) {
	buf := make([]byte, 1<<20)
	for bytes.Count(buf[:runtime.Stack(buf, true)], []byte("["+state)) < n {
		runtime.Gosched()
	}
}

// Outside an attempt, a contended Lock waits in the plain mutex and then holds it, and read locks
// stay shared.
func TestPassThroughContended(t *testing.T) {
	var mu sync.Mutex
	Lock(&mu)
	got := make(chan struct{})
	go func() {
		Lock(&mu) // TryLock fails: the plain Lock waits
		Unlock(&mu)
		close(got)
	}()
	blocked("sync.Mutex.Lock", 1)
	Unlock(&mu)
	<-got
	var rw sync.RWMutex
	WLock(&rw)
	readers, both := make(chan struct{}), make(chan struct{})
	for range 2 {
		go func() {
			RLock(&rw) // TryRLock fails while the writer holds rw
			readers <- struct{}{}
			<-both // both readers hold rw at once
			RUnlock(&rw)
		}()
	}
	blocked("sync.RWMutex.RLock", 2)
	WUnlock(&rw)
	<-readers
	<-readers
	close(both)
	WLock(&rw)
	WUnlock(&rw)
}

// A RandV1().Read outside an attempt does not carry its buffered bytes into the next attempt.
func TestRandV1ReadAfterPassThrough(t *testing.T) {
	read := func() string {
		b := make([]byte, 3)
		synctest.Test(t, func(t *testing.T) {
			drive(1, func() { _, _ = RandV1().Read(b) }, func() bool { return true })
		})
		_, _ = RandV1().Read(make([]byte, 1)) // outside an attempt: buffers 6 bytes of the global generator
		return string(b)
	}
	if a, b := read(), read(); a != b {
		t.Fatalf("seed 1 read %x, then %x after a Read outside the attempt", a, b)
	}
}

// RandV1 draws from the user stream: Int63 is the stream's Uint64 shifted right by one.
func TestRandV1FollowsUserStream(t *testing.T) {
	_, user := streams(5)
	synctest.Test(t, func(t *testing.T) {
		s.Reset(streams(5))
		defer s.Reset(nil, nil)
		if got, want := RandV1().Int63(), int64(user.Uint64()>>1); got != want {
			t.Fatalf("RandV1().Int63() = %d, want %d", got, want)
		}
	})
}
