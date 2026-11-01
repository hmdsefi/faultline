// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package sched

import (
	"context"
	"fmt"
	mrand "math/rand"
	"math/rand/v2"
	"reflect"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/internal/gosched"
)

// streams returns the release and user streams that drive uses for seed.
func streams(seed uint64) (release, user *rand.Rand) {
	return rand.New(rand.NewPCG(seed, 1)), rand.New(rand.NewPCG(seed, 2)) //nolint:gosec // seeded test streams, not secrets
}

// twoReady returns two receive cases that are both ready.
func twoReady() []reflect.SelectCase {
	a, b := make(chan int, 1), make(chan int, 1)
	a <- 1
	b <- 2
	return []reflect.SelectCase{
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(a)},
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(b)},
	}
}

func TestInitSetsActive(t *testing.T) {
	if gosched.Active != gosched.Scheduler(s) {
		t.Fatal("init did not set gosched.Active to the package's scheduler")
	}
}

// With faultline's goroutine-mode driver, a goroutine that a timer wakes while the driver waits
// for virtual time is released at that instant: at its own scheduling point, when it is a Func
// callback that waits for its start, and when the Unlock of a holder that a timer woke moves it
// from the mutex to the scheduling-point list.
func TestWakeReleasesAtTimerInstant(t *testing.T) {
	run := func(t *testing.T, proc func(p *faultline.Process, at *atomic.Int64)) time.Duration {
		var at atomic.Int64
		at.Store(-1)
		faultline.Run(t, faultline.Options{Mode: faultline.ModeGoroutine, Seeds: 1, Duration: time.Second}, func(w *faultline.World) {
			w.AddProcess("p", func(ctx context.Context, p *faultline.Process) error {
				proc(p, &at)
				<-ctx.Done()
				return nil
			})
		})
		return time.Duration(at.Load())
	}
	t.Run("point", func(t *testing.T) {
		got := run(t, func(_ *faultline.Process, at *atomic.Int64) {
			start := time.Now()
			time.Sleep(time.Millisecond)
			Point()
			at.Store(int64(time.Since(start)))
		})
		if got != time.Millisecond {
			t.Errorf("a point reached at 1ms was released at %v, want 1ms", got)
		}
	})
	t.Run("func", func(t *testing.T) {
		got := run(t, func(_ *faultline.Process, at *atomic.Int64) {
			start := time.Now()
			time.AfterFunc(time.Millisecond, Func(func() { at.Store(int64(time.Since(start))) }))
		})
		if got != time.Millisecond {
			t.Errorf("an AfterFunc callback that fired at 1ms started at %v, want 1ms", got)
		}
	})
	t.Run("unlock", func(t *testing.T) {
		got := run(t, func(p *faultline.Process, at *atomic.Int64) {
			start := time.Now()
			var mu sync.Mutex
			Lock(&mu)
			p.Go(func() {
				Lock(&mu) // finds mu taken and waits for the Unlock below
				at.Store(int64(time.Since(start)))
				Unlock(&mu)
			})
			time.Sleep(time.Millisecond)
			Unlock(&mu)
		})
		if got != time.Millisecond {
			t.Errorf("a mutex unlocked at 1ms was taken by its waiter at %v, want 1ms", got)
		}
	})
}

// RandV1().Read starts each attempt without the bytes it buffered in the attempt before.
func TestRandV1ReadPerAttempt(t *testing.T) {
	read := func() string {
		b := make([]byte, 3)
		synctest.Test(t, func(t *testing.T) {
			drive(1, func() { _, _ = RandV1().Read(b) }, func() bool { return true })
		})
		return fmt.Sprintf("%x", b)
	}
	if a, b := read(), read(); a != b {
		t.Fatalf("seed 1 read %s, then %s in the next attempt", a, b)
	}
}

// RandV1().Read, the rewrite of math/rand.Read, is safe for concurrent use as math/rand.Read is:
// two goroutines read at once outside an attempt and in one. Under -race an unguarded access to
// Read's buffer fails the test.
func TestRandV1ReadConcurrent(t *testing.T) {
	readTwo := func() {
		var wg sync.WaitGroup
		for range 2 {
			wg.Go(func() {
				b := make([]byte, 3)
				for range 50 {
					_, _ = RandV1().Read(b)
				}
			})
		}
		wg.Wait()
	}
	readTwo()
	synctest.Test(t, func(t *testing.T) {
		drive(1, readTwo, func() bool { return true })
	})
}

// Reset drops Read's buffered bytes under the lock Read takes: a goroutine reads while attempts
// start, and -race reports no data race. (The race detector reports one race per address, so a
// failure of TestRandV1ReadConcurrent hides this one: run this test alone.)
func TestRandV1ReadDuringReset(t *testing.T) {
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		b := make([]byte, 3)
		for {
			select {
			case <-stop:
				return
			default:
				for range 10 {
					_, _ = RandV1().Read(b)
				}
				runtime.Gosched() // on one P, lets the next Reset run between two batches of reads
			}
		}
	}()
	for seed := range uint64(20) {
		runtime.Gosched()
		synctest.Test(t, func(t *testing.T) {
			drive(seed, func() {}, func() bool { return true })
		})
	}
	close(stop)
	<-done
}

// Each math/rand function the rewrite replaces has a method of the same name and type on RandV1().
func TestRandV1Methods(t *testing.T) {
	for _, f := range []struct {
		name string
		fn   any
	}{
		{"ExpFloat64", mrand.ExpFloat64}, {"Float32", mrand.Float32}, {"Float64", mrand.Float64},
		{"Int", mrand.Int}, {"Int31", mrand.Int31}, {"Int31n", mrand.Int31n}, {"Int63", mrand.Int63},
		{"Int63n", mrand.Int63n}, {"Intn", mrand.Intn}, {"NormFloat64", mrand.NormFloat64},
		{"Perm", mrand.Perm}, {"Read", mrand.Read}, {"Shuffle", mrand.Shuffle}, //nolint:staticcheck // the deprecated Read is a rewritten row
		{"Uint32", mrand.Uint32}, {"Uint64", mrand.Uint64},
	} {
		m := reflect.ValueOf(RandV1()).MethodByName(f.name)
		if !m.IsValid() || m.Type() != reflect.TypeOf(f.fn) {
			t.Errorf("RandV1().%s is %v, want a method of type %v", f.name, m, reflect.TypeOf(f.fn))
		}
	}
}

// Release picks from the waiting goroutines sorted by ID with the release stream only; Keys and
// Select then draw from the user stream, untouched by the release.
func TestStreamsPinned(t *testing.T) {
	m := map[int]bool{0: true, 1: true, 2: true, 3: true, 4: true, 5: true, 6: true, 7: true}
	orders, chosen := map[string]bool{}, map[int]bool{}
	for seed := uint64(1); seed <= 10; seed++ {
		synctest.Test(t, func(t *testing.T) {
			release, user := streams(seed)
			s.Reset(streams(seed))
			var ids [3]atomic.Uint64
			var first atomic.Uint64
			for i := range ids {
				go func() {
					ids[i].Store(self().gid)
					Point()
					first.CompareAndSwap(0, self().gid)
				}()
			}
			synctest.Wait()
			s.Release()
			synctest.Wait()
			sorted := []uint64{ids[0].Load(), ids[1].Load(), ids[2].Load()}
			slices.Sort(sorted)
			if want := sorted[release.IntN(len(sorted))]; first.Load() != want {
				t.Fatalf("seed %d: released goroutine %d of %v, want %d", seed, first.Load(), sorted, want)
			}

			keys := Keys(m)
			want := []int{0, 1, 2, 3, 4, 5, 6, 7}
			user.Shuffle(len(want), func(i, j int) { want[i], want[j] = want[j], want[i] })
			if !slices.Equal(keys, want) {
				t.Fatalf("seed %d: Keys = %v, want %v", seed, keys, want)
			}
			c, _, _ := Select(twoReady(), false)
			order := []int{0, 1}
			user.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
			if c != order[0] {
				t.Fatalf("seed %d: Select chose %d, want %d", seed, c, order[0])
			}
			orders[fmt.Sprint(keys)], chosen[c] = true, true
			s.Reset(nil, nil)
		})
	}
	if len(orders) < 2 {
		t.Error("Keys gave one order for 10 seeds")
	}
	if !chosen[0] || !chosen[1] {
		t.Errorf("Select chose %v for 10 seeds, want both cases", chosen)
	}
}

// A goroutine that finds a mutex taken waits durably on its own channel (F14). The holder's
// Unlock moves it to the scheduling-point list; the end of the attempt lets it continue.
func TestContendedLockParksDurably(t *testing.T) {
	for _, end := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			s.Reset(streams(1))
			var mu sync.Mutex
			var got atomic.Bool
			gate := make(chan struct{})
			go func() {
				Lock(&mu)
				<-gate
				Unlock(&mu)
			}()
			synctest.Wait()
			s.Release() // the holder takes mu and waits on gate
			synctest.Wait()
			go func() {
				Lock(&mu)
				got.Store(true)
				Unlock(&mu)
			}()
			synctest.Wait()
			s.Release()     // the waiter finds mu taken
			synctest.Wait() // returns only if the waiter is durably blocked
			if s.Pending() || got.Load() {
				t.Fatalf("end %v: pending %v, waiter got mu %v while the holder has it", end, s.Pending(), got.Load())
			}
			if end {
				s.Reset(nil, nil)
				close(gate)
			} else {
				close(gate)
				synctest.Wait()
				if !s.Pending() {
					t.Fatal("Unlock did not move the mutex's waiter to the scheduling-point list")
				}
				s.Release()
			}
			synctest.Wait()
			if !got.Load() {
				t.Fatalf("end %v: the waiter did not get mu", end)
			}
			s.Reset(nil, nil)
		})
	}
}

func TestGoStartsAtAPoint(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s.Reset(streams(1))
		var ran atomic.Bool
		Go(func() { ran.Store(true) })
		synctest.Wait()
		if !s.Pending() || ran.Load() {
			t.Fatalf("pending %v, f ran %v before a release", s.Pending(), ran.Load())
		}
		s.Release()
		synctest.Wait()
		if !ran.Load() {
			t.Fatal("f did not run after the release")
		}
		s.Reset(nil, nil)
	})
}

// On the driver, Lock waits after each release until the released holder is blocked again, so
// a holder that reaches several points while it holds the mutex is released at each of them.
func TestLockOnDriverWaitsForHolder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s.Reset(streams(1))
		var mu sync.Mutex
		go func() {
			Lock(&mu)
			for range 5 {
				Point() // parks, holding mu
			}
			Unlock(&mu)
		}()
		synctest.Wait()
		s.Release() // the holder takes mu and parks at its first Point
		synctest.Wait()
		Lock(&mu)
		Unlock(&mu)
		s.Reset(nil, nil)
	})
}

func TestPassThroughSelectIndexOrder(t *testing.T) {
	if c, _, _ := Select(twoReady(), false); c != 0 {
		t.Fatalf("pass-through Select chose %d with two ready cases, want 0", c)
	}
}
