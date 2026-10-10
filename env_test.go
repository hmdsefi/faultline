// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
)

// fakeEnv returns a lookupFunc over m.
func fakeEnv(m map[string]string) lookupFunc {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func resolveWith(t *testing.T, opts Options, env map[string]string, short bool) (*plan, error) {
	t.Helper()
	return resolve(resolveInput{opts: opts, testName: "TestScenario", short: short, lookup: fakeEnv(env), exploreBase: func() uint64 { return 7 }})
}

// AT-API-53; API-012, API-013, API-017 items 6 and 7: a variable that cannot turn off what Options
// turned on is logged once, in API-017's order, and the option still wins.
func TestResolveEnvBelowOptions(t *testing.T) {
	const (
		running  = "faultline: running 1 seed from base 0xb83f592e2ee6cccf (test name)"
		check    = "faultline: FAULTLINE_CHECK_DETERMINISM=0 does not turn off Options.CheckDeterminism; the determinism check still runs (set Options.CheckDeterminism to false to turn it off)"
		trace    = "faultline: FAULTLINE_TRACE=hash does not lower Options.Trace.Level from kernel.TraceFull; runs still record full traces (set Options.Trace.Level to kernel.TraceHash to lower it)"
		minimize = "faultline: FAULTLINE_MINIMIZE is set, but minimization is not available in this version; ignoring"
	)
	full := kernel.TraceConfig{Level: kernel.TraceFull}
	cases := []struct {
		name string
		opts Options
		env  map[string]string
		logs []string
	}{
		{"check", Options{Seeds: 1, CheckDeterminism: true}, map[string]string{"FAULTLINE_CHECK_DETERMINISM": "0"}, []string{running, check}},
		{"trace", Options{Seeds: 1, Trace: full}, map[string]string{"FAULTLINE_TRACE": "hash"}, []string{running, trace}},
		{"both, then minimize", Options{Seeds: 1, CheckDeterminism: true, Trace: full}, map[string]string{"FAULTLINE_CHECK_DETERMINISM": " 0 ", "FAULTLINE_TRACE": "hash", "FAULTLINE_MINIMIZE": "1"}, []string{running, check, trace, minimize}},
		{"check, option off", Options{Seeds: 1}, map[string]string{"FAULTLINE_CHECK_DETERMINISM": "0"}, []string{running}},
		{"trace, option hash", Options{Seeds: 1}, map[string]string{"FAULTLINE_TRACE": "hash"}, []string{running}},
		{"variables that agree", Options{Seeds: 1, CheckDeterminism: true, Trace: full}, map[string]string{"FAULTLINE_CHECK_DETERMINISM": "1", "FAULTLINE_TRACE": "full"}, []string{running}},
		{"options alone", Options{Seeds: 1, CheckDeterminism: true, Trace: full}, nil, []string{running}},
	}
	for _, c := range cases {
		p, err := resolveWith(t, c.opts, c.env, false)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !slices.Equal(p.logs, c.logs) {
			t.Errorf("%s: logs %q\nwant %q", c.name, p.logs, c.logs)
		}
		if (c.opts.CheckDeterminism && !p.opts.CheckDeterminism) || (c.opts.Trace.Level == kernel.TraceFull && p.opts.Trace.Level != kernel.TraceFull) {
			t.Errorf("%s: a variable turned an option off: %+v", c.name, p.opts)
		}
	}
}

// derivedSeeds returns the first n seeds derived from base.
func derivedSeeds(base uint64, n int) []uint64 {
	out := make([]uint64, n)
	for i := range out {
		out[i] = DeriveSeed(base, i)
	}
	return out
}

func scenarioSeeds(n int) []uint64 {
	return derivedSeeds(NameBase("TestScenario"), n)
}

// API-001, API-002, API-011 to API-017 (AT-API-02 to AT-API-07 at the unit level).
func TestResolveSeeds(t *testing.T) {
	cases := []struct {
		name   string
		opts   Options
		env    map[string]string
		short  bool
		seeds  []uint64
		source string
		logs   []string
	}{
		{"defaults", Options{}, nil, false, scenarioSeeds(20), "derived",
			[]string{"faultline: running 20 seeds from base 0xb83f592e2ee6cccf (test name)"}},
		{"blank is unset", Options{Seeds: 1}, map[string]string{"FAULTLINE_SEED": "  ", "FAULTLINE_TRACE": ""}, false, scenarioSeeds(1), "derived",
			[]string{"faultline: running 1 seed from base 0xb83f592e2ee6cccf (test name)"}},
		// strings.TrimSpace: a value from a YAML block scalar ends in a newline.
		{"newline and tab trimmed", Options{}, map[string]string{"FAULTLINE_SEED": "\t42\n", "FAULTLINE_TRACE": "\n"}, false, []uint64{42}, "env",
			[]string{"faultline: FAULTLINE_SEED=0x000000000000002a: running 1 seed"}},
		{"short", Options{}, nil, true, scenarioSeeds(5), "derived",
			[]string{"faultline: running 5 seeds from base 0xb83f592e2ee6cccf (test name)", "faultline: -short: running 5 of 20 seeds"}},
		{"short at the cap", Options{Seeds: 5}, nil, true, scenarioSeeds(5), "derived",
			[]string{"faultline: running 5 seeds from base 0xb83f592e2ee6cccf (test name)"}},
		{"short below the cap", Options{Seeds: 3}, nil, true, scenarioSeeds(3), "derived",
			[]string{"faultline: running 3 seeds from base 0xb83f592e2ee6cccf (test name)"}},
		{"short with FAULTLINE_SEEDS", Options{}, map[string]string{"FAULTLINE_SEEDS": "7"}, true, scenarioSeeds(7), "derived",
			[]string{"faultline: running 7 seeds from base 0xb83f592e2ee6cccf (test name)"}},
		{"seed", Options{}, map[string]string{"FAULTLINE_SEED": " 42 ", "FAULTLINE_SEEDS": "9", "FAULTLINE_BASE_SEED": "2", "FAULTLINE_EXPLORE": "1"}, false, []uint64{42}, "env",
			[]string{"faultline: FAULTLINE_SEED is set; ignoring FAULTLINE_SEEDS", "faultline: FAULTLINE_SEED is set; ignoring FAULTLINE_BASE_SEED", "faultline: FAULTLINE_SEED is set; ignoring FAULTLINE_EXPLORE", "faultline: FAULTLINE_SEED=0x000000000000002a: running 1 seed"}},
		{"seed 0X, upper-case hex", Options{}, map[string]string{"FAULTLINE_SEED": "0X2A"}, false, []uint64{42}, "env",
			[]string{"faultline: FAULTLINE_SEED=0x000000000000002a: running 1 seed"}},
		{"options base", Options{Seeds: 3, BaseSeed: 1}, nil, false, []uint64{0x910a2dec89025cc1, 0xbeeb8da1658eec67, 0xf893a2eefb32555e}, "derived",
			[]string{"faultline: running 3 seeds from base 0x0000000000000001 (Options.BaseSeed)"}},
		{"env base", Options{Seeds: 3, BaseSeed: 1}, map[string]string{"FAULTLINE_BASE_SEED": "0x2a"}, false, []uint64{0xbdd732262feb6e95, 0x28efe333b266f103, 0x47526757130f9f52}, "derived",
			[]string{"faultline: running 3 seeds from base 0x000000000000002a (FAULTLINE_BASE_SEED)"}},
		{"env base beats explore", Options{Seeds: 3, BaseSeed: 1}, map[string]string{"FAULTLINE_BASE_SEED": "0x2a", "FAULTLINE_EXPLORE": "1"}, false, []uint64{0xbdd732262feb6e95, 0x28efe333b266f103, 0x47526757130f9f52}, "derived",
			[]string{"faultline: FAULTLINE_BASE_SEED is set; ignoring FAULTLINE_EXPLORE", "faultline: running 3 seeds from base 0x000000000000002a (FAULTLINE_BASE_SEED)"}},
		// FAULTLINE_BASE_SEED=B FAULTLINE_SEEDS=N runs the seeds of faultline sweep -base B -n N (API-015).
		{"env base and count", Options{Seeds: 9}, map[string]string{"FAULTLINE_BASE_SEED": "0x2a", "FAULTLINE_SEEDS": "3"}, true, derivedSeeds(0x2a, 3), "derived",
			[]string{"faultline: running 3 seeds from base 0x000000000000002a (FAULTLINE_BASE_SEED)"}},
		// The explore line's rerun with FAULTLINE_BASE_SEED gets the same -short cap.
		{"env base under -short", Options{}, map[string]string{"FAULTLINE_BASE_SEED": "0x2a"}, true, derivedSeeds(0x2a, 5), "derived",
			[]string{"faultline: running 5 seeds from base 0x000000000000002a (FAULTLINE_BASE_SEED)", "faultline: -short: running 5 of 20 seeds"}},
		{"explore", Options{Seeds: 1}, map[string]string{"FAULTLINE_EXPLORE": "1"}, false, []uint64{DeriveSeed(7, 0)}, "derived",
			[]string{"faultline: running 1 seed from base 0x0000000000000007 (FAULTLINE_EXPLORE)", "faultline: FAULTLINE_EXPLORE: base seed 0x0000000000000007 (rerun this set with FAULTLINE_BASE_SEED=0x0000000000000007)"}},
		{"short, then explore", Options{}, map[string]string{"FAULTLINE_EXPLORE": "1"}, true, derivedSeeds(7, 5), "derived",
			[]string{"faultline: running 5 seeds from base 0x0000000000000007 (FAULTLINE_EXPLORE)", "faultline: -short: running 5 of 20 seeds", "faultline: FAULTLINE_EXPLORE: base seed 0x0000000000000007 (rerun this set with FAULTLINE_BASE_SEED=0x0000000000000007)"}},
		// The rerun hint repeats FAULTLINE_SEEDS, or the rerun would run the default count (API-016).
		{"explore with FAULTLINE_SEEDS", Options{}, map[string]string{"FAULTLINE_EXPLORE": "1", "FAULTLINE_SEEDS": "3"}, true, derivedSeeds(7, 3), "derived",
			[]string{"faultline: running 3 seeds from base 0x0000000000000007 (FAULTLINE_EXPLORE)", "faultline: FAULTLINE_EXPLORE: base seed 0x0000000000000007 (rerun this set with FAULTLINE_BASE_SEED=0x0000000000000007 FAULTLINE_SEEDS=3)"}},
		// Full traces with artifacts off write no artifacts, so Run says so (API-017 item 5), before
		// the minimize line of API-092.
		{"full traces, artifacts off", Options{Seeds: 1}, map[string]string{"FAULTLINE_TRACE": "full", "FAULTLINE_ARTIFACTS": "Off", "FAULTLINE_MINIMIZE": "1"}, false, scenarioSeeds(1), "derived",
			[]string{"faultline: running 1 seed from base 0xb83f592e2ee6cccf (test name)", "faultline: FAULTLINE_TRACE=full writes no artifacts while FAULTLINE_ARTIFACTS is off; unset one of them", "faultline: FAULTLINE_MINIMIZE is set, but minimization is not available in this version; ignoring"}},
		{"full traces, artifacts on", Options{Seeds: 1}, map[string]string{"FAULTLINE_TRACE": "full", "FAULTLINE_ARTIFACTS": "/tmp/x"}, false, scenarioSeeds(1), "derived",
			[]string{"faultline: running 1 seed from base 0xb83f592e2ee6cccf (test name)"}},
		{"hash traces, artifacts off", Options{Seeds: 1}, map[string]string{"FAULTLINE_TRACE": "hash", "FAULTLINE_ARTIFACTS": "off"}, false, scenarioSeeds(1), "derived",
			[]string{"faultline: running 1 seed from base 0xb83f592e2ee6cccf (test name)"}},
		// -short does not cap a seed list.
		{"seed list", Options{}, map[string]string{"FAULTLINE_SEED_LIST": "0x1,0x2,0x1,3,4,5,6", "FAULTLINE_SEEDS": "3", "FAULTLINE_BASE_SEED": "2", "FAULTLINE_EXPLORE": "0"}, true, []uint64{1, 2, 3, 4, 5, 6}, "list",
			[]string{"faultline: FAULTLINE_SEED_LIST is set; ignoring FAULTLINE_SEEDS", "faultline: FAULTLINE_SEED_LIST is set; ignoring FAULTLINE_BASE_SEED", "faultline: FAULTLINE_SEED_LIST is set; ignoring FAULTLINE_EXPLORE", "faultline: FAULTLINE_SEED_LIST: running 6 seeds"}},
		{"minimize", Options{Seeds: 1}, map[string]string{"FAULTLINE_MINIMIZE": "runs=3,out=/tmp/a,b"}, false, scenarioSeeds(1), "derived",
			[]string{"faultline: running 1 seed from base 0xb83f592e2ee6cccf (test name)", "faultline: FAULTLINE_MINIMIZE is set, but minimization is not available in this version; ignoring"}},
		{"minimize 0", Options{Seeds: 1}, map[string]string{"FAULTLINE_MINIMIZE": "0"}, false, scenarioSeeds(1), "derived",
			[]string{"faultline: running 1 seed from base 0xb83f592e2ee6cccf (test name)"}},
		// Decimal values are base 10 even with leading zeros, and CR LF separates seed-list entries.
		{"leading zeros", Options{}, map[string]string{"FAULTLINE_SEEDS": "010"}, false, scenarioSeeds(10), "derived",
			[]string{"faultline: running 10 seeds from base 0xb83f592e2ee6cccf (test name)"}},
		{"CRLF list", Options{}, map[string]string{"FAULTLINE_SEED_LIST": "0042\r\n2\r\n"}, false, []uint64{42, 2}, "list",
			[]string{"faultline: FAULTLINE_SEED_LIST: running 2 seeds"}},
		// One seed, after duplicates are removed, is "1 seed".
		{"list of one seed", Options{}, map[string]string{"FAULTLINE_SEED_LIST": "0x7,7"}, false, []uint64{7}, "list",
			[]string{"faultline: FAULTLINE_SEED_LIST: running 1 seed"}},
	}
	for _, c := range cases {
		p, err := resolveWith(t, c.opts, c.env, c.short)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !slices.Equal(p.seeds, c.seeds) || p.seedSource != c.source || !slices.Equal(p.logs, c.logs) {
			t.Errorf("%s: seeds %x source %s logs %q\nwant %x %s %q", c.name, p.seeds, p.seedSource, p.logs, c.seeds, c.source, c.logs)
		}
		if p.opts.Seeds != len(c.seeds) {
			t.Errorf("%s: Options().Seeds = %d", c.name, p.opts.Seeds)
		}
	}
}

// API-010: FAULTLINE_SEEDS accepts 1 to MaxSeeds.
func TestResolveSeedsBounds(t *testing.T) {
	for _, n := range []int{1, MaxSeeds} {
		p, err := resolveWith(t, Options{}, map[string]string{"FAULTLINE_SEEDS": fmt.Sprint(n)}, false)
		if err != nil {
			t.Errorf("FAULTLINE_SEEDS=%d: %v", n, err)
		} else if len(p.seeds) != n || p.seeds[n-1] != DeriveSeed(NameBase("TestScenario"), n-1) {
			t.Errorf("FAULTLINE_SEEDS=%d: %d seeds", n, len(p.seeds))
		}
	}
}

// API-015: the base seed's precedence and its source (report base_source).
func TestResolveBaseSource(t *testing.T) {
	cases := []struct {
		name   string
		opts   Options
		env    map[string]string
		base   uint64
		source string
	}{
		{"test name", Options{}, nil, NameBase("TestScenario"), "test_name"},
		{"options", Options{BaseSeed: 1}, nil, 1, "options"},
		{"explore beats options", Options{BaseSeed: 1}, map[string]string{"FAULTLINE_EXPLORE": "1"}, 7, "explore"},
		{"FAULTLINE_EXPLORE=0", Options{}, map[string]string{"FAULTLINE_EXPLORE": "0"}, NameBase("TestScenario"), "test_name"},
		{"env beats options", Options{BaseSeed: 1}, map[string]string{"FAULTLINE_BASE_SEED": "0x2a"}, 0x2a, "env"},
		{"FAULTLINE_BASE_SEED=0", Options{BaseSeed: 1}, map[string]string{"FAULTLINE_BASE_SEED": "0"}, 0, "env"},
	}
	for _, c := range cases {
		c.opts.Seeds = 1
		p, err := resolveWith(t, c.opts, c.env, false)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if p.base != c.base || p.baseSource != c.source || p.seeds[0] != DeriveSeed(c.base, 0) || p.opts.BaseSeed != c.base {
			t.Errorf("%s: base %#x source %q seed %#x Options().BaseSeed %#x; want base %#x source %q", c.name, p.base, p.baseSource, p.seeds[0], p.opts.BaseSeed, c.base, c.source)
		}
	}
	// exploreBase is called once, and the explore line shows the base that was used (API-016).
	var calls uint64
	p, err := resolve(resolveInput{testName: "T", lookup: fakeEnv(map[string]string{"FAULTLINE_EXPLORE": "1", "FAULTLINE_SEEDS": "1"}),
		exploreBase: func() uint64 { calls++; return calls * 7 }})
	if err != nil {
		t.Fatal(err)
	}
	if want := "faultline: FAULTLINE_EXPLORE: base seed 0x0000000000000007 (rerun this set with FAULTLINE_BASE_SEED=0x0000000000000007 FAULTLINE_SEEDS=1)"; calls != 1 || p.base != 7 || p.logs[len(p.logs)-1] != want {
		t.Errorf("explore: %d calls, base %#x, logs %q", calls, p.base, p.logs)
	}
}

func TestResolveEffectiveOptions(t *testing.T) {
	p, err := resolveWith(t, Options{Seeds: 2, KeepGoing: false}, map[string]string{"FAULTLINE_TRACE": "full", "FAULTLINE_CHECK_DETERMINISM": "1", "FAULTLINE_SEED_LIST": "5"}, false)
	if err != nil {
		t.Fatal(err)
	}
	o := p.opts
	if o.Duration != DefaultDuration || o.MaxEvents != DefaultMaxEvents || o.Net.Default.Latency != time.Millisecond {
		t.Errorf("defaults not applied: %+v", o)
	}
	if o.Trace.Level != kernel.TraceFull || !p.passArtifacts || !o.CheckDeterminism || !p.envCheck || !o.KeepGoing || o.BaseSeed != 0 || o.Seeds != 1 {
		t.Errorf("effective options: %+v, check from the environment %v", o, p.envCheck)
	}
	p2, _ := resolveWith(t, Options{Trace: kernel.TraceConfig{Level: kernel.TraceFull, Buffer: 9}}, map[string]string{"FAULTLINE_TRACE": "hash", "FAULTLINE_CHECK_DETERMINISM": "0"}, false)
	if p2.opts.Trace.Level != kernel.TraceFull || p2.opts.Trace.Buffer != 9 || p2.passArtifacts || p2.opts.CheckDeterminism || p2.opts.BaseSeed != NameBase("TestScenario") {
		t.Errorf("hash must not downgrade: %+v", p2.opts)
	}
	// full keeps Trace.Buffer, the option side of each OR counts, and BaseSeed is 0 for FAULTLINE_SEED.
	p3, err := resolveWith(t, Options{Trace: kernel.TraceConfig{Buffer: 9}, CheckDeterminism: true, KeepGoing: true, BaseSeed: 5}, map[string]string{"FAULTLINE_TRACE": "full", "FAULTLINE_CHECK_DETERMINISM": "0", "FAULTLINE_SEED": "1"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if o := p3.opts; o.Trace != (kernel.TraceConfig{Level: kernel.TraceFull, Buffer: 9}) || !p3.passArtifacts || !o.CheckDeterminism || p3.envCheck || !o.KeepGoing || o.BaseSeed != 0 {
		t.Errorf("options with FAULTLINE_TRACE=full and FAULTLINE_SEED: %+v, check from the environment %v", o, p3.envCheck)
	}
	// Options.CheckDeterminism already turns the check on: the replay needs no variable (API-080).
	p5, err := resolveWith(t, Options{CheckDeterminism: true}, map[string]string{"FAULTLINE_CHECK_DETERMINISM": "1"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !p5.checkDeterminism || p5.envCheck {
		t.Errorf("CheckDeterminism in both: check %v, from the environment %v", p5.checkDeterminism, p5.envCheck)
	}
	// Only FAULTLINE_SEED_LIST forces KeepGoing (API-013), not FAULTLINE_SEED.
	p4, err := resolveWith(t, Options{}, map[string]string{"FAULTLINE_SEED": "1"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if p4.opts.KeepGoing || p4.keepGoing {
		t.Errorf("FAULTLINE_SEED forced KeepGoing: %+v", p4.opts)
	}
}

// AT-API-05, AT-API-41 and §7.1 at the unit level.
func TestResolveErrors(t *testing.T) {
	dir := t.TempDir()
	artifactsErr := func(v string) string {
		return fmt.Sprintf("faultline: invalid FAULTLINE_ARTIFACTS value %q: artifacts are on by default; set a directory path for the artifact root, or off to turn artifacts off", v)
	}
	cases := []struct {
		opts Options
		env  map[string]string
		want string
	}{
		{Options{}, map[string]string{"FAULTLINE_SEED": "0xZZ"}, `faultline: invalid FAULTLINE_SEED value "0xZZ": want a decimal or 0x-prefixed hexadecimal uint64`},
		{Options{}, map[string]string{"FAULTLINE_SEED": "-1"}, `faultline: invalid FAULTLINE_SEED value "-1": want a decimal or 0x-prefixed hexadecimal uint64`},
		{Options{}, map[string]string{"FAULTLINE_SEED": "0x"}, `faultline: invalid FAULTLINE_SEED value "0x": want a decimal or 0x-prefixed hexadecimal uint64`},
		{Options{}, map[string]string{"FAULTLINE_SEED": "0x1_0"}, `faultline: invalid FAULTLINE_SEED value "0x1_0": want a decimal or 0x-prefixed hexadecimal uint64`},
		{Options{}, map[string]string{"FAULTLINE_SEED": "18446744073709551616"}, `faultline: invalid FAULTLINE_SEED value "18446744073709551616": want a decimal or 0x-prefixed hexadecimal uint64`},
		{Options{}, map[string]string{"FAULTLINE_BASE_SEED": "x"}, `faultline: invalid FAULTLINE_BASE_SEED value "x": want a decimal or 0x-prefixed hexadecimal uint64`},
		{Options{}, map[string]string{"FAULTLINE_SEEDS": "0"}, `faultline: invalid FAULTLINE_SEEDS value "0": want a decimal integer from 1 to 1000000`},
		{Options{}, map[string]string{"FAULTLINE_SEEDS": "1000001"}, `faultline: invalid FAULTLINE_SEEDS value "1000001": want a decimal integer from 1 to 1000000`},
		// validated although FAULTLINE_SEED wins (API-010)
		{Options{}, map[string]string{"FAULTLINE_SEED": "1", "FAULTLINE_SEEDS": "0"}, `faultline: invalid FAULTLINE_SEEDS value "0": want a decimal integer from 1 to 1000000`},
		{Options{}, map[string]string{"FAULTLINE_EXPLORE": "yes"}, `faultline: invalid FAULTLINE_EXPLORE value "yes": want 0 or 1`},
		{Options{}, map[string]string{"FAULTLINE_CHECK_DETERMINISM": "true"}, `faultline: invalid FAULTLINE_CHECK_DETERMINISM value "true": want 0 or 1`},
		{Options{}, map[string]string{"FAULTLINE_TRACE": "Full"}, `faultline: invalid FAULTLINE_TRACE value "Full": want hash or full`},
		{Options{}, map[string]string{"FAULTLINE_ARTIFACTS": "0"}, artifactsErr("0")},
		{Options{}, map[string]string{"FAULTLINE_ARTIFACTS": "False"}, artifactsErr("False")},
		{Options{}, map[string]string{"FAULTLINE_ARTIFACTS": " no "}, artifactsErr("no")},
		{Options{}, map[string]string{"FAULTLINE_ARTIFACTS": "1"}, artifactsErr("1")},
		{Options{}, map[string]string{"FAULTLINE_ARTIFACTS": "TRUE"}, artifactsErr("TRUE")},
		{Options{}, map[string]string{"FAULTLINE_ARTIFACTS": "Yes"}, artifactsErr("Yes")},
		{Options{}, map[string]string{"FAULTLINE_ARTIFACTS": " on "}, artifactsErr("on")},
		{Options{}, map[string]string{"FAULTLINE_SEED_LIST": "1,,0xZZ"}, `faultline: invalid FAULTLINE_SEED_LIST entry 2 "0xZZ" on line 1: want decimal or 0x-prefixed hexadecimal uint64 seeds separated by commas or white space`},
		{Options{}, map[string]string{"FAULTLINE_SEED_LIST": "1\r\n\n2 3,\n0xZZ"}, `faultline: invalid FAULTLINE_SEED_LIST entry 4 "0xZZ" on line 4: want decimal or 0x-prefixed hexadecimal uint64 seeds separated by commas or white space`},
		{Options{}, map[string]string{"FAULTLINE_SEED_LIST": " , "}, `faultline: FAULTLINE_SEED_LIST contains no seeds; want decimal or 0x-prefixed hexadecimal uint64 seeds separated by commas or white space`},
		{Options{}, map[string]string{"FAULTLINE_SEED": "1", "FAULTLINE_SEED_LIST": "2"}, `faultline: FAULTLINE_SEED and FAULTLINE_SEED_LIST are both set; set only one`},
		{Options{}, map[string]string{"FAULTLINE_SCHEDULE": "/nonexistent.json"}, `faultline: FAULTLINE_SCHEDULE=/nonexistent.json: open /nonexistent.json: no such file or directory`},
		{Options{}, map[string]string{"FAULTLINE_MINIMIZE": "fast"}, `faultline: invalid FAULTLINE_MINIMIZE "fast": parameter "fast" is not key=value`},
		{Options{}, map[string]string{"FAULTLINE_SWARM": "0"}, `faultline: FAULTLINE_SWARM is set, but swarm testing is not in this release yet; unset it, and see the roadmap at https://github.com/hmdsefi/faultline/issues/167`},
		{Options{}, map[string]string{"FAULTLINE_SWARM_CONFIG": "/x.json"}, `faultline: FAULTLINE_SWARM_CONFIG is set, but swarm testing is not in this release yet; unset it, and see the roadmap at https://github.com/hmdsefi/faultline/issues/167`},
		{Options{}, map[string]string{"FAULTLINE_EXACT": "run"}, `faultline: FAULTLINE_EXACT is set, but exact replay in goroutine mode is not in this release yet; unset it, and see the roadmap at https://github.com/hmdsefi/faultline/issues/167`},
		{Options{Seeds: -1}, nil, `faultline: Options.Seeds is -1; want 0 (default 20) or more`},
		{Options{Seeds: MaxSeeds + 1}, nil, `faultline: Options.Seeds is 1000001; want at most 1000000`},
		{Options{Duration: -time.Second}, nil, `faultline: Options.Duration is -1s; want 0 (default 1m0s) or more`},
		{Options{Trace: kernel.TraceConfig{Level: 7}}, nil, `faultline: Options.Trace.Level is 7; want kernel.TraceHash or kernel.TraceFull`},
		{Options{Trace: kernel.TraceConfig{Buffer: -1}}, nil, `faultline: Options.Trace.Buffer is -1; want 0 (unbounded) or more`},
		{Options{Mode: ModeGoroutine}, nil, `faultline: Options.Mode is ModeGoroutine, but goroutine mode is not in this release yet; set it to ModeEvent, and see the roadmap at https://github.com/hmdsefi/faultline/issues/167`},
		{Options{Mode: 9}, nil, `faultline: unknown Options.Mode 9; want ModeEvent (the zero value)`},
		{Options{Procs: 2}, nil, `faultline: Options.Procs is set, but goroutine mode is not in this release yet; set it to 0, and see the roadmap at https://github.com/hmdsefi/faultline/issues/167`},
		{Options{Drain: time.Second}, nil, `faultline: Options.Drain is set, but goroutine mode is not in this release yet; set it to 0, and see the roadmap at https://github.com/hmdsefi/faultline/issues/167`},
		{Options{StallTimeout: -1}, nil, `faultline: Options.StallTimeout is set, but goroutine mode is not in this release yet; set it to 0, and see the roadmap at https://github.com/hmdsefi/faultline/issues/167`},
		{Options{FailOnLeak: true}, nil, `faultline: Options.FailOnLeak is set, but goroutine mode is not in this release yet; set it to false, and see the roadmap at https://github.com/hmdsefi/faultline/issues/167`},
		{Options{Swarm: true}, nil, `faultline: Options.Swarm is set, but swarm testing is not in this release yet; set it to false, and see the roadmap at https://github.com/hmdsefi/faultline/issues/167`},
		// table order: FAULTLINE_SEED is checked before FAULTLINE_TRACE, env before options
		{Options{Seeds: -1}, map[string]string{"FAULTLINE_TRACE": "x", "FAULTLINE_SEED": "y"}, `faultline: invalid FAULTLINE_SEED value "y": want a decimal or 0x-prefixed hexadecimal uint64`},
	}
	for _, c := range cases {
		_, err := resolveWith(t, c.opts, c.env, false)
		if err == nil || err.Error() != c.want {
			t.Errorf("opts %+v env %v: err = %v\nwant %s", c.opts, c.env, err, c.want)
		}
	}
	// The cap holds under -short too, which would otherwise cut the count to 5 first.
	if _, err := resolveWith(t, Options{Seeds: MaxSeeds + 1}, nil, true); err == nil || err.Error() != "faultline: Options.Seeds is 1000001; want at most 1000000" {
		t.Errorf("Options.Seeds above MaxSeeds under -short: err = %v", err)
	}
	listFile := filepath.Join(dir, "seeds.txt")
	if err := os.WriteFile(listFile, []byte("0x1\n0x2 3,0x2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := resolveWith(t, Options{}, map[string]string{"FAULTLINE_SEED_LIST": "@" + listFile}, false)
	if err != nil || !slices.Equal(p.seeds, []uint64{1, 2, 3}) {
		t.Fatalf("@file: %v %v", p, err)
	}
	if _, err := resolveWith(t, Options{}, map[string]string{"FAULTLINE_SEED_LIST": "@" + filepath.Join(dir, "missing")}, false); err == nil || !strings.HasPrefix(err.Error(), "faultline: FAULTLINE_SEED_LIST: open ") {
		t.Fatalf("missing @file: %v", err)
	}
	// An @file error names the file and the line, so a long list can be fixed (§7.1).
	for _, c := range []struct{ content, want string }{
		{"0x1\n0x2 zz,0x2\n", `faultline: invalid FAULTLINE_SEED_LIST entry 3 "zz" on line 2 of ` + listFile + `: ` + "want decimal or 0x-prefixed hexadecimal uint64 seeds separated by commas or white space"},
		{"\n , \n", `faultline: FAULTLINE_SEED_LIST file ` + listFile + ` contains no seeds; ` + "want decimal or 0x-prefixed hexadecimal uint64 seeds separated by commas or white space"},
	} {
		if err := os.WriteFile(listFile, []byte(c.content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveWith(t, Options{}, map[string]string{"FAULTLINE_SEED_LIST": "@" + listFile}, false); err == nil || err.Error() != c.want {
			t.Errorf("@file %q: err = %v\nwant %s", c.content, err, c.want)
		}
	}
}

// API-010: the first invalid variable in table order is the error. Each variable is set to an
// invalid value together with every later one. FAULTLINE_RESULTS accepts any value, so it is not
// listed.
func TestResolveErrorOrder(t *testing.T) {
	invalid := [][2]string{
		{"FAULTLINE_SEED", "x"},
		{"FAULTLINE_SEEDS", "0"},
		{"FAULTLINE_BASE_SEED", "x"},
		{"FAULTLINE_EXPLORE", "2"},
		{"FAULTLINE_SCHEDULE", "/nonexistent.json"},
		{"FAULTLINE_ARTIFACTS", "0"},
		{"FAULTLINE_CHECK_DETERMINISM", "2"},
		{"FAULTLINE_TRACE", "x"},
		{"FAULTLINE_MINIMIZE", "x"},
		{"FAULTLINE_SWARM", "0"},
		{"FAULTLINE_SWARM_CONFIG", "/x.json"},
		{"FAULTLINE_EXACT", "run"},
		{"FAULTLINE_SEED_LIST", "x"},
	}
	for i, v := range invalid {
		_, alone := resolveWith(t, Options{}, map[string]string{v[0]: v[1]}, false)
		env := map[string]string{}
		for _, later := range invalid[i:] {
			env[later[0]] = later[1]
		}
		_, err := resolveWith(t, Options{}, env, false)
		if alone == nil || err == nil || err.Error() != alone.Error() {
			t.Errorf("%s and every later variable invalid: err = %v\nwant %v", v[0], err, alone)
		}
	}
}

// API-050 and API-017 item 4: FAULTLINE_SCHEDULE is made absolute, read, parsed, hashed over the
// file content and logged; a file that does not parse is a setup error.
func TestResolveSchedule(t *testing.T) {
	var buf bytes.Buffer
	s := fault.Schedule{Version: 1, Events: []fault.Event{{At: kernel.Time(time.Second), Kind: fault.KindPause, Node: "n1"}}}
	if err := s.Write(&buf); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "schedule.json")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(cwd, path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := resolveWith(t, Options{Seeds: 1}, map[string]string{"FAULTLINE_SCHEDULE": rel}, false)
	if err != nil {
		t.Fatal(err)
	}
	if p.env.schedulePath != path || p.env.schedule == nil || len(p.env.schedule.Events) != 1 {
		t.Errorf("schedule %q %+v, want %q with 1 event", p.env.schedulePath, p.env.schedule, path)
	}
	if want := fmt.Sprintf("0x%016x", NameBase(buf.String())); p.env.scheduleHash != want {
		t.Errorf("schedule hash %s, want %s", p.env.scheduleHash, want)
	}
	if want := "faultline: FAULTLINE_SCHEDULE=" + path + ": 1 events; planners are disabled"; p.logs[len(p.logs)-1] != want {
		t.Errorf("logs %q, want the last one %q", p.logs, want)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveWith(t, Options{}, map[string]string{"FAULTLINE_SCHEDULE": bad}, false); err == nil || !strings.HasPrefix(err.Error(), "faultline: FAULTLINE_SCHEDULE="+bad+": fault: schedule: ") {
		t.Errorf("unparsable schedule: %v", err)
	}
	// A relative path is named absolute in the error too.
	relBad, err := filepath.Rel(cwd, bad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolveWith(t, Options{}, map[string]string{"FAULTLINE_SCHEDULE": relBad}, false); err == nil || !strings.HasPrefix(err.Error(), "faultline: FAULTLINE_SCHEDULE="+bad+": fault: schedule: ") {
		t.Errorf("unparsable schedule at a relative path: %v", err)
	}
}

// API-010: FAULTLINE_ARTIFACTS is off in any letter case or a path, FAULTLINE_RESULTS is a path,
// and relative paths are made absolute.
func TestResolveArtifactsAndResults(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"off", "OFF", " Off "} {
		p, err := resolveWith(t, Options{}, map[string]string{"FAULTLINE_ARTIFACTS": v}, false)
		if err != nil {
			t.Fatal(err)
		}
		if !p.env.artifactsOff || p.env.artifactsRoot != "" {
			t.Errorf("FAULTLINE_ARTIFACTS=%q: off %v, root %q", v, p.env.artifactsOff, p.env.artifactsRoot)
		}
	}
	// Only the bare words are rejected: ./0 is a folder named 0.
	p, err := resolveWith(t, Options{}, map[string]string{"FAULTLINE_ARTIFACTS": "./0"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if p.env.artifactsOff || p.env.artifactsRoot != filepath.Join(cwd, "0") {
		t.Errorf("FAULTLINE_ARTIFACTS=./0: off %v, root %q", p.env.artifactsOff, p.env.artifactsRoot)
	}
	p, err = resolveWith(t, Options{}, map[string]string{"FAULTLINE_ARTIFACTS": "rel", "FAULTLINE_RESULTS": " r.jsonl "}, false)
	if err != nil {
		t.Fatal(err)
	}
	if p.env.artifactsOff || p.env.artifactsRoot != filepath.Join(cwd, "rel") || p.env.results != filepath.Join(cwd, "r.jsonl") {
		t.Errorf("relative paths: off %v, root %q, results %q", p.env.artifactsOff, p.env.artifactsRoot, p.env.results)
	}
	if got := resultsPath(fakeEnv(map[string]string{"FAULTLINE_RESULTS": " r.jsonl "})); got != filepath.Join(cwd, "r.jsonl") {
		t.Errorf("resultsPath = %q", got)
	}
	if got := resultsPath(fakeEnv(map[string]string{"FAULTLINE_RESULTS": " "})); got != "" {
		t.Errorf("resultsPath of a blank value = %q", got)
	}
}

// MIN §4.1 grammar and reasons (AT-MIN-25 values), validated from Phase 1 on (API-092).
func TestParseMinimize(t *testing.T) {
	valid := map[string]bool{"0": false, "1": true, "runs=200": true, "time=0,runs=9": true, "runs=3,out=/tmp/a,b": true}
	for v, want := range valid {
		on, reason := parseMinimize(v)
		if reason != "" || on != want {
			t.Errorf("parseMinimize(%q) = %v, %q", v, on, reason)
		}
	}
	invalid := map[string]string{
		"true":          `parameter "true" is not key=value`,
		"false":         `parameter "false" is not key=value`,
		"fast":          `parameter "fast" is not key=value`,
		"runs=0":        "runs=0: want a positive integer",
		"runs=x":        "runs=x: want a positive integer",
		"runs=+5":       "runs=+5: want a positive integer",
		"time=-1s":      "time=-1s: want a duration of 0 or more",
		"out=rel/dir":   "out=rel/dir: want an absolute path",
		"x=1":           `unknown parameter "x"`,
		"runs=1,":       "empty parameter after ','",
		"runs=1,runs=2": "parameter runs given twice",
		"runs=1,runs=x": "parameter runs given twice",
		// The duplicate check runs before any key or value is looked at.
		"x=1,x=2":       "parameter x given twice",
		"runs=0,runs=1": "parameter runs given twice",
		// Then the parameters are checked left to right, and the first problem wins.
		"runs=0,time=-1s": "runs=0: want a positive integer",
	}
	for v, want := range invalid {
		if _, reason := parseMinimize(v); reason != want {
			t.Errorf("parseMinimize(%q) reason %q, want %q", v, reason, want)
		}
	}
}

func BenchmarkResolveSeeds(b *testing.B) {
	env := fakeEnv(map[string]string{"FAULTLINE_SEEDS": "1000"})
	for b.Loop() {
		if _, err := resolve(resolveInput{testName: "TestKV", lookup: env, exploreBase: func() uint64 { return 0 }}); err != nil {
			b.Fatal(err)
		}
	}
}
