// AT-ART-18: run view model and helpers. Run from the repository root:
//   node --test ui/jstest/*.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";

import { parseTrace, buildModel, firstAtOrAfter, listFaults } from "../static/js/timeline/model.js";
import { formatTime, shortTime, tickStep, tickDecimals, tickLabel, formatCount, counted, laneLabel, recordNode, windowBanner, nextTheme } from "../static/js/timeline/format.js";
import { FIXTURE_TRACE, fixtureData, faultData, edgeData, S } from "./fixtures.mjs";
import { AXIS_HEIGHT, GUTTER, MIN_SPAN_NS, PALETTE, GLYPHS, DRAW_ORDER, category, glyphD, gutterWidth, laneHeight, laneY, toX, timeX, fitView, zoomView, panView, failureView, hitTest, sliceEdges, ticks, draw } from "../static/js/timeline/render.js";
import { RecordingContext } from "./fake-dom.mjs";

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

// near asserts that the numbers in got are within 1e-6 of want.
function near(got, want) {
  assert.ok(got.every((g, i) => Math.abs(g - want[i]) < 1e-6), JSON.stringify(got) + " != " + JSON.stringify(want));
}

// viewOf returns a fitted view of model on a canvas width x height px.
function viewOf(model, mode, width, height) {
  const v = { mode, from: 0, to: 1, width, height, scrollY: 0, laneH: laneHeight(height, model.lanes.length), gutter: GUTTER };
  return fitView(model, v);
}

test("view geometry: fit, zoom around the pointer, limits, hit test", () => {
  const m = buildModel(fixtureData());
  const filters = { showEvents: false, hiddenNamespaces: new Set() };
  const view = viewOf(m, "time", 1136, 400);
  assert.equal(view.laneH, 120);
  assert.deepEqual([view.from, view.to], [-60000, 3060000]);
  const x = toX(view, 1000000);
  const zoomed = zoomView(m, view, x, 2);
  assert.ok(Math.abs(toX(zoomed, 1000000) - x) < 1e-6, "the value under the pointer stays put");
  assert.equal(zoomed.to - zoomed.from, 1560000);
  assert.equal(zoomView(m, view, x, 1e9).to - zoomView(m, view, x, 1e9).from, MIN_SPAN_NS);
  const out = zoomView(m, view, x, 0.1);
  assert.deepEqual([out.from, out.to], [-300000, 3300000], "zooming out stops at 1.2 times the run, panned to its margin");
  const far = panView(m, view, -100000);
  assert.equal(far.to, 3300000, "panning stops a tenth of the run past its end");
  // record 9 (net.send on n1, lane 1) is at t=1ms
  const y = laneY(view, 1);
  assert.equal(y, AXIS_HEIGHT + 120 + 60);
  assert.equal(hitTest(m, view, filters, x + 3, y), 8);
  assert.equal(hitTest(m, view, filters, x + 20, y), -1);
  assert.equal(laneHeight(400, 40), 32, "lanes that do not fit stay 32 px and scroll");
  assert.deepEqual([gutterWidth(599), gutterWidth(600)], [88, 136], "the gutter narrows below 600 px");
});

test("Jump to failure frames the causal slice with the failure at 95 % (UI-106)", () => {
  const m = buildModel(fixtureData());
  const v = failureView(m, viewOf(m, "time", 1136, 400));
  near([v.from, v.to], [3000000 - 0.95 * 3300000, 3000000 + 0.05 * 3300000]);
  const seq = failureView(m, viewOf(m, "seq", 1136, 400));
  near([seq.from, seq.to], [11 - 0.95 * 12.1, 11 + 0.05 * 12.1]);
  const alone = fixtureData();
  alone.slice = null;
  const lone = buildModel(alone);
  const c = failureView(lone, viewOf(lone, "time", 1136, 400));
  assert.deepEqual([c.from, c.to], [2850000, 3150000], "without earlier slice members the failure is centered in a tenth of the run");
  const none = buildModel(faultData());
  assert.equal(failureView(none, viewOf(none, "time", 1136, 400)), null);
});

// runData is a one-node run whose records sit at times (ns), the last a check.violation, ending at
// the last time, with a causal slice of the last k records (no slice when k is 0).
function runData(times, k) {
  const lines = [JSON.stringify({ faultline_trace: 1, records: times.length, dropped: 0, nodes: [{ id: 1, name: "n1", tags: [] }] })];
  times.forEach((at, i) => {
    const kind = i === times.length - 1 ? "check.violation" : "net.send";
    lines.push(JSON.stringify({ seq: i + 1, at, node: i === times.length - 1 ? 0 : 1, inc: 1, kind, cause: i, text: "r" + (i + 1) }));
  });
  const n = times.length;
  const seqs = [];
  for (let s = n - k + 1; s <= n; s++) {
    seqs.push(s);
  }
  return {
    report: { status: "fail", failure: { record_seq: n }, run: { end_ns: times[n - 1] } },
    trace: lines.join("\n") + "\n",
    slice: k > 0 ? { root: n, seqs, cap: k, truncated: true } : null,
    window: null,
    total: n,
  };
}

test("Jump to failure limits the span first, so the failure stays at 95 % or the center (UI-106)", () => {
  // Sequence mode: the last 5 of 100 records span 4 indexes, 4.4 after the 10 % margin, which
  // MIN_SPAN_SEQ raises to 20.
  const seq = buildModel(runData(Array.from({ length: 100 }, (_, i) => (i + 1) * 1000000), 5));
  const s = failureView(seq, viewOf(seq, "seq", 1136, 400));
  near([s.from, s.to], [99 - 0.95 * 20, 99 + 0.05 * 20]);
  // Time mode: the slice spans 600 ns, 660 ns with the margin, which MIN_SPAN_NS raises to 1000.
  const at = 500000600;
  const time = buildModel(runData([0, 500000000, 500000300, at], 3));
  const t = failureView(time, viewOf(time, "time", 1136, 400));
  near([t.from, t.to], [at - 950, at + 50]);
  // Without a slice the failure is centered. A tenth of 11 indexes is raised to the fit span
  // (11.44), and a tenth of 5500 ns to MIN_SPAN_NS.
  const lone = buildModel(runData(Array.from({ length: 12 }, (_, i) => i * 500), 0));
  const c = failureView(lone, viewOf(lone, "seq", 1136, 400));
  near([c.from, c.to], [11 - 5.72, 11 + 5.72]);
  const tl = failureView(lone, viewOf(lone, "time", 1136, 400));
  near([tl.from, tl.to], [5500 - 500, 5500 + 500]);
});

test("axis ticks: seconds with the fewest decimals, #seq in sequence mode", () => {
  const m = buildModel(fixtureData());
  const t = ticks(m, { ...viewOf(m, "time", 456, 400), from: 0, to: 3000000 });
  assert.deepEqual(t.map((x) => x.label), ["0.000s", "0.001s", "0.002s", "0.003s"]);
  assert.equal(ticks(m, { ...viewOf(m, "time", 1136, 400), from: 0, to: 3000000 })[1].label, "0.0005s");
  const s = ticks(m, { ...viewOf(m, "seq", 616, 400), from: 0, to: 11 });
  assert.deepEqual(s.map((x) => x.label), ["#1", "#3", "#5", "#7", "#9", "#11"]);
  const f = buildModel(faultData());
  assert.equal(timeX(f, { ...viewOf(f, "seq", 1136, 400), from: 0, to: 7 }, 2 * S), toX({ gutter: GUTTER, width: 1136, from: 0, to: 7 }, 2));
});

test("glyph categories and shapes (UI-088)", () => {
  const m = buildModel(faultData());
  assert.deepEqual(m.records.map((r) => category(r, 8)), ["fault", "crash", "boot", "fault", "fault", "fault", "send", "violation"]);
  assert.equal(category({ seq: 1, kind: "net.drop" }, 0), "drop");
  assert.equal(category({ seq: 1, kind: "kernel.event" }, 0), "event");
  assert.equal(category({ seq: 1, kind: "run.phase" }, 0), "other");
  assert.equal(category({ seq: 1, kind: "net.partition" }, 0), "fault", "topology records are faults");
  assert.equal(glyphD("diamond", 6.5, 6.5, 9), "M6.5 2L11 6.5L6.5 11L2 6.5Z");
  assert.equal(glyphD("circle", 6.5, 6.5, 7), "M3 6.5a3.5 3.5 0 1 0 7 0a3.5 3.5 0 1 0 -7 0Z");
  assert.equal(glyphD("star5", 6.5, 6.5, 13).split("L").length, 10);
  assert.deepEqual(DRAW_ORDER, ["other", "event", "history", "disk", "dup", "deliver", "send", "drop", "assert", "check", "resume", "pause", "boot", "crash", "fault", "violation"]);
  assert.deepEqual([...DRAW_ORDER].sort(), Object.keys(GLYPHS).sort());
});

test("causal-slice edges include program order", () => {
  const edges = sliceEdges(buildModel(fixtureData()));
  assert.equal(edges.length, 12);
  assert.ok(edges.some((e) => e.from === 7 && e.to === 10));
  assert.ok(edges.some((e) => e.from === 11 && e.to === 12));
  const data = faultData();
  data.slice = { root: 8, seqs: [7, 8], cap: 200, truncated: false };
  assert.deepEqual(sliceEdges(buildModel(data)), [{ from: 7, to: 8 }], "record 7's cause, the heal, is a record outside the slice");
  const self = fixtureData();
  self.trace = self.trace.replace('"kind":"net.send","cause":8', '"kind":"net.send","cause":9');
  assert.deepEqual(sliceEdges(buildModel(self)).filter((e) => e.to === 9), [{ from: 8, to: 9 }], "a record whose cause is its own seq gets no edge from it");
});

// paint draws model m at fit on a 1136 x 400 canvas whose colors are the token names, and
// returns the recording; extra overrides parts of the draw state.
function paint(m, extra) {
  const ctx = new RecordingContext();
  const colors = Object.fromEntries(PALETTE.map((n) => [n, n]));
  const state = { filters: { showEvents: false, hiddenNamespaces: new Set() }, selected: -1, sliceOn: true, edges: sliceEdges(m), colors, fonts: { sans: "sans", mono: "mono" }, failureNode: 0, window: null, ...extra };
  draw(ctx, m, viewOf(m, "time", 1136, 400), state);
  return ctx;
}

test("draw paints traffic first and the failure last, one path per category, and dims records outside the slice", () => {
  const data = faultData();
  data.slice = { root: 8, seqs: [7, 8], cap: 200, truncated: false };
  const m = buildModel(data);
  const ctx = paint(m, {});
  const fills = ctx.ops.filter((o) => o.op === "fill" && o.fill !== "bg");
  assert.deepEqual(fills.map((o) => o.fill), ["send", "node", "node", "fault", "violation", "violation"]);
  assert.deepEqual(fills.map((o) => o.alpha), [1, 0.3, 0.3, 0.3, 1, 1]);
  assert.equal(fills[3].path.filter((op) => op[0] === "M").length, 4, "one path for the four fault glyphs");
  assert.ok(ctx.ops.some((o) => o.op === "stroke" && o.stroke === "violation" && o.dash.join() === "4,3"), "dashed failure line");
  assert.ok(ctx.ops.some((o) => o.op === "stroke" && o.stroke === "select" && o.dash.join() === "3,2"), "dashed slice edge");
  assert.equal(ctx.ops.filter((o) => o.op === "stroke" && o.stroke === "bg").length, 2, "the failure glyph and the failure star are edged in --fl-bg");
  const labels = ctx.ops.filter((o) => o.op === "fillText").map((o) => o.text);
  for (const want of ["n1", "[server]", "crash n1", "partition n1|…", "restart n1", "heal", "down", "failure", "end", "time"]) {
    assert.ok(labels.includes(want), want + " in " + JSON.stringify(labels));
  }
});

test("arrows touching the selection use --fl-select (ART-079 item 5)", () => {
  const m = buildModel(fixtureData());
  const heads = (selected) => paint(m, { selected, sliceOn: false }).ops.filter((o) => o.op === "fill" && (o.fill === "arrow" || o.fill === "select")).map((o) => o.fill);
  assert.deepEqual(heads(-1), ["arrow"]);
  assert.deepEqual(heads(8), ["select"], "record 9 sends the message");
});

test("faults without a band carry their text on the lane; banded and planner records do not (ART-079 item 4)", () => {
  const line = (o) => JSON.stringify(o);
  const trace = [
    line({ faultline_trace: 1, records: 3, dropped: 0, nodes: [{ id: 1, name: "n1", tags: [] }, { id: 2, name: "n2", tags: [] }] }),
    line({ seq: 1, at: 1 * S, node: 1, kind: "fault.crash", text: "crash n1 (noop)", attrs: [["id", "1"], ["source", "inject"], ["node", "n1"], ["effect", "noop"]] }),
    line({ seq: 2, at: 1 * S, node: 2, kind: "fault.sync_fail", text: "sync-fail n2 2", attrs: [["id", "2"], ["source", "inject"], ["node", "n2"], ["n", "2"], ["effect", "applied"]] }),
    line({ seq: 3, at: 2 * S, kind: "fault.recover", text: "recover random: 0 active", attrs: [["planner", "random"], ["active", "0"]] }),
  ].join("\n") + "\n";
  const m = buildModel({ report: { status: "pass", run: { end_ns: 10 * S } }, trace, slice: null, window: null, total: 3 });
  const texts = paint(m, {}).ops.filter((o) => o.op === "fillText").map((o) => o.text);
  assert.ok(texts.includes("crash n1 (noop)"), "a noop crash opened no band");
  assert.ok(texts.includes("sync-fail n2 2 (until end)"), "the band carries the label");
  assert.ok(!texts.includes("sync-fail n2 2"), "a banded fault is not labeled on the lane");
  assert.ok(!texts.includes("recover random: 0 active"), "planner records are not labeled");
});

test("kinds named like Object.prototype members are other records (KRN-090)", () => {
  for (const kind of ["constructor", "toString", "valueOf", "hasOwnProperty", "__proto__"]) {
    assert.equal(category({ seq: 1, kind }, 0), "other", kind);
  }
});

test("hit test: the axis and the gutter select nothing, hidden kinds are skipped (ART-079 item 8)", () => {
  const m = buildModel(fixtureData());
  const filters = { showEvents: false, hiddenNamespaces: new Set() };
  const view = viewOf(m, "time", 1136, 400);
  const x = toX(view, 1000000);
  const under = { ...view, scrollY: 200 };
  assert.equal(laneY(under, 1), 10, "lane 1 scrolled under the axis");
  assert.equal(hitTest(m, under, filters, x, 10), -1);
  const below = { ...view, scrollY: 150 };
  assert.equal(hitTest(m, below, filters, x, laneY(below, 1)), 8);
  const flush = { ...view, from: 0, to: 3000000 };
  assert.equal(hitTest(m, flush, filters, GUTTER + 1, laneY(flush, 1)), 4, "record 5 at t=0 on the gutter's edge");
  assert.equal(hitTest(m, flush, filters, GUTTER - 3, laneY(flush, 1)), -1);
  assert.equal(hitTest(m, view, { showEvents: false, hiddenNamespaces: new Set(["net"]) }, x + 3, laneY(view, 1)), -1);
});

test("the failure node's lane gets a second star; a cut trace marks and shades its window (ART-079 items 6 and 7)", () => {
  const m = buildModel(fixtureData());
  const edged = (ctx) => ctx.ops.filter((o) => o.op === "stroke" && o.stroke === "bg").length;
  assert.equal(edged(paint(m, { failureNode: 2 })), 3, "n2's lane carries a star too");
  const cut = paint(m, { window: { from_seq: 9, from_ns: 1000000, extra: 0 } });
  assert.ok(cut.ops.some((o) => o.op === "stroke" && o.stroke === "muted" && o.dash.join() === "4,3"), "window line");
  assert.ok(cut.ops.some((o) => o.op === "fillText" && o.text === "window from #9"), "window tag");
  const view = viewOf(m, "time", 1136, 400);
  const shade = cut.ops.filter((o) => o.op === "fillRect" && o.fill === "muted" && o.alpha === 0.06);
  assert.equal(shade.length, 1);
  near(shade[0].rect.slice(0, 3), [toX(view, 0), AXIS_HEIGHT, toX(view, 1000000) - toX(view, 0)]);
});

test("in sequence mode a band open to the end stops at the last record (ART-079 item 6, UI-087)", () => {
  const data = faultData();
  data.trace = data.trace.replace('["undoes","3"]', '["undoes","99"]');
  const m = buildModel(data);
  const view = viewOf(m, "seq", 1136, 400);
  const ctx = new RecordingContext();
  const colors = Object.fromEntries(PALETTE.map((n) => [n, n]));
  draw(ctx, m, view, { filters: { showEvents: false, hiddenNamespaces: new Set() }, selected: -1, sliceOn: false, edges: [], colors, fonts: { sans: "sans", mono: "mono" }, failureNode: 0, window: null });
  const tint = ctx.ops.filter((o) => o.op === "fillRect" && o.fill === "fault" && o.alpha === 0.07 && o.rect[1] === AXIS_HEIGHT + 1);
  assert.equal(tint.length, 1, "the partition on the global lane, open to the end");
  near([tint[0].rect[0] + tint[0].rect[2]], [toX(view, m.records.length - 1)]);
});

test("axis tags hide the tick labels they cover; drops on one lane get no line (ART-079 items 3 and 5)", () => {
  const m = buildModel(fixtureData());
  const ctx = new RecordingContext();
  const colors = Object.fromEntries(PALETTE.map((n) => [n, n]));
  const state = { filters: { showEvents: false, hiddenNamespaces: new Set() }, selected: -1, sliceOn: false, edges: [], colors, fonts: { sans: "sans", mono: "mono" }, failureNode: 0, window: null };
  draw(ctx, m, { ...viewOf(m, "time", 1136, 400), from: 0, to: 4000000 }, state);
  const labels = ctx.ops.filter((o) => o.op === "fillText").map((o) => o.text);
  assert.ok(labels.includes("0.0025s") && labels.includes("failure"), JSON.stringify(labels));
  assert.ok(!labels.includes("0.0030s"), "the failure tag covers the 3 ms label");
  const drops = (data) => paint(buildModel(data), { sliceOn: false }).ops.filter((o) => o.op === "stroke" && o.stroke === "drop" && o.dash.join() === "4,3").length;
  const same = faultData();
  same.trace = same.trace.replace('"seq":8,"at":6000000000,"node":2', '"seq":8,"at":6000000000,"node":1');
  assert.deepEqual([drops(faultData()), drops(same)], [1, 0]);
});

test("a slice member missing from the trace gets no edge and does not stop the drawing (ART-079 item 10)", () => {
  const data = fixtureData();
  data.trace = data.trace.split("\n").filter((l) => !l.startsWith('{"seq":8,')).join("\n");
  const m = buildModel(data);
  assert.deepEqual([m.bySeq.has(8), m.slice.has(8)], [false, true], "record 8 is cut; data.slice still names it");
  const edges = sliceEdges(m);
  assert.deepEqual(edges.filter((e) => e.from === 8 || e.to === 8), []);
  assert.ok(edges.some((e) => e.from === 5 && e.to === 9), "record 9's program order skips the missing record");
  assert.doesNotThrow(() => paint(m, {}));
});
