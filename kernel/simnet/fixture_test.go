package simnet_test

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// tmsg is a string-like payload whose kernel.Describe text is the string itself (NET §10).
type tmsg string

func (m tmsg) Describe() string { return string(m) }

// delivery is one handler call recorded by the fixture.
type delivery struct {
	at      kernel.Time
	to      kernel.NodeID
	from    kernel.NodeID
	payload any
}

// world is the NET §10 fixture.
type world struct {
	s       *kernel.Sim
	nw      *simnet.Network
	a, b, c *kernel.Node
	install map[string]bool
	got     []delivery
}

// newNet creates a TraceFull Sim with seed, a Network with cfg, and nodes a, b, c (IDs 1, 2, 3)
// whose boot installs a recording handler when install[name] is true, then runs to t=0.
func newNet(seed uint64, cfg simnet.Config) *world {
	w := &world{install: map[string]bool{"a": true, "b": true, "c": true}}
	w.s = kernel.New(kernel.Config{Seed: seed, Trace: kernel.TraceConfig{Level: kernel.TraceFull}})
	w.nw = simnet.New(w.s, cfg)
	w.a = w.s.AddNode("a", w.boot)
	w.b = w.s.AddNode("b", w.boot)
	w.c = w.s.AddNode("c", w.boot)
	w.s.RunUntil(0)
	if err := w.s.Err(); err != nil {
		panic(err)
	}
	return w
}

func (w *world) boot(n *kernel.Node) {
	if !w.install[n.Name()] {
		return
	}
	w.nw.Handle(n, func(from kernel.NodeID, payload any) {
		w.got = append(w.got, delivery{at: w.s.Now(), to: n.ID(), from: from, payload: payload})
	})
}

// addNode adds a node with the fixture's boot function; install defaults to true.
func (w *world) addNode(name string) *kernel.Node {
	if _, ok := w.install[name]; !ok {
		w.install[name] = true
	}
	return w.s.AddNode(name, w.boot)
}

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

// countPrefix counts the kept records whose kind starts with prefix.
func (w *world) countPrefix(prefix string) int {
	n := 0
	for _, r := range w.s.Records() {
		if strings.HasPrefix(r.Kind, prefix) {
			n++
		}
	}
	return n
}

// at schedules fn as a global event at virtual time d.
func (w *world) at(d time.Duration, fn func()) {
	w.s.At(kernel.Time(d), "test", fn)
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

// checkRecord compares a record's Node, Text and attributes ("k=v, k=v").
func checkRecord(t *testing.T, r kernel.Record, node kernel.NodeID, text, attrs string) {
	t.Helper()
	if r.Node != node || r.Text != text || attrString(r) != attrs {
		t.Errorf("record %s: got node %d text %q attrs %q, want node %d text %q attrs %q",
			r.Kind, r.Node, r.Text, attrString(r), node, text, attrs)
	}
}

// mustPanic runs f and checks that it panics with the string want.
func mustPanic(t *testing.T, want string, f func()) {
	t.Helper()
	defer func() {
		t.Helper()
		got := recover()
		if got == nil {
			t.Errorf("no panic, want %q", want)
			return
		}
		if s, ok := got.(string); !ok || s != want {
			t.Errorf("panic %v, want %q", got, want)
		}
	}()
	f()
}

// replayDecision replays NET-013 for one message on r, independently of the package.
func replayDecision(l simnet.Link, r *rand.Rand) simnet.Decision {
	chance := func(ppm uint32) bool {
		if ppm == 0 {
			return false
		}
		if ppm >= simnet.MaxPPM {
			return true
		}
		return kernel.Chance(r, ppm)
	}
	uniform := func(max time.Duration) time.Duration {
		if max == 0 {
			return 0
		}
		return kernel.Uniform(r, 0, max)
	}
	delay := func() time.Duration {
		x := l.Latency + uniform(l.Jitter)
		if chance(l.TailPPM) {
			x += uniform(l.Tail)
		}
		return x
	}
	if chance(l.DropPPM) {
		return simnet.Decision{Drop: true}
	}
	d1 := delay()
	if chance(l.DupPPM) {
		return simnet.Decision{Delay: d1, Dup: true, DupDelay: delay()}
	}
	return simnet.Decision{Delay: d1}
}

// freshStream returns stream label of a new Sim with seed (NET §10 replays).
func freshStream(seed uint64, label string) *rand.Rand {
	return kernel.New(kernel.Config{Seed: seed}).Rand(label)
}

// untouched reports whether stream label of s has not been drawn from: its next value equals the
// first value of the same stream in a fresh Sim with the same seed. It consumes one value from the
// stream, which the network shares, so call it last for that stream.
func untouched(s *kernel.Sim, label string) bool {
	return s.Rand(label).Uint64() == freshStream(s.Seed(), label).Uint64()
}

// payloadInt returns the int payload of d and fails the test if the payload has another type.
func payloadInt(t *testing.T, d delivery) int {
	t.Helper()
	v, ok := d.payload.(int)
	if !ok {
		t.Fatalf("payload %v has type %T, want int", d.payload, d.payload)
	}
	return v
}

func ns(d time.Duration) string { return fmt.Sprint(int64(d)) }
