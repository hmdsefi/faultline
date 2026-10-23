// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package gosched connects the goroutine-mode driver to prototype B's scheduler.
package gosched

import "math/rand/v2"

// Scheduler is implemented by runner/toolexec/sched, which sets Active in its init function.
// The goroutine-mode driver calls its methods on the driver goroutine, with the world lock held.
// Only goroutines of the attempt's synctest bubble may wait at a scheduling point.
type Scheduler interface {
	// Reset starts a new attempt: release draws from release, user randomness from user.
	// Reset(nil, nil) ends the attempt: every waiting goroutine continues, and the scheduler
	// passes calls through until the next Reset.
	// The driver calls Reset with streams when it is created, before any process goroutine or
	// body of the attempt runs. It calls Reset(nil, nil) when the run ends, while goroutines that
	// the end woke may already run.
	Reset(release, user *rand.Rand)
	// Pending reports whether a goroutine waits at a scheduling point.
	// The driver calls it right after every other goroutine of the bubble is durably blocked, so
	// the waiting goroutines cannot change before the Release that follows.
	Pending() bool
	// Release lets exactly one waiting goroutine continue, chosen with the release stream.
	// The driver calls it only right after Pending reported true.
	Release()
}

// Active is nil unless the test binary was built with the rewriter. The driver reads it without
// synchronization, so set it only before the first Run, in an init function.
var Active Scheduler

// OnDriver reports whether the caller is the goroutine-mode driver goroutine of the attempt in
// progress. Package faultline sets it in an init function; nil means never.
var OnDriver func() bool

// Wake makes the driver of the attempt in progress start its next pass at once, so that a
// goroutine that starts waiting at a scheduling point while the driver waits for virtual time is
// released at that instant. A Scheduler calls it after every change that can make Pending true:
// after it registers a waiting goroutine, and after it moves goroutines into the waiting set (the
// waiters of a mutex on Unlock). Wake never blocks. It does nothing when no attempt is in
// progress or when the caller is not a goroutine of the attempt's bubble. Package faultline sets
// it in an init function; nil means there is no driver to wake.
var Wake func()
