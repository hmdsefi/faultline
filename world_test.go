package faultline_test

import (
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel"
)

// AT-API-11
func TestWorldNodes(t *testing.T) {
	faultline.Run(t, faultline.Options{Seeds: 1, Duration: time.Second}, func(w *faultline.World) {
		boot := func(n *kernel.Node) {}
		s1 := w.AddServer("s1", boot)
		s2 := w.AddServer("s2", boot)
		c1 := w.AddClient("c1", boot, kernel.WithTags("x"))
		s3 := w.Sim.AddNode("s3", boot, kernel.WithTags("server"))
		servers := w.Servers()
		if len(servers) != 3 || servers[0] != s1 || servers[1] != s2 || servers[2] != s3 {
			t.Errorf("Servers() = %v", servers)
		}
		if clients := w.Clients(); len(clients) != 1 || clients[0] != c1 {
			t.Errorf("Clients() = %v", clients)
		}
		if !c1.HasTag("x") || !c1.HasTag("client") {
			t.Errorf("c1 tags = %v", c1.Tags())
		}
		if w.Net == nil || w.Disk == nil || w.Faults == nil || w.History == nil {
			t.Error("World component is nil")
		}
	})
}

// AT-API-12
func TestWorldRand(t *testing.T) {
	faultline.Run(t, faultline.Options{Seeds: 1, Duration: time.Second}, func(w *faultline.World) {
		a, b, c := w.Rand("load"), w.Rand("load"), w.Sim.Rand("workload/load")
		if a != b || a != c {
			t.Error("World.Rand streams differ")
		}
		func() {
			defer func() {
				v := recover()
				err, ok := v.(error)
				if !ok || err.Error() != "faultline: World.Rand: empty label" {
					t.Errorf("w.Rand(\"\") panicked with %v", v)
				}
			}()
			w.Rand("")
		}()
	})
}

// AT-API-13
func TestWorldRunFor(t *testing.T) {
	faultline.Run(t, faultline.Options{Seeds: 1, Duration: 2 * time.Second}, func(w *faultline.World) {
		tk := addTicker(w)
		w.RunFor(500 * time.Millisecond)
		if now := w.Sim.Now(); now != kernel.Time(500*time.Millisecond) {
			t.Errorf("after RunFor: now %s", now)
		}
		w.Final("end", func() error {
			if now := w.Sim.Now(); now != kernel.Time(2*time.Second) {
				t.Errorf("final: now %s", now)
			}
			if tk.count != 200 {
				t.Errorf("ticks = %d, want 200", tk.count)
			}
			return nil
		})
	})
}

// AT-API-38
func TestEnvDetOn(t *testing.T) {
	t.Setenv("FAULTLINE_CHECK_DETERMINISM", "1")
	calls := 0
	faultline.Run(t, faultline.Options{Seeds: 2, Duration: time.Second}, func(w *faultline.World) {
		calls++
		addTicker(w)
	})
	if calls != 4 {
		t.Fatalf("body calls = %d, want 4", calls)
	}
}

// AT-API-38
func TestEnvDetOff(t *testing.T) {
	t.Setenv("FAULTLINE_CHECK_DETERMINISM", "0")
	calls := 0
	faultline.Run(t, faultline.Options{Seeds: 2, Duration: time.Second}, func(w *faultline.World) {
		calls++
		addTicker(w)
	})
	if calls != 2 {
		t.Fatalf("body calls = %d, want 2", calls)
	}
}

func TestWorldOptionsAndMisuse(t *testing.T) {
	faultline.Run(t, faultline.Options{Seeds: 1, Duration: time.Second}, func(w *faultline.World) {
		o := w.Options()
		if o.Seeds != 1 || o.Duration != time.Second || o.MaxEvents != faultline.DefaultMaxEvents || o.BaseSeed != faultline.NameBase(t.Name()) {
			t.Errorf("Options() = %+v", o)
		}
		if w.End() != kernel.Time(time.Second) || w.RecoveryStart() != kernel.Time(time.Second) {
			t.Errorf("End %s RecoveryStart %s", w.End(), w.RecoveryStart())
		}
		mustPanic := func(want string, fn func()) {
			t.Helper()
			defer func() {
				v := recover()
				if err, ok := v.(error); !ok || err.Error() != want {
					t.Errorf("panic %v, want %q", v, want)
				}
			}()
			fn()
		}
		mustPanic(`faultline: World.Invariant: empty name`, func() { w.Invariant("", func() error { return nil }) })
		mustPanic(`faultline: World.Final "f": nil function`, func() { w.Final("f", nil) })
		w.Invariant("i", func() error { return nil })
		mustPanic(`faultline: World.Invariant: duplicate name "i"`, func() { w.Invariant("i", func() error { return nil }) })
		w.Final("i", func() error { return nil }) // separate namespace
		mustPanic(`faultline: World.Role: empty name`, func() { w.Role("", func() []kernel.NodeID { return nil }) })
		mustPanic(`faultline: World.Plan: nil planner`, func() { w.Plan(nil) })
		mustPanic(`faultline: World.RunFor: negative duration -1s`, func() { w.RunFor(-time.Second) })
	})
}
