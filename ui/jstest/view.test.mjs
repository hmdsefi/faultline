// AT-ART-18: the run view's DOM (ART-079), on the fake DOM of fake-dom.mjs.
import { test } from "node:test";
import assert from "node:assert/strict";

import { mountTimeline, mountError, wheelNotches } from "../static/js/timeline/view.js";
import { fixtureData, faultData } from "./fixtures.mjs";
import { FakeDocument, RecordingContext, findAll, byClass, byTag, byText } from "./fake-dom.mjs";

// richData is the fixture timeline with the report fields a real artifact has (ART-021).
function richData() {
  const data = fixtureData();
  Object.assign(data.report, {
    package: "example.com/toy",
    test: "TestToy",
    replay: { command: "FAULTLINE_SEED=0x0000000000000001 go test -run '^TestToy$' ./toy" },
    versions: { faultline: "v0.1.0", go: "go1.26.0", goos: "linux", goarch: "amd64" },
    nodes: [{ id: 1, name: "n1", tags: ["server"] }, { id: 2, name: "n2", tags: ["server"] }],
    dir: "/tmp/faultline/example.com_toy/TestToy/0000000000000001",
  });
  Object.assign(data.report.failure, { message: "got ping", at: "0.003000000s", node: "n2", event: 4 });
  Object.assign(data.report.run, { events: 4, stop: "failed", attempts: 1 });
  return data;
}

// laneData is a run of n nodes that each send one message, at 1 ms, 2 ms and on, so with 30 nodes
// the lanes overflow the well.
function laneData(n) {
  const nodes = Array.from({ length: n }, (_, i) => ({ id: i + 1, name: "n" + (i + 1), tags: [] }));
  const lines = [JSON.stringify({ faultline_trace: 1, records: n, dropped: 0, nodes })];
  for (const nd of nodes) {
    lines.push(JSON.stringify({ seq: nd.id, at: nd.id * 1000000, node: nd.id, inc: 1, kind: "net.send", text: "send " + nd.id }));
  }
  return { report: { status: "pass", run: { end_ns: n * 1000000 } }, trace: lines.join("\n") + "\n", slice: null, window: null, total: n };
}

// mount mounts data on a fresh fake document and returns the pieces the tests look at.
function mount(data, options) {
  const doc = new FakeDocument(options);
  const root = doc.createElement("div");
  doc.body.append(root);
  const themes = [];
  const host = { current: "system", theme: () => host.current, cycleTheme: () => themes.push("cycle") };
  const api = mountTimeline(root, data, host);
  const canvas = byTag(root, "canvas")[0];
  const key = (k, mods) => canvas.dispatch("keydown", { key: k, ...mods });
  const live = () => byClass(root, "fl-vh")[0].textContent;
  const valuetext = () => byClass(root, "fl-overview")[0].getAttribute("aria-valuetext");
  const inspector = () => findAll(root, (e) => e.getAttribute("aria-label") === "Inspector")[0];
  return { doc, root, api, host, themes, canvas, key, live, valuetext, inspector };
}

const texts = (els) => els.map((e) => e.textContent);

test("header, title row and side panels of a failing run (ART-079 item 1)", () => {
  const { root } = mount(richData());
  assert.ok(root.classList.contains("fl-run"));
  assert.deepEqual(texts(byTag(byTag(root, "nav")[0], "li")), ["example.com/toy", "TestToy", "seed=0x0000000000000001"]);
  assert.equal(byTag(root, "h1")[0].textContent, "TestToy");
  assert.equal(byClass(root, "fl-status")[0].textContent, "FAIL");
  assert.equal(byClass(root, "fl-headline")[0].textContent, 'invariant "no pong" violated at t=0.003000000s on n2 (event 4)');
  const chips = byClass(byClass(root, "fl-head")[0], "fl-pill").filter((e) => !e.classList.contains("fl-status"));
  assert.deepEqual(texts(chips), ["0.003s", "4 events", "12 records", "2 nodes", "stop failed"]);
  assert.equal(byTag(byClass(root, "fl-head")[0], "code")[0].textContent, "FAULTLINE_SEED=0x0000000000000001 go test -run '^TestToy$' ./toy");
  assert.equal(byTag(root, "canvas")[0].getAttribute("aria-label"), "Run timeline, 12 records, 3 lanes");
  assert.equal(byClass(byClass(root, "fl-tl-head")[0], "fl-count")[0].textContent, "3 lanes, 12 records");
  const failure = findAll(root, (e) => e.getAttribute("aria-label") === "Failure")[0];
  assert.equal(byTag(failure, "pre")[0].textContent, "got ping");
  assert.deepEqual(texts(byTag(failure, "dt")), ["Check", "Kind", "Time", "Node", "Event", "Record"]);
  assert.equal(byTag(failure, "dd")[5].textContent, "#12 check.violation");
  assert.equal(byClass(failure, "fl-hint")[0].textContent, "The failure record #12 is on the global lane. n2 ran event 4, the last event before it, so n2's lane carries a star too.");
  const faults = findAll(root, (e) => e.getAttribute("aria-label") === "Faults")[0];
  assert.equal(faults.textContent, "Faults0No faults in this trace.");
  const report = findAll(root, (e) => e.getAttribute("aria-label") === "Report")[0];
  assert.deepEqual(texts(byTag(report, "dt")), ["Test", "Subtest", "Package", "Seed", "Ended", "Stop", "Events", "Records", "Attempts", "faultline", "Go", "Platform", "Artifacts"]);
  assert.equal(byTag(root, "header")[0].textContent, "faultlineexample.com/toyTestToyseed=0x0000000000000001v0.1.0");
  assert.equal(byText(root, "button", "Skip to timeline").getAttribute("class"), "fl-skip fl-primary");
  assert.deepEqual(byTag(root, "h2").map((e) => e.getAttribute("aria-label")), ["Timeline, 3 lanes, 12 records", "Failure", "Faults, 0", "Inspector", "Report", "Legend and keys"]);
  const legend = findAll(root, (e) => e.tagName === "SECTION" && e.getAttribute("aria-label") === "Legend and keys")[0];
  assert.deepEqual(texts(byTag(legend, "h3")), ["Bands and lines", "Keys, with the timeline focused"]);
  assert.deepEqual(texts(byTag(legend, "dd")), ["Zoom in and out", "Pan by a tenth of the view", "Scroll the lanes", "Fit the whole run", "Jump to the failure", "Change the theme", "Zoom around the pointer", "Scroll the lanes", "Pan"]);
  assert.deepEqual(texts(byTag(legend, "dt")).slice(2, 3).concat(texts(byTag(legend, "dt")).slice(7, 8)), ["↑↓", "Shift+Wheel"]);
  assert.deepEqual(findAll(root, (e) => e.hasAttribute("aria-keyshortcuts")).map((e) => e.getAttribute("aria-keyshortcuts")), ["t"], "only t works away from the timeline");
});

test("kind chips filter records; scheduler events start hidden (ART-079 item 4)", () => {
  const { root, canvas } = mount(richData());
  const chips = byClass(byClass(root, "fl-kinds")[0], "fl-chip");
  assert.deepEqual(texts(chips), ["check1", "kernel5", "net2", "scheduler events4"]);
  assert.deepEqual(chips.map((c) => c.getAttribute("aria-pressed")), ["true", "true", "true", "false"]);
  const shown = byClass(root, "fl-shown")[0];
  const sliceEdges = () => canvas.context.ops.filter((o) => o.op === "stroke" && o.dash.join() === "3,2").at(-1).path.length / 2;
  assert.equal(shown.textContent, "8 of 12 shown");
  assert.equal(sliceEdges(), 8, "slice edges bridge the hidden scheduler events (ART-079 item 10)");
  chips[2].click();
  assert.equal(chips[2].getAttribute("aria-pressed"), "false");
  assert.equal(shown.textContent, "6 of 12 shown");
  assert.equal(sliceEdges(), 6);
  chips[3].click();
  assert.equal(shown.textContent, "10 of 12 shown");
  assert.equal(sliceEdges(), 10);
  assert.deepEqual(texts(byTag(byClass(root, "fl-legend")[0], "li")), ["failure", "boot", "scheduler event", "other"]);
});

test("selecting a record fills the inspector and follows its links (ART-079 item 8)", () => {
  const { root, api, live, inspector } = mount(richData());
  assert.equal(inspector().textContent, "InspectorSelect a record to see its fields, its cause and its effects.");
  api.select(9);
  assert.equal(byTag(inspector(), "h3")[0].textContent, "net.send");
  assert.equal(live(), 'Selected seq 9, 0.001000000s, n1#1, net.send: send #1 n1 -> n2: "ping"');
  assert.equal(byClass(root, "fl-counter")[0].textContent, "#9");
  assert.deepEqual(texts(byTag(byTag(inspector(), "table")[0], "td")), ["9", "0.001000000s (1000000 ns)", "n1#1", "#8 kernel.event"]);
  assert.deepEqual(texts(byTag(byTag(inspector(), "table")[1], "th")), ["msg", "from", "to", "payload"]);
  assert.equal(byText(inspector(), "span", "In causal slice") !== undefined, true);
  const effect = byText(inspector(), "button", "#10");
  assert.equal(effect.getAttribute("aria-label"), "Select record 10, kernel.event");
  byText(inspector(), "button", "#8").click();
  assert.equal(byTag(inspector(), "h3")[0].textContent, "kernel.event");
  findAll(inspector(), (e) => e.getAttribute("aria-label") === "Clear the selection")[0].click();
  assert.equal(byClass(root, "fl-counter").length, 0);
});

test("the Inspector keeps keyboard focus when a link or Clear rebuilds it (UI-175)", () => {
  const { doc, api, inspector } = mount(richData());
  api.select(9);
  byText(inspector(), "button", "#8").click();
  const heading = byTag(inspector(), "h3")[0];
  assert.ok(doc.activeElement === heading, "focus is on the kind heading, not " + String(doc.activeElement && doc.activeElement.tagName));
  assert.equal(heading.textContent, "kernel.event");
  findAll(inspector(), (e) => e.getAttribute("aria-label") === "Clear the selection")[0].click();
  const hint = byClass(inspector(), "fl-hint")[0];
  assert.ok(doc.activeElement === hint, "focus is on the hint, not " + String(doc.activeElement && doc.activeElement.tagName));
  assert.equal(hint.getAttribute("tabindex"), "-1");
});

test("a link centers its record; the Inspector caps effects and notes records outside the view (ART-079 item 8)", () => {
  const { api, key, valuetext, inspector } = mount(faultData());
  api.select(3);
  for (let i = 0; i < 8; i++) {
    key("+");
  }
  assert.equal(valuetext(), "Showing 1.497s to 3.242s", "zoomed around the selection at 2 s");
  byText(inspector(), "button", "#2").click();
  assert.equal(valuetext(), "Showing 0.128s to 1.872s", "record 2 at 1 s is centered");
  assert.equal(byText(inspector(), "span", "In causal slice") === undefined, true, "faultData has no slice");

  const line = (o) => JSON.stringify(o);
  const trace = [line({ faultline_trace: 1, records: 15, dropped: 0, nodes: [{ id: 1, name: "n1", tags: [] }] })];
  trace.push(line({ seq: 2, at: 0, node: 1, inc: 1, kind: "app.start", cause: 1, text: "start" }));
  for (let s = 3; s <= 16; s++) {
    trace.push(line({ seq: s, at: s * 1000, node: 1, inc: 1, kind: "app.step", cause: 2, text: "step" }));
  }
  const data = { report: { status: "pass", run: { end_ns: 16000 } }, trace: trace.join("\n") + "\n", slice: { root: 0, seqs: [2], cap: 200, truncated: true }, window: null, total: 16 };
  const cut = mount(data);
  cut.api.select(2);
  assert.equal(byText(cut.inspector(), "span", "#1 (not in this view)").getAttribute("class"), "fl-aside");
  assert.equal(byTag(cut.inspector(), "h4").at(-1).textContent, "Effects 14");
  assert.equal(findAll(cut.inspector(), (e) => e.getAttribute("class") === "fl-link").length, 12);
  assert.ok(byText(cut.inspector(), "span", "and 2 more"));
  assert.ok(byText(cut.inspector(), "span", "In causal slice"));
  cut.api.select(3);
  assert.equal(byText(cut.inspector(), "span", "In causal slice") === undefined, true, "record 3 is not in the slice");
});

test("keys zoom, pan, fit and jump to the failure (ART-079 item 9, UI-106)", () => {
  const faults = mount(faultData());
  assert.equal(faults.valuetext(), "Showing 0.000s to 10.000s");
  faults.key("+");
  assert.equal(faults.valuetext(), "Showing 0.840s to 9.160s");
  faults.key("ArrowRight");
  assert.equal(faults.valuetext(), "Showing 1.672s to 9.992s");
  faults.key("0");
  assert.equal(faults.valuetext(), "Showing 0.000s to 10.000s");
  assert.equal(faults.key("x").defaultPrevented, false);
  faults.key("=");
  assert.equal(faults.valuetext(), "Showing 0.840s to 9.160s", "= zooms like +");
  faults.key("0");
  for (const mods of [{ ctrlKey: true }, { metaKey: true }, { altKey: true }]) {
    assert.equal(faults.key("+", mods).defaultPrevented, false, JSON.stringify(mods));
  }
  assert.equal(faults.valuetext(), "Showing 0.000s to 10.000s", "browser zoom keys are left alone");
  faults.api.select(3);
  faults.key("+");
  assert.equal(faults.valuetext(), "Showing 0.000s to 7.920s", "zoom keeps the selection at 2 s in place");
  faults.key("f");
  assert.equal(faults.live(), "No failure recorded");
  assert.ok(byText(faults.root, "button", "Jump to failure").hasAttribute("disabled"));

  const fail = mount(richData());
  fail.key("F");
  assert.equal(byTag(fail.inspector(), "h3")[0].textContent, "check.violation");
  assert.deepEqual(texts(byClass(fail.inspector(), "fl-pill")), ["Failure", "In causal slice"]);
  byText(fail.root, "button", "Sequence").click();
  assert.equal(fail.valuetext(), "Showing #1 to #12");
});

test("one wheel notch zooms by 1.25 in pixel, line and page mode (ART-079 item 9)", () => {
  assert.deepEqual([wheelNotches(100, 0), wheelNotches(3, 1), wheelNotches(1, 2)], [1, 1, 1]);
  const { canvas, key, valuetext } = mount(faultData());
  for (const [deltaY, deltaMode] of [[-100, 0], [-3, 1], [-1, 2]]) {
    key("0");
    canvas.dispatch("wheel", { deltaY, deltaMode, offsetX: 568 });
    assert.equal(valuetext(), "Showing 0.840s to 9.160s", "deltaMode " + deltaMode);
  }
});

test("Shift with the wheel scrolls the lanes; the well's scroll moves them (ART-079 items 2 and 9)", () => {
  const { root, canvas, valuetext } = mount(laneData(30));
  const wrap = byClass(root, "fl-wrap")[0];
  const before = valuetext();
  canvas.dispatch("wheel", { deltaY: 100, deltaMode: 0, shiftKey: true, offsetX: 568 });
  assert.equal(wrap.scrollTop, 100);
  canvas.dispatch("wheel", { deltaY: 3, deltaMode: 1, shiftKey: true, offsetX: 568 });
  assert.equal(wrap.scrollTop, 148, "a line is 16 px");
  canvas.dispatch("wheel", { deltaY: 0, deltaX: 20, deltaMode: 0, shiftKey: true, offsetX: 568 });
  assert.equal(wrap.scrollTop, 168, "some browsers report Shift+wheel as deltaX");
  assert.equal(valuetext(), before, "no zoom");
  const laneLabel = () => canvas.context.ops.filter((o) => o.op === "fillText" && o.text === "n1").at(-1).at[1];
  wrap.scrollTop = 0;
  wrap.dispatch("scroll", {});
  const top = laneLabel();
  wrap.scrollTop = 40;
  wrap.dispatch("scroll", {});
  assert.equal(laneLabel(), top - 40);
});

test("selecting a record scrolls its lane into view (ART-079 item 2)", () => {
  const { root, api } = mount(laneData(30));
  const wrap = byClass(root, "fl-wrap")[0];
  api.select(30);
  assert.equal(wrap.scrollTop, 30 * 32 + 32 - 370, "lane 30's bottom meets the well's bottom");
  api.select(1);
  assert.equal(wrap.scrollTop, 32, "lane 1's top meets the axis");
});

test("a click selects, a drag pans, and a cancelled drag stops (ART-079 items 8 and 9)", () => {
  const { canvas, key, valuetext, inspector } = mount(faultData());
  const press = (type, x, y) => canvas.dispatch(type, { offsetX: x, offsetY: y, button: 0, pointerId: 1 });
  // record 7, net.send on n1 at 5 s: the middle of the fitted axis and of lane 1
  press("pointerdown", 568, 210);
  press("pointerup", 568, 210);
  assert.equal(byTag(inspector(), "h3")[0].textContent, "net.send");
  press("pointerdown", 568, 15);
  press("pointerup", 568, 15);
  assert.equal(byTag(inspector(), "h3").length, 0, "a click on the axis selects nothing");
  key("+");
  press("pointerdown", 600, 210);
  press("pointermove", 500, 210);
  assert.equal(valuetext(), "Showing 1.803s to 10.000s", "dragging left shows later times");
  press("pointerup", 500, 210);
  assert.equal(byTag(inspector(), "h3").length, 0, "a drag selects nothing");
  for (const type of ["pointercancel", "lostpointercapture"]) {
    key("0");
    press("pointerdown", 600, 210);
    canvas.dispatch(type, { pointerId: 1 });
    press("pointermove", 500, 210);
    assert.equal(valuetext(), "Showing 0.000s to 10.000s", type);
  }
});

test("the overview track pans and shows the view in its own color (ART-079 item 11)", () => {
  const { root, valuetext } = mount(faultData());
  const overview = byClass(root, "fl-overview")[0];
  overview.value = "800";
  overview.dispatch("input", {});
  assert.equal(valuetext(), "Showing 0.600s to 10.000s");
  assert.ok(overview.style["--fl-ov"].includes("var(--fl-ov-view)"), overview.style["--fl-ov"]);
});

test("the canvas follows its well when the rows around it change height (ART-079 item 13)", () => {
  const { doc, root, api, canvas } = mount(faultData());
  const wrap = byClass(root, "fl-wrap")[0];
  assert.equal(canvas.style.height, "400px");
  wrap.clientHeight = 360;
  doc.resized();
  assert.equal(canvas.style.height, "360px");
  assert.equal(canvas.height, 360);
  const ops = canvas.context.ops.length;
  doc.resized();
  assert.equal(canvas.context.ops.length, ops, "an unchanged size is not redrawn");
  assert.equal(doc.defaultView.listeners.filter((l) => l.type === "resize").length, 1);
  api.destroy();
  assert.deepEqual([doc.observers.length, doc.defaultView.listeners.length], [0, 0]);
});

test("the canvas paints with the theme's tokens", () => {
  const { canvas } = mount(faultData(), { style: { "--fl-bg": " #010203 " } });
  assert.equal(canvas.context.ops.find((o) => o.op === "fillRect").fill, "#010203");
  assert.ok(canvas.context.ops.some((o) => o.fill === "#888"), "tokens the style lacks fall back to #888");
});

test("kinds named like Object.prototype members are drawn as other records (KRN-090)", () => {
  const line = (o) => JSON.stringify(o);
  const trace = [
    line({ faultline_trace: 1, records: 3, dropped: 0, nodes: [{ id: 1, name: "n1", tags: [] }] }),
    line({ seq: 1, at: 0, node: 1, inc: 1, kind: "constructor", text: "a" }),
    line({ seq: 2, at: 1000, node: 1, inc: 1, kind: "toString", text: "b" }),
    line({ seq: 3, at: 2000, node: 1, inc: 1, kind: "hasOwnProperty.x", text: "c" }),
  ].join("\n") + "\n";
  const { root, api, inspector } = mount({ report: { status: "pass", run: { end_ns: 2000 } }, trace, slice: null, window: null, total: 3 });
  assert.deepEqual(texts(byClass(byClass(root, "fl-kinds")[0], "fl-chip")), ["constructor1", "hasOwnProperty1", "toString1"]);
  assert.deepEqual(texts(byTag(byClass(root, "fl-legend")[0], "li")), ["other"]);
  api.select(2);
  assert.equal(byTag(inspector(), "h3")[0].textContent, "toString");
});

test("the stage fills the window below the title, at least 520 px (ART-079 item 13)", () => {
  const stageHeight = (innerHeight) => byClass(mount(faultData(), { innerHeight }).root, "fl-stage")[0].style["--fl-stage-h"];
  assert.equal(stageHeight(900), "884px");
  assert.equal(stageHeight(300), "520px");
});

test("faults panel rows select their record", () => {
  const { root, inspector } = mount(faultData());
  const rows = byClass(root, "fl-row").filter((e) => e.tagName === "BUTTON");
  assert.deepEqual(rows.map((r) => r.getAttribute("aria-label")), ["crash n1, 1.000s to 2.000s", "partition n1|n2, 3.000s to 4.000s"]);
  rows[1].click();
  assert.equal(byTag(inspector(), "h3")[0].textContent, "fault.partition");
  assert.deepEqual(rows.map((r) => r.getAttribute("aria-current")), ["false", "true"]);
});

test("theme button, Copy and the window banner", async () => {
  const writes = [];
  const clipboard = { writeText: (t) => {
    writes.push(t);
    return Promise.resolve();
  } };
  const data = richData();
  data.window = { from_seq: 5, from_ns: 0, extra: 4 };
  const { root, api, host, themes, live } = mount(data, { clipboard });
  assert.equal(byClass(root, "fl-banner")[0].textContent, "Showing 12 of 12 records: every record from t=0.000000000s (#5) to the end, plus 4 earlier fault, check, node-lifecycle and causal-slice records. The complete trace is in trace.jsonl and timeline.txt.");
  const theme = findAll(root, (e) => e.getAttribute("aria-keyshortcuts") === "t")[0];
  assert.equal(theme.getAttribute("aria-label"), "Theme: system. Change theme");
  theme.click();
  assert.deepEqual(themes, ["cycle"]);
  host.current = "dark";
  api.repaint();
  assert.equal(theme.getAttribute("aria-label"), "Theme: dark. Change theme");
  const copy = findAll(root, (e) => e.getAttribute("aria-label") === "Copy replay command")[0];
  copy.click();
  await Promise.resolve();
  assert.deepEqual(writes, ["FAULTLINE_SEED=0x0000000000000001 go test -run '^TestToy$' ./toy"]);
  assert.equal(copy.textContent, "Copied");
  assert.equal(live(), "Copied replay command");
  copy.dispatch("pointerleave", {});
  assert.equal(copy.textContent, "Copy");
  copy.click();
  await Promise.resolve();
  assert.equal(copy.textContent, "Copied");
  assert.equal(live(), "Copied replay command\u00a0", "a repeat differs, so it is read again");
  copy.dispatch("blur", {});
  assert.equal(copy.textContent, "Copy");
});

test("without a clipboard, or when it refuses, Copy selects the command and names the keys", async () => {
  for (const clipboard of [undefined, { writeText: () => Promise.reject(new Error("denied")) }]) {
    const { doc, root, live } = mount(richData(), { clipboard });
    findAll(root, (e) => e.getAttribute("aria-label") === "Copy replay command")[0].click();
    await new Promise((resolve) => setImmediate(resolve));
    assert.equal(doc.selection.length, 1);
    assert.equal(doc.selection[0].node.textContent, "FAULTLINE_SEED=0x0000000000000001 go test -run '^TestToy$' ./toy");
    assert.equal(live(), "Selected. Press Ctrl+C or Command+C to copy");
  }
});

test("slice members missing from the trace are ignored, not an error (ART-079 item 10)", () => {
  const cut = richData();
  cut.trace = cut.trace.split("\n").filter((l) => !l.startsWith('{"seq":8,')).join("\n");
  const { root } = mount(cut);
  assert.equal(byClass(root, "fl-error").length, 0);
  const toggle = byClass(root, "fl-toggle")[0];
  assert.deepEqual([toggle.getAttribute("aria-pressed"), byClass(toggle, "fl-count")[0].textContent], ["true", "11"]);
  const one = richData();
  one.slice = { root: 12, seqs: [12, 99], cap: 200, truncated: false };
  const single = byClass(mount(one).root, "fl-toggle")[0];
  assert.deepEqual([single.getAttribute("aria-pressed"), byClass(single, "fl-count")[0].textContent], ["true", "1"], "one member left is a slice");
  const capped = richData();
  capped.slice = { root: 12, seqs: [12, 11, 99], cap: 3, truncated: true };
  const cap = byClass(mount(capped).root, "fl-toggle")[0];
  assert.deepEqual([cap.getAttribute("aria-pressed"), cap.getAttribute("title"), byClass(cap, "fl-count")[0].textContent], ["true", "The slice stops at its cap of 3 records", "2+"]);
  const gone = richData();
  gone.slice = { root: 99, seqs: [99], cap: 200, truncated: false };
  const none = byClass(mount(gone).root, "fl-toggle")[0];
  assert.deepEqual([none.getAttribute("aria-pressed"), none.hasAttribute("disabled"), none.getAttribute("title")], ["false", true, "This run has no causal slice"]);
  assert.equal(byClass(none, "fl-count").length, 0, "no member left shows no count");
});

test("an empty trace shows the empty state", () => {
  const data = faultData();
  data.trace = "";
  data.total = 0;
  const { root } = mount(data);
  assert.equal(byTag(root, "canvas").length, 0);
  assert.equal(byTag(root, "h3")[0].textContent, "No records to show");
  assert.equal(byClass(root, "fl-narr")[0].textContent, "No records, so there is nothing to select.");
  assert.ok(byText(root, "button", "Sequence").hasAttribute("disabled"));
});

test("mountError shows the report and the render command", () => {
  const doc = new FakeDocument();
  const root = doc.createElement("div");
  mountError(root, richData(), new SyntaxError("Unexpected end of JSON input"), null);
  assert.equal(findAll(root, (e) => e.getAttribute("role") === "alert").length, 1);
  assert.equal(byTag(root, "h3")[0].textContent, "This page could not read its run data");
  assert.equal(byTag(byClass(root, "fl-error")[0], "pre")[0].textContent, "SyntaxError: Unexpected end of JSON input");
  assert.equal(byTag(byClass(root, "fl-error")[0], "code")[0].textContent, "faultline render /tmp/faultline/example.com_toy/TestToy/0000000000000001");
  assert.equal(byTag(root, "h1")[0].textContent, "TestToy");
  assert.equal(findAll(root, (e) => e.tagName === "SECTION" && e.getAttribute("aria-label") === "Failure").length, 1);
  const bare = doc.createElement("div");
  mountError(bare, null, new SyntaxError("x"), null);
  assert.equal(byTag(bare, "h1").length, 0);
  assert.equal(byTag(byClass(bare, "fl-error")[0], "code")[0].textContent, "faultline render .");
});

test("an edited report cannot stop the error card", () => {
  const doc = new FakeDocument();
  const data = richData();
  data.report.subtest = 5;
  const root = doc.createElement("div");
  mountError(root, data, new SyntaxError("x"), null);
  assert.equal(byTag(root, "h1")[0].textContent, "TestToy");
  assert.equal(byTag(byTag(root, "nav")[0], "li")[2].textContent, "5");
  const broken = richData();
  Object.defineProperty(broken.report, "run", { enumerable: true, get() {
    throw new TypeError("run");
  } });
  const bare = doc.createElement("div");
  mountError(bare, broken, new SyntaxError("x"), null);
  assert.equal(byTag(bare, "h1").length, 0, "built again without the report");
  assert.equal(findAll(bare, (e) => e.getAttribute("role") === "alert").length, 1);
});

test("a draw error leaves no listeners behind", () => {
  const doc = new FakeDocument();
  const create = doc.createElement.bind(doc);
  doc.createElement = (tag) => {
    const el = create(tag);
    if (tag === "canvas") {
      el.getContext = () => Object.assign(new RecordingContext(), { fillRect() {
        throw new Error("context lost");
      } });
    }
    return el;
  };
  assert.throws(() => mountTimeline(doc.createElement("div"), faultData(), null), /context lost/);
  assert.deepEqual([doc.defaultView.listeners.length, doc.observers.length], [0, 0]);
});

// boot runs timeline-main.js on doc; the query string gives each run its own module instance.
async function boot(doc, dataText, run) {
  const app = doc.createElement("div");
  app.setAttribute("id", "app");
  const script = doc.createElement("script");
  script.setAttribute("id", "faultline-data");
  script.textContent = dataText;
  doc.body.append(app, script);
  globalThis.document = doc;
  globalThis.window = doc.defaultView;
  await import("../static/js/timeline-main.js?" + run);
  return app;
}

test("timeline-main.js shows load errors and owns the theme (UI-178)", async () => {
  try {
    const doc = new FakeDocument();
    const app = await boot(doc, "{", "broken");
    assert.equal(byTag(app, "h3")[0].textContent, "This page could not read its run data");
    assert.equal(doc.documentElement.hasAttribute("data-theme"), false);
    doc.dispatch("keydown", { key: "t" });
    assert.equal(doc.documentElement.getAttribute("data-theme"), "light");
    assert.equal(doc.defaultView.localStorage.getItem("faultline.theme"), "light");
    doc.dispatch("keydown", { key: "t", metaKey: true });
    doc.dispatch("keydown", { key: "t" });
    assert.equal(doc.documentElement.getAttribute("data-theme"), "dark");
    doc.dispatch("keydown", { key: "t" });
    assert.equal(doc.documentElement.hasAttribute("data-theme"), false);
    doc.dispatch("keydown", { key: "t", defaultPrevented: true });
    assert.equal(doc.documentElement.hasAttribute("data-theme"), false, "a key the page handled is left alone");
    doc.dispatch("keydown", { key: "T" });
    assert.equal(doc.documentElement.getAttribute("data-theme"), "light", "Caps Lock");

    const next = new FakeDocument();
    next.defaultView.localStorage.setItem("faultline.theme", "dark");
    const page = await boot(next, JSON.stringify(richData()), "stored");
    assert.equal(next.documentElement.getAttribute("data-theme"), "dark");
    assert.equal(byTag(page, "canvas").length, 1);
    const theme = findAll(page, (e) => e.getAttribute("aria-keyshortcuts") === "t")[0];
    assert.equal(theme.getAttribute("aria-label"), "Theme: dark. Change theme");
    next.documentElement.removeAttribute("data-theme");
    next.mediaChanged();
    assert.equal(theme.getAttribute("aria-label"), "Theme: system. Change theme", "a color-scheme change repaints");

    const cut = new FakeDocument();
    const data = richData();
    data.trace = "{";
    const kept = await boot(cut, JSON.stringify(data), "cut");
    assert.equal(byTag(kept, "h3")[0].textContent, "This page could not read its run data");
    assert.equal(byTag(kept, "h1")[0].textContent, "TestToy", "a trace error keeps the report");
  } finally {
    delete globalThis.document;
    delete globalThis.window;
  }
});
