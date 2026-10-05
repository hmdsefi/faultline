package kernel

import (
	"math"
	"strconv"
	"time"
)

// Epoch is the instant that virtual time 0 represents. It equals the start time of a
// testing/synctest bubble.
var Epoch = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// Time is a virtual instant in nanoseconds since Epoch.
type Time int64

// Add returns t+d. The result saturates at math.MinInt64 and math.MaxInt64 instead of wrapping.
func (t Time) Add(d time.Duration) Time {
	return Time(addSat(int64(t), int64(d)))
}

// Sub returns t-u. The result saturates at math.MinInt64 and math.MaxInt64 nanoseconds.
func (t Time) Sub(u Time) time.Duration {
	return time.Duration(subSat(int64(t), int64(u)))
}

// Std returns Epoch.Add(time.Duration(t)): a UTC time.Time without a monotonic clock reading.
func (t Time) Std() time.Time {
	return Epoch.Add(time.Duration(t))
}

// String formats t as decimal seconds with exactly nine fractional digits and the suffix "s",
// for example "0.000000000s", "41.207000000s", "-0.000000001s". See KRN-002.
func (t Time) String() string {
	u := uint64(t)
	if t < 0 {
		u = -u
	}
	var buf [32]byte
	b := buf[:0]
	if t < 0 {
		b = append(b, '-')
	}
	b = strconv.AppendUint(b, u/1_000_000_000, 10)
	b = append(b, '.')
	frac := u % 1_000_000_000
	var digits [9]byte
	for i := len(digits) - 1; i >= 0; i-- {
		digits[i] = byte('0' + frac%10)
		frac /= 10
	}
	b = append(b, digits[:]...)
	b = append(b, 's')
	return string(b)
}

// addSat returns a+b, saturated to the int64 range.
func addSat(a, b int64) int64 {
	s := a + b
	if (a >= 0) == (b >= 0) && (s >= 0) != (a >= 0) {
		if a >= 0 {
			return math.MaxInt64
		}
		return math.MinInt64
	}
	return s
}

// subSat returns a-b, saturated to the int64 range.
func subSat(a, b int64) int64 {
	d := a - b
	if (a >= 0) != (b >= 0) && (d >= 0) != (a >= 0) {
		if a >= 0 {
			return math.MaxInt64
		}
		return math.MinInt64
	}
	return d
}
