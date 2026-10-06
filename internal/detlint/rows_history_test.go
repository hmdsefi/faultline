package detlint_test

import (
	"slices"
	"testing"

	"github.com/hmdsefi/faultline/internal/detlint"
)

// TestRowsHistory pins the DET-011 class and the DET-020 import row of check/history.
func TestRowsHistory(t *testing.T) {
	root, err := detlint.FindRoot(".", "github.com/hmdsefi/faultline")
	if err != nil {
		t.Fatal(err)
	}
	m := detlint.Faultline(root)
	const pkg = "check/history"
	if got := m.Classes[pkg]; got != detlint.ClassCore {
		t.Errorf("Classes[%q] = %v, want core (DET-011)", pkg, got)
	}
	a, ok := m.Imports[pkg]
	if !ok || !a.Stdlib || a.Gograph || len(a.Testing) != 0 ||
		!slices.Equal(slices.Sorted(slices.Values(a.Packages)), []string{"kernel"}) {
		t.Errorf("Imports[%q] = %+v (present %v), want {Stdlib: true, Packages: [kernel]} (DET-020)", pkg, a, ok)
	}
}
