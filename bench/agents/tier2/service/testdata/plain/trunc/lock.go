package leasekv

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"
)

// Lock grants the leases, each with its own holder and expiry, and with epochs from one counter.
// The holder renews its lease. Another node gets a lease only after it expired on the lock's
// clock, and then with the next epoch. An expired lease that nobody took since goes back to its
// last holder with the same epoch: nobody else held it in between, so nothing was written under
// another epoch, and what the holder loaded for that epoch is still current.
type Lock struct {
	Conn Conn
	TTL  time.Duration

	mu     sync.Mutex
	epoch  uint64            // the last epoch granted, of any lease
	leases map[string]*lease // by name
}

type lease struct {
	holder string
	epoch  uint64
	expiry time.Time
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
		ls = &lease{}
		l.leases[req.Lease] = ls
	}
	now := time.Now()
	held := now.Before(ls.expiry)
	if held && ls.holder != from {
		l.Conn.Send(from, Busy{Holder: ls.holder})
		return
	}
	if !held && ls.holder != from {
		l.epoch++
		ls.holder, ls.epoch = from, l.epoch
	}
	ls.expiry = now.Add(l.TTL)
	l.Conn.Send(from, Grant{Epoch: ls.epoch, TTL: l.TTL, Sent: req.Sent})
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
