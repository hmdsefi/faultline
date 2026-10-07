package simnet_test

import (
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

const ms = time.Millisecond

// AT-NET-02
func TestBasicDelivery(t *testing.T) {
	w := newNet(1, simnet.Config{Default: simnet.Link{Latency: 5 * ms}})
	w.nw.Send(w.a, w.b.ID(), tmsg("hello"))
	w.s.Run()
	want := []delivery{{at: kernel.Time(5 * ms), to: 2, from: 1, payload: tmsg("hello")}}
	if !slices.Equal(w.got, want) {
		t.Fatalf("got %+v, want %+v", w.got, want)
	}
	sends := w.records("net.send")
	if len(sends) != 1 {
		t.Fatalf("%d net.send records, want 1", len(sends))
	}
	checkRecord(t, sends[0], 1, "send #1 a -> b: hello", "msg=1, from=a, to=b, payload=hello")
	dels := w.records("net.deliver")
	if len(dels) != 1 || dels[0].At != kernel.Time(5*ms) {
		t.Fatalf("net.deliver records %+v, want one at 5ms", dels)
	}
	checkRecord(t, dels[0], 2, "deliver #1.1 a -> b", "msg=1, copy=1, from=a, to=b, latency_ns=5000000")
	if st := w.nw.Stats(); st != (simnet.Stats{Sent: 1, Delivered: 1}) {
		t.Fatalf("Stats() = %+v, want {Sent:1 Delivered:1}", st)
	}
	// KRN causality: the delivery event is caused by the net.send record (NET-012)
	for _, r := range w.s.Records() {
		if r.Kind == "kernel.event" && r.Text == "net.deliver" && r.Cause != sends[0].Seq {
			t.Fatalf("delivery event cause %d, want net.send seq %d", r.Cause, sends[0].Seq)
		}
	}
}

// NET-012, §8, KRN causality: copy 2 is recorded by net.dup on the sender, and its delivery event
// is caused by net.dup, while copy 1's is caused by net.send. Both copies are due at 1ms, so the
// seed decides which event runs first; each net.deliver record is matched to its own event.
func TestDuplicateCopy(t *testing.T) {
	for seed := uint64(1); seed <= 3; seed++ {
		w := newNet(seed, simnet.Config{Default: simnet.Link{Latency: ms, DupPPM: simnet.MaxPPM}})
		w.nw.Send(w.a, 2, tmsg("d"))
		w.s.Run()
		sends, dups := w.records("net.send"), w.records("net.dup")
		if len(sends) != 1 || len(dups) != 1 {
			t.Fatalf("seed %d: %d net.send and %d net.dup records, want 1 and 1", seed, len(sends), len(dups))
		}
		checkRecord(t, dups[0], 1, "dup #1 a -> b", "msg=1, copy=2, from=a, to=b")
		bySeq := map[uint64]kernel.Record{}
		for _, r := range w.s.Records() {
			bySeq[r.Seq] = r
		}
		cause := map[string]uint64{"1": sends[0].Seq, "2": dups[0].Seq}
		dels := w.records("net.deliver")
		if len(dels) != 2 || attr(dels[0], "copy") == attr(dels[1], "copy") {
			t.Fatalf("seed %d: net.deliver records %+v, want one per copy", seed, dels)
		}
		for _, d := range dels {
			cp := attr(d, "copy")
			if ev := bySeq[d.Cause]; ev.Kind != "kernel.event" || ev.Text != "net.deliver" || ev.Cause != cause[cp] {
				t.Fatalf("seed %d: copy %s ran in event %+v, want a net.deliver event caused by seq %d", seed, cp, ev, cause[cp])
			}
			if cp == "2" {
				checkRecord(t, d, 2, "deliver #1.2 a -> b", "msg=1, copy=2, from=a, to=b, latency_ns=1000000")
			}
		}
		want := simnet.Stats{Sent: 1, Duplicated: 1, Delivered: 2}
		if st, ls := w.nw.Stats(), w.nw.LinkStats(1, 2); st != want || ls != want {
			t.Fatalf("seed %d: Stats() = %+v, LinkStats(1, 2) = %+v; want %+v", seed, st, ls, want)
		}
	}
}

// NET-015: a zero delay delivers in a later event at the same virtual time, never inside Send.
func TestZeroDelay(t *testing.T) {
	w := newNet(1, simnet.Config{})
	w.nw.Send(w.a, 2, tmsg("now"))
	if len(w.got) != 0 || w.nw.Stats().InFlight != 1 {
		t.Fatalf("after Send: got %+v, InFlight %d; want nothing delivered, 1 in flight", w.got, w.nw.Stats().InFlight)
	}
	w.s.Run()
	want := []delivery{{at: 0, to: 2, from: 1, payload: tmsg("now")}}
	if !slices.Equal(w.got, want) {
		t.Fatalf("got %+v, want %+v", w.got, want)
	}
}

// NET-014: on a FIFO link every copy, copy 2 of a message included, is delivered after the copies
// scheduled before it.
func TestFIFOWithDuplicates(t *testing.T) {
	w := newNet(3, simnet.Config{Default: simnet.Link{Latency: ms, Jitter: 4 * ms, DupPPM: simnet.MaxPPM, FIFO: true}})
	for i := 0; i < 50; i++ {
		w.nw.Send(w.a, 2, i)
	}
	w.s.Run()
	dels := w.records("net.deliver")
	if len(dels) != 100 {
		t.Fatalf("%d net.deliver records, want 100", len(dels))
	}
	for k, r := range dels {
		want := strconv.Itoa(k/2+1) + "." + strconv.Itoa(k%2+1)
		if got := attr(r, "msg") + "." + attr(r, "copy"); got != want || k > 0 && r.At <= dels[k-1].At {
			t.Fatalf("delivery %d is copy %s at %v, want copy %s after the previous one", k, got, r.At, want)
		}
	}
}

// NET-017: payloads are passed by reference, never copied.
func TestPayloadByReference(t *testing.T) {
	w := newNet(1, simnet.Config{Default: simnet.Link{Latency: ms}})
	buf := []byte("payload")
	w.nw.Send(w.a, 2, buf)
	w.s.Run()
	if len(w.got) != 1 {
		t.Fatalf("%d deliveries, want 1", len(w.got))
	}
	if p, ok := w.got[0].payload.([]byte); !ok || len(p) != len(buf) || &p[0] != &buf[0] {
		t.Fatalf("handler got %#v, not the sent slice", w.got[0].payload)
	}
}

// AT-NET-03
func TestJitterBounds(t *testing.T) {
	w := newNet(2, simnet.Config{Default: simnet.Link{Latency: ms, Jitter: 4 * ms}})
	for i := 0; i < 1000; i++ {
		w.nw.Send(w.a, 2, i)
	}
	w.s.Run()
	if len(w.got) != 1000 {
		t.Fatalf("%d deliveries, want 1000", len(w.got))
	}
	reordered := false
	for k, d := range w.got {
		if d.at < kernel.Time(ms) || d.at > kernel.Time(5*ms) {
			t.Fatalf("delivery at %v outside [1ms, 5ms]", d.at)
		}
		if payloadInt(t, d) != k {
			reordered = true
		}
	}
	if !reordered {
		t.Fatalf("jitter did not reorder any message")
	}
}

// AT-NET-05
func TestExactDrawOrder(t *testing.T) {
	l := simnet.Link{Latency: ms, Jitter: 4 * ms, TailPPM: 100000, Tail: 50 * ms, DropPPM: 300000, DupPPM: 200000}
	w := newNet(7, simnet.Config{Default: l})
	for i := 0; i < 200; i++ {
		w.at(time.Duration(i)*100*ms, func() { w.nw.Send(w.a, 2, i) })
	}
	w.s.Run()
	dropped := map[string]string{}
	latency := map[string]string{} // "<msg>.<copy>" -> latency_ns
	for _, r := range w.records("net.drop") {
		dropped[attr(r, "msg")] = attr(r, "reason")
	}
	for _, r := range w.records("net.deliver") {
		latency[attr(r, "msg")+"."+attr(r, "copy")] = attr(r, "latency_ns")
	}
	r := freshStream(7, "net/link/a/b")
	var drops, dups int
	for i := 0; i < 200; i++ {
		d := replayDecision(l, r)
		msg := strconv.Itoa(i + 1)
		if d.Drop {
			drops++
			if dropped[msg] != "loss" {
				t.Fatalf("message %s: replay drops, trace has reason %q", msg, dropped[msg])
			}
			continue
		}
		if got := latency[msg+".1"]; got != ns(d.Delay) {
			t.Fatalf("message %s copy 1: latency %s, replay %v", msg, got, d.Delay)
		}
		got2, has2 := latency[msg+".2"]
		if has2 != d.Dup || d.Dup && got2 != ns(d.DupDelay) {
			t.Fatalf("message %s copy 2: trace %q (present %v), replay %+v", msg, got2, has2, d)
		}
		if d.Dup {
			dups++
		}
	}
	if drops == 0 || dups == 0 {
		t.Fatalf("drops=%d dups=%d: the test must exercise both", drops, dups)
	}
}

// AT-NET-06
func TestLossAndDuplicationAtTheEdges(t *testing.T) {
	w := newNet(1, simnet.Config{Default: simnet.Link{DropPPM: simnet.MaxPPM}})
	for i := 0; i < 100; i++ {
		w.nw.Send(w.a, 2, i)
	}
	w.s.Run()
	drops := w.records("net.drop")
	if len(drops) != 100 || len(w.got) != 0 || w.nw.Stats().DroppedLoss != 100 {
		t.Fatalf("(a) drops=%d got=%d DroppedLoss=%d", len(drops), len(w.got), w.nw.Stats().DroppedLoss)
	}
	for _, r := range drops {
		if attr(r, "reason") != "loss" || r.Node != 1 {
			t.Fatalf("(a) drop record %+v", r)
		}
	}
	if !untouched(w.s, "net/link/a/b") {
		t.Fatalf("(c) DropPPM = MaxPPM drew from the link stream")
	}

	w = newNet(1, simnet.Config{Default: simnet.Link{Latency: ms, DupPPM: simnet.MaxPPM}})
	for i := 0; i < 100; i++ {
		w.nw.Send(w.a, 2, i)
	}
	w.s.Run()
	copies := map[string]int{}
	for _, r := range w.records("net.deliver") {
		copies[attr(r, "msg")] += map[string]int{"1": 1, "2": 2}[attr(r, "copy")]
	}
	if len(w.got) != 200 || len(copies) != 100 || w.nw.Stats().Duplicated != 100 {
		t.Fatalf("(b) got=%d messages=%d Duplicated=%d", len(w.got), len(copies), w.nw.Stats().Duplicated)
	}
	for msg, sum := range copies {
		if sum != 3 {
			t.Fatalf("(b) message %s: copies 1 and 2 not both delivered", msg)
		}
	}
	if !untouched(w.s, "net/link/a/b") {
		t.Fatalf("(c) DupPPM = MaxPPM drew from the link stream")
	}
}

// AT-NET-07
func TestLossRate(t *testing.T) {
	w := newNet(3, simnet.Config{Default: simnet.Link{DropPPM: 500000}})
	for i := 0; i < 10000; i++ {
		w.nw.Send(w.a, 2, i)
	}
	w.s.Run()
	st := w.nw.Stats()
	if st.DroppedLoss < 4800 || st.DroppedLoss > 5200 {
		t.Fatalf("DroppedLoss = %d, want [4800, 5200]", st.DroppedLoss)
	}
	if st.Delivered != 10000-st.DroppedLoss {
		t.Fatalf("Delivered = %d, want %d", st.Delivered, 10000-st.DroppedLoss)
	}
}

// AT-NET-08
func TestPartitionAtSendAndInFlight(t *testing.T) {
	cfg := simnet.Config{Default: simnet.Link{Latency: 10 * ms}}
	w := newNet(1, cfg)
	w.nw.Partition([]kernel.NodeID{1}, []kernel.NodeID{2, 3})
	w.nw.Send(w.a, 2, tmsg("p"))
	d := w.records("net.drop")
	if len(d) != 1 {
		t.Fatalf("(a) %d drops, want 1", len(d))
	}
	checkRecord(t, d[0], 1, "drop #1.1 a -> b: partition", "msg=1, copy=1, from=a, to=b, reason=partition")
	if sends := w.records("net.send"); len(sends) != 1 || attr(sends[0], "msg") != "1" || sends[0].Seq >= d[0].Seq {
		t.Fatalf("(a) net.send records %+v, want msg 1 before the drop", sends)
	}

	w.nw.Heal()
	w.nw.Send(w.a, 2, tmsg("q"))
	w.at(5*ms, func() { w.nw.Cut(1, 2) })
	w.s.Run()
	d = w.records("net.drop")
	if len(d) != 2 || d[1].At != kernel.Time(10*ms) {
		t.Fatalf("(b) drops %+v, want a second drop at 10ms", d)
	}
	checkRecord(t, d[1], 2, "drop #2.1 a -> b: partition-in-flight", "msg=2, copy=1, from=a, to=b, reason=partition-in-flight")
	if w.nw.Stats().DroppedInFlight != 1 || len(w.got) != 0 {
		t.Fatalf("(b) stats %+v got %v", w.nw.Stats(), w.got)
	}

	cfg.NoDeliveryCheck = true
	w = newNet(1, cfg)
	w.nw.Send(w.a, 2, tmsg("q"))
	w.at(5*ms, func() { w.nw.Cut(1, 2) })
	w.s.Run()
	if len(w.got) != 1 || w.got[0].at != kernel.Time(10*ms) {
		t.Fatalf("(c) got %+v, want one delivery at 10ms", w.got)
	}

	// a send-time partition drop draws nothing, even on a link with jitter and loss (§6.1)
	w = newNet(1, simnet.Config{Default: simnet.Link{Latency: ms, Jitter: 4 * ms, DropPPM: 500000}})
	w.nw.Cut(1, 2)
	w.nw.Send(w.a, 2, tmsg("p"))
	if d := w.records("net.drop"); len(d) != 1 || attr(d[0], "reason") != "partition" {
		t.Fatalf("(d) drops %+v, want one partition drop", d)
	}
	if !untouched(w.s, "net/link/a/b") {
		t.Fatalf("(d) a partition drop drew from the link stream")
	}
}

// NET-018: the partition-in-flight check runs before the destination's state is looked at.
func TestInFlightCheckComesFirst(t *testing.T) {
	for _, stop := range []string{"pause", "crash"} {
		w := newNet(1, simnet.Config{Default: simnet.Link{Latency: 10 * ms}})
		w.nw.Send(w.a, 2, tmsg("x"))
		w.at(5*ms, func() {
			w.nw.Cut(1, 2)
			if stop == "pause" {
				w.b.Pause()
			} else {
				w.b.Crash()
			}
		})
		w.s.Run()
		d := w.records("net.drop")
		if len(d) != 1 || d[0].At != kernel.Time(10*ms) || d[0].Node != 2 || attr(d[0], "reason") != "partition-in-flight" {
			t.Fatalf("%s: drops %+v, want one partition-in-flight drop at 10ms on node 2", stop, d)
		}
		if n := len(w.records("net.defer")); n != 0 {
			t.Fatalf("%s: %d net.defer records, want 0", stop, n)
		}
	}
}

// AT-NET-09
func TestDownDestinationAndRestart(t *testing.T) {
	w := newNet(1, simnet.Config{Default: simnet.Link{Latency: 10 * ms}})
	w.at(0, func() { w.nw.Send(w.a, 2, tmsg("m1")) })
	w.at(1*ms, w.b.Crash)
	w.at(20*ms, func() { w.nw.Send(w.a, 2, tmsg("m2")) })
	w.at(21*ms, func() { w.install["b"] = false; w.b.Restart() })
	w.at(40*ms, func() { w.nw.Send(w.a, 2, tmsg("m3")) })
	w.at(41*ms, func() { w.install["b"] = true; w.b.Crash(); w.b.Restart() })
	w.s.Run()
	d := w.records("net.drop")
	if len(d) != 2 {
		t.Fatalf("%d drops, want 2", len(d))
	}
	if d[0].At != kernel.Time(10*ms) || attr(d[0], "reason") != "down" || d[0].Node != 2 {
		t.Fatalf("first drop %+v, want reason down at 10ms on node 2", d[0])
	}
	if d[1].At != kernel.Time(30*ms) || attr(d[1], "reason") != "no-handler" || d[1].Node != 2 {
		t.Fatalf("second drop %+v, want reason no-handler at 30ms on node 2", d[1])
	}
	dels := w.records("net.deliver")
	if len(dels) != 1 || dels[0].At != kernel.Time(50*ms) || dels[0].Inc != w.b.Incarnation() || w.b.Incarnation() != 3 {
		t.Fatalf("deliveries %+v, want one at 50ms to incarnation %d", dels, w.b.Incarnation())
	}
	if len(w.got) != 1 || w.got[0].payload != tmsg("m3") {
		t.Fatalf("got %+v", w.got)
	}
}

// NET-021: copies sent by a node that later crashes are still delivered.
func TestSenderCrash(t *testing.T) {
	w := newNet(1, simnet.Config{Default: simnet.Link{Latency: 10 * ms}})
	w.nw.Send(w.a, 2, tmsg("in flight"))
	w.at(ms, w.a.Crash)
	w.s.Run()
	if len(w.got) != 1 || w.got[0].at != kernel.Time(10*ms) || w.got[0].from != 1 {
		t.Fatalf("got %+v, want one delivery at 10ms from a", w.got)
	}
}

// AT-NET-10
func TestPausedDestination(t *testing.T) {
	cfg := simnet.Config{Default: simnet.Link{Latency: 10 * ms, FIFO: true}}
	w := newNet(1, cfg)
	w.b.Pause()
	for i := 0; i < 3; i++ {
		w.nw.Send(w.a, 2, i)
	}
	w.s.RunUntil(kernel.Time(20 * ms))
	defers := w.records("net.defer")
	if len(defers) != 3 || len(w.got) != 0 {
		t.Fatalf("%d net.defer records, %d deliveries; want 3, 0", len(defers), len(w.got))
	}
	for k, r := range defers {
		if r.At != kernel.Time(10*ms)+kernel.Time(k) {
			t.Fatalf("net.defer %d at %v, want 10ms+%dns", k, r.At, k)
		}
	}
	checkRecord(t, defers[0], 2, "defer #1.1 a -> b: b paused", "msg=1, copy=1, from=a, to=b")
	w.at(50*ms, w.b.Resume)
	w.s.Run()
	if len(w.got) != 3 {
		t.Fatalf("%d deliveries after resume, want 3", len(w.got))
	}
	for k, d := range w.got {
		if d.payload != k || d.at != kernel.Time(50*ms) {
			t.Fatalf("delivery %d = %+v, want payload %d at 50ms", k, d, k)
		}
	}
	if got := attr(w.records("net.deliver")[0], "latency_ns"); got != "50000000" {
		t.Fatalf("first latency_ns = %s, want 50000000", got)
	}
	if st := w.nw.Stats(); st.Deferred != 3 || st.Delivered != 3 || st.InFlight != 0 {
		t.Fatalf("Stats() = %+v", st)
	}

	w = newNet(1, cfg)
	w.b.Pause()
	for i := 0; i < 3; i++ {
		w.nw.Send(w.a, 2, i)
	}
	w.at(30*ms, w.b.Crash)
	w.at(40*ms, w.b.Restart)
	w.s.Run()
	d := w.records("net.drop")
	if len(d) != 3 || len(w.got) != 0 {
		t.Fatalf("crash variant: %d drops, %d deliveries; want 3, 0", len(d), len(w.got))
	}
	for k, r := range d {
		if r.At != kernel.Time(30*ms) || r.Node != 2 || attr(r, "reason") != "down" || attr(r, "msg") != strconv.Itoa(k+1) {
			t.Fatalf("crash variant drop %d = %+v", k, r)
		}
	}
	want := simnet.Stats{Sent: 3, DroppedDown: 3, Deferred: 3}
	if st, ls := w.nw.Stats(), w.nw.LinkStats(1, 2); st != want || ls != want {
		t.Fatalf("crash variant: Stats() = %+v, LinkStats(1, 2) = %+v; want %+v", st, ls, want)
	}
}

// NET-019: a parked copy has already arrived, so it is delivered after resume even if the link
// was cut while it waited.
func TestParkedCopyIgnoresLaterCuts(t *testing.T) {
	w := newNet(1, simnet.Config{Default: simnet.Link{Latency: 10 * ms}})
	w.b.Pause()
	w.nw.Send(w.a, 2, tmsg("x"))
	w.at(20*ms, func() { w.nw.Cut(1, 2) })
	w.at(30*ms, w.b.Resume)
	w.s.Run()
	want := []delivery{{at: kernel.Time(30 * ms), to: 2, from: 1, payload: tmsg("x")}}
	if !slices.Equal(w.got, want) || len(w.records("net.drop")) != 0 {
		t.Fatalf("got %+v, drops %+v; want %+v and no drop", w.got, w.records("net.drop"), want)
	}
}

// NET-019: a parked copy is posted as net.deliver and leaves the parked list before it is
// delivered, so a later crash of the destination drops nothing, even a crash its own handler
// causes.
func TestParkedCopyLeavesTheList(t *testing.T) {
	for _, self := range []bool{false, true} {
		w := newNet(1, simnet.Config{Default: simnet.Link{Latency: ms}})
		if self {
			w.nw.Handle(w.b, func(kernel.NodeID, any) { w.b.Crash() })
		}
		w.b.Pause()
		w.nw.Send(w.a, 2, tmsg("x"))
		w.at(5*ms, w.b.Resume)
		w.at(10*ms, w.b.Crash)
		w.s.Run()
		defers := w.records("kernel.defer")
		if len(defers) != 1 || defers[0].Text != "net.deliver" {
			t.Fatalf("self=%v: kernel.defer records %+v, want one net.deliver", self, defers)
		}
		want := simnet.Stats{Sent: 1, Delivered: 1, Deferred: 1}
		if st, ls := w.nw.Stats(), w.nw.LinkStats(1, 2); st != want || ls != want || len(w.records("net.drop")) != 0 {
			t.Fatalf("self=%v: Stats() = %+v, LinkStats(1, 2) = %+v, drops %+v; want %+v and no drop",
				self, st, ls, w.records("net.drop"), want)
		}
	}
}

// NET-009, NET-018: net.deliver is emitted and the copy counted before the handler runs, a handler
// may Send, and a handler panic propagates to the kernel.
func TestHandlerCalls(t *testing.T) {
	w := newNet(1, simnet.Config{Default: simnet.Link{Latency: ms}})
	seen := -1
	w.nw.Handle(w.b, func(from kernel.NodeID, p any) {
		seen = len(w.records("net.deliver"))
		m, _ := p.(tmsg) // a wrong type fails the check below
		w.nw.Send(w.b, from, "re: "+m)
	})
	w.nw.Send(w.a, 2, tmsg("ping"))
	w.s.Run()
	if seen != 1 {
		t.Fatalf("the handler saw %d net.deliver records, want 1", seen)
	}
	want := []delivery{{at: kernel.Time(2 * ms), to: 1, from: 2, payload: tmsg("re: ping")}}
	if !slices.Equal(w.got, want) {
		t.Fatalf("got %+v, want %+v", w.got, want)
	}

	w = newNet(1, simnet.Config{Default: simnet.Link{Latency: ms}})
	var inside simnet.Stats
	w.nw.Handle(w.b, func(kernel.NodeID, any) {
		inside = w.nw.Stats()
		panic("boom")
	})
	w.nw.Send(w.a, 2, tmsg("x"))
	w.s.Run()
	var pe *kernel.PanicError
	if !errors.As(w.s.Err(), &pe) || pe.Value != "boom" {
		t.Fatalf("Err() = %v, want the handler's panic", w.s.Err())
	}
	if st, want := w.nw.Stats(), (simnet.Stats{Sent: 1, Delivered: 1}); inside != want || st != want {
		t.Fatalf("Stats() in the handler = %+v, after the panic = %+v; want %+v for both", inside, st, want)
	}
}

// AT-NET-11 (Send)
func TestSendMisuse(t *testing.T) {
	w := newNet(1, simnet.DefaultConfig())
	mustPanic(t, "simnet: Send: unknown node id 99", func() { w.nw.Send(w.a, 99, tmsg("x")) })
	mustPanic(t, "simnet: Send: nil *kernel.Node", func() { w.nw.Send(nil, 1, tmsg("x")) })
	w.a.Pause()
	mustPanic(t, "simnet: Send: node a (id 1) is paused", func() { w.nw.Send(w.a, 2, tmsg("x")) })
	mustPanic(t, "simnet: Send: node a (id 1) is paused", func() { w.nw.Send(w.a, 99, tmsg("x")) })
	w.a.Resume()
	w.a.Crash()
	mustPanic(t, "simnet: Send: node a (id 1) is down", func() { w.nw.Send(w.a, 2, tmsg("x")) })
	s := kernel.New(kernel.Config{Seed: 1})
	foreign := s.AddNode("a", func(*kernel.Node) {})
	mustPanic(t, "simnet: Send: node a belongs to a different Sim", func() { w.nw.Send(foreign, 1, tmsg("x")) })
	if w.nw.Stats().Sent != 0 || len(w.records("net.send")) != 0 {
		t.Fatalf("a rejected Send was counted or recorded")
	}
	w.nw.Send(w.b, 3, tmsg("ok")) // rejected sends used no message ID (NET-011)
	if sends := w.records("net.send"); len(sends) != 1 || attr(sends[0], "msg") != "1" {
		t.Fatalf("net.send after the rejected sends %+v, want msg 1", sends)
	}
	nw := simnet.New(s, simnet.Config{})
	mustPanic(t, "simnet: Send: node a (id 1) is down", func() { nw.Send(foreign, 1, tmsg("x")) }) // not booted
}

// AT-NET-16
func TestSelfSends(t *testing.T) {
	w := newNet(1, simnet.Config{Default: simnet.Link{Latency: 2 * ms}})
	w.nw.Isolate(1)
	w.nw.Send(w.a, 1, tmsg("self"))
	w.s.Run()
	want := []delivery{{at: kernel.Time(2 * ms), to: 1, from: 1, payload: tmsg("self")}}
	if !slices.Equal(w.got, want) {
		t.Fatalf("got %+v, want %+v", w.got, want)
	}
	if ls := w.nw.LinkStats(1, 1); ls != (simnet.Stats{Sent: 1, Delivered: 1}) {
		t.Fatalf("LinkStats(1, 1) = %+v, want {Sent:1 Delivered:1}", ls)
	}

	l := simnet.Link{Latency: 2 * ms, Jitter: 4 * ms}
	w = newNet(5, simnet.Config{Default: l})
	w.nw.Isolate(1)
	for i := 0; i < 20; i++ {
		w.nw.Send(w.a, 1, i)
	}
	w.s.Run()
	r := freshStream(5, "net/link/a/a")
	lat := map[string]string{}
	for _, rec := range w.records("net.deliver") {
		lat[attr(rec, "msg")] = attr(rec, "latency_ns")
	}
	for i := 0; i < 20; i++ {
		d := replayDecision(l, r)
		if got := lat[strconv.Itoa(i+1)]; got != ns(d.Delay) {
			t.Fatalf("self message %d latency %s, replay of net/link/a/a %v", i+1, got, d.Delay)
		}
	}
}

// AT-NET-19
func TestHandlerLifecycle(t *testing.T) {
	w := newNet(1, simnet.Config{Default: simnet.Link{Latency: ms}})
	w.nw.Handle(w.b, nil)
	w.nw.Send(w.a, 2, tmsg("x"))
	w.s.Run()
	if d := w.records("net.drop"); len(d) != 1 || attr(d[0], "reason") != "no-handler" {
		t.Fatalf("drops %+v, want one no-handler", d)
	}
	hs := w.records("net.handle")
	if last := hs[len(hs)-1]; attr(last, "installed") != "false" || attr(last, "node") != "b" {
		t.Fatalf("last net.handle %+v, want installed=false for b", last)
	}

	var h2 []any
	w.nw.Handle(w.b, func(_ kernel.NodeID, p any) { h2 = append(h2, p) })
	w.nw.Send(w.a, 2, tmsg("y"))
	w.s.Run()
	if len(h2) != 1 || len(w.got) != 0 {
		t.Fatalf("h2 got %v, original handler got %v", h2, w.got)
	}

	w.install["b"] = false
	before := len(w.records("net.handle"))
	w.b.Crash()
	if len(w.records("net.handle")) != before {
		t.Fatalf("crash emitted net.handle")
	}
	w.b.Restart()
	w.nw.Send(w.a, 2, tmsg("z"))
	w.s.Run()
	d := w.records("net.drop")
	if last := d[len(d)-1]; attr(last, "reason") != "no-handler" || len(h2) != 1 {
		t.Fatalf("after restart without handler: last drop %+v, h2 %v", last, h2)
	}
}

// AT-NET-21
func TestLateNodes(t *testing.T) {
	w := newNet(1, simnet.DefaultConfig())
	w.nw.Partition([]kernel.NodeID{1}, []kernel.NodeID{2, 3})
	v := w.nw.TopologyVersion()
	d := w.addNode("d")
	if !w.nw.Connected(4, 1) || !w.nw.Connected(1, 4) || w.nw.Connected(1, 2) {
		t.Fatalf("late node: Connected(4,1)=%v Connected(1,4)=%v Connected(1,2)=%v",
			w.nw.Connected(4, 1), w.nw.Connected(1, 4), w.nw.Connected(1, 2))
	}
	if w.nw.TopologyVersion() != v+1 {
		t.Fatalf("TopologyVersion = %d, want %d", w.nw.TopologyVersion(), v+1)
	}
	w.nw.Send(w.a, d.ID(), tmsg("x"))
	s := w.records("net.send")
	if len(s) != 1 || attr(s[0], "to") != "d" {
		t.Fatalf("net.send %+v, want to=d", s)
	}

	// Send as the first network call after AddNode registers the new node (NET-002)
	w = newNet(1, simnet.Config{Default: simnet.Link{Latency: ms}})
	w.addNode("d")
	w.nw.Send(w.a, 4, tmsg("y"))
	w.s.Run()
	want := []delivery{{at: kernel.Time(ms), to: 4, from: 1, payload: tmsg("y")}}
	if !slices.Equal(w.got, want) {
		t.Fatalf("got %+v, want %+v", w.got, want)
	}
}

// AT-NET-23
func TestTail(t *testing.T) {
	w := newNet(1, simnet.Config{Default: simnet.Link{Latency: ms, TailPPM: simnet.MaxPPM, Tail: 100 * ms}})
	for i := 0; i < 1000; i++ {
		w.nw.Send(w.a, 2, i)
	}
	w.s.Run()
	if len(w.got) != 1000 {
		t.Fatalf("%d deliveries, want 1000", len(w.got))
	}
	slow := 0
	for _, d := range w.got {
		if d.at < kernel.Time(ms) || d.at > kernel.Time(101*ms) {
			t.Fatalf("delay %v outside [1ms, 101ms]", d.at)
		}
		if d.at > kernel.Time(50*ms) {
			slow++
		}
	}
	if slow == 0 {
		t.Fatalf("no delay above 50ms")
	}
}
