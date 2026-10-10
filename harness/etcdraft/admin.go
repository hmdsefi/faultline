// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package etcdraft

import (
	"slices"

	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"

	"github.com/hmdsefi/faultline/kernel"

	"github.com/hmdsefi/faultline/harness/etcdraft/internal/kv"
	"github.com/hmdsefi/faultline/harness/etcdraft/internal/member"
	"github.com/hmdsefi/faultline/harness/etcdraft/internal/wal"
	"github.com/hmdsefi/faultline/harness/etcdraft/internal/wire"
)

// confJSON is the history output of "conf-read" and "conf-change" (ETC-171).
type confJSON struct {
	Voters         []uint64 `json:"voters"`
	Learners       []uint64 `json:"learners"`
	VotersOutgoing []uint64 `json:"voters_outgoing"`
	LearnersNext   []uint64 `json:"learners_next"`
	AutoLeave      bool     `json:"auto_leave"`
}

func sortedIDs(ids []uint64) []uint64 {
	out := slices.Sorted(slices.Values(ids))
	if out == nil {
		out = []uint64{}
	}
	return out
}

func confOutput(cs *raftpb.ConfState) confJSON {
	return confJSON{
		Voters:         sortedIDs(cs.GetVoters()),
		Learners:       sortedIDs(cs.GetLearners()),
		VotersOutgoing: sortedIDs(cs.GetVotersOutgoing()),
		LearnersNext:   sortedIDs(cs.GetLearnersNext()),
		AutoLeave:      cs.GetAutoLeave(),
	}
}

// confChangeInput is the history input of "conf-change" (ETC-171).
type confChangeInput struct {
	Kind   string `json:"kind"`
	Target uint64 `json:"target"`
	Remove uint64 `json:"remove"`
}

func kindName(k member.Kind) string {
	switch k {
	case member.AddLearner:
		return "add-learner"
	case member.Promote:
		return "promote"
	case member.Remove:
		return "remove"
	case member.Demote:
		return "demote"
	case member.Replace:
		return "replace"
	}
	return "unknown"
}

// decodeConf decodes a reply's ConfState; an undecodable one is a harness error with a fixed
// text (Task 8a's decodeError: ETC-121, protobuf's message is only the wrapped cause).
func (cl *client) decodeConf(rep wire.Reply) (*raftpb.ConfState, bool) {
	cs := new(raftpb.ConfState)
	if err := proto.Unmarshal(rep.Data, cs); err != nil {
		cl.c.harnessError(cl.node, &decodeError{text: "conf state decode: undecodable reply", cause: err})
		return nil, false
	}
	return cs, true
}

// see records every raft ID of cs in the admin's seen set (ETC-173).
func (c *Cluster) see(cs *raftpb.ConfState) {
	for _, ids := range [][]uint64{cs.GetVoters(), cs.GetLearners(), cs.GetVotersOutgoing(), cs.GetLearnersNext()} {
		for _, id := range ids {
			c.seen[id] = true
		}
	}
}

// adminWait is ETC-171 step 1.
func (cl *client) adminWait() {
	every := cl.c.cfg.Membership.Every
	cl.node.After(kernel.Uniform(cl.node.Rand(), every/2, every+every/2), "etcdraft/admin", cl.adminRound)
}

// adminRound is ETC-171 steps 2 to 4.
func (cl *client) adminRound() {
	c := cl.c
	if cl.now() >= c.drainStart {
		return
	}
	cl.nextID++
	hid := c.w.History.Invoke(cl.name, "conf-read", nil)
	var cs *raftpb.ConfState
	cl.begin(&clientOp{hid: hid, req: wire.Request{Client: cl.cid, ID: cl.nextID, Op: kv.OpConfRead}, timeout: c.cfg.Workload.OpTimeout,
		output: func(rep wire.Reply) any {
			var ok bool
			if cs, ok = cl.decodeConf(rep); !ok {
				return nil
			}
			c.see(cs)
			return confOutput(cs)
		},
		done: func(wire.Reply) {
			if cs == nil {
				return
			}
			cl.adminChange(cs)
		}})
}

func (cl *client) adminChange(cs *raftpb.ConfState) {
	c := cl.c
	if cl.now() >= c.drainStart {
		return
	}
	v := member.ViewOf(cs, c.spareIDs(), func(id uint64) bool { return c.seen[id] })
	ch, ok := member.Choose(cl.node.Rand(), v)
	if !ok {
		cl.adminWait()
		return
	}
	cl.nextID++
	hid := c.w.History.Invoke(cl.name, "conf-change", confChangeInput{Kind: kindName(ch.Kind), Target: ch.Target, Remove: ch.Remove})
	cl.begin(&clientOp{hid: hid, req: wire.Request{Client: cl.cid, ID: cl.nextID, Op: kv.OpConfChange, Data: wal.Marshal(ch.CC)}, timeout: c.cfg.Workload.OpTimeout,
		output: func(rep wire.Reply) any {
			cs, ok := cl.decodeConf(rep)
			if !ok {
				return nil
			}
			c.see(cs)
			return confOutput(cs)
		},
		done: func(wire.Reply) { cl.adminWait() }})
}

// spareIDs returns the raft IDs of the spare servers, ascending.
func (c *Cluster) spareIDs() []uint64 {
	var ids []uint64
	for _, s := range c.servers {
		if !s.initial {
			ids = append(ids, s.id)
		}
	}
	return ids
}
