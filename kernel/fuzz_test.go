// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package kernel

import (
	"encoding/binary"
	"errors"
	"hash/fnv"
	"math"
	"math/big"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// qmEntry is a pending entry of the FuzzQueueOrder model.
type qmEntry struct {
	id      EventID
	at      Time
	tb, seq uint64
	node    bool // bound to node n
}

// queueModel predicts the kernel's queue (KRN-022, KRN-030, KRN-036 step 6, KRN-063).
type queueModel struct {
	sched    *rand.Rand // nil under FIFO
	seq      uint64
	now      Time
	paused   bool
	pending  []*qmEntry
	deferred []*qmEntry
	defers   []EventID // deferral order
	executed uint64
}

func (m *queueModel) tieBreak(front bool) uint64 {
	if front || m.sched == nil {
		return 0
	}
	return m.sched.Uint64()
}

func (m *queueModel) add(id EventID, at Time, front, node bool) {
	m.seq++
	m.pending = append(m.pending, &qmEntry{id: id, at: at, tb: m.tieBreak(front), seq: m.seq, node: node})
}

// step mirrors Sim.Step: it defers entries of the paused node and returns the executed entry.
func (m *queueModel) step() *qmEntry {
	for len(m.pending) > 0 {
		i := 0
		for j, e := range m.pending {
			b := m.pending[i]
			if e.at < b.at || (e.at == b.at && (e.tb < b.tb || (e.tb == b.tb && e.seq < b.seq))) {
				i = j
			}
		}
		e := m.pending[i]
		m.pending = slices.Delete(m.pending, i, i+1)
		m.now = e.at
		if e.node && m.paused {
			m.deferred = append(m.deferred, e)
			m.defers = append(m.defers, e.id)
			continue
		}
		m.executed++
		return e
	}
	return nil
}

// cancel mirrors Sim.Cancel for pending and deferred entries.
func (m *queueModel) cancel(id EventID) bool {
	for _, list := range []*[]*qmEntry{&m.pending, &m.deferred} {
		if i := slices.IndexFunc(*list, func(e *qmEntry) bool { return e.id == id }); i >= 0 {
			*list = slices.Delete(*list, i, i+1)
			return true
		}
	}
	return false
}

// resume mirrors the resume procedure (KRN-063).
func (m *queueModel) resume() {
	m.paused = false
	for _, e := range m.deferred {
		m.seq++
		e.at, e.tb, e.seq = m.now, 0, m.seq
		m.pending = append(m.pending, e)
	}
	m.deferred = nil
}

// AT-KRN-46, DET-060
func FuzzQueueOrder(f *testing.F) {
	f.Add(uint64(1), false, []byte(nil))
	f.Add(uint64(1), true, []byte{0, 0, 5, 0, 0, 5, 2, 0, 0})
	f.Add(uint64(7), false, []byte{0, 1, 0, 3, 0, 0, 1, 0, 0, 2, 0, 0, 0, 0, 1})
	mixed := []byte{6, 0, 5, 7, 0, 0, 0, 0, 9, 2, 0, 0, 4, 0, 0, 3, 0, 0, 7, 0, 0, 2, 0, 0, 2, 0, 0, 2, 0, 0}
	f.Add(uint64(3), false, mixed)
	f.Add(uint64(3), true, mixed)
	twice := []byte{6, 0, 5, 7, 0, 0, 2, 0, 0, 7, 0, 0, 6, 0, 5, 7, 0, 0, 2, 0, 0, 7, 0, 0, 3, 0, 0, 3, 0, 0}
	f.Add(uint64(1), false, twice)
	f.Add(uint64(5), false, twice) // one kernel/sched draw per Resume call, or per resumed entry, reorders this seed
	cancelDeferred := []byte{6, 0, 5, 7, 0, 0, 2, 0, 0, 1, 0, 0}
	f.Add(uint64(1), false, cancelDeferred)
	f.Fuzz(func(t *testing.T, seed uint64, fifo bool, ops []byte) {
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d fifo %v ops %x: "+format, append([]any{seed, fifo, ops}, args...)...)
		}
		cfg := Config{Seed: seed, Trace: TraceConfig{Level: TraceFull}}
		m := &queueModel{}
		if fifo {
			cfg.TieBreak = TieBreakFIFO
		} else {
			m.sched = rand.New(rand.NewPCG(streamSeeds(seed, "kernel/sched"))) //nolint:gosec // the model replays the kernel's seeded sched stream
		}
		s := New(cfg)
		n := s.AddNode("n", func(*Node) {})
		m.add(1, 0, false, false) // the initial boot event
		var ran []EventID
		ranAt := map[EventID]Time{}
		cb := func(id *EventID) func() {
			return func() {
				ran = append(ran, *id)
				ranAt[*id] = s.Now()
			}
		}
		check := func(e *qmEntry, stepped bool) {
			t.Helper()
			if (e != nil) != stepped {
				fail("Step = %v, model executed %v", stepped, e)
			}
			if e != nil && e.id != 1 {
				last := EventID(0)
				if len(ran) > 0 {
					last = ran[len(ran)-1]
				}
				if last != e.id || ranAt[last] != e.at {
					fail("executed %v, the last ran id %d at %v; model expects id %d at %v", ran, last, ranAt[last], e.id, e.at)
				}
			}
			if s.Now() != m.now || s.Executed() != m.executed {
				fail("Now %v Executed %d, model %v %d", s.Now(), s.Executed(), m.now, m.executed)
			}
		}
		check(m.step(), s.Step())
		var ids []EventID
		lastAt := s.Now()
		schedule := func(at Time, front, node bool) {
			id := new(EventID)
			switch {
			case node:
				*id = n.After(time.Duration(at-s.Now()), "n", cb(id))
			case front:
				*id = s.AtFront(at, "f", cb(id))
			default:
				*id = s.At(at, "e", cb(id))
			}
			if want := EventID(len(ids) + 2); *id != want {
				fail("EventID %d, want %d", *id, want)
			}
			ids = append(ids, *id)
			lastAt = at
			m.add(*id, at, front, node)
		}
		atOrNow := func() Time { return max(lastAt, s.Now()) }
		for g := 0; g+3 <= len(ops) && g < 3*512; g += 3 {
			b0, b1, b2 := ops[g], ops[g+1], ops[g+2]
			off := Time((uint16(b1)<<8 + uint16(b2)) % 1000)
			switch b0 % 8 {
			case 0:
				schedule(s.Now()+off, false, false)
			case 1:
				if len(ids) > 0 {
					id := ids[int(b1)%len(ids)]
					if got, want := s.Cancel(id), m.cancel(id); got != want {
						fail("Cancel(%d) = %v, model %v", id, got, want)
					}
				}
			case 2:
				check(m.step(), s.Step())
			case 3:
				schedule(atOrNow(), false, false)
			case 4:
				schedule(s.Now()+off, true, false)
			case 5:
				schedule(atOrNow(), true, false)
			case 6:
				schedule(s.Now()+off, false, true)
			case 7:
				if n.State() == NodeUp {
					n.Pause()
					m.paused = true
				} else {
					n.Resume()
					m.resume()
				}
			}
		}
		if n.State() == NodePaused {
			n.Resume()
			m.resume()
		}
		var want []*qmEntry
		for e := m.step(); e != nil; e = m.step() {
			want = append(want, e)
		}
		before := len(ran)
		if r := s.Run(); r != StopIdle {
			fail("Run = %v", r)
		}
		got := ran[before:]
		if len(got) != len(want) {
			fail("Run executed %v, model expects %d events", got, len(want))
		}
		for i, e := range want {
			if g := got[i]; g != e.id || ranAt[g] != e.at {
				fail("Run executed %v; event %d is id %d at %v, model expects id %d at %v", got, i+1, g, ranAt[g], e.id, e.at)
			}
		}
		if s.Now() != m.now || s.Executed() != m.executed {
			fail("Now %v Executed %d, model %v %d", s.Now(), s.Executed(), m.now, m.executed)
		}
		var defers []EventID
		var last Time
		for _, r := range s.Records() {
			if r.At < last {
				fail("record %d goes back in time", r.Seq)
			}
			last = r.At
			if r.Kind == "kernel.defer" {
				defers = append(defers, idAttr(t, r))
			}
		}
		if !slices.Equal(defers, m.defers) {
			fail("kernel.defer ids %v, model %v", defers, m.defers)
		}
		if h, want := s.TraceHash(), HashRecords(s.Records()); h != want {
			fail("TraceHash() = %#x, HashRecords(Records()) = %#x", h, want)
		}
	})
}

func idAttr(t *testing.T, r Record) EventID {
	t.Helper()
	if len(r.Attrs) != 1 || r.Attrs[0].Key != "id" {
		t.Fatalf("record %d (%s) has attributes %v, want one id", r.Seq, r.Kind, r.Attrs)
	}
	id, err := strconv.ParseUint(r.Attrs[0].Value, 10, 64)
	if err != nil {
		t.Fatalf("record %d (%s): %v", r.Seq, r.Kind, err)
	}
	return EventID(id)
}

// AT-KRN-46, DET-060
func FuzzStreamSeeds(f *testing.F) {
	for _, in := range []struct {
		seed  uint64
		label string
	}{
		{0, ""}, {0, "kernel/sched"}, {0, "node/n1/1"}, {0, "a"},
		{1, "kernel/sched"}, {0x5e1f9a2c4b7d3e80, "node/n1/1"}, {1, "net/link/1/2"},
		{0xffffffffffffffff, "workload/kv"}, {1, "workload/test"},
	} {
		f.Add(in.seed, in.label)
	}
	f.Fuzz(func(t *testing.T, seed uint64, label string) {
		h := fnv.New64a()
		h.Write([]byte(label))
		if got := fnv1a64(label); got != h.Sum64() {
			t.Fatalf("fnv1a64(%q) = %#x, hash/fnv %#x", label, got, h.Sum64())
		}
		a1, a2 := streamSeeds(seed, label)
		if b1, b2 := streamSeeds(seed, label); a1 != b1 || a2 != b2 {
			t.Fatalf("streamSeeds(%d, %q) = (%#x, %#x), then (%#x, %#x)", seed, label, a1, a2, b1, b2)
		}
		if label != "" && !reservedLabel(label) {
			got := New(Config{Seed: seed}).Rand(label)
			want := rand.New(rand.NewPCG(a1, a2)) //nolint:gosec // the PCG stream that Sim.Rand must match
			for i := 0; i < 4; i++ {
				if g, w := got.Uint64(), want.Uint64(); g != w {
					t.Fatalf("seed %d label %q output %d: Sim.Rand %#x, PCG %#x", seed, label, i, g, w)
				}
			}
		}
		if c1, c2 := streamSeeds(seed, label+"x"); c1 == a1 && c2 == a2 {
			t.Fatalf("seed %d: labels %q and %q give the same seeds", seed, label, label+"x")
		}
	})
}

func reservedLabel(label string) bool {
	return strings.HasPrefix(label, "kernel/") || strings.HasPrefix(label, "node/")
}

// decodeRecord decodes one KRN-095 encoding from b and returns the rest.
func decodeRecord(b []byte) (Record, []byte, error) {
	var r Record
	errShort := errors.New("short buffer")
	errRange := errors.New("value out of range")
	uv := func() uint64 {
		v, n := binary.Uvarint(b)
		if n <= 0 {
			panic(errShort)
		}
		b = b[n:]
		return v
	}
	sv := func() int64 {
		v, n := binary.Varint(b)
		if n <= 0 {
			panic(errShort)
		}
		b = b[n:]
		return v
	}
	str := func() string {
		l := uv()
		if l > uint64(len(b)) {
			panic(errShort)
		}
		s := string(b[:l])
		b = b[l:]
		return s
	}
	var err error
	func() {
		defer func() {
			if v := recover(); v != nil {
				e, ok := v.(error)
				if !ok || (e != errShort && e != errRange) {
					panic(v)
				}
				err = e
			}
		}()
		r.Seq = uv()
		r.At = Time(sv())
		node := sv()
		if node != int64(NodeID(node)) { //nolint:gosec // the round trip is the range check
			panic(errRange)
		}
		r.Node = NodeID(node) //nolint:gosec // in range: checked above
		inc := uv()
		if inc > math.MaxUint32 {
			panic(errRange)
		}
		r.Inc = uint32(inc)
		r.Kind = str()
		r.Cause = uv()
		r.Text = str()
		for n := uv(); n > 0; n-- {
			r.Attrs = append(r.Attrs, Attr{Key: str(), Value: str()})
		}
	}()
	return r, b, err
}

func sameRecord(a, b Record) bool {
	return a.Seq == b.Seq && a.At == b.At && a.Node == b.Node && a.Inc == b.Inc && a.Kind == b.Kind &&
		a.Cause == b.Cause && a.Text == b.Text && slices.Equal(a.Attrs, b.Attrs)
}

// AT-KRN-46, DET-060
func FuzzRecordEncoding(f *testing.F) {
	f.Add(uint64(1), int64(0), int32(0), uint32(0), "kernel.start", uint64(0), "start",
		"seed", "0x0000000000000001", "tie_break", "seeded", uint8(2))
	f.Add(uint64(300), int64(-2), int32(3), uint32(2), "net.send", uint64(299), "ping",
		"to", "n2", "", "", uint8(1))
	f.Add(uint64(math.MaxUint64), int64(math.MinInt64), int32(math.MinInt32), uint32(math.MaxUint32), "k",
		uint64(math.MaxUint64), "", "", "", "", "", uint8(0))
	f.Fuzz(func(t *testing.T, seq uint64, at int64, node int32, inc uint32, kind string, cause uint64, text string,
		k1, v1, k2, v2 string, nattrs uint8) {
		r := Record{Seq: seq, At: Time(at), Node: NodeID(node), Inc: inc, Kind: kind, Cause: cause, Text: text}
		for i := 0; i < int(nattrs%3); i++ {
			r.Attrs = append(r.Attrs, []Attr{{Key: k1, Value: v1}, {Key: k2, Value: v2}}[i])
		}
		enc := AppendRecord(nil, r)
		got, rest, err := decodeRecord(enc)
		if err != nil || len(rest) != 0 || !sameRecord(got, r) {
			t.Fatalf("decode: %+v, %d bytes left, %v; want %+v", got, len(rest), err, r)
		}
		r2 := r
		r2.Seq++
		both := AppendRecord(AppendRecord(nil, r), r2)
		g1, rest, err1 := decodeRecord(both)
		g2, rest, err2 := decodeRecord(rest)
		if err1 != nil || err2 != nil || len(rest) != 0 || !sameRecord(g1, r) || !sameRecord(g2, r2) {
			t.Fatalf("concatenation is not uniquely decodable: %+v (%v), %+v (%v), %d bytes left", g1, err1, g2, err2, len(rest))
		}
		h := fnv.New64a()
		h.Write(enc)
		if got := HashRecords([]Record{r}); got != h.Sum64() {
			t.Fatalf("HashRecords = %#x, hash/fnv %#x", got, h.Sum64())
		}
	})
}

// AT-KRN-46, DET-060
func FuzzClock(f *testing.F) {
	for _, in := range []struct {
		d   int64
		ppm int32
	}{
		{int64(time.Second), 0}, {int64(time.Second), 100}, {int64(time.Second), -100},
		{int64(time.Second), 1000000}, {int64(time.Second), -500000}, {1, 100}, {3, 333333},
		{int64(24 * time.Hour), 250}, {math.MaxInt64, -500000}, {math.MaxInt64, 1}, {0, 100}, {-5, 100},
	} {
		f.Add(in.d, in.ppm, int64(1), int64(0))
	}
	f.Add(int64(1), int32(-100), int64(1), int64(0))
	f.Add(int64(time.Second), int32(-500000), int64(1), int64(math.MaxInt64))
	f.Add(int64(time.Second), int32(100), int64(1), int64(math.MaxInt64-5))
	f.Add(int64(time.Second), int32(-100), int64(1), int64(math.MinInt64))
	f.Fuzz(func(t *testing.T, d int64, ppm int32, delta int64, l0 int64) {
		if ppm < MinDriftPPM || ppm > MaxDriftPPM {
			ppm = MinDriftPPM + int32(uint32(ppm)%uint32(MaxDriftPPM-MinDriftPPM+1)) //nolint:gosec // wraps on purpose: folds any fuzz input into the drift range
		}
		g := localToGlobal(time.Duration(d), ppm)
		if d <= 0 {
			if g != 0 {
				t.Fatalf("localToGlobal(%d, %d) = %d, want 0", d, ppm, g)
			}
		} else {
			den := big.NewInt(1_000_000 + int64(ppm))
			need := new(big.Int).Mul(big.NewInt(d), big.NewInt(1_000_000))
			if new(big.Int).Mul(big.NewInt(int64(g)-1), den).Cmp(need) >= 0 ||
				g != math.MaxInt64 && new(big.Int).Mul(big.NewInt(int64(g)), den).Cmp(need) < 0 {
				t.Fatalf("localToGlobal(%d, %d) = %d is not the ceiling", d, ppm, g)
			}
		}
		abs := func(x int64) (int64, bool) {
			if x == math.MinInt64 {
				return 0, false
			}
			if x < 0 {
				return -x, true
			}
			return x, true
		}
		d1, ok1 := abs(d)
		dd, ok2 := abs(delta)
		if !ok1 || !ok2 || d1 > math.MaxInt64-dd {
			return
		}
		d2 := d1 + dd
		want := new(big.Int).Mul(big.NewInt(d1), big.NewInt(int64(ppm)))
		want.Div(want, big.NewInt(1_000_000)) // Euclidean division: floor for a positive divisor
		if got := drift(d1, ppm); !want.IsInt64() || got != want.Int64() {
			t.Fatalf("drift(%d, %d) = %d, want %s", d1, ppm, got, want)
		}
		c := clock{l0: Time(l0), ppm: ppm}
		local := new(big.Int).Add(big.NewInt(l0), big.NewInt(d1))
		local.Add(local, want) // at least l0, since the drift is at least -d1/2
		if local.Cmp(big.NewInt(math.MaxInt64)) > 0 {
			local.SetInt64(math.MaxInt64)
		}
		if got := c.at(Time(d1)); int64(got) != local.Int64() {
			t.Fatalf("clock{l0: %d, ppm: %d}.at(%d) = %d, want %s", l0, ppm, d1, got, local)
		}
		if r1, r2 := c.at(Time(d1)), c.at(Time(d2)); r1 > r2 {
			t.Fatalf("clock{l0: %d, ppm: %d}: reading %d at %d, then %d at %d", l0, ppm, r1, d1, r2, d2)
		}
	})
}
