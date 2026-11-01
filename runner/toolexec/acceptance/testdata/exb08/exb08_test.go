package exb08

import "testing"

func TestSample(t *testing.T) {
	c := &Counter{}
	c.Init()
	c.Inc()
	if c.Get() != 1 || len(c.Keys()) != 3 {
		t.Fatal("counter")
	}
	if r := Pipeline(5); len(r) != 5 {
		t.Fatal(r)
	}
	a, b, q := make(chan int, 1), make(chan int), make(chan struct{})
	a <- 4
	if v, ok := Sel(a, b, q); v != 4 || !ok {
		t.Fatal(v, ok)
	}
	if SelDefault(make(chan int)) != -2 {
		t.Fatal("default")
	}
	if StartWorkers(4) != 6 {
		t.Fatal("workers")
	}
	if r := Roll(); r < 0 || r > 5 {
		t.Fatal(r)
	}
}
