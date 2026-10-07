package fault

import (
	"bytes"
	"encoding/json"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// writtenDocuments returns documents that Write writes: no events; every kind with every field of
// its field set, a role, IDs, Undoes, End and Recovery; every link field, extreme and negative
// integers, sub-microsecond durations and non-ASCII names. The last one has names that
// json.Marshal escapes, so readCanonical does not take it.
func writtenDocuments(tb testing.TB) [][]byte {
	tb.Helper()
	l := &simnet.Link{Latency: 50 * time.Millisecond, Jitter: 1500 * time.Nanosecond, TailPPM: 1000000, Tail: time.Hour,
		DropPPM: 1, DupPPM: 999999, FIFO: true}
	all := Schedule{End: kernel.Time(time.Minute), Recovery: kernel.Time(45 * time.Second), Events: []Event{
		{At: kernel.Time(time.Second), Kind: KindPartition, Groups: [][]string{{"n1", "é ü"}, {"n3"}}, ID: 1},
		{At: kernel.Time(time.Second), Kind: KindIsolate, Node: "n1"},
		{At: kernel.Time(1500 * time.Nanosecond), Kind: KindCut, Node: "n1", Peer: "n\xc2\xa0"}, // U+00A0
		{At: 2, Kind: KindHeal, ID: 12, Undoes: []int{1, 3}},
		{At: 3, Kind: KindHealLink, Node: "n1", Peer: "n2"},
		{At: 3, Kind: KindLink, Node: "n1", Peer: "n2", Link: l, Undoes: []int{2}},
		{At: 3, Kind: KindLink, Node: "n2", Peer: "n1", Link: &simnet.Link{}},
		{At: 4, Kind: KindLinkReset, Node: "n1", Peer: "n2", Undoes: []int{6, 7}},
		{At: kernel.Time(25 * time.Hour), Kind: KindCrash, Role: "leader"},
		{At: kernel.Time(25 * time.Hour), Kind: KindRestart, Node: "n1", ID: 999999999999999999},
		{At: 5, Kind: KindPause, Node: "~ !#$%'()*+,-./:;=?@[]^_`{|}"},
		{At: 5, Kind: KindResume, Node: "n1"},
		{At: 6, Kind: KindClockJump, Node: "n1", N: -int64(MaxRuleDuration)},
		{At: 6, Kind: KindClockDrift, Node: "n1", N: -500000},
		{At: 7, Kind: KindSyncFail, Node: "n1"},
		{At: 7, Kind: KindDiskCapacity, Node: "n1", N: 1 << 40},
		{At: 8, Kind: KindCorrupt, Node: "n1", Path: "/wal/0 1", Off: 1 << 50, Len: 16},
	}}
	var docs [][]byte
	for _, s := range []Schedule{{}, {End: 1}, all, {Events: []Event{{Kind: KindCrash, Node: "<n>"}, {Kind: KindCut, Node: "n1", Peer: "n\xe2\x80\xa8"}}}} { // <n> and U+2028 are escaped
		var b bytes.Buffer
		if err := s.Write(&b); err != nil {
			tb.Fatal(err)
		}
		docs = append(docs, b.Bytes())
	}
	return docs
}

// ReadSchedule reads a document with readCanonical when it can and with readStrict otherwise:
// readCanonical must take the documents that Write writes without escapes, and must return what
// readStrict returns for every document it takes. The documents below are written documents,
// every document one byte or one ", " away from them (", " puts a separator before a first member
// or element), and the member and integer variants of memberVariants, each checked as it is built.
func TestReadCanonical(t *testing.T) {
	docs := writtenDocuments(t)
	for _, d := range docs[:len(docs)-1] {
		if _, ok := readCanonical(d); !ok {
			t.Errorf("readCanonical does not take the written document\n%s", d)
		}
	}
	taken := 0
	check := func(m []byte) {
		fast, ok := readCanonical(m)
		if !ok {
			return
		}
		taken++
		if strict, err := readStrict(m); err != nil || !reflect.DeepEqual(fast, strict) {
			t.Errorf("%s\nreadCanonical = %+v\n   readStrict = %+v, %v", m, fast, strict, err)
		}
	}
	for _, d := range docs {
		memberVariants(d, check)
		for i := 0; i <= len(d); i++ {
			if i < len(d) {
				check(cat(d[:i], d[i+1:]))
				check(cat(d[:i+1], d[i:]))
			}
			for _, c := range []string{" ", "\t", "\n", `"`, `\`, ",", ", ", ":", "{", "}", "[", "]", ".", "e", "0", "9", "-", "+", "µ", "\x00", "\x1f", "\xff"} {
				check(cat(d[:i], []byte(c), d[i:]))
				if i < len(d) {
					check(cat(d[:i], []byte(c), d[i+1:]))
				}
			}
		}
	}
	if taken < 1000 {
		t.Errorf("readCanonical took only %d documents", taken)
	}
}

// cat returns the concatenation of parts in a new slice.
func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

// edgeValues replace the integers and the true literals of written documents in memberVariants;
// the empty one deletes them.
var edgeValues = []string{"0", "-0", "01", "1.0", "1e3", "-1", "+1", "4294967295", "4294967296", "999999999999999999",
	"1000000000000000000", "9223372036854775807", "9223372036854775808", "-9999999999999999999", `"1"`, "null", "true", "false",
	"2", "-4294967295", "-4293967296", ""}

// memberVariants calls check with d with each integer and true literal of a line replaced by
// each of edgeValues, and with one event member deleted, duplicated, swapped with the next one
// or followed by an extra member (an unknown key, a role or a zero-valued field), for every
// member of every event line.
func memberVariants(d []byte, check func([]byte)) {
	values := regexp.MustCompile(`-?[0-9]+|true`)
	lines := strings.Split(string(d), "\n")
	for i, l := range lines {
		for _, loc := range values.FindAllStringIndex(l, -1) {
			for _, v := range edgeValues {
				cp := slices.Clone(lines)
				cp[i] = l[:loc[0]] + v + l[loc[1]:]
				check([]byte(strings.Join(cp, "\n")))
			}
		}
		head, rest, ok := strings.Cut(l, "{")
		if !ok || !strings.HasPrefix(l, "    {") {
			continue
		}
		body, tail := rest[:strings.LastIndexByte(rest, '}')], rest[strings.LastIndexByte(rest, '}'):]
		ms := splitMembers(body)
		for j := range ms {
			variants := [][]string{
				slices.Delete(slices.Clone(ms), j, j+1),
				slices.Insert(slices.Clone(ms), j, ms[j]),
				slices.Insert(slices.Clone(ms), j+1, `"role": "r"`),
				slices.Insert(slices.Clone(ms), j+1, `"x": 1`),
			}
			for _, zero := range []string{`"node": ""`, `"peer": ""`, `"n": 0`, `"path": ""`, `"off": 0`, `"len": 0`} {
				variants = append(variants, slices.Insert(slices.Clone(ms), j+1, zero))
			}
			if j+1 < len(ms) {
				sw := slices.Clone(ms)
				sw[j], sw[j+1] = sw[j+1], sw[j]
				variants = append(variants, sw)
			}
			for _, v := range variants {
				cp := slices.Clone(lines)
				cp[i] = head + "{" + strings.Join(v, ", ") + tail
				check([]byte(strings.Join(cp, "\n")))
			}
		}
	}
}

// splitMembers splits the members of a written object body at the ", " between them.
func splitMembers(body string) []string {
	var ms []string
	depth, start, inStr := 0, 0, false
	for i := 0; i < len(body); i++ {
		switch c := body[i]; {
		case inStr && c == '\\':
			i++
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
		case c == ',' && depth == 0:
			ms = append(ms, body[start:i])
			start = i + 2
		}
	}
	return append(ms, body[start:])
}

// readCanonical sizes its events before it checks them. The capacity must equal the number of
// events of a document it takes, also of the densest one and of one with "    {" in a name, and
// must stay at most 8 bytes per input byte on input that only starts like a written document
// (FLT §9).
func TestReadCanonicalSizing(t *testing.T) {
	var b bytes.Buffer
	if err := (Schedule{Events: []Event{{Kind: KindCrash, Node: "    {"}}}).Write(&b); err != nil {
		t.Fatal(err)
	}
	heal := `    {"at": "0", "kind": "heal"}`
	docs := writtenDocuments(t)
	docs[len(docs)-1] = b.Bytes() // in place of the one with escapes, which readCanonical does not take
	docs = append(docs, []byte(scheduleHeader+"  \"events\": [\n"+strings.Repeat(heal+",\n", 99)+heal+"\n  ]\n}\n"))
	for _, d := range docs {
		if s, ok := readCanonical(d); !ok || cap(s.Events) != len(s.Events) {
			t.Errorf("taken %v, %d events, capacity %d\n%s", ok, len(s.Events), cap(s.Events), d)
		}
	}
	var before, after runtime.MemStats
	for _, rest := range []string{strings.Repeat("\n", 1<<16), strings.Repeat("\n    {", 1<<14)} {
		d := []byte(scheduleHeader + rest)
		runtime.ReadMemStats(&before)
		_, ok := readCanonical(d)
		runtime.ReadMemStats(&after)
		if n := after.TotalAlloc - before.TotalAlloc; ok || n > 8*uint64(len(d)) {
			t.Errorf("readCanonical allocated %d bytes for %d bytes of input (at most 8 per byte), took it: %v", n, len(d), ok)
		}
	}
}

// FuzzReadCanonical: for any bytes that readCanonical takes, readStrict succeeds and returns the
// same schedule. go test runs the seeds, the documents that Write writes.
func FuzzReadCanonical(f *testing.F) {
	for _, d := range writtenDocuments(f) {
		f.Add(d)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		fast, ok := readCanonical(b)
		if !ok {
			return
		}
		if strict, err := readStrict(b); err != nil || !reflect.DeepEqual(fast, strict) {
			t.Fatalf("%q\nreadCanonical = %+v\n   readStrict = %+v, %v", b, fast, strict, err)
		}
	})
}

// appendString writes a string as json.Marshal does, also when it skips json.Marshal.
func TestAppendString(t *testing.T) {
	ins := []string{"", "n1", "a b", "\xe2\x80\xa8", "é", "\xff", "<&>", `"\`, "\x7f", "a\x00"}
	for c := 0; c < 256; c++ {
		ins = append(ins, string([]byte{byte(c)}), "x"+string([]byte{byte(c)})+"y")
	}
	for _, s := range ins {
		want, err := json.Marshal(s)
		if got := appendString([]byte("p"), s); err != nil || string(got) != "p"+string(want) {
			t.Errorf("appendString(%q) = %s, want p%s", s, got, want)
		}
	}
}

// Write and readCanonical share scheduleHeader, whose version must be ScheduleVersion (FLT-028).
func TestScheduleHeader(t *testing.T) {
	if want := "{\n  \"faultline_schedule\": " + strconv.Itoa(ScheduleVersion) + ",\n"; scheduleHeader != want {
		t.Errorf("scheduleHeader = %q, want %q", scheduleHeader, want)
	}
}

// event sets bit k for eventKeys[k], so each key's bit must be 1<<k.
func TestKeyBits(t *testing.T) {
	bits := map[string]uint16{"id": keyID, "at": keyAt, "kind": keyKind, "node": keyNode, "role": keyRole, "peer": keyPeer,
		"groups": keyGroups, "link": keyLink, "n": keyN, "path": keyPath, "off": keyOff, "len": keyLen, "undoes": keyUndoes}
	for k, key := range eventKeys {
		if bits[key] != 1<<k {
			t.Errorf("the bit of %q is %#x, want %#x", key, bits[key], 1<<k)
		}
	}
	if len(bits) != len(eventKeys) {
		t.Errorf("%d key bits for %d keys", len(bits), len(eventKeys))
	}
}
