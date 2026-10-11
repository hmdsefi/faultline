// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package artifact

import (
	"slices"
	"sort"

	"github.com/hmdsefi/faultline/kernel"
)

// Slice is a causal slice: the records that happened before a root record. CausalSlice finds them
// breadth first, following each record's Cause and the previous record of the same node
// incarnation.
type Slice struct {
	Root      uint64   // 0: no slice
	Seqs      []uint64 // members in discovery order; Seqs[0] == Root
	Cap       int
	Truncated bool
}

// Contains reports whether seq is in s.
func (s Slice) Contains(seq uint64) bool {
	return seq != 0 && slices.Contains(s.Seqs, seq)
}

// poKey identifies a node incarnation for program order (ART-050).
type poKey struct {
	node kernel.NodeID
	inc  uint32
}

// hbIndex answers happens-before predecessor queries over records in ascending Seq (ART-050).
type hbIndex struct {
	records    []kernel.Record
	contiguous bool
	po         []int32 // index of the program-order predecessor, or -1
}

func newHBIndex(records []kernel.Record) *hbIndex {
	x := newSeqIndex(records)
	x.po = make([]int32, len(records))
	last := make(map[poKey]int32) // lookups only; never iterated
	for i, r := range records {
		x.po[i] = -1
		if r.Node == 0 {
			continue
		}
		k := poKey{r.Node, r.Inc}
		if j, ok := last[k]; ok {
			x.po[i] = j
		}
		last[k] = int32(i)
	}
	return x
}

// newSeqIndex returns an index for index only. It skips the program order that preds needs, which
// costs newHBIndex 4 bytes and a map lookup per record.
func newSeqIndex(records []kernel.Record) *hbIndex {
	n := len(records)
	return &hbIndex{records: records, contiguous: n == 0 || records[n-1].Seq-records[0].Seq+1 == uint64(n)}
}

// index returns the position of seq in the record list.
func (x *hbIndex) index(seq uint64) (int, bool) {
	n := len(x.records)
	if n == 0 || seq < x.records[0].Seq || seq > x.records[n-1].Seq {
		return 0, false
	}
	if x.contiguous {
		return int(seq - x.records[0].Seq), true //nolint:gosec // below len(records): seq is in range and the seqs are contiguous
	}
	i := sort.Search(n, func(i int) bool { return x.records[i].Seq >= seq })
	return i, i < n && x.records[i].Seq == seq
}

// preds returns the indexes of the happens-before predecessors of record i, in ART-050 order:
// the cause, then the program-order predecessor when it differs from the cause.
func (x *hbIndex) preds(i int, buf []int) []int {
	buf = buf[:0]
	r := x.records[i]
	cause := -1
	if r.Cause != 0 && r.Cause < r.Seq {
		if j, ok := x.index(r.Cause); ok {
			cause = j
			buf = append(buf, j)
		}
	}
	if p := int(x.po[i]); p >= 0 && p != cause {
		buf = append(buf, p)
	}
	return buf
}

// CausalSlice computes the causal slice of root in records (ascending Seq) with at most cap
// members. Truncated reports whether the cap cut some predecessor off.
func CausalSlice(records []kernel.Record, root uint64, cap int) Slice {
	if root == 0 || cap < 1 {
		return Slice{Cap: cap}
	}
	x := newHBIndex(records)
	ri, ok := x.index(root)
	if !ok {
		return Slice{Cap: cap}
	}
	in := make([]bool, len(records))
	in[ri] = true
	order := []uint64{root}
	members := []int{ri}
	queue := []int{ri}
	var buf []int
	for len(queue) > 0 && len(order) < cap {
		b := queue[0]
		queue = queue[1:]
		buf = x.preds(b, buf)
		for _, p := range buf {
			if in[p] || len(order) == cap {
				continue
			}
			in[p] = true
			order = append(order, records[p].Seq)
			members = append(members, p)
			queue = append(queue, p)
		}
	}
	truncated := false
	for _, m := range members {
		buf = x.preds(m, buf)
		for _, p := range buf {
			if !in[p] {
				truncated = true
			}
		}
	}
	return Slice{Root: root, Seqs: order, Cap: cap, Truncated: truncated}
}
