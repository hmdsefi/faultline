// AT-ART-18: run view model and helpers. Run from the repository root:
//   node --test ui/jstest/*.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";

import { parseTrace, buildModel, firstAtOrAfter, listFaults } from "../static/js/timeline/model.js";
import { formatTime, shortTime, tickStep, tickDecimals, tickLabel, formatCount, counted, laneLabel, recordNode, windowBanner, nextTheme } from "../static/js/timeline/format.js";
import { FIXTURE_TRACE, fixtureData, faultData, edgeData, S } from "./fixtures.mjs";

test("parseTrace defaults missing fields", () => {
  // A blank CRLF line is "\r" after the split: it parses only once its one trailing \r is dropped.
  const { header, records } = parseTrace(FIXTURE_TRACE.replaceAll("\n", "\r\n") + "\r\n");
  assert.equal(header.records, 12);
  assert.equal(records.length, 12);
  assert.deepEqual(records[0], { seq: 1, at: 0, node: 0, inc: 0, kind: "kernel.start", cause: 0, text: "start", attrs: [["seed", "0x0000000000000001"], ["tie_break", "seeded"]] });
  assert.deepEqual(records[4].attrs, []);
});

test("parseTrace names the line it cannot read or place", () => {
  const header = '{"faultline_trace":1,"records":1}';
  assert.throws(() => parseTrace(header + "\n\n{bad\n"), /^SyntaxError: trace line 3: /);
  for (const rec of ['{"seq":1,"kind":"k"}', '{"seq":1,"at":"5","kind":"k"}', '{"at":0,"kind":"k"}', '{"seq":1,"at":0}', "null", "5"]) {
    assert.throws(() => parseTrace(header + "\n" + rec + "\n"), /^Error: trace line 2: a record needs a number seq and at and a string kind$/, rec);
  }
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

test("edge fixture: lanes, end, text and root (ART-078)", () => {
  const m = buildModel(edgeData());
  assert.deepEqual(m.lanes.map((l) => [l.id, laneLabel(l)]), [[0, "global"], [1, "n1"], [2, "n2 [server,leader]"]]);
  assert.equal(m.end, 13 * S, "the last record is later than end_ns");
  assert.equal(m.records[22].text, "");
  assert.equal(m.root, 22, "without a slice the root is failure.record_seq");
});

test("edge fixture: net.send_raw matches, deliveries and drops without a send (ART-078)", () => {
  const m = buildModel(edgeData());
  assert.deepEqual(m.messages, [{ send: 19, recv: 20 }]);
  assert.deepEqual(m.drops, [{ send: null, drop: 22 }]);
});

test("edge fixture: band open and close rules and order (ART-078)", () => {
  const m = buildModel(edgeData());
  // paused closes on its node's resume, or on a crash; down closes only on its own node's boot;
  // noop and n=0 faults open no band; the heal's undoes "4,5" closes two bands; the recovery band
  // (seq 0) sorts before the fault bands that start with it.
  assert.deepEqual(m.bands.map((b) => [b.type, b.lane, b.from / S, b.to / S, b.seq]), [
    ["paused", 1, 1, 3, 1],
    ["paused", 2, 4, 5, 4],
    ["down", 2, 5, 7, 5],
    ["recovery", 0, 8, 13, 0],
    ["fault", 1, 8, 13, 10],
    ["fault", 1, 8, 9, 11],
    ["fault", 2, 8, 9, 12],
  ]);
});

test("listFaults leaves out undo and planner records (ART-079 item 8)", () => {
  assert.deepEqual(listFaults(buildModel(edgeData())).map((f) => f.seq), [8, 9, 10, 11, 12]);
});

test("the causal slice's root wins over failure.record_seq", () => {
  const data = fixtureData();
  data.report.failure.record_seq = 11;
  assert.equal(buildModel(data).root, 12);
});

test("recovery band from report.run.recovery_ns", () => {
  const data = faultData();
  data.report.run.recovery_ns = 7000000000;
  const m = buildModel(data);
  const last = m.bands[m.bands.length - 1];
  assert.deepEqual(last, { lane: 0, from: 7000000000, to: 10000000000, type: "recovery", label: "recovery", seq: 0 });
});

test("firstAtOrAfter places times among the records (UI-087)", () => {
  const m = buildModel(fixtureData());
  assert.equal(firstAtOrAfter(m, 0), 0);
  assert.equal(firstAtOrAfter(m, 1), 7);
  assert.equal(firstAtOrAfter(m, 1000000), 7);
  assert.equal(firstAtOrAfter(m, 3000000), 9);
  assert.equal(firstAtOrAfter(m, 3000001), 12);
});

test("listFaults keeps injected faults with their spans", () => {
  const faults = listFaults(buildModel(faultData()));
  assert.deepEqual(faults.map((f) => [f.seq, f.text, f.band && f.band.from, f.band && f.band.to]), [
    [1, "crash n1", 1 * S, 2 * S],
    [5, "partition n1|n2", 3 * S, 4 * S],
  ]);
  const data = faultData();
  data.trace += JSON.stringify({ seq: 9, at: 7 * S, node: 2, kind: "fault.clock_jump", text: "clock-jump n2 -250ms", attrs: [["id", "5"], ["source", "inject"], ["node", "n2"], ["n", "-250000000"], ["effect", "applied"]] }) + "\n";
  const jump = listFaults(buildModel(data)).at(-1);
  assert.deepEqual([jump.seq, jump.at, jump.band], [9, 7 * S, null]);
});

test("time formatting and ticks", () => {
  assert.equal(formatTime(41207000000), "41.207000000s");
  assert.equal(formatTime(0), "0.000000000s");
  assert.equal(shortTime(41207400000), "41.207s");
  assert.equal(shortTime(2999500), "0.003s");
  assert.equal(tickStep(10000000000, 10), 1000000000);
  assert.equal(tickStep(3000000, 10), 500000);
  assert.equal(tickStep(7, 10), 1);
  assert.equal(tickStep(1500, 10), 200);
  assert.equal(tickStep(20, 10), 2);
  for (const [span, max] of [[NaN, 10], [Infinity, 10], [-Infinity, 10], [100, NaN], [100, undefined], [100, "10"]]) {
    assert.throws(() => tickStep(span, max), /tickStep: span must be finite and maxTicks a number/, span + ", " + max);
  }
  assert.equal(tickDecimals(1000000000), 0);
  assert.equal(tickDecimals(500000000), 1);
  assert.equal(tickDecimals(2000000), 3);
  assert.equal(tickDecimals(5), 9);
  assert.equal(tickLabel(1500000000, 1), "1.5s");
  assert.equal(tickLabel(2000000000, 0), "2s");
  assert.equal(tickLabel(4000000, 3), "0.004s");
});

test("counts, lane labels and record nodes", () => {
  assert.equal(formatCount(1949), "1,949");
  assert.equal(formatCount(1234567), "1,234,567");
  assert.equal(formatCount(901), "901");
  assert.equal(counted(1, "record"), "1 record");
  assert.equal(counted(60000, "record"), "60,000 records");
  const m = buildModel(fixtureData());
  assert.deepEqual(m.lanes.map(laneLabel), ["global", "n1 [server]", "n2 [server]"]);
  assert.equal(recordNode(m, m.records[8]), "n1#1");
  assert.equal(recordNode(m, m.records[11]), "global");
});

test("window banner text (ART-080)", () => {
  const data = { total: 60000, window: { from_seq: 10002, from_ns: 10002000, extra: 1 } };
  assert.equal(
    windowBanner(data, 50000),
    "Showing 50000 of 60000 records: every record from t=0.010002000s (#10002) to the end, plus 1 earlier fault, check, node-lifecycle and causal-slice records. The complete trace is in trace.jsonl and timeline.txt.",
  );
});

test("the theme cycles system, light, dark (UI-178)", () => {
  assert.equal(nextTheme("system"), "light");
  assert.equal(nextTheme("light"), "dark");
  assert.equal(nextTheme("dark"), "system");
  assert.equal(nextTheme("sepia"), "system");
});

test("every durable kind opens a band, a durable fault closes what it undoes, undo kinds stay out of the Faults list (FLT-011, FLT-012, ART-079 item 8)", () => {
  const rec = (seq, kind, node, text, attrs) => JSON.stringify({ seq, at: seq * S, node, kind, text, attrs: [...attrs, ["effect", "applied"]] });
  const trace = [
    JSON.stringify({ faultline_trace: 1, records: 11, dropped: 0, nodes: [{ id: 1, name: "n1", tags: [] }, { id: 2, name: "n2", tags: [] }] }),
    rec(1, "fault.link", 1, "link n1 -> n2", [["id", "1"], ["source", "inject"], ["node", "n1"], ["peer", "n2"], ["latency_ns", "5000000"]]),
    rec(2, "fault.pause", 2, "pause n2", [["id", "2"], ["source", "inject"], ["node", "n2"]]),
    rec(3, "fault.disk_capacity", 1, "disk-capacity n1 4096", [["id", "3"], ["source", "inject"], ["node", "n1"], ["n", "4096"]]),
    rec(4, "fault.crash", 2, "crash n2", [["id", "4"], ["source", "inject"], ["node", "n2"], ["undoes", "2"]]),
    rec(5, "fault.disk_capacity", 1, "disk-capacity n1 0", [["id", "5"], ["source", "inject"], ["node", "n1"], ["n", "0"], ["undoes", "3"]]),
    rec(6, "fault.link_reset", 1, "link-reset n1 -> n2", [["id", "6"], ["source", "inject"], ["node", "n1"], ["peer", "n2"], ["undoes", "1"]]),
    rec(7, "fault.restart", 2, "restart n2", [["id", "7"], ["source", "inject"], ["node", "n2"], ["undoes", "4"]]),
    rec(8, "fault.cut", 1, "cut n1 -> n2", [["id", "8"], ["source", "inject"], ["node", "n1"], ["peer", "n2"]]),
    rec(9, "fault.heal_link", 1, "heal-link n1 -> n2", [["id", "9"], ["source", "inject"], ["node", "n1"], ["peer", "n2"], ["undoes", "8"]]),
    rec(10, "fault.pause", 1, "pause n1", [["id", "10"], ["source", "inject"], ["node", "n1"]]),
    rec(11, "fault.resume", 1, "resume n1", [["id", "11"], ["source", "inject"], ["node", "n1"], ["undoes", "10"]]),
  ].join("\n") + "\n";
  const m = buildModel({ trace, report: { status: "pass", run: { end_ns: 12 * S } }, slice: null, window: null, total: 11, dropped: 0 });
  // A crash ends a pause and a disk-capacity of 0 ends the previous one (FLT-012): both closers
  // are fault records that are not undo kinds.
  assert.deepEqual(m.bands.map((b) => [b.seq, b.from / S, b.to / S]), [[1, 1, 6], [2, 2, 4], [3, 3, 5], [4, 4, 7], [8, 8, 9], [10, 10, 11]]);
  assert.deepEqual(listFaults(m).map((f) => f.seq), [1, 2, 3, 4, 5, 8, 10]);
});

test("tick labels left of t=0 keep their sign", () => {
  assert.equal(tickLabel(-500000000, 1), "-0.5s");
  assert.equal(tickLabel(-2000000000, 0), "-2s");
});
