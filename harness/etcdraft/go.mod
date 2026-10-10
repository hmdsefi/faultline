module github.com/hmdsefi/faultline/harness/etcdraft

go 1.26.0

require (
	go.etcd.io/raft/v3 v3.7.0
	google.golang.org/protobuf v1.36.11
)

replace github.com/hmdsefi/faultline => ../..
