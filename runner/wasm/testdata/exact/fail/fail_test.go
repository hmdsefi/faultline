package fail

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
)

// TestA fails seed 2 of 3 (index 1) with an invariant.
func TestA(t *testing.T) {
	bad := faultline.DeriveSeed(faultline.NameBase("TestA"), 1)
	faultline.Run(t, faultline.Options{Mode: faultline.ModeGoroutine, Seeds: 3, Duration: time.Second},
		func(w *faultline.World) {
			w.AddProcess("p", func(ctx context.Context, p *faultline.Process) error {
				<-ctx.Done()
				return nil
			})
			if w.Seed() == bad {
				w.Invariant("seed two fails", func() error { return errors.New("seed two fails") })
			}
		})
}
