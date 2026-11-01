// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package sched

import (
	"os"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

//go:noinline
func atDepth(d int, f func()) {
	if d == 0 {
		f()
		return
	}
	atDepth(d-1, f)
}

// TestPointReleaseCost logs the wall time of one Point and its Release, with the driver's
// synctest.Wait, at stack depths 0 and 40 (XTE §9). It runs only with SCHED_COST=1.
func TestPointReleaseCost(t *testing.T) {
	if os.Getenv("SCHED_COST") != "1" {
		t.Skip("set SCHED_COST=1 to measure")
	}
	const n = 20000
	for _, depth := range []int{0, 40} {
		start := time.Now()
		synctest.Test(t, func(t *testing.T) {
			s.Reset(streams(1))
			var finished atomic.Bool
			go func() {
				atDepth(depth, func() {
					for range n {
						Point()
					}
				})
				finished.Store(true)
			}()
			for !finished.Load() {
				synctest.Wait()
				if s.Pending() {
					s.Release()
				}
			}
			s.Reset(nil, nil)
		})
		t.Logf("depth %d: %v per Point and Release", depth, time.Since(start)/n)
	}
}
