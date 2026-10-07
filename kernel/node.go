package kernel

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"time"
)

// NodeID identifies a node. IDs are dense, start at 1, and follow AddNode order. 0 means global.
type NodeID int32

// NodeState is the lifecycle state of a node.
type NodeState uint8

const (
	NodeUp NodeState = iota + 1
	NodeDown
	NodePaused
)

// String returns "up", "down", "paused", or "NodeState(<n>)".
func (st NodeState) String() string {
	switch st {
	case NodeUp:
		return "up"
	case NodeDown:
		return "down"
	case NodePaused:
		return "paused"
	}
	return "NodeState(" + strconv.Itoa(int(st)) + ")"
}

// BootFunc installs a node's behavior. It runs at every boot.
type BootFunc func(n *Node)

// NodeConfig holds the options of AddNode.
type NodeConfig struct {
	ClockOffset time.Duration // initial local clock offset
	DriftPPM    int32         // initial drift, MinDriftPPM..MaxDriftPPM
	Tags        []string      // tags in the order given; duplicates removed
}

// NodeOption sets fields of a NodeConfig.
type NodeOption func(*NodeConfig)

// WithClock sets ClockOffset and DriftPPM. The last WithClock wins.
func WithClock(offset time.Duration, driftPPM int32) NodeOption {
	return func(c *NodeConfig) {
		c.ClockOffset = offset
		c.DriftPPM = driftPPM
	}
}

// WithTags appends tags. Several WithTags accumulate.
func WithTags(tags ...string) NodeOption {
	return func(c *NodeConfig) { c.Tags = append(c.Tags, tags...) }
}

// Node is a logical machine. The *Node value is shared by all incarnations.
type Node struct {
	sim      *Sim
	id       NodeID
	name     string
	tags     []string
	boot     BootFunc
	state    NodeState
	inc      uint32
	clk      clock      // belongs to the node, not to an incarnation (KRN-073)
	bootEv   *entry     // the initial boot event while it is pending
	deferred []*entry   // entries deferred while paused, in processing order
	rnd      *rand.Rand // the stream of incarnation rndInc
	rndInc   uint32
}

// AddNode creates a node in state NodeDown with incarnation 0 and schedules its initial boot at
// Now(). It panics on an invalid or duplicate name, a nil boot, or invalid options (KRN §7).
func (s *Sim) AddNode(name string, boot BootFunc, opts ...NodeOption) *Node {
	if !validName(name) {
		panic(fmt.Sprintf("kernel: invalid node name %q: want 1-64 characters [A-Za-z0-9._-], starting with a letter or digit", name))
	}
	if s.byName[name] != nil {
		panic(fmt.Sprintf("kernel: duplicate node name %q", name))
	}
	if boot == nil {
		panic(fmt.Sprintf("kernel: nil BootFunc for node %q", name))
	}
	var cfg NodeConfig
	for _, o := range opts {
		o(&cfg)
	}
	checkDrift(cfg.DriftPPM)
	var tags []string
	for _, tag := range cfg.Tags {
		if !validName(tag) {
			panic(fmt.Sprintf("kernel: invalid tag %q for node %q", tag, name))
		}
		if !slices.Contains(tags, tag) {
			tags = append(tags, tag)
		}
	}
	n := &Node{
		sim:   s,
		id:    NodeID(len(s.nodes) + 1), //nolint:gosec // not checked: KRN-056 sets no limit, but 2^31 nodes need over 300 GB
		name:  name,
		tags:  tags,
		boot:  boot,
		state: NodeDown,
		clk:   clock{g0: s.now, l0: s.now.Add(cfg.ClockOffset), ppm: cfg.DriftPPM},
	}
	s.nodes = append(s.nodes, n)
	s.byName[name] = n
	s.emit(Record{
		Kind: "kernel.add_node",
		Node: n.id,
		Text: name,
		Attrs: []Attr{
			{Key: "tags", Value: strings.Join(tags, ",")},
			{Key: "offset_ns", Value: strconv.FormatInt(int64(cfg.ClockOffset), 10)},
			{Key: "drift_ppm", Value: strconv.FormatInt(int64(cfg.DriftPPM), 10)},
		},
	})
	e := s.schedule(s.now, "boot", func() { s.bootProc(n) }, false)
	e.node, e.kind = n, entryBoot
	n.bootEv = e
	return n
}

// validName reports whether s matches ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ (KRN-055).
func validName(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9':
		case i > 0 && (c == '.' || c == '_' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// checkDrift panics if ppm is outside [MinDriftPPM, MaxDriftPPM].
func checkDrift(ppm int32) {
	if ppm < MinDriftPPM || ppm > MaxDriftPPM {
		panic(fmt.Sprintf("kernel: drift %d ppm out of range [-500000, 1000000]", ppm))
	}
}

// Node returns the node with the given ID, or nil if there is none.
func (s *Sim) Node(id NodeID) *Node {
	if id < 1 || int(id) > len(s.nodes) {
		return nil
	}
	return s.nodes[id-1]
}

// Lookup returns the node with the given name, or nil if there is none.
func (s *Sim) Lookup(name string) *Node { return s.byName[name] }

// Nodes returns a new slice of all nodes in ID order.
func (s *Sim) Nodes() []*Node { return append([]*Node(nil), s.nodes...) }

// ID returns the node's ID.
func (n *Node) ID() NodeID { return n.id }

// Name returns the node's name.
func (n *Node) Name() string { return n.name }

// Tags returns a copy of the node's tags in the order given, without duplicates.
func (n *Node) Tags() []string { return slices.Clone(n.tags) }

// HasTag reports whether the node has tag.
func (n *Node) HasTag(tag string) bool { return slices.Contains(n.tags, tag) }

// Sim returns the Sim the node belongs to.
func (n *Node) Sim() *Sim { return n.sim }

// State returns the node's lifecycle state.
func (n *Node) State() NodeState { return n.state }

// Incarnation returns the current incarnation: 0 before the first boot.
func (n *Node) Incarnation() uint32 { return n.inc }

// Rand returns the stream "node/<name>/<incarnation>" for the current incarnation. Node streams are
// not kept in the Sim: an old incarnation's stream is never returned again (KRN-080).
func (n *Node) Rand() *rand.Rand {
	if n.rnd == nil || n.rndInc != n.inc {
		n.rnd = newStream(n.sim.cfg.Seed, "node/"+n.name+"/"+strconv.FormatUint(uint64(n.inc), 10))
		n.rndInc = n.inc
	}
	return n.rnd
}

// After schedules fn after the local duration d, bound to the current incarnation.
// It returns 0 and schedules nothing if the node is down.
func (n *Node) After(d time.Duration, label string, fn func()) EventID {
	checkEvent(label, fn)
	if n.state == NodeDown {
		return 0
	}
	s := n.sim
	e := s.schedule(s.now.Add(localToGlobal(d, n.clk.ppm)), label, fn, false)
	e.node, e.inc, e.kind = n, n.inc, entryNode
	return e.id
}

// Post is After(0, label, fn).
func (n *Node) Post(label string, fn func()) EventID { return n.After(0, label, fn) }

// Cancel cancels an event scheduled through n.After or n.Post. Other IDs return false.
func (n *Node) Cancel(id EventID) bool {
	e, ok := n.sim.pending[id]
	if !ok || e.kind != entryNode || e.node != n {
		return false
	}
	return n.sim.Cancel(id)
}

// Crash takes the node down (KRN-060, KRN-062). Crashing a node before its first boot cancels the
// boot. Crashing a down node is otherwise a no-op.
func (n *Node) Crash() {
	s := n.sim
	switch n.state {
	case NodeUp, NodePaused:
		prev := n.state
		n.state = NodeDown
		if prev == NodePaused {
			for _, e := range n.deferred {
				s.discard(e)
			}
			n.deferred = nil
		}
		s.emit(Record{Kind: "kernel.crash", Node: n.id, Inc: n.inc, Text: "crash",
			Attrs: []Attr{{Key: "from", Value: prev.String()}}})
		hooks := s.crashHooks
		s.call(n, "crash", func() {
			for _, h := range hooks {
				h(n)
			}
		})
	case NodeDown:
		if n.inc == 0 && n.bootEv != nil {
			e := n.bootEv
			s.q.remove(e.idx)
			s.discard(e)
			s.emit(Record{Kind: "kernel.crash", Node: n.id, Text: "crash",
				Attrs: []Attr{{Key: "from", Value: NodeDown.String()}}})
		}
	}
}

// Restart boots a down node (KRN-061). It is a no-op unless the node is down.
func (n *Node) Restart() {
	if n.state != NodeDown {
		return
	}
	s := n.sim
	s.call(n, "restart", func() { s.bootProc(n) })
}

// bootProc is the boot procedure (KRN-061).
func (s *Sim) bootProc(n *Node) {
	n.inc++
	n.state = NodeUp
	s.emit(Record{Kind: "kernel.boot", Node: n.id, Inc: n.inc, Text: "boot"})
	hooks := s.bootHooks
	for _, h := range hooks {
		h(n)
	}
	n.boot(n)
}

// Logf emits a kernel.log record for this node with Text fmt.Sprintf(format, args...).
func (n *Node) Logf(format string, args ...any) {
	n.sim.emit(Record{Kind: "kernel.log", Node: n.id, Inc: n.inc, Text: fmt.Sprintf(format, args...)})
}

// Pause stops running the node's events (KRN-060): they are deferred when due and run after
// Resume in their original order. It is a no-op unless the node is up.
func (n *Node) Pause() {
	if n.state != NodeUp {
		return
	}
	n.state = NodePaused
	n.sim.emit(Record{Kind: "kernel.pause", Node: n.id, Inc: n.inc, Text: "pause"})
}

// Resume makes a paused node up again (KRN-063). Its deferred events run at Now(), in the order they
// were deferred: under TieBreakSeeded before the seeded events at Now(), under TieBreakFIFO after the
// events already queued for Now(). It is a no-op unless the node is paused.
func (n *Node) Resume() {
	if n.state != NodePaused {
		return
	}
	s := n.sim
	n.state = NodeUp
	s.emit(Record{Kind: "kernel.resume", Node: n.id, Inc: n.inc, Text: "resume",
		Attrs: []Attr{{Key: "deferred", Value: strconv.Itoa(len(n.deferred))}}})
	for _, e := range n.deferred {
		s.nextSeq++
		e.at, e.tb, e.seq = s.now, 0, s.nextSeq
		s.q.push(e)
	}
	n.deferred = nil
}
