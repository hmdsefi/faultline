// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package golden

import (
	"bytes"
	"errors"
	"time"

	"github.com/hmdsefi/faultline/internal/toys"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
	"github.com/hmdsefi/faultline/kernel/simdisk"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// netTransport adapts *simnet.Network to toys.Transport (DET-053).
type netTransport struct{ nw *simnet.Network }

func (t netTransport) Handle(n *kernel.Node, h func(kernel.NodeID, any)) { t.nw.Handle(n, h) }
func (t netTransport) Send(from *kernel.Node, to kernel.NodeID, msg any) { t.nw.Send(from, to, msg) }

// stackScenarios are the DET-053 scenarios, appended after the kernel-only list.
var stackScenarios = []scenario{
	{name: "stack/register-simnet", seeds: []uint64{1, 2, 3}, run: func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
		nw := simnet.New(s, simnet.DefaultConfig())
		toys.Register(s, netTransport{nw}, toys.RegisterConfig{Ops: 20})
		return s, s.Run()
	}},
	{name: "stack/register-partition", seeds: []uint64{1, 2, 3}, run: func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
		nw := simnet.New(s, simnet.DefaultConfig())
		d := simdisk.New(s, simdisk.Config{})
		in := fault.NewInjector(s, nw, d)
		toys.Register(s, netTransport{nw}, toys.RegisterConfig{Ops: 20})
		in.Load(fault.Schedule{Version: 1, Events: []fault.Event{
			{At: ms(50), Kind: fault.KindPartition, Groups: [][]string{{"p"}, {"r1", "r2", "c"}}},
			{At: ms(150), Kind: fault.KindHeal},
		}})
		return s, s.RunUntil(kernel.Time(time.Second))
	}},
	{name: "stack/journal", seeds: []uint64{1, 2, 3}, run: func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
		nw := simnet.New(s, simnet.DefaultConfig())
		d := simdisk.New(s, simdisk.Config{})
		in := fault.NewInjector(s, nw, d)
		s.AddNode("j", func(n *kernel.Node) {
			v := d.Volume(n)
			f, err := v.Open("/log")
			if errors.Is(err, simdisk.ErrNotExist) {
				f, err = v.Create("/log")
			}
			if err != nil {
				s.Fail(err)
				return
			}
			n.Logf("recovered %d bytes", f.Size())
			count := 0
			var tick func()
			tick = func() {
				count++
				if _, err := f.Append(bytes.Repeat([]byte{byte(count)}, 16)); err != nil {
					n.Logf("append: %v", err)
				}
				if count%3 == 0 {
					if err := f.Sync(); err != nil {
						n.Logf("sync: %v", err)
					}
				}
				n.After(10*time.Millisecond, "append", tick)
			}
			n.After(10*time.Millisecond, "append", tick)
		}, kernel.WithTags("server"))
		in.Load(fault.Schedule{Version: 1, Events: []fault.Event{
			{At: ms(95), Kind: fault.KindCrash, Node: "j"},
			{At: ms(100), Kind: fault.KindRestart, Node: "j"},
			{At: ms(205), Kind: fault.KindCrash, Node: "j"},
			{At: ms(210), Kind: fault.KindRestart, Node: "j"},
		}})
		return s, s.RunUntil(ms(300))
	}},
}

func init() { scenarios = append(scenarios, stackScenarios...) } //nolint:gochecknoinits // DET-051: other test files add scenarios only in init
