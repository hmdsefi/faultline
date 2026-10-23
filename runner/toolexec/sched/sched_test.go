// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package sched

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/hmdsefi/faultline/internal/gosched"
)

// drive stands in for faultline's driver (EXB-036): body starts goroutines; then, until done
// reports true, it releases one waiter per quiescence, or lets bubble time pass when none waits;
// finally it ends the attempt.
func drive(seed uint64, body func(), done func() bool) {
	s.Reset(rand.New(rand.NewPCG(seed, 1)), rand.New(rand.NewPCG(seed, 2))) //nolint:gosec // seeded test streams, not secrets
	body()
	for {
		synctest.Wait()
		if s.Pending() {
			s.Release()
			continue
		}
		if done() {
			break
		}
		time.Sleep(time.Millisecond)
	}
	s.Reset(nil, nil)
}

// exchange is AT-EXB-09's program written against sched directly: two goroutines pass 100
// values over an unbuffered channel and append to a shared slice under a mutex.
func exchange(t *testing.T, seed uint64) string {
	var out string
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		var order []byte
		var finished atomic.Int32
		drive(seed, func() {
			ch := make(chan int)
			Go(func() {
				defer finished.Add(1)
				for i := 0; i < 100; i++ {
					Point()
					ch <- i
					Lock(&mu)
					order = append(order, 'a')
					Unlock(&mu)
				}
				Point()
				close(ch)
			})
			Go(func() {
				defer finished.Add(1)
				Point()
				for range ch {
					Point()
					Lock(&mu)
					order = append(order, 'b')
					Unlock(&mu)
				}
			})
		}, func() bool { return finished.Load() == 2 })
		out = string(order)
	})
	return out
}

func TestExchangeDeterministicPerSeed(t *testing.T) {
	distinct := map[string]bool{}
	for seed := uint64(1); seed <= 10; seed++ {
		a, b := exchange(t, seed), exchange(t, seed)
		if a != b {
			t.Fatalf("seed %d: the two runs differ\n%s\n%s", seed, a, b)
		}
		if len(a) != 200 {
			t.Fatalf("seed %d: %d appends, want 200", seed, len(a))
		}
		distinct[a] = true
	}
	if len(distinct) < 2 {
		t.Fatal("every seed gave the same order")
	}
}

func TestPassThroughWithoutDriver(t *testing.T) {
	var mu sync.Mutex
	Lock(&mu)
	Unlock(&mu)
	var rw sync.RWMutex
	RLock(&rw)
	RUnlock(&rw)
	WLock(&rw)
	WUnlock(&rw)
	Point()
	var o sync.Once
	n := 0
	OnceDo(&o, func() { n++ })
	OnceDo(&o, func() { n++ })
	if n != 1 {
		t.Fatalf("OnceDo ran f %d times", n)
	}
	if got := fmt.Sprint(Keys(map[string]int{"b": 1, "a": 2, "c": 3})); got != "[a b c]" {
		t.Fatalf("Keys = %s, want sorted", got)
	}
	ch := make(chan int, 1)
	ch <- 7
	c, v, ok := Select([]reflect.SelectCase{{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ch)}}, true)
	if c != 0 || v.Int() != 7 || !ok {
		t.Fatalf("Select = %d, %v, %v", c, v, ok)
	}
	if c, _, _ := Select([]reflect.SelectCase{{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(make(chan int))}}, true); c != -1 {
		t.Fatalf("Select with nothing ready = %d, want -1 (default)", c)
	}
	done := make(chan struct{})
	Go(func() { close(done) })
	<-done
}

func TestUserStream(t *testing.T) {
	run := func(seed uint64) string {
		var out string
		synctest.Test(t, func(t *testing.T) {
			drive(seed, func() {
				a, b := make(chan int, 1), make(chan int, 1)
				a <- 1
				b <- 2
				c, _, _ := Select([]reflect.SelectCase{
					{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(a)},
					{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(b)},
				}, false)
				keys := Keys(map[int]bool{0: true, 1: true, 2: true, 3: true, 4: true, 5: true, 6: true, 7: true})
				out = fmt.Sprint(keys, c, Rand().IntN(1000), RandV1().Intn(1000))
			}, func() bool { return true })
		})
		return out
	}
	if a, b := run(1), run(1); a != b {
		t.Fatalf("seed 1: %s vs %s", a, b)
	}
	distinct := map[string]bool{}
	for seed := uint64(1); seed <= 10; seed++ {
		distinct[run(seed)] = true
	}
	if len(distinct) < 2 {
		t.Fatal("the user stream does not vary with the seed")
	}
}

func TestOnceDoConcurrent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var o sync.Once
		var calls, finished atomic.Int32
		var order []string
		var mu sync.Mutex
		drive(3, func() {
			for g := range 4 {
				Go(func() {
					defer finished.Add(1)
					OnceDo(&o, func() {
						calls.Add(1)
						time.Sleep(time.Millisecond)
					})
					mu.Lock()
					order = append(order, fmt.Sprint(g))
					mu.Unlock()
				})
			}
		}, func() bool { return finished.Load() == 4 })
		if calls.Load() != 1 || len(order) != 4 {
			t.Fatalf("calls %d, finished %v", calls.Load(), order)
		}
	})
}

func TestEndReleasesParkedGoroutines(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var resumed atomic.Bool
		s.Reset(rand.New(rand.NewPCG(1, 1)), rand.New(rand.NewPCG(1, 2))) //nolint:gosec // seeded test streams, not secrets
		go func() {
			Point()
			resumed.Store(true)
		}()
		synctest.Wait()
		if !s.Pending() || resumed.Load() {
			t.Fatalf("pending %v, resumed %v before the end", s.Pending(), resumed.Load())
		}
		s.Reset(nil, nil)
		synctest.Wait()
		if !resumed.Load() || s.Pending() {
			t.Fatalf("Reset(nil, nil) did not let the parked goroutine continue")
		}
	})
}

func TestLockOnDriverReleasesHolder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		driver, old := goid(), gosched.OnDriver
		gosched.OnDriver = func() bool { return goid() == driver }
		defer func() { gosched.OnDriver = old }()
		s.Reset(rand.New(rand.NewPCG(1, 1)), rand.New(rand.NewPCG(1, 2))) //nolint:gosec // seeded test streams, not secrets
		var mu sync.Mutex
		go func() {
			Lock(&mu) // parks at its scheduling point
			Point()   // parks again, holding mu
			Unlock(&mu)
		}()
		synctest.Wait()
		s.Release() // the goroutine takes mu and parks at Point
		synctest.Wait()
		Lock(&mu) // on the driver: releases the holder until mu is free
		Unlock(&mu)
		s.Reset(nil, nil)
	})
}

func TestResetWhileOnceRuns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s.Reset(rand.New(rand.NewPCG(1, 1)), rand.New(rand.NewPCG(1, 2))) //nolint:gosec // seeded test streams, not secrets
		var o sync.Once
		var finished atomic.Int32
		for range 2 {
			go func() {
				defer finished.Add(1)
				OnceDo(&o, func() { Point() }) // the first caller parks inside f, the second waits on o
			}()
			synctest.Wait()
		}
		s.Reset(nil, nil)
		synctest.Wait()
		if finished.Load() != 2 {
			t.Fatalf("%d of 2 callers finished after Reset(nil, nil)", finished.Load())
		}
	})
}
