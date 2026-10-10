package etcdraft

import (
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"strconv"

	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simdisk"

	"github.com/hmdsefi/faultline/harness/etcdraft/internal/kv"
	"github.com/hmdsefi/faultline/harness/etcdraft/internal/oracle"
	"github.com/hmdsefi/faultline/harness/etcdraft/internal/wal"
	"github.com/hmdsefi/faultline/harness/etcdraft/internal/wire"
)

const (
	walDir  = "/raft"
	walPath = "/raft/wal"
	// maxReadiesPerInstant is the ETC-063 livelock bound.
	maxReadiesPerInstant = 10_000
)

// server is the harness state of one server node across incarnations.
type server struct {
	c       *Cluster
	node    *kernel.Node
	id      uint64
	initial bool         // an initial voter (bootstraps when fresh)
	inc     *incarnation // nil before the first boot and while down

	lastState raft.StateType // last observed (ETC-054); survives crashes
	lastTerm  uint64
}

// incarnation is the volatile state of one boot of a server (glossary "Incarnation state").
type incarnation struct {
	s   *server
	c   *Cluster
	n   *kernel.Node
	log *raftLogger

	wal *simdisk.File
	ms  *raft.MemoryStorage
	rn  *raft.RawNode
	kv  *kv.State

	applied     uint64            // last index applied to kv (ETC-067)
	raftApplied uint64            // last index raft handed out in CommittedEntries
	snapIndex   uint64            // latest snapshot index (durable view)
	confState   *raftpb.ConfState // configuration after the last applied conf change or snapshot

	waiters map[waiterKey]waiter // ETC-093, accessed by key only

	readyPending bool
	inflight     *raft.Ready       // Ready waiting for its sync event (ETC-061 step 7)
	requiredHS   *raftpb.HardState // HardState of the Ready being persisted; nil if none
	persisted    bool              // the current Ready's persist point has passed (ETC-114 rule 4)
	sentEarly    bool              // the current Ready's messages were sent before its records
	walLast      uint64            // last entry index written to the WAL
	syncedLast   uint64            // walLast at the latest persist point (ETC-114 rule 7)
	unreachable  []uint64          // peers collected by ETC-066 for ETC-065 step 5

	instant          kernel.Time
	readiesAtInstant int
}

// clone returns a deep copy of m. proto.Clone returns a message of m's own type, so the
// assertion cannot fail.
func clone[M proto.Message](m M) M {
	c, _ := proto.Clone(m).(M)
	return c
}

func fnv64(b []byte) uint64 {
	h := fnv.New64a()
	h.Write(b)
	return h.Sum64()
}

func u64(v uint64) string { return strconv.FormatUint(v, 10) }

func boolStr(b bool) string { return strconv.FormatBool(b) }

// describeHS is raft.DescribeHardState, or "" for an empty HardState.
func describeHS(hs *raftpb.HardState) string {
	if raft.IsEmptyHardState(hs) {
		return ""
	}
	return raft.DescribeHardState(hs)
}

// bootSnapshot returns the ETC-033 bootstrap snapshot for initial voters 1..n. AutoLeave
// is set explicitly, as raft sets it on every ConfState it builds.
func bootSnapshot(n int) *raftpb.Snapshot {
	voters := make([]uint64, n)
	for i := range voters {
		voters[i] = uint64(i + 1)
	}
	return &raftpb.Snapshot{
		Metadata: &raftpb.SnapshotMetadata{
			Index:     new(uint64(1)),
			Term:      new(uint64(1)),
			ConfState: &raftpb.ConfState{Voters: voters, AutoLeave: new(false)},
		},
		Data: kv.New().Encode(),
	}
}

func (inc *incarnation) now() kernel.Time { return inc.c.w.Sim.Now() }
func (inc *incarnation) cfg() *Config     { return &inc.c.cfg }

func (inc *incarnation) harnessError(err error) { inc.c.harnessError(inc.n, err) }

// contract records an ETC-114 violation of rule.
func (inc *incarnation) contract(rule int, format string, args ...any) {
	inc.c.o.Contract(inc.now(), inc.n.Name(), fmt.Sprintf("rule %d: ", rule)+fmt.Sprintf(format, args...))
}

// walError handles a WAL error (ETC-037). The caller must return immediately afterwards
// without touching node-scoped APIs.
func (inc *incarnation) walError(op string, err error) {
	c, n := inc.c, inc.n
	c.emit(n, "etcdraft.io_crash", fmt.Sprintf("io crash %s: %v", op, err), attr("op", op), attr("err", err.Error()))
	c.stats.IOCrashes++
	num := n.Incarnation()
	n.Crash()
	c.w.Sim.After(c.cfg.CrashRestartDelay, "etcdraft/io-restart", func() {
		if n.State() == kernel.NodeDown && n.Incarnation() == num {
			n.Restart()
		}
	})
}

// boot is the server's kernel.BootFunc (ETC-030 to ETC-036).
func (s *server) boot(n *kernel.Node) {
	c := s.c
	inc := &incarnation{s: s, c: c, n: n, log: &raftLogger{sim: c.w.Sim, node: n, level: c.cfg.LogLevel},
		waiters: map[waiterKey]waiter{}}
	s.inc = inc
	c.stats.Boots++
	st, ok := inc.openWAL()
	if !ok {
		return
	}
	parsedEnd, parsedSize := st.ValidEnd, st.Size
	fresh := st.Records == 0
	if fresh && st.Size > 0 && !inc.truncate(0) {
		return
	}
	if !fresh && st.ValidEnd < st.Size && !inc.truncate(st.ValidEnd) {
		return
	}
	if !fresh {
		c.stats.Recoveries++
	}
	c.o.Recovered(inc.now(), n.Name(), s.id, oracle.Recovery{Fresh: fresh, ValidEnd: st.ValidEnd, HardState: st.HardState, SnapIndex: st.SnapIndex()})
	if fresh && s.initial {
		boot := bootSnapshot(c.cfg.Nodes)
		hs := &raftpb.HardState{Term: new(uint64(1)), Vote: new(uint64(0)), Commit: new(uint64(1))}
		if !inc.append(wal.RecSnapshotInstall, wal.Marshal(boot)) || !inc.append(wal.RecHardState, wal.Marshal(hs)) {
			return
		}
		if err := inc.wal.Sync(); err != nil {
			inc.walError("sync", err)
			return
		}
		c.stats.Syncs++
		c.o.Persisted(s.id, hs, inc.wal.Size(), 1)
		st = wal.State{Snapshot: boot, HardState: hs}
	}
	if !inc.recover(&st) {
		return
	}
	c.w.Net.Handle(n, inc.onPacket)
	n.After(kernel.Uniform(n.Rand(), 1, c.cfg.TickInterval), "etcdraft/tick", inc.onTick)
	hs := st.HardState
	c.emit(n, "etcdraft.boot",
		fmt.Sprintf("boot fresh=%s snap=%d entries=%d hs=%s", boolStr(fresh), st.SnapIndex(), len(st.Entries), describeHS(hs)),
		attr("raft_id", u64(s.id)), attr("fresh", boolStr(fresh)),
		attr("valid_end", strconv.FormatInt(parsedEnd, 10)), attr("size", strconv.FormatInt(parsedSize, 10)),
		attr("snap_index", u64(st.SnapIndex())), attr("hs_term", u64(hs.GetTerm())), attr("hs_vote", u64(hs.GetVote())),
		attr("hs_commit", u64(hs.GetCommit())), attr("entries", strconv.Itoa(len(st.Entries))))
	inc.afterEvent()
}

// openWAL implements ETC-031. It returns false if the node crashed or stopped booting.
func (inc *incarnation) openWAL() (wal.State, bool) {
	vol := inc.c.w.Disk.Volume(inc.n)
	_, err := vol.Stat(walPath)
	if err != nil && errors.Is(err, simdisk.ErrNotExist) {
		if err := vol.MkdirAll(walDir); err != nil {
			inc.walError("mkdir", err)
			return wal.State{}, false
		}
		if err := vol.SyncDir("/"); err != nil {
			inc.walError("syncdir", err)
			return wal.State{}, false
		}
		f, err := vol.Create(walPath)
		if err != nil {
			inc.walError("create", err)
			return wal.State{}, false
		}
		if err := vol.SyncDir(walDir); err != nil {
			inc.walError("syncdir", err)
			return wal.State{}, false
		}
		inc.wal = f
		return wal.State{}, true
	}
	if err != nil {
		inc.walError("stat", err)
		return wal.State{}, false
	}
	f, err := vol.Open(walPath)
	if err != nil {
		inc.walError("open", err)
		return wal.State{}, false
	}
	inc.wal = f
	buf := make([]byte, f.Size())
	if n, err := f.ReadAt(buf, 0); err != nil && (!errors.Is(err, io.EOF) || n != len(buf)) {
		inc.walError("read", err)
		return wal.State{}, false
	}
	st, err := wal.Read(buf)
	if err != nil {
		inc.harnessError(fmt.Errorf("wal: %w", err))
		inc.n.Crash()
		return wal.State{}, false
	}
	return st, true
}

// truncate shortens the WAL to size and syncs it (ETC-032).
func (inc *incarnation) truncate(size int64) bool {
	if err := inc.wal.Truncate(size); err != nil {
		inc.walError("truncate", err)
		return false
	}
	if err := inc.wal.Sync(); err != nil {
		inc.walError("sync", err)
		return false
	}
	inc.c.stats.Syncs++
	return true
}

// append writes one WAL record (ETC-041). It returns false if a WAL error crashed the node.
func (inc *incarnation) append(typ wal.RecordType, payload []byte) bool {
	if _, err := inc.wal.Append(wal.AppendRecord(nil, typ, payload)); err != nil {
		inc.walError("append", err)
		return false
	}
	return true
}

// stopBoot records a harness error and stops booting (ETC-031 step 3).
func (inc *incarnation) stopBoot(err error) bool {
	inc.harnessError(err)
	inc.n.Crash()
	return false
}

// recover rebuilds storage and creates the RawNode (ETC-034 to ETC-036).
func (inc *incarnation) recover(st *wal.State) bool {
	c, s := inc.c, inc.s
	inc.ms = raft.NewMemoryStorage()
	if st.Snapshot != nil {
		if err := inc.ms.ApplySnapshot(st.Snapshot); err != nil {
			return inc.stopBoot(fmt.Errorf("memory storage apply snapshot: %w", err))
		}
	}
	if err := inc.ms.Append(st.Entries); err != nil {
		return inc.stopBoot(fmt.Errorf("memory storage append: %w", err))
	}
	hs := st.HardState
	if hs != nil {
		hs = clone(hs)
		if hs.GetCommit() < st.SnapIndex() {
			hs.Commit = new(st.SnapIndex())
		}
		if hs.GetCommit() > st.LastIndex() {
			return inc.stopBoot(fmt.Errorf("commit %d beyond last index %d after recovery", hs.GetCommit(), st.LastIndex()))
		}
		if err := inc.ms.SetHardState(hs); err != nil {
			return inc.stopBoot(fmt.Errorf("memory storage set hardstate: %w", err))
		}
		st.HardState = hs
	}
	inc.kv = kv.New()
	inc.confState = &raftpb.ConfState{}
	if st.Snapshot != nil {
		state, err := kv.Decode(st.Snapshot.GetData())
		if err != nil {
			return inc.stopBoot(fmt.Errorf("snapshot decode: %w", err))
		}
		inc.kv = state
		inc.confState = clone(st.Snapshot.GetMetadata().GetConfState())
		c.o.SnapshotState(inc.now(), inc.n.Name(), "recover", st.SnapIndex(), fnv64(st.Snapshot.GetData()), inc.confState)
	}
	inc.applied, inc.raftApplied, inc.snapIndex = st.SnapIndex(), st.SnapIndex(), st.SnapIndex()
	inc.walLast, inc.syncedLast = st.LastIndex(), st.LastIndex()
	c.o.HardState(inc.now(), inc.n.Name(), s.id, hs, true)
	rn, err := raft.NewRawNode(&raft.Config{
		ID:                          s.id,
		ElectionTick:                c.cfg.ElectionTick,
		HeartbeatTick:               c.cfg.HeartbeatTick,
		Storage:                     inc.ms,
		Applied:                     st.SnapIndex(),
		MaxSizePerMsg:               c.cfg.MaxSizePerMsg,
		MaxUncommittedEntriesSize:   c.cfg.MaxUncommittedEntriesSize,
		MaxInflightMsgs:             c.cfg.MaxInflightMsgs,
		CheckQuorum:                 c.cfg.CheckQuorum,
		PreVote:                     c.cfg.PreVote,
		ReadOnlyOption:              raft.ReadOnlySafe,
		Logger:                      inc.log,
		DisableProposalForwarding:   c.cfg.DisableProposalForwarding,
		StepDownOnRemoval:           c.cfg.StepDownOnRemoval,
		AsyncStorageWrites:          false,
		DisableConfChangeValidation: false,
	})
	if err != nil {
		return inc.stopBoot(fmt.Errorf("new raw node: %w", err))
	}
	inc.rn = rn
	return true
}

// ----- input events (ETC-050 to ETC-053) -----

// input counts an input event that runs while a Ready is in flight (ETC-053).
func (inc *incarnation) input() {
	if inc.inflight != nil {
		inc.c.stats.StepsDuringPersist++
	}
}

func (inc *incarnation) onTick() {
	inc.input()
	inc.rn.Tick()
	inc.n.After(inc.cfg().TickInterval, "etcdraft/tick", inc.onTick)
	inc.afterEvent()
}

// onPacket is the server's network handler (ETC-051).
func (inc *incarnation) onPacket(from kernel.NodeID, payload any) {
	inc.input()
	c := inc.c
	sender := c.w.Sim.Node(from).Name()
	p, ok := payload.(*packet)
	if !ok {
		inc.harnessError(fmt.Errorf("unexpected payload type %T from %s", payload, sender))
		return
	}
	switch p.data[0] {
	case wire.TagRaft:
		m, err := wire.DecodeRaft(p.data)
		if err != nil {
			inc.harnessError(fmt.Errorf("decode raft from %s: %w", sender, err))
			return
		}
		if m.GetTo() != inc.s.id {
			inc.harnessError(fmt.Errorf("message to %d delivered to %d", m.GetTo(), inc.s.id))
			return
		}
		if err := inc.rn.Step(m); err != nil {
			if !errors.Is(err, raft.ErrProposalDropped) && !errors.Is(err, raft.ErrStepPeerNotFound) {
				inc.harnessError(fmt.Errorf("step: %w", err))
				return
			}
			c.emit(inc.n, "etcdraft.step_error", fmt.Sprintf("step %s from %d: %v", m.GetType().String(), m.GetFrom(), err),
				attr("err", err.Error()), attr("msg_type", m.GetType().String()), attr("from", u64(m.GetFrom())))
		}
	case wire.TagRequest:
		if !inc.onRequest(from, sender, p) {
			return
		}
	default:
		inc.harnessError(fmt.Errorf("unexpected %s packet from %s", tagName(p.data[0]), sender))
		return
	}
	inc.afterEvent()
}

// ----- after-event step (ETC-054, ETC-055, ETC-060) -----

func (inc *incarnation) afterEvent() {
	inc.observe()
	inc.kick()
}

// observe implements ETC-054.
func (inc *incarnation) observe() {
	c, s := inc.c, inc.s
	st := inc.rn.BasicStatus()
	changed := c.o.Role(inc.now(), inc.n.Name(), s.id, st.GetTerm(), st.RaftState, st.Lead)
	s.lastState, s.lastTerm = st.RaftState, st.GetTerm()
	if !changed {
		return
	}
	c.leaderChanges = append(c.leaderChanges, LeaderChange{At: inc.now(), Node: s.id, Term: st.GetTerm(), State: st.RaftState, Lead: st.Lead})
	c.emit(inc.n, "etcdraft.role", fmt.Sprintf("role %s term=%d lead=%d", st.RaftState, st.GetTerm(), st.Lead),
		attr("term", u64(st.GetTerm())), attr("state", st.RaftState.String()), attr("lead", u64(st.Lead)))
	if st.RaftState == raft.StateLeader && c.cfg.StepDownOnRemoval {
		voters := inc.rn.Status().Config.Voters
		_, in := voters[0][s.id]
		_, out := voters[1][s.id]
		c.o.LeaderIsVoter(inc.now(), inc.n.Name(), s.id, st.GetTerm(), in || out, raft.DescribeConfState(inc.confState))
	}
}

// kick implements ETC-060.
func (inc *incarnation) kick() {
	if inc.inflight != nil || inc.readyPending || !inc.rn.HasReady() {
		return
	}
	inc.readyPending = true
	inc.n.Post("etcdraft/ready", inc.onReady)
}
