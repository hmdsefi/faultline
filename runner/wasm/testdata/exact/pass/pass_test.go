package pass

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
)

func TestA(t *testing.T) {
	faultline.Run(t, faultline.Options{Mode: faultline.ModeGoroutine, Seeds: 3, Duration: time.Second},
		func(w *faultline.World) {
			w.AddProcess("p", func(ctx context.Context, p *faultline.Process) error {
				time.Sleep(100 * time.Millisecond)
				<-ctx.Done()
				return nil
			})
		})
}

func TestB(t *testing.T) { fmt.Println("B") }
