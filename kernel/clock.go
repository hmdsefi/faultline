package kernel

import (
	"math"
	"math/bits"
	"strconv"
	"time"
)

// Drift limits: local clocks run between half and double speed.
const (
	MinDriftPPM int32 = -500_000
	MaxDriftPPM int32 = 1_000_000
)

// clock is a node's local clock (KRN-070): the local reading l0 at global time g0, and the drift
// ppm, which is in [MinDriftPPM, MaxDriftPPM].
type clock struct {
	g0  Time
	l0  Time
	ppm int32
}

// at returns the local reading at global time now (now >= g0) (KRN-070).
func (c clock) at(now Time) Time {
	delta := int64(now - c.g0)
	q, r := delta/1_000_000, delta%1_000_000
	drift := q*int64(c.ppm) + floorDiv(r*int64(c.ppm), 1_000_000) // floor(delta*ppm/1e6), exact
	if drift < 0 {
		return c.l0.Add(time.Duration(delta + drift)) // delta+drift in [delta/2, delta]: no overflow
	}
	return c.l0.Add(time.Duration(delta)).Add(time.Duration(drift))
}

// floorDiv returns a/b rounded toward negative infinity, for b > 0.
func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// localToGlobal converts the local duration d into the global duration that elapses on a clock
// with drift ppm, rounded up so a timer never fires early on the local clock (KRN-072). ppm must be
// in [MinDriftPPM, MaxDriftPPM]; d <= 0 gives 0, and the result saturates at math.MaxInt64.
func localToGlobal(d time.Duration, ppm int32) time.Duration {
	if d <= 0 {
		return 0
	}
	den := uint64(1_000_000 + int64(ppm)) // in [500_000, 2_000_000]
	hi, lo := bits.Mul64(uint64(d), 1_000_000)
	if hi >= den {
		return math.MaxInt64 // quotient does not fit: saturate
	}
	q, rem := bits.Div64(hi, lo, den)
	if rem != 0 {
		q++ // ceil: never fire early on the local clock
	}
	if q > math.MaxInt64 {
		return math.MaxInt64
	}
	return time.Duration(q)
}

// LocalTime returns the node's local clock reading as a virtual Time (KRN-074).
func (n *Node) LocalTime() Time { return n.clk.at(n.sim.now) }

// Now returns the node's local wall time: n.LocalTime().Std().
func (n *Node) Now() time.Time { return n.LocalTime().Std() }

// Drift returns the node's current drift in parts per million.
func (n *Node) Drift() int32 { return n.clk.ppm }

// JumpClock steps the local clock by d (may be negative). Scheduled timers keep their global
// times (KRN-071).
func (n *Node) JumpClock(d time.Duration) {
	now := n.sim.now
	n.clk = clock{g0: now, l0: n.clk.at(now).Add(d), ppm: n.clk.ppm}
	n.sim.emit(Record{Kind: "kernel.clock_jump", Node: n.id, Inc: n.inc, Text: "clock jump", Attrs: []Attr{
		{Key: "delta_ns", Value: strconv.FormatInt(int64(d), 10)},
		{Key: "local_ns", Value: strconv.FormatInt(int64(n.clk.l0), 10)},
	}})
}

// SetDrift changes the drift from now on. Scheduled timers keep their global times (KRN-071). It
// panics if ppm is outside [MinDriftPPM, MaxDriftPPM].
func (n *Node) SetDrift(ppm int32) {
	checkDrift(ppm)
	now := n.sim.now
	prev := n.clk.ppm
	n.clk = clock{g0: now, l0: n.clk.at(now), ppm: ppm}
	n.sim.emit(Record{Kind: "kernel.clock_drift", Node: n.id, Inc: n.inc, Text: "clock drift", Attrs: []Attr{
		{Key: "ppm", Value: strconv.FormatInt(int64(ppm), 10)},
		{Key: "prev_ppm", Value: strconv.FormatInt(int64(prev), 10)},
	}})
}
