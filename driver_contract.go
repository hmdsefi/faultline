// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

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

// OnDriver reports whether the caller is the driver goroutine (GOR-040).
func (d *driver) OnDriver() bool {
	if d.waiting.Load() {
		return false
	}
	return curGoroutineID() == d.ga.gid
}

// SyncClock makes kernel time equal bubble time (GOR-040). Lock held; never inside a kernel event.
func (d *driver) SyncClock() {
	d.assertHeld("SyncClock")
	if b := bubbleNow(); d.w.Sim.Now() < b {
		d.advance(b)
	}
}
