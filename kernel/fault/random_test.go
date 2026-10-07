package fault_test

import (
	"math/rand/v2"
	"reflect"
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
			`fault: Random: rules[0]: shape "ring" requires the partition planner (Phase 3)`},
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
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindCorrupt, Every: s, MaxFor: s, Path: "/f"}}},
			"fault: Random: rules[0]: corrupt has no undo; MinFor and MaxFor must be 0"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindClockJump, Every: s}}},
			"fault: Random: rules[0]: clock-jump requires Magnitude in [1, 36000000000000000] (got 0)"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindClockDrift, Every: s, Magnitude: 500001}}},
			"fault: Random: rules[0]: clock-drift requires Magnitude in [1, 500000] (got 500001)"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindCorrupt, Every: s, Path: "/f", Magnitude: 1048577}}},
			"fault: Random: rules[0]: corrupt requires Magnitude in [0, 1048576] (got 1048577)"},
		// two checks fail at once: the earlier one of FLT-081 wins, for the first failing rule
		{fault.Random{Rules: []fault.Rule{{Kind: "x"}}}, `fault: Random: rules[0]: unknown kind "x"`},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindCrash, MinFor: 2 * s, MaxFor: s}}}, "fault: Random: rules[0]: Every must be in (0s, 10000h0m0s] (got 0s)"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindClockJump, Every: s, MaxFor: s, Target: "boss", Magnitude: 5}}},
			"fault: Random: rules[0]: clock-jump has no undo; MinFor and MaxFor must be 0"},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindCrash, Every: s, Target: "boss", Shape: ring{}}}}, `fault: Random: rules[0]: unknown role "boss"`},
		{fault.Random{Rules: []fault.Rule{{Kind: fault.KindPartition, Every: s, Shape: ring{}, Link: &simnet.Link{}}}},
			`fault: Random: rules[0]: shape "ring" requires the partition planner (Phase 3)`},
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
	type expect func(ref *rand.Rand, w *world) []captured
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
		{"clock-drift", fault.Rule{Kind: fault.KindClockDrift, Every: second, Magnitude: 100}, func(ref *rand.Rand, _ *world) []captured {
			t0 := at(between(ref, time.Second, 3*time.Second))
			e := ev(fault.KindClockDrift, names[ref.IntN(5)])
			e.N = ref.Int64N(201) - 100
			return []captured{{t0, e}}
		}},
		{"sync-fail", fault.Rule{Kind: fault.KindSyncFail, Every: second, MinFor: 0, MaxFor: time.Second}, func(ref *rand.Rand, _ *world) []captured {
			t0 := at(between(ref, time.Second, 3*time.Second))
			n := names[ref.IntN(5)]
			f, u := ev(fault.KindSyncFail, n), ev(fault.KindSyncFail, n)
			f.N = 1
			return []captured{{t0, f}, {t0 + at(between(ref, 0, time.Second)), u}}
		}},
		{"disk-capacity", fault.Rule{Kind: fault.KindDiskCapacity, Every: second, Magnitude: 4096}, func(ref *rand.Rand, _ *world) []captured {
			t0 := at(between(ref, time.Second, 3*time.Second))
			f := ev(fault.KindDiskCapacity, names[ref.IntN(5)])
			f.N = 4096
			return []captured{{t0, f}}
		}},
		{"cut", fault.Rule{Kind: fault.KindCut, Every: second, MinFor: time.Second, MaxFor: time.Second}, func(ref *rand.Rand, _ *world) []captured {
			t0 := at(between(ref, time.Second, 3*time.Second))
			i := ref.IntN(5)
			peers := append(append([]string(nil), names[:i]...), names[i+1:]...)
			p := peers[ref.IntN(4)]
			d := between(ref, time.Second, time.Second)
			return []captured{{t0, fault.Event{Kind: fault.KindCut, Node: names[i], Peer: p}}, {t0 + at(d), fault.Event{Kind: fault.KindHealLink, Node: names[i], Peer: p}}}
		}},
		{"link", fault.Rule{Kind: fault.KindLink, Every: second, Link: &simnet.Link{DropPPM: 7}}, func(ref *rand.Rand, _ *world) []captured {
			t0 := at(between(ref, time.Second, 3*time.Second))
			i := ref.IntN(5)
			peers := append(append([]string(nil), names[:i]...), names[i+1:]...)
			return []captured{{t0, fault.Event{Kind: fault.KindLink, Node: names[i], Peer: peers[ref.IntN(4)], Link: &simnet.Link{DropPPM: 7}}}}
		}},
		{"crash", fault.Rule{Kind: fault.KindCrash, Every: second, MaxFor: 500 * time.Millisecond}, func(ref *rand.Rand, _ *world) []captured {
			t0 := at(between(ref, time.Second, 3*time.Second))
			n := names[ref.IntN(5)]
			return []captured{{t0, ev(fault.KindCrash, n)}, {t0 + at(between(ref, 0, 500*time.Millisecond)), ev(fault.KindRestart, n)}}
		}},
		{"corrupt", fault.Rule{Kind: fault.KindCorrupt, Every: second, Path: "/f", Target: "one"}, func(ref *rand.Rand, w *world) []captured {
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
		want := c.want(ref, w)
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

// FLT-084: eligibility by node state and kind, MaxDown limits only crash and pause, candidates
// are deduplicated servers in ID order, and a partition needs two candidates.
func TestRandomCandidates(t *testing.T) {
	ms := 500 * time.Millisecond
	// run starts rule on a W5 where n2 is down and n3 paused, with servers n5, n1..n5 (n5 twice, not
	// in ID order), and returns the fault.target candidates and the captured events.
	run := func(maxDown int, rule fault.Rule, setup ...fault.Event) (*world, []string, []captured) {
		w := newW5(full(1), false)
		for _, e := range setup {
			w.in.Inject(e)
		}
		ctx, got := capture(w, w.s.Rand("fault/random"), append([]*kernel.Node{w.node(5)}, w.servers...), nil, time.Second)
		(&fault.Random{MaxDown: maxDown, Rules: []fault.Rule{rule}}).Start(ctx)
		w.s.RunUntil(at(time.Second))
		var cands []string
		for _, tr := range w.records("fault.target") {
			cands = append(cands, attr(tr, "candidates"))
		}
		return w, cands, *got
	}
	downAndPaused := []fault.Event{{Kind: fault.KindCrash, Node: "n2"}, {Kind: fault.KindPause, Node: "n3"}}
	for _, c := range []struct {
		kind fault.Kind
		want string
	}{{fault.KindCrash, "n1,n4,n5"}, {fault.KindPause, "n1,n4,n5"}, {fault.KindIsolate, "n1,n3,n4,n5"}, {fault.KindClockJump, "n1,n3,n4,n5"}} {
		rule := fault.Rule{Kind: c.kind, Every: ms}
		if c.kind == fault.KindClockJump {
			rule.Magnitude = 1
		}
		_, cands, _ := run(5, rule, downAndPaused...)
		if len(cands) == 0 {
			t.Fatalf("%s: no target", c.kind)
		}
		for _, got := range cands {
			if got != c.want {
				t.Fatalf("%s: candidates %s, want %s", c.kind, got, c.want)
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
// resume, restart), each category in start order.
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
	var want []fault.Event
	for _, cat := range []fault.Kind{fault.KindHeal, fault.KindHealLink, fault.KindLinkReset, fault.KindSyncFail, fault.KindDiskCapacity, fault.KindResume, fault.KindRestart} {
		for _, s := range starts {
			if undo[s.Kind] != cat {
				continue
			}
			u := fault.Event{Kind: cat, Node: s.Node, Peer: s.Peer}
			if cat == fault.KindHeal {
				u.Node = ""
			}
			want = append(want, u)
		}
	}
	if len(want) < 10 || !reflect.DeepEqual(recovered, want) {
		t.Fatalf("recovery\n got %v\nwant %v", recovered, want)
	}
}

// FLT-087: a Random stopped mid-run and started on a new Sim behaves exactly like a new one.
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
