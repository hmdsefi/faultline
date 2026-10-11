// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package etcdraft

import (
	"slices"
	"strconv"

	"go.etcd.io/raft/v3"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/check/history"
	"github.com/hmdsefi/faultline/kernel"

	"github.com/hmdsefi/faultline/harness/etcdraft/internal/oracle"
)

// Cluster is the harness's view of one simulated cluster. Its methods are read-only
// and may be called from invariants, final checks, roles, planners, and scripted steps.
type Cluster struct {
	w   *faultline.World
	cfg Config
	o   *oracle.Oracle

	servers      []*server                 // index raft ID - 1: initial voters, then spares
	byNode       map[kernel.NodeID]*server // accessed by key only
	clients      []*client                 // c1..cK
	probe        *client
	admin        *client                   // nil unless Membership.Enabled
	clientByNode map[kernel.NodeID]*client // accessed by key only

	stats         Stats // counters; history and oracle fields are filled by Stats()
	leaderChanges []LeaderChange
	drainStart    kernel.Time      // End − Drain
	recoveryStart kernel.Time      // R of ETC-096
	probeOp       uint64           // client op ID of the probe write; 0 until invoked
	probeAcked    bool             // the probe write completed OK
	probeAckedAt  kernel.Time      // when it did
	opIDs         map[int64]uint64 // history op ID -> client op ID, accessed by key only
	seen          map[uint64]bool  // ETC-173 raft IDs the admin has seen, accessed by key only
}

// KVInput is the check/history input of "read" ({"key": k}) and "write" ({"key": k, "value": v})
// operations. Write values are never empty.
type KVInput struct {
	Key   string `json:"key"`
	Value string `json:"value,omitempty"` // writes only
}

// The output of an OK "read" is the value read as a Go string (a JSON string), or nil (JSON null)
// when the key is absent; the output of an OK "write" is nil (ETC-095).

// LeaderChange is one observed change of a server's SoftState (lead or raft state).
type LeaderChange struct {
	At    kernel.Time
	Node  uint64 // raft ID
	Term  uint64
	State raft.StateType
	Lead  uint64
}

// Stats are run counters for tests and reports.
type Stats struct {
	OpsOK, OpsInfo, OpsPending int    // history operations of all clients including probe and admin
	Terms                      int    // distinct terms with an observed leader
	Readies, Syncs             uint64 // Readys handled; File.Sync calls that returned nil
	StepsDuringPersist         uint64 // input events handled while a Ready was in flight
	Boots, Recoveries          int    // server boots; boots that recovered a non-empty WAL
	IOCrashes                  int    // servers crashed by the harness on a WAL error
	SnapshotsCreated           int    // local snapshots persisted
	SnapshotsInstalled         int    // snapshots applied from rd.Snapshot
	SnapshotsSent              int    // MsgSnap messages sent
	SnapshotReportsFinish      int    // ReportSnapshot(SnapshotFinish) calls
	SnapshotReportsFailure     int    // ReportSnapshot(SnapshotFailure) calls
	ConfChangesApplied         int    // canonical-log indices holding conf-change entries
	ProbeAcked                 bool
	ProbeIndex                 uint64 // canonical index of the probe write; 0 if not applied
	LastIndex                  uint64 // canonical log end
}

// Config returns the effective configuration (defaults applied).
func (c *Cluster) Config() Config { return c.cfg }

// Servers returns all servers in raft-ID order (initial voters, then spares).
func (c *Cluster) Servers() []*kernel.Node {
	out := make([]*kernel.Node, len(c.servers))
	for i, s := range c.servers {
		out[i] = s.node
	}
	return out
}

// Clients returns the workload clients c1..cK in order (not probe or admin).
func (c *Cluster) Clients() []*kernel.Node {
	out := make([]*kernel.Node, len(c.clients))
	for i, cl := range c.clients {
		out[i] = cl.node
	}
	return out
}

// RaftID returns n's raft ID, or 0 if n is not a server.
func (c *Cluster) RaftID(n *kernel.Node) uint64 {
	if n == nil {
		return 0
	}
	if s, ok := c.byNode[n.ID()]; ok && s.node == n {
		return s.id
	}
	return 0
}

// server returns the server with raft ID id, or nil.
func (c *Cluster) server(id uint64) *server {
	if id < 1 || id > uint64(len(c.servers)) {
		return nil
	}
	return c.servers[id-1]
}

// Server returns the server with raft ID id, or nil.
func (c *Cluster) Server(id uint64) *kernel.Node {
	if s := c.server(id); s != nil {
		return s.node
	}
	return nil
}

// Leader returns the raft ID and term of the up or paused server whose last observed
// state is StateLeader with the highest term (lowest raft ID on a tie), or 0, 0.
func (c *Cluster) Leader() (id, term uint64) {
	for _, s := range c.servers {
		if s.node.State() == kernel.NodeDown || s.lastState != raft.StateLeader {
			continue
		}
		if id == 0 || s.lastTerm > term {
			id, term = s.id, s.lastTerm
		}
	}
	return id, term
}

// Status returns the BasicStatus of server id's current incarnation; false if the
// server is down or unknown.
func (c *Cluster) Status(id uint64) (raft.BasicStatus, bool) {
	s := c.server(id)
	if s == nil || s.inc == nil || s.node.State() == kernel.NodeDown {
		return raft.BasicStatus{}, false
	}
	return s.inc.rn.BasicStatus(), true
}

// Applied returns the index of the last entry server id applied to its KV state in
// its current incarnation; false if the server is down or unknown.
func (c *Cluster) Applied(id uint64) (uint64, bool) {
	s := c.server(id)
	if s == nil || s.inc == nil || s.node.State() == kernel.NodeDown {
		return 0, false
	}
	return s.inc.applied, true
}

// LeaderChanges returns every observed SoftState change, in observation order.
func (c *Cluster) LeaderChanges() []LeaderChange { return slices.Clone(c.leaderChanges) }

// Stats returns the counters of the run so far.
func (c *Cluster) Stats() Stats {
	st := c.stats
	for _, op := range c.w.History.Ops() {
		switch op.Status {
		case history.OK:
			st.OpsOK++
		case history.Info:
			st.OpsInfo++
		case history.Pending:
			st.OpsPending++
		}
	}
	st.Terms = c.o.Terms()
	st.ConfChangesApplied = c.o.ConfChanges()
	st.LastIndex = c.o.LastIndex()
	st.ProbeAcked = c.probeAcked
	if c.probeOp != 0 {
		st.ProbeIndex, _ = c.o.FirstApplied(c.probe.cid, c.probeOp)
	}
	return st
}

// clientName returns the node name of client ID cid for record texts.
func (c *Cluster) clientName(cid uint32) string {
	switch {
	case cid >= 1 && int(cid) <= len(c.clients):
		return c.clients[cid-1].name
	case c.probe != nil && cid == c.probe.cid:
		return c.probe.name
	case c.admin != nil && cid == c.admin.cid:
		return c.admin.name
	}
	return "client" + strconv.FormatUint(uint64(cid), 10)
}

// clientByName returns the client (workload, probe or admin) named name, or nil.
func (c *Cluster) clientByName(name string) *client {
	n := c.w.Sim.Lookup(name)
	if n == nil {
		return nil
	}
	return c.clientByNode[n.ID()]
}

// finalVoters returns the raft IDs in Voters or VotersOutgoing of the latest canonical
// configuration, ascending.
func (c *Cluster) finalVoters() []uint64 {
	cs := c.o.Conf()
	ids := append(slices.Clone(cs.GetVoters()), cs.GetVotersOutgoing()...)
	slices.Sort(ids)
	return slices.Compact(ids)
}

// nodeIDs maps raft IDs to kernel node IDs, ascending.
func (c *Cluster) nodeIDs(raftIDs []uint64) []kernel.NodeID {
	var out []kernel.NodeID
	for _, id := range raftIDs {
		if s := c.server(id); s != nil {
			out = append(out, s.node.ID())
		}
	}
	slices.Sort(out)
	return out
}

// registerRoles registers the ETC-140 roles.
func (c *Cluster) registerRoles() {
	c.w.Role("leader", func() []kernel.NodeID {
		if id, _ := c.Leader(); id != 0 {
			return []kernel.NodeID{c.server(id).node.ID()}
		}
		return nil
	})
	c.w.Role("followers", func() []kernel.NodeID {
		voters := c.finalVoters()
		var ids []uint64
		for _, s := range c.servers {
			if s.node.State() != kernel.NodeDown && s.lastState == raft.StateFollower && slices.Contains(voters, s.id) {
				ids = append(ids, s.id)
			}
		}
		return c.nodeIDs(ids)
	})
	c.w.Role("voters", func() []kernel.NodeID { return c.nodeIDs(c.finalVoters()) })
	c.w.Role("learners", func() []kernel.NodeID {
		return c.nodeIDs(slices.Sorted(slices.Values(c.o.Conf().GetLearners())))
	})
}

func attr(k, v string) kernel.Attr { return kernel.Attr{Key: k, Value: v} }

// emit emits a harness record for n (nil: a global record).
func (c *Cluster) emit(n *kernel.Node, kind, text string, attrs ...kernel.Attr) {
	r := kernel.Record{Kind: kind, Text: text, Attrs: attrs}
	if n != nil {
		r.Node = n.ID()
	}
	c.w.Sim.Emit(r)
}

// harnessError records err for node n (ETC-117).
func (c *Cluster) harnessError(n *kernel.Node, err error) {
	c.emit(n, "etcdraft.harness_error", "harness error: "+err.Error(), attr("err", err.Error()))
	c.o.HarnessError(c.w.Sim.Now(), n.Name(), err)
}
