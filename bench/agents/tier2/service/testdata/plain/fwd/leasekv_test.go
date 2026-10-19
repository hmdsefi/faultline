package leasekv_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"example.com/leasekv"
)

const (
	ttl        = 100 * time.Millisecond
	renewEvery = 90 * time.Millisecond
	timeout    = 50 * time.Millisecond
	backoff    = 10 * time.Millisecond
	think      = 60 * time.Millisecond
	duration   = 7 * time.Second
	stopBefore = 500 * time.Millisecond // clients start no operation in the last stopBefore of a run
)

var keys = []string{"k1", "k2"}

// appends is the percent of operations that are appends, per client: c1 mostly reads.
var appends = []int{10, 50}

var leasePlan = plan{
	crashEvery: 1 * time.Second, crashMin: 10 * time.Millisecond, crashMax: 300 * time.Millisecond,
	partitionEvery: 700 * time.Millisecond, partitionMin: 250 * time.Millisecond, partitionMax: 450 * time.Millisecond,
}

// TestLeaseKV runs the lock, store replicas st1 and st2, servers s1 and s2, and clients c1 and
// c2 for 7 seconds. For the first 5.25 seconds it crashes s1, s2, st1 or st2, one at a time, and
// splits the network in two with s1 and s2, or st1 and st2, on different sides. Then it checks
// the replicas and the clients' operations.
func TestLeaseKV(t *testing.T) {
	c := newCluster()
	c.run(leasePlan)
	if err := c.check(); err != nil {
		t.Fatal(err)
	}
}

// check runs the checks and returns the first failure as one line: the check's name and the
// values involved.
func (c *cluster) check() error {
	c.mu.Lock()
	stores, err := slices.Clone(c.stores), c.err
	c.mu.Unlock()
	if err != nil {
		return fmt.Errorf("store failed: %v", err)
	}
	var ops []leasekv.Op
	started := make([]int, len(c.clients))
	for i, cl := range c.clients {
		n, done := cl.Ops()
		started[i] = n
		ops = append(ops, done...)
	}
	slices.SortStableFunc(ops, func(a, b leasekv.Op) int { return a.End.Compare(b.End) })
	checks := []struct {
		name string
		fn   func() error
	}{
		{"no duplicate appends", func() error { return noDuplicates(stores) }},
		{"reads are fresh", func() error { return c.freshReads(ops) }},
		{"acked appends survive", func() error { return c.ackedSurvive(stores, ops) }},
		{"replicas agree", func() error { return replicasAgree(stores) }},
		{"every op completes", func() error { return c.allComplete(started) }},
	}
	for _, ch := range checks {
		if err := ch.fn(); err != nil {
			return fmt.Errorf("%s: %v", ch.name, err)
		}
	}
	return nil
}

func (c *cluster) at(t time.Time) string { return t.Sub(c.origin).Round(time.Microsecond).String() }

func noDuplicates(stores []*leasekv.Store) error {
	for i, st := range stores {
		for _, k := range keys {
			seen := map[string]bool{}
			for _, v := range st.List(k) {
				if seen[v] {
					return fmt.Errorf("key %s on %s holds %s twice", k, replicas[i], v)
				}
				seen[v] = true
			}
		}
	}
	return nil
}

// readValue is a value that a get returned. A get that starts after end returns it too.
type readValue struct {
	value, client string
	end           time.Time
}

// freshReads checks every get against the appends acknowledged before it started, and against
// the values that gets which ended before it started returned. ops are in completion order.
func (c *cluster) freshReads(ops []leasekv.Op) error {
	acked := map[string][]leasekv.Op{}
	returned := map[string][]readValue{} // per key: the values gets returned, each with the first get
	for _, op := range ops {
		if op.Kind == "append" {
			acked[op.Key] = append(acked[op.Key], op)
			continue
		}
		for _, a := range acked[op.Key] {
			if !op.Start.Before(a.End) && !slices.Contains(op.List, a.Value) {
				return fmt.Errorf("get %s by %s, started at %s, returned %d values without %s, which %s had acked at %s",
					op.Key, op.Client, c.at(op.Start), len(op.List), a.Value, a.Client, c.at(a.End))
			}
		}
		for _, r := range returned[op.Key] {
			if !op.Start.Before(r.end) && !slices.Contains(op.List, r.value) {
				return fmt.Errorf("get %s by %s, started at %s, returned %d values without %s, which a get by %s had returned at %s",
					op.Key, op.Client, c.at(op.Start), len(op.List), r.value, r.client, c.at(r.end))
			}
		}
		for _, v := range op.List {
			if !slices.ContainsFunc(returned[op.Key], func(r readValue) bool { return r.value == v }) {
				returned[op.Key] = append(returned[op.Key], readValue{value: v, client: op.Client, end: op.End})
			}
		}
	}
	return nil
}

func (c *cluster) ackedSurvive(stores []*leasekv.Store, ops []leasekv.Op) error {
	for _, op := range ops {
		if op.Kind != "append" {
			continue
		}
		for i, st := range stores {
			if !slices.Contains(st.List(op.Key), op.Value) {
				return fmt.Errorf("append %s=%s by %s, acked at %s, is not on %s",
					op.Key, op.Value, op.Client, c.at(op.End), replicas[i])
			}
		}
	}
	return nil
}

func replicasAgree(stores []*leasekv.Store) error {
	for _, k := range keys {
		a, b := stores[0].List(k), stores[1].List(k)
		if !slices.Equal(a, b) {
			return fmt.Errorf("key %s: st1 holds %d values, st2 holds %d, and they differ", k, len(a), len(b))
		}
	}
	return nil
}

func (c *cluster) allComplete(started []int) error {
	for i, cl := range c.clients {
		_, done := cl.Ops()
		if len(done) < started[i] {
			return fmt.Errorf("c%d completed %d of %d ops", i+1, len(done), started[i])
		}
	}
	return nil
}
