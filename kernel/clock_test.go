package kernel

import (
	"math"
	"math/big"
	"math/rand/v2"
	"testing"
	"time"
)

// AT-KRN-33
func TestLocalToGlobalVectors(t *testing.T) {
	cases := []struct {
		d    time.Duration
		ppm  int32
		want time.Duration
	}{
		{time.Second, 0, 1000000000},
		{time.Second, 100, 999900010},
		{time.Second, -100, 1000100011},
		{time.Second, 1000000, 500000000},
		{time.Second, -500000, 2000000000},
		{1, 100, 1},
		{3, 333333, 3},
		{24 * time.Hour, 250, 86378405398651},
		{math.MaxInt64, -500000, math.MaxInt64},
		{math.MaxInt64, 1, 9223362813491962316},
		{0, 100, 0},
		{-5, 100, 0},
	}
	for _, c := range cases {
		if got := localToGlobal(c.d, c.ppm); got != c.want {
			t.Errorf("localToGlobal(%d, %d) = %d, want %d", c.d, c.ppm, got, c.want)
		}
	}
}

// KRN-070: the drift term equals floor(delta*ppm/1e6) computed exactly.
func TestClockReading(t *testing.T) {
	c := clock{g0: 0, l0: sec(5), ppm: 100}
	if got := c.at(sec(10)); got != Time(15001*time.Millisecond) {
		t.Errorf("reading at 10s = %v, want 15.001s", got)
	}
	if got := (clock{ppm: -1}).at(1); got != 0 {
		t.Errorf("ppm -1 at 1ns = %d, want 0 (floor)", got)
	}
	if got := (clock{l0: math.MaxInt64 - 60, ppm: -500000}).at(1000); got != math.MaxInt64 {
		t.Errorf("reading near the maximum = %d, want %d (saturated, never backward)", got, int64(math.MaxInt64))
	}
	if got := (clock{ppm: MaxDriftPPM}).at(math.MaxInt64); got != math.MaxInt64 {
		t.Errorf("max drift at MaxInt64 = %d, want %d", got, int64(math.MaxInt64))
	}
	if got := (clock{ppm: MinDriftPPM}).at(math.MaxInt64); got != 4611686018427387903 {
		t.Errorf("min drift at MaxInt64 = %d, want 4611686018427387903", got)
	}
	r := rand.New(rand.NewPCG(3, 4))
	for i := 0; i < 10000; i++ {
		delta := int64(r.Uint64N(math.MaxInt64 / 2))
		ppm := MinDriftPPM + int32(r.Uint32N(uint32(MaxDriftPPM-MinDriftPPM+1)))
		c := clock{ppm: ppm}
		want := new(big.Int).Mul(big.NewInt(delta), big.NewInt(int64(ppm)))
		want.Div(want, big.NewInt(1_000_000)) // big.Int.Div rounds toward negative infinity for a positive divisor
		want.Add(want, big.NewInt(delta))
		if !want.IsInt64() {
			continue
		}
		if got := c.at(Time(delta)); int64(got) != want.Int64() {
			t.Fatalf("delta %d ppm %d: reading %d, want %s", delta, ppm, got, want)
		}
	}
}

func TestFloorDiv(t *testing.T) {
	cases := []struct{ a, b, want int64 }{{7, 2, 3}, {-7, 2, -4}, {-6, 2, -3}, {0, 5, 0}, {-1, 1_000_000, -1}}
	for _, c := range cases {
		if got := floorDiv(c.a, c.b); got != c.want {
			t.Errorf("floorDiv(%d, %d) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
