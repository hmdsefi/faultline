// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing/synctest"
	"time"

	"github.com/hmdsefi/faultline/kernel"
)

// maxCheckpoints caps the per-attempt checkpoint list (GOR-075, 32 MiB).
const maxCheckpoints = 4_194_304

// driver runs the kernel of one goroutine-mode attempt on the bubble's test goroutine. Its exported
// methods are the driver contract of GOR-040; the netsim and fsx shims see them through their own
// Driver interfaces.
type driver struct {
	w  *World
	ga *goAttempt

	mu      sync.Mutex  // the world lock (GOR-015)
	holds   atomic.Bool // the driver goroutine holds mu
	waiting atomic.Bool // the driver is in synctest.Wait, the timer select or the drain sleep

	wake chan struct{} // GOR-024 (ga.wake)

	executed    bool     // set by the OnEvent observer (GOR-011)
	ended       bool     // E1 ran (GOR-065)
	stackCap    int      // goroutine dump cap; 0 is GOR-067's 64 MiB, tests set less
	failNow     bool     // a process goroutine called t.FailNow or t.Fatal (GOR-047, GOR-058)
	checkpoints []uint64 // GOR-075

	detached []*Process // incarnations whose netsim host endIncarnation detached on a process goroutine

	queue []*request  // requests queued since the last drain (GOR-030)
	seq   uint64      // arrival counter
	reqN  uint64      // request IDs (GOR-031)
	nodes []*nodeProc // indexed by kernel.NodeID; index 0 unused
	snaps []clockSnap // snapshotLocal's buffer, reused by every Step (GOR-055)
}

// newDriver creates the driver after API-030 step 8 and takes the world lock (GOR-010 step 1).
func newDriver(w *World, ga *goAttempt) *driver {
	d := &driver{w: w, ga: ga, wake: ga.wake}
	ga.drv = d
	d.lockDriver()
	w.Sim.OnCrash(d.onCrash) // after simnet's and simdisk's hooks (API-030 created them first)
	ga.watch.phase.Store(watchBody)
	d.schedStart()
	return d
}

func (d *driver) lockDriver() {
	d.mu.Lock()
	d.holds.Store(true)
}

func (d *driver) unlockDriver() {
	d.holds.Store(false)
	d.mu.Unlock()
}

// bubbleNow is bubble time as a kernel.Time (GOR-020).
func bubbleNow() kernel.Time { return kernel.Time(time.Since(kernel.Epoch)) }

// advance is Sim.AdvanceTo(b) with GOR-021's internal-error message.
func (d *driver) advance(b kernel.Time) {
	s := d.w.Sim
	defer func() {
		if v := recover(); v != nil {
			if next, ok := s.NextAt(); ok && next < b {
				panic(fmt.Sprintf("faultline: internal error: bubble time %s passed pending kernel event at %s", b, next))
			}
			panic(v)
		}
	}()
	s.AdvanceTo(b)
}

// quiesce is step (1) of GOR-020: release the lock, wait until every other bubble goroutine is
// durably blocked, count progress for the watchdog, take the lock again.
func (d *driver) quiesce() {
	d.unlockDriver()
	d.waiting.Store(true)
	synctest.Wait()
	d.waiting.Store(false)
	d.ga.watch.progress.Add(1)
	d.lockDriver()
}

// sleepFor is step (3) of GOR-020: wait for a bubble timer of dur or a wake-up (GOR-024).
func (d *driver) sleepFor(dur time.Duration) {
	d.unlockDriver()
	t := time.NewTimer(dur)
	d.waiting.Store(true)
	select {
	case <-t.C:
	case <-d.wake:
	}
	d.waiting.Store(false)
	t.Stop()
	d.lockDriver()
}

func (d *driver) checkpoint() {
	if len(d.checkpoints) < maxCheckpoints {
		d.checkpoints = append(d.checkpoints, d.w.Sim.TraceHash())
	}
}

func (d *driver) emitDrive(until kernel.Time) {
	d.w.Sim.Emit(kernel.Record{
		Kind:  "driver.drive",
		Text:  "drive until " + until.String(),
		Attrs: []kernel.Attr{{Key: "until_ns", Value: strconv.FormatInt(int64(until), 10)}},
	})
}

// drive is World.drive in goroutine mode (GOR-020). The lock is held on entry and on return.
func (d *driver) drive(until kernel.Time) kernel.StopReason {
	s := d.w.Sim
	horizon := until
	if mt := s.Config().MaxTime; mt != 0 && mt < until {
		horizon = mt
	}
	d.emitDrive(until)
	for {
		d.quiesce()           // (1)
		if d.schedRelease() { // EXB-036: one release per pass, before time and events advance
			continue
		}
		b := bubbleNow()
		d.advance(b) // GOR-021
		if d.failNow {
			d.ga.bt.FailNow() // a process goroutine called t.FailNow: stop as t.FailNow in body does (GOR-047, GOR-058)
		}
		d.drainAsserts() // GOR-085 point 2
		if d.executed {  // GOR-022
			d.executed = false
			d.unlockDriver()
			d.w.runInvariants()
			d.lockDriver()
		}
		d.refreshClocks(b)    // GOR-055
		d.wakePausedWaiters() // GOR-041
		d.drain()             // (2) GOR-031
		d.advance(b)          // defers paused-node requests at b
		if s.Err() != nil {
			return kernel.StopFailed
		}
		next, ok := s.NextAt()
		if ok && next == b && b <= horizon {
			if mx := s.Config().MaxEvents; mx != 0 && s.Executed() >= mx {
				return kernel.StopMaxEvents
			}
			snap := d.snapshotLocal()
			before := s.Executed()
			s.Step() // (4)
			d.clocksAfter(snap)
			d.drainAsserts() // GOR-085 point 1
			if s.Executed() > before {
				d.checkpoint()
				d.ga.watch.stepped(s)
			}
			d.wakePausedWaiters() // GOR-041
			continue
		}
		if b >= horizon {
			if horizon == until {
				return kernel.StopDeadline
			}
			return kernel.StopMaxTime
		}
		target := horizon
		if ok && next < horizon {
			target = next
		}
		if d.ga.asserts.pending() { // GOR-085: drain at the instant of the call
			continue
		}
		d.sleepFor(time.Duration(target - b)) // (3)
	}
}

// assertQueue is the per-attempt FIFO of assert recording functions (GOR-085). It has its own
// mutex; assertRun never takes the world lock. It has no size limit and empties only at the drain
// points, so a goroutine that queues calls without ever blocking grows it until the stall watchdog
// ends the binary (GOR-080), or without end when the watchdog is off.
type assertQueue struct {
	mu sync.Mutex
	fs []func()
}

// push appends f to the queue.
func (q *assertQueue) push(f func()) {
	q.mu.Lock()
	q.fs = append(q.fs, f)
	q.mu.Unlock()
}

// take returns the queued functions and empties the queue. The caller owns the returned slice:
// pushes made while it runs the functions go to a new one.
func (q *assertQueue) take() []func() {
	q.mu.Lock()
	fs := q.fs
	q.fs = nil
	q.mu.Unlock()
	return fs
}

// pending reports whether the queue holds a function.
func (q *assertQueue) pending() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.fs) > 0
}

// assertRun queues f and wakes the driver; it never runs f, takes the world lock or blocks. It is
// the run function of assert.Attach in goroutine mode (GOR-085, EXP-009), and drainAsserts runs f
// later. Only goroutines of the attempt's bubble may call it: the wake channel belongs to the
// bubble, and a send on it from outside the bubble is a fatal runtime error that recover cannot
// catch.
func (ga *goAttempt) assertRun(f func()) {
	// Push before the send: if drain takes the token first, the wait rule in drive sees f.
	ga.asserts.push(f)
	select {
	case ga.wake <- struct{}{}:
	default:
	}
}

// drainAsserts executes queued functions in FIFO order until the queue is empty, so a call that
// f queues runs in the same drain. Driver goroutine, world lock held (GOR-085). f is EXP-001's
// recording step: it may update counters, emit records and call Sim.Fail; it must not block, call
// shims (GOR-016) or drive the World (World.RunFor).
func (d *driver) drainAsserts() {
	d.assertHeld("drainAsserts")
	for {
		fs := d.ga.asserts.take()
		if len(fs) == 0 {
			return
		}
		for _, f := range fs {
			f()
		}
	}
}
