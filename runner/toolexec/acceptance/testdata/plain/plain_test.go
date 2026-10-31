package plain

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
)

// TestPlain has nothing for the rewriter: its process only sleeps. XTE_PROCS sets Options.Procs.
func TestPlain(t *testing.T) {
	procs, _ := strconv.Atoi(os.Getenv("XTE_PROCS"))
	faultline.Run(t, faultline.Options{Mode: faultline.ModeGoroutine, Seeds: 1, Duration: time.Second, Procs: procs},
		func(w *faultline.World) {
			w.AddProcess("p", func(ctx context.Context, p *faultline.Process) error {
				time.Sleep(10 * time.Millisecond)
				return nil
			})
		})
}
