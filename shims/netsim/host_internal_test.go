// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim

import (
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// stubDriver reports every incarnation alive unless dead names its node.
type stubDriver struct{ dead kernel.NodeID }

func (*stubDriver) Lock()                                                      {}
func (*stubDriver) Unlock()                                                    {}
func (d *stubDriver) Alive(node kernel.NodeID, _ uint32) bool                  { return node != d.dead }
func (*stubDriver) Post(kernel.NodeID, uint32, string, string, func(), func()) {}
func (*stubDriver) OnDriver() bool                                             { return true }

// at runs fn as a kernel event at the current virtual time.
func at(s *kernel.Sim, fn func()) {
	s.At(s.Now(), "test", fn)
	s.RunFor(time.Millisecond)
}

// The ghost check and the host marking of Release, the crash hook and Shutdown; per-node
// state outlives incarnations.
func TestHostMarking(t *testing.T) {
	s := kernel.New(kernel.Config{Seed: 1})
	nw := simnet.New(s, simnet.DefaultConfig())
	d := &stubDriver{}
	st := New(s, nw, d, Config{})
	boot := func(n *kernel.Node) { st.Host(n) }
	a := s.AddNode("a", boot)
	b := s.AddNode("b", boot)
	s.RunFor(time.Millisecond)

	ha, hb := st.cur(a.ID()), st.cur(b.ID())
	if ha == nil || hb == nil || !ha.alive() || !hb.alive() {
		t.Fatal("booted hosts missing or not alive")
	}
	if ha.Name() != "a" || ha.Addr() != netip.MustParseAddr("10.0.0.1") || ha.Incarnation() != 1 || !st.node(a.ID()).ever {
		t.Fatalf("host a: %s %s %d", ha.Name(), ha.Addr(), ha.Incarnation())
	}
	if st.cur(99) != nil {
		t.Fatal("cur of an unknown node is not nil")
	}

	d.dead = a.ID()
	if ha.alive() {
		t.Fatal("alive with the driver reporting the incarnation gone")
	}
	d.dead = 0

	st.Release(ha)
	if ha.alive() || !hb.alive() {
		t.Fatalf("after Release(a): a alive %v, b alive %v", ha.alive(), hb.alive())
	}

	at(s, b.Crash)
	if hb.alive() {
		t.Fatal("b's host alive after b crashed")
	}
	nb := st.node(b.ID())
	nb.tcpCtr, nb.udpCtr = 7, 9
	at(s, b.Restart)
	hb2 := st.cur(b.ID())
	if hb2 == hb || hb2.Incarnation() != 2 || !hb2.alive() {
		t.Fatal("restart did not install a live host for incarnation 2")
	}
	if st.node(b.ID()) != nb || nb.tcpCtr != 7 || nb.udpCtr != 9 {
		t.Fatal("per-node state did not outlive the incarnation")
	}

	st.Shutdown()
	if hb2.alive() {
		t.Fatal("host alive after Shutdown")
	}
}

func TestRender(t *testing.T) {
	s := kernel.New(kernel.Config{Seed: 1})
	st := New(s, simnet.New(s, simnet.DefaultConfig()), &stubDriver{}, Config{})
	s.AddNode("n1", func(*kernel.Node) {})
	for _, tc := range []struct{ in, want string }{
		{"10.0.0.1:5000", "n1:5000"},
		{"[::ffff:10.0.0.1]:80", "n1:80"},
		{"127.0.0.1:80", "127.0.0.1:80"},
		{"10.0.0.2:80", "10.0.0.2:80"},
		{"192.168.1.1:53", "192.168.1.1:53"},
	} {
		if got := st.render(netip.MustParseAddrPort(tc.in)); got != tc.want {
			t.Errorf("render(%s) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// A blocking call on the driver goroutine panics with NSM-003's message.
func TestBlockOnDriver(t *testing.T) {
	s := kernel.New(kernel.Config{Seed: 1})
	st := New(s, simnet.New(s, simnet.DefaultConfig()), &stubDriver{}, Config{})
	defer func() {
		want := "netsim: blocking call UDPConn.ReadFrom on the driver goroutine (body, a check or a kernel callback)"
		if v := recover(); v != want {
			t.Fatalf("panic %v, want %q", v, want)
		}
	}()
	st.block("UDPConn.ReadFrom", nil, nil, time.Time{})
}

// ListenPacket and a UDP dial of a gone incarnation fail with ErrProcessGone; WriteTo resolves
// addresses of every net.Addr type.
func TestUDPHostCalls(t *testing.T) {
	s := kernel.New(kernel.Config{Seed: 1})
	d := &stubDriver{}
	st := New(s, simnet.New(s, simnet.DefaultConfig()), d, Config{})
	var h *Host
	s.AddNode("a", func(n *kernel.Node) { h = st.Host(n) })
	s.AddNode("b", func(*kernel.Node) {})
	s.RunFor(time.Millisecond)

	u, err := h.bindUDP("listen", "udp", h.addr, 0, netip.AddrPort{})
	if err != nil || u.port != 49152 || h.udps[49152] != u || len(h.uports) != 1 {
		t.Fatalf("bindUDP: %v, port %d, ports %v", err, u.port, h.uports)
	}
	for _, tc := range []struct {
		addr net.Addr
		want string
	}{
		{&net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 9}, "10.0.0.2:9"},
		{&net.UDPAddr{IP: net.IP{10, 0, 0, 2}, Port: 9}, "10.0.0.2:9"},
		{&net.TCPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 9}, "10.0.0.2:9"},
		{&net.UnixAddr{Name: "b:9"}, "10.0.0.2:9"},
		{&net.UnixAddr{Name: "localhost:9"}, "127.0.0.1:9"},
	} {
		if got, err := h.udpTarget(tc.addr); err != nil || got.String() != tc.want {
			t.Errorf("udpTarget(%v) = %v, %v; want %s", tc.addr, got, err, tc.want)
		}
	}
	var ae *net.AddrError
	if _, err := u.WriteTo([]byte("x"), &net.UDPAddr{IP: net.IP{1, 2, 3}, Port: 9}); !errors.As(err, &ae) || ae.Err != "invalid address" {
		t.Errorf("WriteTo with a 3-byte IP: %v", err)
	}
	var dns *net.DNSError
	if _, err := u.WriteTo([]byte("x"), &net.UnixAddr{Name: "nosuch:9"}); !errors.As(err, &dns) {
		t.Errorf("WriteTo to an unknown name: %v", err)
	}

	d.dead = h.id
	if _, err := h.ListenPacket("udp", ":9"); !errors.Is(err, ErrProcessGone) {
		t.Errorf("ListenPacket of a gone incarnation: %v", err)
	}
	if _, err := h.DialContext(t.Context(), "udp", "b:9"); !errors.Is(err, ErrProcessGone) {
		t.Errorf("UDP dial of a gone incarnation: %v", err)
	}
}

func TestDatagramDescribe(t *testing.T) {
	d := &Datagram{Src: netip.MustParseAddrPort("10.0.0.1:7"), Dst: netip.MustParseAddrPort("10.0.0.2:9"), Data: []byte("hi")}
	if got, want := kernel.Describe(d), "udp 10.0.0.1:7 -> 10.0.0.2:9 len=2"; got != want {
		t.Fatalf("Describe = %q, want %q", got, want)
	}
}
