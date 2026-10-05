package kernel

import (
	"fmt"
	"strconv"
	"sync/atomic"
	"time"
)

// active is the Sim currently inside Step, Run, RunUntil, RunFor or a guarded call made outside
// the loop (KRN-110). It is the only package-level mutable state of the kernel (KRN-116).
var active atomic.Pointer[Sim]

// Active returns the Sim that is currently inside Step, Run, RunUntil, RunFor, or a guarded call
// made outside the loop, and nil otherwise. It exists for package assert (Phase 3). It is not
// meaningful if two Sims run concurrently on different goroutines, and it is process-wide: it does
// not identify the calling goroutine (KRN-110).
func Active() *Sim { return active.Load() }

// StopReason tells why Run, RunUntil or RunFor returned.
type StopReason uint8

const (
	StopIdle      StopReason = iota // the queue is empty (Run only)
	StopDeadline                    // the requested deadline was reached (RunUntil, RunFor)
	StopMaxTime                     // the next entry is later than Config.MaxTime
	StopMaxEvents                   // Config.MaxEvents events were executed and another is ready
	StopFailed                      // Err() != nil
)

// String returns "idle", "deadline", "max-time", "max-events", "failed", or "StopReason(<n>)".
func (r StopReason) String() string {
	switch r {
	case StopIdle:
		return "idle"
	case StopDeadline:
		return "deadline"
	case StopMaxTime:
		return "max-time"
	case StopMaxEvents:
		return "max-events"
	case StopFailed:
		return "failed"
	}
	return "StopReason(" + strconv.Itoa(int(r)) + ")"
}

// enter marks the Sim as running and makes it the active Sim (KRN-033, KRN-110). It returns the
// previously active Sim for leave.
func (s *Sim) enter() *Sim {
	if s.running {
		panic("kernel: Step/Run called while the simulation is running")
	}
	s.running = true
	return active.Swap(s)
}

// leave undoes enter. Callers defer it, so it also runs on panic and runtime.Goexit.
func (s *Sim) leave(prev *Sim) {
	s.running = false
	active.Store(prev)
}

// Step processes queue entries until exactly one event has executed and returns true, or returns
// false without executing an event if the Sim has failed, the queue is empty, or a limit applies.
func (s *Sim) Step() bool {
	prev := s.enter()
	defer s.leave(prev)
	e, _ := s.ready(0, false)
	if e == nil {
		return false
	}
	s.q.pop()
	s.execute(e)
	return true
}

// Run processes events until the queue is empty, a limit applies, or the Sim fails.
func (s *Sim) Run() StopReason {
	prev := s.enter()
	defer s.leave(prev)
	return s.loop(0, false)
}

// RunUntil processes every event with at <= t, then sets Now() to t and returns StopDeadline,
// unless the Sim fails or a limit applies first (KRN-036). It panics if t < Now().
func (s *Sim) RunUntil(t Time) StopReason {
	prev := s.enter()
	defer s.leave(prev)
	if t < s.now {
		panic(fmt.Sprintf("kernel: RunUntil(%s) is before Now() %s", t, s.now))
	}
	return s.loop(t, true)
}

// RunFor is RunUntil(Now().Add(d)). It panics if d < 0.
func (s *Sim) RunFor(d time.Duration) StopReason {
	prev := s.enter()
	defer s.leave(prev)
	if d < 0 {
		panic(fmt.Sprintf("kernel: RunFor(%s): negative duration", d))
	}
	return s.loop(s.now.Add(d), true)
}

// loop is the run loop of KRN-036.
func (s *Sim) loop(deadline Time, hasDeadline bool) StopReason {
	for {
		e, stop := s.ready(deadline, hasDeadline)
		if e == nil {
			return stop
		}
		s.q.pop()
		s.execute(e)
	}
}

// ready runs steps 1-7 of KRN-036: it returns the queue head if it may execute now, or nil and the
// reason to stop. On the way it defers the heads of paused nodes (step 6), which moves Now. The
// caller pops the returned entry and executes it (step 8).
func (s *Sim) ready(deadline Time, hasDeadline bool) (*entry, StopReason) {
	for {
		if s.err != nil {
			return nil, StopFailed
		}
		e := s.peek()
		if hasDeadline && (e == nil || e.at > deadline) {
			if s.cfg.MaxTime != 0 && deadline > s.cfg.MaxTime {
				return nil, StopMaxTime
			}
			s.now = deadline
			return nil, StopDeadline
		}
		if e == nil {
			return nil, StopIdle
		}
		if s.cfg.MaxTime != 0 && e.at > s.cfg.MaxTime {
			return nil, StopMaxTime
		}
		if e.kind == entryNode && e.node.state == NodePaused {
			s.deferHead(e)
			continue
		}
		if s.cfg.MaxEvents != 0 && s.executed >= s.cfg.MaxEvents {
			return nil, StopMaxEvents
		}
		return e, 0
	}
}

// execute runs a popped entry (KRN-040): it advances the clock, emits kernel.event, runs the
// callback and then the observers registered before the event started, all as guarded calls.
func (s *Sim) execute(e *entry) {
	s.now = e.at
	s.executed++
	nobs := len(s.observers)
	id, label, cause, fn, node, inc := e.id, e.label, e.cause, e.fn, e.node, e.inc
	delete(s.pending, id)
	if e.kind == entryBoot {
		node.bootEv = nil
	}
	s.release(e)
	var nid NodeID
	if node != nil {
		nid = node.id
	}
	s.emitEvent("kernel.event", id, label, nid, inc, cause)
	s.ctx = guardCtx{event: id, label: label, node: node, inc: inc}
	s.guard(fn)
	if s.err == nil && nobs > 0 {
		s.ctx.observer = true
		for _, o := range s.observers[:nobs] {
			s.guard(o)
			if s.err != nil {
				break
			}
		}
	}
}

// call runs fn for a lifecycle operation on n: the OnCrash hooks of Crash (label "crash") or the
// boot procedure of Restart (label "restart"). Inside the loop or another guarded call it runs fn
// directly, so a panic reaches the enclosing guard. Otherwise it runs fn as a guarded call with
// the Sim marked running and active (KRN-045).
func (s *Sim) call(n *Node, label string, fn func()) {
	if s.running {
		fn()
		return
	}
	prev := s.enter()
	defer s.leave(prev)
	s.ctx = guardCtx{label: label, node: n}
	s.guard(fn)
}

// deferHead moves e, the head returned by peek and an entry of a paused node, to the node's deferred
// list: it pops e, sets Now to e.at and emits kernel.defer (KRN-036 step 6).
func (s *Sim) deferHead(e *entry) {
	s.q.pop()
	s.now = e.at
	n := e.node
	n.deferred = append(n.deferred, e)
	s.emitEvent("kernel.defer", e.id, e.label, n.id, e.inc, e.cause)
}
