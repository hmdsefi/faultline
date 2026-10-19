package leasekv

import (
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel"
)

// Lock grants the lease. The holder renews it. Another server gets it only after it expired on
// the lock's clock, and each new holder gets the next epoch.
type Lock struct {
	W   *faultline.World
	TTL time.Duration

	holder string
	epoch  uint64
	expiry time.Time
}

// Boot starts the lock.
func (l *Lock) Boot(n *kernel.Node) {
	l.holder, l.epoch, l.expiry = "", 0, time.Time{}
	l.W.Net.Handle(n, func(from kernel.NodeID, msg any) {
		if _, ok := msg.(Acquire); !ok {
			return
		}
		name, now := n.Sim().Node(from).Name(), n.Now()
		held := now.Before(l.expiry)
		if held && l.holder != name {
			l.W.Net.Send(n, from, Busy{Holder: l.holder})
			return
		}
		if !held {
			l.holder = name
			l.epoch++
		}
		l.expiry = now.Add(l.TTL)
		l.W.Net.Send(n, from, Grant{Epoch: l.epoch, TTL: l.TTL})
	})
}
