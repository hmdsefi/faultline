package lintok

import (
	"slices"

	. "maps"
)

// A dot-imported maps.Keys inside slices.Sorted is as fine as a qualified one.
func dotSorted(m map[string]int) []string {
	return slices.Sorted(Keys(m))
}
