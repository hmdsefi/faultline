package leasekv

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"slices"
	"sync"
	"time"
)

// heartbeat is how often the primary sends the backup its log and commit index.
const heartbeat = 80 * time.Millisecond

// Disk is a replica's log file. Bytes written with Append are durable only after Sync returns
// nil. A Disk belongs to one start of the replica.
type Disk interface {
	ReadAll() ([]byte, error)
	Append(p []byte) error
	Sync() error
}

// Store is one of the two store replicas, named Name. The replica that holds the "store" lease
// is the primary, and Peer is the other replica, the backup. The replica asks the lock for the
// lease every RenewEvery; a grant is valid until the time it sent the request plus the TTL.
//
// The primary appends entries to its log for Syncs and Writes and sends them to the backup. An
// entry is committed once the backup has acknowledged it. Committed entries are applied in order,
// on both replicas: a write appends its value to the data and records the client's Seq, so a write
// sent again is applied once, and a Sync raises the fence, below which writes are rejected. A
// replica appends to its write-ahead log and syncs it before it answers or acknowledges anything.
type Store struct {
	Name       string
	Conn       Conn
	Disk       Disk
	Lock, Peer string
	RenewEvery time.Duration

	mu      sync.Mutex
	epoch   uint64    // the newest store epoch the replica knows
	primary bool      // the replica holds the lease for epoch
	until   time.Time // the lease is valid before until
	holder  string    // the primary the replica knows

	log     []Entry
	commit  uint64            // the entries up to commit are committed and applied
	next    uint64            // primary: the next entry to send to the backup
	match   uint64            // primary: the backup's log matches up to match
	pending map[uint64]string // primary: per entry, the server to answer when it commits

	data  map[string][]string
	last  map[string]uint64 // per client: Seq of its last applied write
	fence uint64            // the highest kv epoch of an applied Sync
}

// record is one line of the write-ahead log: entry Index of the log, or the commit index when
// Index is 0. An entry replaces the entries from Index on.
type record struct {
	Index  uint64 `json:"index,omitempty"`
	Entry  Entry  `json:"entry,omitzero"`
	Commit uint64 `json:"commit,omitempty"`
}

// Start rebuilds the replica from its log. It starts as a backup, and asks for the lease and
// sends heartbeats until ctx is done. Call it before the first Handle.
func (s *Store) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epoch, s.primary, s.until, s.holder = 0, false, time.Time{}, ""
	s.log, s.commit, s.next, s.match = nil, 0, 0, 0
	s.pending = map[uint64]string{}
	s.data, s.last, s.fence = map[string][]string{}, map[string]uint64{}, 0
	if err := s.recover(); err != nil {
		return err
	}
	every(ctx, s.RenewEvery, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.Conn.Send(s.Lock, Acquire{Lease: leaseStore, Sent: time.Now()})
	})
	every(ctx, heartbeat, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.leading() {
			s.replicate()
		}
	})
	return nil
}

// List returns a copy of the list of key in the replica's applied data.
func (s *Store) List(key string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.data[key])
}

// Handle handles one message from the node named from.
func (s *Store) Handle(from string, msg any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch m := msg.(type) {
	case Grant:
		s.granted(m)
	case Busy:
		s.holder = m.Holder
	case Sync:
		s.request(from, Entry{KV: m.Epoch})
	case Write:
		s.request(from, Entry{KV: m.Epoch, Client: m.Client, Seq: m.Seq, Key: m.Key, Value: m.Value})
	case Replicate:
		s.replicated(from, m)
	case Appended:
		if m.Epoch != s.epoch || !s.primary {
			return
		}
		s.match, s.next = max(s.match, m.Index), max(s.next, m.Index+1)
		if s.match > s.commit && s.persist(record{Commit: s.match}) == nil {
			s.advance(s.match)
		}
	case Mismatch:
		if m.Epoch == s.epoch && s.primary {
			s.next = max(s.match+1, min(m.Len+1, m.Prev))
			s.replicate()
		}
	case Stale:
		if m.Epoch > s.epoch {
			s.follow(m.Epoch, from)
		}
	}
}

// leading reports whether the replica is the primary and its lease is valid.
func (s *Store) leading() bool {
	return s.primary && time.Now().Before(s.until)
}

// hint names the primary the replica knows, if that is the other replica.
func (s *Store) hint() string {
	if s.holder == s.Name {
		return ""
	}
	return s.holder
}

func (s *Store) granted(m Grant) {
	if m.Epoch < s.epoch {
		return
	}
	if m.Epoch > s.epoch || !s.primary {
		s.follow(m.Epoch, s.Name)
		s.primary, s.next, s.match = true, uint64(len(s.log))+1, 0
	}
	if until := m.Sent.Add(m.TTL); until.After(s.until) {
		s.until = until
	}
}

// follow makes the replica a backup in epoch, whose primary is holder.
func (s *Store) follow(epoch uint64, holder string) {
	s.epoch, s.primary, s.until, s.holder = epoch, false, time.Time{}, holder
	clear(s.pending)
}

// request appends entry e, if the replica is the primary, and sends it to the backup.
func (s *Store) request(from string, e Entry) {
	if !s.leading() {
		s.Conn.Send(from, NotPrimary{Hint: s.hint()})
		return
	}
	e.Epoch = s.epoch
	// A request sent again before its entry is committed gets no second entry.
	for i := s.commit; i < uint64(len(s.log)); i++ {
		if s.log[i] == e {
			s.pending[i+1] = from
			s.replicate()
			return
		}
	}
	if err := s.persist(record{Index: uint64(len(s.log)) + 1, Entry: e}); err != nil {
		return
	}
	s.log = append(s.log, e)
	s.pending[uint64(len(s.log))] = from
	s.replicate()
}

// replicate sends the backup the log from entry next on, and the commit index.
func (s *Store) replicate() {
	prev := s.next - 1
	m := Replicate{Epoch: s.epoch, Prev: prev, Entries: slices.Clone(s.log[prev:]), Commit: s.commit}
	if prev > 0 {
		m.PrevEpoch = s.log[prev-1].Epoch
	}
	s.Conn.Send(s.Peer, m)
}

// replicated handles a Replicate as the backup. It rejects an older store epoch, and answers a
// Replicate whose entry Prev it does not hold with the length of its log. Otherwise it appends
// the entries it lacks; where its own entry has another epoch than the primary's, it drops that
// entry and the ones after it first. It syncs the entries and the commit index it learned,
// applies the entries up to that index, and acknowledges.
func (s *Store) replicated(from string, m Replicate) {
	if m.Epoch < s.epoch {
		s.Conn.Send(from, Stale{Epoch: s.epoch})
		return
	}
	if m.Epoch > s.epoch || s.primary {
		s.follow(m.Epoch, "")
	}
	s.holder = from
	if size := uint64(len(s.log)); m.Prev > size || m.Prev > 0 && s.log[m.Prev-1].Epoch != m.PrevEpoch {
		s.Conn.Send(from, Mismatch{Epoch: m.Epoch, Prev: m.Prev, Len: size})
		return
	}
	var recs []record
	for k, e := range m.Entries {
		i := m.Prev + uint64(k) + 1
		if i <= uint64(len(s.log)) {
			if s.log[i-1].Epoch == e.Epoch {
				continue
			}
			s.log = s.log[:i-1]
		}
		s.log = append(s.log, e)
		recs = append(recs, record{Index: i, Entry: e})
	}
	last := m.Prev + uint64(len(m.Entries))
	commit := min(m.Commit, last)
	if commit > s.commit {
		recs = append(recs, record{Commit: commit})
	}
	if err := s.persist(recs...); err != nil {
		return
	}
	s.advance(commit)
	s.Conn.Send(from, Appended{Epoch: m.Epoch, Index: last})
}

// advance applies the entries up to index c in order, and answers the requests the replica
// appended them for.
func (s *Store) advance(c uint64) {
	for s.commit < c && s.commit < uint64(len(s.log)) {
		s.commit++
		e := s.log[s.commit-1]
		fenced := e.KV < s.fence
		switch {
		case fenced:
		case e.Key == "":
			s.fence = e.KV
		case e.Seq > s.last[e.Client]:
			s.data[e.Key] = append(s.data[e.Key], e.Value)
			s.last[e.Client] = e.Seq
		}
		to, ok := s.pending[s.commit]
		if !ok {
			continue
		}
		delete(s.pending, s.commit)
		switch {
		case fenced:
			s.Conn.Send(to, Fenced{Epoch: e.KV, Client: e.Client, Seq: e.Seq})
		case e.Key == "":
			s.Conn.Send(to, SyncOK{Epoch: e.KV, Data: clone(s.data)})
		default:
			// A write sent again is answered with the key's current list, which holds the value.
			s.Conn.Send(to, Written{Client: e.Client, Seq: e.Seq, Key: e.Key, List: slices.Clone(s.data[e.Key])})
		}
	}
}

// persist appends the records to the write-ahead log and syncs it.
func (s *Store) persist(recs ...record) error {
	if len(recs) == 0 {
		return nil
	}
	var b []byte
	for _, r := range recs {
		line, err := json.Marshal(r)
		if err != nil {
			return err
		}
		b = append(append(b, line...), '\n')
	}
	if err := s.Disk.Append(b); err != nil {
		return err
	}
	return s.Disk.Sync()
}

// recover replays the write-ahead log, the entries and the last commit index it holds, and
// applies the entries up to that index.
func (s *Store) recover() error {
	b, err := s.Disk.ReadAll()
	if err != nil {
		return err
	}
	var commit uint64
	for line := range bytes.Lines(b) {
		var r record
		if err := json.Unmarshal(line, &r); err != nil {
			return err
		}
		if r.Index == 0 {
			commit = r.Commit
			continue
		}
		s.log = append(s.log[:r.Index-1], r.Entry)
		s.epoch = max(s.epoch, r.Entry.Epoch)
	}
	s.advance(commit)
	return nil
}

// clone returns a deep copy of data.
func clone(data map[string][]string) map[string][]string {
	c := maps.Clone(data)
	for k, v := range c {
		c[k] = slices.Clone(v)
	}
	return c
}
