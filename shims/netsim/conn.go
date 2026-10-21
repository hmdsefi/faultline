// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim

import "time"

// passed reports whether deadline is set and not in the future (bubble time).
func passed(deadline time.Time) bool { return !deadline.IsZero() && !time.Now().Before(deadline) }

// block waits until ch or dlCh is closed or deadline passes (NSM-003, NSM-043). Lock held on entry
// and on return; released while waiting. On the driver goroutine it panics with the lock still held:
// every blocking method defers the Unlock, which releases it while the panic unwinds.
func (st *Stack) block(op string, ch, dlCh <-chan struct{}, deadline time.Time) {
	if st.d.OnDriver() {
		panic("netsim: blocking call " + op + " on the driver goroutine (body, a check or a kernel callback)")
	}
	st.d.Unlock()
	var tc <-chan time.Time
	var t *time.Timer
	if !deadline.IsZero() {
		t = time.NewTimer(time.Until(deadline))
		tc = t.C
	}
	select {
	case <-ch:
	case <-dlCh:
	case <-tc:
	}
	if t != nil {
		t.Stop()
	}
	st.d.Lock()
}
