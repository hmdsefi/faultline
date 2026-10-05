package kernel

import (
	"errors"
	"strings"
	"testing"
	"time"
)

type describerStub struct{}

func (describerStub) Describe() string { return "d!" }

type Payload []byte

type Key string

type T struct{ X int }

// AT-KRN-39
func TestDescribe(t *testing.T) {
	long := strings.Repeat("a", 70)
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"nil", nil, "nil"},
		{"describer", describerStub{}, "d!"},
		{"stringer", time.Second, "1s"},
		{"error", errors.New("oops"), "oops"},
		{"bytes", []byte("hello"), "bytes len=5 fnv=a430d84680aabd0b"},
		{"named bytes", Payload("hello"), "bytes len=5 fnv=a430d84680aabd0b"},
		{"empty bytes", []byte{}, "bytes len=0 fnv=cbf29ce484222325"},
		{"string", "hi", `"hi"`},
		{"named string", Key("k1"), `"k1"`},
		{"long string", long, `"` + strings.Repeat("a", 64) + `"...(70 bytes)`},
		{"bool", true, "true"},
		{"int", -42, "-42"},
		{"uint16", uint16(7), "7"},
		{"uintptr", uintptr(5), "uintptr"},
		// the test type lives in package kernel (AT-KRN-39)
		{"struct", T{X: 1}, "kernel.T"},
		{"pointer", &T{}, "*kernel.T"},
		{"float", 1.5, "float64"},
		{"int slice", []int{1}, "[]int"},
		{"map", map[string]int{}, "map[string]int"},
		{"NodeID-like int32", int32(3), "3"},
	}
	for _, c := range cases {
		if got := Describe(c.in); got != c.want {
			t.Errorf("%s: Describe(%T) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

type nilDescriber struct{ s string }

func (d *nilDescriber) Describe() string { return d.s }

// KRN-100: a typed nil pointer whose type implements Describer has that method called.
func TestDescribeTypedNilCallsMethod(t *testing.T) {
	var d *nilDescriber
	defer func() {
		if recover() == nil {
			t.Fatal("Describe of a typed nil *nilDescriber did not call its method")
		}
	}()
	Describe(d)
}
