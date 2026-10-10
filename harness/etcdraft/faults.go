// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package etcdraft

import (
	"time"

	"github.com/hmdsefi/faultline/kernel/fault"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// presetRule returns rule id ('A'..'I') of the ETC-141 table.
func presetRule(id byte) fault.Rule {
	switch id {
	case 'A':
		return fault.Rule{Kind: fault.KindPartition, Every: 6 * time.Second, MinFor: time.Second, MaxFor: 4 * time.Second}
	case 'B':
		return fault.Rule{Kind: fault.KindIsolate, Every: 10 * time.Second, MinFor: time.Second, MaxFor: 3 * time.Second, Target: "leader"}
	case 'C':
		return fault.Rule{Kind: fault.KindCut, Every: 9 * time.Second, MinFor: time.Second, MaxFor: 3 * time.Second}
	case 'D':
		return fault.Rule{Kind: fault.KindLink, Every: 7 * time.Second, MinFor: time.Second, MaxFor: 3 * time.Second,
			Link: &simnet.Link{Latency: 20 * time.Millisecond, Jitter: 80 * time.Millisecond, DropPPM: 200_000}}
	case 'E':
		return fault.Rule{Kind: fault.KindCrash, Every: 6 * time.Second, MinFor: 200 * time.Millisecond, MaxFor: 3 * time.Second}
	case 'F':
		return fault.Rule{Kind: fault.KindCrash, Every: 12 * time.Second, MinFor: 200 * time.Millisecond, MaxFor: 2 * time.Second, Target: "leader"}
	case 'G':
		return fault.Rule{Kind: fault.KindPause, Every: 9 * time.Second, MinFor: 100 * time.Millisecond, MaxFor: 1500 * time.Millisecond}
	case 'H':
		return fault.Rule{Kind: fault.KindSyncFail, Every: 15 * time.Second, Magnitude: 1}
	case 'I':
		return fault.Rule{Kind: fault.KindClockDrift, Every: 20 * time.Second, Magnitude: 50_000}
	}
	panic("etcdraft: unknown preset rule " + string(id))
}

func presetRules(ids string) []fault.Rule {
	rules := make([]fault.Rule, 0, len(ids))
	for i := 0; i < len(ids); i++ {
		rules = append(rules, presetRule(ids[i]))
	}
	return rules
}

// selfTestRules is the FaultsSelfTest preset (ETC-141).
func selfTestRules() []fault.Rule {
	return []fault.Rule{
		{Kind: fault.KindPartition, Every: 3 * time.Second, MinFor: 500 * time.Millisecond, MaxFor: 2 * time.Second},
		{Kind: fault.KindIsolate, Every: 4 * time.Second, MinFor: 500 * time.Millisecond, MaxFor: 2 * time.Second, Target: "leader"},
		{Kind: fault.KindCrash, Every: 1500 * time.Millisecond, MinFor: 50 * time.Millisecond, MaxFor: 500 * time.Millisecond},
		{Kind: fault.KindCrash, Every: 3 * time.Second, MinFor: 50 * time.Millisecond, MaxFor: 500 * time.Millisecond, Target: "leader"},
		{Kind: fault.KindPause, Every: 4 * time.Second, MinFor: 50 * time.Millisecond, MaxFor: 500 * time.Millisecond},
		{Kind: fault.KindSyncFail, Every: 5 * time.Second, Magnitude: 1},
	}
}

// Faults returns a new planner for cfg.Faults. FaultsNone gives fault.None. Every other preset
// gives a fault.Random with the preset's rules (see FaultPreset) and cfg.Quiet as its recovery
// window. Its default MaxDown keeps fewer than half the servers down or paused at once. Call
// Faults once per seed.
func Faults(cfg Config) fault.Planner {
	cfg = cfg.withDefaults()
	var rules []fault.Rule
	switch cfg.Faults {
	case FaultsNone:
		return fault.None()
	case FaultsDefault:
		rules = presetRules("ABCDEFGHI")
	case FaultsNetwork:
		rules = presetRules("ABCD")
	case FaultsCrash:
		rules = presetRules("EFH")
	case FaultsSelfTest:
		rules = selfTestRules()
	default:
		panic("etcdraft: Faults: undefined preset")
	}
	return &fault.Random{Rules: rules, MaxDown: 0, Quiet: cfg.Quiet}
}
