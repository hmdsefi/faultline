// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
)

// lookupFunc has the signature of os.LookupEnv; unit tests pass a fake.
type lookupFunc func(string) (string, bool)

// Environment variable names, in the table order of API-010.
const (
	envSeed             = "FAULTLINE_SEED"
	envSeeds            = "FAULTLINE_SEEDS"
	envBaseSeed         = "FAULTLINE_BASE_SEED"
	envExplore          = "FAULTLINE_EXPLORE"
	envSchedule         = "FAULTLINE_SCHEDULE"
	envArtifacts        = "FAULTLINE_ARTIFACTS"
	envCheckDeterminism = "FAULTLINE_CHECK_DETERMINISM"
	envTrace            = "FAULTLINE_TRACE"
	envMinimize         = "FAULTLINE_MINIMIZE"
	envSwarm            = "FAULTLINE_SWARM"
	envSwarmConfig      = "FAULTLINE_SWARM_CONFIG"
	envExact            = "FAULTLINE_EXACT"
	envSeedList         = "FAULTLINE_SEED_LIST"
	envResults          = "FAULTLINE_RESULTS"
)

// envOrder is API-010's table order; parsing and validation follow it.
var envOrder = []string{envSeed, envSeeds, envBaseSeed, envExplore, envSchedule, envArtifacts, envCheckDeterminism, envTrace, envMinimize, envSwarm, envSwarmConfig, envExact, envSeedList, envResults}

// environment holds the parsed FAULTLINE_* variables of one Run call (API-010).
type environment struct {
	set              map[string]bool // variables that are set (non-empty after TrimSpace); lookups only
	seed             uint64          // FAULTLINE_SEED
	seeds            int             // FAULTLINE_SEEDS
	baseSeed         uint64          // FAULTLINE_BASE_SEED
	explore          bool            // FAULTLINE_EXPLORE=1
	schedulePath     string          // FAULTLINE_SCHEDULE, absolute
	schedule         *fault.Schedule // parsed FAULTLINE_SCHEDULE
	scheduleHash     string          // 0x%016x of NameBase(file content)
	artifactsOff     bool            // FAULTLINE_ARTIFACTS=off, in any letter case
	artifactsRoot    string          // FAULTLINE_ARTIFACTS, absolute; "" = default root
	checkDeterminism bool            // FAULTLINE_CHECK_DETERMINISM=1
	traceFull        bool            // FAULTLINE_TRACE=full
	minimize         bool            // FAULTLINE_MINIMIZE enables minimization (Phase 3 acts on it)
	seedList         []uint64        // FAULTLINE_SEED_LIST, duplicates removed
	results          string          // FAULTLINE_RESULTS, absolute
}

// envValue returns the trimmed value of name and whether it is set (API-010).
func envValue(lookup lookupFunc, name string) (string, bool) {
	v, ok := lookup(name)
	if !ok {
		return "", false
	}
	v = strings.TrimSpace(v)
	return v, v != ""
}

// resultsPath returns the absolute FAULTLINE_RESULTS path, or "". Run reads it before anything
// else so that parent-level setup errors still get a results line (API-087).
func resultsPath(lookup lookupFunc) string {
	v, ok := envValue(lookup, envResults)
	if !ok {
		return ""
	}
	abs, err := filepath.Abs(v)
	if err != nil {
		return v
	}
	return abs
}

// readEnv reads and validates every FAULTLINE_* variable once (API-010, API-011 row 1). The
// first invalid variable in table order is the error.
func readEnv(lookup lookupFunc) (*environment, error) {
	e := &environment{set: map[string]bool{}}
	for _, name := range envOrder {
		v, ok := envValue(lookup, name)
		if !ok {
			continue
		}
		e.set[name] = true
		if err := e.parse(name, v); err != nil {
			return nil, err
		}
	}
	if e.set[envSeed] && e.set[envSeedList] {
		return nil, errors.New("faultline: FAULTLINE_SEED and FAULTLINE_SEED_LIST are both set; set only one")
	}
	return e, nil
}

func (e *environment) parse(name, v string) error {
	switch name {
	case envSeed, envBaseSeed:
		s, ok := parseSeed(v)
		if !ok {
			return fmt.Errorf("faultline: invalid %s value %q: want a decimal or 0x-prefixed hexadecimal uint64", name, v)
		}
		if name == envSeed {
			e.seed = s
		} else {
			e.baseSeed = s
		}
	case envSeeds:
		n, err := strconv.ParseUint(v, 10, 64)
		if !allDigits(v) || err != nil || n < 1 || n > MaxSeeds {
			return fmt.Errorf("faultline: invalid FAULTLINE_SEEDS value %q: want a decimal integer from 1 to %d", v, MaxSeeds)
		}
		e.seeds = int(n)
	case envExplore, envCheckDeterminism:
		b, ok := parseBool(v)
		if !ok {
			return fmt.Errorf("faultline: invalid %s value %q: want 0 or 1", name, v)
		}
		if name == envExplore {
			e.explore = b
		} else {
			e.checkDeterminism = b
		}
	case envSchedule:
		abs, err := filepath.Abs(v)
		if err != nil {
			return fmt.Errorf("faultline: FAULTLINE_SCHEDULE=%s: %v", v, err)
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return fmt.Errorf("faultline: FAULTLINE_SCHEDULE=%s: %v", abs, err)
		}
		s, err := fault.ReadSchedule(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("faultline: FAULTLINE_SCHEDULE=%s: %v", abs, err)
		}
		e.schedulePath, e.schedule = abs, &s
		e.scheduleHash = fmt.Sprintf("0x%016x", NameBase(string(data)))
	case envArtifacts:
		switch strings.ToLower(v) {
		case "off":
			e.artifactsOff = true
			return nil
		case "0", "false", "no", "1", "true", "yes", "on": // meant as off or on; as a path they would name a folder in the package
			return fmt.Errorf("faultline: invalid FAULTLINE_ARTIFACTS value %q: artifacts are on by default; set a directory path for the artifact root, or off to turn artifacts off", v)
		}
		abs, err := filepath.Abs(v)
		if err != nil {
			return fmt.Errorf("faultline: FAULTLINE_ARTIFACTS=%s: %v", v, err)
		}
		e.artifactsRoot = abs
	case envTrace:
		switch v {
		case "hash":
		case "full":
			e.traceFull = true
		default:
			return fmt.Errorf("faultline: invalid FAULTLINE_TRACE value %q: want hash or full", v)
		}
	case envMinimize:
		on, reason := parseMinimize(v)
		if reason != "" {
			return fmt.Errorf("faultline: invalid FAULTLINE_MINIMIZE \"%s\": %s", v, reason)
		}
		e.minimize = on
	case envSwarm, envSwarmConfig:
		return fmt.Errorf("faultline: %s is not available until Phase 3; unset it", name)
	case envExact:
		return fmt.Errorf("faultline: %s is not available until Phase 2b; unset it", name)
	case envSeedList:
		list, err := parseSeedList(v)
		if err != nil {
			return err
		}
		e.seedList = list
	case envResults:
		e.results = resultsPath(func(string) (string, bool) { return v, true })
	}
	return nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func allHex(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// parseSeed parses the seed format of API-010.
func parseSeed(s string) (uint64, bool) {
	if len(s) > 2 && (s[:2] == "0x" || s[:2] == "0X") {
		if !allHex(s[2:]) {
			return 0, false
		}
		v, err := strconv.ParseUint(s[2:], 16, 64)
		return v, err == nil
	}
	if !allDigits(s) {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 10, 64)
	return v, err == nil
}

// parseBool parses exactly "0" and "1" (API-010, DET-030).
func parseBool(s string) (bool, bool) {
	switch s {
	case "0":
		return false, true
	case "1":
		return true, true
	}
	return false, false
}

// seedListFormat is the end of the seed-list errors (§7.1): the format a list must have.
const seedListFormat = "want decimal or 0x-prefixed hexadecimal uint64 seeds separated by commas or white space"

// parseSeedList parses FAULTLINE_SEED_LIST (API-010): an inline list or "@file", entries
// separated by commas and white space; duplicates are removed, first occurrence kept (API-011).
// An error names the entry and its line, and the file of an @file list (§7.1).
func parseSeedList(v string) ([]uint64, error) {
	text, file := v, ""
	if strings.HasPrefix(v, "@") {
		path, err := filepath.Abs(v[1:])
		if err != nil {
			return nil, fmt.Errorf("faultline: FAULTLINE_SEED_LIST: %v", err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("faultline: FAULTLINE_SEED_LIST: %v", err)
		}
		text, file = string(data), path
	}
	fields := seedFields(text)
	if len(fields) == 0 {
		if file != "" {
			return nil, fmt.Errorf("faultline: FAULTLINE_SEED_LIST file %s contains no seeds; %s", file, seedListFormat)
		}
		return nil, errors.New("faultline: FAULTLINE_SEED_LIST contains no seeds; " + seedListFormat)
	}
	seen := map[uint64]bool{} // lookups only
	var list []uint64
	for i, f := range fields {
		s, ok := parseSeed(f.text)
		if !ok {
			where := fmt.Sprintf("line %d", f.line)
			if file != "" {
				where += " of " + file
			}
			return nil, fmt.Errorf("faultline: invalid FAULTLINE_SEED_LIST entry %d %q on %s: %s", i+1, f.text, where, seedListFormat)
		}
		if !seen[s] {
			seen[s] = true
			list = append(list, s)
		}
	}
	return list, nil
}

// seedField is one entry of a seed list and the 1-based line it is on.
type seedField struct {
	text string
	line int
}

// seedFields splits a seed list as strings.FieldsFunc does with commas and white space as
// separators (API-010), and keeps the line of each field; a line ends at '\n'.
func seedFields(text string) []seedField {
	var fields []seedField
	line, start := 1, -1
	for i, r := range text {
		if r != ',' && !unicode.IsSpace(r) {
			if start < 0 {
				start = i
				fields = append(fields, seedField{line: line})
			}
			continue
		}
		if start >= 0 {
			fields[len(fields)-1].text = text[start:i]
			start = -1
		}
		if r == '\n' {
			line++
		}
	}
	if start >= 0 {
		fields[len(fields)-1].text = text[start:]
	}
	return fields
}

// parseMinimize validates FAULTLINE_MINIMIZE with MIN §4.1's grammar. It returns whether the
// value enables minimization, or a non-empty reason for an invalid value. It splits the whole
// value into parameters first, so a key given twice is found before any key is looked at
// (x=1,x=2 is "parameter x given twice"); then it checks the keys and values left to right.
func parseMinimize(v string) (enabled bool, reason string) {
	switch v {
	case "0":
		return false, ""
	case "1":
		return true, ""
	}
	var params [][2]string    // key, value
	seen := map[string]bool{} // lookups only
	rest := v
	for rest != "" {
		param := rest
		rest = ""
		if !strings.HasPrefix(param, "out=") {
			if i := strings.IndexByte(param, ','); i >= 0 {
				param, rest = param[:i], param[i+1:]
				if rest == "" {
					return false, "empty parameter after ','"
				}
			}
		}
		key, val, ok := strings.Cut(param, "=")
		if !ok {
			return false, fmt.Sprintf("parameter %q is not key=value", param)
		}
		if seen[key] {
			return false, fmt.Sprintf("parameter %s given twice", key)
		}
		seen[key] = true
		params = append(params, [2]string{key, val})
	}
	for _, p := range params {
		key, val := p[0], p[1]
		switch key {
		case "runs":
			n, err := strconv.Atoi(val)
			if !allDigits(val) || err != nil || n <= 0 {
				return false, fmt.Sprintf("runs=%s: want a positive integer", val)
			}
		case "time":
			d, err := time.ParseDuration(val)
			if err != nil || d < 0 {
				return false, fmt.Sprintf("time=%s: want a duration of 0 or more", val)
			}
		case "out":
			if !filepath.IsAbs(val) {
				return false, fmt.Sprintf("out=%s: want an absolute path", val)
			}
		default:
			return false, fmt.Sprintf("unknown parameter %q", key)
		}
	}
	return true, ""
}

// plan is the resolved configuration of one Run call (API-001 to API-017).
type plan struct {
	opts             Options // effective options: World.Options()
	seeds            []uint64
	seedSource       string // report seed_source: "derived", "env", "list"
	base             uint64 // derived lists only
	baseSource       string // report base_source: "test_name", "options", "env", "explore"
	env              *environment
	primaryTrace     kernel.TraceConfig
	checkDeterminism bool
	envCheck         bool // FAULTLINE_CHECK_DETERMINISM turned the check on, Options.CheckDeterminism did not
	keepGoing        bool
	passArtifacts    bool     // FAULTLINE_TRACE=full
	logs             []string // parent-level log lines (API-017), in order
}

// resolveInput is what resolve needs from Run; unit tests fake the environment and the explore base.
type resolveInput struct {
	opts        Options
	testName    string
	short       bool
	lookup      lookupFunc
	exploreBase func() uint64
}

// resolve applies defaults, the environment, validation and seed derivation (API-001 to
// API-017). Its error text is the setup error message.
func resolve(in resolveInput) (*plan, error) {
	o := applyDefaults(in.opts)
	env, err := readEnv(in.lookup)
	if err != nil {
		return nil, err
	}
	if err := validateOptions(o); err != nil {
		return nil, err
	}
	p := &plan{env: env}
	ignore := func(winner string, names ...string) {
		for _, n := range names {
			if env.set[n] {
				p.logs = append(p.logs, fmt.Sprintf("faultline: %s is set; ignoring %s", winner, n))
			}
		}
	}
	var shortLog, exploreLog string
	switch {
	case env.set[envSeedList]:
		ignore(envSeedList, envSeeds, envBaseSeed, envExplore)
		p.seeds, p.seedSource = env.seedList, "list"
		p.logs = append(p.logs, fmt.Sprintf("faultline: FAULTLINE_SEED_LIST: running %d seeds", len(p.seeds)))
	case env.set[envSeed]:
		ignore(envSeed, envSeeds, envBaseSeed, envExplore)
		p.seeds, p.seedSource = []uint64{env.seed}, "env"
		p.logs = append(p.logs, fmt.Sprintf("faultline: FAULTLINE_SEED=0x%016x: running 1 seed", env.seed))
	default:
		if env.set[envBaseSeed] {
			ignore(envBaseSeed, envExplore)
		}
		n := o.Seeds
		if n == 0 {
			n = DefaultSeeds
		}
		if env.set[envSeeds] {
			n = env.seeds
		} else if in.short && n > ShortSeeds {
			shortLog = fmt.Sprintf("faultline: -short: running %d of %d seeds", ShortSeeds, n)
			n = ShortSeeds
		}
		var label string
		switch {
		case env.set[envBaseSeed]:
			p.base, p.baseSource, label = env.baseSeed, "env", "FAULTLINE_BASE_SEED"
		case env.explore:
			p.base, p.baseSource, label = in.exploreBase(), "explore", "FAULTLINE_EXPLORE"
			rerun := fmt.Sprintf("FAULTLINE_BASE_SEED=0x%016x", p.base)
			if env.set[envSeeds] {
				rerun += fmt.Sprintf(" FAULTLINE_SEEDS=%d", env.seeds) // or the rerun runs the default count
			}
			exploreLog = fmt.Sprintf("faultline: FAULTLINE_EXPLORE: base seed 0x%016x (rerun this set with %s)", p.base, rerun)
		case o.BaseSeed != 0:
			p.base, p.baseSource, label = o.BaseSeed, "options", "Options.BaseSeed"
		default:
			p.base, p.baseSource, label = NameBase(in.testName), "test_name", "test name"
		}
		p.seeds = make([]uint64, n)
		for i := range p.seeds {
			p.seeds[i] = DeriveSeed(p.base, i)
		}
		p.seedSource = "derived"
		p.logs = append(p.logs, fmt.Sprintf("faultline: running %d seeds from base 0x%016x (%s)", n, p.base, label))
	}
	if shortLog != "" {
		p.logs = append(p.logs, shortLog)
	}
	if exploreLog != "" {
		p.logs = append(p.logs, exploreLog)
	}
	if env.schedule != nil {
		p.logs = append(p.logs, fmt.Sprintf("faultline: FAULTLINE_SCHEDULE=%s: %d events; planners are disabled", env.schedulePath, len(env.schedule.Events)))
	}
	if env.traceFull && env.artifactsOff {
		p.logs = append(p.logs, "faultline: FAULTLINE_TRACE=full writes no artifacts while FAULTLINE_ARTIFACTS is off; unset one of them")
	}
	// A variable never turns off what Options turned on (API-012, API-013), so say so when it tries.
	if env.set[envCheckDeterminism] && !env.checkDeterminism && o.CheckDeterminism {
		p.logs = append(p.logs, "faultline: FAULTLINE_CHECK_DETERMINISM=0 does not turn off Options.CheckDeterminism; the determinism check still runs (set Options.CheckDeterminism to false to turn it off)")
	}
	if env.set[envTrace] && !env.traceFull && o.Trace.Level == kernel.TraceFull {
		p.logs = append(p.logs, "faultline: FAULTLINE_TRACE=hash does not lower Options.Trace.Level from kernel.TraceFull; runs still record full traces (set Options.Trace.Level to kernel.TraceHash to lower it)")
	}
	if env.minimize {
		p.logs = append(p.logs, "faultline: FAULTLINE_MINIMIZE is set, but minimization is not available in this version; ignoring")
	}

	p.primaryTrace = o.Trace
	if env.traceFull {
		p.primaryTrace.Level = kernel.TraceFull
		p.passArtifacts = true
	}
	p.checkDeterminism = o.CheckDeterminism || env.checkDeterminism
	p.envCheck = env.checkDeterminism && !o.CheckDeterminism
	p.keepGoing = o.KeepGoing || env.set[envSeedList]

	o.Seeds = len(p.seeds)
	o.BaseSeed = 0
	if p.seedSource == "derived" {
		o.BaseSeed = p.base
	}
	o.Trace = p.primaryTrace
	o.CheckDeterminism = p.checkDeterminism
	o.KeepGoing = p.keepGoing
	p.opts = o
	return p, nil
}
