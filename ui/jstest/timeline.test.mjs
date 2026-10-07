// AT-ART-18: run view model and helpers. Run from the repository root:
//   node --test ui/jstest/*.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";

import { parseTrace, buildModel } from "../static/js/timeline/model.js";
import { formatTime, tickStep, tickDecimals, tickLabel, formatAttrValue, windowBanner } from "../static/js/timeline/format.js";

// The trace.jsonl example of ART §5.4 (the AT-ART-10 (a) timeline data's trace).
const FIXTURE_TRACE = [
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

function fixtureData() {
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

// The fault fixture of AT-ART-18.
function faultData() {
  const S = 1000000000;
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

test("parseTrace defaults missing fields", () => {
  const { header, records } = parseTrace(FIXTURE_TRACE.replaceAll("\n", "\r\n"));
  assert.equal(header.records, 12);
  assert.equal(records.length, 12);
  assert.deepEqual(records[0], { seq: 1, at: 0, node: 0, inc: 0, kind: "kernel.start", cause: 0, text: "start", attrs: [["seed", "0x0000000000000001"], ["tie_break", "seeded"]] });
  assert.deepEqual(records[4].attrs, []);
});

test("buildModel of the fixture timeline", () => {
  const data = fixtureData();
  const m = buildModel(data);
  assert.deepEqual(m.lanes.map((l) => l.name), ["global", "n1", "n2"]);
  assert.deepEqual(m.messages, [{ send: 9, recv: 11 }]);
  assert.deepEqual(m.drops, []);
  assert.equal(m.root, 12);
  assert.equal(m.slice.size, data.slice.seqs.length);
  assert.equal(m.end, 3000000);
  assert.equal(m.bySeq.get(12), 11);
});

test("buildModel bands, drops and end of the fault fixture", () => {
  const m = buildModel(faultData());
  const S = 1000000000;
  assert.deepEqual(
    m.bands.map((b) => ({ lane: b.lane, from: b.from, to: b.to, type: b.type })),
    [
      { lane: 1, from: 1 * S, to: 2 * S, type: "fault" },
      { lane: 1, from: 1 * S, to: 2 * S, type: "down" },
      { lane: 0, from: 3 * S, to: 4 * S, type: "fault" },
    ],
  );
  assert.equal(m.bands[0].label, "crash n1");
  assert.deepEqual(m.drops, [{ send: 7, drop: 8 }]);
  assert.equal(m.end, 10 * S);
  assert.equal(m.root, 0);
  assert.equal(m.slice.size, 0);
});

test("recovery band from report.run.recovery_ns", () => {
  const data = faultData();
  data.report.run.recovery_ns = 7000000000;
  const m = buildModel(data);
  const last = m.bands[m.bands.length - 1];
  assert.deepEqual(last, { lane: 0, from: 7000000000, to: 10000000000, type: "recovery", label: "recovery", seq: 0 });
});

test("time formatting and ticks", () => {
  assert.equal(formatTime(41207000000), "41.207000000s");
  assert.equal(formatTime(0), "0.000000000s");
  assert.equal(tickStep(10000000000, 10), 1000000000);
  assert.equal(tickStep(3000000, 10), 500000);
  assert.equal(tickStep(7, 10), 1);
  assert.equal(tickDecimals(1000000000), 0);
  assert.equal(tickDecimals(500000000), 1);
  assert.equal(tickDecimals(2000000), 3);
  assert.equal(tickDecimals(5), 9);
  assert.equal(tickLabel(1500000000, 1), "1.5s");
  assert.equal(tickLabel(2000000000, 0), "2s");
  assert.equal(tickLabel(4000000, 3), "0.004s");
  assert.equal(formatAttrValue("server"), "server");
  assert.equal(formatAttrValue("no pong"), '"no pong"');
});

test("window banner text (ART-080)", () => {
  const data = { total: 60000, window: { from_seq: 10002, from_ns: 10002000, extra: 1 } };
  assert.equal(
    windowBanner(data, 50000),
    "Showing 50000 of 60000 records: every record from t=0.010002000s (#10002) to the end, plus 1 earlier fault, check, node-lifecycle and causal-slice records. The complete trace is in trace.jsonl and timeline.txt.",
  );
});
