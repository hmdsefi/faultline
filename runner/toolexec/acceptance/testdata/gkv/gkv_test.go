package gkv_test

import (
	"testing"
	"time"

	"example.com/gkv"
	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

func TestParseWALTornTail(t *testing.T) {
	a, b := gkv.Frame(gkv.Record{Index: 1, Key: "k", Value: "v"}), gkv.Frame(gkv.Record{Index: 2, Key: "k", Value: "w"})
	data := append(append(append([]byte{}, a...), b...), b[:len(b)-3]...) // a torn third record
	recs, valid := gkv.ParseWAL(data)
	if len(recs) != 2 || valid != len(a)+len(b) || recs[1].Value != "w" {
		t.Fatalf("recs %+v valid %d", recs, valid)
	}
	bad := append(append([]byte{}, a...), 0, 0, 0, 2, '{', 'x')
	if recs, valid := gkv.ParseWAL(bad); len(recs) != 1 || valid != len(a) {
		t.Fatalf("invalid JSON tail: recs %+v valid %d", recs, valid)
	}
}

// TestGKV is corpus C2 (EXB-051).
func TestGKV(t *testing.T) {
	faultline.Run(t, faultline.Options{Mode: faultline.ModeGoroutine, Duration: 30 * time.Second,
		Net: simnet.Config{Default: simnet.Link{Latency: time.Millisecond, Jitter: 2 * time.Millisecond}}},
		func(w *faultline.World) {
			s1 := w.AddProcess("s1", gkv.Leader([]string{"s2:7000", "s3:7000"}))
			s2 := w.AddProcess("s2", gkv.Follower)
			s3 := w.AddProcess("s3", gkv.Follower)
			w.AddClientProcess("c1", gkv.Client(w))
			w.AddClientProcess("c2", gkv.Client(w))
			w.Role("follower", func() []kernel.NodeID { return []kernel.NodeID{s2.ID(), s3.ID()} })
			w.Plan(&fault.Random{Rules: []fault.Rule{
				{Kind: fault.KindPartition, Every: 5 * time.Second},
				{Kind: fault.KindCrash, Every: 7 * time.Second, Target: "follower"},
			}})
			w.Final("acked writes durable", func() error {
				return gkv.CheckDurable(w.History.Ops(), w.Disk.Volume(s1))
			})
		})
}
