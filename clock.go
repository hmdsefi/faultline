// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"math"
	"math/bits"
	"time"

	"github.com/hmdsefi/faultline/kernel"
)

// LocalClock is a process's view of its node's clock: virtual time with the node's clock offset,
// drift and jumps applied. Call its methods from process goroutines and checks. The function
// passed to Run and kernel callbacks read the node's clock with Node.Now, Node.LocalTime and
// Node.Drift instead; LocalClock methods panic there.
type LocalClock interface {
	// Now returns the node's local time. It differs from time.Now in the process by the node's
	// clock offset, drift and jumps.
	Now() time.Time
	// Monotonic returns the local time that has passed since this incarnation booted. It
	// applies the node's drift and ignores clock jumps, so it never decreases. Once the
	// incarnation has crashed or exited, it keeps counting at the drift the node had then.
	Monotonic() time.Duration
	// ToGlobal returns how much virtual time the node's clock takes to advance by d at its
	// current drift, rounded up to a whole nanosecond, or 0 if d <= 0. After
	// time.Sleep(c.ToGlobal(d)), Monotonic has advanced by at least d, unless the node's drift
	// changed or its clock jumped during the sleep.
	ToGlobal(d time.Duration) time.Duration
}

// Clock returns the node's local clock. Its methods are safe for concurrent use.
func (p *Process) Clock() LocalClock { return processClock{p} }

// processClock implements LocalClock (GOR-055): each method takes the world lock and brings kernel
// time up to bubble time first.
type processClock struct{ p *Process }

func (c processClock) Now() time.Time {
	d := c.p.d
	d.lockFor("Clock().Now")
	defer d.Unlock()
	d.SyncClock()
	return c.p.Node.Now()
}

func (c processClock) ToGlobal(dur time.Duration) time.Duration {
	d := c.p.d
	d.lockFor("Clock().ToGlobal")
	defer d.Unlock()
	d.SyncClock()
	return localToGlobal(dur, c.p.Node.Drift())
}

func (c processClock) Monotonic() time.Duration {
	p, d := c.p, c.p.d
	d.lockFor("Clock().Monotonic")
	defer d.Unlock()
	d.SyncClock()
	return addLocal(p.m0, localElapsed(time.Duration(d.w.Sim.Now()-p.g0), p.ppm))
}

// localElapsed is Δ + floor(Δ·ppm/10⁶) for Δ >= 0, computed as KRN-070 does: exact, and saturating
// at math.MaxInt64.
func localElapsed(delta time.Duration, ppm int32) time.Duration {
	dd := int64(delta)
	q, r := dd/1_000_000, dd%1_000_000
	return addLocal(delta, time.Duration(q*int64(ppm)+floorDiv(r*int64(ppm), 1_000_000)))
}

// addLocal returns a+b, saturating as kernel.Time.Add does (KRN-001).
func addLocal(a, b time.Duration) time.Duration { return time.Duration(kernel.Time(a).Add(b)) }

// floorDiv divides rounding toward negative infinity (b > 0).
func floorDiv(a, b int64) int64 { //nolint:unparam // KRN-070's floorDiv(a, b), the same signature as the kernel's
	q := a / b
	if a%b != 0 && a < 0 {
		q--
	}
	return q
}

// localToGlobal is KRN-072's conversion, copied because the kernel's is unexported.
func localToGlobal(d time.Duration, ppm int32) time.Duration {
	if d <= 0 {
		return 0
	}
	den := uint64(1_000_000 + int64(ppm)) //nolint:gosec // in [500_000, 2_000_000]: ppm is in [kernel.MinDriftPPM, kernel.MaxDriftPPM]
	hi, lo := bits.Mul64(uint64(d), 1_000_000)
	if hi >= den {
		return math.MaxInt64
	}
	q, rem := bits.Div64(hi, lo, den)
	if rem != 0 {
		q++
	}
	if q > math.MaxInt64 {
		return math.MaxInt64
	}
	return time.Duration(q)
}

// clockSnap is one live process's local reading before a Step (GOR-055).
type clockSnap struct {
	p     *Process
	local kernel.Time
	ppm   int32
}

// snapshotLocal records LocalTime and drift of every alive process node, ascending node ID. The
// slice is reused by the next call.
func (d *driver) snapshotLocal() []clockSnap {
	out := d.snaps[:0]
	for _, np := range d.nodes {
		if np == nil || len(np.procs) == 0 {
			continue
		}
		p := np.procs[len(np.procs)-1]
		if p.status != incRunning {
			continue
		}
		out = append(out, clockSnap{p: p, local: p.Node.LocalTime(), ppm: p.Node.Drift()})
	}
	d.snaps = out
	return out
}

// clocksAfter rebases the Monotonic state of each snapshotted process whose drift or local
// reading the Step changed: the old drift counts up to this instant, and floor rounding restarts
// here, which can lower Monotonic by up to 1 ns. Monotonic never reads the local reading, so a
// clock jump is never added to it (GOR-055).
func (d *driver) clocksAfter(snap []clockSnap) {
	t := d.w.Sim.Now()
	for _, c := range snap {
		p := c.p
		if p.Node.Drift() != c.ppm || p.Node.LocalTime() != c.local {
			p.m0 = addLocal(p.m0, localElapsed(time.Duration(t-p.g0), p.ppm))
			p.g0 = t
			p.ppm = p.Node.Drift()
		}
	}
}

// refreshClocks applies drift changes made by body outside events (GOR-055).
func (d *driver) refreshClocks(b kernel.Time) {
	for _, np := range d.nodes {
		if np == nil || len(np.procs) == 0 {
			continue
		}
		p := np.procs[len(np.procs)-1]
		if p.status == incRunning && p.Node.Drift() != p.ppm {
			p.m0 = addLocal(p.m0, localElapsed(time.Duration(b-p.g0), p.ppm))
			p.g0 = b
			p.ppm = p.Node.Drift()
		}
	}
}
