package leasekv

import (
	"sync"
	"time"
)

// Lock grants the lease. The holder renews it. Another server gets it only after it expired on
// the lock's clock, and each new holder gets the next epoch.
type Lock struct {
	Conn Conn
	TTL  time.Duration

	mu     sync.Mutex
	holder string
	epoch  uint64
	expiry time.Time
}

// Handle handles one message from the server named from.
func (l *Lock) Handle(from string, msg any) {
	req, ok := msg.(Acquire)
	if !ok {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	held := now.Before(l.expiry)
	if held && l.holder != from {
		l.Conn.Send(from, Busy{Holder: l.holder})
		return
	}
	if !held {
		l.holder = from
		l.epoch++
	}
	l.expiry = now.Add(l.TTL)
	l.Conn.Send(from, Grant{Epoch: l.epoch, TTL: l.TTL, Sent: req.Sent})
}
