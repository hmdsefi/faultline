// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package fault

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// scheduleHeader is the first two lines of a written schedule (FLT-028); its version is
// ScheduleVersion.
const scheduleHeader = "{\n  \"faultline_schedule\": 1,\n"

// topKeys are the top-level keys of FLT-020, in canonical order.
var topKeys = []string{"faultline_schedule", "end", "recovery", "events"}

// eventKeys are the event object keys of FLT-021, in canonical order.
var eventKeys = []string{"id", "at", "kind", "node", "role", "peer", "groups", "link", "n", "path", "off", "len", "undoes"}

// linkKeys are the link object keys of FLT-025, in canonical order.
var linkKeys = []string{"latency", "jitter", "tail_ppm", "tail", "drop_ppm", "dup_ppm", "fifo"}

// fieldBit maps an event key to its field bit; "role" counts as "node" (FLT-026 step 6).
func fieldBit(key string) uint16 {
	switch key {
	case "node", "role":
		return fNode
	case "peer":
		return fPeer
	case "groups":
		return fGroups
	case "link":
		return fLink
	case "n":
		return fN
	case "path":
		return fPath
	case "off":
		return fOff
	case "len":
		return fLen
	}
	return 0
}

func isNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

// unknownKey returns the smallest key of obj (byte order) that is not in known, or "".
func unknownKey(obj map[string]json.RawMessage, known []string) string {
	for _, k := range slices.Sorted(maps.Keys(obj)) {
		if !slices.Contains(known, k) {
			return k
		}
	}
	return ""
}

// ReadSchedule reads one schedule in JSON format v1 (FLT-020 to FLT-027). Events are returned
// stably sorted by At; ID and Undoes are returned as written.
func ReadSchedule(r io.Reader) (Schedule, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return Schedule{}, fmt.Errorf("fault: schedule: read: %w", err)
	}
	if s, ok := readCanonical(data); ok {
		return s, nil
	}
	return readStrict(data)
}

// readStrict reads data by FLT-024 to FLT-026 and returns their errors.
func readStrict(data []byte) (Schedule, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return Schedule{}, fmt.Errorf("fault: schedule: invalid JSON: %w", err)
	}
	if k := unknownKey(top, topKeys); k != "" {
		return Schedule{}, fmt.Errorf("fault: schedule: unknown field %q", k)
	}
	raw, ok := top["faultline_schedule"]
	if !ok {
		return Schedule{}, errors.New(`fault: schedule: missing field "faultline_schedule"`)
	}
	if isNull(raw) {
		return Schedule{}, errors.New(`fault: schedule: field "faultline_schedule" must not be null`)
	}
	var version int
	if err := json.Unmarshal(raw, &version); err != nil {
		return Schedule{}, fmt.Errorf(`fault: schedule: field "faultline_schedule": %w`, err)
	}
	if version != ScheduleVersion {
		return Schedule{}, fmt.Errorf("fault: schedule: unsupported version %d (supported: 1)", version)
	}
	var times [2]kernel.Time
	for i, key := range []string{"end", "recovery"} {
		raw, ok := top[key]
		if !ok {
			continue
		}
		if isNull(raw) {
			return Schedule{}, fmt.Errorf("fault: schedule: field %q must not be null", key)
		}
		d, err := parseDuration(raw)
		if err != nil {
			return Schedule{}, fmt.Errorf("fault: schedule: field %q: %w", key, err)
		}
		if d < 0 {
			return Schedule{}, fmt.Errorf("fault: schedule: %s must be >= 0 (got %s)", key, d)
		}
		times[i] = kernel.Time(d)
	}
	raw, ok = top["events"]
	if !ok {
		return Schedule{}, errors.New(`fault: schedule: missing field "events"`)
	}
	if isNull(raw) {
		return Schedule{}, errors.New(`fault: schedule: field "events" must not be null`)
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(raw, &raws); err != nil {
		return Schedule{}, fmt.Errorf(`fault: schedule: field "events": %w`, err)
	}
	events := make([]Event, 0, len(raws))
	for i, raw := range raws {
		e, err := parseEvent(raw)
		if err != nil {
			return Schedule{}, fmt.Errorf("fault: schedule: events[%d]: %w", i, err)
		}
		events = append(events, e)
	}
	sortByAt(events)
	return Schedule{Version: ScheduleVersion, End: times[0], Recovery: times[1], Events: events}, nil
}

// parseDuration decodes a JSON string and parses it with time.ParseDuration (FLT-022).
func parseDuration(raw json.RawMessage) (time.Duration, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, err
	}
	return time.ParseDuration(s)
}

// parseEvent parses one event object (FLT-026).
func parseEvent(raw json.RawMessage) (Event, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return Event{}, err
	}
	if k := unknownKey(obj, eventKeys); k != "" {
		return Event{}, fmt.Errorf("unknown field %q", k)
	}
	for _, k := range eventKeys {
		if v, ok := obj[k]; ok && isNull(v) {
			return Event{}, fmt.Errorf("field %q must not be null", k)
		}
	}
	for _, k := range []string{"at", "kind"} {
		if _, ok := obj[k]; !ok {
			return Event{}, fmt.Errorf("missing field %q", k)
		}
	}
	var e Event
	for _, k := range eventKeys {
		v, ok := obj[k]
		if !ok {
			continue
		}
		var err error
		switch k {
		case "id":
			err = json.Unmarshal(v, &e.ID)
		case "at":
			var d time.Duration
			d, err = parseDuration(v)
			e.At = kernel.Time(d)
		case "kind":
			var s string
			err = json.Unmarshal(v, &s)
			e.Kind = Kind(s)
		case "node":
			err = json.Unmarshal(v, &e.Node)
		case "role":
			err = json.Unmarshal(v, &e.Role)
		case "peer":
			err = json.Unmarshal(v, &e.Peer)
		case "groups":
			err = json.Unmarshal(v, &e.Groups)
		case "link":
			e.Link, err = parseLink(v)
		case "n":
			err = json.Unmarshal(v, &e.N)
		case "path":
			err = json.Unmarshal(v, &e.Path)
		case "off":
			err = json.Unmarshal(v, &e.Off)
		case "len":
			err = json.Unmarshal(v, &e.Len)
		case "undoes":
			err = json.Unmarshal(v, &e.Undoes)
			if len(e.Undoes) == 0 {
				e.Undoes = nil
			}
		}
		if err != nil {
			return Event{}, fmt.Errorf("field %q: %w", k, err)
		}
	}
	if e.Kind == "" {
		return Event{}, errors.New("missing kind")
	}
	set, valid := fieldSet(e.Kind)
	if !valid {
		return Event{}, fmt.Errorf("unknown kind %q", string(e.Kind))
	}
	k := string(e.Kind)
	_, hasNode := obj["node"]
	_, hasRole := obj["role"]
	if hasNode && hasRole {
		return Event{}, errors.New(k + ": node and role are mutually exclusive")
	}
	for _, key := range eventKeys {
		if _, ok := obj[key]; !ok {
			continue
		}
		switch key {
		case "id", "at", "kind", "undoes":
			continue
		}
		if set&fieldBit(key) == 0 {
			return Event{}, errors.New(k + ": " + key + " is not allowed")
		}
	}
	for _, key := range eventKeys {
		bit := fieldBit(key)
		if key == "role" || set&bit == 0 {
			continue
		}
		if _, ok := obj[key]; !ok && (key != "node" || !hasRole) {
			return Event{}, errors.New(k + ": " + key + " is required")
		}
	}
	if p := e.problem(); p != "" {
		return Event{}, errors.New(p)
	}
	return e, nil
}

// parseLink parses a link object (FLT-025). Errors are not prefixed with the field name.
func parseLink(raw json.RawMessage) (*simnet.Link, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	if k := unknownKey(obj, linkKeys); k != "" {
		return nil, fmt.Errorf("unknown field %q", k)
	}
	var l simnet.Link
	for _, k := range linkKeys {
		v, ok := obj[k]
		if !ok {
			continue
		}
		if isNull(v) {
			return nil, fmt.Errorf("field %q must not be null", k)
		}
		var err error
		switch k {
		case "latency":
			l.Latency, err = parseDuration(v)
		case "jitter":
			l.Jitter, err = parseDuration(v)
		case "tail":
			l.Tail, err = parseDuration(v)
		case "tail_ppm", "drop_ppm", "dup_ppm":
			var n int64
			if err = json.Unmarshal(v, &n); err == nil {
				if n < 0 || n > 4294967295 {
					return nil, fmt.Errorf("%s out of range (got %d)", k, n)
				}
				switch k {
				case "tail_ppm":
					l.TailPPM = uint32(n)
				case "drop_ppm":
					l.DropPPM = uint32(n)
				default:
					l.DupPPM = uint32(n)
				}
			}
		case "fifo":
			err = json.Unmarshal(v, &l.FIFO)
		}
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", k, err)
		}
	}
	return &l, nil
}

// Write validates s and writes it in canonical JSON format v1 (FLT-028). Events are written
// stably sorted by At. Nothing is written if validation fails.
func (s Schedule) Write(w io.Writer) error {
	if err := s.Validate(); err != nil {
		return err
	}
	events := slices.Clone(s.Events)
	sortByAt(events)
	b := make([]byte, 0, 64+96*len(events))
	b = append(b, scheduleHeader...)
	if s.End != 0 {
		b = append(b, "  \"end\": "...)
		b = append(appendDuration(b, time.Duration(s.End)), ",\n"...)
	}
	if s.Recovery != 0 {
		b = append(b, "  \"recovery\": "...)
		b = append(appendDuration(b, time.Duration(s.Recovery)), ",\n"...)
	}
	if len(events) == 0 {
		b = append(b, "  \"events\": []\n"...)
	} else {
		b = append(b, "  \"events\": [\n"...)
		for i, e := range events {
			b = appendEvent(append(b, "    "...), e)
			if i < len(events)-1 {
				b = append(b, ',')
			}
			b = append(b, '\n')
		}
		b = append(b, "  ]\n"...)
	}
	b = append(b, "}\n"...)
	_, err := w.Write(b)
	return err
}

// Bits of the event keys, in FLT-021 order (the order of eventKeys).
const (
	keyID uint16 = 1 << iota
	keyAt
	keyKind
	keyNode
	keyRole
	keyPeer
	keyGroups
	keyLink
	keyN
	keyPath
	keyOff
	keyLen
	keyUndoes
)

// writtenKeys returns the keys that Write writes for e, whose kind has field set set (FLT-021):
// id when non-zero, at and kind, role when non-empty and otherwise node if the set has it, the
// other fields of the set even when zero, and undoes when non-empty.
func writtenKeys(e Event, set uint16) uint16 {
	keys := keyAt | keyKind
	if e.ID != 0 {
		keys |= keyID
	}
	if set&fNode != 0 && e.Role == "" {
		keys |= keyNode
	}
	if e.Role != "" {
		keys |= keyRole
	}
	for _, f := range [...]struct{ field, key uint16 }{
		{fPeer, keyPeer}, {fGroups, keyGroups}, {fLink, keyLink}, {fN, keyN}, {fPath, keyPath}, {fOff, keyOff}, {fLen, keyLen},
	} {
		if set&f.field != 0 {
			keys |= f.key
		}
	}
	if len(e.Undoes) > 0 {
		keys |= keyUndoes
	}
	return keys
}

// appendEvent appends one event object on one line with keys in FLT-021 order (FLT-028).
func appendEvent(b []byte, e Event) []byte {
	set, _ := fieldSet(e.Kind)
	keys := writtenKeys(e, set)
	b = append(b, '{')
	if keys&keyID != 0 {
		b = append(strconv.AppendInt(append(b, `"id": `...), int64(e.ID), 10), ", "...)
	}
	b = appendDuration(append(b, `"at": `...), time.Duration(e.At))
	b = appendString(append(b, `, "kind": `...), string(e.Kind))
	if keys&keyNode != 0 {
		b = appendString(append(b, `, "node": `...), e.Node)
	}
	if keys&keyRole != 0 {
		b = appendString(append(b, `, "role": `...), e.Role)
	}
	if keys&keyPeer != 0 {
		b = appendString(append(b, `, "peer": `...), e.Peer)
	}
	if keys&keyGroups != 0 {
		b = append(b, `, "groups": [`...)
		for i, g := range e.Groups {
			if i > 0 {
				b = append(b, ", "...)
			}
			b = append(b, '[')
			for j, name := range g {
				if j > 0 {
					b = append(b, ", "...)
				}
				b = appendString(b, name)
			}
			b = append(b, ']')
		}
		b = append(b, ']')
	}
	if keys&keyLink != 0 {
		b = appendLink(append(b, `, "link": `...), *e.Link)
	}
	if keys&keyN != 0 {
		b = strconv.AppendInt(append(b, `, "n": `...), e.N, 10)
	}
	if keys&keyPath != 0 {
		b = appendString(append(b, `, "path": `...), e.Path)
	}
	if keys&keyOff != 0 {
		b = strconv.AppendInt(append(b, `, "off": `...), e.Off, 10)
	}
	if keys&keyLen != 0 {
		b = strconv.AppendInt(append(b, `, "len": `...), int64(e.Len), 10)
	}
	if keys&keyUndoes != 0 {
		b = append(b, `, "undoes": [`...)
		for i, id := range e.Undoes {
			if i > 0 {
				b = append(b, ", "...)
			}
			b = strconv.AppendInt(b, int64(id), 10)
		}
		b = append(b, ']')
	}
	return append(b, '}')
}

// appendLink appends the non-zero fields of l in FLT-025 order.
func appendLink(b []byte, l simnet.Link) []byte {
	sep := "{"
	if l.Latency != 0 {
		b = appendDuration(append(append(b, sep...), `"latency": `...), l.Latency)
		sep = ", "
	}
	if l.Jitter != 0 {
		b = appendDuration(append(append(b, sep...), `"jitter": `...), l.Jitter)
		sep = ", "
	}
	if l.TailPPM != 0 {
		b = strconv.AppendUint(append(append(b, sep...), `"tail_ppm": `...), uint64(l.TailPPM), 10)
		sep = ", "
	}
	if l.Tail != 0 {
		b = appendDuration(append(append(b, sep...), `"tail": `...), l.Tail)
		sep = ", "
	}
	if l.DropPPM != 0 {
		b = strconv.AppendUint(append(append(b, sep...), `"drop_ppm": `...), uint64(l.DropPPM), 10)
		sep = ", "
	}
	if l.DupPPM != 0 {
		b = strconv.AppendUint(append(append(b, sep...), `"dup_ppm": `...), uint64(l.DupPPM), 10)
		sep = ", "
	}
	if l.FIFO {
		b = append(append(b, sep...), `"fifo": true`...)
		sep = ", "
	}
	if sep == "{" {
		b = append(b, '{')
	}
	return append(b, '}')
}

// appendDuration appends d as a JSON string in time.Duration.String form (FLT-022).
func appendDuration(b []byte, d time.Duration) []byte {
	return append(append(append(b, '"'), d.String()...), '"')
}

// appendString appends s as a JSON string with json.Marshal's default escaping (FLT-021).
func appendString(b []byte, s string) []byte {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c >= 0x7f || c == '"' || c == '\\' || c == '<' || c == '>' || c == '&' {
			out, _ := json.Marshal(s) // a string always marshals
			return append(b, out...)
		}
	}
	return append(append(append(b, '"'), s...), '"')
}

// readCanonical reads data in exactly the layout that Write writes, for speed (FLT §9): the
// FLT-028 lines, event keys in FLT-021 order and exactly the ones Write writes for the event,
// link keys in FLT-025 order, strings in valid UTF-8 without escapes or control characters,
// integers of at most 18 digits, and events that pass every FLT-026 check. For such data it
// returns what readStrict returns; for any other data it reports false, and readStrict, which
// produces every error, reads it.
func readCanonical(data []byte) (Schedule, bool) {
	c := canonReader{b: data, names: map[string]string{}}
	s := Schedule{Version: ScheduleVersion}
	if !c.lit(scheduleHeader) {
		return Schedule{}, false
	}
	if c.lit("  \"end\": ") {
		d, ok := c.duration()
		if !ok || d < 0 || !c.lit(",\n") {
			return Schedule{}, false
		}
		s.End = kernel.Time(d)
	}
	if c.lit("  \"recovery\": ") {
		d, ok := c.duration()
		if !ok || d < 0 || !c.lit(",\n") {
			return Schedule{}, false
		}
		s.Recovery = kernel.Time(d)
	}
	// One event per event line: "\n    {" starts each one and appears nowhere else in this layout,
	// because strings hold no newline. The rest of the input is not checked yet, so the capacity
	// is also capped at the events it can hold, one per minEventBytes bytes.
	rest := data[c.i:]
	n := min(bytes.Count(rest, []byte("\n    {")), len(rest)/minEventBytes)
	s.Events = make([]Event, 0, n) // non-nil, like readStrict's
	if c.lit("  \"events\": []\n}\n") {
		return s, c.i == len(data)
	}
	if !c.lit("  \"events\": [\n") {
		return Schedule{}, false
	}
	for {
		if !c.lit("    ") {
			return Schedule{}, false
		}
		e, ok := c.event()
		if !ok {
			return Schedule{}, false
		}
		s.Events = append(s.Events, e)
		if c.lit(",\n") {
			continue
		}
		if !c.lit("\n  ]\n}\n") || c.i != len(data) {
			return Schedule{}, false
		}
		sortByAt(s.Events)
		return s, true
	}
}

// minEventBytes is the fewest bytes an event that readCanonical takes can use: the line
// `    {"at": "0", "kind": "heal"}` (31 bytes) and the 2 bytes after it, ",\n" or, after the last
// event, the "\n " that starts "\n  ]". Every event has at and kind, "0" is the shortest string
// that time.ParseDuration accepts, and heal is the only kind with no other key.
const minEventBytes = 33

// canonReader reads the layout that Write writes from position i of b (readCanonical).
type canonReader struct {
	b     []byte
	i     int
	names map[string]string // strings already read, shared between events
}

// lit reads the bytes of t.
func (c *canonReader) lit(t string) bool {
	if len(c.b)-c.i < len(t) || string(c.b[c.i:c.i+len(t)]) != t {
		return false
	}
	c.i += len(t)
	return true
}

// key reads the member name k and its separator: "<k>": .
func (c *canonReader) key(k string) bool {
	n := len(k) + 4
	if len(c.b)-c.i < n || c.b[c.i] != '"' || string(c.b[c.i+1:c.i+1+len(k)]) != k || string(c.b[c.i+1+len(k):c.i+n]) != "\": " {
		return false
	}
	c.i += n
	return true
}

// str reads a string in valid UTF-8 without escapes or control characters and returns its
// contents, which alias b.
func (c *canonReader) str() ([]byte, bool) {
	if c.i >= len(c.b) || c.b[c.i] != '"' {
		return nil, false
	}
	ascii := true
	for j := c.i + 1; j < len(c.b); j++ {
		switch ch := c.b[j]; {
		case ch == '"':
			v := c.b[c.i+1 : j]
			c.i = j + 1
			return v, ascii || utf8.Valid(v)
		case ch < 0x20 || ch == '\\':
			return nil, false
		case ch >= 0x80:
			ascii = false
		}
	}
	return nil, false
}

// name reads a string like str and returns it as a string, shared with earlier equal strings.
func (c *canonReader) name() (string, bool) {
	v, ok := c.str()
	if !ok {
		return "", false
	}
	if s, ok := c.names[string(v)]; ok {
		return s, true
	}
	s := string(v)
	c.names[s] = s
	return s, true
}

// duration reads a string like str and parses it with time.ParseDuration (FLT-022).
func (c *canonReader) duration() (time.Duration, bool) {
	v, ok := c.str()
	if !ok {
		return 0, false
	}
	d, err := time.ParseDuration(string(v))
	return d, err == nil
}

// integer reads a JSON integer of 1 to 18 digits, so it fits an int64.
func (c *canonReader) integer() (int64, bool) {
	j := c.i
	neg := j < len(c.b) && c.b[j] == '-'
	if neg {
		j++
	}
	start, n := j, int64(0)
	for ; j < len(c.b) && '0' <= c.b[j] && c.b[j] <= '9'; j++ {
		n = n*10 + int64(c.b[j]-'0')
	}
	if d := j - start; d == 0 || d > 18 || d > 1 && c.b[start] == '0' {
		return 0, false
	}
	if neg {
		n = -n
	}
	c.i = j
	return n, true
}

// integerInt reads an integer like integer that also fits an int.
func (c *canonReader) integerInt() (int, bool) {
	n, ok := c.integer()
	return int(n), ok && int64(int(n)) == n
}

// event reads one event object and returns it if its keys are exactly the ones Write writes for
// it and FLT-026 accepts it.
func (c *canonReader) event() (Event, bool) {
	var e Event
	if !c.lit("{") {
		return Event{}, false
	}
	var has uint16
	for k, key := range eventKeys {
		mark := c.i
		if has != 0 && !c.lit(", ") {
			break
		}
		if !c.key(key) {
			c.i = mark
			continue
		}
		has |= 1 << k
		ok := false
		switch key {
		case "id":
			e.ID, ok = c.integerInt()
		case "at":
			var d time.Duration
			d, ok = c.duration()
			e.At = kernel.Time(d)
		case "kind":
			var s string
			s, ok = c.name()
			e.Kind = Kind(s)
		case "node":
			e.Node, ok = c.name()
		case "role":
			e.Role, ok = c.name()
		case "peer":
			e.Peer, ok = c.name()
		case "groups":
			e.Groups, ok = c.groups()
		case "link":
			e.Link, ok = c.link()
		case "n":
			e.N, ok = c.integer()
		case "path":
			e.Path, ok = c.name()
		case "off":
			e.Off, ok = c.integer()
		case "len":
			e.Len, ok = c.integerInt()
		case "undoes":
			e.Undoes, ok = c.ints()
		}
		if !ok {
			return Event{}, false
		}
	}
	if !c.lit("}") {
		return Event{}, false
	}
	set, valid := fieldSet(e.Kind)
	if !valid || has != writtenKeys(e, set) || e.problem() != "" {
		return Event{}, false
	}
	return e, true
}

// groups reads a non-empty array of non-empty arrays of strings.
func (c *canonReader) groups() ([][]string, bool) {
	var groups [][]string
	if !c.lit("[") {
		return nil, false
	}
	for len(groups) == 0 || c.lit(", ") {
		var g []string
		if !c.lit("[") {
			return nil, false
		}
		for len(g) == 0 || c.lit(", ") {
			name, ok := c.name()
			if !ok {
				return nil, false
			}
			g = append(g, name)
		}
		if !c.lit("]") {
			return nil, false
		}
		groups = append(groups, g)
	}
	return groups, c.lit("]")
}

// ints reads a non-empty array of integers that fit an int.
func (c *canonReader) ints() ([]int, bool) {
	var ids []int
	if !c.lit("[") {
		return nil, false
	}
	for len(ids) == 0 || c.lit(", ") {
		id, ok := c.integerInt()
		if !ok {
			return nil, false
		}
		ids = append(ids, id)
	}
	return ids, c.lit("]")
}

// link reads a link object with its keys in FLT-025 order, ppm values in [0, 4294967295] and
// fifo true or false.
func (c *canonReader) link() (*simnet.Link, bool) {
	var l simnet.Link
	if !c.lit("{") {
		return nil, false
	}
	first := true
	for _, key := range linkKeys {
		mark := c.i
		if !first && !c.lit(", ") {
			break
		}
		if !c.key(key) {
			c.i = mark
			continue
		}
		first = false
		ok := false
		switch key {
		case "latency":
			l.Latency, ok = c.duration()
		case "jitter":
			l.Jitter, ok = c.duration()
		case "tail":
			l.Tail, ok = c.duration()
		case "tail_ppm":
			l.TailPPM, ok = c.ppm()
		case "drop_ppm":
			l.DropPPM, ok = c.ppm()
		case "dup_ppm":
			l.DupPPM, ok = c.ppm()
		case "fifo":
			l.FIFO = c.lit("true")
			ok = l.FIFO || c.lit("false")
		}
		if !ok {
			return nil, false
		}
	}
	return &l, c.lit("}")
}

// ppm reads an integer in [0, 4294967295] (FLT-025).
func (c *canonReader) ppm() (uint32, bool) {
	n, ok := c.integer()
	if !ok || n < 0 || n > 4294967295 {
		return 0, false
	}
	return uint32(n), true
}
