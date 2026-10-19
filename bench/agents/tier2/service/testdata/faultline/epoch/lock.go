package leasekv

import (
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel"
)

// Lock grants the leases, each with its own holder and expiry, and with epochs from one counter.
// The holder renews its lease. Another node gets a lease only after it expired on the lock's
// clock. A node gets the next epoch with its first grant of a lease, and that epoch again with
// every later grant of the lease.
type Lock struct {
	W   *faultline.World
	TTL time.Duration

	epoch  uint64            // the last epoch granted, of any lease
	leases map[string]*lease // by name
}

type lease struct {
	holder string
	expiry time.Time
	epochs map[string]uint64 // per node: the epoch of its grants
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
			ls = &lease{epochs: map[string]uint64{}}
			l.leases[req.Lease] = ls
		}
		name, now := n.Sim().Node(from).Name(), n.Now()
		held := now.Before(ls.expiry)
		if held && ls.holder != name {
			l.W.Net.Send(n, from, Busy{Holder: ls.holder})
			return
		}
		if !held {
			ls.holder = name
		}
		if ls.epochs[name] == 0 {
			l.epoch++
			ls.epochs[name] = l.epoch
		}
		ls.expiry = now.Add(l.TTL)
		l.W.Net.Send(n, from, Grant{Epoch: ls.epochs[name], TTL: l.TTL, Sent: req.Sent})
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
