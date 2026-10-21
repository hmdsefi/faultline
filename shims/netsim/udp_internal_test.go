// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim

import (
	"errors"
	"net"
	"net/netip"
	"os"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// recDriver is a stubDriver that records the kind and key of every posted request.
type recDriver struct {
	stubDriver
	posts []string
}

func (d *recDriver) Post(_ kernel.NodeID, _ uint32, kind, key string, _, _ func()) {
	d.posts = append(d.posts, kind+" "+key)
}

// udpHost returns the host of node a (10.0.0.1) next to node b (10.0.0.2), on a recDriver.
func udpHost(t *testing.T) (*Host, *recDriver) {
	t.Helper()
	s := kernel.New(kernel.Config{Seed: 1})
	d := &recDriver{}
	st := New(s, simnet.New(s, simnet.DefaultConfig()), d, Config{})
	var h *Host
	s.AddNode("a", func(n *kernel.Node) { h = st.Host(n) })
	s.AddNode("b", func(*kernel.Node) {})
	s.RunFor(time.Millisecond)
	return h, d
}

// asOpErr returns err as a *net.OpError, failing the test if it is not one.
func asOpErr(t *testing.T, what string, err error) *net.OpError {
	t.Helper()
	var oe *net.OpError
	if !errors.As(err, &oe) {
		t.Fatalf("%s: %v (%T), want a *net.OpError", what, err, err)
	}
	return oe
}

// addrString prints a net.Addr, "nil" for a nil interface.
func addrString(a net.Addr) string {
	if a == nil {
		return "nil"
	}
	return a.String()
}

// A failed bind returns a nil conn, as package net does, and the error carries the address.
func TestUDPBindErrors(t *testing.T) {
	h, d := udpHost(t)
	if _, err := h.ListenPacket("udp", ":9"); err != nil {
		t.Fatal(err)
	}
	type row struct {
		what string
		conn any
		err  error
		is   error
		addr string
	}
	var rows []row
	add := func(what string, conn any, err, is error, addr string) {
		rows = append(rows, row{what, conn, err, is, addr})
	}
	pc, err := h.ListenPacket("udp", ":9")
	add("port in use", pc, err, syscall.EADDRINUSE, "10.0.0.1:9")
	pc, err = h.ListenPacket("udp", "b:9")
	add("bind to another node", pc, err, syscall.EADDRNOTAVAIL, "10.0.0.2:9")
	pc, err = h.ListenPacket("udp", "nosuch:9")
	add("bind to an unknown name", pc, err, syscall.EADDRNOTAVAIL, "nil")

	for p := uint16(65535); p >= 49152; p-- {
		h.udps[p] = &UDPConn{}
	}
	pc, err = h.ListenPacket("udp", ":0")
	add("ephemeral ports exhausted (listen)", pc, err, syscall.EADDRINUSE, "10.0.0.1:0")
	pc, err = h.ListenPacket("udp", "localhost:0")
	add("ephemeral ports exhausted (loopback listen)", pc, err, syscall.EADDRINUSE, "127.0.0.1:0")
	c, err := h.Dial("udp", "b:9")
	add("ephemeral ports exhausted (dial)", c, err, syscall.EADDRINUSE, "10.0.0.2:9")

	d.dead = h.id
	pc, err = h.ListenPacket("udp", "localhost:7")
	add("gone incarnation", pc, err, ErrProcessGone, "127.0.0.1:7")

	for _, r := range rows {
		oe := asOpErr(t, r.what, r.err)
		if r.conn != nil {
			t.Errorf("%s: conn %#v, want nil", r.what, r.conn)
		}
		if !errors.Is(r.err, r.is) || addrString(oe.Addr) != r.addr || oe.Source != nil {
			t.Errorf("%s: %v (Addr %s, Source %v), want %v with Addr %s", r.what, r.err, addrString(oe.Addr), oe.Source, r.is, r.addr)
		}
	}
	if want := []string{"net.udp.bind udp:9"}; !slices.Equal(d.posts, want) {
		t.Errorf("posts %v, want %v", d.posts, want)
	}
}

// Bind, send and close of one socket are requests keyed by its local port (NSM §6); a write
// that fails posts nothing.
func TestUDPRequestKeys(t *testing.T) {
	h, d := udpHost(t)
	pc, err := h.ListenPacket("udp", ":9")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pc.WriteTo([]byte("x"), &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 9}); err != nil {
		t.Fatal(err)
	}
	if _, err := pc.WriteTo([]byte("x"), &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 65536}); err == nil {
		t.Fatal("WriteTo port 65536 succeeded")
	}
	if err := pc.Close(); err != nil {
		t.Fatal(err)
	}
	want := []string{"net.udp.bind udp:9", "net.udp.send udp:9", "net.udp.close udp:9"}
	if !slices.Equal(d.posts, want) {
		t.Fatalf("posts %v, want %v", d.posts, want)
	}
}

// WriteTo rejects a missing address and an out-of-range port as package net does, and
// reports EMSGSIZE from sendto.
func TestUDPWriteToErrors(t *testing.T) {
	h, d := udpHost(t)
	pc, err := h.ListenPacket("udp", ":9")
	if err != nil {
		t.Fatal(err)
	}
	var nilUDP *net.UDPAddr
	for _, tc := range []struct {
		what string
		b    []byte
		addr net.Addr
		err  string // OpError.Err.Error()
		is   error
		dst  string // OpError.Addr
	}{
		{"nil address", []byte("x"), nil, "invalid argument", syscall.EINVAL, "nil"},
		{"nil *net.UDPAddr", []byte("x"), nilUDP, "missing address", nil, "nil"},
		{"port 65536", []byte("x"), &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 65536}, "sendto: invalid argument", syscall.EINVAL, "10.0.0.2:65536"},
		{"port -1", []byte("x"), &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: -1}, "sendto: invalid argument", syscall.EINVAL, "10.0.0.2:-1"},
		{"too long", make([]byte, MaxDatagram+1), &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 9}, "sendto: message too long", syscall.EMSGSIZE, "10.0.0.2:9"},
	} {
		n, err := pc.WriteTo(tc.b, tc.addr)
		oe := asOpErr(t, tc.what, err)
		if n != 0 || oe.Op != "write" || oe.Net != "udp" || oe.Err.Error() != tc.err || (tc.is != nil && !errors.Is(err, tc.is)) ||
			addrString(oe.Source) != "10.0.0.1:9" || addrString(oe.Addr) != tc.dst {
			t.Errorf("%s: %d, %v (Err %q, Source %s, Addr %s), want Err %q, Addr %s", tc.what, n, err, oe.Err, addrString(oe.Source),
				addrString(oe.Addr), tc.err, tc.dst)
		}
	}
	if len(d.posts) != 1 {
		t.Errorf("posts %v, want only the bind", d.posts)
	}
}

// Read and ReadFrom on the driver goroutine panic with their own names (NSM-003).
func TestUDPReadOnDriver(t *testing.T) {
	h, _ := udpHost(t)
	pc, err := h.ListenPacket("udp", ":9")
	if err != nil {
		t.Fatal(err)
	}
	u, ok := pc.(*UDPConn)
	if !ok {
		t.Fatalf("ListenPacket returned %T", pc)
	}
	for _, tc := range []struct {
		name string
		call func()
	}{
		{"UDPConn.Read", func() { _, _ = u.Read(make([]byte, 1)) }},
		{"UDPConn.ReadFrom", func() { _, _, _ = u.ReadFrom(make([]byte, 1)) }},
	} {
		func() {
			want := "netsim: blocking call " + tc.name + " on the driver goroutine (body, a check or a kernel callback)"
			defer func() {
				if v := recover(); v != want {
					t.Errorf("panic %v, want %q", v, want)
				}
			}()
			tc.call()
		}()
	}
}

// The queue, the deadlines, the closed state and the OpError fields of an unconnected socket.
func TestUDPSocketState(t *testing.T) {
	h, _ := udpHost(t)
	pc, err := h.ListenPacket("udp", ":9")
	if err != nil {
		t.Fatal(err)
	}
	u, ok := pc.(*UDPConn)
	if !ok {
		t.Fatalf("ListenPacket returned %T", pc)
	}
	to := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 9}
	past := time.Now().Add(-time.Second)
	check := func(what string, err, is error, op, src, dst string) {
		t.Helper()
		oe := asOpErr(t, what, err)
		if !errors.Is(err, is) || oe.Op != op || addrString(oe.Source) != src || addrString(oe.Addr) != dst {
			t.Errorf("%s: %v (Op %s, Source %s, Addr %s), want %v, Op %s, Source %s, Addr %s", what, err, oe.Op,
				addrString(oe.Source), addrString(oe.Addr), is, op, src, dst)
		}
	}

	u.queue = []*Datagram{
		{Src: netip.MustParseAddrPort("10.0.0.2:7"), Data: []byte("first")},
		{Src: netip.MustParseAddrPort("10.0.0.2:8"), Data: []byte("second")},
	}
	u.queued = 11
	backing := u.queue

	_ = u.SetReadDeadline(past)
	_, _, err = u.ReadFrom(make([]byte, 10))
	check("read with a passed deadline and queued data", err, os.ErrDeadlineExceeded, "read", "10.0.0.1:9", "nil")

	_ = u.SetReadDeadline(time.Time{})
	_ = u.SetWriteDeadline(past)
	buf := make([]byte, 2)
	n, from, err := u.ReadFrom(buf)
	if err != nil || n != 2 || string(buf) != "fi" || addrString(from) != "10.0.0.2:7" || u.queued != 6 || len(u.queue) != 1 {
		t.Fatalf("ReadFrom with a 2-byte buffer = %d %q from %s, %v; queued %d, %d datagrams", n, buf[:n], addrString(from), err,
			u.queued, len(u.queue))
	}
	if backing[0] != nil {
		t.Error("the queue still references the datagram it returned")
	}
	_, err = u.WriteTo([]byte("x"), to)
	check("write with a passed write deadline", err, os.ErrDeadlineExceeded, "write", "10.0.0.1:9", "10.0.0.2:9")

	_ = u.SetWriteDeadline(time.Time{})
	_ = u.SetReadDeadline(past)
	if _, err := u.WriteTo([]byte("x"), to); err != nil {
		t.Errorf("write with only a passed read deadline: %v", err)
	}
	_, err = u.Write([]byte("x"))
	check("Write on an unconnected socket", err, syscall.EDESTADDRREQ, "write", "10.0.0.1:9", "nil")

	_ = u.SetWriteDeadline(past)
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = u.WriteTo([]byte("x"), to)
	check("WriteTo after Close with a passed write deadline", err, net.ErrClosed, "write", "10.0.0.1:9", "10.0.0.2:9")
	_, err = u.Write([]byte("x"))
	check("Write on a closed unconnected socket", err, net.ErrClosed, "write", "10.0.0.1:9", "nil")
	_, _, err = u.ReadFrom(make([]byte, 1))
	check("ReadFrom after Close", err, net.ErrClosed, "read", "10.0.0.1:9", "nil")
	check("second Close", u.Close(), net.ErrClosed, "close", "10.0.0.1:9", "nil")
	check("SetDeadline after Close", u.SetDeadline(time.Time{}), net.ErrClosed, "set", "nil", "10.0.0.1:9")

	if _, err := h.ListenPacket("udp", ":9"); err != nil {
		t.Errorf("port 9 not free after Close: %v", err)
	}
}

// The OpError fields of a connected socket name the remote address, as package net does.
func TestUDPConnectedErrors(t *testing.T) {
	h, _ := udpHost(t)
	c, err := h.Dial("udp", "b:9")
	if err != nil {
		t.Fatal(err)
	}
	check := func(what string, err, is error, op, src, dst string) {
		t.Helper()
		oe := asOpErr(t, what, err)
		if !errors.Is(err, is) || oe.Op != op || addrString(oe.Source) != src || addrString(oe.Addr) != dst {
			t.Errorf("%s: %v (Op %s, Source %s, Addr %s), want %v, Op %s, Source %s, Addr %s", what, err, oe.Op,
				addrString(oe.Source), addrString(oe.Addr), is, op, src, dst)
		}
	}
	_ = c.SetReadDeadline(time.Now().Add(-time.Second))
	_, err = c.Read(make([]byte, 1))
	check("Read with a passed deadline", err, os.ErrDeadlineExceeded, "read", "10.0.0.1:49152", "10.0.0.2:9")
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = c.Read(make([]byte, 1))
	check("Read after Close", err, net.ErrClosed, "read", "10.0.0.1:49152", "10.0.0.2:9")
	_, err = c.Write([]byte("x"))
	check("Write after Close", err, net.ErrClosed, "write", "10.0.0.1:49152", "10.0.0.2:9")
	check("second Close", c.Close(), net.ErrClosed, "close", "10.0.0.1:49152", "10.0.0.2:9")
	check("SetReadDeadline after Close", c.SetReadDeadline(time.Time{}), net.ErrClosed, "set", "nil", "10.0.0.1:49152")
}
