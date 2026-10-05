package toys_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/internal/golden"
	"github.com/hmdsefi/faultline/internal/toys"
	"github.com/hmdsefi/faultline/kernel"
)

const ms = time.Millisecond

// recLine renders r as `Seq At Node/Inc Kind cause=C "Text" [k=v ...]` (DET §10 notation).
func recLine(r kernel.Record) string {
	attrs := make([]string, len(r.Attrs))
	for i, a := range r.Attrs {
		attrs[i] = a.Key + "=" + a.Value
	}
	return fmt.Sprintf("%d %s %d/%d %s cause=%d %q [%s]",
		r.Seq, r.At, r.Node, r.Inc, r.Kind, r.Cause, r.Text, strings.Join(attrs, " "))
}

// checkRecords compares the records of s with want, one record per non-empty line.
func checkRecords(t *testing.T, s *kernel.Sim, want string) {
	t.Helper()
	var wantLines []string
	for _, l := range strings.Split(want, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			wantLines = append(wantLines, l)
		}
	}
	rs := s.Records()
	for i := 0; i < len(rs) || i < len(wantLines); i++ {
		got, exp := "<none>", "<none>"
		if i < len(rs) {
			got = recLine(rs[i])
		}
		if i < len(wantLines) {
			exp = wantLines[i]
		}
		if got != exp {
			t.Errorf("record %d:\n got  %s\n want %s", i+1, got, exp)
		}
	}
}

// mustPanic fails the test unless fn panics with exactly want.
func mustPanic(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		t.Helper()
		if v := recover(); fmt.Sprint(v) != want {
			t.Fatalf("panic %v, want %q", v, want)
		}
	}()
	fn()
}

func fullSim(seed uint64) *kernel.Sim {
	return kernel.New(kernel.Config{Seed: seed, Trace: kernel.TraceConfig{Level: kernel.TraceFull}})
}

// AT-DET-14
func TestPingPongTrace(t *testing.T) {
	run := func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
		toys.PingPong(s, toys.NewWire(s, 1*ms, 5*ms), 3)
		return s, s.Run()
	}
	res := golden.Check(t, 1, run)
	if res.Stop != kernel.StopIdle || res.Executed != 8 || res.Hash != 0x2661a4a5aab70232 {
		t.Fatalf("result %+v", res)
	}
	s, _ := run(1, kernel.TraceConfig{Level: kernel.TraceFull})
	checkRecords(t, s, `
 1 0.000000000s 0/0 kernel.start cause=0 "start" [seed=0x0000000000000001 tie_break=seeded]
 2 0.000000000s 1/0 kernel.add_node cause=1 "a" [tags= offset_ns=0 drift_ppm=0]
 3 0.000000000s 2/0 kernel.add_node cause=2 "b" [tags= offset_ns=0 drift_ppm=0]
 4 0.000000000s 1/0 kernel.event cause=2 "boot" [id=1]
 5 0.000000000s 1/1 kernel.boot cause=4 "boot" []
 6 0.000000000s 1/1 toys.send cause=5 "ping 1" [to=b latency_ns=3686623]
 7 0.000000000s 2/0 kernel.event cause=3 "boot" [id=2]
 8 0.000000000s 2/1 kernel.boot cause=7 "boot" []
 9 0.003686623s 0/0 kernel.event cause=6 "toys.deliver" [id=3]
10 0.003686623s 2/1 toys.recv cause=9 "ping 1" [from=a]
11 0.003686623s 2/1 toys.send cause=10 "pong 1" [to=a latency_ns=2053498]
12 0.005740121s 0/0 kernel.event cause=11 "toys.deliver" [id=4]
13 0.005740121s 1/1 toys.recv cause=12 "pong 1" [from=b]
14 0.005740121s 1/1 toys.send cause=13 "ping 2" [to=b latency_ns=1170776]
15 0.006910897s 0/0 kernel.event cause=14 "toys.deliver" [id=5]
16 0.006910897s 2/1 toys.recv cause=15 "ping 2" [from=a]
17 0.006910897s 2/1 toys.send cause=16 "pong 2" [to=a latency_ns=2329798]
18 0.009240695s 0/0 kernel.event cause=17 "toys.deliver" [id=6]
19 0.009240695s 1/1 toys.recv cause=18 "pong 2" [from=b]
20 0.009240695s 1/1 toys.send cause=19 "ping 3" [to=b latency_ns=1272935]
21 0.010513630s 0/0 kernel.event cause=20 "toys.deliver" [id=7]
22 0.010513630s 2/1 toys.recv cause=21 "ping 3" [from=a]
23 0.010513630s 2/1 toys.send cause=22 "pong 3" [to=a latency_ns=2781084]
24 0.013294714s 0/0 kernel.event cause=23 "toys.deliver" [id=8]
25 0.013294714s 1/1 toys.recv cause=24 "pong 3" [from=b]
26 0.013294714s 1/1 kernel.log cause=25 "done 3" []
`)
	if s.TraceHash() != 0x2661a4a5aab70232 {
		t.Errorf("TraceHash = %#016x", s.TraceHash())
	}
	mustPanic(t, "toys: rounds must be >= 1", func() {
		s := kernel.New(kernel.Config{})
		toys.PingPong(s, toys.NewWire(s, 0, 0), 0)
	})
}

// toyRecords renders the toys.* records of s, plus records of the given kernel kinds, as
// `Node/Inc Kind "Text" [k=v ...]` (without Seq, At and Cause).
func toyRecords(s *kernel.Sim, kernelKinds ...string) []string {
	var out []string
	for _, r := range s.Records() {
		if !strings.HasPrefix(r.Kind, "toys.") && !slices.Contains(kernelKinds, r.Kind) {
			continue
		}
		attrs := make([]string, len(r.Attrs))
		for i, a := range r.Attrs {
			attrs[i] = a.Key + "=" + a.Value
		}
		out = append(out, fmt.Sprintf("%d/%d %s %q [%s]", r.Node, r.Inc, r.Kind, r.Text, strings.Join(attrs, " ")))
	}
	return out
}

// twoNodes returns a TraceFull Sim (seed 1), a Wire with a fixed 1ms latency, a sender x and a
// receiver y whose boot func is yBoot; both have booted.
func twoNodes(yBoot func(w *toys.Wire, n *kernel.Node)) (*kernel.Sim, *toys.Wire, *kernel.Node, *kernel.Node) {
	s := fullSim(1)
	w := toys.NewWire(s, ms, ms)
	x := s.AddNode("x", func(*kernel.Node) {})
	y := s.AddNode("y", func(n *kernel.Node) { yBoot(w, n) })
	s.RunUntil(0)
	return s, w, x, y
}

func expectRecords(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("records:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// AT-DET-16
func TestWireEdgeCases(t *testing.T) {
	s := fullSim(1)
	mustPanic(t, "toys: invalid latency [2ms, 1ms]", func() { toys.NewWire(s, 2*ms, ms) })
	mustPanic(t, "toys: invalid latency [-1ns, 1ms]", func() { toys.NewWire(s, -1, ms) })

	handle := func(w *toys.Wire, n *kernel.Node) { w.Handle(n, func(kernel.NodeID, any) {}) }
	const send = `1/1 toys.send "\"m\"" [to=y latency_ns=1000000]`

	t.Run("send from down or paused", func(t *testing.T) {
		s := fullSim(1)
		w := toys.NewWire(s, ms, ms)
		x := s.AddNode("x", func(*kernel.Node) {})
		y := s.AddNode("y", func(*kernel.Node) {})
		mustPanic(t, "toys: Send from node x in state down", func() { w.Send(x, y.ID(), "m") })
		s.Run()
		x.Pause()
		mustPanic(t, "toys: Send from node x in state paused", func() { w.Send(x, y.ID(), "m") })
		x.Resume()
		mustPanic(t, "toys: Send to unknown node 9", func() { w.Send(x, 9, "m") })
		x.Crash()
		mustPanic(t, "toys: Send from node x in state down", func() { w.Send(x, y.ID(), "m") })
	})

	t.Run("down at delivery", func(t *testing.T) {
		s, w, x, y := twoNodes(handle)
		w.Send(x, y.ID(), "m")
		y.Crash()
		s.Run()
		expectRecords(t, toyRecords(s), send, `2/1 toys.drop "\"m\"" [from=x reason=down]`)
	})

	t.Run("no handler after restart", func(t *testing.T) {
		s, w, x, y := twoNodes(func(w *toys.Wire, n *kernel.Node) {
			if n.Incarnation() == 1 {
				w.Handle(n, func(kernel.NodeID, any) { t.Error("stale handler called") })
			}
		})
		y.Crash()
		y.Restart()
		w.Send(x, y.ID(), "m")
		s.Run()
		expectRecords(t, toyRecords(s), send, `2/2 toys.drop "\"m\"" [from=x reason=no-handler]`)
	})

	t.Run("handler replaced after restart", func(t *testing.T) {
		var incs []uint32
		s, w, x, y := twoNodes(func(w *toys.Wire, n *kernel.Node) {
			inc := n.Incarnation()
			w.Handle(n, func(kernel.NodeID, any) { incs = append(incs, inc) })
		})
		y.Crash()
		y.Restart()
		w.Send(x, y.ID(), "m")
		s.Run()
		if !slices.Equal(incs, []uint32{2}) {
			t.Errorf("handlers called by incarnations %v, want [2]", incs)
		}
		expectRecords(t, toyRecords(s), send, `2/2 toys.recv "\"m\"" [from=x]`)
	})

	t.Run("paused at delivery", func(t *testing.T) {
		got := ""
		s, w, x, y := twoNodes(func(w *toys.Wire, n *kernel.Node) {
			w.Handle(n, func(_ kernel.NodeID, m any) { got = m.(string) })
		})
		y.Pause()
		w.Send(x, y.ID(), "m")
		s.RunUntil(kernel.Time(5 * ms))
		y.Resume()
		s.Run()
		if got != "m" {
			t.Fatal("message not delivered after resume")
		}
		expectRecords(t, toyRecords(s, "kernel.defer", "kernel.resume"),
			send,
			`2/1 kernel.defer "toys.deliver" [id=4]`,
			`2/1 kernel.resume "resume" [deferred=1]`,
			`2/1 toys.recv "\"m\"" [from=x]`)
	})

	t.Run("crash while deferred", func(t *testing.T) {
		s, w, x, y := twoNodes(func(w *toys.Wire, n *kernel.Node) {
			w.Handle(n, func(kernel.NodeID, any) { t.Error("delivered after the crash") })
		})
		y.Pause()
		w.Send(x, y.ID(), "m")
		s.RunUntil(kernel.Time(5 * ms))
		y.Crash()
		y.Restart()
		s.Run()
		expectRecords(t, toyRecords(s, "kernel.defer"), send, `2/1 kernel.defer "toys.deliver" [id=4]`)
	})
}
