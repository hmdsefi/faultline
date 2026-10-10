package etcdraft

import (
	"fmt"
	"testing"
	"time"

	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"

	"github.com/hmdsefi/faultline"

	"github.com/hmdsefi/faultline/harness/etcdraft/internal/kv"
	"github.com/hmdsefi/faultline/harness/etcdraft/internal/oracle"
	"github.com/hmdsefi/faultline/harness/etcdraft/internal/wal"
)

// craftedRun runs seed 1 (FaultsNone, 15 s) and, in a final check, calls probe with the cluster
// and the leader's incarnation. probe feeds crafted input to one server-side check and returns
// the check and the first violation it expects ("" for none). The run itself passes: the
// invariants are not evaluated again after the final checks.
func craftedRun(t *testing.T, cfg Config, probe func(c *Cluster, inc *incarnation) (check, want string)) {
	t.Helper()
	opts := testOptions(t, &cfg, 15*time.Second, false)
	var check, want, got string
	runSeed(t, 1, opts, func(w *faultline.World) {
		c := Setup(w, cfg)
		w.Plan(Faults(cfg))
		w.Final("test: crafted", func() error {
			id, _ := c.Leader()
			s := c.server(id)
			if s == nil || s.inc == nil {
				return fmt.Errorf("no leader at the end")
			}
			check, want = probe(c, s.inc)
			got = ""
			if err := c.o.Err(check); err != nil {
				got = err.Error()
			}
			return nil
		})
	})
	if got != want {
		t.Fatalf("%s:\n got %q\nwant %q", check, got, want)
	}
}

func ent(i uint64) *raftpb.Entry {
	return &raftpb.Entry{Index: new(i), Term: new(uint64(1)), Type: raftpb.EntryNormal.Enum()}
}

func snapAt(i uint64, cs *raftpb.ConfState) *raftpb.Snapshot {
	return &raftpb.Snapshot{Metadata: &raftpb.SnapshotMetadata{Index: new(i), Term: new(uint64(1)), ConfState: cs}, Data: kv.New().Encode()}
}

func msg(typ raftpb.MessageType, from, to uint64) *raftpb.Message {
	return &raftpb.Message{Type: typ.Enum(), From: new(from), To: new(to), Term: new(uint64(1))}
}

// ETC-114 rules 1 to 7 on the server side, failing on purpose with crafted Readys.
func TestReadyContractRulesDetect(t *testing.T) {
	voters := &raftpb.ConfState{Voters: []uint64{1, 2, 3}}
	rc := func(inc *incarnation, detail string, args ...any) (string, string) {
		return oracle.ReadyContract, inc.n.Name() + " ready contract: " + fmt.Sprintf(detail, args...)
	}
	cases := []struct {
		name  string
		probe func(c *Cluster, inc *incarnation) (string, string)
	}{
		{"rule1 gap", func(_ *Cluster, inc *incarnation) (string, string) {
			l, _ := inc.ms.LastIndex()
			inc.checkReady(raft.Ready{Entries: []*raftpb.Entry{ent(l + 1), ent(l + 3)}})
			return rc(inc, "rule 1: entries not contiguous at index %d after %d", l+3, l+1)
		}},
		{"rule1 above last+1", func(_ *Cluster, inc *incarnation) (string, string) {
			f, _ := inc.ms.FirstIndex()
			l, _ := inc.ms.LastIndex()
			inc.checkReady(raft.Ready{Entries: []*raftpb.Entry{ent(l + 2)}})
			return rc(inc, "rule 1: first entry %d outside [%d, %d]", l+2, f, l+1)
		}},
		{"rule1 below first", func(_ *Cluster, inc *incarnation) (string, string) {
			f, _ := inc.ms.FirstIndex()
			l, _ := inc.ms.LastIndex()
			inc.checkReady(raft.Ready{Entries: []*raftpb.Entry{ent(f - 1)}})
			return rc(inc, "rule 1: first entry %d outside [%d, %d]", f-1, f, l+1)
		}},
		{"rule1 after snapshot", func(_ *Cluster, inc *incarnation) (string, string) {
			s := inc.applied + 5
			inc.checkReady(raft.Ready{Snapshot: snapAt(s, voters), Entries: []*raftpb.Entry{ent(s + 2)}})
			return rc(inc, "rule 1: first entry %d does not follow snapshot %d", s+2, s)
		}},
		{"rule2 no entries", func(_ *Cluster, inc *incarnation) (string, string) {
			l, _ := inc.ms.LastIndex()
			inc.checkReady(raft.Ready{HardState: &raftpb.HardState{Term: new(uint64(9)), Commit: new(l + 1)}})
			return rc(inc, "rule 2: commit %d beyond last index %d", l+1, l)
		}},
		{"rule2 last is the Ready's last entry", func(_ *Cluster, inc *incarnation) (string, string) {
			l, _ := inc.ms.LastIndex()
			inc.checkReady(raft.Ready{Entries: []*raftpb.Entry{ent(l + 1)}, HardState: &raftpb.HardState{Term: new(uint64(9)), Commit: new(l + 2)}})
			return rc(inc, "rule 2: commit %d beyond last index %d", l+2, l+1)
		}},
		{"rule2 truncating Ready", func(_ *Cluster, inc *incarnation) (string, string) {
			l, _ := inc.ms.LastIndex()
			if err := inc.ms.Append([]*raftpb.Entry{ent(l + 1), ent(l + 2)}); err != nil {
				panic(err)
			}
			// entry l+1 replaces l+1 and l+2: the log ends at l+1 after this Ready
			inc.checkReady(raft.Ready{Entries: []*raftpb.Entry{ent(l + 1)}, HardState: &raftpb.HardState{Term: new(uint64(9)), Commit: new(l + 2)}})
			return rc(inc, "rule 2: commit %d beyond last index %d", l+2, l+1)
		}},
		{"rule2 last is the snapshot", func(_ *Cluster, inc *incarnation) (string, string) {
			s := inc.applied + 5
			inc.checkReady(raft.Ready{Snapshot: snapAt(s, voters), HardState: &raftpb.HardState{Term: new(uint64(9)), Commit: new(s + 1)}})
			return rc(inc, "rule 2: commit %d beyond last index %d", s+1, s)
		}},
		{"rule3", func(_ *Cluster, inc *incarnation) (string, string) {
			a := inc.applied
			inc.checkReady(raft.Ready{Snapshot: snapAt(a, voters)})
			return rc(inc, "rule 3: snapshot index %d not above applied index %d", a, a)
		}},
		{"rule3 above applied passes", func(_ *Cluster, inc *incarnation) (string, string) {
			inc.checkReady(raft.Ready{Snapshot: snapAt(inc.applied+1, voters)})
			return oracle.ReadyContract, ""
		}},
		{"rule4 from another server", func(_ *Cluster, inc *incarnation) (string, string) {
			other := inc.s.id%3 + 1
			inc.persisted = true
			inc.sendMessages(raft.Ready{Messages: []*raftpb.Message{msg(raftpb.MsgApp, other, other%3+1)}})
			return rc(inc, "rule 4: MsgApp from %d sent by %d", other, inc.s.id)
		}},
		{"rule4 forwarded proposal passes", func(_ *Cluster, inc *incarnation) (string, string) {
			other := inc.s.id%3 + 1
			inc.persisted = true
			inc.sendMessages(raft.Ready{Messages: []*raftpb.Message{msg(raftpb.MsgProp, other, other%3+1)}})
			return oracle.ReadyContract, ""
		}},
		{"rule4 to itself", func(_ *Cluster, inc *incarnation) (string, string) {
			inc.persisted = true
			inc.sendMessages(raft.Ready{Messages: []*raftpb.Message{msg(raftpb.MsgHeartbeat, inc.s.id, inc.s.id)}})
			return rc(inc, "rule 4: MsgHeartbeat addressed to itself")
		}},
		{"rule4 before the persist point", func(_ *Cluster, inc *incarnation) (string, string) {
			inc.persisted = false
			inc.sendMessages(raft.Ready{Messages: []*raftpb.Message{msg(raftpb.MsgVote, inc.s.id, inc.s.id%3+1)}})
			return rc(inc, "rule 4: MsgVote to %d sent before the persist point", inc.s.id%3+1)
		}},
		{"rule5 index", func(_ *Cluster, inc *incarnation) (string, string) {
			a := inc.applied
			inc.apply(raft.Ready{Snapshot: snapAt(a, voters)})
			return rc(inc, "rule 5: snapshot index %d not above applied index %d", a, a)
		}},
		{"rule5 empty configuration", func(_ *Cluster, inc *incarnation) (string, string) {
			s := inc.applied + 10
			inc.apply(raft.Ready{Snapshot: snapAt(s, &raftpb.ConfState{})})
			return rc(inc, "rule 5: snapshot at index %d has an empty configuration", s)
		}},
		{"rule5 learners only is not empty", func(_ *Cluster, inc *incarnation) (string, string) {
			inc.apply(raft.Ready{Snapshot: snapAt(inc.applied+10, &raftpb.ConfState{Learners: []uint64{2}})})
			return oracle.ReadyContract, ""
		}},
		{"rule6", func(_ *Cluster, inc *incarnation) (string, string) {
			r := inc.raftApplied
			inc.syncedLast = r + 5
			inc.apply(raft.Ready{CommittedEntries: []*raftpb.Entry{ent(r + 2)}})
			return rc(inc, "rule 6: committed entry %d does not follow applied index %d", r+2, r)
		}},
		{"rule7", func(_ *Cluster, inc *incarnation) (string, string) {
			r := inc.raftApplied
			inc.syncedLast = r
			inc.apply(raft.Ready{CommittedEntries: []*raftpb.Entry{ent(r + 1)}})
			return rc(inc, "rule 7: committed entry %d beyond synced index %d", r+1, r)
		}},
		{"rule7 at the synced index passes", func(_ *Cluster, inc *incarnation) (string, string) {
			r := inc.raftApplied
			inc.syncedLast = r + 1
			inc.apply(raft.Ready{CommittedEntries: []*raftpb.Entry{ent(r + 1)}})
			return oracle.ReadyContract, ""
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Faults = FaultsNone
			craftedRun(t, cfg, tc.probe)
		})
	}
}

// removeSelf returns a ConfChangeV2 that removes raft ID id.
func removeSelf(id uint64) *raftpb.ConfChangeV2 {
	return &raftpb.ConfChangeV2{Changes: []*raftpb.ConfChangeSingle{{Type: raftpb.ConfChangeRemoveNode.Enum(), NodeId: new(id)}}}
}

// ETC-116 at both call sites. raft runs with StepDownOnRemoval off, so a removed leader stays
// leader, as a raft that failed to step down would; the harness then checks with it on.
func TestLeaderIsVoterDetect(t *testing.T) {
	t.Run("applyConf", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Faults = FaultsNone
		cfg.StepDownOnRemoval = false
		craftedRun(t, cfg, func(c *Cluster, inc *incarnation) (string, string) {
			c.cfg.StepDownOnRemoval = true
			term := inc.rn.BasicStatus().GetTerm()
			e := &raftpb.Entry{Index: new(inc.raftApplied + 1), Term: new(term), Type: raftpb.EntryConfChangeV2.Enum(), Data: wal.Marshal(removeSelf(inc.s.id))}
			inc.applyConf(e)
			return oracle.LeaderIsVoter, fmt.Sprintf("%s is leader of term %d but not a voter in %s", inc.n.Name(), term, raft.DescribeConfState(inc.confState))
		})
	})
	t.Run("applyConf after a real step-down passes", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Faults = FaultsNone
		craftedRun(t, cfg, func(_ *Cluster, inc *incarnation) (string, string) {
			e := &raftpb.Entry{Index: new(inc.raftApplied + 1), Term: new(inc.rn.BasicStatus().GetTerm()), Type: raftpb.EntryConfChangeV2.Enum(), Data: wal.Marshal(removeSelf(inc.s.id))}
			inc.applyConf(e)
			return oracle.LeaderIsVoter, ""
		})
	})
	t.Run("observe", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Faults = FaultsNone
		cfg.StepDownOnRemoval = false
		craftedRun(t, cfg, func(c *Cluster, inc *incarnation) (string, string) {
			c.cfg.StepDownOnRemoval = true
			inc.confState = inc.rn.ApplyConfChange(removeSelf(inc.s.id))
			st := inc.rn.BasicStatus()
			c.o.Role(inc.now(), inc.n.Name(), inc.s.id, st.GetTerm(), raft.StateFollower, 0) // the next observation is a change
			inc.observe()
			return oracle.LeaderIsVoter, fmt.Sprintf("%s is leader of term %d but not a voter in %s", inc.n.Name(), st.GetTerm(), raft.DescribeConfState(inc.confState))
		})
	})
}

// ETC-121: an undecodable conf-change entry is a harness error with a fixed text; protobuf's
// error message is not in it.
func TestConfDecodeErrorText(t *testing.T) {
	for _, typ := range []raftpb.EntryType{raftpb.EntryConfChange, raftpb.EntryConfChangeV2} {
		t.Run(typ.String(), func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Faults = FaultsNone
			craftedRun(t, cfg, func(_ *Cluster, inc *incarnation) (string, string) {
				i := inc.raftApplied + 1
				inc.applyConf(&raftpb.Entry{Index: new(i), Term: new(uint64(1)), Type: typ.Enum(), Data: []byte{0xff}})
				return oracle.Harness, fmt.Sprintf("%s: conf change decode: undecodable entry %d", inc.n.Name(), i)
			})
		})
	}
}
