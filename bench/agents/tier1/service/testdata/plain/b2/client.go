package leasekv

import (
	"context"
	"math/rand/v2"
	"strconv"
	"sync"
	"time"
)

// Client runs operations one at a time until Stop, each on a random key of Keys: an append of a
// unique value, or a get. It sends a request again to the other server after Timeout without an
// answer, and after a NotLeader it waits Backoff and tries the hinted server or the other one. It
// waits up to Think between two operations.
type Client struct {
	Name    string
	Conn    Conn
	Servers []string
	Keys    []string
	Appends int       // percent of operations that are appends
	Stop    time.Time // no operation starts at or after Stop
	Timeout time.Duration
	Backoff time.Duration
	Think   time.Duration

	mu      sync.Mutex
	started int
	done    []Op
	replies chan reply
}

// Op is a completed operation.
type Op struct {
	Client string
	Kind   string // "append" or "get"
	Key    string
	Value  string   // the appended value
	List   []string // the list a get returned
	Start  time.Time
	End    time.Time
}

type reply struct {
	from string
	msg  any
}

// Ops returns the number of operations started and the completed ones, in completion order.
func (c *Client) Ops() (started int, done []Op) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.started, append([]Op(nil), c.done...)
}

// Handle queues one message from the server named from for Run. The queue holds 64 messages, and
// Handle drops a message when it is full. With one request in flight, only a few replies wait
// there at a time.
func (c *Client) Handle(from string, msg any) {
	c.mu.Lock()
	replies := c.replies
	c.mu.Unlock()
	select {
	case replies <- reply{from: from, msg: msg}:
	default:
	}
}

// Run runs the operations. It returns after the last one, or when ctx is done.
func (c *Client) Run(ctx context.Context) {
	c.mu.Lock()
	c.started, c.done, c.replies = 0, nil, make(chan reply, 64)
	c.mu.Unlock()
	target := c.Servers[0]
	for seq := uint64(1); time.Now().Before(c.Stop); seq++ {
		op := Op{Client: c.Name, Key: c.Keys[rand.IntN(len(c.Keys))], Start: time.Now()}
		var req any
		if rand.IntN(100) < c.Appends {
			op.Kind, op.Value = "append", c.Name+"-"+strconv.FormatUint(seq, 10)
			req = Append{Seq: seq, Key: op.Key, Value: op.Value}
		} else {
			op.Kind = "get"
			req = Get{Seq: seq, Key: op.Key}
		}
		c.mu.Lock()
		c.started++
		c.mu.Unlock()
		list, ok := c.do(ctx, &target, seq, req)
		if !ok {
			return
		}
		op.List, op.End = list, time.Now()
		c.mu.Lock()
		c.done = append(c.done, op)
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(rand.N(c.Think + 1)):
		}
	}
}

// do sends req until a server answers it and returns the list of a get.
func (c *Client) do(ctx context.Context, target *string, seq uint64, req any) ([]string, bool) {
	c.Conn.Send(*target, req)
	timer := time.NewTimer(c.Timeout)
	defer timer.Stop()
	backoff := false
	for {
		select {
		case <-ctx.Done():
			return nil, false
		case <-timer.C:
			if !backoff {
				*target = c.other(*target)
			}
			backoff = false
			c.Conn.Send(*target, req)
			timer.Reset(c.Timeout)
		case r := <-c.replies:
			switch m := r.msg.(type) {
			case AppendOK:
				if m.Seq == seq {
					return nil, true
				}
			case GetOK:
				if m.Seq == seq {
					return m.List, true
				}
			case NotLeader:
				if m.Seq != seq {
					continue
				}
				*target = c.other(r.from)
				if m.Hint != "" {
					*target = m.Hint
				}
				backoff = true
				timer.Reset(c.Backoff)
			}
		}
	}
}

// other returns the server that is not s.
func (c *Client) other(s string) string {
	if s == c.Servers[0] {
		return c.Servers[1]
	}
	return c.Servers[0]
}
