// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simdisk

import (
	"fmt"
	"io/fs"

	"github.com/hmdsefi/faultline/kernel"
)

// Disks holds the volumes of one simulation.
type Disks struct {
	s    *kernel.Sim
	cfg  Config
	vols []*Volume // index: NodeID-1; nil until Volume(n) is first called for the node
}

// New creates the disks of s and registers the crash hook with s.OnCrash. It panics if s is nil
// or cfg is invalid. Create at most one Disks per Sim.
func New(s *kernel.Sim, cfg Config) *Disks {
	if s == nil {
		panic("simdisk: New: nil *kernel.Sim")
	}
	if err := cfg.Validate(); err != nil {
		panic("simdisk: New: " + err.Error())
	}
	if cfg.SectorSize == 0 {
		cfg.SectorSize = DefaultSectorSize
	}
	d := &Disks{s: s, cfg: cfg}
	s.OnCrash(d.onCrash)
	return d
}

// Config returns the configuration passed to New, with SectorSize defaulted.
func (d *Disks) Config() Config { return d.cfg }

// Volume returns the volume of n, creating an empty one (only the root directory) on the first
// call. It returns the same *Volume for every incarnation of n. It panics if n is nil or belongs
// to another Sim.
func (d *Disks) Volume(n *kernel.Node) *Volume {
	if n == nil {
		panic("simdisk: Volume: nil *kernel.Node")
	}
	if n.Sim() != d.s {
		panic(fmt.Sprintf("simdisk: Volume: node %s belongs to a different Sim", n.Name()))
	}
	i := int(n.ID()) - 1
	for len(d.vols) <= i {
		d.vols = append(d.vols, nil)
	}
	if d.vols[i] == nil {
		d.vols[i] = &Volume{d: d, node: n, root: newDir(), capacity: d.cfg.Capacity}
	}
	return d.vols[i]
}

// Lookup returns the volume of n and true if Volume(n) was called before, or nil and false. It
// never creates a volume, so code that only reads, such as a fault planner, cannot change the
// simulation by calling it. It panics if n is nil or belongs to another Sim.
func (d *Disks) Lookup(n *kernel.Node) (*Volume, bool) {
	if n == nil {
		panic("simdisk: Lookup: nil *kernel.Node")
	}
	if n.Sim() != d.s {
		panic(fmt.Sprintf("simdisk: Lookup: node %s belongs to a different Sim", n.Name()))
	}
	if i := int(n.ID()) - 1; i < len(d.vols) && d.vols[i] != nil {
		return d.vols[i], true
	}
	return nil, false
}

// Info describes a file or directory.
type Info struct {
	Name  string // base name; "/" for the root
	Size  int64  // visible size of a file; 0 for a directory
	IsDir bool
}

// Volume is the simulated disk of one node.
type Volume struct {
	d         *Disks
	node      *kernel.Node
	root      *inode
	nslog     []nsOp // MetadataStrict: pending namespace ops, oldest first (DSK-004)
	epoch     uint64 // crash epoch (DSK-028)
	failSyncs int
	capacity  int64
	used      int64
}

// Node returns the node this volume belongs to.
func (v *Volume) Node() *kernel.Node { return v.node }

// inode is a file or a directory (DSK-004).
type inode struct {
	dir bool
	// directories
	entries map[string]*inode // visible namespace
	durable map[string]*inode // durable namespace
	// files
	cur      []byte   // visible content
	dur      []byte   // durable content
	pending  []dataOp // pending data ops, oldest first
	diverged bool     // dropped data ops exist since the last crash
	linked   bool     // the file has an entry in the visible namespace
}

func newDir() *inode {
	return &inode{dir: true, entries: map[string]*inode{}, durable: map[string]*inode{}}
}

// dataOp is a pending change to one file's content.
type dataOp struct {
	trunc bool   // true: truncate to off; false: write data at off
	off   int64  // truncate length, or write offset
	data  []byte // owned copy; len(data) >= 1 for writes
}

// apply applies op to b and returns the result (DSK-005). Bytes added by growth are zero.
func apply(b []byte, op dataOp) []byte {
	if op.trunc {
		if op.off <= int64(len(b)) {
			return b[:op.off]
		}
		return append(b, make([]byte, op.off-int64(len(b)))...)
	}
	end := op.off + int64(len(op.data))
	if end > int64(len(b)) {
		b = append(b, make([]byte, end-int64(len(b)))...)
	}
	copy(b[op.off:end], op.data)
	return b
}

// mutation sets (target != nil) or deletes (target == nil) one directory entry.
type mutation struct {
	dir    *inode
	name   string
	target *inode
}

// nsOp is a namespace operation: "create", "mkdir", "remove" or "rename".
type nsOp struct {
	kind string
	muts []mutation // a rename lists the deletion of the old entry first
}

// applyTo applies m to the durable (true) or visible (false) namespace (DSK-006).
func (m mutation) applyTo(durable bool) {
	t := m.dir.entries
	if durable {
		t = m.dir.durable
	}
	if m.target == nil {
		delete(t, m.name)
	} else {
		t[m.name] = m.target
	}
}

// record records a namespace op: visible at once; logged under MetadataStrict, durable at once
// under MetadataImmediate (DSK §5.4).
func (v *Volume) record(kind string, muts ...mutation) {
	for _, m := range muts {
		m.applyTo(false)
	}
	if v.d.cfg.Metadata == MetadataStrict {
		v.nslog = append(v.nslog, nsOp{kind: kind, muts: muts})
		return
	}
	for _, m := range muts {
		m.applyTo(true)
	}
}

// emit emits a record for the volume's node; Emit fills Inc with its current incarnation.
func (v *Volume) emit(kind, text string, attrs ...kernel.Attr) {
	v.d.s.Emit(kernel.Record{Node: v.node.ID(), Kind: kind, Text: text, Attrs: attrs})
}

func attr(key, value string) kernel.Attr { return kernel.Attr{Key: key, Value: value} }

// mustBeUp panics unless the volume's node is up (DSK-035, DSK-036).
func (v *Volume) mustBeUp(method string) {
	if st := v.node.State(); st != kernel.NodeUp {
		panic(v.nodeMsg(method, "node is "+stateWord(st)))
	}
}

// nodeMsg formats "simdisk: <Method> on <name> (id <id>): <detail>" (DSK §7).
func (v *Volume) nodeMsg(method, detail string) string {
	return fmt.Sprintf("simdisk: %s on %s (id %d): %s", method, v.node.Name(), v.node.ID(), detail)
}

func stateWord(st kernel.NodeState) string {
	switch st {
	case kernel.NodeDown:
		return "down"
	case kernel.NodePaused:
		return "paused"
	case kernel.NodeUp:
		return "up"
	}
	return "not up"
}

func pathErr(op, p string, err error) error { return &fs.PathError{Op: op, Path: p, Err: err} }
