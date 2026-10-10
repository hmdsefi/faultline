// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
)

// A client pings a server every 100 ms while a fault planner crashes the server at random times.
// The final check asks for pongs after the faults stop. TestPing is an ordinary test function;
// this example compiles it but does not run it, since Run needs the *testing.T of a real test.
func ExampleRun() {
	// pingWorld builds one world. Run calls it once for every run of every seed.
	pingWorld := func(w *faultline.World) {
		// The server answers every ping. Its boot function runs again after each restart.
		srv := w.AddServer("n1", func(n *kernel.Node) {
			w.Net.Handle(n, func(from kernel.NodeID, _ any) { w.Net.Send(n, from, "pong") })
		})
		// The client sends a ping every 100 ms and notes when the last pong arrived.
		var lastPong kernel.Time
		w.AddClient("c1", func(n *kernel.Node) {
			w.Net.Handle(n, func(kernel.NodeID, any) { lastPong = w.Sim.Now() })
			var ping func()
			ping = func() {
				w.Net.Send(n, srv.ID(), "ping")
				n.After(100*time.Millisecond, "ping", ping)
			}
			n.Post("ping", ping)
		})
		// About once a second, crash the server for up to 500 ms.
		w.Plan(&fault.Random{MaxDown: 1, Rules: []fault.Rule{
			{Kind: fault.KindCrash, Every: time.Second, MaxFor: 500 * time.Millisecond},
		}})
		// After the run, check that pongs came back once the faults stopped.
		w.Final("pongs after recovery", func() error {
			if lastPong < w.RecoveryStart() {
				return fmt.Errorf("last pong at %s, recovery started at %s", lastPong, w.RecoveryStart())
			}
			return nil
		})
	}

	TestPing := func(t *testing.T) {
		faultline.Run(t, faultline.Options{Seeds: 50}, pingWorld)
	}
	_ = TestPing
}

// The seed list of a test depends only on its name, so the subtests of TestKV are the same on
// every machine. This prints the names of its first three seed subtests.
func ExampleDeriveSeed() {
	base := faultline.NameBase("TestKV")
	for i := range 3 {
		fmt.Printf("TestKV/seed=0x%016x\n", faultline.DeriveSeed(base, i))
	}
	// Output:
	// TestKV/seed=0x287372ab06f1482e
	// TestKV/seed=0xac1b1f508c3fd743
	// TestKV/seed=0x9cb5a1e8588ac16e
}
