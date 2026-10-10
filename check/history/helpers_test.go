// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package history_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/hmdsefi/faultline/check/history"
)

func ids(ops []history.Op) []int64 {
	out := make([]int64, len(ops))
	for i, op := range ops {
		out[i] = op.ID
	}
	return out
}

// wrapped reports whether err's text is prefix followed by the text of the error it wraps.
func wrapped(err error, prefix string) bool {
	inner := errors.Unwrap(err)
	return inner != nil && err.Error() == prefix+inner.Error()
}

// AT-HIS-11, HIS-030, HIS-031, HIS-032
func TestHelpers(t *testing.T) {
	ops := at01(t).r.Ops()
	if got := history.Processes(ops); !slices.Equal(got, []string{"c1", "c2"}) {
		t.Fatalf("Processes = %v", got)
	}
	// HIS-030: sorted by byte order, not in order of first appearance.
	unsorted := []history.Op{{Process: "b"}, {Process: "a"}, {Process: "B"}, {Process: "b"}}
	if got := history.Processes(unsorted); !slices.Equal(got, []string{"B", "a", "b"}) {
		t.Fatalf("Processes = %v, want [B a b]", got)
	}
	if got := ids(history.ForProcess(ops, "c1")); !slices.Equal(got, []int64{1, 3}) {
		t.Fatalf("ForProcess(c1) = %v", got)
	}
	if history.ForProcess(ops, "zz") != nil || history.Processes(nil) != nil {
		t.Fatalf("empty results must be nil")
	}
	// HIS-031: a new slice, also when every op matches.
	all := []history.Op{{ID: 1, Process: "a"}, {ID: 2, Process: "a"}}
	got := history.ForProcess(all, "a")
	got[0].ID = 99
	if all[0].ID != 1 {
		t.Fatalf("ForProcess returned its input slice")
	}
	shuffled := []history.Op{ops[2], ops[0], ops[1]}
	shuffled[0].Call = ms(2)
	history.SortByCall(shuffled)
	if got := ids(shuffled); !slices.Equal(got, []int64{1, 2, 3}) {
		t.Fatalf("SortByCall = %v", got)
	}
	// HIS-032: Call decides first and ID breaks ties; neither ID nor CallIndex order alone gives 2, 3, 1.
	byCall := []history.Op{{ID: 1, Call: ms(3), CallIndex: 1}, {ID: 3, Call: ms(1), CallIndex: 2},
		{ID: 2, Call: ms(1), CallIndex: 3}}
	history.SortByCall(byCall)
	if got := ids(byCall); !slices.Equal(got, []int64{2, 3, 1}) {
		t.Fatalf("SortByCall = %v, want [2 3 1]", got)
	}
	// HIS-032: ops that tie on Call and are already in ID order stay in ID order.
	tied := []history.Op{{ID: 1, Call: ms(1)}, {ID: 2, Call: ms(1)}, {ID: 3, Call: ms(1)}}
	history.SortByCall(tied)
	if got := ids(tied); !slices.Equal(got, []int64{1, 2, 3}) {
		t.Fatalf("SortByCall = %v, want [1 2 3]", got)
	}
}

// AT-HIS-12, HIS-033
func TestDecoding(t *testing.T) {
	ops := at01(t).r.Ops()
	var m map[string]any
	if err := ops[0].DecodeInput(&m); err != nil || !reflect.DeepEqual(m, map[string]any{"key": "x", "value": float64(1)}) {
		t.Fatalf("DecodeInput into a map = %v, %v", m, err)
	}
	var kv struct {
		Key   string `json:"key"`
		Value int    `json:"value"`
	}
	if err := ops[0].DecodeInput(&kv); err != nil || kv.Key != "x" || kv.Value != 1 {
		t.Fatalf("DecodeInput into a struct = %+v, %v", kv, err)
	}
	// HIS-033: plain json.Unmarshal, which ignores the unknown key "value".
	var k struct {
		Key string `json:"key"`
	}
	if err := ops[0].DecodeInput(&k); err != nil || k.Key != "x" {
		t.Fatalf("DecodeInput into a struct without value = %+v, %v", k, err)
	}
	var n int
	if err := ops[1].DecodeOutput(&n); err != nil || n != 1 {
		t.Fatalf("DecodeOutput = %d, %v", n, err)
	}
	err := history.Op{ID: 9, Input: 5}.DecodeInput(&n)
	if err == nil || err.Error() != "history: op 9: input is int, not json.RawMessage" {
		t.Fatalf("non-raw input: %v", err)
	}
	err = history.Op{ID: 9, Output: "x"}.DecodeOutput(&n)
	if err == nil || err.Error() != "history: op 9: output is string, not json.RawMessage" {
		t.Fatalf("non-raw output: %v", err)
	}
	// HIS-033: only a json.RawMessage is decoded, not nil and not []byte.
	err = history.Op{ID: 9}.DecodeInput(&n)
	if err == nil || err.Error() != "history: op 9: input is <nil>, not json.RawMessage" {
		t.Fatalf("nil input: %v", err)
	}
	err = history.Op{ID: 9, Input: []byte("1")}.DecodeInput(&n)
	if err == nil || err.Error() != "history: op 9: input is []uint8, not json.RawMessage" {
		t.Fatalf("[]byte input: %v", err)
	}
	// Decode errors are exactly the prefix and the wrapped json error, whatever its Go-version text.
	err = ops[0].DecodeInput(&n)
	var typeErr *json.UnmarshalTypeError
	if !wrapped(err, "history: op 1: decode input: ") || !errors.As(err, &typeErr) {
		t.Fatalf("decode into int: %v", err)
	}
	// HIS-033: a DecodeOutput error says "decode output".
	var s string
	err = ops[1].DecodeOutput(&s)
	if !wrapped(err, "history: op 2: decode output: ") || !errors.As(err, &typeErr) {
		t.Fatalf("decode output into string: %v", err)
	}
	// An empty json.RawMessage goes to json.Unmarshal, which rejects it.
	err = history.Op{ID: 9, Input: json.RawMessage(nil)}.DecodeInput(&n)
	if !wrapped(err, "history: op 9: decode input: ") {
		t.Fatalf("empty raw input: %v", err)
	}
}
