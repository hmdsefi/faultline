package kernel

import (
	"fmt"
	"slices"
	"time"
)

// EventID identifies a scheduled event. 0 is never a valid event.
type EventID uint64

// At schedules fn to run at time t. It panics if t < Now(), label is empty, or fn is nil.
func (s *Sim) At(t Time, label string, fn func()) EventID {
	if t < s.now {
		panic(fmt.Sprintf("kernel: At(%s, %q) is before Now() %s", t, label, s.now))
	}
	checkEvent(label, fn)
	return s.schedule(t, label, fn, false).id
}

// After schedules fn to run at Now().Add(d). A negative d is treated as 0.
func (s *Sim) After(d time.Duration, label string, fn func()) EventID {
	checkEvent(label, fn)
	return s.schedule(s.now.Add(max(d, 0)), label, fn, false).id
}

// checkEvent panics on an empty label or a nil callback (KRN-020).
func checkEvent(label string, fn func()) {
	if label == "" {
		panic("kernel: empty event label")
	}
	if fn == nil {
		panic(fmt.Sprintf("kernel: nil callback for event %q", label))
	}
}

// schedule creates and inserts an entry (KRN-022): next EventID, next seq, the tie-break, the
// current cause. With front set the tie-break is 0 and nothing is drawn (AtFront, KRN-026).
func (s *Sim) schedule(at Time, label string, fn func(), front bool) *entry {
	s.nextID++
	s.nextSeq++
	e := s.newEntry()
	e.id = EventID(s.nextID)
	e.seq = s.nextSeq
	if !front && s.cfg.TieBreak == TieBreakSeeded {
		e.tb = s.sched.Uint64()
	}
	e.cause = s.tr.seq
	e.at, e.label, e.fn = at, label, fn
	s.q.push(e)
	s.pending[e.id] = e
	return e
}

// Cancel removes a pending event. It reports whether the call prevented the event from running.
// The initial boot event of a node cannot be cancelled; a stale node-bound entry is removed and
// false is returned (KRN-024).
func (s *Sim) Cancel(id EventID) bool {
	e, ok := s.pending[id]
	if !ok || e.kind == entryBoot {
		return false
	}
	stale := e.stale()
	if e.idx >= 0 {
		s.q.remove(e.idx)
	} else {
		n := e.node
		k := slices.Index(n.deferred, e)
		n.deferred = slices.Delete(n.deferred, k, k+1)
	}
	s.discard(e)
	return !stale
}

// NextAt returns the time of the next entry the loop would process and true, or 0 and false if
// the queue is empty. Stale entries at the head of the queue are discarded first.
func (s *Sim) NextAt() (Time, bool) {
	if e := s.peek(); e != nil {
		return e.at, true
	}
	return 0, false
}

// peek discards stale entries at the head of the queue, without a record and without moving the
// clock, and returns the head, or nil if the queue is empty (KRN-032).
func (s *Sim) peek() *entry {
	for len(s.q) > 0 {
		e := s.q[0]
		if !e.stale() {
			return e
		}
		s.q.pop()
		s.discard(e)
	}
	return nil
}

// newEntry returns a cleared entry from the free list, or a new one.
func (s *Sim) newEntry() *entry {
	if n := len(s.free); n > 0 {
		e := s.free[n-1]
		s.free = s.free[:n-1]
		return e
	}
	return &entry{idx: -1}
}

// release clears e, so its closure can be collected, and puts it on the free list.
func (s *Sim) release(e *entry) {
	*e = entry{idx: -1}
	s.free = append(s.free, e)
}

// AtFront schedules fn at time t like At, but with tie-break 0 and no draw from the
// "kernel/sched" stream, so under TieBreakSeeded it runs before every seeded event at t (KRN-026,
// KRN-027). It is for the fault injector and planners (FLT). It panics like At.
func (s *Sim) AtFront(t Time, label string, fn func()) EventID {
	if t < s.now {
		panic(fmt.Sprintf("kernel: AtFront(%s, %q) is before Now() %s", t, label, s.now))
	}
	checkEvent(label, fn)
	return s.schedule(t, label, fn, true).id
}

// discard forgets an entry that has left the queue or a deferred list without running.
func (s *Sim) discard(e *entry) {
	delete(s.pending, e.id)
	if e.kind == entryBoot && e.node.bootEv == e {
		e.node.bootEv = nil
	}
	s.release(e)
}
