package artifact

import "github.com/hmdsefi/faultline/kernel"

// fixtureTraceText is the trace.jsonl example of ART §5.4, byte for byte.
const fixtureTraceText = `{"faultline_trace":1,"faultline_version":"(devel)","go_version":"go1.26.0","package":"example.com/toy","test":"TestToy","subtest":"TestToy/seed=0x0000000000000001","seed":"0x0000000000000001","trace_hash":"0x00000000000000aa","records":12,"dropped":0,"nodes":[{"id":1,"name":"n1","tags":["server"]},{"id":2,"name":"n2","tags":["server"]}]}
{"seq":1,"at":0,"t":"0.000000000s","kind":"kernel.start","text":"start","attrs":[["seed","0x0000000000000001"],["tie_break","seeded"]]}
{"seq":2,"at":0,"t":"0.000000000s","node":1,"kind":"kernel.add_node","cause":1,"text":"n1","attrs":[["tags","server"],["offset_ns","0"],["drift_ppm","0"]]}
{"seq":3,"at":0,"t":"0.000000000s","node":2,"kind":"kernel.add_node","cause":2,"text":"n2","attrs":[["tags","server"],["offset_ns","0"],["drift_ppm","0"]]}
{"seq":4,"at":0,"t":"0.000000000s","node":1,"kind":"kernel.event","cause":2,"text":"boot","attrs":[["id","1"]]}
{"seq":5,"at":0,"t":"0.000000000s","node":1,"inc":1,"kind":"kernel.boot","cause":4,"text":"boot"}
{"seq":6,"at":0,"t":"0.000000000s","node":2,"kind":"kernel.event","cause":3,"text":"boot","attrs":[["id","2"]]}
{"seq":7,"at":0,"t":"0.000000000s","node":2,"inc":1,"kind":"kernel.boot","cause":6,"text":"boot"}
{"seq":8,"at":1000000,"t":"0.001000000s","node":1,"inc":1,"kind":"kernel.event","cause":5,"text":"tick","attrs":[["id","3"]]}
{"seq":9,"at":1000000,"t":"0.001000000s","node":1,"inc":1,"kind":"net.send","cause":8,"text":"send #1 n1 -> n2: \"ping\"","attrs":[["msg","1"],["from","n1"],["to","n2"],["payload","\"ping\""]]}
{"seq":10,"at":3000000,"t":"0.003000000s","node":2,"inc":1,"kind":"kernel.event","cause":9,"text":"net.deliver","attrs":[["id","4"]]}
{"seq":11,"at":3000000,"t":"0.003000000s","node":2,"inc":1,"kind":"net.deliver","cause":10,"text":"deliver #1.1 n1 -> n2","attrs":[["msg","1"],["copy","1"],["from","n1"],["to","n2"],["latency_ns","2000000"]]}
{"seq":12,"at":3000000,"t":"0.003000000s","kind":"check.violation","cause":11,"text":"invariant \"no pong\" violated","attrs":[["kind","invariant"],["check","no pong"],["error","got ping"],["event","4"]]}
`

func attrs(kv ...string) []kernel.Attr {
	out := make([]kernel.Attr, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, kernel.Attr{Key: kv[i], Value: kv[i+1]})
	}
	return out
}

// fixtureRecords returns the 12 records of ART §10.
func fixtureRecords() []kernel.Record {
	return []kernel.Record{
		{Seq: 1, At: 0, Kind: "kernel.start", Text: "start", Attrs: attrs("seed", "0x0000000000000001", "tie_break", "seeded")},
		{Seq: 2, At: 0, Node: 1, Kind: "kernel.add_node", Cause: 1, Text: "n1", Attrs: attrs("tags", "server", "offset_ns", "0", "drift_ppm", "0")},
		{Seq: 3, At: 0, Node: 2, Kind: "kernel.add_node", Cause: 2, Text: "n2", Attrs: attrs("tags", "server", "offset_ns", "0", "drift_ppm", "0")},
		{Seq: 4, At: 0, Node: 1, Kind: "kernel.event", Cause: 2, Text: "boot", Attrs: attrs("id", "1")},
		{Seq: 5, At: 0, Node: 1, Inc: 1, Kind: "kernel.boot", Cause: 4, Text: "boot"},
		{Seq: 6, At: 0, Node: 2, Kind: "kernel.event", Cause: 3, Text: "boot", Attrs: attrs("id", "2")},
		{Seq: 7, At: 0, Node: 2, Inc: 1, Kind: "kernel.boot", Cause: 6, Text: "boot"},
		{Seq: 8, At: 1000000, Node: 1, Inc: 1, Kind: "kernel.event", Cause: 5, Text: "tick", Attrs: attrs("id", "3")},
		{Seq: 9, At: 1000000, Node: 1, Inc: 1, Kind: "net.send", Cause: 8, Text: `send #1 n1 -> n2: "ping"`, Attrs: attrs("msg", "1", "from", "n1", "to", "n2", "payload", `"ping"`)},
		{Seq: 10, At: 3000000, Node: 2, Inc: 1, Kind: "kernel.event", Cause: 9, Text: "net.deliver", Attrs: attrs("id", "4")},
		{Seq: 11, At: 3000000, Node: 2, Inc: 1, Kind: "net.deliver", Cause: 10, Text: "deliver #1.1 n1 -> n2", Attrs: attrs("msg", "1", "copy", "1", "from", "n1", "to", "n2", "latency_ns", "2000000")},
		{Seq: 12, At: 3000000, Kind: "check.violation", Cause: 11, Text: `invariant "no pong" violated`, Attrs: attrs("kind", "invariant", "check", "no pong", "error", "got ping", "event", "4")},
	}
}

func fixtureNodes() []Node {
	return []Node{{ID: 1, Name: "n1", Tags: []string{"server"}}, {ID: 2, Name: "n2", Tags: []string{"server"}}}
}

// fixtureTrace returns the fixture trace with its header (ART §10).
func fixtureTrace() *Trace {
	return &Trace{
		Header: TraceHeader{
			Version:          1,
			FaultlineVersion: "(devel)",
			GoVersion:        "go1.26.0",
			Package:          "example.com/toy",
			Test:             "TestToy",
			Subtest:          "TestToy/seed=0x0000000000000001",
			Seed:             "0x0000000000000001",
			TraceHash:        "0x00000000000000aa",
			Records:          12,
			Nodes:            fixtureNodes(),
		},
		Records: fixtureRecords(),
	}
}
