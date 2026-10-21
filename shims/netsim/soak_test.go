// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim_test

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
	"github.com/hmdsefi/faultline/kernel/simnet"
	"github.com/hmdsefi/faultline/shims/netsim"
)

// echoPeer is AT-NSM-31's process: an echo server (one goroutine per connection) plus a client
// that every 100 ms picks a peer and a size in [1, 4096] with p.Rand(), reuses or dials a
// connection, writes and reads the echo. echoes counts the completed echoes.
func echoPeer(peers []string, echoes *atomic.Int64) faultline.ProcessFunc {
	return func(ctx context.Context, p *faultline.Process) error {
		ln, err := p.Net.Listen("tcp", ":7")
		if err != nil {
			return err
		}
		p.Go(func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				p.Go(func() { _, _ = io.Copy(c, c); c.Close() })
			}
		})
		conns := map[string]net.Conn{}
		r := p.Rand()
		buf := make([]byte, 4096)
		tk := time.NewTicker(100 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-tk.C:
			}
			peer, size := peers[r.IntN(len(peers))], 1+r.IntN(4096)
			if peer == p.Node.Name() {
				continue
			}
			c := conns[peer]
			if c == nil {
				if c, err = (&netsim.Dialer{Host: p.Net, Timeout: time.Second}).Dial("tcp", peer+":7"); err != nil {
					continue
				}
				conns[peer] = c
			}
			_ = c.SetDeadline(time.Now().Add(time.Second))
			if _, err := c.Write(buf[:size]); err != nil {
				c.Close()
				delete(conns, peer)
				continue
			}
			if _, err := io.ReadFull(c, buf[:size]); err != nil {
				c.Close()
				delete(conns, peer)
				continue
			}
			echoes.Add(1)
		}
	}
}

func TestATNSM31_SoakIsDeterministic(t *testing.T) {
	if testing.Short() {
		t.Skip("soak")
	}
	peers := []string{"a", "b", "c"}
	faultline.Run(t, faultline.Options{
		Mode: faultline.ModeGoroutine, Seeds: 20, CheckDeterminism: true, Duration: 30 * time.Second,
		Net: simnet.Config{Default: simnet.Link{Latency: time.Millisecond, Jitter: 4 * time.Millisecond, DropPPM: 50_000}},
	}, func(w *faultline.World) {
		var echoes atomic.Int64
		for _, name := range peers {
			w.AddProcess(name, echoPeer(peers, &echoes))
		}
		w.Final("echoes completed", func() error {
			if echoes.Load() == 0 {
				return errors.New("no echo completed")
			}
			return nil
		})
		w.Plan(&fault.Random{Rules: []fault.Rule{{Kind: fault.KindPartition, Every: 5 * time.Second}}})
		w.Plan(fault.Script(
			fault.Event{At: kernel.Time(20 * time.Second), Kind: fault.KindCrash, Node: "b"},
			fault.Event{At: kernel.Time(21 * time.Second), Kind: fault.KindRestart, Node: "b"},
		))
	})
}

func init() { //nolint:gochecknoinits // registers the child-process scenarios
	childScenarios["nsm-35-final"] = func(t *testing.T) {
		var ln net.Listener
		world(t, netsim.Config{}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
			w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
				l, err := p.Net.Listen("tcp", ":80")
				if err != nil {
					return err
				}
				ln = l
				<-ctx.Done()
				return nil
			})
			w.Final("accept from a check", func() error {
				_, err := ln.Accept()
				return err
			})
		})
	}
	childScenarios["nsm-35-body"] = func(t *testing.T) {
		world(t, netsim.Config{}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
			n := w.AddProcess("a", wait)
			w.RunFor(time.Millisecond)
			_, _ = w.Process(n.ID()).Net.Dial("tcp", "a:80")
		})
	}
}

func TestATNSM35_BlockingCallsOnTheDriver(t *testing.T) {
	out, code := runChild(t, "nsm-35-final", "FAULTLINE_SEED=0x1")
	if code != 1 || !strings.Contains(out, `panic in final check "accept from a check"`) ||
		!strings.Contains(out, "netsim: blocking call Listener.Accept on the driver goroutine (body, a check or a kernel callback)") {
		t.Fatalf("final check: exit %d\n%s", code, out)
	}
	out, code = runChild(t, "nsm-35-body", "FAULTLINE_SEED=0x1")
	if code != 1 || strings.Contains(out, "panic in body") ||
		!strings.Contains(out, "faultline: netsim/fsx call on the driver goroutine while it holds the world lock") {
		t.Fatalf("body: exit %d\n%s", code, out)
	}
}
