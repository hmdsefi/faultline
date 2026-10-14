// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package etcdraft

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
)

// listedSeeds are the seeds the "testseeds" scenario lists, in that order.
var listedSeeds = []uint64{0x3, 0x1, 0x2}

// validationRows are the errors Test reports for a bad Config or bad Options: ETC-022's, which
// fail t before any seed runs, and an ETC-012 Config error, which Setup reports in the first seed
// (seed set). TestSeeds reports each of them the same way (ETC-025). A noSeeds row calls only
// TestSeeds, with no seeds.
var validationRows = []struct {
	name    string
	cfg     func(c *Config)
	opts    faultline.Options
	msg     string
	seed    bool
	noSeeds bool
}{
	{"NoCryptoSeed", nil, faultline.Options{NoCryptoSeed: true},
		"etcdraft: Options.NoCryptoSeed must be false: etcd/raft draws election timeouts from crypto/rand", false, false},
	{"ModeGoroutine", nil, faultline.Options{Mode: faultline.ModeGoroutine}, "etcdraft: Options.Mode must be ModeEvent", false, false},
	{"mismatch", func(c *Config) { c.Duration = 30 * time.Second }, faultline.Options{Duration: 20 * time.Second},
		"etcdraft: Config.Duration (30s) differs from Options.Duration (20s)", false, false},
	// Options.Duration defaults to 60s first, as in Test.
	{"default Duration", func(c *Config) { c.Duration = 30 * time.Second }, faultline.Options{},
		"etcdraft: Config.Duration (30s) differs from Options.Duration (1m0s)", false, false},
	{"Nodes 4", func(c *Config) { c.Nodes = 4 }, faultline.Options{Seeds: 1, Duration: 20 * time.Second},
		"etcdraft: invalid config: Nodes must be 3 or 5, got 4", true, false},
	// Options.Seeds does not stand in for the missing seed list.
	{"no seeds", nil, faultline.Options{Seeds: 1, Duration: 20 * time.Second},
		"etcdraft: TestSeeds needs at least one seed; list the seeds after opts", false, true},
	// ETC-022's errors come before the missing seed list.
	{"NoCryptoSeed no seeds", nil, faultline.Options{NoCryptoSeed: true},
		"etcdraft: Options.NoCryptoSeed must be false: etcd/raft draws election timeouts from crypto/rand", false, true},
}

func init() { //nolint:gochecknoinits // registers the child-process scenarios
	// "testseeds": TestSeeds with FaultsNone for 20 s, Options.Seeds 7 and BaseSeed 9, and the
	// seeds of listedSeeds.
	scenarios["testseeds"] = func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Faults = FaultsNone
		TestSeeds(t, cfg, faultline.Options{Seeds: 7, BaseSeed: 9, Duration: 20 * time.Second}, listedSeeds...)
	}
	// "validate": row ETCDRAFT_ROW of validationRows under Test, or under TestSeeds with seed
	// 0x1 (no seed for a noSeeds row) when ETCDRAFT_FUNC is TestSeeds. FaultsNone keeps the
	// Config row's seed short.
	scenarios["validate"] = func(t *testing.T) {
		i, err := strconv.Atoi(os.Getenv("ETCDRAFT_ROW"))
		if err != nil {
			t.Fatalf("ETCDRAFT_ROW: %v", err)
		}
		r := validationRows[i]
		cfg := DefaultConfig()
		cfg.Faults = FaultsNone
		if r.cfg != nil {
			r.cfg(&cfg)
		}
		switch {
		case os.Getenv("ETCDRAFT_FUNC") != "TestSeeds":
			Test(t, cfg, r.opts)
		case r.noSeeds:
			TestSeeds(t, cfg, r.opts)
		default:
			TestSeeds(t, cfg, r.opts, 0x1)
		}
	}
	// "selftest-seeds": TestSeeds with SelfTestConfig(BugSkipSync) and the ETC-192 options of
	// the "selftest" scenario, for the seeds ETCDRAFT_SEED^1 and ETCDRAFT_SEED, in that order.
	scenarios["selftest-seeds"] = func(t *testing.T) {
		seed, err := strconv.ParseUint(os.Getenv("ETCDRAFT_SEED"), 0, 64)
		if err != nil {
			t.Fatalf("ETCDRAFT_SEED: %v", err)
		}
		TestSeeds(t, SelfTestConfig(BugSkipSync), selfTestOptions(50), seed^1, seed)
	}
}

// ETC-025, API-110, API-111, AT-ETC-31: TestSeeds runs exactly the listed seeds, in the order
// given. Options.Seeds, Options.BaseSeed and FAULTLINE_SEED do not change the list, and the line
// that says FAULTLINE_SEED is ignored names the test's own file.
func TestTestSeedsRunsListedSeeds(t *testing.T) {
	results := resultsFile(t)
	out, code := runScenario(t, "testseeds", []string{"FAULTLINE_SEED=0x9", "FAULTLINE_RESULTS=" + results}, "-test.v")
	rs := readResults(t, results)
	var got []string
	for _, r := range rs {
		got = append(got, fmt.Sprintf("%s index %d %s", r.Seed, r.Index, r.Status))
	}
	var want []string
	for i, s := range listedSeeds {
		want = append(want, fmt.Sprintf("0x%016x index %d pass", s, i))
	}
	if code != 0 || strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Fatalf("exit %d, seeds %q; want 0 and %q\n%s", code, got, want, tail(out))
	}
	ignored := regexp.MustCompile(`\stestseeds_test\.go:\d+: faultline: RunSeeds ignores FAULTLINE_SEED; it runs the seeds listed in the test\n`)
	if len(ignored.FindAllString(out, -1)) != 1 {
		t.Errorf("want one line at the TestSeeds call saying FAULTLINE_SEED is ignored\n%s", out)
	}
}

// ETC-025, ETC-022, ETC-013, AT-ETC-31: the errors Test reports for bad Options or a bad Config
// fail TestSeeds the same way. ETC-022's errors fail t at the call, in the caller's file, before
// any seed runs; the Config error fails the first seed with kind setup. TestSeeds with no seeds
// fails like an ETC-022 error and does not fall back to Options.Seeds.
func TestTestSeedsValidates(t *testing.T) {
	for i, r := range validationRows {
		fns := []string{"Test", "TestSeeds"}
		if r.noSeeds {
			fns = fns[1:]
		}
		for _, fn := range fns {
			t.Run(r.name+"/"+fn, func(t *testing.T) {
				results := resultsFile(t)
				out, code := runScenario(t, "validate", []string{"ETCDRAFT_ROW=" + strconv.Itoa(i), "ETCDRAFT_FUNC=" + fn, "FAULTLINE_RESULTS=" + results})
				rs := readResults(t, results)
				if code != 1 || !strings.Contains(out, r.msg+"\n") {
					t.Fatalf("exit %d; want 1 and %q\n%s", code, r.msg, tail(out))
				}
				if r.seed {
					if len(rs) != 1 || rs[0].Kind != "setup" || (fn == "TestSeeds" && rs[0].Seed != "0x0000000000000001") {
						t.Errorf("results %+v; want one seed failing with kind setup (seed 0x1 for TestSeeds)", rs)
					}
					return
				}
				at := regexp.MustCompile(`\stestseeds_test\.go:\d+: ` + regexp.QuoteMeta(r.msg) + "\n")
				if len(rs) != 0 || strings.Contains(out, "seed=") || !at.MatchString(out) {
					t.Errorf("results %+v; want no seed and the error at the %s call\n%s", rs, fn, tail(out))
				}
			})
		}
	}
}

// ETC-025, API-114, §5.16, AT-ETC-31: a seed that fails under Test with a bug switch on fails
// under TestSeeds with the same check and trace hash. The printed replay command sets no variable
// and selects the seed's subtest with -run, and running it reproduces the failure.
func TestTestSeedsReplaysSelfTestSeed(t *testing.T) {
	rs, _, out, code := runSelfTest(t, BugSkipSync, 50)
	if code == 0 || len(rs) == 0 || rs[len(rs)-1].Status != "fail" {
		t.Fatalf("Test: exit %d, %d results; want a failing seed among the first 50\n%s", code, len(rs), tail(out))
	}
	first := rs[len(rs)-1]
	same := func(name string, r seedResult) {
		t.Helper()
		if r.Seed != first.Seed || r.Status != "fail" || r.Signature != first.Signature || r.TraceHash != first.TraceHash {
			t.Errorf("%s: seed %s %s %s, trace hash %s; want seed %s fail %s, trace hash %s", name, r.Seed, r.Status, r.Signature, r.TraceHash,
				first.Seed, first.Signature, first.TraceHash)
		}
	}

	results := resultsFile(t)
	env := []string{"ETCDRAFT_SEED=" + first.Seed}
	out, code = runScenario(t, "selftest-seeds", append(env, "FAULTLINE_RESULTS="+results))
	rs = readResults(t, results)
	seed, err := strconv.ParseUint(first.Seed, 0, 64)
	if err != nil {
		t.Fatalf("seed %s: %v", first.Seed, err)
	}
	other := fmt.Sprintf("0x%016x", seed^1)
	if code != 1 || len(rs) != 2 || rs[0].Seed != other || rs[1].Index != 1 {
		t.Fatalf("TestSeeds: exit %d, results %+v; want 1, seed %s first and seed %s at index 1\n%s", code, rs, other, first.Seed, tail(out))
	}
	same("TestSeeds", rs[1])

	replay := regexp.MustCompile(`\n *replay:    (.*)go test -v.* -run '(\^TestScenario\$/\^seed=` + first.Seed + `\$)' ` +
		regexp.QuoteMeta("github.com/hmdsefi/faultline/harness/etcdraft") + "\n")
	m := replay.FindStringSubmatch(out)
	if m == nil || m[1] != "" {
		t.Fatalf("replay command %q; want one that sets no variable and selects the subtest of seed %s\n%s", m, first.Seed, out)
	}
	results = resultsFile(t)
	out, code = runScenario(t, "selftest-seeds", append(env, "FAULTLINE_RESULTS="+results), "-test.run="+m[2])
	rs = readResults(t, results)
	if code != 1 || len(rs) != 1 || rs[0].Index != 1 {
		t.Fatalf("replay: exit %d, results %+v; want 1 and only seed %s, at index 1\n%s", code, rs, first.Seed, tail(out))
	}
	same("replay", rs[0])
}
