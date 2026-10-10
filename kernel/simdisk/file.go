// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simdisk

// File is a read-write handle to a file. It belongs to one incarnation of the node.
type File struct {
	v      *Volume
	f      *inode
	name   string // cleaned path at open
	epoch  uint64 // crash epoch at open (DSK-028)
	inc    uint32 // node incarnation at open (DSK-028)
	closed bool
}

// Name returns the cleaned path the file was opened with.
func (f *File) Name() string { return f.name }

// Size returns the visible size, or -1 if the handle is closed or stale.
func (f *File) Size() int64 {
	if f.closed || f.stale() {
		return -1
	}
	return int64(len(f.f.cur))
}

// Close closes the handle. It does not sync.
func (f *File) Close() error {
	if err := f.check("close", "Close"); err != nil {
		return err
	}
	f.closed = true
	return nil
}

// stale reports whether the handle belongs to an earlier crash epoch or incarnation (DSK-028).
func (f *File) stale() bool {
	return f.epoch != f.v.epoch || f.inc != f.v.node.Incarnation()
}

// check runs the handle checks of DSK-035 for the File method named method with error op op.
func (f *File) check(op, method string) error {
	if f.closed {
		return pathErr(op, f.name, ErrClosed)
	}
	if f.stale() {
		f.v.emit("disk.stale", op+" "+f.name+": stale handle", attr("op", op), attr("path", f.name))
		if method == "Close" {
			f.closed = true
		}
		return pathErr(op, f.name, ErrStale)
	}
	if method != "Close" {
		f.v.mustBeUp(method)
	}
	return nil
}
