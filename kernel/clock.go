package kernel

import (
	"math"
	"math/bits"
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
