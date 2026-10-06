package simnet

import (
	"fmt"
	"slices"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/gograph"
	"github.com/hmdsefi/gograph/connectivity"
	"github.com/hmdsefi/gograph/partition"
)

// Components returns the groups of nodes in among that can reach each other through directed
// paths inside among (strongly connected components). nil or empty among means all nodes.
// Each group is sorted by NodeID; groups are sorted by size descending, then lexicographically.
func (nw *Network) Components(among []kernel.NodeID) [][]kernel.NodeID {
	nw.register()
	ids := nw.normalize("Components", among)
	g := gograph.New[kernel.NodeID](gograph.Directed())
	vs := make([]*gograph.Vertex[kernel.NodeID], len(ids))
	for k, id := range ids {
		vs[k] = g.AddVertexByLabel(id)
	}
	for x, a := range ids {
		for y, b := range ids {
			if a != b && nw.adj[a-1][b-1] {
				if _, err := g.AddEdge(vs[x], vs[y]); err != nil {
					panic("simnet: internal error: query edge: " + err.Error())
				}
			}
		}
	}
	return canonical(connectivity.Tarjan(g))
}

// Cliques returns the maximal groups of nodes in among in which every pair has links in both
// directions. Same input and output ordering rules as Components.
func (nw *Network) Cliques(among []kernel.NodeID) [][]kernel.NodeID {
	nw.register()
	u, _ := nw.bidirectional(nw.normalize("Cliques", among))
	return canonical(partition.MaximalCliques(u))
}

// QuorumHubs returns, sorted by NodeID, the nodes in among that have links in both directions
// with at least quorum-1 other nodes in among. It panics if quorum < 1.
func (nw *Network) QuorumHubs(among []kernel.NodeID, quorum int) []kernel.NodeID {
	nw.register()
	if quorum < 1 {
		panic(fmt.Sprintf("simnet: QuorumHubs: quorum must be >= 1, got %d", quorum))
	}
	ids := nw.normalize("QuorumHubs", among)
	_, vs := nw.bidirectional(ids)
	var out []kernel.NodeID
	for k, id := range ids {
		if vs[k].OutDegree() >= quorum-1 {
			out = append(out, id)
		}
	}
	return out
}

// normalize applies NET-027: empty means every registered node; otherwise validate, sort and
// remove duplicates.
func (nw *Network) normalize(method string, among []kernel.NodeID) []kernel.NodeID {
	if len(among) == 0 {
		ids := make([]kernel.NodeID, len(nw.nodes))
		for i := range ids {
			ids[i] = kernel.NodeID(i + 1)
		}
		return ids
	}
	for _, id := range among {
		nw.index(method, id)
	}
	ids := slices.Clone(among)
	slices.Sort(ids)
	return slices.Compact(ids)
}

// bidirectional builds the undirected graph of NET-029 on ids (ascending) and returns it with
// its vertices in ids order.
func (nw *Network) bidirectional(ids []kernel.NodeID) (gograph.Graph[kernel.NodeID], []*gograph.Vertex[kernel.NodeID]) {
	u := gograph.New[kernel.NodeID]()
	vs := make([]*gograph.Vertex[kernel.NodeID], len(ids))
	for k, id := range ids {
		vs[k] = u.AddVertexByLabel(id)
	}
	for x := range ids {
		for y := x + 1; y < len(ids); y++ {
			a, b := ids[x]-1, ids[y]-1
			if nw.adj[a][b] && nw.adj[b][a] {
				if _, err := u.AddEdge(vs[x], vs[y]); err != nil {
					panic("simnet: internal error: query edge: " + err.Error())
				}
			}
		}
	}
	return u, vs
}

// canonical converts gograph groups to sorted NodeID groups, sorted by size descending, then
// lexicographically (NET-031).
func canonical(groups [][]*gograph.Vertex[kernel.NodeID]) [][]kernel.NodeID {
	out := make([][]kernel.NodeID, 0, len(groups))
	for _, g := range groups {
		ids := make([]kernel.NodeID, len(g))
		for k, v := range g {
			ids[k] = v.Label()
		}
		slices.Sort(ids)
		out = append(out, ids)
	}
	slices.SortFunc(out, func(x, y []kernel.NodeID) int {
		if len(x) != len(y) {
			return len(y) - len(x)
		}
		return slices.Compare(x, y)
	})
	return out
}
