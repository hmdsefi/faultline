// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package sched

import (
	"sync"
	"sync/atomic"
)

// ILock replaces a Lock call through an interface value: mutexes go through Lock and WLock,
// other lockers are called directly.
func ILock(l interface{ Lock() }) {
	switch m := l.(type) {
	case *sync.Mutex:
		Lock(m)
	case *sync.RWMutex:
		WLock(m)
	default:
		l.Lock()
	}
}

// IUnlock replaces an Unlock call through an interface value.
func IUnlock(l interface{ Unlock() }) {
	switch m := l.(type) {
	case *sync.Mutex:
		Unlock(m)
	case *sync.RWMutex:
		WUnlock(m)
	default:
		l.Unlock()
	}
}

type rlocker struct{ rw *sync.RWMutex }

func (r rlocker) Lock()   { RLock(r.rw) }
func (r rlocker) Unlock() { RUnlock(r.rw) }

// RLocker replaces rw.RLocker(): its Lock and Unlock read-lock and read-unlock rw.
func RLocker(rw *sync.RWMutex) sync.Locker { return rlocker{rw} }

// onceFunc runs f once through a scheduled Once. Its function reports whether f returned, and
// panics with f's value when f panicked. When the end of an attempt releases a caller while f
// still runs, that caller gets false at once (XTE-063); done is atomic because the caller in f may
// write it at that moment.
func onceFunc(f func()) func() bool {
	var (
		o    sync.Once
		done atomic.Int32 // 1: f returned; 2: f panicked with p
		p    any
	)
	return func() bool {
		OnceDo(&o, func() {
			defer func() {
				if done.Load() != 1 {
					p = recover()
					done.Store(2)
					panic(p)
				}
			}()
			f()
			done.Store(1)
		})
		switch done.Load() {
		case 1:
			return true
		case 2:
			panic(p)
		}
		return false
	}
}

// OnceFunc replaces sync.OnceFunc: f runs once through a scheduled Once; when f panicked, every
// call panics with the same value.
func OnceFunc(f func()) func() {
	g := onceFunc(f)
	return func() { g() }
}

// OnceValue replaces sync.OnceValue. A call that returns before f returned, because the attempt
// ended, gets the zero value.
func OnceValue[T any](f func() T) func() T {
	var r T
	g := onceFunc(func() { r = f() })
	return func() T {
		if !g() {
			var zero T
			return zero
		}
		return r
	}
}

// OnceValues replaces sync.OnceValues. A call that returns before f returned, because the attempt
// ended, gets the zero values.
func OnceValues[T1, T2 any](f func() (T1, T2)) func() (T1, T2) {
	var (
		r1 T1
		r2 T2
	)
	g := onceFunc(func() { r1, r2 = f() })
	return func() (T1, T2) {
		if !g() {
			var z1 T1
			var z2 T2
			return z1, z2
		}
		return r1, r2
	}
}

// CondWait replaces c.Wait(): the caller waits durably in wait order, unlocking c.L meanwhile.
func CondWait(c *sync.Cond) {
	id := point()
	s.mu.Lock()
	if !s.parks(id) {
		s.mu.Unlock()
		c.Wait()
		return
	}
	w := waiter{gid: id.gid, ch: make(chan struct{})}
	s.conds[c] = append(s.conds[c], w)
	s.mu.Unlock()
	IUnlock(c.L)
	<-w.ch
	ILock(c.L)
}

// CondSignal replaces c.Signal(): the first waiter of c moves to the scheduling-point list.
func CondSignal(c *sync.Cond) { condWake(c, false) }

// CondBroadcast replaces c.Broadcast(): every waiter of c moves to the scheduling-point list.
func CondBroadcast(c *sync.Cond) { condWake(c, true) }

func condWake(c *sync.Cond, all bool) {
	id := point()
	s.mu.Lock()
	if !s.inAttempt(id) {
		s.mu.Unlock()
		if all {
			c.Broadcast()
		} else {
			c.Signal()
		}
		return
	}
	ws := s.conds[c]
	n := min(len(ws), 1)
	if all {
		n = len(ws)
	}
	s.waiting = append(s.waiting, ws[:n]...)
	if n == len(ws) {
		delete(s.conds, c)
	} else {
		s.conds[c] = ws[n:]
	}
	s.mu.Unlock()
	if n > 0 {
		wake()
	}
}

// WaitGroupGo replaces wg.Go(f): f starts at a scheduling point; a panic in f is raised again
// without Done, as (*sync.WaitGroup).Go does.
func WaitGroupGo(wg *sync.WaitGroup, f func()) {
	wg.Add(1)
	Go(func() {
		defer func() {
			if x := recover(); x != nil {
				panic(x)
			}
			wg.Done()
		}()
		f()
	})
}

// funcs numbers Func callbacks in registration order.
var funcs atomic.Uint64

// Func wraps the function of time.AfterFunc and context.AfterFunc: it starts at a scheduling
// point. Callbacks that fire together start in the order of their Func calls. Func(nil) is nil.
func Func(f func()) func() {
	if f == nil {
		return nil
	}
	seq := funcs.Add(1)
	return func() {
		// XTE-061: timers of one instant fire in a random order and a cancel walks its children
		// in map order, so the callback waits in s.fired and Release starts f in registration
		// order; f's goroutine ID then follows that order.
		if s.on.Load() {
			id := self()
			s.mu.Lock()
			if s.parks(id) {
				s.park(id, func(w waiter) { s.fired = append(s.fired, waiter{seq, w.ch}) }, true)
				Go(f)
				return
			}
			s.mu.Unlock()
		}
		f()
	}
}

// The method values of the rewritten sync methods.
func LockFunc(m *sync.Mutex) func()          { return func() { Lock(m) } }
func UnlockFunc(m *sync.Mutex) func()        { return func() { Unlock(m) } }
func RLockFunc(rw *sync.RWMutex) func()      { return func() { RLock(rw) } }
func RUnlockFunc(rw *sync.RWMutex) func()    { return func() { RUnlock(rw) } }
func WLockFunc(rw *sync.RWMutex) func()      { return func() { WLock(rw) } }
func WUnlockFunc(rw *sync.RWMutex) func()    { return func() { WUnlock(rw) } }
func OnceDoFunc(o *sync.Once) func(func())   { return func(f func()) { OnceDo(o, f) } }
func WaitFunc(wg *sync.WaitGroup) func()     { return func() { Point(); wg.Wait() } }
func GoFunc(wg *sync.WaitGroup) func(func()) { return func(f func()) { WaitGroupGo(wg, f) } }
func CondWaitFunc(c *sync.Cond) func()       { return func() { CondWait(c) } }
func CondSignalFunc(c *sync.Cond) func()     { return func() { CondSignal(c) } }
func CondBroadcastFunc(c *sync.Cond) func()  { return func() { CondBroadcast(c) } }
