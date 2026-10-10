// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package etcdraft

import (
	"slices"
	"testing"
	"time"

	"go.etcd.io/raft/v3/raftpb"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel"

	"github.com/hmdsefi/faultline/harness/etcdraft/internal/kv"
	"github.com/hmdsefi/faultline/harness/etcdraft/internal/wal"
	"github.com/hmdsefi/faultline/harness/etcdraft/internal/wire"
)

// AT-ETC-21
func TestMembershipChanges(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Faults = FaultsNone
	cfg.Membership.Enabled = true
	opts := testOptions(t, &cfg, 30*time.Second, false)
	var st Stats
	var voters []uint64
	var spareJoined bool
	var cids []uint32
	runSeed(t, 1, opts, func(w *faultline.World) {
		c := Setup(w, cfg)
		w.Plan(Faults(cfg))
		w.Final("test: stats", func() error {
			st, voters = c.Stats(), c.finalVoters()
			spareJoined, cids = false, nil
			for i := uint64(1); i <= st.LastIndex; i++ {
				cs := c.o.ConfAt(i)
				for _, id := range append(slices.Clone(cs.GetVoters()), cs.GetLearners()...) {
					spareJoined = spareJoined || id > uint64(cfg.Nodes) //nolint:gosec // Nodes is 3 or 5
				}
			}
			for _, cl := range c.clients {
				cids = append(cids, cl.cid)
			}
			cids = append(cids, c.probe.cid, c.admin.cid)
			return nil
		})
	})
	if st.ConfChangesApplied < 3 || len(voters) < 3 || len(voters) > 5 {
		t.Fatalf("ConfChangesApplied = %d, final voters %v", st.ConfChangesApplied, voters)
	}
	if !spareJoined {
		t.Fatalf("no spare joined the configuration in %d conf-change entries", st.ConfChangesApplied)
	}
	if want := []uint32{1, 2, 3, 4, 5}; !slices.Equal(cids, want) { // ETC-090: c<k> = k, probe = K+1, admin = K+2
		t.Fatalf("client IDs %v, want %v", cids, want)
	}
}

// addLearner returns a boot function for a test client "tadmin" that, from 5 s on,
// proposes adding raft ID 4 as a learner to the current leader every 500 ms until a
// reply with StatusOK arrives. It uses client ID 99 and op ID 1 for every attempt, so
// the session makes the change apply at most once.
func addLearner(c *Cluster, done *bool) kernel.BootFunc {
	cc := &raftpb.ConfChangeV2{Changes: []*raftpb.ConfChangeSingle{{Type: raftpb.ConfChangeAddLearnerNode.Enum(), NodeId: new(uint64(4))}}}
	return func(n *kernel.Node) {
		c.w.Net.Handle(n, func(_ kernel.NodeID, payload any) {
			p, ok := payload.(*packet)
			if !ok || p.data[0] != wire.TagReply {
				return
			}
			if rep, err := wire.DecodeReply(p.data); err == nil && rep.Status == wire.StatusOK {
				*done = true
			}
		})
		var attempt uint32
		var try func()
		try = func() {
			if *done {
				return
			}
			if id, _ := c.Leader(); id != 0 {
				attempt++
				q := wire.Request{Client: 99, ID: 1, Attempt: attempt, Op: kv.OpConfChange, Data: wal.Marshal(cc)}
				c.w.Net.Send(n, c.Server(id).ID(), requestPacket("tadmin", q))
			}
			n.After(500*time.Millisecond, "test/add-learner", try)
		}
		n.After(5*time.Second, "test/add-learner", try)
	}
}

// AT-ETC-22
func TestLearnerNeedsConfSnapshot(t *testing.T) {
	run := func(t *testing.T, noConfSnapshot bool) (learner uint64, last uint64, added bool, sent, installed int) {
		cfg := DefaultConfig()
		cfg.Faults = FaultsNone
		cfg.Membership = Membership{Enabled: true, Spares: 1, Every: time.Hour} // the admin never acts
		cfg.noConfSnapshot = noConfSnapshot
		opts := testOptions(t, &cfg, 30*time.Second, false)
		runSeed(t, 1, opts, func(w *faultline.World) {
			c := Setup(w, cfg)
			done := false // per attempt: body runs again for re-runs (API-031)
			w.AddClient("tadmin", addLearner(c, &done))
			w.Final("test: learner", func() error {
				learner, _ = c.Applied(4)
				last = c.Stats().LastIndex
				added = done
				sent, installed = c.Stats().SnapshotsSent, c.Stats().SnapshotsInstalled
				return nil
			})
		})
		return learner, last, added, sent, installed
	}
	var off, on struct {
		learner, last   uint64
		added           bool
		sent, installed int
	}
	t.Run("without ETC-174", func(t *testing.T) { off.learner, off.last, off.added, off.sent, off.installed = run(t, true) })
	t.Run("with ETC-174", func(t *testing.T) { on.learner, on.last, on.added, on.sent, on.installed = run(t, false) })
	if !off.added || off.learner > 1 || off.sent == 0 || off.installed != 0 {
		t.Fatalf("without ETC-174: added=%v, learner applied %d of %d, snapshots sent %d, installed %d; want the learner stuck at or below the bootstrap snapshot (1) with every snapshot refused (F13)", off.added, off.learner, off.last, off.sent, off.installed)
	}
	if !on.added || on.learner != on.last {
		t.Fatalf("with ETC-174: added=%v, learner applied %d of %d; want it caught up", on.added, on.learner, on.last)
	}
}
