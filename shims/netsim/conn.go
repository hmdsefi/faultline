// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim

import (
	"io"
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
	wrShut        bool  // CloseWrite called
	local         netip.AddrPort
	remote        netip.AddrPort
	lst           *Listener // server endpoints
	inAcceptQueue bool
	pumpPending   bool // a net.pump request is pending
	windowPending bool // a net.window request is pending
	notify        chan struct{}
	rdDL, wrDL    time.Time // read and write deadlines; zero means none
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

// reqKey returns the key of the endpoint's stream requests (NSM §6): "tcp:<conn ID>/cli" for the
// client end, "tcp:<conn ID>/srv" for the server end. One endpoint's requests share it and keep
// program order. Both ends of a loopback connection are on one node, so with a shared key the
// order in which their goroutines posted would decide the order of their requests (GOR-031).
func (c *Conn) reqKey() string {
	if c.side == 0 {
		return "tcp:" + c.c.id + "/cli"
	}
	return "tcp:" + c.c.id + "/srv"
}

// LocalAddr returns the local address as a *net.TCPAddr.
func (c *Conn) LocalAddr() net.Addr { return net.TCPAddrFromAddrPort(c.local) }

// RemoteAddr returns the peer's address as a *net.TCPAddr.
func (c *Conn) RemoteAddr() net.Addr { return net.TCPAddrFromAddrPort(c.remote) }

// ID returns the connection ID, e.g. "c7".
func (c *Conn) ID() string { return c.c.id }

// Read reads up to len(b) bytes that the peer sent, in order. It blocks until data or the peer's
// fin arrives, the connection fails, or the read deadline passes. After the peer's CloseWrite and
// after CloseRead it returns io.EOF. A passed deadline fails Read even when data is buffered, as in
// package net.
func (c *Conn) Read(b []byte) (int, error) {
	st := c.h.st
	st.d.Lock()
	defer st.d.Unlock()
	for { // NSM-040's checks, in order
		r := c.c.dirs[1-c.side]
		switch {
		case c.closed:
			return 0, c.opErr("read", net.ErrClosed)
		case passed(c.rdDL):
			return 0, c.opErr("read", os.ErrDeadlineExceeded)
		case len(b) == 0:
			return 0, nil
		case c.rdShut:
			return 0, io.EOF
		case len(r.buf) > 0:
			n := copy(b, r.buf)
			r.buf = r.buf[n:]
			c.requestWindow(r)
			return n, nil
		case c.errno != nil:
			return 0, c.opErr("read", c.errFor("read"))
		case r.eof:
			return 0, io.EOF
		}
		st.block("Conn.Read", c.notify, c.dlCh, c.rdDL)
	}
}

// requestWindow posts a window request when the peer's last pump stopped at the receiver window
// and none is pending (NSM-040 step 5); bytes queued after that pump have a net.pump request of
// their own. The peer's queued bytes must not decide it: the arrival that wakes this Read also
// wakes the peer's blocked Write, which queues bytes without the driver, so the request would
// depend on which goroutine takes the lock first (GOR-031). Lock held.
func (c *Conn) requestWindow(r *direction) {
	if !r.windowFull || c.windowPending {
		return
	}
	c.windowPending = true
	st := c.h.st
	st.d.Post(c.h.id, c.h.inc, "net.window", c.reqKey(),
		func() { c.windowPending = false; st.schedulePump(r) },
		func() { c.windowPending = false })
}

// writeErr is NSM-042's checks, in order. Lock held.
func (c *Conn) writeErr() error {
	switch {
	case c.closed:
		return c.opErr("write", net.ErrClosed)
	case passed(c.wrDL):
		return c.opErr("write", os.ErrDeadlineExceeded)
	case c.errno != nil:
		return c.opErr("write", c.errFor("write"))
	case c.wrShut:
		return c.opErr("write", &os.SyscallError{Syscall: "write", Err: syscall.EPIPE})
	}
	return nil
}

// Write copies b into the send buffer and returns once all of it fits. While the buffer is full it
// blocks; when the write deadline passes or the connection fails, it returns the bytes accepted so
// far with the error. After CloseWrite it fails with EPIPE.
func (c *Conn) Write(b []byte) (int, error) {
	st := c.h.st
	st.d.Lock()
	defer st.d.Unlock()
	if err := c.writeErr(); err != nil {
		return 0, err
	}
	d := c.c.dirs[c.side]
	n := 0
	for { // NSM-042; a Write that fits the free send space never waits for the driver
		if space := st.cfg.SendBuffer - len(d.queued) - d.inflight; space > 0 && n < len(b) {
			k := min(space, len(b)-n)
			d.queued = append(d.queued, b[n:n+k]...)
			n += k
			c.requestPump(d)
		}
		if n == len(b) {
			return n, nil
		}
		st.block("Conn.Write", c.notify, c.dlCh, c.wrDL)
		if err := c.writeErr(); err != nil {
			return n, err
		}
	}
}

// requestPump posts a pump request unless one is pending (NSM-042). Lock held.
func (c *Conn) requestPump(d *direction) {
	if c.pumpPending {
		return
	}
	c.pumpPending = true
	st := c.h.st
	st.d.Post(c.h.id, c.h.inc, "net.pump", c.reqKey(),
		func() { c.pumpPending = false; st.pump(d) },
		func() { c.pumpPending = false })
}

// CloseWrite shuts down the sending side. The peer reads the bytes already written, then io.EOF,
// and can still write back. Later writes fail with EPIPE.
func (c *Conn) CloseWrite() error {
	st := c.h.st
	st.d.Lock()
	defer st.d.Unlock()
	if c.closed {
		return c.opErr("close", net.ErrClosed)
	}
	if c.wrShut {
		return nil
	}
	c.wrShut = true
	c.wake()
	d, h, id := c.c.dirs[c.side], c.h, c.c.id
	st.d.Post(h.id, h.inc, "net.shutdown", c.reqKey(), func() { // NSM-047: a fin after the queued data
		d.finPending = true
		st.pump(d)
		st.emitClose(h.id, id, "shutdown")
	}, func() {})
	return nil
}

// CloseRead shuts down the receiving side. Unread bytes and later arrivals are discarded, and Read
// returns io.EOF. The peer is not told and can keep writing.
func (c *Conn) CloseRead() error {
	st := c.h.st
	st.d.Lock()
	defer st.d.Unlock()
	if c.closed {
		return c.opErr("close", net.ErrClosed)
	}
	// NSM-048: the out-of-order buffer is kept; its segments drain through normal delivery, which
	// discards their data.
	c.rdShut = true
	r := c.c.dirs[1-c.side]
	r.buf = nil
	c.wake()
	c.requestWindow(r)
	return nil
}

// SetDeadline sets the read and write deadlines, in the bubble's virtual time. A zero t means no
// deadline.
func (c *Conn) SetDeadline(t time.Time) error { return c.setDeadline(t, true, true) }

// SetReadDeadline sets the deadline of Read. A blocked Read returns os.ErrDeadlineExceeded when it
// passes, and a deadline in the past fails Read at once.
func (c *Conn) SetReadDeadline(t time.Time) error { return c.setDeadline(t, true, false) }

// SetWriteDeadline sets the deadline of Write. A Write blocked on a full send buffer returns the
// bytes accepted so far and os.ErrDeadlineExceeded when it passes.
func (c *Conn) SetWriteDeadline(t time.Time) error { return c.setDeadline(t, false, true) }

// setDeadline sets absolute bubble-time deadlines and wakes blocked calls so they re-arm their
// timers (NSM-043). Op "set" errors carry the local address only, as package net's.
func (c *Conn) setDeadline(t time.Time, rd, wr bool) error {
	st := c.h.st
	st.d.Lock()
	defer st.d.Unlock()
	if c.closed {
		return &net.OpError{Op: "set", Net: "tcp", Addr: c.LocalAddr(), Err: net.ErrClosed}
	}
	if rd {
		c.rdDL = t
	}
	if wr {
		c.wrDL = t
	}
	close(c.dlCh)
	c.dlCh = make(chan struct{})
	return nil
}

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
