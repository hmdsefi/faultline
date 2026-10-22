// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
	"github.com/hmdsefi/faultline/kernel/simdisk"
	"github.com/hmdsefi/faultline/shims/fsx"
)

func init() { //nolint:gochecknoinits // registers the child-process scenarios
	gorScenarios["fsx-paused-on-driver"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 2 * time.Second}, func(w *World) {
			n := w.AddProcess("p1", func(ctx context.Context, p *Process) error { <-ctx.Done(); return nil })
			w.Plan(fault.Script(fault.Event{At: vt(time.Second), Kind: fault.KindPause, Node: "p1"}))
			w.Final("stat paused", func() error {
				_, err := w.Process(n.ID()).FS.Stat("/")
				return err
			})
		})
	}
}

func TestATFSX03_ConformanceInGoroutineMode(t *testing.T) {
	cases := fsx.Conformance()
	var mu sync.Mutex
	results := map[string]error{}
	gw(t, Options{Duration: time.Second}, func(w *World) {
		for i, c := range cases {
			w.AddProcess(fmt.Sprintf("c%02d", i+1), func(ctx context.Context, p *Process) error {
				// Each case starts in its own millisecond: without latency a case runs entirely in
				// one quiescence window, so cases of different nodes never interleave and the trace
				// is the same under FAULTLINE_CHECK_DETERMINISM=1.
				time.Sleep(time.Duration(i) * time.Millisecond)
				err := c.Run(p.FS)
				mu.Lock()
				results[c.Name] = err
				mu.Unlock()
				<-ctx.Done()
				return nil
			})
		}
	})
	for _, c := range cases {
		if err, ok := results[c.Name]; !ok || err != nil {
			t.Errorf("%s: ran %v, error %v", c.Name, ok, err)
		}
	}
}

// fsHandoff carries incarnation 1's FS and handle to incarnation 2 (AT-FSX-04's "package variable").
type fsHandoff struct {
	fs fsx.FS
	h  fsx.File
}

func TestATFSX04_StaleAfterRestart(t *testing.T) {
	var oldOpen, oldRead atomic.Value
	var newRead atomic.Value // string "<content>|<error>"
	gw(t, Options{Duration: 3 * time.Second}, func(w *World) {
		var handoff atomic.Pointer[fsHandoff]
		w.AddProcess("p", func(ctx context.Context, p *Process) error {
			if p.Incarnation() == 1 {
				f, err := p.FS.Create("a")
				if err != nil {
					return err
				}
				if _, err := f.Write([]byte("x")); err != nil {
					return err
				}
				if err := p.FS.SyncDir("/"); err != nil {
					return err
				}
				if err := f.Sync(); err != nil {
					return err
				}
				handoff.Store(&fsHandoff{fs: p.FS, h: f})
				<-ctx.Done()
				return nil
			}
			old := handoff.Load()
			_, err := old.fs.Open("a")
			oldOpen.Store(errBox{err})
			_, err = old.h.ReadAt(make([]byte, 1), 0)
			oldRead.Store(errBox{err})
			b, err := fsx.ReadFile(p.FS, "a")
			newRead.Store(fmt.Sprintf("%s|%v", b, err))
			<-ctx.Done()
			return nil
		})
		w.Plan(fault.Script(
			fault.Event{At: vt(time.Second), Kind: fault.KindCrash, Node: "p"},
			fault.Event{At: vt(2 * time.Second), Kind: fault.KindRestart, Node: "p"},
		))
	})
	for name, v := range map[string]*atomic.Value{"fs1.Open": &oldOpen, "h.ReadAt": &oldRead} {
		box, _ := v.Load().(errBox)
		if !errors.Is(box.err, syscall.ESTALE) || !errors.Is(box.err, simdisk.ErrStale) {
			t.Fatalf("%s = %v, want ESTALE", name, box.err)
		}
	}
	if s, _ := newRead.Load().(string); s != "x|<nil>" {
		t.Fatalf("incarnation 2 read %q, want x|<nil>", s)
	}
}

func TestATFSX05_CrashLosesUnsynced(t *testing.T) {
	var got atomic.Value
	gw(t, Options{Duration: 3 * time.Second, Disk: simdisk.Config{Crash: simdisk.CrashLoseUnsynced, Metadata: simdisk.MetadataImmediate}},
		func(w *World) {
			w.AddProcess("p", func(ctx context.Context, p *Process) error {
				if p.Incarnation() == 1 {
					f, err := p.FS.Create("f")
					if err != nil {
						return err
					}
					f.Close()
					sf, err := p.FS.OpenFile("f", os.O_WRONLY|os.O_SYNC, 0)
					if err != nil {
						return err
					}
					if _, err := sf.Write([]byte("abc")); err != nil {
						return err
					}
					if err := fsx.WriteFile(p.FS, "g", []byte("xyz"), 0o644); err != nil {
						return err
					}
					<-ctx.Done()
					return nil
				}
				f, ferr := fsx.ReadFile(p.FS, "f")
				g, gerr := fsx.ReadFile(p.FS, "g")
				got.Store(fmt.Sprintf("f=%q %v g=%q %v", f, ferr, g, gerr))
				<-ctx.Done()
				return nil
			})
			w.Plan(fault.Script(
				fault.Event{At: vt(time.Second), Kind: fault.KindCrash, Node: "p"},
				fault.Event{At: vt(2 * time.Second), Kind: fault.KindRestart, Node: "p"},
			))
		})
	if s, _ := got.Load().(string); s != `f="abc" <nil> g="" <nil>` {
		t.Fatalf("after restart: %s", s)
	}
}

func TestATFSX06_Latency(t *testing.T) {
	run := func(t *testing.T, cfg simdisk.Config) ([2]time.Duration, []kernel.Record) {
		var d [2]atomic.Int64
		sink := newRecordSink()
		gw(t, fullTrace(Options{Duration: time.Second, Disk: cfg}), func(w *World) {
			w.AddProcess("p", func(ctx context.Context, p *Process) error {
				f, err := p.FS.Create("f")
				if err != nil {
					return err
				}
				t0 := time.Now()
				if _, err := f.Write([]byte("hello")); err != nil {
					return err
				}
				d[0].Store(int64(time.Since(t0)))
				t1 := time.Now()
				if _, err := f.ReadAt(make([]byte, 5), 0); err != nil && err != io.EOF {
					return err
				}
				d[1].Store(int64(time.Since(t1)))
				<-ctx.Done()
				return nil
			})
			sink.capture(w)
		})
		return [2]time.Duration{time.Duration(d[0].Load()), time.Duration(d[1].Load())}, ofKind(sink.only(t), "fsx.latency")
	}
	t.Run("write latency", func(t *testing.T) {
		cfg := simdisk.DefaultConfig()
		cfg.Latency.Write = simdisk.Latency{Base: 2 * time.Millisecond}
		d, recs := run(t, cfg)
		if d != [2]time.Duration{2 * time.Millisecond, 0} {
			t.Fatalf("Write took %v, Read %v; want 2ms and 0", d[0], d[1])
		}
		if len(recs) != 1 || attr(recs[0], "latency_ns") != "2000000" || attr(recs[0], "class") != "write" ||
			attr(recs[0], "op") != "write" || attr(recs[0], "path") != "/f" || recs[0].Text != "write /f: 2ms" {
			t.Fatalf("fsx.latency %+v", recs)
		}
	})
	t.Run("zero latency", func(t *testing.T) {
		if _, recs := run(t, simdisk.DefaultConfig()); len(recs) != 0 {
			t.Fatalf("fsx.latency records with the zero latency config: %+v", recs)
		}
	})
}

func TestATFSX07_PausedWriteWaits(t *testing.T) {
	var returned atomic.Int64
	sink := newRecordSink()
	gw(t, fullTrace(Options{Duration: 3 * time.Second}), func(w *World) {
		w.AddProcess("p", func(ctx context.Context, p *Process) error {
			f, err := p.FS.Create("f")
			if err != nil {
				return err
			}
			tm := time.NewTimer(1500 * time.Millisecond)
			<-tm.C
			if _, err := f.Write([]byte("x")); err != nil {
				return err
			}
			returned.Store(int64(time.Since(epoch)))
			<-ctx.Done()
			return nil
		})
		w.Plan(fault.Script(
			fault.Event{At: vt(time.Second), Kind: fault.KindPause, Node: "p"},
			fault.Event{At: vt(2 * time.Second), Kind: fault.KindResume, Node: "p"},
		))
		sink.capture(w)
	})
	if time.Duration(returned.Load()) != 2*time.Second {
		t.Fatalf("Write returned at %v, want 2s", time.Duration(returned.Load()))
	}
	writes := ofKind(sink.only(t), "disk.write")
	if len(writes) != 1 || writes[0].At != vt(2*time.Second) {
		t.Fatalf("disk.write %+v, want one at 2s", writes)
	}
}

// FSX-012: an operation waiting for a paused node fails with ESTALE when the node crashes or the
// run ends first. After a crash nothing is applied to the volume: no disk.create record follows.
func TestGoroutineFSStaleDuringPause(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []fault.Event
		at     time.Duration // when Create returns
	}{
		{"crash", []fault.Event{
			{At: vt(time.Second), Kind: fault.KindPause, Node: "p"},
			{At: vt(1500 * time.Millisecond), Kind: fault.KindCrash, Node: "p"},
		}, 1500 * time.Millisecond},
		{"run end", []fault.Event{{At: vt(time.Second), Kind: fault.KindPause, Node: "p"}}, 2 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got atomic.Value
			var returned atomic.Int64
			sink := newRecordSink()
			gw(t, fullTrace(Options{Duration: 2 * time.Second}), func(w *World) {
				w.AddProcess("p", func(ctx context.Context, p *Process) error {
					tm := time.NewTimer(1200 * time.Millisecond)
					<-tm.C
					_, err := p.FS.Create("g")
					got.Store(errBox{err})
					returned.Store(int64(time.Since(epoch)))
					<-ctx.Done()
					return nil
				})
				w.Plan(fault.Script(tc.events...))
				sink.capture(w)
			})
			box, _ := got.Load().(errBox)
			var pe *fs.PathError
			if !errors.As(box.err, &pe) || pe.Op != "open" || pe.Path != "g" ||
				!errors.Is(box.err, syscall.ESTALE) || !errors.Is(box.err, simdisk.ErrStale) {
				t.Fatalf("Create = %v, want *fs.PathError open g ESTALE", box.err)
			}
			if time.Duration(returned.Load()) != tc.at {
				t.Fatalf("Create returned at %v, want %v", time.Duration(returned.Load()), tc.at)
			}
			// The sink copies the records in a final check, which runs before the run's end wakes
			// the waiter, so only the crash case can see what the waiting operation did.
			if tc.name != "crash" {
				return
			}
			if cr := ofKind(sink.only(t), "disk.create"); len(cr) != 0 {
				t.Fatalf("disk.create %+v, want none", cr)
			}
		})
	}
}

func TestATFSX08_InjectedErrors(t *testing.T) {
	var syncErr, writeErr atomic.Value
	var written atomic.Int64
	gw(t, Options{Duration: time.Second}, func(w *World) {
		w.AddProcess("p", func(ctx context.Context, p *Process) error {
			// The faults fire at t=0; starting at 1 ms makes them precede every operation
			// whatever the order of the boot and fault events at t=0.
			time.Sleep(time.Millisecond)
			f, err := p.FS.Create("f")
			if err != nil {
				return err
			}
			n, err := f.Write(make([]byte, 20))
			written.Store(int64(n))
			writeErr.Store(errBox{err})
			syncErr.Store(errBox{f.Sync()})
			<-ctx.Done()
			return nil
		})
		w.Plan(fault.Script(
			fault.Event{At: 0, Kind: fault.KindSyncFail, Node: "p", N: 1},
			fault.Event{At: 0, Kind: fault.KindDiskCapacity, Node: "p", N: 10},
		))
	})
	if box, _ := syncErr.Load().(errBox); !errors.Is(box.err, syscall.EIO) || !errors.Is(box.err, simdisk.ErrIO) {
		t.Fatalf("Sync = %v, want EIO", box.err)
	}
	if box, _ := writeErr.Load().(errBox); written.Load() != 10 || !errors.Is(box.err, syscall.ENOSPC) || !errors.Is(box.err, simdisk.ErrNoSpace) {
		t.Fatalf("Write = %d, %v; want 10, ENOSPC", written.Load(), box.err)
	}
}

func TestATFSX12_MutexAcrossFileIO(t *testing.T) {
	var writes atomic.Int64
	gw(t, Options{Duration: time.Second}, func(w *World) {
		writes.Store(0) // the body runs twice under FAULTLINE_CHECK_DETERMINISM=1
		w.AddProcess("p", func(ctx context.Context, p *Process) error {
			f, err := p.FS.Create("f")
			if err != nil {
				return err
			}
			var mu sync.Mutex
			p.Go(func() { // B
				for ctx.Err() == nil {
					mu.Lock()
					mu.Unlock() //nolint:staticcheck // SA2001: B only waits for the lock A holds
					time.Sleep(time.Millisecond)
				}
			})
			for i := range 100 { // A
				mu.Lock()
				for j := range 10 {
					if _, err := f.WriteAt([]byte("x"), int64(i*10+j)); err == nil {
						writes.Add(1)
					}
				}
				mu.Unlock()
				time.Sleep(time.Millisecond)
			}
			<-ctx.Done()
			return nil
		})
	})
	if writes.Load() != 1000 {
		t.Fatalf("A finished %d writes, want 1000", writes.Load())
	}
}

func TestATFSX13_AppendSyncDeterminism(t *testing.T) {
	gw(t, Options{Seeds: 20, CheckDeterminism: true, Duration: time.Second}, func(w *World) {
		for k, name := range []string{"a", "b", "c"} {
			w.AddProcess(name, func(ctx context.Context, p *Process) error {
				f, err := p.FS.OpenFile("log", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
				if err != nil {
					return err
				}
				rec := make([]byte, 100)
				// Process k starts its ticker k ms late, so no two processes wake at one instant:
				// goroutines woken together run in an order that differs between runs, with or
				// without -race.
				time.Sleep(time.Duration(k) * time.Millisecond)
				tk := time.NewTicker(10 * time.Millisecond)
				defer tk.Stop()
				for {
					select {
					case <-ctx.Done():
						return nil
					case <-tk.C:
						if _, err := f.Write(rec); err != nil {
							return nil
						}
						if err := f.Sync(); err != nil {
							return nil
						}
					}
				}
			})
		}
	})
}

func TestATFSX14_FinalCheckReadsProcessFS(t *testing.T) {
	var seen atomic.Value
	gw(t, Options{Duration: time.Second}, func(w *World) {
		n := w.AddProcess("p", func(ctx context.Context, p *Process) error {
			for i := range 5 {
				if err := fsx.WriteFile(p.FS, "state", []byte(fmt.Sprintf("v%d", i)), 0o644); err != nil {
					return err
				}
				time.Sleep(100 * time.Millisecond)
			}
			<-ctx.Done()
			return nil
		})
		w.Final("read state", func() error {
			b, err := fsx.ReadFile(w.Process(n.ID()).FS, "state")
			seen.Store(fmt.Sprintf("%s|%v", b, err))
			return err
		})
	})
	if s, _ := seen.Load().(string); s != "v4|<nil>" {
		t.Fatalf("final check read %q, want v4", s)
	}
}

// FSX-011 and FSX-013: an operation that no kernel event precedes is recorded at its own instant.
func TestGoroutineFSRecordsAtCurrentTime(t *testing.T) {
	sink := newRecordSink()
	gw(t, fullTrace(Options{Duration: 2 * time.Second}), func(w *World) {
		w.AddProcess("p", func(ctx context.Context, p *Process) error {
			time.Sleep(1500 * time.Millisecond)
			f, err := p.FS.Create("f")
			if err != nil {
				return err
			}
			f.Close()
			<-ctx.Done()
			return nil
		})
		sink.capture(w)
	})
	if cr := ofKind(sink.only(t), "disk.create"); len(cr) != 1 || cr[0].At != vt(1500*time.Millisecond) {
		t.Fatalf("disk.create %+v, want one at 1.5s", cr)
	}
}

// FSX §8, FSX-050 and FSX-051: each fsx.latency record follows its operation's last disk.* record,
// is caused by it (KRN's default), carries the volume's node and the bound incarnation, the op
// name, the clean path, the latency class (sync for an O_SYNC write) and the latency in its
// attributes, in that order, and each operation draws once from the latency stream.
func TestGoroutineFSLatencyRecord(t *testing.T) {
	cfg := simdisk.DefaultConfig()
	cfg.Latency.Write = simdisk.Latency{Jitter: 10 * time.Millisecond}
	cfg.Latency.Sync = simdisk.Latency{Base: 3 * time.Millisecond} // Base only: no draw
	var seed atomic.Uint64
	var id atomic.Int64
	sink := newRecordSink()
	gw(t, fullTrace(Options{Duration: time.Second, Disk: cfg}), func(w *World) {
		seed.Store(w.Seed())
		n := w.AddProcess("p", func(ctx context.Context, p *Process) error {
			f, err := p.FS.Create("f")
			if err != nil {
				return err
			}
			for _, off := range []int64{0, 5} {
				if _, err := f.WriteAt([]byte("hello"), off); err != nil {
					return err
				}
			}
			s, err := p.FS.OpenFile("s", os.O_WRONLY|os.O_CREATE|os.O_SYNC, 0o644)
			if err != nil {
				return err
			}
			if _, err := s.Write([]byte("x")); err != nil {
				return err
			}
			<-ctx.Done()
			return nil
		})
		id.Store(int64(n.ID()))
		sink.capture(w)
	})
	// The two WriteAt latencies are the first two write draws of disk/p/latency for this seed.
	ks := kernel.New(kernel.Config{Seed: seed.Load()})
	kv := simdisk.New(ks, cfg).Volume(ks.AddNode("p", func(*kernel.Node) {}))
	w1 := kv.SampleLatency(simdisk.OpWrite)
	w2 := kv.SampleLatency(simdisk.OpWrite)
	type want struct {
		prev, op, path, class string
		lat                   time.Duration
	}
	var wants []want
	for _, wt := range []want{
		{"disk.write", "writeat", "/f", "write", w1},
		{"disk.write", "writeat", "/f", "write", w2},
		{"disk.sync", "write", "/s", "sync", 3 * time.Millisecond},
	} {
		if wt.lat > 0 { // a zero latency has no record
			wants = append(wants, wt)
		}
	}
	rs := sink.only(t)
	var at []int
	for i, r := range rs {
		if r.Kind == "fsx.latency" {
			at = append(at, i)
		}
	}
	if len(at) != len(wants) {
		t.Fatalf("%d fsx.latency records, want %d (write draws %v, %v)", len(at), len(wants), w1, w2)
	}
	for k, wt := range wants {
		i := at[k]
		r := rs[i]
		attrs := []kernel.Attr{{Key: "op", Value: wt.op}, {Key: "path", Value: wt.path},
			{Key: "class", Value: wt.class}, {Key: "latency_ns", Value: strconv.FormatInt(int64(wt.lat), 10)}}
		if int64(r.Node) != id.Load() || r.Inc != 1 || r.Text != wt.op+" "+wt.path+": "+wt.lat.String() ||
			!reflect.DeepEqual(r.Attrs, attrs) || rs[i-1].Kind != wt.prev || r.Cause != rs[i-1].Seq {
			t.Fatalf("record %d: %+v after %s %d; want attrs %+v after %s, caused by it", i, r, rs[i-1].Kind, rs[i-1].Seq, attrs, wt.prev)
		}
	}
}

// FSX-050 and FSX-051: an operation that simdisk fails was applied, so it draws from the latency
// stream like any other. An operation that fails FSX-030's checks, is skipped by them (a
// zero-length write) or runs on a dead incarnation draws nothing.
func TestGoroutineFSFailedOpsSampling(t *testing.T) {
	cfg := simdisk.DefaultConfig()
	cfg.Latency.Write = simdisk.Latency{Jitter: 10 * time.Millisecond}
	var seed atomic.Uint64
	var id atomic.Int64
	sink := newRecordSink()
	gw(t, fullTrace(Options{Duration: 3 * time.Second, Disk: cfg}), func(w *World) {
		seed.Store(w.Seed())
		var handoff atomic.Pointer[fsHandoff]
		n := w.AddProcess("p", func(ctx context.Context, p *Process) error {
			if p.Incarnation() == 1 {
				f, err := p.FS.Create("f")
				if err != nil {
					return err
				}
				r, err := p.FS.Open("f")
				if err != nil {
					return err
				}
				// FSX-030's checks fail a write on a read-only handle and skip a zero-length write.
				if _, err := r.Write([]byte("x")); !errors.Is(err, syscall.EBADF) {
					return fmt.Errorf("write on a read-only handle: %v, want EBADF", err)
				}
				if k, err := f.WriteAt(nil, 0); k != 0 || err != nil {
					return fmt.Errorf("zero-length writeat: %d, %v; want 0, nil", k, err)
				}
				// The checks pass and simdisk fails the write.
				if _, err := f.WriteAt([]byte("x"), simdisk.MaxFileSize); !errors.Is(err, syscall.EFBIG) {
					return fmt.Errorf("writeat at simdisk.MaxFileSize: %v, want EFBIG", err)
				}
				if _, err := f.WriteAt([]byte("y"), 0); err != nil {
					return err
				}
				handoff.Store(&fsHandoff{fs: p.FS, h: f})
				<-ctx.Done()
				return nil
			}
			if _, err := handoff.Load().h.WriteAt([]byte("z"), 0); !errors.Is(err, syscall.ESTALE) {
				return fmt.Errorf("writeat on incarnation 1's handle: %v, want ESTALE", err)
			}
			g, err := p.FS.Create("g")
			if err != nil {
				return err
			}
			if _, err := g.WriteAt([]byte("w"), 0); err != nil {
				return err
			}
			<-ctx.Done()
			return nil
		})
		id.Store(int64(n.ID()))
		w.Plan(fault.Script(
			fault.Event{At: vt(time.Second), Kind: fault.KindCrash, Node: "p"},
			fault.Event{At: vt(2 * time.Second), Kind: fault.KindRestart, Node: "p"},
		))
		sink.capture(w)
	})
	// The first three write draws of disk/p/latency for this seed: the failed write takes the
	// first, the next write the second, and incarnation 2's write the third.
	ks := kernel.New(kernel.Config{Seed: seed.Load()})
	kv := simdisk.New(ks, cfg).Volume(ks.AddNode("p", func(*kernel.Node) {}))
	type want struct {
		inc  uint32
		path string
		lat  time.Duration
	}
	var wants []want
	for _, wt := range []want{{1, "/f", 0}, {1, "/f", 0}, {2, "/g", 0}} {
		wt.lat = kv.SampleLatency(simdisk.OpWrite)
		if wt.lat > 0 { // a zero latency has no record
			wants = append(wants, wt)
		}
	}
	rs := sink.only(t)
	var at []int
	for i, r := range rs {
		if r.Kind == "fsx.latency" {
			at = append(at, i)
		}
	}
	if len(at) != len(wants) {
		t.Fatalf("%d fsx.latency records, want %d: %+v", len(at), len(wants), ofKind(rs, "fsx.latency"))
	}
	for k, wt := range wants {
		i := at[k]
		r := rs[i]
		attrs := []kernel.Attr{{Key: "op", Value: "writeat"}, {Key: "path", Value: wt.path},
			{Key: "class", Value: "write"}, {Key: "latency_ns", Value: strconv.FormatInt(int64(wt.lat), 10)}}
		if int64(r.Node) != id.Load() || r.Inc != wt.inc || r.Text != "writeat "+wt.path+": "+wt.lat.String() ||
			!reflect.DeepEqual(r.Attrs, attrs) || r.Cause != rs[i-1].Seq {
			t.Fatalf("record %d: %+v after record %d; want inc %d, attrs %+v, caused by the record before it", i, r, rs[i-1].Seq, wt.inc, attrs)
		}
	}
}

// FSX-011: the driver goroutine never sleeps a latency, so a final check that reads with read
// latency configured does not move virtual time.
func TestGoroutineFSNoLatencySleepOnDriver(t *testing.T) {
	cfg := simdisk.DefaultConfig()
	cfg.Latency.Read = simdisk.Latency{Base: 2 * time.Millisecond}
	var got atomic.Value // string "<content>|<elapsed>|<error>"
	gw(t, Options{Duration: time.Second, Disk: cfg}, func(w *World) {
		n := w.AddProcess("p", func(ctx context.Context, p *Process) error {
			if err := fsx.WriteFile(p.FS, "f", []byte("v"), 0o644); err != nil {
				return err
			}
			<-ctx.Done()
			return nil
		})
		w.Final("read", func() error {
			before := time.Since(epoch)
			b, err := fsx.ReadFile(w.Process(n.ID()).FS, "f")
			got.Store(fmt.Sprintf("%s|%v|%v", b, time.Since(epoch)-before, err))
			return nil
		})
	})
	if s, _ := got.Load().(string); s != "v|0s|<nil>" {
		t.Fatalf("final check read %q, want v|0s|<nil>", s)
	}
}

// FSX §7: an fsx call on the driver goroutine for a paused node panics with WaitUnpaused's
// message, and the frame does not unlock a second time.
func TestGoroutineFSPausedOnDriverIsMisuse(t *testing.T) {
	res := runGorScenario(t, "fsx-paused-on-driver", "FAULTLINE_SEED=0x1")
	want := "faultline: fsx call on paused node p1 from the driver goroutine; read its files with w.Disk.Volume(n) instead"
	if res.code != 1 || !strings.Contains(res.out, want) || !strings.Contains(res.out, `panic in final check "stat paused"`) {
		t.Fatalf("exit %d\n%s", res.code, res.out)
	}
}
