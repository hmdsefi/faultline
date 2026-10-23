package exb09

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
)

// TestExchange (AT-EXB-09): two goroutines exchange 100 values over a channel and append to a
// shared slice under a mutex; a final check writes the append order to EXB_OUT.
func TestExchange(t *testing.T) {
	faultline.Run(t, faultline.Options{Mode: faultline.ModeGoroutine, Seeds: 1, Duration: time.Second},
		func(w *faultline.World) {
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
			w.Final("write order", func() error {
				mu.Lock()
				defer mu.Unlock()
				return writeOut(fmt.Sprintf("TestExchange\t0x%016x\t%s\n", w.Seed(), order))
			})
		})
}

// writeOut appends line to the file named by EXB_OUT, if set.
func writeOut(line string) error {
	path := os.Getenv("EXB_OUT")
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = f.WriteString(line)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
