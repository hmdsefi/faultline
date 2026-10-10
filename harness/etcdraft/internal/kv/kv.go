// Package kv is the replicated KV state machine and its command encoding.
package kv

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"slices"
)

// Op is a client operation code.
type Op uint8

// The operation codes. OpConfRead and OpConfChange are admin operations; the workload
// clients send OpPut and OpGet.
const (
	OpPut        Op = 1
	OpGet        Op = 2
	OpConfRead   Op = 3 // admin: read the configuration through the log
	OpConfChange Op = 4 // admin: conf change (session bookkeeping only; never in EntryNormal data)
)

const version = 0x01

// Command is the data of a non-empty EntryNormal entry.
type Command struct {
	Client uint32
	ID     uint64 // per-client operation ID, starting at 1
	Op     Op     // OpPut, OpGet, or OpConfRead
	Key    string
	Value  string // OpPut only
}

// EncodeCommand encodes c per ETC-080. It panics on a key longer than 65535 bytes.
func EncodeCommand(c Command) []byte {
	if len(c.Key) > 0xffff {
		panic(fmt.Sprintf("kv: key of %d bytes is longer than 65535", len(c.Key)))
	}
	b := make([]byte, 0, 20+len(c.Key)+len(c.Value))
	b = append(b, version, byte(c.Op))
	b = binary.BigEndian.AppendUint32(b, c.Client)
	b = binary.BigEndian.AppendUint64(b, c.ID)
	b = binary.BigEndian.AppendUint16(b, uint16(len(c.Key))) //nolint:gosec // checked above: at most 65535
	b = append(b, c.Key...)
	b = binary.BigEndian.AppendUint32(b, uint32(len(c.Value))) //nolint:gosec // a value is far below 4 GiB
	return append(b, c.Value...)
}

// DecodeCommand parses an encoding produced by EncodeCommand (ETC-080).
func DecodeCommand(b []byte) (Command, error) {
	bad := func(reason string) (Command, error) {
		return Command{}, errors.New("kv: bad command: " + reason)
	}
	r := reader{b: b}
	v, op := r.u8(), Op(r.u8())
	client, id := r.u32(), r.u64()
	key := r.bytes(int(r.u16()))
	value := r.bytes(int(r.u32()))
	switch {
	case r.short:
		return bad("short buffer")
	case v != version:
		return bad(fmt.Sprintf("version %d", v))
	case op != OpPut && op != OpGet && op != OpConfRead:
		return bad(fmt.Sprintf("op %d", op))
	case len(r.b) != 0:
		return bad(fmt.Sprintf("%d trailing bytes", len(r.b)))
	case op != OpPut && len(value) != 0:
		return bad(fmt.Sprintf("value for op %d", op))
	}
	return Command{Client: client, ID: id, Op: op, Key: string(key), Value: string(value)}, nil
}

// EncodeContext encodes ConfChangeV2.Context (client, op ID):
// 0x01 · u32 client · u64 id, big-endian.
func EncodeContext(client uint32, id uint64) []byte {
	b := make([]byte, 0, 13)
	b = append(b, version)
	b = binary.BigEndian.AppendUint32(b, client)
	return binary.BigEndian.AppendUint64(b, id)
}

// DecodeContext parses an encoding produced by EncodeContext (ETC-080).
func DecodeContext(b []byte) (client uint32, id uint64, err error) {
	r := reader{b: b}
	v := r.u8()
	client, id = r.u32(), r.u64()
	switch {
	case r.short:
		return 0, 0, errors.New("kv: bad context: short buffer")
	case v != version:
		return 0, 0, fmt.Errorf("kv: bad context: version %d", v)
	case len(r.b) != 0:
		return 0, 0, fmt.Errorf("kv: bad context: %d trailing bytes", len(r.b))
	}
	return client, id, nil
}

// Result is the outcome of an operation.
type Result struct {
	Found bool
	Value string
}

type session struct {
	last   uint64
	lastOp Op
	cached Result
}

// State is the KV map plus one session per client.
type State struct {
	data     map[string]string
	keys     []string // sorted keys of data
	sessions map[uint32]*session
	clients  []uint32 // sorted keys of sessions
}

// New returns an empty state: no keys and no sessions.
func New() *State {
	return &State{data: map[string]string{}, sessions: map[uint32]*session{}}
}

// lookup returns a copy of the client's session. A client with no session yet has the zero
// session (ETC-081: "initially zero"), so its ID 0 is a duplicate.
func (s *State) lookup(client uint32) session {
	if ss, ok := s.sessions[client]; ok {
		return *ss
	}
	return session{}
}

// session returns the client's session, creating it (and its place in clients) if needed.
func (s *State) session(client uint32) *session {
	if ss, ok := s.sessions[client]; ok {
		return ss
	}
	ss := &session{}
	s.sessions[client] = ss
	i, _ := slices.BinarySearch(s.clients, client)
	s.clients = slices.Insert(s.clients, i, client)
	return ss
}

// Apply applies c. dup is true if c.ID <= the client's last applied ID (0 for a client
// with no session, so ID 0 is always a duplicate); a duplicate has no effect. For a duplicate with c.ID == last ID and c.Op == OpGet, res is the
// cached result; otherwise res is the zero Result for duplicates.
func (s *State) Apply(c Command) (res Result, dup bool) {
	if ss := s.lookup(c.Client); c.ID <= ss.last {
		if c.ID == ss.last && c.Op == OpGet {
			return ss.cached, true
		}
		return Result{}, true
	}
	switch c.Op {
	case OpPut:
		if _, ok := s.data[c.Key]; !ok {
			i, _ := slices.BinarySearch(s.keys, c.Key)
			s.keys = slices.Insert(s.keys, i, c.Key)
		}
		s.data[c.Key] = c.Value
	case OpGet:
		v, ok := s.data[c.Key]
		res = Result{Found: ok, Value: v}
	}
	ss := s.session(c.Client)
	ss.last, ss.lastOp, ss.cached = c.ID, c.Op, Result{}
	if c.Op == OpGet {
		ss.cached = res
	}
	return res, false
}

// ApplySession records a session-only operation (a conf change from client) and
// reports whether it is a duplicate.
func (s *State) ApplySession(client uint32, id uint64, op Op) (dup bool) {
	if id <= s.lookup(client).last {
		return true
	}
	ss := s.session(client)
	ss.last, ss.lastOp, ss.cached = id, op, Result{}
	return false
}

// Get returns the value stored under key, read without going through a session.
func (s *State) Get(key string) (value string, found bool) {
	value, found = s.data[key]
	return value, found
}

// Encode returns the canonical encoding (ETC-082); it is the snapshot data.
func (s *State) Encode() []byte {
	b := []byte{version}
	b = binary.BigEndian.AppendUint32(b, uint32(len(s.keys))) //nolint:gosec // the key count is far below 2^32
	for _, k := range s.keys {
		v := s.data[k]
		b = binary.BigEndian.AppendUint16(b, uint16(len(k))) //nolint:gosec // keys come from DecodeCommand, whose key length is a u16
		b = append(b, k...)
		b = binary.BigEndian.AppendUint32(b, uint32(len(v))) //nolint:gosec // a value is far below 4 GiB
		b = append(b, v...)
	}
	b = binary.BigEndian.AppendUint32(b, uint32(len(s.clients))) //nolint:gosec // the client count is far below 2^32
	for _, c := range s.clients {
		ss := s.sessions[c]
		b = binary.BigEndian.AppendUint32(b, c)
		b = binary.BigEndian.AppendUint64(b, ss.last)
		found := byte(0)
		if ss.cached.Found {
			found = 1
		}
		b = append(b, byte(ss.lastOp), found)
		b = binary.BigEndian.AppendUint32(b, uint32(len(ss.cached.Value))) //nolint:gosec // a value is far below 4 GiB
		b = append(b, ss.cached.Value...)
	}
	return b
}

// Decode parses an encoding produced by Encode.
func Decode(b []byte) (*State, error) {
	bad := func(reason string) (*State, error) {
		return nil, errors.New("kv: bad state: " + reason)
	}
	r := reader{b: b}
	if v := r.u8(); !r.short && v != version {
		return bad(fmt.Sprintf("version %d", v))
	}
	s := New()
	n := r.u32()
	for i := uint32(0); i < n && !r.short; i++ {
		k := string(r.bytes(int(r.u16())))
		v := string(r.bytes(int(r.u32())))
		if r.short {
			break
		}
		if len(s.keys) > 0 && k <= s.keys[len(s.keys)-1] {
			return bad(fmt.Sprintf("key %q out of order", k))
		}
		s.keys = append(s.keys, k)
		s.data[k] = v
	}
	m := r.u32()
	for i := uint32(0); i < m && !r.short; i++ {
		c, last := r.u32(), r.u64()
		op, found := Op(r.u8()), r.u8()
		v := string(r.bytes(int(r.u32())))
		if r.short {
			break
		}
		if len(s.clients) > 0 && c <= s.clients[len(s.clients)-1] {
			return bad(fmt.Sprintf("client %d out of order", c))
		}
		if found > 1 {
			return bad(fmt.Sprintf("found flag %d", found))
		}
		s.clients = append(s.clients, c)
		s.sessions[c] = &session{last: last, lastOp: op, cached: Result{Found: found == 1, Value: v}}
	}
	switch {
	case r.short:
		return bad("short buffer")
	case len(r.b) != 0:
		return bad(fmt.Sprintf("%d trailing bytes", len(r.b)))
	}
	return s, nil
}

// Hash returns FNV-1a 64 of Encode().
func (s *State) Hash() uint64 {
	h := fnv.New64a()
	h.Write(s.Encode())
	return h.Sum64()
}

// reader consumes big-endian fields; once a read runs past the end, short is set and
// every later read returns zero values.
type reader struct {
	b     []byte
	short bool
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

func (r *reader) bytes(n int) []byte { return r.take(n) }
