module github.com/hmdsefi/faultline/shims/grpcsim

go 1.26.0

require (
	github.com/hmdsefi/faultline v0.1.2
	google.golang.org/grpc v1.84.0
)

require (
	github.com/hmdsefi/gograph v0.8.2 // indirect
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260706201446-f0a921348800 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace github.com/hmdsefi/faultline => ../..
