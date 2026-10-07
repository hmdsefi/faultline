package fault_test

import (
	"slices"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// FLT-001
func TestKinds(t *testing.T) {
	want := []fault.Kind{"partition", "isolate", "cut", "heal", "heal-link", "link", "link-reset", "crash",
		"restart", "pause", "resume", "clock-jump", "clock-drift", "sync-fail", "disk-capacity", "corrupt"}
	if got := fault.Kinds(); !slices.Equal(got, want) {
		t.Fatalf("Kinds() = %v", got)
	}
	// A new slice per call: a caller's edit does not change the next result.
	kinds := fault.Kinds()
	kinds[0] = "x"
	if got := fault.Kinds()[0]; got != fault.KindPartition {
		t.Fatalf("Kinds()[0] = %q after a caller edited an earlier result", got)
	}
	if fault.ScheduleVersion != 1 || fault.MaxRuleDuration != 10000*time.Hour {
		t.Fatalf("constants")
	}
}

// AT-FLT-05, FLT-003, FLT-004
func TestEventValidate(t *testing.T) {
	link := &simnet.Link{Latency: time.Millisecond}
	cases := []struct {
		e    fault.Event
		want string
	}{
		// AT-FLT-05
		{fault.Event{}, "fault: missing kind"},
		{fault.Event{Kind: "heal", Node: "n1"}, "fault: heal: node is not allowed"},
		{fault.Event{Kind: "partition", Groups: [][]string{{"n1"}}}, "fault: partition: at least 2 groups are required (got 1)"},
		{fault.Event{Kind: "partition", Groups: [][]string{{"n1"}, {}}}, "fault: partition: groups[1] is empty"},
		{fault.Event{Kind: "link", Node: "n1", Peer: "n2"}, "fault: link: link is required"},
		{fault.Event{Kind: "clock-drift", Node: "n1", N: -500_001}, "fault: clock-drift: n must be in [-500000, 1000000] (got -500001)"},
		{fault.Event{Kind: "corrupt", Node: "n1", Path: "/f", Len: 0}, "fault: corrupt: len must be > 0 (got 0)"},
		{fault.Event{Kind: "isolate", Role: "leader", Undoes: []int{0}}, "fault: isolate: undoes[0] must be >= 1 (got 0)"},
		{fault.Event{Kind: "heal", Role: "x"}, "fault: heal: role is not allowed"},
		{fault.Event{Kind: "crash", Node: "\xff"}, "fault: crash: node is not valid UTF-8"},
		// further FLT-003 / FLT-004 rows
		{fault.Event{Kind: "explode"}, `fault: unknown kind "explode"`},
		{fault.Event{Kind: "crash", Node: "n1", At: kernel.Time(-time.Second)}, "fault: crash: at must be >= 0 (got -1s)"},
		{fault.Event{Kind: "crash", Node: "n1", Role: "leader"}, "fault: crash: node and role are mutually exclusive"},
		{fault.Event{Kind: "crash", Node: "n1", Peer: "n2"}, "fault: crash: peer is not allowed"},
		{fault.Event{Kind: "crash", Node: "n1", Link: link}, "fault: crash: link is not allowed"},
		{fault.Event{Kind: "heal", N: 3, Path: "/x"}, "fault: heal: n is not allowed"},
		{fault.Event{Kind: "crash"}, "fault: crash: node is required"},
		{fault.Event{Kind: "crash", Role: "\xff"}, "fault: crash: role is not valid UTF-8"},
		{fault.Event{Kind: "cut", Node: "n1"}, "fault: cut: peer is required"},
		{fault.Event{Kind: "cut", Node: "n1", Peer: "\xfe"}, "fault: cut: peer is not valid UTF-8"},
		{fault.Event{Kind: "cut", Node: "n1", Peer: "n1"}, `fault: cut: node and peer must differ ("n1")`},
		{fault.Event{Kind: "partition", Groups: [][]string{{"n1"}, {"n2", ""}}}, "fault: partition: groups[1][1]: empty node name"},
		{fault.Event{Kind: "partition", Groups: [][]string{{"\xff"}, {"n2"}}}, "fault: partition: groups[0][0] is not valid UTF-8"},
		{fault.Event{Kind: "partition", Groups: [][]string{{"n1"}, {"n1", "n2"}}}, `fault: partition: node "n1" appears more than once`},
		{fault.Event{Kind: "link", Node: "n1", Peer: "n2", Link: &simnet.Link{Latency: -1}},
			"fault: link: invalid link: Latency -1ns out of range [0s, 24h0m0s]"},
		{fault.Event{Kind: "clock-jump", Node: "n1"}, "fault: clock-jump: n must not be 0"},
		{fault.Event{Kind: "clock-jump", Node: "n1", N: int64(fault.MaxRuleDuration) + 1},
			"fault: clock-jump: n must be in [-36000000000000000, 36000000000000000] (got 36000000000000001)"},
		{fault.Event{Kind: "clock-drift", Node: "n1", N: 1_000_001}, "fault: clock-drift: n must be in [-500000, 1000000] (got 1000001)"},
		{fault.Event{Kind: "sync-fail", Node: "n1", N: -1}, "fault: sync-fail: n must be in [0, 2147483647] (got -1)"},
		{fault.Event{Kind: "sync-fail", Node: "n1", N: 1 << 31}, "fault: sync-fail: n must be in [0, 2147483647] (got 2147483648)"},
		{fault.Event{Kind: "disk-capacity", Node: "n1", N: -1}, "fault: disk-capacity: n must be >= 0 (got -1)"},
		{fault.Event{Kind: "corrupt", Node: "n1", Len: 1}, "fault: corrupt: path is required"},
		{fault.Event{Kind: "corrupt", Node: "n1", Path: "\xff", Len: 1}, "fault: corrupt: path is not valid UTF-8"},
		{fault.Event{Kind: "corrupt", Node: "n1", Path: "/f", Off: -1, Len: 1}, "fault: corrupt: off must be >= 0 (got -1)"},
		{fault.Event{Kind: "heal", ID: -1}, "fault: heal: id must be >= 0 (got -1)"},
		{fault.Event{Kind: "heal", Undoes: []int{1, 2, -3}}, "fault: heal: undoes[2] must be >= 1 (got -3)"},
		// range edges
		{fault.Event{Kind: "crash", Node: "n1", At: -1}, "fault: crash: at must be >= 0 (got -1ns)"},
		{fault.Event{Kind: "clock-jump", Node: "n1", N: -int64(fault.MaxRuleDuration) - 1},
			"fault: clock-jump: n must be in [-36000000000000000, 36000000000000000] (got -36000000000000001)"},
		// FLT-003 order: two steps fail and the earlier one wins.
		{fault.Event{Kind: "explode", At: kernel.Time(-time.Second)}, `fault: unknown kind "explode"`},
		{fault.Event{Kind: "heal", Role: "x", At: kernel.Time(-time.Second)}, "fault: heal: at must be >= 0 (got -1s)"},
		{fault.Event{Kind: "heal", Node: "n1", Role: "x"}, "fault: heal: role is not allowed"},
		{fault.Event{Kind: "crash", Node: "n1", Role: "r", Peer: "n2"}, "fault: crash: node and role are mutually exclusive"},
		{fault.Event{Kind: "crash", Peer: "n2"}, "fault: crash: peer is not allowed"},
		{fault.Event{Kind: "crash", ID: -1}, "fault: crash: node is required"},
		{fault.Event{Kind: "heal", ID: -1, Undoes: []int{0}}, "fault: heal: id must be >= 0 (got -1)"},
		// FLT-004 order across fields (node, peer, groups, link, n, path, off, len) and within a field.
		{fault.Event{Kind: "cut", Node: "\xff"}, "fault: cut: node is not valid UTF-8"},
		{fault.Event{Kind: "link", Node: "\xff", Peer: "n2"}, "fault: link: node is not valid UTF-8"},
		{fault.Event{Kind: "link", Node: "n1", Peer: "n1"}, `fault: link: node and peer must differ ("n1")`},
		{fault.Event{Kind: "clock-jump", Node: "\xff"}, "fault: clock-jump: node is not valid UTF-8"},
		{fault.Event{Kind: "corrupt", Node: "\xff", Off: -1}, "fault: corrupt: node is not valid UTF-8"},
		{fault.Event{Kind: "corrupt", Node: "n1", Len: 0}, "fault: corrupt: path is required"},
		{fault.Event{Kind: "corrupt", Node: "n1", Off: -1, Len: 1}, "fault: corrupt: path is required"},
		{fault.Event{Kind: "corrupt", Node: "n1", Path: "/f", Off: -1}, "fault: corrupt: off must be >= 0 (got -1)"},
		{fault.Event{Kind: "partition", Groups: [][]string{{}}}, "fault: partition: at least 2 groups are required (got 1)"},
		{fault.Event{Kind: "partition", Groups: [][]string{{""}, {}}}, "fault: partition: groups[1] is empty"},
		{fault.Event{Kind: "partition", Groups: [][]string{{"\xff"}, {""}}}, "fault: partition: groups[1][0]: empty node name"},
		{fault.Event{Kind: "partition", Groups: [][]string{{"\xff"}, {"\xff"}}}, "fault: partition: groups[0][0] is not valid UTF-8"},
		// FLT-003 step 9 and FLT-004's groups checks report the first bad element.
		{fault.Event{Kind: "heal", Undoes: []int{0, -3}}, "fault: heal: undoes[0] must be >= 1 (got 0)"},
		{fault.Event{Kind: "partition", Groups: [][]string{{"n1"}, {}, {}}}, "fault: partition: groups[1] is empty"},
		{fault.Event{Kind: "partition", Groups: [][]string{{"n1", "", ""}, {""}}}, "fault: partition: groups[0][1]: empty node name"},
		{fault.Event{Kind: "partition", Groups: [][]string{{"n1", "\xff", "\xfe"}, {"\xfd"}}},
			"fault: partition: groups[0][1] is not valid UTF-8"},
		{fault.Event{Kind: "partition", Groups: [][]string{{"a", "b"}, {"b", "a"}}}, `fault: partition: node "b" appears more than once`},
	}
	for _, c := range cases {
		err := c.e.Validate()
		if err == nil || err.Error() != c.want {
			t.Errorf("%#v.Validate() = %v, want %q", c.e, err, c.want)
		}
	}
	valid := []fault.Event{
		{Kind: "clock-drift", Node: "n1", N: 0},
		{Kind: "sync-fail", Node: "n1", N: 0},
		{Kind: "disk-capacity", Node: "n1", N: 0},
		{Kind: "corrupt", Node: "n1", Path: "/f", Off: 0, Len: 1},
		{Kind: "crash", Role: "leader"},
		{Kind: "link", Node: "n1", Peer: "n2", Link: &simnet.Link{}},
		{Kind: "heal", ID: 3, Undoes: []int{1, 2}},
		{Kind: "clock-jump", Node: "n1", N: -int64(fault.MaxRuleDuration)},
		// range edges, and FLT-003 step 6 counts an empty Groups as zero
		{Kind: "clock-jump", Node: "n1", N: int64(fault.MaxRuleDuration)},
		{Kind: "clock-drift", Node: "n1", N: -500_000},
		{Kind: "clock-drift", Node: "n1", N: 1_000_000},
		{Kind: "sync-fail", Node: "n1", N: 1<<31 - 1},
		{Kind: "heal", Groups: [][]string{}},
	}
	for _, e := range valid {
		if err := e.Validate(); err != nil {
			t.Errorf("%#v.Validate() = %v, want nil", e, err)
		}
	}
}

// FLT-001, FLT-002: every kind is valid with each field of its set, and rejects every other field.
func TestFieldSets(t *testing.T) {
	link := &simnet.Link{}
	sets := []struct {
		e      fault.Event // valid, with every field of the set non-zero
		fields []string
	}{
		{fault.Event{Kind: "partition", Groups: [][]string{{"n1"}, {"n2"}}}, []string{"groups"}},
		{fault.Event{Kind: "isolate", Node: "n1"}, []string{"node"}},
		{fault.Event{Kind: "cut", Node: "n1", Peer: "n2"}, []string{"node", "peer"}},
		{fault.Event{Kind: "heal"}, nil},
		{fault.Event{Kind: "heal-link", Node: "n1", Peer: "n2"}, []string{"node", "peer"}},
		{fault.Event{Kind: "link", Node: "n1", Peer: "n2", Link: link}, []string{"node", "peer", "link"}},
		{fault.Event{Kind: "link-reset", Node: "n1", Peer: "n2"}, []string{"node", "peer"}},
		{fault.Event{Kind: "crash", Node: "n1"}, []string{"node"}},
		{fault.Event{Kind: "restart", Node: "n1"}, []string{"node"}},
		{fault.Event{Kind: "pause", Node: "n1"}, []string{"node"}},
		{fault.Event{Kind: "resume", Node: "n1"}, []string{"node"}},
		{fault.Event{Kind: "clock-jump", Node: "n1", N: 1}, []string{"node", "n"}},
		{fault.Event{Kind: "clock-drift", Node: "n1", N: 1}, []string{"node", "n"}},
		{fault.Event{Kind: "sync-fail", Node: "n1", N: 1}, []string{"node", "n"}},
		{fault.Event{Kind: "disk-capacity", Node: "n1", N: 1}, []string{"node", "n"}},
		{fault.Event{Kind: "corrupt", Node: "n1", Path: "/f", Off: 1, Len: 1}, []string{"node", "path", "off", "len"}},
	}
	others := []struct {
		field string
		set   func(*fault.Event)
	}{
		{"node", func(e *fault.Event) { e.Node = "x" }},
		{"peer", func(e *fault.Event) { e.Peer = "x" }},
		{"groups", func(e *fault.Event) { e.Groups = [][]string{{"x"}} }},
		{"link", func(e *fault.Event) { e.Link = link }},
		{"n", func(e *fault.Event) { e.N = 1 }},
		{"path", func(e *fault.Event) { e.Path = "/x" }},
		{"off", func(e *fault.Event) { e.Off = 1 }},
		{"len", func(e *fault.Event) { e.Len = 1 }},
		// FLT-003 step 6 rejects any non-zero value, negative ones too.
		{"n", func(e *fault.Event) { e.N = -1 }},
		{"off", func(e *fault.Event) { e.Off = -1 }},
		{"len", func(e *fault.Event) { e.Len = -1 }},
	}
	for i, c := range sets {
		k := string(c.e.Kind)
		if c.e.Kind != fault.Kinds()[i] {
			t.Fatalf("sets[%d] is %s, want %s", i, k, fault.Kinds()[i])
		}
		if err := c.e.Validate(); err != nil {
			t.Errorf("%#v.Validate() = %v, want nil", c.e, err)
		}
		for _, o := range others {
			if slices.Contains(c.fields, o.field) {
				continue
			}
			e := c.e
			o.set(&e)
			if err, want := e.Validate(), "fault: "+k+": "+o.field+" is not allowed"; err == nil || err.Error() != want {
				t.Errorf("%#v.Validate() = %v, want %q", e, err, want)
			}
		}
		// Role may replace Node only in kinds whose set contains node.
		r := c.e
		r.Node, r.Role = "", "leader"
		want := ""
		if !slices.Contains(c.fields, "node") {
			want = "fault: " + k + ": role is not allowed"
		}
		if err := r.Validate(); err == nil && want != "" || err != nil && err.Error() != want {
			t.Errorf("%#v.Validate() = %v, want %q", r, err, want)
		}
	}
}

// FLT-011
func TestDurable(t *testing.T) {
	durable := map[fault.Kind]bool{"partition": true, "isolate": true, "cut": true, "link": true, "crash": true, "pause": true}
	for _, k := range fault.Kinds() {
		if got := (fault.Event{Kind: k}).Durable(); got != durable[k] {
			t.Errorf("%s.Durable() = %v", k, got)
		}
	}
	for _, k := range []fault.Kind{"sync-fail", "disk-capacity"} {
		if !(fault.Event{Kind: k, N: 1}).Durable() || (fault.Event{Kind: k, N: 0}).Durable() {
			t.Errorf("%s: Durable must follow N > 0", k)
		}
	}
}

// FLT-071
func TestEventString(t *testing.T) {
	link := &simnet.Link{Latency: 50 * time.Millisecond, Jitter: 10 * time.Millisecond, TailPPM: 10000,
		Tail: time.Second, DropPPM: 100000, DupPPM: 5000, FIFO: true}
	cases := []struct {
		e    fault.Event
		want string
	}{
		{fault.Event{Kind: "partition", Groups: [][]string{{"n1", "n2"}, {"n3", "n4", "n5"}}}, "partition n1,n2|n3,n4,n5"},
		{fault.Event{Kind: "crash", Node: "n3"}, "crash n3"},
		{fault.Event{Kind: "isolate", Node: "n5"}, "isolate n5"},
		{fault.Event{Kind: "cut", Node: "n1", Peer: "n3"}, "cut n1 -> n3"},
		{fault.Event{Kind: "link-reset", Node: "n1", Peer: "n2"}, "link-reset n1 -> n2"},
		{fault.Event{Kind: "heal"}, "heal"},
		{fault.Event{Kind: "link", Node: "n1", Peer: "n2", Link: link},
			"link n1 -> n2: latency=50ms jitter=10ms tail=10000ppm/1s drop=100000ppm dup=5000ppm fifo=true"},
		{fault.Event{Kind: "link", Node: "n1", Peer: "n2"}, "link n1 -> n2: <nil>"},
		{fault.Event{Kind: "clock-jump", Node: "n5", N: -250000000}, "clock-jump n5 -250ms"},
		{fault.Event{Kind: "clock-drift", Node: "n2", N: 150}, "clock-drift n2 150ppm"},
		{fault.Event{Kind: "sync-fail", Node: "n1", N: 2}, "sync-fail n1 2"},
		{fault.Event{Kind: "disk-capacity", Node: "n2", N: 1048576}, "disk-capacity n2 1048576"},
		{fault.Event{Kind: "corrupt", Node: "n1", Path: "/wal/0000000000000001.wal", Off: 4096, Len: 16},
			"corrupt n1 /wal/0000000000000001.wal off=4096 len=16"},
		{fault.Event{Kind: "crash", Role: "leader"}, "crash @leader"},
		{fault.Event{Kind: "restart", Node: "n1"}, "restart n1"},
		{fault.Event{Kind: "pause", Node: "n1"}, "pause n1"},
		{fault.Event{Kind: "resume", Node: "n1"}, "resume n1"},
		{fault.Event{Kind: "heal-link", Node: "n1", Peer: "n2"}, "heal-link n1 -> n2"},
		// With Role set, <node> is @<role> in every format that has a node.
		{fault.Event{Kind: "cut", Role: "leader", Peer: "n2"}, "cut @leader -> n2"},
		{fault.Event{Kind: "link", Role: "leader", Peer: "n2"}, "link @leader -> n2: <nil>"},
		{fault.Event{Kind: "link", Role: "leader", Peer: "n2", Link: &simnet.Link{}},
			"link @leader -> n2: latency=0s jitter=0s tail=0ppm/0s drop=0ppm dup=0ppm fifo=false"},
		{fault.Event{Kind: "clock-jump", Role: "leader", N: int64(time.Second)}, "clock-jump @leader 1s"},
		{fault.Event{Kind: "clock-drift", Role: "leader", N: 150}, "clock-drift @leader 150ppm"},
		{fault.Event{Kind: "sync-fail", Role: "leader", N: 2}, "sync-fail @leader 2"},
		{fault.Event{Kind: "corrupt", Role: "leader", Path: "/f", Len: 1}, "corrupt @leader /f off=0 len=1"},
	}
	for _, c := range cases {
		if got := c.e.String(); got != c.want {
			t.Errorf("String() = %q, want %q", got, c.want)
		}
	}
}
