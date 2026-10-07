package history_test

import (
	"testing"

	"github.com/hmdsefi/faultline/internal/golden"
	"github.com/hmdsefi/faultline/kernel"
)

// AT-HIS-13
func TestDeterminism(t *testing.T) {
	s1, r1, _ := clientToy(5, kernel.TraceConfig{})
	s2, r2, _ := clientToy(5, kernel.TraceConfig{})
	if s1.TraceHash() != s2.TraceHash() || jsonl(t, r1) != jsonl(t, r2) {
		t.Fatalf("seed 5 twice differs")
	}
	if r1.Len() < 100 {
		t.Fatalf("the toy recorded only %d ops", r1.Len())
	}
	if _, r3, _ := clientToy(6, kernel.TraceConfig{}); jsonl(t, r3) == jsonl(t, r1) {
		t.Fatalf("seeds 5 and 6 wrote the same history")
	}
	golden.Check(t, 5, func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		s, _, stop := clientToy(seed, trace)
		return s, stop
	})
}
