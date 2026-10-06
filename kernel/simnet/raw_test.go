package simnet_test

import (
	"slices"
	"testing"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// AT-NET-20
func TestSendRawAndDecide(t *testing.T) {
	w := newNet(1, simnet.Config{Default: simnet.Link{Latency: ms, Jitter: 4 * ms, DropPPM: simnet.MaxPPM}})
	if id := w.nw.SendRaw(w.a, 2, tmsg("r"), simnet.RawOptions{Delay: 7 * ms}); id != 1 {
		t.Fatalf("SendRaw returned %d, want 1", id)
	}
	w.nw.SendRaw(w.a, 3, 1, simnet.RawOptions{Delay: 5 * ms, FIFO: true})
	w.nw.SendRaw(w.a, 3, 2, simnet.RawOptions{Delay: ms, FIFO: true})
	w.s.Run()
	want := []delivery{
		{at: kernel.Time(5 * ms), to: 3, from: 1, payload: 1},
		{at: kernel.Time(5*ms) + 1, to: 3, from: 1, payload: 2},
		{at: kernel.Time(7 * ms), to: 2, from: 1, payload: tmsg("r")},
	}
	if len(w.got) != 3 || w.got[0] != want[0] || w.got[1] != want[1] || w.got[2] != want[2] {
		t.Fatalf("got %+v, want %+v", w.got, want)
	}
	raws := w.records("net.send_raw")
	if len(raws) != 3 {
		t.Fatalf("%d net.send_raw records, want 3", len(raws))
	}
	checkRecord(t, raws[0], 1, "send_raw #1 a -> b: r", "msg=1, from=a, to=b, payload=r, delay_ns=7000000, fifo=false")
	if !untouched(w.s.Rand("net/link/a/b"), 1, "net/link/a/b") {
		t.Fatalf("SendRaw drew from net/link/a/b")
	}

	w = newNet(9, simnet.Config{Default: simnet.Link{Latency: ms, Jitter: 4 * ms}})
	recs, st := len(w.s.Records()), w.nw.Stats()
	r := freshStream(9, "net/link/a/b")
	for i := 0; i < 50; i++ {
		if got, want := w.nw.Decide(1, 2), replayDecision(simnet.DefaultConfig().Default, r); got != want {
			t.Fatalf("Decide call %d = %+v, replay %+v", i, got, want)
		}
	}
	if len(w.s.Records()) != recs || w.nw.Stats() != st {
		t.Fatalf("Decide emitted records or changed counters")
	}
}

// NET-003, NET-010, NET-034, §7: SendRaw and Decide panic on misuse; SendRaw validates like
// Send, before the delay.
func TestSendRawMisuse(t *testing.T) {
	w := newNet(1, simnet.Config{})
	mustPanic(t, "simnet: SendRaw: delay -1ns out of range [0s, 24h0m0s]",
		func() { w.nw.SendRaw(w.a, 2, 1, simnet.RawOptions{Delay: -1}) })
	mustPanic(t, "simnet: SendRaw: delay 24h0m0.000000001s out of range [0s, 24h0m0s]",
		func() { w.nw.SendRaw(w.a, 2, 1, simnet.RawOptions{Delay: simnet.MaxDelay + 1}) })
	mustPanic(t, "simnet: SendRaw: unknown node id 7", func() { w.nw.SendRaw(w.a, 7, 1, simnet.RawOptions{}) })
	mustPanic(t, "simnet: Decide: unknown node id 7", func() { w.nw.Decide(7, 1) })
	mustPanic(t, "simnet: Decide: unknown node id 7", func() { w.nw.Decide(1, 7) })
	mustPanic(t, "simnet: SendRaw: unknown node id 9", func() { w.nw.SendRaw(w.a, 9, 1, simnet.RawOptions{Delay: -1}) })
	w.a.Pause()
	mustPanic(t, "simnet: SendRaw: node a (id 1) is paused",
		func() { w.nw.SendRaw(w.a, 2, 1, simnet.RawOptions{Delay: -1}) })
	w.a.Resume()
	w.a.Crash()
	mustPanic(t, "simnet: SendRaw: node a (id 1) is down", func() { w.nw.SendRaw(w.a, 2, 1, simnet.RawOptions{}) })
}

// NET-033: Decide emits no record, changes no counter and leaves the FIFO state alone, also when
// it decides a drop.
func TestDecideHasNoEffects(t *testing.T) {
	w := newNet(1, simnet.Config{Default: simnet.Link{Latency: ms, DropPPM: simnet.MaxPPM}})
	recs := len(w.s.Records())
	for i := 0; i < 3; i++ {
		if d := w.nw.Decide(1, 2); d != (simnet.Decision{Drop: true}) {
			t.Fatalf("Decide = %+v, want a drop", d)
		}
	}
	if n := len(w.s.Records()) - recs; n != 0 {
		t.Fatalf("Decide emitted %d records", n)
	}
	if st, ls := w.nw.Stats(), w.nw.LinkStats(1, 2); st != (simnet.Stats{}) || ls != (simnet.Stats{}) {
		t.Fatalf("after Decide: Stats %+v, LinkStats(1, 2) %+v, want zero", st, ls)
	}

	w = newNet(1, simnet.Config{Default: simnet.Link{Latency: 10 * ms, FIFO: true}})
	w.nw.Decide(1, 2)
	w.nw.Send(w.a, 2, 1)
	w.s.Run()
	if len(w.got) != 1 || w.got[0].at != kernel.Time(10*ms) {
		t.Fatalf("got %+v: Decide changed the FIFO state", w.got)
	}
}

// NET-011, NET-032, NET-034: rejected calls use no message ID; SendRaw counts Sent in the
// totals and the link, and a send-time partition drop is recorded and counted, not scheduled.
func TestSendRawCounts(t *testing.T) {
	w := newNet(1, simnet.Config{})
	mustPanic(t, "simnet: SendRaw: delay -1ns out of range [0s, 24h0m0s]",
		func() { w.nw.SendRaw(w.a, 2, 1, simnet.RawOptions{Delay: -1}) })
	mustPanic(t, "simnet: SendRaw: unknown node id 9", func() { w.nw.SendRaw(w.a, 9, 1, simnet.RawOptions{}) })
	if id := w.nw.SendRaw(w.a, 2, 1, simnet.RawOptions{Delay: simnet.MaxDelay, FIFO: true}); id != 1 {
		t.Fatalf("first accepted SendRaw returned %d, want 1", id)
	}
	one := simnet.Stats{Sent: 1, InFlight: 1}
	if st, ls := w.nw.Stats(), w.nw.LinkStats(1, 2); st != one || ls != one {
		t.Fatalf("after one SendRaw: Stats %+v, LinkStats(1, 2) %+v, want %+v", st, ls, one)
	}
	w.nw.Send(w.a, 3, 2)
	w.nw.Isolate(2)
	if id := w.nw.SendRaw(w.a, 2, 3, simnet.RawOptions{}); id != 3 {
		t.Fatalf("partitioned SendRaw returned %d, want 3", id)
	}
	if id := w.nw.SendRaw(w.a, 3, "x", simnet.RawOptions{}); id != 4 {
		t.Fatalf("fourth message: SendRaw returned %d, want 4", id)
	}
	raws := w.records("net.send_raw")
	if len(raws) != 3 {
		t.Fatalf("%d net.send_raw records, want 3", len(raws))
	}
	checkRecord(t, raws[0], 1, "send_raw #1 a -> b: 1",
		"msg=1, from=a, to=b, payload=1, delay_ns=86400000000000, fifo=true")
	checkRecord(t, raws[1], 1, "send_raw #3 a -> b: 3", "msg=3, from=a, to=b, payload=3, delay_ns=0, fifo=false")
	checkRecord(t, raws[2], 1, `send_raw #4 a -> c: "x"`, `msg=4, from=a, to=c, payload="x", delay_ns=0, fifo=false`)
	drops := w.records("net.drop")
	if len(drops) != 1 {
		t.Fatalf("%d net.drop records, want 1", len(drops))
	}
	checkRecord(t, drops[0], 1, "drop #3.1 a -> b: partition", "msg=3, copy=1, from=a, to=b, reason=partition")
	if ls, want := w.nw.LinkStats(1, 2), (simnet.Stats{Sent: 2, InFlight: 1, DroppedPartition: 1}); ls != want {
		t.Fatalf("LinkStats(1, 2) = %+v, want %+v", ls, want)
	}
	if st, want := w.nw.Stats(), (simnet.Stats{Sent: 4, InFlight: 3, DroppedPartition: 1}); st != want {
		t.Fatalf("Stats = %+v, want %+v", st, want)
	}
}

// NET-014, NET-034: the FIFO rule applies to SendRaw when the effective link is FIFO, and every
// SendRaw copy moves the link's latest scheduled time, FIFO or not.
func TestSendRawFIFO(t *testing.T) {
	w := newNet(1, simnet.Config{})
	w.nw.SetLink(1, 2, simnet.Link{FIFO: true})
	w.nw.SendRaw(w.a, 2, 1, simnet.RawOptions{Delay: 5 * ms})
	w.nw.SendRaw(w.a, 2, 2, simnet.RawOptions{Delay: ms})
	w.nw.Send(w.a, 2, 3)
	w.nw.SendRaw(w.a, 3, 4, simnet.RawOptions{Delay: 6 * ms})
	w.nw.SetLink(1, 3, simnet.Link{FIFO: true})
	w.nw.Send(w.a, 3, 5)
	w.s.Run()
	want := []delivery{
		{at: kernel.Time(5 * ms), to: 2, from: 1, payload: 1},
		{at: kernel.Time(5*ms) + 1, to: 2, from: 1, payload: 2},
		{at: kernel.Time(5*ms) + 2, to: 2, from: 1, payload: 3},
		{at: kernel.Time(6 * ms), to: 3, from: 1, payload: 4},
		{at: kernel.Time(6*ms) + 1, to: 3, from: 1, payload: 5},
	}
	if len(w.got) != len(want) {
		t.Fatalf("got %+v, want %+v", w.got, want)
	}
	for i := range want {
		if w.got[i] != want[i] {
			t.Fatalf("delivery %d = %+v, want %+v (all: %+v)", i, w.got[i], want[i], w.got)
		}
	}
}

// NET-034, NET-014, §8: a send-time partition drop follows its net.send_raw and leaves the FIFO
// state alone; each copy's delivery event is caused by its net.send_raw; the FIFO rule holds when
// the link and opt.FIFO both ask for it; the record's fifo is opt.FIFO.
func TestSendRawOrder(t *testing.T) {
	w := newNet(1, simnet.Config{})
	w.nw.SetLink(1, 2, simnet.Link{FIFO: true})
	w.nw.Isolate(2)
	w.nw.SendRaw(w.a, 2, 1, simnet.RawOptions{Delay: 9 * ms})
	w.nw.Heal()
	w.nw.SendRaw(w.a, 2, 2, simnet.RawOptions{Delay: 5 * ms})
	w.nw.SendRaw(w.a, 2, 3, simnet.RawOptions{Delay: ms, FIFO: true})
	w.s.Run()
	want := []delivery{
		{at: kernel.Time(5 * ms), to: 2, from: 1, payload: 2},
		{at: kernel.Time(5*ms) + 1, to: 2, from: 1, payload: 3},
	}
	if !slices.Equal(w.got, want) {
		t.Fatalf("got %+v, want %+v", w.got, want)
	}
	raws, drops, dels := w.records("net.send_raw"), w.records("net.drop"), w.records("net.deliver")
	if len(raws) != 3 || len(drops) != 1 || len(dels) != 2 {
		t.Fatalf("%d net.send_raw, %d net.drop, %d net.deliver records, want 3, 1, 2", len(raws), len(drops), len(dels))
	}
	if drops[0].Cause != raws[0].Seq {
		t.Fatalf("net.drop has cause %d, want its net.send_raw (seq %d)", drops[0].Cause, raws[0].Seq)
	}
	checkRecord(t, raws[1], 1, "send_raw #2 a -> b: 2", "msg=2, from=a, to=b, payload=2, delay_ns=5000000, fifo=false")
	bySeq := map[uint64]kernel.Record{}
	for _, r := range w.s.Records() {
		bySeq[r.Seq] = r
	}
	for k, d := range dels {
		if ev := bySeq[d.Cause]; ev.Kind != "kernel.event" || ev.Text != "net.deliver" || ev.Cause != raws[k+1].Seq {
			t.Fatalf("message %s ran in event %+v, want a net.deliver event caused by seq %d", attr(d, "msg"), ev, raws[k+1].Seq)
		}
	}
}

// NET-034, NET-016: SendRaw draws nothing even on a lossy, jittery link, a self SendRaw is not a
// partition drop, and the delivery latency counts from the send time.
func TestSendRawNoDraws(t *testing.T) {
	w := newNet(1, simnet.Config{Default: simnet.Link{Jitter: 4 * ms, DropPPM: 500000}})
	w.at(2*ms, func() {
		w.nw.SendRaw(w.a, 2, tmsg("x"), simnet.RawOptions{Delay: 3 * ms})
		w.nw.SendRaw(w.a, 1, tmsg("self"), simnet.RawOptions{Delay: ms})
	})
	w.s.Run()
	want := []delivery{
		{at: kernel.Time(3 * ms), to: 1, from: 1, payload: tmsg("self")},
		{at: kernel.Time(5 * ms), to: 2, from: 1, payload: tmsg("x")},
	}
	if len(w.got) != 2 || w.got[0] != want[0] || w.got[1] != want[1] {
		t.Fatalf("got %+v, want %+v", w.got, want)
	}
	dels := w.records("net.deliver")
	if len(dels) != 2 {
		t.Fatalf("%d net.deliver records, want 2", len(dels))
	}
	checkRecord(t, dels[0], 1, "deliver #2.1 a -> a", "msg=2, copy=1, from=a, to=a, latency_ns=1000000")
	checkRecord(t, dels[1], 2, "deliver #1.1 a -> b", "msg=1, copy=1, from=a, to=b, latency_ns=3000000")
	if !untouched(w.s.Rand("net/link/a/b"), 1, "net/link/a/b") {
		t.Fatalf("SendRaw drew from net/link/a/b")
	}
}

// NET-033: Decide draws from the link's effective configuration (its override) and does not
// check partitions.
func TestDecideOverride(t *testing.T) {
	w := newNet(9, simnet.Config{})
	l := simnet.Link{Latency: ms, Jitter: 4 * ms, TailPPM: 100000, Tail: 50 * ms, DropPPM: 300000, DupPPM: 200000}
	w.nw.SetLink(1, 2, l)
	w.nw.Isolate(2)
	r := freshStream(9, "net/link/a/b")
	for i := 0; i < 20; i++ {
		if got, want := w.nw.Decide(1, 2), replayDecision(l, r); got != want {
			t.Fatalf("Decide call %d = %+v, replay %+v", i, got, want)
		}
	}
}

// NET-002: Decide and SendRaw register nodes added since the network's last call.
func TestRawLateNodes(t *testing.T) {
	w := newNet(1, simnet.Config{})
	w.addNode("d")
	if got := w.nw.Decide(4, 1); got != (simnet.Decision{}) {
		t.Fatalf("Decide(4, 1) = %+v, want the zero Decision", got)
	}

	w = newNet(1, simnet.Config{})
	w.addNode("d")
	w.nw.SendRaw(w.a, 4, tmsg("x"), simnet.RawOptions{Delay: ms})
	w.s.Run()
	if raws := w.records("net.send_raw"); len(raws) != 1 || attr(raws[0], "to") != "d" {
		t.Fatalf("net.send_raw records %+v, want one to d", raws)
	}
	if want := (delivery{at: kernel.Time(ms), to: 4, from: 1, payload: tmsg("x")}); len(w.got) != 1 || w.got[0] != want {
		t.Fatalf("got %+v, want [%+v]", w.got, want)
	}
}
