package etcdraft

import (
	"strings"
	"testing"
	"time"

	"go.etcd.io/raft/v3"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"

	"github.com/hmdsefi/faultline/harness/etcdraft/internal/oracle"
)

// AT-ETC-12 (the server part; TestNoFaults in clients_test.go adds the clients).
func TestLeaderElection(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Faults = FaultsNone
	opts := testOptions(t, &cfg, 20*time.Second, true)
	var st Stats
	var leaders []LeaderChange
	var infoLogs, debugLogs int
	runSeed(t, 1, opts, func(w *faultline.World) {
		c := Setup(w, cfg)
		w.Plan(Faults(cfg))
		w.Final("test: observe", func() error {
			st, leaders, infoLogs, debugLogs = c.Stats(), nil, 0, 0
			for _, lc := range c.LeaderChanges() {
				if lc.State == raft.StateLeader {
					leaders = append(leaders, lc)
				}
			}
			for _, r := range recordsOf(w, "etcdraft.raft_log") {
				switch attrOf(r, "level") {
				case "info":
					infoLogs++
				case "debug":
					debugLogs++
				}
			}
			return nil
		})
	})
	if len(leaders) != 1 || leaders[0].Term < 2 || st.Terms != 1 {
		t.Fatalf("leaders = %+v, Terms = %d; want exactly one leader, for a term >= 2", leaders, st.Terms)
	}
	if st.Readies == 0 || st.Boots != 3 || st.Recoveries != 0 || st.LastIndex < 2 {
		t.Fatalf("stats %+v", st)
	}
	if st.Syncs <= uint64(st.Boots) { //nolint:gosec // Boots is 3. ETC-123: the bootstrap syncs, then a sync for each Ready that wrote records
		t.Fatalf("Syncs = %d with %d boots and %d Readys; want Ready syncs on top of the bootstrap syncs", st.Syncs, st.Boots, st.Readies)
	}
	if infoLogs == 0 || debugLogs != 0 {
		t.Fatalf("raft_log records: %d at info, %d at debug with LogLevel = LogInfo; want some info and no debug", infoLogs, debugLogs)
	}
}

// AT-ETC-12, last clause: no etcdraft.raft_log record below LogLevel. A LogInfo run never has
// a debug record, so only a level above info shows that cfg.LogLevel reaches the server's logger.
func TestLogLevelFilter(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Faults = FaultsNone
	cfg.LogLevel = LogWarning
	opts := testOptions(t, &cfg, 20*time.Second, true)
	var below int
	runSeed(t, 1, opts, func(w *faultline.World) {
		Setup(w, cfg)
		w.Plan(Faults(cfg))
		w.Final("test: observe", func() error {
			below = 0
			for _, r := range recordsOf(w, "etcdraft.raft_log") {
				if l := attrOf(r, "level"); l == "info" || l == "debug" {
					below++
				}
			}
			return nil
		})
	})
	if below != 0 {
		t.Fatalf("%d raft_log records at info or debug with LogLevel = LogWarning", below)
	}
}

// AT-ETC-13
func TestTraceHashDeterministic(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Faults = FaultsNone
	opts := testOptions(t, &cfg, 20*time.Second, false)
	hashOf := func(t *testing.T, seed uint64) uint64 {
		var h uint64
		runSeed(t, seed, opts, func(w *faultline.World) {
			Setup(w, cfg)
			w.Plan(Faults(cfg))
			w.Final("test: hash", func() error { h = w.Sim.TraceHash(); return nil })
		})
		return h
	}
	var a, b, c uint64
	t.Run("first", func(t *testing.T) { a = hashOf(t, 1) })
	t.Run("second", func(t *testing.T) { b = hashOf(t, 1) })
	t.Run("seed2", func(t *testing.T) { c = hashOf(t, 2) })
	if a == 0 || a != b {
		t.Fatalf("seed 1 hashes %#x and %#x differ", a, b)
	}
	if c == a {
		t.Fatalf("seed 2 hash %#x equals seed 1's", c)
	}
}

// downRole returns the servers of c that are down, for scripted restarts.
func downRole(c *Cluster) func() []kernel.NodeID {
	return func() []kernel.NodeID {
		var out []kernel.NodeID
		for _, n := range c.Servers() {
			if n.State() == kernel.NodeDown {
				out = append(out, n.ID())
			}
		}
		return out
	}
}

// AT-ETC-14
func TestCrashLeaderAndRestart(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Faults = FaultsNone
	opts := testOptions(t, &cfg, 20*time.Second, true)
	var st Stats
	var crashed kernel.NodeID
	var restartBoot *kernel.Record
	var freshBoots int
	runSeed(t, 1, opts, func(w *faultline.World) {
		c := Setup(w, cfg)
		w.Role("down", downRole(c))
		w.Plan(fault.Script(
			fault.Event{At: kernel.Time(5 * time.Second), Kind: fault.KindCrash, Role: "leader"},
			fault.Event{At: kernel.Time(6 * time.Second), Kind: fault.KindRestart, Role: "down"},
		))
		w.Final("test: observe", func() error {
			st, crashed, restartBoot, freshBoots = c.Stats(), 0, nil, 0
			for _, r := range w.Sim.Records() {
				switch {
				case r.Kind == "fault.crash":
					crashed = r.Node
				case r.Kind == "etcdraft.boot" && crashed != 0 && r.Node == crashed && r.At == kernel.Time(6*time.Second):
					restartBoot = &r
				case r.Kind == "etcdraft.boot" && attrOf(r, "fresh") == "true":
					freshBoots++
				}
			}
			return nil
		})
	})
	if crashed == 0 || restartBoot == nil {
		t.Fatalf("crashed node %d, restart boot record %v", crashed, restartBoot)
	}
	if st.Recoveries < 1 || attrOf(*restartBoot, "fresh") != "false" {
		t.Fatalf("Recoveries = %d, restart boot attrs %v", st.Recoveries, restartBoot.Attrs)
	}
	if freshBoots != cfg.Nodes { // the first boots find an empty WAL; only the restart recovers
		t.Fatalf("%d boots with fresh=true, want %d", freshBoots, cfg.Nodes)
	}
}

func init() { //nolint:gochecknoinits // registers the child-process scenarios
	scenarios["early-send"] = func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Faults = FaultsNone
		cfg.earlySend = true
		cfg.PreVote = false // the first message is then a candidate's MsgVote, sent with its term and vote unpersisted
		Test(t, cfg, faultline.Options{Seeds: 1, Duration: 20 * time.Second})
	}
	scenarios["setup-invalid-config"] = func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Nodes = 4
		faultline.Run(t, faultline.Options{Seeds: 1, Duration: 20 * time.Second}, func(w *faultline.World) {
			Setup(w, cfg)
			t.Error("Setup returned after an invalid config")
		})
	}
	scenarios["setup-twice"] = func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Duration = 20 * time.Second
		faultline.Run(t, faultline.Options{Seeds: 1, Duration: cfg.Duration}, func(w *faultline.World) {
			Setup(w, cfg)
			Setup(w, cfg)
		})
	}
	scenarios["test-no-crypto-seed"] = func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Faults = FaultsNone
		Test(t, cfg, faultline.Options{Seeds: 1, NoCryptoSeed: true})
		t.Error("Test returned after an option error")
	}
	scenarios["test-fills-options"] = func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Faults = FaultsNone
		Test(t, cfg, faultline.Options{Seeds: 1}) // no Duration, Net or MaxEvents: ETC-022 fills them in
	}
	scenarios["test-explicit-options"] = func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Faults = FaultsNone
		Test(t, cfg, faultline.Options{Seeds: 1, Duration: 60 * time.Second, Net: NetConfig(), MaxEvents: 50_000_000})
	}
}

// AT-ETC-29. With PreVote on, the first message an early-sending server emits is a MsgPreVote,
// whose Ready has no records to persist, so rule 4 would pass it; the scenario turns PreVote off
// and requires the violation for a MsgVote, whose Ready carries the new term and the vote.
func TestEarlySendViolatesRule4(t *testing.T) {
	results := resultsFile(t)
	out, code := runScenario(t, "early-send", []string{"FAULTLINE_SEED=1", "FAULTLINE_RESULTS=" + results})
	rs := readResults(t, results)
	if code == 0 || len(rs) != 1 || rs[0].Signature != "invariant:"+oracle.ReadyContract || !strings.Contains(rs[0].Message, "ready contract: rule 4: MsgVote to ") {
		t.Fatalf("exit %d, results %+v\noutput:\n%s", code, rs, out)
	}
}

// AT-ETC-02 (ETC-013): Setup reports a validation error with t.Fatalf.
func TestSetupRejectsInvalidConfig(t *testing.T) {
	results := resultsFile(t)
	out, code := runScenario(t, "setup-invalid-config", []string{"FAULTLINE_RESULTS=" + results}, "-test.v")
	if rs := readResults(t, results); len(rs) != 1 || rs[0].Kind != "setup" { // ETC-013: failure kind setup
		t.Fatalf("results %+v, want one failure of kind setup\noutput:\n%s", rs, out)
	}
	if code == 0 || !strings.Contains(out, "etcdraft: invalid config: Nodes must be 3 or 5, got 4") {
		t.Fatalf("exit %d, output:\n%s", code, out)
	}
	if strings.Contains(out, "Setup returned after an invalid config") {
		t.Fatalf("Setup did not stop the body:\n%s", out)
	}
}

// §7: a second Setup on one World panics.
func TestSetupTwicePanics(t *testing.T) {
	results := resultsFile(t)
	out, code := runScenario(t, "setup-twice", []string{"FAULTLINE_RESULTS=" + results}, "-test.v")
	if rs := readResults(t, results); len(rs) != 1 || rs[0].Kind != "setup" { // the panic happens in the body, before the first event
		t.Fatalf("results %+v, want one failure of kind setup\noutput:\n%s", rs, out)
	}
	if code == 0 || !strings.Contains(out, "etcdraft: Setup called twice on this World") {
		t.Fatalf("exit %d, output:\n%s", code, out)
	}
}

// ETC-022: Test fails the test with the option error and does not run.
func TestTestRejectsNoCryptoSeed(t *testing.T) {
	out, code := runScenario(t, "test-no-crypto-seed", nil)
	if code == 0 || !strings.Contains(out, "etcdraft: Options.NoCryptoSeed must be false: etcd/raft draws election timeouts from crypto/rand") {
		t.Fatalf("exit %d, output:\n%s", code, out)
	}
	if strings.Contains(out, "Test returned after an option error") {
		t.Fatalf("Test did not stop the test:\n%s", out)
	}
}

// ETC-022: Test runs with the options it filled in. Options without Duration, Net and MaxEvents
// must give the trace of the same options with the filled-in values written out.
func TestTestFillsOptions(t *testing.T) {
	traceHash := func(scenario string) string {
		results := resultsFile(t)
		out, code := runScenario(t, scenario, []string{"FAULTLINE_SEED=1", "FAULTLINE_RESULTS=" + results})
		rs := readResults(t, results)
		if code != 0 || len(rs) != 1 || rs[0].Status != "pass" || rs[0].TraceHash == "" {
			t.Fatalf("%s: exit %d, results %+v\noutput:\n%s", scenario, code, rs, out)
		}
		return rs[0].TraceHash
	}
	if filled, explicit := traceHash("test-fills-options"), traceHash("test-explicit-options"); filled != explicit {
		t.Fatalf("trace hash %s with filled-in options differs from %s with the same options written out", filled, explicit)
	}
}
