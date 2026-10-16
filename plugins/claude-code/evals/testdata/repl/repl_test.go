package repl_test

import (
	"strconv"
	"testing"
	"time"

	"example.com/repl"
)

func TestReplicasCatchUp(t *testing.T) {
	var replicas []*repl.Replica
	var addrs []string
	for range 2 {
		r, err := repl.StartReplica()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		replicas = append(replicas, r)
		addrs = append(addrs, r.Addr())
	}
	p, err := repl.StartPrimary(addrs...)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 100 {
		p.Put("k"+strconv.Itoa(i), "v"+strconv.Itoa(i))
	}
	time.Sleep(50 * time.Millisecond)
	for n, r := range replicas {
		for i := range 100 {
			key, want := "k"+strconv.Itoa(i), "v"+strconv.Itoa(i)
			if got := r.Get(key); got != want {
				t.Fatalf("replica %d holds %s=%q, want %q", n+1, key, got, want)
			}
		}
	}
}
