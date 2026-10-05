package detlint

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

// inventory lists the package directories of the root module (the DET-015 walk), grouped by the
// plan that adds them. A plan that adds or removes a package directory updates this list in the
// same commit; TestPackageInventory then checks that the directory has a class (DET-011) and an
// import rule (DET-020).
//
// Later plans extend it: 1.2 adds kernel/simnet and kernel/simdisk; 1.3 adds kernel/fault and
// check/history; 1.4 adds "" (package faultline), artifact, ui and cmd/faultline; 2.2 adds
// shims/netsim; 2.3 adds shims/fsx; 2.4 adds internal/gosched (spike/exb branch only); 3.3 adds
// minimize; 3.4 adds check/linearizability; 3.5 adds check/isolation; 3.6 adds check/liveness; 3.8
// adds explore and assert. Nested modules are never listed. internal/deps (plan 1.1 until plan
// 1.2) is not a package directory (DET-011) and never appears here.
var inventory = []struct {
	plan string
	pkgs []string
}{
	{"1.1", []string{
		"internal/detlint",
		"internal/golden",
		"internal/toys",
		"kernel",
	}},
}

// TestPackageInventory pins the package directories that exist in this phase.
func TestPackageInventory(t *testing.T) {
	m := Faultline(moduleRoot(t))
	got, err := packageDirs(m.Root)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	var want []string
	for _, row := range inventory {
		want = append(want, row.pkgs...)
	}
	slices.Sort(want)
	if len(slices.Compact(slices.Clone(want))) != len(want) {
		t.Errorf("inventory lists a package twice: %v", want)
	}
	var diff []string
	for _, p := range got {
		if !slices.Contains(want, p) {
			diff = append(diff, "+"+p+" (exists but is not in inventory)")
		}
	}
	for _, p := range want {
		if !slices.Contains(got, p) {
			diff = append(diff, "-"+p+" (in inventory but does not exist)")
		}
	}
	if len(diff) > 0 {
		t.Errorf("package inventory changed; update inventory in internal/detlint/inventory_test.go:\n%s",
			strings.Join(diff, "\n"))
	}
	for _, p := range want {
		if _, ok := m.Classes[p]; !ok {
			t.Errorf("package %q has no class in Faultline (DET-011)", p)
		}
		if _, ok := m.Imports[p]; !ok {
			t.Errorf("package %q has no import rule in Faultline (DET-020)", p)
		}
	}
}

// DET-011, DET-020: the class and import tables, one row per package: path, class, then the
// allowances (stdlib, the testing tree, gograph, module packages), lists sorted.
func TestFaultlineTables(t *testing.T) {
	m := Faultline("/x")
	if m.Root != "/x" || m.Path != "github.com/hmdsefi/faultline" {
		t.Fatalf("Faultline = %+v", m)
	}
	want := []string{
		`"" entry stdlib testing=testing,testing/cryptotest,testing/synctest gograph ` +
			`pkgs=artifact,assert,check/history,check/isolation,check/linearizability,check/liveness,` +
			`explore,internal/gosched,kernel,kernel/fault,kernel/simdisk,kernel/simnet,minimize,shims/fsx,shims/netsim,ui`,
		`"artifact" output stdlib gograph pkgs=check/history,kernel,kernel/fault,ui`,
		`"assert" core stdlib pkgs=kernel`,
		`"check/history" core stdlib pkgs=kernel`,
		`"check/isolation" tool stdlib gograph pkgs=check/history,kernel`,
		`"check/linearizability" tool stdlib gograph pkgs=check/history,kernel`,
		`"check/liveness" tool stdlib gograph pkgs=check/history,kernel`,
		`"cmd/faultline" exempt stdlib pkgs=artifact,explore,minimize,ui`,
		`"explore" tool stdlib pkgs=kernel,kernel/fault,kernel/simnet`,
		`"internal/detlint" exempt stdlib`,
		`"internal/golden" exempt stdlib testing=testing pkgs=internal/toys,kernel`,
		`"internal/gosched" core stdlib`,
		`"internal/toys" core stdlib pkgs=kernel`,
		`"kernel" core stdlib gograph`,
		`"kernel/fault" core stdlib gograph pkgs=kernel,kernel/simdisk,kernel/simnet`,
		`"kernel/simdisk" core stdlib gograph pkgs=kernel`,
		`"kernel/simnet" core stdlib gograph pkgs=kernel`,
		`"minimize" tool stdlib pkgs=kernel,kernel/fault`,
		`"shims/fsx" shim stdlib pkgs=kernel,kernel/simdisk,kernel/simnet`,
		`"shims/netsim" shim stdlib pkgs=kernel,kernel/simdisk,kernel/simnet`,
		`"ui" output stdlib`,
	}
	if got := tableRows(m); !slices.Equal(got, want) {
		t.Errorf("tables:\n  %s\nwant (DET-011, DET-020):\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// tableRows renders every package of m.Classes and m.Imports as one row, sorted by path.
func tableRows(m Module) []string {
	var paths []string
	for p := range m.Classes {
		paths = append(paths, p)
	}
	for p := range m.Imports {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	var rows []string
	for _, p := range slices.Compact(paths) {
		row := strconv.Quote(p) + " "
		if c, ok := m.Classes[p]; ok {
			row += c.String()
		} else {
			row += "-"
		}
		a, ok := m.Imports[p]
		if !ok {
			rows = append(rows, row+" no-import-rule")
			continue
		}
		if a.Stdlib {
			row += " stdlib"
		}
		if len(a.Testing) > 0 {
			row += " testing=" + strings.Join(slices.Sorted(slices.Values(a.Testing)), ",")
		}
		if a.Gograph {
			row += " gograph"
		}
		if len(a.Packages) > 0 {
			row += " pkgs=" + strings.Join(slices.Sorted(slices.Values(a.Packages)), ",")
		}
		rows = append(rows, row)
	}
	return rows
}
