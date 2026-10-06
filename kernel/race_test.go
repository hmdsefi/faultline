//go:build race

package kernel

// raceEnabled reports whether the race detector is on; allocation counts are not representative
// under it.
const raceEnabled = true
