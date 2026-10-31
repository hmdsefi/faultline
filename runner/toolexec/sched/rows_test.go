// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package sched

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// AT-XTE-17: CondSignal wakes the first waiter in wait order, CondBroadcast the others, and every
// waiter holds L when CondWait returns; each seed replays.
func TestATXTE17_CondWaitOrder(t *testing.T) {
	run := func(seed uint64) string {
		var out string
		synctest.Test(t, func(t *testing.T) {
			var mu sync.Mutex
			c := sync.NewCond(&mu)
			var waited, woke []int // under mu
			var finished atomic.Int32
			drive(seed, func() {
				for i := range 3 {
					Go(func() {
						defer finished.Add(1)
						Lock(&mu)
						waited = append(waited, i)
						CondWait(c)
						if mu.TryLock() {
							t.Errorf("waiter %d returned from CondWait without holding L", i)
						}
						woke = append(woke, i)
						Unlock(&mu)
					})
				}
				Go(func() {
					defer finished.Add(1)
					for n := 0; n < 3; {
						Lock(&mu)
						n = len(waited)
						Unlock(&mu)
						Point()
					}
					Lock(&mu)
					CondSignal(c)
					Unlock(&mu)
					for n := 0; n < 1; {
						Lock(&mu)
						n = len(woke)
						Unlock(&mu)
						Point()
					}
					Lock(&mu)
					CondBroadcast(c)
					Unlock(&mu)
				})
			}, func() bool { return finished.Load() == 4 })
			if len(woke) != 3 || woke[0] != waited[0] {
				t.Fatalf("seed %d: waited %v, woke %v; CondSignal must wake the first waiter", seed, waited, woke)
			}
			out = fmt.Sprint(waited, woke)
		})
		return out
	}
	for seed := uint64(1); seed <= 10; seed++ {
		if a, b := run(seed), run(seed); a != b {
			t.Fatalf("seed %d: %s, then %s", seed, a, b)
		}
	}
}

// XTE-069: Reset releases a goroutine waiting on a Cond, and it holds L when CondWait returns.
func TestResetReleasesCondWaiters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s.Reset(streams(1))
		var mu sync.Mutex
		c := sync.NewCond(&mu)
		var finished atomic.Int32
		go func() {
			defer finished.Add(1)
			Lock(&mu)
			CondWait(c)
			if mu.TryLock() {
				t.Error("CondWait returned without holding L")
			}
			Unlock(&mu)
		}()
		synctest.Wait()
		for s.Pending() {
			s.Release()
			synctest.Wait()
		}
		if finished.Load() != 0 {
			t.Fatal("CondWait returned with no signal")
		}
		s.Reset(nil, nil)
		synctest.Wait()
		if finished.Load() != 1 {
			t.Fatal("Reset(nil, nil) did not release the goroutine in CondWait")
		}
	})
}

type countLocker struct{ locks, unlocks int }

func (c *countLocker) Lock()   { c.locks++ }
func (c *countLocker) Unlock() { c.unlocks++ }

// AT-XTE-18
func TestATXTE18_LockersOnceAndStarts(t *testing.T) {
	t.Run("ILock parks durably on a held lock", func(t *testing.T) {
		for _, newLock := range []func() sync.Locker{
			func() sync.Locker { return &sync.Mutex{} },
			func() sync.Locker { return &sync.RWMutex{} },
		} {
			synctest.Test(t, func(t *testing.T) {
				s.Reset(streams(1))
				l := newLock()
				var got atomic.Bool
				gate := make(chan struct{})
				go func() {
					ILock(l)
					<-gate
					IUnlock(l)
				}()
				synctest.Wait()
				s.Release() // the holder takes l and waits on gate
				synctest.Wait()
				go func() {
					ILock(l)
					got.Store(true)
					IUnlock(l)
				}()
				synctest.Wait()
				s.Release()     // the second finds l taken and parks on it
				synctest.Wait() // returns only because that park is durable
				if s.Pending() || got.Load() {
					t.Fatalf("%T: pending %v, got %v while the holder has the lock", l, s.Pending(), got.Load())
				}
				close(gate)
				synctest.Wait()
				s.Release() // IUnlock moved the waiter to the scheduling-point list
				synctest.Wait()
				if !got.Load() {
					t.Fatalf("%T: the waiter did not get the lock after IUnlock", l)
				}
				s.Reset(nil, nil)
			})
		}
	})
	t.Run("ILock calls other lockers directly", func(t *testing.T) {
		var c countLocker
		ILock(&c)
		IUnlock(&c)
		if c.locks != 1 || c.unlocks != 1 {
			t.Fatalf("locks %d, unlocks %d", c.locks, c.unlocks)
		}
	})
	t.Run("RLocker read-locks", func(t *testing.T) {
		var rw sync.RWMutex
		a, b := RLocker(&rw), RLocker(&rw)
		a.Lock()
		b.Lock()
		if rw.TryLock() {
			t.Fatal("a writer got rw while two RLocker holders read")
		}
		a.Unlock()
		b.Unlock()
		if !rw.TryLock() {
			t.Fatal("rw stays locked after both RLocker holders unlocked")
		}
		rw.Unlock()
	})
	t.Run("method-value helpers", func(t *testing.T) {
		var mu sync.Mutex
		LockFunc(&mu)()
		if mu.TryLock() {
			t.Fatal("LockFunc did not lock")
		}
		UnlockFunc(&mu)()
		var rw sync.RWMutex
		RLockFunc(&rw)()
		RUnlockFunc(&rw)()
		WLockFunc(&rw)()
		WUnlockFunc(&rw)()
		if !rw.TryLock() {
			t.Fatal("the RWMutex helpers left rw locked")
		}
		rw.Unlock()
		var o sync.Once
		n := 0
		do := OnceDoFunc(&o)
		do(func() { n++ })
		do(func() { n++ })
		var wg sync.WaitGroup
		var ran atomic.Bool
		GoFunc(&wg)(func() { ran.Store(true) })
		WaitFunc(&wg)()
		if n != 1 || !ran.Load() {
			t.Fatalf("OnceDoFunc ran f %d times; GoFunc ran f: %v", n, ran.Load())
		}
		c := sync.NewCond(&mu)
		CondSignalFunc(c)() // no waiter: nothing happens
		CondBroadcastFunc(c)()
		if CondWaitFunc(c) == nil {
			t.Fatal("CondWaitFunc returned nil")
		}
	})
	t.Run("WaitGroupGo and Func start at a point", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s.Reset(streams(1))
			var wg sync.WaitGroup
			var a, b atomic.Bool
			WaitGroupGo(&wg, func() { a.Store(true) })
			f := Func(func() { b.Store(true) })
			go f()
			synctest.Wait()
			if !s.Pending() || a.Load() || b.Load() {
				t.Fatalf("pending %v, ran %v %v before a release", s.Pending(), a.Load(), b.Load())
			}
			for s.Pending() {
				s.Release()
				synctest.Wait()
			}
			wg.Wait()
			if !a.Load() || !b.Load() {
				t.Fatal("WaitGroupGo's or Func's function did not run after the releases")
			}
			if Func(nil) != nil {
				t.Fatal("Func(nil) is not nil")
			}
			s.Reset(nil, nil)
		})
	})
	t.Run("OnceFunc runs f once for three goroutines", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var calls, finished atomic.Int32
			f := OnceFunc(func() {
				calls.Add(1)
				Point() // f parks once while the other callers wait
			})
			drive(2, func() {
				for range 3 {
					Go(func() {
						defer finished.Add(1)
						f()
					})
				}
			}, func() bool { return finished.Load() == 3 })
			if calls.Load() != 1 {
				t.Fatalf("f ran %d times", calls.Load())
			}
		})
	})
	t.Run("OnceFunc repeats f's panic", func(t *testing.T) {
		f := OnceFunc(func() { panic("boom") })
		for i := range 2 {
			func() {
				defer func() {
					if r := recover(); r != "boom" {
						t.Errorf("call %d recovered %v, want boom", i, r)
					}
				}()
				f()
			}()
		}
	})
	t.Run("OnceValue and OnceValues", func(t *testing.T) {
		n := 0
		v := OnceValue(func() int { n++; return 7 })
		vs := OnceValues(func() (int, string) { n++; return 8, "x" })
		a, b := v(), v()
		c, d := vs()
		_, _ = vs()
		if a != 7 || b != 7 || c != 8 || d != "x" || n != 2 {
			t.Fatalf("got %d %d %d %q with %d calls", a, b, c, d, n)
		}
	})
	t.Run("OnceValue callers return when the attempt ends while f runs", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s.Reset(streams(1))
			gate := make(chan struct{})
			v := OnceValue(func() int {
				Point() // the first caller parks inside f, the second waits on its Once
				<-gate  // after the attempt, f returns only when the test lets it
				return 7
			})
			var finished, panics atomic.Int32
			var sum atomic.Int64
			for range 2 {
				go func() {
					defer finished.Add(1)
					defer func() {
						if recover() != nil {
							panics.Add(1)
						}
					}()
					sum.Add(int64(v()))
				}()
				synctest.Wait()
			}
			s.Reset(nil, nil)
			synctest.Wait() // the caller in f waits on gate; the other one returned
			if finished.Load() != 1 || panics.Load() != 0 || sum.Load() != 0 {
				t.Errorf("after Reset(nil, nil): %d of 2 callers finished, %d panicked, sum %d; want 1, 0, 0", finished.Load(), panics.Load(), sum.Load())
			}
			close(gate)
			synctest.Wait()
			if finished.Load() != 2 || panics.Load() != 0 || sum.Load() != 7 {
				t.Fatalf("after f returned: %d of 2 callers finished, %d panicked, sum %d; want 2, 0, 7", finished.Load(), panics.Load(), sum.Load())
			}
		})
	})
}

// AT-XTE-19: the orders and draws follow the user stream exactly.
func TestATXTE19_OrdersFollowTheUserStream(t *testing.T) {
	type key struct {
		a int
		b string
	}
	m := map[key]int{{1, "x"}: 1, {1, "y"}: 2, {0, "z"}: 3, {2, "a"}: 4}
	bools := map[bool]int{true: 1, false: 0}
	shuffled := func(user interface{ Shuffle(int, func(int, int)) }, xs ...any) []any {
		user.Shuffle(len(xs), func(i, j int) { xs[i], xs[j] = xs[j], xs[i] })
		return xs
	}
	orders := map[string]bool{}
	for seed := uint64(1); seed <= 10; seed++ {
		synctest.Test(t, func(t *testing.T) {
			_, user := streams(seed)
			s.Reset(streams(seed))
			got := KeysCmp(m)
			want := shuffled(user, key{0, "z"}, key{1, "x"}, key{1, "y"}, key{2, "a"})
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("seed %d: KeysCmp = %v, want %v", seed, got, want)
			}
			if gb, wb := KeysCmp(bools), shuffled(user, false, true); fmt.Sprint(gb) != fmt.Sprint(wb) {
				t.Fatalf("seed %d: KeysCmp(bools) = %v, want %v", seed, gb, wb)
			}
			mm := map[int]int{1: 1, 2: 2, 3: 3}
			wantFirst := shuffled(user, 1, 2, 3)[0]
			var seen []int
			for k := range MapAll(mm) {
				seen = append(seen, k)
				for o := range mm { //faultline:maporder a deletion during the loop
					if o != k {
						delete(mm, o)
					}
				}
			}
			if len(seen) != 1 || seen[0] != wantFirst {
				t.Fatalf("seed %d: MapAll yielded %v, want only %v (later keys were deleted)", seed, seen, wantFirst)
			}
			if n, w := N(6), int(user.Uint64N(6)); n != w {
				t.Fatalf("seed %d: N(6) = %d, want %d", seed, n, w)
			}
			a, b := make(chan int, 1), make(chan int, 1)
			a <- 1
			b <- 2
			cases := []reflect.SelectCase{
				{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(a)},
				{Dir: reflect.SelectDefault},
				{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(b)},
			}
			order := []int{0, 1}
			user.Shuffle(2, func(i, j int) { order[i], order[j] = order[j], order[i] })
			if c, _, _ := ReflectSelect(cases); c != []int{0, 2}[order[0]] {
				t.Fatalf("seed %d: ReflectSelect chose %d, want %d", seed, c, []int{0, 2}[order[0]])
			}
			empty := []reflect.SelectCase{{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(make(chan int))}, {Dir: reflect.SelectDefault}}
			user.Shuffle(1, func(int, int) {})
			if c, _, _ := ReflectSelect(empty); c != 1 {
				t.Fatalf("seed %d: ReflectSelect with nothing ready chose %d, want the default's index 1", seed, c)
			}
			orders[fmt.Sprint(got)] = true
			s.Reset(nil, nil)
		})
	}
	if len(orders) < 2 {
		t.Fatal("KeysCmp gave one order for 10 seeds")
	}
	if got := KeysCmp(m); !slices.EqualFunc(got, []key{{0, "z"}, {1, "x"}, {1, "y"}, {2, "a"}}, func(a, b key) bool { return a == b }) {
		t.Fatalf("pass-through KeysCmp = %v, want sorted", got)
	}
}

// XTE-067: MapKeys and MapValues skip a key deleted during the loop.
func TestMapKeysAndValuesSkipDeletedKeys(t *testing.T) {
	m := map[int]string{1: "a", 2: "b", 3: "c"}
	var keys []int
	for k := range MapKeys(m) {
		keys = append(keys, k)
		delete(m, 2)
	}
	n := map[int]string{1: "a", 2: "b", 3: "c"}
	var values []string
	for v := range MapValues(n) {
		values = append(values, v)
		delete(n, 2)
	}
	if fmt.Sprint(keys, values) != "[1 3] [a c]" {
		t.Fatalf("MapKeys yielded %v, MapValues %v; want [1 3] and [a c]", keys, values)
	}
}

// XTE-064: on the driver, CondSignal moves sched's first waiter; outside an attempt the Cond
// helpers are c.Wait, c.Signal and c.Broadcast.
func TestCondOnDriverAndPassThrough(t *testing.T) {
	t.Run("driver signal", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s.Reset(streams(1))
			var mu sync.Mutex
			c := sync.NewCond(&mu)
			var woke atomic.Bool
			go func() {
				Lock(&mu)
				CondWait(c)
				woke.Store(true)
				Unlock(&mu)
			}()
			quiet := func() {
				for synctest.Wait(); s.Pending(); synctest.Wait() {
					s.Release()
				}
			}
			quiet()
			if woke.Load() {
				t.Fatal("CondWait returned with no signal")
			}
			CondSignal(c) // on the driver
			quiet()
			if !woke.Load() {
				t.Fatal("the driver's CondSignal did not wake the waiter")
			}
			s.Reset(nil, nil)
		})
	})
	t.Run("pass-through", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var mu sync.Mutex
			c := sync.NewCond(&mu)
			var woke atomic.Int32
			for range 3 {
				go func() {
					Lock(&mu)
					CondWaitFunc(c)()
					woke.Add(1)
					Unlock(&mu)
				}()
			}
			synctest.Wait()
			if n := woke.Load(); n != 0 {
				t.Fatalf("%d of 3 returned from CondWait with no signal", n)
			}
			CondSignalFunc(c)()
			synctest.Wait()
			if n := woke.Load(); n != 1 {
				t.Fatalf("CondSignal woke %d of 3 waiters, want 1", n)
			}
			CondBroadcastFunc(c)()
			synctest.Wait()
			if n := woke.Load(); n != 3 {
				t.Fatalf("after CondBroadcast %d of 3 waiters returned", n)
			}
		})
	})
}

// XTE-065: the method-value helpers stop at their function's scheduling point.
func TestFuncHelpersStopAtAPoint(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s.Reset(streams(1))
		var mu sync.Mutex
		var wg sync.WaitGroup
		var done atomic.Int32
		go func() { LockFunc(&mu)(); done.Add(1) }()
		go func() { WaitFunc(&wg)(); done.Add(1) }()
		synctest.Wait()
		if done.Load() != 0 || !s.Pending() {
			t.Fatalf("before a release: %d of 2 helpers returned, pending %v", done.Load(), s.Pending())
		}
		for s.Pending() {
			s.Release()
			synctest.Wait()
		}
		if done.Load() != 2 {
			t.Fatalf("%d of 2 helpers returned after the releases", done.Load())
		}
		UnlockFunc(&mu)()
		if !mu.TryLock() {
			t.Fatal("UnlockFunc did not unlock")
		}
		mu.Unlock()
		s.Reset(nil, nil)
	})
}

// XTE-066: ReflectSelect is a scheduling point; with no default it blocks until a case proceeds;
// a default case at index 0 maps back.
func TestReflectSelectPointBlockAndDefault(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s.Reset(streams(1))
		ch := make(chan int)
		var chosen atomic.Int64
		chosen.Store(-2)
		go func() {
			c, _, _ := ReflectSelect([]reflect.SelectCase{{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ch)}})
			chosen.Store(int64(c))
		}()
		synctest.Wait()
		if !s.Pending() {
			t.Fatal("ReflectSelect did not stop at a scheduling point")
		}
		s.Release()
		synctest.Wait()
		if c := chosen.Load(); c != -2 {
			t.Fatalf("ReflectSelect with no default and nothing ready returned %d", c)
		}
		ch <- 1
		synctest.Wait()
		if c := chosen.Load(); c != 0 {
			t.Fatalf("ReflectSelect chose %d after the send, want 0", c)
		}
		s.Reset(nil, nil)
		chosen.Store(-2)
		never := make(chan int)
		go func() {
			c, _, _ := ReflectSelect([]reflect.SelectCase{{Dir: reflect.SelectDefault}, {Dir: reflect.SelectRecv, Chan: reflect.ValueOf(never)}})
			chosen.Store(int64(c))
		}()
		synctest.Wait()
		if c := chosen.Load(); c != 0 {
			close(never)
			t.Fatalf("a default case at index 0: ReflectSelect chose %d, want 0", c)
		}
	})
}

// XTE-067: unsigned and array keys sort naturally outside an attempt; a break stops the Map
// iterators.
func TestKeysCmpUnsignedArraysAndBreak(t *testing.T) {
	arr := map[[2]uint8]int{{1, 2}: 0, {2, 1}: 0, {1, 3}: 0, {1, 0}: 0, {1, 1}: 0, {1, 4}: 0, {1, 5}: 0}
	if got := fmt.Sprint(KeysCmp(arr)); got != "[[1 0] [1 1] [1 2] [1 3] [1 4] [1 5] [2 1]]" {
		t.Fatalf("KeysCmp = %s", got)
	}
	m := map[int]int{1: 1, 2: 2, 3: 3}
	var got []int
	for k := range MapKeys(m) {
		got = append(got, k)
		break
	}
	for v := range MapValues(m) {
		got = append(got, v)
		break
	}
	for k := range MapAll(m) {
		got = append(got, k)
		break
	}
	if fmt.Sprint(got) != "[1 1 1]" {
		t.Fatalf("after a break: %v, want [1 1 1]", got)
	}
}

// XTE-063: a call that the end of an attempt releases before f returned gets zero values and does
// not read f's results while f writes them. Only virtual time orders the two, so the -race build
// reports a read that skips the check.
func TestOnceValuesReleasedEarly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s.Reset(streams(1))
		v := OnceValue(func() int { Point(); time.Sleep(time.Second); return 7 })
		vs := OnceValues(func() (int, int) { Point(); time.Sleep(time.Second); return 8, 9 })
		var sum atomic.Int64
		var finished atomic.Int32
		for range 2 {
			go func() { defer finished.Add(1); sum.Add(int64(v())) }()
			synctest.Wait()
			go func() { defer finished.Add(1); a, b := vs(); sum.Add(int64(a + b)) }()
			synctest.Wait()
		}
		s.Reset(nil, nil)
		synctest.Wait()
		if finished.Load() != 2 || sum.Load() != 0 {
			t.Fatalf("after Reset(nil, nil): %d of 4 calls returned, sum %d; want 2, 0", finished.Load(), sum.Load())
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if finished.Load() != 4 || sum.Load() != 24 {
			t.Fatalf("after f returned: %d of 4 calls returned, sum %d; want 4, 24", finished.Load(), sum.Load())
		}
	})
}

// XTE-062: RLocker's Unlock goes through RUnlock, which moves a writer parked on rw.
func TestRLockerMovesParkedWriter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s.Reset(streams(1))
		var rw sync.RWMutex
		var got atomic.Bool
		gate := make(chan struct{})
		go func() {
			l := RLocker(&rw)
			l.Lock()
			<-gate
			l.Unlock()
		}()
		synctest.Wait()
		s.Release() // the reader takes rw and waits on gate
		synctest.Wait()
		go func() {
			WLock(&rw)
			got.Store(true)
			WUnlock(&rw)
		}()
		synctest.Wait()
		s.Release() // the writer finds rw read-locked and parks on it
		synctest.Wait()
		close(gate)
		synctest.Wait()
		if !s.Pending() {
			t.Fatal("RLocker's Unlock did not move the parked writer to the scheduling-point list")
		}
		s.Release()
		synctest.Wait()
		if !got.Load() {
			t.Fatal("the writer did not get rw after RLocker's Unlock")
		}
		s.Reset(nil, nil)
	})
}

// XTE-061, XTE-065: callbacks of time.AfterFunc and context.AfterFunc that fire at one instant
// start in the order they were registered, so each seed replays. The runtime fires bubble timers of
// one instant in a random order and context's cancel walks its children in map order, so the
// callbacks' goroutine IDs alone do not follow a fixed order.
func TestFuncCallbacksOfOneInstantReplay(t *testing.T) {
	for _, tc := range []struct {
		name     string
		register func(fs []func())
	}{
		{"time.AfterFunc", func(fs []func()) {
			for _, f := range fs {
				time.AfterFunc(time.Millisecond, Func(f))
			}
		}},
		{"context.AfterFunc", func(fs []func()) {
			ctx, cancel := context.WithCancel(context.Background())
			for _, f := range fs {
				context.AfterFunc(ctx, Func(f))
			}
			cancel()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := func(seed uint64) string {
				var out string
				synctest.Test(t, func(t *testing.T) {
					var mu sync.Mutex
					var order []int // under mu
					var finished atomic.Int32
					drive(seed, func() {
						fs := make([]func(), 4)
						for i := range fs {
							fs[i] = func() {
								Lock(&mu)
								order = append(order, i)
								Unlock(&mu)
								finished.Add(1)
							}
						}
						tc.register(fs)
					}, func() bool { return finished.Load() == 4 })
					out = fmt.Sprint(order)
				})
				return out
			}
			distinct := map[string]bool{}
			for seed := uint64(1); seed <= 10; seed++ {
				first := run(seed)
				for range 10 {
					if got := run(seed); got != first {
						t.Fatalf("seed %d: the callbacks ran in the order %s, then %s", seed, first, got)
					}
				}
				distinct[first] = true
			}
			if len(distinct) < 2 {
				t.Fatal("one callback order for 10 seeds; the release stream must choose it")
			}
		})
	}
}

// XTE-061: Release starts fired callbacks lowest number first and draws nothing from the release
// stream for them, so the draws that follow pick among their goroutines in registration order.
func TestReleaseStartsFiredCallbacksInOrder(t *testing.T) {
	for seed := uint64(1); seed <= 10; seed++ {
		synctest.Test(t, func(t *testing.T) {
			s.Reset(streams(seed))
			var mu sync.Mutex // not sched's: f has no scheduling point
			var got []int
			for i := range 4 {
				time.AfterFunc(time.Millisecond, Func(func() {
					mu.Lock()
					got = append(got, i)
					mu.Unlock()
				}))
			}
			time.Sleep(time.Millisecond)
			for synctest.Wait(); s.Pending(); synctest.Wait() {
				s.Release()
			}
			release, _ := streams(seed)
			left, want := []int{0, 1, 2, 3}, []int(nil)
			for len(left) > 0 {
				i := release.IntN(len(left))
				want = append(want, left[i])
				left = slices.Delete(left, i, i+1)
			}
			if !slices.Equal(got, want) {
				t.Errorf("seed %d: the callbacks ran in the order %v, want %v", seed, got, want)
			}
			s.Reset(nil, nil)
		})
	}
}

// XTE-069: Reset lets a fired Func callback that waits for its start run f unscheduled.
func TestResetStartsFiredCallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s.Reset(streams(1))
		var ran atomic.Bool
		time.AfterFunc(time.Millisecond, Func(func() { ran.Store(true) }))
		time.Sleep(time.Millisecond)
		synctest.Wait()
		if !s.Pending() || ran.Load() {
			t.Fatalf("pending %v, ran %v before a release", s.Pending(), ran.Load())
		}
		s.Reset(nil, nil)
		synctest.Wait()
		if !ran.Load() {
			t.Fatal("Reset did not start the fired callback")
		}
	})
}

// XTE-062: a Lock on the driver that only a fired Func callback can unblock starts the callback
// instead of panicking with the deadlock text.
func TestDriverLockStartsFiredCallback(t *testing.T) {
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
		s.Release() // the goroutine takes mu and waits on gate
		time.AfterFunc(time.Millisecond, Func(func() { close(gate) }))
		time.Sleep(time.Millisecond) // the callback fires and waits for its start
		Lock(&mu)
		Unlock(&mu)
		s.Reset(nil, nil)
	})
}
