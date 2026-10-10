package etcdraft

import (
	"reflect"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel/fault"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// ETC-141, ETC-144
func TestFaultPresets(t *testing.T) {
	s, ms := time.Second, time.Millisecond
	A := fault.Rule{Kind: fault.KindPartition, Every: 6 * s, MinFor: s, MaxFor: 4 * s}
	B := fault.Rule{Kind: fault.KindIsolate, Every: 10 * s, MinFor: s, MaxFor: 3 * s, Target: "leader"}
	C := fault.Rule{Kind: fault.KindCut, Every: 9 * s, MinFor: s, MaxFor: 3 * s}
	D := fault.Rule{Kind: fault.KindLink, Every: 7 * s, MinFor: s, MaxFor: 3 * s,
		Link: &simnet.Link{Latency: 20 * ms, Jitter: 80 * ms, DropPPM: 200_000}}
	E := fault.Rule{Kind: fault.KindCrash, Every: 6 * s, MinFor: 200 * ms, MaxFor: 3 * s}
	F := fault.Rule{Kind: fault.KindCrash, Every: 12 * s, MinFor: 200 * ms, MaxFor: 2 * s, Target: "leader"}
	G := fault.Rule{Kind: fault.KindPause, Every: 9 * s, MinFor: 100 * ms, MaxFor: 1500 * ms}
	H := fault.Rule{Kind: fault.KindSyncFail, Every: 15 * s, Magnitude: 1}
	I := fault.Rule{Kind: fault.KindClockDrift, Every: 20 * s, Magnitude: 50_000}
	selfTest := []fault.Rule{
		{Kind: fault.KindPartition, Every: 3 * s, MinFor: 500 * ms, MaxFor: 2 * s},
		{Kind: fault.KindIsolate, Every: 4 * s, MinFor: 500 * ms, MaxFor: 2 * s, Target: "leader"},
		{Kind: fault.KindCrash, Every: 1500 * ms, MinFor: 50 * ms, MaxFor: 500 * ms},
		{Kind: fault.KindCrash, Every: 3 * s, MinFor: 50 * ms, MaxFor: 500 * ms, Target: "leader"},
		{Kind: fault.KindPause, Every: 4 * s, MinFor: 50 * ms, MaxFor: 500 * ms},
		{Kind: fault.KindSyncFail, Every: 5 * s, Magnitude: 1},
	}
	cases := []struct {
		preset FaultPreset
		rules  []fault.Rule
	}{
		{FaultsDefault, []fault.Rule{A, B, C, D, E, F, G, H, I}},
		{FaultsNetwork, []fault.Rule{A, B, C, D}},
		{FaultsCrash, []fault.Rule{E, F, H}},
		{FaultsSelfTest, selfTest},
	}
	for _, c := range cases {
		cfg := DefaultConfig()
		cfg.Faults = c.preset
		cfg.Quiet = 7 * s
		r, ok := Faults(cfg).(*fault.Random)
		if !ok {
			t.Fatalf("preset %d: planner %T, want *fault.Random", c.preset, Faults(cfg))
		}
		if r.MaxDown != 0 || r.Quiet != 7*s || !reflect.DeepEqual(r.Rules, c.rules) {
			t.Errorf("preset %d: MaxDown %d Quiet %v rules\n%+v\nwant\n%+v", c.preset, r.MaxDown, r.Quiet, r.Rules, c.rules)
		}
		for _, rule := range r.Rules {
			if rule.Target != "" && rule.Target != "leader" {
				t.Errorf("preset %d targets role %q; presets target servers only (ETC-144)", c.preset, rule.Target)
			}
		}
	}
	cfg := DefaultConfig()
	cfg.Faults = FaultsNone
	if p := Faults(cfg); p.Name() != "none" {
		t.Fatalf("FaultsNone planner %q, want none", p.Name())
	}
	if r, ok := Faults(DefaultConfig()).(*fault.Random); !ok || r.Quiet != 10*s {
		t.Fatalf("default planner %T, want *fault.Random with Quiet 10s", Faults(DefaultConfig()))
	}
}
