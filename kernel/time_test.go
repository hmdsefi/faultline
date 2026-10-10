// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package kernel

import (
	"math"
	"testing"
	"time"
)

// AT-KRN-01
func TestTimeString(t *testing.T) {
	cases := []struct {
		in   Time
		want string
	}{
		{0, "0.000000000s"},
		{1, "0.000000001s"},
		{999999999, "0.999999999s"},
		{1000000000, "1.000000000s"},
		{1500000000, "1.500000000s"},
		{41207000000, "41.207000000s"},
		{-1, "-0.000000001s"},
		{-1500000000, "-1.500000000s"},
		{math.MaxInt64, "9223372036.854775807s"},
		{math.MinInt64, "-9223372036.854775808s"},
	}
	for _, c := range cases {
		if got := c.in.String(); got != c.want {
			t.Errorf("Time(%d).String() = %q, want %q", int64(c.in), got, c.want)
		}
	}
}

// AT-KRN-02
func TestTimeArithmetic(t *testing.T) {
	if got := Time(math.MaxInt64).Add(1); got != math.MaxInt64 {
		t.Errorf("MaxInt64.Add(1) = %d", int64(got))
	}
	if got := Time(math.MinInt64).Add(-1); got != math.MinInt64 {
		t.Errorf("MinInt64.Add(-1) = %d", int64(got))
	}
	if got := Time(5).Add(-7); got != -2 {
		t.Errorf("Time(5).Add(-7) = %d", int64(got))
	}
	if got := Time(math.MinInt64).Sub(1); got != math.MinInt64 {
		t.Errorf("MinInt64.Sub(1) = %d", int64(got))
	}
	if got := Time(math.MaxInt64).Sub(-1); got != math.MaxInt64 {
		t.Errorf("MaxInt64.Sub(-1) = %d", int64(got))
	}
	if got := Time(3).Sub(5); got != -2 {
		t.Errorf("Time(3).Sub(5) = %d", int64(got))
	}
	if !Time(0).Std().Equal(Epoch) {
		t.Errorf("Time(0).Std() = %v, want Epoch", Time(0).Std())
	}
	if got := Time(1500 * time.Millisecond).Std().Format(time.RFC3339Nano); got != "2000-01-01T00:00:01.5Z" {
		t.Errorf("Std().Format = %q", got)
	}
}
