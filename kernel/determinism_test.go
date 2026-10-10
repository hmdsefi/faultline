// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package kernel_test

import (
	"testing"
	"time"

	"github.com/hmdsefi/faultline/internal/golden"
	"github.com/hmdsefi/faultline/kernel"
)

// lifecycle is the AT-KRN-25 scenario as a golden.RunFunc.
func lifecycle(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
	s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
	counter := 0
	n1 := s.AddNode("n1", func(n *kernel.Node) {
		var tick func()
		tick = func() {
			counter++
			n.Logf("tick %d", counter)
			n.After(100*time.Millisecond, "tick", tick)
		}
		n.After(100*time.Millisecond, "tick", tick)
	}, kernel.WithTags("server"))
	ms := func(x int64) kernel.Time { return kernel.Time(time.Duration(x) * time.Millisecond) }
	s.At(ms(250), "pause", n1.Pause)
	s.At(ms(500), "resume", n1.Resume)
	s.At(ms(650), "crash", n1.Crash)
	s.At(ms(700), "restart", n1.Restart)
	return s, s.RunUntil(kernel.Time(time.Second))
}

// AT-KRN-38 (run-twice determinism through golden.MustBeDeterministic)
func TestLifecycleIsDeterministic(t *testing.T) {
	res := golden.MustBeDeterministic(t, 1, lifecycle)
	want := golden.Result{Seed: 1, Hash: 0x42a2518011c8bc6a, Stop: kernel.StopDeadline, Executed: 12, Now: kernel.Time(time.Second)}
	if res != want {
		t.Fatalf("result %+v (hash 0x%016x), want %+v (hash 0x%016x)", res, res.Hash, want, want.Hash)
	}
}
