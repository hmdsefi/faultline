package simdisk

import (
	"strconv"

	"github.com/hmdsefi/faultline/kernel"
)

// Corrupt flips one bit in each durable byte of [off, off+n) (see DSK-033).
func (v *Volume) Corrupt(path string, off int64, n int) error {
	p, err := clean("corrupt", path)
	if err != nil {
		return err
	}
	f, err := v.lookup(p, false)
	if err != nil {
		return pathErr("corrupt", p, err)
	}
	if f.dir {
		return pathErr("corrupt", p, ErrIsDir)
	}
	if off < 0 || n < 0 {
		return pathErr("corrupt", p, ErrInvalid)
	}
	r := v.d.s.Rand("disk/" + v.node.Name())
	visible := len(f.pending) == 0 && !f.diverged
	end := int64(len(f.dur))
	if off < end && int64(n) < end-off {
		end = off + int64(n)
	}
	flipped := 0
	for i := off; i < end; i++ {
		mask := byte(1) << r.Uint64N(8)
		f.dur[i] ^= mask
		if visible {
			f.cur[i] ^= mask
		}
		flipped++
	}
	v.emit("disk.corrupt", "corrupt "+p+" off="+i64(off)+" len="+strconv.Itoa(flipped),
		attr("path", p), attr("off", i64(off)), attr("len", strconv.Itoa(flipped)),
		attr("visible", strconv.FormatBool(visible)))
	return nil
}

// WriteFileDurable creates or replaces a file with data, durably, creating missing parent
// directories durably. It is a setup helper: allowed only while the node is not up and not
// paused (before its first boot or while down).
func (v *Volume) WriteFileDurable(path string, data []byte) error {
	if st := v.node.State(); st == kernel.NodeUp || st == kernel.NodePaused {
		panic(v.nodeMsg("WriteFileDurable", "node is "+stateWord(st)))
	}
	p, err := clean("writedurable", path)
	if err != nil {
		return err
	}
	if p == "/" {
		return pathErr("writedurable", p, ErrIsDir)
	}
	if int64(len(data)) > MaxFileSize {
		return pathErr("writedurable", p, ErrTooLarge)
	}
	comps := split(p)
	dir := v.root
	for _, c := range comps[:len(comps)-1] {
		next := dir.entries[c]
		switch {
		case next == nil:
			next = newDir()
			dir.entries[c] = next
			dir.durable[c] = next
		case !next.dir:
			return pathErr("writedurable", p, ErrNotDir)
		}
		dir = next
	}
	name := comps[len(comps)-1]
	f := dir.entries[name]
	switch {
	case f == nil:
		f = &inode{}
	case f.dir:
		return pathErr("writedurable", p, ErrIsDir)
	default:
		v.used -= int64(len(f.cur))
	}
	dir.entries[name] = f
	dir.durable[name] = f
	f.cur = append([]byte(nil), data...)
	f.dur = append([]byte(nil), data...)
	f.pending = nil
	f.diverged = false
	f.linked = true
	v.used += int64(len(data))
	v.emit("disk.write_durable", "write_durable "+p+" len="+strconv.Itoa(len(data)),
		attr("path", p), attr("len", strconv.Itoa(len(data))))
	return nil
}
