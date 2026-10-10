// Package lintbad breaks every rule TestDeterminismRules enforces, in each form the rule
// must see: a call, a function or method value, a method expression, an explicit
// instantiation (dot.go and dotreflect.go add the dot imports). Each violating line ends in a
// `// want` comment that quotes its findings; TestDeterminismRulesCatch checks them. It is never
// built.
package lintbad

import (
	"context"
	crand "crypto/rand" // want `import "crypto/rand"`
	"fmt"
	"log"      // want `import "log"`
	"log/slog" // want `import "log/slog"`
	"maps"
	mrand "math/rand"
	"math/rand/v2"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/encoding/protojson" // want `import "google.golang.org/protobuf/encoding/protojson"`
	"google.golang.org/protobuf/encoding/prototext" // want `import "google.golang.org/protobuf/encoding/prototext"`
)

type wrapped struct{ hs *raftpb.HardState }

// cycle is a recursive type that holds a message.
type cycle struct {
	next *cycle
	hs   *raftpb.HardState
}

type logger struct{}

func (logger) Logf(format string, args ...any) {}

func logf(format string, args ...any) {}

func logt[T any](format string, args ...any) {}

func logt2[T, U any](format string, args ...any) {}

// Sorted is not slices.Sorted: wrapping maps.Keys in it does not sort anything.
func Sorted[T any](seq func(yield func(T) bool)) []T { return nil }

type stringMap map[string]int

func each[M ~map[string]int](m M) {
	for range m { // want `range over a map`
	}
}

// mapLike allows only maps; the constraints below reach it through embedding.
type mapLike interface{ ~map[string]int }

type nestedMap interface{ mapLike }

func eachEmbedded[M interface{ mapLike }](m M) {
	for range m { // want `range over a map`
	}
}

func eachNested[M nestedMap](m M) {
	for range m { // want `range over a map`
	}
}

func eachUnion[M interface{ mapLike | nestedMap }](m M) {
	for range m { // want `range over a map`
	}
}

func bad(m map[string]int, sm stringMap, hs *raftpb.HardState, ents []*raftpb.Entry, ch chan int, f string, sb *strings.Builder, nw *simnet.Network, n *kernel.Node) {
	// Statements.
	go func() {}() // want `go statement`
	select {       // want `select statement`
	case <-ch:
	default:
	}

	// Library calls that start a goroutine.
	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Go(func() {})                      // want `use of (*sync.WaitGroup).Go`
	spawn := wg.Go                        // want `use of (*sync.WaitGroup).Go`
	_ = (*sync.WaitGroup).Go              // want `use of (*sync.WaitGroup).Go`
	_ = context.AfterFunc(ctx, func() {}) // want `use of context.AfterFunc`
	_ = spawn

	// Maps are accessed by key only.
	for range m { // want `range over a map`
	}
	for range sm { // want `range over a map`
	}
	for k := range maps.Keys(m) { // want `unordered iteration: maps.Keys`
		_ = k
	}
	for k, v := range maps.All(m) { // want `unordered iteration: maps.All`
		_, _ = k, v
	}
	for v := range maps.Values(m) { // want `unordered iteration: maps.Values`
		_ = v
	}
	_ = Sorted(maps.Keys(m))          // want `unordered iteration: maps.Keys`
	_ = slices.Collect(maps.Keys(m))  // want `unordered iteration: maps.Keys`
	_ = maps.Keys[map[string]int]     // want `unordered iteration: maps.Keys`
	_ = reflect.ValueOf(m).MapKeys()  // want `unordered iteration: (reflect.Value).MapKeys`
	_ = reflect.ValueOf(m).MapRange() // want `unordered iteration: (reflect.Value).MapRange`
	var syncMap sync.Map
	syncMap.Range(func(k, v any) bool { return true }) // want `unordered iteration: (*sync.Map).Range`
	rng := syncMap.Range                               // want `unordered iteration: (*sync.Map).Range`
	_ = rng
	_ = (*sync.Map).Range                   // want `unordered iteration: (*sync.Map).Range`
	_ = reflect.Value.MapKeys               // want `unordered iteration: (reflect.Value).MapKeys`
	mapRange := reflect.ValueOf(m).MapRange // want `unordered iteration: (reflect.Value).MapRange`
	_ = mapRange

	// Floating point.
	_ = 1.5             // want `floating-point value`
	_ = 2i              // want `floating-point value`
	var _ float32       // want `floating-point value`
	_ = float64(len(m)) // want `floating-point value`

	// Output and the environment.
	print("x")                   // want `builtin print`
	println("x")                 // want `builtin println`
	fmt.Fprintln(os.Stdout, "x") // want `use of os.Stdout`
	fmt.Fprintln(os.Stderr, "x") // want `use of os.Stderr`
	fmt.Print("x")               // want `use of fmt.Print`
	fmt.Printf("x")              // want `use of fmt.Printf`
	fmt.Println("x")             // want `use of fmt.Println`
	out := fmt.Println           // want `use of fmt.Println`
	_ = out
	log.Println("x")
	slog.Info("x")
	_ = os.Getenv("X")       // want `use of os.Getenv`
	_, _ = os.LookupEnv("X") // want `use of os.LookupEnv`
	getenv := os.Getenv      // want `use of os.Getenv`
	_ = getenv
	_ = os.Environ()           // want `use of os.Environ`
	_ = os.ExpandEnv("$X")     // want `use of os.ExpandEnv`
	_, _ = syscall.Getenv("X") // want `use of syscall.Getenv`
	_ = syscall.Environ()      // want `use of syscall.Environ`
	environ := os.Environ      // want `use of os.Environ`
	_ = environ
	_, _ = crand.Read(nil)

	// Wall-clock time, as calls and as function values.
	var t time.Time
	_ = time.Now()             // want `use of time.Now`
	_ = time.Since(t)          // want `use of time.Since`
	_ = time.Until(t)          // want `use of time.Until`
	time.Sleep(1)              // want `use of time.Sleep`
	_ = time.After(1)          // want `use of time.After`
	_ = time.AfterFunc(1, nil) // want `use of time.AfterFunc`
	_ = time.NewTimer(1)       // want `use of time.NewTimer`
	_ = time.NewTicker(1)      // want `use of time.NewTicker`
	_ = time.Tick(1)           // want `use of time.Tick`
	now := time.Now            // want `use of time.Now`
	_ = now
	sleep := time.Sleep // want `use of time.Sleep`
	sleep(1)
	_ = time.AfterFunc                            // want `use of time.AfterFunc`
	_, _ = context.WithTimeout(ctx, 1)            // want `use of context.WithTimeout`
	_, _ = context.WithTimeoutCause(ctx, 1, nil)  // want `use of context.WithTimeoutCause`
	_, _ = context.WithDeadline(ctx, t)           // want `use of context.WithDeadline`
	_, _ = context.WithDeadlineCause(ctx, t, nil) // want `use of context.WithDeadlineCause`
	withTimeout := context.WithTimeout            // want `use of context.WithTimeout`
	_ = withTimeout

	// Global randomness: every package-level function, also as a value or instantiated.
	_ = rand.IntN(3)                // want `use of math/rand/v2.IntN`
	_ = rand.N[int64](5)            // want `use of math/rand/v2.N`
	_ = rand.New(rand.NewPCG(1, 2)) // want `use of math/rand/v2.New` `use of math/rand/v2.NewPCG`
	intN := rand.IntN               // want `use of math/rand/v2.IntN`
	_ = intN
	_ = mrand.Intn(3)                 // want `use of math/rand.Intn`
	_ = mrand.New(mrand.NewSource(1)) // want `use of math/rand.New` `use of math/rand.NewSource`

	// Proto messages formatted as text (ETC-005).
	_ = fmt.Sprintf("%v", hs)                                    // want `proto message formatted with %v`
	_ = fmt.Sprintf("%#v", hs)                                   // want `proto message formatted with %v`
	_ = fmt.Sprintf("%5v", hs)                                   // want `proto message formatted with %v`
	_ = fmt.Sprintf("%+v", hs)                                   // want `proto message formatted with %v`
	_ = fmt.Sprintf("%s", hs)                                    // want `proto message formatted with %s`
	_ = fmt.Sprintf("%q", hs)                                    // want `proto message formatted with %q`
	_ = fmt.Sprintf("%x", hs)                                    // want `proto message formatted with %x`
	_ = fmt.Sprintf("%X", hs)                                    // want `proto message formatted with %X`
	_ = fmt.Errorf("%v", hs)                                     // want `proto message formatted with %v`
	fmt.Fprintf(sb, "%v", hs)                                    // want `proto message formatted with %v`
	_ = fmt.Appendf(nil, "%v", hs)                               // want `proto message formatted with %v`
	_ = fmt.Sprintf("100%% %v", hs)                              // want `proto message formatted with %v`
	_ = fmt.Sprintf("%d %v", 1, hs)                              // want `proto message formatted with %v`
	_ = fmt.Sprintf("%[1]v", hs)                                 // want `proto message formatted with %v`
	_ = fmt.Sprintf("%[2]v %[1]d", 1, hs)                        // want `proto message formatted with %v`
	_ = fmt.Sprintf("%*v", 3, hs)                                // want `proto message formatted with %v`
	_ = fmt.Sprintf("%.[2]*[3]v", 1, 3, hs)                      // want `proto message formatted with %v`
	_ = fmt.Sprintf("%.*v", 3, hs)                               // want `proto message formatted with %v`
	_ = fmt.Sprintf("%v", ents)                                  // want `proto message formatted with %v`
	_ = fmt.Sprintf("%v", wrapped{hs})                           // want `proto message formatted with %v`
	_ = fmt.Sprintf("%v", &wrapped{hs})                          // want `proto message formatted with %v`
	_ = fmt.Sprintf("%v", [2]*raftpb.HardState{hs, hs})          // want `proto message formatted with %v`
	_ = fmt.Sprintf("%v", map[string]*raftpb.HardState{"a": hs}) // want `proto message formatted with %v`
	_ = fmt.Sprintf("%v", map[*raftpb.HardState]int{hs: 1})      // want `proto message formatted with %v`
	_ = fmt.Sprintf("%v", &cycle{hs: hs})                        // want `proto message formatted with %v`
	logt[int]("%v", hs)                                          // want `proto message formatted with %v`
	logt2[int, string]("%v", hs)                                 // want `proto message formatted with %v`
	(logf)("%v", hs)                                             // want `proto message formatted with %v`
	_ = fmt.Sprintf(f, hs)                                       // want `proto message passed to Sprintf with a format that is not constant`
	_ = fmt.Sprint(hs)                                           // want `proto message passed to fmt.Sprint`
	_ = fmt.Sprint(ents)                                         // want `proto message passed to fmt.Sprint`
	_ = fmt.Sprintf(f, ents)                                     // want `proto message passed to Sprintf with a format that is not constant`
	_ = fmt.Sprintln(hs)                                         // want `proto message passed to fmt.Sprintln`
	fmt.Fprint(sb, hs)                                           // want `proto message passed to fmt.Fprint`
	logf("%v", hs)                                               // want `proto message formatted with %v`
	logger{}.Logf("%s", hs)                                      // want `proto message formatted with %s`
	_ = hs.String()                                              // want `String() of a proto message`
	str := hs.String                                             // want `String() of a proto message`
	_ = str
	_ = (*raftpb.HardState).String // want `String() of a proto message`
	_ = prototext.Format(hs)
	_ = protojson.Format(hs)

	// Proto messages described with kernel.Describe, which calls String (KRN-100).
	_ = kernel.Describe(hs)                                          // want `proto message passed to Describe`
	_ = (kernel.Describe)(hs)                                        // want `proto message passed to Describe`
	nw.Send(n, 1, hs)                                                // want `proto message passed to Send`
	_ = nw.SendRaw(n, 1, hs, simnet.RawOptions{})                    // want `proto message passed to SendRaw`
	(*simnet.Network).Send(nw, n, 1, hs)                             // want `proto message passed to Send`
	_ = (*simnet.Network).SendRaw(nw, n, 1, hs, simnet.RawOptions{}) // want `proto message passed to SendRaw`
}
