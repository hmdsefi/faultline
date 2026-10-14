// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package etcdraft_test

import (
	"errors"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/harness/etcdraft"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
	"go.etcd.io/raft/v3"
)

// A test runs 100 seeds of a three-node cluster under the default fault preset, with every
// harness check on. This example compiles TestRaft but does not run it, since Test needs the
// *testing.T of a real test.
func ExampleTest() {
	TestRaft := func(t *testing.T) {
		etcdraft.Test(t, etcdraft.DefaultConfig(), faultline.Options{Seeds: 100})
	}
	_ = TestRaft
}

// Seed 0x5e1f9a2c4b7d3e80 of TestRaft failed, and the bug is fixed. TestRaftSeed5e1f9a2c keeps
// that seed as a regression test: it runs only that seed, with the Config and Options of
// TestRaft, and fails again if the bug comes back. Like ExampleTest, this example compiles the
// test but does not run it.
func ExampleTestSeeds() {
	cfg, opts := etcdraft.DefaultConfig(), faultline.Options{Seeds: 100}

	TestRaft := func(t *testing.T) {
		etcdraft.Test(t, cfg, opts)
	}
	TestRaftSeed5e1f9a2c := func(t *testing.T) {
		etcdraft.TestSeeds(t, cfg, opts, 0x5e1f9a2c4b7d3e80)
	}
	_, _ = TestRaft, TestRaftSeed5e1f9a2c
}

// A scenario with its own faults calls Setup inside faultline.Run, with the harness's network from
// NetConfig, and plans the faults itself. Here a script crashes whichever server leads at 5s, and
// an extra final check asks for a new leader after the crash. The harness's own checks run too.
// This example compiles TestLeaderCrash but does not run it, since Run needs the *testing.T of a
// real test.
func ExampleSetup() {
	TestLeaderCrash := func(t *testing.T) {
		cfg := etcdraft.DefaultConfig()
		cfg.Duration = 20 * time.Second
		opts := faultline.Options{Duration: cfg.Duration, Net: etcdraft.NetConfig()}
		faultline.Run(t, opts, func(w *faultline.World) {
			c := etcdraft.Setup(w, cfg)
			w.Plan(fault.Script(fault.Event{At: kernel.Time(5 * time.Second), Kind: fault.KindCrash, Role: "leader"}))
			w.Final("a new leader after the crash", func() error {
				for _, ch := range c.LeaderChanges() {
					if ch.At > kernel.Time(5*time.Second) && ch.State == raft.StateLeader {
						return nil
					}
				}
				return errors.New("no leader elected after the crash at 5s")
			})
		})
	}
	_ = TestLeaderCrash
}
