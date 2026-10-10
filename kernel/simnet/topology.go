// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simnet

import (
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/gograph"
)

// Connected reports whether the edge from -> to exists. Connected(n, n) is always true.
func (nw *Network) Connected(from, to kernel.NodeID) bool {
	nw.register()
	i := nw.index("Connected", from)
	j := nw.index("Connected", to)
	return i == j || nw.adj[i][j]
}

// Topology returns a new directed graph equal to the current topology. Changing it does not
// change the network.
func (nw *Network) Topology() gograph.Graph[kernel.NodeID] {
	nw.register()
	g := gograph.New[kernel.NodeID](gograph.Directed())
	vs := make([]*gograph.Vertex[kernel.NodeID], len(nw.nodes))
	for i := range nw.nodes {
		vs[i] = g.AddVertexByLabel(kernel.NodeID(i + 1))
	}
	for i := range nw.nodes {
		for j := range nw.nodes {
			if i != j && nw.graph.ContainsEdge(nw.vertices[i], nw.vertices[j]) {
				if _, err := g.AddEdge(vs[i], vs[j]); err != nil {
					panic("simnet: internal error: copy edge: " + err.Error())
				}
			}
		}
	}
	return g
}

// TopologyVersion returns a counter that increases whenever the set of edges or nodes changes.
func (nw *Network) TopologyVersion() uint64 {
	nw.register()
	return nw.version
}
