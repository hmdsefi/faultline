// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim_test

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
	"github.com/hmdsefi/faultline/shims/netsim"
)

type fakeDriver struct{}

func (fakeDriver) Lock()                                                      {}
func (fakeDriver) Unlock()                                                    {}
func (fakeDriver) Alive(kernel.NodeID, uint32) bool                           { return true }
func (fakeDriver) Post(kernel.NodeID, uint32, string, string, func(), func()) {}
func (fakeDriver) OnDriver() bool                                             { return true }

// AT-NSM-32, New part.
func TestATNSM32_NewPanics(t *testing.T) {
	s := kernel.New(kernel.Config{Seed: 1})
	nw := simnet.New(s, simnet.DefaultConfig())
	for _, tc := range []struct {
		name string
		f    func()
		want string
	}{
		{"nil sim", func() { netsim.New(nil, nw, fakeDriver{}, netsim.Config{}) }, "netsim: New: nil *kernel.Sim"},
		{"nil network", func() { netsim.New(s, nil, fakeDriver{}, netsim.Config{}) }, "netsim: New: nil *simnet.Network"},
		{"nil driver", func() { netsim.New(s, nw, nil, netsim.Config{}) }, "netsim: New: nil Driver"},
		{"MSS", func() { netsim.New(s, nw, fakeDriver{}, netsim.Config{MSS: 32}) }, "netsim: New: invalid Config: invalid netsim config: MSS 32 out of range [64, 65535]"},
		{"RecvBuffer", func() { netsim.New(s, nw, fakeDriver{}, netsim.Config{RecvBuffer: 100, MSS: 1460}) }, "netsim: New: invalid Config: invalid netsim config: RecvBuffer 100 is smaller than MSS 1460"},
		{"RTOMax", func() { netsim.New(s, nw, fakeDriver{}, netsim.Config{RTOMax: time.Millisecond}) }, "netsim: New: invalid Config: invalid netsim config: RTOMax 1ms must be >= RTOMin and SynRTO"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != tc.want {
					t.Fatalf("panic %v, want %q", r, tc.want)
				}
			}()
			tc.f()
		})
	}
}

func TestHostIdentityAndResolve(t *testing.T) {
	type obs struct {
		name string
		addr netip.Addr
		inc  uint32
		res  map[string]string
	}
	var got result[obs]
	world(t, netsim.Config{}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			o := obs{name: p.Net.Name(), addr: p.Net.Addr(), inc: p.Net.Incarnation(), res: map[string]string{}}
			for _, host := range []string{"", "localhost", "::1", "127.0.0.5", "10.0.0.7", "b", "fe80::1", "zz"} {
				a, err := p.Net.Resolve(host)
				var dns *net.DNSError
				var ae *net.AddrError
				switch {
				case err == nil:
					o.res[host] = a.String()
				case errors.As(err, &dns) && dns.IsNotFound && dns.Name == host && dns.Err == "no such host":
					o.res[host] = "dns-not-found"
				case errors.As(err, &ae) && ae.Err == "IPv6 is not supported by netsim" && ae.Addr == host:
					o.res[host] = "ipv6"
				default:
					o.res[host] = "unexpected: " + err.Error()
				}
			}
			got.put(o)
			<-ctx.Done()
			return nil
		})
		w.AddProcess("b", wait)
	})
	o := got.get(t)
	if o.name != "a" || o.addr != netip.MustParseAddr("10.0.0.1") || o.inc != 1 {
		t.Fatalf("host %+v", o)
	}
	want := map[string]string{
		"": "127.0.0.1", "localhost": "127.0.0.1", "::1": "127.0.0.1", "127.0.0.5": "127.0.0.1",
		"10.0.0.7": "10.0.0.7", "b": "10.0.0.2", "fe80::1": "ipv6", "zz": "dns-not-found",
	}
	for k, v := range want {
		if o.res[k] != v {
			t.Errorf("Resolve(%q) = %s, want %s", k, o.res[k], v)
		}
	}
}

// NSM-010: payloads that are neither segments nor datagrams are dropped as foreign.
func TestForeignPayloadDropped(t *testing.T) {
	var rec records
	world(t, netsim.Config{}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		w.AddServer("e", func(n *kernel.Node) {
			n.After(10*time.Millisecond, "send", func() { w.Net.Send(n, 2, []byte("x")) })
		})
		w.AddProcess("a", wait)
		rec.capture(w)
	})
	drops := rec.kind("net.udp.drop")
	if len(drops) != 1 || attr(drops[0], "reason") != "foreign" || drops[0].Node != 2 ||
		drops[0].Text != "udp drop e -> a: foreign" || attr(drops[0], "len") != "0" {
		t.Fatalf("net.udp.drop %+v", drops)
	}
}
