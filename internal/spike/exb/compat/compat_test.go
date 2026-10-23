// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package compat holds corpus C6 (EXB-055): standard-library compatibility tests.
package compat

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"testing/cryptotest"
	"testing/synctest"
	"time"

	"github.com/hmdsefi/faultline"
)

// TestMain clears FAULTLINE_CHECK_DETERMINISM, which CI's nested job sets: Part A replays the
// corpus only at level 3, and exbmeasure measures that (EXB-061), so a divergence here is a
// measurement, not a test failure. Measured runs never set the variable (EXB-060).
func TestMain(m *testing.M) {
	os.Unsetenv("FAULTLINE_CHECK_DETERMINISM")
	os.Exit(m.Run())
}

// inProcess runs fn in a goroutine-mode process for 3 seeds; fn returns the text the final
// check compares with want.
func inProcess(t *testing.T, want string, fn func(ctx context.Context, p *faultline.Process) string) {
	faultline.Run(t, faultline.Options{Mode: faultline.ModeGoroutine, Seeds: 3, Duration: time.Second}, func(w *faultline.World) {
		var got atomic.Value
		w.AddProcess("p", func(ctx context.Context, p *faultline.Process) error {
			got.Store(fn(ctx, p))
			<-ctx.Done()
			return nil
		})
		w.Final("result", func() error {
			if s, _ := got.Load().(string); s != want {
				return fmt.Errorf("got %q, want %q", s, want)
			}
			return nil
		})
	})
}

func TestCompatSync(t *testing.T) {
	inProcess(t, "total=400 once=1 map=400 seen=[100 100 100 100]", func(ctx context.Context, p *faultline.Process) string {
		var (
			mu    sync.Mutex
			rw    sync.RWMutex
			wg    sync.WaitGroup
			once  sync.Once
			cond  = sync.NewCond(&sync.Mutex{})
			ready bool
			sm    sync.Map
			pool  = sync.Pool{New: func() any { return new(bytes.Buffer) }}
			total int
			calls int
			seen  [4]int
		)
		for g := range 4 {
			wg.Add(1)
			p.Go(func() {
				defer wg.Done()
				once.Do(func() { calls++ })
				cond.L.Lock()
				for !ready {
					cond.Wait()
				}
				cond.L.Unlock()
				for i := range 100 {
					mu.Lock()
					total++
					mu.Unlock()
					rw.Lock()
					seen[g]++
					rw.Unlock()
					rw.RLock()
					_ = seen[g]
					rw.RUnlock()
					sm.Store(g*1000+i, i)
					buf, _ := pool.Get().(*bytes.Buffer)
					buf.Reset()
					buf.WriteString("x")
					pool.Put(buf)
					time.Sleep(time.Millisecond)
				}
			})
		}
		time.Sleep(10 * time.Millisecond)
		cond.L.Lock()
		ready = true
		cond.Broadcast()
		cond.L.Unlock()
		wg.Wait()
		n := 0
		sm.Range(func(_, _ any) bool { n++; return true })
		return fmt.Sprintf("total=%d once=%d map=%d seen=%v", total, calls, n, seen)
	})
}

type nested struct {
	Name  string
	Tags  []string
	Attrs map[string][]int
	Kids  []nested
}

// fixed is a fixed-size value for encoding/binary, which cannot encode slices of variable
// length inside structs or maps (EXB-055).
type fixed struct {
	A [4]uint32
	B [2]int64
	C [2][3]uint16
}

func TestCompatEncoding(t *testing.T) {
	inProcess(t, "json=ok gob=ok binary=ok", func(ctx context.Context, p *faultline.Process) string {
		v := nested{Name: "root", Tags: []string{"a", "b"}, Attrs: map[string][]int{"x": {1, 2}, "y": nil},
			Kids: []nested{{Name: "kid", Attrs: map[string][]int{"z": {3}}}}}
		res := map[string]string{}
		var j nested
		b, err := json.Marshal(v)
		if err == nil {
			err = json.Unmarshal(b, &j)
		}
		res["json"] = verdict(err, reflect.DeepEqual(normalize(j), normalize(v)))
		var g nested
		var buf bytes.Buffer
		err = gob.NewEncoder(&buf).Encode(v)
		if err == nil {
			err = gob.NewDecoder(&buf).Decode(&g)
		}
		res["gob"] = verdict(err, reflect.DeepEqual(normalize(g), normalize(v)))
		f := fixed{A: [4]uint32{1, 2, 3, 4}, B: [2]int64{-5, 6}, C: [2][3]uint16{{7, 8, 9}, {10, 11, 12}}}
		var fb bytes.Buffer
		var f2 fixed
		err = binary.Write(&fb, binary.BigEndian, f)
		if err == nil {
			err = binary.Read(&fb, binary.BigEndian, &f2)
		}
		res["binary"] = verdict(err, f2 == f)
		return fmt.Sprintf("json=%s gob=%s binary=%s", res["json"], res["gob"], res["binary"])
	})
}

func verdict(err error, equal bool) string {
	switch {
	case err != nil:
		return err.Error()
	case !equal:
		return "differs"
	}
	return "ok"
}

// normalize maps nil and empty slices to one form (gob and json do not keep the difference).
func normalize(v nested) nested {
	if len(v.Tags) == 0 {
		v.Tags = nil
	}
	attrs := map[string][]int{}
	for k, s := range v.Attrs {
		if len(s) == 0 {
			s = nil
		}
		attrs[k] = s
	}
	v.Attrs = attrs
	kids := []nested(nil)
	for _, k := range v.Kids {
		kids = append(kids, normalize(k))
	}
	v.Kids = kids
	return v
}

// TestCompatSynctest uses synctest.Wait and time.Sleep in a plain bubble (not in faultline: GOR
// forbids synctest.Wait in processes).
func TestCompatSynctest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		var fired atomic.Bool
		go func() {
			time.Sleep(time.Hour)
			fired.Store(true)
		}()
		synctest.Wait()
		if fired.Load() {
			t.Fatal("the timer fired before bubble time advanced")
		}
		time.Sleep(time.Hour + time.Second)
		synctest.Wait()
		if !fired.Load() || time.Since(start) != time.Hour+time.Second {
			t.Fatalf("fired %v after %v", fired.Load(), time.Since(start))
		}
	})
}

// TestCompatCryptotest: the same seed gives the same bytes, another seed different ones.
func TestCompatCryptotest(t *testing.T) {
	read := func(seed uint64) []byte {
		var b []byte
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			cryptotest.SetGlobalRandom(t, seed)
			b = make([]byte, 16)
			rand.Read(b)
		})
		return b
	}
	a1, a2, b := read(1), read(1), read(2)
	if !bytes.Equal(a1, a2) || bytes.Equal(a1, b) {
		t.Fatalf("seed 1: %x, %x; seed 2: %x", a1, a2, b)
	}
}
