// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/shims/netsim"
)

func init() { //nolint:gochecknoinits // registers the child-process scenarios
	gorScenarios["netsim-invalid"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, NetSim: netsim.Config{MSS: 32}}, func(w *World) {})
	}
}

func TestGoroutineNetSimOption(t *testing.T) {
	t.Run("invalid", func(t *testing.T) {
		res := runGorScenario(t, "netsim-invalid")
		want := "faultline: invalid Options.NetSim: invalid netsim config: MSS 32 out of range [64, 65535]"
		if res.code != 1 || !strings.Contains(res.out, want) {
			t.Fatalf("exit %d\n%s", res.code, res.out)
		}
	})
	t.Run("default", func(t *testing.T) {
		var eff Options
		gw(t, Options{Duration: time.Millisecond}, func(w *World) { eff = w.Options() })
		if eff.NetSim != netsim.DefaultConfig() {
			t.Fatalf("effective NetSim %+v, want DefaultConfig()", eff.NetSim)
		}
	})
	t.Run("event mode", func(t *testing.T) {
		var eff Options
		Run(t, Options{Seeds: 1, Duration: time.Millisecond, NetSim: netsim.Config{MSS: 32}}, func(w *World) { eff = w.Options() })
		if eff.NetSim.MSS != 32 {
			t.Fatal("event mode resolved or validated NetSim")
		}
	})
	t.Run("hash", func(t *testing.T) {
		s := string(hashedOptionsJSON(Options{Mode: ModeGoroutine, Procs: 1, Drain: DefaultDrain, NetSim: netsim.DefaultConfig()}, ""))
		if !strings.Contains(s, `"netsim":{`) {
			t.Fatalf("goroutine-mode hash input lacks netsim: %s", s)
		}
	})
	t.Run("report options", func(t *testing.T) {
		o := Options{Mode: ModeGoroutine, Procs: 1, Drain: DefaultDrain, NetSim: netsim.Config{MSS: 1000}}
		if got, want := string(runOptions(o, &environment{}).NetSim), string(mustJSON(o.NetSim)); got != want {
			t.Fatalf("goroutine-mode report options netsim %s, want %s", got, want)
		}
		o.Mode = ModeEvent
		if ro := runOptions(o, &environment{}); ro.NetSim != nil {
			t.Fatalf("event-mode report options have netsim %s", ro.NetSim)
		}
		if s := string(hashedOptionsJSON(o, "")); strings.Contains(s, "netsim") {
			t.Fatalf("event-mode hash input has netsim: %s", s)
		}
	})
}
