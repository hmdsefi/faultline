// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package sched is the runtime of prototype B. Rewritten code calls it.
package sched

import (
	"bytes"
	"cmp"
	mrand "math/rand"
	"math/rand/v2"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"testing/synctest"

	"github.com/hmdsefi/faultline/internal/gosched"
)

// waiter is a goroutine parked at a scheduling point or on a mutex.
type waiter struct {
	gid uint64        // goroutine ID, the tie-break: start order while goroutine mode runs on one P
	ch  chan struct{} // closed to release; created in the bubble, so the receive is durable
}

type onceState struct {
	state   int // 0 not started, 1 running, 2 done
	waiters []chan struct{}
}

type scheduler struct {
	mu      sync.Mutex
	active  bool
	release *rand.Rand
	user    *rand.Rand
	waiting []waiter
	mutexes map[any][]waiter // per-mutex waiters, keyed by *sync.Mutex or *sync.RWMutex
	onces   map[*sync.Once]*onceState
}

var s = &scheduler{mutexes: map[any][]waiter{}, onces: map[*sync.Once]*onceState{}}

func init() { gosched.Active = s } //nolint:gochecknoinits // registers the scheduler with the driver before any test runs

// goid returns the calling goroutine's ID from the first line of its stack ("goroutine N [").
func goid() uint64 {
	var buf [64]byte
	b := buf[:runtime.Stack(buf[:], false)]
	b = bytes.TrimPrefix(b, []byte("goroutine "))
	if i := bytes.IndexByte(b, ' '); i > 0 {
		b = b[:i]
	}
	n, _ := strconv.ParseUint(string(b), 10, 64)
	return n
}

func onDriver() bool { return gosched.OnDriver != nil && gosched.OnDriver() }

// Reset implements gosched.Scheduler.
func (s *scheduler) Reset(release, user *rand.Rand) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.waiting {
		close(w.ch)
	}
	for _, ws := range s.mutexes {
		for _, w := range ws {
			close(w.ch)
		}
	}
	for _, o := range s.onces {
		for _, ch := range o.waiters {
			close(ch)
		}
		o.waiters = nil // the caller still running f would close them again
	}
	s.waiting, s.mutexes, s.onces = nil, map[any][]waiter{}, map[*sync.Once]*onceState{}
	s.release, s.user, s.active = release, user, release != nil
	if release != nil {
		// Drop the bytes RandV1().Read buffered in the attempt before. Only at the start: no
		// goroutine of the attempt runs yet, while at the end woken goroutines may call Read.
		randV1.Seed(0)
	}
}

// Pending implements gosched.Scheduler.
func (s *scheduler) Pending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active && len(s.waiting) > 0
}

// Release implements gosched.Scheduler: the waiting list is ordered by goroutine ID, then one
// waiter is chosen with the release stream.
func (s *scheduler) Release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active || len(s.waiting) == 0 {
		return
	}
	slices.SortFunc(s.waiting, func(a, b waiter) int { return cmp.Compare(a.gid, b.gid) })
	i := s.release.IntN(len(s.waiting))
	w := s.waiting[i]
	s.waiting = slices.Delete(s.waiting, i, i+1)
	close(w.ch)
}

// park registers the caller with add, wakes the driver and blocks until released. s.mu must be
// held; park releases it.
func (s *scheduler) park(add func(w waiter)) {
	w := waiter{gid: goid(), ch: make(chan struct{})}
	add(w)
	s.mu.Unlock()
	wake()
	<-w.ch
}

// wake tells the driver that a goroutine may wait at a scheduling point, so that it releases
// the goroutine at the current instant even while it waits for virtual time.
func wake() {
	if gosched.Wake != nil {
		gosched.Wake()
	}
}

// Point is a scheduling point.
func Point() {
	if onDriver() {
		return
	}
	s.mu.Lock()
	if !s.active {
		s.mu.Unlock()
		return
	}
	s.park(func(w waiter) { s.waiting = append(s.waiting, w) })
}

// Go starts f as a scheduled goroutine.
func Go(f func()) {
	go func() {
		Point()
		f()
	}()
}

// lockWith acquires a lock with try; key identifies the lock's waiter list.
func lockWith(key any, try func() bool, block func()) {
	Point()
	for {
		s.mu.Lock()
		if try() {
			s.mu.Unlock()
			return
		}
		if !s.active {
			s.mu.Unlock()
			block()
			return
		}
		if onDriver() {
			pending := len(s.waiting) > 0
			s.mu.Unlock()
			if !pending {
				block()
				return
			}
			s.Release()
			synctest.Wait()
			continue
		}
		s.park(func(w waiter) { s.mutexes[key] = append(s.mutexes[key], w) })
	}
}

// unlockWith releases a lock and moves its waiters to the scheduling-point list.
func unlockWith(key any, unlock func()) {
	s.mu.Lock()
	unlock()
	moved := len(s.mutexes[key]) > 0
	s.waiting = append(s.waiting, s.mutexes[key]...)
	delete(s.mutexes, key)
	s.mu.Unlock()
	if moved {
		wake()
	}
}

func Lock(m *sync.Mutex)      { lockWith(m, m.TryLock, m.Lock) }
func Unlock(m *sync.Mutex)    { unlockWith(m, m.Unlock) }
func RLock(m *sync.RWMutex)   { lockWith(m, m.TryRLock, m.RLock) }
func RUnlock(m *sync.RWMutex) { unlockWith(m, m.RUnlock) }
func WLock(m *sync.RWMutex)   { lockWith(m, m.TryLock, m.Lock) }
func WUnlock(m *sync.RWMutex) { unlockWith(m, m.Unlock) }

// OnceDo runs f through o once; a concurrent caller waits durably until f returned. When the
// attempt ends while f runs, the waiting callers return at once, before f has returned.
func OnceDo(o *sync.Once, f func()) {
	s.mu.Lock()
	if !s.active {
		s.mu.Unlock()
		o.Do(f)
		return
	}
	st := s.onces[o]
	if st == nil {
		st = &onceState{}
		s.onces[o] = st
	}
	switch st.state {
	case 2:
		s.mu.Unlock()
		return
	case 1:
		ch := make(chan struct{})
		st.waiters = append(st.waiters, ch)
		s.mu.Unlock()
		<-ch
		return
	}
	st.state = 1
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		st.state = 2
		for _, ch := range st.waiters {
			close(ch)
		}
		st.waiters = nil
		s.mu.Unlock()
	}()
	o.Do(f)
}

// Select chooses a ready case in an order drawn from the user stream. It returns -1
// for the default case.
func Select(cases []reflect.SelectCase, hasDefault bool) (chosen int, recv reflect.Value, recvOK bool) {
	order := make([]int, len(cases))
	for i := range order {
		order[i] = i
	}
	s.mu.Lock()
	if s.active && s.user != nil {
		s.user.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
	}
	s.mu.Unlock()
	for _, i := range order {
		one := []reflect.SelectCase{cases[i], {Dir: reflect.SelectDefault}}
		if c, v, ok := reflect.Select(one); c == 0 {
			return i, v, ok
		}
	}
	if hasDefault {
		return -1, reflect.Value{}, false
	}
	return reflect.Select(cases)
}

// Keys returns m's keys sorted, then shuffled with the user stream.
func Keys[K cmp.Ordered, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	s.mu.Lock()
	if s.active && s.user != nil {
		s.user.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
	}
	s.mu.Unlock()
	return keys
}

// userSource draws from the current attempt's user stream, or from the global generator
// outside attempts.
type userSource struct{}

func (userSource) Uint64() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active && s.user != nil {
		return s.user.Uint64()
	}
	return rand.Uint64() //nolint:gosec // pass-through: the global generator the rewritten calls used before
}

// v1Source adapts userSource to math/rand.Source64.
type v1Source struct{}

func (v1Source) Int63() int64   { return int64(userSource{}.Uint64() >> 1) }
func (v1Source) Uint64() uint64 { return userSource{}.Uint64() }
func (v1Source) Seed(int64)     {}

var (
	randV2 = rand.New(userSource{}) //nolint:gosec // draws from the attempt's seeded user stream
	randV1 = mrand.New(v1Source{})  //nolint:gosec // draws from the attempt's seeded user stream
)

// Rand replaces the package-level functions of math/rand/v2.
func Rand() *rand.Rand { return randV2 }

// RandV1 replaces the package-level functions of math/rand.
func RandV1() *mrand.Rand { return randV1 }
