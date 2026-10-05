//go:build tools

// Package deps keeps github.com/hmdsefi/gograph in go.mod until a package of the root module
// imports it (plan 1.2: kernel/simnet). The tools build tag excludes this file from every build,
// from the determinism lint and from the import-boundary check; go mod tidy still sees the import.
// Delete this directory in the change that first imports gograph from a real package.
package deps

import _ "github.com/hmdsefi/gograph"
