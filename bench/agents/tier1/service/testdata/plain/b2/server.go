package leasekv

import (
	"context"
	"math/rand/v2"
	"slices"
	"sync"
	"time"
)

// Server serves clients while it holds the lease. It asks the lock for the lease every
// RenewEvery. A grant is valid until the time the server sent the request plus the TTL. On a new
// epoch the server loads the store's data into its cache before it serves. Appends go to the
// store, and the server acknowledges them when the store confirms them. It remembers the last
// confirmed append of every client, so an append sent again is not written twice. Gets are
// answered from the cache.
type Server struct {
	Name       string
	Conn       Conn
	Lock       string
	Store      string
	RenewEvery time.Duration

	mu      sync.Mutex
	epoch   uint64
	until   time.Time // the lease is valid before until
	ready   bool      // the cache holds the store's data for epoch
	holder  string    // the holder the lock named last
	cache   map[string][]string
	waiting map[string]uint64 // per client: Seq of the write sent to the store for it
	applied map[string]uint64 // per client: Seq of its last confirmed append
}

// Start starts the server with an empty cache and no lease. It renews the lease until ctx is
// done.
func (s *Server) Start(ctx context.Context) {
	s.mu.Lock()
	s.epoch, s.until, s.ready, s.holder = 0, time.Time{}, false, ""
	s.cache, s.waiting, s.applied = map[string][]string{}, map[string]uint64{}, map[string]uint64{}
	s.mu.Unlock()
	go func() {
		t := time.NewTimer(rand.N(s.RenewEvery + 1))
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			s.renew()
			t.Reset(s.RenewEvery)
		}
	}()
}

func (s *Server) renew() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Conn.Send(s.Lock, Acquire{Sent: time.Now()})
	if s.epoch > 0 && !s.ready && time.Now().Before(s.until) {
		s.Conn.Send(s.Store, Sync{Epoch: s.epoch})
	}
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
			s.cache, s.ready = m.Data, true
		}
	case Fenced:
		s.fenced(m)
	case Written:
		s.written(m)
	case Append:
		s.append(from, m)
	case Get:
		if !s.serving() {
			s.Conn.Send(from, NotLeader{Seq: m.Seq, Hint: s.hint()})
			return
		}
		s.Conn.Send(from, GetOK{Seq: m.Seq, List: slices.Clone(s.cache[m.Key])})
	}
}

// serving reports whether the server holds a valid lease and has loaded the store's data.
func (s *Server) serving() bool {
	return s.ready && time.Now().Before(s.until)
}

func (s *Server) granted(m Grant) {
	if m.Epoch < s.epoch {
		return
	}
	until := m.Sent.Add(m.TTL)
	if m.Epoch > s.epoch {
		s.epoch, s.ready, s.until = m.Epoch, false, until
		s.cache = map[string][]string{}
		s.Conn.Send(s.Store, Sync{Epoch: m.Epoch})
	}
	if until.After(s.until) {
		s.until = until
	}
	s.holder = s.Name
}

// fenced steps down: a newer epoch exists.
func (s *Server) fenced(m Fenced) {
	if m.Epoch == s.epoch {
		s.ready, s.until = false, time.Time{}
	}
	if seq, ok := s.waiting[m.Client]; ok && seq == m.Seq {
		delete(s.waiting, m.Client)
		s.Conn.Send(m.Client, NotLeader{Seq: m.Seq})
	}
}

func (s *Server) append(from string, m Append) {
	if !s.serving() {
		s.Conn.Send(from, NotLeader{Seq: m.Seq, Hint: s.hint()})
		return
	}
	if m.Seq <= s.applied[from] {
		s.Conn.Send(from, AppendOK{Seq: m.Seq})
		return
	}
	s.waiting[from] = m.Seq
	s.Conn.Send(s.Store, Write{Epoch: s.epoch, Client: from, Seq: m.Seq, Key: m.Key, Value: m.Value})
}

// written updates the cache with the store's list and acknowledges the append it confirms.
func (s *Server) written(m Written) {
	if len(m.List) > len(s.cache[m.Key]) {
		s.cache[m.Key] = m.List
	}
	s.applied[m.Client] = max(s.applied[m.Client], m.Seq)
	if seq, ok := s.waiting[m.Client]; !ok || seq != m.Seq {
		return
	}
	delete(s.waiting, m.Client)
	s.Conn.Send(m.Client, AppendOK{Seq: m.Seq})
}

// hint names the holder the lock named last, if that is another server.
func (s *Server) hint() string {
	if s.holder == s.Name {
		return ""
	}
	return s.holder
}
