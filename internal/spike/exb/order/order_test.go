// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package order holds corpus programs C4 (map order, EXB-053), C5 (select order, EXB-054) and
// C7 (in-process goroutine order, EXB-056; informational).
package order

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/internal/spike/exb/exbout"
)

// TestMain clears FAULTLINE_CHECK_DETERMINISM, which CI's nested job sets: Part A replays the
// corpus only at level 3, and exbmeasure measures that (EXB-061), so a divergence here is a
// measurement, not a test failure. Measured runs never set the variable (EXB-060).
func TestMain(m *testing.M) {
	os.Unsetenv("FAULTLINE_CHECK_DETERMINISM")
	os.Exit(m.Run())
}

// TestMapOrder records the iteration order of map[int]int with keys 0..7 as 8 digits.
func TestMapOrder(t *testing.T) {
	faultline.Run(t, faultline.Options{Mode: faultline.ModeGoroutine, Duration: time.Second}, func(w *faultline.World) {
		var order atomic.Value
		w.AddProcess("p", func(ctx context.Context, p *faultline.Process) error {
			m := map[int]int{}
			for i := range 8 {
				m[i] = i
			}
			var b strings.Builder
			for k := range m {
				b.WriteString(strconv.Itoa(k))
			}
			order.Store(b.String())
			<-ctx.Done()
			return nil
		})
		w.Final("record order", func() error {
			s, _ := order.Load().(string)
			if len(s) != 8 {
				return fmt.Errorf("order %q", s)
			}
			return exbout.Write("TestMapOrder", w.Seed(), s)
		})
	})
}

// TestSelectOrder records which of two ready channels each of 16 selects took, as a 16-bit mask
// (bit i set: the second case won the i-th select).
func TestSelectOrder(t *testing.T) {
	faultline.Run(t, faultline.Options{Mode: faultline.ModeGoroutine, Duration: time.Second}, func(w *faultline.World) {
		var mask atomic.Value
		w.AddProcess("p", func(ctx context.Context, p *faultline.Process) error {
			a, b := make(chan int, 16), make(chan int, 16)
			for i := range 16 {
				a <- i
				b <- i
			}
			m := 0
			for i := range 16 {
				select {
				case <-a:
				case <-b:
					m |= 1 << i
				}
			}
			mask.Store(fmt.Sprintf("0x%04x", m))
			<-ctx.Done()
			return nil
		})
		w.Final("record mask", func() error {
			s, _ := mask.Load().(string)
			if len(s) != 6 {
				return fmt.Errorf("mask %q", s)
			}
			return exbout.Write("TestSelectOrder", w.Seed(), s)
		})
	})
}

// TestGoroutineOrder (C7, EXB-056) records how two goroutines of one process interleave: the
// first sends 0..99 over an unbuffered channel and appends 'a' after each send, the second
// appends 'b' for each value it receives, both under one mutex. It is AT-EXB-09's exchange.
func TestGoroutineOrder(t *testing.T) {
	faultline.Run(t, faultline.Options{Mode: faultline.ModeGoroutine, Duration: time.Second}, func(w *faultline.World) {
		var mu sync.Mutex
		var order []byte
		w.AddProcess("p", func(ctx context.Context, p *faultline.Process) error {
			ch := make(chan int)
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				for i := 0; i < 100; i++ {
					ch <- i
					mu.Lock()
					order = append(order, 'a')
					mu.Unlock()
				}
				close(ch)
			}()
			go func() {
				defer wg.Done()
				for range ch {
					mu.Lock()
					order = append(order, 'b')
					mu.Unlock()
				}
			}()
			wg.Wait()
			<-ctx.Done()
			return nil
		})
		w.Final("record goroutine order", func() error {
			mu.Lock()
			defer mu.Unlock()
			if len(order) != 200 {
				return fmt.Errorf("order has %d letters, want 200", len(order))
			}
			return exbout.Write("TestGoroutineOrder", w.Seed(), string(order))
		})
	})
}
