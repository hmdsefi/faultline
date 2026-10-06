package simnet_test

import (
	"reflect"
	"testing"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

type ids = []kernel.NodeID

func checkGroups(t *testing.T, name string, got, want [][]kernel.NodeID) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

// AT-NET-13
func TestBridgePartitionQueries(t *testing.T) {
	w := newNet(1, simnet.DefaultConfig())
	w.nw.Cut(1, 3)
	w.nw.Cut(3, 1)
	before := len(w.s.Records())
	checkGroups(t, "Components(nil)", w.nw.Components(nil), [][]kernel.NodeID{{1, 2, 3}})
	checkGroups(t, "Cliques(nil)", w.nw.Cliques(nil), [][]kernel.NodeID{{1, 2}, {2, 3}})
	if got := w.nw.QuorumHubs(nil, 2); !reflect.DeepEqual(got, ids{1, 2, 3}) {
		t.Errorf("QuorumHubs(nil, 2) = %v, want [1 2 3]", got)
	}
	if got := w.nw.QuorumHubs(nil, 3); !reflect.DeepEqual(got, ids{2}) {
		t.Errorf("QuorumHubs(nil, 3) = %v, want [2]", got)
	}
	if got := w.nw.QuorumHubs(ids{1, 3}, 2); got != nil {
		t.Errorf("QuorumHubs([1 3], 2) = %v, want nil", got)
	}
	if len(w.s.Records()) != before {
		t.Errorf("queries emitted records (NET-027)")
	}
}

// AT-NET-14
func TestOneWayComponents(t *testing.T) {
	w := newNet(1, simnet.DefaultConfig())
	w.nw.Cut(1, 2)
	w.nw.Cut(3, 2)
	checkGroups(t, "Components(nil)", w.nw.Components(nil), [][]kernel.NodeID{{1, 3}, {2}})
	checkGroups(t, "Components([2 1 2])", w.nw.Components(ids{2, 1, 2}), [][]kernel.NodeID{{1}, {2}})
}

// AT-NET-15
func TestCanonicalCliqueOrder(t *testing.T) {
	w := newNet(1, simnet.DefaultConfig())
	w.addNode("d")
	want := [][]kernel.NodeID{{1, 2}, {1, 3}, {2, 4}, {3, 4}}
	w.nw.Cut(1, 4)
	w.nw.Cut(4, 1)
	w.nw.Cut(2, 3)
	w.nw.Cut(3, 2)
	checkGroups(t, "Cliques(nil)", w.nw.Cliques(nil), want)
	w.nw.Heal()
	w.nw.Cut(3, 2)
	w.nw.Cut(4, 1)
	w.nw.Cut(2, 3)
	w.nw.Cut(1, 4)
	checkGroups(t, "Cliques(nil) after re-cut", w.nw.Cliques(nil), want)
}

// NET-029, NET-031: with only one-way links to 1, node 1 is a singleton clique, and the larger
// group sorts first although [1] is lexicographically smaller. Tarjan emits [1] first after
// Cut(1, 2), Cut(1, 3).
func TestQueryGroupOrder(t *testing.T) {
	want := [][]kernel.NodeID{{2, 3}, {1}}
	w := newNet(1, simnet.DefaultConfig())
	w.nw.Cut(2, 1)
	w.nw.Cut(3, 1)
	checkGroups(t, "Components(nil)", w.nw.Components(nil), want)
	checkGroups(t, "Components([])", w.nw.Components(ids{}), want)
	checkGroups(t, "Cliques(nil)", w.nw.Cliques(nil), want)
	if got := w.nw.QuorumHubs(nil, 2); !reflect.DeepEqual(got, ids{2, 3}) {
		t.Errorf("QuorumHubs(nil, 2) = %v, want [2 3]", got)
	}
	w = newNet(1, simnet.DefaultConfig())
	w.nw.Cut(1, 2)
	w.nw.Cut(1, 3)
	checkGroups(t, "Components(nil) after Cut(1, 2), Cut(1, 3)", w.nw.Components(nil), want)
	checkGroups(t, "Cliques(nil) after Cut(1, 2), Cut(1, 3)", w.nw.Cliques(nil), want)
	if got := w.nw.QuorumHubs(nil, 2); !reflect.DeepEqual(got, ids{2, 3}) {
		t.Errorf("QuorumHubs(nil, 2) after Cut(1, 2), Cut(1, 3) = %v, want [2 3]", got)
	}
}

// NET-028, NET-029: paths and links count only between nodes in among.
func TestQueriesInsideAmong(t *testing.T) {
	w := newNet(1, simnet.DefaultConfig())
	w.nw.Cut(1, 2)
	// over all nodes, 1 -> 3 -> 2 and 2 -> 1 join 1 and 2
	checkGroups(t, "Components([1 2])", w.nw.Components(ids{1, 2}), [][]kernel.NodeID{{1}, {2}})
	w.nw.Heal()
	w.nw.Cut(1, 3)
	w.nw.Cut(3, 1)
	checkGroups(t, "Cliques([1 3])", w.nw.Cliques(ids{1, 3}), [][]kernel.NodeID{{1}, {3}})
}

// NET-002: a query that is the first network call after AddNode includes the new node, also
// when among names it.
func TestQueryLateNodes(t *testing.T) {
	queries := []struct {
		name  string
		query func(nw *simnet.Network) any
		want  any
	}{
		{"Components(nil)", func(nw *simnet.Network) any { return nw.Components(nil) }, [][]kernel.NodeID{{1, 2, 3, 4}}},
		{"Cliques(nil)", func(nw *simnet.Network) any { return nw.Cliques(nil) }, [][]kernel.NodeID{{1, 2, 3, 4}}},
		{"QuorumHubs(nil, 4)", func(nw *simnet.Network) any { return nw.QuorumHubs(nil, 4) }, ids{1, 2, 3, 4}},
		{"Components([4 1])", func(nw *simnet.Network) any { return nw.Components(ids{4, 1}) }, [][]kernel.NodeID{{1, 4}}},
		{"Cliques([4 1])", func(nw *simnet.Network) any { return nw.Cliques(ids{4, 1}) }, [][]kernel.NodeID{{1, 4}}},
		{"QuorumHubs([4 1], 2)", func(nw *simnet.Network) any { return nw.QuorumHubs(ids{4, 1}, 2) }, ids{1, 4}},
	}
	for _, q := range queries {
		w := newNet(1, simnet.DefaultConfig()) // a fresh world each: the first query registers d
		w.addNode("d")
		if got := q.query(w.nw); !reflect.DeepEqual(got, q.want) {
			t.Errorf("%s = %v, want %v", q.name, got, q.want)
		}
	}
}

// NET-027: queries change neither the topology, its version nor the caller's slice, emit no
// record and draw from no stream. Their results are new slices.
func TestQueriesHaveNoSideEffects(t *testing.T) {
	w := newNet(1, simnet.DefaultConfig())
	w.nw.Cut(1, 3)
	w.nw.Cut(3, 1)
	v := w.nw.TopologyVersion()
	conn := connectedSet(w.nw, 3)
	before := len(w.s.Records())
	among := ids{3, 1, 3}
	w.nw.Components(among)
	w.nw.Cliques(among)
	w.nw.QuorumHubs(among, 1)
	w.nw.Components(nil)
	w.nw.Cliques(nil)
	w.nw.QuorumHubs(nil, 1)
	if !reflect.DeepEqual(among, ids{3, 1, 3}) {
		t.Errorf("among changed to %v", among)
	}
	if w.nw.TopologyVersion() != v || !reflect.DeepEqual(connectedSet(w.nw, 3), conn) {
		t.Errorf("queries changed the topology (version %d, want %d)", w.nw.TopologyVersion(), v)
	}
	if len(w.s.Records()) != before {
		t.Errorf("queries emitted records")
	}
	w.nw.Components(nil)[0][0] = 9
	w.nw.Cliques(nil)[0][0] = 9
	w.nw.QuorumHubs(nil, 1)[0] = 9
	checkGroups(t, "Components(nil) after writing into a result", w.nw.Components(nil), [][]kernel.NodeID{{1, 2, 3}})
	checkGroups(t, "Cliques(nil) after writing into a result", w.nw.Cliques(nil), [][]kernel.NodeID{{1, 2}, {2, 3}})
	if got := w.nw.QuorumHubs(nil, 1); !reflect.DeepEqual(got, ids{1, 2, 3}) {
		t.Errorf("QuorumHubs(nil, 1) after writing into a result = %v, want [1 2 3]", got)
	}
	names := []string{"a", "b", "c"}
	for _, x := range names {
		for _, y := range names {
			if l := "net/link/" + x + "/" + y; !untouched(w.s.Rand(l), 1, l) {
				t.Errorf("queries drew from %s", l)
			}
		}
	}
}

// NET-003, NET-027, NET-030, AT-NET-11 (QuorumHubs)
func TestQueryMisuse(t *testing.T) {
	w := newNet(1, simnet.DefaultConfig())
	mustPanic(t, "simnet: QuorumHubs: quorum must be >= 1, got 0", func() { w.nw.QuorumHubs(nil, 0) })
	mustPanic(t, "simnet: QuorumHubs: quorum must be >= 1, got -1", func() { w.nw.QuorumHubs(nil, -1) })
	mustPanic(t, "simnet: QuorumHubs: unknown node id 4", func() { w.nw.QuorumHubs(ids{4}, 1) })
	mustPanic(t, "simnet: Components: unknown node id 4", func() { w.nw.Components(ids{1, 4}) })
	mustPanic(t, "simnet: Cliques: unknown node id 0", func() { w.nw.Cliques(ids{0}) })
	// the first unknown ID in the caller's order; the quorum before the IDs, in NET-030's order
	mustPanic(t, "simnet: Components: unknown node id 5", func() { w.nw.Components(ids{5, 4}) })
	mustPanic(t, "simnet: QuorumHubs: quorum must be >= 1, got 0", func() { w.nw.QuorumHubs(ids{9}, 0) })
	// a down node keeps its edges
	w.c.Crash()
	checkGroups(t, "Components(nil) with c down", w.nw.Components(nil), [][]kernel.NodeID{{1, 2, 3}})
	checkGroups(t, "Cliques(nil) with c down", w.nw.Cliques(nil), [][]kernel.NodeID{{1, 2, 3}})
	if got := w.nw.QuorumHubs(nil, 3); !reflect.DeepEqual(got, ids{1, 2, 3}) {
		t.Errorf("QuorumHubs(nil, 3) with c down = %v, want [1 2 3]", got)
	}
}
