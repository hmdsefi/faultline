// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package etcdraft

import (
	"math"
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

// confReply decodes the ConfState of a conf-read or conf-change reply, adds its raft IDs to the
// seen set, and returns it with its history output. An undecodable ConfState is a harness error
// with a fixed text (decodeError, ETC-121: protobuf's message is only the wrapped cause), and
// both results are then nil.
func (cl *client) confReply(rep wire.Reply) (*raftpb.ConfState, any) {
	cs := new(raftpb.ConfState)
	if err := proto.Unmarshal(rep.Data, cs); err != nil {
		cl.c.harnessError(cl.node, &decodeError{text: "conf state decode: undecodable reply", cause: err})
		return nil, nil
	}
	cl.c.see(cs)
	return cs, confOutput(cs)
}

// see records every raft ID of cs in the admin's seen set (ETC-173).
func (c *Cluster) see(cs *raftpb.ConfState) {
	for _, ids := range [][]uint64{cs.GetVoters(), cs.GetLearners(), cs.GetVotersOutgoing(), cs.GetLearnersNext()} {
		for _, id := range ids {
			c.seen[id] = true
		}
	}
}

// adminWait is ETC-171 step 1. The upper bound saturates instead of overflowing, so a very long
// Membership.Every only delays the next round.
func (cl *client) adminWait() {
	every := cl.c.cfg.Membership.Every
	hi := every + min(every/2, math.MaxInt64-every)
	cl.node.After(kernel.Uniform(cl.node.Rand(), every/2, hi), "etcdraft/admin", cl.adminRound)
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
		output: func(rep wire.Reply) (out any) {
			cs, out = cl.confReply(rep)
			return out
		},
		done: func(wire.Reply) {
			if cs == nil {
				return
			}
			cl.adminChange(cs)
		}})
}

// adminChange is ETC-171 steps 3 and 4: it chooses a change for the configuration cs that the
// conf read returned and proposes it, or waits for the next round when no change is valid.
// It does nothing once the drain has started.
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
			_, out := cl.confReply(rep)
			return out
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
