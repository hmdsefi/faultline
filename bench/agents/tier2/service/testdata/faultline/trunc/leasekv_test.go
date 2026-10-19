package leasekv_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"example.com/leasekv"
	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
)

const (
	ttl        = 100 * time.Millisecond
	renewEvery = 90 * time.Millisecond
	timeout    = 50 * time.Millisecond
	backoff    = 10 * time.Millisecond
	think      = 60 * time.Millisecond
	duration   = 7 * time.Second
	stopBefore = 500 * time.Millisecond // clients start no operation in the last stopBefore of a run

	crashEvery     = 1 * time.Second
	crashMin       = 10 * time.Millisecond
	crashMax       = 300 * time.Millisecond
	partitionEvery = 700 * time.Millisecond
	partitionMin   = 250 * time.Millisecond
	partitionMax   = 450 * time.Millisecond
)

var keys = []string{"k1", "k2"}

// appends is the percent of operations that are appends, per client: c1 mostly reads.
var appends = []int{10, 50}

var leaseOptions = faultline.Options{Seeds: 100, Duration: duration}

func TestLeaseKV(t *testing.T) { faultline.Run(t, leaseOptions, leaseWorld) }

// leaseFaults crashes s1, s2, st1 or st2, one at a time, and splits the network in two: s1 and
// s2 on different sides, or st1 and st2 on different sides, every other node on a random side.
var leaseFaults = []fault.Rule{
	{Kind: fault.KindCrash, Target: "crashable", Every: crashEvery, MinFor: crashMin, MaxFor: crashMax},
	{Kind: fault.KindPartition, Target: "servers", Every: partitionEvery, MinFor: partitionMin, MaxFor: partitionMax},
	{Kind: fault.KindPartition, Target: "stores", Every: partitionEvery, MinFor: partitionMin, MaxFor: partitionMax},
}

// leaseWorld builds one world: the lock, store replicas st1 and st2, servers s1 and s2, and
// clients c1 and c2, with the fault plan leaseFaults.
func leaseWorld(w *faultline.World) { buildWorld(w, leaseFaults) }

// buildWorld builds the world of leaseWorld with the fault rules rules and registers the checks.
func buildWorld(w *faultline.World, rules []fault.Rule) {
	lock := w.AddServer("lock", (&leasekv.Lock{W: w, TTL: ttl}).Boot).ID()
	var stores []*leasekv.Store
	var storeIDs []kernel.NodeID
	for _, name := range []string{"st1", "st2"} {
		st := &leasekv.Store{W: w, Lock: lock, RenewEvery: renewEvery}
		stores = append(stores, st)
		storeIDs = append(storeIDs, w.AddServer(name, st.Boot).ID())
	}
	stores[0].Peer, stores[1].Peer = storeIDs[1], storeIDs[0]
	var servers []kernel.NodeID
	for _, name := range []string{"s1", "s2"} {
		s := &leasekv.Server{W: w, Lock: lock, Stores: storeIDs, RenewEvery: renewEvery}
		servers = append(servers, w.AddServer(name, s.Boot).ID())
	}
	origin := w.Sim.Now().Std()
	var clients []*leasekv.Client
	for i, name := range []string{"c1", "c2"} {
		c := &leasekv.Client{
			W: w, Servers: servers, Keys: keys, Appends: appends[i], Stop: w.End().Add(-stopBefore).Std(),
			Timeout: timeout, Backoff: backoff, Think: think,
		}
		w.AddClient(name, c.Boot)
		clients = append(clients, c)
	}
	crashable := append(slices.Clone(servers), storeIDs...)
	w.Role("crashable", func() []kernel.NodeID { return crashable })
	w.Role("servers", func() []kernel.NodeID { return servers })
	w.Role("stores", func() []kernel.NodeID { return storeIDs })
	w.Plan(&fault.Random{MaxDown: 1, Rules: rules})

	c := &checker{stores: stores, clients: clients, origin: origin}
	w.Invariant("no duplicate appends", c.noDuplicates)
	w.Invariant("reads are fresh", c.freshReads)
	w.Final("acked appends survive", c.ackedSurvive)
	w.Final("replicas agree", c.replicasAgree)
	w.Final("every op completes", c.allComplete)
}

// replicas names the store replicas, in the order of checker.stores.
var replicas = []string{"st1", "st2"}

// checker holds the state of the checks. The invariants look only at what changed since their
// last call.
type checker struct {
	stores  []*leasekv.Store
	clients []*leasekv.Client
	origin  time.Time

	lists    map[string][]string     // per replica and key: the list checked for duplicates last
	checked  []int                   // per client: completed ops checked for freshness
	acked    map[string][]leasekv.Op // per key: acknowledged appends
	returned map[string][]readValue  // per key: the values gets returned, each with the first get
}

// readValue is a value that a get returned. A get that starts after end returns it too.
type readValue struct {
	value, client string
	end           time.Time
}

func (c *checker) at(t time.Time) string { return t.Sub(c.origin).Round(time.Microsecond).String() }

func (c *checker) noDuplicates() error {
	if c.lists == nil {
		c.lists = map[string][]string{}
	}
	for i, st := range c.stores {
		for _, k := range keys {
			id, list := replicas[i]+"/"+k, st.List(k)
			if slices.Equal(list, c.lists[id]) {
				continue
			}
			c.lists[id] = list
			seen := map[string]bool{}
			for _, v := range list {
				if seen[v] {
					return fmt.Errorf("key %s on %s holds %s twice", k, replicas[i], v)
				}
				seen[v] = true
			}
		}
	}
	return nil
}

// freshReads checks every get against the appends acknowledged before it started, and against
// the values that gets which ended before it started returned.
func (c *checker) freshReads() error {
	if c.checked == nil {
		c.checked, c.acked = make([]int, len(c.clients)), map[string][]leasekv.Op{}
		c.returned = map[string][]readValue{}
	}
	for i, cl := range c.clients {
		for ; c.checked[i] < len(cl.Done); c.checked[i]++ {
			op := cl.Done[c.checked[i]]
			if op.Kind == "append" {
				c.acked[op.Key] = append(c.acked[op.Key], op)
				continue
			}
			for _, a := range c.acked[op.Key] {
				if !op.Start.Before(a.End) && !slices.Contains(op.List, a.Value) {
					return fmt.Errorf("get %s by %s, started at %s, returned %d values without %s, which %s had acked at %s",
						op.Key, op.Client, c.at(op.Start), len(op.List), a.Value, a.Client, c.at(a.End))
				}
			}
			for _, r := range c.returned[op.Key] {
				if !op.Start.Before(r.end) && !slices.Contains(op.List, r.value) {
					return fmt.Errorf("get %s by %s, started at %s, returned %d values without %s, which a get by %s had returned at %s",
						op.Key, op.Client, c.at(op.Start), len(op.List), r.value, r.client, c.at(r.end))
				}
			}
			for _, v := range op.List {
				if !slices.ContainsFunc(c.returned[op.Key], func(r readValue) bool { return r.value == v }) {
					c.returned[op.Key] = append(c.returned[op.Key], readValue{value: v, client: op.Client, end: op.End})
				}
			}
		}
	}
	return nil
}

func (c *checker) ackedSurvive() error {
	for _, cl := range c.clients {
		for _, op := range cl.Done {
			if op.Kind != "append" {
				continue
			}
			for i, st := range c.stores {
				if !slices.Contains(st.List(op.Key), op.Value) {
					return fmt.Errorf("append %s=%s by %s, acked at %s, is not on %s",
						op.Key, op.Value, op.Client, c.at(op.End), replicas[i])
				}
			}
		}
	}
	return nil
}

func (c *checker) replicasAgree() error {
	for _, k := range keys {
		a, b := c.stores[0].List(k), c.stores[1].List(k)
		if !slices.Equal(a, b) {
			return fmt.Errorf("key %s: st1 holds %d values, st2 holds %d, and they differ", k, len(a), len(b))
		}
	}
	return nil
}

func (c *checker) allComplete() error {
	for i, cl := range c.clients {
		if len(cl.Done) < cl.Started {
			return fmt.Errorf("c%d completed %d of %d ops", i+1, len(cl.Done), cl.Started)
		}
	}
	return nil
}
