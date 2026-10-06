package simnet_test

import (
	"fmt"
	"testing"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// connectedSet lists the ordered pairs (a != b) that are connected, as "a>b" strings.
func connectedSet(nw *simnet.Network, n int) map[string]bool {
	out := map[string]bool{}
	for a := 1; a <= n; a++ {
		for b := 1; b <= n; b++ {
			if a != b && nw.Connected(kernel.NodeID(a), kernel.NodeID(b)) {
				out[fmt.Sprintf("%d>%d", a, b)] = true
			}
		}
	}
	return out
}

// checkGraph fails unless Topology() has the n nodes and exactly the edges that Connected reports
// (NET-022).
func checkGraph(t *testing.T, nw *simnet.Network, n int) {
	t.Helper()
	g := nw.Topology()
	if int(g.Order()) != n {
		t.Fatalf("Topology(): %d vertices, want %d", g.Order(), n)
	}
	for a := kernel.NodeID(1); int(a) <= n; a++ {
		for b := kernel.NodeID(1); int(b) <= n; b++ {
			if a != b && g.ContainsEdge(g.GetVertexByID(a), g.GetVertexByID(b)) != nw.Connected(a, b) {
				t.Fatalf("Topology() and Connected disagree on %d -> %d", a, b)
			}
		}
	}
}

// AT-NET-12, NET-022, NET-023
func TestTopologyOperations(t *testing.T) {
	w := newNet(1, simnet.DefaultConfig())
	w.addNode("d")
	nw := w.nw
	v := nw.TopologyVersion()
	step := func(name string, wantChange bool) {
		t.Helper()
		nv := nw.TopologyVersion()
		if wantChange && nv != v+1 || !wantChange && nv != v {
			t.Fatalf("%s: TopologyVersion %d -> %d, change wanted: %v", name, v, nv, wantChange)
		}
		v = nv
		checkGraph(t, nw, 4)
	}

	nw.Partition([]kernel.NodeID{1, 2}, []kernel.NodeID{3})
	step("Partition", true)
	got := connectedSet(nw, 4)
	if len(got) != 2 || !got["1>2"] || !got["2>1"] {
		t.Fatalf("after Partition({1,2},{3}) connected pairs = %v, want only 1>2 and 2>1", got)
	}
	for id := kernel.NodeID(1); id <= 4; id++ {
		if !nw.Connected(id, id) {
			t.Fatalf("Connected(%d, %d) = false", id, id)
		}
	}
	p := w.records("net.partition")
	checkRecord(t, p[0], 0, "partition a,b|c|d", "groups=a,b|c|d, removed=10")

	nw.Heal()
	step("Heal", true)
	if len(connectedSet(nw, 4)) != 12 {
		t.Fatalf("Heal did not restore the full mesh")
	}
	checkRecord(t, w.records("net.heal")[0], 0, "heal", "added=10")

	nw.Cut(1, 2)
	step("Cut", true)
	if nw.Connected(1, 2) || !nw.Connected(2, 1) {
		t.Fatalf("Cut(1, 2): Connected(1,2)=%v Connected(2,1)=%v", nw.Connected(1, 2), nw.Connected(2, 1))
	}
	checkRecord(t, w.records("net.cut")[0], 0, "cut a -> b", "from=a, to=b, removed=1")

	nw.HealLink(1, 2)
	step("HealLink", true)
	if !nw.Connected(1, 2) {
		t.Fatalf("HealLink(1, 2) did not restore 1 -> 2")
	}
	checkRecord(t, w.records("net.heal_link")[0], 0, "heal a -> b", "from=a, to=b, added=1")

	nw.Isolate(3)
	step("Isolate", true)
	for id := kernel.NodeID(1); id <= 4; id++ {
		if id != 3 && (nw.Connected(id, 3) || nw.Connected(3, id)) {
			t.Fatalf("Isolate(3): edge between %d and 3 remains", id)
		}
	}
	if !nw.Connected(3, 3) {
		t.Fatalf("Connected(3, 3) = false")
	}
	checkRecord(t, w.records("net.isolate")[0], 0, "isolate c", "node=c, removed=6")

	nw.Isolate(3)
	step("second Isolate", false)
	checkRecord(t, w.records("net.isolate")[1], 0, "isolate c", "node=c, removed=0")
}

// AT-NET-25, NET-023, NET-024: records are emitted when nothing changes; empty groups are ignored;
// no group, or one group that lists every node, removes nothing; one group that lists only some
// nodes splits the nodes in two (that group and the unlisted group).
func TestPartitionGroups(t *testing.T) {
	w := newNet(1, simnet.DefaultConfig())
	nw := w.nw
	v := nw.TopologyVersion()
	nw.Partition()
	nw.Partition(nil, []kernel.NodeID{2, 1, 3}, []kernel.NodeID{})
	if nw.TopologyVersion() != v || len(connectedSet(nw, 3)) != 6 {
		t.Fatalf("no group, or one group of every node, changed the topology")
	}
	p := w.records("net.partition")
	checkRecord(t, p[0], 0, "partition a,b,c", "groups=a,b,c, removed=0")
	checkRecord(t, p[1], 0, "partition a,b,c", "groups=a,b,c, removed=0")

	nw.Partition([]kernel.NodeID{1, 2})
	if got := connectedSet(nw, 3); nw.TopologyVersion() != v+1 || len(got) != 2 || !got["1>2"] || !got["2>1"] {
		t.Fatalf("Partition({a, b}): version %d, connected %v; want %d, {1>2 2>1}", nw.TopologyVersion(), got, v+1)
	}
	checkRecord(t, w.records("net.partition")[2], 0, "partition a,b|c", "groups=a,b|c, removed=4")
	nw.Heal()
	checkRecord(t, w.records("net.heal")[0], 0, "heal", "added=4")

	nw.Partition([]kernel.NodeID{3}, []kernel.NodeID{1})
	checkRecord(t, w.records("net.partition")[3], 0, "partition c|a|b", "groups=c|a|b, removed=6")
	// partitions compose: a second partition only removes edges
	nw.Heal()
	nw.Partition([]kernel.NodeID{1}, []kernel.NodeID{2, 3})
	nw.Partition([]kernel.NodeID{1, 2}, []kernel.NodeID{3})
	got := connectedSet(nw, 3)
	if len(got) != 0 {
		t.Fatalf("composed partitions left %v connected", got)
	}
	checkRecord(t, w.records("net.partition")[5], 0, "partition a,b|c", "groups=a,b|c, removed=2")
	nw.Heal()
	checkRecord(t, w.records("net.heal")[2], 0, "heal", "added=6")
	v = nw.TopologyVersion()
	step := func(name string, delta uint64) {
		t.Helper()
		if nv := nw.TopologyVersion(); nv != v+delta {
			t.Fatalf("%s: TopologyVersion %d -> %d, want +%d", name, v, nv, delta)
		}
		v += delta
	}
	nw.Heal()
	step("Heal with nothing to add", 0)
	checkRecord(t, w.records("net.heal")[3], 0, "heal", "added=0")
	nw.HealLink(1, 2)
	step("HealLink of an existing edge", 0)
	checkRecord(t, w.records("net.heal_link")[0], 0, "heal a -> b", "from=a, to=b, added=0")
	nw.Cut(2, 3)
	step("Cut", 1)
	nw.Cut(2, 3)
	step("second Cut", 0)
	checkRecord(t, w.records("net.cut")[1], 0, "cut b -> c", "from=b, to=c, removed=0")

	// NET-023: changing a single edge is a change.
	nw.Heal()
	step("Heal of one edge", 1)
	checkRecord(t, w.records("net.heal")[4], 0, "heal", "added=1")
	nw.Isolate(1)
	nw.HealLink(2, 1)
	step("Isolate, HealLink", 2)
	nw.Partition([]kernel.NodeID{1})
	step("Partition of one edge", 1)
	checkRecord(t, w.records("net.partition")[6], 0, "partition a|b,c", "groups=a|b,c, removed=1")
	nw.HealLink(1, 3)
	nw.Isolate(1)
	step("HealLink, Isolate of one edge", 2)
	checkRecord(t, w.records("net.isolate")[1], 0, "isolate a", "node=a, removed=1")
	checkGraph(t, nw, 3)
}

// NET §8: each group's names are listed by ascending ID, not by name.
func TestPartitionGroupOrder(t *testing.T) {
	w := newNet(1, simnet.DefaultConfig())
	w.addNode("aa") // ID 4: sorts between a and b by name
	w.nw.Partition([]kernel.NodeID{4, 2})
	checkRecord(t, w.records("net.partition")[0], 0, "partition b,aa|a,c", "groups=b,aa|a,c, removed=8")
}

// NET-002: each topology change first registers the nodes added since the last network call.
func TestTopologyLateNodes(t *testing.T) {
	w := newNet(1, simnet.DefaultConfig())
	nw := w.nw
	w.addNode("d")
	nw.Partition([]kernel.NodeID{1, 2}, []kernel.NodeID{3})
	checkRecord(t, w.records("net.partition")[0], 0, "partition a,b|c|d", "groups=a,b|c|d, removed=10")
	if got := connectedSet(nw, 4); len(got) != 2 || !got["1>2"] || !got["2>1"] {
		t.Fatalf("Partition right after AddNode(d): connected %v, want only 1>2 and 2>1", got)
	}
	w.addNode("e")
	nw.Isolate(5)
	checkRecord(t, w.records("net.isolate")[0], 0, "isolate e", "node=e, removed=8")
	w.addNode("f")
	nw.Cut(1, 6)
	checkRecord(t, w.records("net.cut")[0], 0, "cut a -> f", "from=a, to=f, removed=1")
	w.addNode("g")
	nw.HealLink(7, 1)
	checkRecord(t, w.records("net.heal_link")[0], 0, "heal g -> a", "from=g, to=a, added=0")
	checkGraph(t, nw, 7)
}

// AT-NET-11 (Partition, Cut), NET-025
func TestTopologyMisuse(t *testing.T) {
	w := newNet(1, simnet.DefaultConfig())
	nw := w.nw
	v := nw.TopologyVersion()
	mustPanic(t, "simnet: Partition: node b (id 2) is in more than one group",
		func() { nw.Partition([]kernel.NodeID{1, 2}, []kernel.NodeID{2}) })
	mustPanic(t, "simnet: Partition: node a (id 1) is in more than one group",
		func() { nw.Partition([]kernel.NodeID{1, 1}) })
	mustPanic(t, "simnet: Partition: node b (id 2) is in more than one group",
		func() { nw.Partition([]kernel.NodeID{1}, []kernel.NodeID{2}, []kernel.NodeID{2}) })
	mustPanic(t, "simnet: Partition: unknown node id 7", func() { nw.Partition([]kernel.NodeID{1}, []kernel.NodeID{7}) })
	mustPanic(t, "simnet: Partition: unknown node id 7",
		func() { nw.Partition([]kernel.NodeID{1}, []kernel.NodeID{2}, []kernel.NodeID{7}) })
	// NET-024: a panicking Partition changes nothing, even after earlier groups were read.
	if len(connectedSet(nw, 3)) != 6 || nw.TopologyVersion() != v || len(w.records("net.partition")) != 0 {
		t.Fatalf("a panicking Partition changed edges, the version or the trace")
	}
	mustPanic(t, "simnet: Cut: from and to are the same node a (id 1)", func() { nw.Cut(1, 1) })
	mustPanic(t, "simnet: HealLink: from and to are the same node b (id 2)", func() { nw.HealLink(2, 2) })
	mustPanic(t, "simnet: Isolate: unknown node id 0", func() { nw.Isolate(0) })
	mustPanic(t, "simnet: Cut: unknown node id 9", func() { nw.Cut(9, 9) })
	mustPanic(t, "simnet: Cut: unknown node id 9", func() { nw.Cut(1, 9) })
	mustPanic(t, "simnet: HealLink: unknown node id 9", func() { nw.HealLink(1, 9) })
}

// AT-NET-22
func TestTopologySnapshot(t *testing.T) {
	w := newNet(1, simnet.DefaultConfig())
	w.nw.Cut(1, 2)
	g := w.nw.Topology()
	if g.Order() != 3 || g.Size() != 5 {
		t.Fatalf("Topology(): %d vertices, %d edges; want 3, 5", g.Order(), g.Size())
	}
	v1, v2 := g.GetVertexByID(1), g.GetVertexByID(2)
	if g.ContainsEdge(v1, v2) || !g.ContainsEdge(v2, v1) {
		t.Fatalf("Topology(): edge 1->2 present or 2->1 missing")
	}
	g.RemoveEdges(g.GetEdge(v2, v1))
	if !w.nw.Connected(2, 1) {
		t.Fatalf("changing the snapshot changed the network")
	}
}
