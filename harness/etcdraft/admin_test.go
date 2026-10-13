// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package etcdraft

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"go.etcd.io/raft/v3/raftpb"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/check/history"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"

	"github.com/hmdsefi/faultline/harness/etcdraft/internal/member"
	"github.com/hmdsefi/faultline/harness/etcdraft/internal/oracle"
	"github.com/hmdsefi/faultline/harness/etcdraft/internal/wire"
)

// adminConfig is stage 1c with FaultsNone and an admin round about every second.
func adminConfig() Config {
	cfg := DefaultConfig()
	cfg.Faults = FaultsNone
	cfg.Membership = Membership{Enabled: true, Spares: 2, Every: time.Second}
	return cfg
}

// runSeeds runs body for n seeds from base seed 1 under faultline.Run, overriding the
// seed-selection variables a developer may have exported.
func runSeeds(t *testing.T, n int, opts faultline.Options, body func(w *faultline.World)) {
	t.Helper()
	for _, v := range []string{"FAULTLINE_SEED", "FAULTLINE_SEED_LIST", "FAULTLINE_SEEDS", "FAULTLINE_BASE_SEED", "FAULTLINE_EXPLORE", "FAULTLINE_SCHEDULE", "FAULTLINE_RESULTS"} {
		t.Setenv(v, "")
	}
	opts.Seeds, opts.BaseSeed = n, 1
	faultline.Run(t, opts, body)
}

// confOutputIDs checks the history output of an OK conf-read or conf-change against ETC-171's
// ConfState JSON: exactly the five keys, ascending ID arrays written as arrays (never null),
// and at least member.MinVoters incoming voters (ETC-172). It returns the output's raft IDs.
func confOutputIDs(raw json.RawMessage) ([]uint64, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if len(m) != 5 {
		return nil, fmt.Errorf("%d keys, want 5", len(m))
	}
	if string(m["auto_leave"]) != "true" && string(m["auto_leave"]) != "false" {
		return nil, fmt.Errorf("auto_leave is %s", m["auto_leave"])
	}
	var all []uint64
	for _, k := range []string{"voters", "learners", "voters_outgoing", "learners_next"} {
		var ids []uint64
		if !bytes.HasPrefix(m[k], []byte("[")) || json.Unmarshal(m[k], &ids) != nil {
			return nil, fmt.Errorf("%s is %s, want an array", k, m[k])
		}
		if !slices.IsSorted(ids) || len(slices.Compact(slices.Clone(ids))) != len(ids) {
			return nil, fmt.Errorf("%s %v is not ascending", k, ids)
		}
		if k == "voters" && len(ids) < member.MinVoters {
			return nil, fmt.Errorf("voters %v: fewer than %d", ids, member.MinVoters)
		}
		all = append(all, ids...)
	}
	return all, nil
}

// idsOf returns ids sorted, as an empty slice rather than nil.
func idsOf(ids []uint64) []uint64 {
	return append([]uint64{}, slices.Sorted(slices.Values(ids))...)
}

// confChangeProblem checks a conf-change history input against ETC-171.
func confChangeProblem(raw json.RawMessage) error {
	var in confChangeInput
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return err
	}
	kinds := []string{"add-learner", "promote", "remove", "demote", "replace"}
	if !slices.Contains(kinds, in.Kind) || in.Target == 0 || (in.Remove != 0) != (in.Kind == "replace") {
		return fmt.Errorf("input %s: want a kind among %v, a target, and a remove ID exactly for replace", raw, kinds)
	}
	return nil
}

// confChangeAgrees checks that an OK conf change's output is the configuration its input asked
// for (ETC-171): an added or demoted learner is a learner, a promoted one a voter, a removed
// server no incoming voter or learner, and a replace has the learner among the voters and the
// removed ID outside them.
func confChangeAgrees(in, out json.RawMessage) error {
	var ch confChangeInput
	var cs confJSON
	if err := json.Unmarshal(in, &ch); err != nil {
		return err
	}
	if err := json.Unmarshal(out, &cs); err != nil {
		return err
	}
	voter := slices.Contains(cs.Voters, ch.Target)
	var ok bool
	switch ch.Kind {
	case "add-learner", "demote":
		ok = slices.Contains(cs.Learners, ch.Target) || slices.Contains(cs.LearnersNext, ch.Target)
	case "promote":
		ok = voter
	case "remove":
		ok = !voter && !slices.Contains(cs.Learners, ch.Target)
	case "replace":
		ok = voter && !slices.Contains(cs.Voters, ch.Remove)
	default:
		return nil // confChangeProblem reports an unknown kind
	}
	if !ok {
		return fmt.Errorf("input %s does not match output %s", in, out)
	}
	return nil
}

// ETC-171 and ETC-173 over 10 seeds of 30 s: the admin's history shapes and values, its waits,
// the drain, the seen set, and what the seen set protects: no removed server comes back and no
// configuration the admin reads has fewer than member.MinVoters voters.
func TestAdminHistory(t *testing.T) {
	cfg := adminConfig()
	opts := testOptions(t, &cfg, 30*time.Second, false)
	every := cfg.Membership.Every
	var problems []string
	var below, above, changes int // waits shorter and longer than Every; OK conf changes
	runSeeds(t, 10, opts, func(w *faultline.World) {
		c := Setup(w, cfg)
		w.Final("test: admin history", func() error {
			bad := func(format string, args ...any) {
				problems = append(problems, fmt.Sprintf("seed %#x: ", w.Seed())+fmt.Sprintf(format, args...))
			}
			want := map[uint64]bool{} // initial voters and every ID in an OK output (ETC-173)
			for i := 1; i <= cfg.Nodes; i++ {
				want[uint64(i)] = true //nolint:gosec // i <= Nodes <= 5
			}
			maxID := uint64(cfg.Nodes + cfg.Membership.Spares) //nolint:gosec // at most 5 + 4
			known := map[string]bool{}                         // every configuration of the canonical log, as normalized JSON
			left := map[uint64]uint64{}                        // raft ID -> index of the first configuration without it, after one with it
			was := map[uint64]bool{}
			for i := uint64(1); i <= c.o.LastIndex(); i++ {
				cs := c.o.ConfAt(i)
				b, err := json.Marshal(map[string]any{"voters": idsOf(cs.GetVoters()), "learners": idsOf(cs.GetLearners()),
					"voters_outgoing": idsOf(cs.GetVotersOutgoing()), "learners_next": idsOf(cs.GetLearnersNext()), "auto_leave": cs.GetAutoLeave()})
				if err != nil {
					return err
				}
				known[string(b)] = true
				now := map[uint64]bool{}
				for _, ids := range [][]uint64{cs.GetVoters(), cs.GetLearners(), cs.GetVotersOutgoing(), cs.GetLearnersNext()} {
					for _, id := range ids {
						now[id] = true
					}
				}
				for id := uint64(1); id <= maxID; id++ {
					switch {
					case now[id] && left[id] != 0:
						bad("raft ID %d left the configuration at index %d and is back at index %d (ETC-173)", id, left[id], i)
						left[id] = 0
					case was[id] && !now[id]:
						left[id] = i
					}
					was[id] = now[id]
				}
			}
			var prev *history.Op
			for _, op := range w.History.Ops() {
				if op.Process != "admin" {
					continue
				}
				in, _ := op.Input.(json.RawMessage)
				switch {
				case op.Call >= c.drainStart:
					bad("admin op %d (%s) invoked at %s, in the drain from %s", op.ID, op.F, op.Call, c.drainStart)
				case op.F == "conf-read" && string(in) != "null":
					bad("conf-read op %d has input %s, want null", op.ID, in)
				case op.F == "conf-read" && prev != nil:
					gap := op.Call.Sub(prev.Return)
					if gap < every/2 || gap > every+every/2 {
						bad("conf-read op %d starts %v after the admin's previous op ended; want a wait in [%v, %v]", op.ID, gap, every/2, every+every/2)
					}
					if gap < every {
						below++
					} else if gap > every {
						above++
					}
				case op.F == "conf-change":
					if err := confChangeProblem(in); err != nil {
						bad("conf-change op %d: %v", op.ID, err)
					}
					if prev == nil || prev.F != "conf-read" || prev.Status != history.OK || op.Call != prev.Return {
						bad("conf-change op %d does not follow an OK conf-read at once", op.ID)
					}
				case op.F != "conf-read":
					bad("admin op %d has f %q", op.ID, op.F)
				}
				if op.Status == history.OK {
					out, _ := op.Output.(json.RawMessage)
					ids, err := confOutputIDs(out)
					if err != nil {
						bad("%s op %d output %s: %v", op.F, op.ID, out, err)
					}
					var norm map[string]any
					if json.Unmarshal(out, &norm) == nil {
						if b, err := json.Marshal(norm); err != nil || !known[string(b)] {
							bad("%s op %d output %s is no configuration of the canonical log (ETC-171)", op.F, op.ID, out)
						}
					}
					for _, id := range ids {
						want[id] = true
					}
					if op.F == "conf-change" {
						changes++
						if err := confChangeAgrees(in, out); err != nil {
							bad("conf-change op %d: %v", op.ID, err)
						}
					}
				}
				prev = &op
			}
			var seen, wantIDs []uint64
			for id := uint64(1); id <= maxID; id++ {
				if c.seen[id] {
					seen = append(seen, id)
				}
				if want[id] {
					wantIDs = append(wantIDs, id)
				}
			}
			if !slices.Equal(seen, wantIDs) {
				bad("seen set %v, want the initial voters and the IDs of every received ConfState %v (ETC-173)", seen, wantIDs)
			}
			return nil
		})
	})
	for _, p := range problems {
		t.Error(p)
	}
	if below == 0 || above == 0 || changes == 0 {
		t.Errorf("%d admin waits below Every and %d above, %d OK conf changes; want the waits drawn from [Every/2, 3*Every/2] and some changes", below, above, changes)
	}
}

// ETC-171 steps 1 and 3: once the drain has started the admin starts no operation, neither a
// round nor a conf change for a configuration where one is valid.
func TestAdminStopsAtDrain(t *testing.T) {
	cfg := adminConfig()
	opts := testOptions(t, &cfg, 20*time.Second, false)
	var problems []string
	runSeed(t, 1, opts, func(w *faultline.World) {
		c := Setup(w, cfg)
		w.Sim.At(c.drainStart, "test/admin-in-drain", func() {
			ops, id := len(w.History.Ops()), c.admin.nextID
			c.admin.adminRound()
			c.admin.adminChange(&raftpb.ConfState{Voters: []uint64{1, 2, 3}, Learners: []uint64{4}}) // Promote is valid
			if n := len(w.History.Ops()); n != ops || c.admin.nextID != id {
				problems = append(problems, fmt.Sprintf("in the drain: %d new history ops, last op ID %d -> %d; want none", n-ops, id, c.admin.nextID))
			}
		})
	})
	for _, p := range problems {
		t.Error(p)
	}
}

// ETC-171 step 2 and ETC-097: an admin operation that times out completes as Info and the admin
// goes back to waiting, so after an isolation longer than OpTimeout the rounds resume.
func TestAdminResumesAfterInfo(t *testing.T) {
	cfg := adminConfig()
	opts := testOptions(t, &cfg, 30*time.Second, false)
	heal := kernel.Time(15 * time.Second)
	var infos, after int
	runSeed(t, 1, opts, func(w *faultline.World) {
		Setup(w, cfg)
		w.Plan(fault.Script(
			fault.Event{At: kernel.Time(5 * time.Second), Kind: fault.KindIsolate, Node: "admin"},
			fault.Event{At: heal, Kind: fault.KindHeal},
		))
		w.Final("test: admin ops", func() error {
			infos, after = 0, 0
			for _, op := range w.History.Ops() {
				switch {
				case op.Process != "admin":
				case op.Status == history.Info:
					infos++
				case op.Call > heal && op.Status == history.OK:
					after++
				}
			}
			return nil
		})
	})
	if infos == 0 || after == 0 {
		t.Fatalf("%d admin ops completed as Info and %d OK ones started after the heal at %s; want both", infos, after, heal)
	}
}

// ETC-090, §8: the admin is client K+2 and its applied operations name it; the setup record
// counts the spares.
func TestAdminRecords(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Faults = FaultsNone
	cfg.Membership.Enabled = true
	opts := testOptions(t, &cfg, 30*time.Second, true)
	var setup []kernel.Record
	var reads, changes int
	var problems []string
	runSeed(t, 1, opts, func(w *faultline.World) {
		Setup(w, cfg)
		w.Final("test: records", func() error {
			setup, reads, changes, problems = recordsOf(w, "etcdraft.setup"), 0, 0, nil
			for _, r := range recordsOf(w, "etcdraft.apply") {
				op := attrOf(r, "op")
				if op != "conf-read" && op != "conf-change" {
					continue
				}
				if op == "conf-read" {
					reads++
				} else {
					changes++
				}
				want := fmt.Sprintf("apply %s %s  admin#%s", attrOf(r, "index"), op, attrOf(r, "op_id"))
				if r.Text != want || attrOf(r, "client") != "5" {
					problems = append(problems, fmt.Sprintf("%q with client=%s, want %q with client=5", r.Text, attrOf(r, "client"), want))
				}
			}
			return nil
		})
	})
	if len(setup) != 1 || setup[0].Text != "setup nodes=3 clients=3 spares=2" || attrOf(setup[0], "spares") != "2" || attrOf(setup[0], "membership") != "true" {
		t.Errorf("setup records %+v; want one with text %q, spares=2 and membership=true", setup, "setup nodes=3 clients=3 spares=2")
	}
	if reads == 0 || changes == 0 {
		t.Errorf("%d conf-read and %d conf-change apply records; want both", reads, changes)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// ETC-121: an undecodable ConfState reply is a harness error with a fixed text; protobuf's
// error message is not in it.
func TestConfStateDecodeErrorText(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Faults = FaultsNone
	cfg.Membership.Enabled = true
	craftedRun(t, cfg, func(c *Cluster, _ *incarnation) (string, string) {
		if cs, out := c.admin.confReply(wire.Reply{Data: []byte{0xff}}); cs != nil || out != nil {
			return oracle.Harness, "confReply returned a result for an undecodable reply"
		}
		return oracle.Harness, "admin: conf state decode: undecodable reply"
	})
}
