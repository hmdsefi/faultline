// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simnet_test

import (
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// AT-NET-01
func TestDefaults(t *testing.T) {
	want := simnet.Config{Default: simnet.Link{Latency: time.Millisecond, Jitter: 4 * time.Millisecond}}
	if got := simnet.DefaultConfig(); got != want {
		t.Fatalf("DefaultConfig() = %+v, want %+v", got, want)
	}
	if err := (simnet.Link{}).Validate(); err != nil {
		t.Fatalf("Link{}.Validate() = %v", err)
	}
}

// NET-004, NET §7
func TestLinkValidate(t *testing.T) {
	cases := []struct {
		l    simnet.Link
		want string
	}{
		{simnet.Link{Latency: -1}, "invalid link: Latency -1ns out of range [0s, 24h0m0s]"},
		{simnet.Link{Jitter: 25 * time.Hour}, "invalid link: Jitter 25h0m0s out of range [0s, 24h0m0s]"},
		{simnet.Link{Tail: -time.Second}, "invalid link: Tail -1s out of range [0s, 24h0m0s]"},
		{simnet.Link{TailPPM: 1_000_001}, "invalid link: TailPPM 1000001 exceeds 1000000"},
		{simnet.Link{DropPPM: 2_000_000}, "invalid link: DropPPM 2000000 exceeds 1000000"},
		{simnet.Link{DupPPM: 1_000_001}, "invalid link: DupPPM 1000001 exceeds 1000000"},
		// order: Latency, Jitter, Tail, TailPPM, DropPPM, DupPPM
		{simnet.Link{Latency: -1, DropPPM: 2_000_000}, "invalid link: Latency -1ns out of range [0s, 24h0m0s]"},
		{simnet.Link{Tail: -1, TailPPM: 2_000_000}, "invalid link: Tail -1ns out of range [0s, 24h0m0s]"},
		{simnet.Link{TailPPM: 2_000_000, DupPPM: 2_000_000}, "invalid link: TailPPM 2000000 exceeds 1000000"},
		{simnet.Link{Latency: -1, Jitter: -1}, "invalid link: Latency -1ns out of range [0s, 24h0m0s]"},
		{simnet.Link{Jitter: -1, Tail: -1}, "invalid link: Jitter -1ns out of range [0s, 24h0m0s]"},
		{simnet.Link{TailPPM: 2_000_000, DropPPM: 2_000_000}, "invalid link: TailPPM 2000000 exceeds 1000000"},
		{simnet.Link{DropPPM: 2_000_000, DupPPM: 2_000_000}, "invalid link: DropPPM 2000000 exceeds 1000000"},
	}
	for _, c := range cases {
		err := c.l.Validate()
		if err == nil || err.Error() != c.want {
			t.Errorf("%+v.Validate() = %v, want %q", c.l, err, c.want)
		}
	}
	max := simnet.Link{Latency: simnet.MaxDelay, Jitter: simnet.MaxDelay, Tail: simnet.MaxDelay,
		TailPPM: simnet.MaxPPM, DropPPM: simnet.MaxPPM, DupPPM: simnet.MaxPPM, FIFO: true}
	if err := max.Validate(); err != nil {
		t.Errorf("maximal link: %v", err)
	}
}

func TestDropReasonString(t *testing.T) {
	cases := map[simnet.DropReason]string{
		simnet.DropPartition:         "partition",
		simnet.DropLoss:              "loss",
		simnet.DropPartitionInFlight: "partition-in-flight",
		simnet.DropDown:              "down",
		simnet.DropNoHandler:         "no-handler",
		0:                            "DropReason(0)",
		9:                            "DropReason(9)",
	}
	for r, want := range cases {
		if got := r.String(); got != want {
			t.Errorf("DropReason(%d).String() = %q, want %q", r, got, want)
		}
	}
}

func TestStatsDropped(t *testing.T) {
	st := simnet.Stats{DroppedPartition: 1, DroppedLoss: 2, DroppedInFlight: 4, DroppedDown: 8, DroppedNoHandler: 16, Deferred: 100}
	if st.Dropped() != 31 {
		t.Fatalf("Dropped() = %d, want 31", st.Dropped())
	}
}

// NET-001, AT-NET-11 (New)
func TestNewPanics(t *testing.T) {
	mustPanic(t, "simnet: New: nil *kernel.Sim", func() { simnet.New(nil, simnet.Config{}) })
	s := kernel.New(kernel.Config{Seed: 1})
	mustPanic(t, "simnet: New: invalid Config.Default: invalid link: Latency -1ns out of range [0s, 24h0m0s]",
		func() { simnet.New(s, simnet.Config{Default: simnet.Link{Latency: -1}}) })
}

// NET-001: New emits no record, schedules nothing, and draws nothing.
func TestNewIsSilent(t *testing.T) {
	s := kernel.New(kernel.Config{Seed: 1, Trace: kernel.TraceConfig{Level: kernel.TraceFull}})
	s.AddNode("a", func(*kernel.Node) {})
	s.RunUntil(0)
	recs, hash := len(s.Records()), s.TraceHash()
	nw := simnet.New(s, simnet.DefaultConfig())
	if len(s.Records()) != recs || s.TraceHash() != hash {
		t.Fatalf("New emitted records")
	}
	if _, ok := s.NextAt(); ok {
		t.Fatalf("New scheduled an event")
	}
	if nw.Sim() != s || nw.Config() != simnet.DefaultConfig() {
		t.Fatalf("Sim/Config accessors")
	}
	cfg := simnet.Config{Default: simnet.Link{Latency: 7 * time.Millisecond}, NoDeliveryCheck: true}
	if got := simnet.New(kernel.New(kernel.Config{Seed: 1}), cfg).Config(); got != cfg {
		t.Fatalf("Config() = %+v, want %+v", got, cfg)
	}
	if !untouched(s, "net/link/a/a") {
		t.Fatalf("New drew from the self link stream")
	}
}

// NET-002, NET-003, NET-022, NET-026
func TestRegistration(t *testing.T) {
	s := kernel.New(kernel.Config{Seed: 1})
	s.AddNode("a", func(*kernel.Node) {})
	s.AddNode("b", func(*kernel.Node) {})
	nw := simnet.New(s, simnet.Config{})
	s.AddNode("c", func(*kernel.Node) {})
	if v := nw.TopologyVersion(); v != 3 {
		t.Fatalf("TopologyVersion() = %d, want 3 (one per registered node)", v)
	}
	for a := kernel.NodeID(1); a <= 3; a++ {
		for b := kernel.NodeID(1); b <= 3; b++ {
			if !nw.Connected(a, b) {
				t.Errorf("Connected(%d, %d) = false", a, b)
			}
		}
	}
	g := nw.Topology()
	if g.Order() != 3 || g.Size() != 6 {
		t.Fatalf("Topology(): %d vertices, %d edges; want 3, 6", g.Order(), g.Size())
	}
	s.AddNode("d", func(*kernel.Node) {})
	if !nw.Connected(4, 1) || !nw.Connected(1, 4) || nw.TopologyVersion() != 4 {
		t.Fatalf("late node d not registered")
	}
	mustPanic(t, "simnet: Connected: unknown node id 9", func() { nw.Connected(1, 9) })
	mustPanic(t, "simnet: Connected: unknown node id 0", func() { nw.Connected(0, 1) })
	mustPanic(t, "simnet: LinkStats: unknown node id 5", func() { nw.LinkStats(5, 1) })
}

// NET-002, NET-020: LinkStats, Topology and the crash hook register nodes themselves.
func TestLateNodeReaders(t *testing.T) {
	s := kernel.New(kernel.Config{Seed: 1})
	s.AddNode("a", func(*kernel.Node) {})
	nw := simnet.New(s, simnet.Config{})
	s.AddNode("b", func(*kernel.Node) {})
	if st := nw.LinkStats(2, 1); st != (simnet.Stats{}) {
		t.Fatalf("LinkStats(2, 1) = %+v", st)
	}
	s.AddNode("c", func(*kernel.Node) {})
	if g := nw.Topology(); g.Order() != 3 || g.Size() != 6 {
		t.Fatalf("Topology(): %d vertices, %d edges; want 3, 6", g.Order(), g.Size())
	}
	d := s.AddNode("d", func(*kernel.Node) {}) // its boot never calls the network
	s.RunUntil(0)
	d.Crash()
	if err := s.Err(); err != nil {
		t.Fatalf("crash of a node the network had not registered: %v", err)
	}
	if v := nw.TopologyVersion(); v != 4 {
		t.Fatalf("TopologyVersion() = %d, want 4", v)
	}
}

// NET-026: the snapshot is a copy, with vertices in ascending ID order.
func TestTopologyIsACopy(t *testing.T) {
	w := newNet(1, simnet.DefaultConfig())
	g := w.nw.Topology()
	for k, v := range g.GetAllVertices() {
		if v.Label() != kernel.NodeID(k+1) {
			t.Fatalf("Topology(): vertex %d has label %d, want %d", k, v.Label(), k+1)
		}
	}
	g.RemoveEdges(g.GetEdge(g.GetVertexByID(1), g.GetVertexByID(2)))
	if g2 := w.nw.Topology(); g2.Size() != 6 || !g2.ContainsEdge(g2.GetVertexByID(1), g2.GetVertexByID(2)) {
		t.Fatalf("changing a snapshot changed the next one: %d edges", g2.Size())
	}
}

// NET-007, NET-008, NET §8 net.handle
func TestHandle(t *testing.T) {
	w := newNet(1, simnet.DefaultConfig())
	hs := w.records("net.handle")
	if len(hs) != 3 {
		t.Fatalf("%d net.handle records at boot, want 3", len(hs))
	}
	for _, r := range hs { // boots at t=0 run in seed-dependent order
		if r.Node == 1 {
			checkRecord(t, r, 1, "handler installed on a", "node=a, installed=true")
			if r.Inc != 1 {
				t.Errorf("net.handle Inc = %d, want 1", r.Inc)
			}
		}
	}
	w.nw.Handle(w.b, nil)
	hs = w.records("net.handle")
	if len(hs) != 4 {
		t.Fatalf("%d net.handle records after Handle(b, nil), want 4", len(hs))
	}
	checkRecord(t, hs[3], 2, "handler removed from b", "node=b, installed=false")

	w.c.Pause()
	w.nw.Handle(w.c, func(kernel.NodeID, any) {}) // allowed while paused
	w.c.Resume()

	before := w.countPrefix("net.")
	w.b.Crash()
	if err := w.s.Err(); err != nil {
		t.Fatalf("crash hook failed: %v", err)
	}
	if w.countPrefix("net.") != before {
		t.Fatalf("crash emitted net.* records")
	}
	mustPanic(t, "simnet: Handle: node b (id 2) is down", func() { w.nw.Handle(w.b, nil) })
	mustPanic(t, "simnet: Handle: nil *kernel.Node", func() { w.nw.Handle(nil, nil) })
	other := kernel.New(kernel.Config{Seed: 1})
	foreign := other.AddNode("a", func(*kernel.Node) {})
	mustPanic(t, "simnet: Handle: node a belongs to a different Sim", func() { w.nw.Handle(foreign, nil) })
}
