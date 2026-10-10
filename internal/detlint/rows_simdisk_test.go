// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package detlint_test

import (
	"slices"
	"testing"

	"github.com/hmdsefi/faultline/internal/detlint"
)

// TestRowsSimdisk pins the DET-011 class, the DET-020 import row and the DET §4.1 APIPackages
// entry of kernel/simdisk.
func TestRowsSimdisk(t *testing.T) {
	root, err := detlint.FindRoot(".", "github.com/hmdsefi/faultline")
	if err != nil {
		t.Fatal(err)
	}
	m := detlint.Faultline(root)
	const pkg = "kernel/simdisk"
	if got := m.Classes[pkg]; got != detlint.ClassCore {
		t.Errorf("Classes[%q] = %v, want core (DET-011)", pkg, got)
	}
	a, ok := m.Imports[pkg]
	if !ok || !a.Stdlib || !a.Gograph || len(a.Testing) != 0 ||
		!slices.Equal(slices.Sorted(slices.Values(a.Packages)), []string{"kernel"}) {
		t.Errorf("Imports[%q] = %+v (present %v), want {Stdlib: true, Gograph: true, Packages: [kernel]} (DET-020)", pkg, a, ok)
	}
	if !slices.Contains(detlint.APIPackages, pkg) {
		t.Errorf("APIPackages = %v, want it to contain %q (DET §4.1)", detlint.APIPackages, pkg)
	}
}
