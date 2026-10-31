// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"sync/atomic"

	"github.com/hmdsefi/faultline/internal/gosched"
)

// activeDriver is the driver of the goroutine-mode attempt in progress (API-104 allows one at a
// time). gosched.Wake wakes it when a goroutine of its bubble starts waiting at a scheduling point.
var activeDriver atomic.Pointer[driver]

func init() { //nolint:gochecknoinits // gosched.Wake must be set before any test runs
	gosched.Wake = func() {
		if d := activeDriver.Load(); d != nil {
			d.signal() // GOR-024; the scheduler calls Wake only from the attempt's bubble (XTE-070)
		}
	}
}

// schedStart publishes d for gosched.Wake and starts an attempt of the toolexec backend's
// scheduler: releases draw from toolexec/release, rewritten code's randomness from toolexec/user.
// It runs on the driver goroutine, which the scheduler takes as the driver (XTE-060).
func (d *driver) schedStart() {
	activeDriver.Store(d)
	if a := gosched.Active; a != nil {
		a.Reset(d.w.Sim.Rand("toolexec/release"), d.w.Sim.Rand("toolexec/user"))
	}
}

// schedRelease lets one goroutine waiting at a scheduling point continue and reports whether one
// was waiting. The driver calls it after each quiescence, before time, requests or events advance.
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
