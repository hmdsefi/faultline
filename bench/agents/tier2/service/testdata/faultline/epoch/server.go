package leasekv

import (
	"slices"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel"
)

// storeTimeout is how long a server waits for a store replica to answer before it sends the
// request to the other replica.
const storeTimeout = 30 * time.Millisecond

// Server serves clients while it holds the "kv" lease. It asks the lock for the lease every
// RenewEvery. A grant is valid until the time the server sent the request plus the TTL. On a new
// epoch the server loads the store's data into its cache before it serves. Appends go to the
// store, and the server acknowledges them when the store confirms them. Gets are answered from
// the cache. A server that does not serve passes a client's request to the holder it knows, in a
// Fwd, and passes the holder's answer back to the client.
//
// Store requests go to the replica the server learned last as the primary. A server sends a
// request again every storeTimeout until it is answered: to the replica that a NotPrimary named,
// or else to the other replica.
type Server struct {
	W          *faultline.World
	Lock       kernel.NodeID
	Stores     []kernel.NodeID
	RenewEvery time.Duration

	epoch   uint64
	until   time.Time // the lease is valid before until
	ready   bool      // the cache holds the store's data for epoch
	holder  string    // the holder the lock named last
	cache   map[string][]string
	waiting map[string]waiter         // per client: the write sent to the store for it
	relay   map[string]waiter         // per client: the request passed to the holder for it
	primary kernel.NodeID             // the replica learned last as the primary
	calls   map[string]kernel.EventID // timeouts of unanswered store requests, by client ("" for the Sync)
}

// waiter is where the answer to request seq of a client goes: to the client, or with fwd in a
// FwdReply to the server that passed the request on.
type waiter struct {
	seq uint64
	to  kernel.NodeID
	fwd bool
}

// Boot starts the server with an empty cache and no lease.
func (s *Server) Boot(n *kernel.Node) {
	s.epoch, s.until, s.ready, s.holder = 0, time.Time{}, false, ""
	s.cache, s.waiting, s.relay = map[string][]string{}, map[string]waiter{}, map[string]waiter{}
	s.primary, s.calls = s.Stores[0], map[string]kernel.EventID{}
	s.W.Net.Handle(n, func(from kernel.NodeID, msg any) { s.handle(n, from, msg) })
	every(n, s.RenewEvery, "renew", func() { s.W.Net.Send(n, s.Lock, Acquire{Lease: leaseKV, Sent: n.Now()}) })
}

func (s *Server) handle(n *kernel.Node, from kernel.NodeID, msg any) {
	switch m := msg.(type) {
	case Grant:
		s.granted(n, m)
	case Busy:
		s.holder = m.Holder
	case SyncOK:
		if m.Epoch == s.epoch && !s.ready {
			s.answered(n, from, "")
			s.cache, s.ready = m.Data, true
		}
	case Fenced:
		s.fenced(n, from, m)
	case Written:
		s.written(n, from, m)
	case NotPrimary:
		s.redirect(n, from, m)
	case Append, Get:
		s.request(n, from, n.Sim().Node(from).Name(), m, false)
	case Fwd:
		s.request(n, from, m.Client, m.Req, true)
	case FwdReply:
		if r, ok := s.relay[m.Client]; ok && r.seq == seq(m.Reply) {
			delete(s.relay, m.Client)
			s.W.Net.Send(n, r.to, m.Reply)
		}
	}
}

// serving reports whether the server holds a valid lease and has loaded the store's data.
func (s *Server) serving(n *kernel.Node) bool {
	return s.ready && n.Now().Before(s.until)
}

// granted extends the lease. On a new epoch it empties the cache, and while the cache is not
// loaded it sends the Sync for the epoch.
func (s *Server) granted(n *kernel.Node, m Grant) {
	if m.Epoch < s.epoch {
		return
	}
	until := m.Sent.Add(m.TTL)
	if m.Epoch > s.epoch {
		s.epoch, s.ready, s.until = m.Epoch, false, until
		s.cache = map[string][]string{}
	}
	if until.After(s.until) {
		s.until = until
	}
	s.holder = n.Name()
	if !s.ready {
		s.ask(n, "", Sync{Epoch: s.epoch})
	}
}

// request handles request req of client: from the client itself, or with fwd in a Fwd from the
// server from. A server that does not serve passes a client's own request on to the holder it
// knows and records it as the client's relay entry. It never passes on a request that came in a
// Fwd.
func (s *Server) request(n *kernel.Node, from kernel.NodeID, client string, req any, fwd bool) {
	if !s.serving(n) {
		if h := s.hint(n); h != "" && !fwd {
			s.relay[client] = waiter{seq: seq(req), to: from}
			s.W.Net.Send(n, n.Sim().Lookup(h).ID(), Fwd{Client: client, Req: req})
			return
		}
		s.answer(n, waiter{to: from, fwd: fwd}, client, NotLeader{Seq: seq(req), Hint: s.hint(n)})
		return
	}
	switch m := req.(type) {
	case Get:
		s.answer(n, waiter{to: from, fwd: fwd}, client, GetOK{Seq: m.Seq, List: slices.Clone(s.cache[m.Key])})
	case Append:
		s.waiting[client] = waiter{seq: m.Seq, to: from, fwd: fwd}
		s.ask(n, client, Write{Epoch: s.epoch, Client: client, Seq: m.Seq, Key: m.Key, Value: m.Value})
	}
}

// answer sends msg to w.to: in a FwdReply for client when the request came in a Fwd.
func (s *Server) answer(n *kernel.Node, w waiter, client string, msg any) {
	if w.fwd {
		msg = FwdReply{Client: client, Reply: msg}
	}
	s.W.Net.Send(n, w.to, msg)
}

// written updates the cache with the store's list and acknowledges the append it confirms.
func (s *Server) written(n *kernel.Node, from kernel.NodeID, m Written) {
	// Keep the longer list: answers can arrive out of order, and a key's list in the store only
	// grows, so the shorter list is the older one.
	if len(m.List) > len(s.cache[m.Key]) {
		s.cache[m.Key] = m.List
	}
	w, ok := s.waiting[m.Client]
	if !ok || w.seq != m.Seq {
		return
	}
	delete(s.waiting, m.Client)
	s.answered(n, from, m.Client)
	s.answer(n, w, m.Client, AppendOK{Seq: m.Seq})
}

// fenced steps down when the store fenced the server's epoch. The client of a fenced write gets
// a NotLeader.
func (s *Server) fenced(n *kernel.Node, from kernel.NodeID, m Fenced) {
	if m.Epoch == s.epoch {
		s.ready, s.until = false, time.Time{}
		if m.Client == "" {
			s.answered(n, from, "")
		}
	}
	if w, ok := s.waiting[m.Client]; ok && w.seq == m.Seq {
		delete(s.waiting, m.Client)
		s.answered(n, from, m.Client)
		s.answer(n, w, m.Client, NotLeader{Seq: m.Seq})
	}
}

// ask sends the store request req, of client or the Sync (""), to the replica learned last as
// the primary. Until an answer comes, it sends req again after every storeTimeout: to the other
// replica, unless a NotPrimary named a replica in the meantime.
func (s *Server) ask(n *kernel.Node, key string, req any) {
	n.Cancel(s.calls[key])
	to := s.primary
	s.W.Net.Send(n, to, req)
	s.calls[key] = n.After(storeTimeout, "store timeout", func() {
		if s.primary == to {
			s.primary = s.other(to)
		}
		s.ask(n, key, req)
	})
}

// answered ends the store request of key: replica from answered it as the primary.
func (s *Server) answered(n *kernel.Node, from kernel.NodeID, key string) {
	s.primary = from
	n.Cancel(s.calls[key])
	delete(s.calls, key)
}

// redirect notes the primary that a NotPrimary from replica from names, or else the other
// replica, as the replica for the next send of a store request.
func (s *Server) redirect(n *kernel.Node, from kernel.NodeID, m NotPrimary) {
	if h := n.Sim().Lookup(m.Hint); h != nil {
		s.primary = h.ID()
	} else if s.primary == from {
		s.primary = s.other(from)
	}
}

// other returns the replica that is not r.
func (s *Server) other(r kernel.NodeID) kernel.NodeID {
	if r == s.Stores[0] {
		return s.Stores[1]
	}
	return s.Stores[0]
}

// hint names the holder the lock named last, if that is another server. While the lease moves,
// each server can name the other for a short while, and a client that follows the hints goes
// back and forth, Backoff apart.
func (s *Server) hint(n *kernel.Node) string {
	if s.holder == n.Name() {
		return ""
	}
	return s.holder
}
