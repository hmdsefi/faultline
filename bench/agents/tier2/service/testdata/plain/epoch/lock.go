package leasekv

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"
)

// Lock grants the leases, each with its own holder and expiry, and with epochs from one counter.
// The holder renews its lease. Another node gets a lease only after it expired on the lock's
// clock. A node gets the next epoch with its first grant of a lease, and that epoch again with
// every later grant of the lease.
type Lock struct {
	Conn Conn
	TTL  time.Duration

	mu     sync.Mutex
	epoch  uint64            // the last epoch granted, of any lease
	leases map[string]*lease // by name
}

type lease struct {
	holder string
	expiry time.Time
	epochs map[string]uint64 // per node: the epoch of its grants
}

// Handle handles one message from the node named from.
func (l *Lock) Handle(from string, msg any) {
	req, ok := msg.(Acquire)
	if !ok {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.leases == nil {
		l.leases = map[string]*lease{}
	}
	ls := l.leases[req.Lease]
	if ls == nil {
		ls = &lease{epochs: map[string]uint64{}}
		l.leases[req.Lease] = ls
	}
	now := time.Now()
	held := now.Before(ls.expiry)
	if held && ls.holder != from {
		l.Conn.Send(from, Busy{Holder: ls.holder})
		return
	}
	if !held {
		ls.holder = from
	}
	if ls.epochs[from] == 0 {
		l.epoch++
		ls.epochs[from] = l.epoch
	}
	ls.expiry = now.Add(l.TTL)
	l.Conn.Send(from, Grant{Epoch: ls.epochs[from], TTL: l.TTL, Sent: req.Sent})
}

// every runs f every d until ctx is done, the first time after a random delay in [0, d].
func every(ctx context.Context, d time.Duration, f func()) {
	go func() {
		t := time.NewTimer(rand.N(d + 1))
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			f()
			t.Reset(d)
		}
	}()
}
