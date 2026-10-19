package leasekv

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"sync"
)

// Disk is the store's log file. Bytes written with Append are durable only after Sync returns
// nil. A Disk belongs to one start of the store.
type Disk interface {
	ReadAll() ([]byte, error)
	Append(p []byte) error
	Sync() error
}

// Store keeps the list of every key in a write-ahead log on its disk. It syncs the log before
// every reply, so a reply only describes durable state. It remembers the last write of every
// client in the same log, so a write sent again is applied once. It rejects requests whose epoch
// is below the highest epoch it has seen.
type Store struct {
	Conn Conn
	Disk Disk

	mu    sync.Mutex
	data  map[string][]string
	last  map[string]uint64 // per client: Seq of its last applied write
	fence uint64
}

// record is one line of the write-ahead log: a write, or only an epoch when Key is empty.
type record struct {
	Epoch  uint64 `json:"epoch"`
	Client string `json:"client,omitempty"`
	Seq    uint64 `json:"seq,omitempty"`
	Key    string `json:"key,omitempty"`
	Value  string `json:"value,omitempty"`
}

// Start rebuilds the store from its log. Call it before the first Handle.
func (s *Store) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data, s.last, s.fence = map[string][]string{}, map[string]uint64{}, 0
	return s.recover()
}

// List returns a copy of the list of key.
func (s *Store) List(key string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.data[key])
}

// Handle handles one message from the server named from.
func (s *Store) Handle(from string, msg any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch m := msg.(type) {
	case Sync:
		s.sync(from, m)
	case Write:
		s.write(from, m)
	}
}

func (s *Store) sync(from string, m Sync) {
	if m.Epoch < s.fence {
		s.Conn.Send(from, Fenced{Epoch: m.Epoch, Fence: s.fence})
		return
	}
	if m.Epoch > s.fence {
		if err := s.log(record{Epoch: m.Epoch}); err != nil {
			return
		}
	}
	data := maps.Clone(s.data)
	for k, v := range data {
		data[k] = slices.Clone(v)
	}
	s.Conn.Send(from, SyncOK{Epoch: m.Epoch, Data: data})
}

func (s *Store) write(from string, m Write) {
	if m.Epoch < s.fence {
		s.Conn.Send(from, Fenced{Epoch: m.Epoch, Fence: s.fence, Client: m.Client, Seq: m.Seq})
		return
	}
	if m.Seq > s.last[m.Client] {
		r := record{Epoch: m.Epoch, Client: m.Client, Seq: m.Seq, Key: m.Key, Value: m.Value}
		if err := s.log(r); err != nil {
			return
		}
	}
	s.Conn.Send(from, Written{Client: m.Client, Seq: m.Seq, Key: m.Key, List: slices.Clone(s.data[m.Key])})
}

// log appends r to the log, syncs it, and applies it.
func (s *Store) log(r record) error {
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := s.Disk.Append(append(line, '\n')); err != nil {
		return err
	}
	if err := s.Disk.Sync(); err != nil {
		return err
	}
	s.apply(r)
	return nil
}

func (s *Store) apply(r record) {
	s.fence = max(s.fence, r.Epoch)
	if r.Key != "" {
		s.data[r.Key] = append(s.data[r.Key], r.Value)
		s.last[r.Client] = r.Seq
	}
}

// recover replays the log.
func (s *Store) recover() error {
	log, err := s.Disk.ReadAll()
	if err != nil {
		return err
	}
	for line := range bytes.Lines(log) {
		var r record
		if err := json.Unmarshal(line, &r); err != nil {
			return err
		}
		s.apply(r)
	}
	return nil
}
