// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package httpcorpus_test

import (
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/internal/spike/exb/httpcorpus"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// TestMain clears FAULTLINE_CHECK_DETERMINISM, which CI's nested job sets: Part A replays the
// corpus only at level 3, and exbmeasure measures that (EXB-061), so a divergence here is a
// measurement, not a test failure. Measured runs never set the variable (EXB-060).
func TestMain(m *testing.M) {
	os.Unsetenv("FAULTLINE_CHECK_DETERMINISM")
	os.Exit(m.Run())
}

// TestHTTPCorpus is corpus C3 (EXB-052).
func TestHTTPCorpus(t *testing.T) {
	faultline.Run(t, faultline.Options{Mode: faultline.ModeGoroutine, Duration: 20 * time.Second,
		Net: simnet.Config{Default: simnet.Link{Latency: time.Millisecond, Jitter: 2 * time.Millisecond}}},
		func(w *faultline.World) {
			var failures atomic.Int64
			var first atomic.Value
			w.AddProcess("srv", httpcorpus.Server)
			w.AddClientProcess("cli", httpcorpus.Client(&failures, &first))
			w.Invariant("no request fails", func() error {
				if n := failures.Load(); n > 0 {
					return fmt.Errorf("%d requests failed; first: %v", n, first.Load())
				}
				return nil
			})
		})
}
