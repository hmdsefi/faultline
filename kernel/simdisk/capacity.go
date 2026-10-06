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

// Usage returns the bytes counted against the capacity (see DSK-030).
func (v *Volume) Usage() int64 { return v.used }
