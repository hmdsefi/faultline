package toys

import (
	"fmt"
	"strconv"
	"time"

	"github.com/hmdsefi/faultline/kernel"
)

// RegisterConfig configures Register.
type RegisterConfig struct {
	Ops      int  // >= 0, client operations
	EarlyAck bool // bug switch: the primary acknowledges writes before replicating
}

// Messages of the register toy.
type (
	Write        struct{ Op, V int }
	WriteAck     struct{ Op int }
	Read         struct{ Op int }
	ReadResp     struct{ Op, V int }
	Replicate    struct{ Op, V int }
	ReplicateAck struct{ Op int }
)

// Describe returns "write op=<Op> v=<V>".
func (m Write) Describe() string { return "write op=" + strconv.Itoa(m.Op) + " v=" + strconv.Itoa(m.V) }

// Describe returns "write-ack op=<Op>".
func (m WriteAck) Describe() string { return "write-ack op=" + strconv.Itoa(m.Op) }

// Describe returns "read op=<Op>".
func (m Read) Describe() string { return "read op=" + strconv.Itoa(m.Op) }

// Describe returns "read-resp op=<Op> v=<V>".
func (m ReadResp) Describe() string {
	return "read-resp op=" + strconv.Itoa(m.Op) + " v=" + strconv.Itoa(m.V)
}

// Describe returns "replicate op=<Op> v=<V>".
func (m Replicate) Describe() string {
	return "replicate op=" + strconv.Itoa(m.Op) + " v=" + strconv.Itoa(m.V)
}

// Describe returns "replicate-ack op=<Op>".
func (m ReplicateAck) Describe() string { return "replicate-ack op=" + strconv.Itoa(m.Op) }

// Register returns the nodes [p, r1, r2, c] of a single-register store: a primary that
// replicates writes to two replicas and a client that alternates writes and reads (DET-043).
// With EarlyAck a read served by a replica can miss the last acknowledged write, and the client
// fails the Sim with "stale read: …". Without it no read is stale while every server stays up,
// even when messages arrive twice or the client restarts: the client only takes the answer it
// waits for.
func Register(s *kernel.Sim, t Transport, cfg RegisterConfig) []*kernel.Node {
	if cfg.Ops < 0 {
		panic("toys: invalid RegisterConfig")
	}
	var p, r1, r2, c *kernel.Node
	p = s.AddNode("p", func(n *kernel.Node) {
		value := 0
		acks := map[int]int{} // lookups only; bit 1 is r1, bit 2 is r2
		t.Handle(n, func(from kernel.NodeID, msg any) {
			switch m := msg.(type) {
			case Write:
				value = max(value, m.V)
				t.Send(n, r1.ID(), Replicate{Op: m.Op, V: m.V})
				t.Send(n, r2.ID(), Replicate{Op: m.Op, V: m.V})
				if cfg.EarlyAck {
					t.Send(n, c.ID(), WriteAck{Op: m.Op})
				}
			case ReplicateAck:
				bit := 1
				if from == r2.ID() {
					bit = 2
				}
				if was := acks[m.Op]; was&bit == 0 {
					acks[m.Op] = was | bit
					if was|bit == 3 && !cfg.EarlyAck {
						t.Send(n, c.ID(), WriteAck{Op: m.Op})
					}
				}
			case Read:
				t.Send(n, from, ReadResp{Op: m.Op, V: value})
			}
		})
	}, kernel.WithTags("server"))
	replica := func(n *kernel.Node) {
		value := 0
		t.Handle(n, func(from kernel.NodeID, msg any) {
			switch m := msg.(type) {
			case Replicate:
				if m.V > value {
					value = m.V
				}
				t.Send(n, from, ReplicateAck{Op: m.Op})
			case Read:
				t.Send(n, from, ReadResp{Op: m.Op, V: value})
			}
		})
	}
	r1 = s.AddNode("r1", replica, kernel.WithTags("server"))
	r2 = s.AddNode("r2", replica, kernel.WithTags("server"))
	c = s.AddNode("c", func(n *kernel.Node) {
		op, lastAcked, waiting := 0, 0, false
		targets := []*kernel.Node{p, r1, r2}
		var next func()
		think := func() {
			n.After(kernel.Uniform(n.Rand(), 0, 2*time.Millisecond), "next", next)
		}
		next = func() {
			if op == cfg.Ops {
				n.Logf("done")
				return
			}
			op++
			waiting = true
			if op%2 == 1 {
				t.Send(n, p.ID(), Write{Op: op, V: op})
			} else {
				t.Send(n, targets[n.Rand().Uint64N(3)].ID(), Read{Op: op})
			}
		}
		t.Handle(n, func(_ kernel.NodeID, msg any) {
			switch m := msg.(type) {
			case WriteAck:
				if !waiting || m.Op != op {
					return
				}
				lastAcked = m.Op
			case ReadResp:
				if !waiting || m.Op != op {
					return
				}
				if m.V < lastAcked {
					s.Fail(fmt.Errorf("stale read: op %d got %d, last acked write %d", m.Op, m.V, lastAcked))
					return
				}
			default:
				return
			}
			waiting = false
			think()
		})
		think()
	}, kernel.WithTags("client"))
	return []*kernel.Node{p, r1, r2, c}
}
