// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simdisk_test

import (
	"encoding/binary"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/internal/golden"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simdisk"
)

// walWorkload builds and runs the AT-DSK-22 scenario and returns the Sim, the final /wal content
// and the stop reason. With sample set, the boot also calls SampleLatency(OpWrite) before every
// append.
func walWorkload(t testing.TB, seed uint64, trace kernel.TraceConfig, cfg simdisk.Config, sample bool) (*kernel.Sim, string, kernel.StopReason) {
	s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
	d := simdisk.New(s, cfg)
	a := s.AddNode("a", func(n *kernel.Node) {
		v := d.Volume(n)
		var f *simdisk.File
		var err error
		if _, serr := v.Stat("/wal"); errors.Is(serr, simdisk.ErrNotExist) {
			f, err = v.Create("/wal")
		} else {
			f, err = v.Open("/wal")
		}
		if err != nil {
			s.Fail(err)
			return
		}
		count := 0
		var tick func()
		tick = func() {
			count++
			if sample {
				v.SampleLatency(simdisk.OpWrite)
			}
			buf := make([]byte, 64)
			for i := 0; i < 64; i += 8 {
				binary.LittleEndian.PutUint64(buf[i:], n.Rand().Uint64())
			}
			if _, err := f.Append(buf); err != nil {
				s.Fail(err)
				return
			}
			if count%3 == 0 {
				if err := f.Sync(); err != nil {
					s.Fail(err)
					return
				}
			}
			if count%10 == 0 {
				if err := v.SyncDir("/"); err != nil {
					s.Fail(err)
					return
				}
			}
			n.After(time.Millisecond, "append", tick)
		}
		n.After(time.Millisecond, "append", tick)
	})
	ms := func(x int) kernel.Time { return kernel.Time(time.Duration(x) * time.Millisecond) }
	s.At(ms(35), "crash", a.Crash)
	s.At(ms(40), "restart", a.Restart)
	s.At(ms(77), "crash", a.Crash)
	s.At(ms(80), "restart", a.Restart)
	s.At(ms(85), "corrupt", func() { _ = d.Volume(a).Corrupt("/wal", 10, 4) })
	stop := s.RunUntil(ms(100))
	if s.Err() != nil {
		t.Fatalf("seed %d: %v", seed, s.Err())
	}
	wal, _ := d.Volume(a).ReadFile("/wal")
	return s, string(wal), stop
}

// faultWorkload is a second determinism scenario beside AT-DSK-22's, under DefaultConfig. It
// reaches what that one does not: two files with pending data at every crash (traversal order),
// namespace ops pending at every crash (the MetadataStrict draw), and one forced sync failure per
// incarnation, tolerated as ErrIO. It crashes three times, an odd number, so a hidden state that
// alternates from crash to crash also differs between two runs.
func faultWorkload(t testing.TB, seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
	s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
	d := simdisk.New(s, simdisk.DefaultConfig())
	failed := func(err error) bool { // ErrIO is the forced sync failure
		if err != nil && !errors.Is(err, simdisk.ErrIO) {
			s.Fail(err)
			return true
		}
		return false
	}
	a := s.AddNode("a", func(n *kernel.Node) {
		v := d.Volume(n)
		if failed(v.MkdirAll("/d")) {
			return
		}
		var files []*simdisk.File
		for _, p := range []string{"/w", "/d/w"} {
			f, err := v.Open(p)
			if errors.Is(err, simdisk.ErrNotExist) {
				f, err = v.Create(p)
			}
			if failed(err) {
				return
			}
			files = append(files, f)
		}
		v.FailSyncs(1)
		count := 0
		var tick func()
		tick = func() {
			count++
			for _, f := range files {
				if _, err := f.Append([]byte{byte(n.Rand().Uint64())}); failed(err) { //nolint:gosec // any byte will do: truncation is intended
					return
				}
			}
			if count%3 == 0 { // a new file: a namespace op that stays pending until the next SyncDir
				name := "/d/x" + strconv.Itoa(int(n.Incarnation())) + "-" + strconv.Itoa(count)
				if _, err := v.Create(name); failed(err) {
					return
				}
			}
			if count%4 == 0 {
				for _, f := range files {
					if failed(f.Sync()) {
						return
					}
				}
			}
			if count%10 == 0 && (failed(v.SyncDir("/")) || failed(v.SyncDir("/d"))) {
				return
			}
			n.After(time.Millisecond, "tick", tick)
		}
		n.After(time.Millisecond, "tick", tick)
	})
	ms := func(x int) kernel.Time { return kernel.Time(time.Duration(x) * time.Millisecond) }
	for _, at := range []int{23, 51, 79} { // 23 ms after each boot: an op of each kind is pending
		s.At(ms(at), "crash", a.Crash)
		s.At(ms(at+5), "restart", a.Restart)
	}
	stop := s.RunUntil(ms(100))
	if s.Err() != nil {
		t.Fatalf("seed %d: %v", seed, s.Err())
	}
	return s, stop
}

// AT-DSK-22
func TestDeterminism(t *testing.T) {
	cfg := simdisk.DefaultConfig()
	s1, w1, _ := walWorkload(t, 1, kernel.TraceConfig{}, cfg, false)
	s2, w2, _ := walWorkload(t, 1, kernel.TraceConfig{}, cfg, false)
	if s1.TraceHash() != s2.TraceHash() || w1 != w2 {
		t.Fatalf("seed 1 twice: hashes %#x %#x, wal equal %v", s1.TraceHash(), s2.TraceHash(), w1 == w2)
	}
	if len(w1) == 0 {
		t.Fatalf("empty wal: the workload did not run")
	}
	s3, _, _ := walWorkload(t, 2, kernel.TraceConfig{}, cfg, false)
	if s3.TraceHash() == s1.TraceHash() {
		t.Fatalf("seeds 1 and 2 have the same hash")
	}
	lat := cfg
	lat.Latency = simdisk.DefaultLatency()
	s4, w4, _ := walWorkload(t, 1, kernel.TraceConfig{}, lat, true)
	if s4.TraceHash() != s1.TraceHash() || w4 != w1 {
		t.Fatalf("SampleLatency changed the run: %#x vs %#x", s4.TraceHash(), s1.TraceHash())
	}
	golden.Check(t, 1, func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		s, _, stop := walWorkload(t, seed, trace, cfg, false)
		return s, stop
	})

	// the fault workload: the same hash twice, and every path it is there for is reached
	f1, _ := faultWorkload(t, 1, kernel.TraceConfig{Level: kernel.TraceFull})
	f2, _ := faultWorkload(t, 1, kernel.TraceConfig{Level: kernel.TraceFull})
	if f1.TraceHash() != f2.TraceHash() {
		t.Fatalf("fault workload, seed 1 twice: hashes %#x %#x", f1.TraceHash(), f2.TraceHash())
	}
	metaDraws, syncFails, applies := 0, 0, map[kernel.Time]int{}
	for _, r := range f1.Records() {
		switch r.Kind {
		case "disk.crash_meta":
			if attr(r, "pending") != "0" {
				metaDraws++
			}
		case "disk.sync_fail", "disk.sync_dir_fail":
			syncFails++
		case "disk.crash_apply":
			applies[r.At]++
		}
	}
	twoFiles := 0
	for _, n := range applies {
		if n >= 2 {
			twoFiles++
		}
	}
	if metaDraws != 3 || syncFails == 0 || twoFiles != 3 {
		t.Fatalf("fault workload: of 3 crashes, %d with pending namespace ops and %d with two files with pending data; %d failed syncs", metaDraws, twoFiles, syncFails)
	}
	golden.Check(t, 1, func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		return faultWorkload(t, seed, trace)
	})
}
