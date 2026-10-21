// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// Datagram is the simnet payload of one UDP datagram. Event-style nodes may send *Datagram to a
// process node with simnet.Network.Send and receive *Datagram from processes. Data must not be
// modified after sending.
type Datagram struct {
	Src  netip.AddrPort
	Dst  netip.AddrPort
	Data []byte
}

// Describe returns "udp <src> -> <dst> len=<n>", the text the trace shows for the datagram.
func (d *Datagram) Describe() string {
	return fmt.Sprintf("udp %s -> %s len=%d", d.Src, d.Dst, len(d.Data))
}

// UDPConn is a UDP socket. It implements net.PacketConn and net.Conn (Read, Write and RemoteAddr
// need a connected socket from DialContext).
type UDPConn struct {
	h         *Host
	port      uint16
	local     netip.AddrPort
	remote    netip.AddrPort
	connected bool
	closed    bool
	queue     []*Datagram
	queued    int
	notify    chan struct{}
	rdDL      time.Time
	wrDL      time.Time
	dlCh      chan struct{}
}

func (u *UDPConn) wake() {
	close(u.notify)
	u.notify = make(chan struct{})
}

func (u *UDPConn) opErr(op string, addr net.Addr, err error) error {
	return &net.OpError{Op: op, Net: "udp", Source: u.LocalAddr(), Addr: addr, Err: err}
}

// ListenPacket binds a UDP socket. network is "udp" or "udp4". The result is a *UDPConn.
func (h *Host) ListenPacket(network, address string) (net.PacketConn, error) {
	if network != "udp" && network != "udp4" {
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
		var at net.Addr
		if ip, err := h.resolveLocked(host); err == nil {
			at = net.UDPAddrFromAddrPort(netip.AddrPortFrom(ip, port))
		}
		return nil, &net.OpError{Op: "listen", Net: network, Addr: at, Err: &os.SyscallError{Syscall: "bind", Err: syscall.EADDRNOTAVAIL}}
	}
	ip := h.addr
	if loop {
		ip = loopback
	}
	if !h.alive() {
		return nil, &net.OpError{Op: "listen", Net: network, Addr: net.UDPAddrFromAddrPort(netip.AddrPortFrom(ip, port)), Err: ErrProcessGone}
	}
	u, err := h.bindUDP("listen", network, ip, port, netip.AddrPort{})
	if err != nil {
		return nil, err
	}
	return u, nil
}

// bindUDP binds a socket at once and posts its net.udp.bind record (NSM-050). Lock held.
func (h *Host) bindUDP(op, network string, ip netip.Addr, port uint16, remote netip.AddrPort) (*UDPConn, error) {
	st := h.st
	if port == 0 {
		p, ok := ephemeral(&st.node(h.id).udpCtr, func(p uint16) bool { return h.udps[p] != nil })
		if !ok {
			return nil, bindInUse(op, network, ip, port, remote)
		}
		port = p
	} else if h.udps[port] != nil {
		return nil, bindInUse(op, network, ip, port, remote)
	}
	u := &UDPConn{h: h, port: port, local: netip.AddrPortFrom(ip, port), remote: remote, connected: remote.IsValid(),
		notify: make(chan struct{}), dlCh: make(chan struct{})}
	h.udps[port] = u
	h.uports = insertSorted(h.uports, port)
	ps := strconv.Itoa(int(port))
	st.d.Post(h.id, h.inc, "net.udp.bind", "udp:"+ps, func() {
		st.emit(h.id, "net.udp.bind", "udp bind :"+ps, kernel.Attr{Key: "port", Value: ps})
	}, func() {})
	return u, nil
}

// bindInUse is bindUDP's EADDRINUSE error. Its Addr is the dialed address, or else the address
// being bound, as package net reports them.
func bindInUse(op, network string, ip netip.Addr, port uint16, remote netip.AddrPort) error {
	at := remote
	if !remote.IsValid() {
		at = netip.AddrPortFrom(ip, port)
	}
	return &net.OpError{Op: op, Net: network, Addr: net.UDPAddrFromAddrPort(at), Err: &os.SyscallError{Syscall: "bind", Err: syscall.EADDRINUSE}}
}

// Dial is DialContext(context.Background(), network, address).
func (h *Host) Dial(network, address string) (net.Conn, error) {
	return h.DialContext(context.Background(), network, address)
}

// DialContext connects to address. network is "udp" or "udp4", and the result is a connected
// *UDPConn. Its signature matches http.Transport.DialContext.
func (h *Host) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	switch network {
	case "udp", "udp4":
		return h.dialUDP(network, address)
	}
	return nil, &net.OpError{Op: "dial", Net: network, Err: net.UnknownNetworkError(network)}
}

// dialUDP binds an ephemeral socket connected to address (NSM-050).
func (h *Host) dialUDP(network, address string) (net.Conn, error) {
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
	if !isLoopback(ip) {
		if id, ok := AddrNode(ip); !ok || st.s.Node(id) == nil {
			return nil, &net.OpError{Op: "dial", Net: network, Addr: net.UDPAddrFromAddrPort(target),
				Err: &os.SyscallError{Syscall: "connect", Err: syscall.EHOSTUNREACH}}
		}
	}
	if !h.alive() {
		return nil, &net.OpError{Op: "dial", Net: network, Addr: net.UDPAddrFromAddrPort(target), Err: ErrProcessGone}
	}
	local := h.addr
	if isLoopback(ip) {
		local = loopback
	}
	u, err := h.bindUDP("dial", network, local, 0, target)
	if err != nil {
		return nil, err
	}
	return u, nil
}

// errMissingAddress is package net's error for a WriteTo to a nil *net.UDPAddr.
var errMissingAddress = errors.New("missing address")

// udpTarget resolves a WriteTo address (NSM-051, NSM §7). Lock held.
func (h *Host) udpTarget(addr net.Addr) (netip.AddrPort, error) {
	if addr == nil {
		return netip.AddrPort{}, syscall.EINVAL
	}
	if ua, ok := addr.(*net.UDPAddr); ok {
		if ua == nil {
			return netip.AddrPort{}, errMissingAddress
		}
		ip, ok := netip.AddrFromSlice(ua.IP)
		if !ok {
			return netip.AddrPort{}, &net.AddrError{Err: "invalid address", Addr: ua.String()}
		}
		if ua.Port < 0 || ua.Port > 65535 {
			return netip.AddrPort{}, &os.SyscallError{Syscall: "sendto", Err: syscall.EINVAL}
		}
		return netip.AddrPortFrom(ip.Unmap(), uint16(ua.Port)), nil
	}
	host, port, err := splitAddr("udp", addr.String())
	if err != nil {
		return netip.AddrPort{}, err
	}
	ip, err := h.resolveLocked(host)
	if err != nil {
		return netip.AddrPort{}, err
	}
	return netip.AddrPortFrom(ip, port), nil
}

// WriteTo sends b as one datagram to addr and returns len(b) without waiting for delivery. It
// fails with EMSGSIZE when b is longer than MaxDatagram.
func (u *UDPConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	st := u.h.st
	st.d.Lock()
	defer st.d.Unlock()
	return u.writeTo(b, addr)
}

func (u *UDPConn) writeTo(b []byte, addr net.Addr) (int, error) {
	st := u.h.st
	switch {
	case u.closed:
		return 0, u.opErr("write", addr, net.ErrClosed)
	case passed(u.wrDL):
		return 0, u.opErr("write", addr, os.ErrDeadlineExceeded)
	case len(b) > MaxDatagram:
		return 0, u.opErr("write", addr, &os.SyscallError{Syscall: "sendto", Err: syscall.EMSGSIZE})
	}
	dst, err := u.h.udpTarget(addr)
	if err != nil {
		if ua, ok := addr.(*net.UDPAddr); ok && ua == nil {
			addr = nil // package net reports no Addr for a nil *net.UDPAddr
		}
		return 0, u.opErr("write", addr, err)
	}
	data, src, h := bytes.Clone(b), u.local, u.h
	st.d.Post(h.id, h.inc, "net.udp.send", "udp:"+strconv.Itoa(int(u.port)), func() { st.sendDatagram(h, src, dst, data) }, func() {})
	return len(b), nil
}

// sendDatagram is the net.udp.send request (driver, sender up).
func (st *Stack) sendDatagram(h *Host, src, dst netip.AddrPort, data []byte) {
	d := &Datagram{Src: src, Dst: dst, Data: data}
	if isLoopback(dst.Addr()) {
		st.nw.SendRaw(h.n, h.id, d, simnet.RawOptions{Delay: st.cfg.Loopback, FIFO: true})
		return
	}
	id, ok := AddrNode(dst.Addr())
	if !ok || st.s.Node(id) == nil {
		st.emitUDPDrop(h.id, src, dst, len(data), "no-route")
		return
	}
	st.nw.Send(h.n, id, d)
}

// deliverDatagram handles a *Datagram arriving at node id (NSM-052).
func (st *Stack) deliverDatagram(id kernel.NodeID, d *Datagram) {
	h := st.cur(id)
	var u *UDPConn
	if h != nil && !h.gone {
		u = h.udps[d.Dst.Port()]
	}
	reason := ""
	switch {
	case u == nil:
		reason = "no-socket"
	case u.connected && u.remote != d.Src:
		reason = "filtered"
	case u.queued+len(d.Data) > st.cfg.UDPRecvBuffer:
		reason = "overflow"
	}
	if reason != "" {
		st.emitUDPDrop(id, d.Src, d.Dst, len(d.Data), reason)
		return
	}
	u.queue = append(u.queue, d)
	u.queued += len(d.Data)
	u.wake()
	src, dst, n := st.render(d.Src), st.render(d.Dst), strconv.Itoa(len(d.Data))
	st.emit(id, "net.udp.recv", "udp recv "+src+" -> "+dst+" len="+n,
		kernel.Attr{Key: "src", Value: src}, kernel.Attr{Key: "dst", Value: dst}, kernel.Attr{Key: "len", Value: n})
}

// ReadFrom copies the oldest queued datagram into b and returns its length and source address.
// It blocks until a datagram arrives, the read deadline passes or the socket closes. Bytes that do
// not fit in b are discarded.
func (u *UDPConn) ReadFrom(b []byte) (int, net.Addr, error) {
	return u.readFrom("UDPConn.ReadFrom", b)
}

// Read is ReadFrom without the source address.
func (u *UDPConn) Read(b []byte) (int, error) {
	n, _, err := u.readFrom("UDPConn.Read", b)
	return n, err
}

// readFrom is ReadFrom; method names the caller in the NSM-003 panic.
func (u *UDPConn) readFrom(method string, b []byte) (int, net.Addr, error) {
	st := u.h.st
	st.d.Lock()
	defer st.d.Unlock()
	for {
		switch {
		case u.closed:
			return 0, nil, u.opErr("read", u.RemoteAddr(), net.ErrClosed)
		case passed(u.rdDL):
			return 0, nil, u.opErr("read", u.RemoteAddr(), os.ErrDeadlineExceeded)
		case len(u.queue) > 0:
			d := u.queue[0]
			u.queue[0] = nil
			u.queue = u.queue[1:]
			u.queued -= len(d.Data)
			return copy(b, d.Data), net.UDPAddrFromAddrPort(d.Src), nil
		}
		st.block(method, u.notify, u.dlCh, u.rdDL)
	}
}

// Write sends b to the connected address. On a socket from ListenPacket it fails with
// EDESTADDRREQ.
func (u *UDPConn) Write(b []byte) (int, error) {
	st := u.h.st
	st.d.Lock()
	defer st.d.Unlock()
	switch {
	case !u.connected && u.closed:
		return 0, u.opErr("write", nil, net.ErrClosed)
	case !u.connected:
		return 0, u.opErr("write", nil, &os.SyscallError{Syscall: "write", Err: syscall.EDESTADDRREQ})
	}
	return u.writeTo(b, u.RemoteAddr())
}

// Close closes the socket, frees its port and wakes blocked readers. A second Close returns
// net.ErrClosed.
func (u *UDPConn) Close() error {
	st := u.h.st
	st.d.Lock()
	defer st.d.Unlock()
	if u.closed {
		return u.opErr("close", u.RemoteAddr(), net.ErrClosed)
	}
	st.closeUDP(u)
	h, port := u.h, u.port
	st.d.Post(h.id, h.inc, "net.udp.close", "udp:"+strconv.Itoa(int(port)), func() { st.emitUDPClose(h.id, port, "close") }, func() {})
	return nil
}

// closeUDP marks u closed, unbinds its port and wakes its readers. Lock held.
func (st *Stack) closeUDP(u *UDPConn) {
	u.closed = true
	if u.h.udps[u.port] == u {
		delete(u.h.udps, u.port)
		u.h.uports = removeSorted(u.h.uports, u.port)
	}
	u.wake()
}

// LocalAddr returns the bound address as a *net.UDPAddr.
func (u *UDPConn) LocalAddr() net.Addr { return net.UDPAddrFromAddrPort(u.local) }

// RemoteAddr returns the connected address as a *net.UDPAddr, or nil for a socket from
// ListenPacket.
func (u *UDPConn) RemoteAddr() net.Addr {
	if !u.connected {
		return nil
	}
	return net.UDPAddrFromAddrPort(u.remote)
}

// SetDeadline sets the read and write deadlines, in the bubble's virtual time. A zero t means
// no deadline.
func (u *UDPConn) SetDeadline(t time.Time) error { return u.setDeadline(t, true, true) }

// SetReadDeadline sets the deadline of ReadFrom and Read. A passed deadline wakes blocked readers
// with os.ErrDeadlineExceeded.
func (u *UDPConn) SetReadDeadline(t time.Time) error { return u.setDeadline(t, true, false) }

// SetWriteDeadline sets the deadline of WriteTo and Write. Writes never block, so a write fails
// only when the deadline has already passed.
func (u *UDPConn) SetWriteDeadline(t time.Time) error { return u.setDeadline(t, false, true) }

// setDeadline sets the chosen deadlines and wakes blocked calls so they re-arm (NSM-043).
func (u *UDPConn) setDeadline(t time.Time, rd, wr bool) error {
	st := u.h.st
	st.d.Lock()
	defer st.d.Unlock()
	if u.closed {
		return &net.OpError{Op: "set", Net: "udp", Addr: u.LocalAddr(), Err: net.ErrClosed}
	}
	if rd {
		u.rdDL = t
	}
	if wr {
		u.wrDL = t
	}
	close(u.dlCh)
	u.dlCh = make(chan struct{})
	return nil
}
