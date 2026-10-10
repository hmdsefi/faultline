// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package kernel is the simulator under faultline: a single-threaded event loop with virtual
// time and a seeded scheduler. It also owns named random streams, nodes, and the trace of
// everything that happens.
//
// Most tests use the kernel through faultline.Run, which creates one Sim per run and passes it as
// World.Sim. Use the package directly to drive a simulation by hand:
//
//	s := kernel.New(kernel.Config{Seed: 7})
//	s.AddNode("n1", func(n *kernel.Node) {
//		n.After(time.Second, "tick", func() { n.Logf("tick") })
//	})
//	s.Run()
//
// Virtual time jumps from one event to the next and never waits. Events due at the same time run
// in an order drawn from the seed. So the same seed and code give the same run, record for
// record, and the same trace hash (Sim.TraceHash).
//
// A Node is one simulated machine. Its BootFunc runs at every boot and schedules the node's work
// with Node.After and Node.Post. Crash ends the current incarnation and drops its pending events,
// and Restart boots the next one. Each node has a local clock that can be offset, drift or jump.
//
// A Sim is not safe for concurrent use, and the kernel never starts a goroutine. Code that runs
// in a simulation must take time from Sim.Now or Node.Now, and randomness from Sim.Rand or
// Node.Rand. Then a run depends on nothing but its seed.
package kernel
