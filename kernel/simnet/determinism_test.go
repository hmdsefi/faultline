// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simnet_test

import (
	"encoding/binary"
	"slices"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/internal/golden"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// workloadConfig is the AT-NET-17 link configuration: DefaultConfig plus loss and duplication.
var workloadConfig = simnet.Config{Default: simnet.Link{Latency: ms, Jitter: 4 * ms, DropPPM: 100000, DupPPM: 50000}}

// workload builds and runs the AT-NET-17 scenario. Nodes a, b, c each send, every 1ms of local
// time, 32 random bytes from Node.Rand to one of the two other nodes chosen with Node.Rand.IntN.
// Partition({a}, {b, c}) at 50ms, crash b at 70ms, restart b at 90ms, Heal at 100ms; run to 200ms.
// observe, if not nil, is registered with OnEvent before the run.
func workload(seed uint64, trace kernel.TraceConfig, observe func(nw *simnet.Network)) (*kernel.Sim, *simnet.Network, kernel.StopReason) {
	s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
	nw := simnet.New(s, workloadConfig)
	for _, name := range []string{"a", "b", "c"} {
		s.AddNode(name, func(n *kernel.Node) {
			nw.Handle(n, func(kernel.NodeID, any) {})
			var tick func()
			tick = func() {
				buf := make([]byte, 32)
				for i := 0; i < 32; i += 8 {
					binary.LittleEndian.PutUint64(buf[i:], n.Rand().Uint64())
				}
				peer := kernel.NodeID(1 + (int(n.ID())+n.Rand().IntN(2))%3)
				nw.Send(n, peer, buf)
				n.After(ms, "tick", tick)
			}
			n.After(ms, "tick", tick)
		})
	}
	s.At(kernel.Time(50*ms), "partition", func() { nw.Partition([]kernel.NodeID{1}, []kernel.NodeID{2, 3}) })
	s.At(kernel.Time(70*ms), "crash", func() { s.Node(2).Crash() })
	s.At(kernel.Time(90*ms), "restart", func() { s.Node(2).Restart() })
	s.At(kernel.Time(100*ms), "heal", nw.Heal)
	if observe != nil {
		s.OnEvent(func() { observe(nw) })
	}
	return s, nw, s.RunUntil(kernel.Time(200 * ms))
}

// AT-NET-17
func TestDeterminism(t *testing.T) {
	s1, nw1, _ := workload(1, kernel.TraceConfig{}, nil)
	s2, nw2, _ := workload(1, kernel.TraceConfig{}, nil)
	if s1.TraceHash() != s2.TraceHash() || nw1.Stats() != nw2.Stats() {
		t.Fatalf("seed 1 twice: hashes %#x %#x, stats %+v %+v", s1.TraceHash(), s2.TraceHash(), nw1.Stats(), nw2.Stats())
	}
	st := nw1.Stats()
	if st.DroppedLoss == 0 || st.Duplicated == 0 || st.DroppedPartition == 0 || st.DroppedDown+st.DroppedNoHandler == 0 {
		t.Fatalf("workload does not exercise loss, duplication, partitions and crashes: %+v", st)
	}
	s3, _, _ := workload(2, kernel.TraceConfig{}, nil)
	if s3.TraceHash() == s1.TraceHash() {
		t.Fatalf("seeds 1 and 2 have the same hash %#x", s1.TraceHash())
	}
	golden.Check(t, 1, func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		s, _, stop := workload(seed, trace, nil)
		return s, stop
	})
}

// AT-NET-17 (stream independence): the a -> c deliveries do not depend on traffic on a -> b.
func TestStreamIndependence(t *testing.T) {
	run := func(withB bool) []delivery {
		w := newNet(1, workloadConfig)
		for k := 0; k < 10; k++ {
			w.at(time.Duration(k)*ms, func() {
				if withB {
					w.nw.Send(w.a, 2, k)
				}
				w.nw.Send(w.a, 3, k)
			})
		}
		w.s.Run()
		var toC []delivery
		for _, d := range w.got {
			if d.to == 3 {
				toC = append(toC, d)
			}
		}
		slices.SortFunc(toC, func(x, y delivery) int {
			if xp, yp := payloadInt(t, x), payloadInt(t, y); xp != yp {
				return xp - yp
			}
			return int(x.at - y.at)
		})
		return toC
	}
	a, b := run(true), run(false)
	if len(a) == 0 || !slices.Equal(a, b) {
		t.Fatalf("a -> c deliveries differ:\n with a->b: %+v\n without:   %+v", a, b)
	}
}

// identity checks NET-032 for the totals, every link, and the sum of the links.
func identity(nw *simnet.Network) string {
	ok := func(st simnet.Stats) bool {
		return st.Sent+st.Duplicated == st.Delivered+st.Dropped()+st.InFlight
	}
	total := nw.Stats()
	if !ok(total) {
		return "totals"
	}
	var sum simnet.Stats
	for x := kernel.NodeID(1); x <= 3; x++ {
		for y := kernel.NodeID(1); y <= 3; y++ {
			st := nw.LinkStats(x, y)
			if !ok(st) {
				return "link"
			}
			sum.Sent += st.Sent
			sum.Duplicated += st.Duplicated
			sum.Delivered += st.Delivered
			sum.Deferred += st.Deferred
			sum.DroppedPartition += st.DroppedPartition
			sum.DroppedLoss += st.DroppedLoss
			sum.DroppedInFlight += st.DroppedInFlight
			sum.DroppedDown += st.DroppedDown
			sum.DroppedNoHandler += st.DroppedNoHandler
			sum.InFlight += st.InFlight
		}
	}
	if sum != total {
		return "sum of links"
	}
	return ""
}

// AT-NET-18
func TestStatsIdentity(t *testing.T) {
	events := 0
	_, nw, _ := workload(1, kernel.TraceConfig{}, func(nw *simnet.Network) {
		events++
		if bad := identity(nw); bad != "" {
			t.Errorf("after event %d: identity fails for %s", events, bad)
		}
	})
	if bad := identity(nw); bad != "" {
		t.Fatalf("after the run: identity fails for %s", bad)
	}
	if events < 500 {
		t.Fatalf("observer ran %d times; the workload is too small", events)
	}
}
