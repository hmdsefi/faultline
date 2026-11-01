package httpcorpus_test

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"example.com/httpcorpus"
	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

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
