package oracle

import (
	"hash/fnv"
	"strings"
	"testing"
	"time"

	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"

	"github.com/hmdsefi/faultline/kernel"

	"github.com/hmdsefi/faultline/harness/etcdraft/internal/kv"
)

func at(ms int) kernel.Time { return kernel.Time(time.Duration(ms) * time.Millisecond) }

func bootSnap() *raftpb.Snapshot {
	return &raftpb.Snapshot{
		Metadata: &raftpb.SnapshotMetadata{Index: new(uint64(1)), Term: new(uint64(1)), ConfState: &raftpb.ConfState{Voters: []uint64{1, 2, 3}}},
		Data:     kv.New().Encode(),
	}
}

// put is client's operation 1: a put of value under key.
func put(index, term uint64, client uint32, key, value string) *raftpb.Entry {
	return &raftpb.Entry{Index: new(index), Term: new(term), Type: raftpb.EntryNormal.Enum(),
		Data: kv.EncodeCommand(kv.Command{Client: client, ID: 1, Op: kv.OpPut, Key: key, Value: value})}
}

func empty(index, term uint64) *raftpb.Entry {
	return &raftpb.Entry{Index: new(index), Term: new(term), Type: raftpb.EntryNormal.Enum()}
}

func hash(b []byte) uint64 {
	h := fnv.New64a()
	h.Write(b)
	return h.Sum64()
}

func wantErr(t *testing.T, o *Oracle, check, substr string) {
	t.Helper()
	err := o.Err(check)
	if err == nil || !strings.Contains(err.Error(), substr) {
		t.Fatalf("Err(%q) = %v, want it to contain %q", check, err, substr)
	}
}

func wantNoErr(t *testing.T, o *Oracle, checks ...string) {
	t.Helper()
	for _, c := range checks {
		if err := o.Err(c); err != nil {
			t.Fatalf("Err(%q) = %v, want nil", c, err)
		}
	}
}

// ETC-110
func TestElectionSafety(t *testing.T) {
	o := New(bootSnap())
	if !o.Role(at(1), "n1", 1, 1, raft.StateFollower, 0) {
		t.Fatal("first observation must report a change")
	}
	if o.Role(at(2), "n1", 1, 1, raft.StateFollower, 0) {
		t.Fatal("unchanged role reported as a change")
	}
	o.Role(at(3), "n1", 1, 2, raft.StateLeader, 1)
	o.Role(at(4), "n1", 1, 2, raft.StateLeader, 1)
	o.Role(at(5), "n2", 2, 3, raft.StateLeader, 2)
	wantNoErr(t, o, ElectionSafety)
	if o.Terms() != 2 {
		t.Fatalf("Terms() = %d, want 2", o.Terms())
	}
	o.Role(at(6), "n3", 3, 2, raft.StateLeader, 3)
	wantErr(t, o, ElectionSafety, "term 2 has two leaders: n1 (since 0.003000000s) and n3 (at 0.006000000s)")
}

// ETC-111
func TestStateMachineSafety(t *testing.T) {
	o := New(bootSnap())
	if h, ok := o.RefHash(1); !ok || h != kv.New().Hash() {
		t.Fatalf("RefHash(1) = %x, %v", h, ok)
	}
	o.Apply(at(1), "n1", 1, empty(2, 2))
	o.Apply(at(1), "n1", 1, put(3, 2, 1, "k1", "c1-1"))
	o.Apply(at(2), "n2", 2, empty(2, 2))
	o.Apply(at(2), "n2", 2, put(3, 2, 1, "k1", "c1-1"))
	o.Apply(at(3), "n1", 1, put(4, 2, 1, "k1", "c1-1")) // duplicate of client 1 op 1
	wantNoErr(t, o, StateMachineSafety)
	if o.LastIndex() != 4 {
		t.Fatalf("LastIndex = %d, want 4", o.LastIndex())
	}
	if i, ok := o.FirstApplied(1, 1); !ok || i != 3 {
		t.Fatalf("FirstApplied(1,1) = %d, %v; want 3, true", i, ok)
	}
	ref := kv.New()
	ref.Apply(kv.Command{Client: 1, ID: 1, Op: kv.OpPut, Key: "k1", Value: "c1-1"})
	if h, _ := o.RefHash(3); h != ref.Hash() {
		t.Fatal("RefHash(3) is not the hash after the put")
	}
	if h3, _ := o.RefHash(3); func() bool { h4, _ := o.RefHash(4); return h4 != h3 }() {
		t.Fatal("a duplicate must not change the reference state")
	}
	o.Apply(at(4), "n3", 3, put(3, 3, 2, "k2", "c2-1"))
	wantErr(t, o, StateMachineSafety, "index 3: n1 applied term=2 type=EntryNormal")
	o2 := New(bootSnap())
	o2.Apply(at(5), "n2", 2, empty(3, 2))
	wantErr(t, o2, StateMachineSafety, "n2 applied index 3 but the canonical log ends at 1")
}

// ETC-111: term, type and data hash must each match the first applier's; one field differs at a time.
func TestStateMachineSafetyFields(t *testing.T) {
	canon := put(2, 2, 1, "k", "v")
	for _, c := range []struct {
		name string
		e    *raftpb.Entry
		want string
	}{
		{"data", put(2, 2, 1, "k", "w"), "but n2 applied term=2 type=EntryNormal data="},
		{"term", put(2, 3, 1, "k", "v"), "but n2 applied term=3 type=EntryNormal data="},
		{"type", &raftpb.Entry{Index: new(uint64(2)), Term: new(uint64(2)), Type: raftpb.EntryConfChangeV2.Enum(), Data: canon.GetData()},
			"but n2 applied term=2 type=EntryConfChangeV2 data="},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := New(bootSnap())
			o.Apply(at(1), "n1", 1, canon)
			o.Apply(at(2), "n2", 2, c.e)
			wantErr(t, o, StateMachineSafety, c.want)
		})
	}
}

// ETC-111 snapshot checks
func TestSnapshotState(t *testing.T) {
	o := New(bootSnap())
	boot := bootSnap()
	o.SnapshotState(at(0), "n1", "recover", 1, hash(boot.Data), boot.Metadata.ConfState)
	wantNoErr(t, o, StateMachineSafety)
	// raft's ConfStates set AutoLeave explicitly; the bootstrap one does not. Both are equal.
	o.SnapshotState(at(0), "n2", "recover", 1, hash(boot.Data), raftpb.EnsureConfState(&raftpb.ConfState{Voters: []uint64{1, 2, 3}}))
	wantNoErr(t, o, StateMachineSafety)
	o.Apply(at(1), "n1", 1, put(2, 2, 1, "k", "v"))
	o.SnapshotState(at(2), "n1", "create", 2, hash(boot.Data), boot.Metadata.ConfState)
	wantErr(t, o, StateMachineSafety, "create snapshot at index 2 on n1 has state hash")
	o2 := New(bootSnap())
	o2.SnapshotState(at(2), "n1", "install", 7, 1, boot.Metadata.ConfState)
	wantErr(t, o2, StateMachineSafety, "reference has no state at index 7")
	o3 := New(bootSnap())
	o3.SnapshotState(at(2), "n1", "create", 1, hash(boot.Data), &raftpb.ConfState{Voters: []uint64{1, 2}})
	wantErr(t, o3, StateMachineSafety, "has configuration Voters:[1 2]")
}

// ETC-115
func TestConfigurationAgreement(t *testing.T) {
	o := New(bootSnap())
	cs5 := &raftpb.ConfState{Voters: []uint64{1, 2, 3}, Learners: []uint64{4}, AutoLeave: new(false)}
	o.ConfApplied(at(1), "n1", 5, cs5)
	o.ConfApplied(at(2), "n2", 5, &raftpb.ConfState{Voters: []uint64{1, 2, 3}, Learners: []uint64{4}, AutoLeave: new(false)})
	wantNoErr(t, o, ConfigAgreement)
	if got := raft.DescribeConfState(o.ConfAt(4)); got != raft.DescribeConfState(bootSnap().Metadata.ConfState) {
		t.Fatalf("ConfAt(4) = %s, want the bootstrap configuration", got)
	}
	if got := raft.DescribeConfState(o.ConfAt(9)); got != raft.DescribeConfState(cs5) {
		t.Fatalf("ConfAt(9) = %s", got)
	}
	if got := raft.DescribeConfState(o.Conf()); got != raft.DescribeConfState(cs5) {
		t.Fatalf("Conf() = %s", got)
	}
	o.ConfApplied(at(3), "n3", 5, &raftpb.ConfState{Voters: []uint64{1, 2, 3, 4}, AutoLeave: new(false)})
	wantErr(t, o, ConfigAgreement, "index 5: n1 applied configuration Voters:[1 2 3] VotersOutgoing:[] Learners:[4] LearnersNext:[] AutoLeave:false but n3 got Voters:[1 2 3 4]")
}

// ETC-112
func TestDurableState(t *testing.T) {
	hs := func(term, vote, commit uint64) *raftpb.HardState {
		return &raftpb.HardState{Term: new(term), Vote: new(vote), Commit: new(commit)}
	}
	cases := []struct {
		name string
		rec  Recovery
		want string // "" = no violation
	}{
		{"ok", Recovery{ValidEnd: 100, HardState: hs(3, 2, 10), SnapIndex: 1}, ""},
		{"newer term", Recovery{ValidEnd: 120, HardState: hs(4, 0, 5), SnapIndex: 1}, ""},
		{"lost bytes", Recovery{ValidEnd: 99, HardState: hs(3, 2, 10), SnapIndex: 1}, "n1 recovered 99 WAL bytes but 100 were persisted"},
		{"fresh", Recovery{Fresh: true, ValidEnd: 100}, "n1 booted with an empty WAL after persisting 100 bytes"},
		{"no hardstate", Recovery{ValidEnd: 100, SnapIndex: 1}, "n1 recovered HardState none older than persisted Term:3 Vote:2 Commit:10"},
		{"other vote", Recovery{ValidEnd: 100, HardState: hs(3, 1, 10), SnapIndex: 1}, "older than persisted"},
		{"lower commit", Recovery{ValidEnd: 100, HardState: hs(3, 2, 9), SnapIndex: 1}, "older than persisted"},
		{"lower snapshot", Recovery{ValidEnd: 100, HardState: hs(3, 2, 10), SnapIndex: 0}, "n1 recovered snapshot index 0 below persisted snapshot index 1"},
		{"lower term", Recovery{ValidEnd: 100, HardState: hs(2, 2, 10), SnapIndex: 1}, "n1 recovered HardState Term:2 Vote:2 Commit:10 older than persisted Term:3 Vote:2 Commit:10"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := New(bootSnap())
			o.Persisted(1, hs(3, 2, 10), 100, 1)
			o.Persisted(1, nil, 100, 0) // a HardState-less persist point keeps D.hs and D.snapIndex
			o.Recovered(at(1), "n1", 1, c.rec)
			if c.want == "" {
				wantNoErr(t, o, DurableState)
				return
			}
			wantErr(t, o, DurableState, c.want)
		})
	}
	o := New(bootSnap())
	o.Persisted(2, hs(2, 1, 4), 50, 1)
	o.Recovered(at(1), "n2", 2, Recovery{ValidEnd: 80, HardState: hs(2, 1, 4), SnapIndex: 1})
	o.Recovered(at(2), "n2", 2, Recovery{ValidEnd: 70, HardState: hs(2, 1, 4), SnapIndex: 1})
	wantErr(t, o, DurableState, "n2 recovered 70 WAL bytes but 80 were persisted") // D.walEnd = ValidEnd after recovery
	// ETC-112 rule 3: a vote cast after an unvoted persist point is not older (the persisted vote is 0).
	o3 := New(bootSnap())
	o3.Persisted(3, hs(3, 0, 10), 100, 1)
	o3.Recovered(at(1), "n3", 3, Recovery{ValidEnd: 100, HardState: hs(3, 1, 10), SnapIndex: 1})
	wantNoErr(t, o3, DurableState)
}

// ETC-113
func TestHardStateMonotonic(t *testing.T) {
	hs := func(term, vote, commit uint64) *raftpb.HardState {
		return &raftpb.HardState{Term: new(term), Vote: new(vote), Commit: new(commit)}
	}
	o := New(bootSnap())
	o.HardState(at(0), "n1", 1, hs(1, 0, 1), true)
	o.HardState(at(1), "n1", 1, hs(2, 1, 1), false)
	o.HardState(at(2), "n1", 1, hs(2, 1, 5), false)
	o.HardState(at(3), "n1", 1, hs(3, 0, 5), false)
	o.HardState(at(4), "n1", 1, hs(1, 0, 0), true) // new incarnation: baseline
	wantNoErr(t, o, HardStateMonotonic)
	o.HardState(at(5), "n1", 1, hs(1, 2, 0), false) // vote set from 0: allowed
	o.HardState(at(6), "n1", 1, hs(1, 3, 0), false)
	wantErr(t, o, HardStateMonotonic, "n1 HardState went from Term:1 Vote:2 Commit:0 to Term:1 Vote:3 Commit:0")
	o2 := New(bootSnap())
	o2.HardState(at(0), "n2", 2, hs(2, 0, 7), true)
	o2.HardState(at(1), "n2", 2, hs(2, 0, 6), false)
	wantErr(t, o2, HardStateMonotonic, "Commit:7 to Term:2 Commit:6")
	// ETC-113: a term that goes back.
	o3 := New(bootSnap())
	o3.HardState(at(0), "n3", 3, hs(3, 1, 5), true)
	o3.HardState(at(1), "n3", 3, hs(2, 1, 5), false)
	wantErr(t, o3, HardStateMonotonic, "n3 HardState went from Term:3 Vote:1 Commit:5 to Term:2 Vote:1 Commit:5")
}

// ETC-114, ETC-116, ETC-117, OnViolation
func TestRecordedViolations(t *testing.T) {
	o := New(bootSnap())
	var calls []string
	o.OnViolation(func(check string, err error) { calls = append(calls, check+"|"+err.Error()) })
	o.Contract(at(1), "n2", "rule 4: MsgApp to 1 sent before the persist point")
	o.Contract(at(2), "n2", "rule 6: second")
	o.LeaderIsVoter(at(3), "n1", 1, 4, true, "Voters:[1 2 3]")
	o.LeaderIsVoter(at(3), "n4", 4, 5, false, "Voters:[1 2 3]")
	o.HarnessError(at(4), "c1", errDecode)
	wantErr(t, o, ReadyContract, "n2 ready contract: rule 4: MsgApp to 1 sent before the persist point")
	wantErr(t, o, LeaderIsVoter, "n4 is leader of term 5 but not a voter in Voters:[1 2 3]")
	wantErr(t, o, Harness, "c1: decode reply from n1: short")
	want := []string{
		ReadyContract + "|n2 ready contract: rule 4: MsgApp to 1 sent before the persist point",
		LeaderIsVoter + "|n4 is leader of term 5 but not a voter in Voters:[1 2 3]",
		Harness + "|c1: decode reply from n1: short",
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("OnViolation calls:\n%s\nwant:\n%s", strings.Join(calls, "\n"), strings.Join(want, "\n"))
	}
}

var errDecode = errorString("decode reply from n1: short")

type errorString string

func (e errorString) Error() string { return string(e) }
