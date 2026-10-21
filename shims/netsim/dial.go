// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"slices"
	"syscall"
	"time"

	"github.com/hmdsefi/faultline/kernel"
)

// dialRecord is one pending TCP dial (NSM-021).
type dialRecord struct {
	h         *Host
	target    netip.AddrPort
	loop      bool
	dst       kernel.NodeID
	key       string
	done      chan struct{}
	conn      *Conn
	err       error
	finished  bool
	abandoned bool
	c         *connection
}

// Dialer mirrors net.Dialer's Timeout for a Host.
type Dialer struct {
	Host    *Host
	Timeout time.Duration // 0: no limit besides ctx and ConnTimeout
}

// Dial is DialContext(context.Background(), network, address).
func (d *Dialer) Dial(network, address string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, address)
}

// DialContext is d.Host.DialContext(ctx, network, address), except that a non-zero Timeout also
// ends a TCP dial when it passes.
func (d *Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d.Host.dial(ctx, network, address, d.Timeout)
}

// Dial is DialContext(context.Background(), network, address).
func (h *Host) Dial(network, address string) (net.Conn, error) {
	return h.DialContext(context.Background(), network, address)
}

// DialTimeout is Dialer{Host: h, Timeout: timeout}.Dial(network, address).
func (h *Host) DialTimeout(network, address string, timeout time.Duration) (net.Conn, error) {
	return (&Dialer{Host: h, Timeout: timeout}).Dial(network, address)
}

// DialContext connects to address. network "tcp" or "tcp4" gives a *Conn and blocks until the
// connection is established, the dial fails or ctx is done; "udp" or "udp4" gives a connected
// *UDPConn at once. Its signature matches http.Transport.DialContext.
func (h *Host) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return h.dial(ctx, network, address, 0)
}

func (h *Host) dial(ctx context.Context, network, address string, timeout time.Duration) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4":
		return h.dialTCP(ctx, network, address, timeout)
	case "udp", "udp4":
		return h.dialUDP(network, address)
	}
	return nil, &net.OpError{Op: "dial", Net: network, Err: net.UnknownNetworkError(network)}
}

func (dr *dialRecord) finish(c *Conn, err error) {
	if dr.finished {
		return
	}
	dr.finished, dr.conn, dr.err = true, c, err
	close(dr.done)
}

func (h *Host) removeDial(dr *dialRecord) {
	if i := slices.Index(h.dials, dr); i >= 0 {
		h.dials = slices.Delete(h.dials, i, i+1)
	}
}

// timeoutError and canceledError mirror package net's context errors (NSM-046).
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
func (timeoutError) Is(target error) bool {
	return target == context.DeadlineExceeded || target == os.ErrDeadlineExceeded
}

type canceledError struct{}

func (canceledError) Error() string        { return "operation was canceled" }
func (canceledError) Is(target error) bool { return target == context.Canceled }

func ctxError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return timeoutError{}
	}
	return canceledError{}
}

var errAbandoned = errors.New("netsim: dial abandoned")

// dialTCP is NSM-021.
func (h *Host) dialTCP(ctx context.Context, network, address string, timeout time.Duration) (net.Conn, error) {
	st := h.st
	host, port, err := splitAddr(network, address)
	if err != nil {
		return nil, &net.OpError{Op: "dial", Net: network, Err: err}
	}
	st.d.Lock()
	defer st.d.Unlock()
	ip, err := h.resolveLocked(host)
	if err != nil {
		return nil, &net.OpError{Op: "dial", Net: network, Err: err}
	}
	target := netip.AddrPortFrom(ip, port)
	taddr := net.TCPAddrFromAddrPort(target)
	loop, dst := isLoopback(ip), h.id
	if !loop {
		id, ok := AddrNode(ip)
		if !ok || st.s.Node(id) == nil {
			return nil, &net.OpError{Op: "dial", Net: network, Addr: taddr, Err: &os.SyscallError{Syscall: "connect", Err: syscall.EHOSTUNREACH}}
		}
		dst = id
	}
	if port == 0 {
		return nil, &net.OpError{Op: "dial", Net: network, Addr: taddr, Err: &net.AddrError{Err: "invalid port", Addr: address}}
	}
	if err := ctx.Err(); err != nil { // step 2
		return nil, &net.OpError{Op: "dial", Net: network, Addr: taddr, Err: ctxError(err)}
	}
	if !h.alive() { // step 3
		return nil, &net.OpError{Op: "dial", Net: network, Addr: taddr, Err: ErrProcessGone}
	}
	if st.d.OnDriver() { // NSM-003, before posting anything
		panic("netsim: blocking call Host.DialContext on the driver goroutine (body, a check or a kernel callback)")
	}
	dr := &dialRecord{h: h, target: target, loop: loop, dst: dst, key: "dial:" + target.String(), done: make(chan struct{})}
	h.dials = append(h.dials, dr)
	st.post(h, "net.dial", dr.key, func() { st.startDial(dr) },
		func() { dr.h.removeDial(dr); dr.finish(nil, ErrProcessGone) })
	st.d.Unlock()
	var tc <-chan time.Time // step 4; a negative timeout has passed already, as in package net
	if timeout != 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		tc = t.C
	}
	var cerr error
	select {
	case <-dr.done:
	case <-ctx.Done():
		cerr = ctxError(ctx.Err())
	case <-tc:
		cerr = timeoutError{}
	}
	st.d.Lock()
	if cerr != nil && (!dr.finished || dr.err != nil) { // a dial that completed successfully wins
		dr.abandoned = true
		st.post(h, "net.dial_abort", dr.key, func() { st.abortDial(dr) }, func() {})
		return nil, &net.OpError{Op: "dial", Net: network, Addr: taddr, Err: cerr}
	}
	if dr.err != nil {
		return nil, &net.OpError{Op: "dial", Net: network, Addr: taddr, Err: dr.err}
	}
	return dr.conn, nil
}

// startDial is the net.dial request (NSM-022): driver, as the dialing node, node up.
func (st *Stack) startDial(dr *dialRecord) {
	h := dr.h
	if dr.abandoned || dr.finished {
		h.removeDial(dr) // a dial abandoned before this request ran has no other way out of h.dials
		return
	}
	// An event-style node never has a netsim host. A process node may boot after this request at
	// the same instant, so its tag counts as well as its host.
	if !st.node(dr.dst).ever && !st.s.Node(dr.dst).HasTag("process") {
		h.removeDial(dr)
		st.emit(h.id, "net.conn.refused", "refused "+st.render(dr.target), kernel.Attr{Key: "conn", Value: ""})
		dr.finish(nil, &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED})
		return
	}
	port, ok := ephemeral(&st.node(h.id).tcpCtr, h.tcpInUse)
	if !ok {
		h.removeDial(dr)
		dr.finish(nil, &os.SyscallError{Syscall: "bind", Err: syscall.EADDRINUSE})
		return
	}
	c := st.newConnection(h.id, dr.dst, dr.target.Port(), dr)
	dr.c = c
	lip := h.addr
	if dr.loop {
		lip = loopback
	}
	cli := newConn(c, 0, h, stDialing, netip.AddrPortFrom(lip, port), dr.target)
	c.cli = cli
	h.addEndpoint(cli)
	src, dst := st.render(cli.local), st.render(dr.target)
	st.emit(h.id, "net.conn.dial", "dial "+c.id+" "+src+" -> "+dst,
		kernel.Attr{Key: "conn", Value: c.id}, kernel.Attr{Key: "src", Value: src}, kernel.Attr{Key: "dst", Value: dst})
	st.transmit(&segment{c: c, dir: c2s, kind: kSyn})
}

// abortDial is the net.dial_abort request (NSM-022).
func (st *Stack) abortDial(dr *dialRecord) {
	if c := dr.c; c != nil && c.dial == dr && c.cli != nil && c.cli.state == stDialing {
		st.failDial(c, errAbandoned, false)
	}
}
