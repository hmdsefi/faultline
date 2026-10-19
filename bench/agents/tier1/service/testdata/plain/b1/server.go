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
// store; the server adds the value to its cache and acknowledges the append as it sends it. Gets
// are answered from the cache.
type Server struct {
	Name       string
	Conn       Conn
	Lock       string
	Store      string
	RenewEvery time.Duration

	mu     sync.Mutex
	epoch  uint64
	until  time.Time // the lease is valid before until
	ready  bool      // the cache holds the store's data for epoch
	holder string    // the holder the lock named last
	cache  map[string][]string
}

// Start starts the server with an empty cache and no lease. It renews the lease until ctx is
// done.
func (s *Server) Start(ctx context.Context) {
	s.mu.Lock()
	s.epoch, s.until, s.ready, s.holder = 0, time.Time{}, false, ""
	s.cache = map[string][]string{}
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
}

func (s *Server) append(from string, m Append) {
	if !s.serving() {
		s.Conn.Send(from, NotLeader{Seq: m.Seq, Hint: s.hint()})
		return
	}
	s.Conn.Send(s.Store, Write{Epoch: s.epoch, Client: from, Seq: m.Seq, Key: m.Key, Value: m.Value})
	s.cache[m.Key] = append(s.cache[m.Key], m.Value)
	s.Conn.Send(from, AppendOK{Seq: m.Seq})
}

// written updates the cache with the store's list.
func (s *Server) written(m Written) {
	if len(m.List) > len(s.cache[m.Key]) {
		s.cache[m.Key] = m.List
	}
}

// hint names the holder the lock named last, if that is another server.
func (s *Server) hint() string {
	if s.holder == s.Name {
		return ""
	}
	return s.holder
}
