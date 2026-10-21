// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim

import (
	"errors"
	"net"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

func TestIsLoopback(t *testing.T) {
	if loopback != netip.MustParseAddr("127.0.0.1") {
		t.Fatalf("loopback = %v, want 127.0.0.1", loopback)
	}
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.255.255.254", true},
		{"::ffff:127.0.0.2", true},
		{"10.0.0.1", false},
		{"128.0.0.1", false},
		{"::1", false},
	} {
		if got := isLoopback(netip.MustParseAddr(tc.addr)); got != tc.want {
			t.Errorf("isLoopback(%s) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

func TestWithDefaults(t *testing.T) {
	if got := (Config{}).withDefaults(); got != DefaultConfig() {
		t.Fatalf("Config{}.withDefaults() = %+v, want DefaultConfig() %+v", got, DefaultConfig())
	}
}

func TestSplitAddr(t *testing.T) {
	host, port, err := splitAddr("udp", "a:65535")
	if host != "a" || port != 65535 || err != nil {
		t.Fatalf("splitAddr(a:65535) = %q, %d, %v", host, port, err)
	}
	for _, tc := range []struct{ addr, want string }{
		{"a:65536", "udp/65536"},
		{"a:-1", "udp/-1"},
		{"a:http", "udp/http"},
		{"a:", "udp/"},
	} {
		var ae *net.AddrError
		if _, _, err := splitAddr("udp", tc.addr); !errors.As(err, &ae) || ae.Err != "unknown port" || ae.Addr != tc.want {
			t.Errorf("splitAddr(%s) error %v, want unknown port %s", tc.addr, err, tc.want)
		}
	}
	if _, _, err := splitAddr("udp", "no-port"); err == nil {
		t.Error("splitAddr without a port succeeded")
	}
}

func TestBindKind(t *testing.T) {
	s := kernel.New(kernel.Config{Seed: 1})
	st := New(s, simnet.New(s, simnet.DefaultConfig()), &stubDriver{}, Config{})
	var h *Host
	s.AddNode("a", func(n *kernel.Node) { h = st.Host(n) })
	s.RunFor(time.Millisecond)
	for _, tc := range []struct {
		host     string
		loop, ok bool
	}{
		{"", false, true},
		{"0.0.0.0", false, true},
		{"::", false, true},
		{"a", false, true},
		{"10.0.0.1", false, true},
		{"::ffff:10.0.0.1", false, true},
		{"localhost", true, true},
		{"::1", true, true},
		{"127.0.0.1", true, true},
		{"127.1.2.3", true, true},
		{"b", false, false},
		{"10.0.0.2", false, false},
		{"192.168.0.1", false, false},
	} {
		if loop, ok := h.bindKind(tc.host); loop != tc.loop || ok != tc.ok {
			t.Errorf("bindKind(%q) = %v, %v; want %v, %v", tc.host, loop, ok, tc.loop, tc.ok)
		}
	}
}

func TestEphemeral(t *testing.T) {
	var ctr uint64
	used := map[uint16]bool{49152: true, 49153: true}
	if p, ok := ephemeral(&ctr, func(p uint16) bool { return used[p] }); p != 49154 || !ok || ctr != 3 {
		t.Fatalf("ephemeral = %d, %v, counter %d; want 49154, true, 3", p, ok, ctr)
	}
	ctr = 16383
	if p, ok := ephemeral(&ctr, func(uint16) bool { return false }); p != 65535 || !ok || ctr != 16384 {
		t.Fatalf("ephemeral at counter 16383 = %d, %v, counter %d", p, ok, ctr)
	}
	if p, ok := ephemeral(&ctr, func(uint16) bool { return false }); p != 49152 || !ok {
		t.Fatalf("ephemeral after the wrap = %d, %v; want 49152", p, ok)
	}
	ctr = 5
	tries := 0
	if _, ok := ephemeral(&ctr, func(uint16) bool { tries++; return true }); ok || tries != 16384 || ctr != 5+16384 {
		t.Fatalf("ephemeral with every port used: ok %v after %d tries, counter %d", ok, tries, ctr)
	}
}

func TestSortedSlices(t *testing.T) {
	var s []uint16
	for _, v := range []uint16{9, 3, 7, 3} {
		s = insertSorted(s, v)
	}
	if !slices.Equal(s, []uint16{3, 7, 9}) {
		t.Fatalf("insertSorted gave %v", s)
	}
	s = removeSorted(removeSorted(s, 7), 8)
	if !slices.Equal(s, []uint16{3, 9}) {
		t.Fatalf("removeSorted gave %v", s)
	}
}
