// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package etcdraft

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
)

// AT-ETC-16 (ETC-195): DefaultConfig with FaultsDefault passes 200 seeds for 3 and 5 nodes.
func TestDefaultFaults(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("200 seeds per cluster size; skipped under -short and -race")
	}
	for _, nodes := range []int{3, 5} {
		t.Run(strconv.Itoa(nodes)+"nodes", func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Nodes = nodes
			Test(t, cfg, faultline.Options{Seeds: 200, Duration: 60 * time.Second})
		})
	}
}

// TestHunt is the Phase 1 bug hunt (ETC-195): 10 000 seeds of stage 1a and of stage 1b,
// for 3 and 5 nodes, every seed run (KeepGoing). It runs only with ETCDRAFT_HUNT=1;
// FAULTLINE_SEEDS overrides the seed count and FAULTLINE_RESULTS collects the results.
func TestHunt(t *testing.T) {
	if os.Getenv("ETCDRAFT_HUNT") != "1" {
		t.Skip("set ETCDRAFT_HUNT=1 to run the Phase 1 bug hunt")
	}
	stages := []struct {
		name          string
		snapshotEvery uint64
	}{{"1a", 0}, {"1b", 50}}
	for _, st := range stages {
		for _, nodes := range []int{3, 5} {
			t.Run("stage"+st.name+"/"+strconv.Itoa(nodes)+"nodes", func(t *testing.T) {
				cfg := DefaultConfig()
				cfg.Nodes = nodes
				cfg.SnapshotEvery = st.snapshotEvery
				Test(t, cfg, faultline.Options{Seeds: 10_000, Duration: 60 * time.Second, KeepGoing: true})
			})
		}
	}
}
