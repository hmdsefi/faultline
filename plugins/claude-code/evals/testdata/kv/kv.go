// Package kv is a key-value server with a write-ahead log, and a client that writes to it.
package kv

import (
	"bytes"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/check/history"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simdisk"
)

// Put asks the server to store Value under Key.
type Put struct{ Key, Value string }

// Ack tells the client that Key is stored.
type Ack struct{ Key string }

// Describe returns the text the trace shows for a Put.
func (p Put) Describe() string { return "put " + p.Key + "=" + p.Value }

// Describe returns the text the trace shows for an Ack.
func (a Ack) Describe() string { return "ack " + a.Key }

// Server stores puts in memory and appends them to /wal.
type Server struct {
	W    *faultline.World
	Data map[string]string // rebuilt from /wal at every boot
}

// Boot runs at every boot of the node: its first start and every restart.
func (s *Server) Boot(n *kernel.Node) {
	vol := s.W.Disk.Volume(n) // the same volume in every incarnation
	s.Data = load(vol)        // memory starts empty: rebuild it from the disk
	wal, err := vol.Open("/wal")
	if err != nil {
		n.Sim().Fail(err)
		return
	}
	s.W.Net.Handle(n, func(from kernel.NodeID, msg any) {
		p := msg.(Put)
		if _, err := wal.Append([]byte(p.Key + "=" + p.Value + "\n")); err != nil {
			return // no ack: the client retries
		}
		s.Data[p.Key] = p.Value
		s.W.Net.Send(n, from, Ack{Key: p.Key})
	})
	var flush func()
	flush = func() {
		_ = wal.Sync()
		n.After(5*time.Millisecond, "flush", flush)
	}
	n.After(5*time.Millisecond, "flush", flush)
}

// load reads the records of /wal and cuts a torn last record off the file.
func load(vol *simdisk.Volume) map[string]string {
	data := map[string]string{}
	log, err := vol.ReadFile("/wal")
	if err != nil {
		return data
	}
	end := bytes.LastIndexByte(log, '\n') + 1
	for _, line := range bytes.Split(log[:end], []byte("\n")) {
		if k, v, ok := bytes.Cut(line, []byte("=")); ok {
			data[string(k)] = string(v)
		}
	}
	if end < len(log) {
		if f, err := vol.Open("/wal"); err == nil {
			_ = f.Truncate(int64(end))
			_ = f.Sync()
			_ = f.Close()
		}
	}
	return data
}

// Client writes keys k1 to kN one at a time, retries a put every 50 ms until it is
// acknowledged, and records each put in the world's history.
type Client struct {
	W      *faultline.World
	Server kernel.NodeID
	Puts   int
	Acked  map[string]string
}

// Boot runs at every boot of the client node.
func (c *Client) Boot(n *kernel.Node) {
	c.Acked = map[string]string{}
	i := 0
	var cur Put
	var op int64
	var retry kernel.EventID
	var send func()
	send = func() {
		c.W.Net.Send(n, c.Server, cur)
		retry = n.After(50*time.Millisecond, "retry", send)
	}
	next := func() {
		if i++; i > c.Puts {
			return
		}
		cur = Put{Key: "k" + strconv.Itoa(i), Value: "v" + strconv.Itoa(i)}
		op = c.W.History.Invoke(n.Name(), "put", cur.Key+"="+cur.Value)
		send()
	}
	c.W.Net.Handle(n, func(_ kernel.NodeID, msg any) {
		if a := msg.(Ack); a.Key != cur.Key || i > c.Puts || c.Acked[a.Key] != "" {
			return
		}
		n.Cancel(retry)
		c.Acked[cur.Key] = cur.Value
		c.W.History.Complete(op, history.OK, nil)
		n.After(kernel.Uniform(n.Rand(), 0, 40*time.Millisecond), "next", next)
	})
	n.Post("next", next)
}

// Lost returns an error for the first acknowledged put, in key order, that the server
// does not hold while it is up.
func (c *Client) Lost(s *Server, server *kernel.Node) error {
	if server.State() != kernel.NodeUp {
		return nil
	}
	for _, k := range slices.Sorted(maps.Keys(c.Acked)) {
		if got := s.Data[k]; got != c.Acked[k] {
			return fmt.Errorf("%s=%s was acknowledged, but n1 holds %q", k, c.Acked[k], got)
		}
	}
	return nil
}

// Done returns an error unless every put was acknowledged.
func (c *Client) Done() error {
	if len(c.Acked) != c.Puts {
		return fmt.Errorf("%d of %d puts acknowledged", len(c.Acked), c.Puts)
	}
	return nil
}
