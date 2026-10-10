// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package history_test

import (
	"fmt"
	"os"
	"time"

	"github.com/hmdsefi/faultline/check/history"
	"github.com/hmdsefi/faultline/kernel"
)

// Two clients write and read a key. The write is acknowledged; the read times out, so its outcome
// is unknown and it completes with Info. WriteJSONL prints the history.jsonl form.
func ExampleRecorder() {
	s := kernel.New(kernel.Config{Seed: 1})
	h := history.NewRecorder(s)
	type kv struct {
		Key   string `json:"key"`
		Value string `json:"value,omitempty"`
	}

	at := func(ms int, fn func()) { s.At(kernel.Time(time.Duration(ms)*time.Millisecond), "client", fn) }

	var write, read int64
	at(10, func() { write = h.Invoke("c1", "write", kv{"k1", "v1"}) })
	at(15, func() { read = h.Invoke("c2", "read", kv{Key: "k1"}) })
	at(20, func() { h.Complete(write, history.OK, nil) })
	at(515, func() { h.Complete(read, history.Info, nil) }) // no reply within 500 ms
	s.Run()

	if err := h.WriteJSONL(os.Stdout); err != nil {
		fmt.Println(err)
	}
	// Output:
	// {"faultline_history":1,"ops":2}
	// {"id":1,"process":"c1","f":"write","status":"ok","call":10000000,"return":20000000,"call_index":1,"return_index":3,"input":{"key":"k1","value":"v1"},"output":null}
	// {"id":2,"process":"c2","f":"read","status":"info","call":15000000,"return":515000000,"call_index":2,"return_index":4,"input":{"key":"k1"},"output":null}
}
