package etcdraft

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"

	"github.com/hmdsefi/faultline/kernel"

	"github.com/hmdsefi/faultline/harness/etcdraft/internal/kv"
	"github.com/hmdsefi/faultline/harness/etcdraft/internal/wal"
)

// onReady is the ready event (ETC-061, ETC-063).
func (inc *incarnation) onReady() {
	inc.readyPending = false
	if inc.inflight != nil || !inc.rn.HasReady() {
		return
	}
	c, cfg := inc.c, inc.cfg()
	if now := inc.now(); now != inc.instant {
		inc.instant, inc.readiesAtInstant = now, 0
	}
	inc.readiesAtInstant++
	if inc.readiesAtInstant > maxReadiesPerInstant {
		inc.harnessError(fmt.Errorf("ready livelock at %s", inc.now()))
		return
	}
	rd := inc.rn.Ready()
	c.stats.Readies++
	inc.emitReady(rd)
	inc.checkReady(rd)
	inc.requiredHS = nil
	if !raft.IsEmptyHardState(rd.HardState) {
		c.o.HardState(inc.now(), inc.n.Name(), inc.s.id, rd.HardState, false)
		inc.requiredHS = rd.HardState
	}
	inc.persisted, inc.sentEarly = false, false
	if cfg.earlySend {
		inc.sendMessages(rd)
		inc.sentEarly = true
	}
	wrote, ok := inc.writeReady(rd)
	if !ok {
		return
	}
	needSync := wrote && (!cfg.SyncOnlyMustSync || rd.MustSync || !raft.IsEmptySnap(rd.Snapshot))
	if needSync {
		var d time.Duration
		if cfg.SyncLatencyMax > 0 {
			d = kernel.Uniform(inc.n.Rand(), cfg.SyncLatencyMin, cfg.SyncLatencyMax)
		}
		if d > 0 {
			inc.inflight = &rd
			inc.n.After(d, "etcdraft/sync", inc.onSync)
			return
		}
		if !inc.syncStep(rd) {
			return
		}
	} else {
		inc.persisted = true // nothing to sync: the records (if any) need no persist point
	}
	inc.finish(rd)
}

func (inc *incarnation) emitReady(rd raft.Ready) {
	first, last := uint64(0), uint64(0)
	if n := len(rd.Entries); n > 0 {
		first, last = rd.Entries[0].GetIndex(), rd.Entries[n-1].GetIndex()
	}
	hs := describeHS(rd.HardState)
	inc.c.emit(inc.n, "etcdraft.ready",
		fmt.Sprintf("ready e=%d c=%d m=%d hs=%s", len(rd.Entries), len(rd.CommittedEntries), len(rd.Messages), hs),
		attr("entries", strconv.Itoa(len(rd.Entries))), attr("first", u64(first)), attr("last", u64(last)),
		attr("committed", strconv.Itoa(len(rd.CommittedEntries))), attr("msgs", strconv.Itoa(len(rd.Messages))),
		attr("snap_index", u64(rd.Snapshot.GetMetadata().GetIndex())), attr("must_sync", boolStr(rd.MustSync)), attr("hs", hs))
}

// checkReady applies ETC-114 rules 1 to 3.
func (inc *incarnation) checkReady(rd raft.Ready) {
	snap := !raft.IsEmptySnap(rd.Snapshot)
	first, _ := inc.ms.FirstIndex()
	lastMS, _ := inc.ms.LastIndex()
	last := lastMS
	if snap {
		last = rd.Snapshot.GetMetadata().GetIndex()
	}
	for i := 1; i < len(rd.Entries); i++ {
		if rd.Entries[i].GetIndex() != rd.Entries[i-1].GetIndex()+1 {
			inc.contract(1, "entries not contiguous at index %d after %d", rd.Entries[i].GetIndex(), rd.Entries[i-1].GetIndex())
			break
		}
	}
	if len(rd.Entries) > 0 {
		e0 := rd.Entries[0].GetIndex()
		switch {
		case snap && e0 != rd.Snapshot.GetMetadata().GetIndex()+1:
			inc.contract(1, "first entry %d does not follow snapshot %d", e0, rd.Snapshot.GetMetadata().GetIndex())
		case !snap && (e0 < first || e0 > lastMS+1):
			inc.contract(1, "first entry %d outside [%d, %d]", e0, first, lastMS+1)
		}
		last = rd.Entries[len(rd.Entries)-1].GetIndex()
	}
	if !raft.IsEmptyHardState(rd.HardState) && rd.GetCommit() > last {
		inc.contract(2, "commit %d beyond last index %d", rd.GetCommit(), last)
	}
	if snap && rd.Snapshot.GetMetadata().GetIndex() <= inc.applied {
		inc.contract(3, "snapshot index %d not above applied index %d", rd.Snapshot.GetMetadata().GetIndex(), inc.applied)
	}
}

// writeReady appends the Ready's records in ETC-042 order. wrote reports whether any
// record was written; ok is false if a WAL error crashed the node.
func (inc *incarnation) writeReady(rd raft.Ready) (wrote, ok bool) {
	if !raft.IsEmptySnap(rd.Snapshot) {
		if !inc.append(wal.RecSnapshotInstall, wal.Marshal(rd.Snapshot)) {
			return false, false
		}
		wrote = true
		inc.walLast = rd.Snapshot.GetMetadata().GetIndex()
	}
	for _, e := range rd.Entries {
		if !inc.append(wal.RecEntry, wal.Marshal(e)) {
			return false, false
		}
		wrote = true
		inc.walLast = e.GetIndex()
	}
	if !raft.IsEmptyHardState(rd.HardState) {
		if !inc.append(wal.RecHardState, wal.Marshal(rd.HardState)) {
			return false, false
		}
		wrote = true
	}
	return wrote, true
}

// onSync is the sync event (ETC-062).
func (inc *incarnation) onSync() {
	rd := *inc.inflight
	inc.inflight = nil
	if !inc.syncStep(rd) {
		return
	}
	inc.finish(rd)
}

// syncStep implements ETC-064. It returns false if a WAL error crashed the node.
func (inc *incarnation) syncStep(rd raft.Ready) bool {
	c := inc.c
	if err := inc.wal.Sync(); err != nil {
		inc.walError("sync", err)
		return false
	}
	c.stats.Syncs++
	inc.persistPoint(rd, true)
	return true
}

// persistPoint records the Ready's persist point (ETC-064).
func (inc *incarnation) persistPoint(rd raft.Ready, synced bool) {
	c := inc.c
	if !raft.IsEmptySnap(rd.Snapshot) {
		inc.snapIndex = rd.Snapshot.GetMetadata().GetIndex()
	}
	inc.persisted = true
	inc.syncedLast = inc.walLast
	size := inc.wal.Size()
	c.o.Persisted(inc.s.id, inc.requiredHS, size, inc.snapIndex)
	hs := inc.requiredHS
	c.emit(inc.n, "etcdraft.persist", fmt.Sprintf("persist wal_end=%d synced=%s", size, boolStr(synced)),
		attr("wal_end", strconv.FormatInt(size, 10)), attr("synced", boolStr(synced)),
		attr("hs_term", u64(hs.GetTerm())), attr("hs_vote", u64(hs.GetVote())), attr("hs_commit", u64(hs.GetCommit())))
}

// finish implements ETC-065.
func (inc *incarnation) finish(rd raft.Ready) {
	if !raft.IsEmptySnap(rd.Snapshot) {
		if err := inc.ms.ApplySnapshot(rd.Snapshot); err != nil {
			inc.harnessError(fmt.Errorf("memory storage apply snapshot: %w", err))
			return
		}
	}
	if err := inc.ms.Append(rd.Entries); err != nil {
		inc.harnessError(fmt.Errorf("memory storage append: %w", err))
		return
	}
	if !raft.IsEmptyHardState(rd.HardState) {
		if err := inc.ms.SetHardState(rd.HardState); err != nil {
			inc.harnessError(fmt.Errorf("memory storage set hardstate: %w", err))
			return
		}
	}
	if !inc.sentEarly {
		inc.sendMessages(rd)
	}
	if !inc.apply(rd) {
		return
	}
	inc.rn.Advance(rd)
	if inc.cfg().ReportUnreachable && len(inc.unreachable) > 0 {
		peers := slices.Compact(slices.Sorted(slices.Values(inc.unreachable)))
		inc.unreachable = nil
		names := make([]string, len(peers))
		for i, p := range peers {
			inc.rn.ReportUnreachable(p)
			names[i] = u64(p)
		}
		joined := strings.Join(names, ",")
		inc.c.emit(inc.n, "etcdraft.unreachable", "unreachable "+joined, attr("peers", joined))
	}
	inc.afterEvent()
}

// sendMessages implements ETC-066, checking ETC-114 rule 4 for every message.
func (inc *incarnation) sendMessages(rd raft.Ready) {
	c, cfg := inc.c, inc.cfg()
	for _, m := range rd.Messages {
		switch {
		case !inc.persisted:
			inc.contract(4, "%s to %d sent before the persist point", m.GetType().String(), m.GetTo())
		case m.GetFrom() != inc.s.id && m.GetType() != raftpb.MsgProp: // a forwarded proposal keeps its originator
			inc.contract(4, "%s from %d sent by %d", m.GetType().String(), m.GetFrom(), inc.s.id)
		case m.GetTo() == inc.s.id:
			inc.contract(4, "%s addressed to itself", m.GetType().String())
		}
		to := c.server(m.GetTo())
		if to == nil {
			inc.harnessError(fmt.Errorf("message to unknown raft ID %d", m.GetTo()))
			return
		}
		c.w.Net.Send(inc.n, to.node.ID(), raftPacket(m))
		if cfg.ReportUnreachable && (to.node.State() == kernel.NodeDown || !c.w.Net.Connected(inc.n.ID(), to.node.ID())) {
			inc.unreachable = append(inc.unreachable, m.GetTo())
		}
	}
}

// apply implements ETC-067. It returns false after a harness error or a WAL error.
func (inc *incarnation) apply(rd raft.Ready) bool {
	c := inc.c
	if !raft.IsEmptySnap(rd.Snapshot) {
		snap := rd.Snapshot
		idx := snap.GetMetadata().GetIndex()
		cs := snap.GetMetadata().GetConfState()
		if idx <= inc.applied {
			inc.contract(5, "snapshot index %d not above applied index %d", idx, inc.applied)
		}
		if len(cs.GetVoters()) == 0 && len(cs.GetLearners()) == 0 && len(cs.GetVotersOutgoing()) == 0 {
			inc.contract(5, "snapshot at index %d has an empty configuration", idx)
		}
		state, err := kv.Decode(snap.GetData())
		if err != nil {
			inc.harnessError(fmt.Errorf("snapshot decode: %w", err))
			return false
		}
		inc.kv = state
		inc.applied, inc.raftApplied, inc.snapIndex = idx, idx, idx
		inc.confState = clone(cs)
		h := fnv64(snap.GetData())
		c.o.SnapshotState(inc.now(), inc.n.Name(), "install", idx, h, inc.confState)
		c.stats.SnapshotsInstalled++
		c.emit(inc.n, "etcdraft.snapshot", fmt.Sprintf("snapshot install %d", idx),
			attr("action", "install"), attr("index", u64(idx)), attr("data_hash", fmt.Sprintf("%016x", h)))
	}
	for _, e := range rd.CommittedEntries {
		if e.GetIndex() != inc.raftApplied+1 {
			inc.contract(6, "committed entry %d does not follow applied index %d", e.GetIndex(), inc.raftApplied)
		}
		if e.GetIndex() > inc.syncedLast {
			inc.contract(7, "committed entry %d beyond synced index %d", e.GetIndex(), inc.syncedLast)
		}
		inc.raftApplied = e.GetIndex()
		switch e.GetType() {
		case raftpb.EntryNormal:
			if e.GetIndex() <= inc.applied {
				continue // applied early under BugApplyBeforeCommit
			}
			if !inc.applyNormal(e) {
				return false
			}
		case raftpb.EntryConfChange, raftpb.EntryConfChangeV2:
			if !inc.applyConf(e) {
				return false
			}
		}
	}
	return true
}

// applyNormal applies one EntryNormal entry and records it.
func (inc *incarnation) applyNormal(e *raftpb.Entry) bool {
	var cmd kv.Command
	var dup bool
	if len(e.GetData()) > 0 {
		var err error
		cmd, err = kv.DecodeCommand(e.GetData())
		if err != nil {
			inc.harnessError(fmt.Errorf("command decode: %w", err))
			return false
		}
		_, dup = inc.kv.Apply(cmd)
	}
	inc.c.o.Apply(inc.now(), inc.n.Name(), inc.s.id, e)
	inc.emitApply(e, cmd, dup)
	inc.applied = e.GetIndex()
	return true
}

func (inc *incarnation) emitApply(e *raftpb.Entry, cmd kv.Command, dup bool) {
	op, text := "", fmt.Sprintf("apply %d", e.GetIndex())
	if cmd.Op != 0 {
		op = opName(cmd.Op)
		text = fmt.Sprintf("apply %d %s %s %s#%d", e.GetIndex(), op, cmd.Key, inc.c.clientName(cmd.Client), cmd.ID)
	}
	inc.c.emit(inc.n, "etcdraft.apply", text,
		attr("index", u64(e.GetIndex())), attr("term", u64(e.GetTerm())), attr("type", e.GetType().String()),
		attr("client", strconv.FormatUint(uint64(cmd.Client), 10)), attr("op_id", u64(cmd.ID)), attr("op", op),
		attr("key", cmd.Key), attr("dup", boolStr(dup)))
}

// decodeError is the harness error for a protobuf payload that does not unmarshal. Its text is
// fixed and never includes protobuf's error message: protobuf-go varies the spacing of its
// messages from one binary to the next on purpose, and harness errors are hashed (ETC-121). The
// protobuf error stays reachable through errors.Is and errors.As.
type decodeError struct {
	text  string
	cause error // the proto.Unmarshal error; not part of the text
}

func (e *decodeError) Error() string { return e.text }

func (e *decodeError) Unwrap() error { return e.cause }

// applyConf applies a conf-change entry (ETC-067 step 3).
func (inc *incarnation) applyConf(e *raftpb.Entry) bool {
	c, cfg := inc.c, inc.cfg()
	var cs *raftpb.ConfState
	var cmd kv.Command
	dup := false
	if e.GetType() == raftpb.EntryConfChange {
		cc := new(raftpb.ConfChange)
		if err := proto.Unmarshal(e.GetData(), cc); err != nil {
			inc.harnessError(&decodeError{text: fmt.Sprintf("conf change decode: undecodable entry %d", e.GetIndex()), cause: err})
			return false
		}
		cs = inc.rn.ApplyConfChange(cc)
	} else {
		cc := new(raftpb.ConfChangeV2)
		if err := proto.Unmarshal(e.GetData(), cc); err != nil {
			inc.harnessError(&decodeError{text: fmt.Sprintf("conf change decode: undecodable entry %d", e.GetIndex()), cause: err})
			return false
		}
		if len(cc.GetContext()) > 0 {
			client, id, err := kv.DecodeContext(cc.GetContext())
			if err != nil {
				inc.harnessError(fmt.Errorf("context decode: %w", err))
				return false
			}
			cmd = kv.Command{Client: client, ID: id, Op: kv.OpConfChange}
			if dup = inc.kv.ApplySession(client, id, kv.OpConfChange); dup {
				for _, ch := range cc.Changes {
					ch.NodeId = new(uint64(0)) // cancel a duplicate (F2, F16)
				}
			}
		}
		cs = inc.rn.ApplyConfChange(cc)
	}
	inc.confState = cs
	c.o.ConfApplied(inc.now(), inc.n.Name(), e.GetIndex(), cs)
	c.emit(inc.n, "etcdraft.conf", fmt.Sprintf("conf %d %s", e.GetIndex(), raft.DescribeConfState(cs)),
		attr("index", u64(e.GetIndex())), attr("conf", raft.DescribeConfState(cs)))
	if st := inc.rn.BasicStatus(); st.RaftState == raft.StateLeader && cfg.StepDownOnRemoval {
		isVoter := slices.Contains(cs.GetVoters(), inc.s.id) || slices.Contains(cs.GetVotersOutgoing(), inc.s.id)
		c.o.LeaderIsVoter(inc.now(), inc.n.Name(), inc.s.id, st.GetTerm(), isVoter, raft.DescribeConfState(cs))
	}
	c.o.Apply(inc.now(), inc.n.Name(), inc.s.id, e)
	inc.emitApply(e, cmd, dup)
	inc.applied = e.GetIndex()
	return true
}
