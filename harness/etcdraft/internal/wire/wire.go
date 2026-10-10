// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package wire encodes the bytes servers and clients exchange over simnet.
//
// Every encoding starts with its tag byte; multi-byte integers are big-endian.
//
//	raft:    TagRaft    · u32 len · proto raftpb.Message (wal.Marshal)
//	request: TagRequest · u32 client · u64 id · u32 attempt · u8 op · u16 len · key · u32 len · value · u32 len · data
//	reply:   TagReply   · u64 id · u32 attempt · u8 status · u64 leader · u8 found · u32 len · value · u32 len · data
//	snapack: TagSnapAck · u64 index
//
// Decoders reject a wrong tag, a short buffer, trailing bytes, and out-of-range enums.
package wire

import (
	"encoding/binary"
	"fmt"

	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"

	"github.com/hmdsefi/faultline/harness/etcdraft/internal/kv"
	"github.com/hmdsefi/faultline/harness/etcdraft/internal/wal"
)

// The tag byte that starts every encoding (ETC-091).
const (
	TagRaft    byte = 1 // a raft message between servers
	TagRequest byte = 2 // a client request to a server
	TagReply   byte = 3 // a server reply to a client
	TagSnapAck byte = 4 // a follower's acknowledgement that it installed a snapshot
)

// Request is a client request to a server.
type Request struct {
	Client  uint32
	ID      uint64
	Attempt uint32
	Op      kv.Op
	Key     string
	Value   string
	Data    []byte // OpConfChange: proto ConfChangeV2 without Context
}

// Status is a reply status.
type Status uint8

const (
	StatusOK      Status = 0
	StatusDropped Status = 1 // the server's Propose returned raft.ErrProposalDropped
)

// Reply is a server reply to a client.
type Reply struct {
	ID      uint64
	Attempt uint32
	Status  Status
	Leader  uint64 // replying server's SoftState.Lead; 0 = unknown
	Found   bool
	Value   string
	Data    []byte // OpConfRead and OpConfChange: proto ConfState
}

// EncodeRaft encodes m with wal.Marshal behind the TagRaft byte and a u32 length.
func EncodeRaft(m *raftpb.Message) []byte {
	pb := wal.Marshal(m)
	b := make([]byte, 0, 5+len(pb))
	b = append(b, TagRaft)
	b = binary.BigEndian.AppendUint32(b, uint32(len(pb))) //nolint:gosec // a raft message is far below 4 GiB
	return append(b, pb...)
}

// DecodeRaft parses an encoding produced by EncodeRaft (ETC-091). An undecodable message
// reads wire: bad raft: undecodable message; see badError.
func DecodeRaft(b []byte) (*raftpb.Message, error) {
	r, err := open(b, TagRaft, "raft")
	if err != nil {
		return nil, err
	}
	pb := r.take(int(r.u32()))
	if err := r.done("raft"); err != nil {
		return nil, err
	}
	m := new(raftpb.Message)
	if err := proto.Unmarshal(pb, m); err != nil {
		return nil, &badError{text: "wire: bad raft: undecodable message", cause: err}
	}
	return m, nil
}

// EncodeRequest encodes q (ETC-091). It panics on a key longer than 65535 bytes, as
// kv.EncodeCommand does (ETC-080): the length is a u16, and the server's check in
// kv.EncodeCommand runs after the client has encoded the request.
func EncodeRequest(q Request) []byte {
	if len(q.Key) > 0xffff {
		panic(fmt.Sprintf("wire: key of %d bytes is longer than 65535", len(q.Key)))
	}
	b := []byte{TagRequest}
	b = binary.BigEndian.AppendUint32(b, q.Client)
	b = binary.BigEndian.AppendUint64(b, q.ID)
	b = binary.BigEndian.AppendUint32(b, q.Attempt)
	b = append(b, byte(q.Op))
	b = binary.BigEndian.AppendUint16(b, uint16(len(q.Key))) //nolint:gosec // checked above: at most 65535
	b = append(b, q.Key...)
	b = binary.BigEndian.AppendUint32(b, uint32(len(q.Value))) //nolint:gosec // a value is far below 4 GiB
	b = append(b, q.Value...)
	b = binary.BigEndian.AppendUint32(b, uint32(len(q.Data))) //nolint:gosec // a conf change is far below 4 GiB
	return append(b, q.Data...)
}

// DecodeRequest parses an encoding produced by EncodeRequest (ETC-091).
func DecodeRequest(b []byte) (Request, error) {
	r, err := open(b, TagRequest, "request")
	if err != nil {
		return Request{}, err
	}
	var q Request
	q.Client, q.ID, q.Attempt = r.u32(), r.u64(), r.u32()
	q.Op = kv.Op(r.u8())
	q.Key = string(r.take(int(r.u16())))
	q.Value = string(r.take(int(r.u32())))
	q.Data = r.bytes(int(r.u32()))
	if err := r.done("request"); err != nil {
		return Request{}, err
	}
	if q.Op < kv.OpPut || q.Op > kv.OpConfChange {
		return Request{}, fmt.Errorf("wire: bad request: op %d", q.Op)
	}
	return q, nil
}

// EncodeReply encodes p (ETC-091).
func EncodeReply(p Reply) []byte {
	b := []byte{TagReply}
	b = binary.BigEndian.AppendUint64(b, p.ID)
	b = binary.BigEndian.AppendUint32(b, p.Attempt)
	b = append(b, byte(p.Status))
	b = binary.BigEndian.AppendUint64(b, p.Leader)
	found := byte(0)
	if p.Found {
		found = 1
	}
	b = append(b, found)
	b = binary.BigEndian.AppendUint32(b, uint32(len(p.Value))) //nolint:gosec // a value is far below 4 GiB
	b = append(b, p.Value...)
	b = binary.BigEndian.AppendUint32(b, uint32(len(p.Data))) //nolint:gosec // a ConfState is far below 4 GiB
	return append(b, p.Data...)
}

// DecodeReply parses an encoding produced by EncodeReply (ETC-091).
func DecodeReply(b []byte) (Reply, error) {
	r, err := open(b, TagReply, "reply")
	if err != nil {
		return Reply{}, err
	}
	var p Reply
	p.ID, p.Attempt = r.u64(), r.u32()
	p.Status = Status(r.u8())
	p.Leader = r.u64()
	found := r.u8()
	p.Value = string(r.take(int(r.u32())))
	p.Data = r.bytes(int(r.u32()))
	if err := r.done("reply"); err != nil {
		return Reply{}, err
	}
	if p.Status > StatusDropped {
		return Reply{}, fmt.Errorf("wire: bad reply: status %d", p.Status)
	}
	if found > 1 {
		return Reply{}, fmt.Errorf("wire: bad reply: found flag %d", found)
	}
	p.Found = found == 1
	return p, nil
}

// EncodeSnapAck encodes the acknowledgement of the snapshot at index (ETC-091).
func EncodeSnapAck(index uint64) []byte {
	return binary.BigEndian.AppendUint64([]byte{TagSnapAck}, index)
}

// DecodeSnapAck parses an encoding produced by EncodeSnapAck (ETC-091).
func DecodeSnapAck(b []byte) (uint64, error) {
	r, err := open(b, TagSnapAck, "snapack")
	if err != nil {
		return 0, err
	}
	i := r.u64()
	if err := r.done("snapack"); err != nil {
		return 0, err
	}
	return i, nil
}

// badError is the error DecodeRaft returns for a message that does not unmarshal. Its text
// is fixed and never includes protobuf's error message: protobuf-go varies the spacing of
// its messages from one binary to the next on purpose, and harness errors are hashed
// (ETC-121). The protobuf error stays reachable through errors.Is and errors.As.
type badError struct {
	text  string
	cause error // the proto.Unmarshal error; not part of the text
}

func (e *badError) Error() string { return e.text }

func (e *badError) Unwrap() error { return e.cause }

type reader struct {
	b     []byte
	short bool
}

func open(b []byte, tag byte, what string) (*reader, error) {
	if len(b) == 0 {
		return nil, fmt.Errorf("wire: bad %s: empty", what)
	}
	if b[0] != tag {
		return nil, fmt.Errorf("wire: bad %s: tag %d", what, b[0])
	}
	return &reader{b: b[1:]}, nil
}

func (r *reader) done(what string) error {
	switch {
	case r.short:
		return fmt.Errorf("wire: bad %s: short buffer", what)
	case len(r.b) != 0:
		return fmt.Errorf("wire: bad %s: %d trailing bytes", what, len(r.b))
	}
	return nil
}

func (r *reader) take(n int) []byte {
	if r.short || n < 0 || len(r.b) < n {
		r.short = true
		return nil
	}
	out := r.b[:n]
	r.b = r.b[n:]
	return out
}

// bytes returns a copy of the next n bytes, or nil when n == 0.
func (r *reader) bytes(n int) []byte {
	b := r.take(n)
	if len(b) == 0 {
		return nil
	}
	return append([]byte(nil), b...)
}

func (r *reader) u8() uint8 {
	if b := r.take(1); b != nil {
		return b[0]
	}
	return 0
}

func (r *reader) u16() uint16 {
	if b := r.take(2); b != nil {
		return binary.BigEndian.Uint16(b)
	}
	return 0
}

func (r *reader) u32() uint32 {
	if b := r.take(4); b != nil {
		return binary.BigEndian.Uint32(b)
	}
	return 0
}

func (r *reader) u64() uint64 {
	if b := r.take(8); b != nil {
		return binary.BigEndian.Uint64(b)
	}
	return 0
}
