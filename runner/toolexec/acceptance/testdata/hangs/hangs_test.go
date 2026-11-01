package hangs

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
)

// run runs procs in goroutine mode; a final check writes the order they logged to EXB_OUT and
// fails unless they logged exactly want, in any order.
func run(t *testing.T, want []string, procs func(w *faultline.World, log func(string))) {
	faultline.Run(t, faultline.Options{Mode: faultline.ModeGoroutine, Duration: time.Second, StallTimeout: 5 * time.Second},
		func(w *faultline.World) {
			var mu sync.Mutex
			var order []string
			procs(w, func(s string) {
				mu.Lock()
				order = append(order, s)
				mu.Unlock()
			})
			w.Final("order", func() error {
				mu.Lock()
				defer mu.Unlock()
				if err := writeOut(fmt.Sprintf("%s\t0x%016x\t%v\n", t.Name(), w.Seed(), order)); err != nil {
					return err
				}
				if !slices.Equal(slices.Sorted(slices.Values(order)), slices.Sorted(slices.Values(want))) {
					return fmt.Errorf("logged %v, want %v in any order", order, want)
				}
				return nil
			})
		})
}

func one(w *faultline.World, main func()) {
	w.AddProcess("p", func(ctx context.Context, p *faultline.Process) error {
		main()
		return nil
	})
}

func TestCond(t *testing.T) {
	run(t, []string{"w0", "w1", "w2"}, func(w *faultline.World, log func(string)) {
		one(w, func() {
			var mu sync.Mutex
			c := sync.NewCond(&mu)
			ready := 0
			var wg sync.WaitGroup
			for i := range 3 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					mu.Lock()
					ready++
					c.Wait()
					log(fmt.Sprint("w", i))
					mu.Unlock()
				}()
			}
			for {
				mu.Lock()
				n := ready
				mu.Unlock()
				if n == 3 {
					break
				}
				runtime.Gosched()
			}
			mu.Lock()
			c.Signal()
			mu.Unlock()
			time.Sleep(time.Millisecond)
			mu.Lock()
			c.Broadcast()
			mu.Unlock()
			wg.Wait()
		})
	})
}

func TestLocker(t *testing.T) {
	run(t, []string{"l0", "l1", "l2", "r0", "r1", "r2"}, func(w *faultline.World, log func(string)) {
		one(w, func() {
			var mu sync.Mutex
			var l sync.Locker = &mu
			var rw sync.RWMutex
			rl := rw.RLocker()
			var wg sync.WaitGroup
			for i := range 3 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					l.Lock()
					log(fmt.Sprint("l", i))
					time.Sleep(time.Millisecond) // holds the lock across a durable sleep
					l.Unlock()
					rl.Lock()
					log(fmt.Sprint("r", i))
					rl.Unlock()
				}()
			}
			wg.Wait()
		})
	})
}

func TestMethodValue(t *testing.T) {
	run(t, []string{"m0", "m1", "m2"}, func(w *faultline.World, log func(string)) {
		one(w, func() {
			var mu sync.Mutex
			lock, unlock := mu.Lock, mu.Unlock
			var wg sync.WaitGroup
			for i := range 3 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					locked(lock, unlock, func() {
						log(fmt.Sprint("m", i))
						time.Sleep(time.Millisecond)
					})
				}()
			}
			wg.Wait()
		})
	})
}

// locked calls f between lock and unlock, method values its caller passes.
func locked(lock, unlock func(), f func()) {
	lock()
	f()
	unlock()
}

func TestOnceFunc(t *testing.T) {
	run(t, []string{"o0", "o1", "o2", "calls1"}, func(w *faultline.World, log func(string)) {
		one(w, func() {
			var calls atomic.Int32
			f := sync.OnceFunc(func() {
				calls.Add(1)
				time.Sleep(time.Millisecond) // the other callers wait while f sleeps
			})
			var wg sync.WaitGroup
			for i := range 3 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					f()
					log(fmt.Sprint("o", i))
				}()
			}
			wg.Wait()
			log(fmt.Sprint("calls", calls.Load()))
		})
	})
}

func TestWaitGroupGo(t *testing.T) {
	run(t, []string{"g0", "g1", "g2"}, func(w *faultline.World, log func(string)) {
		one(w, func() {
			var wg sync.WaitGroup
			for i := range 3 {
				wg.Go(func() { log(fmt.Sprint("g", i)) })
			}
			wg.Wait()
		})
	})
}

func TestAfterFunc(t *testing.T) {
	run(t, []string{"t0", "t1", "t2"}, func(w *faultline.World, log func(string)) {
		one(w, func() {
			var mu sync.Mutex
			done := make(chan struct{})
			n := 0
			for i := range 3 {
				time.AfterFunc(time.Millisecond, func() {
					mu.Lock()
					log(fmt.Sprint("t", i))
					n++
					if n == 3 {
						close(done)
					}
					mu.Unlock()
				})
			}
			<-done
		})
	})
}

// TestGoschedSpin waits on an atomic flag with runtime.Gosched; the setter is runnable, so a
// release reaches it.
func TestGoschedSpin(t *testing.T) {
	run(t, []string{"set", "seen"}, func(w *faultline.World, log func(string)) {
		one(w, func() {
			var flag atomic.Bool
			go func() {
				log("set")
				flag.Store(true)
			}()
			for !flag.Load() {
				runtime.Gosched()
			}
			log("seen")
		})
	})
}

// spawn starts out(v) in a goroutine of a generic function.
func spawn[T any](wg *sync.WaitGroup, v T, out func(T)) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		out(v)
	}()
	go out(v)
}

func TestGenericGo(t *testing.T) {
	run(t, []string{"x", "x"}, func(w *faultline.World, log func(string)) {
		one(w, func() {
			var wg sync.WaitGroup
			var mu sync.Mutex
			n := 0
			spawn(&wg, "x", func(s string) {
				mu.Lock()
				n++
				mu.Unlock()
				log(s)
			})
			wg.Wait()
			for {
				mu.Lock()
				m := n
				mu.Unlock()
				if m == 2 {
					break
				}
				runtime.Gosched()
			}
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
