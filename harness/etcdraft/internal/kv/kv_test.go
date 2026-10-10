package kv

import (
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
)

// AT-ETC-09
func TestGoldenVectors(t *testing.T) {
	put := Command{Client: 1, ID: 2, Op: OpPut, Key: "k1", Value: "v"}
	if got := hex.EncodeToString(EncodeCommand(put)); got != "010100000001000000000000000200026b310000000176" {
		t.Fatalf("EncodeCommand = %s", got)
	}
	s := New()
	if got := hex.EncodeToString(s.Encode()); got != "010000000000000000" {
		t.Fatalf("New().Encode() = %s", got)
	}
	if got := s.Hash(); got != 0x529a2cdc8ff533ac {
		t.Fatalf("New().Hash() = %#x", got)
	}
	if _, dup := s.Apply(put); dup {
		t.Fatal("first put reported as duplicate")
	}
	if got := hex.EncodeToString(s.Encode()); got != "010000000100026b31000000017600000001000000010000000000000002010000000000" {
		t.Fatalf("one-put Encode() = %s", got)
	}
	if got := s.Hash(); got != 0x9377d6d1dad603af {
		t.Fatalf("one-put Hash() = %#x", got)
	}
}

// AT-ETC-10
func TestApplySessions(t *testing.T) {
	s := New()
	steps := []struct {
		c       Command
		wantRes Result
		wantDup bool
	}{
		{Command{Client: 1, ID: 1, Op: OpPut, Key: "k", Value: "v1"}, Result{}, false},
		{Command{Client: 1, ID: 2, Op: OpGet, Key: "k"}, Result{Found: true, Value: "v1"}, false},
		{Command{Client: 2, ID: 1, Op: OpPut, Key: "k", Value: "v2"}, Result{}, false},
		{Command{Client: 1, ID: 2, Op: OpGet, Key: "k"}, Result{Found: true, Value: "v1"}, true}, // cached, not v2
		{Command{Client: 1, ID: 1, Op: OpPut, Key: "k", Value: "v1"}, Result{}, true},
		{Command{Client: 1, ID: 3, Op: OpGet, Key: "missing"}, Result{}, false},
		{Command{Client: 1, ID: 5, Op: OpConfRead}, Result{}, false}, // IDs may skip values
		{Command{Client: 1, ID: 4, Op: OpGet, Key: "k"}, Result{}, true},
	}
	for i, st := range steps {
		res, dup := s.Apply(st.c)
		if res != st.wantRes || dup != st.wantDup {
			t.Fatalf("step %d: Apply(%+v) = %+v, %v; want %+v, %v", i, st.c, res, dup, st.wantRes, st.wantDup)
		}
	}
	if v, ok := s.Get("k"); !ok || v != "v2" {
		t.Fatalf("Get(k) = %q, %v; want v2, true", v, ok)
	}
	if s.ApplySession(1, 5, OpConfChange) != true || s.ApplySession(1, 6, OpConfChange) != false || s.ApplySession(1, 6, OpConfChange) != true {
		t.Fatal("ApplySession duplicate detection wrong")
	}
	if s.ApplySession(9, 1, OpConfChange) {
		t.Fatal("ApplySession of a new client reported duplicate")
	}
}

func TestStateRoundTrip(t *testing.T) {
	s := New()
	for i := 0; i < 20; i++ {
		s.Apply(Command{Client: uint32(i%3 + 1), ID: uint64(i + 1), Op: OpPut, Key: "k" + strconv.Itoa(i%7), Value: "v" + strconv.Itoa(i)})
	}
	s.Apply(Command{Client: 9, ID: 1, Op: OpGet, Key: "k1"})
	s.ApplySession(10, 4, OpConfChange)
	d, err := Decode(s.Encode())
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if d.Hash() != s.Hash() || hex.EncodeToString(d.Encode()) != hex.EncodeToString(s.Encode()) {
		t.Fatal("round trip changed the encoding")
	}
	if res, dup := d.Apply(Command{Client: 9, ID: 1, Op: OpGet, Key: "k1"}); !dup || !res.Found {
		t.Fatalf("decoded session lost the cached read: %+v %v", res, dup)
	}
}

func TestDecodeRejects(t *testing.T) {
	good := EncodeCommand(Command{Client: 1, ID: 2, Op: OpGet, Key: "k"})
	cases := map[string][]byte{
		"short buffer":   good[:len(good)-1],
		"version 2":      append([]byte{2}, good[1:]...),
		"op 4":           append([]byte{1, 4}, good[2:]...),
		"1 trailing":     append(append([]byte(nil), good...), 0),
		"value for op 2": EncodeCommand(Command{Client: 1, ID: 2, Op: OpGet, Key: "k", Value: "x"}),
		"value for op 3": EncodeCommand(Command{Client: 1, ID: 2, Op: OpConfRead, Value: "x"}),
	}
	for want, b := range cases {
		_, err := DecodeCommand(b)
		if err == nil || !strings.HasPrefix(err.Error(), "kv: bad command: ") || !strings.Contains(err.Error(), want) {
			t.Errorf("DecodeCommand(%s case) error = %v", want, err)
		}
	}
	state := New()
	state.Apply(Command{Client: 1, ID: 1, Op: OpPut, Key: "a", Value: "1"})
	state.Apply(Command{Client: 1, ID: 2, Op: OpPut, Key: "b", Value: "2"})
	enc := state.Encode()
	// Swap the two keys so they are out of order: "a"/"1" and "b"/"2" have equal sizes.
	swapped := append([]byte(nil), enc...)
	first := 5 // after version and nKeys
	recLen := 2 + 1 + 4 + 1
	copy(swapped[first:], enc[first+recLen:first+2*recLen])
	copy(swapped[first+recLen:], enc[first:first+recLen])
	for name, b := range map[string][]byte{
		"short buffer": enc[:len(enc)-1],
		"trailing":     append(append([]byte(nil), enc...), 0),
		"out of order": swapped,
		"version":      append([]byte{9}, enc[1:]...),
	} {
		if _, err := Decode(b); err == nil || !strings.HasPrefix(err.Error(), "kv: bad state: ") {
			t.Errorf("Decode(%s) error = %v", name, err)
		}
	}
	if c, id, err := DecodeContext(EncodeContext(7, 99)); err != nil || c != 7 || id != 99 {
		t.Fatalf("context round trip = %d, %d, %v", c, id, err)
	}
	if _, _, err := DecodeContext([]byte{1, 2}); err == nil {
		t.Fatal("short context accepted")
	}
}

// ETC-080: EncodeCommand panics on a key longer than 65 535 bytes and accepts one of exactly 65 535.
func TestEncodeCommandKeyLimit(t *testing.T) {
	longest := Command{Client: 1, ID: 1, Op: OpGet, Key: strings.Repeat("k", 0xffff)}
	if got, err := DecodeCommand(EncodeCommand(longest)); err != nil || got != longest {
		t.Fatalf("a key of 65535 bytes: round trip failed: %v", err)
	}
	want := "kv: key of 65536 bytes is longer than 65535"
	var got any
	func() {
		defer func() { got = recover() }()
		EncodeCommand(Command{Client: 1, ID: 1, Op: OpGet, Key: strings.Repeat("k", 0x10000)})
	}()
	if got != want {
		t.Fatalf("EncodeCommand of a 65536-byte key: panic = %v, want %q", got, want)
	}
}

// ETC-080, ETC-067: every committed command is decoded with DecodeCommand, so each field
// must come back as it went in.
func TestCommandRoundTrip(t *testing.T) {
	for _, c := range []Command{
		{Client: 7, ID: 1<<40 + 3, Op: OpPut, Key: "key", Value: "value"},
		{Client: 1<<32 - 1, ID: 1<<64 - 1, Op: OpPut, Key: "", Value: ""},
		{Client: 2, ID: 9, Op: OpGet, Key: "k0"},
		{Client: 3, ID: 10, Op: OpConfRead},
	} {
		got, err := DecodeCommand(EncodeCommand(c))
		if err != nil || got != c {
			t.Errorf("round trip of %+v = %+v, %v", c, got, err)
		}
	}
}

// ETC-080: DecodeContext rejects a short buffer, a wrong version and trailing bytes, with the text kv: bad context: <reason>.
func TestDecodeContextRejects(t *testing.T) {
	good := EncodeContext(7, 99)
	cases := []struct {
		name string
		b    []byte
		want string
	}{
		{"empty", nil, "kv: bad context: short buffer"},
		{"short", good[:len(good)-1], "kv: bad context: short buffer"},
		{"version 2", append([]byte{2}, good[1:]...), "kv: bad context: version 2"},
		{"trailing", append(append([]byte(nil), good...), 0), "kv: bad context: 1 trailing bytes"},
	}
	for _, c := range cases {
		if _, _, err := DecodeContext(c.b); err == nil || err.Error() != c.want {
			t.Errorf("%s: error = %v, want %q", c.name, err, c.want)
		}
	}
}

// ETC-081: the session rules that a test with ascending IDs and one cached read cannot tell apart.
func TestSessionRules(t *testing.T) {
	apply := func(s *State, c Command, wantRes Result, wantDup bool) {
		t.Helper()
		if res, dup := s.Apply(c); res != wantRes || dup != wantDup {
			t.Fatalf("Apply(%+v) = %+v, %v; want %+v, %v", c, res, dup, wantRes, wantDup)
		}
	}
	found := Result{Found: true, Value: "v"}
	t.Run("a duplicate get is cached only at the last ID", func(t *testing.T) {
		s := New()
		apply(s, Command{Client: 1, ID: 1, Op: OpPut, Key: "k", Value: "v"}, Result{}, false)
		apply(s, Command{Client: 1, ID: 5, Op: OpGet, Key: "k"}, found, false)
		apply(s, Command{Client: 1, ID: 5, Op: OpGet, Key: "k"}, found, true)
		apply(s, Command{Client: 1, ID: 4, Op: OpGet, Key: "k"}, Result{}, true) // older than the last ID: no cache
	})
	t.Run("a non-get clears the cached result", func(t *testing.T) {
		s := New()
		apply(s, Command{Client: 1, ID: 1, Op: OpPut, Key: "k", Value: "v"}, Result{}, false)
		apply(s, Command{Client: 1, ID: 2, Op: OpGet, Key: "k"}, found, false)
		apply(s, Command{Client: 1, ID: 3, Op: OpPut, Key: "k", Value: "w"}, Result{}, false)
		apply(s, Command{Client: 1, ID: 3, Op: OpGet, Key: "k"}, Result{}, true) // the put is last; nothing cached
	})
	t.Run("a duplicate changes nothing", func(t *testing.T) {
		s := New()
		apply(s, Command{Client: 1, ID: 2, Op: OpPut, Key: "k", Value: "v"}, Result{}, false)
		before := hex.EncodeToString(s.Encode())
		apply(s, Command{Client: 1, ID: 2, Op: OpPut, Key: "other", Value: "x"}, Result{}, true)
		apply(s, Command{Client: 1, ID: 1, Op: OpPut, Key: "k", Value: "y"}, Result{}, true)
		if hex.EncodeToString(s.Encode()) != before {
			t.Fatal("a duplicate changed the state")
		}
	})
	t.Run("ApplySession sets the session to {id, OpConfChange, zero}", func(t *testing.T) {
		s := New()
		apply(s, Command{Client: 1, ID: 2, Op: OpGet, Key: "k"}, Result{}, false)
		apply(s, Command{Client: 1, ID: 3, Op: OpPut, Key: "k", Value: "v"}, Result{}, false)
		apply(s, Command{Client: 1, ID: 4, Op: OpGet, Key: "k"}, found, false) // a cached result to clear
		if s.ApplySession(1, 7, OpConfChange) {
			t.Fatal("ApplySession reported a duplicate")
		}
		if got, want := *s.sessions[1], (session{last: 7, lastOp: OpConfChange}); got != want {
			t.Fatalf("session = %+v, want %+v", got, want)
		}
		apply(s, Command{Client: 1, ID: 7, Op: OpGet, Key: "k"}, Result{}, true) // nothing cached
		apply(s, Command{Client: 1, ID: 8, Op: OpGet, Key: "k"}, found, false)
	})
}

// ETC-081: "initially zero": a client with no session has last = 0, so its ID 0 is a
// duplicate and creates no session. Workload IDs start at 1.
func TestApplyIDZero(t *testing.T) {
	s := New()
	empty := hex.EncodeToString(s.Encode())
	for _, op := range []Op{OpPut, OpGet, OpConfRead} {
		if res, dup := s.Apply(Command{Client: 1, ID: 0, Op: op, Key: "k", Value: "v"}); res != (Result{}) || !dup {
			t.Fatalf("Apply(ID 0, op %d) = %+v, %v; want the zero Result and dup", op, res, dup)
		}
	}
	if !s.ApplySession(1, 0, OpConfChange) {
		t.Fatal("ApplySession(ID 0) of a client with no session was not a duplicate")
	}
	if _, found := s.Get("k"); found || hex.EncodeToString(s.Encode()) != empty {
		t.Fatal("ID 0 changed the state")
	}
	if _, dup := s.Apply(Command{Client: 1, ID: 1, Op: OpPut, Key: "k", Value: "v"}); dup {
		t.Fatal("ID 1 of a new client was a duplicate")
	}
}

// ETC-082: keys and clients are encoded in ascending order whatever order they arrive in; a
// snapshot with another order would not Decode.
func TestEncodeSortsKeysAndClients(t *testing.T) {
	cmds := []Command{
		{Client: 3, ID: 1, Op: OpPut, Key: "b", Value: "2"},
		{Client: 1, ID: 1, Op: OpPut, Key: "c", Value: "3"},
		{Client: 2, ID: 1, Op: OpPut, Key: "a", Value: "1"},
	}
	const golden = "01" + "00000003" +
		"0001" + "61" + "00000001" + "31" +
		"0001" + "62" + "00000001" + "32" +
		"0001" + "63" + "00000001" + "33" +
		"00000003" +
		"00000001" + "0000000000000001" + "01" + "00" + "00000000" +
		"00000002" + "0000000000000001" + "01" + "00" + "00000000" +
		"00000003" + "0000000000000001" + "01" + "00" + "00000000"
	for _, order := range [][]int{{0, 1, 2}, {2, 1, 0}, {1, 0, 2}} {
		s := New()
		for _, i := range order {
			s.Apply(cmds[i])
		}
		enc := s.Encode()
		if got := hex.EncodeToString(enc); got != golden {
			t.Errorf("order %v: Encode = %s, want %s", order, got, golden)
		}
		if _, err := Decode(enc); err != nil {
			t.Errorf("order %v: Decode(Encode) = %v", order, err)
		}
	}
}

// ETC-082: Decode rejects each malformed state with its own reason.
func TestDecodeStateRejects(t *testing.T) {
	two := New() // keys a, b and clients 1, 2: equal-sized records that can be swapped or overwritten
	two.Apply(Command{Client: 1, ID: 1, Op: OpPut, Key: "a", Value: "1"})
	two.Apply(Command{Client: 2, ID: 1, Op: OpPut, Key: "b", Value: "2"})
	enc := two.Encode()
	const keyRec = 2 + 1 + 4 + 1              // u16 len, key, u32 len, value
	const sessRec = 4 + 8 + 1 + 1 + 4         // u32 client, u64 last, u8 lastOp, u8 found, u32 len, no value
	keys := 1 + 4                             // version, nKeys
	sessions := keys + 2*keyRec + 4           // after nSessions
	mutate := func(f func(b []byte)) []byte { // a modified copy of enc
		b := append([]byte(nil), enc...)
		f(b)
		return b
	}
	cases := []struct {
		name string
		b    []byte
		want string
	}{
		{"empty", nil, "kv: bad state: short buffer"},
		{"short", enc[:len(enc)-1], "kv: bad state: short buffer"},
		{"trailing", append(append([]byte(nil), enc...), 0), "kv: bad state: 1 trailing bytes"},
		{"version 9", append([]byte{9}, enc[1:]...), "kv: bad state: version 9"},
		{"keys out of order", mutate(func(b []byte) {
			copy(b[keys:], enc[keys+keyRec:keys+2*keyRec])
			copy(b[keys+keyRec:], enc[keys:keys+keyRec])
		}), `kv: bad state: key "a" out of order`},
		{"duplicate key", mutate(func(b []byte) { b[keys+keyRec+2] = 'a' }), `kv: bad state: key "a" out of order`},
		{"clients out of order", mutate(func(b []byte) {
			copy(b[sessions:], enc[sessions+sessRec:sessions+2*sessRec])
			copy(b[sessions+sessRec:], enc[sessions:sessions+sessRec])
		}), "kv: bad state: client 1 out of order"},
		{"duplicate client", mutate(func(b []byte) { b[sessions+sessRec+3] = 1 }), "kv: bad state: client 1 out of order"},
		{"found flag 2", mutate(func(b []byte) { b[sessions+4+8+1] = 2 }), "kv: bad state: found flag 2"},
	}
	for _, c := range cases {
		if _, err := Decode(c.b); err == nil || err.Error() != c.want {
			t.Errorf("%s: error = %v, want %q", c.name, err, c.want)
		}
	}
	if _, err := Decode(enc); err != nil {
		t.Fatalf("the unmodified state does not decode: %v", err)
	}
}

func BenchmarkKVEncode(b *testing.B) {
	s := New()
	for i := 0; i < 64; i++ {
		s.Apply(Command{Client: uint32(i%3 + 1), ID: uint64(i + 1), Op: OpPut, Key: "k" + strconv.Itoa(i), Value: "c1-" + strconv.Itoa(i)})
	}
	for b.Loop() {
		_ = s.Encode()
	}
}
