// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package oracle holds the run-wide knowledge behind the etcdraft checks. It lives
// outside every node incarnation and is never visible to the code under test.
package oracle

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"slices"

	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"

	"github.com/hmdsefi/faultline/kernel"

	"github.com/hmdsefi/faultline/harness/etcdraft/internal/kv"
	"github.com/hmdsefi/faultline/harness/etcdraft/internal/wal"
)

// Check names. Setup registers them in this order (ETC-120).
const (
	Harness            = "etcdraft: harness"
	ElectionSafety     = "etcdraft: election safety"
	StateMachineSafety = "etcdraft: state machine safety"
	DurableState       = "etcdraft: durable state"
	HardStateMonotonic = "etcdraft: hardstate monotonic"
	ReadyContract      = "etcdraft: ready contract"
	ConfigAgreement    = "etcdraft: configuration agreement"
	LeaderIsVoter      = "etcdraft: leader is voter"

	Progress            = "etcdraft: progress"
	AckedWritesSurvive  = "etcdraft: acked writes survive"
	ReplicasAgree       = "etcdraft: replicas agree"
	WALAgreesWithMemory = "etcdraft: wal agrees with memory"
)

type leaderSeen struct {
	node string
	at   kernel.Time
}

type role struct {
	lead  uint64
	state raft.StateType
}

type canonEntry struct {
	term     uint64
	typ      raftpb.EntryType
	dataHash uint64
	node     string
}

type confRecord struct {
	cs   *raftpb.ConfState
	node string // first server that applied it ("bootstrap" for index 1)
}

type opKey struct {
	client uint32
	id     uint64
}

type durable struct {
	hs        *raftpb.HardState
	walEnd    int64
	snapIndex uint64
}

// Oracle records observations and the first violation of each check.
type Oracle struct {
	violations  map[string]error
	onViolation []func(check string, err error)

	leaders map[uint64]leaderSeen // term -> first leader seen
	roles   map[uint64]role       // raft ID -> last (Lead, RaftState)

	canon        []canonEntry // canon[i-1] is index i; index 1 is the bootstrap
	ref          *kv.State
	refHash      []uint64 // refHash[i-1] is the reference state hash after index i
	firstApplied map[opKey]uint64
	confChanges  int

	confIdx []uint64 // ascending indices with a recorded configuration
	confAt  map[uint64]confRecord

	durable map[uint64]*durable
	prevHS  map[uint64]*raftpb.HardState // per raft ID, the current incarnation's last HardState
}

// New returns an oracle for a cluster bootstrapped with boot (index 1, term 1).
func New(boot *raftpb.Snapshot) *Oracle {
	o := &Oracle{
		violations:   map[string]error{},
		leaders:      map[uint64]leaderSeen{},
		roles:        map[uint64]role{},
		ref:          kv.New(),
		firstApplied: map[opKey]uint64{},
		confAt:       map[uint64]confRecord{},
		durable:      map[uint64]*durable{},
		prevHS:       map[uint64]*raftpb.HardState{},
	}
	md := boot.GetMetadata()
	o.canon = []canonEntry{{term: md.GetTerm(), typ: raftpb.EntryNormal, dataHash: fnv64(boot.GetData()), node: "bootstrap"}}
	o.refHash = []uint64{o.ref.Hash()}
	o.confIdx = []uint64{md.GetIndex()}
	o.confAt[md.GetIndex()] = confRecord{cs: clone(md.GetConfState()), node: "bootstrap"}
	return o
}

func fnv64(b []byte) uint64 {
	h := fnv.New64a()
	h.Write(b)
	return h.Sum64()
}

// clone returns a deep copy of m. proto.Clone returns a message of m's own type, so the
// assertion cannot fail.
func clone[M proto.Message](m M) M {
	c, _ := proto.Clone(m).(M)
	return c
}

// confBytes is the comparison key of a configuration: wal.Marshal of a copy whose
// AutoLeave is explicitly set, because raft sets it on every ConfState it builds
// while a bootstrap ConfState may leave it unset.
func confBytes(cs *raftpb.ConfState) []byte {
	c := raftpb.EnsureConfState(clone(cs))
	return wal.Marshal(c)
}

func describeConf(cs *raftpb.ConfState) string {
	if cs == nil {
		return "none"
	}
	return raft.DescribeConfState(cs)
}

func describeHS(hs *raftpb.HardState) string {
	if hs == nil {
		return "none"
	}
	return raft.DescribeHardState(hs)
}

func (o *Oracle) violate(check string, err error) {
	if _, ok := o.violations[check]; ok {
		return
	}
	o.violations[check] = err
	for _, fn := range o.onViolation {
		fn(check, err)
	}
}

// Err returns the first violation recorded for check, or nil.
func (o *Oracle) Err(check string) error { return o.violations[check] }

// OnViolation registers fn; it is called once per check, at its first violation.
func (o *Oracle) OnViolation(fn func(check string, err error)) {
	o.onViolation = append(o.onViolation, fn)
}

// Role records a server's state after an event (ETC-110) and reports whether its
// (Lead, RaftState) changed since the previous call for id.
func (o *Oracle) Role(at kernel.Time, node string, id, term uint64, st raft.StateType, lead uint64) bool {
	if st == raft.StateLeader {
		if prev, ok := o.leaders[term]; !ok {
			o.leaders[term] = leaderSeen{node: node, at: at}
		} else if prev.node != node {
			o.violate(ElectionSafety, fmt.Errorf("term %d has two leaders: %s (since %s) and %s (at %s)", term, prev.node, prev.at, node, at))
		}
	}
	cur := role{lead: lead, state: st}
	prev, ok := o.roles[id]
	o.roles[id] = cur
	return !ok || prev != cur
}

// LeaderIsVoter checks ETC-116.
func (o *Oracle) LeaderIsVoter(at kernel.Time, node string, id, term uint64, isVoter bool, conf string) {
	if !isVoter {
		o.violate(LeaderIsVoter, fmt.Errorf("%s is leader of term %d but not a voter in %s", node, term, conf))
	}
}

// Apply records that server id applied e (ETC-111).
func (o *Oracle) Apply(at kernel.Time, node string, id uint64, e *raftpb.Entry) {
	i, end := e.GetIndex(), uint64(len(o.canon))
	ce := canonEntry{term: e.GetTerm(), typ: e.GetType(), dataHash: fnv64(e.GetData()), node: node}
	switch {
	case i == end+1:
		o.canon = append(o.canon, ce)
		o.applyRef(i, e)
		o.refHash = append(o.refHash, o.ref.Hash())
	case i >= 1 && i <= end:
		c := o.canon[i-1]
		if c.term != ce.term || c.typ != ce.typ || c.dataHash != ce.dataHash {
			o.violate(StateMachineSafety, fmt.Errorf("index %d: %s applied term=%d type=%s data=%016x but %s applied term=%d type=%s data=%016x",
				i, c.node, c.term, c.typ, c.dataHash, node, ce.term, ce.typ, ce.dataHash))
		}
	default:
		o.violate(StateMachineSafety, fmt.Errorf("%s applied index %d but the canonical log ends at %d", node, i, end))
	}
}

// applyRef applies the entry at canonical index i to the reference state exactly as a
// server does (ETC-067). Undecodable data is skipped: the server reports it as a
// harness error.
func (o *Oracle) applyRef(i uint64, e *raftpb.Entry) {
	switch e.GetType() {
	case raftpb.EntryNormal:
		if len(e.GetData()) == 0 {
			return
		}
		cmd, err := kv.DecodeCommand(e.GetData())
		if err != nil {
			return
		}
		if _, dup := o.ref.Apply(cmd); !dup {
			o.firstApplied[opKey{cmd.Client, cmd.ID}] = i
		}
	case raftpb.EntryConfChangeV2:
		o.confChanges++
		cc := new(raftpb.ConfChangeV2)
		if proto.Unmarshal(e.GetData(), cc) != nil || len(cc.GetContext()) == 0 {
			return
		}
		client, id, err := kv.DecodeContext(cc.GetContext())
		if err != nil {
			return
		}
		if !o.ref.ApplySession(client, id, kv.OpConfChange) {
			o.firstApplied[opKey{client, id}] = i
		}
	case raftpb.EntryConfChange:
		o.confChanges++
	}
}

// ConfApplied records the ConfState that ApplyConfChange returned at index (ETC-115).
func (o *Oracle) ConfApplied(at kernel.Time, node string, index uint64, cs *raftpb.ConfState) {
	if prev, ok := o.confAt[index]; ok {
		if !bytes.Equal(confBytes(prev.cs), confBytes(cs)) {
			o.violate(ConfigAgreement, fmt.Errorf("index %d: %s applied configuration %s but %s got %s",
				index, prev.node, describeConf(prev.cs), node, describeConf(cs)))
		}
		return
	}
	o.confAt[index] = confRecord{cs: clone(cs), node: node}
	k, _ := slices.BinarySearch(o.confIdx, index)
	o.confIdx = slices.Insert(o.confIdx, k, index)
}

// SnapshotState checks a snapshot's data hash and ConfState (ETC-111). how is
// "create", "install", or "recover".
func (o *Oracle) SnapshotState(at kernel.Time, node, how string, index, dataHash uint64, cs *raftpb.ConfState) {
	h, ok := o.RefHash(index)
	switch {
	case !ok:
		o.violate(StateMachineSafety, fmt.Errorf("%s snapshot at index %d on %s has state hash %016x, reference has no state at index %d", how, index, node, dataHash, index))
		return
	case h != dataHash:
		o.violate(StateMachineSafety, fmt.Errorf("%s snapshot at index %d on %s has state hash %016x, reference has %016x", how, index, node, dataHash, h))
		return
	}
	if want := o.ConfAt(index); !bytes.Equal(confBytes(want), confBytes(cs)) {
		o.violate(StateMachineSafety, fmt.Errorf("%s snapshot at index %d on %s has configuration %s, canonical configuration is %s", how, index, node, describeConf(cs), describeConf(want)))
	}
}

func (o *Oracle) view(id uint64) *durable {
	d, ok := o.durable[id]
	if !ok {
		d = &durable{}
		o.durable[id] = d
	}
	return d
}

// Persisted records a synced persist point of server id (ETC-112).
func (o *Oracle) Persisted(id uint64, hs *raftpb.HardState, walEnd int64, snapIndex uint64) {
	d := o.view(id)
	if !raft.IsEmptyHardState(hs) {
		d.hs = clone(hs)
	}
	d.walEnd = walEnd
	d.snapIndex = max(d.snapIndex, snapIndex)
}

// Recovery describes what a server recovered at boot.
type Recovery struct {
	Fresh     bool
	ValidEnd  int64
	HardState *raftpb.HardState // as read, before the commit fix-up (ETC-034); nil if none
	SnapIndex uint64
}

// Recovered checks a boot's recovery against D (ETC-112) and then sets D.walEnd.
func (o *Oracle) Recovered(at kernel.Time, node string, id uint64, r Recovery) {
	d := o.view(id)
	switch {
	case r.ValidEnd < d.walEnd:
		o.violate(DurableState, fmt.Errorf("%s recovered %d WAL bytes but %d were persisted", node, r.ValidEnd, d.walEnd))
	case r.Fresh && d.walEnd != 0:
		o.violate(DurableState, fmt.Errorf("%s booted with an empty WAL after persisting %d bytes", node, d.walEnd))
	case d.hs != nil && olderHS(r.HardState, d.hs):
		o.violate(DurableState, fmt.Errorf("%s recovered HardState %s older than persisted %s", node, describeHS(r.HardState), describeHS(d.hs)))
	case r.SnapIndex < d.snapIndex:
		o.violate(DurableState, fmt.Errorf("%s recovered snapshot index %d below persisted snapshot index %d", node, r.SnapIndex, d.snapIndex))
	}
	d.walEnd = r.ValidEnd
}

// olderHS reports whether got is missing or older than want (ETC-112 rule 3).
func olderHS(got, want *raftpb.HardState) bool {
	switch {
	case got == nil:
		return true
	case got.GetTerm() > want.GetTerm():
		return false
	case got.GetTerm() < want.GetTerm():
		return true
	case want.GetVote() != 0 && got.GetVote() != want.GetVote():
		return true
	}
	return got.GetCommit() < want.GetCommit()
}

// HardState checks ETC-113. baseline sets the previous value without checking.
func (o *Oracle) HardState(at kernel.Time, node string, id uint64, hs *raftpb.HardState, baseline bool) {
	cur := &raftpb.HardState{}
	if hs != nil {
		cur = clone(hs)
	}
	prev, ok := o.prevHS[id]
	o.prevHS[id] = cur
	if baseline || !ok {
		return
	}
	bad := cur.GetTerm() < prev.GetTerm() ||
		(cur.GetTerm() == prev.GetTerm() && prev.GetVote() != 0 && cur.GetVote() != prev.GetVote()) ||
		cur.GetCommit() < prev.GetCommit()
	if bad {
		o.violate(HardStateMonotonic, fmt.Errorf("%s HardState went from %s to %s", node, raft.DescribeHardState(prev), raft.DescribeHardState(cur)))
	}
}

// Contract records a ready-contract violation (ETC-114). msg is "rule <k>: <detail>".
func (o *Oracle) Contract(at kernel.Time, node, msg string) {
	o.violate(ReadyContract, fmt.Errorf("%s ready contract: %s", node, msg))
}

// HarnessError records a harness error (ETC-117).
func (o *Oracle) HarnessError(at kernel.Time, node string, err error) {
	o.violate(Harness, fmt.Errorf("%s: %w", node, err))
}

// LastIndex returns the canonical log end.
func (o *Oracle) LastIndex() uint64 { return uint64(len(o.canon)) }

// RefHash returns the reference state hash after index.
func (o *Oracle) RefHash(index uint64) (uint64, bool) {
	if index < 1 || index > uint64(len(o.refHash)) {
		return 0, false
	}
	return o.refHash[index-1], true
}

// FirstApplied returns the canonical index of the first non-duplicate application of
// (client, id).
func (o *Oracle) FirstApplied(client uint32, id uint64) (index uint64, ok bool) {
	index, ok = o.firstApplied[opKey{client, id}]
	return index, ok
}

// Conf returns the latest canonical configuration.
func (o *Oracle) Conf() *raftpb.ConfState { return o.confAt[o.confIdx[len(o.confIdx)-1]].cs }

// ConfAt returns the configuration in effect after index.
func (o *Oracle) ConfAt(index uint64) *raftpb.ConfState {
	k, found := slices.BinarySearch(o.confIdx, index)
	if !found {
		k--
	}
	if k < 0 {
		return nil
	}
	return o.confAt[o.confIdx[k]].cs
}

// Terms returns the number of distinct terms with an observed leader.
func (o *Oracle) Terms() int { return len(o.leaders) }

// ConfChanges returns the number of canonical indices holding conf-change entries.
func (o *Oracle) ConfChanges() int { return o.confChanges }
