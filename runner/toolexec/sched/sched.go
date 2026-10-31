// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package sched is the runtime of faultline's toolexec backend. Code that faultline-toolexec
// rewrote calls it; tests never import it.
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
	"strings"
	"sync"
	"sync/atomic"
	"testing/synctest"

	"github.com/hmdsefi/faultline/internal/gosched"
)

// deadlock is the panic of a Lock on the driver goroutine whose holder is durably blocked.
const deadlock = "faultline: toolexec backend: the driver goroutine (the body, an invariant or a final check) locks a mutex that a blocked goroutine holds; do not lock in the body or a check a mutex that a process holds across a blocking call"

// identity is a goroutine's ID and its synctest bubble's ID (0 outside any bubble).
type identity struct{ gid, bubble uint64 }

// self reads the caller's identity from the first line of its stack,
// "goroutine <id> [<state>, synctest bubble <b>]:", with one runtime.Stack call.
func self() identity {
	var buf [128]byte
	line := buf[:runtime.Stack(buf[:], false)]
	if i := bytes.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	var id identity
	rest, ok := bytes.CutPrefix(line, []byte("goroutine "))
	if !ok {
		return id
	}
	if i := bytes.IndexByte(rest, ' '); i > 0 {
		id.gid, _ = strconv.ParseUint(string(rest[:i]), 10, 64)
	}
	if _, b, ok := bytes.Cut(rest, []byte("synctest bubble ")); ok {
		n := 0
		for n < len(b) && b[n] >= '0' && b[n] <= '9' {
			n++
		}
		id.bubble, _ = strconv.ParseUint(string(b[:n]), 10, 64)
	}
	return id
}

// waiter is a goroutine parked at a scheduling point, on a mutex or on a Cond.
type waiter struct {
	gid uint64        // goroutine ID, the tie-break: start order while goroutine mode runs on one P
	ch  chan struct{} // closed to release; created in the bubble, so the receive is durable
}

type onceState struct {
	state   int // 0 not started, 1 running, 2 done
	waiters []chan struct{}
}

type scheduler struct {
	on      atomic.Bool // an attempt is in progress; read before the stack walk, without mu
	mu      sync.Mutex
	active  bool
	driver  identity // the goroutine that called Reset with streams, and its bubble
	release *rand.Rand
	user    *rand.Rand
	waiting []waiter
	mutexes map[any][]waiter // per-mutex waiters, keyed by *sync.Mutex or *sync.RWMutex
	onces   map[*sync.Once]*onceState
	conds   map[*sync.Cond][]waiter // per-Cond waiters, in wait order
}

var s = &scheduler{mutexes: map[any][]waiter{}, onces: map[*sync.Once]*onceState{}, conds: map[*sync.Cond][]waiter{}}

func init() { gosched.Active = s } //nolint:gochecknoinits // registers the scheduler with the driver before any test runs

// parks reports whether id waits at scheduling points: a goroutine of the attempt's bubble other
// than the driver. s.mu must be held.
func (s *scheduler) parks(id identity) bool {
	return s.active && id.bubble != 0 && id.bubble == s.driver.bubble && id.gid != s.driver.gid
}

// inAttempt reports whether id is the driver or another goroutine of the attempt's bubble, the
// goroutines that may call gosched.Wake. s.mu must be held.
func (s *scheduler) inAttempt(id identity) bool {
	return s.active && id.bubble != 0 && id.bubble == s.driver.bubble
}

// Reset implements gosched.Scheduler.
func (s *scheduler) Reset(release, user *rand.Rand) {
	var id identity
	if release != nil {
		id = self()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.waiting {
		close(w.ch)
	}
	for _, ws := range s.mutexes { //faultline:maporder every waiter's channel is closed; the order does not matter
		for _, w := range ws {
			close(w.ch)
		}
	}
	for _, ws := range s.conds { //faultline:maporder every waiter's channel is closed; the order does not matter
		for _, w := range ws {
			close(w.ch)
		}
	}
	for _, o := range s.onces { //faultline:maporder every waiter's channel is closed; the order does not matter
		for _, ch := range o.waiters {
			close(ch)
		}
		o.waiters = nil // the caller still running f would close them again
	}
	s.waiting, s.mutexes, s.onces = nil, map[any][]waiter{}, map[*sync.Once]*onceState{}
	s.conds = map[*sync.Cond][]waiter{}
	s.release, s.user, s.active, s.driver = release, user, release != nil, id
	s.on.Store(s.active)
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

// gaps is the registry of Gaps.
var gaps struct {
	mu    sync.Mutex
	lines []string
}

// Gaps registers coverage gaps, one "<file>:<line>: <kind>" per line. Rewritten files call it in
// a package-level variable initializer; it always returns true.
func Gaps(lines string) bool {
	gaps.mu.Lock()
	defer gaps.mu.Unlock()
	for _, l := range strings.Split(lines, "\n") {
		if l != "" {
			gaps.lines = append(gaps.lines, l)
		}
	}
	return true
}

// Gaps implements gosched.Scheduler.
func (*scheduler) Gaps() []string {
	gaps.mu.Lock()
	defer gaps.mu.Unlock()
	out := slices.Clone(gaps.lines)
	slices.Sort(out)
	return slices.Compact(out)
}

// park registers the caller with add and blocks until released. s.mu must be held; park
// releases it. wakeDriver says the new waiter can make Pending true.
func (s *scheduler) park(id identity, add func(w waiter), wakeDriver bool) {
	w := waiter{gid: id.gid, ch: make(chan struct{})}
	add(w)
	s.mu.Unlock()
	if wakeDriver {
		wake()
	}
	<-w.ch
}

// wake tells the driver that a goroutine may wait at a scheduling point, so that it releases the
// goroutine at the current instant even while it waits for virtual time. Only goroutines of the
// attempt's bubble call it.
func wake() {
	if gosched.Wake != nil {
		gosched.Wake()
	}
}

// wakeIfInAttempt calls wake when the caller is the driver or another goroutine of the attempt's
// bubble: Wake sends on a channel of that bubble.
func wakeIfInAttempt() {
	id := self()
	s.mu.Lock()
	ok := s.inAttempt(id)
	s.mu.Unlock()
	if ok {
		wake()
	}
}

// point is a scheduling point that returns the caller's identity, or the zero identity when no
// attempt is in progress.
func point() identity {
	if !s.on.Load() {
		return identity{}
	}
	id := self()
	s.mu.Lock()
	if !s.parks(id) {
		s.mu.Unlock()
		return id
	}
	s.park(id, func(w waiter) { s.waiting = append(s.waiting, w) }, true)
	return id
}

// Point is a scheduling point.
func Point() { point() }

// Go starts f as a scheduled goroutine.
func Go(f func()) {
	go func() {
		Point()
		f()
	}()
}

// lockWith acquires a lock with try; key identifies the lock's waiter list.
func lockWith(key any, try func() bool, block func()) {
	id := point()
	for {
		s.mu.Lock()
		if try() {
			s.mu.Unlock()
			return
		}
		switch {
		case s.parks(id):
			s.park(id, func(w waiter) { s.mutexes[key] = append(s.mutexes[key], w) }, false)
		case s.active && id.gid == s.driver.gid:
			s.mu.Unlock()
			synctest.Wait() // goroutines the driver code woke stop at their next point first
			s.mu.Lock()
			if try() {
				s.mu.Unlock()
				return
			}
			pending := len(s.waiting) > 0
			s.mu.Unlock()
			if !pending {
				panic(deadlock)
			}
			s.Release()
		default: // no attempt, or a goroutine outside the attempt's bubble
			s.mu.Unlock()
			block()
			return
		}
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
		wakeIfInAttempt()
	}
}

// Lock replaces m.Lock(): a scheduling point, then a durable wait while m is taken.
func Lock(m *sync.Mutex) { lockWith(m, m.TryLock, m.Lock) }

// Unlock replaces m.Unlock(): the goroutines waiting for m move to the scheduling-point list.
func Unlock(m *sync.Mutex) { unlockWith(m, m.Unlock) }

// RLock replaces m.RLock(): a scheduling point, then a durable wait while a writer holds m.
func RLock(m *sync.RWMutex) { lockWith(m, m.TryRLock, m.RLock) }

// RUnlock replaces m.RUnlock(): the goroutines waiting for m move to the scheduling-point list.
func RUnlock(m *sync.RWMutex) { unlockWith(m, m.RUnlock) }

// WLock replaces m.Lock() of a sync.RWMutex: a scheduling point, then a durable wait while m is
// taken.
func WLock(m *sync.RWMutex) { lockWith(m, m.TryLock, m.Lock) }

// WUnlock replaces m.Unlock() of a sync.RWMutex: the goroutines waiting for m move to the
// scheduling-point list.
func WUnlock(m *sync.RWMutex) { unlockWith(m, m.Unlock) }

// OnceDo runs f through o once; a concurrent caller waits durably until f returned. When the
// attempt ends while f runs, the waiting callers return at once, before f has returned.
func OnceDo(o *sync.Once, f func()) {
	var id identity
	if s.on.Load() {
		id = self()
	}
	s.mu.Lock()
	if !s.parks(id) { // no attempt, the driver, or a goroutine outside the bubble
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
