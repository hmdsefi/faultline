// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim

import (
	"net"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"syscall"
	"time"

	"github.com/hmdsefi/faultline/kernel"
)

// Listener is a TCP listener. It implements net.Listener.
type Listener struct {
	h       *Host
	port    uint16
	addr    netip.AddrPort // reported Addr (NSM-018)
	closed  bool
	synrcvd int     // half-open endpoints
	queue   []*Conn // accept queue, FIFO
	notify  chan struct{}
	dl      time.Time
	dlCh    chan struct{}
}

// wake closes and replaces the listener's state channel. Lock held.
func (l *Listener) wake() {
	close(l.notify)
	l.notify = make(chan struct{})
}

// enqueue appends ep to the accept queue and wakes a blocked Accept. Lock held.
func (l *Listener) enqueue(ep *Conn) {
	l.queue = append(l.queue, ep)
	ep.inAcceptQueue = true
	l.wake()
}

// dequeue removes ep from the accept queue if it is there. Lock held.
func (l *Listener) dequeue(ep *Conn) {
	if i := slices.Index(l.queue, ep); i >= 0 {
		l.queue = slices.Delete(l.queue, i, i+1)
	}
	ep.inAcceptQueue = false
}

func (l *Listener) opErr(op string, err error) error {
	return &net.OpError{Op: op, Net: "tcp", Addr: l.Addr(), Err: err}
}

// Listen binds a TCP listener. network is "tcp" or "tcp4". The port is bound before Listen
// returns, so a second Listen on it fails with EADDRINUSE; port 0 picks an ephemeral port.
func (h *Host) Listen(network, address string) (net.Listener, error) {
	if network != "tcp" && network != "tcp4" {
		return nil, &net.OpError{Op: "listen", Net: network, Err: net.UnknownNetworkError(network)}
	}
	host, port, err := splitAddr(network, address)
	if err != nil {
		return nil, &net.OpError{Op: "listen", Net: network, Err: err}
	}
	loop, ok := h.bindKind(host)
	st := h.st
	st.d.Lock()
	defer st.d.Unlock()
	if !ok {
		var addr net.Addr // the foreign address, when it resolves
		if a, err := h.resolveLocked(host); err == nil {
			addr = net.TCPAddrFromAddrPort(netip.AddrPortFrom(a, port))
		}
		return nil, &net.OpError{Op: "listen", Net: network, Addr: addr, Err: &os.SyscallError{Syscall: "bind", Err: syscall.EADDRNOTAVAIL}}
	}
	ip := h.addr
	if loop {
		ip = loopback
	}
	addr := net.TCPAddrFromAddrPort(netip.AddrPortFrom(ip, port)) // port 0 for an ephemeral port
	if !h.alive() {
		return nil, &net.OpError{Op: "listen", Net: network, Addr: addr, Err: ErrProcessGone}
	}
	if port == 0 {
		p, ok := ephemeral(&st.node(h.id).tcpCtr, h.tcpInUse)
		if !ok {
			return nil, &net.OpError{Op: "listen", Net: network, Addr: addr, Err: &os.SyscallError{Syscall: "bind", Err: syscall.EADDRINUSE}}
		}
		port = p
	} else if h.listenInUse(port) {
		return nil, &net.OpError{Op: "listen", Net: network, Addr: addr, Err: &os.SyscallError{Syscall: "bind", Err: syscall.EADDRINUSE}}
	}
	l := &Listener{h: h, port: port, addr: netip.AddrPortFrom(ip, port), notify: make(chan struct{}), dlCh: make(chan struct{})}
	h.listeners[port] = l
	h.lports = insertSorted(h.lports, port)
	ps, bl := strconv.Itoa(int(port)), strconv.Itoa(st.cfg.Backlog)
	st.d.Post(h.id, h.inc, "net.listen", "listen:"+ps, func() {
		st.emit(h.id, "net.conn.listen", "listen :"+ps, kernel.Attr{Key: "port", Value: ps}, kernel.Attr{Key: "backlog", Value: bl})
	}, func() {})
	return l, nil
}

// Accept waits for a connection and returns the oldest one in the accept queue. It fails with
// net.ErrClosed once the listener is closed or its process is gone, and with
// os.ErrDeadlineExceeded once the deadline passes.
func (l *Listener) Accept() (net.Conn, error) {
	st := l.h.st
	st.d.Lock()
	defer st.d.Unlock()
	for {
		switch {
		case l.closed || !l.h.alive():
			return nil, l.opErr("accept", net.ErrClosed)
		case passed(l.dl):
			return nil, l.opErr("accept", os.ErrDeadlineExceeded)
		case len(l.queue) > 0:
			ep := l.queue[0]
			l.dequeue(ep)
			return ep, nil
		}
		st.block("Listener.Accept", l.notify, l.dlCh, l.dl)
	}
}

// Addr returns the listener's address as a *net.TCPAddr: 127.0.0.1 for a loopback bind, the
// node address otherwise.
func (l *Listener) Addr() net.Addr { return net.TCPAddrFromAddrPort(l.addr) }

// SetDeadline sets the deadline of Accept, in the bubble's virtual time. A zero t means no
// deadline. A new deadline wakes a blocked Accept.
func (l *Listener) SetDeadline(t time.Time) error {
	st := l.h.st
	st.d.Lock()
	defer st.d.Unlock()
	if l.closed || !l.h.alive() {
		return l.opErr("set", net.ErrClosed)
	}
	l.dl = t
	close(l.dlCh)
	l.dlCh = make(chan struct{})
	return nil
}
