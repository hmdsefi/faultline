package partial

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
)

// TestMain ends every instance's stdout with a partial line.
func TestMain(m *testing.M) {
	code := m.Run()
	fmt.Print("partial-end")
	os.Exit(code)
}

// TestA prints a partial line right before its plan lines (without -v Run logs nothing first), and
// its first default seed fails an invariant.
func TestA(t *testing.T) {
	fmt.Print("partial-start ")
	bad := faultline.DeriveSeed(faultline.NameBase("TestA"), 0)
	faultline.Run(t, faultline.Options{Mode: faultline.ModeGoroutine, Seeds: 3, Duration: time.Second},
		func(w *faultline.World) {
			w.AddProcess("p", func(ctx context.Context, p *faultline.Process) error {
				<-ctx.Done()
				return nil
			})
			if w.Seed() == bad {
				w.Invariant("seed one fails", func() error { return errors.New("seed one fails") })
			}
		})
}
