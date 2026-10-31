package orders

import (
	"context"
	"fmt"
	"maps"
	"math/rand"
	r2 "math/rand/v2"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
)

type key struct {
	a int
	b string
}

// run runs body once in a process and writes its outcome to EXB_OUT from a final check.
func run(t *testing.T, body func() string) {
	faultline.Run(t, faultline.Options{Mode: faultline.ModeGoroutine, Duration: time.Second},
		func(w *faultline.World) {
			var out string
			w.AddProcess("p", func(ctx context.Context, p *faultline.Process) error {
				out = body()
				return nil
			})
			w.Final("outcome", func() error {
				return writeOut(fmt.Sprintf("%s\t0x%016x\t%s\n", t.Name(), w.Seed(), out))
			})
		})
}

func TestStructKeys(t *testing.T) {
	run(t, func() string {
		var b strings.Builder
		for k := range map[key]int{{1, "x"}: 1, {1, "y"}: 2, {0, "z"}: 3, {2, "a"}: 4} {
			fmt.Fprint(&b, k.a, k.b, " ")
		}
		return b.String()
	})
}

func TestBoolKeys(t *testing.T) {
	run(t, func() string {
		var b strings.Builder
		for i := range 4 {
			for k := range map[bool]int{true: i, false: i} {
				fmt.Fprint(&b, k, " ")
			}
		}
		return b.String()
	})
}

func TestMapsAll(t *testing.T) {
	run(t, func() string {
		var b strings.Builder
		for k, v := range maps.All(map[string]int{"a": 1, "b": 2, "c": 3, "d": 4}) {
			fmt.Fprint(&b, k, v, " ")
		}
		return b.String()
	})
}

func TestRandValue(t *testing.T) {
	run(t, func() string {
		pick := rand.Intn
		return fmt.Sprint(pick(1000), pick(1000), pick(1000))
	})
}

func TestRandN(t *testing.T) {
	run(t, func() string {
		return fmt.Sprint(r2.N(1000), r2.N(time.Hour))
	})
}

func TestReflectSelect(t *testing.T) {
	run(t, func() string {
		var b strings.Builder
		for range 16 {
			a, c := make(chan int, 1), make(chan int, 1)
			a <- 1
			c <- 2
			i, _, _ := reflect.Select([]reflect.SelectCase{
				{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(a)},
				{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(c)},
			})
			fmt.Fprint(&b, i)
		}
		return b.String()
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
