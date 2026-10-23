package guest

import (
	"crypto/rand"
	"fmt"
	mrand "math/rand"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDeterminism(t *testing.T) {
	m := map[int]bool{}
	for i := 0; i < 8; i++ {
		m[i] = true
	}
	var order []string
	for k := range m {
		order = append(order, fmt.Sprint(k))
	}
	a, b := make(chan int, 16), make(chan int, 16)
	for i := 0; i < 16; i++ {
		a <- i
		b <- i
	}
	mask := 0
	for i := 0; i < 16; i++ {
		select {
		case <-a:
		case <-b:
			mask |= 1 << i
		}
	}
	var mu sync.Mutex
	var seq []string
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 3; j++ {
				mu.Lock()
				seq = append(seq, fmt.Sprint(g))
				mu.Unlock()
				runtime.Gosched()
			}
		}()
	}
	wg.Wait()
	start := time.Now()
	time.Sleep(1500 * time.Millisecond)
	el := time.Since(start)
	cb := make([]byte, 4)
	rand.Read(cb)
	fmt.Printf("RESULT map=%s select=%04x sched=%s mathrand=%d crypto=%x sleep=%v now=%v GOMAXPROCS=%d\n",
		strings.Join(order, ""), mask, strings.Join(seq, ""), mrand.Intn(1000000), cb, el,
		start.UTC().Format(time.RFC3339Nano), runtime.GOMAXPROCS(0))
}

func TestFail(t *testing.T) { t.Fatal("boom") }

func TestSleepHour(t *testing.T) {
	start := time.Now()
	time.Sleep(time.Hour)
	fmt.Println(time.Since(start))
}

func TestCryptoRand(t *testing.T) {
	b := make([]byte, 16)
	rand.Read(b)
	fmt.Printf("%x\n", b)
}

// TestBadPlan prints a malformed plan line in a plan instance (the runner's TestBadPlanLine).
func TestBadPlan(t *testing.T) {
	if os.Getenv("FAULTLINE_EXACT") == "plan" && os.Getenv("BAD_PLAN") == "1" {
		os.Stdout.WriteString("faultline-exact-plan\tTestBadPlan\tnot-a-seed\n")
	}
}
