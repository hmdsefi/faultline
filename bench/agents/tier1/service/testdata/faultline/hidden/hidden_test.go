package leasekv_test

import (
	"testing"

	"github.com/hmdsefi/faultline"
)

// TestLeaseKVHidden runs the world of TestLeaseKV, with its four checks, for 2,000 seeds. Its
// name differs from TestLeaseKV, so its seeds differ too. hidden.sh builds it with the original
// test files of the version, never with the files of the module it checks.
func TestLeaseKVHidden(t *testing.T) {
	opts := leaseOptions
	opts.Seeds = 2000
	faultline.Run(t, opts, leaseWorld)
}
