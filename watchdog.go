// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/artifact"
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

var watchPhaseNames = [...]string{"body", "run", "final", "drain"}

func (ws *watchState) stepped(s *kernel.Sim) {
	ws.nowNS.Store(int64(s.Now()))
	ws.executed.Store(s.Executed())
}

// watchdogsStarted counts started watchdogs (TestGoroutineWatchdogDisabled).
var watchdogsStarted atomic.Int64

// watchdogsRunning counts watchdog goroutines that have not returned (TestStartWatchdogStopWaits).
var watchdogsRunning atomic.Int64

// startWatchdog starts the stall watchdog on a goroutine outside the bubble (GOR-081). It is the
// only wall-clock use of GOR (GOR §6).
func (r *runner) startWatchdog(st *testing.T, ga *goAttempt, attempt int) (stop func()) {
	watchdogsStarted.Add(1)
	timeout := r.plan.opts.StallTimeout
	period := max(timeout/4, time.Nanosecond)
	done, exited := make(chan struct{}), make(chan struct{})
	watchdogsRunning.Add(1)
	go func() {
		defer close(exited)
		defer watchdogsRunning.Add(-1)
		tk := time.NewTicker(period)
		defer tk.Stop()
		last, since := ga.watch.progress.Load(), time.Now()
		for {
			select {
			case <-done:
				return
			case now := <-tk.C:
				// GOR-081: final checks run on the driver without waiting for other goroutines, so
				// the final phase never counts as a stall.
				if p := ga.watch.progress.Load(); p != last || ga.watch.phase.Load() == watchFinal {
					last, since = p, now
				} else if now.Sub(since) >= timeout {
					select {
					case <-done: // the attempt ended while this tick was picked
						return
					default:
					}
					r.stall(st, ga, attempt)
				}
			}
		}
	}()
	// stop waits for the goroutine, so no watchdog outlives its attempt.
	return func() {
		close(done)
		<-exited
	}
}

// stall writes the stall report to stderr and stall.txt and ends the test binary (GOR-081).
func (r *runner) stall(st *testing.T, ga *goAttempt, attempt int) {
	ws := ga.watch
	var blocks []string
	for _, b := range splitStacks(allStacks(), ga.bubble.Load(), 0, false) {
		blocks = append(blocks, b.text)
	}
	last := ""
	if p := ws.label.Load(); p != nil {
		last = *p
	}
	text := formatStallReport(r.plan.opts.StallTimeout, st.Name(), strconv.Itoa(attempt),
		kernel.Time(ws.nowNS.Load()), watchPhaseNames[ws.phase.Load()], ws.executed.Load(), last,
		r.replay(ga.seed, nil).Command, ga.bubble.Load(), blocks)
	fmt.Fprint(os.Stderr, text)
	if dir := r.stallDir(ga.seed); dir != "" { // "" with FAULTLINE_ARTIFACTS=off
		writeStall(dir, text)
	}
	panic("faultline: goroutine mode stalled; see the report above")
}

// writeStall writes text to dir/stall.txt (GOR-081) and creates dir if it is missing. Like
// artifact.Write (ART-012), it never follows a symbolic link at dir and never opens an existing
// file: it skips a dir that is a symbolic link or not a directory, writes a new temporary file in
// dir and renames it to stall.txt, which replaces a symbolic link or a FIFO of that name. The
// file's mode is exactly 0o644. Errors are ignored.
func writeStall(dir, text string) {
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return
	}
	f, err := os.CreateTemp(dir, ".stall.txt.tmp-")
	if err != nil {
		return
	}
	tmp := f.Name()
	_, err = f.WriteString(text)
	if err == nil {
		err = f.Chmod(0o644) // after create, so the umask does not narrow it
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, filepath.Join(dir, "stall.txt"))
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
}

// stallDir returns the folder for stall.txt (GOR-081): the folder where writeArtifact would put
// the seed's artifact. That is artifact.AltDir when the seed's directory holds a report.json of
// another package, test or seed (ART-001), the same test ownReport makes, and "" when artifacts
// are off.
func (r *runner) stallDir(seed uint64) string {
	dir := r.artifactDir(seed)
	if dir == "" {
		return ""
	}
	pkg, test := r.build.importPath, r.t.Name()
	if rep, _ := artifact.ReadReportFile(dir); rep != nil && (rep.Package != pkg || rep.Test != test || rep.Seed != hex16(seed)) {
		return artifact.AltDir(r.artifactRoot(), pkg, test, seed)
	}
	return dir
}

// formatStallReport returns GOR-081's report text.
func formatStallReport(timeout time.Duration, test, attempt string, now kernel.Time, phase string,
	executed uint64, last, replay string, bubble uint64, blocks []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "faultline: goroutine mode stalled: no driver progress for %v of wall time\n", timeout)
	fmt.Fprintf(&b, "  test:     %s (attempt %s)\n", test, attempt)
	fmt.Fprintf(&b, "  virtual:  t=%s, phase %s, %d events executed, last event %q\n", now, phase, executed, last)
	fmt.Fprintf(&b, "  replay:   %s\n", replay)
	b.WriteString("  synctest advances only when every goroutine in the bubble is durably blocked.\n")
	b.WriteString("  Look for goroutines below whose state has no \"(durable)\":\n")
	b.WriteString("    [sync.Mutex.Lock] / [sync.RWMutex.Lock] / [sync.RWMutex.RLock]: waiting for a mutex whose\n")
	b.WriteString("        holder is blocked; do not hold a mutex across a blocking netsim, fsx or Process.Do call\n")
	b.WriteString("    [chan receive] / [chan send] / [select] without \"(durable)\": a channel created outside the\n")
	b.WriteString("        bubble, for example a package-level channel made during init\n")
	b.WriteString("    [IO wait] / [syscall]: real I/O that does not go through netsim or fsx\n")
	b.WriteString("    [running] / [runnable]: a goroutine that never blocks (busy loop)\n")
	fmt.Fprintf(&b, "goroutines of this simulation (synctest bubble %d):\n", bubble)
	for _, blk := range blocks {
		b.WriteString(blk)
		b.WriteString("\n\n")
	}
	return b.String()
}
