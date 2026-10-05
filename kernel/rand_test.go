package kernel

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"
)

// AT-KRN-05 (derivation vectors; the Sim.Rand vector is in sim_test.go)
func TestStreamDerivationVectors(t *testing.T) {
	fnv := []struct {
		in   string
		want uint64
	}{
		{"", 0xcbf29ce484222325},
		{"kernel/sched", 0x043536c106fbc8c8},
		{"node/n1/1", 0x1cb6212d49cff067},
		{"a", 0xaf63dc4c8601ec8c},
	}
	for _, c := range fnv {
		if got := fnv1a64(c.in); got != c.want {
			t.Errorf("fnv1a64(%q) = %#016x, want %#016x", c.in, got, c.want)
		}
	}

	sm := []struct {
		state uint64
		want  [3]uint64
	}{
		{0, [3]uint64{0xe220a8397b1dcdaf, 0x6e789e6aa1b965f4, 0x06c45d188009454f}},
		{1, [3]uint64{0x910a2dec89025cc1, 0xbeeb8da1658eec67, 0xf893a2eefb32555e}},
	}
	for _, c := range sm {
		st := c.state
		for i, w := range c.want {
			if got := splitMix64(&st); got != w {
				t.Errorf("splitMix64 from %d, call %d = %#016x, want %#016x", c.state, i+1, got, w)
			}
		}
	}

	streams := []struct {
		seed         uint64
		label        string
		seed1, seed2 uint64
		first        []uint64
	}{
		{1, "kernel/sched", 0x46efa33d0374af3a, 0x79016df9a4656287,
			[]uint64{0xa84ae634d0a57409, 0xd51dac9b82a7a946, 0x7e1b91956417dda5}},
		{0, "kernel/sched", 0xd092960b270d92e2, 0x3dca573cde69019b,
			[]uint64{0xafd019cb714e351d, 0xcdcd0695d8dcfc32, 0x27c55309c30015cf}},
		{0x5e1f9a2c4b7d3e80, "node/n1/1", 0x5c1281747d0aba77, 0xe2bb20dad803fbbf,
			[]uint64{0x5f9d802d8bdde2bf, 0xeb30757d16b16fcf, 0x7128fb83006546a8}},
		{1, "net/link/1/2", 0x68c90d3ccb254143, 0xaca4fac5f37f3ed1,
			[]uint64{0xf735ce98737a176b, 0x32ff83603189633d, 0x88167cf6e43cbb90}},
		{0xffffffffffffffff, "workload/kv", 0xd17778cce0d51668, 0x81c3e39117bece22,
			[]uint64{0x2d030e98dfb8de7f, 0x42a410f67ad59ec7, 0xf7fe492a3775b84f}},
		{1, "workload/test", 0x5bcfa227f91652dd, 0x0841ed65e7f97644,
			[]uint64{0xdb41245153972361, 0x51b0c0ea0dba085f, 0xd63feaa04963d9b5,
				0xed1cc375c76adcd7, 0xbbc4f6e64a27ca30, 0xf8f880dce2dacf4c}},
	}
	for _, c := range streams {
		s1, s2 := streamSeeds(c.seed, c.label)
		if s1 != c.seed1 || s2 != c.seed2 {
			t.Errorf("streamSeeds(%#x, %q) = (%#016x, %#016x), want (%#016x, %#016x)",
				c.seed, c.label, s1, s2, c.seed1, c.seed2)
		}
		r := newStream(c.seed, c.label)
		for i, w := range c.first {
			if got := r.Uint64(); got != w {
				t.Errorf("stream (%#x, %q) output %d = %#016x, want %#016x", c.seed, c.label, i+1, got, w)
			}
		}
	}
}

// testStream returns a fresh stream with seed 1 and label "workload/test" (AT-KRN-07).
func testStream() *rand.Rand { return newStream(1, "workload/test") }

// AT-KRN-07
func TestChanceAndUniform(t *testing.T) {
	r := testStream()
	for i, want := range []bool{false, true, false} {
		if got := Chance(r, 500000); got != want {
			t.Errorf("Chance #%d = %v, want %v", i+1, got, want)
		}
	}
	for i, want := range []time.Duration{19262202, 17334742, 19725419} {
		if got := Uniform(r, 10*time.Millisecond, 20*time.Millisecond); got != want {
			t.Errorf("Uniform #%d = %d, want %d", i+1, got, want)
		}
	}

	r = testStream()
	if Chance(r, 0) {
		t.Error("Chance(r, 0) = true")
	}
	if !Chance(r, 1000000) {
		t.Error("Chance(r, 1000000) = false")
	}
	if got := Uniform(r, 5, 5); got != 5 {
		t.Errorf("Uniform(r, 5, 5) = %d", got)
	}
	if got := r.Uint64(); got != 0xed1cc375c76adcd7 {
		t.Errorf("next raw output = %#016x, want the fourth raw output 0xed1cc375c76adcd7", got)
	}

	mustPanic(t, "kernel: Uniform: min 2ns > max 1ns", func() { Uniform(r, 2, 1) })

	r = testStream()
	probe := testStream()
	x := probe.Uint64()
	if got, want := Uniform(r, math.MinInt64, math.MaxInt64), time.Duration(math.MinInt64)+time.Duration(x); got != want {
		t.Errorf("Uniform(full range) = %d, want %d", got, want)
	}
}
