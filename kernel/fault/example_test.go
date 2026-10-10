// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package fault_test

import (
	"fmt"
	"os"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// A Random planner crashes one of three servers about every two seconds and restarts it up to a
// second later. At 8 s the recovery window starts and the planner stops. faultline.Run sets this
// up for each run; here it is done by hand to print the concrete schedule.
func ExampleRandom() {
	s := kernel.New(kernel.Config{Seed: 3})
	in := fault.NewInjector(s, simnet.New(s, simnet.DefaultConfig()), nil)
	var servers []*kernel.Node
	for _, name := range []string{"n1", "n2", "n3"} {
		servers = append(servers, s.AddNode(name, func(*kernel.Node) {}))
	}

	planner := &fault.Random{MaxDown: 1, Rules: []fault.Rule{
		{Kind: fault.KindCrash, Every: 2 * time.Second, MaxFor: time.Second},
	}}
	planner.Start(in.NewPlanContext(planner, servers, nil, kernel.Time(8*time.Second)))
	s.RunFor(10 * time.Second)

	for _, e := range in.Applied().Events {
		fmt.Println(e.At, e)
	}
	// Output:
	// 1.488917591s crash n1
	// 1.713266251s restart n1
	// 3.914547483s crash n1
	// 4.892182607s restart n1
	// 5.771040385s crash n2
	// 5.968659564s restart n2
	// 7.699454103s crash n3
	// 8.000000000s restart n3
}

// Write prints a schedule in the canonical form of schedule.json. Normalize numbers the events
// and pairs each one with the fault it ends.
func ExampleSchedule_Write() {
	s := fault.Schedule{Events: []fault.Event{
		{At: kernel.Time(1500 * time.Millisecond), Kind: fault.KindPartition, Groups: [][]string{{"n1"}, {"n2", "n3"}}},
		{At: kernel.Time(2 * time.Second), Kind: fault.KindCrash, Node: "n2"},
		{At: kernel.Time(3 * time.Second), Kind: fault.KindHeal},
		{At: kernel.Time(4 * time.Second), Kind: fault.KindRestart, Node: "n2"},
	}}
	ns, err := s.Normalize()
	if err == nil {
		err = ns.Write(os.Stdout)
	}
	if err != nil {
		fmt.Println(err)
	}
	// Output:
	// {
	//   "faultline_schedule": 1,
	//   "events": [
	//     {"id": 1, "at": "1.5s", "kind": "partition", "groups": [["n1"], ["n2", "n3"]]},
	//     {"id": 2, "at": "2s", "kind": "crash", "node": "n2"},
	//     {"id": 3, "at": "3s", "kind": "heal", "undoes": [1]},
	//     {"id": 4, "at": "4s", "kind": "restart", "node": "n2", "undoes": [2]}
	//   ]
	// }
}

// Script applies fixed events at fixed times. A role names the node to act on and is resolved
// when the event fires; here "first" is always n1.
func ExampleScript() {
	s := kernel.New(kernel.Config{Seed: 1})
	in := fault.NewInjector(s, nil, nil)
	s.OnCrash(func(n *kernel.Node) { fmt.Println(s.Now(), "crash", n.Name()) })
	n1 := s.AddNode("n1", func(n *kernel.Node) { fmt.Println(s.Now(), "boot", n.Name()) })
	roles := fault.Roles{"first": func() []kernel.NodeID { return []kernel.NodeID{n1.ID()} }}

	planner := fault.Script(
		fault.Event{At: kernel.Time(time.Second), Kind: fault.KindCrash, Role: "first"},
		fault.Event{At: kernel.Time(3 * time.Second), Kind: fault.KindRestart, Node: "n1"},
	)
	planner.Start(in.NewPlanContext(planner, []*kernel.Node{n1}, roles, kernel.Time(5*time.Second)))
	s.RunFor(5 * time.Second)
	fmt.Println(n1.State(), "incarnation", n1.Incarnation())
	// Output:
	// 0.000000000s boot n1
	// 1.000000000s crash n1
	// 3.000000000s boot n1
	// up incarnation 2
}
