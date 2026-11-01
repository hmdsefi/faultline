// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package sched

import (
	"runtime"
	"testing"
)

// TestMain runs the tests on one P, as an exact seed runs (XTE-090). Release breaks ties by
// goroutine ID, and IDs follow start order only on one P: each P hands them out from its own cache.
func TestMain(m *testing.M) {
	runtime.GOMAXPROCS(1)
	m.Run()
}
