// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package detlint

import (
	"slices"
	"strings"
	"testing"
)

// AT-DET-03
func TestClassRules(t *testing.T) {
	all := []Rule{RuleWallClock, RuleGlobalRand, RuleCryptoRand, RuleGo, RuleSelect, RuleMapRange,
		RuleFloat, RuleEnv, RuleUnordered, RuleMapIter, RulePlatform}
	cases := []struct {
		c    Class
		want []Rule
	}{
		{ClassCore, all},
		{ClassTool, all},
		{ClassEntry, []Rule{RuleGlobalRand, RuleCryptoRand, RuleMapRange, RuleUnordered, RuleMapIter, RulePlatform}},
		{ClassShim, []Rule{RuleGlobalRand, RuleCryptoRand, RuleMapRange, RuleFloat, RuleEnv, RuleUnordered, RuleMapIter, RulePlatform}},
		{ClassOutput, []Rule{RuleGlobalRand, RuleCryptoRand, RuleMapRange, RuleEnv, RuleUnordered, RuleMapIter, RulePlatform}},
		{ClassExempt, nil},
	}
	for _, c := range cases {
		if got := c.c.Rules(); !slices.Equal(got, c.want) {
			t.Errorf("%v.Rules() = %v, want %v", c.c, got, c.want)
		}
	}
	fs, err := LintDir("testdata/src/dl001", "fixture/dl001", ClassEntry)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 0 {
		t.Errorf("dl001 as entry: %v", fs)
	}
	names := map[Class]string{ClassCore: "core", ClassTool: "tool", ClassEntry: "entry", ClassShim: "shim",
		ClassOutput: "output", ClassExempt: "exempt", Class(0): "Class(0)", Class(9): "Class(9)"}
	for c, want := range names {
		if got := c.String(); got != want {
			t.Errorf("Class(%d).String() = %q, want %q", c, got, want)
		}
	}
}

// AT-DET-04
func TestFindingString(t *testing.T) {
	f := Finding{File: "kernel/sim.go", Line: 12, Col: 3, Rule: RuleGo, Msg: "go statement is forbidden in simulation code"}
	if got := f.String(); got != "kernel/sim.go:12:3: DL004: go statement is forbidden in simulation code" {
		t.Errorf("String() = %q", got)
	}
}

// DET-002: the exact messages of DL001–DL011.
func TestMessages(t *testing.T) {
	cases := []struct {
		fixture string
		line    int
		msg     string
	}{
		{"dl001", 6, "wall-clock time: time.Now is forbidden; use Sim.Now, Node.Now or Node.After"},
		{"dl002", 9, "global randomness: math/rand/v2.IntN is forbidden; use Sim.Rand or Node.Rand"},
		{"dl002", 10, "global randomness: math/rand.Intn is forbidden; use Sim.Rand or Node.Rand"},
		{"dl003", 3, "crypto/rand is forbidden in simulation code"},
		{"dl004", 6, "go statement is forbidden in simulation code"},
		{"dl005", 4, "select statement is forbidden in simulation code"},
		{"dl006", 8, "range over map map[string]int has random order; range over sorted keys or annotate //faultline:maporder <reason>"},
		{"dl006", 11, "range over map Named has random order; range over sorted keys or annotate //faultline:maporder <reason>"},
		{"dl006", 20, "range over map M has random order; range over sorted keys or annotate //faultline:maporder <reason>"},
		{"dl007", 6, "floating-point type float64 in decision code; use integers (ppm, nanoseconds)"},
		{"dl008", 9, "environment access: os.Getenv is only allowed in package faultline"},
		{"dl008", 13, "environment access: syscall.Getenv is only allowed in package faultline"},
		{"dl009", 9, "unordered iteration: (reflect.Value).MapKeys; sort the keys or annotate //faultline:maporder <reason>"},
		{"dl009", 12, "unordered iteration: (*sync.Map).Range; sort the keys or annotate //faultline:maporder <reason>"},
		{"dl010", 10, "maps.Keys yields in random order; wrap it in slices.Sorted or annotate //faultline:maporder <reason>"},
		{"dl011", 1, "platform-specific file x_linux.go in a simulation package"},
	}
	for _, c := range cases {
		fs, err := LintDir("testdata/src/"+c.fixture, "fixture/"+c.fixture, ClassCore)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, f := range fs {
			if f.Line == c.line && f.Msg == c.msg {
				found = true
			}
		}
		if !found {
			t.Errorf("%s line %d: no finding %q in\n  %s", c.fixture, c.line, c.msg, findingLines(fs))
		}
	}
}

// AT-DET-02: directive messages.
func TestDirectiveMessages(t *testing.T) {
	fs, err := LintDir("testdata/src/directives", "fixture/directives", ClassCore)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"a.go:13:21: DL000: //faultline:maporder needs a reason",
		"a.go:14:2: DL006: range over map map[string]int has random order; range over sorted keys or annotate //faultline:maporder <reason>",
		"a.go:18:2: DL006: range over map map[string]int has random order; range over sorted keys or annotate //faultline:maporder <reason>",
		"a.go:21:19: DL000: unknown directive //faultline:sorted",
		"a.go:23:19: DL000: //faultline:maporder suppresses no finding",
		"a.go:28:25: DL000: //faultline:wallclock is not allowed in core packages",
		"a.go:29:11: DL001: wall-clock time: time.Now is forbidden; use Sim.Now, Node.Now or Node.After",
		"b.go:7:3: DL006: range over map map[string]int has random order; range over sorted keys or annotate //faultline:maporder <reason>",
		"b.go:14:19: DL000: //faultline:maporder suppresses no finding",
		"b.go:15:11: DL001: wall-clock time: time.Now is forbidden; use Sim.Now, Node.Now or Node.After",
		"b.go:21:4: DL000: //faultline:maporder suppresses no finding",
		"b.go:22:2: DL006: range over map map[string]int has random order; range over sorted keys or annotate //faultline:maporder <reason>",
	}
	var got []string
	for _, f := range fs {
		got = append(got, f.String())
	}
	if !slices.Equal(got, want) {
		t.Errorf("findings:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}
