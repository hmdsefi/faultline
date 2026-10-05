package kernel

import "encoding/binary"

// Attr is one key/value pair of a record. Keys are snake_case.
type Attr struct{ Key, Value string }

// Record is one trace entry (architecture §12.1).
type Record struct {
	Seq   uint64 // 1-based emission order
	At    Time
	Node  NodeID // 0 = global
	Inc   uint32 // node incarnation
	Kind  string // namespaced, e.g. "kernel.event", "net.send"
	Cause uint64 // Seq of the causing record; 0 only for kernel.start
	Text  string // short human description
	Attrs []Attr // ordered
}

// trace holds the running hash and the kept records of a Sim.
type trace struct {
	level  TraceLevel
	buffer int
	seq    uint64   // Seq of the most recent record
	hash   uint64   // running FNV-1a 64 over the encodings of all records
	buf    []byte   // reused encoding buffer
	kept   []Record // TraceFull: all records (buffer 0) or the ring (buffer > 0)
	next   int      // ring: index of the oldest record once the ring is full
}

func newTrace(cfg TraceConfig) trace {
	return trace{level: cfg.Level, buffer: cfg.Buffer, hash: fnvOffset64}
}

// AppendRecord appends the canonical binary encoding of r (KRN-095) to b.
func AppendRecord(b []byte, r Record) []byte {
	b = binary.AppendUvarint(b, r.Seq)
	b = binary.AppendVarint(b, int64(r.At))
	b = binary.AppendVarint(b, int64(r.Node))
	b = binary.AppendUvarint(b, uint64(r.Inc))
	b = appendString(b, r.Kind)
	b = binary.AppendUvarint(b, r.Cause)
	b = appendString(b, r.Text)
	b = binary.AppendUvarint(b, uint64(len(r.Attrs)))
	for _, a := range r.Attrs {
		b = appendString(b, a.Key)
		b = appendString(b, a.Value)
	}
	return b
}

// appendString appends the uvarint length of s and its bytes.
func appendString(b []byte, s string) []byte {
	b = binary.AppendUvarint(b, uint64(len(s)))
	return append(b, s...)
}

// HashRecords returns the trace hash of a complete record sequence starting at Seq 1.
func HashRecords(rs []Record) uint64 {
	h := uint64(fnvOffset64)
	var buf []byte
	for _, r := range rs {
		buf = AppendRecord(buf[:0], r)
		h = fnvFold(h, buf)
	}
	return h
}

// emit assigns Seq and At, defaults Cause to the previous record, folds the record into the hash
// and keeps it with TraceFull (KRN-090 steps 3-5). Callers have validated r and set Inc.
func (s *Sim) emit(r Record) uint64 {
	tr := &s.tr
	r.Seq = tr.seq + 1
	r.At = s.now
	if r.Cause == 0 {
		r.Cause = tr.seq
	}
	tr.seq = r.Seq
	tr.buf = AppendRecord(tr.buf[:0], r)
	tr.hash = fnvFold(tr.hash, tr.buf)
	if tr.level == TraceFull {
		if len(r.Attrs) > 0 {
			r.Attrs = append([]Attr(nil), r.Attrs...)
		}
		tr.keep(r)
	}
	return r.Seq
}

// keep stores r, dropping the oldest record when a bounded ring is full (KRN-097).
func (tr *trace) keep(r Record) {
	if tr.buffer == 0 || len(tr.kept) < tr.buffer {
		tr.kept = append(tr.kept, r)
		return
	}
	tr.kept[tr.next] = r
	tr.next = (tr.next + 1) % tr.buffer
}

// Cause returns the Seq of the most recent record emitted.
func (s *Sim) Cause() uint64 { return s.tr.seq }

// TraceHash returns the running FNV-1a 64 hash over all records emitted so far.
func (s *Sim) TraceHash() uint64 { return s.tr.hash }

// Records returns a new slice with the kept records in Seq order (nil with TraceHash).
// Callers must not modify the Attrs slices of the returned records.
func (s *Sim) Records() []Record {
	tr := &s.tr
	if tr.level != TraceFull {
		return nil
	}
	out := make([]Record, 0, len(tr.kept))
	out = append(out, tr.kept[tr.next:]...)
	return append(out, tr.kept[:tr.next]...)
}
