package fault

import (
	"fmt"
	"slices"
	"strconv"
	"time"
)

// script is the planner returned by Script.
type script struct{ events []Event }

// Script returns a planner named "script" that applies a copy of events at their At
// (FLT-090).
func Script(events ...Event) Planner { return &script{events: cloneEvents(events)} }

func (sc *script) Name() string { return "script" }

func (sc *script) Start(ctx *PlanContext) {
	now := ctx.Sim.Now()
	for i, e := range sc.events {
		who := "fault: Script: events[" + strconv.Itoa(i) + "]: "
		if p := e.problem(); p != "" {
			panic(who + p)
		}
		if e.Role != "" && ctx.Roles[e.Role] == nil {
			panic(who + "unknown role " + strconv.Quote(e.Role))
		}
		for _, name := range e.names() {
			if ctx.Sim.Lookup(name) == nil {
				panic(who + "unknown node " + strconv.Quote(name))
			}
		}
		if e.At < now {
			panic(fmt.Sprintf("%sat %s is before now (%s)", who, time.Duration(e.At), time.Duration(now)))
		}
	}
	idx := make([]int, len(sc.events)) // indices into sc.events, stably sorted by At
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(a, b int) int {
		switch x, y := sc.events[a].At, sc.events[b].At; {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	})
	for start := 0; start < len(idx); {
		end := start + 1
		for end < len(idx) && sc.events[idx[end]].At == sc.events[idx[start]].At {
			end++
		}
		group := idx[start:end]
		ctx.Sim.AtFront(sc.events[group[0]].At, "fault/script", func() {
			for _, i := range group {
				// ctx.Inject may be any func; it must not alias sc.events
				e := sc.events[i].clone()
				if e.Role != "" {
					cands := resolveRole(ctx, e.Role, "Script: events["+strconv.Itoa(i)+"]")
					if len(cands) == 0 {
						emitSkip(ctx, "script", "event", i, e.Kind, "no-target")
						continue
					}
					n := pick(ctx.Rand, cands)
					emitTarget(ctx, "script", "event", i, e.Kind, e.Role, cands, n)
					e.Node, e.Role = n.Name(), ""
				}
				ctx.Inject(e)
			}
		})
		start = end
	}
}

// none is the planner returned by None.
type none struct{}

// None returns a planner named "none" that does nothing.
func None() Planner { return none{} }

func (none) Name() string         { return "none" }
func (none) Start(_ *PlanContext) {}
