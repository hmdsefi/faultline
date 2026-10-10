// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simdisk

import (
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/hmdsefi/faultline/kernel"
)

// SampleLatency draws the latency of one operation of class op from the volume's latency
// stream. Used by shims/fsx in Phase 2; Phase 1 code does not call it.
func (v *Volume) SampleLatency(op Op) time.Duration {
	lc := v.d.cfg.Latency
	var l Latency
	switch op {
	case OpRead:
		l = lc.Read
	case OpWrite:
		l = lc.Write
	case OpSync:
		l = lc.Sync
	case OpMeta:
		l = lc.Meta
	default:
		panic("simdisk: SampleLatency: unknown Op " + strconv.Itoa(int(op)))
	}
	r := v.d.s.Rand("disk/" + v.node.Name() + "/latency")
	d := l.Base
	if l.Jitter > 0 {
		d += kernel.Uniform(r, 0, l.Jitter)
	}
	if slow(r, lc.SlowPPM) && lc.Slow > 0 {
		d += kernel.Uniform(r, 0, lc.Slow)
	}
	return d
}

// slow decides the slow path without drawing at the edges 0 and >= 1_000_000 (DSK-038).
func slow(r *rand.Rand, ppm uint32) bool {
	switch {
	case ppm == 0:
		return false
	case ppm >= 1_000_000:
		return true
	}
	return kernel.Chance(r, ppm)
}
