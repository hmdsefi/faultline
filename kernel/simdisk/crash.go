package simdisk

import (
	"maps"
	"math/rand/v2"
	"slices"
	"strconv"

	"github.com/hmdsefi/faultline/kernel"
)

// half is the survival probability of one op (CrashKeepSubset) or one torn chunk, in ppm.
const half = 500_000

// onCrash is the crash hook registered by New (DSK-025).
func (d *Disks) onCrash(n *kernel.Node) {
	i := int(n.ID()) - 1
	if i >= len(d.vols) || d.vols[i] == nil {
		return // no volume: no records, no draws
	}
	d.vols[i].crash()
}

// crash applies the crash procedure of DSK-025 to v.
func (v *Volume) crash() {
	v.epoch++ // every handle of the volume becomes stale
	r := v.d.s.Rand("disk/" + v.node.Name())

	// 1. Metadata
	pending, kept := len(v.nslog), 0
	if v.d.cfg.Metadata == MetadataStrict && pending > 0 {
		kept = int(r.Uint64N(uint64(pending) + 1))
		for _, op := range v.nslog[:kept] {
			for _, m := range op.muts {
				m.applyTo(true)
			}
		}
	}
	v.nslog = nil
	meta := v.d.cfg.Metadata.String()
	v.emit("disk.crash_meta", "crash metadata "+meta+": kept "+strconv.Itoa(kept)+" of "+strconv.Itoa(pending),
		attr("model", meta), attr("pending", strconv.Itoa(pending)), attr("kept", strconv.Itoa(kept)))

	// 2. Data, per file in traversal order (DSK-026)
	reachable := traverse(v.root, true)
	for _, e := range reachable {
		f := e.n
		if f.dir || len(f.pending) == 0 {
			continue
		}
		model := v.d.cfg.Crash
		if model == CrashAny {
			model = [...]CrashModel{CrashLoseUnsynced, CrashKeepPrefix, CrashKeepSubset, CrashTorn}[r.Uint64N(4)]
		}
		var keptOps, torn int
		f.dur, keptOps, torn = applyModel(model, f.dur, f.pending, r, int64(v.d.cfg.SectorSize))
		m := len(f.pending)
		v.emit("disk.crash_apply", "crash "+e.path+" "+model.String()+": kept "+strconv.Itoa(keptOps)+" of "+strconv.Itoa(m),
			attr("path", e.path), attr("model", model.String()), attr("pending", strconv.Itoa(m)),
			attr("kept", strconv.Itoa(keptOps)), attr("torn_sectors", strconv.Itoa(torn)),
			attr("size", strconv.Itoa(len(f.dur))))
	}

	// 3. Reset the visible state to the durable state
	for _, e := range traverse(v.root, false) {
		if !e.n.dir {
			e.n.linked = false // unreachable inodes are discarded
		}
	}
	v.used = 0
	for _, e := range reachable {
		n := e.n
		if n.dir {
			n.entries = maps.Clone(n.durable)
			continue
		}
		n.cur = append([]byte(nil), n.dur...)
		n.pending = nil
		n.diverged = false
		n.linked = true
		v.used += int64(len(n.dur))
	}
}

// entry is one inode visited by traverse, with its path.
type entry struct {
	path string
	n    *inode
}

// traverse visits the durable (or visible) namespace depth-first in pre-order from root,
// children in ascending byte order of their names, each inode at most once (DSK-026).
func traverse(root *inode, durable bool) []entry {
	var out []entry
	visited := map[*inode]bool{} // indexed only
	var walk func(p string, n *inode)
	walk = func(p string, n *inode) {
		if visited[n] {
			return
		}
		visited[n] = true
		out = append(out, entry{path: p, n: n})
		if !n.dir {
			return
		}
		children := n.entries
		if durable {
			children = n.durable
		}
		for _, name := range slices.Sorted(maps.Keys(children)) {
			child := "/" + name
			if p != "/" {
				child = p + child
			}
			walk(child, children[name])
		}
	}
	walk("/", root)
	return out
}

// applyModel applies the pending ops P that survive a crash under model to dur (DSK-027) and
// returns the new durable content, the kept count and the torn sector count. model must be one of
// the four concrete models, never CrashAny: CrashTorn is the final case, the code after the
// switch, so any other value runs it. P must not be empty (Torn draws Uint64N(len(P))); crash, the
// only caller, skips files with no pending ops.
func applyModel(model CrashModel, dur []byte, P []dataOp, r *rand.Rand, sector int64) ([]byte, int, int) {
	m := len(P)
	switch model {
	case CrashLoseUnsynced:
		return dur, 0, 0
	case CrashKeepPrefix:
		k := int(r.Uint64N(uint64(m) + 1))
		for _, op := range P[:k] {
			dur = apply(dur, op)
		}
		return dur, k, 0
	case CrashKeepSubset:
		kept := 0
		for _, op := range P {
			if kernel.Chance(r, half) {
				dur = apply(dur, op)
				kept++
			}
		}
		return dur, kept, 0
	}
	// CrashTorn
	k := int(r.Uint64N(uint64(m)))
	for _, op := range P[:k] {
		dur = apply(dur, op)
	}
	torn := 0
	t := P[k]
	if t.trunc {
		if kernel.Chance(r, half) {
			dur = apply(dur, t)
			torn = 1
		}
		return dur, k, torn
	}
	end := t.off + int64(len(t.data))
	for start := t.off; start < end; {
		next := min((start/sector+1)*sector, end) // next sector boundary
		if kernel.Chance(r, half) {
			dur = apply(dur, dataOp{off: start, data: t.data[start-t.off : next-t.off]})
			torn++
		}
		start = next
	}
	return dur, k, torn
}
