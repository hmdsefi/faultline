// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package etcdraft

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.etcd.io/raft/v3/raftpb"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
	"github.com/hmdsefi/faultline/kernel/simnet"

	"github.com/hmdsefi/faultline/harness/etcdraft/internal/oracle"
)

// isolateN3 is the AT-ETC-19 script: isolate n3 from 2 s to 12 s.
func isolateN3() []fault.Event {
	return []fault.Event{
		{At: kernel.Time(2 * time.Second), Kind: fault.KindIsolate, Node: "n3"},
		{At: kernel.Time(12 * time.Second), Kind: fault.KindHeal},
	}
}

// Raft's debug lines after ReportSnapshot(SnapshotFinish) and ReportSnapshot(SnapshotFailure)
// for a peer it was sending a snapshot to.
const (
	raftSnapFinish  = "snapshot succeeded, resumed sending replication messages"
	raftSnapFailure = "snapshot failed, resumed sending replication messages"
)

// snapRecord is an etcdraft.snapshot record and the name of its node.
type snapRecord struct {
	node string
	kernel.Record
}

func (r snapRecord) action() string { return attrOf(r.Record, "action") }

func (r snapRecord) index() uint64 {
	i, _ := strconv.ParseUint(attrOf(r.Record, "index"), 10, 64)
	return i
}

// memState is an up server's MemoryStorage first index and snapIndex at the end of a run.
type memState struct {
	node             string
	first, snapIndex uint64
}

// snapOutcome is what a stage 1b run leaves for its assertions.
type snapOutcome struct {
	cfg             Config
	stats           Stats
	snaps           []snapRecord
	finishes, fails int // raft's raftSnapFinish and raftSnapFailure lines
	mem             []memState
}

// snapshotRun runs the AT-ETC-19 setup (seed 1, SnapshotEvery 50, n3 isolated from 2 s to
// 12 s) with raft's debug log in a full trace. With lat > 0 it adds the AT-ETC-20 link: lat on
// the leader -> n3 link from 12 s to 20 s, where the leader is the one at 12 s, kept for the
// undo at 20 s.
func snapshotRun(t *testing.T, lat time.Duration) snapOutcome {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Faults = FaultsNone
	cfg.SnapshotEvery = 50
	cfg.LogLevel = LogDebug
	opts := testOptions(t, &cfg, 30*time.Second, true)
	var o snapOutcome
	runSeed(t, 1, opts, func(w *faultline.World) {
		c := Setup(w, cfg)
		evs := isolateN3()
		if lat > 0 {
			var linked kernel.NodeID
			w.Role("snap-source", func() []kernel.NodeID {
				if linked == 0 {
					if id, _ := c.Leader(); id != 0 {
						linked = c.Server(id).ID()
					}
				}
				if linked == 0 {
					return nil
				}
				return []kernel.NodeID{linked}
			})
			evs = append(evs,
				fault.Event{At: kernel.Time(12 * time.Second), Kind: fault.KindLink, Role: "snap-source", Peer: "n3", Link: &simnet.Link{Latency: lat}},
				fault.Event{At: kernel.Time(20 * time.Second), Kind: fault.KindLinkReset, Role: "snap-source", Peer: "n3"})
		}
		w.Plan(fault.Script(evs...))
		w.Final("test: snapshots", func() error {
			o = snapOutcome{cfg: c.cfg, stats: c.Stats()}
			for _, r := range w.Sim.Records() {
				switch {
				case r.Kind == "etcdraft.snapshot":
					o.snaps = append(o.snaps, snapRecord{w.Sim.Node(r.Node).Name(), r})
				case r.Kind == "etcdraft.raft_log" && strings.Contains(r.Text, raftSnapFinish):
					o.finishes++
				case r.Kind == "etcdraft.raft_log" && strings.Contains(r.Text, raftSnapFailure):
					o.fails++
				}
			}
			for _, s := range c.servers {
				if s.inc != nil && s.node.State() == kernel.NodeUp {
					first, _ := s.inc.ms.FirstIndex()
					o.mem = append(o.mem, memState{s.node.Name(), first, s.inc.snapIndex})
				}
			}
			return nil
		})
	})
	return o
}

var hex16 = regexp.MustCompile(`^[0-9a-f]{16}$`)

// snapshotProblems checks a stage 1b run's etcdraft.snapshot records against §8, the ETC-152
// bookkeeping, the Stats counters, the ETC-150 spacing and the ETC-151 compaction index. The
// compaction check needs SnapshotEvery >= CompactKeep: then every local snapshot lies at
// least CompactKeep entries above the previous snapshot, so MemoryStorage starts at
// index - CompactKeep + 1 after it. With a smaller SnapshotEvery the function reports only that.
func snapshotProblems(o snapOutcome) []string {
	if o.cfg.SnapshotEvery < o.cfg.CompactKeep {
		return []string{fmt.Sprintf("snapshotProblems needs SnapshotEvery >= CompactKeep, got %d and %d", o.cfg.SnapshotEvery, o.cfg.CompactKeep)}
	}
	var problems []string
	bad := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	count := map[string]int{}
	pending := map[string]uint64{} // "<node>/<peer>": index of the latest send not yet acked or timed out (ETC-152)
	prev := map[string]uint64{}    // latest snapshot index of each node; the bootstrap snapshot is 1
	last := map[string]snapRecord{}
	exact := 0
	for _, r := range o.snaps {
		a, i, peer := r.action(), r.index(), attrOf(r.Record, "peer")
		count[a]++
		var keys []string
		for _, at := range r.Attrs {
			keys = append(keys, at.Key)
		}
		want, wantKeys := fmt.Sprintf("snapshot %s %d", a, i), []string{"action", "index", "peer"}
		switch a {
		case "create", "install":
			wantKeys = []string{"action", "index", "data_hash"}
			if !hex16.MatchString(attrOf(r.Record, "data_hash")) {
				bad("%s: %q has data_hash %q, want 16 lowercase hex digits", r.node, r.Text, attrOf(r.Record, "data_hash"))
			}
		case "send", "timeout":
			want += " to " + peer
		case "ack":
			want += " from " + peer
		default:
			bad("%s: %q has action %q", r.node, r.Text, a)
		}
		if r.Text != want || !slices.Equal(keys, wantKeys) {
			bad("%s: record %q with attributes %v, want %q with %v (§8)", r.node, r.Text, keys, want, wantKeys)
		}
		key := r.node + "/" + peer
		switch a {
		case "send":
			pending[key] = i
		case "ack", "timeout":
			if p, ok := pending[key]; !ok {
				bad("%s: %q with no snapshot pending to %s (ETC-152)", r.node, r.Text, peer)
			} else if p != i {
				bad("%s: %q, but the latest snapshot sent to %s is %d (ETC-152)", r.node, r.Text, peer, p)
			}
			delete(pending, key)
		case "install":
			prev[r.node] = i
			last[r.node] = r
		case "create":
			p := max(prev[r.node], 1)
			if i < p+o.cfg.SnapshotEvery {
				bad("%s: snapshot create %d only %d entries after snapshot %d; SnapshotEvery is %d (ETC-150)", r.node, i, i-p, p, o.cfg.SnapshotEvery)
			}
			if i == p+o.cfg.SnapshotEvery {
				exact++
			}
			prev[r.node] = i
			last[r.node] = r
		}
	}
	if exact == 0 {
		bad("no snapshot was taken at exactly SnapshotEvery (%d) entries after the previous one (ETC-150 uses >=)", o.cfg.SnapshotEvery)
	}
	st := o.stats
	for _, c := range []struct {
		name   string
		action string
		n      int
	}{
		{"SnapshotsCreated", "create", st.SnapshotsCreated},
		{"SnapshotsInstalled", "install", st.SnapshotsInstalled},
		{"SnapshotsSent", "send", st.SnapshotsSent},
		{"SnapshotReportsFinish", "ack", st.SnapshotReportsFinish},
		{"SnapshotReportsFailure", "timeout", st.SnapshotReportsFailure},
	} {
		if c.n != count[c.action] {
			bad("Stats.%s = %d, but %d %q records", c.name, c.n, count[c.action], c.action)
		}
	}
	if o.finishes > count["ack"] || o.fails > count["timeout"] {
		bad("raft logged %d finished and %d failed snapshot reports for %d acks and %d timeouts", o.finishes, o.fails, count["ack"], count["timeout"])
	}
	// ETC-151 step 6: memory keeps CompactKeep entries below a local snapshot; an installed
	// snapshot leaves none.
	for _, m := range o.mem {
		r, ok := last[m.node]
		if !ok {
			continue
		}
		i := r.index()
		want := i + 1
		if r.action() == "create" && i > o.cfg.CompactKeep {
			want = i - o.cfg.CompactKeep + 1
		}
		if m.first != want || m.snapIndex != i {
			bad("%s: after snapshot %s %d, MemoryStorage starts at %d and snapIndex is %d; want %d and %d (ETC-151 step 6)", m.node, r.action(), i, m.first, m.snapIndex, want, i)
		}
	}
	return problems
}

// AT-ETC-19, plus §8 record texts, ETC-150 spacing, ETC-151 compaction and raft seeing
// ReportSnapshot(SnapshotFinish) (ETC-152).
func TestSnapshotTransfer(t *testing.T) {
	o := snapshotRun(t, 0)
	st := o.stats
	if st.SnapshotsCreated < 1 || st.SnapshotsInstalled < 1 || st.SnapshotReportsFinish < 1 || st.SnapshotsSent < 1 {
		t.Fatalf("stats %+v; want at least one snapshot created, sent, installed and acknowledged", st)
	}
	if o.finishes < 1 {
		t.Errorf("raft logged no %q line: ReportSnapshot(SnapshotFinish) never reached it", raftSnapFinish)
	}
	for _, p := range snapshotProblems(o) {
		t.Error(p)
	}
}

// AT-ETC-20: a 3 s latency on the leader -> n3 link (with every leader -> n3 message lost
// instead, n3 never answers, the leader never marks it recently active, and no MsgSnap is
// sent). A late ack of an earlier send is ignored (ETC-152). With a 1.5 s latency every
// snapshot is acknowledged within SnapshotTimeout (2 s), so the timeout is not too short.
func TestSnapshotTimeout(t *testing.T) {
	t.Run("3s", func(t *testing.T) {
		o := snapshotRun(t, 3*time.Second)
		if o.stats.SnapshotReportsFailure < 1 || o.fails < 1 {
			t.Fatalf("stats %+v, %d %q lines; want at least one ReportSnapshot(SnapshotFailure) that raft saw", o.stats, o.fails, raftSnapFailure)
		}
		for _, p := range snapshotProblems(o) {
			t.Error(p)
		}
	})
	t.Run("1.5s", func(t *testing.T) {
		o := snapshotRun(t, 1500*time.Millisecond)
		if st := o.stats; st.SnapshotReportsFailure != 0 || st.SnapshotReportsFinish < 1 {
			t.Fatalf("stats %+v; want every snapshot acknowledged before its timeout", st)
		}
		for _, p := range snapshotProblems(o) {
			t.Error(p)
		}
	})
}

// ETC-152 bookkeeping on n1, with crafted calls on its live incarnation and no real MsgSnap
// (SnapshotEvery is 0): a second send to the same peer cancels the first send's timer, an ack
// of another index is ignored, and the matching ack reports, clears the entry and cancels the
// second timer: a third send right after the ack is still pending after the second timer's
// due time (8 s), and only its own timer times it out later.
func TestSnapshotBookkeeping(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Faults = FaultsNone
	opts := testOptions(t, &cfg, 15*time.Second, true)
	var problems, texts []string
	var st Stats
	runSeed(t, 1, opts, func(w *faultline.World) {
		c := Setup(w, cfg)
		msnap := &raftpb.Message{Type: raftpb.MsgSnap.Enum(), From: new(uint64(1)), To: new(uint64(3)),
			Snapshot: &raftpb.Snapshot{Metadata: &raftpb.SnapshotMetadata{Index: new(uint64(1000))}}}
		at := func(d time.Duration, what string, fn func(inc *incarnation), wantPending bool) {
			w.Sim.At(kernel.Time(d), "test/"+what, func() {
				inc := c.server(1).inc
				fn(inc)
				if _, ok := inc.pendingSnap[3]; ok != wantPending {
					problems = append(problems, fmt.Sprintf("after %s at %v: snapshot to 3 pending = %v, want %v", what, d, ok, wantPending))
				}
			})
		}
		n3 := c.server(3).node.ID()
		at(5*time.Second, "send", func(inc *incarnation) { inc.trackSnap(msnap) }, true)
		at(6*time.Second, "resend", func(inc *incarnation) { inc.trackSnap(msnap) }, true)
		at(7500*time.Millisecond, "the first send's timeout", func(*incarnation) {}, true)
		at(7600*time.Millisecond, "an ack of 999", func(inc *incarnation) { inc.onSnapAck(n3, "n3", snapAckPacket(999)) }, true)
		at(7700*time.Millisecond, "an ack of 1000", func(inc *incarnation) { inc.onSnapAck(n3, "n3", snapAckPacket(1000)) }, false)
		at(7800*time.Millisecond, "a third send", func(inc *incarnation) { inc.trackSnap(msnap) }, true)
		at(8100*time.Millisecond, "the second send's timeout", func(*incarnation) {}, true)
		w.Final("test: records", func() error {
			st, texts = c.Stats(), nil
			for _, r := range recordsOf(w, "etcdraft.snapshot") {
				texts = append(texts, r.Text)
			}
			return nil
		})
	})
	for _, p := range problems {
		t.Error(p)
	}
	want := []string{"snapshot send 1000 to 3", "snapshot send 1000 to 3", "snapshot ack 1000 from 3", "snapshot send 1000 to 3", "snapshot timeout 1000 to 3"}
	if !slices.Equal(texts, want) || st.SnapshotReportsFinish != 1 || st.SnapshotReportsFailure != 1 {
		t.Errorf("records %q, %d finish and %d failure reports; want %q, 1 and 1", texts, st.SnapshotReportsFinish, st.SnapshotReportsFailure, want)
	}
}

// ETC-151 step 4: a local snapshot is a persist point, so a recovery that lost it is a
// durable-state violation.
func TestLocalSnapshotPersistPoint(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Faults = FaultsNone
	craftedRun(t, cfg, func(c *Cluster, inc *incarnation) (string, string) {
		before, l := inc.snapIndex, inc.applied
		if !inc.takeSnapshot(l) {
			return oracle.DurableState, "takeSnapshot failed"
		}
		hs, _, _ := inc.ms.InitialState()
		c.o.Recovered(inc.now(), inc.n.Name(), inc.s.id, oracle.Recovery{ValidEnd: inc.wal.Size(), HardState: hs, SnapIndex: before})
		return oracle.DurableState, fmt.Sprintf("%s recovered snapshot index %d below persisted snapshot index %d", inc.n.Name(), before, l)
	})
}

// ETC-190: BugSkipSync also skips the sync of a local snapshot, so with snapshots on and no
// crash only the bootstrap syncs are counted.
func TestSkipSyncSkipsSnapshotSync(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Faults = FaultsNone
	cfg.SnapshotEvery = 50
	cfg.Bugs = BugSkipSync
	opts := testOptions(t, &cfg, 15*time.Second, false)
	var st Stats
	runSeed(t, 1, opts, func(w *faultline.World) {
		c := Setup(w, cfg)
		w.Final("test: stats", func() error { st = c.Stats(); return nil })
	})
	if st.SnapshotsCreated == 0 || st.Syncs != uint64(st.Boots) { //nolint:gosec // Boots is 3
		t.Fatalf("SnapshotsCreated = %d, Syncs = %d with %d boots; want snapshots and only the bootstrap syncs", st.SnapshotsCreated, st.Syncs, st.Boots)
	}
}

// ETC-151 step 3 and ETC-037: a failed sync of a local snapshot crashes the server before the
// snapshot counts (no create record), and the server restarts and recovers. No shipped preset
// reaches this sync: each fails one sync, and every Finish that snapshots has just synced its
// Ready, which takes the failure. So a crafted event at 5 s makes n3's next sync fail and takes
// a snapshot on its live incarnation; with no sync latency no Ready is in flight then.
func TestSnapshotSyncFailure(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Faults = FaultsNone
	cfg.SyncLatencyMin, cfg.SyncLatencyMax = 0, 0
	opts := testOptions(t, &cfg, 20*time.Second, true)
	at := kernel.Time(5 * time.Second)
	var took bool
	var l uint64
	var got []string
	var st Stats
	runSeed(t, 1, opts, func(w *faultline.World) {
		c := Setup(w, cfg)
		w.Sim.At(at, "test/snapshot-sync-failure", func() {
			inc := c.server(3).inc
			l = inc.applied
			w.Disk.Volume(inc.n).FailSyncs(1)
			took = inc.takeSnapshot(l)
		})
		w.Final("test: records", func() error {
			got, st = nil, c.Stats()
			n3 := c.server(3).node.ID()
			for _, r := range w.Sim.Records() {
				if r.Node != n3 || r.At < at {
					continue
				}
				switch r.Kind {
				case "etcdraft.io_crash":
					got = append(got, "io_crash op="+attrOf(r, "op"))
				case "etcdraft.snapshot":
					got = append(got, r.Text)
				case "etcdraft.boot":
					got = append(got, "boot fresh="+attrOf(r, "fresh"))
				}
				if r.Kind == "etcdraft.boot" {
					break // the restart: n3's records up to it are the ones that matter
				}
			}
			return nil
		})
	})
	want := []string{"io_crash op=sync", "boot fresh=false"}
	if took || !slices.Equal(got, want) || st.IOCrashes != 1 || st.Recoveries != 1 {
		t.Fatalf("takeSnapshot(%d) = %v; n3's records from %v to its restart: %q; IOCrashes %d, Recoveries %d; want false, %q, 1 and 1",
			l, took, at, got, st.IOCrashes, st.Recoveries, want)
	}
}
