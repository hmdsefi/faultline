// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package kernel_test

import (
	"fmt"
	"slices"
	"time"

	"github.com/hmdsefi/faultline/kernel"
)

// Two nodes tick on their own timers until one second of virtual time. Run executes the events in
// time order and returns when none is left, without waiting.
func Example() {
	s := kernel.New(kernel.Config{Seed: 1})
	ticker := func(every time.Duration) kernel.BootFunc {
		return func(n *kernel.Node) {
			var tick func()
			tick = func() {
				fmt.Println(s.Now(), n.Name(), "tick")
				if s.Now() < kernel.Time(time.Second) {
					n.After(every, "tick", tick)
				}
			}
			n.After(every, "tick", tick)
		}
	}
	s.AddNode("n1", ticker(300*time.Millisecond))
	s.AddNode("n2", ticker(500*time.Millisecond))
	fmt.Println(s.Run())
	// Output:
	// 0.300000000s n1 tick
	// 0.500000000s n2 tick
	// 0.600000000s n1 tick
	// 0.900000000s n1 tick
	// 1.000000000s n2 tick
	// 1.200000000s n1 tick
	// idle
}

// A crash drops the events of the node's current incarnation. Restart boots the next incarnation,
// and the BootFunc runs again.
func ExampleNode_Crash() {
	s := kernel.New(kernel.Config{Seed: 1})
	n := s.AddNode("n1", func(n *kernel.Node) {
		fmt.Println(s.Now(), "boot, incarnation", n.Incarnation())
		n.After(time.Second, "work", func() { fmt.Println(s.Now(), "work of incarnation", n.Incarnation()) })
	})
	s.RunFor(1500 * time.Millisecond)
	n.Crash()
	s.RunFor(time.Second)
	n.Restart()
	s.RunFor(2 * time.Second)
	fmt.Println(n.State())
	// Output:
	// 0.000000000s boot, incarnation 1
	// 1.000000000s work of incarnation 1
	// 2.500000000s boot, incarnation 2
	// 3.500000000s work of incarnation 2
	// up
}

// Every random choice comes from a stream named by a label. Two Sims with the same seed return
// the same numbers for the same label, and another label gives an independent stream.
func ExampleSim_Rand() {
	draw := func(seed uint64, label string) []int {
		r := kernel.New(kernel.Config{Seed: seed}).Rand(label)
		return []int{r.IntN(1000), r.IntN(1000), r.IntN(1000)}
	}
	fmt.Println(slices.Equal(draw(7, "workload/keys"), draw(7, "workload/keys")))
	fmt.Println(slices.Equal(draw(7, "workload/keys"), draw(8, "workload/keys")))
	fmt.Println(slices.Equal(draw(7, "workload/keys"), draw(7, "workload/values")))
	// Output:
	// true
	// false
	// false
}

func ExampleTime_String() {
	fmt.Println(kernel.Time(41207 * time.Millisecond))
	fmt.Println(kernel.Time(0).Add(-time.Nanosecond))
	// Output:
	// 41.207000000s
	// -0.000000001s
}
