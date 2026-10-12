// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package etcdraft

import (
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/check/history"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"

	"github.com/hmdsefi/faultline/harness/etcdraft/internal/kv"
	"github.com/hmdsefi/faultline/harness/etcdraft/internal/oracle"
	"github.com/hmdsefi/faultline/harness/etcdraft/internal/wal"
)

// ETC-130 to ETC-133: each final check passes on a healthy run and reports a tampered
// state with its ETC message. The tampering runs in a final check registered after
// Setup's, so it cannot affect the harness's own verdict. It calls the checks directly, so
// selfTestHook hears of nothing: a passing final check does not report (ETC-196).
func TestFinalChecksDetect(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Faults = FaultsNone
	var problems []string
	cfg.selfTestHook = func(_ uint64, check string) { problems = append(problems, "selfTestHook called for "+check) }
	opts := testOptions(t, &cfg, 20*time.Second, false)
	runSeed(t, 1, opts, func(w *faultline.World) {
		c := Setup(w, cfg)
		// expect records a problem unless err matches want: the ETC text in full, with <hex>
		// for a 16-digit hash and <any> for anything. An empty want means the check must pass.
		expect := func(what, want string, err error) {
			re := regexp.MustCompile("^" + strings.NewReplacer("<hex>", "[0-9a-f]{16}", "<any>", ".*").Replace(regexp.QuoteMeta(want)) + "$")
			if (want == "" && err != nil) || (want != "" && (err == nil || !re.MatchString(err.Error()))) {
				problems = append(problems, fmt.Sprintf("%s: err = %v, want %q", what, err, want))
			}
		}
		noErr := func(err error) {
			if err != nil {
				problems = append(problems, "tampering: "+err.Error())
			}
		}
		w.Final("test: tamper", func() error {
			s := c.server
			expect("healthy progress", "", c.checkProgress())
			expect("healthy acked writes", "", c.checkAckedWrites())
			expect("healthy replicas", "", c.checkReplicasAgree())
			expect("healthy WAL", "", c.checkWAL())

			// ETC-130: an acknowledgement at the deadline passes, one a nanosecond later fails.
			deadline := c.recoveryStart.Add(cfg.ProgressWithin)
			c.probeAckedAt = deadline
			expect("progress at the deadline", "", c.checkProgress())
			c.probeAckedAt = deadline.Add(1)
			expect("progress", "probe write acknowledged at 15.000000001s, after the deadline 15.000000000s", c.checkProgress())

			// ETC-131: A is the highest index of an acknowledged write, not the last one's. The
			// last operation of the history is a second acknowledgement of the probe write, whose
			// index is far below the workload's later writes.
			hid := w.History.Invoke("probe", "write", KVInput{Key: "probe", Value: "probe"})
			w.History.Complete(hid, history.OK, nil)
			c.opIDs[hid] = c.probeOp
			var highest uint64
			for _, op := range w.History.Ops() {
				if op.F != "write" || op.Status != history.OK {
					continue
				}
				if idx, ok := c.o.FirstApplied(c.clientByName(op.Process).cid, c.opIDs[op.ID]); ok {
					highest = max(highest, idx)
				}
			}
			applied := s(3).inc.applied
			s(3).inc.applied = highest - 1
			expect("acked writes", fmt.Sprintf("n3 applied up to %d at the end, below acknowledged write index %d", highest-1, highest), c.checkAckedWrites())
			s(3).inc.applied = applied

			// ETC-132: a state beyond the canonical log, another configuration, another state.
			end := c.o.LastIndex()
			s(3).inc.applied = end + 1
			expect("replicas, no state", fmt.Sprintf("n3 at index %d has state hash <hex>, reference has no state at index %d", end+1, end+1), c.checkReplicasAgree())
			s(3).inc.applied = applied
			a2, cs2 := s(2).inc.applied, s(2).inc.confState
			s(2).inc.confState = &raftpb.ConfState{Voters: []uint64{1, 2}}
			expect("replicas, configuration", fmt.Sprintf("n2 at index %d has configuration %s, canonical configuration is %s", a2,
				"Voters:[1 2] VotersOutgoing:[] Learners:[] LearnersNext:[] AutoLeave:false",
				"Voters:[1 2 3] VotersOutgoing:[] Learners:[] LearnersNext:[] AutoLeave:false"), c.checkReplicasAgree())
			s(2).inc.confState = cs2
			s(2).inc.kv.Apply(kv.Command{Client: 99, ID: 1, Op: kv.OpPut, Key: "x", Value: "y"})
			expect("replicas, state hash", fmt.Sprintf("n2 at index %d has state hash <hex>, reference has <hex>", a2), c.checkReplicasAgree())

			// ETC-133 on one server at a time (walAgrees): the in-flight Ready's HardState, an
			// in-flight entry of another term (which must not write into MemoryStorage), the
			// in-flight entry written again as it is (agrees: no HardState keeps memory's), with
			// other data or of another type, the snapshot index, and, through checkWAL, an entry
			// only memory has.
			last, _ := s(1).inc.ms.LastIndex()
			termBefore, _ := s(1).inc.ms.Term(last)
			inflight := s(1).inc.inflight
			s(1).inc.inflight = &raft.Ready{HardState: &raftpb.HardState{Term: new(uint64(99))}}
			err := s(1).inc.walAgrees()
			expect("WAL, HardState", "HardState <any> in the WAL, Term:99 Commit:0 in memory", err)
			s(1).inc.inflight = &raft.Ready{Entries: []*raftpb.Entry{{Index: new(last), Term: new(uint64(99))}}}
			err = s(1).inc.walAgrees()
			expect("WAL, in-flight entry", fmt.Sprintf("entry %d differs: WAL <any>, memory 99/%d <any>", last, last), err)
			if termAfter, _ := s(1).inc.ms.Term(last); termAfter != termBefore {
				problems = append(problems, fmt.Sprintf("walAgrees changed MemoryStorage: term of entry %d was %d, is %d", last, termBefore, termAfter))
			}
			ents, err := s(1).inc.ms.Entries(last, last+1, math.MaxUint64)
			noErr(err)
			same, data, typ := proto.CloneOf(ents[0]), proto.CloneOf(ents[0]), proto.CloneOf(ents[0])
			data.Data = append(slices.Clone(data.GetData()), 'x')
			typ.Type = raftpb.EntryConfChangeV2.Enum()
			if same.GetType() == raftpb.EntryConfChangeV2 {
				typ.Type = raftpb.EntryNormal.Enum()
			}
			s(1).inc.inflight = &raft.Ready{Entries: []*raftpb.Entry{same}}
			expect("WAL, in-flight entry unchanged", "", s(1).inc.walAgrees())
			s(1).inc.inflight = &raft.Ready{Entries: []*raftpb.Entry{data}}
			expect("WAL, in-flight entry data", fmt.Sprintf("entry %d differs: WAL <any>, memory <any>", last), s(1).inc.walAgrees())
			s(1).inc.inflight = &raft.Ready{Entries: []*raftpb.Entry{typ}}
			expect("WAL, in-flight entry type", fmt.Sprintf("entry %d differs: WAL <any>, memory <any>", last), s(1).inc.walAgrees())
			s(1).inc.inflight = inflight
			_, err = s(3).inc.ms.CreateSnapshot(applied, nil, nil)
			noErr(err)
			expect("WAL, snapshot index", fmt.Sprintf("snapshot index 1 in the WAL, %d in memory", applied), s(3).inc.walAgrees())
			noErr(s(1).inc.ms.Append([]*raftpb.Entry{{Index: new(last + 1), Term: new(uint64(99))}}))
			expect("WAL", fmt.Sprintf("n1 WAL and memory disagree: %d entries above 1 in the WAL, %d in memory", last-1, last), c.checkWAL())

			// ETC-131: an acknowledged write that no server applied. Last, because the history keeps it.
			hid = w.History.Invoke("probe", "write", KVInput{Key: "nope", Value: "v"})
			w.History.Complete(hid, history.OK, nil)
			c.opIDs[hid] = 1 << 40
			expect("acked writes, never applied", fmt.Sprintf("write probe#%d (nope=v) was acknowledged but never applied", uint64(1)<<40), c.checkAckedWrites())
			return nil
		})
	})
	for _, p := range problems {
		t.Error(p)
	}
}

// ETC-133 for stage 1b states, each built on its own server and each agreeing: one entry left
// above a local snapshot, an in-flight snapshot install below the end of memory's log (the WAL
// drops every entry written before an install record), and a recovered install whose
// HardState record was lost (the WAL side gets the ETC-034 commit fix-up, as recovery gives
// memory).
func TestWALAgreesStage1b(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Faults = FaultsNone
	opts := testOptions(t, &cfg, 15*time.Second, false)
	var problems []string
	runSeed(t, 1, opts, func(w *faultline.World) {
		c := Setup(w, cfg)
		w.Final("test: stage 1b WAL states", func() error {
			problems = nil
			agrees := func(what string, inc *incarnation, err error) {
				if err == nil {
					err = inc.walAgrees()
				}
				if err != nil {
					problems = append(problems, fmt.Sprintf("%s on %s: %v", what, inc.n.Name(), err))
				}
			}
			appendRecord := func(inc *incarnation, typ wal.RecordType, m proto.Message) error {
				if !inc.append(typ, wal.Marshal(m)) {
					return errors.New("WAL append failed")
				}
				return nil
			}

			inc := c.server(1).inc
			last, _ := inc.ms.LastIndex()
			snap, err := inc.ms.CreateSnapshot(last-1, inc.confState, inc.kv.Encode())
			if err == nil {
				err = inc.ms.Compact(last - 1)
			}
			if err == nil {
				err = appendRecord(inc, wal.RecSnapshotLocal, snap)
			}
			agrees("one entry above a local snapshot", inc, err)

			inc = c.server(2).inc
			last, _ = inc.ms.LastIndex()
			hs, _, _ := inc.ms.InitialState()
			install := snapAt(last-2, inc.confState)
			err = appendRecord(inc, wal.RecSnapshotInstall, install)
			if err == nil {
				err = appendRecord(inc, wal.RecHardState, hs)
			}
			inc.inflight = &raft.Ready{Snapshot: install, HardState: hs}
			agrees("an in-flight install below the last entry", inc, err)

			inc = c.server(3).inc
			last, _ = inc.ms.LastIndex()
			hs, _, _ = inc.ms.InitialState()
			install = snapAt(last+5, inc.confState)
			hs = proto.CloneOf(hs)
			hs.Commit = new(last + 5)
			err = appendRecord(inc, wal.RecSnapshotInstall, install)
			if err == nil {
				err = inc.ms.ApplySnapshot(install)
			}
			if err == nil {
				err = inc.ms.SetHardState(hs)
			}
			agrees("an install whose HardState record was lost", inc, err)
			return nil
		})
	})
	for _, p := range problems {
		t.Error(p)
	}
}

func init() { //nolint:gochecknoinits // registers the child-process scenarios
	// "no-progress": every server crashes for good at 9 s, before the probe write at
	// R = 10 s, so the probe is never acknowledged.
	scenarios["no-progress"] = func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Faults = FaultsNone
		cfg.Duration = 20 * time.Second
		faultline.Run(t, faultline.Options{Seeds: 1, Duration: cfg.Duration}, func(w *faultline.World) {
			Setup(w, cfg)
			var evs []fault.Event
			for _, n := range []string{"n1", "n2", "n3"} {
				evs = append(evs, fault.Event{At: kernel.Time(9 * time.Second), Kind: fault.KindCrash, Node: n})
			}
			w.Plan(fault.Script(evs...))
		})
	}
	// "final-checks-fail": a millisecond before the end, inside the drain where no request is
	// in flight, the state each final check reads is broken: the probe is not acknowledged,
	// n3 has applied too little, n2's state differs, n1's memory has an entry the WAL lacks.
	// selfTestHook appends "<seed hex> <check>" to the file ETCDRAFT_HOOK_OUT.
	scenarios["final-checks-fail"] = func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Faults = FaultsNone
		cfg.Duration = 20 * time.Second
		out := os.Getenv("ETCDRAFT_HOOK_OUT")
		cfg.selfTestHook = func(seed uint64, check string) {
			f, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // out is the parent test's own temp file
			if err == nil {
				_, err = fmt.Fprintf(f, "0x%016x %s\n", seed, check)
				if cerr := f.Close(); err == nil {
					err = cerr
				}
			}
			if err != nil {
				panic(err)
			}
		}
		faultline.Run(t, faultline.Options{Seeds: 1, Duration: cfg.Duration}, func(w *faultline.World) {
			c := Setup(w, cfg)
			w.Sim.At(w.End().Add(-time.Millisecond), "test/tamper", func() {
				c.probeAcked = false
				c.server(3).inc.applied = 1
				c.server(2).inc.kv.Apply(kv.Command{Client: 99, ID: 1, Op: kv.OpPut, Key: "x", Value: "y"})
				last, _ := c.server(1).inc.ms.LastIndex()
				if err := c.server(1).inc.ms.Append([]*raftpb.Entry{{Index: new(last + 1), Term: new(uint64(99))}}); err != nil {
					t.Errorf("append to memory: %v", err)
				}
			})
		})
	}
}

// ETC-130: the progress check fails the run when the probe is never acknowledged.
func TestProgressFailure(t *testing.T) {
	results := resultsFile(t)
	out, code := runScenario(t, "no-progress", []string{"FAULTLINE_RESULTS=" + results})
	rs := readResults(t, results)
	want := "probe write not acknowledged within 5s of recovery start 10.000000000s"
	if code == 0 || len(rs) != 1 || rs[0].Signature != "final:"+oracle.Progress || !strings.Contains(rs[0].Message, want) {
		t.Fatalf("exit %d, results %+v\noutput:\n%s", code, rs, out)
	}
}

// ETC-020 step 7 and ETC-196: the four final checks are registered in the listed order, and
// each failure reaches selfTestHook with the run's seed, like an oracle violation. All four
// fail in one run; the first one fails the seed.
func TestFinalChecksReachHook(t *testing.T) {
	results := resultsFile(t)
	hookOut := results + ".hook"
	out, code := runScenario(t, "final-checks-fail", []string{"FAULTLINE_SEED=1", "FAULTLINE_RESULTS=" + results, "ETCDRAFT_HOOK_OUT=" + hookOut})
	rs := readResults(t, results)
	if code == 0 || len(rs) != 1 || rs[0].Signature != "final:"+oracle.Progress {
		t.Fatalf("exit %d, results %+v\noutput:\n%s", code, rs, out)
	}
	var want []string
	for _, check := range []string{oracle.Progress, oracle.AckedWritesSurvive, oracle.ReplicasAgree, oracle.WALAgreesWithMemory} {
		want = append(want, rs[0].Seed+" "+check)
	}
	data, err := os.ReadFile(hookOut)
	if err != nil {
		t.Fatalf("no hook output: %v\noutput:\n%s", err, out)
	}
	// A failing seed runs again for its artifact (API-074): the four lines come once per attempt.
	got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	for i, line := range got {
		if line != want[i%len(want)] {
			t.Fatalf("selfTestHook line %d is %q; the lines %q should repeat %q", i, line, got, want)
		}
	}
	if len(got)%len(want) != 0 {
		t.Fatalf("selfTestHook lines %q are not whole repeats of %q", got, want)
	}
}
