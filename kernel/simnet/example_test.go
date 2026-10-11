// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simnet_test

import (
	"fmt"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// A client pings a server over links with a fixed 10 ms latency. The ping sent during the
// partition is dropped; after Heal, pings get through again.
func Example() {
	s := kernel.New(kernel.Config{Seed: 1})
	nw := simnet.New(s, simnet.Config{Default: simnet.Link{Latency: 10 * time.Millisecond}})
	server := s.AddNode("server", func(n *kernel.Node) {
		nw.Handle(n, func(from kernel.NodeID, msg any) {
			fmt.Println(s.Now(), "server got", msg)
			nw.Send(n, from, "pong")
		})
	})
	client := s.AddNode("client", func(n *kernel.Node) {
		nw.Handle(n, func(_ kernel.NodeID, msg any) { fmt.Println(s.Now(), "client got", msg) })
	})
	ping := func() { nw.Send(client, server.ID(), "ping") }

	s.At(kernel.Time(1*time.Second), "ping", ping)
	s.At(kernel.Time(2*time.Second), "partition", func() { nw.Partition([]kernel.NodeID{server.ID()}) })
	s.At(kernel.Time(3*time.Second), "ping", ping)
	s.At(kernel.Time(4*time.Second), "heal", nw.Heal)
	s.At(kernel.Time(5*time.Second), "ping", ping)
	s.Run()
	fmt.Println("dropped by the partition:", nw.Stats().DroppedPartition)
	// Output:
	// 1.010000000s server got ping
	// 1.020000000s client got pong
	// 5.010000000s server got ping
	// 5.020000000s client got pong
	// dropped by the partition: 1
}
