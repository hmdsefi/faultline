package leasekv

import (
	"strconv"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/check/history"
	"github.com/hmdsefi/faultline/kernel"
)

// Client runs operations one at a time until Stop, each on a random key of Keys: an append of a
// unique value, or a get. It sends a request again to the other server after Timeout without an
// answer, and after a NotLeader it waits Backoff and tries the hinted server or the other one. It
// waits up to Think between two operations.
type Client struct {
	W       *faultline.World
	Servers []kernel.NodeID
	Keys    []string
	Appends int       // percent of operations that are appends
	Stop    time.Time // no operation starts at or after Stop
	Timeout time.Duration
	Backoff time.Duration
	Think   time.Duration

	Started int  // operations started
	Done    []Op // completed operations, in completion order
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

// Boot starts the client's operations.
func (c *Client) Boot(n *kernel.Node) {
	c.Started, c.Done = 0, nil
	r := n.Rand()
	var (
		seq    uint64
		op     Op
		req    any
		id     int64
		active bool
		target = c.Servers[0]
		timer  kernel.EventID
	)
	var send func()
	send = func() {
		c.W.Net.Send(n, target, req)
		timer = n.After(c.Timeout, "timeout", func() {
			target = c.other(target)
			send()
		})
	}
	next := func() {
		if !n.Now().Before(c.Stop) {
			return
		}
		c.Started++
		seq++
		op = Op{Client: n.Name(), Key: c.Keys[r.IntN(len(c.Keys))], Start: n.Now()}
		if r.IntN(100) < c.Appends {
			op.Kind, op.Value = "append", n.Name()+"-"+strconv.FormatUint(seq, 10)
			req = Append{Seq: seq, Key: op.Key, Value: op.Value}
			id = c.W.History.Invoke(n.Name(), op.Kind, map[string]string{"key": op.Key, "value": op.Value})
		} else {
			op.Kind = "get"
			req = Get{Seq: seq, Key: op.Key}
			id = c.W.History.Invoke(n.Name(), op.Kind, map[string]string{"key": op.Key})
		}
		active = true
		send()
	}
	complete := func(list []string) {
		n.Cancel(timer)
		active = false
		op.List, op.End = list, n.Now()
		c.Done = append(c.Done, op)
		c.W.History.Complete(id, history.OK, list)
		n.After(kernel.Uniform(r, 0, c.Think), "next", next)
	}
	c.W.Net.Handle(n, func(from kernel.NodeID, msg any) {
		switch m := msg.(type) {
		case AppendOK:
			if active && m.Seq == seq {
				complete(nil)
			}
		case GetOK:
			if active && m.Seq == seq {
				complete(m.List)
			}
		case NotLeader:
			if !active || m.Seq != seq {
				return
			}
			n.Cancel(timer)
			target = c.other(from)
			if m.Hint != "" {
				if h := n.Sim().Lookup(m.Hint); h != nil {
					target = h.ID()
				}
			}
			timer = n.After(c.Backoff, "backoff", send)
		}
	})
	n.Post("next", next)
}

// other returns the server that is not s.
func (c *Client) other(s kernel.NodeID) kernel.NodeID {
	if s == c.Servers[0] {
		return c.Servers[1]
	}
	return c.Servers[0]
}
