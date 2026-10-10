// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package wal

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"strings"
	"testing"

	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

func entry(index, term uint64, data string) *raftpb.Entry {
	return &raftpb.Entry{Index: new(index), Term: new(term), Type: raftpb.EntryNormal.Enum(), Data: []byte(data)}
}

func snapshot(index, term uint64, voters []uint64, data string) *raftpb.Snapshot {
	return &raftpb.Snapshot{
		Metadata: &raftpb.SnapshotMetadata{
			Index:     new(index),
			Term:      new(term),
			ConfState: &raftpb.ConfState{Voters: voters, AutoLeave: new(false)},
		},
		Data: []byte(data),
	}
}

func hardState(term, vote, commit uint64) *raftpb.HardState {
	return &raftpb.HardState{Term: new(term), Vote: new(vote), Commit: new(commit)}
}

// build encodes records in order and returns the bytes and each record's end offset.
func build(recs ...any) ([]byte, []int64) {
	var b []byte
	var ends []int64
	for _, r := range recs {
		switch v := r.(type) {
		case *raftpb.Entry:
			b = AppendRecord(b, RecEntry, Marshal(v))
		case *raftpb.HardState:
			b = AppendRecord(b, RecHardState, Marshal(v))
		case installed:
			b = AppendRecord(b, RecSnapshotInstall, Marshal(v.s))
		case local:
			b = AppendRecord(b, RecSnapshotLocal, Marshal(v.s))
		default:
			panic(fmt.Sprintf("build: unexpected %T", r))
		}
		ends = append(ends, int64(len(b)))
	}
	return b, ends
}

type installed struct{ s *raftpb.Snapshot }
type local struct{ s *raftpb.Snapshot }

func indices(ents []*raftpb.Entry) []uint64 {
	var out []uint64
	for _, e := range ents {
		out = append(out, e.GetIndex())
	}
	return out
}

func sameIndices(t *testing.T, got []*raftpb.Entry, want ...uint64) {
	t.Helper()
	if fmt.Sprint(indices(got)) != fmt.Sprint(want) {
		t.Fatalf("entry indices = %v, want %v", indices(got), want)
	}
}

// AT-ETC-04
func TestReadEveryRecordType(t *testing.T) {
	boot := snapshot(1, 1, []uint64{1, 2, 3}, "boot")
	loc := snapshot(2, 2, []uint64{1, 2, 3}, "local")
	hs1, hs2 := hardState(1, 0, 1), hardState(2, 1, 3)
	e2, e3, e4 := entry(2, 2, "a"), entry(3, 2, "b"), entry(4, 2, "c")
	data, _ := build(installed{boot}, hs1, e2, e3, hs2, local{loc}, e4)

	st, err := Read(data)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !proto.Equal(st.Snapshot, loc) {
		t.Fatalf("Snapshot index = %d, want the local snapshot at 2", st.Snapshot.GetMetadata().GetIndex())
	}
	sameIndices(t, st.Entries, 3, 4)
	if !proto.Equal(st.Entries[0], e3) || !proto.Equal(st.Entries[1], e4) {
		t.Fatal("entries differ from the written ones")
	}
	if !proto.Equal(st.HardState, hs2) {
		t.Fatal("HardState differs from the last written one")
	}
	if st.Records != 7 || st.ValidEnd != st.Size || st.Size != int64(len(data)) {
		t.Fatalf("Records=%d ValidEnd=%d Size=%d, want 7, %d, %d", st.Records, st.ValidEnd, st.Size, len(data), len(data))
	}
	if st.SnapIndex() != 2 || st.LastIndex() != 4 {
		t.Fatalf("SnapIndex=%d LastIndex=%d, want 2, 4", st.SnapIndex(), st.LastIndex())
	}
}

// ETC-040: the bytes of one record of each type are u32 L (type and payload), u32 CRC32-C
// of type and payload, the type byte, the payload, all big-endian. The vectors were computed
// with a bitwise CRC32-C that shares no code with this package, so a change made to the
// writer and the reader alike (another polynomial, byte order or field order) fails here.
func TestRecordBytes(t *testing.T) {
	cases := []struct {
		name string
		typ  RecordType
		msg  proto.Message
		hex  string
	}{
		{"entry", RecEntry, entry(1, 1, "a"), "0000000a5ab7091301080010011801220161"},
		{"hardstate", RecHardState, hardState(3, 2, 1), "00000007b5bcc91c02080310021801"},
		{"snapshot install", RecSnapshotInstall, snapshot(5, 2, []uint64{1, 2}, "s"), "000000128cf2ca6a030a0173120c0a0608010802280010051802"},
		{"snapshot local", RecSnapshotLocal, snapshot(5, 2, []uint64{1, 2}, "s"), "00000012a0edc4bb040a0173120c0a0608010802280010051802"},
	}
	for _, c := range cases {
		if got := hex.EncodeToString(AppendRecord(nil, c.typ, Marshal(c.msg))); got != c.hex {
			t.Errorf("%s: AppendRecord = %s, want %s", c.name, got, c.hex)
		}
		want, err := hex.DecodeString(c.hex)
		if err != nil {
			t.Fatal(err)
		}
		if st, err := Read(want); err != nil || st.Records != 1 || st.ValidEnd != int64(len(want)) {
			t.Errorf("%s: Read(golden) = %d records, valid end %d of %d, %v", c.name, st.Records, st.ValidEnd, len(want), err)
		}
	}
}

// ETC-040: L = 1 + len(payload) is at most MaxRecordLen; AppendRecord panics above it.
func TestAppendRecordLimit(t *testing.T) {
	rec := AppendRecord(nil, RecEntry, make([]byte, MaxRecordLen-1)) // the largest record
	if l := binary.BigEndian.Uint32(rec); l != MaxRecordLen || len(rec) != HeaderSize+MaxRecordLen {
		t.Fatalf("largest record: L = %d, len = %d, want %d, %d", l, len(rec), MaxRecordLen, HeaderSize+MaxRecordLen)
	}
	want := fmt.Sprintf("wal: record of %d bytes exceeds MaxRecordLen", MaxRecordLen+1)
	var got any
	func() {
		defer func() { got = recover() }()
		AppendRecord(nil, RecEntry, make([]byte, MaxRecordLen))
	}()
	if got != want {
		t.Fatalf("AppendRecord of MaxRecordLen+1 bytes: panic = %v, want %q", got, want)
	}
}

// AT-ETC-05
func TestReadStopsAtInvalidTail(t *testing.T) {
	data, ends := build(entry(1, 1, "a"), entry(2, 1, "b"), entry(3, 1, "c"))
	prev := ends[1]

	t.Run("truncated", func(t *testing.T) {
		st, err := Read(data[:len(data)-1])
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if st.ValidEnd != prev || st.Records != 2 {
			t.Fatalf("ValidEnd=%d Records=%d, want %d, 2", st.ValidEnd, st.Records, prev)
		}
		sameIndices(t, st.Entries, 1, 2)
	})
	t.Run("every prefix", func(t *testing.T) {
		for n := 0; n <= len(data); n++ {
			wantEnd, wantRecords := int64(0), 0
			for i, end := range ends {
				if end <= int64(n) {
					wantEnd, wantRecords = end, i+1
				}
			}
			st, err := Read(data[:n])
			if err != nil || st.ValidEnd != wantEnd || st.Records != wantRecords || st.Size != int64(n) {
				t.Fatalf("prefix of %d bytes: ValidEnd=%d Records=%d Size=%d err=%v, want %d, %d, %d, nil",
					n, st.ValidEnd, st.Records, st.Size, err, wantEnd, wantRecords, n)
			}
		}
	})
	t.Run("bit flip", func(t *testing.T) {
		bad := append([]byte(nil), data...)
		bad[len(bad)-1] ^= 0x01 // last payload byte of the last record
		st, err := Read(bad)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if st.ValidEnd != prev || st.Records != 2 {
			t.Fatalf("ValidEnd=%d Records=%d, want %d, 2", st.ValidEnd, st.Records, prev)
		}
		sameIndices(t, st.Entries, 1, 2)
	})
	t.Run("every bit flip in the last record", func(t *testing.T) {
		for i := int(prev); i < len(data); i++ {
			for bit := 0; bit < 8; bit++ {
				bad := append([]byte(nil), data...)
				bad[i] ^= 1 << bit
				st, err := Read(bad)
				if err != nil || st.ValidEnd != prev || st.Records != 2 {
					t.Fatalf("byte %d bit %d: ValidEnd=%d Records=%d err=%v, want %d, 2, nil", i, bit, st.ValidEnd, st.Records, err, prev)
				}
			}
		}
	})
	t.Run("short header and zero length", func(t *testing.T) {
		for _, tail := range [][]byte{{0, 0, 0}, {0, 0, 0, 0, 0, 0, 0, 0, 1}} {
			st, err := Read(append(append([]byte(nil), data[:prev]...), tail...))
			if err != nil || st.ValidEnd != prev {
				t.Fatalf("tail %v: ValidEnd=%d err=%v, want %d, nil", tail, st.ValidEnd, err, prev)
			}
		}
	})
	t.Run("length above MaxRecordLen", func(t *testing.T) {
		// A record whose L is MaxRecordLen+1 and whose bytes are all there, with a valid CRC.
		body := make([]byte, MaxRecordLen+1)
		body[0] = byte(RecEntry)
		big := binary.BigEndian.AppendUint32(append([]byte(nil), data[:prev]...), MaxRecordLen+1)
		big = binary.BigEndian.AppendUint32(big, crc32.Checksum(body, castagnoli))
		big = append(big, body...)
		st, err := Read(big)
		if err != nil || st.ValidEnd != prev || st.Records != 2 {
			t.Fatalf("ValidEnd=%d Records=%d err=%v, want %d, 2, nil", st.ValidEnd, st.Records, err, prev)
		}
	})
	t.Run("empty", func(t *testing.T) {
		st, err := Read(nil)
		if err != nil || st.Records != 0 || st.ValidEnd != 0 || st.Size != 0 || len(st.Entries) != 0 || st.Snapshot != nil || st.HardState != nil {
			t.Fatalf("Read(nil) = %d records, valid end %d, size %d, %d entries, %v", st.Records, st.ValidEnd, st.Size, len(st.Entries), err)
		}
	})
}

// AT-ETC-06: the errors and their texts (ETC-043). Every text is fixed: none carries
// protobuf's error message, which protobuf-go spaces differently from one binary to the
// next, because harness errors are hashed (ETC-121).
func TestReadErrors(t *testing.T) {
	t.Run("entry gap", func(t *testing.T) {
		data, ends := build(entry(1, 1, "a"), entry(2, 1, "b"), entry(3, 1, "c"), entry(5, 1, "e"))
		_, err := Read(data)
		want := fmt.Sprintf("entry gap: entry 5 at offset %d, expected at most 4", ends[2])
		if !errors.Is(err, ErrEntryGap) || err.Error() != want {
			t.Fatalf("err = %v, want ErrEntryGap with text %q", err, want)
		}
	})
	t.Run("entry gap after a snapshot", func(t *testing.T) {
		data, ends := build(installed{snapshot(10, 3, []uint64{1}, "s")}, entry(12, 3, "c"))
		_, err := Read(data)
		want := fmt.Sprintf("entry gap: entry 12 at offset %d, expected at most 11", ends[0])
		if !errors.Is(err, ErrEntryGap) || err.Error() != want {
			t.Fatalf("err = %v, want ErrEntryGap with text %q", err, want)
		}
	})
	t.Run("unknown record", func(t *testing.T) {
		prefix, ends := build(entry(1, 1, "a"))
		for _, c := range []struct {
			typ RecordType
			off int64
		}{{9, 0}, {0, 0}, {9, ends[0]}, {5, ends[0]}} {
			data := AppendRecord(append([]byte(nil), prefix[:c.off]...), c.typ, []byte("x"))
			_, err := Read(data)
			want := fmt.Sprintf("unknown record type: type %d at offset %d", c.typ, c.off)
			if !errors.Is(err, ErrUnknownRecord) || err.Error() != want {
				t.Errorf("type %d at %d: err = %v, want ErrUnknownRecord with text %q", c.typ, c.off, err, want)
			}
		}
	})
	t.Run("bad payload", func(t *testing.T) {
		prefix, ends := build(entry(1, 1, "a"))
		for _, c := range []struct {
			typ  RecordType
			what string
		}{{RecEntry, "entry"}, {RecHardState, "hardstate"}, {RecSnapshotInstall, "snapshot"}, {RecSnapshotLocal, "snapshot"}} {
			for _, off := range []int64{0, ends[0]} {
				data := AppendRecord(append([]byte(nil), prefix[:off]...), c.typ, []byte{0xff, 0xff, 0xff})
				_, err := Read(data)
				want := fmt.Sprintf("bad payload: %s record at offset %d", c.what, off)
				var pe *payloadError
				if !errors.Is(err, ErrBadPayload) || err.Error() != want || !errors.As(err, &pe) || pe.cause == nil || !errors.Is(err, pe.cause) {
					t.Errorf("type %d at %d: err = %v, want ErrBadPayload with text %q and the proto error as its cause", c.typ, off, err, want)
				}
				if err != nil && strings.Contains(err.Error(), "proto") {
					t.Errorf("type %d at %d: text %q contains protobuf's error message", c.typ, off, err)
				}
			}
		}
	})
}

// AT-ETC-07
func TestReadTruncatesOnOverwrite(t *testing.T) {
	data, _ := build(entry(1, 1, "a"), entry(2, 1, "b"), entry(3, 1, "c"), entry(4, 1, "d"), entry(5, 1, "e"), entry(3, 2, "x"))
	st, err := Read(data)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	sameIndices(t, st.Entries, 1, 2, 3)
	if st.Entries[2].GetTerm() != 2 || string(st.Entries[2].GetData()) != "x" {
		t.Fatalf("entry 3 = term %d data %q, want term 2 data \"x\"", st.Entries[2].GetTerm(), st.Entries[2].GetData())
	}
}

// AT-ETC-08
func TestReadLocalSnapshots(t *testing.T) {
	recs := []any{}
	for i := uint64(1); i <= 6; i++ {
		recs = append(recs, entry(i, 1, "v"))
	}
	recs = append(recs,
		local{snapshot(4, 1, []uint64{1, 2, 3}, "s4")},
		local{snapshot(2, 1, []uint64{1, 2, 3}, "s2")},  // older: ignored
		local{snapshot(4, 2, []uint64{1, 2, 3}, "s4b")}, // same index: ignored (ETC-043: s.Index > base)
	)
	data, _ := build(recs...)
	st, err := Read(data)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if st.SnapIndex() != 4 || string(st.Snapshot.GetData()) != "s4" || st.Snapshot.GetMetadata().GetTerm() != 1 {
		t.Fatalf("snapshot index %d term %d data %q, want 4, 1, \"s4\"", st.SnapIndex(), st.Snapshot.GetMetadata().GetTerm(), st.Snapshot.GetData())
	}
	sameIndices(t, st.Entries, 5, 6)
	if st.Records != 9 {
		t.Fatalf("Records = %d, want 9 (an ignored snapshot is still a valid record)", st.Records)
	}
}

// ETC-043, RecSnapshotLocal: a snapshot beyond the last entry drops them all and moves LastIndex to it.
func TestReadLocalSnapshotBeyondLog(t *testing.T) {
	data, _ := build(entry(1, 1, "a"), entry(2, 1, "b"), local{snapshot(7, 1, []uint64{1}, "s7")})
	st, err := Read(data)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	sameIndices(t, st.Entries)
	if st.SnapIndex() != 7 || st.LastIndex() != 7 {
		t.Fatalf("SnapIndex=%d LastIndex=%d, want 7, 7", st.SnapIndex(), st.LastIndex())
	}
}

// ETC-043, RecEntry: i <= base is ignored, but the record is valid (Records counts it).
func TestReadIgnoresEntriesAtOrBelowSnapshot(t *testing.T) {
	t.Run("after an install", func(t *testing.T) {
		data, _ := build(installed{snapshot(10, 3, []uint64{1}, "s")}, entry(10, 9, "at"), entry(9, 9, "below"), entry(1, 9, "far below"), entry(11, 3, "c"))
		st, err := Read(data)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		sameIndices(t, st.Entries, 11)
		if st.Records != 5 || st.ValidEnd != st.Size {
			t.Fatalf("Records=%d ValidEnd=%d Size=%d, want 5 and ValidEnd == Size", st.Records, st.ValidEnd, st.Size)
		}
	})
	t.Run("after a local snapshot", func(t *testing.T) {
		data, _ := build(entry(1, 1, "a"), entry(2, 1, "b"), entry(3, 1, "c"), local{snapshot(2, 1, []uint64{1}, "s2")}, entry(2, 9, "at"), entry(1, 9, "below"), entry(4, 1, "d"))
		st, err := Read(data)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		sameIndices(t, st.Entries, 3, 4)
		if st.Entries[0].GetTerm() != 1 {
			t.Fatalf("entry 3 has term %d, want its own term 1", st.Entries[0].GetTerm())
		}
	})
}

// ETC-043, RecSnapshotInstall: the whole log is discarded, whatever the indexes.
func TestReadInstallDiscardsLog(t *testing.T) {
	recs := []any{entry(1, 1, "a"), entry(2, 1, "b"), installed{snapshot(10, 3, []uint64{1, 2, 3}, "s")}}
	read := func(more ...any) (State, error) {
		data, _ := build(append(append([]any(nil), recs...), more...)...)
		return Read(data)
	}
	t.Run("right after the install", func(t *testing.T) {
		st, err := read()
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		sameIndices(t, st.Entries)
		if st.SnapIndex() != 10 || st.LastIndex() != 10 {
			t.Fatalf("SnapIndex=%d LastIndex=%d, want 10, 10", st.SnapIndex(), st.LastIndex())
		}
	})
	t.Run("the next entry is 11", func(t *testing.T) {
		st, err := read(entry(11, 3, "c"))
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		sameIndices(t, st.Entries, 11)
	})
	t.Run("12 is a gap", func(t *testing.T) {
		if _, err := read(entry(12, 3, "c")); !errors.Is(err, ErrEntryGap) {
			t.Fatalf("err = %v, want ErrEntryGap", err)
		}
	})
	t.Run("an older install still resets", func(t *testing.T) {
		st, err := read(installed{snapshot(5, 2, []uint64{1}, "older")})
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if st.SnapIndex() != 5 || string(st.Snapshot.GetData()) != "older" {
			t.Fatalf("SnapIndex=%d data %q, want 5, \"older\"", st.SnapIndex(), st.Snapshot.GetData())
		}
	})
}

// ETC-006: Marshal is deterministic. raftpb messages have no map fields, so a message with
// a map (a structpb.Struct) is what shows the option is set.
func TestMarshalDeterministic(t *testing.T) {
	s := &structpb.Struct{Fields: map[string]*structpb.Value{}}
	for i := 0; i < 64; i++ {
		s.Fields[fmt.Sprintf("key%02d", i)] = structpb.NewStringValue(fmt.Sprint(i))
	}
	first := Marshal(s)
	want, err := proto.MarshalOptions{Deterministic: true}.Marshal(s)
	if err != nil || string(first) != string(want) {
		t.Fatalf("Marshal differs from the deterministic encoding (err %v)", err)
	}
	for i := 0; i < 20; i++ {
		if got := Marshal(s); string(got) != string(first) {
			t.Fatalf("Marshal run %d differs from the first", i)
		}
	}
}

func BenchmarkWALRead(b *testing.B) {
	var data []byte
	payload := make([]byte, 1000)
	for i := uint64(1); len(data) < 1<<20; i++ {
		data = AppendRecord(data, RecEntry, Marshal(&raftpb.Entry{Index: new(i), Term: new(uint64(1)), Data: payload}))
	}
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		if _, err := Read(data); err != nil {
			b.Fatal(err)
		}
	}
}
