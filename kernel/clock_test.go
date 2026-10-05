package kernel

import (
	"math"
	"math/big"
	"math/rand/v2"
	"strings"
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

// AT-KRN-32
func TestNodeClock(t *testing.T) {
	s := fullSim(1)
	var firedAt Time = -1
	c1 := s.AddNode("c1", func(n *Node) { n.After(time.Second, "t", func() { firedAt = s.Now() }) },
		WithClock(5*time.Second, 100))
	s.RunUntil(sec(10))
	if firedAt != 999900010 {
		t.Fatalf("timer fired at %d, want 999900010", firedAt)
	}
	if got := c1.LocalTime(); got != Time(15001*time.Millisecond) {
		t.Fatalf("LocalTime = %v, want 15.001s", got)
	}
	if got := c1.Now().Format(time.RFC3339Nano); got != "2000-01-01T00:00:15.001Z" {
		t.Fatalf("Now() = %s", got)
	}
	if c1.Drift() != 100 {
		t.Fatalf("Drift = %d", c1.Drift())
	}
	c1.JumpClock(-2 * time.Second)
	if got := c1.LocalTime(); got != Time(13001*time.Millisecond) {
		t.Fatalf("after JumpClock: %v", got)
	}
	if got := recLine(lastRecord(s)); !strings.HasSuffix(got, `1/1 kernel.clock_jump cause=5 "clock jump" [delta_ns=-2000000000 local_ns=13001000000]`) {
		t.Fatalf("clock_jump record %s", got)
	}
	c1.SetDrift(-200)
	if got := recLine(lastRecord(s)); !strings.HasSuffix(got, `kernel.clock_drift cause=6 "clock drift" [ppm=-200 prev_ppm=100]`) {
		t.Fatalf("clock_drift record %s", got)
	}
	s.RunUntil(sec(20))
	if got := c1.LocalTime(); got != Time(22999*time.Millisecond) {
		t.Fatalf("LocalTime at 20s = %v, want 22.999s", got)
	}
	mustPanic(t, "kernel: drift 1000001 ppm out of range [-500000, 1000000]", func() { c1.SetDrift(1_000_001) })
	before := c1.LocalTime()
	c1.Crash()
	c1.Restart()
	c1.Pause()
	c1.Resume()
	if c1.LocalTime() != before {
		t.Fatalf("lifecycle changed the clock: %v -> %v", before, c1.LocalTime())
	}
}

// KRN-071: changing the drift does not retime scheduled timers.
func TestSetDriftKeepsTimers(t *testing.T) {
	s := New(Config{Seed: 1})
	var firedAt Time = -1
	n := s.AddNode("n", func(n *Node) { n.After(time.Second, "t", func() { firedAt = s.Now() }) })
	s.RunUntil(0)
	n.SetDrift(1_000_000)
	n.JumpClock(time.Hour)
	s.Run()
	if firedAt != sec(1) {
		t.Fatalf("timer fired at %v, want 1s", firedAt)
	}
}

// KRN-071, KRN-073: SetDrift keeps the reading continuous, a rejected SetDrift changes nothing, the
// lifecycle leaves the clock alone, and JumpClock saturates (KRN-001).
func TestClockRebase(t *testing.T) {
	s := fullSim(1)
	n := s.AddNode("n", func(*Node) {}, WithClock(0, 100))
	s.RunUntil(sec(10))
	n.SetDrift(-200)
	if got := n.LocalTime(); got != Time(10001*time.Millisecond) {
		t.Fatalf("SetDrift moved the reading: %v", got)
	}
	if r := lastRecord(s); r.Node != n.ID() || r.Inc != 1 {
		t.Fatalf("clock_drift record %s", recLine(r))
	}
	mustPanic(t, "kernel: drift -500001 ppm out of range [-500000, 1000000]", func() { n.SetDrift(MinDriftPPM - 1) })
	if n.Drift() != -200 || lastRecord(s).Kind != "kernel.clock_drift" {
		t.Fatalf("rejected SetDrift changed the node: drift %d, last %s", n.Drift(), recLine(lastRecord(s)))
	}
	s.RunUntil(sec(20))
	n.Crash()
	n.Restart()
	n.Pause()
	n.Resume()
	s.RunUntil(sec(30))
	if n.Drift() != -200 || n.LocalTime() != Time(29997*time.Millisecond) {
		t.Fatalf("after lifecycle: drift %d, LocalTime %v, want -200 and 29.997s", n.Drift(), n.LocalTime())
	}
	n.JumpClock(math.MaxInt64)
	n.JumpClock(math.MaxInt64)
	if n.LocalTime() != math.MaxInt64 {
		t.Fatalf("JumpClock did not saturate: %v", n.LocalTime())
	}
}
