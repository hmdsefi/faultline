// Package lintok holds code the determinism rules must accept; TestDeterminismRulesAllow
// expects no findings. It is never built.
package lintok

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"
)

// A variable of this package named like os.Stdout is not os.Stdout.
var Stdout, Stderr = 1, 2

type logger struct{}

func (logger) Logf(format string, args ...any) {}

func sortedKeys[M ~map[string]int](m M) []string { return slices.Sorted(maps.Keys(m)) }

func sum[S ~[]int](s S) int {
	n := 0
	for _, v := range s {
		n += v
	}
	return n
}

// sliceLike is embedded like the map constraints of lintbad, but allows only slices.
type sliceLike interface{ ~[]int }

func sumEmbedded[S interface{ sliceLike }](s S) int {
	n := 0
	for _, v := range s {
		n += v
	}
	return n
}

func ok(r *rand.Rand, m map[string]int, hs *raftpb.HardState, ents []*raftpb.Entry, f string, err error, d time.Duration, lg logger, sb *strings.Builder, nw *simnet.Network, n *kernel.Node) string {
	// Methods on a stream from the kernel are allowed, and so are the types of math/rand/v2.
	var stream *rand.Rand = r
	_ = stream.IntN(3) + int(r.Uint64()%2)

	// Maps are read by key; listings are sorted; everything else iterates slices, arrays and integers.
	_ = m["a"]
	for _, k := range slices.Sorted(maps.Keys(m)) {
		_ = m[k]
	}
	for _, v := range slices.Sorted(maps.Values(m)) {
		_ = v
	}
	_ = slices.SortedFunc(maps.Keys(m), strings.Compare)
	_ = slices.SortedStableFunc(maps.Keys(m), strings.Compare)
	keys := sortedKeys(m)
	sort.Strings(keys)
	_ = maps.Clone(m)
	for i := range 3 {
		_ = i
	}
	for _, e := range ents {
		_ = e.GetIndex()
	}
	_ = sum([]int{1, 2})
	_ = sumEmbedded([]int{1, 2})
	_ = reflect.ValueOf(m).MapIndex(reflect.ValueOf("a"))

	_ = Stdout + Stderr

	// A WaitGroup without Go, and a context without a deadline or AfterFunc.
	var wg sync.WaitGroup
	wg.Add(1)
	wg.Done()
	wg.Wait()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = ctx.Err()

	// Durations are integers; no clock is read.
	_ = d + 2*time.Second + time.Duration(3)*time.Millisecond
	_ = d.Milliseconds()
	var t time.Time
	_ = t.Add(d).IsZero()

	// Probabilities are integers (parts per million).
	var ppm uint32 = 250_000
	_ = ppm * 4 / 1_000_000

	// %v of an error, %% before a verb, a format that is not constant, %T of a message,
	// explicit argument indexes and `*` widths that do not reach a message.
	s := fmt.Sprintf("%v %s %q", err, errors.New("x"), "y")
	s += fmt.Sprintf("100%% %d", hs.GetTerm())
	s += fmt.Sprintf(f, 1, "x")
	s += fmt.Sprintf("%T", hs)
	s += fmt.Sprintf("%[2]d %[1]v", err, 7)
	s += fmt.Sprintf("%*d", 5, 3)
	s += fmt.Sprintf("%d %v", hs.GetTerm(), hs.GetVote())
	s += fmt.Sprintf("%d", len(ents))
	s += fmt.Sprintf("%v %v", 1) // a missing argument is fmt's business
	s += fmt.Sprintf("%[0]v", 1) // so is a bad argument index
	s += fmt.Sprint(1, 2) + fmt.Sprintln("x")
	fmt.Fprintf(sb, "%v", err)
	_ = fmt.Errorf("wrap: %w", err)
	lg.Logf("%v %d", err, hs.GetCommit())

	// Enum names are deterministic (ETC-091), and messages are compared and copied, not printed.
	s += raftpb.MsgApp.String() + ents[0].GetType().String()
	_ = proto.Equal(hs, hs)
	_ = proto.Clone(hs)

	// kernel.Describe prints the type name of a value that only holds a message, and an enum's name.
	s += kernel.Describe(ents) + kernel.Describe(raftpb.MsgApp)
	nw.Send(n, 1, []byte("x"))
	_ = nw.SendRaw(n, 1, ents, simnet.RawOptions{})
	return s
}
