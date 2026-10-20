// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"context"
	"os"
	"os/signal"
	"regexp"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

// synctestLeakPrefix is the start of the error synctest.Test panics with when the bubble's main
// goroutine returns while other goroutines in the bubble are still blocked (GOR-008, AT-GOR-25).
const synctestLeakPrefix = "deadlock: main bubble goroutine has exited but blocked goroutines remain"

var (
	signalOnce  sync.Once
	runLabels   atomic.Uint64 // GOR-007 step 1: process-wide run label counter
	stackHeader = regexp.MustCompile(`^goroutine (\d+) \[[^\]]*synctest bubble (\d+)`)
)

// preNotifySignals registers and stops an os.Interrupt notification once per process, outside any
// bubble, so that code under test may call signal.Notify inside a bubble (GOR-004).
func preNotifySignals() {
	signalOnce.Do(func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt)
		signal.Stop(c)
	})
}

// isSynctestLeakPanic reports whether v is the panic of synctestLeakPrefix (GOR-008).
func isSynctestLeakPanic(v any) bool {
	err, ok := v.(error)
	return ok && strings.HasPrefix(err.Error(), synctestLeakPrefix)
}

// parseStackHeader parses the first line of runtime.Stack(buf, false) (GOR-007 step 3).
func parseStackHeader(line string) (gid, bubble uint64, ok bool) {
	m := stackHeader.FindStringSubmatch(line)
	if m == nil {
		return 0, 0, false
	}
	gid, err1 := strconv.ParseUint(m[1], 10, 64)
	bubble, err2 := strconv.ParseUint(m[2], 10, 64)
	return gid, bubble, err1 == nil && err2 == nil
}

// curGoroutineID returns the calling goroutine's ID (GOR §11).
func curGoroutineID() uint64 {
	var buf [64]byte
	b := buf[:runtime.Stack(buf[:], false)]
	b = b[len("goroutine "):]
	i := 0
	for i < len(b) && b[i] >= '0' && b[i] <= '9' {
		i++
	}
	id, _ := strconv.ParseUint(string(b[:i]), 10, 64)
	return id
}

// goAttempt is the state one goroutine-mode attempt shares between runAttemptGoroutine (outside
// the bubble), the attempt function and the driver (inside it), and the watchdog.
type goAttempt struct {
	r        *runner
	seed     uint64
	res      *attemptResult
	run      string        // run label (GOR-007)
	gid      uint64        // driver goroutine ID
	bubble   atomic.Uint64 // synctest bubble ID, read by the watchdog
	wake     chan struct{} // capacity 1 (GOR-024)
	bt       *testing.T    // the bubble's T
	returned bool          // fn returned normally
	watch    *watchState   // published for the stall watchdog (GOR-080)
	drv      *driver       // created by newWorld (GOR-010 step 1)
}

// runAttemptGoroutine is runAttempt (API-090) in goroutine mode (GOR-006).
func (r *runner) runAttemptGoroutine(st *testing.T, seed uint64, res *attemptResult, fn func(st *testing.T, ga *goAttempt)) {
	preNotifySignals() // GOR-004 (idempotent; also called before the first seed)
	ga := &goAttempt{r: r, seed: seed, res: res, watch: &watchState{}}
	prev := runtime.GOMAXPROCS(r.plan.opts.Procs) // step 1
	defer runtime.GOMAXPROCS(prev)
	if r.plan.opts.StallTimeout > 0 { // resolved: 0 became DefaultStallTimeout; negative disables
		stop := r.startWatchdog(st, ga, r.attemptNo)
		defer stop()
	}
	func() {
		defer func() { // GOR-008
			if v := recover(); v != nil && !isSynctestLeakPanic(v) {
				panic(v)
			}
		}()
		synctest.Test(st, func(bt *testing.T) { // step 3
			ga.bt = bt
			ga.begin()
			defer ga.end() // GOR-065; runs also when fn calls runtime.Goexit
			fn(bt, ga)
			ga.returned = true
		})
	}()
	// Reached when synctest.Test returned normally or its leak panic was swallowed.
	switch {
	case ga.bt != nil && ga.bt.Failed():
		st.FailNow() // what synctest.Test does itself when no leak panic pre-empts it
	case !ga.returned && ga.bt != nil && ga.bt.Skipped():
		st.SkipNow() // step 4 for t.Skip* inside the bubble
	case !ga.returned:
		runtime.Goexit() // step 4
	}
}

// begin allocates the attempt's run label, sets the driver goroutine's pprof labels and records
// the driver's goroutine ID and the bubble ID (GOR-007). It runs first in the bubble, on the
// driver goroutine.
func (ga *goAttempt) begin() {
	ga.run = strconv.FormatUint(runLabels.Add(1), 10)
	pprof.SetGoroutineLabels(pprof.WithLabels(context.Background(), pprof.Labels(
		"faultline_run", ga.run, "faultline_node", "0", "faultline_inc", "0")))
	var buf [256]byte
	line, _, _ := strings.Cut(string(buf[:runtime.Stack(buf[:], false)]), "\n")
	gid, bubble, ok := parseStackHeader(line)
	if !ok {
		panic("faultline: internal error: cannot parse the driver goroutine header " + strconv.Quote(line))
	}
	ga.gid = gid
	ga.bubble.Store(bubble)
	ga.wake = make(chan struct{}, 1)
}

// end runs on the driver goroutine after fn returned or unwound (GOR-065). It leaves the world
// lock released.
func (ga *goAttempt) end() {
	d := ga.drv
	if d == nil { // world construction failed before GOR-010
		return
	}
	if !d.holds.Load() { // e.g. after a Goexit inside a check
		d.lockDriver()
	}
	d.endRun() // E1
	d.unlockDriver()
	blocks, cut := d.drainProcesses() // E2
	if cut {                          // GOR-067: the count may be too low
		ga.res.warnings = append(ga.res.warnings, dumpCutWarning)
		ga.bt.Logf("faultline: %s", dumpCutWarning)
	}
	if len(blocks) > 0 { // E3: the drain's last count (GOR-067)
		rep := d.collectLeaks(blocks)
		ga.res.leaks = rep
		addLeakFile(ga.res, rep) // goroutines.txt (API-077 item 2)
		// After a Goexit (t.FailNow, t.Skip) the outcome is not a pass, whatever res says.
		if ga.r.plan.opts.FailOnLeak && ga.returned && ga.res.isPass() {
			ga.res.setLeakFailure(rep) // GOR-069
		} else {
			ga.bt.Logf("%s", rep.text)
		}
	}
	// E4: return. synctest.Test panics if goroutines are still blocked, and runAttemptGoroutine
	// swallows that panic (GOR-008, GOR-070).
}
