package fault_test

import (
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// AT-FLT-12
func TestPairingTable(t *testing.T) {
	l := &simnet.Link{}
	events := []fault.Event{
		{Kind: "crash", Node: "a"},                            // 1
		{Kind: "pause", Node: "b"},                            // 2
		{Kind: "crash", Node: "b"},                            // 3
		{Kind: "cut", Node: "a", Peer: "b"},                   // 4
		{Kind: "partition", Groups: [][]string{{"a"}, {"b"}}}, // 5
		{Kind: "heal-link", Node: "a", Peer: "b"},             // 6
		{Kind: "heal"},                                        // 7
		{Kind: "link", Node: "a", Peer: "b", Link: l},         // 8
		{Kind: "link", Node: "a", Peer: "b", Link: l},         // 9
		{Kind: "link-reset", Node: "a", Peer: "b"},            // 10
		{Kind: "sync-fail", Node: "a", N: 3},                  // 11
		{Kind: "sync-fail", Node: "a", N: 1},                  // 12
		{Kind: "sync-fail", Node: "a", N: 0},                  // 13
		{Kind: "disk-capacity", Node: "a", N: 5},              // 14
		{Kind: "disk-capacity", Node: "a", N: 0},              // 15
		{Kind: "restart", Node: "a"},                          // 16
		{Kind: "restart", Node: "b"},                          // 17
	}
	ns, err := fault.Schedule{Events: events}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	want := [][]int{nil, nil, {2}, nil, nil, {4}, {5}, nil, nil, {8, 9}, nil, {11}, {12}, nil, {14}, {1}, {3}}
	if len(ns.Events) != len(want) {
		t.Fatalf("Normalize returned %d events, want %d", len(ns.Events), len(want))
	}
	for i, e := range ns.Events {
		if e.ID != i+1 || !reflect.DeepEqual(e.Undoes, want[i]) {
			t.Errorf("event %d: ID %d Undoes %v, want ID %d Undoes %v", i+1, e.ID, e.Undoes, i+1, want[i])
		}
	}
}

// FLT-010, FLT-012: every row of the pairing table, with near misses (another node, another or
// reversed link, another kind, N = 0). All events are at 0s, so Normalize keeps their order.
func TestPairingRows(t *testing.T) {
	l := &simnet.Link{}
	cases := []struct {
		name   string
		events []fault.Event
		want   [][]int
	}{
		{"heal ends partition, isolate and cut", []fault.Event{
			{Kind: "partition", Groups: [][]string{{"a"}, {"b"}}}, // 1
			{Kind: "isolate", Node: "c"},                          // 2
			{Kind: "cut", Node: "a", Peer: "b"},                   // 3
			{Kind: "link", Node: "a", Peer: "b", Link: l},         // 4
			{Kind: "crash", Node: "a"},                            // 5
			{Kind: "pause", Node: "b"},                            // 6
			{Kind: "sync-fail", Node: "a", N: 1},                  // 7
			{Kind: "disk-capacity", Node: "a", N: 1},              // 8
			{Kind: "heal"},                                        // 9
			{Kind: "heal"},                                        // 10
		}, [][]int{nil, nil, nil, nil, nil, nil, nil, nil, {1, 2, 3}, nil}},
		{"heal-link ends cut on the same link", []fault.Event{
			{Kind: "cut", Node: "a", Peer: "b"},           // 1
			{Kind: "cut", Node: "b", Peer: "a"},           // 2
			{Kind: "cut", Node: "a", Peer: "c"},           // 3
			{Kind: "cut", Node: "c", Peer: "b"},           // 4
			{Kind: "link", Node: "a", Peer: "b", Link: l}, // 5
			{Kind: "isolate", Node: "a"},                  // 6
			{Kind: "heal-link", Node: "a", Peer: "b"},     // 7
			{Kind: "heal-link", Node: "a", Peer: "b"},     // 8
		}, [][]int{nil, nil, nil, nil, nil, nil, {1}, nil}},
		{"link-reset ends every link on the same link", []fault.Event{
			{Kind: "link", Node: "a", Peer: "b", Link: l}, // 1
			{Kind: "link", Node: "b", Peer: "a", Link: l}, // 2
			{Kind: "link", Node: "a", Peer: "c", Link: l}, // 3
			{Kind: "link", Node: "c", Peer: "b", Link: l}, // 4
			{Kind: "cut", Node: "a", Peer: "b"},           // 5
			{Kind: "link", Node: "a", Peer: "b", Link: l}, // 6
			{Kind: "link-reset", Node: "a", Peer: "b"},    // 7
			{Kind: "link-reset", Node: "a", Peer: "b"},    // 8
		}, [][]int{nil, nil, nil, nil, nil, nil, {1, 6}, nil}},
		{"restart ends crash on the same node", []fault.Event{
			{Kind: "crash", Node: "n1"},   // 1
			{Kind: "crash", Node: "n1"},   // 2
			{Kind: "crash", Node: "n2"},   // 3
			{Kind: "pause", Node: "n1"},   // 4
			{Kind: "restart", Node: "n1"}, // 5
			{Kind: "restart", Node: "n1"}, // 6
		}, [][]int{nil, nil, nil, nil, {1, 2}, nil}},
		{"resume ends pause on the same node", []fault.Event{
			{Kind: "pause", Node: "n1"},   // 1
			{Kind: "pause", Node: "n2"},   // 2
			{Kind: "pause", Node: "n1"},   // 3
			{Kind: "isolate", Node: "n1"}, // 4
			{Kind: "resume", Node: "n1"},  // 5
			{Kind: "resume", Node: "n1"},  // 6
		}, [][]int{nil, nil, nil, nil, {1, 3}, nil}},
		{"crash ends pause on the same node", []fault.Event{
			{Kind: "pause", Node: "n1"},   // 1
			{Kind: "pause", Node: "n2"},   // 2
			{Kind: "crash", Node: "n1"},   // 3
			{Kind: "crash", Node: "n1"},   // 4
			{Kind: "resume", Node: "n1"},  // 5
			{Kind: "restart", Node: "n1"}, // 6
		}, [][]int{nil, nil, {1}, nil, nil, {3, 4}}},
		{"sync-fail ends sync-fail on the same node, and N = 0 is not durable", []fault.Event{
			{Kind: "sync-fail", Node: "a", N: 3},     // 1
			{Kind: "sync-fail", Node: "b", N: 3},     // 2
			{Kind: "disk-capacity", Node: "a", N: 3}, // 3
			{Kind: "sync-fail", Node: "a", N: 0},     // 4
			{Kind: "sync-fail", Node: "a", N: 0},     // 5
			{Kind: "sync-fail", Node: "a", N: 2},     // 6
			{Kind: "sync-fail", Node: "a", N: 5},     // 7
		}, [][]int{nil, nil, nil, {1}, nil, nil, {6}}},
		{"disk-capacity ends disk-capacity on the same node, and N = 0 is not durable", []fault.Event{
			{Kind: "disk-capacity", Node: "a", N: 3}, // 1
			{Kind: "disk-capacity", Node: "b", N: 3}, // 2
			{Kind: "sync-fail", Node: "a", N: 3},     // 3
			{Kind: "disk-capacity", Node: "a", N: 0}, // 4
			{Kind: "disk-capacity", Node: "a", N: 0}, // 5
			{Kind: "disk-capacity", Node: "a", N: 7}, // 6
			{Kind: "disk-capacity", Node: "a", N: 9}, // 7
		}, [][]int{nil, nil, nil, {1}, nil, nil, {6}}},
		{"other kinds end nothing", []fault.Event{
			{Kind: "partition", Groups: [][]string{{"a"}, {"b"}}}, // 1
			{Kind: "partition", Groups: [][]string{{"a"}, {"b"}}}, // 2
			{Kind: "isolate", Node: "a"},                          // 3
			{Kind: "isolate", Node: "a"},                          // 4
			{Kind: "cut", Node: "a", Peer: "b"},                   // 5
			{Kind: "cut", Node: "a", Peer: "b"},                   // 6
			{Kind: "pause", Node: "a"},                            // 7
			{Kind: "pause", Node: "a"},                            // 8
			{Kind: "corrupt", Node: "a", Path: "/f", Len: 1},      // 9
			{Kind: "clock-jump", Node: "a", N: 5},                 // 10
			{Kind: "clock-drift", Node: "a", N: 5},                // 11
			{Kind: "heal"},                                        // 12
			{Kind: "resume", Node: "a"},                           // 13
		}, [][]int{nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, {1, 2, 3, 4, 5, 6}, {7, 8}}},
	}
	for _, c := range cases {
		ns, err := fault.Schedule{Events: c.events}.Normalize()
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var got [][]int
		for _, e := range ns.Events {
			got = append(got, e.Undoes)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: Undoes %v, want %v", c.name, got, c.want)
		}
	}
}

// FLT-012: an event ends only the kinds its row names, and the other kinds end nothing. The
// setup holds one active fault of each durable kind on node a or link a->b, and none of them
// ends an earlier one. Then each kind, in Kinds() order, follows the setup once.
func TestPairingMatrix(t *testing.T) {
	l := &simnet.Link{}
	active := []fault.Event{
		{Kind: "partition", Groups: [][]string{{"a"}, {"b"}}}, // 1
		{Kind: "isolate", Node: "a"},                          // 2
		{Kind: "cut", Node: "a", Peer: "b"},                   // 3
		{Kind: "link", Node: "a", Peer: "b", Link: l},         // 4
		{Kind: "crash", Node: "a"},                            // 5
		{Kind: "pause", Node: "a"},                            // 6
		{Kind: "sync-fail", Node: "a", N: 1},                  // 7
		{Kind: "disk-capacity", Node: "a", N: 1},              // 8
	}
	cases := []struct {
		e    fault.Event
		want []int
	}{
		{fault.Event{Kind: "partition", Groups: [][]string{{"a"}, {"b"}}}, nil},
		{fault.Event{Kind: "isolate", Node: "a"}, nil},
		{fault.Event{Kind: "cut", Node: "a", Peer: "b"}, nil},
		{fault.Event{Kind: "heal"}, []int{1, 2, 3}},
		{fault.Event{Kind: "heal-link", Node: "a", Peer: "b"}, []int{3}},
		{fault.Event{Kind: "link", Node: "a", Peer: "b", Link: l}, nil},
		{fault.Event{Kind: "link-reset", Node: "a", Peer: "b"}, []int{4}},
		{fault.Event{Kind: "crash", Node: "a"}, []int{6}},
		{fault.Event{Kind: "restart", Node: "a"}, []int{5}},
		{fault.Event{Kind: "pause", Node: "a"}, nil},
		{fault.Event{Kind: "resume", Node: "a"}, []int{6}},
		{fault.Event{Kind: "clock-jump", Node: "a", N: 5}, nil},
		{fault.Event{Kind: "clock-drift", Node: "a", N: 5}, nil},
		{fault.Event{Kind: "sync-fail", Node: "a", N: 0}, []int{7}},
		{fault.Event{Kind: "disk-capacity", Node: "a", N: 0}, []int{8}},
		{fault.Event{Kind: "corrupt", Node: "a", Path: "/f", Len: 1}, nil},
	}
	var kinds []fault.Kind
	for _, c := range cases {
		kinds = append(kinds, c.e.Kind)
	}
	if !reflect.DeepEqual(kinds, fault.Kinds()) {
		t.Fatalf("cases cover %v, want %v", kinds, fault.Kinds())
	}
	setup, err := fault.Schedule{Events: active}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range setup.Events {
		if e.Undoes != nil {
			t.Fatalf("setup event %d ends %v", i+1, e.Undoes)
		}
	}
	for _, c := range cases {
		events := append(append([]fault.Event(nil), active...), c.e)
		ns, err := fault.Schedule{Events: events}.Normalize()
		if err != nil {
			t.Fatalf("%s: %v", c.e.Kind, err)
		}
		if got := ns.Events[len(active)].Undoes; !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: Undoes %v, want %v", c.e.Kind, got, c.want)
		}
	}
}

// FLT-005, FLT-013 step 1
func TestScheduleValidate(t *testing.T) {
	cases := []struct {
		s    fault.Schedule
		want string
	}{
		{fault.Schedule{Version: 2}, "fault: schedule: unsupported version 2 (supported: 1)"},
		{fault.Schedule{End: kernel.Time(-time.Second)}, "fault: schedule: end must be >= 0 (got -1s)"},
		{fault.Schedule{Recovery: -1}, "fault: schedule: recovery must be >= 0 (got -1ns)"},
		{fault.Schedule{Events: []fault.Event{{Kind: "heal"}, {Kind: "crash"}}}, "fault: schedule: events[1]: crash: node is required"},
		{fault.Schedule{Version: -1}, "fault: schedule: unsupported version -1 (supported: 1)"},
		// Several checks fail: the first in FLT-005 order wins, and the first bad event in slice order.
		{fault.Schedule{Version: 2, End: -1, Recovery: -1, Events: []fault.Event{{}}}, "fault: schedule: unsupported version 2 (supported: 1)"},
		{fault.Schedule{End: kernel.Time(-time.Second), Recovery: -1, Events: []fault.Event{{}}}, "fault: schedule: end must be >= 0 (got -1s)"},
		{fault.Schedule{Recovery: -1, Events: []fault.Event{{}}}, "fault: schedule: recovery must be >= 0 (got -1ns)"},
		{fault.Schedule{Events: []fault.Event{{Kind: "heal"}, {}, {Kind: "x"}}}, "fault: schedule: events[1]: missing kind"},
	}
	for _, c := range cases {
		err := c.s.Validate()
		if err == nil || err.Error() != c.want {
			t.Errorf("Validate() = %v, want %q", err, c.want)
		}
		if _, err := c.s.Normalize(); err == nil || err.Error() != c.want {
			t.Errorf("Normalize() = %v, want %q", err, c.want)
		}
	}
	ok := fault.Schedule{Version: 1, End: 5, Recovery: 3, Events: []fault.Event{{Kind: "crash", Role: "leader"}}}
	if err := ok.Validate(); err != nil {
		t.Errorf("Validate() of a schedule with a role = %v", err)
	}
	// FLT-005 does not relate End, Recovery and At, does not require At order, and does not
	// cross-check ID or Undoes; Normalize ignores both (FLT-013).
	loose := fault.Schedule{End: 1, Recovery: 5, Events: []fault.Event{
		{At: 9, Kind: "crash", Node: "n1", ID: 3, Undoes: []int{7, 7}},
		{At: 2, Kind: "restart", Node: "n1", ID: 3, Undoes: []int{1}},
	}}
	if err := loose.Validate(); err != nil {
		t.Errorf("Validate() of %#v = %v", loose, err)
	}
	ns, err := loose.Normalize()
	want := fault.Schedule{Version: 1, End: 1, Recovery: 5, Events: []fault.Event{
		{At: 2, Kind: "restart", Node: "n1", ID: 1},
		{At: 9, Kind: "crash", Node: "n1", ID: 2},
	}}
	if err != nil || !reflect.DeepEqual(ns, want) {
		t.Errorf("Normalize() of %#v = %#v, %v", loose, ns, err)
	}
}

// FLT-006, FLT-013
func TestNormalize(t *testing.T) {
	s := fault.Schedule{End: 9, Recovery: 7, Events: []fault.Event{
		{At: 2, Kind: "heal", ID: 77, Undoes: []int{5}},
		{At: 1, Kind: "crash", Node: "n1"},
		{At: 1, Kind: "partition", Groups: [][]string{{"n1"}, {"n2"}}},
		{At: 0, Kind: "restart", Node: "n1"},
	}}
	ns, err := s.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	want := fault.Schedule{Version: 1, End: 9, Recovery: 7, Events: []fault.Event{
		{At: 0, Kind: "restart", Node: "n1", ID: 1},
		{At: 1, Kind: "crash", Node: "n1", ID: 2},
		{At: 1, Kind: "partition", Groups: [][]string{{"n1"}, {"n2"}}, ID: 3},
		{At: 2, Kind: "heal", ID: 4, Undoes: []int{3}},
	}}
	if !reflect.DeepEqual(ns, want) {
		t.Fatalf("Normalize() = %+v\nwant %+v", ns, want)
	}
	again, err := ns.Normalize()
	if err != nil || !reflect.DeepEqual(again, ns) {
		t.Fatalf("Normalize is not idempotent: %+v, %v", again, err)
	}
	ns.Events[2].Groups[0][0] = "zz"
	if s.Events[2].Groups[0][0] != "n1" {
		t.Fatalf("Normalize did not deep-copy Groups")
	}
	if s.Events[0].ID != 77 || s.Events[0].At != 2 {
		t.Fatalf("Normalize changed its input")
	}
	_, err = fault.Schedule{Events: []fault.Event{{Kind: "heal"}, {Kind: "crash", Role: "leader"}}}.Normalize()
	if err == nil || err.Error() != `fault: schedule: events[1]: crash: role "leader" is not allowed in a concrete schedule` {
		t.Fatalf("Normalize with a role: %v", err)
	}
	if _, err := (fault.Schedule{Version: 3}).Normalize(); err == nil || err.Error() != "fault: schedule: unsupported version 3 (supported: 1)" {
		t.Fatalf("Normalize of an invalid schedule: %v", err)
	}
	// Validate's error comes before a role error, and the role error names the first event with
	// a role in input order, not in At order.
	for _, c := range []struct {
		s    fault.Schedule
		want string
	}{
		{fault.Schedule{Events: []fault.Event{{Kind: "crash", Role: "r"}, {Kind: "crash"}}}, "fault: schedule: events[1]: crash: node is required"},
		{fault.Schedule{Events: []fault.Event{{At: 5, Kind: "heal"}, {At: 1, Kind: "pause", Role: "x"}, {At: 0, Kind: "crash", Role: "y"}}}, `fault: schedule: events[1]: pause: role "x" is not allowed in a concrete schedule`},
	} {
		if _, err := c.s.Normalize(); err == nil || err.Error() != c.want {
			t.Errorf("Normalize() = %v, want %q", err, c.want)
		}
	}
	// The copy is deep for Link too.
	l := &simnet.Link{DropPPM: 5}
	ln, err := fault.Schedule{Events: []fault.Event{{Kind: "link", Node: "n1", Peer: "n2", Link: l}}}.Normalize()
	if err != nil || ln.Events[0].Link == nil || *ln.Events[0].Link != *l {
		t.Fatalf("Normalize of a link event = %#v, %v", ln, err)
	}
	ln.Events[0].Link.DropPPM = 7
	if l.DropPPM != 5 {
		t.Fatalf("Normalize did not deep-copy Link")
	}
	empty, err := fault.Schedule{}.Normalize()
	if err != nil || empty.Version != 1 || empty.Events != nil {
		t.Fatalf("Normalize(Schedule{}) = %+v, %v", empty, err)
	}
}

// FLT-006. Go sorts up to 12 elements by insertion sort, which is stable anyway, so this schedule
// has 40 events whose equal At values are interleaved in input order.
func TestNormalizeStable(t *testing.T) {
	ats := []kernel.Time{5, 0, 5, 3, 0, 5, 1, 0, 3, 5, 0, 1, 5, 0, 3, 3, 0, 5, 1, 1, 0, 5, 3, 0, 1, 5, 0, 0, 3, 5, 1, 0, 0, 5, 5, 3, 1, 0, 1, 5}
	var events []fault.Event
	for i, a := range ats {
		events = append(events, fault.Event{At: a, Kind: "crash", Node: "n" + strconv.Itoa(i)})
	}
	ns, err := fault.Schedule{Events: events}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	var got, want []string
	for _, e := range ns.Events {
		got = append(got, e.Node)
	}
	for _, a := range []kernel.Time{0, 1, 3, 5} {
		for i, b := range ats {
			if b == a {
				want = append(want, "n"+strconv.Itoa(i))
			}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Normalize did not keep the input order of equal At values:\ngot  %v\nwant %v", got, want)
	}
}
