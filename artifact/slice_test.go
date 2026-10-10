// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package artifact

import (
	"slices"
	"testing"

	"github.com/hmdsefi/faultline/kernel"
)

// AT-ART-06
func TestCausalSlice(t *testing.T) {
	recs := fixtureRecords()
	cases := []struct {
		root      uint64
		cap       int
		want      []uint64
		truncated bool
	}{
		{12, 200, []uint64{12, 11, 10, 9, 7, 8, 6, 5, 3, 4, 2, 1}, false},
		{12, 5, []uint64{12, 11, 10, 9, 7}, true},
		{12, 1, []uint64{12}, true},
		{0, 200, nil, false},
		{99, 200, nil, false},
		// The cap falls between record 10's predecessors: its cause 9 is in, 7 is not.
		{12, 4, []uint64{12, 11, 10, 9}, true},
		// The cap is the whole slice: no member misses a predecessor.
		{12, 12, []uint64{12, 11, 10, 9, 7, 8, 6, 5, 3, 4, 2, 1}, false},
		{12, 0, nil, false},
		{12, -1, nil, false},
	}
	for _, c := range cases {
		s := CausalSlice(recs, c.root, c.cap)
		if !slices.Equal(s.Seqs, c.want) || s.Truncated != c.truncated || s.Cap != c.cap {
			t.Errorf("CausalSlice(root %d, cap %d) = %+v, want seqs %v truncated %v", c.root, c.cap, s, c.want, c.truncated)
		}
		if len(c.want) == 0 && s.Root != 0 {
			t.Errorf("CausalSlice(root %d) has root %d, want 0", c.root, s.Root)
		}
		if len(c.want) > 0 && s.Root != c.root {
			t.Errorf("CausalSlice(root %d) has root %d", c.root, s.Root)
		}
		for q := uint64(0); q <= 13; q++ {
			if s.Contains(q) != slices.Contains(c.want, q) {
				t.Errorf("CausalSlice(root %d, cap %d).Contains(%d) = %t", c.root, c.cap, q, s.Contains(q))
			}
		}
	}
}

func TestCausalSliceWithGaps(t *testing.T) {
	// Seqs 1, 2, 4 and 6 to 8 are missing; the cause 2 of seqs 3 and 5 is ignored (ART-050).
	recs := []kernel.Record{
		{Seq: 3, Node: 1, Inc: 1, Kind: "a", Cause: 2},
		{Seq: 5, Node: 1, Inc: 1, Kind: "b", Cause: 2},
		{Seq: 9, Kind: "check.violation", Cause: 5},
	}
	s := CausalSlice(recs, 9, 10)
	if !slices.Equal(s.Seqs, []uint64{9, 5, 3}) || s.Truncated {
		t.Fatalf("got %+v", s)
	}
}

// ART-050 and ART-051 on record lists that do not start at seq 1 or have holes.
func TestCausalSliceEdges(t *testing.T) {
	rec := func(seq uint64, node kernel.NodeID, inc uint32, cause uint64) kernel.Record {
		return kernel.Record{Seq: seq, Node: node, Inc: inc, Kind: "k", Cause: cause}
	}
	// A ring buffer kept seqs 5 to 10, so causes 1 to 4 are gone. Seq 8 starts n1's second
	// incarnation: 7 is not its program-order predecessor.
	ring := []kernel.Record{rec(5, 1, 1, 4), rec(6, 2, 1, 3), rec(7, 1, 1, 6), rec(8, 1, 2, 2), rec(9, 2, 1, 8), rec(10, 0, 0, 9)}
	// Seq 3 is missing, so 5's cause is ignored and seq 4 is not its predecessor. 6's cause 7 is
	// not earlier. Global records have no program order, whatever their Inc.
	holes := []kernel.Record{rec(1, 0, 0, 0), rec(2, 1, 1, 1), rec(4, 2, 1, 2), rec(5, 1, 1, 3), rec(6, 1, 1, 7), rec(7, 0, 2, 6), rec(8, 0, 2, 6)}
	// 4's cause 3 is already in the slice when 4 is walked; its program-order predecessor 2 is
	// reached only through 4.
	diamond := []kernel.Record{rec(1, 0, 0, 0), rec(2, 1, 1, 1), rec(3, 2, 1, 1), rec(4, 1, 1, 3), rec(5, 2, 1, 4)}
	cases := []struct {
		name      string
		records   []kernel.Record
		root      uint64
		cap       int
		want      []uint64
		truncated bool
	}{
		{"ring", ring, 10, 200, []uint64{10, 9, 8, 6}, false},
		{"ring", ring, 10, 3, []uint64{10, 9, 8}, true}, // 9's predecessor 6 is left out
		{"ring", ring, 4, 200, nil, false},
		{"holes", holes, 8, 200, []uint64{8, 6, 5, 2, 1}, false},
		{"holes", holes, 3, 200, nil, false},
		{"diamond", diamond, 5, 200, []uint64{5, 4, 3, 2, 1}, false},
		{"empty", nil, 1, 200, nil, false},
	}
	for _, c := range cases {
		s := CausalSlice(c.records, c.root, c.cap)
		root := c.root
		if c.want == nil {
			root = 0
		}
		if !slices.Equal(s.Seqs, c.want) || s.Truncated != c.truncated || s.Root != root || s.Cap != c.cap {
			t.Errorf("%s: CausalSlice(root %d, cap %d) = %+v, want seqs %v truncated %v", c.name, c.root, c.cap, s, c.want, c.truncated)
		}
	}
}

// §7 and ART-051: records out of order, repeated or with seq 0 give an unspecified slice, but
// CausalSlice and Contains must not panic.
func TestCausalSliceBadInput(t *testing.T) {
	rec := func(seq uint64, node kernel.NodeID, cause uint64) kernel.Record {
		return kernel.Record{Seq: seq, Node: node, Inc: 1, Kind: "k", Cause: cause}
	}
	for _, recs := range [][]kernel.Record{
		{rec(1, 1, 0), rec(3, 1, 2), rec(2, 0, 3), rec(4, 2, 9)}, // out of order, yet last - first + 1 == len
		{rec(0, 1, 0), rec(0, 1, 0), rec(2, 1, 1)},               // seq 0, repeated
		{rec(5, 1, 4), rec(3, 2, 5), rec(4, 1, 3)},               // last below first
	} {
		for root := uint64(0); root <= 6; root++ {
			for _, c := range []int{0, 1, 200} {
				s := CausalSlice(recs, root, c)
				for q := uint64(0); q <= 6; q++ {
					s.Contains(q)
				}
			}
		}
	}
}

func BenchmarkCausalSlice(b *testing.B) {
	recs := make([]kernel.Record, 1_000_000)
	for i := range recs {
		recs[i] = kernel.Record{Seq: uint64(i + 1), Node: kernel.NodeID(i%5 + 1), Inc: 1, Kind: "k", Cause: uint64(i)}
	}
	b.ResetTimer()
	for b.Loop() {
		CausalSlice(recs, 1_000_000, DefaultSliceCap)
	}
}
