package etcdraft

import (
	"strconv"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
)

// AT-ETC-16 (ETC-195): DefaultConfig with FaultsDefault passes 200 seeds for 3 and 5 nodes.
func TestDefaultFaults(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("200 seeds per cluster size; skipped under -short and -race")
	}
	for _, nodes := range []int{3, 5} {
		t.Run(strconv.Itoa(nodes)+"nodes", func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Nodes = nodes
			Test(t, cfg, faultline.Options{Seeds: 200, Duration: 60 * time.Second})
		})
	}
}
