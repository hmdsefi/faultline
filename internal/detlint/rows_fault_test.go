package detlint_test

import (
	"slices"
	"testing"

	"github.com/hmdsefi/faultline/internal/detlint"
)

// TestRowsFault pins the DET-011 class, the DET-020 import row and the DET §4.1 APIPackages
// entry of kernel/fault.
func TestRowsFault(t *testing.T) {
	root, err := detlint.FindRoot(".", "github.com/hmdsefi/faultline")
	if err != nil {
		t.Fatal(err)
	}
	m := detlint.Faultline(root)
	const pkg = "kernel/fault"
	if got := m.Classes[pkg]; got != detlint.ClassCore {
		t.Errorf("Classes[%q] = %v, want core (DET-011)", pkg, got)
	}
	want := []string{"kernel", "kernel/simdisk", "kernel/simnet"}
	a, ok := m.Imports[pkg]
	if !ok || !a.Stdlib || !a.Gograph || len(a.Testing) != 0 ||
		!slices.Equal(slices.Sorted(slices.Values(a.Packages)), want) {
		t.Errorf("Imports[%q] = %+v (present %v), want {Stdlib: true, Gograph: true, Packages: %v} (DET-020)", pkg, a, ok, want)
	}
	if !slices.Contains(detlint.APIPackages, pkg) {
		t.Errorf("APIPackages = %v, want it to contain %q (DET §4.1)", detlint.APIPackages, pkg)
	}
}
