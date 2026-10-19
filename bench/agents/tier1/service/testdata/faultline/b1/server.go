package leasekv

import (
	"slices"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel"
)

// Server serves clients while it holds the lease. It asks the lock for the lease every
// RenewEvery. A grant is valid until the time the server sent the request plus the TTL. On a new
// epoch the server loads the store's data into its cache before it serves. Appends go to the
// store; the server adds the value to its cache and acknowledges the append as it sends it. Gets
// are answered from the cache.
type Server struct {
	W          *faultline.World
	Lock       kernel.NodeID
	Store      kernel.NodeID
	RenewEvery time.Duration

	epoch  uint64
	until  time.Time // the lease is valid before until
	ready  bool      // the cache holds the store's data for epoch
	holder string    // the holder the lock named last
	cache  map[string][]string
}

// Boot starts the server with an empty cache and no lease.
func (s *Server) Boot(n *kernel.Node) {
	s.epoch, s.until, s.ready, s.holder = 0, time.Time{}, false, ""
	s.cache = map[string][]string{}
	s.W.Net.Handle(n, func(from kernel.NodeID, msg any) { s.handle(n, from, msg) })
	var renew func()
	renew = func() {
		s.W.Net.Send(n, s.Lock, Acquire{Sent: n.Now()})
		if s.epoch > 0 && !s.ready && n.Now().Before(s.until) {
			s.W.Net.Send(n, s.Store, Sync{Epoch: s.epoch})
		}
		n.After(s.RenewEvery, "renew", renew)
	}
	n.After(kernel.Uniform(n.Rand(), 0, s.RenewEvery), "renew", renew)
}

func (s *Server) handle(n *kernel.Node, from kernel.NodeID, msg any) {
	switch m := msg.(type) {
	case Grant:
		s.granted(n, m)
	case Busy:
		s.holder = m.Holder
	case SyncOK:
		if m.Epoch == s.epoch && !s.ready {
			s.cache, s.ready = m.Data, true
		}
	case Fenced:
		s.fenced(m)
	case Written:
		s.written(m)
	case Append:
		s.append(n, from, m)
	case Get:
		if !s.serving(n) {
			s.W.Net.Send(n, from, NotLeader{Seq: m.Seq, Hint: s.hint(n)})
			return
		}
		s.W.Net.Send(n, from, GetOK{Seq: m.Seq, List: slices.Clone(s.cache[m.Key])})
	}
}

// serving reports whether the server holds a valid lease and has loaded the store's data.
func (s *Server) serving(n *kernel.Node) bool {
	return s.ready && n.Now().Before(s.until)
}

func (s *Server) granted(n *kernel.Node, m Grant) {
	if m.Epoch < s.epoch {
		return
	}
	until := m.Sent.Add(m.TTL)
	if m.Epoch > s.epoch {
		s.epoch, s.ready, s.until = m.Epoch, false, until
		s.cache = map[string][]string{}
		s.W.Net.Send(n, s.Store, Sync{Epoch: m.Epoch})
	}
	if until.After(s.until) {
		s.until = until
	}
	s.holder = n.Name()
}

// fenced steps down: a newer epoch exists.
func (s *Server) fenced(m Fenced) {
	if m.Epoch == s.epoch {
		s.ready, s.until = false, time.Time{}
	}
}

func (s *Server) append(n *kernel.Node, from kernel.NodeID, m Append) {
	if !s.serving(n) {
		s.W.Net.Send(n, from, NotLeader{Seq: m.Seq, Hint: s.hint(n)})
		return
	}
	client := n.Sim().Node(from).Name()
	s.W.Net.Send(n, s.Store, Write{Epoch: s.epoch, Client: client, Seq: m.Seq, Key: m.Key, Value: m.Value})
	s.cache[m.Key] = append(s.cache[m.Key], m.Value)
	s.W.Net.Send(n, from, AppendOK{Seq: m.Seq})
}

// written updates the cache with the store's list.
func (s *Server) written(m Written) {
	if len(m.List) > len(s.cache[m.Key]) {
		s.cache[m.Key] = m.List
	}
}

// hint names the holder the lock named last, if that is another server.
func (s *Server) hint(n *kernel.Node) string {
	if s.holder == n.Name() {
		return ""
	}
	return s.holder
}
