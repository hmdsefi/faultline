package simnet

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
)

// NET-013: probabilities 0 and MaxPPM and zero maxima decide without drawing.
func TestDecideEdgesDrawNothing(t *testing.T) {
	links := []Link{
		{},
		{Latency: 3 * time.Millisecond},
		{DropPPM: MaxPPM, Jitter: time.Second},
		{DupPPM: MaxPPM, Latency: time.Millisecond},
		{TailPPM: MaxPPM},
	}
	for _, l := range links {
		r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // fixed seed: compared with a fresh copy below
		d := decide(l, r)
		if got, want := r.Uint64(), rand.New(rand.NewPCG(1, 2)).Uint64(); got != want { //nolint:gosec // a fresh copy of r's stream
			t.Errorf("decide(%+v) drew from the stream", l)
		}
		switch {
		case l.DropPPM == MaxPPM && !d.Drop:
			t.Errorf("decide(%+v) = %+v, want Drop", l, d)
		case l.DupPPM == MaxPPM && (!d.Dup || d.Delay != l.Latency || d.DupDelay != l.Latency):
			t.Errorf("decide(%+v) = %+v, want Dup with both delays %v", l, d, l.Latency)
		}
	}
}

// NET-013: exact draw order chance(Drop), uniform(Jitter), chance(Tail), uniform(Tail),
// chance(Dup), and the copy-2 delay draws.
func TestDecideDrawOrder(t *testing.T) {
	l := Link{Latency: time.Millisecond, Jitter: 4 * time.Millisecond, TailPPM: 100000,
		Tail: 50 * time.Millisecond, DropPPM: 300000, DupPPM: 200000}
	r1 := rand.New(rand.NewPCG(7, 7)) //nolint:gosec // fixed seed: r2 replays r1's draws
	r2 := rand.New(rand.NewPCG(7, 7)) //nolint:gosec // same seed as r1
	delay2 := func() time.Duration {
		x := l.Latency + kernel.Uniform(r2, 0, l.Jitter)
		if kernel.Chance(r2, l.TailPPM) {
			x += kernel.Uniform(r2, 0, l.Tail)
		}
		return x
	}
	drops, dups := 0, 0
	for i := 0; i < 500; i++ {
		got := decide(l, r1)
		var want Decision
		if kernel.Chance(r2, l.DropPPM) {
			want = Decision{Drop: true}
		} else {
			want.Delay = delay2()
			if kernel.Chance(r2, l.DupPPM) {
				want.Dup = true
				want.DupDelay = delay2()
			}
		}
		if got != want {
			t.Fatalf("message %d: decide = %+v, want %+v", i, got, want)
		}
		if got.Drop {
			drops++
		}
		if got.Dup {
			dups++
		}
	}
	if drops == 0 || dups == 0 {
		t.Fatalf("drops=%d dups=%d: both paths must be exercised", drops, dups)
	}
}
