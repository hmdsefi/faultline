// Package wal encodes and parses the harness's write-ahead log. It is pure: callers
// do the file I/O.
package wal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"

	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"
)

// RecordType identifies a WAL record's payload.
type RecordType uint8

const (
	RecEntry           RecordType = 1 // proto raftpb.Entry
	RecHardState       RecordType = 2 // proto raftpb.HardState
	RecSnapshotInstall RecordType = 3 // proto raftpb.Snapshot from rd.Snapshot (or the bootstrap)
	RecSnapshotLocal   RecordType = 4 // proto raftpb.Snapshot from MemoryStorage.CreateSnapshot
)

const (
	HeaderSize   = 8        // uint32 length + uint32 CRC32-C
	MaxRecordLen = 64 << 20 // maximum value of the length field
)

var (
	ErrEntryGap      = errors.New("entry gap")           // an entry index skips past the next expected index
	ErrUnknownRecord = errors.New("unknown record type") // a CRC-valid record has an unknown type
	ErrBadPayload    = errors.New("bad payload")         // a CRC-valid record's payload fails proto.Unmarshal
)

// payloadError is the error Read returns for ErrBadPayload. Its text names the record
// and where it is and never includes protobuf's error message: protobuf-go varies the
// spacing of its messages from one binary to the next on purpose, and harness errors are
// hashed (ETC-121). The protobuf error stays reachable through errors.Is and errors.As.
type payloadError struct {
	what  string // "entry", "hardstate" or "snapshot"
	off   int    // offset of the record
	cause error  // the proto.Unmarshal error; not part of the text
}

func (e *payloadError) Error() string {
	return fmt.Sprintf("%s: %s record at offset %d", ErrBadPayload, e.what, e.off)
}

func (e *payloadError) Unwrap() []error { return []error{ErrBadPayload, e.cause} }

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

var marshalOptions = proto.MarshalOptions{Deterministic: true}

// AppendRecord appends one encoded record to dst and returns the extended slice.
// It panics if 1+len(payload) exceeds MaxRecordLen.
func AppendRecord(dst []byte, typ RecordType, payload []byte) []byte {
	l := 1 + len(payload)
	if l > MaxRecordLen {
		panic(fmt.Sprintf("wal: record of %d bytes exceeds MaxRecordLen", l))
	}
	crc := crc32.Update(0, castagnoli, []byte{byte(typ)})
	crc = crc32.Update(crc, castagnoli, payload)
	dst = binary.BigEndian.AppendUint32(dst, uint32(l))
	dst = binary.BigEndian.AppendUint32(dst, crc)
	dst = append(dst, byte(typ))
	return append(dst, payload...)
}

// Marshal encodes msg with proto.MarshalOptions{Deterministic: true}. It panics on a
// marshal error, which raftpb messages cannot produce.
func Marshal(msg proto.Message) []byte {
	b, err := marshalOptions.Marshal(msg)
	if err != nil {
		panic(fmt.Sprintf("wal: marshal %s: %v", proto.MessageName(msg), err))
	}
	return b
}

// State is the log content recovered from WAL bytes.
type State struct {
	Snapshot  *raftpb.Snapshot  // latest effective snapshot; nil if none
	Entries   []*raftpb.Entry   // contiguous, first index SnapIndex()+1
	HardState *raftpb.HardState // last HardState record; nil if none
	Records   int               // valid records read
	ValidEnd  int64             // byte offset just past the last valid record
	Size      int64             // len(data)
}

// SnapIndex returns the snapshot index, or 0 if Snapshot is nil.
func (s *State) SnapIndex() uint64 {
	return s.Snapshot.GetMetadata().GetIndex()
}

// LastIndex returns the index of the last entry, or SnapIndex() if there are none.
func (s *State) LastIndex() uint64 {
	if n := len(s.Entries); n > 0 {
		return s.Entries[n-1].GetIndex()
	}
	return s.SnapIndex()
}

// Read parses data per ETC-043. It stops without error at the first invalid record
// (torn or corrupt tail) and returns an error only for ErrEntryGap, ErrUnknownRecord,
// and ErrBadPayload.
func Read(data []byte) (State, error) {
	st := State{Size: int64(len(data))}
	var base uint64 // snapshot index, 0 without a snapshot
	var ents []*raftpb.Entry
	off := 0
	for {
		rest := len(data) - off
		if rest < HeaderSize {
			break
		}
		l := binary.BigEndian.Uint32(data[off:])
		if l == 0 || l > MaxRecordLen || uint64(rest-HeaderSize) < uint64(l) {
			break
		}
		body := data[off+HeaderSize : off+HeaderSize+int(l)]
		if crc32.Checksum(body, castagnoli) != binary.BigEndian.Uint32(data[off+4:]) {
			break
		}
		typ, payload := RecordType(body[0]), body[1:]
		switch typ {
		case RecEntry:
			e := new(raftpb.Entry)
			if err := proto.Unmarshal(payload, e); err != nil {
				return State{}, &payloadError{"entry", off, err}
			}
			i := e.GetIndex()
			if i <= base {
				break // below the snapshot: ignore
			}
			next := base + uint64(len(ents)) + 1
			if i > next {
				return State{}, fmt.Errorf("%w: entry %d at offset %d, expected at most %d", ErrEntryGap, i, off, next)
			}
			ents = append(ents[:i-base-1], e)
		case RecHardState:
			hs := new(raftpb.HardState)
			if err := proto.Unmarshal(payload, hs); err != nil {
				return State{}, &payloadError{"hardstate", off, err}
			}
			st.HardState = hs
		case RecSnapshotInstall, RecSnapshotLocal:
			s := new(raftpb.Snapshot)
			if err := proto.Unmarshal(payload, s); err != nil {
				return State{}, &payloadError{"snapshot", off, err}
			}
			idx := s.GetMetadata().GetIndex()
			if typ == RecSnapshotInstall {
				st.Snapshot, base, ents = s, idx, nil
				break
			}
			if st.Snapshot != nil && idx <= base {
				break // older local snapshot: ignore
			}
			if keep := idx - base; keep < uint64(len(ents)) {
				ents = ents[keep:]
			} else {
				ents = nil
			}
			st.Snapshot, base = s, idx
		default:
			return State{}, fmt.Errorf("%w: type %d at offset %d", ErrUnknownRecord, typ, off)
		}
		off += HeaderSize + int(l)
		st.Records++
		st.ValidEnd = int64(off)
	}
	st.Entries = ents
	return st, nil
}
