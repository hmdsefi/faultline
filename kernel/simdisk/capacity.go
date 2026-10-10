// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simdisk

// SetCapacity sets the capacity in bytes; 0 means unlimited.
func (v *Volume) SetCapacity(bytes int64) {
	if bytes < 0 {
		panic(v.nodeMsg("SetCapacity", "negative capacity "+i64(bytes)))
	}
	v.capacity = bytes
	v.emit("disk.capacity", "capacity "+i64(bytes)+" used="+i64(v.used),
		attr("bytes", i64(bytes)), attr("used", i64(v.used)))
}

// Capacity returns the current capacity in bytes; 0 means unlimited.
func (v *Volume) Capacity() int64 { return v.capacity }

// Usage returns the bytes counted against the capacity: the visible size of every file that has
// a name. A write counts at once, and a truncate, remove or rename frees bytes at once, synced or
// not. After a crash, usage is computed again from the durable state.
func (v *Volume) Usage() int64 { return v.used }
