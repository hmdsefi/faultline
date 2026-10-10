// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package fault

import (
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
)

// AT-FLT-17
func TestBetweenVectors(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // fixed seed: the vectors below depend on it
	want := []time.Duration{884686635, 808218112, 892214001, 898298290, 617138135}
	for i, w := range want {
		if got := between(r, 500*time.Millisecond, time.Second); got != w {
			t.Fatalf("between call %d = %v, want %v", i+1, got, w)
		}
	}
	r = rand.New(rand.NewPCG(1, 2))                                                  //nolint:gosec // fixed seed: compared with a fresh copy below
	if between(r, 7, 7) != 7 || r.Uint64() == rand.New(rand.NewPCG(1, 2)).Uint64() { //nolint:gosec // a fresh copy of r's stream
		t.Fatalf("between(lo == hi) must still draw once")
	}
}

// AT-FLT-17, FLT-085
func TestSplit(t *testing.T) {
	for _, client := range []bool{false, true} {
		s := kernel.New(kernel.Config{Seed: 1})
		var servers []*kernel.Node
		for _, name := range []string{"n1", "n2", "n3", "n4", "n5"} {
			servers = append(servers, s.AddNode(name, func(*kernel.Node) {}, kernel.WithTags("server")))
		}
		if client {
			s.AddNode("c1", func(*kernel.Node) {}, kernel.WithTags("client"))
		}
		s.RunUntil(0)
		ctx := &PlanContext{Sim: s, Rand: rand.New(rand.NewPCG(1, 2)), Servers: servers} //nolint:gosec // fixed seed: the splits below depend on it
		faults, undos := split(ctx, servers)
		if !reflect.DeepEqual(undos, []Event{{Kind: KindHeal}}) || len(faults) != 1 || faults[0].Kind != KindPartition {
			t.Fatalf("split = %v, %v", faults, undos)
		}
		g := faults[0].Groups
		if !client {
			if !reflect.DeepEqual(g, [][]string{{"n1", "n3", "n4", "n5"}, {"n2"}}) {
				t.Fatalf("split on W5 = %v", g)
			}
			continue
		}
		if len(g) != 2 || (!slices.Contains(g[0], "c1") && !slices.Contains(g[1], "c1")) || len(g[0])+len(g[1]) != 6 {
			t.Fatalf("split on W5+c = %v", g)
		}
		// FLT-085's draws on a fresh copy of the stream: Shuffle, the size, then one IntN(2) for c1
		// (ID 6); groups sort by ID, and the group with the smallest ID comes first.
		ref := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // a fresh copy of ctx.Rand's stream
		perm := []kernel.NodeID{1, 2, 3, 4, 5}
		ref.Shuffle(len(perm), func(i, j int) { perm[i], perm[j] = perm[j], perm[i] })
		k := 1 + ref.IntN(len(perm)-1)
		g0, g1 := slices.Clone(perm[:k]), slices.Clone(perm[k:])
		if ref.IntN(2) == 0 {
			g0 = append(g0, 6)
		} else {
			g1 = append(g1, 6)
		}
		slices.Sort(g0)
		slices.Sort(g1)
		if g1[0] < g0[0] {
			g0, g1 = g1, g0
		}
		if want := [][]string{nodeNames(ctx, g0), nodeNames(ctx, g1)}; !reflect.DeepEqual(g, want) {
			t.Fatalf("split on W5+c = %v, want %v", g, want)
		}
	}
}
