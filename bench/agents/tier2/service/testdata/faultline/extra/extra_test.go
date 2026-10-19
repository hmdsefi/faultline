package leasekv_test

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel/fault"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// TestLeaseKVSeeds runs the world of TestLeaseKV, or a harsher one, for LEASEKV_SEEDS seeds from
// the base of the test name LEASEKV_BASE, with KeepGoing. LEASEKV_WORLD picks the world:
//
//   - "" or "base": leaseWorld;
//   - "crashes": crashes every 500 ms, both partition rules every 300 ms;
//   - "long": 20 s runs with crashes every 700 ms;
//   - "pause": leaseFaults plus a pause rule on s1, s2, st1 and st2 (10 to 400 ms, every 2 s);
//   - "tail": leaseWorld with tail delays on every link (2 % of messages, up to 40 ms).
//
// calibrate.sh copies this file into a copy of a module, next to the version's original test
// files. It is never part of a module.
func TestLeaseKVSeeds(t *testing.T) {
	n, err := strconv.Atoi(os.Getenv("LEASEKV_SEEDS"))
	if err != nil || n <= 0 || os.Getenv("LEASEKV_BASE") == "" {
		t.Fatalf("set LEASEKV_SEEDS (a positive count) and LEASEKV_BASE (a test name)")
	}
	opts := leaseOptions
	opts.Seeds, opts.KeepGoing = n, true
	opts.BaseSeed = faultline.NameBase(os.Getenv("LEASEKV_BASE"))
	rules := leaseFaults
	switch os.Getenv("LEASEKV_WORLD") {
	case "", "base":
	case "crashes":
		rules = []fault.Rule{
			{Kind: fault.KindCrash, Target: "crashable", Every: 500 * time.Millisecond, MinFor: crashMin, MaxFor: crashMax},
			{Kind: fault.KindPartition, Target: "servers", Every: 300 * time.Millisecond, MinFor: partitionMin, MaxFor: partitionMax},
			{Kind: fault.KindPartition, Target: "stores", Every: 300 * time.Millisecond, MinFor: partitionMin, MaxFor: partitionMax},
		}
	case "long":
		opts.Duration = 20 * time.Second
		rules = append([]fault.Rule{
			{Kind: fault.KindCrash, Target: "crashable", Every: 700 * time.Millisecond, MinFor: crashMin, MaxFor: crashMax},
		}, leaseFaults[1:]...)
	case "pause":
		rules = append(append([]fault.Rule(nil), leaseFaults...), fault.Rule{
			Kind: fault.KindPause, Target: "crashable", Every: 2 * time.Second,
			MinFor: 10 * time.Millisecond, MaxFor: 400 * time.Millisecond,
		})
	case "tail":
		opts.Net = simnet.Config{Default: simnet.Link{
			Latency: time.Millisecond, Jitter: 4 * time.Millisecond, TailPPM: 20_000, Tail: 40 * time.Millisecond,
		}}
	default:
		t.Fatalf("unknown LEASEKV_WORLD %q", os.Getenv("LEASEKV_WORLD"))
	}
	faultline.Run(t, opts, func(w *faultline.World) { buildWorld(w, rules) })
}
