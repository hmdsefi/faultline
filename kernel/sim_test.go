package kernel

import (
	"encoding/hex"
	"errors"
	"testing"
)

// AT-KRN-03
func TestNewAndStartRecord(t *testing.T) {
	s := New(Config{Seed: 1})
	if s.Now() != 0 || s.Executed() != 0 || s.Err() != nil || s.Records() != nil || s.Cause() != 1 {
		t.Fatalf("New: Now=%v Executed=%d Err=%v Records=%v Cause=%d",
			s.Now(), s.Executed(), s.Err(), s.Records(), s.Cause())
	}
	if got := s.TraceHash(); got != 0x8627ec127688a847 {
		t.Errorf("TraceHash = %#016x, want 0x8627ec127688a847", got)
	}
	start := Record{Seq: 1, Kind: "kernel.start", Text: "start", Attrs: []Attr{
		{Key: "seed", Value: "0x0000000000000001"}, {Key: "tie_break", Value: "seeded"}}}
	const wantHex = "010000000c6b65726e656c2e73746172740005737461727402047365656412307830303030303030303030303030303031097469655f627265616b06736565646564"
	enc := AppendRecord(nil, start)
	if got := hex.EncodeToString(enc); got != wantHex || len(enc) != 66 {
		t.Errorf("AppendRecord(start) = %s (%d bytes), want %s (66 bytes)", got, len(enc), wantHex)
	}
	if got := New(Config{Seed: 0, TieBreak: TieBreakFIFO}).TraceHash(); got != 0xd9c216103e6cc0fe {
		t.Errorf("FIFO seed 0 TraceHash = %#016x, want 0xd9c216103e6cc0fe", got)
	}
	f := fullSim(1)
	checkRecords(t, f, `1 0.000000000s 0/0 kernel.start cause=0 "start" [seed=0x0000000000000001 tie_break=seeded]`)
	if f.TraceHash() != 0x8627ec127688a847 {
		t.Errorf("TraceFull hash differs: %#016x", f.TraceHash())
	}
}

// AT-KRN-04
func TestConfigValidation(t *testing.T) {
	mustPanic(t, "kernel: invalid Config.TieBreak 2", func() { New(Config{TieBreak: 2}) })
	mustPanic(t, "kernel: invalid Config.Trace.Level 2", func() { New(Config{Trace: TraceConfig{Level: 2}}) })
	mustPanic(t, "kernel: negative Config.Trace.Buffer -1", func() { New(Config{Trace: TraceConfig{Buffer: -1}}) })
	mustPanic(t, "kernel: negative Config.MaxTime -0.000000001s", func() { New(Config{MaxTime: -1}) })
	New(Config{Trace: TraceConfig{Level: TraceHash, Buffer: 5}})
}

// KRN-005
func TestConfigAccessors(t *testing.T) {
	cfg := Config{Seed: 9, MaxEvents: 3, MaxTime: 7, TieBreak: TieBreakFIFO, Trace: TraceConfig{Level: TraceFull, Buffer: 4}}
	s := New(cfg)
	if s.Config() != cfg || s.Seed() != 9 {
		t.Errorf("Config() = %+v, Seed() = %d", s.Config(), s.Seed())
	}
}

// AT-KRN-05 (Sim.Rand vector)
func TestSimRandVector(t *testing.T) {
	if got := New(Config{Seed: 1}).Rand("net/link/1/2").Uint64(); got != 0xf735ce98737a176b {
		t.Errorf("Rand(net/link/1/2).Uint64() = %#016x", got)
	}
}

// AT-KRN-06 (identity and reservation; the tie-break part is in loop_test.go)
func TestRandIdentityAndReservation(t *testing.T) {
	s := New(Config{Seed: 1})
	if s.Rand("x") != s.Rand("x") { //nolint:staticcheck // two calls must return the same pointer
		t.Error("Rand(x) returned different pointers")
	}
	if s.Rand("x") == s.Rand("y") {
		t.Error("Rand(x) == Rand(y)")
	}
	mustPanic(t, "kernel: empty stream label", func() { s.Rand("") })
	mustPanic(t, `kernel: stream label "kernel/sched" is reserved`, func() { s.Rand("kernel/sched") })
	mustPanic(t, `kernel: stream label "node/a/1" is reserved`, func() { s.Rand("node/a/1") })
}

// AT-KRN-37
func TestEncodingAndHashVectors(t *testing.T) {
	r := Record{Seq: 300, At: -2, Node: 3, Inc: 2, Kind: "net.send", Cause: 299, Text: "ping",
		Attrs: []Attr{{Key: "to", Value: "n2"}}}
	const wantHex = "ac02030602086e65742e73656e64ab020470696e670102746f026e32"
	if got := hex.EncodeToString(AppendRecord(nil, r)); got != wantHex {
		t.Errorf("AppendRecord = %s, want %s", got, wantHex)
	}
	start := Record{Seq: 1, Kind: "kernel.start", Text: "start", Attrs: []Attr{
		{Key: "seed", Value: "0x0000000000000001"}, {Key: "tie_break", Value: "seeded"}}}
	if got := HashRecords([]Record{start, r}); got != 0x3cf82149347fbe02 {
		t.Errorf("HashRecords = %#016x, want 0x3cf82149347fbe02", got)
	}
	if got := HashRecords(nil); got != 0xcbf29ce484222325 {
		t.Errorf("HashRecords(nil) = %#016x", got)
	}
	prefix := []byte{0xff}
	if got := AppendRecord(prefix, r); hex.EncodeToString(got) != "ff"+wantHex {
		t.Errorf("AppendRecord does not append to b: %x", got)
	}
}

// KRN-096, KRN-097: retention never changes the hash; a ring keeps the newest records.
func TestRetention(t *testing.T) {
	run := func(tc TraceConfig) *Sim {
		s := New(Config{Seed: 1, Trace: tc})
		for i := 0; i < 6; i++ {
			s.Logf("log %d", i)
		}
		return s
	}
	h := run(TraceConfig{}).TraceHash()
	full := run(TraceConfig{Level: TraceFull})
	ring := run(TraceConfig{Level: TraceFull, Buffer: 3})
	if full.TraceHash() != h || ring.TraceHash() != h {
		t.Fatalf("hash depends on retention: %#x %#x %#x", h, full.TraceHash(), ring.TraceHash())
	}
	if got := HashRecords(full.Records()); got != h {
		t.Errorf("HashRecords(Records()) = %#x, want %#x", got, h)
	}
	rs := ring.Records()
	if len(rs) != 3 || rs[0].Seq != 5 || rs[1].Seq != 6 || rs[2].Seq != 7 {
		t.Fatalf("ring records: %v", rs)
	}
	if rs[0].Text != "log 3" {
		t.Errorf("oldest kept record text %q", rs[0].Text)
	}
	if len(full.Records()) != 7 {
		t.Errorf("unbounded records: %d", len(full.Records()))
	}
}

// AT-KRN-35 (Sim.Logf; Node.Logf is in node_test.go)
func TestSimLogf(t *testing.T) {
	s := fullSim(1)
	s.Logf("g")
	s.Logf("x=%d", 5)
	checkRecords(t, s, `
1 0.000000000s 0/0 kernel.start cause=0 "start" [seed=0x0000000000000001 tie_break=seeded]
2 0.000000000s 0/0 kernel.log cause=1 "g" []
3 0.000000000s 0/0 kernel.log cause=2 "x=5" []
`)
}

// AT-KRN-43 (TieBreak and TraceLevel; StopReason and NodeState are tested with their types)
func TestConfigStrings(t *testing.T) {
	cases := []struct{ got, want string }{
		{TieBreakSeeded.String(), "seeded"},
		{TieBreakFIFO.String(), "fifo"},
		{TieBreak(7).String(), "TieBreak(7)"},
		{TraceHash.String(), "hash"},
		{TraceFull.String(), "full"},
		{TraceLevel(9).String(), "TraceLevel(9)"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

// AT-KRN-39 (the NodeID case)
func TestDescribeNodeID(t *testing.T) {
	if got := Describe(NodeID(3)); got != "3" {
		t.Errorf("Describe(NodeID(3)) = %q", got)
	}
}

// KRN-041 (the loop part of AT-KRN-18 is in loop_test.go)
func TestFailRecordsFirstError(t *testing.T) {
	s := fullSim(7)
	errBad, errOther := errors.New("bad"), errors.New("other")
	mustPanic(t, "kernel: Fail called with a nil error", func() { s.Fail(nil) })
	s.Fail(errBad)
	h := s.TraceHash()
	s.Fail(errOther)
	if s.Err() != errBad || s.TraceHash() != h {
		t.Fatalf("Err = %v, hash changed %v", s.Err(), s.TraceHash() != h)
	}
	checkRecords(t, s, `
1 0.000000000s 0/0 kernel.start cause=0 "start" [seed=0x0000000000000007 tie_break=seeded]
2 0.000000000s 0/0 kernel.fail cause=1 "bad" []
`)
}
