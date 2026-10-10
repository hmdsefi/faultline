// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simdisk

// DurableSize returns the durable size of a file: the number of bytes, from offset 0, that
// Corrupt can damage (see DSK-043). Like Corrupt, it resolves the path in the visible namespace.
// Allowed in every node state.
func (v *Volume) DurableSize(path string) (int64, error) {
	p, err := clean("durablesize", path)
	if err != nil {
		return 0, err
	}
	f, err := v.lookup(p, false)
	if err != nil {
		return 0, pathErr("durablesize", p, err)
	}
	if f.dir {
		return 0, pathErr("durablesize", p, ErrIsDir)
	}
	return int64(len(f.dur)), nil
}
