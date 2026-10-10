package etcdraft

import (
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simdisk"

	"github.com/hmdsefi/faultline/harness/etcdraft/internal/oracle"
)

// selfTestCase is one row of ETC-190 and ETC-193.
type selfTestCase struct {
	name  string
	bug   Bug
	bound int
	// checks are the expected first failing checks (ETC-190); panics lists substrings
	// of raft panic messages that also count (ETC-190).
	checks []string
	panics []string
}

var selfTestCases = []selfTestCase{
	{"SendBeforePersist", BugSendBeforePersist, 500,
		[]string{oracle.ElectionSafety, oracle.StateMachineSafety, oracle.AckedWritesSurvive},
		[]string{"is out of range [lastIndex("}},
	{"SkipSync", BugSkipSync, 50, []string{oracle.DurableState}, nil},
	{"ApplyBeforeCommit", BugApplyBeforeCommit, 200,
		[]string{oracle.StateMachineSafety, oracle.AckedWritesSurvive, oracle.ReplicasAgree},
		[]string{"unexpected error when getting unapplied entries"}},
	{"ForgetHardState", BugForgetHardState, 50, []string{oracle.DurableState, oracle.WALAgreesWithMemory}, nil},
}

// selfTestOptions are the ETC-192 options.
func selfTestOptions(seeds int) faultline.Options {
	return faultline.Options{Duration: 20 * time.Second, BaseSeed: 1, Seeds: seeds, Disk: simdisk.Config{Crash: simdisk.CrashLoseUnsynced}}
}

func init() { //nolint:gochecknoinits // registers the child-process scenario
	// "selftest": runs SelfTestConfig(Bug(ETCDRAFT_BUG)) for ETCDRAFT_SEEDS seeds with the
	// ETC-192 options, stopping at the first failing seed. Each first violation seen by
	// selfTestHook is appended to the file ETCDRAFT_HOOK_OUT as "<seed hex> <check>".
	scenarios["selftest"] = func(t *testing.T) {
		b, err := strconv.ParseUint(os.Getenv("ETCDRAFT_BUG"), 10, 32)
		if err != nil {
			t.Fatalf("ETCDRAFT_BUG: %v", err)
		}
		seeds, err := strconv.Atoi(os.Getenv("ETCDRAFT_SEEDS"))
		if err != nil {
			t.Fatalf("ETCDRAFT_SEEDS: %v", err)
		}
		cfg := SelfTestConfig(Bug(b))
		cfg.selfTestHook = hookWriter(os.Getenv("ETCDRAFT_HOOK_OUT"))
		Test(t, cfg, selfTestOptions(seeds))
	}
}

// runSelfTest runs the "selftest" scenario and returns the results, the hook lines, the
// child's output and its exit code.
func runSelfTest(t *testing.T, b Bug, seeds int) ([]seedResult, []string, string, int) {
	t.Helper()
	results := resultsFile(t)
	hookOut := results + ".hook"
	out, code := runScenario(t, "selftest", []string{
		"ETCDRAFT_BUG=" + strconv.FormatUint(uint64(b), 10),
		"ETCDRAFT_SEEDS=" + strconv.Itoa(seeds),
		"ETCDRAFT_HOOK_OUT=" + hookOut,
		"FAULTLINE_RESULTS=" + results,
	}, "-test.timeout=0")
	return readResults(t, results), readHook(t, hookOut), out, code
}

// AT-ETC-17
func TestSelfTestSwitches(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("self-test switches run hundreds of seeds; skipped under -short and -race")
	}
	for _, tc := range selfTestCases {
		t.Run(tc.name, func(t *testing.T) {
			rs, hooks, out, code := runSelfTest(t, tc.bug, tc.bound)
			if len(rs) == 0 || rs[len(rs)-1].Status != "fail" {
				t.Fatalf("no failing seed among the first %d (exit %d)\n%s", tc.bound, code, tail(out))
			}
			if code == 0 {
				t.Fatalf("the child exited 0 although seed %s failed\n%s", rs[len(rs)-1].Seed, tail(out))
			}
			first := rs[len(rs)-1]
			t.Logf("%s: first failing seed index %d (%s): %s", tc.name, first.Index, first.Seed, first.Signature)
			okCheck := false
			for _, c := range tc.checks {
				okCheck = okCheck || first.Signature == "invariant:"+c || first.Signature == "final:"+c
			}
			for _, p := range tc.panics {
				okCheck = okCheck || first.Kind == "panic" && strings.Contains(first.Message, p)
			}
			if !okCheck {
				t.Fatalf("first failing check %q (%s) is not in the expected set %v / panics %v", first.Signature, first.Message, tc.checks, tc.panics)
			}
			if first.Kind == "invariant" {
				want := first.Seed + " " + first.Check
				if len(hooks) == 0 || !strings.HasPrefix(hooks[len(hooks)-1], first.Seed+" ") || !slices.Contains(hooks, want) {
					t.Fatalf("selfTestHook lines %v do not report %q", hooks, want)
				}
			}
		})
	}
}

// AT-ETC-18
func TestSelfTestConfigPasses(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("500 seeds; skipped under -short and -race")
	}
	rs, _, out, code := runSelfTest(t, 0, 500)
	if code != 0 || len(rs) != 500 {
		t.Fatalf("exit %d, %d results, want exit 0 and 500\n%s", code, len(rs), tail(out))
	}
	for _, r := range rs {
		if r.Status != "pass" {
			t.Fatalf("seed %s (index %d) failed: %s: %s", r.Seed, r.Index, r.Signature, r.Message)
		}
	}
}

// ETC-197: Setup records the enabled switches in one global record, and no record when none
// is set. ETC-064: under BugSkipSync every persist point says synced=false and no sync is
// counted beyond the bootstrap ones.
func TestBugsEnabledRecord(t *testing.T) {
	type outcome struct {
		setupBugs         string
		enabled, persists []kernel.Record
		stats             Stats
	}
	run := func(t *testing.T, bugs Bug) (o outcome) {
		cfg := DefaultConfig()
		cfg.Faults = FaultsNone
		cfg.Bugs = bugs
		opts := testOptions(t, &cfg, 15*time.Second, true)
		runSeed(t, 1, opts, func(w *faultline.World) {
			c := Setup(w, cfg)
			w.Final("test: records", func() error {
				o = outcome{enabled: recordsOf(w, "etcdraft.bugs_enabled"), persists: recordsOf(w, "etcdraft.persist"), stats: c.Stats()}
				for _, r := range recordsOf(w, "etcdraft.setup") {
					o.setupBugs = attrOf(r, "bugs")
				}
				return nil
			})
		})
		return o
	}
	t.Run("SkipSync", func(t *testing.T) {
		const names = "SkipSync,SnapshotOffByOne"
		got := run(t, BugSkipSync|BugSnapshotOffByOne) // harmless without crashes and snapshots
		if got.setupBugs != "18" || len(got.enabled) != 1 {
			t.Fatalf("setup bugs=%q, %d etcdraft.bugs_enabled records; want 18 and 1", got.setupBugs, len(got.enabled))
		}
		if r := got.enabled[0]; r.Node != 0 || r.Text != "bugs enabled: "+names || attrOf(r, "bugs") != names {
			t.Fatalf("bugs_enabled record %+v; want a global record with text %q and bugs=%s", r, "bugs enabled: "+names, names)
		}
		if len(got.persists) == 0 {
			t.Fatal("no etcdraft.persist record: BugSkipSync still records the persist points (ETC-190)")
		}
		for _, r := range got.persists {
			if attrOf(r, "synced") != "false" {
				t.Fatalf("%q under BugSkipSync; want synced=false (ETC-064)", r.Text)
			}
		}
		if got.stats.Syncs != uint64(got.stats.Boots) { //nolint:gosec // Boots is 3
			t.Fatalf("Syncs = %d with %d boots; want only the bootstrap syncs under BugSkipSync (ETC-064)", got.stats.Syncs, got.stats.Boots)
		}
	})
	t.Run("NoBugs", func(t *testing.T) {
		if got := run(t, 0); len(got.enabled) != 0 {
			t.Fatalf("%d etcdraft.bugs_enabled records with no switch set; want none", len(got.enabled))
		}
	})
}
