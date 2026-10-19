package leasekv_test

import (
	"strconv"
	"testing"
	"time"
)

// hiddenPlan crashes twice as often and partitions about 1.75 times as often as leasePlan.
var hiddenPlan = plan{
	crashEvery: 500 * time.Millisecond, crashMin: 10 * time.Millisecond, crashMax: 300 * time.Millisecond,
	partitionEvery: 400 * time.Millisecond, partitionMin: 250 * time.Millisecond, partitionMax: 450 * time.Millisecond,
}

// hiddenRuns is the most clusters TestLeaseKVHidden runs. A batch of hiddenBatch clusters runs in
// parallel (hidden.sh passes -test.parallel=60).
const (
	hiddenRuns  = 600
	hiddenBatch = 60
)

// TestLeaseKVHidden runs the scenario of TestLeaseKV with hiddenPlan and the same five checks,
// hiddenRuns times in batches of hiddenBatch. It stops after the first batch in which a cluster
// fails. hidden.sh builds it with the original test files of the version, never with the files
// of the module it checks.
func TestLeaseKVHidden(t *testing.T) {
	for b := range hiddenRuns / hiddenBatch {
		ok := t.Run("batch="+strconv.Itoa(b+1), func(t *testing.T) {
			for i := range hiddenBatch {
				t.Run("run="+strconv.Itoa(b*hiddenBatch+i+1), func(t *testing.T) {
					t.Parallel()
					c := newCluster()
					c.run(hiddenPlan)
					if err := c.check(); err != nil {
						t.Fatal(err)
					}
				})
			}
		})
		if !ok {
			return
		}
	}
}
