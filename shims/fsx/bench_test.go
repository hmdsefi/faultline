// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package fsx_test

import (
	"context"
	"io"
	"os"
	"runtime/debug"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/shims/fsx"
)

var block4K = make([]byte, 4096)

// write4K is FSX §9's pattern: 4 KiB at increasing offsets, the file synced and reset every
// 1 MiB. The sync empties simdisk's list of unsynced writes, which a truncate alone does not.
func write4K(f fsx.File, i int) error {
	off := int64(i%256) * 4096
	if off == 0 {
		if err := f.Sync(); err != nil {
			return err
		}
		if err := f.Truncate(0); err != nil {
			return err
		}
	}
	_, err := f.WriteAt(block4K, off)
	return err
}

// TestWrite4KSyncsOnReset checks that write4K syncs before each reset, so simdisk's list of
// unsynced writes holds at most 1 MiB and the per-op cost does not grow with the op count.
func TestWrite4KSyncsOnReset(t *testing.T) {
	v := newVolume(t)
	f, err := fsx.Sim(v).Create("f")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for i := range 257 {
		if err := write4K(f, i); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := v.DurableSize("/f"); err != nil || got != 1<<20 {
		t.Fatalf("DurableSize after 257 writes = %d, %v; want %d, nil (the reset syncs the first 1 MiB)", got, err, 1<<20)
	}
}

func benchWrite4K(b *testing.B, fsys fsx.FS) {
	f, err := fsys.Create("f")
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	b.ResetTimer()
	for i := range b.N {
		if err := write4K(f, i); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSimDirectWrite4K(b *testing.B) { benchWrite4K(b, fsx.Sim(newVolume(b))) }

func BenchmarkOSWrite4K(b *testing.B) {
	fsys := fsx.OS(b.TempDir())
	if cl, ok := fsys.(io.Closer); ok {
		defer cl.Close()
	}
	benchWrite4K(b, fsys)
}

// raceBuild reports whether the test binary was built with -race (Go records the setting only
// when it was given).
func raceBuild() bool {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, s := range bi.Settings {
		if s.Key == "-race" {
			return s.Value == "true"
		}
	}
	return false
}

// TestFSXThroughput measures the goroutine-mode targets of FSX §9.
func TestFSXThroughput(t *testing.T) {
	if os.Getenv("FAULTLINE_PERF") != "1" {
		t.Skip("set FAULTLINE_PERF=1 to measure FSX §9 targets (without -race)")
	}
	if raceBuild() {
		t.Skip("FSX §9 targets are measured without -race, and this test binary was built with -race")
	}
	// measure times the whole run with the wall clock outside the bubble (time.Now inside a
	// synctest bubble is virtual); run setup is small next to the measured operations. It divides
	// the operations done, not ops: with FAULTLINE_CHECK_DETERMINISM=1 the seed runs twice inside
	// the one Run, and so do the operations.
	measure := func(t *testing.T, name string, ops int, target float64, op func(fsys fsx.FS, f fsx.File, i int) error) {
		var done atomic.Int64
		start := time.Now()
		faultline.Run(t, faultline.Options{Mode: faultline.ModeGoroutine, Seeds: 1, Duration: time.Second, Drain: time.Millisecond},
			func(w *faultline.World) {
				w.AddProcess("p", func(ctx context.Context, p *faultline.Process) error {
					f, err := p.FS.Create("f")
					if err != nil {
						return err
					}
					for i := range ops {
						if err := op(p.FS, f, i); err != nil {
							return err
						}
					}
					done.Add(int64(ops))
					<-ctx.Done()
					return nil
				})
			})
		rate := float64(done.Load()) / time.Since(start).Seconds()
		t.Logf("%s: %.0f ops/s (target >= %.0f)", name, rate, target)
		if rate < target {
			t.Errorf("%s: %.0f ops/s, want >= %.0f", name, rate, target)
		}
	}
	t.Run("SimDriverWrite4K", func(t *testing.T) {
		measure(t, "BenchmarkSimDriverWrite4K", 200_000, 200_000, func(_ fsx.FS, f fsx.File, i int) error {
			return write4K(f, i)
		})
	})
	t.Run("SimDriverOpenStatClose", func(t *testing.T) {
		measure(t, "BenchmarkSimDriverOpenStatClose", 150_000, 150_000, func(fsys fsx.FS, _ fsx.File, _ int) error {
			g, err := fsys.Open("f")
			if err != nil {
				return err
			}
			if _, err := g.Stat(); err != nil {
				return err
			}
			return g.Close()
		})
	})
}
