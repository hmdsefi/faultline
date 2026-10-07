package simdisk_test

import (
	"math/rand/v2"
	"strconv"
	"testing"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simdisk"
)

// benchDisk returns a TraceHash Sim with one up node "a" and its volume.
func benchDisk(cfg simdisk.Config) (*kernel.Sim, *kernel.Node, *simdisk.Volume) {
	s := kernel.New(kernel.Config{Seed: 1})
	d := simdisk.New(s, cfg)
	a := s.AddNode("a", func(*kernel.Node) {})
	s.RunUntil(0)
	return s, a, d.Volume(a)
}

// walSize is the size at which the append benchmarks start their file over, so that no
// -benchtime takes it to MaxFileSize. The 100x and 2000x runs never reach it.
const walSize = 64 << 20

// warm appends p to f until the next append would pass walSize, syncing every 64 appends, then
// starts f over, all before the timer starts. The file's buffers then already hold walSize bytes
// and the allocator has memory to reuse, so at every -benchtime the append benchmarks measure the
// same regime: appends into memory the file already has.
func warm(b *testing.B, f *simdisk.File, p []byte) {
	for i := 0; f.Size()+int64(len(p)) <= walSize; i++ {
		if _, err := f.Append(p); err != nil {
			b.Fatal(err)
		}
		if i%64 == 63 {
			if err := f.Sync(); err != nil {
				b.Fatal(err)
			}
		}
	}
	if err := f.Truncate(0); err != nil {
		b.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		b.Fatal(err)
	}
}

// rewind truncates f to 0 and syncs it, with the timer stopped, when appending n more bytes would
// take f past walSize.
func rewind(b *testing.B, f *simdisk.File, n int) {
	if f.Size()+int64(n) <= walSize {
		return
	}
	b.StopTimer()
	if err := f.Truncate(0); err != nil {
		b.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		b.Fatal(err)
	}
	b.StartTimer()
}

func BenchmarkAppendSync128(b *testing.B) {
	_, _, v := benchDisk(simdisk.DefaultConfig())
	f, err := v.Create("/wal")
	if err != nil {
		b.Fatal(err)
	}
	p := make([]byte, 128)
	warm(b, f, p)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rewind(b, f, len(p))
		if _, err := f.Append(p); err != nil {
			b.Fatal(err)
		}
		if err := f.Sync(); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "pairs/s")
}

func BenchmarkAppend4K(b *testing.B) {
	_, _, v := benchDisk(simdisk.DefaultConfig())
	f, err := v.Create("/wal")
	if err != nil {
		b.Fatal(err)
	}
	p := make([]byte, 4096)
	warm(b, f, p)
	b.SetBytes(4096)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rewind(b, f, len(p))
		if _, err := f.Append(p); err != nil {
			b.Fatal(err)
		}
		if i%64 == 63 {
			if err := f.Sync(); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkReadAt4K(b *testing.B) {
	_, _, v := benchDisk(simdisk.DefaultConfig())
	f, err := v.Create("/data")
	if err != nil {
		b.Fatal(err)
	}
	const size = 64 << 20
	if _, err := f.WriteAt(make([]byte, size), 0); err != nil {
		b.Fatal(err)
	}
	r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // fixed seed: repeatable read offsets
	p := make([]byte, 4096)
	b.SetBytes(4096)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := f.ReadAt(p, int64(r.IntN(size-4096))); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCreateSyncDir: one op = Create + SyncDir("/d") in a directory of 1,000 files. The
// backlog row also leaves 10,000 creates pending in /e, which is never synced, so every SyncDir
// scans them and keeps them in the log.
func BenchmarkCreateSyncDir(b *testing.B) {
	for _, backlog := range []int{0, 10_000} {
		b.Run("backlog="+strconv.Itoa(backlog), func(b *testing.B) {
			_, _, v := benchDisk(simdisk.DefaultConfig())
			if err := v.Mkdir("/d"); err != nil {
				b.Fatal(err)
			}
			for i := 0; i < 1000; i++ {
				if _, err := v.Create("/d/f" + strconv.Itoa(i)); err != nil {
					b.Fatal(err)
				}
			}
			if err := v.SyncDir("/d"); err != nil {
				b.Fatal(err)
			}
			if backlog > 0 {
				if err := v.Mkdir("/e"); err != nil {
					b.Fatal(err)
				}
				for i := 0; i < backlog; i++ {
					if _, err := v.Create("/e/f" + strconv.Itoa(i)); err != nil {
						b.Fatal(err)
					}
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := v.Create("/d/x" + strconv.Itoa(i)); err != nil {
					b.Fatal(err)
				}
				if err := v.SyncDir("/d"); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "ops/s")
		})
	}
}

// BenchmarkCrash: 100 durable files with 10 pending 4 KiB writes each, CrashAny, MetadataStrict
// with 100 pending namespace ops; one op = crash + restart.
func BenchmarkCrash(b *testing.B) {
	p := make([]byte, 4096)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		s, a, v := benchDisk(simdisk.DefaultConfig())
		if err := v.Mkdir("/d"); err != nil {
			b.Fatal(err)
		}
		files := make([]*simdisk.File, 100)
		for j := range files {
			f, err := v.Create("/d/f" + strconv.Itoa(j))
			if err != nil {
				b.Fatal(err)
			}
			files[j] = f
		}
		if err := v.SyncDir("/d"); err != nil {
			b.Fatal(err)
		}
		if err := v.SyncDir("/"); err != nil {
			b.Fatal(err)
		}
		for _, f := range files {
			for w := 0; w < 10; w++ {
				if _, err := f.Append(p); err != nil {
					b.Fatal(err)
				}
			}
		}
		for j := 0; j < 100; j++ {
			if err := v.Mkdir("/m" + strconv.Itoa(j)); err != nil {
				b.Fatal(err)
			}
		}
		b.StartTimer()
		a.Crash()
		a.Restart()
		s.RunFor(0)
	}
}
