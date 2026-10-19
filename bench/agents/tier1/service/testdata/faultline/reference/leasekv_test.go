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
	clientStop = duration - 500*time.Millisecond

	crashEvery     = 2 * time.Second
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

// leaseWorld builds one world: the lock, the store, servers s1 and s2, and clients c1 and c2.
// The fault plan crashes s1, s2 or the store, one at a time, and splits the network in two with
// s1 and s2 on different sides.
func leaseWorld(w *faultline.World) {
	lock := w.AddServer("lock", (&leasekv.Lock{W: w, TTL: ttl}).Boot)
	st := &leasekv.Store{W: w}
	store := w.AddServer("store", st.Boot)
	var servers []kernel.NodeID
	for _, name := range []string{"s1", "s2"} {
		s := &leasekv.Server{W: w, Lock: lock.ID(), Store: store.ID(), RenewEvery: renewEvery}
		servers = append(servers, w.AddServer(name, s.Boot).ID())
	}
	origin := w.Sim.Now().Std()
	var clients []*leasekv.Client
	for i, name := range []string{"c1", "c2"} {
		c := &leasekv.Client{
			W: w, Servers: servers, Keys: keys, Appends: appends[i], Stop: origin.Add(clientStop),
			Timeout: timeout, Backoff: backoff, Think: think,
		}
		w.AddClient(name, c.Boot)
		clients = append(clients, c)
	}
	crashable := []kernel.NodeID{servers[0], servers[1], store.ID()}
	w.Role("crashable", func() []kernel.NodeID { return crashable })
	w.Role("replicas", func() []kernel.NodeID { return servers })
	w.Plan(&fault.Random{MaxDown: 1, Rules: []fault.Rule{
		{Kind: fault.KindCrash, Target: "crashable", Every: crashEvery, MinFor: crashMin, MaxFor: crashMax},
		{Kind: fault.KindPartition, Target: "replicas", Every: partitionEvery, MinFor: partitionMin, MaxFor: partitionMax},
	}})

	c := &checker{store: st, clients: clients, origin: origin}
	w.Invariant("no duplicate appends", c.noDuplicates)
	w.Invariant("reads are fresh", c.freshReads)
	w.Final("acked appends survive", c.ackedSurvive)
	w.Final("every op completes", c.allComplete)
}

// checker holds the state of the checks. The invariants look only at what changed since their
// last call.
type checker struct {
	store   *leasekv.Store
	clients []*leasekv.Client
	origin  time.Time

	listed   map[string]int             // per key: values checked for duplicates
	values   map[string]map[string]bool // per key: values seen
	checked  []int                      // per client: completed ops checked for freshness
	acked    map[string][]leasekv.Op    // per key: acknowledged appends
	returned map[string][]readValue     // per key: the values gets returned, each with the first get
}

// readValue is a value that a get returned. A get that starts after end returns it too.
type readValue struct {
	value, client string
	end           time.Time
}

func (c *checker) at(t time.Time) string { return t.Sub(c.origin).Round(time.Microsecond).String() }

func (c *checker) noDuplicates() error {
	if c.values == nil {
		c.listed, c.values = map[string]int{}, map[string]map[string]bool{}
	}
	for _, k := range keys {
		list := c.store.List(k)
		if len(list) < c.listed[k] {
			c.listed[k], c.values[k] = 0, nil
		}
		if c.values[k] == nil {
			c.values[k] = map[string]bool{}
		}
		for _, v := range list[c.listed[k]:] {
			if c.values[k][v] {
				return fmt.Errorf("key %s holds %s twice", k, v)
			}
			c.values[k][v] = true
		}
		c.listed[k] = len(list)
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
			if op.Kind == "append" && !slices.Contains(c.store.List(op.Key), op.Value) {
				return fmt.Errorf("append %s=%s by %s, acked at %s, is not in the store",
					op.Key, op.Value, op.Client, c.at(op.End))
			}
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
