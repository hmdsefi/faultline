// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package fault_test

import (
	"errors"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
	"github.com/hmdsefi/faultline/kernel/simdisk"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// AT-FLT-06
func TestCrashNoopRestart(t *testing.T) {
	w := newW5(full(1), false)
	n1 := w.node(1)
	w.in.Inject(fault.Event{Kind: fault.KindCrash, Node: "n1", At: at(5 * time.Second), ID: 42, Undoes: []int{7}})
	if n1.State() != kernel.NodeDown {
		t.Fatalf("n1 state %v", n1.State())
	}
	if got := w.in.Applied().Events[0]; !reflect.DeepEqual(got, fault.Event{ID: 1, At: 0, Kind: fault.KindCrash, Node: "n1"}) {
		t.Fatalf("Applied().Events[0] = %+v", got)
	}
	r := w.last(t, "fault.crash")
	if r.Node != 1 || r.Inc != 1 || r.Text != "crash n1" || attrString(r) != "id=1, source=inject, node=n1, effect=applied" {
		t.Fatalf("fault.crash record %+v", r)
	}
	if kc := w.last(t, "kernel.crash"); kc.Seq <= r.Seq {
		t.Fatalf("fault.crash (seq %d) is not before kernel.crash (seq %d)", r.Seq, kc.Seq)
	}
	w.in.Inject(fault.Event{Kind: fault.KindCrash, Node: "n1"})
	r = w.last(t, "fault.crash")
	if n1.State() != kernel.NodeDown || attr(r, "effect") != "noop" || r.Text != "crash n1 (noop)" || w.in.Applied().Events[1].ID != 2 {
		t.Fatalf("second crash: %+v", r)
	}
	w.in.Inject(fault.Event{Kind: fault.KindRestart, Node: "n1"})
	if n1.State() != kernel.NodeUp || !reflect.DeepEqual(w.in.Applied().Events[2].Undoes, []int{1, 2}) ||
		attrString(w.last(t, "fault.restart")) != "id=3, source=inject, node=n1, undoes=1,2, effect=applied" {
		t.Fatalf("restart: state %v, applied %+v", n1.State(), w.in.Applied().Events[2])
	}
	w.in.Inject(fault.Event{Kind: fault.KindRestart, Node: "n1"})
	if attr(w.last(t, "fault.restart"), "effect") != "noop" || w.in.Applied().Events[3].Undoes != nil {
		t.Fatalf("second restart: %+v", w.in.Applied().Events[3])
	}
}

// AT-FLT-07
func TestPauseAndCrashOfPausedNode(t *testing.T) {
	w := newW5(kernel.Config{Seed: 1}, false)
	n2 := w.node(2)
	steps := []struct {
		kind   fault.Kind
		state  kernel.NodeState
		undoes []int
	}{
		{fault.KindPause, kernel.NodePaused, nil},
		{fault.KindPause, kernel.NodePaused, nil},
		{fault.KindCrash, kernel.NodeDown, []int{1, 2}},
		{fault.KindResume, kernel.NodeDown, nil},
		{fault.KindRestart, kernel.NodeUp, []int{3}},
	}
	for i, st := range steps {
		w.in.Inject(fault.Event{Kind: st.kind, Node: "n2"})
		a := w.in.Applied().Events[i]
		if n2.State() != st.state || a.ID != i+1 || !reflect.DeepEqual(a.Undoes, st.undoes) {
			t.Fatalf("step %d (%s): state %v, applied %+v", i+1, st.kind, n2.State(), a)
		}
	}
}

// AT-FLT-08
func TestLinkOverrideAndReset(t *testing.T) {
	w := newW5(full(1), false)
	L1 := simnet.Link{Latency: 50 * time.Millisecond}
	L2 := simnet.Link{DropPPM: 500000}
	link := func(from, to string, l simnet.Link) {
		w.in.Inject(fault.Event{Kind: fault.KindLink, Node: from, Peer: to, Link: &l})
	}
	reset := func(from, to string) { w.in.Inject(fault.Event{Kind: fault.KindLinkReset, Node: from, Peer: to}) }

	link("n1", "n2", L1)
	if w.nw.Link(1, 2) != L1 {
		t.Fatalf("(a) Link(1, 2) = %+v", w.nw.Link(1, 2))
	}
	link("n1", "n2", L2)
	if w.nw.Link(1, 2) != L2 {
		t.Fatalf("(a) Link(1, 2) = %+v", w.nw.Link(1, 2))
	}
	reset("n1", "n2")
	r := w.last(t, "fault.link_reset")
	if w.nw.Link(1, 2) != simnet.DefaultConfig().Default || attr(r, "effect") != "applied" || attr(r, "undoes") != "1,2" {
		t.Fatalf("(a) after link-reset: Link %+v, record %s", w.nw.Link(1, 2), attrString(r))
	}
	if nr := w.last(t, "net.link_reset"); nr.Seq <= r.Seq {
		t.Fatalf("(a) net.link_reset does not follow fault.link_reset")
	}
	reset("n1", "n2")
	if attr(w.last(t, "fault.link_reset"), "effect") != "noop" || w.nw.Link(1, 2) != simnet.DefaultConfig().Default {
		t.Fatalf("(a) second link-reset is not a no-op: Link(1, 2) = %+v", w.nw.Link(1, 2))
	}

	w.nw.SetLink(2, 3, L1)
	link("n2", "n3", L2)
	before := len(w.records("net.link_config"))
	reset("n2", "n3")
	if w.nw.Link(2, 3) != L1 || len(w.records("net.link_config")) != before+1 {
		t.Fatalf("(b) Link(2, 3) = %+v, want L1 restored with SetLink", w.nw.Link(2, 3))
	}

	w.in.Inject(fault.Event{Kind: fault.KindCut, Node: "n1", Peer: "n4"})
	link("n1", "n4", L1)
	reset("n1", "n4")
	if w.nw.Connected(1, 4) {
		t.Fatalf("(c) link-reset changed connectivity")
	}
	link("n1", "n5", L1)
	w.in.Inject(fault.Event{Kind: fault.KindHeal})
	if w.nw.Link(1, 5) != L1 || !w.nw.Connected(1, 4) {
		t.Fatalf("(c) heal changed a link config or did not heal")
	}
	lr := w.last(t, "fault.link")
	if attrString(lr) != "id=10, source=inject, node=n1, peer=n5, latency_ns=50000000, jitter_ns=0, tail_ppm=0, tail_ns=0, drop_ppm=0, dup_ppm=0, fifo=false, effect=applied" {
		t.Fatalf("fault.link attrs %s", attrString(lr))
	}
	all := simnet.Link{Latency: time.Millisecond, Jitter: 2 * time.Millisecond, TailPPM: 3, Tail: 4 * time.Millisecond, DropPPM: 5, DupPPM: 6, FIFO: true}
	link("n3", "n1", all)
	lr = w.last(t, "fault.link")
	if attrString(lr) != "id=12, source=inject, node=n3, peer=n1, latency_ns=1000000, jitter_ns=2000000, tail_ppm=3, tail_ns=4000000, drop_ppm=5, dup_ppm=6, fifo=true, effect=applied" ||
		lr.Text != "link n3 -> n1: latency=1ms jitter=2ms tail=3ppm/4ms drop=5ppm dup=6ppm fifo=true" || w.nw.Link(3, 1) != all {
		t.Fatalf("fault.link with every field: %s, %q", attrString(lr), lr.Text)
	}
}

// AT-FLT-09
func TestNetworkKinds(t *testing.T) {
	w := newW5(full(1), true)
	w.in.Inject(fault.Event{Kind: fault.KindPartition, Groups: [][]string{{"n1", "n2"}, {"n3", "n4", "n5"}}})
	if !w.nw.Connected(1, 2) || !w.nw.Connected(3, 5) || w.nw.Connected(1, 3) || w.nw.Connected(3, 1) {
		t.Fatalf("partition topology wrong")
	}
	for id := kernel.NodeID(1); id <= 5; id++ {
		if w.nw.Connected(6, id) || w.nw.Connected(id, 6) {
			t.Fatalf("c1 (unlisted) still connected to server %d", id)
		}
	}
	r := w.last(t, "fault.partition")
	if r.Node != 0 || attrString(r) != "id=1, source=inject, groups=n1,n2|n3,n4,n5, effect=applied" {
		t.Fatalf("fault.partition record %+v", r)
	}
	if g := attr(w.last(t, "net.partition"), "groups"); g != "n1,n2|n3,n4,n5|c1" {
		t.Fatalf("net.partition groups %q: the groups are not passed in event order", g)
	}
	w.in.Inject(fault.Event{Kind: fault.KindHeal})
	if r := w.last(t, "fault.heal"); r.Node != 0 || r.Text != "heal" || attrString(r) != "id=2, source=inject, undoes=1, effect=applied" {
		t.Fatalf("fault.heal record %+v", r)
	}
	for a := kernel.NodeID(1); a <= 6; a++ {
		for b := kernel.NodeID(1); b <= 6; b++ {
			if !w.nw.Connected(a, b) {
				t.Fatalf("heal: %d -> %d not connected", a, b)
			}
		}
	}
	w.in.Inject(fault.Event{Kind: fault.KindCut, Node: "n1", Peer: "n2"})
	if w.nw.Connected(1, 2) || !w.nw.Connected(2, 1) {
		t.Fatalf("cut n1 -> n2")
	}
	w.in.Inject(fault.Event{Kind: fault.KindHealLink, Node: "n1", Peer: "n2"})
	if !w.nw.Connected(1, 2) {
		t.Fatalf("heal-link n1 -> n2")
	}
	if r := w.last(t, "fault.heal_link"); r.Node != 1 || r.Text != "heal-link n1 -> n2" ||
		attrString(r) != "id=4, source=inject, node=n1, peer=n2, undoes=3, effect=applied" {
		t.Fatalf("fault.heal_link record %+v", r)
	}
	w.in.Inject(fault.Event{Kind: fault.KindIsolate, Node: "n3"})
	for id := kernel.NodeID(1); id <= 6; id++ {
		if id != 3 && (w.nw.Connected(id, 3) || w.nw.Connected(3, id)) {
			t.Fatalf("isolate n3: edge with %d remains", id)
		}
	}
}

// AT-FLT-10
func TestDiskKinds(t *testing.T) {
	w := newW5(full(1), false)
	v := w.d.Volume(w.node(1))
	f, err := v.Create("/f")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Append(make([]byte, 100)); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	inject := func(e fault.Event) fault.Event {
		w.in.Inject(e)
		a := w.in.Applied().Events
		return a[len(a)-1]
	}
	inject(fault.Event{Kind: fault.KindSyncFail, Node: "n1", N: 2})
	err1, err2, err3 := f.Sync(), f.Sync(), f.Sync()
	if !errors.Is(err1, simdisk.ErrIO) || !errors.Is(err2, simdisk.ErrIO) || err3 != nil {
		t.Fatalf("(1) sync-fail n=2 did not fail exactly two syncs: %v, %v, %v", err1, err2, err3)
	}
	if a := inject(fault.Event{Kind: fault.KindSyncFail, Node: "n1", N: 2}); !reflect.DeepEqual(a.Undoes, []int{1}) {
		t.Fatalf("(2) Undoes %v", a.Undoes)
	}
	if a := inject(fault.Event{Kind: fault.KindSyncFail, Node: "n1", N: 0}); !reflect.DeepEqual(a.Undoes, []int{2}) || f.Sync() != nil {
		t.Fatalf("(3) Undoes %v or Sync failed", a.Undoes)
	}
	inject(fault.Event{Kind: fault.KindDiskCapacity, Node: "n1", N: 10})
	if _, err := f.Append(make([]byte, 1024)); !errors.Is(err, simdisk.ErrNoSpace) {
		t.Fatalf("(4) Append = %v, want ErrNoSpace", err)
	}
	if a := inject(fault.Event{Kind: fault.KindDiskCapacity, Node: "n1", N: 0}); !reflect.DeepEqual(a.Undoes, []int{4}) {
		t.Fatalf("(5) Undoes %v", a.Undoes)
	}
	if _, err := f.Append(make([]byte, 1024)); err != nil {
		t.Fatalf("(5) Append = %v", err)
	}
	inject(fault.Event{Kind: fault.KindCorrupt, Node: "n1", Path: "/missing", Off: 0, Len: 1})
	c, e := w.last(t, "fault.corrupt"), w.last(t, "fault.error")
	if attr(c, "effect") != "applied" || attr(e, "id") != "6" || e.Seq <= c.Seq || e.Node != 1 {
		t.Fatalf("(6) fault.corrupt %s, fault.error %+v", attrString(c), e)
	}
	if e.Text != "corrupt 6 failed: corrupt /missing: file does not exist" || attrString(e) != "id=6, error=corrupt /missing: file does not exist" {
		t.Fatalf("(6) fault.error Text %q, attrs %s", e.Text, attrString(e))
	}
}

// FLT-032: corrupt calls Corrupt(Path, Off, Len); bytes at or past the durable size are not
// damaged, and the event is still applied without a fault.error.
func TestCorrupt(t *testing.T) {
	w := newW5(full(1), false)
	f, err := w.d.Volume(w.node(1)).Create("/f")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Append(make([]byte, 100)); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Append(make([]byte, 50)); err != nil { // not synced: past the durable size
		t.Fatal(err)
	}
	for i, c := range []struct {
		off     int64
		n       int
		attrs   string
		damaged string
	}{
		{10, 4, "id=1, source=inject, node=n1, path=/f, off=10, len=4, effect=applied", "4"},
		{98, 10, "id=2, source=inject, node=n1, path=/f, off=98, len=10, effect=applied", "2"},
		{120, 5, "id=3, source=inject, node=n1, path=/f, off=120, len=5, effect=applied", "0"},
	} {
		w.in.Inject(fault.Event{Kind: fault.KindCorrupt, Node: "n1", Path: "/f", Off: c.off, Len: c.n})
		r, d := w.last(t, "fault.corrupt"), w.last(t, "disk.corrupt")
		if attrString(r) != c.attrs || d.Seq <= r.Seq || attr(d, "off") != strconv.FormatInt(c.off, 10) || attr(d, "len") != c.damaged {
			t.Fatalf("corrupt %d: fault.corrupt %s, disk.corrupt %s", i+1, attrString(r), attrString(d))
		}
	}
	if len(w.records("fault.error")) != 0 {
		t.Fatalf("fault.error for a corrupt of an existing file")
	}
}

// AT-FLT-11
func TestClockKinds(t *testing.T) {
	w := newW5(full(1), false)
	n4 := w.node(4)
	w.in.Inject(fault.Event{Kind: fault.KindClockJump, Node: "n4", N: -250000000})
	if !n4.Now().Equal(kernel.Epoch.Add(-250 * time.Millisecond)) {
		t.Fatalf("after clock-jump n4.Now() = %v", n4.Now())
	}
	w.in.Inject(fault.Event{Kind: fault.KindClockDrift, Node: "n4", N: 1000})
	w.s.RunUntil(at(time.Second))
	if !n4.Now().Equal(kernel.Epoch.Add(time.Second - 250*time.Millisecond + time.Millisecond)) {
		t.Fatalf("after clock-drift n4.Now() = %v", n4.Now())
	}
	n4.Crash()
	w.in.Inject(fault.Event{Kind: fault.KindClockJump, Node: "n4", N: 1})
	w.in.Inject(fault.Event{Kind: fault.KindClockDrift, Node: "n4", N: 0})
	if !n4.Now().Equal(kernel.Epoch.Add(time.Second-250*time.Millisecond+time.Millisecond+1)) || n4.Drift() != 0 {
		t.Fatalf("clock faults on a down node did not reach the clock: Now %v, drift %d", n4.Now(), n4.Drift())
	}
	if attr(w.last(t, "fault.clock_jump"), "effect") != "applied" || attr(w.last(t, "fault.clock_drift"), "effect") != "applied" {
		t.Fatalf("clock faults on a down node are not applied")
	}
	if got := w.last(t, "fault.clock_jump"); got.Text != "clock-jump n4 1ns" || attrString(got) != "id=3, source=inject, node=n4, n=1, effect=applied" {
		t.Fatalf("fault.clock_jump %+v", got)
	}
	// FLT-030 step 4, FLT-033: each applied event has At = Now() when it was applied, and the
	// concrete schedule has End and Recovery 0
	want := fault.Schedule{Version: fault.ScheduleVersion, Events: []fault.Event{
		{Kind: fault.KindClockJump, Node: "n4", N: -250000000, ID: 1},
		{Kind: fault.KindClockDrift, Node: "n4", N: 1000, ID: 2},
		{At: at(time.Second), Kind: fault.KindClockJump, Node: "n4", N: 1, ID: 3},
		{At: at(time.Second), Kind: fault.KindClockDrift, Node: "n4", ID: 4},
	}}
	if got := w.in.Applied(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Applied() = %+v, want %+v", got, want)
	}
}

// FLT-030, FLT-031, AT-FLT-27 (Inject prefix), FLT-033
func TestInjectMisuse(t *testing.T) {
	w := newW5(full(1), false)
	mustPanic(t, `fault: Inject: unknown kind "x"`, func() { w.in.Inject(fault.Event{Kind: "x"}) })
	mustPanic(t, `fault: Inject: crash: unknown node "zz"`, func() { w.in.Inject(fault.Event{Kind: fault.KindCrash, Node: "zz"}) })
	mustPanic(t, `fault: Inject: partition: unknown node "n9"`,
		func() { w.in.Inject(fault.Event{Kind: fault.KindPartition, Groups: [][]string{{"n1"}, {"n2", "n9"}}}) })
	mustPanic(t, `fault: Inject: crash: role "leader" is not resolved; only planners resolve roles`,
		func() { w.in.Inject(fault.Event{Kind: fault.KindCrash, Role: "leader"}) })
	// two rules broken at once: the earlier step of FLT-030 wins
	mustPanic(t, "fault: Inject: crash: at must be >= 0 (got -1ns)", func() { w.in.Inject(fault.Event{Kind: fault.KindCrash, Role: "r", At: -1}) })
	mustPanic(t, `fault: Inject: cut: unknown node "zz"`, func() { w.in.Inject(fault.Event{Kind: fault.KindCut, Node: "zz", Peer: "yy"}) })
	mustPanic(t, `fault: Inject: partition: unknown node "x1"`,
		func() { w.in.Inject(fault.Event{Kind: fault.KindPartition, Groups: [][]string{{"n1", "x1"}, {"x2"}}}) })
	bare := fault.NewInjector(w.s, nil, nil)
	mustPanic(t, `fault: Inject: cut: unknown node "zz"`, func() { bare.Inject(fault.Event{Kind: fault.KindCut, Node: "zz", Peer: "n1"}) })
	mustPanic(t, "fault: Inject: heal: injector has no simnet.Network", func() { bare.Inject(fault.Event{Kind: fault.KindHeal}) })
	mustPanic(t, "fault: Inject: sync-fail: injector has no simdisk.Disks",
		func() { bare.Inject(fault.Event{Kind: fault.KindSyncFail, Node: "n1", N: 1}) })
	mustPanic(t, "fault: NewInjector: nil *kernel.Sim", func() { fault.NewInjector(nil, nil, nil) })
	if len(w.in.Applied().Events) != 0 || len(bare.Applied().Events) != 0 {
		t.Fatalf("a panicking Inject was recorded")
	}

	// steps 2-4 complete before the call: an Inject from a crash hook gets the next ID
	w.s.OnCrash(func(n *kernel.Node) {
		if n.Name() == "n3" {
			w.in.Inject(fault.Event{Kind: fault.KindHeal})
		}
	})
	w.in.Inject(fault.Event{Kind: fault.KindCrash, Node: "n3"})
	a := w.in.Applied()
	if len(a.Events) != 2 || a.Events[0].Kind != fault.KindCrash || a.Events[1].Kind != fault.KindHeal || a.Events[1].ID != 2 {
		t.Fatalf("re-entrant Inject: %+v", a.Events)
	}
	ns, err := a.Normalize()
	if err != nil || !reflect.DeepEqual(ns, a) {
		t.Fatalf("Applied() is not normalized: %v", err)
	}
	a.Events[0].Node = "changed"
	if w.in.Applied().Events[0].Node != "n3" {
		t.Fatalf("Applied() is not a copy")
	}
}

// FLT-031: on an injector with neither network nor disks, every network kind panics, every disk
// kind panics, and the other kinds apply.
func TestComponentsPerKind(t *testing.T) {
	w := newW5(kernel.Config{Seed: 1}, false)
	bare := fault.NewInjector(w.s, nil, nil)
	events := []fault.Event{
		{Kind: fault.KindPartition, Groups: [][]string{{"n1"}, {"n2"}}},
		{Kind: fault.KindIsolate, Node: "n1"},
		{Kind: fault.KindCut, Node: "n1", Peer: "n2"},
		{Kind: fault.KindHeal},
		{Kind: fault.KindHealLink, Node: "n1", Peer: "n2"},
		{Kind: fault.KindLink, Node: "n1", Peer: "n2", Link: &simnet.Link{}},
		{Kind: fault.KindLinkReset, Node: "n1", Peer: "n2"},
		{Kind: fault.KindCrash, Node: "n3"},
		{Kind: fault.KindRestart, Node: "n3"},
		{Kind: fault.KindPause, Node: "n4"},
		{Kind: fault.KindResume, Node: "n4"},
		{Kind: fault.KindClockJump, Node: "n5", N: 1},
		{Kind: fault.KindClockDrift, Node: "n5", N: 1},
		{Kind: fault.KindSyncFail, Node: "n1", N: 1},
		{Kind: fault.KindDiskCapacity, Node: "n1", N: 1},
		{Kind: fault.KindCorrupt, Node: "n1", Path: "/f", Len: 1},
	}
	var applied []fault.Kind
	for i, e := range events {
		if e.Kind != fault.Kinds()[i] {
			t.Fatalf("events[%d] is %s, want %s", i, e.Kind, fault.Kinds()[i])
		}
		switch {
		case i < 7:
			mustPanic(t, "fault: Inject: "+string(e.Kind)+": injector has no simnet.Network", func() { bare.Inject(e) })
		case i >= 13:
			mustPanic(t, "fault: Inject: "+string(e.Kind)+": injector has no simdisk.Disks", func() { bare.Inject(e) })
		default:
			bare.Inject(e)
			applied = append(applied, e.Kind)
		}
	}
	var got []fault.Kind
	for _, e := range bare.Applied().Events {
		got = append(got, e.Kind)
	}
	if !reflect.DeepEqual(got, applied) || len(got) != 6 {
		t.Fatalf("applied %v, want %v", got, applied)
	}
}

// FLT-030 step 4, FLT-033: the applied event holds its own Link and Groups, and Applied returns a
// deep copy whose Link, Undoes and Groups can be changed without changing the next Applied.
func TestAppliedCopies(t *testing.T) {
	w := newW5(kernel.Config{Seed: 1}, false)
	l := simnet.Link{Latency: time.Millisecond}
	w.in.Inject(fault.Event{Kind: fault.KindLink, Node: "n1", Peer: "n2", Link: &l})
	w.in.Inject(fault.Event{Kind: fault.KindLinkReset, Node: "n1", Peer: "n2"})
	l.Latency = time.Hour
	a := w.in.Applied()
	if a.Events[0].Link.Latency != time.Millisecond {
		t.Fatalf("editing the injected Link changed the applied event: %+v", a.Events[0].Link)
	}
	a.Events[0].Link.Latency = time.Second
	a.Events[1].Undoes[0] = 99
	if b := w.in.Applied(); b.Events[0].Link.Latency != time.Millisecond || !reflect.DeepEqual(b.Events[1].Undoes, []int{1}) {
		t.Fatalf("editing Applied() changed the next Applied(): %+v, %v", b.Events[0].Link, b.Events[1].Undoes)
	}
	groups := [][]string{{"n1", "n2"}, {"n3", "n4", "n5"}}
	w.in.Inject(fault.Event{Kind: fault.KindPartition, Groups: groups})
	groups[0][0] = "zz"
	w.in.Applied().Events[2].Groups[1][0] = "yy"
	if g := w.in.Applied().Events[2].Groups; !reflect.DeepEqual(g, [][]string{{"n1", "n2"}, {"n3", "n4", "n5"}}) {
		t.Fatalf("editing the injected Groups or Applied() changed the next Applied(): %v", g)
	}
}

// FLT-032: the node-state rules, also for the states the ATs leave out. Network and disk kinds
// apply to a down node; crash applies to a node that has not booted yet.
func TestNodeStateEffects(t *testing.T) {
	w := newW5(full(1), false)
	n6 := w.s.AddNode("n6", func(*kernel.Node) {}) // down, incarnation 0, boot pending
	steps := []struct {
		e      fault.Event
		effect string
	}{
		{fault.Event{Kind: fault.KindCrash, Node: "n6"}, "applied"},
		{fault.Event{Kind: fault.KindRestart, Node: "n2"}, "noop"},
		{fault.Event{Kind: fault.KindResume, Node: "n2"}, "noop"},
		{fault.Event{Kind: fault.KindPause, Node: "n2"}, "applied"},
		{fault.Event{Kind: fault.KindRestart, Node: "n2"}, "noop"},
		{fault.Event{Kind: fault.KindPause, Node: "n2"}, "noop"},
		{fault.Event{Kind: fault.KindResume, Node: "n2"}, "applied"},
		{fault.Event{Kind: fault.KindCrash, Node: "n3"}, "applied"},
		{fault.Event{Kind: fault.KindPause, Node: "n3"}, "noop"},
		{fault.Event{Kind: fault.KindResume, Node: "n3"}, "noop"},
		{fault.Event{Kind: fault.KindIsolate, Node: "n3"}, "applied"},
		{fault.Event{Kind: fault.KindCut, Node: "n3", Peer: "n1"}, "applied"},
		{fault.Event{Kind: fault.KindSyncFail, Node: "n3", N: 1}, "applied"},
		{fault.Event{Kind: fault.KindDiskCapacity, Node: "n3", N: 1}, "applied"},
		{fault.Event{Kind: fault.KindCorrupt, Node: "n3", Path: "/f", Len: 1}, "applied"},
	}
	for i, st := range steps {
		w.in.Inject(st.e)
		r := w.last(t, "fault."+strings.ReplaceAll(string(st.e.Kind), "-", "_"))
		if got := attr(r, "effect"); got != st.effect || attr(r, "id") != strconv.Itoa(i+1) {
			t.Fatalf("step %d (%s): effect %s, want %s", i+1, st.e, got, st.effect)
		}
	}
	if len(w.records("disk.fail_syncs")) != 1 || len(w.records("disk.capacity")) != 1 {
		t.Fatalf("disk faults on a down node did not reach the volume")
	}
	w.s.RunUntil(at(time.Second))
	if n6.State() != kernel.NodeDown || n6.Incarnation() != 0 || w.node(2).State() != kernel.NodeUp {
		t.Fatalf("n6 %v (incarnation %d), n2 %v", n6.State(), n6.Incarnation(), w.node(2).State())
	}
	if w.nw.Connected(1, 3) || w.nw.Connected(3, 1) {
		t.Fatalf("isolate of a down node did not cut its links")
	}
}

// FLT §9: in steady state a pause and its resume allocate 7 objects: for each event its "id"
// value and its record text, and for the resume also its Undoes, its "undoes" value and the
// kernel's resume. A record that stops reusing its attribute buffer allocates more objects. The
// applied list grows in blocks that are never copied, so a pause and its resume cost about the
// size of two events (about 470 bytes); a list that grows one slice copies its events and costs
// more than 1000 bytes, so it fails the bound of 800. Applied still returns every event in order
// across the blocks.
func TestInjectAllocs(t *testing.T) {
	s := kernel.New(kernel.Config{Seed: 1})
	s.AddNode("n1", func(*kernel.Node) {})
	s.RunUntil(0)
	in := fault.NewInjector(s, nil, nil)
	pause := fault.Event{Kind: fault.KindPause, Node: "n1"}
	resume := fault.Event{Kind: fault.KindResume, Node: "n1"}
	step := func() {
		in.Inject(pause)
		in.Inject(resume)
	}
	for range 50 {
		step() // IDs from 101 on: strconv has the strings of smaller numbers ready
	}
	if n := testing.AllocsPerRun(100, step); n > 7 {
		t.Errorf("pause + resume: %v allocations, want at most 7", n)
	}
	if b := bytesPerRun(1000, step); b > 800 {
		t.Errorf("pause + resume: %d bytes, want at most 800", b)
	}
	events := in.Applied().Events
	for i, e := range events {
		if e.ID != i+1 || e.Kind != []fault.Kind{fault.KindPause, fault.KindResume}[i%2] {
			t.Fatalf("applied[%d] = %+v", i, e)
		}
	}
	if len(events) != 2302 {
		t.Fatalf("%d applied events, want 2302", len(events))
	}
}

// bytesPerRun is testing.AllocsPerRun for bytes: the average number of heap bytes f allocates.
func bytesPerRun(runs uint64, f func()) uint64 {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	before := m.TotalAlloc
	for range runs {
		f()
	}
	runtime.ReadMemStats(&m)
	return (m.TotalAlloc - before) / runs
}

func BenchmarkInjectPauseResume(b *testing.B) {
	s := kernel.New(kernel.Config{Seed: 1})
	for _, name := range []string{"n1", "n2", "n3"} {
		s.AddNode(name, func(*kernel.Node) {})
	}
	s.RunUntil(0)
	in := fault.NewInjector(s, nil, nil)
	pause := fault.Event{Kind: fault.KindPause, Node: "n1"}
	resume := fault.Event{Kind: fault.KindResume, Node: "n1"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		in.Inject(pause)
		in.Inject(resume)
	}
}
