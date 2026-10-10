package etcdraft

import (
	"bytes"
	"fmt"
	"math"
	"slices"

	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"

	"github.com/hmdsefi/faultline/check/history"
	"github.com/hmdsefi/faultline/kernel"

	"github.com/hmdsefi/faultline/harness/etcdraft/internal/oracle"
	"github.com/hmdsefi/faultline/harness/etcdraft/internal/wal"
)

// invariantNames are the ETC-120 invariants in registration order.
var invariantNames = []string{
	oracle.Harness,
	oracle.ElectionSafety,
	oracle.StateMachineSafety,
	oracle.DurableState,
	oracle.HardStateMonotonic,
	oracle.ReadyContract,
	oracle.ConfigAgreement,
	oracle.LeaderIsVoter,
}

// registerChecks registers the invariants (ETC-120) and the final checks (ETC-130 to
// ETC-133) in order.
func (c *Cluster) registerChecks() {
	for _, name := range invariantNames {
		c.w.Invariant(name, func() error { return c.o.Err(name) })
	}
	c.final(oracle.Progress, c.checkProgress)
	c.final(oracle.AckedWritesSurvive, c.checkAckedWrites)
	c.final(oracle.ReplicasAgree, c.checkReplicasAgree)
	c.final(oracle.WALAgreesWithMemory, c.checkWAL)
}

// final registers a final check; a failure is also reported to selfTestHook (ETC-196).
func (c *Cluster) final(name string, check func() error) {
	c.w.Final(name, func() error {
		err := check()
		if err != nil && c.cfg.selfTestHook != nil {
			c.cfg.selfTestHook(c.w.Seed(), name)
		}
		return err
	})
}

// upFinalVoters returns the up servers listed as final voters, in raft-ID order.
func (c *Cluster) upFinalVoters() []*server {
	var out []*server
	for _, id := range c.finalVoters() {
		if s := c.server(id); s != nil && s.node.State() == kernel.NodeUp && s.inc != nil {
			out = append(out, s)
		}
	}
	return out
}

// checkProgress implements ETC-130.
func (c *Cluster) checkProgress() error {
	deadline := c.recoveryStart.Add(c.cfg.ProgressWithin)
	if !c.probeAcked {
		return fmt.Errorf("probe write not acknowledged within %v of recovery start %s", c.cfg.ProgressWithin, c.recoveryStart)
	}
	if c.probeAckedAt > deadline {
		return fmt.Errorf("probe write acknowledged at %s, after the deadline %s", c.probeAckedAt, deadline)
	}
	return nil
}

// checkAckedWrites implements ETC-131.
func (c *Cluster) checkAckedWrites() error {
	var highest uint64
	for _, op := range c.w.History.Ops() {
		if op.F != "write" || op.Status != history.OK {
			continue
		}
		var in KVInput
		if err := op.DecodeInput(&in); err != nil {
			return fmt.Errorf("write %s#%d: input decode: %v", op.Process, op.ID, err)
		}
		cl := c.clientByName(op.Process)
		if cl == nil {
			return fmt.Errorf("write %s#%d: unknown process", op.Process, op.ID)
		}
		opID := c.opIDs[op.ID]
		idx, ok := c.o.FirstApplied(cl.cid, opID)
		if !ok {
			return fmt.Errorf("write %s#%d (%s=%s) was acknowledged but never applied", op.Process, opID, in.Key, in.Value)
		}
		highest = max(highest, idx)
	}
	for _, s := range c.upFinalVoters() {
		if s.inc.applied < highest {
			return fmt.Errorf("%s applied up to %d at the end, below acknowledged write index %d", s.node.Name(), s.inc.applied, highest)
		}
	}
	return nil
}

// checkReplicasAgree implements ETC-132.
func (c *Cluster) checkReplicasAgree() error {
	for _, s := range c.upFinalVoters() {
		a := s.inc.applied
		want, ok := c.o.RefHash(a)
		got := s.inc.kv.Hash()
		if !ok {
			return fmt.Errorf("%s at index %d has state hash %016x, reference has no state at index %d", s.node.Name(), a, got, a)
		}
		if got != want {
			return fmt.Errorf("%s at index %d has state hash %016x, reference has %016x", s.node.Name(), a, got, want)
		}
		ref := c.o.ConfAt(a)
		if !bytes.Equal(confKey(s.inc.confState), confKey(ref)) {
			return fmt.Errorf("%s at index %d has configuration %s, canonical configuration is %s", s.node.Name(), a,
				raft.DescribeConfState(s.inc.confState), raft.DescribeConfState(ref))
		}
	}
	return nil
}

// confKey is wal.Marshal of cs with AutoLeave set explicitly (see oracle.confBytes).
func confKey(cs *raftpb.ConfState) []byte {
	return wal.Marshal(raftpb.EnsureConfState(clone(cs)))
}

// checkWAL implements ETC-133.
func (c *Cluster) checkWAL() error {
	for _, s := range c.servers {
		if s.node.State() != kernel.NodeUp || s.inc == nil {
			continue
		}
		if err := s.inc.walAgrees(); err != nil {
			return fmt.Errorf("%s WAL and memory disagree: %v", s.node.Name(), err)
		}
	}
	return nil
}

func (inc *incarnation) walAgrees() error {
	data, err := inc.c.w.Disk.Volume(inc.n).ReadFile(walPath)
	if err != nil {
		return fmt.Errorf("read: %v", err)
	}
	st, err := wal.Read(data)
	if err != nil {
		return fmt.Errorf("parse: %v", err)
	}
	// Memory view: MemoryStorage with the in-flight Ready applied on top as wal.Read would.
	snap, _ := inc.ms.Snapshot()
	snapIndex := snap.GetMetadata().GetIndex()
	first, _ := inc.ms.FirstIndex()
	last, _ := inc.ms.LastIndex()
	var ents []*raftpb.Entry
	if last >= first {
		ents, err = inc.ms.Entries(first, last+1, math.MaxUint64)
		if err != nil {
			return fmt.Errorf("memory entries: %v", err)
		}
	}
	hs, _, _ := inc.ms.InitialState()
	if rd := inc.inflight; rd != nil {
		ents = slices.Clone(ents) // the loop below truncates and appends: never write into MemoryStorage's array
		if !raft.IsEmptySnap(rd.Snapshot) {
			snapIndex, ents = rd.Snapshot.GetMetadata().GetIndex(), nil
		}
		for _, e := range rd.Entries {
			for len(ents) > 0 && ents[len(ents)-1].GetIndex() >= e.GetIndex() {
				ents = ents[:len(ents)-1]
			}
			ents = append(ents, e)
		}
		if !raft.IsEmptyHardState(rd.HardState) {
			hs = rd.HardState
		}
	}
	if st.SnapIndex() != snapIndex {
		return fmt.Errorf("snapshot index %d in the WAL, %d in memory", st.SnapIndex(), snapIndex)
	}
	var mem []*raftpb.Entry
	for _, e := range ents {
		if e.GetIndex() > snapIndex {
			mem = append(mem, e)
		}
	}
	if len(st.Entries) != len(mem) {
		return fmt.Errorf("%d entries above %d in the WAL, %d in memory", len(st.Entries), snapIndex, len(mem))
	}
	for i, e := range st.Entries {
		m := mem[i]
		if e.GetIndex() != m.GetIndex() || e.GetTerm() != m.GetTerm() || e.GetType() != m.GetType() || !bytes.Equal(e.GetData(), m.GetData()) {
			return fmt.Errorf("entry %d differs: WAL %s, memory %s", m.GetIndex(), raft.DescribeEntry(e, nil), raft.DescribeEntry(m, nil))
		}
	}
	walHS := st.HardState
	if walHS != nil && walHS.GetCommit() < st.SnapIndex() {
		walHS = clone(walHS)
		walHS.Commit = new(st.SnapIndex())
	}
	if raft.DescribeHardState(walHS) != raft.DescribeHardState(hs) {
		return fmt.Errorf("HardState %s in the WAL, %s in memory", raft.DescribeHardState(walHS), raft.DescribeHardState(hs))
	}
	return nil
}
