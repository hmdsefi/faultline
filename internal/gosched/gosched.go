// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package gosched connects the goroutine-mode driver of package faultline to the scheduler of
// the toolexec backend (module runner/toolexec, package sched).
package gosched

import "math/rand/v2"

// Scheduler is implemented by package sched, which sets Active in its init function. The
// goroutine-mode driver calls Reset, Pending and Release on the driver goroutine with the world
// lock held.
type Scheduler interface {
	// Reset with non-nil streams starts an attempt: release chooses which waiting goroutine
	// continues, user draws the randomness of rewritten code. The driver calls it before any
	// process goroutine or the body of the attempt runs; the scheduler takes the caller as the
	// driver goroutine and the caller's synctest bubble as the attempt's bubble.
	// Reset(nil, nil) ends the attempt: every waiting goroutine continues, and the scheduler
	// passes calls through until the next Reset with streams.
	Reset(release, user *rand.Rand)
	// Pending reports whether a goroutine of the attempt's bubble waits at a scheduling point.
	// The driver calls it right after every other goroutine of the bubble is durably blocked.
	Pending() bool
	// Release lets exactly one waiting goroutine continue, chosen with the release stream. The
	// driver calls it only right after Pending reported true.
	Release()
	// Gaps returns the coverage gaps of the rewritten packages linked into the test binary, one
	// "<file>:<line>: <kind>" line each, sorted, without duplicates.
	Gaps() []string
}

// Active is nil unless the test binary was built with the toolexec backend. The driver reads it
// without synchronization, so it is set only in an init function.
var Active Scheduler

// Wake makes the driver of the attempt in progress start its next pass at once, so that a
// goroutine that starts waiting at a scheduling point while the driver waits for virtual time is
// released at that instant. A Scheduler calls it, only from a goroutine of the attempt's bubble,
// after every change that can make Pending true. Wake never blocks and does nothing when no
// attempt is in progress. Package faultline sets it in an init function.
var Wake func()

// Wrapper is the -toolexec value that builds this test binary again, for replay commands. The
// wrapper sets it when it links a test binary; empty means faultline-toolexec.
var Wrapper string
