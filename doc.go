// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package faultline runs deterministic simulation tests inside go test. Run turns one test into
// many runs of a simulated world, one per seed. Each run builds nodes on a simulated network and
// disk, injects faults, and checks the invariants the test registers. Every random choice comes
// from the seed, so a failing seed replays exactly, as often as it takes to find and fix the bug.
//
// A test passes Run its options and a function that builds one world. In the following test, a
// client pings a server every 100 ms while a fault planner crashes the server at random times.
// The final check asks for pongs after the faults stop:
//
//	func TestPing(t *testing.T) {
//		faultline.Run(t, faultline.Options{Seeds: 50}, pingWorld)
//	}
//
//	// pingWorld builds one world. Run calls it once for every run.
//	func pingWorld(w *faultline.World) {
//		srv := w.AddServer("n1", func(n *kernel.Node) {
//			w.Net.Handle(n, func(from kernel.NodeID, _ any) { w.Net.Send(n, from, "pong") })
//		})
//		var lastPong kernel.Time
//		w.AddClient("c1", func(n *kernel.Node) {
//			w.Net.Handle(n, func(kernel.NodeID, any) { lastPong = w.Sim.Now() })
//			var ping func()
//			ping = func() {
//				w.Net.Send(n, srv.ID(), "ping")
//				n.After(100*time.Millisecond, "ping", ping)
//			}
//			n.Post("ping", ping)
//		})
//		w.Plan(&fault.Random{MaxDown: 1, Rules: []fault.Rule{
//			{Kind: fault.KindCrash, Every: time.Second, MaxFor: 500 * time.Millisecond},
//		}})
//		w.Final("pongs after recovery", func() error {
//			if lastPong < w.RecoveryStart() {
//				return fmt.Errorf("last pong at %s, recovery started at %s", lastPong, w.RecoveryStart())
//			}
//			return nil
//		})
//	}
//
// Code inside the world runs as callbacks on one goroutine, in virtual time. It must take time
// from the kernel (Node.Now, Node.After) and randomness from World.Rand or Node.Rand, and it must
// not start goroutines. A node's BootFunc runs again at every restart, so a node rebuilds its
// state there, from its disk if it has one. Packages kernel, simnet, simdisk and fault describe
// the event loop, the network, the disks and the faults.
//
// Each seed runs as a subtest named seed=0x followed by 16 hex digits. By default the seeds come
// from the test's name (NameBase, DeriveSeed), so they are the same on every machine. When a seed
// fails, Run prints the failure and a command that replays that seed. With a server that stops
// answering after its first restart, TestPing fails like this:
//
//	--- FAIL: TestPing/seed=0x0fa36865fb584320 (0.02s)
//	    faultline: final check "pongs after recovery" failed after the run at t=60.000000000s
//	      last pong at 1.305834063s, recovery started at 45.000000000s
//	    replay:    FAULTLINE_SEED=0x0fa36865fb584320 go test -v -run '^TestPing$' example.com/ping
//
// The line after the replay command names the artifact directory, by default under
// faultline-<uid> in os.TempDir(). Its files include report.txt (the lines above), trace.jsonl
// (every event of the run), schedule.json (the faults) and timeline.html (a page that shows the
// run). Package artifact describes them all. The replay command runs the seed again and gets the
// same run, record for record. That holds as long as the code, the options and the Go version
// stay the same.
//
// To keep a seed as a regression test, set FAULTLINE_SEED in a test of its own:
//
//	func TestPingSeed0fa36865(t *testing.T) {
//		t.Setenv("FAULTLINE_SEED", "0x0fa36865fb584320")
//		t.Setenv("FAULTLINE_SEED_LIST", "") // FAULTLINE_SEED and FAULTLINE_SEED_LIST cannot both be set
//		faultline.Run(t, faultline.Options{Seeds: 50}, pingWorld)
//	}
//
// Run's documentation lists the environment variables that choose seeds, replay a schedule and
// turn on the determinism check. The guides and reference pages are at
// https://github.com/hmdsefi/faultline/tree/main/docs.
package faultline
