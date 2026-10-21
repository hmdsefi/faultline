// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim_test

import (
	"net/netip"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/shims/netsim"
)

func TestDefaultConfig(t *testing.T) {
	want := netsim.Config{
		MSS: 1460, SendBuffer: 65536, RecvBuffer: 65536, Backlog: 128,
		ConnTimeout: 30 * time.Second, RTOMin: 200 * time.Millisecond, RTOMax: 10 * time.Second,
		SynRTO: time.Second, Loopback: 10 * time.Microsecond, UDPRecvBuffer: 212992,
	}
	if got := netsim.DefaultConfig(); got != want {
		t.Fatalf("DefaultConfig() = %+v, want %+v", got, want)
	}
	if err := (netsim.Config{}).Validate(); err != nil {
		t.Fatalf("zero Config: %v", err)
	}
	if netsim.MaxDatagram != 65507 {
		t.Fatal("MaxDatagram")
	}
	if netsim.ErrProcessGone.Error() != "netsim: process is not running" {
		t.Fatal("ErrProcessGone text")
	}
}

// AT-NSM-32 (Validate) plus every §7 message.
func TestATNSM32_ConfigValidate(t *testing.T) {
	for _, tc := range []struct {
		cfg  netsim.Config
		want string
	}{
		{netsim.Config{MSS: 32}, "invalid netsim config: MSS 32 out of range [64, 65535]"},
		{netsim.Config{MSS: 65536}, "invalid netsim config: MSS 65536 out of range [64, 65535]"},
		{netsim.Config{MSS: -1}, "invalid netsim config: MSS -1 out of range [64, 65535]"},
		{netsim.Config{SendBuffer: 100}, "invalid netsim config: SendBuffer 100 is smaller than MSS 1460"},
		{netsim.Config{RecvBuffer: 100, MSS: 1460}, "invalid netsim config: RecvBuffer 100 is smaller than MSS 1460"},
		{netsim.Config{Backlog: -1}, "invalid netsim config: Backlog -1 must be >= 1"},
		{netsim.Config{ConnTimeout: -time.Second}, "invalid netsim config: ConnTimeout -1s must be > 0"},
		{netsim.Config{RTOMin: -1}, "invalid netsim config: RTOMin -1ns must be > 0"},
		{netsim.Config{SynRTO: -1}, "invalid netsim config: SynRTO -1ns must be > 0"},
		{netsim.Config{RTOMax: time.Millisecond}, "invalid netsim config: RTOMax 1ms must be >= RTOMin and SynRTO"},
		{netsim.Config{Loopback: -1}, "invalid netsim config: Loopback -1ns out of range [0s, 24h0m0s]"},
		{netsim.Config{Loopback: 25 * time.Hour}, "invalid netsim config: Loopback 25h0m0s out of range [0s, 24h0m0s]"},
		{netsim.Config{UDPRecvBuffer: 1000}, "invalid netsim config: UDPRecvBuffer 1000 is smaller than 65507"},
		// Each half of the RTOMax rule on its own.
		{netsim.Config{RTOMax: 500 * time.Millisecond}, "invalid netsim config: RTOMax 500ms must be >= RTOMin and SynRTO"},
		{netsim.Config{RTOMin: 2 * time.Second, RTOMax: time.Second}, "invalid netsim config: RTOMax 1s must be >= RTOMin and SynRTO"},
		// Two invalid fields: the first in §7's order is reported (SynRTO before RTOMax).
		{netsim.Config{MSS: 32, SendBuffer: 10}, "invalid netsim config: MSS 32 out of range [64, 65535]"},
		{netsim.Config{SynRTO: -1, RTOMax: time.Millisecond}, "invalid netsim config: SynRTO -1ns must be > 0"},
		{netsim.Config{Loopback: -1, UDPRecvBuffer: 1000}, "invalid netsim config: Loopback -1ns out of range [0s, 24h0m0s]"},
	} {
		err := tc.cfg.Validate()
		if err == nil || err.Error() != tc.want {
			t.Errorf("Validate(%+v) = %v, want %q", tc.cfg, err, tc.want)
		}
	}
	// The boundaries of each range are valid.
	for _, cfg := range []netsim.Config{
		{MSS: 64, SendBuffer: 64, RecvBuffer: 64},
		{MSS: 65535, SendBuffer: 65535, RecvBuffer: 65535},
		{RTOMax: time.Second},
		{Loopback: 24 * time.Hour},
		{UDPRecvBuffer: netsim.MaxDatagram},
	} {
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v, want nil", cfg, err)
		}
	}
}

func TestATNSM33_Addresses(t *testing.T) {
	if got := netsim.NodeAddr(1); got != netip.MustParseAddr("10.0.0.1") {
		t.Fatalf("NodeAddr(1) = %v", got)
	}
	if got := netsim.NodeAddr(258); got != netip.MustParseAddr("10.0.1.2") {
		t.Fatalf("NodeAddr(258) = %v", got)
	}
	if id, ok := netsim.AddrNode(netip.MustParseAddr("::ffff:10.0.0.3")); id != 3 || !ok {
		t.Fatalf("AddrNode(::ffff:10.0.0.3) = %d, %v", id, ok)
	}
	if id, ok := netsim.AddrNode(netip.MustParseAddr("127.0.0.1")); id != 0 || ok {
		t.Fatalf("AddrNode(127.0.0.1) = %d, %v", id, ok)
	}
	if id, ok := netsim.AddrNode(netip.MustParseAddr("10.0.0.0")); ok {
		t.Fatalf("AddrNode(10.0.0.0) = %d, true; want false (low 24 bits must be >= 1)", id)
	}
	if id, ok := netsim.AddrNode(netip.MustParseAddr("2001:db8::1")); id != 0 || ok {
		t.Fatalf("AddrNode(2001:db8::1) = %d, %v", id, ok)
	}
	if got := netsim.NodeAddr(16_777_214); got != netip.MustParseAddr("10.255.255.254") {
		t.Fatalf("NodeAddr(16777214) = %v", got)
	}
	for _, id := range []kernel.NodeID{1, 255, 256, 258, 65536, 16_777_214} {
		if got, ok := netsim.AddrNode(netsim.NodeAddr(id)); got != id || !ok {
			t.Fatalf("AddrNode(NodeAddr(%d)) = %d, %v", id, got, ok)
		}
	}
	for _, id := range []kernel.NodeID{-1, 0, 16_777_215} {
		func() {
			defer func() {
				want := "netsim: NodeAddr: node id " + map[kernel.NodeID]string{-1: "-1", 0: "0", 16_777_215: "16777215"}[id] + " out of range [1, 16777214]"
				if r := recover(); r != want {
					t.Fatalf("NodeAddr(%d) panicked with %v, want %q", id, r, want)
				}
			}()
			netsim.NodeAddr(id)
		}()
	}
}
