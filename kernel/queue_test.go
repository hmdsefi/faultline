// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package kernel

import (
	"math/rand/v2"
	"slices"
	"testing"
)

// KRN-030: the heap pops entries in (at, tiebreak, seq) order under random pushes and removals.
func TestQueueOrder(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // fixed seed: repeatable test inputs
	var q queue
	var model []*entry
	seq := uint64(0)
	for i := 0; i < 20000; i++ {
		switch op := r.IntN(10); {
		case op < 5:
			seq++
			e := &entry{at: Time(r.IntN(50)), tb: r.Uint64N(4), seq: seq, idx: -1}
			q.push(e)
			model = append(model, e)
		case op < 7 && len(model) > 0:
			k := r.IntN(len(model))
			e := model[k]
			if got := q.remove(e.idx); got != e || e.idx != -1 {
				t.Fatalf("remove returned %p (idx %d), want %p", got, e.idx, e)
			}
			model = slices.Delete(model, k, k+1)
		case len(model) > 0:
			want := slices.MinFunc(model, func(a, b *entry) int {
				if a.before(b) {
					return -1
				}
				return 1
			})
			if got := q.pop(); got != want {
				t.Fatalf("pop = (%d,%d,%d), want (%d,%d,%d)", got.at, got.tb, got.seq, want.at, want.tb, want.seq)
			}
			model = slices.DeleteFunc(model, func(e *entry) bool { return e == want })
		}
		for j, e := range q {
			if e.idx != j {
				t.Fatalf("entry at heap index %d has idx %d", j, e.idx)
			}
		}
	}
}
