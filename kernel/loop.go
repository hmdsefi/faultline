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
// reason to stop. The caller pops the returned entry and executes it (step 8).
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
		if s.cfg.MaxEvents != 0 && s.executed >= s.cfg.MaxEvents {
			return nil, StopMaxEvents
		}
		return e, 0
	}
}

// execute runs a popped entry (KRN-040).
func (s *Sim) execute(e *entry) {
	s.now = e.at
	s.executed++
	id, label, cause, fn := e.id, e.label, e.cause, e.fn
	delete(s.pending, id)
	s.release(e)
	s.emitEvent("kernel.event", id, label, 0, 0, cause)
	fn()
}
