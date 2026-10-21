// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	"github.com/hmdsefi/faultline/kernel"
)

// gor016Msg is GOR-016's misuse message.
const gor016Msg = "faultline: netsim/fsx call on the driver goroutine while it holds the world lock (body, a kernel event, a boot function, a hook or a Process.Do function) would deadlock; prepare files with w.Disk.Volume(n).WriteFileDurable and use shims only from processes and checks"

// Lock takes the world lock for shim code (GOR-016).
func (d *driver) Lock() {
	if d.mu.TryLock() {
		return
	}
	if d.holds.Load() && d.OnDriver() {
		panic(misuse{msg: gor016Msg})
	}
	d.mu.Lock()
}

// Unlock releases the world lock taken by Lock.
func (d *driver) Unlock() { d.mu.Unlock() }

// assertHeld panics when nobody holds the world lock, that is when method, which needs it
// (GOR-040), was called without taking it. While the lock is held it only reads the mutex state.
func (d *driver) assertHeld(method string) {
	if d.mu.TryLock() {
		d.mu.Unlock()
		panic("faultline: internal error: " + method + " called without the world lock")
	}
}

// acquire takes the world lock for driver-goroutine code that runs without it (invariants and
// final checks, GOR-015) and returns the function that releases it. It does nothing when the
// driver already holds the lock (body, kernel events, boot functions).
func (d *driver) acquire() (release func()) {
	if d.holds.Load() {
		return func() {}
	}
	d.lockDriver()
	return d.unlockDriver
}

// OnDriver reports whether the caller is the driver goroutine (GOR-040).
func (d *driver) OnDriver() bool {
	if d.waiting.Load() {
		return false
	}
	return curGoroutineID() == d.ga.gid
}

// SyncClock makes kernel time equal bubble time (GOR-040). Lock held; never inside a kernel event.
// After the run ended it does nothing: kernel time stays at the end of the run while bubble time
// moves on through the drain, past events that will never run (GOR-066).
func (d *driver) SyncClock() {
	d.assertHeld("SyncClock")
	if d.ended {
		return
	}
	if b := bubbleNow(); d.w.Sim.Now() < b {
		d.advance(b)
	}
}

// request is one unit of world-changing work queued by a process goroutine (GOR §3).
type request struct {
	node        kernel.NodeID
	inc         uint32
	kind, key   string
	seq, req    uint64
	run, cancel func()
	cancelled   bool
	started     bool
}

// Alive reports whether the run has not ended and inc is node's current incarnation with status
// running (GOR-040). Lock held.
func (d *driver) Alive(node kernel.NodeID, inc uint32) bool {
	if d.ended {
		return false
	}
	p := d.proc(node, inc)
	return p != nil && p.status == incRunning && p.Node.Incarnation() == inc
}

// Post queues a request (GOR-030). Lock held.
func (d *driver) Post(node kernel.NodeID, inc uint32, kind, key string, run, cancel func()) {
	if !d.Alive(node, inc) {
		cancel()
		return
	}
	d.seq++
	d.queue = append(d.queue, &request{node: node, inc: inc, kind: kind, key: key, seq: d.seq, run: run, cancel: cancel})
	d.signal()
}

// signal makes the driver's next wait return at once (GOR-024). Never blocks.
func (d *driver) signal() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// drain numbers the queued requests in (node, key, arrival) order and schedules them on their
// nodes (GOR-031). Lock held.
func (d *driver) drain() {
	q := d.queue
	d.queue = nil
	select {
	case <-d.wake:
	default:
	}
	slices.SortFunc(q, func(a, b *request) int {
		if c := cmp.Compare(a.node, b.node); c != 0 {
			return c
		}
		if c := strings.Compare(a.key, b.key); c != 0 {
			return c
		}
		return cmp.Compare(a.seq, b.seq)
	})
	for _, r := range q {
		d.reqN++
		r.req = d.reqN
		if !d.Alive(r.node, r.inc) {
			reason := "crashed"
			if p := d.proc(r.node, r.inc); p != nil && p.status == incExited && p.Node.Incarnation() == r.inc {
				reason = "exited"
			}
			d.emitCancel(r, reason)
			r.cancelled = true
			r.cancel()
			continue
		}
		np := d.node(r.node)
		np.pending = append(np.pending, r)
		d.postNext(r.node, np)
	}
}

// postNext records and schedules the kernel event of node id's first scheduled request, unless
// the node has a live request event already. With one live request event per node, a node's
// requests run in req order; the seeded tie-break orders them only against other events. The
// driver.request record comes right before the Post, so it is the cause of the event (GOR-031).
func (d *driver) postNext(id kernel.NodeID, np *nodeProc) {
	if np.posted != nil || len(np.pending) == 0 {
		return
	}
	r := np.pending[0]
	np.posted = r
	d.emitRequest(r)
	if d.w.Sim.Node(id).Post("req "+r.kind, func() { d.runRequest(r) }) == 0 {
		panic("faultline: internal error: request event for a down node") // a live incarnation's node is up
	}
}

// runRequest is the wrapper of a scheduled request (GOR-032). After run it schedules the event of
// the node's next request.
func (d *driver) runRequest(r *request) {
	np := d.node(r.node)
	if np.posted == r {
		np.posted = nil
	}
	if r.cancelled {
		return
	}
	r.started = true
	if i := slices.Index(np.pending, r); i >= 0 {
		np.pending = slices.Delete(np.pending, i, i+1)
	}
	label := "req " + r.kind
	d.ga.watch.label.Store(&label)
	r.run()
	d.postNext(r.node, np)
}

// cancelScheduled cancels the scheduled requests of node id's current incarnation (GOR-033).
func (d *driver) cancelScheduled(id kernel.NodeID, reason string) {
	np := d.node(id)
	pending := np.pending
	np.pending, np.posted = nil, nil
	for _, r := range pending {
		d.emitCancel(r, reason)
		r.cancelled = true
		r.cancel()
	}
}

// cancelAllRequests cancels every queued and scheduled request, in (req, seq) order, without
// records (GOR-033, E1).
func (d *driver) cancelAllRequests() {
	all := d.queue
	d.queue = nil
	for _, np := range d.nodes {
		if np != nil {
			all = append(all, np.pending...)
			np.pending, np.posted = nil, nil
		}
	}
	slices.SortFunc(all, func(a, b *request) int {
		if c := cmp.Compare(a.req, b.req); c != 0 {
			return c
		}
		return cmp.Compare(a.seq, b.seq)
	})
	for _, r := range all {
		r.cancelled = true
		r.cancel()
	}
}

func (d *driver) emitRequest(r *request) {
	d.w.Sim.Emit(kernel.Record{Node: r.node, Inc: r.inc, Kind: "driver.request", Text: r.kind + " " + r.key,
		Attrs: []kernel.Attr{
			{Key: "req", Value: strconv.FormatUint(r.req, 10)},
			{Key: "kind", Value: r.kind},
			{Key: "key", Value: r.key},
		}})
}

func (d *driver) emitCancel(r *request, reason string) {
	d.w.Sim.Emit(kernel.Record{Node: r.node, Inc: r.inc, Kind: "driver.cancel",
		Text: "cancel " + r.kind + " " + r.key + ": " + reason,
		Attrs: []kernel.Attr{
			{Key: "req", Value: strconv.FormatUint(r.req, 10)},
			{Key: "kind", Value: r.kind},
			{Key: "key", Value: r.key},
			{Key: "reason", Value: reason},
		}})
}

// endRun is E1 of GOR-065: no live incarnations, cancelled contexts, shut-down netsim hosts,
// cancelled requests, woken WaitUnpaused waiters and an empty assert queue. Lock held.
func (d *driver) endRun() {
	d.ended = true
	for _, np := range d.nodes {
		if np == nil || len(np.procs) == 0 {
			continue
		}
		if p := np.procs[len(np.procs)-1]; p.status == incRunning {
			p.cancel(ErrRunEnded)
		}
	}
	d.w.stack.Shutdown()
	d.cancelAllRequests()
	d.wakeAllWaiters()
	d.ga.asserts.take() // discard the queue (GOR-085)
}

// WaitUnpaused blocks a process goroutine while node is paused (GOR-040). It reports whether
// incarnation inc is still alive. The lock is held on entry and on return, except on the driver
// goroutine: there it releases the lock and panics, so callers must not defer Unlock.
func (d *driver) WaitUnpaused(node kernel.NodeID, inc uint32) bool {
	for {
		if !d.Alive(node, inc) {
			return false
		}
		n := d.w.Sim.Node(node)
		if n.State() != kernel.NodePaused {
			return true
		}
		if d.OnDriver() {
			d.Unlock()
			panicMisuse("faultline: fsx call on paused node %s from the driver goroutine; read its files with w.Disk.Volume(n) instead", n.Name())
		}
		np := d.node(node)
		if np.waitCh == nil {
			np.waitCh = make(chan struct{})
		}
		ch := np.waitCh
		d.Unlock()
		<-ch
		d.Lock()
	}
}

// wakePausedWaiters closes the wait channel of every node that is no longer paused or whose
// incarnation is no longer alive, in ascending node ID (GOR-041).
func (d *driver) wakePausedWaiters() {
	for id, np := range d.nodes {
		if np == nil || np.waitCh == nil {
			continue
		}
		n := d.w.Sim.Node(kernel.NodeID(id))
		if n.State() != kernel.NodePaused || !d.Alive(n.ID(), n.Incarnation()) {
			close(np.waitCh)
			np.waitCh = nil
		}
	}
}

// wakeWaiters wakes the WaitUnpaused waiters of node id (GOR-051, GOR-048).
func (d *driver) wakeWaiters(id kernel.NodeID) {
	np := d.node(id)
	if np.waitCh != nil {
		close(np.waitCh)
		np.waitCh = nil
	}
}

// wakeAllWaiters wakes every WaitUnpaused waiter (E1).
func (d *driver) wakeAllWaiters() {
	for _, np := range d.nodes {
		if np != nil && np.waitCh != nil {
			close(np.waitCh)
			np.waitCh = nil
		}
	}
}
