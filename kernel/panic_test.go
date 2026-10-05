package kernel

import (
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// panicErr returns s.Err() as a *PanicError or fails the test.
func panicErr(t *testing.T, s *Sim) *PanicError {
	t.Helper()
	pe, ok := s.Err().(*PanicError)
	if !ok {
		t.Fatalf("Err() = %#v, want *PanicError", s.Err())
	}
	return pe
}

// AT-KRN-19
func TestPanicRecovery(t *testing.T) {
	s := fullSim(7)
	gRan := false
	s.After(time.Second, "boom", func() { panic("boom") })
	s.After(2*time.Second, "later", func() { gRan = true })
	if r := s.Run(); r != StopFailed {
		t.Fatalf("Run = %v", r)
	}
	pe := panicErr(t, s)
	if pe.Value != "boom" || pe.At != sec(1) || pe.Event != 1 || pe.Label != "boom" || pe.Node != 0 ||
		pe.Observer || pe.Goexit || pe.Seed != 7 || len(pe.Stack) == 0 {
		t.Fatalf("PanicError = %+v", pe)
	}
	const wantErr = `kernel: panic at 1.000000000s in event 1 "boom" (seed 0x0000000000000007): boom`
	if got := s.Err().Error(); got != wantErr {
		t.Errorf("Error() = %q, want %q", got, wantErr)
	}
	if gRan {
		t.Error("later event ran after the panic")
	}
	checkRecords(t, s, `
1 0.000000000s 0/0 kernel.start cause=0 "start" [seed=0x0000000000000007 tie_break=seeded]
2 1.000000000s 0/0 kernel.event cause=1 "boom" [id=1]
3 1.000000000s 0/0 kernel.panic cause=2 "boom" [event_id=1 label=boom observer=false]
`)
	if got := s.TraceHash(); got != 0xcd12ddfaded9b65b {
		t.Errorf("TraceHash = %#016x, want 0xcd12ddfaded9b65b", got)
	}

	errE := errors.New("e")
	s2 := New(Config{Seed: 7})
	s2.After(0, "err", func() { panic(errE) })
	s2.Run()
	if !errors.Is(s2.Err(), errE) {
		t.Errorf("errors.Is(Err(), e) = false for %v", s2.Err())
	}

	s3 := fullSim(7)
	s3.After(0, "nilmap", func() {
		var m map[string]int
		m["x"] = 1
	})
	s3.Run()
	rs := s3.Records()
	if last := rs[len(rs)-1]; last.Kind != "kernel.panic" || last.Text != "assignment to entry in nil map" {
		t.Errorf("nil-map panic record %s", recLine(last))
	}

	s4 := New(Config{Seed: 7})
	s4.After(0, "other", func() { panic(42) })
	s4.Run()
	if !strings.HasSuffix(s4.Err().Error(), "): 42") || panicErr(t, s4).Unwrap() != nil {
		t.Errorf("non-string panic: %q", s4.Err().Error())
	}
}

// KRN-042: a panic after the Sim already failed is swallowed and emits nothing.
func TestPanicAfterFailIsSwallowed(t *testing.T) {
	errBad := errors.New("bad")
	s := fullSim(7)
	s.After(0, "x", func() {
		s.Fail(errBad)
		panic("late")
	})
	s.Run()
	rs := s.Records()
	if s.Err() != errBad || rs[len(rs)-1].Kind != "kernel.fail" {
		t.Fatalf("Err %v, last record %s", s.Err(), recLine(rs[len(rs)-1]))
	}
}

// AT-KRN-20
func TestObserverPanicAndOrder(t *testing.T) {
	s := New(Config{Seed: 7})
	var log orderLog
	events := 0
	s.OnEvent(func() { log = append(log, "o1") })
	s.OnEvent(func() {
		log = append(log, "o2")
		if events == 2 {
			panic("o2 failed")
		}
	})
	s.OnEvent(func() { log = append(log, "o3") })
	s.At(sec(1), "first", func() { events++; log = append(log, "e1") })
	s.At(sec(2), "second", func() { events++; log = append(log, "e2") })
	s.At(sec(3), "third", func() { events++ })
	if r := s.Run(); r != StopFailed {
		t.Fatalf("Run = %v", r)
	}
	if got := log.String(); got != "e1 o1 o2 o3 e2 o1 o2" {
		t.Errorf("log %q", got)
	}
	pe := panicErr(t, s)
	if !pe.Observer || pe.Label != "second" || !strings.Contains(pe.Error(), " (observer) ") {
		t.Errorf("PanicError %+v, Error %q", pe, pe.Error())
	}

	s2 := New(Config{Seed: 7})
	var log2 orderLog
	s2.At(sec(1), "register", func() { s2.OnEvent(func() { log2 = append(log2, "late") }) })
	s2.At(sec(2), "next", func() { log2 = append(log2, "next") })
	s2.Run()
	if got := log2.String(); got != "next late" {
		t.Errorf("late observer log %q, want %q", got, "next late")
	}
	mustPanic(t, "kernel: nil function passed to OnEvent", func() { s2.OnEvent(nil) })
}

// KRN-046, KRN-051: no observer runs after a failed event.
func TestNoObserverAfterFailedEvent(t *testing.T) {
	s := New(Config{Seed: 7})
	ran := false
	s.OnEvent(func() { ran = true })
	s.After(0, "fail", func() { s.Fail(errors.New("x")) })
	s.Run()
	if ran {
		t.Error("observer ran after a failed event")
	}
}

// AT-KRN-21
func TestGoexit(t *testing.T) {
	s := fullSim(7)
	s.After(0, "exit", func() { runtime.Goexit() })
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.Run()
	}()
	wg.Wait()
	pe := panicErr(t, s)
	if !pe.Goexit || pe.Value != nil {
		t.Fatalf("PanicError %+v", pe)
	}
	rs := s.Records()
	if last := rs[len(rs)-1]; last.Kind != "kernel.panic" || last.Text != "runtime.Goexit called in simulation callback" {
		t.Errorf("last record %s", recLine(last))
	}
	if Active() != nil {
		t.Error("Active() != nil after Goexit")
	}
	if r := s.Run(); r != StopFailed {
		t.Errorf("Run after Goexit = %v, want failed", r)
	}
}

// AT-KRN-22 (callback and observer; the OnBoot hook case is in node_test.go)
func TestReentrancy(t *testing.T) {
	const msg = "kernel: Step/Run called while the simulation is running"
	s := New(Config{Seed: 1})
	s.After(0, "nested", func() { s.Run() })
	s.Run()
	if pe := panicErr(t, s); pe.Value != msg {
		t.Errorf("callback: Value %v", pe.Value)
	}

	s2 := New(Config{Seed: 1})
	s2.OnEvent(func() { s2.Step() })
	s2.After(0, "e", func() {})
	s2.Run()
	if pe := panicErr(t, s2); pe.Value != msg || !pe.Observer {
		t.Errorf("observer: %+v", pe)
	}

	for name, call := range map[string]func(s *Sim){
		"RunUntil": func(s *Sim) { s.RunUntil(s.Now()) },
		"RunFor":   func(s *Sim) { s.RunFor(0) },
	} {
		s3 := New(Config{Seed: 1})
		s3.After(0, "e", func() { call(s3) })
		s3.Run()
		if pe := panicErr(t, s3); pe.Value != msg {
			t.Errorf("%s: Value %v", name, pe.Value)
		}
	}
}

// AT-KRN-40 (observer)
func TestActiveInObserver(t *testing.T) {
	s := New(Config{Seed: 1})
	var seen *Sim
	s.OnEvent(func() { seen = Active() })
	s.After(0, "e", func() {})
	s.Run()
	if seen != s {
		t.Errorf("Active() in observer = %p, want %p", seen, s)
	}
}
