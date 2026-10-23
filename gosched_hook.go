// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"runtime"
	"strings"
	"sync/atomic"

	"github.com/hmdsefi/faultline/internal/gosched"
)

// activeDriver is the driver of the goroutine-mode attempt in progress (API-104 allows one at a
// time). Prototype B's scheduler asks it whether a caller is the driver goroutine, and wakes it
// when a goroutine starts waiting at a scheduling point (spike branch, EXB-036).
var activeDriver atomic.Pointer[driver]

func init() { //nolint:gochecknoinits // gosched.OnDriver and gosched.Wake must be set before any test runs (EXB §4.2)
	gosched.OnDriver = func() bool {
		d := activeDriver.Load()
		return d != nil && d.OnDriver()
	}
	gosched.Wake = func() {
		if d := activeDriver.Load(); d != nil && d.inBubble() {
			d.signal() // GOR-024: the driver's next wait returns at once
		}
	}
}

// inBubble reports whether the caller is a goroutine of the synctest bubble of d's attempt. Only
// those goroutines may send on d.wake: a send from outside the bubble is a fatal runtime error.
func (d *driver) inBubble() bool {
	var buf [256]byte
	line, _, _ := strings.Cut(string(buf[:runtime.Stack(buf[:], false)]), "\n")
	_, bubble, ok := parseStackHeader(line)
	return ok && bubble == d.ga.bubble.Load()
}

// schedStart publishes d for gosched.OnDriver and gosched.Wake, also when no scheduler is set.
// It then resets prototype B's scheduler for a new attempt (EXB-036): releases draw from
// toolexec/release, user randomness from toolexec/user.
func (d *driver) schedStart() {
	activeDriver.Store(d)
	if a := gosched.Active; a != nil {
		a.Reset(d.w.Sim.Rand("toolexec/release"), d.w.Sim.Rand("toolexec/user"))
	}
}

// schedRelease lets one goroutine waiting at a scheduling point continue and reports whether one
// was waiting. The driver calls it after each quiescence, before time or kernel events advance.
func (d *driver) schedRelease() bool {
	a := gosched.Active
	if a == nil || !a.Pending() {
		return false
	}
	a.Release()
	return true
}

// schedEnd lets every waiting goroutine continue unscheduled (E1), so that the drain can end them.
func (d *driver) schedEnd() {
	if a := gosched.Active; a != nil {
		a.Reset(nil, nil)
	}
	activeDriver.CompareAndSwap(d, nil)
}
