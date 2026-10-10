// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simnet_test

import (
	"strconv"
	"testing"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// benchNet builds n nodes with no-op handlers on a TraceHash Sim and boots them.
func benchNet(n int, cfg simnet.Config) (*kernel.Sim, *simnet.Network, []*kernel.Node) {
	s := kernel.New(kernel.Config{Seed: 1})
	nw := simnet.New(s, cfg)
	nodes := make([]*kernel.Node, n)
	for i := range nodes {
		nodes[i] = s.AddNode("n"+strconv.Itoa(i+1), func(n *kernel.Node) {
			nw.Handle(n, func(kernel.NodeID, any) {})
		})
	}
	s.RunUntil(0)
	return s, nw, nodes
}

func benchSend(b *testing.B, cfg simnet.Config, isolate bool) {
	s, nw, nodes := benchNet(3, cfg)
	if isolate {
		nw.Isolate(2)
	}
	payload := make([]byte, 64)
	const warm = 100 // message IDs below 100 are formatted without allocating
	for i := 0; i < warm; i++ {
		nw.Send(nodes[0], 2, payload)
		s.Run()
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		nw.Send(nodes[0], 2, payload)
		s.Run()
	}
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "messages/s")
	// Every copy is delivered or dropped by the configured partition or loss.
	st := nw.Stats()
	if st.Sent != uint64(warm+b.N) || st.InFlight != 0 || st.DroppedInFlight+st.DroppedDown+st.DroppedNoHandler != 0 { //nolint:gosec // warm and b.N are positive
		b.Fatalf("Stats() = %+v after %d sends", st, warm+b.N)
	}
}

func BenchmarkSendDeliver(b *testing.B) { benchSend(b, simnet.DefaultConfig(), false) }

func BenchmarkSendDeliverFIFO(b *testing.B) {
	cfg := simnet.DefaultConfig()
	cfg.Default.FIFO = true
	benchSend(b, cfg, false)
}

func BenchmarkSendLossDup(b *testing.B) {
	cfg := simnet.DefaultConfig()
	cfg.Default.DropPPM, cfg.Default.DupPPM = 100000, 100000
	benchSend(b, cfg, false)
}

func BenchmarkSendPartitioned(b *testing.B) { benchSend(b, simnet.DefaultConfig(), true) }

// bridge5 returns a 5-node network with a bridge partition: {1, 2} and {4, 5} only reach each
// other through 3.
func bridge5() *simnet.Network {
	_, nw, _ := benchNet(5, simnet.DefaultConfig())
	for _, x := range []kernel.NodeID{1, 2} {
		for _, y := range []kernel.NodeID{4, 5} {
			nw.Cut(x, y)
			nw.Cut(y, x)
		}
	}
	return nw
}

func BenchmarkComponents5(b *testing.B) {
	nw := bridge5()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		nw.Components(nil)
	}
}

func BenchmarkCliques5(b *testing.B) {
	nw := bridge5()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		nw.Cliques(nil)
	}
}

func BenchmarkQuorumHubs5(b *testing.B) {
	nw := bridge5()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		nw.QuorumHubs(nil, 3)
	}
}

func BenchmarkPartitionHeal16(b *testing.B) {
	_, nw, _ := benchNet(16, simnet.DefaultConfig())
	g1 := []kernel.NodeID{1, 2, 3, 4, 5}
	g2 := []kernel.NodeID{6, 7, 8, 9, 10}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		nw.Partition(g1, g2)
		nw.Heal()
	}
}
