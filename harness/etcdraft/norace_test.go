//go:build !race

package etcdraft

// raceEnabled reports whether the test binary was built with -race.
const raceEnabled = false
