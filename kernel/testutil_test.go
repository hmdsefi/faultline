package kernel

import (
	"fmt"
	"testing"
)

// mustPanic calls fn and fails the test unless it panics with exactly the value want.
func mustPanic(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		t.Helper()
		v := recover()
		if v == nil {
			t.Fatalf("no panic; want %q", want)
		}
		if got := fmt.Sprint(v); got != want {
			t.Fatalf("panic %q; want %q", got, want)
		}
	}()
	fn()
}
