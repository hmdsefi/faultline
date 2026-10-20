// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"sync/atomic"

	"github.com/hmdsefi/faultline/kernel"
)

// watchState is what the driver publishes for the stall watchdog (GOR-080). Atomics only: the
// watchdog reads it from outside the bubble.
type watchState struct {
	progress atomic.Uint64          // incremented at every synctest.Wait return and drain chunk
	phase    atomic.Int32           // watchBody … watchDrain
	nowNS    atomic.Int64           // Sim.Now() after the last step
	executed atomic.Uint64          // Sim.Executed() after the last step
	label    atomic.Pointer[string] // kernel label of the last driver request whose wrapper ran
}

const (
	watchBody int32 = iota
	watchRun
	watchFinal
	watchDrain
)

var watchPhaseNames = [...]string{"body", "run", "final", "drain"} //nolint:unused // the stall report names the phase

func (ws *watchState) stepped(s *kernel.Sim) {
	ws.nowNS.Store(int64(s.Now()))
	ws.executed.Store(s.Executed())
}
