package detlint

import (
	"slices"
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
