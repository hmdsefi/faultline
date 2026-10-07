package fault_test

import (
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
	"github.com/hmdsefi/faultline/kernel/simdisk"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// world is the FLT §10 fixture W5 (and W5+c).
type world struct {
	s       *kernel.Sim
	nw      *simnet.Network
	d       *simdisk.Disks
	in      *fault.Injector
	servers []*kernel.Node // n1..n5, IDs 1..5
	c1      *kernel.Node   // W5+c only, ID 6
}

// newW5 builds W5 with cfg (FLT §10 uses kernel.Config{Seed: 1}); client adds c1 (W5+c).
func newW5(cfg kernel.Config, client bool) *world {
	w := &world{s: kernel.New(cfg)}
	w.nw = simnet.New(w.s, simnet.DefaultConfig())
	w.d = simdisk.New(w.s, simdisk.Config{})
	for _, name := range []string{"n1", "n2", "n3", "n4", "n5"} {
		w.servers = append(w.servers, w.s.AddNode(name, func(*kernel.Node) {}, kernel.WithTags("server")))
	}
	if client {
		w.c1 = w.s.AddNode("c1", func(*kernel.Node) {}, kernel.WithTags("client"))
	}
	w.s.RunUntil(0)
	w.in = fault.NewInjector(w.s, w.nw, w.d)
	return w
}

// full is kernel.Config{Seed: seed} with TraceFull.
func full(seed uint64) kernel.Config { //nolint:unparam // the Random planner tests that come next pass other seeds
	return kernel.Config{Seed: seed, Trace: kernel.TraceConfig{Level: kernel.TraceFull}}
}

// node returns server n<i>.
func (w *world) node(i int) *kernel.Node { return w.servers[i-1] }

// records returns the kept records of kind, in order.
func (w *world) records(kind string) []kernel.Record {
	var out []kernel.Record
	for _, r := range w.s.Records() {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

// last returns the last kept record of kind.
func (w *world) last(t *testing.T, kind string) kernel.Record {
	t.Helper()
	rs := w.records(kind)
	if len(rs) == 0 {
		t.Fatalf("no %s record", kind)
	}
	return rs[len(rs)-1]
}

// attrString renders attributes as "k=v, k=v".
func attrString(r kernel.Record) string {
	parts := make([]string, len(r.Attrs))
	for i, a := range r.Attrs {
		parts[i] = a.Key + "=" + a.Value
	}
	return strings.Join(parts, ", ")
}

// attr returns the value of key in r, or "" if absent.
func attr(r kernel.Record, key string) string {
	for _, a := range r.Attrs {
		if a.Key == key {
			return a.Value
		}
	}
	return ""
}

// mustPanic runs f and checks that it panics with the string want.
func mustPanic(t *testing.T, want string, f func()) {
	t.Helper()
	defer func() {
		t.Helper()
		got := recover()
		if s, ok := got.(string); !ok || s != want {
			t.Errorf("panic %v, want %q", got, want)
		}
	}()
	f()
}

// at converts a duration to a kernel.Time.
func at(d time.Duration) kernel.Time { return kernel.Time(d) }
