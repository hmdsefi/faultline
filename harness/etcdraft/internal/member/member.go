// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package member chooses the membership changes of the harness's admin client.
package member

import (
	"math/rand/v2"
	"slices"

	"go.etcd.io/raft/v3/raftpb"
)

// Kind is a membership change kind.
type Kind uint8

const (
	AddLearner Kind = iota + 1 // l<spare>
	Promote                    // v<learner>
	Remove                     // r<voter>
	Demote                     // l<voter>
	Replace                    // v<learner> r<voter>, joint consensus with auto-leave
)

const (
	MinVoters = 3
	MaxVoters = 5
)

// View is the admin's view of the configuration.
type View struct {
	Voters   []uint64 // incoming voters, ascending
	Learners []uint64 // ascending
	Joint    bool     // VotersOutgoing is non-empty
	Unused   []uint64 // spare IDs never seen in any configuration, ascending
}

// Change is a chosen change.
type Change struct {
	Kind   Kind
	Target uint64               // learner for Promote and Replace; the node for the others
	Remove uint64               // Replace only: the voter removed
	CC     *raftpb.ConfChangeV2 // Transition Auto, Context unset
}

// Choose draws a change valid for v from r (ETC-172), or returns false.
func Choose(r *rand.Rand, v View) (Change, bool) {
	if v.Joint {
		return Change{}, false
	}
	var valid []Kind
	if len(v.Unused) > 0 && len(v.Learners) < 2 {
		valid = append(valid, AddLearner)
	}
	if len(v.Learners) > 0 && len(v.Voters) < MaxVoters {
		valid = append(valid, Promote)
	}
	if len(v.Voters) > MinVoters {
		valid = append(valid, Remove, Demote)
	}
	if len(v.Learners) > 0 && len(v.Voters) >= MinVoters {
		valid = append(valid, Replace)
	}
	if len(valid) == 0 {
		return Change{}, false
	}
	ch := Change{Kind: valid[r.IntN(len(valid))]}
	switch ch.Kind {
	case AddLearner:
		ch.Target = v.Unused[r.IntN(len(v.Unused))]
		ch.CC = cc(single(raftpb.ConfChangeAddLearnerNode, ch.Target))
	case Promote:
		ch.Target = v.Learners[r.IntN(len(v.Learners))]
		ch.CC = cc(single(raftpb.ConfChangeAddNode, ch.Target))
	case Remove:
		ch.Target = v.Voters[r.IntN(len(v.Voters))]
		ch.CC = cc(single(raftpb.ConfChangeRemoveNode, ch.Target))
	case Demote:
		ch.Target = v.Voters[r.IntN(len(v.Voters))]
		ch.CC = cc(single(raftpb.ConfChangeAddLearnerNode, ch.Target))
	case Replace:
		ch.Target = v.Learners[r.IntN(len(v.Learners))]
		ch.Remove = v.Voters[r.IntN(len(v.Voters))]
		ch.CC = cc(single(raftpb.ConfChangeAddNode, ch.Target), single(raftpb.ConfChangeRemoveNode, ch.Remove))
	}
	return ch, true
}

func single(t raftpb.ConfChangeType, id uint64) *raftpb.ConfChangeSingle {
	return &raftpb.ConfChangeSingle{Type: t.Enum(), NodeId: new(id)}
}

func cc(changes ...*raftpb.ConfChangeSingle) *raftpb.ConfChangeV2 {
	return &raftpb.ConfChangeV2{Transition: raftpb.ConfChangeTransitionAuto.Enum(), Changes: changes}
}

// ViewOf builds a View from cs and the spare IDs not in seen.
func ViewOf(cs *raftpb.ConfState, spares []uint64, seen func(id uint64) bool) View {
	v := View{
		Voters:   slices.Sorted(slices.Values(cs.GetVoters())),
		Learners: slices.Sorted(slices.Values(cs.GetLearners())),
		Joint:    len(cs.GetVotersOutgoing()) > 0,
	}
	for _, id := range slices.Sorted(slices.Values(spares)) {
		if !seen(id) {
			v.Unused = append(v.Unused, id)
		}
	}
	return v
}
