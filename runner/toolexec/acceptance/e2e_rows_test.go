// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package acceptance

import "testing"

// AT-XTE-28
func TestATXTE28_HangsFixed(t *testing.T) {
	dir := fixture(t, "hangs")
	bin := build(t, dir, ".", true, nil)
	for _, test := range []string{"TestCond", "TestLocker", "TestMethodValue", "TestOnceFunc", "TestWaitGroupGo", "TestAfterFunc", "TestGoschedSpin", "TestGenericGo"} {
		a, oa := runSeeds(t, bin, dir, test, 10)
		b, ob := runSeeds(t, bin, dir, test, 10)
		for s, r := range a {
			if r.Status != "pass" || b[s].Status != "pass" {
				t.Errorf("%s seed %s: statuses %s and %s, want pass", test, s, r.Status, b[s].Status)
			}
			if oa[s] == "" || oa[s] != ob[s] {
				t.Errorf("%s seed %s: outcomes %q and %q", test, s, oa[s], ob[s])
			}
		}
	}
}

// AT-XTE-29
func TestATXTE29_OrdersFollowTheSeed(t *testing.T) {
	dir := fixture(t, "orders")
	bin := build(t, dir, ".", true, nil)
	for _, test := range []string{"TestStructKeys", "TestBoolKeys", "TestMapsAll", "TestRandValue", "TestRandN", "TestReflectSelect"} {
		_, oa := runSeeds(t, bin, dir, test, 20)
		_, ob := runSeeds(t, bin, dir, test, 20)
		distinct := map[string]bool{}
		for _, s := range seeds(20) {
			if oa[s] == "" || oa[s] != ob[s] {
				t.Errorf("%s seed %s: outcomes %q and %q", test, s, oa[s], ob[s])
			}
			distinct[oa[s]] = true
		}
		if len(distinct) < 2 {
			t.Errorf("%s: one outcome for 20 seeds", test)
		}
	}
}
