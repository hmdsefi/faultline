// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simnet_test

import (
	"slices"
	"testing"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// AT-NET-04
func TestFIFO(t *testing.T) {
	w := newNet(2, simnet.Config{Default: simnet.Link{Latency: ms, Jitter: 4 * ms}})
	w.nw.SetLink(1, 2, simnet.Link{Latency: ms, Jitter: 4 * ms, FIFO: true})
	for i := 0; i < 1000; i++ {
		w.nw.Send(w.a, 2, i)
	}
	w.s.Run()
	if len(w.got) != 1000 {
		t.Fatalf("%d deliveries, want 1000", len(w.got))
	}
	for k, d := range w.got {
		if payloadInt(t, d) != k {
			t.Fatalf("delivery %d has payload %v: FIFO order broken", k, d.payload)
		}
		if k > 0 && d.at <= w.got[k-1].at {
			t.Fatalf("delivery times not strictly increasing at %d: %v after %v", k, d.at, w.got[k-1].at)
		}
	}
}

// AT-NET-24
func TestLinkOverrides(t *testing.T) {
	w := newNet(1, simnet.DefaultConfig())
	w.nw.Send(w.a, 2, tmsg("before")) // drawn with the default link
	w.nw.SetLink(1, 2, simnet.Link{Latency: 9 * ms})
	if w.nw.Link(1, 2).Latency != 9*ms {
		t.Fatalf("Link(1, 2) = %+v", w.nw.Link(1, 2))
	}
	if w.nw.Link(2, 1) != simnet.DefaultConfig().Default {
		t.Fatalf("Link(2, 1) = %+v, want the default", w.nw.Link(2, 1))
	}
	lc := w.records("net.link_config")
	if len(lc) != 1 {
		t.Fatalf("%d net.link_config records, want 1", len(lc))
	}
	checkRecord(t, lc[0], 0, "link a -> b: latency=9ms jitter=0s tail=0ppm/0s drop=0ppm dup=0ppm fifo=false",
		"from=a, to=b, latency_ns=9000000, jitter_ns=0, tail_ppm=0, tail_ns=0, drop_ppm=0, dup_ppm=0, fifo=false")
	w.nw.Send(w.a, 2, tmsg("after"))
	w.s.Run()
	if len(w.got) != 2 {
		t.Fatalf("%d deliveries, want 2", len(w.got))
	}
	before := replayDecision(simnet.DefaultConfig().Default, freshStream(1, "net/link/a/b"))
	for _, d := range w.got {
		switch d.payload {
		case tmsg("before"):
			if d.at != kernel.Time(before.Delay) {
				t.Fatalf("message sent before SetLink delivered at %v, want %v", d.at, before.Delay)
			}
		case tmsg("after"):
			if d.at != kernel.Time(9*ms) {
				t.Fatalf("message sent after SetLink delivered at %v, want 9ms", d.at)
			}
		}
	}
	w.nw.ResetLink(1, 2)
	if w.nw.Link(1, 2) != simnet.DefaultConfig().Default {
		t.Fatalf("after ResetLink: Link(1, 2) = %+v", w.nw.Link(1, 2))
	}
	lr := w.records("net.link_reset")
	if len(lr) != 1 {
		t.Fatalf("%d net.link_reset records, want 1", len(lr))
	}
	checkRecord(t, lr[0], 0, "link a -> b: reset to default", "from=a, to=b")
	w.nw.SetLink(1, 1, simnet.Link{Latency: 3 * ms}) // self links accept overrides
	if w.nw.Link(1, 1).Latency != 3*ms {
		t.Fatalf("self link override not applied")
	}
}

// NET-005, NET §8: SetLink stores the link and net.link_config carries every field, in the Text
// and as attributes.
func TestLinkConfigRecord(t *testing.T) {
	w := newNet(1, simnet.DefaultConfig())
	l := simnet.Link{Latency: 2 * ms, Jitter: 3 * ms, TailPPM: 4, Tail: 5 * ms, DropPPM: 6, DupPPM: 7, FIFO: true}
	w.nw.SetLink(1, 3, l)
	if w.nw.Link(1, 3) != l {
		t.Fatalf("Link(1, 3) = %+v, want %+v", w.nw.Link(1, 3), l)
	}
	lc := w.records("net.link_config")
	if len(lc) != 1 {
		t.Fatalf("%d net.link_config records, want 1", len(lc))
	}
	checkRecord(t, lc[0], 0, "link a -> c: latency=2ms jitter=3ms tail=4ppm/5ms drop=6ppm dup=7ppm fifo=true",
		"from=a, to=c, latency_ns=2000000, jitter_ns=3000000, tail_ppm=4, tail_ns=5000000, drop_ppm=6, dup_ppm=7, fifo=true")
}

// NET-005, NET-006: ResetLink without an override is a no-op that still emits net.link_reset,
// also on a link no message has used, and it does not reset the last delivery time.
func TestResetLink(t *testing.T) {
	w := newNet(1, simnet.Config{Default: simnet.Link{Latency: 50 * ms, FIFO: true}})
	w.nw.ResetLink(2, 1) // a link no message has used
	w.nw.Send(w.a, 2, 1) // 50ms
	w.nw.ResetLink(1, 2) // a used link without an override
	w.nw.Send(w.a, 2, 2) // FIFO: after message 1
	lr := w.records("net.link_reset")
	if len(lr) != 2 {
		t.Fatalf("%d net.link_reset records, want 2", len(lr))
	}
	checkRecord(t, lr[0], 0, "link b -> a: reset to default", "from=b, to=a")
	checkRecord(t, lr[1], 0, "link a -> b: reset to default", "from=a, to=b")
	w.s.Run()
	want := []delivery{
		{at: kernel.Time(50 * ms), to: 2, from: 1, payload: 1},
		{at: kernel.Time(50*ms) + 1, to: 2, from: 1, payload: 2},
	}
	if !slices.Equal(w.got, want) {
		t.Fatalf("got %+v, want %+v", w.got, want)
	}
}

// NET-005, NET-032: SetLink and ResetLink change only the override of the named directed link,
// and they keep that link's counters.
func TestOverridesAreScoped(t *testing.T) {
	def := simnet.Link{Latency: 10 * ms}
	w := newNet(1, simnet.Config{Default: def})
	ac, ba := simnet.Link{Latency: ms}, simnet.Link{Latency: 2 * ms}
	w.nw.SetLink(1, 3, ac)
	w.nw.SetLink(2, 1, ba)
	w.nw.Send(w.a, 2, 1) // in flight on a -> b
	st := w.nw.LinkStats(1, 2)
	others := func(when string) {
		t.Helper()
		if w.nw.Link(1, 1) != def || w.nw.Link(1, 3) != ac || w.nw.Link(2, 1) != ba {
			t.Fatalf("%s: Link(1, 1) = %+v, Link(1, 3) = %+v, Link(2, 1) = %+v", when,
				w.nw.Link(1, 1), w.nw.Link(1, 3), w.nw.Link(2, 1))
		}
		if got := w.nw.LinkStats(1, 2); got != st {
			t.Fatalf("%s: LinkStats(1, 2) = %+v, want %+v", when, got, st)
		}
	}
	w.nw.SetLink(1, 2, simnet.Link{Latency: 5 * ms})
	others("after SetLink(1, 2)")
	w.nw.ResetLink(1, 2)
	others("after ResetLink(1, 2)")
	w.s.Run()
	if want := []delivery{{at: kernel.Time(10 * ms), to: 2, from: 1, payload: 1}}; !slices.Equal(w.got, want) {
		t.Fatalf("got %+v, want %+v", w.got, want)
	}
}

// NET §6.1, NET-013: SetLink, ResetLink and Link draw nothing from the link's stream.
func TestOverridesDrawNothing(t *testing.T) {
	w := newNet(1, simnet.DefaultConfig())
	w.nw.SetLink(1, 3, simnet.Link{Latency: ms, Jitter: ms})
	w.nw.Link(1, 3)
	w.nw.ResetLink(1, 3)
	if !untouched(w.s, "net/link/a/c") {
		t.Fatalf("SetLink, ResetLink or Link drew from net/link/a/c")
	}
}

// NET-002: SetLink, ResetLink and Link each register a node added since the network's last call.
func TestOverridesRegisterLateNodes(t *testing.T) {
	w := newNet(1, simnet.DefaultConfig())
	w.addNode("d")
	w.nw.SetLink(4, 1, simnet.Link{Latency: ms})
	checkRecord(t, w.records("net.link_config")[0], 0, "link d -> a: latency=1ms jitter=0s tail=0ppm/0s drop=0ppm dup=0ppm fifo=false",
		"from=d, to=a, latency_ns=1000000, jitter_ns=0, tail_ppm=0, tail_ns=0, drop_ppm=0, dup_ppm=0, fifo=false")
	w.addNode("e")
	w.nw.ResetLink(1, 5)
	checkRecord(t, w.records("net.link_reset")[0], 0, "link a -> e: reset to default", "from=a, to=e")
	w.addNode("f")
	if got := w.nw.Link(6, 6); got != simnet.DefaultConfig().Default {
		t.Fatalf("Link(6, 6) = %+v, want the default", got)
	}
}

// NET-006, NET-014: SetLink does not reset the last delivery time, so switching a link to FIFO
// orders new copies after every copy already scheduled on it. The last delivery time is the
// latest one scheduled, not the most recent: message 2 is scheduled after message 1 but arrives
// before it.
func TestSwitchToFIFO(t *testing.T) {
	w := newNet(1, simnet.Config{Default: simnet.Link{Latency: 50 * ms}})
	w.nw.Send(w.a, 2, 1)
	w.nw.SetLink(1, 2, simnet.Link{Latency: 10 * ms})
	w.nw.Send(w.a, 2, 2)
	w.nw.SetLink(1, 2, simnet.Link{Latency: ms, FIFO: true})
	w.nw.Send(w.a, 2, 3)
	w.s.Run()
	want := []delivery{
		{at: kernel.Time(10 * ms), to: 2, from: 1, payload: 2},
		{at: kernel.Time(50 * ms), to: 2, from: 1, payload: 1},
		{at: kernel.Time(50*ms) + 1, to: 2, from: 1, payload: 3},
	}
	if !slices.Equal(w.got, want) {
		t.Fatalf("got %+v, want %+v", w.got, want)
	}
}

// AT-NET-11 (SetLink), NET-003, NET-005: a rejected SetLink leaves the earlier override in place,
// no rejected call emits a record, and the IDs are checked before the link.
func TestOverrideMisuse(t *testing.T) {
	w := newNet(1, simnet.DefaultConfig())
	l := simnet.Link{Latency: 2 * ms}
	w.nw.SetLink(1, 2, l)
	mustPanic(t, "simnet: SetLink a -> b: invalid link: Latency -1ns out of range [0s, 24h0m0s]",
		func() { w.nw.SetLink(1, 2, simnet.Link{Latency: -1}) })
	if w.nw.Link(1, 2) != l {
		t.Fatalf("a rejected SetLink changed the override: Link(1, 2) = %+v", w.nw.Link(1, 2))
	}
	mustPanic(t, "simnet: SetLink: unknown node id 9", func() { w.nw.SetLink(1, 9, simnet.Link{}) })
	mustPanic(t, "simnet: SetLink: unknown node id 9", func() { w.nw.SetLink(9, 1, simnet.Link{Latency: -1}) })
	mustPanic(t, "simnet: ResetLink: unknown node id 0", func() { w.nw.ResetLink(0, 1) })
	mustPanic(t, "simnet: ResetLink: unknown node id 0", func() { w.nw.ResetLink(1, 0) })
	mustPanic(t, "simnet: Link: unknown node id 4", func() { w.nw.Link(4, 1) })
	mustPanic(t, "simnet: Link: unknown node id 4", func() { w.nw.Link(1, 4) })
	if len(w.records("net.link_config")) != 1 || len(w.records("net.link_reset")) != 0 {
		t.Fatalf("a rejected call emitted a record")
	}
}
