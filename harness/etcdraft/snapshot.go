package etcdraft

import (
	"errors"
	"fmt"

	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"

	"github.com/hmdsefi/faultline/kernel"

	"github.com/hmdsefi/faultline/harness/etcdraft/internal/wal"
	"github.com/hmdsefi/faultline/harness/etcdraft/internal/wire"
)

// pendingSnap is a sent MsgSnap awaiting its receipt (ETC-152).
type pendingSnap struct {
	index uint64
	timer kernel.EventID
}

// maybeSnapshot implements ETC-150. It returns false if a WAL error crashed the node
// or a harness error was recorded.
func (inc *incarnation) maybeSnapshot() bool {
	every := inc.cfg().SnapshotEvery
	if every > 0 && inc.applied-inc.snapIndex >= every {
		return inc.takeSnapshot(inc.applied)
	}
	return true
}

// takeSnapshot implements ETC-151 for index i.
func (inc *incarnation) takeSnapshot(i uint64) bool {
	c, cfg := inc.c, inc.cfg()
	data := inc.kv.Encode()
	cs := inc.confState
	l := i
	if cfg.Bugs&BugSnapshotOffByOne != 0 {
		l = i - 1
		if l <= inc.snapIndex {
			return true
		}
	}
	snap, err := inc.ms.CreateSnapshot(l, cs, data)
	if errors.Is(err, raft.ErrSnapOutOfDate) {
		return true
	}
	if err != nil {
		inc.harnessError(fmt.Errorf("memory storage create snapshot: %w", err))
		return false
	}
	if !inc.append(wal.RecSnapshotLocal, wal.Marshal(snap)) {
		return false
	}
	if cfg.Bugs&BugSkipSync == 0 {
		if err := inc.wal.Sync(); err != nil {
			inc.walError("sync", err)
			return false
		}
		c.stats.Syncs++
	}
	c.o.Persisted(inc.s.id, nil, inc.wal.Size(), l)
	inc.snapIndex = l
	h := fnv64(data)
	c.o.SnapshotState(inc.now(), inc.n.Name(), "create", l, h, cs)
	if l > cfg.CompactKeep {
		if err := inc.ms.Compact(l - cfg.CompactKeep); err != nil && !errors.Is(err, raft.ErrCompacted) {
			inc.harnessError(fmt.Errorf("memory storage compact: %w", err))
			return false
		}
	}
	c.stats.SnapshotsCreated++
	c.emit(inc.n, "etcdraft.snapshot", fmt.Sprintf("snapshot create %d", l),
		attr("action", "create"), attr("index", u64(l)), attr("data_hash", fmt.Sprintf("%016x", h)))
	return true
}

// trackSnap records a sent MsgSnap (ETC-152).
func (inc *incarnation) trackSnap(m *raftpb.Message) {
	peer, idx := m.GetTo(), m.GetSnapshot().GetMetadata().GetIndex()
	if old, ok := inc.pendingSnap[peer]; ok {
		inc.n.Cancel(old.timer)
	}
	timer := inc.n.After(inc.cfg().SnapshotTimeout, "etcdraft/snap-timeout", func() { inc.onSnapTimeout(peer, idx) })
	inc.pendingSnap[peer] = pendingSnap{index: idx, timer: timer}
	inc.c.emit(inc.n, "etcdraft.snapshot", fmt.Sprintf("snapshot send %d to %d", idx, peer),
		attr("action", "send"), attr("index", u64(idx)), attr("peer", u64(peer)))
}

// onSnapAck handles a TagSnapAck packet (ETC-152). It returns false after a harness error.
func (inc *incarnation) onSnapAck(from kernel.NodeID, sender string, p *packet) bool {
	idx, err := wire.DecodeSnapAck(p.data)
	if err != nil {
		inc.harnessError(fmt.Errorf("decode snapack from %s: %w", sender, err))
		return false
	}
	peer := inc.c.RaftID(inc.c.w.Sim.Node(from))
	ps, ok := inc.pendingSnap[peer]
	if !ok || ps.index != idx {
		return true
	}
	inc.n.Cancel(ps.timer)
	delete(inc.pendingSnap, peer)
	inc.rn.ReportSnapshot(peer, raft.SnapshotFinish)
	inc.c.stats.SnapshotReportsFinish++
	inc.c.emit(inc.n, "etcdraft.snapshot", fmt.Sprintf("snapshot ack %d from %d", idx, peer),
		attr("action", "ack"), attr("index", u64(idx)), attr("peer", u64(peer)))
	return true
}

// onSnapTimeout is the snapshot timeout event (ETC-052, ETC-152).
func (inc *incarnation) onSnapTimeout(peer, idx uint64) {
	inc.input()
	if ps, ok := inc.pendingSnap[peer]; ok && ps.index == idx {
		delete(inc.pendingSnap, peer)
		inc.rn.ReportSnapshot(peer, raft.SnapshotFailure)
		inc.c.stats.SnapshotReportsFailure++
		inc.c.emit(inc.n, "etcdraft.snapshot", fmt.Sprintf("snapshot timeout %d to %d", idx, peer),
			attr("action", "timeout"), attr("index", u64(idx)), attr("peer", u64(peer)))
	}
	inc.afterEvent()
}
