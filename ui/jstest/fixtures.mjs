// Timeline data fixtures of AT-ART-18, shared by the ui/jstest suites.

// FIXTURE_TRACE is the trace.jsonl example of ART §5.4 (the AT-ART-10 (a) timeline data's trace).
export const FIXTURE_TRACE = [
  '{"faultline_trace":1,"faultline_version":"(devel)","go_version":"go1.26.0","package":"example.com/toy","test":"TestToy","subtest":"TestToy/seed=0x0000000000000001","seed":"0x0000000000000001","trace_hash":"0x00000000000000aa","records":12,"dropped":0,"nodes":[{"id":1,"name":"n1","tags":["server"]},{"id":2,"name":"n2","tags":["server"]}]}',
  '{"seq":1,"at":0,"t":"0.000000000s","kind":"kernel.start","text":"start","attrs":[["seed","0x0000000000000001"],["tie_break","seeded"]]}',
  '{"seq":2,"at":0,"t":"0.000000000s","node":1,"kind":"kernel.add_node","cause":1,"text":"n1","attrs":[["tags","server"],["offset_ns","0"],["drift_ppm","0"]]}',
  '{"seq":3,"at":0,"t":"0.000000000s","node":2,"kind":"kernel.add_node","cause":2,"text":"n2","attrs":[["tags","server"],["offset_ns","0"],["drift_ppm","0"]]}',
  '{"seq":4,"at":0,"t":"0.000000000s","node":1,"kind":"kernel.event","cause":2,"text":"boot","attrs":[["id","1"]]}',
  '{"seq":5,"at":0,"t":"0.000000000s","node":1,"inc":1,"kind":"kernel.boot","cause":4,"text":"boot"}',
  '{"seq":6,"at":0,"t":"0.000000000s","node":2,"kind":"kernel.event","cause":3,"text":"boot","attrs":[["id","2"]]}',
  '{"seq":7,"at":0,"t":"0.000000000s","node":2,"inc":1,"kind":"kernel.boot","cause":6,"text":"boot"}',
  '{"seq":8,"at":1000000,"t":"0.001000000s","node":1,"inc":1,"kind":"kernel.event","cause":5,"text":"tick","attrs":[["id","3"]]}',
  '{"seq":9,"at":1000000,"t":"0.001000000s","node":1,"inc":1,"kind":"net.send","cause":8,"text":"send #1 n1 -> n2: \\"ping\\"","attrs":[["msg","1"],["from","n1"],["to","n2"],["payload","\\"ping\\""]]}',
  '{"seq":10,"at":3000000,"t":"0.003000000s","node":2,"inc":1,"kind":"kernel.event","cause":9,"text":"net.deliver","attrs":[["id","4"]]}',
  '{"seq":11,"at":3000000,"t":"0.003000000s","node":2,"inc":1,"kind":"net.deliver","cause":10,"text":"deliver #1.1 n1 -> n2","attrs":[["msg","1"],["copy","1"],["from","n1"],["to","n2"],["latency_ns","2000000"]]}',
  '{"seq":12,"at":3000000,"t":"0.003000000s","kind":"check.violation","cause":11,"text":"invariant \\"no pong\\" violated","attrs":[["kind","invariant"],["check","no pong"],["error","got ping"],["event","4"]]}',
].join("\n") + "\n";

// fixtureData is the timeline data of AT-ART-10 (a).
export function fixtureData() {
  return {
    faultline_timeline: 1,
    title: "faultline TestToy/seed=0x0000000000000001",
    report: {
      faultline_report: 1,
      status: "fail",
      subtest: "TestToy/seed=0x0000000000000001",
      seed: "0x0000000000000001",
      failure: { kind: "invariant", check: "no pong", headline: 'invariant "no pong" violated at t=0.003000000s on n2 (event 4)', record_seq: 12, node_id: 2 },
      run: { trace_hash: "0x00000000000000aa", end_ns: 3000000 },
    },
    trace: FIXTURE_TRACE,
    schedule: '{\n  "faultline_schedule": 1,\n  "events": []\n}\n',
    total: 12,
    dropped: 0,
    window: null,
    slice: { root: 12, seqs: [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12], cap: 200, truncated: false },
  };
}

function line(o) {
  return JSON.stringify(o);
}

// S is one second in nanoseconds.
export const S = 1000000000;

// faultData is the fault fixture of AT-ART-18; records carry the attrs FLT-070 and KRN §8 define.
export function faultData() {
  const trace = [
    line({ faultline_trace: 1, records: 8, dropped: 0, nodes: [{ id: 1, name: "n1", tags: ["server"] }, { id: 2, name: "n2", tags: ["server"] }] }),
    line({ seq: 1, at: 1 * S, node: 1, kind: "fault.crash", text: "crash n1", attrs: [["id", "1"], ["source", "inject"], ["node", "n1"], ["effect", "applied"]] }),
    line({ seq: 2, at: 1 * S, node: 1, inc: 1, kind: "kernel.crash", cause: 1, text: "crash", attrs: [["from", "up"]] }),
    line({ seq: 3, at: 2 * S, node: 1, inc: 2, kind: "kernel.boot", cause: 2, text: "boot" }),
    line({ seq: 4, at: 2 * S, node: 1, kind: "fault.restart", cause: 3, text: "restart n1", attrs: [["id", "2"], ["source", "inject"], ["node", "n1"], ["undoes", "1"], ["effect", "applied"]] }),
    line({ seq: 5, at: 3 * S, kind: "fault.partition", cause: 4, text: "partition n1|n2", attrs: [["id", "3"], ["source", "inject"], ["groups", "n1|n2"], ["effect", "applied"]] }),
    line({ seq: 6, at: 4 * S, kind: "fault.heal", cause: 5, text: "heal", attrs: [["id", "4"], ["source", "inject"], ["undoes", "3"], ["effect", "applied"]] }),
    line({ seq: 7, at: 5 * S, node: 1, inc: 2, kind: "net.send", cause: 6, text: "send #7 n1 -> n2", attrs: [["msg", "7"], ["from", "n1"], ["to", "n2"]] }),
    line({ seq: 8, at: 6 * S, node: 2, inc: 1, kind: "net.drop", cause: 7, text: "drop #7", attrs: [["msg", "7"], ["reason", "partition-in-flight"]] }),
  ].join("\n") + "\n";
  return {
    faultline_timeline: 1,
    title: "faultline faults",
    report: { status: "pass", run: { end_ns: 10 * S } },
    trace,
    schedule: "",
    total: 8,
    dropped: 0,
    window: null,
    slice: null,
  };
}

// edgeData is a timeline whose records exercise the ART-078 rules the two fixtures above leave
// out: a header out of id order with a two-tag node, paused bands closed by a resume and by a
// crash, a down band that only its own node's boot closes, fault records that open no band (noop,
// n=0), a heal whose undoes lists two IDs, a band open to the end, the planner's fault records,
// net.send_raw, a delivery and a drop without a send, a record without text, a last record after
// end_ns, and a recovery band that starts with fault bands.
export function edgeData() {
  const trace = [
    line({ faultline_trace: 1, records: 23, dropped: 0, nodes: [{ id: 2, name: "n2", tags: ["server", "leader"] }, { id: 1, name: "n1" }] }),
    line({ seq: 1, at: 1 * S, node: 1, inc: 1, kind: "kernel.pause", text: "pause" }),
    line({ seq: 2, at: 2 * S, node: 2, inc: 1, kind: "kernel.resume", text: "resume" }),
    line({ seq: 3, at: 3 * S, node: 1, inc: 1, kind: "kernel.resume", text: "resume" }),
    line({ seq: 4, at: 4 * S, node: 2, inc: 1, kind: "kernel.pause", text: "pause" }),
    line({ seq: 5, at: 5 * S, node: 2, inc: 1, kind: "kernel.crash", text: "crash", attrs: [["from", "paused"]] }),
    line({ seq: 6, at: 6 * S, node: 1, inc: 2, kind: "kernel.boot", text: "boot" }),
    line({ seq: 7, at: 7 * S, node: 2, inc: 2, kind: "kernel.boot", text: "boot" }),
    line({ seq: 8, at: 8 * S, kind: "fault.partition", text: "partition n1|n2 (noop)", attrs: [["id", "1"], ["source", "inject"], ["groups", "n1|n2"], ["effect", "noop"]] }),
    line({ seq: 9, at: 8 * S, node: 1, kind: "fault.sync_fail", text: "sync-fail n1 0", attrs: [["id", "2"], ["source", "inject"], ["node", "n1"], ["n", "0"], ["effect", "applied"]] }),
    line({ seq: 10, at: 8 * S, node: 1, kind: "fault.sync_fail", text: "sync-fail n1 2", attrs: [["id", "3"], ["source", "inject"], ["node", "n1"], ["n", "2"], ["effect", "applied"]] }),
    line({ seq: 11, at: 8 * S, node: 1, kind: "fault.cut", text: "cut n1 -> n2", attrs: [["id", "4"], ["source", "inject"], ["node", "n1"], ["peer", "n2"], ["effect", "applied"]] }),
    line({ seq: 12, at: 8 * S, node: 2, kind: "fault.isolate", text: "isolate n2", attrs: [["id", "5"], ["source", "inject"], ["node", "n2"], ["effect", "applied"]] }),
    line({ seq: 13, at: 9 * S, kind: "fault.heal", text: "heal", attrs: [["id", "6"], ["source", "inject"], ["undoes", "4,5"], ["effect", "applied"]] }),
    line({ seq: 14, at: 9 * S, kind: "fault.target", text: "target random 0 crash -> n1", attrs: [["planner", "random"], ["rule", "0"], ["candidates", "n1,n2"], ["chosen", "n1"]] }),
    line({ seq: 15, at: 9 * S, kind: "fault.skip", text: "skip random 1 pause: no candidate", attrs: [["planner", "random"], ["rule", "1"], ["kind", "pause"], ["reason", "no candidate"]] }),
    line({ seq: 16, at: 9 * S, kind: "fault.recover", text: "recover random: 1 active", attrs: [["planner", "random"], ["active", "1"]] }),
    line({ seq: 17, at: 9 * S, node: 1, kind: "fault.error", text: "crash 7 failed: n1 is down", attrs: [["id", "7"], ["error", "n1 is down"]] }),
    line({ seq: 18, at: 9 * S, kind: "fault.suppressed", text: "suppressed (replay): crash n1", attrs: [["source", "replay"], ["detail", "crash n1"]] }),
    line({ seq: 19, at: 10 * S, node: 1, inc: 2, kind: "net.send_raw", text: "send #7 n1 -> n2", attrs: [["msg", "7"], ["from", "n1"], ["to", "n2"]] }),
    line({ seq: 20, at: 10 * S, node: 2, inc: 2, kind: "net.deliver", text: "deliver #7.1 n1 -> n2", attrs: [["msg", "7"], ["copy", "1"]] }),
    line({ seq: 21, at: 11 * S, node: 2, inc: 2, kind: "net.deliver", text: "deliver #99.1", attrs: [["msg", "99"], ["copy", "1"]] }),
    line({ seq: 22, at: 11 * S, node: 1, inc: 2, kind: "net.drop", text: "drop #98", attrs: [["msg", "98"], ["reason", "loss"]] }),
    line({ seq: 23, at: 13 * S, kind: "run.phase" }),
  ].join("\n") + "\n";
  return {
    faultline_timeline: 1,
    title: "faultline edges",
    report: { status: "fail", failure: { record_seq: 22 }, run: { end_ns: 12.5 * S, recovery_ns: 8 * S } },
    trace,
    schedule: "",
    total: 23,
    dropped: 0,
    window: null,
    slice: null,
  };
}
