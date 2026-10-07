package fault

import "testing"

// pair filters the active set in place. In steady state, a pause and its resume allocate only
// the resume's Undoes; copying the active set on every call made 10,000 repeated links cost
// gigabytes. The ended entry is zeroed, so it does not keep its node names alive.
func TestPairAllocs(t *testing.T) {
	var p pairer
	id := 0
	step := func() {
		id += 2
		if u := p.pair(Event{Kind: KindPause, Node: "n1", ID: id - 1}); u != nil {
			t.Fatalf("pause ends %v", u)
		}
		if u := p.pair(Event{Kind: KindResume, Node: "n1", ID: id}); len(u) != 1 || u[0] != id-1 {
			t.Fatalf("resume ends %v, want [%d]", u, id-1)
		}
	}
	step()
	if n := testing.AllocsPerRun(100, step); n > 1 {
		t.Errorf("pause + resume: %v allocations, want at most 1", n)
	}
	if got := p.active[:cap(p.active)]; len(p.active) != 0 || len(got) == 0 || got[0] != (activeFault{}) {
		t.Errorf("active set after resume: len %d, backing %+v", len(p.active), got)
	}
}
