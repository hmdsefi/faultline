package history

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/hmdsefi/faultline/kernel"
)

// FormatVersion is the history.jsonl format version written by WriteJSONL and accepted by Read.
const FormatVersion = 1

// header is line 1 of history.jsonl (HIS-020).
type header struct {
	Version int `json:"faultline_history"`
	Ops     int `json:"ops"`
}

// line is one op line of history.jsonl, fields in HIS-020 order.
type line struct {
	ID          int64           `json:"id"`
	Process     string          `json:"process"`
	F           string          `json:"f"`
	Status      string          `json:"status"`
	Call        int64           `json:"call"`
	Return      int64           `json:"return"`
	CallIndex   int64           `json:"call_index"`
	ReturnIndex int64           `json:"return_index"`
	Input       json.RawMessage `json:"input"`
	Output      json.RawMessage `json:"output"`
}

// lineKeys are the op line keys in HIS-020 order.
var lineKeys = []string{"id", "process", "f", "status", "call", "return", "call_index", "return_index", "input", "output"}

// WriteJSONL writes the history in history.jsonl format v1 (HIS-020).
func (r *Recorder) WriteJSONL(w io.Writer) error { return writeOps(w, r.ops) }

// writeOps writes ops with the HIS-020 encoding and returns the first error from w, or the error of
// json.Marshal for an Input or Output that is not valid JSON (ops of this package never hold one).
func writeOps(w io.Writer, ops []Op) error {
	bw := bufio.NewWriter(w)
	h, err := json.Marshal(header{Version: FormatVersion, Ops: len(ops)})
	if err != nil {
		return err
	}
	// bufio.Writer keeps the first error from w and returns it from every later call, so only the
	// result of Flush is checked.
	_, _ = bw.Write(h)
	_ = bw.WriteByte('\n')
	for _, op := range ops {
		b, err := json.Marshal(line{
			ID: op.ID, Process: op.Process, F: op.F, Status: op.Status.String(),
			Call: int64(op.Call), Return: int64(op.Return), CallIndex: op.CallIndex, ReturnIndex: op.ReturnIndex,
			Input: rawOf(op.Input), Output: rawOf(op.Output),
		})
		if err != nil {
			return err
		}
		_, _ = bw.Write(b)
		_ = bw.WriteByte('\n')
	}
	return bw.Flush()
}

// rawOf returns v as a json.RawMessage; ops of this package always hold one.
func rawOf(v any) json.RawMessage {
	if raw, ok := v.(json.RawMessage); ok {
		return raw
	}
	return json.RawMessage("null")
}

func isNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

// unknownKey returns the smallest key of obj not in known, or "".
func unknownKey(obj map[string]json.RawMessage, known []string) string {
	for _, k := range slices.Sorted(maps.Keys(obj)) {
		if !slices.Contains(known, k) {
			return k
		}
	}
	return ""
}

// parseStatus maps a status string to a Status.
func parseStatus(s string) (Status, bool) {
	for _, st := range []Status{Pending, OK, Fail, Info} {
		if st.String() == s {
			return st, true
		}
	}
	return 0, false
}

// Read reads a history.jsonl stream (HIS-022). It returns the operations in ID order.
func Read(rd io.Reader) ([]Op, error) {
	br := bufio.NewReader(rd)
	// next returns the next line without its "\n" and one "\r". Each line must be a new slice, as
	// ReadBytes returns, because the Input and Output that decodeCanonical returns alias it.
	next := func() ([]byte, bool, error) {
		b, err := br.ReadBytes('\n')
		if err != nil && err != io.EOF {
			return nil, false, fmt.Errorf("history: read: %w", err)
		}
		if len(b) == 0 {
			return nil, false, nil
		}
		b = bytes.TrimSuffix(b, []byte("\n"))
		return bytes.TrimSuffix(b, []byte("\r")), true, nil
	}
	first, ok, err := next()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("history: missing header")
	}
	declared, err := parseHeader(first)
	if err != nil {
		return nil, fmt.Errorf("history: line 1: %w", err)
	}
	ops := []Op{}
	for lineNo := 2; ; lineNo++ {
		b, ok, err := next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		var prev *Op
		if len(ops) > 0 {
			prev = &ops[len(ops)-1]
		}
		op, err := parseOp(b, int64(lineNo-1), prev)
		if err != nil {
			return nil, fmt.Errorf("history: line %d: %w", lineNo, err)
		}
		ops = append(ops, op)
	}
	if len(ops) != declared {
		return nil, fmt.Errorf("history: header declares %d ops, found %d", declared, len(ops))
	}
	if err := checkIndices(ops); err != nil {
		return nil, err
	}
	if err := checkProcesses(ops); err != nil {
		return nil, err
	}
	return ops, nil
}

// parseHeader validates line 1 and returns the declared op count.
func parseHeader(b []byte) (int, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(b, &obj); err != nil {
		return 0, fmt.Errorf("invalid JSON: %w", err)
	}
	keys := []string{"faultline_history", "ops"}
	if k := unknownKey(obj, keys); k != "" {
		return 0, fmt.Errorf("unknown field %q", k)
	}
	for _, k := range keys {
		if _, ok := obj[k]; !ok {
			return 0, fmt.Errorf("missing field %q", k)
		}
	}
	var vals [2]int
	for i, k := range keys {
		if isNull(obj[k]) {
			return 0, fmt.Errorf("field %q must not be null", k)
		}
		if err := json.Unmarshal(obj[k], &vals[i]); err != nil {
			return 0, fmt.Errorf("field %q: %w", k, err)
		}
	}
	if vals[0] != FormatVersion {
		return 0, fmt.Errorf("unsupported version %d (supported: 1)", vals[0])
	}
	if vals[1] < 0 {
		return 0, fmt.Errorf("ops must be >= 0 (got %d)", vals[1])
	}
	return vals[1], nil
}

// parseOp validates one op line with expected ID k (HIS-022 table).
func parseOp(b []byte, k int64, prev *Op) (Op, error) {
	l, ok := decodeCanonical(b)
	if !ok {
		var err error
		if l, err = decodeLine(b); err != nil {
			return Op{}, err
		}
	}
	status, ok := parseStatus(l.Status)
	switch {
	case l.ID != k:
		return Op{}, fmt.Errorf("id %d, want %d", l.ID, k)
	case l.Process == "":
		return Op{}, errors.New("process must not be empty")
	case l.F == "":
		return Op{}, errors.New("f must not be empty")
	case !ok:
		return Op{}, fmt.Errorf("unknown status %q", l.Status)
	case l.Call < 0:
		return Op{}, fmt.Errorf("call must be >= 0 (got %d)", l.Call)
	case prev != nil && l.Call < int64(prev.Call):
		return Op{}, fmt.Errorf("call %d is before the previous op's call %d", l.Call, int64(prev.Call))
	case prev == nil && l.CallIndex < 1:
		return Op{}, fmt.Errorf("call_index must be >= 1 (got %d)", l.CallIndex)
	case prev != nil && l.CallIndex <= prev.CallIndex:
		return Op{}, fmt.Errorf("call_index %d is not greater than the previous op's call_index %d", l.CallIndex, prev.CallIndex)
	case status == Pending && (l.Return != 0 || l.ReturnIndex != 0 || string(l.Output) != "null"):
		return Op{}, errors.New("pending op must have return 0, return_index 0 and output null")
	case status != Pending && l.Return < l.Call:
		return Op{}, fmt.Errorf("return %d is before call %d", l.Return, l.Call)
	case status != Pending && l.ReturnIndex <= l.CallIndex:
		return Op{}, fmt.Errorf("return_index %d must be greater than call_index %d", l.ReturnIndex, l.CallIndex)
	}
	return Op{
		ID: l.ID, Process: l.Process, F: l.F, Status: status, Input: l.Input, Output: l.Output,
		Call: kernel.Time(l.Call), Return: kernel.Time(l.Return), CallIndex: l.CallIndex, ReturnIndex: l.ReturnIndex,
	}, nil
}

// decodeLine runs checks 1 to 6 of the HIS-022 table on an op line and returns its fields, with
// input and output compacted.
func decodeLine(b []byte) (line, error) {
	if len(b) == 0 {
		return line{}, errors.New("empty line")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(b, &obj); err != nil {
		return line{}, fmt.Errorf("invalid JSON: %w", err)
	}
	if key := unknownKey(obj, lineKeys); key != "" {
		return line{}, fmt.Errorf("unknown field %q", key)
	}
	for _, key := range lineKeys {
		if _, ok := obj[key]; !ok {
			return line{}, fmt.Errorf("missing field %q", key)
		}
	}
	for _, key := range lineKeys {
		if key != "input" && key != "output" && isNull(obj[key]) {
			return line{}, fmt.Errorf("field %q must not be null", key)
		}
	}
	var l line
	targets := map[string]any{"id": &l.ID, "process": &l.Process, "f": &l.F, "status": &l.Status, "call": &l.Call,
		"return": &l.Return, "call_index": &l.CallIndex, "return_index": &l.ReturnIndex}
	for _, key := range lineKeys[:8] {
		if err := json.Unmarshal(obj[key], targets[key]); err != nil {
			return line{}, fmt.Errorf("field %q: %w", key, err)
		}
	}
	var in, out bytes.Buffer
	if err := json.Compact(&in, obj["input"]); err != nil {
		return line{}, fmt.Errorf("field %q: %w", "input", err)
	}
	if err := json.Compact(&out, obj["output"]); err != nil {
		return line{}, fmt.Errorf("field %q: %w", "output", err)
	}
	l.Input, l.Output = in.Bytes(), out.Bytes()
	return l, nil
}

// decodeCanonical decodes an op line in the layout that WriteJSONL writes, for speed: valid UTF-8
// and valid JSON, the HIS-020 keys once each and in table order, no space outside strings,
// integers of at most 18 digits, and process, f and status without escapes. For such a line it
// returns what decodeLine returns; for any other line it reports false. Input and Output alias b,
// each with its capacity cut to its length.
func decodeCanonical(b []byte) (line, bool) {
	if !utf8.Valid(b) || !json.Valid(b) {
		return line{}, false
	}
	var l line
	s := scanner{b: b}
	ok := s.lit(`{"id":`) && s.integer(&l.ID) &&
		s.lit(`,"process":`) && s.str(&l.Process) &&
		s.lit(`,"f":`) && s.str(&l.F) &&
		s.lit(`,"status":`) && s.str(&l.Status) &&
		s.lit(`,"call":`) && s.integer(&l.Call) &&
		s.lit(`,"return":`) && s.integer(&l.Return) &&
		s.lit(`,"call_index":`) && s.integer(&l.CallIndex) &&
		s.lit(`,"return_index":`) && s.integer(&l.ReturnIndex) &&
		s.lit(`,"input":`) && s.value(&l.Input) &&
		s.lit(`,"output":`) && s.value(&l.Output) &&
		s.lit(`}`) && s.i == len(b)
	return l, ok
}

// scanner reads the tokens of a valid JSON line b from position i.
type scanner struct {
	b []byte
	i int
}

// lit reads the bytes of t.
func (s *scanner) lit(t string) bool {
	if len(s.b)-s.i < len(t) || string(s.b[s.i:s.i+len(t)]) != t {
		return false
	}
	s.i += len(t)
	return true
}

// integer reads an integer of 1 to 18 digits, so it fits an int64.
func (s *scanner) integer(v *int64) bool {
	neg := s.i < len(s.b) && s.b[s.i] == '-'
	if neg {
		s.i++
	}
	start, n := s.i, int64(0)
	for ; s.i < len(s.b) && '0' <= s.b[s.i] && s.b[s.i] <= '9'; s.i++ {
		n = n*10 + int64(s.b[s.i]-'0')
	}
	if d := s.i - start; d == 0 || d > 18 {
		return false
	}
	if neg {
		n = -n
	}
	*v = n
	return true
}

// str reads a string without escapes.
func (s *scanner) str(v *string) bool {
	if s.i >= len(s.b) || s.b[s.i] != '"' {
		return false
	}
	n := bytes.IndexByte(s.b[s.i+1:], '"')
	if n < 0 || bytes.IndexByte(s.b[s.i+1:s.i+1+n], '\\') >= 0 {
		return false
	}
	*v = string(s.b[s.i+1 : s.i+1+n])
	s.i += n + 2
	return true
}

// value reads a JSON value, up to the ',' or '}' that ends it, if it has no space outside its
// strings.
func (s *scanner) value(v *json.RawMessage) bool {
	start, depth := s.i, 0
	for ; s.i < len(s.b); s.i++ {
		switch s.b[s.i] {
		case ' ', '\t', '\n', '\r':
			return false
		case '"':
			for s.i++; s.i < len(s.b) && s.b[s.i] != '"'; s.i++ {
				if s.b[s.i] == '\\' {
					s.i++
				}
			}
		case '{', '[':
			depth++
		case '}', ']', ',':
			if depth == 0 {
				*v = s.b[start:s.i:s.i]
				return true
			}
			if s.b[s.i] != ',' {
				depth--
			}
		}
	}
	return false
}

// checkIndices checks that the event indices are exactly 1..M, each used once (HIS-022).
func checkIndices(ops []Op) error {
	m := int64(len(ops))
	for _, op := range ops {
		if op.Status != Pending {
			m++
		}
	}
	owner := make([]int64, m+1) // index -> op ID; 0 = unused
	for _, op := range ops {
		idx := []int64{op.CallIndex}
		if op.ReturnIndex != 0 {
			idx = append(idx, op.ReturnIndex)
		}
		for _, v := range idx {
			if v < 1 || v > m {
				return fmt.Errorf("history: op %d: index %d out of range 1..%d", op.ID, v, m)
			}
			if owner[v] != 0 {
				return fmt.Errorf("history: index %d is used by op %d and op %d", v, owner[v], op.ID)
			}
			owner[v] = op.ID
		}
	}
	return nil
}

// checkProcesses checks that ops of one process do not overlap (HIS-022). It sorts the op indices
// by process name, stably, so the ops of each process stay in ID order, and compares neighbors:
// the first fault found is in the smallest process name, at its first pair by ID.
func checkProcesses(ops []Op) error {
	idx := make([]int, len(ops))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(i, j int) int { return strings.Compare(ops[i].Process, ops[j].Process) })
	for k := 1; k < len(idx); k++ {
		a, b := &ops[idx[k-1]], &ops[idx[k]]
		if a.Process != b.Process {
			continue
		}
		if a.Status == Pending {
			return fmt.Errorf("history: process %q: op %d invoked while op %d is pending", a.Process, b.ID, a.ID)
		}
		if a.ReturnIndex > b.CallIndex {
			return fmt.Errorf("history: process %q: op %d invoked before op %d completed", a.Process, b.ID, a.ID)
		}
	}
	return nil
}
