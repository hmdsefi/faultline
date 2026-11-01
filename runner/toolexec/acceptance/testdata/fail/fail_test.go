package fail

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
)

// TestFail fails its final check on every seed, after two processes interleave under a mutex.
func TestFail(t *testing.T) {
	faultline.Run(t, faultline.Options{Mode: faultline.ModeGoroutine, Seeds: 1, Duration: time.Second},
		func(w *faultline.World) {
			var mu sync.Mutex
			var order []byte
			for _, name := range []string{"a", "b"} {
				w.AddProcess(name, func(ctx context.Context, p *faultline.Process) error {
					for range 5 {
						mu.Lock()
						order = append(order, name[0])
						mu.Unlock()
						time.Sleep(time.Millisecond)
					}
					return nil
				})
			}
			w.Final("order", func() error {
				mu.Lock()
				defer mu.Unlock()
				return fmt.Errorf("order %s", order)
			})
		})
}
