package fault_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/internal/golden"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
	"github.com/hmdsefi/faultline/kernel/simdisk"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// faultKinds are the record kinds of applied events (FLT-070), in the order of fault.Kinds(), so
// that a failure names the same kind on every run.
var faultKinds = func() []string {
	var kinds []string
	for _, k := range fault.Kinds() {
		kinds = append(kinds, "fault."+strings.ReplaceAll(string(k), "-", "_"))
	}
	return kinds
}()

// AT-FLT-13
func TestLoad(t *testing.T) {
	w := newW5(full(1), false)
	w.in.Load(fault.Schedule{Events: []fault.Event{
		{At: at(2 * time.Second), Kind: fault.KindHeal},
		{At: at(time.Second), Kind: fault.KindCrash, Node: "n1"},
		{At: at(time.Second), Kind: fault.KindCrash, Node: "n2"},
		{At: at(3 * time.Second), Kind: fault.KindRestart, Node: "n1"},
	}})
	w.s.RunUntil(at(3 * time.Second))
	loads := 0
	for _, r := range w.records("kernel.event") {
		if r.Text == "fault/load" {
			loads++
		}
	}
	if loads != 3 {
		t.Fatalf("%d fault/load events, want 3", loads)
	}
	want := []fault.Event{
		{ID: 1, At: at(time.Second), Kind: fault.KindCrash, Node: "n1"},
		{ID: 2, At: at(time.Second), Kind: fault.KindCrash, Node: "n2"},
		{ID: 3, At: at(2 * time.Second), Kind: fault.KindHeal},
		{ID: 4, At: at(3 * time.Second), Kind: fault.KindRestart, Node: "n1", Undoes: []int{1}},
	}
	if got := w.in.Applied().Events; !reflect.DeepEqual(got, want) {
		t.Fatalf("Applied() = %+v", got)
	}
	for _, kind := range faultKinds {
		for _, r := range w.records(kind) {
			if attr(r, "source") != "load" {
				t.Fatalf("%s source %s", kind, attr(r, "source"))
			}
		}
	}
	mustPanic(t, "fault: Load: schedule: events[0]: at 2s is before now (3s)", func() {
		w.in.Load(fault.Schedule{Events: []fault.Event{{At: at(2 * time.Second), Kind: fault.KindHeal}}})
	})
	// The first event in slice order with At < Now is reported; an event at Now is allowed.
	mustPanic(t, "fault: Load: schedule: events[1]: at 2.999999999s is before now (3s)", func() {
		w.in.Load(fault.Schedule{Events: []fault.Event{
			{At: at(3 * time.Second), Kind: fault.KindHeal},
			{At: at(3*time.Second - 1), Kind: fault.KindHeal},
			{At: at(time.Second), Kind: fault.KindHeal},
		}})
	})
	mustPanic(t, `fault: Load: schedule: events[0]: crash: role "leader" is not allowed in a concrete schedule`, func() {
		w.in.Load(fault.Schedule{Events: []fault.Event{{At: at(4 * time.Second), Kind: fault.KindCrash, Role: "leader"}}})
	})
	mustPanic(t, "fault: Load: schedule: events[2]: crash: node is required", func() {
		w.in.Load(fault.Schedule{Events: []fault.Event{{Kind: fault.KindHeal}, {Kind: fault.KindHeal}, {Kind: fault.KindCrash}}})
	})
	w.in.Load(fault.Schedule{Events: []fault.Event{{At: at(4 * time.Second), Kind: fault.KindCrash, Node: "n9"}}})
	if stop := w.s.RunUntil(at(5 * time.Second)); stop != kernel.StopFailed ||
		!strings.Contains(w.s.Err().Error(), `fault: Load: crash: unknown node "n9"`) {
		t.Fatalf("stop %v err %v", stop, w.s.Err())
	}
}

// runA is run A of AT-FLT-14: W5 with seed and Random{Rules: R()} until 30s. It returns the
// world, the bytes of Applied() and the stop reason.
func runA(seed uint64, trace kernel.TraceConfig) (*world, string, kernel.StopReason) {
	w := newW5(kernel.Config{Seed: seed, Trace: trace}, false)
	r := &fault.Random{Rules: R()}
	r.Start(w.in.NewPlanContext(r, w.servers, nil, at(30*time.Second)))
	stop := w.s.RunUntil(at(30 * time.Second))
	var b strings.Builder
	if err := w.in.Applied().Write(&b); err != nil {
		panic(err)
	}
	return w, b.String(), stop
}

// AT-FLT-14
func TestReplayRoundTrip(t *testing.T) {
	wa, bA, _ := runA(7, kernel.TraceConfig{Level: kernel.TraceFull})
	if n := len(wa.in.Applied().Events); n <= 10 {
		t.Fatalf("run A applied only %d events", n)
	}
	for _, kind := range faultKinds {
		for _, r := range wa.records(kind) {
			if attr(r, "source") != "random" {
				t.Fatalf("run A: %s source %s", kind, attr(r, "source"))
			}
		}
	}
	sched, err := fault.ReadSchedule(strings.NewReader(bA))
	if err != nil {
		t.Fatal(err)
	}
	s := kernel.New(full(7))
	nw := simnet.New(s, simnet.DefaultConfig())
	d := simdisk.New(s, simdisk.Config{})
	in := fault.NewInjector(s, nw, d)
	if err := in.Replay(sched); err != nil || !in.Replaying() {
		t.Fatalf("Replay: %v", err)
	}
	for _, name := range []string{"n1", "n2", "n3", "n4", "n5"} {
		s.AddNode(name, func(*kernel.Node) {}, kernel.WithTags("server"))
	}
	s.RunUntil(at(30 * time.Second))
	if got := write(t, in.Applied()); got != bA {
		t.Fatalf("replayed Applied() differs from run A")
	}
	for _, r := range s.Records() {
		if r.Kind == "fault.target" || r.Kind == "fault.skip" {
			t.Fatalf("run B has %s", r.Kind)
		}
		if slices.Contains(faultKinds, r.Kind) && attr(r, "source") != "replay" {
			t.Fatalf("run B: %s source %s", r.Kind, attr(r, "source"))
		}
	}
}

// AT-FLT-15
func TestReplayMode(t *testing.T) {
	w := newW5(full(1), false)
	if err := w.in.Replay(fault.Schedule{}); err != nil {
		t.Fatal(err)
	}
	w.in.Inject(fault.Event{Kind: fault.KindCrash, Node: "n1"})
	sup := w.last(t, "fault.suppressed")
	if w.node(1).State() != kernel.NodeUp || len(w.in.Applied().Events) != 0 ||
		attrString(sup) != "source=inject, detail=crash n1" || sup.Text != "suppressed (replay): crash n1" || sup.Node != 0 {
		t.Fatalf("Inject in replay mode: %+v", sup)
	}
	w.in.Load(fault.Schedule{Events: []fault.Event{{At: at(time.Second), Kind: fault.KindCrash, Node: "n2"}}})
	if attrString(w.last(t, "fault.suppressed")) != "source=load, detail=load 1 events" {
		t.Fatalf("Load in replay mode: %s", attrString(w.last(t, "fault.suppressed")))
	}
	if _, ok := w.s.NextAt(); ok {
		t.Fatalf("Load in replay mode scheduled an event")
	}
	// every attempted injection gives one fault.suppressed (FLT-043)
	r := &fault.Random{Rules: R()}
	ctx := w.in.NewPlanContext(r, w.servers, nil, at(10*time.Second))
	inner, calls := ctx.Inject, 0
	ctx.Inject = func(e fault.Event) { calls++; inner(e) }
	r.Start(ctx)
	w.s.RunUntil(at(10 * time.Second))
	random := 0
	for _, s := range w.records("fault.suppressed") {
		if attr(s, "source") == "random" {
			random++
		}
	}
	if len(w.in.Applied().Events) != 0 || calls == 0 || random != calls || w.node(2).State() != kernel.NodeUp {
		t.Fatalf("replay mode: applied %d, %d ctx.Inject calls, %d suppressed from random", len(w.in.Applied().Events), calls, random)
	}
	mustPanic(t, `fault: Inject: unknown kind "x"`, func() { w.in.Inject(fault.Event{Kind: "x"}) })
	if err := w.in.Replay(fault.Schedule{}); err == nil || err.Error() != "fault: Replay: already replaying" {
		t.Fatalf("second Replay: %v", err)
	}
	// In replay mode, Load still panics on an invalid schedule (a Role included), before it records
	// anything, but does not check times, and Replay reports "already replaying" before any other
	// error.
	suppressed := len(w.records("fault.suppressed"))
	mustPanic(t, "fault: Load: schedule: events[1]: crash: node is required", func() {
		w.in.Load(fault.Schedule{Events: []fault.Event{{At: at(time.Second), Kind: fault.KindHeal}, {At: at(11 * time.Second), Kind: fault.KindCrash}}})
	})
	mustPanic(t, `fault: Load: schedule: events[0]: crash: role "leader" is not allowed in a concrete schedule`, func() {
		w.in.Load(fault.Schedule{Events: []fault.Event{{At: at(time.Second), Kind: fault.KindCrash, Role: "leader"}}})
	})
	if got := len(w.records("fault.suppressed")); got != suppressed {
		t.Fatalf("an invalid Load in replay mode emitted %d fault.suppressed records", got-suppressed)
	}
	w.in.Load(fault.Schedule{Events: []fault.Event{{At: at(time.Second), Kind: fault.KindHeal}}})
	if sup := w.last(t, "fault.suppressed"); attrString(sup) != "source=load, detail=load 1 events" {
		t.Fatalf("Load of a past event in replay mode: %s", attrString(sup))
	}
	if err := w.in.Replay(fault.Schedule{Events: []fault.Event{{At: at(time.Second), Kind: fault.KindCrash}}}); err == nil || err.Error() != "fault: Replay: already replaying" {
		t.Fatalf("second Replay of an invalid, past schedule: %v", err)
	}

	w = newW5(full(1), false)
	w.in.Inject(fault.Event{Kind: fault.KindHeal})
	if err := w.in.Replay(fault.Schedule{Events: []fault.Event{{At: at(time.Second), Kind: fault.KindCrash, Node: "n1"}}}); err != nil {
		t.Fatal(err)
	}
	w.s.RunUntil(at(time.Second))
	a := w.in.Applied().Events
	if len(a) != 2 || a[0].Kind != fault.KindHeal || a[1].ID != 2 || attr(w.last(t, "fault.crash"), "source") != "replay" {
		t.Fatalf("Applied() = %+v", a)
	}
}

// FLT-042: Replay errors have no side effects: no replay mode, no event, no record (the trace hash
// is unchanged).
func TestReplayErrors(t *testing.T) {
	w := newW5(kernel.Config{Seed: 1}, false)
	h := w.s.TraceHash()
	err := w.in.Replay(fault.Schedule{Events: []fault.Event{{Kind: fault.KindCrash}}})
	if err == nil || err.Error() != "fault: Replay: schedule: events[0]: crash: node is required" || w.in.Replaying() || w.s.TraceHash() != h {
		t.Fatalf("invalid schedule: %v", err)
	}
	w.s.RunUntil(at(2 * time.Second))
	h = w.s.TraceHash()
	for _, c := range []struct {
		events []fault.Event
		want   string
	}{
		{[]fault.Event{{At: at(time.Second), Kind: fault.KindHeal}}, "fault: Replay: schedule: events[0]: at 1s is before now (2s)"},
		// the first event in slice order with At < Now; an event at Now is allowed
		{[]fault.Event{{At: at(2 * time.Second), Kind: fault.KindHeal}, {At: at(2*time.Second - 1), Kind: fault.KindHeal}, {At: at(time.Second), Kind: fault.KindHeal}},
			"fault: Replay: schedule: events[1]: at 1.999999999s is before now (2s)"},
		// Normalize's error comes before the time check
		{[]fault.Event{{At: at(time.Second), Kind: fault.KindHeal}, {At: at(3 * time.Second), Kind: fault.KindCrash}},
			"fault: Replay: schedule: events[1]: crash: node is required"},
	} {
		err = w.in.Replay(fault.Schedule{Events: c.events})
		if err == nil || err.Error() != c.want || w.in.Replaying() {
			t.Fatalf("Replay(%v): %v", c.events, err)
		}
	}
	if _, ok := w.s.NextAt(); ok || w.s.TraceHash() != h || len(w.in.Applied().Events) != 0 {
		t.Fatalf("a failed Replay scheduled events, emitted records or applied events")
	}
}

// FLT-040 step 4, FLT-042: one kernel event per distinct At of the sorted schedule, ascending, with
// the method's label.
func TestLoadPerInstant(t *testing.T) {
	for _, label := range []string{"fault/load", "fault/replay"} {
		w := newW5(full(1), false)
		s := fault.Schedule{Events: []fault.Event{
			{At: at(2 * time.Second), Kind: fault.KindPause, Node: "n2"},
			{At: at(time.Second), Kind: fault.KindCrash, Node: "n1"},
			{At: at(2 * time.Second), Kind: fault.KindPause, Node: "n3"},
			{At: at(time.Second), Kind: fault.KindCrash, Node: "n4"},
		}}
		if label == "fault/load" {
			w.in.Load(s)
		} else if err := w.in.Replay(s); err != nil {
			t.Fatal(err)
		}
		w.s.RunUntil(at(2 * time.Second))
		var got []string
		for _, r := range w.records("kernel.event") {
			if strings.HasPrefix(r.Text, "fault/") {
				got = append(got, time.Duration(r.At).String()+" "+r.Text)
			}
		}
		if want := []string{"1s " + label, "2s " + label}; !slices.Equal(got, want) {
			t.Fatalf("kernel events %q, want %q", got, want)
		}
	}
}

// AT-FLT-25
func TestDeterminism(t *testing.T) {
	w1, b1, _ := runA(7, kernel.TraceConfig{})
	w2, b2, _ := runA(7, kernel.TraceConfig{})
	if w1.s.TraceHash() != w2.s.TraceHash() || b1 != b2 {
		t.Fatalf("seed 7 twice differs")
	}
	if _, b3, _ := runA(8, kernel.TraceConfig{}); b3 == b1 {
		t.Fatalf("seeds 7 and 8 applied the same schedule")
	}
	wf, _, _ := runA(7, kernel.TraceConfig{Level: kernel.TraceFull})
	if wf.s.TraceHash() != w1.s.TraceHash() {
		t.Fatalf("TraceFull changed the hash")
	}
	golden.Check(t, 7, func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		w, _, stop := runA(seed, trace)
		return w.s, stop
	})
}

// delivery is one handler call of AT-FLT-28's log L.
type delivery struct {
	at      kernel.Time
	from    kernel.NodeID
	payload any
}

// wt builds world WT of AT-FLT-28 and returns it with the log L and a func that adds n1 to n5
// and returns them.
func wt() (*kernel.Sim, *fault.Injector, *[]delivery, func() []*kernel.Node) {
	s := kernel.New(full(7))
	nw := simnet.New(s, simnet.DefaultConfig())
	d := simdisk.New(s, simdisk.Config{})
	in := fault.NewInjector(s, nw, d)
	var log []delivery
	add := func() []*kernel.Node {
		var servers []*kernel.Node
		for _, name := range []string{"n1", "n2", "n3", "n4", "n5"} {
			servers = append(servers, s.AddNode(name, func(n *kernel.Node) {
				nw.Handle(n, func(from kernel.NodeID, payload any) {
					log = append(log, delivery{s.Now(), from, payload})
				})
				var tick func()
				tick = func() {
					nw.Send(n, kernel.NodeID(1+n.Rand().IntN(5)), n.Rand().Uint64()) //nolint:gosec // in [1, 5]: a server's ID
					n.After(10*time.Millisecond, "tick", tick)
				}
				n.After(10*time.Millisecond, "tick", tick)
			}, kernel.WithTags("server")))
		}
		return servers
	}
	return s, in, &log, add
}

// netDisk returns the net.* and disk.* records without Seq and Cause.
func netDisk(s *kernel.Sim) []kernel.Record {
	var out []kernel.Record
	for _, r := range s.Records() {
		if strings.HasPrefix(r.Kind, "net.") || strings.HasPrefix(r.Kind, "disk.") {
			r.Seq, r.Cause = 0, 0
			out = append(out, r)
		}
	}
	return out
}

// AT-FLT-28
func TestReplayReproducesTheRun(t *testing.T) {
	sA, inA, logA, addA := wt()
	servers := addA()
	r := &fault.Random{Rules: R()}
	r.Start(inA.NewPlanContext(r, servers, nil, at(25*time.Second)))
	sA.RunUntil(at(30 * time.Second))

	sB, inB, logB, addB := wt()
	if err := inB.Replay(inA.Applied()); err != nil {
		t.Fatal(err)
	}
	addB()
	sB.RunUntil(at(30 * time.Second))

	if len(*logA) < 1000 || !slices.Equal(*logA, *logB) {
		t.Fatalf("logs differ: %d vs %d deliveries", len(*logA), len(*logB))
	}
	if a, b := netDisk(sA), netDisk(sB); !reflect.DeepEqual(a, b) {
		t.Fatalf("net/disk records differ: %d vs %d", len(a), len(b))
	}
	if !reflect.DeepEqual(inA.Applied(), inB.Applied()) {
		t.Fatalf("Applied() differs")
	}
}
