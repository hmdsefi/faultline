package member

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"go.etcd.io/raft/v3/raftpb"
)

// AT-ETC-23. valid lists the kinds ETC-172 allows for the view, in enum order; the test
// replays ETC-172's draws on a twin stream (same PCG seed) and compares the result.
func TestChoose(t *testing.T) {
	cases := []struct {
		name  string
		view  View
		valid []Kind
	}{
		{"joint", View{Voters: []uint64{1, 2, 3}, Learners: []uint64{4}, Joint: true, Unused: []uint64{5}}, nil},
		{"3 voters, 2 unused", View{Voters: []uint64{1, 2, 3}, Unused: []uint64{4, 5}}, []Kind{AddLearner}},
		{"3 voters, 1 learner, 1 unused", View{Voters: []uint64{1, 2, 3}, Learners: []uint64{4}, Unused: []uint64{5}}, []Kind{AddLearner, Promote, Replace}},
		{"3 voters, 2 learners", View{Voters: []uint64{1, 2, 3}, Learners: []uint64{4, 5}, Unused: []uint64{6}}, []Kind{Promote, Replace}},
		{"4 voters, 1 learner", View{Voters: []uint64{1, 2, 3, 4}, Learners: []uint64{5}}, []Kind{Promote, Remove, Demote, Replace}},
		{"5 voters, no spares", View{Voters: []uint64{1, 2, 3, 4, 5}}, []Kind{Remove, Demote}},
		{"5 voters, 1 learner", View{Voters: []uint64{1, 2, 3, 4, 5}, Learners: []uint64{6}}, []Kind{Remove, Demote, Replace}},
		{"3 voters, no spares", View{Voters: []uint64{1, 2, 3}}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for draw := 0; draw < 20; draw++ {
				r := rand.New(rand.NewPCG(1, 2))    //nolint:gosec // a seeded test stream, not a secret
				twin := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // a seeded test stream, not a secret
				for i := 0; i < draw; i++ {         // advance both streams to vary the draws
					r.Uint64()
					twin.Uint64()
				}
				ch, ok := Choose(r, c.view)
				if len(c.valid) == 0 {
					if ok {
						t.Fatalf("Choose returned %+v, want false", ch)
					}
					if r.Uint64() != twin.Uint64() {
						t.Fatal("Choose drew from the stream although no change is valid")
					}
					return
				}
				want := Change{Kind: c.valid[twin.IntN(len(c.valid))]}
				var changes string
				switch want.Kind {
				case AddLearner:
					want.Target = c.view.Unused[twin.IntN(len(c.view.Unused))]
					changes = fmt.Sprintf("l%d", want.Target)
				case Promote:
					want.Target = c.view.Learners[twin.IntN(len(c.view.Learners))]
					changes = fmt.Sprintf("v%d", want.Target)
				case Remove:
					want.Target = c.view.Voters[twin.IntN(len(c.view.Voters))]
					changes = fmt.Sprintf("r%d", want.Target)
				case Demote:
					want.Target = c.view.Voters[twin.IntN(len(c.view.Voters))]
					changes = fmt.Sprintf("l%d", want.Target)
				case Replace:
					want.Target = c.view.Learners[twin.IntN(len(c.view.Learners))]
					want.Remove = c.view.Voters[twin.IntN(len(c.view.Voters))]
					changes = fmt.Sprintf("v%d r%d", want.Target, want.Remove)
				}
				if !ok || ch.Kind != want.Kind || ch.Target != want.Target || ch.Remove != want.Remove {
					t.Fatalf("draw %d: Choose = %+v, %v; want kind %d target %d remove %d", draw, ch, ok, want.Kind, want.Target, want.Remove)
				}
				if got := raftpb.ConfChangesToString(ch.CC.GetChanges()); got != changes {
					t.Fatalf("draw %d: changes %q, want %q", draw, got, changes)
				}
				if ch.CC.GetTransition() != raftpb.ConfChangeTransitionAuto || len(ch.CC.GetContext()) != 0 {
					t.Fatalf("draw %d: transition %v context %q", draw, ch.CC.GetTransition(), ch.CC.GetContext())
				}
				if r.Uint64() != twin.Uint64() {
					t.Fatalf("draw %d: Choose consumed a different number of draws than ETC-172", draw)
				}
			}
		})
	}
}

func TestViewOf(t *testing.T) {
	cs := &raftpb.ConfState{Voters: []uint64{3, 1, 2}, Learners: []uint64{5}, VotersOutgoing: []uint64{1, 2, 4}}
	seen := map[uint64]bool{1: true, 2: true, 3: true, 4: true, 5: true}
	v := ViewOf(cs, []uint64{7, 4, 5, 6}, func(id uint64) bool { return seen[id] })
	if !slices.Equal(v.Voters, []uint64{1, 2, 3}) || !slices.Equal(v.Learners, []uint64{5}) || !v.Joint || !slices.Equal(v.Unused, []uint64{6, 7}) {
		t.Fatalf("ViewOf = %+v", v)
	}
}
