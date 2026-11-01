package gapfail

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
)

type node struct{ name string }

// TestGapFail ranges over a map with pointer keys, a coverage gap, and fails its final check.
func TestGapFail(t *testing.T) {
	faultline.Run(t, faultline.Options{Mode: faultline.ModeGoroutine, Seeds: 1, Duration: time.Second},
		func(w *faultline.World) {
			w.AddProcess("p", func(ctx context.Context, p *faultline.Process) error {
				total := 0
				for _, v := range map[*node]int{{"a"}: 1, {"b"}: 2} { // the gap
					total += v
				}
				if total != 3 {
					return errors.New("lost a value")
				}
				return nil
			})
			w.Final("fails", func() error { return errors.New("boom") })
		})
}
