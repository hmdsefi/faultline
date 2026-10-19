package leasekv

import (
	"context"
	"slices"
	"sync"
	"time"
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
	Name       string
	Conn       Conn
	Lock       string
	Stores     []string
	RenewEvery time.Duration

	mu      sync.Mutex
	ctx     context.Context
	epoch   uint64
	until   time.Time // the lease is valid before until
	ready   bool      // the cache holds the store's data for epoch
	holder  string    // the holder the lock named last
	cache   map[string][]string
	waiting map[string]waiter      // per client: the write sent to the store for it
	relay   map[string]waiter      // per client: the request passed to the holder for it
	primary string                 // the replica learned last as the primary
	calls   map[string]*time.Timer // timeouts of unanswered store requests, by client ("" for the Sync)
}

// waiter is where the answer to request seq of a client goes: to the client, or with fwd in a
// FwdReply to the server that passed the request on.
type waiter struct {
	seq uint64
	to  string
	fwd bool
}

// Start starts the server with an empty cache and no lease. It renews the lease and sends store
// requests until ctx is done.
func (s *Server) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ctx, s.epoch, s.until, s.ready, s.holder = ctx, 0, time.Time{}, false, ""
	s.cache, s.waiting, s.relay = map[string][]string{}, map[string]waiter{}, map[string]waiter{}
	s.primary, s.calls = s.Stores[0], map[string]*time.Timer{}
	every(ctx, s.RenewEvery, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.Conn.Send(s.Lock, Acquire{Lease: leaseKV, Sent: time.Now()})
	})
}

// Handle handles one message from the node named from.
func (s *Server) Handle(from string, msg any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch m := msg.(type) {
	case Grant:
		s.granted(m)
	case Busy:
		s.holder = m.Holder
	case SyncOK:
		if m.Epoch == s.epoch && !s.ready {
			s.answered(from, "")
			s.cache, s.ready = m.Data, true
		}
	case Fenced:
		s.fenced(from, m)
	case Written:
		s.written(from, m)
	case NotPrimary:
		s.redirect(from, m)
	case Append, Get:
		s.request(from, from, m, false)
	case Fwd:
		s.request(from, m.Client, m.Req, true)
	case FwdReply:
		if r, ok := s.relay[m.Client]; ok && r.seq == seq(m.Reply) {
			delete(s.relay, m.Client)
			s.Conn.Send(r.to, m.Reply)
		}
	}
}

// serving reports whether the server holds a valid lease and has loaded the store's data.
func (s *Server) serving() bool {
	return s.ready && time.Now().Before(s.until)
}

// granted extends the lease. On a new epoch it empties the cache, and while the cache is not
// loaded it sends the Sync for the epoch.
func (s *Server) granted(m Grant) {
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
	s.holder = s.Name
	if !s.ready {
		s.ask("", Sync{Epoch: s.epoch})
	}
}

// request handles request req of client: from the client itself, or with fwd in a Fwd from the
// server from. A server that does not serve passes a client's own request on to the holder it
// knows and records it as the client's relay entry. It never passes on a request that came in a
// Fwd.
func (s *Server) request(from, client string, req any, fwd bool) {
	if !s.serving() {
		if h := s.hint(); h != "" && !fwd {
			s.relay[client] = waiter{seq: seq(req), to: from}
			s.Conn.Send(h, Fwd{Client: client, Req: req})
			return
		}
		s.answer(waiter{to: from, fwd: fwd}, client, NotLeader{Seq: seq(req), Hint: s.hint()})
		return
	}
	switch m := req.(type) {
	case Get:
		s.answer(waiter{to: from, fwd: fwd}, client, GetOK{Seq: m.Seq, List: slices.Clone(s.cache[m.Key])})
	case Append:
		s.waiting[client] = waiter{seq: m.Seq, to: from, fwd: fwd}
		s.ask(client, Write{Epoch: s.epoch, Client: client, Seq: m.Seq, Key: m.Key, Value: m.Value})
	}
}

// answer sends msg to w.to: in a FwdReply for client when the request came in a Fwd.
func (s *Server) answer(w waiter, client string, msg any) {
	if w.fwd {
		msg = FwdReply{Client: client, Reply: msg}
	}
	s.Conn.Send(w.to, msg)
}

// written updates the cache with the store's list and acknowledges the append it confirms.
func (s *Server) written(from string, m Written) {
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
	s.answered(from, m.Client)
	s.answer(w, m.Client, AppendOK{Seq: m.Seq})
}

// fenced steps down when the store fenced the server's epoch. The client of a fenced write gets
// a NotLeader.
func (s *Server) fenced(from string, m Fenced) {
	if m.Epoch == s.epoch {
		s.ready, s.until = false, time.Time{}
		if m.Client == "" {
			s.answered(from, "")
		}
	}
	if w, ok := s.waiting[m.Client]; ok && w.seq == m.Seq {
		delete(s.waiting, m.Client)
		s.answered(from, m.Client)
		s.answer(w, m.Client, NotLeader{Seq: m.Seq})
	}
}

// ask sends the store request req, of client or the Sync (""), to the replica learned last as
// the primary. Until an answer comes, it sends req again after every storeTimeout: to the other
// replica, unless a NotPrimary named a replica in the meantime.
func (s *Server) ask(key string, req any) {
	if t := s.calls[key]; t != nil {
		t.Stop()
	}
	to := s.primary
	s.Conn.Send(to, req)
	var t *time.Timer
	t = time.AfterFunc(storeTimeout, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.calls[key] != t || s.ctx.Err() != nil {
			return
		}
		if s.primary == to {
			s.primary = s.other(to)
		}
		s.ask(key, req)
	})
	s.calls[key] = t
}

// answered ends the store request of key: replica from answered it as the primary.
func (s *Server) answered(from, key string) {
	s.primary = from
	if t := s.calls[key]; t != nil {
		t.Stop()
	}
	delete(s.calls, key)
}

// redirect notes the primary that a NotPrimary from replica from names, or else the other
// replica, as the replica for the next send of a store request.
func (s *Server) redirect(from string, m NotPrimary) {
	if m.Hint != "" {
		s.primary = m.Hint
	} else if s.primary == from {
		s.primary = s.other(from)
	}
}

// other returns the replica that is not r.
func (s *Server) other(r string) string {
	if r == s.Stores[0] {
		return s.Stores[1]
	}
	return s.Stores[0]
}

// hint names the holder the lock named last, if that is another server. While the lease moves,
// each server can name the other for a short while, and a client that follows the hints goes
// back and forth, Backoff apart.
func (s *Server) hint() string {
	if s.holder == s.Name {
		return ""
	}
	return s.holder
}
