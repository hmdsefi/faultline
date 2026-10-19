package leasekv

import (
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel"
)

// Lock grants the leases, each with its own holder and expiry, and with epochs from one counter.
// The holder renews its lease. Another node gets a lease only after it expired on the lock's
// clock, and then with the next epoch. An expired lease that nobody took since goes back to its
// last holder with the same epoch: nobody else held it in between, so nothing was written under
// another epoch, and what the holder loaded for that epoch is still current.
type Lock struct {
	W   *faultline.World
	TTL time.Duration

	epoch  uint64            // the last epoch granted, of any lease
	leases map[string]*lease // by name
}

type lease struct {
	holder string
	epoch  uint64
	expiry time.Time
}

// Boot starts the lock with no lease granted.
func (l *Lock) Boot(n *kernel.Node) {
	l.epoch, l.leases = 0, map[string]*lease{}
	l.W.Net.Handle(n, func(from kernel.NodeID, msg any) {
		req, ok := msg.(Acquire)
		if !ok {
			return
		}
		ls := l.leases[req.Lease]
		if ls == nil {
			ls = &lease{}
			l.leases[req.Lease] = ls
		}
		name, now := n.Sim().Node(from).Name(), n.Now()
		held := now.Before(ls.expiry)
		if held && ls.holder != name {
			l.W.Net.Send(n, from, Busy{Holder: ls.holder})
			return
		}
		if !held && ls.holder != name {
			l.epoch++
			ls.holder, ls.epoch = name, l.epoch
		}
		ls.expiry = now.Add(l.TTL)
		l.W.Net.Send(n, from, Grant{Epoch: ls.epoch, TTL: l.TTL, Sent: req.Sent})
	})
}

// every runs f every d on node n, the first time after a random delay in [0, d].
func every(n *kernel.Node, d time.Duration, label string, f func()) {
	var tick func()
	tick = func() {
		f()
		n.After(d, label, tick)
	}
	n.After(kernel.Uniform(n.Rand(), 0, d), label, tick)
}
