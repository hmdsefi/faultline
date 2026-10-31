package exb08

import (
	"math/rand"
	"sort"
	"sync"
)

type Counter struct {
	sync.Mutex
	n  int
	rw sync.RWMutex
	m  map[string]int
	o  sync.Once
}

func (c *Counter) Inc() {
	c.Lock()
	defer c.Unlock()
	c.n++
}

func (c *Counter) Get() int {
	c.rw.RLock()
	defer c.rw.RUnlock()
	return c.n
}

func (c *Counter) Init() {
	c.o.Do(func() { c.m = map[string]int{"b": 2, "a": 1, "c": 3} })
}

func (c *Counter) Keys() []string {
	var ks []string
	for k, v := range c.m {
		_ = v
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func Pipeline(n int) []int {
	ch := make(chan int)
	out := make(chan int, n)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for v := range ch {
			out <- v * 2
		}
	}()
	for i := 0; i < n; i++ {
		ch <- i
	}
	close(ch)
	wg.Wait()
	close(out)
	var res []int
	for v := range out {
		res = append(res, v)
	}
	return res
}

func Sel(a, b chan int, quit chan struct{}) (int, bool) {
	select {
	case v, ok := <-a:
		return v, ok
	case b <- 7:
		return -1, true
	case <-quit:
		return 0, false
	}
}

func SelDefault(a chan int) int {
	select {
	case v := <-a:
		return v
	default:
		return -2
	}
}

func Roll() int { return rand.Intn(6) }

func worker(id int, done chan<- int) { done <- id }

func StartWorkers(n int) int {
	done := make(chan int, n)
	for i := 0; i < n; i++ {
		go worker(i, done)
	}
	sum := 0
	for i := 0; i < n; i++ {
		sum += <-done
	}
	return sum
}
