package leasekv

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"slices"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simdisk"
)

const (
	walPath = "/wal"
	// heartbeat is how often the primary sends the backup its log and commit index.
	heartbeat = 80 * time.Millisecond
)

// Store is one of the two store replicas. The replica that holds the "store" lease is the
// primary, and Peer is the other replica, the backup. The replica asks the lock for the lease
// every RenewEvery; a grant is valid until the time it sent the request plus the TTL.
//
// The primary appends entries to its log for Syncs and Writes and sends them to the backup. An
// entry is committed once the backup has acknowledged it. Committed entries are applied in order,
// on both replicas: a write appends its value to the data and records the client's Seq, so a write
// sent again is applied once, and a Sync raises the fence, below which writes are rejected. A
// replica appends to its write-ahead log and syncs it before it answers or acknowledges anything.
type Store struct {
	W          *faultline.World
	Lock, Peer kernel.NodeID
	RenewEvery time.Duration

	epoch   uint64    // the newest store epoch the replica knows
	primary bool      // the replica holds the lease for epoch
	until   time.Time // the lease is valid before until
	holder  string    // the primary the replica knows

	log     []Entry
	commit  uint64                   // the entries up to commit are committed and applied
	next    uint64                   // primary: the next entry to send to the backup
	match   uint64                   // primary: the backup's log matches up to match
	pending map[uint64]kernel.NodeID // primary: per entry, the server to answer when it commits

	data  map[string][]string
	last  map[string]uint64 // per client: Seq of its last applied write
	fence uint64            // the highest kv epoch of an applied Sync
	wal   *simdisk.File
}

// record is one line of the write-ahead log: entry Index of the log, or the commit index when
// Index is 0. An entry replaces the entries from Index on.
type record struct {
	Index  uint64 `json:"index,omitempty"`
	Entry  Entry  `json:"entry,omitzero"`
	Commit uint64 `json:"commit,omitempty"`
}

// Boot rebuilds the replica from its log. It starts as a backup.
func (s *Store) Boot(n *kernel.Node) {
	s.epoch, s.primary, s.until, s.holder = 0, false, time.Time{}, ""
	s.log, s.commit, s.next, s.match = nil, 0, 0, 0
	s.pending = map[uint64]kernel.NodeID{}
	s.data, s.last, s.fence = map[string][]string{}, map[string]uint64{}, 0
	wal, err := s.recover(n)
	if err != nil {
		n.Sim().Fail(err)
		return
	}
	s.wal = wal
	s.W.Net.Handle(n, func(from kernel.NodeID, msg any) { s.handle(n, from, msg) })
	every(n, s.RenewEvery, "renew", func() { s.W.Net.Send(n, s.Lock, Acquire{Lease: leaseStore, Sent: n.Now()}) })
	every(n, heartbeat, "heartbeat", func() {
		if s.leading(n) {
			s.replicate(n)
		}
	})
}

// List returns a copy of the list of key in the replica's applied data.
func (s *Store) List(key string) []string { return slices.Clone(s.data[key]) }

func (s *Store) handle(n *kernel.Node, from kernel.NodeID, msg any) {
	switch m := msg.(type) {
	case Grant:
		s.granted(n, m)
	case Busy:
		s.holder = m.Holder
	case Sync:
		s.sync(n, from, m)
	case Write:
		s.request(n, from, Entry{KV: m.Epoch, Client: m.Client, Seq: m.Seq, Key: m.Key, Value: m.Value})
	case Replicate:
		s.replicated(n, from, m)
	case Appended:
		if m.Epoch != s.epoch || !s.primary {
			return
		}
		s.match, s.next = max(s.match, m.Index), max(s.next, m.Index+1)
		if s.match > s.commit && s.persist(record{Commit: s.match}) == nil {
			s.advance(n, s.match)
		}
	case Mismatch:
		if m.Epoch == s.epoch && s.primary {
			s.next = max(s.match+1, min(m.Len+1, m.Prev))
			s.replicate(n)
		}
	case Stale:
		if m.Epoch > s.epoch {
			s.follow(m.Epoch, n.Sim().Node(from).Name())
		}
	}
}

// leading reports whether the replica is the primary and its lease is valid.
func (s *Store) leading(n *kernel.Node) bool {
	return s.primary && n.Now().Before(s.until)
}

// hint names the primary the replica knows, if that is the other replica.
func (s *Store) hint(n *kernel.Node) string {
	if s.holder == n.Name() {
		return ""
	}
	return s.holder
}

func (s *Store) granted(n *kernel.Node, m Grant) {
	if m.Epoch < s.epoch {
		return
	}
	if m.Epoch > s.epoch || !s.primary {
		s.follow(m.Epoch, n.Name())
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

// sync answers a Sync with the applied data. When the Sync raises the fence, the primary first
// appends the fence entry, syncs it and sends it to the backup.
func (s *Store) sync(n *kernel.Node, from kernel.NodeID, m Sync) {
	switch {
	case !s.leading(n):
		s.W.Net.Send(n, from, NotPrimary{Hint: s.hint(n)})
		return
	case m.Epoch < s.fence:
		s.W.Net.Send(n, from, Fenced{Epoch: m.Epoch})
		return
	case m.Epoch > s.fence:
		e := Entry{Epoch: s.epoch, KV: m.Epoch}
		if err := s.persist(record{Index: uint64(len(s.log)) + 1, Entry: e}); err != nil {
			return
		}
		s.log = append(s.log, e)
		s.replicate(n)
	}
	s.W.Net.Send(n, from, SyncOK{Epoch: m.Epoch, Data: clone(s.data)})
}

// request appends entry e, if the replica is the primary, and sends it to the backup.
func (s *Store) request(n *kernel.Node, from kernel.NodeID, e Entry) {
	if !s.leading(n) {
		s.W.Net.Send(n, from, NotPrimary{Hint: s.hint(n)})
		return
	}
	e.Epoch = s.epoch
	// A request sent again before its entry is committed gets no second entry.
	for i := s.commit; i < uint64(len(s.log)); i++ {
		if s.log[i] == e {
			s.pending[i+1] = from
			s.replicate(n)
			return
		}
	}
	if err := s.persist(record{Index: uint64(len(s.log)) + 1, Entry: e}); err != nil {
		return
	}
	s.log = append(s.log, e)
	s.pending[uint64(len(s.log))] = from
	s.replicate(n)
}

// replicate sends the backup the log from entry next on, and the commit index.
func (s *Store) replicate(n *kernel.Node) {
	prev := s.next - 1
	m := Replicate{Epoch: s.epoch, Prev: prev, Entries: slices.Clone(s.log[prev:]), Commit: s.commit}
	if prev > 0 {
		m.PrevEpoch = s.log[prev-1].Epoch
	}
	s.W.Net.Send(n, s.Peer, m)
}

// replicated handles a Replicate as the backup. It rejects an older store epoch, and answers a
// Replicate whose entry Prev it does not hold with the length of its log. Otherwise it appends
// the entries it lacks; where its own entry has another epoch than the primary's, it drops that
// entry and the ones after it first. It syncs the entries and the commit index it learned,
// applies the entries up to that index, and acknowledges.
func (s *Store) replicated(n *kernel.Node, from kernel.NodeID, m Replicate) {
	if m.Epoch < s.epoch {
		s.W.Net.Send(n, from, Stale{Epoch: s.epoch})
		return
	}
	if m.Epoch > s.epoch || s.primary {
		s.follow(m.Epoch, "")
	}
	s.holder = n.Sim().Node(from).Name()
	if size := uint64(len(s.log)); m.Prev > size || m.Prev > 0 && s.log[m.Prev-1].Epoch != m.PrevEpoch {
		s.W.Net.Send(n, from, Mismatch{Epoch: m.Epoch, Prev: m.Prev, Len: size})
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
	s.advance(n, commit)
	s.W.Net.Send(n, from, Appended{Epoch: m.Epoch, Index: last})
}

// advance applies the entries up to index c in order, and answers the requests the replica
// appended them for.
func (s *Store) advance(n *kernel.Node, c uint64) {
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
		if fenced {
			s.W.Net.Send(n, to, Fenced{Epoch: e.KV, Client: e.Client, Seq: e.Seq})
			continue
		}
		// A write sent again is answered with the key's current list, which holds the value.
		s.W.Net.Send(n, to, Written{Client: e.Client, Seq: e.Seq, Key: e.Key, List: slices.Clone(s.data[e.Key])})
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
	if _, err := s.wal.Append(b); err != nil {
		return err
	}
	return s.wal.Sync()
}

// recover replays the write-ahead log, the entries and the last commit index it holds, and
// applies the entries up to that index. It returns the log open for appends.
func (s *Store) recover(n *kernel.Node) (*simdisk.File, error) {
	vol := s.W.Disk.Volume(n)
	b, err := vol.ReadFile(walPath)
	if errors.Is(err, fs.ErrNotExist) {
		f, err := vol.Create(walPath)
		if err != nil {
			return nil, err
		}
		return f, vol.SyncDir("/")
	}
	if err != nil {
		return nil, err
	}
	var commit uint64
	for line := range bytes.Lines(b) {
		var r record
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, err
		}
		if r.Index == 0 {
			commit = r.Commit
			continue
		}
		s.log = append(s.log[:r.Index-1], r.Entry)
		s.epoch = max(s.epoch, r.Entry.Epoch)
	}
	s.advance(n, commit)
	return vol.Open(walPath)
}

// clone returns a deep copy of data.
func clone(data map[string][]string) map[string][]string {
	c := maps.Clone(data)
	for k, v := range c {
		c[k] = slices.Clone(v)
	}
	return c
}
