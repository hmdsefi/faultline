// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simnet

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/hmdsefi/faultline/kernel"
)

// Partition removes every edge between nodes of different groups. Nodes not listed in any group
// form one extra group, so one group that lists only some nodes splits the network in two. Edges
// already removed stay removed. It panics if a node is listed twice or an ID is unknown.
func (nw *Network) Partition(groups ...[]kernel.NodeID) {
	nw.register()
	n := len(nw.nodes)
	group := make([]int, n) // 0: unlisted group; k >= 1: k-th listed non-empty group
	var listed [][]kernel.NodeID
	for _, g := range groups {
		if len(g) == 0 {
			continue // NET-024: empty groups are ignored
		}
		listed = append(listed, g)
		for _, id := range g {
			i := nw.index("Partition", id)
			if group[i] != 0 {
				panic(fmt.Sprintf("simnet: Partition: node %s (id %d) is in more than one group", nw.nodes[i].name, id))
			}
			group[i] = len(listed)
		}
	}
	// NET-024: the listed groups and the unlisted group (index 0) partition the nodes, so no
	// group, or one group that lists every node, removes nothing.
	removed := 0
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			if i != j && group[i] != group[j] && nw.adj[i][j] {
				nw.removeEdge(i, j)
				removed++
			}
		}
	}
	if removed > 0 {
		nw.version++
	}
	parts := make([]string, 0, len(listed)+1)
	for k := 1; k <= len(listed); k++ {
		parts = append(parts, nw.groupNames(group, k))
	}
	if unlisted := nw.groupNames(group, 0); unlisted != "" {
		parts = append(parts, unlisted)
	}
	gs := strings.Join(parts, "|")
	nw.emit("net.partition", 0, "partition "+gs, attr("groups", gs), attr("removed", strconv.Itoa(removed)))
}

// groupNames joins with "," the names of the nodes in group k, by ascending ID.
func (nw *Network) groupNames(group []int, k int) string {
	var names []string
	for i, g := range group {
		if g == k {
			names = append(names, nw.nodes[i].name)
		}
	}
	return strings.Join(names, ",")
}

// Isolate removes every edge to and from id.
func (nw *Network) Isolate(id kernel.NodeID) {
	nw.register()
	k := nw.index("Isolate", id)
	removed := 0
	for i := range nw.nodes {
		for j := range nw.nodes {
			if i != j && (i == k || j == k) && nw.adj[i][j] {
				nw.removeEdge(i, j)
				removed++
			}
		}
	}
	if removed > 0 {
		nw.version++
	}
	name := nw.nodes[k].name
	nw.emit("net.isolate", 0, "isolate "+name, attr("node", name), attr("removed", strconv.Itoa(removed)))
}

// Cut removes the edge from → to (one direction only). It panics if from == to.
func (nw *Network) Cut(from, to kernel.NodeID) {
	nw.register()
	i, j := nw.pair("Cut", from, to)
	removed := 0
	if nw.adj[i][j] {
		nw.removeEdge(i, j)
		removed = 1
		nw.version++
	}
	f, t := nw.nodes[i].name, nw.nodes[j].name
	nw.emit("net.cut", 0, "cut "+f+" -> "+t, attr("from", f), attr("to", t), attr("removed", strconv.Itoa(removed)))
}

// Heal restores every edge (full mesh). Link overrides are not changed.
func (nw *Network) Heal() {
	nw.register()
	added := 0
	for i := range nw.nodes {
		for j := range nw.nodes {
			if i != j && !nw.adj[i][j] {
				nw.addEdge(i, j)
				added++
			}
		}
	}
	if added > 0 {
		nw.version++
	}
	nw.emit("net.heal", 0, "heal", attr("added", strconv.Itoa(added)))
}

// HealLink restores the edge from → to (one direction only). It panics if from == to.
func (nw *Network) HealLink(from, to kernel.NodeID) {
	nw.register()
	i, j := nw.pair("HealLink", from, to)
	added := 0
	if !nw.adj[i][j] {
		nw.addEdge(i, j)
		added = 1
		nw.version++
	}
	f, t := nw.nodes[i].name, nw.nodes[j].name
	nw.emit("net.heal_link", 0, "heal "+f+" -> "+t, attr("from", f), attr("to", t), attr("added", strconv.Itoa(added)))
}

// pair validates a directed link that must not be a self link (NET-025).
func (nw *Network) pair(method string, from, to kernel.NodeID) (int, int) {
	i := nw.index(method, from)
	j := nw.index(method, to)
	if i == j {
		panic(fmt.Sprintf("simnet: %s: from and to are the same node %s (id %d)", method, nw.nodes[i].name, from))
	}
	return i, j
}
