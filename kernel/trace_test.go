// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package kernel

import (
	"fmt"
	"testing"
	"time"
)

// AT-KRN-36
func TestEmit(t *testing.T) {
	s := fullSim(1)
	for i := 2; i <= 7; i++ {
		if seq := s.Emit(Record{Kind: "t.a"}); seq != uint64(i) {
			t.Fatalf("Emit #%d returned %d", i-1, seq)
		}
	}
	s.RunUntil(sec(2))
	if seq := s.Emit(Record{Kind: "t.x", Text: "hi"}); seq != 8 {
		t.Fatalf("Emit returned %d, want 8", seq)
	}
	if r := lastRecord(s); r.At != sec(2) || r.Cause != 7 || r.Text != "hi" {
		t.Fatalf("record %s", recLine(r))
	}
	s.Emit(Record{Kind: "t.y", Cause: 3})
	if r := lastRecord(s); r.Cause != 3 {
		t.Fatalf("explicit cause not kept: %s", recLine(r))
	}
	n := s.AddNode("n1", func(*Node) {})
	s.RunFor(0)
	if n.Incarnation() != 1 {
		t.Fatalf("n1 not booted: inc %d", n.Incarnation())
	}
	s.Emit(Record{Kind: "t.z", Node: n.ID()})
	if r := lastRecord(s); r.Inc != 1 || r.Node != n.ID() {
		t.Fatalf("default Inc: %s", recLine(r))
	}
	s.Emit(Record{Kind: "t.inc", Node: n.ID(), Inc: 7})
	if r := lastRecord(s); r.Inc != 7 {
		t.Fatalf("explicit Inc not kept: %s", recLine(r))
	}
	attrs := []Attr{{Key: "k", Value: "v"}}
	s.Emit(Record{Kind: "t.attrs", Attrs: attrs})
	attrs[0].Value = "changed"
	if r := lastRecord(s); r.Attrs[0].Value != "v" {
		t.Fatal("Records() shares the caller's Attrs slice")
	}
	s.Emit(Record{Kind: "t.empty", Attrs: make([]Attr, 0, 2)})
	if r := lastRecord(s); r.Attrs != nil {
		t.Fatal("Records() keeps an empty Attrs slice of the caller")
	}
	s.Emit(Record{Kind: "kernelx.y", Cause: s.Cause()})
	if r := lastRecord(s); r.Kind != "kernelx.y" || r.Cause != r.Seq-1 {
		t.Fatalf("near-miss kind or Cause == Cause(): %s", recLine(r))
	}
	next := s.Cause() + 1
	mustPanic(t, "kernel: Emit: empty Kind", func() { s.Emit(Record{}) })
	mustPanic(t, `kernel: Emit: kind "kernel.x" is reserved for the kernel`, func() { s.Emit(Record{Kind: "kernel.x"}) })
	mustPanic(t, `kernel: Emit: empty attribute key in "t.k"`, func() { s.Emit(Record{Kind: "t.k", Attrs: []Attr{{Value: "v"}}}) })
	mustPanic(t, `kernel: Emit: unknown node 99 in "t.n"`, func() { s.Emit(Record{Kind: "t.n", Node: 99}) })
	mustPanic(t, fmt.Sprintf("kernel: Emit: cause %d is not an earlier record (next seq %d)", next, next),
		func() { s.Emit(Record{Kind: "t.c", Cause: next}) })
	mustPanic(t, fmt.Sprintf("kernel: Emit: cause %d is not an earlier record (next seq %d)", next+4, next),
		func() { s.Emit(Record{Kind: "t.c", Cause: next + 4}) })
	mustPanic(t, `kernel: Emit: unknown node -1 in "t.n"`, func() { s.Emit(Record{Kind: "t.n", Node: -1}) })

	// The records below fail several checks of KRN-090 step 1 at once; the first check must win.
	bad := []Attr{{Key: "a", Value: "1"}, {Value: "v"}, {Key: "b", Value: "2"}}
	mustPanic(t, "kernel: Emit: empty Kind", func() { s.Emit(Record{Attrs: bad, Node: 99, Cause: next}) })
	mustPanic(t, `kernel: Emit: kind "kernel.x" is reserved for the kernel`,
		func() { s.Emit(Record{Kind: "kernel.x", Attrs: bad, Node: 99, Cause: next}) })
	mustPanic(t, `kernel: Emit: empty attribute key in "t.k"`,
		func() { s.Emit(Record{Kind: "t.k", Attrs: bad, Node: 99, Cause: next}) })
	mustPanic(t, `kernel: Emit: unknown node 99 in "t.n"`, func() { s.Emit(Record{Kind: "t.n", Node: 99, Cause: next}) })
	if s.Cause() != next-1 {
		t.Fatalf("a rejected Emit moved Cause() to %d", s.Cause())
	}
}

// KRN-096: Emit folds the same records into the hash at every trace level, including the default
// Inc of a node record.
func TestEmitHashLevels(t *testing.T) {
	var hashes []uint64
	for _, tc := range []TraceConfig{{Level: TraceHash}, {Level: TraceFull}, {Level: TraceFull, Buffer: 2}} {
		s := New(Config{Seed: 1, Trace: tc})
		n := s.AddNode("n1", func(*Node) {})
		s.RunFor(0)
		send := s.Emit(Record{Kind: "t.send", Node: n.ID(), Attrs: []Attr{{Key: "a", Value: "1"}, {Key: "b", Value: "2"}}})
		s.Emit(Record{Kind: "t.recv", Cause: send})
		s.Emit(Record{Kind: "t.done"})
		hashes = append(hashes, s.TraceHash())
		if tc.Level == TraceFull && tc.Buffer == 0 && HashRecords(s.Records()) != s.TraceHash() {
			t.Errorf("HashRecords(Records()) = %#016x, TraceHash %#016x", HashRecords(s.Records()), s.TraceHash())
		}
	}
	if hashes[0] != 0x9b96a7e825a71e7d || hashes[1] != hashes[0] || hashes[2] != hashes[0] {
		t.Errorf("hashes by level %#016x", hashes)
	}
}

// KRN-091, KRN-092: records emitted in a callback chain from its kernel.event; an event's cause is
// the record emitted most recently before it was scheduled.
func TestCauseChain(t *testing.T) {
	s := fullSim(1)
	s.After(time.Second, "outer", func() {
		s.Emit(Record{Kind: "t.send"})
		s.After(time.Second, "deliver", func() { s.Emit(Record{Kind: "t.recv"}) })
		s.Emit(Record{Kind: "t.after"})
	})
	s.Run()
	checkRecords(t, s, `
1 0.000000000s 0/0 kernel.start cause=0 "start" [seed=0x0000000000000001 tie_break=seeded]
2 1.000000000s 0/0 kernel.event cause=1 "outer" [id=1]
3 1.000000000s 0/0 t.send cause=2 "" []
4 1.000000000s 0/0 t.after cause=3 "" []
5 2.000000000s 0/0 kernel.event cause=3 "deliver" [id=2]
6 2.000000000s 0/0 t.recv cause=5 "" []
`)
}

// AT-KRN-38 (retention; the golden.MustBeDeterministic part is in determinism_test.go)
func TestDeterminismAndRetention(t *testing.T) {
	for _, tc := range []TraceConfig{{Level: TraceHash}, {Level: TraceFull}, {Level: TraceFull, Buffer: 5}} {
		s, _, _ := lifecycleScenario(tc)
		if s.TraceHash() != 0x42a2518011c8bc6a || s.Executed() != 12 || s.Now() != sec(1) {
			t.Errorf("%+v: hash %#016x executed %d now %v", tc, s.TraceHash(), s.Executed(), s.Now())
		}
		if tc.Buffer == 5 {
			rs := s.Records()
			if len(rs) != 5 || rs[0].Seq != 23 || rs[4].Seq != 27 {
				t.Errorf("ring records %v", rs)
			}
		}
	}
}
