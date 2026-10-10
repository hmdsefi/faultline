// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package toys

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hmdsefi/faultline/kernel"
)

// GossipConfig configures Gossip.
type GossipConfig struct {
	Nodes    int           // >= 2
	Ticks    int           // >= 0, gossip rounds per incarnation
	Interval time.Duration // > 0, mean gap between ticks
}

// GossipMsg carries a node's view of every node's counter.
type GossipMsg struct{ Counts []uint64 }

// Describe returns "gossip [<c1> <c2> ...]".
func (m GossipMsg) Describe() string {
	parts := make([]string, len(m.Counts))
	for i, c := range m.Counts {
		parts[i] = strconv.FormatUint(c, 10)
	}
	return "gossip [" + strings.Join(parts, " ") + "]"
}

// Gossip adds nodes "g1" … "g<Nodes>" (tag "server") that increment their own counter on every
// tick and send their view to a random peer, which merges it with an element-wise max (DET-042).
// State is volatile: a restarted node starts from zero counts and gossips Ticks more times.
func Gossip(s *kernel.Sim, t Transport, cfg GossipConfig) []*kernel.Node {
	if cfg.Nodes < 2 || cfg.Ticks < 0 || cfg.Interval <= 0 {
		panic("toys: invalid GossipConfig")
	}
	nodes := make([]*kernel.Node, cfg.Nodes)
	for i := range nodes {
		self := i
		nodes[i] = s.AddNode("g"+strconv.Itoa(i+1), func(n *kernel.Node) {
			counts := make([]uint64, cfg.Nodes)
			done := 0
			t.Handle(n, func(_ kernel.NodeID, msg any) {
				m := msg.(GossipMsg) //nolint:errcheck // gossip nodes send only GossipMsg; another type is a bug and panics
				for j := range counts {
					counts[j] = max(counts[j], m.Counts[j])
				}
			})
			gap := func() time.Duration {
				return kernel.Uniform(n.Rand(), cfg.Interval/2, cfg.Interval*3/2)
			}
			var tick func()
			tick = func() {
				if done == cfg.Ticks {
					return
				}
				done++
				counts[self]++
				k := int(n.Rand().Uint64N(uint64(cfg.Nodes - 1))) //nolint:gosec // cfg.Nodes >= 2, checked above
				if k >= self {
					k++
				}
				t.Send(n, nodes[k].ID(), GossipMsg{Counts: slices.Clone(counts)})
				n.After(gap(), "tick", tick)
			}
			n.After(gap(), "tick", tick)
		}, kernel.WithTags("server"))
	}
	return nodes
}
