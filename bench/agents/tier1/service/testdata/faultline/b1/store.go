package leasekv

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"slices"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simdisk"
)

const walPath = "/wal"

// Store keeps the list of every key in a write-ahead log on its volume. It syncs the log before
// every reply, so a reply only describes durable state. It remembers the last write of every
// client in the same log, so a write sent again is applied once. It rejects requests whose epoch
// is below the highest epoch it has seen.
type Store struct {
	W *faultline.World

	data  map[string][]string
	last  map[string]uint64 // per client: Seq of its last applied write
	fence uint64
	wal   *simdisk.File
}

// record is one line of the write-ahead log: a write, or only an epoch when Key is empty.
type record struct {
	Epoch  uint64 `json:"epoch"`
	Client string `json:"client,omitempty"`
	Seq    uint64 `json:"seq,omitempty"`
	Key    string `json:"key,omitempty"`
	Value  string `json:"value,omitempty"`
}

// Boot rebuilds the store from its log and starts serving.
func (s *Store) Boot(n *kernel.Node) {
	s.data, s.last, s.fence = map[string][]string{}, map[string]uint64{}, 0
	wal, err := s.recover(s.W.Disk.Volume(n))
	if err != nil {
		n.Sim().Fail(err)
		return
	}
	s.wal = wal
	s.W.Net.Handle(n, func(from kernel.NodeID, msg any) {
		switch m := msg.(type) {
		case Sync:
			s.sync(n, from, m)
		case Write:
			s.write(n, from, m)
		}
	})
}

// List returns a copy of the list of key.
func (s *Store) List(key string) []string { return slices.Clone(s.data[key]) }

func (s *Store) sync(n *kernel.Node, from kernel.NodeID, m Sync) {
	if m.Epoch < s.fence {
		s.W.Net.Send(n, from, Fenced{Epoch: m.Epoch, Fence: s.fence})
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
	s.W.Net.Send(n, from, SyncOK{Epoch: m.Epoch, Data: data})
}

func (s *Store) write(n *kernel.Node, from kernel.NodeID, m Write) {
	if m.Epoch < s.fence {
		s.W.Net.Send(n, from, Fenced{Epoch: m.Epoch, Fence: s.fence})
		return
	}
	if m.Seq > s.last[m.Client] {
		r := record{Epoch: m.Epoch, Client: m.Client, Seq: m.Seq, Key: m.Key, Value: m.Value}
		if err := s.log(r); err != nil {
			return
		}
	}
	s.W.Net.Send(n, from, Written{Client: m.Client, Seq: m.Seq, Key: m.Key, List: slices.Clone(s.data[m.Key])})
}

// log appends r to the log, syncs it, and applies it.
func (s *Store) log(r record) error {
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err := s.wal.Append(append(line, '\n')); err != nil {
		return err
	}
	if err := s.wal.Sync(); err != nil {
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

// recover replays the log and returns it open for appends.
func (s *Store) recover(vol *simdisk.Volume) (*simdisk.File, error) {
	log, err := vol.ReadFile(walPath)
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
	for line := range bytes.Lines(log) {
		var r record
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, err
		}
		s.apply(r)
	}
	return vol.Open(walPath)
}
