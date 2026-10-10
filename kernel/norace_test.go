// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

//go:build !race

package kernel

// raceEnabled reports whether the race detector is on; allocation counts are not representative
// under it.
const raceEnabled = false
