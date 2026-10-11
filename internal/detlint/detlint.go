// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package detlint is faultline's determinism lint, import-boundary check and exported-API
// snapshot generator. Non-test files import only the standard library; the tests apply the
// checks to the root module.
package detlint

import (
	"cmp"
	"fmt"
	"go/build"
	"slices"
	"strconv"
)

// Rule identifies a check.
type Rule string

const (
	RuleMeta       Rule = "DL000" // directive problems, unclassified packages
	RuleWallClock  Rule = "DL001" // wall-clock time and real timers
	RuleGlobalRand Rule = "DL002" // package-level math/rand and math/rand/v2 functions
	RuleCryptoRand Rule = "DL003" // import of crypto/rand
	RuleGo         Rule = "DL004" // go statement
	RuleSelect     Rule = "DL005" // select statement
	RuleMapRange   Rule = "DL006" // range over a map
	RuleFloat      Rule = "DL007" // floating-point and complex types
	RuleEnv        Rule = "DL008" // environment access
	RuleUnordered  Rule = "DL009" // reflect map iteration, sync.Map.Range
	RuleMapIter    Rule = "DL010" // maps.All, maps.Keys, maps.Values
	RulePlatform   Rule = "DL011" // GOOS/GOARCH-specific files
	RuleImport     Rule = "DL012" // import boundary (architecture §5.1)
)

// Class selects the lint rules for a package (DET-010).
type Class uint8

const (
	ClassCore   Class = iota + 1 // simulation core
	ClassTool                    // simulation tooling: like core, //faultline:wallclock honored
	ClassEntry                   // package faultline
	ClassShim                    // shims/*
	ClassOutput                  // artifact, ui
	ClassExempt                  // not linted (import boundaries still apply)
)

// Rules returns the lint rules (DL001 to DL011) that apply to c, in ID order (DET-010).
func (c Class) Rules() []Rule {
	all := []Rule{RuleWallClock, RuleGlobalRand, RuleCryptoRand, RuleGo, RuleSelect, RuleMapRange,
		RuleFloat, RuleEnv, RuleUnordered, RuleMapIter, RulePlatform}
	switch c {
	case ClassCore, ClassTool:
		return all
	case ClassEntry:
		return []Rule{RuleGlobalRand, RuleCryptoRand, RuleMapRange, RuleUnordered, RuleMapIter, RulePlatform}
	case ClassShim:
		return []Rule{RuleGlobalRand, RuleCryptoRand, RuleMapRange, RuleFloat, RuleEnv, RuleUnordered,
			RuleMapIter, RulePlatform}
	case ClassOutput:
		return []Rule{RuleGlobalRand, RuleCryptoRand, RuleMapRange, RuleEnv, RuleUnordered, RuleMapIter,
			RulePlatform}
	}
	return nil
}

// String returns "core", "tool", "entry", "shim", "output", "exempt", or "Class(<n>)".
func (c Class) String() string {
	switch c {
	case ClassCore:
		return "core"
	case ClassTool:
		return "tool"
	case ClassEntry:
		return "entry"
	case ClassShim:
		return "shim"
	case ClassOutput:
		return "output"
	case ClassExempt:
		return "exempt"
	}
	return "Class(" + strconv.Itoa(int(c)) + ")"
}

// Finding is one violation.
type Finding struct {
	File string // slash-separated path relative to the module root ("" for module-level findings)
	Line int    // 1-based; 0 for directory-level findings
	Col  int    // 1-based; 0 for directory-level findings
	Rule Rule
	Msg  string
}

// String returns "<File>:<Line>:<Col>: <Rule>: <Msg>".
func (f Finding) String() string {
	return fmt.Sprintf("%s:%d:%d: %s: %s", f.File, f.Line, f.Col, f.Rule, f.Msg)
}

// sortFindings sorts by (File, Line, Col, Rule, Msg).
func sortFindings(fs []Finding) {
	slices.SortFunc(fs, func(a, b Finding) int {
		return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line), cmp.Compare(a.Col, b.Col),
			cmp.Compare(a.Rule, b.Rule), cmp.Compare(a.Msg, b.Msg))
	})
}

// checkContexts are the four go/build contexts of DET §3: linux/amd64, linux/arm64,
// darwin/arm64 and windows/amd64, without cgo.
func checkContexts() []build.Context {
	targets := [][2]string{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "arm64"}, {"windows", "amd64"}}
	out := make([]build.Context, len(targets))
	for i, t := range targets {
		c := build.Default
		c.GOOS, c.GOARCH, c.CgoEnabled = t[0], t[1], false
		out[i] = c
	}
	return out
}

// hostContext returns a copy of build.Default without cgo, as in the check contexts: it selects the
// files that are type-checked (DET-015), so the result does not depend on CGO_ENABLED.
func hostContext() *build.Context {
	c := build.Default
	c.CgoEnabled = false
	return &c
}

// contextMatches returns how many check contexts match the file name in dir.
func contextMatches(dir, name string) (int, error) {
	n := 0
	for _, c := range checkContexts() {
		ok, err := c.MatchFile(dir, name)
		if err != nil {
			return 0, err
		}
		if ok {
			n++
		}
	}
	return n, nil
}
