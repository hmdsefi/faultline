// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim

import (
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
