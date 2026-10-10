// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package wire

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"

	"github.com/hmdsefi/faultline/harness/etcdraft/internal/kv"
)

func raftMessages() []*raftpb.Message {
	snap := &raftpb.Snapshot{
		Metadata: &raftpb.SnapshotMetadata{Index: new(uint64(250)), Term: new(uint64(3)), ConfState: &raftpb.ConfState{Voters: []uint64{1, 2, 3}, AutoLeave: new(false)}},
		Data:     []byte("state"),
	}
	ents := []*raftpb.Entry{
		{Index: new(uint64(17)), Term: new(uint64(3)), Type: raftpb.EntryNormal.Enum(), Data: []byte("x")},
		{Index: new(uint64(18)), Term: new(uint64(3)), Type: raftpb.EntryConfChangeV2.Enum()},
	}
	return []*raftpb.Message{
		{Type: raftpb.MsgApp.Enum(), From: new(uint64(1)), To: new(uint64(2)), Term: new(uint64(3)), LogTerm: new(uint64(3)), Index: new(uint64(16)), Commit: new(uint64(16)), Entries: ents},
		{Type: raftpb.MsgAppResp.Enum(), From: new(uint64(2)), To: new(uint64(1)), Term: new(uint64(3)), Index: new(uint64(18)), Reject: new(true), RejectHint: new(uint64(9))},
		{Type: raftpb.MsgPreVote.Enum(), From: new(uint64(3)), To: new(uint64(1)), Term: new(uint64(4)), LogTerm: new(uint64(3)), Index: new(uint64(18))},
		{Type: raftpb.MsgHeartbeat.Enum(), From: new(uint64(1)), To: new(uint64(3)), Term: new(uint64(3)), Commit: new(uint64(18)), Context: []byte("ctx")},
		{Type: raftpb.MsgSnap.Enum(), From: new(uint64(1)), To: new(uint64(3)), Term: new(uint64(3)), Snapshot: snap},
	}
}

// AT-ETC-11
func TestRoundTrip(t *testing.T) {
	for _, m := range raftMessages() {
		got, err := DecodeRaft(EncodeRaft(m))
		if err != nil || !proto.Equal(got, m) {
			t.Fatalf("raft %s: round trip = %v (equal=%v)", m.GetType(), err, proto.Equal(got, m))
		}
	}
	requests := []Request{
		{Client: 2, ID: 14, Attempt: 1, Op: kv.OpPut, Key: "k3", Value: "c2-14"},
		{Client: 2, ID: 15, Attempt: 3, Op: kv.OpGet, Key: "k0"},
		{Client: 5, ID: 1, Attempt: 1, Op: kv.OpConfRead},
		{Client: 5, ID: 2, Attempt: 2, Op: kv.OpConfChange, Data: []byte{0x12, 0x04, 0x08, 0x03, 0x10, 0x04}},
	}
	for _, q := range requests {
		got, err := DecodeRequest(EncodeRequest(q))
		if err != nil || !reflect.DeepEqual(got, q) {
			t.Fatalf("request %+v: got %+v, %v", q, got, err)
		}
	}
	replies := []Reply{
		{ID: 14, Attempt: 1, Status: StatusOK, Leader: 1},
		{ID: 15, Attempt: 3, Status: StatusOK, Leader: 2, Found: true, Value: "c1-9"},
		{ID: 16, Attempt: 1, Status: StatusDropped},
		{ID: 2, Attempt: 2, Status: StatusOK, Leader: 3, Data: []byte{0x08, 0x01}},
	}
	for _, p := range replies {
		got, err := DecodeReply(EncodeReply(p))
		if err != nil || !reflect.DeepEqual(got, p) {
			t.Fatalf("reply %+v: got %+v, %v", p, got, err)
		}
	}
	if i, err := DecodeSnapAck(EncodeSnapAck(250)); err != nil || i != 250 {
		t.Fatalf("snapack: got %d, %v", i, err)
	}
}

func TestRejects(t *testing.T) {
	m := raftMessages()[0]
	cases := []struct {
		name string
		err  func() error
		want string
	}{
		{"raft trailing", func() error { _, err := DecodeRaft(append(EncodeRaft(m), 0)); return err }, "trailing"},
		{"raft short", func() error { b := EncodeRaft(m); _, err := DecodeRaft(b[:len(b)-1]); return err }, "short"},
		{"raft tag", func() error { _, err := DecodeRaft(EncodeSnapAck(1)); return err }, "tag 4"},
		{"request trailing", func() error { _, err := DecodeRequest(append(EncodeRequest(Request{Op: kv.OpGet}), 0)); return err }, "trailing"},
		{"raft undecodable", func() error { _, err := DecodeRaft([]byte{TagRaft, 0, 0, 0, 1, 0xff}); return err }, "undecodable message"},
		{"request op 0", func() error { _, err := DecodeRequest(EncodeRequest(Request{Op: 0})); return err }, "op 0"},
		{"request op 5", func() error { _, err := DecodeRequest(EncodeRequest(Request{Op: 5})); return err }, "op 5"},
		{"request op 9", func() error { _, err := DecodeRequest(EncodeRequest(Request{Op: 9})); return err }, "op 9"},
		{"reply trailing", func() error { _, err := DecodeReply(append(EncodeReply(Reply{}), 0)); return err }, "trailing"},
		{"reply status 2", func() error { _, err := DecodeReply(EncodeReply(Reply{Status: 2})); return err }, "status 2"},
		{"reply status 7", func() error { _, err := DecodeReply(EncodeReply(Reply{Status: 7})); return err }, "status 7"},
		{"reply found flag 2", func() error {
			b := EncodeReply(Reply{})
			b[22] = 2 // tag, u64 id, u32 attempt, u8 status, u64 leader, then the found flag
			_, err := DecodeReply(b)
			return err
		}, "found flag 2"},
		{"snapack trailing", func() error { _, err := DecodeSnapAck(append(EncodeSnapAck(1), 0)); return err }, "trailing"},
		{"empty", func() error { _, err := DecodeReply(nil); return err }, "empty"},
	}
	for _, c := range cases {
		err := c.err()
		if err == nil || !strings.HasPrefix(err.Error(), "wire: bad ") || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to contain %q", c.name, err, c.want)
		}
	}
}

// ETC-091: the text of an undecodable raft message is fixed. protobuf-go spaces its own
// error messages differently from one binary to the next, and harness errors are hashed
// (ETC-121), so the protobuf error is kept as the cause and never printed.
func TestDecodeRaftErrorText(t *testing.T) {
	_, err := DecodeRaft([]byte{TagRaft, 0, 0, 0, 1, 0xff})
	if want := "wire: bad raft: undecodable message"; err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if strings.Contains(err.Error(), "proto") {
		t.Fatalf("text %q contains protobuf's error message", err)
	}
	if errors.Unwrap(err) == nil {
		t.Fatal("the protobuf error is not kept as the cause")
	}
}

// ETC-091: EncodeRequest panics on a key longer than 65535 bytes, as kv.EncodeCommand does,
// instead of encoding a truncated length (a 65536-byte key would read back as a different request).
func TestEncodeRequestKeyLimit(t *testing.T) {
	longest := Request{Client: 1, ID: 2, Attempt: 3, Op: kv.OpGet, Key: strings.Repeat("k", 0xffff)}
	if got, err := DecodeRequest(EncodeRequest(longest)); err != nil || !reflect.DeepEqual(got, longest) {
		t.Fatalf("a key of 65535 bytes: round trip failed: %v", err)
	}
	want := "wire: key of 65536 bytes is longer than 65535"
	var got any
	func() {
		defer func() { got = recover() }()
		EncodeRequest(Request{Op: kv.OpGet, Key: strings.Repeat("k", 0x10000)})
	}()
	if got != want {
		t.Fatalf("EncodeRequest of a 65536-byte key: panic = %v, want %q", got, want)
	}
}

// ETC-091: decoded Data is a copy, not a view of the packet bytes.
func TestDecodeCopiesData(t *testing.T) {
	b := EncodeRequest(Request{Op: kv.OpConfChange, Data: []byte{1, 2, 3}})
	q, err := DecodeRequest(b)
	if err != nil {
		t.Fatal(err)
	}
	for i := range b {
		b[i] = 0xee
	}
	if !reflect.DeepEqual(q.Data, []byte{1, 2, 3}) {
		t.Fatalf("Data = %v after the packet bytes changed, want [1 2 3]", q.Data)
	}
}
