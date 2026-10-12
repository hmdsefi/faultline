// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package etcdraft

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.etcd.io/raft/v3/raftpb"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/check/history"
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

// membershipSeeds is the number of seeds per cluster size in TestMembershipUnderFaults.
const membershipSeeds = 300

// TestMembershipUnderFaults is stage 1c under FaultsDefault for 3 and 5 nodes: every seed passes
// the harness's checks, and the admin keeps going until the drain (adminStopped). Skipped under
// -short and under the race detector.
func TestMembershipUnderFaults(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip(strconv.Itoa(membershipSeeds) + " seeds per cluster size; skipped under -short and -race")
	}
	for _, nodes := range []int{3, 5} {
		t.Run(strconv.Itoa(nodes)+"nodes", func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Nodes = nodes
			cfg.Membership.Enabled = true
			cfg, opts, err := validateOptions(cfg, faultline.Options{Seeds: membershipSeeds, Duration: 60 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			faultline.Run(t, opts, func(w *faultline.World) {
				c := Setup(w, cfg)
				w.Plan(Faults(cfg))
				w.Final("test: admin keeps going", func() error { return adminStopped(c) })
			})
		})
	}
}

// adminStopped returns an error if the admin stopped before the drain. ETC-171: after a conf
// change, a conf read that allows no change, or an operation that ends as Info, the admin waits at
// most 3*Every/2 and starts its next round, so its last operation either is still pending or ended
// less than 3*Every/2 before the drain.
func adminStopped(c *Cluster) error {
	var last history.Op
	for _, op := range c.w.History.Ops() {
		if op.Process == c.admin.name {
			last = op
		}
	}
	if last.ID == 0 {
		return errors.New("the admin invoked no operation")
	}
	every := c.cfg.Membership.Every
	if due := last.Return.Add(every + every/2); last.Status != history.Pending && due < c.drainStart {
		return fmt.Errorf("admin stopped early: its last operation (op %d, %s) completed as %s at %s, so its next round was due by %s, before the drain at %s",
			last.ID, last.F, last.Status, last.Return, due, c.drainStart)
	}
	return nil
}

// ETC-174 and ETC-037: a WAL error while a server snapshots a conf change crashes it, and it
// applies nothing more of that Ready. TestMembershipUnderFaults does not reach this: a sync-fail
// fault fails one sync, and a Ready that commits a conf change syncs its own records before the
// snapshot. So the test fills the server's volume while such a Ready, which also commits a later
// entry, waits for its sync; the snapshot's WAL append then fails with ErrNoSpace.
func TestConfSnapshotWALError(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Faults = FaultsNone
	cfg.Membership.Enabled = true
	cfg.Membership.Every = time.Second
	// Slow syncs and busy clients, so a conf change commits in one Ready with later entries.
	cfg.SyncLatencyMin, cfg.SyncLatencyMax = 5*time.Millisecond, 50*time.Millisecond
	cfg.Workload.ThinkMin, cfg.Workload.ThinkMax = 0, 20*time.Millisecond
	opts := testOptions(t, &cfg, 30*time.Second, true)
	var filled, name string // the filled server: where and when, and its name
	var crashes, after []string
	runSeed(t, 1, opts, func(w *faultline.World) {
		c := Setup(w, cfg)
		var full *kernel.Node // the server whose volume is full, until it crashes
		filled, name = "", ""
		w.Sim.OnEvent(func() {
			if full != nil && full.State() == kernel.NodeDown {
				w.Disk.Volume(full).SetCapacity(0)
				full = nil
			}
			if filled != "" {
				return
			}
			for _, s := range c.servers {
				if s.inc == nil || s.inc.inflight == nil {
					continue
				}
				ents := s.inc.inflight.CommittedEntries
				for _, e := range ents[:max(len(ents)-1, 0)] {
					if e.GetType() == raftpb.EntryConfChangeV2 {
						v := w.Disk.Volume(s.node)
						v.SetCapacity(v.Usage())
						full, name = s.node, s.node.Name()
						filled = fmt.Sprintf("%s before applying %d", name, e.GetIndex())
						return
					}
				}
			}
		})
		w.Final("test: records", func() error {
			crashes, after = nil, nil
			down := map[kernel.NodeID]bool{} // crashed by a WAL error and not booted since
			for _, r := range w.Sim.Records() {
				switch {
				case r.Kind == "etcdraft.io_crash":
					crashes = append(crashes, w.Sim.Node(r.Node).Name()+" "+attrOf(r, "op"))
					down[r.Node] = true
				case r.Kind == "kernel.boot":
					down[r.Node] = false
				case down[r.Node] && strings.HasPrefix(r.Kind, "etcdraft."):
					after = append(after, w.Sim.Node(r.Node).Name()+": "+r.Text)
				}
			}
			return nil
		})
	})
	if filled == "" || len(crashes) != 1 || crashes[0] != name+" append" {
		t.Fatalf("filled volume: %q, WAL-error crashes %v; want one append crash of the filled server", filled, crashes)
	}
	if len(after) != 0 {
		t.Fatalf("filled %s; records of the crashed server before its restart: %v", filled, after)
	}
}

// ETC-171 step 1: the admin's wait saturates instead of overflowing, so a Membership.Every too
// long for Every + Every/2 to fit in a time.Duration runs without a panic.
func TestAdminWaitSaturates(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Faults = FaultsNone
	cfg.Membership = Membership{Enabled: true, Spares: 1, Every: math.MaxInt64}
	opts := testOptions(t, &cfg, 20*time.Second, false)
	var st Stats
	runSeed(t, 1, opts, func(w *faultline.World) {
		c := Setup(w, cfg)
		w.Final("test: stats", func() error {
			st = c.Stats()
			return nil
		})
	})
	if st.ConfChangesApplied != 0 || st.LastIndex == 0 {
		t.Fatalf("ConfChangesApplied = %d, LastIndex = %d; want a run with no conf change", st.ConfChangesApplied, st.LastIndex)
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
