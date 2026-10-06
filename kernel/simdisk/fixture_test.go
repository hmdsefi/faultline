package simdisk_test

import (
	"errors"
	"io/fs"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simdisk"
)

// imm is the AT shorthand "Imm" (DSK §10).
var imm = simdisk.Config{Metadata: simdisk.MetadataImmediate}

// disk is the DSK §10 fixture.
type disk struct {
	s *kernel.Sim
	d *simdisk.Disks
	a *kernel.Node
	v *simdisk.Volume
}

// newDisk creates a TraceFull Sim with seed, Disks with cfg, node a (ID 1) with an empty boot
// function, runs to t=0 so a is up, and returns a's volume.
func newDisk(seed uint64, cfg simdisk.Config) *disk {
	k := &disk{s: kernel.New(kernel.Config{Seed: seed, Trace: kernel.TraceConfig{Level: kernel.TraceFull}})}
	k.d = simdisk.New(k.s, cfg)
	k.a = k.s.AddNode("a", func(*kernel.Node) {})
	k.s.RunUntil(0)
	k.v = k.d.Volume(k.a)
	k.mustNotFail()
	return k
}

func (k *disk) crash() {
	k.a.Crash()
	k.mustNotFail()
}

func (k *disk) restart() {
	k.a.Restart()
	k.s.RunFor(0)
	k.mustNotFail()
}

// mustNotFail panics with s.Err(): the kernel guard turns a panicking crash hook into a Sim
// error, which no test would see otherwise.
func (k *disk) mustNotFail() {
	if err := k.s.Err(); err != nil {
		panic(err)
	}
}

// records returns the kept records whose kind starts with "disk.", in order.
func (k *disk) records() []kernel.Record {
	var out []kernel.Record
	for _, r := range k.s.Records() {
		if strings.HasPrefix(r.Kind, "disk.") {
			out = append(out, r)
		}
	}
	return out
}

// recordsOf returns the kept records of kind, in order.
func (k *disk) recordsOf(kind string) []kernel.Record {
	var out []kernel.Record
	for _, r := range k.s.Records() {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

// replay is kernel.New(kernel.Config{Seed: seed}).Rand(label) (DSK §10).
func replay(seed uint64, label string) *rand.Rand {
	return kernel.New(kernel.Config{Seed: seed}).Rand(label)
}

// summary renders a record as "kind{k=v, k=v}".
func summary(r kernel.Record) string {
	parts := make([]string, len(r.Attrs))
	for i, a := range r.Attrs {
		parts[i] = a.Key + "=" + a.Value
	}
	return r.Kind + "{" + strings.Join(parts, ", ") + "}"
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

// checkPathErr checks that err is a *fs.PathError with exactly op, path and target.
func checkPathErr(t *testing.T, err error, op, path string, target error) {
	t.Helper()
	var pe *fs.PathError
	if !errors.As(err, &pe) || pe.Op != op || pe.Path != path || pe.Err != target {
		t.Errorf("error %#v, want &fs.PathError{Op: %q, Path: %q, Err: %v}", err, op, path, target)
	}
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

// must fails the test if err is not nil.
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// names returns the names of infos.
func names(infos []simdisk.Info) []string {
	out := []string{}
	for _, i := range infos {
		out = append(out, i.Name)
	}
	return out
}
