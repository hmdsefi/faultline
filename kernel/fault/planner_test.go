// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package fault_test

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// testPlanner is a planner with a given name whose Start calls start (if not nil).
type testPlanner struct {
	name  string
	start func(ctx *fault.PlanContext)
}

func (p testPlanner) Name() string { return p.name }

func (p testPlanner) Start(ctx *fault.PlanContext) {
	if p.start != nil {
		p.start(ctx)
	}
}

// AT-FLT-23
func TestScript(t *testing.T) {
	w := newW5(full(1), false)
	events := []fault.Event{
		{At: at(2 * time.Second), Kind: fault.KindRestart, Node: "n1"},
		{At: at(time.Second), Kind: fault.KindCrash, Role: "leader"},
		{At: at(time.Second), Kind: fault.KindPause, Node: "n2"},
	}
	p := fault.Script(events...)
	events[2].Node = "n3" // Script copied its input
	roles := fault.Roles{"leader": func() []kernel.NodeID { return []kernel.NodeID{1} }}
	p.Start(w.in.NewPlanContext(p, w.servers, roles, at(10*time.Second)))
	w.s.RunUntil(at(time.Second))
	if w.node(1).State() != kernel.NodeDown || w.node(2).State() != kernel.NodePaused {
		t.Fatalf("at 1s: n1 %v, n2 %v", w.node(1).State(), w.node(2).State())
	}
	crash, pause := w.last(t, "fault.crash"), w.last(t, "fault.pause")
	if crash.Seq >= pause.Seq {
		t.Fatalf("crash n1 must be applied before pause n2 (schedule order within one instant)")
	}
	w.s.RunUntil(at(2 * time.Second))
	if w.node(1).State() != kernel.NodeUp {
		t.Fatalf("at 2s n1 is %v", w.node(1).State())
	}
	a := w.in.Applied().Events
	if len(a) != 3 || a[0].Node != "n1" || a[0].Role != "" || a[1].Node != "n2" || a[2].Kind != fault.KindRestart {
		t.Fatalf("Applied() = %+v", a)
	}
	tr := w.last(t, "fault.target")
	if attrString(tr) != "planner=script, event=1, role=leader, candidates=n1, chosen=n1" || tr.Text != "target script events[1] crash -> n1" || tr.Node != 0 {
		t.Fatalf("fault.target %q %s, node %d", tr.Text, attrString(tr), tr.Node)
	}
	if attr(crash, "source") != "script" {
		t.Fatalf("source = %s", attr(crash, "source"))
	}
	labels := 0
	for _, r := range w.records("kernel.event") {
		if r.Text == "fault/script" {
			labels++
		}
	}
	if labels != 2 {
		t.Fatalf("%d fault/script events, want 2 (one per distinct At)", labels)
	}

	w = newW5(kernel.Config{Seed: 1}, false)
	bad := fault.Script(fault.Event{At: 0, Kind: fault.KindCrash, Node: "n9"})
	mustPanic(t, `fault: Script: events[0]: unknown node "n9"`, func() { bad.Start(w.in.NewPlanContext(bad, w.servers, nil, 0)) })
	w.s.RunUntil(at(2 * time.Second))
	late := fault.Script(fault.Event{At: at(time.Second), Kind: fault.KindCrash, Node: "n1"})
	mustPanic(t, "fault: Script: events[0]: at 1s is before now (2s)", func() { late.Start(w.in.NewPlanContext(late, w.servers, nil, 0)) })
	norole := fault.Script(fault.Event{At: at(3 * time.Second), Kind: fault.KindCrash, Role: "leader"})
	mustPanic(t, `fault: Script: events[0]: unknown role "leader"`, func() { norole.Start(w.in.NewPlanContext(norole, w.servers, nil, 0)) })
	nilrole := fault.Roles{"leader": nil} // present, but ctx.Roles["leader"] == nil
	mustPanic(t, `fault: Script: events[0]: unknown role "leader"`, func() { norole.Start(w.in.NewPlanContext(norole, w.servers, nilrole, 0)) })
	invalid := fault.Script(fault.Event{At: at(3 * time.Second), Kind: fault.KindCrash})
	mustPanic(t, "fault: Script: events[0]: crash: node is required", func() { invalid.Start(w.in.NewPlanContext(invalid, w.servers, nil, 0)) })
	// two checks fail at once: the earlier FLT-090 check wins, for the first failing event in slice
	// order (not At order), and the first unknown name in the order Node, Peer, Groups
	for _, c := range []struct {
		events []fault.Event
		want   string
	}{
		{[]fault.Event{{At: at(3 * time.Second), Kind: fault.KindCrash, Role: "x", Peer: "n2"}}, "fault: Script: events[0]: crash: peer is not allowed"},
		{[]fault.Event{{At: at(3 * time.Second), Kind: fault.KindCut, Role: "x", Peer: "zz"}}, `fault: Script: events[0]: unknown role "x"`},
		{[]fault.Event{{At: at(time.Second), Kind: fault.KindCrash, Node: "n9"}}, `fault: Script: events[0]: unknown node "n9"`},
		{[]fault.Event{{At: at(time.Second), Kind: fault.KindCut, Node: "n1", Peer: "zz"}}, `fault: Script: events[0]: unknown node "zz"`},
		{[]fault.Event{{At: at(time.Second), Kind: fault.KindPartition, Groups: [][]string{{"n1", "y1"}, {"x2"}}}}, `fault: Script: events[0]: unknown node "y1"`},
		{[]fault.Event{{At: at(time.Second), Kind: fault.KindCrash, Node: "n1"}, {At: at(3 * time.Second), Kind: fault.KindCrash, Node: "n9"}}, "fault: Script: events[0]: at 1s is before now (2s)"},
		{[]fault.Event{{At: at(3 * time.Second), Kind: fault.KindCrash, Node: "n9"}, {At: at(time.Second), Kind: fault.KindCrash, Node: "n1"}}, `fault: Script: events[0]: unknown node "n9"`},
		{[]fault.Event{{At: at(3 * time.Second), Kind: fault.KindCrash, Node: "n1"}, {At: at(4 * time.Second), Kind: fault.KindCrash, Node: "n9"}}, `fault: Script: events[1]: unknown node "n9"`},
	} {
		sc := fault.Script(c.events...)
		mustPanic(t, c.want, func() { sc.Start(w.in.NewPlanContext(sc, w.servers, nil, 0)) })
	}
	if _, pending := w.s.NextAt(); pending {
		t.Fatalf("a Start that panicked scheduled events")
	}
	now := fault.Script(fault.Event{At: at(2 * time.Second), Kind: fault.KindCrash, Node: "n2"})
	now.Start(w.in.NewPlanContext(now, w.servers, nil, 0))
	w.s.RunUntil(at(2 * time.Second))
	if w.node(2).State() != kernel.NodeDown {
		t.Fatalf("an event at Now() did not run")
	}
	if fault.Script().Name() != "script" || fault.None().Name() != "none" {
		t.Fatalf("planner names")
	}
}

// FLT-090: Script copies its events deeply at call time.
func TestScriptCopies(t *testing.T) {
	w := newW5(full(1), false)
	l := simnet.Link{Latency: time.Millisecond}
	events := []fault.Event{
		{At: at(time.Second), Kind: fault.KindLink, Node: "n1", Peer: "n2", Link: &l},
		{At: at(time.Second), Kind: fault.KindPartition, Groups: [][]string{{"n1", "n2"}, {"n3"}}},
	}
	p := fault.Script(events...)
	l.Latency = time.Hour
	events[1].Groups[0][1] = "n3"
	p.Start(w.in.NewPlanContext(p, w.servers, nil, 0))
	w.s.RunUntil(at(time.Second))
	a := w.in.Applied().Events
	if w.nw.Link(1, 2).Latency != time.Millisecond || !reflect.DeepEqual(a[1].Groups, [][]string{{"n1", "n2"}, {"n3"}}) {
		t.Fatalf("Script did not copy Link and Groups: %+v, %v", w.nw.Link(1, 2), a[1].Groups)
	}
}

// FLT-090: Start leaves the script unchanged, so one Script value gives the same run each time
// it is started (the test API starts a planner once per attempt).
func TestScriptReuse(t *testing.T) {
	p := fault.Script(fault.Event{At: at(time.Second), Kind: fault.KindPause, Role: "any"})
	roles := fault.Roles{"any": func() []kernel.NodeID { return []kernel.NodeID{1, 2, 3} }}
	var hashes [2]uint64
	for run := range hashes {
		w := newW5(full(1), false)
		p.Start(w.in.NewPlanContext(p, w.servers, roles, 0))
		w.s.RunUntil(at(time.Second))
		if n := len(w.records("fault.target")); n != 1 {
			t.Fatalf("run %d: %d fault.target records, want 1", run, n)
		}
		hashes[run] = w.s.TraceHash()
	}
	if hashes[0] != hashes[1] {
		t.Fatalf("trace hashes %x and %x: a second Start of the same Script changed the run", hashes[0], hashes[1])
	}
}

// FLT-060, FLT-080: a role's IDs are sorted and deduplicated, the role function is called once per
// decision and its slice is left alone, and each pick draws exactly one IntN from the planner's
// stream, also for a single candidate.
func TestScriptPicks(t *testing.T) {
	w := newW5(full(1), false)
	ids := []kernel.NodeID{3, 1, 2, 3}
	calls := 0
	roles := fault.Roles{
		"three": func() []kernel.NodeID { calls++; return ids },
		"one":   func() []kernel.NodeID { return []kernel.NodeID{4} },
	}
	p := fault.Script(
		fault.Event{At: at(time.Second), Kind: fault.KindPause, Role: "three"},
		fault.Event{At: at(time.Second), Kind: fault.KindPause, Role: "one"},
		fault.Event{At: at(time.Second), Kind: fault.KindPause, Role: "three"},
	)
	p.Start(w.in.NewPlanContext(p, w.servers, roles, 0))
	if calls != 0 {
		t.Fatalf("Start called a role function: roles resolve when the event fires")
	}
	w.s.RunUntil(at(time.Second))
	ref := kernel.New(kernel.Config{Seed: 1}).Rand("fault/script")
	three, one := []string{"n1", "n2", "n3"}, []string{"n4"}
	first := three[ref.IntN(len(three))]
	_ = one[ref.IntN(len(one))]
	third := three[ref.IntN(len(three))]
	if third == three[0] {
		t.Fatalf("seed 1 no longer makes the third pick choose a node other than the first candidate")
	}
	tr := w.records("fault.target")
	if len(tr) != 3 || attrString(tr[0]) != "planner=script, event=0, role=three, candidates=n1,n2,n3, chosen="+first ||
		attrString(tr[1]) != "planner=script, event=1, role=one, candidates=n4, chosen=n4" ||
		attrString(tr[2]) != "planner=script, event=2, role=three, candidates=n1,n2,n3, chosen="+third {
		t.Fatalf("fault.target records %v", tr)
	}
	if calls != 2 || !reflect.DeepEqual(ids, []kernel.NodeID{3, 1, 2, 3}) {
		t.Fatalf("role function called %d times for 2 decisions, its slice is now %v", calls, ids)
	}
	if w.s.Rand("fault/script").Uint64() != ref.Uint64() {
		t.Fatalf("the picks did not draw exactly one IntN each from the planner's stream")
	}
	// a hand-built context with another stream: the pick draws from ctx.Rand (FLT-052)
	w = newW5(full(1), false)
	q := fault.Script(fault.Event{At: at(time.Second), Kind: fault.KindPause, Role: "three"})
	ctx := w.in.NewPlanContext(q, w.servers, roles, 0)
	ctx.Rand, ref = kernel.New(kernel.Config{Seed: 99}).Rand("x"), kernel.New(kernel.Config{Seed: 99}).Rand("x")
	q.Start(ctx)
	w.s.RunUntil(at(time.Second))
	if got := attr(w.last(t, "fault.target"), "chosen"); got != three[ref.IntN(len(three))] || ctx.Rand.Uint64() != ref.Uint64() {
		t.Fatalf("the pick did not draw from ctx.Rand: chosen %s", got)
	}
}

// FLT-006, FLT-090: Script applies the events of an instant in input order, stably sorted by At
// (40 events, more than an insertion sort covers), in one AtFront event per instant, before the
// seeded events of that instant.
func TestScriptOrder(t *testing.T) {
	w := newW5(full(1), false)
	ats := []time.Duration{3, 0, 3, 2, 0, 3, 1, 0, 2, 3, 0, 1, 3, 0, 2, 2, 0, 3, 1, 1, 0, 3, 2, 0, 1, 3, 0, 0, 2, 3, 1, 0, 0, 3, 3, 2, 1, 0, 1, 3}
	var events []fault.Event
	for i, a := range ats {
		events = append(events, fault.Event{At: at(a * time.Second), Kind: fault.KindClockJump, Node: "n1", N: int64(i + 1)})
	}
	for range 20 {
		w.s.At(at(2*time.Second), "seeded", func() {})
	}
	p := fault.Script(events...)
	p.Start(w.in.NewPlanContext(p, w.servers, nil, 0))
	w.s.RunUntil(at(3 * time.Second))
	var got, want []int64
	for _, e := range w.in.Applied().Events {
		got = append(got, e.N)
	}
	for _, a := range []time.Duration{0, 1, 2, 3} {
		for i, b := range ats {
			if b == a {
				want = append(want, int64(i+1))
			}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("applied order\n got %v\nwant %v", got, want)
	}
	for _, r := range w.records("kernel.event") {
		if r.At == at(2*time.Second) {
			if r.Text != "fault/script" {
				t.Fatalf("the first event at 2s is %q, want fault/script before the seeded events", r.Text)
			}
			break
		}
	}
	// FLT §6: Start calls AtFront once per distinct At, ascending, so the kernel event IDs rise
	prev := 0
	for _, r := range w.records("kernel.event") {
		if r.Text != "fault/script" {
			continue
		}
		id, _ := strconv.Atoi(attr(r, "id"))
		if id <= prev {
			t.Fatalf("fault/script event %d runs after event %d: AtFront calls not in ascending At", id, prev)
		}
		prev = id
	}
}

// FLT-060, FLT-090: empty roles skip; unknown IDs panic inside the event; Until is ignored.
func TestScriptRoles(t *testing.T) {
	w := newW5(full(1), false)
	roles := fault.Roles{
		"none":  func() []kernel.NodeID { return nil },
		"ghost": func() []kernel.NodeID { return []kernel.NodeID{1, 9} },
	}
	p := fault.Script(
		fault.Event{At: at(time.Second), Kind: fault.KindPause, Role: "none"},
		fault.Event{At: at(3 * time.Second), Kind: fault.KindPause, Node: "n4"},
		fault.Event{At: at(4 * time.Second), Kind: fault.KindCrash, Role: "ghost"},
		fault.Event{At: at(time.Second), Kind: fault.KindPause, Node: "n5"},
	)
	p.Start(w.in.NewPlanContext(p, w.servers, roles, at(2*time.Second)))
	w.s.RunUntil(at(3 * time.Second))
	sk := w.last(t, "fault.skip")
	if attrString(sk) != "planner=script, event=0, kind=pause, reason=no-target" || sk.Text != "skip script events[0] pause: no-target" {
		t.Fatalf("fault.skip %q %s", sk.Text, attrString(sk))
	}
	if w.node(4).State() != kernel.NodePaused {
		t.Fatalf("an event after Until did not run")
	}
	if w.node(5).State() != kernel.NodePaused {
		t.Fatalf("a skip ended the other events of its instant")
	}
	if stop := w.s.RunUntil(at(5 * time.Second)); stop != kernel.StopFailed ||
		!strings.Contains(w.s.Err().Error(), `fault: Script: events[2]: role "ghost" returned unknown node id 9`) {
		t.Fatalf("stop %v err %v", stop, w.s.Err())
	}
	if w.s.Rand("fault/script").Uint64() != kernel.New(kernel.Config{Seed: 1}).Rand("fault/script").Uint64() {
		t.Fatalf("the skipped event drew from the planner's stream")
	}
}

// FLT-090: Script resolves roles with no eligibility filter, so a role may name a down node.
func TestScriptRoleDownNode(t *testing.T) {
	w := newW5(full(1), false)
	roles := fault.Roles{"crashed": func() []kernel.NodeID { return []kernel.NodeID{2} }}
	p := fault.Script(
		fault.Event{At: at(time.Second), Kind: fault.KindCrash, Node: "n2"},
		fault.Event{At: at(2 * time.Second), Kind: fault.KindRestart, Role: "crashed"},
	)
	p.Start(w.in.NewPlanContext(p, w.servers, roles, 0))
	w.s.RunUntil(at(2 * time.Second))
	if w.node(2).State() != kernel.NodeUp || attr(w.last(t, "fault.target"), "chosen") != "n2" {
		t.Fatalf("restart @crashed: n2 is %v", w.node(2).State())
	}
}

// AT-FLT-26, FLT-050, FLT-091
func TestNewPlanContext(t *testing.T) {
	w := newW5(full(1), false)
	n1, n2, n3 := w.node(1), w.node(2), w.node(3)
	roles := fault.Roles{"a": func() []kernel.NodeID { return nil }}
	recs := len(w.s.Records())
	servers := []*kernel.Node{n3, n1, n2}
	ctx := w.in.NewPlanContext(fault.None(), servers, roles, at(10*time.Second))
	if len(ctx.Servers) != 3 || ctx.Servers[0] != n1 || ctx.Servers[1] != n2 || ctx.Servers[2] != n3 {
		t.Fatalf("Servers = %v", ctx.Servers)
	}
	if servers[0] != n3 || servers[1] != n1 {
		t.Fatalf("NewPlanContext sorted the caller's slice")
	}
	if ctx.Sim != w.s || ctx.Rand != w.s.Rand("fault/none") || ctx.Until != at(10*time.Second) || ctx.Disks != w.d {
		t.Fatalf("PlanContext fields")
	}
	roles["b"] = roles["a"]
	if len(ctx.Roles) != 2 || ctx.Roles["a"] == nil || ctx.Roles["b"] == nil {
		t.Fatalf("ctx.Roles is not roles")
	}
	if len(w.s.Records()) != recs {
		t.Fatalf("NewPlanContext emitted records")
	}
	fault.None().Start(ctx)
	if _, pending := w.s.NextAt(); pending || len(w.s.Records()) != recs || ctx.Rand.Uint64() != kernel.New(kernel.Config{Seed: 1}).Rand("fault/none").Uint64() {
		t.Fatalf("NewPlanContext or None().Start scheduled an event, emitted a record or drew from the stream")
	}
	ctx.Inject(fault.Event{Kind: fault.KindHeal})
	if attr(w.last(t, "fault.heal"), "source") != "none" {
		t.Fatalf("source of a planner injection")
	}
	for _, name := range []string{"Bad", "load", "", "inject", "replay", "-x", "a_b", "a/b", "a:b", "a`b", "a{b"} {
		mustPanic(t, `fault: NewPlanContext: invalid planner name "`+name+`"`,
			func() { w.in.NewPlanContext(testPlanner{name: name}, nil, nil, 0) })
	}
	for _, name := range []string{"p-2", "2p", "0", "az-09"} {
		if ctx := w.in.NewPlanContext(testPlanner{name: name}, nil, nil, 0); ctx.Rand != w.s.Rand("fault/"+name) {
			t.Fatalf("stream of planner %s", name)
		}
	}
}

// AT-FLT-27
func TestPlannerInjectionPrefix(t *testing.T) {
	w := newW5(kernel.Config{Seed: 1}, false)
	crash := func(ctx *fault.PlanContext) { ctx.Inject(fault.Event{Kind: fault.KindCrash, Node: "zz"}) }
	p := testPlanner{name: "p", start: crash}
	mustPanic(t, `fault: planner p: crash: unknown node "zz"`, func() { p.Start(w.in.NewPlanContext(p, w.servers, nil, 0)) })
	hand := &fault.PlanContext{Sim: w.s, Rand: w.s.Rand("fault/p"), Inject: w.in.Inject, Servers: w.servers}
	mustPanic(t, `fault: Inject: crash: unknown node "zz"`, func() { p.Start(hand) })
}
