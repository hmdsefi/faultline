package simdisk

import (
	"slices"
	"strconv"
)

// Sync makes the pending data ops of the file durable (or fails, see DSK-021).
func (f *File) Sync() error {
	if err := f.check("sync", "Sync"); err != nil {
		return err
	}
	v, n := f.v, f.f
	m := len(n.pending)
	// Each value is converted once: DSK §9 budgets these allocations.
	ops := strconv.Itoa(m)
	if v.failSyncs > 0 {
		v.failSyncs--
		model := v.d.cfg.FailedSync
		if model == FailedSyncDropDirty {
			if m > 0 {
				n.diverged = true
			}
			n.pending = nil // cur unchanged: dropped ops stay readable until a crash
		}
		v.emit("disk.sync_fail", "sync "+f.name+" failed: "+model.String()+" ops="+ops,
			attr("path", f.name), attr("model", model.String()), attr("ops", ops),
			attr("left", strconv.Itoa(v.failSyncs)))
		return pathErr("sync", f.name, ErrIO)
	}
	for _, op := range n.pending {
		n.dur = apply(n.dur, op)
	}
	n.pending = nil
	size := strconv.Itoa(len(n.dur))
	v.emit("disk.sync", "sync "+f.name+" ops="+ops+" size="+size,
		attr("path", f.name), attr("ops", ops), attr("size", size))
	return nil
}

// FailSyncs makes the next n sync operations (File.Sync or Volume.SyncDir) fail with ErrIO,
// replacing any remaining count. n == 0 clears it.
func (v *Volume) FailSyncs(n int) {
	if n < 0 {
		panic(v.nodeMsg("FailSyncs", "negative count "+strconv.Itoa(n)))
	}
	v.failSyncs = n
	v.emit("disk.fail_syncs", "fail next "+strconv.Itoa(n)+" syncs", attr("n", strconv.Itoa(n)))
}

// SyncDir makes the directory's pending namespace operations durable (see DSK-024).
func (v *Volume) SyncDir(path string) error {
	v.mustBeUp("SyncDir")
	p, err := clean("syncdir", path)
	if err != nil {
		return err
	}
	d, err := v.lookup(p, false)
	if err != nil {
		return pathErr("syncdir", p, err)
	}
	if !d.dir {
		return pathErr("syncdir", p, ErrNotDir)
	}
	if v.failSyncs > 0 {
		v.failSyncs--
		v.emit("disk.sync_dir_fail", "syncdir "+p+" failed", attr("path", p), attr("left", strconv.Itoa(v.failSyncs)))
		return pathErr("syncdir", p, ErrIO)
	}
	ops := v.syncDir(d)
	v.emit("disk.sync_dir", "syncdir "+p+" ops="+strconv.Itoa(ops), attr("path", p), attr("ops", strconv.Itoa(ops)))
	return nil
}

// syncDir applies the durability closure of DSK-024 for directory d and returns the number of
// namespace ops made durable. It marks the selected ops in a slice of one flag per logged op and
// returns early when none is selected. Otherwise it applies them to the durable namespace in log
// order and compacts the log in place: the ops it keeps stay in order and are not copied to a new
// slice, and the slots past them are cleared so that the dropped ops release their inodes.
func (v *Volume) syncDir(d *inode) int {
	selected := make([]bool, len(v.nslog))
	dirs := []*inode{d} // a set; small, searched linearly
	count := 0
	for i := len(v.nslog) - 1; i >= 0; i-- {
		touches := false
		for _, m := range v.nslog[i].muts {
			if slices.Contains(dirs, m.dir) {
				touches = true
				break
			}
		}
		if !touches {
			continue
		}
		selected[i] = true
		count++
		for _, m := range v.nslog[i].muts {
			if !slices.Contains(dirs, m.dir) {
				dirs = append(dirs, m.dir)
			}
		}
	}
	if count == 0 {
		return 0
	}
	kept := v.nslog[:0]
	for i, op := range v.nslog {
		if !selected[i] {
			kept = append(kept, op)
			continue
		}
		for _, m := range op.muts {
			m.applyTo(true)
		}
	}
	clear(v.nslog[len(kept):]) // the dropped ops release their inodes
	v.nslog = kept
	return count
}

// ReadDurable returns a copy of a file's durable content, looked up in the durable namespace.
// Allowed in every node state.
func (v *Volume) ReadDurable(path string) ([]byte, error) {
	p, err := clean("readdurable", path)
	if err != nil {
		return nil, err
	}
	n, err := v.lookup(p, true)
	if err != nil {
		return nil, pathErr("readdurable", p, err)
	}
	if n.dir {
		return nil, pathErr("readdurable", p, ErrIsDir)
	}
	return append([]byte{}, n.dur...), nil
}
