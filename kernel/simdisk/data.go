// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simdisk

import (
	"io"
	"strconv"
)

// ReadAt reads len(p) bytes at off from the visible content, with os.File.ReadAt semantics.
func (f *File) ReadAt(p []byte, off int64) (int, error) {
	if err := f.check("readat", "ReadAt"); err != nil {
		return 0, err
	}
	if off < 0 {
		return 0, pathErr("readat", f.name, ErrInvalid)
	}
	if len(p) == 0 {
		return 0, nil
	}
	cur := f.f.cur
	if off >= int64(len(cur)) {
		return 0, io.EOF
	}
	n := copy(p, cur[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// WriteAt writes p at off. Writing beyond the end zero-fills the gap.
func (f *File) WriteAt(p []byte, off int64) (int, error) {
	if err := f.check("writeat", "WriteAt"); err != nil {
		return 0, err
	}
	return f.write("writeat", p, off)
}

// Append writes p at the current visible size.
func (f *File) Append(p []byte) (int, error) {
	if err := f.check("append", "Append"); err != nil {
		return 0, err
	}
	return f.write("append", p, int64(len(f.f.cur)))
}

// write implements DSK-018 for WriteAt and Append (op is the error and record op).
func (f *File) write(op string, p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, pathErr(op, f.name, ErrInvalid)
	}
	if len(p) == 0 {
		return 0, nil
	}
	if off > MaxFileSize-int64(len(p)) { // off + len(p) > MaxFileSize, without overflow
		return 0, pathErr(op, f.name, ErrTooLarge)
	}
	v, n := f.v, f.f
	size := int64(len(n.cur))
	grow := max(size, off+int64(len(p))) - size
	w := int64(len(p))
	var avail int64
	if n.linked && v.capacity > 0 && v.used+grow > v.capacity {
		avail = max(0, v.capacity-v.used)
		w = min(max(size+avail-off, 0), int64(len(p))) // longest prefix of p that fits
	}
	if w > 0 {
		d := dataOp{off: off, data: append([]byte(nil), p[:w]...)}
		n.cur = apply(n.cur, d)
		n.pending = append(n.pending, d)
		if n.linked {
			v.used += max(size, off+w) - size
		}
		// Each value is converted once: DSK §9 budgets these allocations.
		offText, lenText := i64(off), i64(w)
		v.emit("disk.write", op+" "+f.name+" off="+offText+" len="+lenText,
			attr("path", f.name), attr("op", op), attr("off", offText), attr("len", lenText))
	}
	if w < int64(len(p)) {
		v.emit("disk.nospace", op+" "+f.name+": no space (need "+i64(grow)+", avail "+i64(avail)+")",
			attr("path", f.name), attr("op", op), attr("need", i64(grow)), attr("avail", i64(avail)))
		return int(w), pathErr(op, f.name, ErrNoSpace)
	}
	return int(w), nil
}

// Truncate changes the visible size. Growing zero-fills.
func (f *File) Truncate(size int64) error {
	if err := f.check("truncate", "Truncate"); err != nil {
		return err
	}
	if size < 0 {
		return pathErr("truncate", f.name, ErrInvalid)
	}
	if size > MaxFileSize {
		return pathErr("truncate", f.name, ErrTooLarge)
	}
	v, n := f.v, f.f
	old := int64(len(n.cur))
	if grow := size - old; grow > 0 && n.linked && v.capacity > 0 && v.used+grow > v.capacity {
		avail := max(0, v.capacity-v.used)
		v.emit("disk.nospace", "truncate "+f.name+": no space (need "+i64(grow)+", avail "+i64(avail)+")",
			attr("path", f.name), attr("op", "truncate"), attr("need", i64(grow)), attr("avail", i64(avail)))
		return pathErr("truncate", f.name, ErrNoSpace)
	}
	d := dataOp{trunc: true, off: size}
	n.cur = apply(n.cur, d)
	n.pending = append(n.pending, d)
	if n.linked {
		v.used += size - old
	}
	v.emit("disk.truncate", "truncate "+f.name+" "+i64(old)+" -> "+i64(size),
		attr("path", f.name), attr("old_size", i64(old)), attr("size", i64(size)))
	return nil
}

// ReadFile returns a copy of a file's visible content. Allowed in every node state.
func (v *Volume) ReadFile(path string) ([]byte, error) {
	p, err := clean("readfile", path)
	if err != nil {
		return nil, err
	}
	n, err := v.lookup(p, false)
	if err != nil {
		return nil, pathErr("readfile", p, err)
	}
	if n.dir {
		return nil, pathErr("readfile", p, ErrIsDir)
	}
	return append([]byte{}, n.cur...), nil
}

func i64(x int64) string { return strconv.FormatInt(x, 10) }
