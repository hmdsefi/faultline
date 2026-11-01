// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline_test

import (
	"context"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/internal/gosched"
)

// spinScheduler is a gosched.Scheduler whose point parks the caller until the next Release.
type spinScheduler struct {
	mu      sync.Mutex
	active  bool
	waiting []chan struct{}
}

func (s *spinScheduler) Reset(release, _ *rand.Rand) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = release != nil
	if !s.active {
		for _, ch := range s.waiting {
			close(ch)
		}
		s.waiting = nil
	}
}

func (s *spinScheduler) Pending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.waiting) > 0
}

func (s *spinScheduler) Release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	close(s.waiting[0])
	s.waiting = s.waiting[1:]
}

func (s *spinScheduler) Gaps() []string { return nil }

func (s *spinScheduler) point() {
	s.mu.Lock()
	if !s.active {
		s.mu.Unlock()
		return
	}
	ch := make(chan struct{})
	s.waiting = append(s.waiting, ch)
	s.mu.Unlock()
	gosched.Wake()
	<-ch
}

// xte-livelock: a process reaches a scheduling point again right after every release, so every
// driver pass ends in a release and virtual time never moves.
var _ = scenario("xte-livelock", func(t *testing.T) {
	s := &spinScheduler{}
	gosched.Active = s // this child process ends in the stall report's panic
	faultline.Run(t, faultline.Options{Mode: faultline.ModeGoroutine, Seeds: 1, Duration: time.Second, StallTimeout: time.Second},
		func(w *faultline.World) {
			w.AddProcess("spin", func(ctx context.Context, p *faultline.Process) error {
				for ctx.Err() == nil {
					s.point()
				}
				return nil
			})
		})
})

// AT-XTE-20
func TestATXTE20_ReleaseOnlyPassesStall(t *testing.T) {
	out, code := runScenario(t, "xte-livelock", nil)
	if code == 0 || strings.Contains(out, "DATA RACE") {
		t.Fatalf("the livelocked run exited %d (want non-zero, without a data race)\n%s", code, out)
	}
	for _, want := range []string{
		"faultline: goroutine mode stalled: no driver progress for 1s of wall time",
		// XTE-082: the two lines come right after the [running] / [runnable] line.
		"    [running] / [runnable]: a goroutine that never blocks (busy loop)\n" +
			"    [toolexec] goroutines parked in sched.Point and nothing else moving: a goroutine reaches\n" +
			"        scheduling points without ever blocking (a polling loop, for example a select with a default)\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q\n%s", want, out)
		}
	}
}
