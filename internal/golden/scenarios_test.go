package golden

import (
	"time"

	"github.com/hmdsefi/faultline/internal/toys"
	"github.com/hmdsefi/faultline/kernel"
)

// ms returns x milliseconds as a kernel.Time.
func ms(x int64) kernel.Time { return kernel.Time(time.Duration(x) * time.Millisecond) }

// scenario is one golden scenario: a RunFunc and the seeds it runs with.
type scenario struct {
	name  string
	seeds []uint64
	run   RunFunc
}

// scenarios is the fixed, ordered list of DET-051.
var scenarios = []scenario{
	{"pingpong/fifo", []uint64{1}, func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		s := kernel.New(kernel.Config{Seed: seed, Trace: trace, TieBreak: kernel.TieBreakFIFO})
		toys.PingPong(s, toys.NewWire(s, time.Millisecond, time.Millisecond), 50)
		return s, s.Run()
	}},
	{"pingpong/seeded", []uint64{1, 2, 0x5e1f9a2c4b7d3e80}, func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
		toys.PingPong(s, toys.NewWire(s, time.Millisecond, 5*time.Millisecond), 50)
		return s, s.Run()
	}},
	{"gossip/5", []uint64{1, 2, 3}, func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
		toys.Gossip(s, toys.NewWire(s, time.Millisecond, 20*time.Millisecond),
			toys.GossipConfig{Nodes: 5, Ticks: 20, Interval: 100 * time.Millisecond})
		return s, s.Run()
	}},
	{"gossip/5-faults", []uint64{1, 2, 3}, func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
		ns := toys.Gossip(s, toys.NewWire(s, time.Millisecond, 20*time.Millisecond),
			toys.GossipConfig{Nodes: 5, Ticks: 20, Interval: 100 * time.Millisecond})
		s.At(0, "fault", func() { ns[4].SetDrift(2000) })
		s.At(ms(300), "fault", func() { ns[1].Crash() })
		s.At(ms(400), "fault", func() { ns[3].JumpClock(250 * time.Millisecond) })
		s.At(ms(500), "fault", func() { ns[2].Pause() })
		s.At(ms(800), "fault", func() { ns[1].Restart() })
		s.At(ms(1200), "fault", func() { ns[2].Resume() })
		return s, s.Run()
	}},
	{"register/correct", []uint64{1, 2, 3, 4, 5}, func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
		toys.Register(s, toys.NewWire(s, time.Millisecond, 10*time.Millisecond), toys.RegisterConfig{Ops: 20})
		return s, s.Run()
	}},
	{"register/early-ack", []uint64{1, 2, 3, 4, 5}, func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
		toys.Register(s, toys.NewWire(s, time.Millisecond, 10*time.Millisecond), toys.RegisterConfig{Ops: 20, EarlyAck: true})
		return s, s.Run()
	}},
	{"kernel/panic", []uint64{1}, func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
		s.After(time.Second, "boom", func() {
			var m map[string]int
			m["x"] = 1
		})
		return s, s.Run()
	}},
	{"kernel/limits", []uint64{1}, func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		s := kernel.New(kernel.Config{Seed: seed, Trace: trace, MaxEvents: 1000})
		var f func()
		f = func() { s.After(time.Millisecond, "spin", f) }
		s.After(0, "spin", f)
		return s, s.Run()
	}},
}
