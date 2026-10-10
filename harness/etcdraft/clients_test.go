// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package etcdraft

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.etcd.io/raft/v3"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/check/history"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
	"github.com/hmdsefi/faultline/kernel/simnet"

	"github.com/hmdsefi/faultline/harness/etcdraft/internal/kv"
	"github.com/hmdsefi/faultline/harness/etcdraft/internal/oracle"
	"github.com/hmdsefi/faultline/harness/etcdraft/internal/wire"
)

// AT-ETC-12
func TestNoFaults(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Faults = FaultsNone
	opts := testOptions(t, &cfg, 20*time.Second, true)
	var st Stats
	var leaders []LeaderChange
	var debugLogs int
	runSeed(t, 1, opts, func(w *faultline.World) {
		c := Setup(w, cfg)
		w.Plan(Faults(cfg))
		w.Final("test: observe", func() error {
			st, leaders, debugLogs = c.Stats(), nil, 0
			for _, lc := range c.LeaderChanges() {
				if lc.State == raft.StateLeader {
					leaders = append(leaders, lc)
				}
			}
			for _, r := range recordsOf(w, "etcdraft.raft_log") {
				if attrOf(r, "level") == "debug" {
					debugLogs++
				}
			}
			return nil
		})
	})
	if len(leaders) != 1 || leaders[0].Term < 2 || st.Terms != 1 {
		t.Fatalf("leaders = %+v, Terms = %d; want exactly one leader, for a term >= 2", leaders, st.Terms)
	}
	if !st.ProbeAcked || st.ProbeIndex == 0 || st.OpsOK == 0 || st.OpsPending != 0 {
		t.Fatalf("stats %+v; want the probe acknowledged and applied, OK ops, nothing pending", st)
	}
	if debugLogs != 0 {
		t.Fatalf("%d raft_log records at debug level with LogLevel = LogInfo", debugLogs)
	}
}

// AT-ETC-15
func TestSyncFailureCrashesAndRestarts(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Faults = FaultsNone
	opts := testOptions(t, &cfg, 20*time.Second, true)
	var st Stats
	var crashAt, bootAt kernel.Time
	runSeed(t, 1, opts, func(w *faultline.World) {
		c := Setup(w, cfg)
		w.Plan(fault.Script(fault.Event{At: kernel.Time(3 * time.Second), Kind: fault.KindSyncFail, Node: "n1", N: 1}))
		w.Final("test: observe", func() error {
			st, crashAt, bootAt = c.Stats(), 0, 0
			n1 := w.Sim.Lookup("n1").ID()
			for _, r := range recordsOf(w, "etcdraft.io_crash") {
				if r.Node == n1 {
					crashAt = r.At
					break
				}
			}
			for _, r := range recordsOf(w, "etcdraft.boot") {
				if r.Node == n1 && crashAt != 0 && r.At > crashAt {
					bootAt = r.At
					break
				}
			}
			return nil
		})
	})
	if st.IOCrashes != 1 {
		t.Fatalf("IOCrashes = %d, want 1", st.IOCrashes)
	}
	if crashAt < kernel.Time(3*time.Second) || bootAt != crashAt.Add(cfg.CrashRestartDelay) {
		t.Fatalf("io_crash at %s, reboot at %s; want the reboot CrashRestartDelay (%v) later", crashAt, bootAt, cfg.CrashRestartDelay)
	}
}

func init() { //nolint:gochecknoinits // registers the child-process scenarios
	scenarios["quiet-output"] = func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Faults = FaultsNone
		Test(t, cfg, faultline.Options{Seeds: 1, Duration: 15 * time.Second})
	}
	// The planner's recovery window is one second shorter than Config.Quiet: ETC-096.
	scenarios["late-recovery"] = func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Faults = FaultsNone
		cfg.Duration = 20 * time.Second
		faultline.Run(t, faultline.Options{Seeds: 1, Duration: cfg.Duration}, func(w *faultline.World) {
			Setup(w, cfg)
			w.Plan(&fault.Random{Rules: presetRules("B"), Quiet: cfg.Quiet - time.Second})
		})
	}
	// A client sends a conf change request whose Data is no ConfChangeV2: ETC-093.
	scenarios["bad-conf-change"] = func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Faults = FaultsNone
		cfg.Duration = 20 * time.Second
		faultline.Run(t, faultline.Options{Seeds: 1, Duration: cfg.Duration}, func(w *faultline.World) {
			c := Setup(w, cfg)
			w.AddClient("rogue", func(n *kernel.Node) {
				n.After(time.Second, "test/send", func() {
					w.Net.Send(n, c.Servers()[0].ID(), requestPacket("rogue", wire.Request{Client: 99, ID: 1, Attempt: 1, Op: kv.OpConfChange, Data: []byte{0xff}}))
				})
			})
		})
	}
	// A server's packet type reaches a client: ETC-098.
	scenarios["client-gets-request"] = func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Faults = FaultsNone
		cfg.Duration = 20 * time.Second
		faultline.Run(t, faultline.Options{Seeds: 1, Duration: cfg.Duration}, func(w *faultline.World) {
			c := Setup(w, cfg)
			w.AddClient("rogue", func(n *kernel.Node) {
				n.After(time.Second, "test/send", func() {
					w.Net.Send(n, c.Clients()[0].ID(), requestPacket("rogue", wire.Request{Client: 99, ID: 1, Attempt: 1, Op: kv.OpGet, Key: "k"}))
				})
			})
		})
	}
}

// failureOf runs the child-process scenario name and returns the message of its one failing seed.
func failureOf(t *testing.T, name string) string {
	t.Helper()
	results := resultsFile(t)
	out, code := runScenario(t, name, []string{"FAULTLINE_SEED=1", "FAULTLINE_RESULTS=" + results})
	rs := readResults(t, results)
	if code == 0 || len(rs) != 1 || rs[0].Signature != "invariant:"+oracle.Harness {
		t.Fatalf("exit %d, results %+v\noutput:\n%s", code, rs, out)
	}
	return rs[0].Message
}

// ETC-096: a fault plan whose recovery starts after the harness's is a harness error.
func TestProbeRejectsLateRecovery(t *testing.T) {
	msg := failureOf(t, "late-recovery")
	if want := "fault recovery starts at 11.000000000s, after the harness recovery start 10.000000000s; set the planner's Quiet to at least Config.Quiet"; !strings.Contains(msg, want) {
		t.Fatalf("message %q; want it to contain %q", msg, want)
	}
}

// ETC-093: an undecodable conf change is a harness error whose text is fixed. protobuf-go
// varies the spacing of its own messages from one binary to the next and harness errors
// are hashed, so the text must not contain the protobuf error (ETC-121).
func TestUndecodableConfChangeText(t *testing.T) {
	msg := failureOf(t, "bad-conf-change")
	if want := "n1: conf change decode: undecodable request from rogue"; msg != want || strings.Contains(strings.ToLower(msg), "proto") {
		t.Fatalf("message %q; want %q, which says nothing of protobuf", msg, want)
	}
}

// AT-ETC-24
func TestNoStdoutOrStderr(t *testing.T) {
	out, code := runScenario(t, "quiet-output", nil)
	if code != 0 || out != "PASS\n" {
		t.Fatalf("exit %d, output %q; want exit 0 and exactly \"PASS\\n\"", code, out)
	}
	count := func(level LogLevel) (all, nonPanic int) {
		cfg := DefaultConfig()
		cfg.Faults = FaultsNone
		cfg.LogLevel = level
		opts := testOptions(t, &cfg, 15*time.Second, true)
		t.Run("level"+strconv.Itoa(int(level)), func(t *testing.T) {
			runSeed(t, 1, opts, func(w *faultline.World) {
				Setup(w, cfg)
				w.Final("test: count", func() error {
					all, nonPanic = 0, 0
					for _, r := range recordsOf(w, "etcdraft.raft_log") {
						all++
						if l := attrOf(r, "level"); l != "panic" && l != "fatal" {
							nonPanic++
						}
					}
					return nil
				})
			})
		})
		return all, nonPanic
	}
	if all, _ := count(LogInfo); all == 0 {
		t.Fatal("no etcdraft.raft_log records at LogInfo")
	}
	if _, nonPanic := count(LogNone); nonPanic != 0 {
		t.Fatalf("%d non-panic raft_log records at LogNone", nonPanic)
	}
}

// AT-ETC-27
func TestHistoryShapes(t *testing.T) {
	cfg := DefaultConfig()
	opts := testOptions(t, &cfg, 60*time.Second, false)
	var problems []string
	var ok int
	runSeed(t, 1, opts, func(w *faultline.World) {
		c := Setup(w, cfg)
		w.Plan(Faults(cfg))
		w.Final("test: history", func() error {
			problems, ok = nil, 0
			pending := map[string]int{}
			for _, op := range w.History.Ops() {
				if op.Status == history.Pending {
					pending[op.Process]++
				}
				if op.F != "read" && op.F != "write" {
					problems = append(problems, "unexpected f "+op.F)
					continue
				}
				// LIN rejects an input key the model does not know (LIN-023), so unknown keys fail here too.
				var in KVInput
				rawIn, _ := op.Input.(json.RawMessage)
				dec := json.NewDecoder(bytes.NewReader(rawIn))
				dec.DisallowUnknownFields()
				if err := dec.Decode(&in); err != nil || in.Key == "" || (op.F == "write") != (in.Value != "") {
					problems = append(problems, "bad input of op "+strconv.FormatInt(op.ID, 10))
				}
				if op.Status != history.OK {
					continue
				}
				ok++
				raw, _ := op.Output.(json.RawMessage)
				switch {
				case op.F == "write" && string(raw) != "null":
					problems = append(problems, "write output "+string(raw))
				case op.F == "read" && string(raw) != "null" && !strings.HasPrefix(string(raw), `"`):
					problems = append(problems, "read output "+string(raw))
				case op.F == "read" && string(raw) == `""`:
					problems = append(problems, "read output is the empty string: values are never empty, an absent key reads null")
				}
				if op.F == "write" {
					cl := c.clientByName(op.Process)
					if _, applied := c.o.FirstApplied(cl.cid, c.opIDs[op.ID]); !applied {
						problems = append(problems, "acked write not applied: op "+strconv.FormatInt(op.ID, 10))
					}
				}
			}
			for _, p := range history.Processes(w.History.Ops()) {
				if pending[p] > 1 {
					problems = append(problems, p+" has several pending ops")
				}
			}
			// ETC-090: c<k> is client k and the probe is K+1.
			for k := 1; k <= cfg.Clients; k++ {
				if cl := c.clientByName("c" + strconv.Itoa(k)); cl == nil || cl.cid != uint32(k) { //nolint:gosec // k <= Clients <= 16
					problems = append(problems, "client c"+strconv.Itoa(k)+" has the wrong client ID")
				}
			}
			if cl := c.clientByName("probe"); cl == nil || cl.cid != uint32(cfg.Clients+1) { //nolint:gosec // Clients <= 16
				problems = append(problems, "the probe's client ID is not Clients+1")
			}
			return nil
		})
	})
	if ok == 0 || len(problems) != 0 {
		t.Fatalf("%d OK ops; problems: %v", ok, problems)
	}
}

// AT-ETC-28
func TestIsolateTargetsLeader(t *testing.T) {
	cfg := DefaultConfig()
	opts := testOptions(t, &cfg, 60*time.Second, true)
	var targets, mismatches []string
	runSeed(t, 1, opts, func(w *faultline.World) {
		Setup(w, cfg)
		w.Plan(&fault.Random{Rules: []fault.Rule{presetRule('B')}, Quiet: cfg.Quiet})
		w.Final("test: targets", func() error {
			targets, mismatches = nil, nil
			type role struct {
				state string
				term  uint64
			}
			last := map[kernel.NodeID]role{}
			for _, r := range w.Sim.Records() {
				switch r.Kind {
				case "etcdraft.role":
					term, _ := strconv.ParseUint(attrOf(r, "term"), 10, 64)
					last[r.Node] = role{attrOf(r, "state"), term}
				case "fault.target":
					leader, best := "", uint64(0)
					for _, n := range w.Servers() { // ascending ID: the lowest ID wins a tie
						if l, ok := last[n.ID()]; ok && l.state == raft.StateLeader.String() && (leader == "" || l.term > best) {
							leader, best = n.Name(), l.term
						}
					}
					targets = append(targets, attrOf(r, "chosen"))
					if attrOf(r, "chosen") != leader {
						mismatches = append(mismatches, r.At.String()+": chose "+attrOf(r, "chosen")+", leader "+leader)
					}
				}
			}
			return nil
		})
	})
	if len(targets) == 0 || len(mismatches) != 0 {
		t.Fatalf("targets %v; mismatches %v", targets, mismatches)
	}
}

// msTime is the virtual time n milliseconds after the start, for scripted faults.
func msTime(n int) kernel.Time { return kernel.Time(time.Duration(n) * time.Millisecond) }

// parseRequest reads the description of a request packet, "req c2#14.1 put k3".
func parseRequest(desc string) (client string, id, attempt uint64, ok bool) {
	f := strings.Fields(desc)
	if len(f) < 2 || f[0] != "req" {
		return "", 0, 0, false
	}
	hash, dot := strings.Index(f[1], "#"), strings.LastIndex(f[1], ".")
	if hash < 0 || dot < hash {
		return "", 0, 0, false
	}
	id, err1 := strconv.ParseUint(f[1][hash+1:dot], 10, 64)
	attempt, err2 := strconv.ParseUint(f[1][dot+1:], 10, 64)
	return f[1][:hash], id, attempt, err1 == nil && err2 == nil
}

// parseReply reads the description of a reply packet, "rep #14.1 ok lead=1".
func parseReply(desc string) (id, attempt uint64, status string, lead uint64, ok bool) {
	f := strings.Fields(desc)
	if len(f) != 4 || f[0] != "rep" || !strings.HasPrefix(f[1], "#") || !strings.HasPrefix(f[3], "lead=") {
		return 0, 0, "", 0, false
	}
	dot := strings.LastIndex(f[1], ".")
	if dot < 0 {
		return 0, 0, "", 0, false
	}
	id, err1 := strconv.ParseUint(f[1][1:dot], 10, 64)
	attempt, err2 := strconv.ParseUint(f[1][dot+1:], 10, 64)
	lead, err3 := strconv.ParseUint(strings.TrimPrefix(f[3], "lead="), 10, 64)
	return id, attempt, f[2], lead, err1 == nil && err2 == nil && err3 == nil
}

// clientTrace is what checkClients found in a trace, and how often each rule was exercised.
type clientTrace struct {
	problems []string

	hintAfterDropped int // requests that went to the leader a delivered Dropped reply named
	hintAfterOK      int // requests that went to the leader a delivered OK reply named
	excluded         int // attempt-timeout picks that avoided the server that timed out
	retries          int // retries sent exactly RetryDelay after the last Dropped reply
	dupDropped       int // Dropped replies delivered again for an attempt that already had one
	staleDropped     int // Dropped replies for an attempt that was no longer the current one
	armedBeforeDrain int // Dropped replies whose retry fell at or after the drain start
	starved          int // operations still waiting when an attempt timeout fell in the drain
}

// summary returns the number of problems and the first few of them.
func (tr clientTrace) summary() string {
	return fmt.Sprintf("%d problems:\n\t%s", len(tr.problems), strings.Join(tr.problems[:min(len(tr.problems), 5)], "\n\t"))
}

// checkClients replays ETC-092 and ETC-095 for the workload clients against w's trace
// (TraceFull) and the history: attempts are numbered 1..k per operation; after a delivered
// Dropped reply for the current attempt the next attempt follows RetryDelay after the last
// such reply, and goes to the leader the reply named; after an attempt timeout it goes
// AttemptTimeout after the previous attempt to another server; after an OK reply the next
// operation goes to the leader the reply named; and from the drain start on, no request is
// sent and no operation is invoked. The probe is not checked.
func checkClients(w *faultline.World, c *Cluster) clientTrace {
	var tr clientTrace
	wl, drain := c.cfg.Workload, c.drainStart
	bad := func(at kernel.Time, format string, args ...any) {
		tr.problems = append(tr.problems, at.String()+": "+fmt.Sprintf(format, args...))
	}
	type state struct {
		id, attempt uint64      // the operation in progress (or the last one) and its last attempt
		target      string      // the server of the last attempt
		start, sent kernel.Time // first and last attempt of the operation
		hint        string      // the server the next request must go to, "" = any
		hintAfter   string      // "ok" or "dropped": the reply that set the hint
		dropped     bool        // a Dropped reply for the current attempt was delivered
		retryAt     kernel.Time // when the retry after it is due, 0 = none is due
		done        bool        // the operation completed with an OK reply
	}
	st := map[string]*state{} // by client name
	payloads := map[string]string{}
	for _, r := range w.Sim.Records() {
		switch r.Kind {
		case "net.send":
			desc := attrOf(r, "payload")
			payloads[attrOf(r, "msg")] = desc
			name, id, attempt, ok := parseRequest(desc)
			if !ok || name == "probe" {
				continue
			}
			to := attrOf(r, "to")
			if r.At >= drain {
				bad(r.At, "%s -> %s sent during the drain (it starts at %s)", desc, to, drain)
			}
			s := st[name]
			if s == nil {
				s = &state{}
				st[name] = s
			}
			if id == s.id {
				if attempt != s.attempt+1 {
					bad(r.At, "%s follows attempt %d", desc, s.attempt)
				}
				if s.dropped {
					if r.At != s.retryAt {
						bad(r.At, "%s: sent after a Dropped reply, but the retry was due at %s (0: none was due)", desc, s.retryAt)
					} else {
						tr.retries++
					}
				} else {
					if r.At != s.sent.Add(wl.AttemptTimeout) {
						bad(r.At, "%s: the attempt timeout falls at %s", desc, s.sent.Add(wl.AttemptTimeout))
					}
					s.hint = ""
					if to == s.target {
						bad(r.At, "%s repeats the server that timed out", desc)
					} else {
						tr.excluded++
					}
				}
			} else {
				if attempt != 1 {
					bad(r.At, "%s: the first attempt of an operation is not 1", desc)
				}
				if s.dropped && !s.done && s.retryAt != 0 && s.retryAt < s.start.Add(wl.OpTimeout) {
					bad(r.At, "client %s: the retry due at %s was never sent", name, s.retryAt)
				}
				s.start = r.At
			}
			if s.hint != "" {
				if to != s.hint {
					bad(r.At, "%s -> %s, but the hint names %s", desc, to, s.hint)
				} else if s.hintAfter == "ok" {
					tr.hintAfterOK++
				} else {
					tr.hintAfterDropped++
				}
			}
			s.id, s.attempt, s.target, s.sent = id, attempt, to, r.At
			s.dropped, s.retryAt, s.done = false, 0, false
		case "net.deliver":
			id, attempt, status, lead, ok := parseReply(payloads[attrOf(r, "msg")])
			if !ok {
				continue
			}
			s := st[attrOf(r, "to")]
			if s == nil || s.id != id || s.done || r.At >= s.start.Add(wl.OpTimeout) {
				continue // for no operation in progress: the client ignores it
			}
			hint := ""
			if lead != 0 {
				hint = "n" + strconv.FormatUint(lead, 10)
			}
			switch {
			case status == "ok":
				s.hint, s.hintAfter, s.done, s.dropped, s.retryAt = hint, "ok", true, false, 0
			case attempt != s.attempt:
				tr.staleDropped++
			default:
				if s.dropped {
					tr.dupDropped++
				}
				s.hint, s.hintAfter, s.dropped, s.retryAt = hint, "dropped", true, 0
				if due := r.At.Add(wl.RetryDelay); due < drain {
					s.retryAt = due
				} else if r.At < drain {
					tr.armedBeforeDrain++
				}
			}
		}
	}
	for _, cl := range c.clients {
		s := st[cl.name]
		if s == nil || s.done {
			continue
		}
		if s.dropped && s.retryAt != 0 && s.retryAt < s.start.Add(wl.OpTimeout) {
			bad(w.End(), "client %s: the retry due at %s was never sent", cl.name, s.retryAt)
		}
		if due := s.sent.Add(wl.AttemptTimeout); !s.dropped && due >= drain && due < min(s.start.Add(wl.OpTimeout), w.End()) {
			tr.starved++
		}
	}
	for _, op := range w.History.Ops() {
		if op.Process != "probe" && op.Call >= drain {
			bad(op.Call, "%s invoked operation %d during the drain", op.Process, op.ID)
		}
	}
	return tr
}

// replyLinks returns one event per link from a server to a workload client: at time at,
// the link takes the config l (Reset: it goes back to the default config instead).
func replyLinks(cfg Config, at kernel.Time, l *simnet.Link) []fault.Event {
	var events []fault.Event
	for i := 1; i <= cfg.Nodes; i++ {
		for k := 1; k <= cfg.Clients; k++ {
			e := fault.Event{At: at, Kind: fault.KindLinkReset, Node: "n" + strconv.Itoa(i), Peer: "c" + strconv.Itoa(k)}
			if l != nil {
				e.Kind, e.Link = fault.KindLink, &simnet.Link{}
				*e.Link = *l
			}
			events = append(events, e)
		}
	}
	return events
}

// runTrace runs one seed with plan, in a subtest, and returns what checkClients found.
func runTrace(t *testing.T, name string, cfg Config, seed uint64, plan func(Config) fault.Planner) clientTrace {
	t.Helper()
	opts := testOptions(t, &cfg, cfg.Duration, true)
	var tr clientTrace
	t.Run(name, func(t *testing.T) {
		runSeed(t, seed, opts, func(w *faultline.World) {
			c := Setup(w, cfg)
			w.Plan(plan(cfg))
			w.Final("test: clients", func() error { tr = checkClients(w, c); return nil })
		})
	})
	return tr
}

// ETC-092 and ETC-095 under the default faults: targets, numbering, retries and the drain,
// with proposal forwarding on (a follower proposes for the client) and off (a follower
// answers Dropped and names the leader, so the client redirects).
func TestClientsFollowTheRules(t *testing.T) {
	var sum clientTrace
	for _, noForwarding := range []bool{false, true} {
		cfg := DefaultConfig()
		cfg.Duration = 60 * time.Second
		cfg.DisableProposalForwarding = noForwarding
		for seed := uint64(1); seed <= 6; seed++ {
			tr := runTrace(t, fmt.Sprintf("nofwd=%t/seed%d", noForwarding, seed), cfg, seed, Faults)
			if len(tr.problems) != 0 {
				t.Errorf("no forwarding %t, seed %d: %s", noForwarding, seed, tr.summary())
			}
			sum.hintAfterDropped += tr.hintAfterDropped
			sum.hintAfterOK += tr.hintAfterOK
			sum.excluded += tr.excluded
			sum.retries += tr.retries
		}
	}
	// Each rule must have been exercised, or a pass proves nothing.
	if sum.hintAfterDropped == 0 || sum.hintAfterOK == 0 || sum.excluded == 0 || sum.retries == 0 {
		t.Fatalf("rules exercised: %+v; want every count above 0", sum)
	}
}

// A retry armed by a Dropped reply just before the drain must not send once the drain has
// started (ETC-095): the quorum goes away shortly before the drain, so the one server left
// answers Dropped, a retry is armed, and the drain starts before it fires. RetryDelay is
// long, so that a Dropped reply falls in the window with certainty.
func TestClientsSendNothingInTheDrain(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Faults = FaultsNone
	cfg.Clients = 8
	cfg.Duration = 20 * time.Second
	cfg.Workload.RetryDelay = 400 * time.Millisecond
	cfg.Workload.AttemptTimeout = 100 * time.Millisecond
	tr := runTrace(t, "no-quorum", cfg, 1, func(Config) fault.Planner {
		return fault.Script(
			fault.Event{At: msTime(15000), Kind: fault.KindIsolate, Node: "n1"},
			fault.Event{At: msTime(15000), Kind: fault.KindIsolate, Node: "n2"})
	})
	if len(tr.problems) != 0 {
		t.Fatal(tr.summary())
	}
	if tr.armedBeforeDrain == 0 || tr.starved == 0 {
		t.Fatalf("no retry fell in the drain (%d) or no attempt timeout did (%d): the run does not test the rule", tr.armedBeforeDrain, tr.starved)
	}
}

// A duplicated Dropped reply must not arm a second retry (ETC-095): every reply is
// delivered twice, with independent delays, while no leader exists yet and every request
// is answered Dropped.
func TestClientsDuplicatedReplies(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Faults = FaultsNone
	cfg.Duration = 20 * time.Second
	link := simnet.Link{Latency: 2 * time.Millisecond, Jitter: 10 * time.Millisecond, DupPPM: simnet.MaxPPM}
	tr := runTrace(t, "dup", cfg, 1, func(cfg Config) fault.Planner { return fault.Script(replyLinks(cfg, 0, &link)...) })
	if len(tr.problems) != 0 {
		t.Fatal(tr.summary())
	}
	if tr.dupDropped == 0 {
		t.Fatal("no Dropped reply was delivered twice: the run does not test the rule")
	}
}

// A Dropped reply for an attempt that is no longer the current one is ignored (ETC-095):
// every reply takes longer than AttemptTimeout, so the client has sent the next attempt
// when the reply for the previous one arrives.
func TestClientsSlowReplies(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Faults = FaultsNone
	cfg.Duration = 20 * time.Second
	link := simnet.Link{Latency: cfg.withDefaults().Workload.AttemptTimeout + 100*time.Millisecond}
	tr := runTrace(t, "slow", cfg, 1, func(cfg Config) fault.Planner { return fault.Script(replyLinks(cfg, 0, &link)...) })
	if len(tr.problems) != 0 {
		t.Fatal(tr.summary())
	}
	if tr.staleDropped == 0 || tr.excluded == 0 {
		t.Fatalf("stale Dropped replies %d, attempt timeouts %d: the run does not test the rule", tr.staleDropped, tr.excluded)
	}
}

// A request the cluster applied before its reply was lost is acknowledged when the client
// sends it again (ETC-094, ETC-095): the servers' replies are lost for a while, the
// requests still get through, and once the replies flow again every operation completes
// OK within OpTimeout. A duplicate that was not acknowledged would leave the operation to
// time out as Info.
func TestClientsLostReplies(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Faults = FaultsNone
	cfg.Duration = 20 * time.Second
	opts := testOptions(t, &cfg, cfg.Duration, true)
	from, to := msTime(3000), msTime(4200)
	var problems []string
	var spanning int
	runSeed(t, 1, opts, func(w *faultline.World) {
		c := Setup(w, cfg)
		lost := simnet.Link{Latency: 2 * time.Millisecond, DropPPM: simnet.MaxPPM}
		w.Plan(fault.Script(append(replyLinks(cfg, from, &lost), replyLinks(cfg, to, nil)...)...))
		w.Final("test: lost replies", func() error {
			problems, spanning = checkClients(w, c).problems, 0
			for _, op := range w.History.Ops() {
				if op.Process == "probe" {
					continue
				}
				if op.Status == history.Info {
					problems = append(problems, fmt.Sprintf("%s: operation %d ended as Info at %s", op.Process, op.ID, op.Return))
				}
				if op.Status == history.OK && op.Call >= from && op.Call < to && op.Return > to {
					spanning++
				}
			}
			return nil
		})
	})
	if len(problems) != 0 {
		t.Fatalf("%d problems, the first: %v", len(problems), problems[:min(len(problems), 5)])
	}
	if spanning == 0 {
		t.Fatal("no operation started while the replies were lost and completed after: the run does not test the rule")
	}
}

// ETC-097: a client that crashes with an operation in progress completes it as Info at the
// crash, and its operation IDs go on counting after the restart. The probe, which ETC-096
// starts once, does not invoke a second operation after it was acknowledged and restarted.
func TestClientCrash(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Faults = FaultsNone
	opts := testOptions(t, &cfg, 20*time.Second, true)
	var problems []string
	var inProgress, restarted int
	var probeOps []history.Op
	runSeed(t, 1, opts, func(w *faultline.World) {
		c := Setup(w, cfg)
		w.Plan(fault.Script(
			fault.Event{At: msTime(4000), Kind: fault.KindIsolate, Node: "c1"},
			fault.Event{At: msTime(5000), Kind: fault.KindCrash, Node: "c1"},
			fault.Event{At: msTime(5500), Kind: fault.KindHeal},
			fault.Event{At: msTime(6000), Kind: fault.KindRestart, Node: "c1"},
			fault.Event{At: msTime(13000), Kind: fault.KindCrash, Node: "probe"},
			fault.Event{At: msTime(14000), Kind: fault.KindRestart, Node: "probe"},
		))
		w.Final("test: client crash", func() error {
			problems, inProgress, restarted, probeOps = nil, 0, 0, nil
			var last uint64 // the client op ID of c1's previous operation
			for _, op := range w.History.Ops() {
				if op.Process == "probe" {
					probeOps = append(probeOps, op)
					continue
				}
				if op.Process != "c1" {
					continue
				}
				id := c.opIDs[op.ID]
				if id != last+1 {
					problems = append(problems, fmt.Sprintf("c1 operation %d has client op ID %d after %d", op.ID, id, last))
				}
				last = id
				switch {
				case op.Status == history.Pending:
					problems = append(problems, fmt.Sprintf("c1 operation %d is still pending", op.ID))
				case op.Return == msTime(5000):
					inProgress++
					if op.Status != history.Info {
						problems = append(problems, fmt.Sprintf("c1 operation %d in progress at the crash is %s, want Info", op.ID, op.Status))
					}
				case op.Call >= msTime(6000):
					restarted++
				}
			}
			return nil
		})
	})
	if len(problems) != 0 {
		t.Fatalf("%d problems, the first: %v", len(problems), problems[:min(len(problems), 5)])
	}
	if inProgress != 1 || restarted == 0 {
		t.Fatalf("%d operations were in progress at the crash, %d started after the restart; want 1 and some", inProgress, restarted)
	}
	if len(probeOps) != 1 || probeOps[0].Status != history.OK || probeOps[0].F != "write" {
		t.Fatalf("probe operations %+v; want exactly one OK write", probeOps)
	}
}

// ETC-098: a client accepts replies only; the harness error names the tag by its name.
func TestClientRejectsOtherPackets(t *testing.T) {
	if msg, want := failureOf(t, "client-gets-request"), "c1: client c1: unexpected tag request"; !strings.HasSuffix(msg, want) {
		t.Fatalf("message %q; want it to end with %q", msg, want)
	}
}
