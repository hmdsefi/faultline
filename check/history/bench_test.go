// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package history_test

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/hmdsefi/faultline/check/history"
	"github.com/hmdsefi/faultline/kernel"
)

// kvWrite is the LIN-022 KV write input used by the benchmarks.
var kvWrite = map[string]any{"key": "x", "value": 2}

func BenchmarkInvokeComplete(b *testing.B) {
	r := history.NewRecorder(kernel.New(kernel.Config{Seed: 1}))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id := r.Invoke("p", "write", kvWrite)
		r.Complete(id, history.OK, nil)
	}
}

// recorder100k returns a recorder with 100,000 completed KV writes.
func recorder100k() *history.Recorder {
	r := history.NewRecorder(kernel.New(kernel.Config{Seed: 1}))
	for i := 0; i < 100000; i++ {
		r.Complete(r.Invoke("p", "write", kvWrite), history.OK, nil)
	}
	return r
}

func BenchmarkWriteJSONL100k(b *testing.B) {
	r := recorder100k()
	var buf bytes.Buffer
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		if err := r.WriteJSONL(&buf); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRead100k(b *testing.B) {
	var buf bytes.Buffer
	if err := recorder100k().WriteJSONL(&buf); err != nil {
		b.Fatal(err)
	}
	data := buf.Bytes()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := history.Read(bytes.NewReader(data)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkReadProcesses reads 100,000 completed KV writes spread over 1,000 and over 10,000
// processes. HIS §9 measures one process; Read's time must not grow with their number.
func BenchmarkReadProcesses(b *testing.B) {
	for _, procs := range []int{1000, 10000} {
		b.Run(fmt.Sprint(procs), func(b *testing.B) {
			r := history.NewRecorder(kernel.New(kernel.Config{Seed: 1}))
			for i := 0; i < 100000; i++ {
				r.Complete(r.Invoke(fmt.Sprintf("p%d", i%procs), "write", kvWrite), history.OK, nil)
			}
			var buf bytes.Buffer
			if err := r.WriteJSONL(&buf); err != nil {
				b.Fatal(err)
			}
			data := buf.Bytes()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := history.Read(bytes.NewReader(data)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
