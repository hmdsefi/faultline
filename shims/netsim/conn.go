// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim

import (
	"net"
	"net/netip"
	"os"
	"syscall"
	"time"
)

// passed reports whether deadline is set and not in the future (bubble time).
func passed(deadline time.Time) bool { return !deadline.IsZero() && !time.Now().Before(deadline) }

// block waits until ch or dlCh is closed or deadline passes (NSM-003, NSM-043). Lock held on entry
// and on return; released while waiting. On the driver goroutine it panics with the lock still held:
// every blocking method defers the Unlock, which releases it while the panic unwinds.
func (st *Stack) block(op string, ch, dlCh <-chan struct{}, deadline time.Time) {
	if st.d.OnDriver() {
		panic("netsim: blocking call " + op + " on the driver goroutine (body, a check or a kernel callback)")
	}
	st.d.Unlock()
	var tc <-chan time.Time
	var t *time.Timer
	if !deadline.IsZero() {
		t = time.NewTimer(time.Until(deadline))
		tc = t.C
	}
	select {
	case <-ch:
	case <-dlCh:
	case <-tc:
	}
	if t != nil {
		t.Stop()
	}
	st.d.Lock()
}

type epState uint8

const (
	stDialing epState = iota + 1
	stSynRcvd
	stEstablished
	stRemoved
)

// Conn is one endpoint of a TCP-like connection. It implements net.Conn.
type Conn struct {
	c             *connection
	side          int // 0 client, 1 server
	h             *Host
	state         epState
	errno         error // error state: syscall.ECONNRESET, syscall.ETIMEDOUT, ErrProcessGone or net.ErrClosed
	closed        bool  // Close called
	rdShut        bool  // CloseRead called
	wrShut        bool  //nolint:unused // CloseWrite called; close state, not wired up yet
	local         netip.AddrPort
	remote        netip.AddrPort
	lst           *Listener // server endpoints
	inAcceptQueue bool
	pumpPending   bool //nolint:unused // byte-stream state, not wired up yet
	windowPending bool //nolint:unused // byte-stream state, not wired up yet
	notify        chan struct{}
	rdDL, wrDL    time.Time //nolint:unused // deadline state, not wired up yet
	dlCh          chan struct{}
}

func newConn(c *connection, side int, h *Host, state epState, local, remote netip.AddrPort) *Conn {
	return &Conn{c: c, side: side, h: h, state: state, local: local, remote: remote,
		notify: make(chan struct{}), dlCh: make(chan struct{})}
}

// wake closes and replaces the endpoint's state channel (GOR §11 waiting pattern). Lock held.
func (c *Conn) wake() {
	close(c.notify)
	c.notify = make(chan struct{})
}

// LocalAddr returns the local address as a *net.TCPAddr.
func (c *Conn) LocalAddr() net.Addr { return net.TCPAddrFromAddrPort(c.local) }

// RemoteAddr returns the peer's address as a *net.TCPAddr.
func (c *Conn) RemoteAddr() net.Addr { return net.TCPAddrFromAddrPort(c.remote) }

// ID returns the connection ID, e.g. "c7".
func (c *Conn) ID() string { return c.c.id }

func (c *Conn) opErr(op string, err error) error {
	return &net.OpError{Op: op, Net: "tcp", Source: c.LocalAddr(), Addr: c.RemoteAddr(), Err: err}
}

// errFor wraps the endpoint's error state for op ("read" or "write"): errnos in os.SyscallError.
func (c *Conn) errFor(op string) error {
	if e, ok := c.errno.(syscall.Errno); ok {
		return &os.SyscallError{Syscall: op, Err: e}
	}
	return c.errno
}

// epKey is the endpoint table key of side (0 client, 1 server) of connection c<k>: 2k + side. The
// table sorts by (connection ID, side), and both ends of a loopback connection fit in one host's
// table (NSM-011).
func epKey(k, side int) int { return 2*k + side }

// addEndpoint adds ep to its host's endpoint table. Lock held.
func (h *Host) addEndpoint(ep *Conn) {
	key := epKey(ep.c.k, ep.side)
	h.eps[key] = ep
	h.eks = insertSorted(h.eks, key)
}

// holds reports whether ep is in h's endpoint table. Lock held.
func (h *Host) holds(ep *Conn) bool { return h.eps[epKey(ep.c.k, ep.side)] == ep }

// dropEndpoint deletes ep from h's endpoint table; an entry that is not ep stays. Lock held.
func (h *Host) dropEndpoint(ep *Conn) {
	if h.holds(ep) {
		key := epKey(ep.c.k, ep.side)
		delete(h.eps, key)
		h.eks = removeSorted(h.eks, key)
	}
}

// endpoints returns h's endpoints in ascending (connection ID, side) order, client before server
// (NSM-011). The slice is a copy, so the caller may add or remove endpoints while it walks it.
// Lock held.
func (h *Host) endpoints() []*Conn {
	eps := make([]*Conn, len(h.eks))
	for i, key := range h.eks {
		eps[i] = h.eps[key]
	}
	return eps
}

// tcpInUse reports whether a listener or an endpoint of h uses TCP port p (NSM-016 ephemeral ports).
// Lock held.
func (h *Host) tcpInUse(p uint16) bool {
	if h.listeners[p] != nil {
		return true
	}
	for _, key := range h.eks {
		if h.eps[key].local.Port() == p {
			return true
		}
	}
	return false
}

// listenInUse reports whether Listen on the explicit TCP port p fails with EADDRINUSE (NSM-020): a
// listener or a client endpoint of h uses p. Server endpoints left by a closed listener do not
// count (Go's net sets SO_REUSEADDR on listeners). Lock held.
func (h *Host) listenInUse(p uint16) bool {
	if h.listeners[p] != nil {
		return true
	}
	for _, key := range h.eks {
		if ep := h.eps[key]; ep.side == 0 && ep.local.Port() == p {
			return true
		}
	}
	return false
}
