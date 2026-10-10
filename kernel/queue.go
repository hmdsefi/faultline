// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package kernel

// entry is one scheduled event: in the queue, or deferred while its node is paused.
type entry struct {
	at    Time
	tb    uint64 // tie-break
	seq   uint64
	id    EventID
	cause uint64 // Cause() when the event was scheduled
	fn    func()
	label string
	node  *Node  // nil for global entries
	inc   uint32 // node incarnation captured at scheduling
	kind  entryKind
	idx   int // index in the heap; -1 when not in the heap
}

// before reports whether a sorts before b by (at, tiebreak, seq) (KRN-030). seq is unique, so
// the order is total and the pop order never depends on the heap layout.
func (a *entry) before(b *entry) bool {
	if a.at != b.at {
		return a.at < b.at
	}
	if a.tb != b.tb {
		return a.tb < b.tb
	}
	return a.seq < b.seq
}

// queue is a binary min-heap of entries ordered by entry.before.
type queue []*entry

// push inserts e.
func (q *queue) push(e *entry) {
	e.idx = len(*q)
	*q = append(*q, e)
	q.up(e.idx)
}

// pop removes and returns the minimum entry. The queue must not be empty.
func (q *queue) pop() *entry { return q.remove(0) }

// remove removes and returns the entry at heap index i.
func (q *queue) remove(i int) *entry {
	h := *q
	last := len(h) - 1
	e := h[i]
	if i != last {
		h[i] = h[last]
		h[i].idx = i
	}
	h[last] = nil
	*q = h[:last]
	if i != last {
		if !q.down(i) {
			q.up(i)
		}
	}
	e.idx = -1
	return e
}

// up sifts the entry at i towards the root.
func (q queue) up(i int) {
	for i > 0 {
		p := (i - 1) / 2
		if !q[i].before(q[p]) {
			break
		}
		q.swap(i, p)
		i = p
	}
}

// down sifts the entry at i towards the leaves and reports whether it moved.
func (q queue) down(i int) bool {
	start := i
	n := len(q)
	for {
		l := 2*i + 1
		if l >= n {
			break
		}
		m := l
		if r := l + 1; r < n && q[r].before(q[l]) {
			m = r
		}
		if !q[m].before(q[i]) {
			break
		}
		q.swap(i, m)
		i = m
	}
	return i > start
}

func (q queue) swap(i, j int) {
	q[i], q[j] = q[j], q[i]
	q[i].idx = i
	q[j].idx = j
}

// entryKind tells what an entry is bound to.
type entryKind uint8

const (
	entryGlobal entryKind = iota // Sim.At, Sim.After, Sim.AtFront: never stale
	entryNode                    // Node.After, Node.Post: bound to an incarnation
	entryBoot                    // the initial boot event of AddNode
)

// stale reports whether the entry must be discarded instead of run (KRN-031).
func (e *entry) stale() bool {
	switch e.kind {
	case entryNode:
		return e.node.state == NodeDown || e.node.inc != e.inc
	case entryBoot:
		return e.node.inc != 0 || e.node.state != NodeDown
	}
	return false
}
