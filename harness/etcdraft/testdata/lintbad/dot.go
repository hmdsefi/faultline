package lintbad

import (
	. "fmt"
	. "maps"
	. "math/rand/v2"
	. "os"
	"slices"
	. "time"

	"go.etcd.io/raft/v3/raftpb"
)

// dot reaches the forbidden identifiers through dot imports.
func dot(hs *raftpb.HardState, m map[string]int) {
	_ = Now()                    // want `use of time.Now`
	Sleep(1)                     // want `use of time.Sleep`
	_ = Getenv("X")              // want `use of os.Getenv`
	_ = Stdout                   // want `use of os.Stdout`
	_, _ = Fprintln(Stderr, "x") // want `use of os.Stderr`
	Println("x")                 // want `use of fmt.Println`
	_ = IntN(3)                  // want `use of math/rand/v2.IntN`
	_ = N[int64](5)              // want `use of math/rand/v2.N`
	_ = slices.Collect(Keys(m))  // want `unordered iteration: maps.Keys`
	_ = Sprintf("%v", hs)        // want `proto message formatted with %v`
}
