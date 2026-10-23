module github.com/hmdsefi/faultline/runner/wasm

go 1.26.0

require (
	github.com/hmdsefi/faultline v0.1.2
	github.com/tetratelabs/wazero v1.12.0
)

require (
	github.com/hmdsefi/gograph v0.8.2 // indirect
	golang.org/x/sys v0.44.0 // indirect
)

replace github.com/hmdsefi/faultline => ../..
