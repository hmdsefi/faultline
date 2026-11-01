package livelock

import (
	"context"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
)

// TestLivelock polls a channel that never gets a value; every poll is a scheduling point.
func TestLivelock(t *testing.T) {
	faultline.Run(t, faultline.Options{Mode: faultline.ModeGoroutine, Seeds: 1, Duration: time.Second, StallTimeout: 2 * time.Second},
		func(w *faultline.World) {
			w.AddProcess("poll", func(ctx context.Context, p *faultline.Process) error {
				ch := make(chan int)
				for {
					select {
					case <-ch:
						return nil
					default:
					}
				}
			})
		})
}
