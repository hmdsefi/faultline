// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package fault_test

import (
	"math/rand/v2"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

type captured struct {
	at kernel.Time
	e  fault.Event
}

// AT-FLT-16
func TestRandomGoldenRun(t *testing.T) {
	w := newW5(full(1), false)
	var got []captured
	ctx := &fault.PlanContext{
		Sim:     w.s,
		Rand:    rand.New(rand.NewPCG(1, 2)), //nolint:gosec // fixed seed: the golden table below depends on it
		Inject:  func(e fault.Event) { got = append(got, captured{w.s.Now(), e}) },
		Servers: w.servers,
		Until:   at(10 * time.Second),
	}
	r := &fault.Random{Rules: []fault.Rule{
		{Kind: fault.KindCrash, Every: 2 * time.Second, MinFor: 500 * time.Millisecond, MaxFor: time.Second},
		{Kind: fault.KindPartition, Every: 3 * time.Second, MinFor: time.Second, MaxFor: 2 * time.Second},
		{Kind: fault.KindClockJump, Every: 4 * time.Second, Magnitude: int64(100 * time.Millisecond)},
		{Kind: fault.KindPause, Every: 5 * time.Second},
		{Kind: fault.KindIsolate, Every: 3 * time.Second, MinFor: time.Second, MaxFor: 3 * time.Second},
	}}
	// FLT-082, FLT-084, FLT-086: starts, undos and the recovery run before the seeded events of
	// their instant. These seeded events draw from kernel/sched only, not from ctx.Rand.
	firsts := []struct {
		at    kernel.Time
		label string
	}{{at(2202828812), "fault/random/start"}, {at(3130612127), "fault/random/undo"}, {at(10 * time.Second), "fault/random/recover"}}
	for _, f := range firsts {
		for range 20 {
			w.s.At(f.at, "seeded", func() {})
		}
	}
	r.Start(ctx)
	w.s.RunUntil(at(12 * time.Second))
	if r.RecoverAt() != at(10*time.Second) {
		t.Fatalf("RecoverAt() = %v", r.RecoverAt())
	}
	for _, f := range firsts {
		for _, rec := range w.records("kernel.event") {
			if rec.At == f.at {
				if rec.Text != f.label {
					t.Fatalf("the first event at %v is %q, want %q", time.Duration(f.at), rec.Text, f.label)
				}
				break
			}
		}
	}
	ev := func(k fault.Kind, node string) fault.Event { return fault.Event{Kind: k, Node: node} }
	jump := func(node string, n int64) fault.Event {
		return fault.Event{Kind: fault.KindClockJump, Node: node, N: n}
	}
	want := []struct {
		at time.Duration
		e  fault.Event
	}{
		{2202828812, ev(fault.KindIsolate, "n1")},
		{2538746539, ev(fault.KindCrash, "n1")},
		{3130612127, ev(fault.KindRestart, "n1")},
		{4202652323, ev(fault.KindHeal, "")},
		{4862579318, ev(fault.KindCrash, "n5")},
		{5050980445, ev(fault.KindIsolate, "n4")},
		{5137712007, jump("n1", -33033051)},
		{5423303534, ev(fault.KindRestart, "n5")},
		{6482982893, ev(fault.KindPause, "n1")},
		{6655252650, ev(fault.KindCrash, "n4")},
		{7512619916, ev(fault.KindHeal, "")},
		{7524360106, ev(fault.KindRestart, "n4")},
		{8076740629, ev(fault.KindIsolate, "n5")},
		{9301255693, ev(fault.KindCrash, "n3")},
		{9699656167, jump("n5", -69909693)},
		{9804973601, ev(fault.KindRestart, "n3")},
		{9998283686, ev(fault.KindHeal, "")},
		{10 * time.Second, ev(fault.KindResume, "n1")},
	}
	if len(got) != len(want) {
		for _, c := range got {
			t.Logf("%v %v", time.Duration(c.at), c.e)
		}
		t.Fatalf("%d injections, want %d", len(got), len(want))
	}
	for i, c := range got {
		if c.at != at(want[i].at) || c.e.Kind != want[i].e.Kind || c.e.Node != want[i].e.Node || c.e.N != want[i].e.N {
			t.Errorf("injection %d: %v %v, want %v %v", i+1, time.Duration(c.at), c.e, want[i].at, want[i].e)
		}
	}
	skips := w.records("fault.skip")
	wantSkips := []time.Duration{3349308671, 6183675581, 8305583106}
	if len(skips) != 3 {
		t.Fatalf("%d fault.skip records, want 3", len(skips))
	}
	for i, s := range skips {
		if s.At != at(wantSkips[i]) || attrString(s) != "planner=random, rule=1, kind=partition, reason=network-busy" {
			t.Errorf("skip %d at %v: %s", i, time.Duration(s.At), attrString(s))
		}
	}
	rec := w.records("fault.recover")
	if len(rec) != 1 || rec[0].At != at(10*time.Second) || attrString(rec[0]) != "planner=random, active=1" || rec[0].Text != "recover random: 1 active" {
		t.Fatalf("fault.recover records %+v", rec)
	}
}

// R is the rule set of AT-FLT-14: one rule of every non-corrupt rule kind.
func R() []fault.Rule {
	ms := time.Millisecond
	rule := func(k fault.Kind) fault.Rule {
		return fault.Rule{Kind: k, Every: time.Second, MinFor: 200 * ms, MaxFor: 2 * time.Second}
	}
	rules := []fault.Rule{
		rule(fault.KindPartition), rule(fault.KindIsolate), rule(fault.KindCut), rule(fault.KindLink),
		rule(fault.KindCrash), rule(fault.KindPause),
		{Kind: fault.KindClockJump, Every: time.Second, Magnitude: int64(50 * ms)},
		{Kind: fault.KindClockDrift, Every: time.Second, Magnitude: 100},
		rule(fault.KindSyncFail), rule(fault.KindDiskCapacity),
	}
	rules[3].Link = &simnet.Link{DropPPM: 100000}
	rules[8].Magnitude = 1
	rules[9].Magnitude = 4096
	return rules
}

// startRandom starts r on w with real injection and until.
func startRandom(w *world, r *fault.Random, roles fault.Roles, until time.Duration) {
	r.Start(w.in.NewPlanContext(r, w.servers, roles, at(until)))
}

// AT-FLT-18
func TestMaxDown(t *testing.T) {
	for _, maxDown := range []int{0, 1} {
		bound := 2
		if maxDown == 1 {
			bound = 1
		}
		skipped := false
		for seed := uint64(1); seed <= 20; seed++ {
			w := newW5(full(seed), false)
			w.s.OnEvent(func() {
				down := 0
				for _, n := range w.servers {
					if st := n.State(); st == kernel.NodeDown || st == kernel.NodePaused {
						down++
					}
				}
				if down > bound {
					t.Errorf("MaxDown %d seed %d: %d servers down or paused at %v", maxDown, seed, down, w.s.Now())
				}
			})
			r := &fault.Random{MaxDown: maxDown, Rules: []fault.Rule{
				{Kind: fault.KindCrash, Every: 200 * time.Millisecond, MinFor: time.Second, MaxFor: 3 * time.Second},
				{Kind: fault.KindPause, Every: 300 * time.Millisecond, MinFor: time.Second, MaxFor: 2 * time.Second},
			}}
			startRandom(w, r, nil, 60*time.Second)
			w.s.RunUntil(at(60 * time.Second))
			for _, s := range w.records("fault.skip") {
				if attr(s, "reason") == "max-down" {
					skipped = true
				}
			}
		}
		if !skipped {
			t.Errorf("MaxDown %d: no max-down skip in 20 seeds", maxDown)
		}
	}
}

// AT-FLT-19
func TestRecovery(t *testing.T) {
	undoKinds := map[string]bool{"fault.heal": true, "fault.heal_link": true, "fault.link_reset": true, "fault.resume": true, "fault.restart": true}
	for seed := uint64(1); seed <= 20; seed++ {
		w := newW5(full(seed), false)
		r := &fault.Random{Rules: R()}
		startRandom(w, r, nil, 25*time.Second)
		w.s.RunUntil(at(30 * time.Second))
		if r.RecoverAt() != at(25*time.Second) {
			t.Fatalf("seed %d: RecoverAt() = %v", seed, r.RecoverAt())
		}
		for _, n := range w.servers {
			if n.State() != kernel.NodeUp {
				t.Fatalf("seed %d: %s is %v after recovery", seed, n.Name(), n.State())
			}
			f, err := w.d.Volume(n).Create("/s")
			if err != nil {
				t.Fatal(err)
			}
			if err := f.Sync(); err != nil {
				t.Fatalf("seed %d: Sync on %s: %v", seed, n.Name(), err)
			}
		}
		for a := kernel.NodeID(1); a <= 5; a++ {
			for b := kernel.NodeID(1); b <= 5; b++ {
				if !w.nw.Connected(a, b) || w.nw.Link(a, b) != w.nw.Config().Default {
					t.Fatalf("seed %d: link %d -> %d not restored", seed, a, b)
				}
			}
		}
		var recoverSeq uint64
		for _, rec := range w.s.Records() {
			if rec.Kind == "fault.recover" {
				recoverSeq = rec.Seq
			}
			if len(rec.Kind) < 6 || rec.Kind[:6] != "fault." || rec.At < at(25*time.Second) || rec.Kind == "fault.recover" {
				continue
			}
			isZero := (rec.Kind == "fault.sync_fail" || rec.Kind == "fault.disk_capacity") && attr(rec, "n") == "0"
			if rec.At != at(25*time.Second) || recoverSeq == 0 || (!undoKinds[rec.Kind] && !isZero) {
				t.Fatalf("seed %d: unexpected %s at %v: %s", seed, rec.Kind, time.Duration(rec.At), attrString(rec))
			}
		}
	}

	w := newW5(full(1), false)
	w.s.RunUntil(at(3 * time.Second))
	r := &fault.Random{Rules: R()}
	startRandom(w, r, nil, 2*time.Second)
	w.s.RunUntil(at(30 * time.Second))
	if r.RecoverAt() != at(3*time.Second) || len(w.records("fault.target"))+len(w.records("fault.skip")) != 0 || len(w.in.Applied().Events) != 0 {
		t.Fatalf("a rule fired although Until was in the past")
	}
}

// AT-FLT-20
func TestGapsAndNetworkExclusivity(t *testing.T) {
	busy := false
	for seed := uint64(1); seed <= 20; seed++ {
		w := newW5(full(seed), false)
		startRandom(w, &fault.Random{Rules: []fault.Rule{{Kind: fault.KindCrash, Every: time.Second, MinFor: time.Millisecond, MaxFor: time.Millisecond}}}, nil, 120*time.Second)
		w.s.RunUntil(at(120 * time.Second))
		prev := kernel.Time(0)
		n := 0
		for _, rec := range w.s.Records() {
			if (rec.Kind == "fault.target" || rec.Kind == "fault.skip") && attr(rec, "rule") == "0" {
				if gap := rec.At - prev; gap < at(500*time.Millisecond) || gap > at(1500*time.Millisecond) {
					t.Fatalf("seed %d: gap %v", seed, time.Duration(gap))
				}
				prev = rec.At
				n++
			}
		}
		if n < 60 {
			t.Fatalf("seed %d: only %d occurrences", seed, n)
		}

		w = newW5(full(seed), false)
		rule := func(k fault.Kind) fault.Rule {
			return fault.Rule{Kind: k, Every: 500 * time.Millisecond, MinFor: 100 * time.Millisecond, MaxFor: 2 * time.Second}
		}
		startRandom(w, &fault.Random{Rules: []fault.Rule{rule(fault.KindPartition), rule(fault.KindIsolate), rule(fault.KindCut)}}, nil, 120*time.Second)
		w.s.RunUntil(at(120 * time.Second))
		active := map[int]bool{}
		for _, e := range w.in.Applied().Events {
			for _, id := range e.Undoes {
				delete(active, id)
			}
			if e.Kind == fault.KindPartition || e.Kind == fault.KindIsolate || e.Kind == fault.KindCut {
				active[e.ID] = true
			}
			if len(active) > 1 {
				t.Fatalf("seed %d: %d network faults active at %v", seed, len(active), time.Duration(e.At))
			}
		}
		for _, s := range w.records("fault.skip") {
			if attr(s, "reason") == "network-busy" {
				busy = true
			}
		}
	}
	if !busy {
		t.Fatalf("no network-busy skip")
	}
}

// FLT-084: a network rule that starts exactly when another network fault ends, before the undo of
// that fault has run (KRN-027 runs the AtFront entries of one instant in insertion order), is
// skipped as network-busy, for every network kind and only while a network fault is active: network
// faults never overlap, every undo ends its own fault, and a link (not a network fault) never makes
// the network busy.
func TestRandomNetworkTie(t *testing.T) {
	ties := 0
	for seed := uint64(1); seed <= 3; seed++ {
		w := newW5(full(seed), false)
		rule := func(k fault.Kind) fault.Rule { return fault.Rule{Kind: k, Every: 20, MinFor: 1, MaxFor: 10} }
		link := rule(fault.KindLink)
		link.Link = &simnet.Link{DropPPM: 1}
		rules := []fault.Rule{rule(fault.KindPartition), rule(fault.KindIsolate), rule(fault.KindCut), link}
		startRandom(w, &fault.Random{Rules: rules}, nil, 50000)
		w.s.RunUntil(50000)
		active := false // a network fault of the planner is active
		var skipAt kernel.Time = -1
		for _, rec := range w.s.Records() {
			switch rec.Kind {
			case "fault.partition", "fault.isolate", "fault.cut":
				if active {
					t.Fatalf("seed %d: %s at %v while a network fault is active", seed, rec.Kind, time.Duration(rec.At))
				}
				active = true
			case "fault.heal", "fault.heal_link":
				if u := attr(rec, "undoes"); u == "" || strings.Contains(u, ",") {
					t.Fatalf("seed %d: %s at %v undoes %q, want exactly one fault", seed, rec.Kind, time.Duration(rec.At), u)
				}
				active = false
				if rec.At == skipAt {
					ties++
				}
			case "fault.skip":
				if attr(rec, "reason") == "network-busy" {
					if !active {
						t.Fatalf("seed %d: network-busy skip at %v while no network fault is active", seed, time.Duration(rec.At))
					}
					skipAt = rec.At
				}
			}
		}
	}
	if ties == 0 {
		t.Fatalf("no start at the instant a network fault ended")
	}
}

// AT-FLT-21
func TestReuseAndDoubleStart(t *testing.T) {
	r := &fault.Random{Rules: R()}
	run := func() string {
		w := newW5(kernel.Config{Seed: 7}, false)
		startRandom(w, r, nil, 25*time.Second)
		w.s.RunUntil(at(30 * time.Second))
		return write(t, w.in.Applied())
	}
	a, b := run(), run()
	if a != b || len(a) < 200 {
		t.Fatalf("reused Random differs (%d vs %d bytes)", len(a), len(b))
	}
	w := newW5(kernel.Config{Seed: 7}, false)
	ctx := w.in.NewPlanContext(r, w.servers, nil, at(25*time.Second))
	r.Start(ctx)
	mustPanic(t, "fault: Random: started twice on the same simulation", func() { r.Start(ctx) })
	if (&fault.Random{}).Name() != "random" {
		t.Fatalf("Name()")
	}
}

// AT-FLT-22
func TestRoles(t *testing.T) {
	roles := fault.Roles{
		"leader": func() []kernel.NodeID { return []kernel.NodeID{3, 1, 3} },
		"none":   func() []kernel.NodeID { return nil },
		"client": func() []kernel.NodeID { return []kernel.NodeID{6} },
		"ghost":  func() []kernel.NodeID { return []kernel.NodeID{1, 9} },
	}
	crashRule := func(target string) fault.Rule {
		return fault.Rule{Kind: fault.KindCrash, Every: time.Second, MinFor: time.Second, MaxFor: time.Second, Target: target}
	}

	w := newW5(full(1), true)
	startRandom(w, &fault.Random{Rules: []fault.Rule{crashRule("leader")}}, roles, 10*time.Second)
	w.s.RunUntil(at(1500 * time.Millisecond))
	tr := w.records("fault.target")
	if len(tr) != 1 {
		t.Fatalf("%d fault.target records", len(tr))
	}
	chosen := attr(tr[0], "chosen")
	if attrString(tr[0]) != "planner=random, rule=0, role=leader, candidates=n1,n3, chosen="+chosen || (chosen != "n1" && chosen != "n3") {
		t.Fatalf("fault.target %s", attrString(tr[0]))
	}
	if w.s.Lookup(chosen).State() != kernel.NodeDown {
		t.Fatalf("%s was not crashed", chosen)
	}

	w = newW5(full(1), true)
	startRandom(w, &fault.Random{Rules: []fault.Rule{crashRule("none")}}, roles, 10*time.Second)
	w.s.RunUntil(at(1500 * time.Millisecond))
	if sk := w.records("fault.skip"); len(sk) != 1 || attr(sk[0], "reason") != "no-target" {
		t.Fatalf("role none: skips %+v", sk)
	}

	w = newW5(full(1), true)
	w.in.Inject(fault.Event{Kind: fault.KindCrash, Node: "n1"})
	w.in.Inject(fault.Event{Kind: fault.KindCrash, Node: "n2"})
	startRandom(w, &fault.Random{MaxDown: 1, Rules: []fault.Rule{crashRule("client")}}, roles, 10*time.Second)
	w.s.RunUntil(at(1500 * time.Millisecond))
	if w.c1.State() != kernel.NodeDown {
		t.Fatalf("c1 was not crashed under MaxDown 1 with two servers down")
	}

	w = newW5(full(1), true)
	mustPanic(t, `fault: Random: rules[0]: unknown role "boss"`,
		func() { startRandom(w, &fault.Random{Rules: []fault.Rule{crashRule("boss")}}, roles, 10*time.Second) })
	startRandom(w, &fault.Random{Rules: []fault.Rule{crashRule("ghost")}}, roles, 10*time.Second)
	if stop := w.s.RunUntil(at(5 * time.Second)); stop != kernel.StopFailed ||
		!strings.Contains(w.s.Err().Error(), `fault: Random: rules[0]: role "ghost" returned unknown node id 9`) {
		t.Fatalf("ghost role: stop %v err %v", stop, w.s.Err())
	}
}

// ring is a test-local partition shape (AT-FLT-24).
type ring struct{}

func (ring) Name() string { return "ring" }

// AT-FLT-24, FLT-081
func TestRandomValidation(t *testing.T) {
	s := time.Second
	cases := []struct {
		r    fault.Random
		want string
	}{
		{fault.Random{MaxDown: -1}, "fault: Random: MaxDown must be >= 0 (got -1)"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindHeal, Every: s}}}, `fault: Random: rules[0]: kind "heal" is not a rule kind`},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindCrash}}}, "fault: Random: rules[0]: Every must be in (0s, 10000h0m0s] (got 0s)"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindCrash, Every: s, MinFor: 2 * s, MaxFor: s}}},
			"fault: Random: rules[0]: MinFor and MaxFor must satisfy 0 <= MinFor <= MaxFor <= 10000h0m0s (got MinFor=2s MaxFor=1s)"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindClockJump, Every: s, MaxFor: s, Magnitude: 5}}},
			"fault: Random: rules[0]: clock-jump has no undo; MinFor and MaxFor must be 0"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindClockDrift, Every: s}}},
			"fault: Random: rules[0]: clock-drift requires Magnitude in [1, 500000] (got 0)"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindPartition, Every: s, Shape: ring{}}}},
			`fault: Random: rules[0]: Shape "ring" is set, but the partition planner is not in this release yet; set it to nil, and see the roadmap at https://github.com/hmdsefi/faultline/issues/167`},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindCrash, Every: s, Shape: ring{}}}},
			"fault: Random: rules[0]: Shape is only allowed for partition"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindLink, Every: s}}}, "fault: Random: rules[0]: link requires Link"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindCrash, Every: s, Magnitude: 3}}},
			"fault: Random: rules[0]: Magnitude is not allowed for crash"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindCorrupt, Every: s, Path: "/f", Magnitude: 2000000}}},
			"fault: Random: rules[0]: corrupt requires Magnitude in [0, 1048576] (got 2000000)"},
		// further FLT-081 rows
		{fault.Random{Rules: []fault.Rule{{Kind: "x", Every: s}}}, `fault: Random: rules[0]: unknown kind "x"`},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindCrash, Every: 10001 * time.Hour}}},
			"fault: Random: rules[0]: Every must be in (0s, 10000h0m0s] (got 10001h0m0s)"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindCrash, Every: s, Link: &simnet.Link{DropPPM: 100000}}}},
			"fault: Random: rules[0]: Link is only allowed for link"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindLink, Every: s, Link: &simnet.Link{Latency: -1}}}},
			"fault: Random: rules[0]: link: invalid link: Latency -1ns out of range [0s, 24h0m0s]"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindCrash, Every: s, Path: "/f"}}},
			"fault: Random: rules[0]: Path is only allowed for corrupt"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindCorrupt, Every: s}}}, "fault: Random: rules[0]: corrupt requires Path"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindCrash, Every: s}, {Kind: fault.KindSyncFail, Every: s, Magnitude: -1}}},
			"fault: Random: rules[1]: sync-fail requires Magnitude in [0, 2147483647] (got -1)"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindDiskCapacity, Every: s}}},
			"fault: Random: rules[0]: disk-capacity requires Magnitude in [1, 9223372036854775807] (got 0)"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindClockJump, Every: s, Magnitude: int64(fault.MaxRuleDuration) + 1}}},
			"fault: Random: rules[0]: clock-jump requires Magnitude in [1, 36000000000000000] (got 36000000000000001)"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindHealLink, Every: s}}}, `fault: Random: rules[0]: kind "heal-link" is not a rule kind`},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindLinkReset, Every: s}}}, `fault: Random: rules[0]: kind "link-reset" is not a rule kind`},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindRestart, Every: s}}}, `fault: Random: rules[0]: kind "restart" is not a rule kind`},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindResume, Every: s}}}, `fault: Random: rules[0]: kind "resume" is not a rule kind`},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindCrash, Every: s, MinFor: -1}}},
			"fault: Random: rules[0]: MinFor and MaxFor must satisfy 0 <= MinFor <= MaxFor <= 10000h0m0s (got MinFor=-1ns MaxFor=0s)"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindCrash, Every: s, MaxFor: fault.MaxRuleDuration + 1}}},
			"fault: Random: rules[0]: MinFor and MaxFor must satisfy 0 <= MinFor <= MaxFor <= 10000h0m0s (got MinFor=0s MaxFor=10000h0m0.000000001s)"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindCorrupt, Every: s, MaxFor: s, Path: "/f"}}},
			"fault: Random: rules[0]: corrupt has no undo; MinFor and MaxFor must be 0"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindClockDrift, Every: s, MaxFor: s, Magnitude: 1}}},
			"fault: Random: rules[0]: clock-drift has no undo; MinFor and MaxFor must be 0"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindClockJump, Every: s}}},
			"fault: Random: rules[0]: clock-jump requires Magnitude in [1, 36000000000000000] (got 0)"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindClockDrift, Every: s, Magnitude: 500001}}},
			"fault: Random: rules[0]: clock-drift requires Magnitude in [1, 500000] (got 500001)"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindCorrupt, Every: s, Path: "/f", Magnitude: 1048577}}},
			"fault: Random: rules[0]: corrupt requires Magnitude in [0, 1048576] (got 1048577)"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindCorrupt, Every: s, Path: "/f", Magnitude: -1}}},
			"fault: Random: rules[0]: corrupt requires Magnitude in [0, 1048576] (got -1)"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindSyncFail, Every: s, Magnitude: 2147483648}}},
			"fault: Random: rules[0]: sync-fail requires Magnitude in [0, 2147483647] (got 2147483648)"},
		// two checks fail at once: the earlier one of FLT-081 wins, for the first failing rule
		{fault.Random{Rules: []fault.Rule{{Kind: "x"}}}, `fault: Random: rules[0]: unknown kind "x"`},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindCrash, MinFor: 2 * s, MaxFor: s}}}, "fault: Random: rules[0]: Every must be in (0s, 10000h0m0s] (got 0s)"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindClockJump, Every: s, MaxFor: s, Target: "boss", Magnitude: 5}}},
			"fault: Random: rules[0]: clock-jump has no undo; MinFor and MaxFor must be 0"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindCrash, Every: s, Target: "boss", Shape: ring{}}}}, `fault: Random: rules[0]: unknown role "boss"`},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindPartition, Every: s, Shape: ring{}, Link: &simnet.Link{}}}},
			`fault: Random: rules[0]: Shape "ring" is set, but the partition planner is not in this release yet; set it to nil, and see the roadmap at https://github.com/hmdsefi/faultline/issues/167`},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindLink, Every: s, Path: "/f"}}}, "fault: Random: rules[0]: link requires Link"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindLink, Every: s, Link: &simnet.Link{Latency: -1}, Path: "/f"}}},
			"fault: Random: rules[0]: link: invalid link: Latency -1ns out of range [0s, 24h0m0s]"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindCrash, Every: s, Path: "/f", Magnitude: 3}}}, "fault: Random: rules[0]: Path is only allowed for corrupt"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindCrash}, {Kind: "x"}}}, "fault: Random: rules[0]: Every must be in (0s, 10000h0m0s] (got 0s)"},
	}
	for _, c := range cases {
		w := newW5(kernel.Config{Seed: 1}, false)
		r := c.r
		mustPanic(t, c.want, func() { startRandom(w, &r, nil, 10*time.Second) })
	}
	w := newW5(kernel.Config{Seed: 1}, false)
	r := &fault.Random{Rules: []fault.Rule{{Kind: fault.KindCorrupt, Every: s, Path: "/f"}}}
	mustPanic(t, "fault: Random: rules[0]: corrupt requires PlanContext.Disks",
		func() { r.Start(&fault.PlanContext{Sim: w.s, Rand: w.s.Rand("fault/random"), Inject: w.in.Inject}) })
	hand := &fault.PlanContext{Sim: w.s, Rand: w.s.Rand("fault/random"), Inject: w.in.Inject}
	mustPanic(t, "fault: Random: rules[0]: corrupt requires Path",
		func() { (&fault.Random{Rules: []fault.Rule{{Kind: fault.KindCorrupt, Every: s}}}).Start(hand) })
	mustPanic(t, "fault: Random: rules[0]: corrupt requires PlanContext.Disks",
		func() {
			(&fault.Random{Rules: []fault.Rule{{Kind: fault.KindCorrupt, Every: s, Path: "/f", Magnitude: 2000000}}}).Start(hand)
		})
	mustPanic(t, "fault: Random: PlanContext.Sim is nil", func() { r.Start(&fault.PlanContext{}) })
	mustPanic(t, "fault: Random: PlanContext.Rand is nil", func() { r.Start(&fault.PlanContext{Sim: w.s}) })
	mustPanic(t, "fault: Random: PlanContext.Inject is nil", func() { r.Start(&fault.PlanContext{Sim: w.s, Rand: w.s.Rand("x")}) })

	// The edges of every range are valid.
	w = newW5(kernel.Config{Seed: 1}, false)
	longest := fault.MaxRuleDuration
	edges := &fault.Random{Rules: []fault.Rule{
		{Kind: fault.KindCrash, Every: longest, MaxFor: longest},
		{Kind: fault.KindClockJump, Every: s, Magnitude: int64(longest)},
		{Kind: fault.KindClockDrift, Every: s, Magnitude: 500000},
		{Kind: fault.KindSyncFail, Every: s},
		{Kind: fault.KindSyncFail, Every: s, Magnitude: 2147483647},
		{Kind: fault.KindDiskCapacity, Every: s, Magnitude: 1},
		{Kind: fault.KindDiskCapacity, Every: s, Magnitude: 9223372036854775807},
		{Kind: fault.KindCorrupt, Every: s, Path: "/f"},
		{Kind: fault.KindCorrupt, Every: s, Path: "/f", Magnitude: 1048576},
	}}
	startRandom(w, edges, nil, 10*time.Second)

	// FLT-081: validation runs before any state changes, and "started twice" (check 2) comes before
	// MaxDown (check 3) and holds for any context on the same Sim.
	w = newW5(kernel.Config{Seed: 1}, false)
	reused := &fault.Random{Rules: []fault.Rule{{Kind: fault.KindCrash}}}
	mustPanic(t, "fault: Random: rules[0]: Every must be in (0s, 10000h0m0s] (got 0s)", func() { startRandom(w, reused, nil, 10*time.Second) })
	reused.Rules[0].Every = s
	startRandom(w, reused, nil, 10*time.Second)
	reused.MaxDown = -1
	mustPanic(t, "fault: Random: started twice on the same simulation", func() { startRandom(w, reused, nil, 10*time.Second) })
}

// AT-FLT-29, FLT-083: corrupt rules never create volumes, skip missing files and files with fewer
// than Len synced bytes, and draw the offset within the synced bytes.
func TestRandomCorrupt(t *testing.T) {
	w := newW5(full(1), false)
	roles := fault.Roles{"one": func() []kernel.NodeID { return []kernel.NodeID{1} }}
	r := &fault.Random{Rules: []fault.Rule{{Kind: fault.KindCorrupt, Every: time.Second, Path: "/f", Magnitude: 4, Target: "one"}}}
	startRandom(w, r, roles, 100*time.Second)
	occurrence := func() { // runs events up to and including the next occurrence of rule 0
		t.Helper()
		for n := len(w.records("fault.target")); len(w.records("fault.target")) == n; {
			if !w.s.Step() {
				t.Fatalf("no further occurrence")
			}
		}
	}
	skipped := func(n int, reason string) { // the last occurrence was the n-th skip, with reason
		t.Helper()
		sk := w.records("fault.skip")
		if len(sk) != n || attr(sk[n-1], "reason") != reason || len(w.records("fault.corrupt")) != 0 {
			t.Fatalf("%d skips, %d corruptions; want skip %d with reason %s", len(sk), len(w.records("fault.corrupt")), n, reason)
		}
	}
	occurrence()
	skipped(1, "no-file")
	if _, ok := w.d.Lookup(w.node(1)); ok {
		t.Fatalf("the corrupt rule created a volume for n1")
	}
	v := w.d.Volume(w.node(1))
	f, err := v.Create("/f")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Append([]byte("ab")); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	occurrence()
	skipped(2, "file-too-small")
	if _, err := f.Append(make([]byte, 98)); err != nil {
		t.Fatal(err)
	}
	occurrence() // 100 bytes, but only 2 synced
	skipped(3, "file-too-small")
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	occurrence()
	c, d := w.last(t, "fault.corrupt"), w.last(t, "disk.corrupt")
	off, _ := strconv.Atoi(attr(c, "off"))
	if attr(c, "len") != "4" || attr(c, "path") != "/f" || off < 0 || off > 96 || len(w.records("fault.skip")) != 3 {
		t.Fatalf("fault.corrupt %s", attrString(c))
	}
	if attr(d, "off") != attr(c, "off") || attr(d, "len") != "4" {
		t.Fatalf("disk.corrupt %s after fault.corrupt %s", attrString(d), attrString(c))
	}
}

// FLT-083, DSK-043: with 8 synced bytes and a 4096-byte unsynced tail, every planned range lies
// within the synced bytes, so every corruption damages Len bytes, and every offset of [0, 4] occurs.
func TestRandomCorruptSyncedBytes(t *testing.T) {
	offs := map[int]bool{}
	for seed := uint64(1); seed <= 5; seed++ {
		w := newW5(full(seed), false)
		f, err := w.d.Volume(w.node(1)).Create("/f")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Append(make([]byte, 8)); err != nil {
			t.Fatal(err)
		}
		if err := f.Sync(); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Append(make([]byte, 4096)); err != nil {
			t.Fatal(err)
		}
		roles := fault.Roles{"one": func() []kernel.NodeID { return []kernel.NodeID{1} }}
		r := &fault.Random{Rules: []fault.Rule{{Kind: fault.KindCorrupt, Every: 100 * time.Millisecond, Path: "/f", Magnitude: 4, Target: "one"}}}
		startRandom(w, r, roles, 10*time.Second)
		w.s.RunUntil(at(10 * time.Second))
		planned, damaged := w.records("fault.corrupt"), w.records("disk.corrupt")
		if len(planned) < 50 || len(damaged) != len(planned) {
			t.Fatalf("seed %d: %d fault.corrupt and %d disk.corrupt records", seed, len(planned), len(damaged))
		}
		for i, c := range planned {
			off, _ := strconv.Atoi(attr(c, "off"))
			if off < 0 || off+4 > 8 || attr(damaged[i], "off") != attr(c, "off") || attr(damaged[i], "len") != "4" {
				t.Fatalf("seed %d: fault.corrupt %s, disk.corrupt %s", seed, attrString(c), attrString(damaged[i]))
			}
			offs[off] = true
		}
	}
	if len(offs) != 5 {
		t.Fatalf("offsets %v, want each of 0 to 4", offs)
	}
}

// capture is a PlanContext on w whose Inject records (time, event) without applying anything.
func capture(w *world, rnd *rand.Rand, servers []*kernel.Node, roles fault.Roles, until time.Duration) (*fault.PlanContext, *[]captured) {
	var got []captured
	return &fault.PlanContext{
		Sim: w.s, Rand: rnd, Servers: servers, Roles: roles, Until: at(until), Disks: w.d,
		Inject: func(e fault.Event) { got = append(got, captured{w.s.Now(), e}) },
	}, &got
}

// FLT-080, FLT-082 to FLT-084: the draws of one occurrence and their order: the gap, the target
// pick, the kind's draws, the duration (only with an undo), then the next gap. The expected values
// come from a fresh copy of the stream.
func TestRandomDraws(t *testing.T) {
	type expect func(ref *rand.Rand) []captured
	between := func(r *rand.Rand, lo, hi time.Duration) time.Duration {
		return lo + time.Duration(r.Int64N(int64(hi-lo)+1))
	}
	names := []string{"n1", "n2", "n3", "n4", "n5"}
	ev := func(k fault.Kind, node string) fault.Event { return fault.Event{Kind: k, Node: node} }
	second := 2 * time.Second
	cases := []struct {
		name string
		rule fault.Rule
		want expect
	}{
		{"clock-drift", fault.Rule{Kind: fault.KindClockDrift, Every: second, Magnitude: 100}, func(ref *rand.Rand) []captured {
			t0 := at(between(ref, time.Second, 3*time.Second))
			e := ev(fault.KindClockDrift, names[ref.IntN(5)])
			e.N = ref.Int64N(201) - 100
			return []captured{{t0, e}}
		}},
		{"sync-fail", fault.Rule{Kind: fault.KindSyncFail, Every: second, MinFor: 0, MaxFor: time.Second}, func(ref *rand.Rand) []captured {
			t0 := at(between(ref, time.Second, 3*time.Second))
			n := names[ref.IntN(5)]
			f, u := ev(fault.KindSyncFail, n), ev(fault.KindSyncFail, n)
			f.N = 1
			return []captured{{t0, f}, {t0 + at(between(ref, 0, time.Second)), u}}
		}},
		{"disk-capacity", fault.Rule{Kind: fault.KindDiskCapacity, Every: second, Magnitude: 4096}, func(ref *rand.Rand) []captured {
			t0 := at(between(ref, time.Second, 3*time.Second))
			f := ev(fault.KindDiskCapacity, names[ref.IntN(5)])
			f.N = 4096
			return []captured{{t0, f}}
		}},
		{"cut", fault.Rule{Kind: fault.KindCut, Every: second, MinFor: time.Second, MaxFor: time.Second}, func(ref *rand.Rand) []captured {
			t0 := at(between(ref, time.Second, 3*time.Second))
			i := ref.IntN(5)
			peers := append(append([]string(nil), names[:i]...), names[i+1:]...)
			p := peers[ref.IntN(4)]
			d := between(ref, time.Second, time.Second)
			return []captured{{t0, fault.Event{Kind: fault.KindCut, Node: names[i], Peer: p}}, {t0 + at(d), fault.Event{Kind: fault.KindHealLink, Node: names[i], Peer: p}}}
		}},
		{"link", fault.Rule{Kind: fault.KindLink, Every: second, Link: &simnet.Link{DropPPM: 7}}, func(ref *rand.Rand) []captured {
			t0 := at(between(ref, time.Second, 3*time.Second))
			i := ref.IntN(5)
			peers := append(append([]string(nil), names[:i]...), names[i+1:]...)
			return []captured{{t0, fault.Event{Kind: fault.KindLink, Node: names[i], Peer: peers[ref.IntN(4)], Link: &simnet.Link{DropPPM: 7}}}}
		}},
		{"crash", fault.Rule{Kind: fault.KindCrash, Every: second, MaxFor: 500 * time.Millisecond}, func(ref *rand.Rand) []captured {
			t0 := at(between(ref, time.Second, 3*time.Second))
			n := names[ref.IntN(5)]
			return []captured{{t0, ev(fault.KindCrash, n)}, {t0 + at(between(ref, 0, 500*time.Millisecond)), ev(fault.KindRestart, n)}}
		}},
		{"corrupt", fault.Rule{Kind: fault.KindCorrupt, Every: second, Path: "/f", Target: "one"}, func(ref *rand.Rand) []captured {
			t0 := at(between(ref, time.Second, 3*time.Second))
			one := []string{"n1"}
			e := fault.Event{Kind: fault.KindCorrupt, Node: one[ref.IntN(len(one))], Path: "/f", Len: 1}
			e.Off = ref.Int64N(10)
			return []captured{{t0, e}}
		}},
	}
	for _, c := range cases {
		w := newW5(full(1), false)
		f, err := w.d.Volume(w.node(1)).Create("/f")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Append(make([]byte, 10)); err != nil {
			t.Fatal(err)
		}
		if err := f.Sync(); err != nil {
			t.Fatal(err)
		}
		roles := fault.Roles{"one": func() []kernel.NodeID { return []kernel.NodeID{1} }}
		ctx, got := capture(w, rand.New(rand.NewPCG(1, 2)), w.servers, roles, 100*time.Second) //nolint:gosec // fixed seed: the reference copy below
		r := &fault.Random{Rules: []fault.Rule{c.rule}}
		r.Start(ctx)
		ref := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // a fresh copy of ctx.Rand's stream
		want := c.want(ref)
		// the next occurrence's time is drawn after the duration
		next := want[0].at + at(between(ref, time.Second, 3*time.Second))
		w.s.RunUntil(next - 1)
		if !reflect.DeepEqual((*got)[:min(len(*got), len(want))], want) || len(*got) < len(want) {
			t.Fatalf("%s: captured %v, want %v", c.name, *got, want)
		}
		if len(w.records("fault.target")) != 1 {
			t.Fatalf("%s: an occurrence before %v", c.name, time.Duration(next))
		}
		w.s.RunUntil(next)
		if len(w.records("fault.target"))+len(w.records("fault.skip")) != 2 {
			t.Fatalf("%s: no occurrence at %v", c.name, time.Duration(next))
		}
	}
	// peers are the servers in ascending ID, also when ctx.Servers is not sorted (FLT-083)
	for _, c := range cases {
		if c.name != "cut" && c.name != "link" {
			continue
		}
		w := newW5(full(1), false)
		servers := []*kernel.Node{w.node(5), w.node(3), w.node(1), w.node(4), w.node(2)}
		ctx, got := capture(w, rand.New(rand.NewPCG(1, 2)), servers, nil, 100*time.Second) //nolint:gosec // fixed seed: the reference copy below
		(&fault.Random{Rules: []fault.Rule{c.rule}}).Start(ctx)
		want := c.want(rand.New(rand.NewPCG(1, 2))) //nolint:gosec // a fresh copy of ctx.Rand's stream
		w.s.RunUntil(want[0].at)
		if len(*got) == 0 || !reflect.DeepEqual((*got)[0], want[0]) {
			t.Fatalf("%s with unsorted servers: captured %v, want %v", c.name, *got, want[0])
		}
	}
	// a rule whose Link is copied: editing the rule's Link afterwards changes no captured event
	w := newW5(kernel.Config{Seed: 1}, false)
	ctx, got := capture(w, rand.New(rand.NewPCG(1, 2)), w.servers, nil, 100*time.Second) //nolint:gosec // fixed seed
	l := &simnet.Link{DropPPM: 7}
	(&fault.Random{Rules: []fault.Rule{{Kind: fault.KindLink, Every: second, Link: l}}}).Start(ctx)
	w.s.RunUntil(at(3 * time.Second))
	l.DropPPM = 9
	if len(*got) == 0 || (*got)[0].e.Link == l || (*got)[0].e.Link.DropPPM != 7 {
		t.Fatalf("the injected link event shares the rule's Link")
	}
}

// FLT-084, FLT-085: a partition draws its split, then its duration (only with an undo); the nodes
// outside the candidates join a side in ascending ID, one IntN(2) each.
func TestRandomPartitionDraws(t *testing.T) {
	between := func(r *rand.Rand, lo, hi time.Duration) time.Duration {
		return lo + time.Duration(r.Int64N(int64(hi-lo)+1))
	}
	w := newW5(full(1), true)
	two := fault.Roles{"two": func() []kernel.NodeID { return []kernel.NodeID{2, 1} }}
	ctx, got := capture(w, rand.New(rand.NewPCG(1, 2)), w.servers, two, 100*time.Second) //nolint:gosec // fixed seed: the reference copy below
	rule := fault.Rule{Kind: fault.KindPartition, Every: 2 * time.Second, MinFor: time.Second, MaxFor: 2 * time.Second, Target: "two"}
	(&fault.Random{Rules: []fault.Rule{rule}}).Start(ctx)
	ref := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // a fresh copy of ctx.Rand's stream
	t0 := at(between(ref, time.Second, 3*time.Second))
	perm := []kernel.NodeID{1, 2}
	ref.Shuffle(len(perm), func(i, j int) { perm[i], perm[j] = perm[j], perm[i] })
	k := 1 + ref.IntN(len(perm)-1)
	g0, g1 := slices.Clone(perm[:k]), slices.Clone(perm[k:])
	for _, id := range []kernel.NodeID{3, 4, 5, 6} {
		if ref.IntN(2) == 0 {
			g0 = append(g0, id)
		} else {
			g1 = append(g1, id)
		}
	}
	slices.Sort(g0)
	slices.Sort(g1)
	if g1[0] < g0[0] {
		g0, g1 = g1, g0
	}
	name := func(ids []kernel.NodeID) []string {
		var out []string
		for _, id := range ids {
			out = append(out, w.s.Node(id).Name())
		}
		return out
	}
	d := between(ref, time.Second, 2*time.Second)
	want := []captured{
		{t0, fault.Event{Kind: fault.KindPartition, Groups: [][]string{name(g0), name(g1)}}},
		{t0 + at(d), fault.Event{Kind: fault.KindHeal}},
	}
	w.s.RunUntil(t0 + at(d))
	if len(*got) < 2 || !reflect.DeepEqual((*got)[:2], want) {
		t.Fatalf("captured %v, want %v", *got, want)
	}
}

// FLT-080, FLT-084, FLT-088: a skipped occurrence draws only the target pick made before the skip
// (for the kinds whose builder skips), and then its next gap.
func TestRandomSkipDraws(t *testing.T) {
	between := func(r *rand.Rand, lo, hi time.Duration) time.Duration {
		return lo + time.Duration(r.Int64N(int64(hi-lo)+1))
	}
	every := 2 * time.Second
	one := func() []kernel.NodeID { return []kernel.NodeID{1} }
	roles := fault.Roles{"one": one, "none": func() []kernel.NodeID { return nil }, "up": func() []kernel.NodeID { return []kernel.NodeID{3, 4, 5} }}
	cases := []struct {
		name    string
		rule    fault.Rule
		servers int            // the first servers of W5 in ctx.Servers
		maxDown int            // Random.MaxDown
		setup   func(w *world) // before Start
		first   []int          // the candidates of each pick of a first occurrence that is not skipped
		reason  string         // the skip reason of every other occurrence
		pick    int            // the candidates of the pick before the skip; 0 if none
	}{
		{"no-target", fault.Rule{Kind: fault.KindCrash, Every: every, Target: "none"}, 5, 0, nil, nil, "no-target", 0},
		{"max-down", fault.Rule{Kind: fault.KindCrash, Every: every, Target: "up"}, 5, 1, func(w *world) {
			w.in.Inject(fault.Event{Kind: fault.KindCrash, Node: "n1"})
			w.in.Inject(fault.Event{Kind: fault.KindCrash, Node: "n2"})
		}, nil, "max-down", 0},
		{"too-few-nodes", fault.Rule{Kind: fault.KindPartition, Every: every, Target: "one"}, 5, 0, nil, nil, "too-few-nodes", 0},
		{"no-peer", fault.Rule{Kind: fault.KindCut, Every: every, Target: "one"}, 1, 0, nil, nil, "no-peer", 1},
		{"no-peer after a link", fault.Rule{Kind: fault.KindLink, Every: every, Target: "one", Link: &simnet.Link{DropPPM: 1}}, 2, 0, nil,
			[]int{1, 1}, "no-peer", 1}, // the target pick and the peer pick
		{"no-file", fault.Rule{Kind: fault.KindCorrupt, Every: every, Path: "/f", Target: "one"}, 5, 0, nil, nil, "no-file", 1},
		{"file-too-small", fault.Rule{Kind: fault.KindCorrupt, Every: every, Path: "/f", Magnitude: 4, Target: "one"}, 5, 0, func(w *world) {
			f, err := w.d.Volume(w.node(1)).Create("/f")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.Append([]byte("ab")); err != nil {
				t.Fatal(err)
			}
			if err := f.Sync(); err != nil {
				t.Fatal(err)
			}
		}, nil, "file-too-small", 1},
	}
	until := at(10 * time.Second)
	for _, c := range cases {
		w := newW5(full(1), false)
		if c.setup != nil {
			c.setup(w)
		}
		r := &fault.Random{MaxDown: c.maxDown, Rules: []fault.Rule{c.rule}}
		r.Start(w.in.NewPlanContext(r, w.servers[:c.servers], roles, until))
		w.s.RunUntil(until)
		ref := kernel.New(kernel.Config{Seed: 1}).Rand("fault/random")
		var want []kernel.Time
		for next, first := at(between(ref, time.Second, 3*time.Second)), true; next < until; next, first = next.Add(between(ref, time.Second, 3*time.Second)), false {
			if first && c.first != nil {
				for _, n := range c.first {
					ref.IntN(n)
				}
				continue
			}
			if c.pick > 0 {
				ref.IntN(c.pick)
			}
			want = append(want, next)
		}
		var got []kernel.Time
		for _, sk := range w.records("fault.skip") {
			if attr(sk, "reason") != c.reason {
				t.Fatalf("%s: skip %s", c.name, attrString(sk))
			}
			got = append(got, sk.At)
		}
		if len(want) == 0 || !reflect.DeepEqual(got, want) || w.s.Rand("fault/random").Uint64() != ref.Uint64() {
			t.Fatalf("%s: skips at %v, want %v, or the stream moved differently", c.name, got, want)
		}
	}
}

// FLT-084: eligibility by node state and kind, for servers and for role targets alike, MaxDown
// limits only crash and pause, candidates are deduplicated servers in ID order, and a partition
// needs two candidates.
func TestRandomCandidates(t *testing.T) {
	ms := 500 * time.Millisecond
	// run starts rule on a W5 where n2 is down and n3 paused, with servers n5, n1..n5 (n5 twice, not
	// in ID order), and returns the fault.target candidates and the captured events.
	run := func(maxDown int, rule fault.Rule, setup ...fault.Event) (*world, []string, []captured) {
		w := newW5(full(1), false)
		for _, e := range setup {
			w.in.Inject(e)
		}
		all := fault.Roles{"all": func() []kernel.NodeID { return []kernel.NodeID{1, 2, 3, 4, 5} }}
		ctx, got := capture(w, w.s.Rand("fault/random"), append([]*kernel.Node{w.node(5)}, w.servers...), all, time.Second)
		(&fault.Random{MaxDown: maxDown, Rules: []fault.Rule{rule}}).Start(ctx)
		w.s.RunUntil(at(time.Second))
		var cands []string
		for _, tr := range w.records("fault.target") {
			cands = append(cands, attr(tr, "candidates"))
		}
		return w, cands, *got
	}
	downAndPaused := []fault.Event{{Kind: fault.KindCrash, Node: "n2"}, {Kind: fault.KindPause, Node: "n3"}}
	for _, target := range []string{"", "all"} {
		for _, c := range []struct {
			kind fault.Kind
			want string
		}{{fault.KindCrash, "n1,n4,n5"}, {fault.KindPause, "n1,n4,n5"}, {fault.KindIsolate, "n1,n3,n4,n5"}, {fault.KindClockJump, "n1,n3,n4,n5"}} {
			rule := fault.Rule{Kind: c.kind, Every: ms, Target: target}
			if c.kind == fault.KindClockJump {
				rule.Magnitude = 1
			}
			_, cands, _ := run(5, rule, downAndPaused...)
			if len(cands) == 0 {
				t.Fatalf("%s (target %q): no target", c.kind, target)
			}
			for _, got := range cands {
				if got != c.want {
					t.Fatalf("%s (target %q): candidates %s, want %s", c.kind, target, got, c.want)
				}
			}
		}
	}
	// a partition takes nodes in any state: with one server up it still splits all five
	w, _, got := run(5, fault.Rule{Kind: fault.KindPartition, Every: ms},
		fault.Event{Kind: fault.KindCrash, Node: "n2"}, fault.Event{Kind: fault.KindCrash, Node: "n3"},
		fault.Event{Kind: fault.KindPause, Node: "n4"}, fault.Event{Kind: fault.KindPause, Node: "n5"})
	if len(got) == 0 || len(got[0].e.Groups[0])+len(got[0].e.Groups[1]) != 5 {
		t.Fatalf("partition with one server up: %v", got)
	}
	for _, sk := range w.records("fault.skip") {
		if attr(sk, "reason") != "network-busy" {
			t.Fatalf("partition with one server up: skip %s", attrString(sk))
		}
	}
	// MaxDown 1 with two servers down or paused: crash is skipped, isolate is not limited
	w, _, _ = run(1, fault.Rule{Kind: fault.KindCrash, Every: ms}, downAndPaused...)
	if sk := w.records("fault.skip"); len(sk) == 0 || attr(sk[0], "reason") != "max-down" {
		t.Fatalf("crash under MaxDown: skips %v", sk)
	}
	if _, cands, _ := run(1, fault.Rule{Kind: fault.KindIsolate, Every: ms}, downAndPaused...); len(cands) == 0 {
		t.Fatalf("isolate was limited by MaxDown")
	}
	// a partition of a role with two nodes is split; with one node it is skipped as too-few-nodes
	roles := fault.Roles{"two": func() []kernel.NodeID { return []kernel.NodeID{2, 1} }, "one": func() []kernel.NodeID { return []kernel.NodeID{1} }}
	for _, c := range []struct{ role, reason string }{{"two", ""}, {"one", "too-few-nodes"}} {
		w := newW5(full(1), false)
		ctx, got := capture(w, w.s.Rand("fault/random"), w.servers, roles, time.Second)
		(&fault.Random{Rules: []fault.Rule{{Kind: fault.KindPartition, Every: ms, MinFor: time.Millisecond, MaxFor: time.Millisecond, Target: c.role}}}).Start(ctx)
		w.s.RunUntil(at(time.Second))
		sk := w.records("fault.skip")
		if c.reason == "" && (len(*got) == 0 || len(sk) != 0) || c.reason != "" && (len(*got) != 0 || len(sk) == 0 || attr(sk[0], "reason") != c.reason) {
			t.Fatalf("role %s: captured %v, skips %v", c.role, *got, sk)
		}
	}
}

// FLT-083: corrupt skips a directory as no-file and takes a file of exactly Len synced bytes.
func TestRandomCorruptEdges(t *testing.T) {
	w := newW5(full(1), false)
	v := w.d.Volume(w.node(1))
	if err := v.Mkdir("/d"); err != nil {
		t.Fatal(err)
	}
	f, err := v.Create("/g")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Append([]byte("abcd")); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	roles := fault.Roles{"one": func() []kernel.NodeID { return []kernel.NodeID{1} }}
	ctx, got := capture(w, w.s.Rand("fault/random"), w.servers, roles, time.Second)
	(&fault.Random{Rules: []fault.Rule{
		{Kind: fault.KindCorrupt, Every: 500 * time.Millisecond, Path: "/d", Magnitude: 4, Target: "one"},
		{Kind: fault.KindCorrupt, Every: 500 * time.Millisecond, Path: "/g", Magnitude: 4, Target: "one"},
	}}).Start(ctx)
	w.s.RunUntil(at(time.Second))
	for _, sk := range w.records("fault.skip") {
		if attr(sk, "rule") != "0" || attr(sk, "reason") != "no-file" {
			t.Fatalf("skip %s", attrString(sk))
		}
	}
	if len(*got) == 0 || len(w.records("fault.skip")) == 0 {
		t.Fatalf("captured %v, skips %v", *got, w.records("fault.skip"))
	}
	for _, c := range *got {
		if c.e.Path != "/g" || c.e.Off != 0 || c.e.Len != 4 {
			t.Fatalf("corrupt %v", c.e)
		}
	}
}

// FLT-083: a cut's peer may be in any state; a link never doubles an active link of the planner on
// the same ordered pair, its mark clears when the link resets, and no-peer means that every peer
// is linked.
func TestRandomPeers(t *testing.T) {
	w := newW5(full(1), false)
	w.in.Inject(fault.Event{Kind: fault.KindCrash, Node: "n2"})
	roles := fault.Roles{"one": func() []kernel.NodeID { return []kernel.NodeID{1} }}
	ctx, got := capture(w, w.s.Rand("fault/random"), w.servers[:2], roles, time.Second)
	(&fault.Random{Rules: []fault.Rule{{Kind: fault.KindCut, Every: 500 * time.Millisecond, Target: "one"}}}).Start(ctx)
	w.s.RunUntil(at(time.Second))
	if len(*got) == 0 || (*got)[0].e.Peer != "n2" {
		t.Fatalf("cut to a down peer: %v", *got)
	}

	relinked, noPeer := false, false
	for seed := uint64(1); seed <= 5; seed++ {
		w := newW5(full(seed), false)
		r := &fault.Random{Rules: []fault.Rule{{Kind: fault.KindLink, Every: 100 * time.Millisecond, MinFor: 200 * time.Millisecond, MaxFor: time.Second, Link: &simnet.Link{DropPPM: 1}}}}
		r.Start(w.in.NewPlanContext(r, w.servers[:2], nil, at(20*time.Second)))
		w.s.RunUntil(at(20 * time.Second))
		active, links := map[[2]string]bool{}, map[[2]string]int{}
		last := ""
		for _, rec := range w.s.Records() {
			switch rec.Kind {
			case "fault.link":
				k := [2]string{attr(rec, "node"), attr(rec, "peer")}
				if active[k] {
					t.Fatalf("seed %d: a second active link on %v", seed, k)
				}
				active[k] = true
				links[k]++
				relinked = relinked || links[k] > 1
			case "fault.link_reset":
				delete(active, [2]string{attr(rec, "node"), attr(rec, "peer")})
			case "fault.target":
				last = attr(rec, "chosen")
			case "fault.skip":
				if attr(rec, "reason") != "no-peer" {
					t.Fatalf("seed %d: skip %s", seed, attrString(rec))
				}
				noPeer = true
				peer := map[string]string{"n1": "n2", "n2": "n1"}[last]
				if !active[[2]string{last, peer}] {
					t.Fatalf("seed %d: no-peer for %s although %s -> %s is not linked", seed, last, last, peer)
				}
			}
		}
	}
	if !relinked || !noPeer {
		t.Fatalf("relinked %v, no-peer %v: want both", relinked, noPeer)
	}

	// only link leaves out the marked peers: a cut still picks n2 while n1 -> n2 is linked
	w = newW5(full(1), false)
	r := &fault.Random{Rules: []fault.Rule{
		{Kind: fault.KindLink, Every: 100 * time.Millisecond, Target: "one", Link: &simnet.Link{DropPPM: 1}},
		{Kind: fault.KindCut, Every: 300 * time.Millisecond, MinFor: time.Millisecond, MaxFor: time.Millisecond, Target: "one"},
	}}
	r.Start(w.in.NewPlanContext(r, w.servers[:2], roles, at(5*time.Second)))
	w.s.RunUntil(at(5 * time.Second))
	for _, sk := range w.records("fault.skip") {
		if attr(sk, "rule") == "1" {
			t.Fatalf("a cut was skipped: %s", attrString(sk))
		}
	}
	if len(w.records("fault.link")) != 1 || len(w.records("fault.cut")) < 10 {
		t.Fatalf("%d links, %d cuts", len(w.records("fault.link")), len(w.records("fault.cut")))
	}
}

// AT-FLT-30 (b, c), FLT-084: a node with an active sync-fail or disk-capacity fault of the planner
// that has an undo is not a candidate for another fault of that kind until the undo runs: the
// occurrence is skipped as no-target and draws only its next gap. An undo at the instant of an
// occurrence of its rule runs first, and a mark excludes the node only for its own kind.
func TestRandomDiskMarks(t *testing.T) {
	between := func(r *rand.Rand, lo, hi time.Duration) time.Duration {
		return lo + time.Duration(r.Int64N(int64(hi-lo)+1))
	}
	roles := fault.Roles{"one": func() []kernel.NodeID { return []kernel.NodeID{1} }, "pair": func() []kernel.NodeID { return []kernel.NodeID{1, 2} }}
	one := []string{"n1"} // the candidates of every pick
	until := 1000 * time.Nanosecond
	for _, kind := range []fault.Kind{fault.KindSyncFail, fault.KindDiskCapacity} {
		w := newW5(full(1), false)
		rule := fault.Rule{Kind: kind, Every: 2, MinFor: 2, MaxFor: 2, Target: "one", Magnitude: 1}
		ctx, got := capture(w, rand.New(rand.NewPCG(1, 2)), w.servers, roles, until) //nolint:gosec // fixed seed: the reference copy below
		(&fault.Random{Rules: []fault.Rule{rule}}).Start(ctx)
		w.s.RunUntil(at(until))
		ref := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // a fresh copy of ctx.Rand's stream
		var want []captured
		var skips []kernel.Time
		var busy kernel.Time // the end of the active fault
		ties := 0
		for next := at(between(ref, 1, 3)); next < at(until); next = next.Add(between(ref, 1, 3)) {
			if next < busy {
				skips = append(skips, next)
				continue
			}
			if next == busy {
				ties++
			}
			n := one[ref.IntN(len(one))]
			busy = min(next.Add(between(ref, 2, 2)), at(until))
			want = append(want, captured{next, fault.Event{Kind: kind, Node: n, N: 1}}, captured{busy, fault.Event{Kind: kind, Node: n}})
		}
		var gotSkips []kernel.Time
		for _, sk := range w.records("fault.skip") {
			if attr(sk, "reason") != "no-target" {
				t.Fatalf("%s: skip %s", kind, attrString(sk))
			}
			gotSkips = append(gotSkips, sk.At)
		}
		if len(skips) == 0 || ties == 0 || !reflect.DeepEqual(*got, want) || !reflect.DeepEqual(gotSkips, skips) {
			t.Fatalf("%s: %d ties; captured %v, want %v; skips at %v, want %v", kind, ties, *got, want, gotSkips, skips)
		}
		if ctx.Rand.Uint64() != ref.Uint64() {
			t.Fatalf("%s: the stream moved differently", kind)
		}
	}
	// the marks are per kind and only for their kind: n1 takes one sync-fail and one disk-capacity,
	// each with its undo at the recovery, every other disk occurrence is skipped, and every
	// clock-drift, crash and partition occurrence still takes n1 (each eligibility branch)
	w := newW5(full(1), false)
	ctx, got := capture(w, w.s.Rand("fault/random"), w.servers, roles, 10*time.Second)
	long := 20 * time.Second // beyond the recovery, so the recovery undoes both
	(&fault.Random{Rules: []fault.Rule{
		{Kind: fault.KindSyncFail, Every: time.Second, MinFor: long, MaxFor: long, Target: "one"},
		{Kind: fault.KindDiskCapacity, Every: time.Second, MinFor: long, MaxFor: long, Magnitude: 1, Target: "one"},
		{Kind: fault.KindClockDrift, Every: time.Second, Magnitude: 100, Target: "one"},
		{Kind: fault.KindCrash, Every: time.Second, Target: "one"},
		{Kind: fault.KindPartition, Every: time.Second, MinFor: time.Millisecond, MaxFor: time.Millisecond, Target: "pair"},
	}}).Start(ctx)
	w.s.RunUntil(at(10*time.Second - 1))
	syncs, disks, drifts, crashes, partitions := 0, 0, 0, 0, 0
	for _, c := range *got {
		switch c.e.Kind {
		case fault.KindSyncFail:
			syncs++
		case fault.KindDiskCapacity:
			disks++
		case fault.KindClockDrift:
			drifts++
		case fault.KindCrash:
			crashes++
		case fault.KindPartition:
			partitions++
		}
	}
	skips := w.records("fault.skip")
	for _, sk := range skips {
		if r := attr(sk, "rule"); r != "0" && r != "1" || attr(sk, "reason") != "no-target" {
			t.Fatalf("skip %s", attrString(sk))
		}
	}
	if syncs != 1 || disks != 1 || drifts < 5 || crashes < 5 || partitions < 5 || len(skips) < 10 {
		t.Fatalf("%d sync-fail, %d disk-capacity, %d clock-drift, %d crash and %d partition with %d skips", syncs, disks, drifts, crashes, partitions, len(skips))
	}
}

// AT-FLT-30 (d), FLT-084, FLT-012: a sync-fail without an undo takes no mark, so it repeats on its
// node, each start replacing the previous one, except while a sync-fail with an undo holds the
// mark. Every undo ends exactly the active fault, and the recovery leaves none.
func TestRandomDiskUndoless(t *testing.T) {
	w := newW5(full(1), false)
	roles := fault.Roles{"one": func() []kernel.NodeID { return []kernel.NodeID{1} }}
	r := &fault.Random{Rules: []fault.Rule{
		{Kind: fault.KindSyncFail, Every: 500 * time.Millisecond, Magnitude: 1, Target: "one"},
		{Kind: fault.KindSyncFail, Every: 4 * time.Second, MinFor: 3 * time.Second, MaxFor: 3 * time.Second, Magnitude: 1, Target: "one"},
	}}
	startRandom(w, r, roles, 20*time.Second)
	w.s.RunUntil(at(21 * time.Second))
	timed, ended := false, false // a timed sync-fail holds n1's mark; one has ended
	before, blocked, after := 0, 0, 0
	for _, rec := range w.s.Records() {
		switch {
		case rec.Kind == "fault.target" && attr(rec, "rule") == "1":
			timed = true
		case rec.Kind == "fault.sync_fail" && attr(rec, "n") == "0" && rec.At < at(20*time.Second):
			timed, ended = false, true
		case rec.Kind == "fault.target" && attr(rec, "rule") == "0":
			switch {
			case timed:
				t.Fatalf("a sync-fail without an undo at %v while the timed one holds the mark", time.Duration(rec.At))
			case ended:
				after++
			default:
				before++
			}
		case rec.Kind == "fault.skip" && attr(rec, "rule") == "0":
			if !timed || attr(rec, "reason") != "no-target" {
				t.Fatalf("skip at %v: %s", time.Duration(rec.At), attrString(rec))
			}
			blocked++
		}
	}
	if before < 2 || blocked == 0 || after < 2 {
		t.Fatalf("%d repeats before a timed sync-fail, %d skips while it held the mark, %d repeats after", before, blocked, after)
	}
	active, undos := 0, 0 // the ID of n1's active sync-fail, 0 if none
	for _, e := range w.in.Applied().Events {
		if e.Kind != fault.KindSyncFail {
			continue
		}
		if e.N != 0 {
			active = e.ID
			continue
		}
		if active == 0 || !slices.Equal(e.Undoes, []int{active}) {
			t.Fatalf("the undo %d at %v ends %v, want exactly [%d]", e.ID, time.Duration(e.At), e.Undoes, active)
		}
		active = 0
		undos++
	}
	if active != 0 || undos < 2 {
		t.Fatalf("%d undos, %d left active", undos, active)
	}
}

// AT-FLT-30 (a), FLT-084, FLT-012: in 100 runs of R(), sync-fail and disk-capacity faults of the
// planner never overlap on a node, and every undo ends exactly its own fault.
func TestRandomDiskPairing(t *testing.T) {
	undos := 0
	for seed := uint64(1); seed <= 100; seed++ {
		w := newW5(kernel.Config{Seed: seed}, false)
		startRandom(w, &fault.Random{Rules: R()}, nil, 25*time.Second)
		w.s.RunUntil(at(30 * time.Second))
		active := map[[2]string]int{} // kind and node: the ID of the active fault
		for _, e := range w.in.Applied().Events {
			if e.Kind != fault.KindSyncFail && e.Kind != fault.KindDiskCapacity {
				continue
			}
			k := [2]string{string(e.Kind), e.Node}
			id, ok := active[k]
			if e.N != 0 {
				if ok || len(e.Undoes) != 0 {
					t.Fatalf("seed %d: %s %s %d starts while %d is active (undoes %v)", seed, e.Kind, e.Node, e.ID, id, e.Undoes)
				}
				active[k] = e.ID
				continue
			}
			undos++
			if !ok || !slices.Equal(e.Undoes, []int{id}) {
				t.Fatalf("seed %d: the undo %d of %s %s ends %v, want exactly [%d]", seed, e.ID, e.Kind, e.Node, e.Undoes, id)
			}
			delete(active, k)
		}
		if len(active) != 0 {
			t.Fatalf("seed %d: %d faults never undone", seed, len(active))
		}
	}
	if undos < 1000 {
		t.Fatalf("only %d undos", undos)
	}
}

// FLT-084: network faults never overlap, a fault ending at an instant frees the network for an
// occurrence at that instant, and link faults are not network faults.
func TestRandomNetworkBusy(t *testing.T) {
	w := newW5(full(1), false)
	r := &fault.Random{Rules: []fault.Rule{{Kind: fault.KindIsolate, Every: 2, MinFor: 1, MaxFor: 1}}}
	startRandom(w, r, nil, 1000)
	w.s.RunUntil(1000)
	if len(w.records("fault.isolate")) < 100 || len(w.records("fault.skip")) != 0 {
		t.Fatalf("%d isolates, skips %v", len(w.records("fault.isolate")), w.records("fault.skip"))
	}
	w = newW5(full(1), false)
	ms := 100 * time.Millisecond
	r = &fault.Random{Rules: []fault.Rule{
		{Kind: fault.KindIsolate, Every: ms, MinFor: ms, MaxFor: 5 * ms},
		{Kind: fault.KindLink, Every: ms, MinFor: ms, MaxFor: 5 * ms, Link: &simnet.Link{DropPPM: 1}},
	}}
	startRandom(w, r, nil, 10*time.Second)
	w.s.RunUntil(at(10 * time.Second))
	for _, sk := range w.records("fault.skip") {
		if attr(sk, "kind") == "link" && attr(sk, "reason") == "network-busy" {
			t.Fatalf("a link occurrence was skipped as network-busy")
		}
	}
}

// FLT-082: no occurrence is scheduled at or after recoverAt.
func TestRandomUntilBoundary(t *testing.T) {
	w := newW5(full(1), false)
	r := &fault.Random{Rules: []fault.Rule{{Kind: fault.KindClockJump, Every: 1, Magnitude: 1}}}
	startRandom(w, r, nil, 1)
	w.s.RunUntil(10)
	for _, rec := range w.s.Records() {
		if (rec.Kind == "fault.target" || rec.Kind == "fault.skip") && rec.At >= 1 {
			t.Fatalf("an occurrence at %v, at or after recoverAt", time.Duration(rec.At))
		}
	}
	if len(w.records("fault.target")) == 0 {
		t.Fatalf("no occurrence before recoverAt")
	}
}

// FLT-082: MaxDown 0 means (len(Servers)-1)/2: 1 for four servers.
func TestMaxDownDefault(t *testing.T) {
	w := newW5(full(1), false)
	four := w.servers[:4]
	w.s.OnEvent(func() {
		down := 0
		for _, n := range four {
			if st := n.State(); st == kernel.NodeDown || st == kernel.NodePaused {
				down++
			}
		}
		if down > 1 {
			t.Errorf("%d of four servers down at %v", down, w.s.Now())
		}
	})
	r := &fault.Random{Rules: []fault.Rule{{Kind: fault.KindCrash, Every: 100 * time.Millisecond, MinFor: time.Second, MaxFor: 2 * time.Second}}}
	r.Start(w.in.NewPlanContext(r, four, nil, at(10*time.Second)))
	w.s.RunUntil(at(10 * time.Second))
	if len(w.records("fault.skip")) == 0 {
		t.Fatalf("no max-down skip")
	}
}

// FLT-086: the recovery undoes by category (heal, heal-link, link-reset, sync-fail, disk-capacity,
// resume, restart), each category in start order. A sync-fail or disk-capacity without an undo that a
// later one of its kind on the same node replaced is not undone (FLT-084).
func TestRecoveryOrder(t *testing.T) {
	w := newW5(full(1), false)
	ctx, got := capture(w, rand.New(rand.NewPCG(3, 4)), w.servers, nil, 3*time.Second) //nolint:gosec // fixed seed
	ms := 300 * time.Millisecond
	r := &fault.Random{MaxDown: 5, Rules: []fault.Rule{
		{Kind: fault.KindPause, Every: ms},
		{Kind: fault.KindCrash, Every: ms},
		{Kind: fault.KindIsolate, Every: ms},
		{Kind: fault.KindLink, Every: ms, Link: &simnet.Link{DropPPM: 1}},
		{Kind: fault.KindDiskCapacity, Every: ms, Magnitude: 10},
		{Kind: fault.KindSyncFail, Every: ms},
		{Kind: fault.KindCut, Every: ms},
	}}
	r.Start(ctx)
	w.s.RunUntil(at(4 * time.Second))
	undo := map[fault.Kind]fault.Kind{fault.KindIsolate: fault.KindHeal, fault.KindCut: fault.KindHealLink, fault.KindLink: fault.KindLinkReset,
		fault.KindSyncFail: fault.KindSyncFail, fault.KindDiskCapacity: fault.KindDiskCapacity, fault.KindPause: fault.KindResume, fault.KindCrash: fault.KindRestart}
	var starts, recovered []fault.Event
	for _, c := range *got {
		if c.at < at(3*time.Second) {
			starts = append(starts, c.e)
		} else {
			recovered = append(recovered, c.e)
		}
	}
	replaced := func(j int) bool {
		s := starts[j]
		return (s.Kind == fault.KindSyncFail || s.Kind == fault.KindDiskCapacity) &&
			slices.ContainsFunc(starts[j+1:], func(l fault.Event) bool { return l.Kind == s.Kind && l.Node == s.Node })
	}
	var want []fault.Event
	n := 0 // replaced starts
	for _, cat := range []fault.Kind{fault.KindHeal, fault.KindHealLink, fault.KindLinkReset, fault.KindSyncFail, fault.KindDiskCapacity, fault.KindResume, fault.KindRestart} {
		for j, s := range starts {
			if undo[s.Kind] != cat {
				continue
			}
			if replaced(j) {
				n++
				continue
			}
			u := fault.Event{Kind: cat, Node: s.Node, Peer: s.Peer}
			if cat == fault.KindHeal {
				u.Node = ""
			}
			want = append(want, u)
		}
	}
	if len(want) < 10 || n == 0 || !reflect.DeepEqual(recovered, want) {
		t.Fatalf("recovery (%d replaced)\n got %v\nwant %v", n, recovered, want)
	}
}

// FLT-087: each Start begins a new run, and the kernel events of an earlier run keep their own
// state: stepping the earlier Sim after a new Start changes only that Sim, and each run equals a
// fresh one byte for byte.
func TestRandomReuseInterleaved(t *testing.T) {
	// R() with its sync-fail rule without an undo, so that the runs also replace entries (FLT-084)
	rules := func() []fault.Rule {
		rs := R()
		rs[8].MinFor, rs[8].MaxFor = 0, 0
		return rs
	}
	fresh := func(seed uint64, until time.Duration) string {
		w := newW5(kernel.Config{Seed: seed}, false)
		startRandom(w, &fault.Random{Rules: rules()}, nil, until)
		w.s.RunUntil(at(30 * time.Second))
		return write(t, w.in.Applied())
	}
	r := &fault.Random{Rules: rules()}
	a := newW5(kernel.Config{Seed: 3}, false)
	startRandom(a, r, nil, 25*time.Second)
	a.s.RunUntil(at(5 * time.Second))
	// B has its own Until, so a run that read the other run's recoverAt would differ from fresh.
	b := newW5(kernel.Config{Seed: 7}, false)
	startRandom(b, r, nil, 8*time.Second)
	a.s.RunUntil(at(30 * time.Second)) // the earlier Sim keeps running, through its recovery
	if n := len(b.in.Applied().Events); n != 0 {
		t.Fatalf("stepping the earlier Sim injected %d events into the new run", n)
	}
	b.s.RunUntil(at(30 * time.Second))
	if write(t, b.in.Applied()) != fresh(7, 8*time.Second) {
		t.Fatalf("the new run differs from a fresh one")
	}
	if write(t, a.in.Applied()) != fresh(3, 25*time.Second) {
		t.Fatalf("the earlier run differs from a fresh one")
	}
}

// FLT-087: a Random stopped mid-run and started on a new Sim behaves exactly like a new one.
// FLT-081: Random does not read Quiet.
func TestRandomReuseMidRun(t *testing.T) {
	run := func(r *fault.Random) string {
		w := newW5(kernel.Config{Seed: 7}, false)
		startRandom(w, r, nil, 25*time.Second)
		w.s.RunUntil(at(30 * time.Second))
		return write(t, w.in.Applied())
	}
	r := &fault.Random{Rules: R()}
	w := newW5(kernel.Config{Seed: 3}, false)
	startRandom(w, r, nil, 25*time.Second)
	w.s.RunUntil(at(5 * time.Second)) // stops with faults and link marks active
	if got, want := run(r), run(&fault.Random{Rules: R()}); got != want {
		t.Fatalf("a reused Random differs from a new one")
	}
	for _, q := range []time.Duration{-1, time.Hour} {
		if got, want := run(&fault.Random{Rules: R(), Quiet: q}), run(&fault.Random{Rules: R()}); got != want {
			t.Fatalf("Quiet %v changed the run", q)
		}
	}
}

func BenchmarkRandom60s(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		w := newW5(kernel.Config{Seed: uint64(i)}, false)
		r := &fault.Random{Rules: R()}
		startRandom(w, r, nil, 60*time.Second)
		w.s.RunUntil(at(60 * time.Second))
	}
}
