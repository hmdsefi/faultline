package leasekv_test

import (
	"context"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"example.com/leasekv"
)

// network delivers each message after a random delay of 1 to 5 ms. During a partition it drops
// messages between nodes on different sides, at send time and at delivery. It drops messages to
// a node that is down and from an earlier start of a node.
type network struct {
	mu     sync.Mutex
	handle map[string]func(from string, msg any) // nil while the node is down
	start  map[string]int                        // how many times each node started
	side   map[string]int                        // nil when there is no partition
}

func newNetwork() *network {
	return &network{handle: map[string]func(string, any){}, start: map[string]int{}}
}

// conn is one start of one node.
type conn struct {
	nw    *network
	from  string
	start int
}

func (c conn) Send(to string, msg any) {
	nw := c.nw
	nw.mu.Lock()
	ok := nw.start[c.from] == c.start && nw.handle[c.from] != nil && nw.connected(c.from, to)
	nw.mu.Unlock()
	if !ok {
		return
	}
	time.AfterFunc(time.Millisecond+rand.N(4*time.Millisecond), func() {
		nw.mu.Lock()
		h := nw.handle[to]
		if !nw.connected(c.from, to) {
			h = nil
		}
		nw.mu.Unlock()
		if h != nil {
			h(c.from, msg)
		}
	})
}

func (nw *network) connected(a, b string) bool { return nw.side == nil || nw.side[a] == nw.side[b] }

// next returns the conn of the next start of name. It sends nothing until up.
func (nw *network) next(name string) conn {
	nw.mu.Lock()
	defer nw.mu.Unlock()
	return conn{nw: nw, from: name, start: nw.start[name] + 1}
}

// up starts name: messages to it go to h.
func (nw *network) up(name string, h func(string, any)) {
	nw.mu.Lock()
	defer nw.mu.Unlock()
	nw.start[name]++
	nw.handle[name] = h
}

func (nw *network) down(name string) {
	nw.mu.Lock()
	defer nw.mu.Unlock()
	nw.handle[name] = nil
}

func (nw *network) partition(side map[string]int) {
	nw.mu.Lock()
	defer nw.mu.Unlock()
	nw.side = side
}

// disk is a store replica's disk. A crash keeps the synced bytes only, and the files of earlier
// starts stop working.
type disk struct {
	mu     sync.Mutex
	data   []byte
	synced int
	start  int
}

type diskFile struct {
	d     *disk
	start int
}

func (d *disk) open() *diskFile {
	d.mu.Lock()
	defer d.mu.Unlock()
	return &diskFile{d: d, start: d.start}
}

func (d *disk) crash() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.data = d.data[:d.synced]
	d.start++
}

func (f *diskFile) do(op func(d *disk)) error {
	f.d.mu.Lock()
	defer f.d.mu.Unlock()
	if f.start != f.d.start {
		return errStale
	}
	op(f.d)
	return nil
}

func (f *diskFile) ReadAll() (b []byte, err error) {
	err = f.do(func(d *disk) { b = slices.Clone(d.data) })
	return b, err
}

func (f *diskFile) Append(p []byte) error {
	return f.do(func(d *disk) { d.data = append(d.data, p...) })
}

func (f *diskFile) Sync() error { return f.do(func(d *disk) { d.synced = len(d.data) }) }

type staleError struct{}

func (staleError) Error() string { return "stale file" }

var errStale error = staleError{}

// plan is the fault plan: crashes of s1, s2, st1 or st2, one at a time, and partitions that put
// s1 and s2, or st1 and st2, on different sides and every other node on a random side. A rule
// starts a fault after a random gap of Every/2 to 3*Every/2, and the fault lasts min to max.
// There is one partition rule for each pair, with the same timing. Partitions do not overlap.
type plan struct {
	crashEvery, crashMin, crashMax             time.Duration
	partitionEvery, partitionMin, partitionMax time.Duration
}

// cluster is one run of the service.
type cluster struct {
	nw      *network
	disks   map[string]*disk
	origin  time.Time
	lock    *leasekv.Lock
	clients []*leasekv.Client

	mu        sync.Mutex
	stores    []*leasekv.Store // st1 and st2, the current start of each
	cancel    map[string]context.CancelFunc
	down      map[string]bool
	splitDone bool // a partition is active
	recovered bool
	err       error
}

var (
	servers   = []string{"s1", "s2"}
	replicas  = []string{"st1", "st2"}
	crashable = []string{"s1", "s2", "st1", "st2"}
	nodes     = []string{"lock", "st1", "st2", "s1", "s2", "c1", "c2"}
)

func newCluster() *cluster {
	c := &cluster{nw: newNetwork(), disks: map[string]*disk{"st1": {}, "st2": {}}, origin: time.Now(),
		stores: make([]*leasekv.Store, len(replicas)), cancel: map[string]context.CancelFunc{}, down: map[string]bool{}}
	c.lock = &leasekv.Lock{Conn: c.nw.next("lock"), TTL: ttl}
	c.nw.up("lock", c.lock.Handle)
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, name := range append(slices.Clone(replicas), servers...) {
		c.startNode(name)
	}
	for i, name := range []string{"c1", "c2"} {
		cl := &leasekv.Client{
			Name: name, Conn: c.nw.next(name), Servers: servers, Keys: keys,
			Appends: appends[i], Stop: c.origin.Add(duration - stopBefore),
			Timeout: timeout, Backoff: backoff, Think: think,
		}
		c.nw.up(name, cl.Handle)
		c.clients = append(c.clients, cl)
	}
	return c
}

// startNode starts s1, s2, st1 or st2 with empty memory. c.mu is held.
func (c *cluster) startNode(name string) {
	c.down[name] = false
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel[name] = cancel
	if i := slices.Index(replicas, name); i >= 0 {
		st := &leasekv.Store{Name: name, Conn: c.nw.next(name), Disk: c.disks[name].open(),
			Lock: "lock", Peer: replicas[1-i], RenewEvery: renewEvery}
		if err := st.Start(ctx); err != nil && c.err == nil {
			c.err = err
		}
		c.stores[i] = st
		c.nw.up(name, st.Handle)
		return
	}
	s := &leasekv.Server{Name: name, Conn: c.nw.next(name), Lock: "lock", Stores: replicas, RenewEvery: renewEvery}
	s.Start(ctx)
	c.nw.up(name, s.Handle)
}

// crashNode stops a node: its memory is gone and only synced bytes stay on its disk. c.mu is held.
func (c *cluster) crashNode(name string) {
	c.down[name] = true
	c.nw.down(name)
	c.cancel[name]()
	if d := c.disks[name]; d != nil {
		d.crash()
	}
}

// run runs the clients until they finish, with faults until the recovery starts, and then lets
// the nodes run to the end of the run. Then it stops every node.
func (c *cluster) run(p plan) {
	ctx, cancel := context.WithDeadline(context.Background(), c.origin.Add(duration))
	defer cancel()
	go c.rule(ctx, p.crashEvery, p.crashMin, p.crashMax, c.crash)
	for _, pair := range [][]string{servers, replicas} {
		go c.rule(ctx, p.partitionEvery, p.partitionMin, p.partitionMax, func() func() { return c.split(pair) })
	}
	time.AfterFunc(time.Until(c.origin.Add(duration*3/4)), c.recover)
	var wg sync.WaitGroup
	for _, cl := range c.clients {
		wg.Go(func() { cl.Run(ctx) })
	}
	wg.Wait()
	<-ctx.Done()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recovered = true
	for _, name := range nodes {
		c.nw.down(name)
	}
	for _, cancel := range c.cancel {
		cancel()
	}
}

// rule starts faults with start until the recovery. start returns the fault's undo, or nil when
// it starts nothing.
func (c *cluster) rule(ctx context.Context, every, minFor, maxFor time.Duration, start func() func()) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(every/2 + rand.N(every+1)):
		}
		c.mu.Lock()
		if c.recovered {
			c.mu.Unlock()
			return
		}
		undo := start()
		c.mu.Unlock()
		if undo != nil {
			time.AfterFunc(minFor+rand.N(maxFor-minFor+1), func() {
				c.mu.Lock()
				defer c.mu.Unlock()
				if !c.recovered {
					undo()
				}
			})
		}
	}
}

func (c *cluster) crash() func() {
	for _, name := range crashable {
		if c.down[name] {
			return nil
		}
	}
	name := crashable[rand.IntN(len(crashable))]
	c.crashNode(name)
	return func() { c.startNode(name) }
}

// split puts the two nodes of pair on different sides and every other node on a random side.
func (c *cluster) split(pair []string) func() {
	if c.splitDone {
		return nil
	}
	c.splitDone = true
	side := map[string]int{}
	for _, name := range nodes {
		side[name] = rand.IntN(2)
	}
	side[pair[1]] = 1 - side[pair[0]]
	c.nw.partition(side)
	return func() {
		c.nw.partition(nil)
		c.splitDone = false
	}
}

// recover ends every fault and starts no more.
func (c *cluster) recover() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recovered = true
	c.nw.partition(nil)
	for _, name := range crashable {
		if c.down[name] {
			c.startNode(name)
		}
	}
}
