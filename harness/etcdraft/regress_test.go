package etcdraft

import (
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
)

// regression is a seed pinned after a harness bug was fixed (ETC-204).
type regression struct {
	name   string // the slug of the findings report that explains the bug
	seed   uint64
	config func() Config // the configuration the seed failed with
	dur    time.Duration // faultline.Options.Duration of the failing run
}

// regressions lists the pinned seeds. Append one entry for every harness bug fixed under
// the triage protocol; never remove one.
var regressions []regression

// TestRegressions runs every pinned seed with the configuration it failed with; each
// must now pass.
func TestRegressions(t *testing.T) {
	for _, r := range regressions {
		t.Run(r.name, func(t *testing.T) {
			cfg := r.config()
			opts := testOptions(t, &cfg, r.dur, false)
			runSeed(t, r.seed, opts, func(w *faultline.World) {
				Setup(w, cfg)
				w.Plan(Faults(cfg))
			})
		})
	}
}
