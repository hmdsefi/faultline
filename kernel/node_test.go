package kernel

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"
)

func noBoot(*Node) {}

// lastRecord returns the most recent kept record of a TraceFull Sim.
func lastRecord(s *Sim) Record {
	rs := s.Records()
	return rs[len(rs)-1]
}

// AT-KRN-23
func TestHookOrder(t *testing.T) {
	s := fullSim(1)
	var log orderLog
	s.OnBoot(func(n *Node) {
		log = append(log, "h1")
		if n.State() != NodeUp || n.Incarnation() != 1 || lastRecord(s).Kind != "kernel.boot" {
			t.Errorf("in h1: state %v inc %d last %s", n.State(), n.Incarnation(), recLine(lastRecord(s)))
		}
	})
	s.OnBoot(func(*Node) { log = append(log, "h2") })
	s.OnCrash(func(n *Node) {
		log = append(log, "c1")
		if n.State() != NodeDown || lastRecord(s).Kind != "kernel.crash" {
			t.Errorf("in c1: state %v last %s", n.State(), recLine(lastRecord(s)))
		}
	})
	s.OnCrash(func(*Node) { log = append(log, "c2") })
	n := s.AddNode("n1", func(*Node) { log = append(log, "boot") })
	s.Run()
	if got := log.String(); got != "h1 h2 boot" {
		t.Fatalf("boot log %q", got)
	}
	log = nil
	n.Crash()
	if got := log.String(); got != "c1 c2" {
		t.Fatalf("crash log %q", got)
	}
	mustPanic(t, "kernel: nil function passed to OnBoot", func() { s.OnBoot(nil) })
	mustPanic(t, "kernel: nil function passed to OnCrash", func() { s.OnCrash(nil) })
}

// KRN-050: a hook registered during a boot takes effect from the next boot.
func TestHookRegisteredDuringBoot(t *testing.T) {
	s := New(Config{Seed: 1})
	var log orderLog
	s.OnBoot(func(*Node) {
		log = append(log, "h1")
		if len(log) == 1 {
			s.OnBoot(func(*Node) { log = append(log, "late") })
		}
	})
	n := s.AddNode("n1", noBoot)
	s.Run()
	n.Crash()
	n.Restart()
	if got := log.String(); got != "h1 h1 late" {
		t.Fatalf("log %q, want %q", got, "h1 h1 late")
	}
}

// AT-KRN-24
func TestAddNode(t *testing.T) {
	s := fullSim(1)
	n1 := s.AddNode("n1", noBoot)
	n2 := s.AddNode("n2", noBoot)
	n3 := s.AddNode("n3", noBoot)
	if n1.ID() != 1 || n2.ID() != 2 || n3.ID() != 3 {
		t.Fatalf("IDs %d %d %d", n1.ID(), n2.ID(), n3.ID())
	}
	nodes := s.Nodes()
	if !slices.Equal(nodes, []*Node{n1, n2, n3}) {
		t.Fatalf("Nodes() = %v", nodes)
	}
	nodes[0] = nil
	if s.Nodes()[0] != n1 {
		t.Fatal("Nodes() does not return a new slice")
	}
	if s.Lookup("n2").ID() != 2 || s.Lookup("zz") != nil || s.Node(0) != nil || s.Node(4) != nil || s.Node(2) != n2 {
		t.Fatal("Lookup/Node mismatch")
	}
	if n1.State() != NodeDown || n1.Incarnation() != 0 || n1.Name() != "n1" || n1.Sim() != s {
		t.Fatalf("before boot: state %v inc %d", n1.State(), n1.Incarnation())
	}
	if s.Executed() != 0 {
		t.Fatal("AddNode ran the boot synchronously")
	}
	s.Run()
	for _, n := range s.Nodes() {
		if n.State() != NodeUp || n.Incarnation() != 1 {
			t.Fatalf("%s after Run: state %v inc %d", n.Name(), n.State(), n.Incarnation())
		}
	}

	badName := func(name string) string {
		return `kernel: invalid node name "` + name + `": want 1-64 characters [A-Za-z0-9._-], starting with a letter or digit`
	}
	for _, name := range []string{"", "a/b", "-x", strings.Repeat("a", 65)} {
		mustPanic(t, badName(name), func() { s.AddNode(name, noBoot) })
	}
	s.AddNode(strings.Repeat("a", 64), noBoot)
	s.AddNode("A.b_c-9", noBoot)
	mustPanic(t, `kernel: duplicate node name "n1"`, func() { s.AddNode("n1", noBoot) })
	mustPanic(t, `kernel: nil BootFunc for node "x"`, func() { s.AddNode("x", nil) })
	mustPanic(t, "kernel: drift 1000001 ppm out of range [-500000, 1000000]", func() { s.AddNode("x", noBoot, WithClock(0, 1_000_001)) })
	mustPanic(t, "kernel: drift -500001 ppm out of range [-500000, 1000000]", func() { s.AddNode("x", noBoot, WithClock(0, -500_001)) })
	mustPanic(t, `kernel: invalid tag "a,b" for node "x"`, func() { s.AddNode("x", noBoot, WithTags("a,b")) })

	tagged := s.AddNode("tagged", noBoot, WithTags("server", "x"), WithTags("server"))
	if got := tagged.Tags(); !slices.Equal(got, []string{"server", "x"}) {
		t.Fatalf("Tags() = %v", got)
	}
	tagged.Tags()[0] = "changed"
	if !tagged.HasTag("server") || !tagged.HasTag("x") || tagged.HasTag("y") {
		t.Fatal("HasTag mismatch or Tags() is not a copy")
	}
	if r := lastRecord(s); r.Kind != "kernel.add_node" || r.Node != tagged.ID() || r.Inc != 0 || r.Text != "tagged" ||
		!strings.HasSuffix(recLine(r), `[tags=server,x offset_ns=0 drift_ppm=0]`) {
		t.Errorf("add_node record %s", recLine(r))
	}
	s.AddNode("clocked", noBoot, WithClock(time.Second, 7), WithClock(2*time.Second, -3))
	if r := lastRecord(s); !strings.HasSuffix(recLine(r), `[tags= offset_ns=2000000000 drift_ppm=-3]`) {
		t.Errorf("the last WithClock does not win: %s", recLine(r))
	}
}

// AT-KRN-27
func TestCrashBeforeFirstBoot(t *testing.T) {
	s := fullSim(1)
	var log orderLog
	s.OnCrash(func(*Node) { log = append(log, "crash-hook") })
	s.OnBoot(func(*Node) { log = append(log, "boot-hook") })
	n := s.AddNode("n1", func(*Node) { log = append(log, "boot") })
	n.Crash()
	if r := lastRecord(s); recLine(r) != `3 0.000000000s 1/0 kernel.crash cause=2 "crash" [from=down]` {
		t.Fatalf("crash record %s", recLine(r))
	}
	if len(log) != 0 {
		t.Fatalf("hooks ran: %v", log)
	}
	if r := s.Run(); r != StopIdle || s.Executed() != 0 || n.State() != NodeDown || n.Incarnation() != 0 {
		t.Fatalf("Run %v Executed %d state %v inc %d", r, s.Executed(), n.State(), n.Incarnation())
	}
	count := len(s.Records())
	n.Crash()
	if len(s.Records()) != count {
		t.Fatal("second Crash emitted a record")
	}
	n.Restart()
	if n.Incarnation() != 1 || n.State() != NodeUp || log.String() != "boot-hook boot" {
		t.Fatalf("Restart: inc %d state %v log %q", n.Incarnation(), n.State(), log)
	}
}

// KRN-031, KRN-061: Restart before the initial boot event runs makes that event stale.
func TestRestartBeforeInitialBoot(t *testing.T) {
	s := New(Config{Seed: 1})
	boots := 0
	n := s.AddNode("n1", func(*Node) { boots++ })
	n.Restart()
	s.Run()
	if boots != 1 || n.Incarnation() != 1 || s.Executed() != 0 {
		t.Fatalf("boots %d inc %d executed %d", boots, n.Incarnation(), s.Executed())
	}
}

// AT-KRN-28
func TestIncarnationBoundEvents(t *testing.T) {
	s := fullSim(1)
	var log orderLog
	var stored EventID
	n := s.AddNode("n", func(n *Node) {
		id := n.After(time.Second, "t", func() { log = append(log, fmt.Sprintf("t@inc%d", n.Incarnation())) })
		if n.Incarnation() == 1 {
			stored = id
			s.After(2*time.Second, "global", log.add("global"))
		}
	})
	s.At(msec(500), "crash", func() {
		n.Crash()
		count := len(s.Records())
		if id := n.After(time.Second, "dead", log.add("dead")); id != 0 || len(s.Records()) != count {
			t.Errorf("After on a down node = %d, records +%d", id, len(s.Records())-count)
		}
		if id := n.Post("dead", log.add("dead")); id != 0 {
			t.Errorf("Post on a down node = %d", id)
		}
		if n.Cancel(stored) {
			t.Error("n.Cancel of a stale timer = true")
		}
	})
	s.At(msec(600), "restart", n.Restart)
	s.Run()
	if got := log.String(); got != "t@inc2 global" {
		t.Fatalf("log %q, want %q", got, "t@inc2 global")
	}

	s2 := New(Config{Seed: 1})
	var log2 orderLog
	var nodeTimer EventID
	m := s2.AddNode("m", func(m *Node) { nodeTimer = m.After(time.Second, "nt", log2.add("nt")) })
	g := s2.After(2*time.Second, "g", log2.add("g"))
	s2.RunUntil(0)
	if m.Cancel(g) {
		t.Error("n.Cancel of a global event = true")
	}
	if !s2.Cancel(nodeTimer) {
		t.Error("s.Cancel of a pending node timer = false")
	}
	s2.Run()
	if got := log2.String(); got != "g" {
		t.Fatalf("log %q, want %q", got, "g")
	}
}

// KRN-064, KRN-065: a node may crash itself; the callback runs to completion.
func TestSelfCrash(t *testing.T) {
	s := New(Config{Seed: 1})
	after := ""
	var n *Node
	n = s.AddNode("n", func(*Node) {
		n.Post("suicide", func() {
			n.Crash()
			after = n.State().String()
			if id := n.After(time.Second, "x", func() {}); id != 0 {
				t.Errorf("After on the crashed node = %d", id)
			}
		})
	})
	s.Run()
	if after != "down" {
		t.Fatalf("state after self-crash %q", after)
	}
}

// AT-KRN-34
func TestNodeRand(t *testing.T) {
	const seed = 0x5e1f9a2c4b7d3e80
	s := New(Config{Seed: seed})
	n1 := s.AddNode("n1", noBoot)
	s.Run()
	if got := n1.Rand().Uint64(); got != 0x5f9d802d8bdde2bf {
		t.Fatalf("first value %#016x", got)
	}
	if n1.Rand() != n1.Rand() {
		t.Fatal("Rand() pointers differ within one incarnation")
	}
	old := n1.Rand()
	n1.Crash()
	n1.Restart()
	if n1.Rand() == old {
		t.Fatal("Rand() reused the old incarnation's stream")
	}
	want := rand.New(rand.NewPCG(streamSeeds(seed, "node/n1/2"))).Uint64()
	if got := n1.Rand().Uint64(); got != want {
		t.Fatalf("incarnation 2 first value %#016x, want %#016x", got, want)
	}
}

// KRN-080: restarts do not grow the Sim's stream table.
func TestNodeRandNotRetained(t *testing.T) {
	s := New(Config{Seed: 1})
	n1 := s.AddNode("n1", noBoot)
	s.Run()
	before := len(s.streams)
	for range 3 {
		n1.Rand()
		n1.Crash()
		n1.Restart()
	}
	n1.Rand()
	if len(s.streams) != before {
		t.Fatalf("Sim streams grew from %d to %d across restarts", before, len(s.streams))
	}
}

// AT-KRN-35 (Node.Logf)
func TestNodeLogf(t *testing.T) {
	s := fullSim(1)
	n := s.AddNode("n", noBoot)
	s.Run()
	n.Logf("x=%d", 5)
	if got := recLine(lastRecord(s)); got != `5 0.000000000s 1/1 kernel.log cause=4 "x=5" []` {
		t.Fatalf("log record %s", got)
	}
}

// AT-KRN-41
func TestGuardedCallsOutsideTheLoop(t *testing.T) {
	s := fullSim(1)
	n := s.AddNode("n", func(n *Node) {
		if n.Incarnation() == 2 {
			panic("boot failed")
		}
	})
	s.Run()
	n.Crash()
	n.Restart()
	pe := panicErr(t, s)
	if pe.Event != 0 || pe.Label != "restart" || pe.Node != n.ID() || pe.NodeName != "n" || pe.Inc != 2 {
		t.Fatalf("PanicError %+v", pe)
	}
	const wantErr = `kernel: panic at 0.000000000s in event 0 "restart" on node "n" (inc 2) (seed 0x0000000000000001): boot failed`
	if got := pe.Error(); got != wantErr {
		t.Errorf("Error() = %q, want %q", got, wantErr)
	}
	if got := recLine(lastRecord(s)); !strings.HasSuffix(got, `1/2 kernel.panic cause=6 "boot failed" [event_id=0 label=restart observer=false]`) {
		t.Errorf("last record %s", got)
	}
	if r := s.Run(); r != StopFailed {
		t.Fatalf("Run after the failed restart = %v", r)
	}
	if Active() != nil {
		t.Fatal("Active() != nil after the guarded call")
	}

	s2 := New(Config{Seed: 1})
	m := s2.AddNode("m", noBoot)
	s2.Run()
	s2.OnCrash(func(*Node) { panic("hook failed") })
	m.Crash()
	if pe := panicErr(t, s2); pe.Label != "crash" || pe.Event != 0 || pe.Node != m.ID() || pe.Inc != 1 {
		t.Fatalf("crash hook PanicError %+v", pe)
	}
}

// KRN-045: Crash called from an event runs its hooks directly; the event's guard catches a panic.
func TestHookPanicInsideEvent(t *testing.T) {
	s := New(Config{Seed: 1})
	n := s.AddNode("n", noBoot)
	s.OnCrash(func(*Node) { panic("hook failed") })
	s.At(sec(1), "fault", n.Crash)
	if r := s.Run(); r != StopFailed {
		t.Fatalf("Run = %v", r)
	}
	if pe := panicErr(t, s); pe.Label != "fault" || pe.Event == 0 || pe.Node != 0 {
		t.Fatalf("PanicError %+v", pe)
	}
}

// AT-KRN-22 (OnBoot hook)
func TestReentrancyFromBootHook(t *testing.T) {
	s := New(Config{Seed: 1})
	s.OnBoot(func(*Node) { s.Run() })
	n := s.AddNode("n", noBoot)
	s.Run()
	pe := panicErr(t, s)
	if pe.Value != "kernel: Step/Run called while the simulation is running" || pe.Label != "boot" || pe.Node != n.ID() || pe.Inc != 0 {
		t.Fatalf("PanicError %+v", pe)
	}
}

// AT-KRN-40 (OnBoot hook, and OnCrash hook of a Crash called outside the loop)
func TestActiveInHooks(t *testing.T) {
	s := New(Config{Seed: 1})
	var inBoot, inCrash *Sim
	s.OnBoot(func(*Node) { inBoot = Active() })
	s.OnCrash(func(*Node) { inCrash = Active() })
	n := s.AddNode("n", noBoot)
	s.Run()
	n.Crash()
	if inBoot != s || inCrash != s {
		t.Fatalf("Active in boot hook %p, crash hook %p, want %p", inBoot, inCrash, s)
	}
	if Active() != nil {
		t.Fatal("Active() != nil after Crash")
	}
}

// AT-KRN-42 (stale node timer)
func TestNextAtDiscardsStale(t *testing.T) {
	s := fullSim(1)
	n := s.AddNode("n", func(n *Node) { n.After(time.Second, "t", func() {}) })
	s.At(sec(2), "a", func() {})
	s.RunUntil(0)
	n.Crash()
	count := len(s.Records())
	if at, ok := s.NextAt(); at != sec(2) || !ok {
		t.Fatalf("NextAt = (%v, %v), want (2s, true)", at, ok)
	}
	if len(s.Records()) != count || s.Now() != 0 {
		t.Fatal("discarding a stale entry emitted a record or moved the clock")
	}
}

// AT-KRN-43 (NodeState)
func TestNodeStateString(t *testing.T) {
	cases := map[NodeState]string{NodeUp: "up", NodeDown: "down", NodePaused: "paused", NodeState(0): "NodeState(0)"}
	for st, want := range cases {
		if got := st.String(); got != want {
			t.Errorf("NodeState(%d).String() = %q, want %q", st, got, want)
		}
	}
}

// KRN-024: the initial boot event cannot be cancelled.
func TestCancelBootEvent(t *testing.T) {
	s := New(Config{Seed: 1})
	n := s.AddNode("n", noBoot)
	if s.Cancel(1) || n.Cancel(1) {
		t.Fatal("Cancel of the initial boot event = true")
	}
	s.Run()
	if n.State() != NodeUp {
		t.Fatal("boot did not run")
	}
}
