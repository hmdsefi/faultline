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

// AT-DET-15
func TestRegisterBugSwitch(t *testing.T) {
	wantFail := map[uint64]string{
		1:  "stale read: op 8 got 5, last acked write 7",
		11: "stale read: op 4 got 1, last acked write 3",
		14: "stale read: op 2 got 0, last acked write 1",
		16: "stale read: op 10 got 7, last acked write 9",
		18: "stale read: op 14 got 11, last acked write 13",
	}
	for _, early := range []bool{false, true} {
		run := func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
			s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
			toys.Register(s, toys.NewWire(s, 1*ms, 10*ms), toys.RegisterConfig{Ops: 20, EarlyAck: early})
			return s, s.Run()
		}
		for seed := uint64(1); seed <= 20; seed++ {
			res := golden.Check(t, seed, run)
			want, fails := wantFail[seed]
			switch {
			case !early || !fails:
				if res.Stop != kernel.StopIdle || res.Executed != 105 || res.Err != "" {
					t.Errorf("early=%v seed %d: %+v, want idle with 105 events", early, seed, res)
				}
			default:
				if res.Stop != kernel.StopFailed || res.Err != want {
					t.Errorf("early=%v seed %d: %+v, want failure %q", early, seed, res, want)
				}
			}
		}
	}
	mustPanic(t, "toys: invalid RegisterConfig", func() {
		s := kernel.New(kernel.Config{})
		toys.Register(s, toys.NewWire(s, 0, 0), toys.RegisterConfig{Ops: -1})
	})
}

// DET-042
func TestGossip(t *testing.T) {
	run := func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
		toys.Gossip(s, toys.NewWire(s, 1*ms, 20*ms), toys.GossipConfig{Nodes: 5, Ticks: 20, Interval: 100 * ms})
		return s, s.Run()
	}
	for seed := uint64(1); seed <= 3; seed++ {
		if res := golden.Check(t, seed, run); res.Stop != kernel.StopIdle || res.Executed != 210 {
			t.Errorf("seed %d: %+v, want idle with 210 events", seed, res)
		}
	}
	small := func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
		toys.Gossip(s, toys.NewWire(s, ms, ms), toys.GossipConfig{Nodes: 3, Ticks: 1, Interval: 10 * ms})
		return s, s.Run()
	}
	golden.Check(t, 1, small)
	s, _ := small(1, kernel.TraceConfig{Level: kernel.TraceFull})
	nodes := s.Nodes()
	if len(nodes) != 3 || nodes[0].Name() != "g1" || nodes[2].Name() != "g3" || !nodes[1].HasTag("server") {
		t.Fatalf("nodes %v", nodes)
	}
	sends := 0
	for _, r := range s.Records() {
		if r.Kind == "toys.send" {
			sends++
			if !strings.HasPrefix(r.Text, "gossip [") {
				t.Errorf("send text %q", r.Text)
			}
		}
	}
	if sends != 3 {
		t.Errorf("%d sends, want one per node", sends)
	}
	if got := (toys.GossipMsg{Counts: []uint64{1, 0, 12}}).Describe(); got != "gossip [1 0 12]" {
		t.Errorf("Describe = %q", got)
	}
	for _, cfg := range []toys.GossipConfig{{Nodes: 1, Interval: ms}, {Nodes: 2, Ticks: -1, Interval: ms}, {Nodes: 2}} {
		mustPanic(t, "toys: invalid GossipConfig", func() {
			s := kernel.New(kernel.Config{})
			toys.Gossip(s, toys.NewWire(s, 0, 0), cfg)
		})
	}
}

// DET-043: message descriptions.
func TestRegisterDescribe(t *testing.T) {
	cases := []struct {
		msg  kernel.Describer
		want string
	}{
		{toys.Write{Op: 1, V: 2}, "write op=1 v=2"},
		{toys.WriteAck{Op: 3}, "write-ack op=3"},
		{toys.Read{Op: 4}, "read op=4"},
		{toys.ReadResp{Op: 5, V: 6}, "read-resp op=5 v=6"},
		{toys.Replicate{Op: 7, V: 8}, "replicate op=7 v=8"},
		{toys.ReplicateAck{Op: 9}, "replicate-ack op=9"},
		{toys.Ping{N: 1}, "ping 1"},
		{toys.Pong{N: 2}, "pong 2"},
	}
	for _, c := range cases {
		if got := c.msg.Describe(); got != c.want {
			t.Errorf("%T.Describe() = %q, want %q", c.msg, got, c.want)
		}
	}
}

// DET-042, DET-043: the smallest accepted configurations.
func TestToyMinimalConfigs(t *testing.T) {
	gossip := func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
		toys.Gossip(s, toys.NewWire(s, ms, ms), toys.GossipConfig{Nodes: 2, Ticks: 0, Interval: ms})
		return s, s.Run()
	}
	if res := golden.Check(t, 1, gossip); res.Stop != kernel.StopIdle || res.Executed != 4 {
		t.Errorf("gossip: %+v, want idle with 4 events", res)
	}
	register := func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
		toys.Register(s, toys.NewWire(s, ms, ms), toys.RegisterConfig{Ops: 0})
		return s, s.Run()
	}
	if res := golden.Check(t, 1, register); res.Stop != kernel.StopIdle || res.Executed != 5 {
		t.Errorf("register: %+v, want idle with 5 events", res)
	}
}

// DET-043: Register returns [p, r1, r2, c], and a restarted client starts again from op 1.
func TestRegisterRestart(t *testing.T) {
	s := kernel.New(kernel.Config{})
	ns := toys.Register(s, toys.NewWire(s, ms, ms), toys.RegisterConfig{})
	want := []struct{ name, tag string }{{"p", "server"}, {"r1", "server"}, {"r2", "server"}, {"c", "client"}}
	if len(ns) != len(want) || len(s.Nodes()) != len(want) {
		t.Fatalf("Register returned %d nodes, the Sim has %d, want %d", len(ns), len(s.Nodes()), len(want))
	}
	for i, w := range want {
		if n := ns[i]; n != s.Nodes()[i] || n.Name() != w.name || !slices.Equal(n.Tags(), []string{w.tag}) {
			t.Errorf("node %d is %s %v, want %s [%s]", i, n.Name(), n.Tags(), w.name, w.tag)
		}
	}

	run := func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
		c := toys.Register(s, toys.NewWire(s, ms, ms), toys.RegisterConfig{Ops: 2})[3]
		s.RunUntil(kernel.Time(50 * ms))
		c.Crash()
		c.Restart()
		return s, s.Run()
	}
	if res := golden.Check(t, 1, run); res.Stop != kernel.StopIdle || res.Executed != 21 {
		t.Errorf("restart: %+v, want idle with 21 events", res)
	}
	s, _ = run(1, kernel.TraceConfig{Level: kernel.TraceFull})
	var sends []string
	for _, r := range s.Records() {
		if r.Kind == "toys.send" && r.Node == s.Nodes()[3].ID() {
			sends = append(sends, fmt.Sprintf("%d %s", r.Inc, r.Text))
		}
	}
	if want := []string{"1 write op=1 v=1", "1 read op=2", "2 write op=1 v=1"}; !slices.Equal(sends, want) {
		t.Errorf("client sends %q, want %q", sends, want)
	}
}

// dupTransport sends every message twice.
type dupTransport struct{ toys.Transport }

func (d dupTransport) Send(from *kernel.Node, to kernel.NodeID, msg any) {
	d.Transport.Send(from, to, msg)
	d.Transport.Send(from, to, msg)
}

// DET-043: with EarlyAck false no read is stale, also when answers to a crashed client's requests
// reach its next incarnation, or when every message arrives twice.
func TestRegisterNoStaleRead(t *testing.T) {
	cases := []struct {
		name     string
		seed     uint64
		crash    time.Duration // the client crashes and restarts at this time; 0 for no crash
		dup      bool
		executed uint64
	}{
		{"ack of the old client's write", 5, 35500 * time.Microsecond, false, 27},
		{"old client's write arrives late", 61, 26 * ms, false, 111},
		{"duplicated messages", 495, 0, true, 365},
	}
	for _, c := range cases {
		run := func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
			s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
			var tr toys.Transport = toys.NewWire(s, ms, 10*ms)
			if c.dup {
				tr = dupTransport{tr}
			}
			client := toys.Register(s, tr, toys.RegisterConfig{Ops: 20})[3]
			if c.crash > 0 {
				s.RunUntil(kernel.Time(c.crash))
				client.Crash()
				client.Restart()
			}
			return s, s.Run()
		}
		res := golden.Check(t, c.seed, run)
		if res.Stop != kernel.StopIdle || res.Err != "" || res.Executed != c.executed {
			t.Errorf("%s: %+v, want idle with %d events", c.name, res, c.executed)
		}
	}
}

// DET-042: the messages of one small run, which show the increment, the peer choice, the max merge
// and the copy of the counts that is sent.
func TestGossipMessages(t *testing.T) {
	s := fullSim(1)
	toys.Gossip(s, toys.NewWire(s, ms, 5*ms), toys.GossipConfig{Nodes: 3, Ticks: 5, Interval: 10 * ms})
	if stop := s.Run(); stop != kernel.StopIdle {
		t.Fatalf("stop %v, want idle", stop)
	}
	var got []string
	for _, r := range s.Records() {
		if r.Kind == "toys.send" || r.Kind == "toys.recv" {
			got = append(got, recLine(r))
		}
	}
	want := []string{
		`12 0.005281524s 2/1 toys.send cause=11 "gossip [0 1 0]" [to=g1 latency_ns=3686623]`,
		`14 0.006827417s 1/1 toys.send cause=13 "gossip [1 0 0]" [to=g2 latency_ns=2053498]`,
		`16 0.008880915s 2/1 toys.recv cause=15 "gossip [1 0 0]" [from=g1]`,
		`18 0.008968147s 1/1 toys.recv cause=17 "gossip [0 1 0]" [from=g2]`,
		`20 0.012854809s 3/1 toys.send cause=19 "gossip [0 0 1]" [to=g2 latency_ns=1170776]`,
		`22 0.014025585s 2/1 toys.recv cause=21 "gossip [0 0 1]" [from=g3]`,
		`24 0.016214753s 1/1 toys.send cause=23 "gossip [2 1 0]" [to=g2 latency_ns=2329798]`,
		`26 0.018200063s 2/1 toys.send cause=25 "gossip [1 2 1]" [to=g1 latency_ns=1272935]`,
		`28 0.018544551s 2/1 toys.recv cause=27 "gossip [2 1 0]" [from=g1]`,
		`30 0.019472998s 1/1 toys.recv cause=29 "gossip [1 2 1]" [from=g2]`,
		`32 0.023812819s 3/1 toys.send cause=31 "gossip [0 0 2]" [to=g1 latency_ns=2781084]`,
		`34 0.025636452s 1/1 toys.send cause=33 "gossip [3 2 1]" [to=g2 latency_ns=4778959]`,
		`36 0.026593903s 1/1 toys.recv cause=35 "gossip [0 0 2]" [from=g3]`,
		`38 0.026785783s 2/1 toys.send cause=37 "gossip [2 3 1]" [to=g1 latency_ns=4098743]`,
		`40 0.030047423s 3/1 toys.send cause=39 "gossip [0 0 3]" [to=g2 latency_ns=1350783]`,
		`42 0.030415411s 2/1 toys.recv cause=41 "gossip [3 2 1]" [from=g1]`,
		`44 0.030884526s 1/1 toys.recv cause=43 "gossip [2 3 1]" [from=g2]`,
		`46 0.031398206s 2/1 toys.recv cause=45 "gossip [0 0 3]" [from=g3]`,
		`48 0.037094079s 3/1 toys.send cause=47 "gossip [0 0 4]" [to=g1 latency_ns=3710086]`,
		`50 0.038965750s 2/1 toys.send cause=49 "gossip [3 4 3]" [to=g1 latency_ns=3660470]`,
		`52 0.039949885s 1/1 toys.send cause=51 "gossip [4 3 2]" [to=g3 latency_ns=2860350]`,
		`54 0.040804165s 1/1 toys.recv cause=53 "gossip [0 0 4]" [from=g3]`,
		`56 0.042626220s 1/1 toys.recv cause=55 "gossip [3 4 3]" [from=g2]`,
		`58 0.042810235s 3/1 toys.recv cause=57 "gossip [4 3 2]" [from=g1]`,
		`60 0.045242417s 3/1 toys.send cause=59 "gossip [4 3 5]" [to=g2 latency_ns=3674061]`,
		`62 0.047626069s 1/1 toys.send cause=61 "gossip [5 4 4]" [to=g2 latency_ns=4407308]`,
		`64 0.048916478s 2/1 toys.recv cause=63 "gossip [4 3 5]" [from=g3]`,
		`66 0.049693218s 2/1 toys.send cause=65 "gossip [4 5 5]" [to=g3 latency_ns=4647984]`,
		`68 0.052033377s 2/1 toys.recv cause=67 "gossip [5 4 4]" [from=g1]`,
		`70 0.054341202s 3/1 toys.recv cause=69 "gossip [4 5 5]" [from=g2]`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("messages:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}
