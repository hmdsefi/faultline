// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim

import (
	"net/netip"
	"testing"
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
