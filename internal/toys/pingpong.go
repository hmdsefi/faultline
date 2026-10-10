// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package toys

import (
	"strconv"

	"github.com/hmdsefi/faultline/kernel"
)

// Ping is the message a sends to b.
type Ping struct{ N int }

// Pong is b's answer to a Ping.
type Pong struct{ N int }

// Describe returns "ping <N>".
func (m Ping) Describe() string { return "ping " + strconv.Itoa(m.N) }

// Describe returns "pong <N>".
func (m Pong) Describe() string { return "pong " + strconv.Itoa(m.N) }

// PingPong adds nodes "a" and "b": a sends Ping{1}..Ping{rounds}, b answers each with a Pong, and
// a logs "done <rounds>" after the last Pong (DET-041). A run executes 2 + 2*rounds events.
func PingPong(s *kernel.Sim, t Transport, rounds int) (a, b *kernel.Node) {
	if rounds < 1 {
		panic("toys: rounds must be >= 1")
	}
	a = s.AddNode("a", func(n *kernel.Node) {
		t.Handle(n, func(from kernel.NodeID, msg any) {
			p := msg.(Pong) //nolint:errcheck // a gets only Pong, from b; another type is a bug and panics
			if p.N == rounds {
				n.Logf("done %d", p.N)
				return
			}
			t.Send(n, b.ID(), Ping{N: p.N + 1})
		})
		t.Send(n, b.ID(), Ping{N: 1})
	})
	b = s.AddNode("b", func(n *kernel.Node) {
		t.Handle(n, func(from kernel.NodeID, msg any) {
			t.Send(n, from, Pong{N: msg.(Ping).N}) //nolint:errcheck // b gets only Ping, from a; another type is a bug and panics
		})
	})
	return a, b
}
