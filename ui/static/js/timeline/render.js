// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

import { tickStep, tickDecimals, tickLabel, formatTime } from "./format.js";
import { firstAtOrAfter } from "./model.js";

// Canvas drawing of the run view (ART-079): lanes, the UI-088 glyphs, message arrows, drops,
// bands, the failure line and the causal slice. Geometry and glyph helpers are pure so node can
// test them; draw only touches the 2D context it is given.

export const AXIS_HEIGHT = 30;
export const GUTTER = 136;
export const GUTTER_NARROW = 88;
export const NARROW_WIDTH = 600;
export const LANE_MIN = 32;
export const LANE_MAX = 120;
export const HIT_RADIUS = 6;
export const TICK_MIN_PX = 80;
export const MIN_SPAN_NS = 1000;
export const MIN_SPAN_SEQ = 20;
export const MAX_SPAN_FACTOR = 1.2;
export const PAN_MARGIN = 0.1;
export const FIT_PAD = 0.02;
export const SLICE_DIM = 0.3;
export const BRIDGE_MAX = 1000;

// PALETTE names the theme.css tokens (--fl-<name>) that draw reads; ui/theme_test.go checks that
// both themes define each one.
export const PALETTE = ["bg", "lane-alt", "border", "fg", "muted", "send", "deliver", "drop", "dup", "fault", "violation", "node", "paused", "down", "disk", "assert", "history", "event", "other", "arrow", "select"];

// GLYPHS are UI-088's record categories in table order: shape, size in px, stroke width (0 means
// filled), color token and legend label.
export const GLYPHS = {
  violation: { shape: "star5", size: 13, stroke: 0, color: "violation", label: "failure" },
  drop: { shape: "cross", size: 9, stroke: 2, color: "drop", label: "drop" },
  send: { shape: "circle", size: 7, stroke: 0, color: "send", label: "send" },
  deliver: { shape: "square", size: 7, stroke: 0, color: "deliver", label: "deliver" },
  dup: { shape: "ring", size: 9, stroke: 1.5, color: "dup", label: "duplicate" },
  fault: { shape: "diamond", size: 9, stroke: 0, color: "fault", label: "fault" },
  crash: { shape: "tri-down", size: 9, stroke: 0, color: "node", label: "crash" },
  boot: { shape: "tri-up", size: 9, stroke: 0, color: "node", label: "boot" },
  pause: { shape: "bars2", size: 9, stroke: 0, color: "paused", label: "pause" },
  resume: { shape: "tri-right", size: 9, stroke: 0, color: "paused", label: "resume" },
  check: { shape: "square-ring", size: 9, stroke: 1.5, color: "violation", label: "check" },
  assert: { shape: "diamond-ring", size: 9, stroke: 1.5, color: "assert", label: "assert" },
  disk: { shape: "bar-h", size: 8, stroke: 0, color: "disk", label: "disk" },
  history: { shape: "plus", size: 9, stroke: 2, color: "history", label: "history" },
  event: { shape: "dot", size: 4, stroke: 0, color: "event", label: "scheduler event" },
  other: { shape: "tick", size: 10, stroke: 1.5, color: "other", label: "other" },
};

// DRAW_ORDER paints message traffic first, then checks, node lifecycle and faults, and the
// failure last, so a busy lane never hides the marks a reader looks for (UI-096).
export const DRAW_ORDER = ["other", "event", "history", "disk", "dup", "deliver", "send", "drop", "assert", "check", "resume", "pause", "boot", "crash", "fault", "violation"];

const TOPOLOGY = ["net.partition", "net.isolate", "net.cut", "net.heal", "net.heal_link", "net.link_config", "net.link_reset"];
const LIFECYCLE = { "kernel.crash": "crash", "kernel.boot": "boot", "kernel.pause": "pause", "kernel.resume": "resume" };
const PREFIXES = [["fault.", "fault"], ["check.", "check"], ["assert.", "assert"], ["disk.", "disk"], ["history.", "history"]];

// category returns the UI-088 category of record r; root is the failure record's seq.
export function category(r, root) {
  const k = r.kind;
  if (r.seq === root || k === "check.violation") {
    return "violation";
  }
  if (k === "net.drop") {
    return "drop";
  }
  if (k === "net.send" || k === "net.send_raw") {
    return "send";
  }
  if (k === "net.deliver") {
    return "deliver";
  }
  if (k === "net.dup") {
    return "dup";
  }
  if (k.startsWith("fault.") || TOPOLOGY.includes(k)) {
    return "fault";
  }
  // Kinds come from the trace: "constructor" must not find Object.prototype's.
  if (Object.hasOwn(LIFECYCLE, k)) {
    return LIFECYCLE[k];
  }
  for (const [prefix, cat] of PREFIXES) {
    if (k.startsWith(prefix)) {
      return cat;
    }
  }
  return k === "kernel.event" ? "event" : "other";
}

// glyphOps returns the outline of a glyph centered on (cx, cy) as steps: ["M", x, y] and
// ["L", x, y] move and draw, ["Z"] closes, ["O", x, y, r] is a full circle (UI-088's glyphPath).
export function glyphOps(shape, cx, cy, size) {
  const h = size / 2;
  const x0 = cx - h;
  const x1 = cx + h;
  const y0 = cy - h;
  const y1 = cy + h;
  switch (shape) {
    case "star5": {
      const ops = [];
      for (let i = 0; i < 10; i++) {
        const r = i % 2 === 1 ? size / 5 : h;
        const a = -Math.PI / 2 + (i * Math.PI) / 5;
        ops.push([i === 0 ? "M" : "L", cx + r * Math.cos(a), cy + r * Math.sin(a)]);
      }
      ops.push(["Z"]);
      return ops;
    }
    case "cross":
      return [["M", x0, y0], ["L", x1, y1], ["M", x1, y0], ["L", x0, y1]];
    case "circle":
    case "ring":
    case "dot":
      return [["O", cx, cy, h]];
    case "square":
    case "square-ring":
      return [["M", x0, y0], ["L", x1, y0], ["L", x1, y1], ["L", x0, y1], ["Z"]];
    case "diamond":
    case "diamond-ring":
      return [["M", cx, y0], ["L", x1, cy], ["L", cx, y1], ["L", x0, cy], ["Z"]];
    case "tri-down":
      return [["M", x0, y0], ["L", x1, y0], ["L", cx, y1], ["Z"]];
    case "tri-up":
      return [["M", x0, y1], ["L", x1, y1], ["L", cx, y0], ["Z"]];
    case "tri-right":
      return [["M", x0, y0], ["L", x1, cy], ["L", x0, y1], ["Z"]];
    case "bars2": {
      const w = size / 3;
      return [["M", x0, y0], ["L", x0 + w, y0], ["L", x0 + w, y1], ["L", x0, y1], ["Z"],
        ["M", x1 - w, y0], ["L", x1, y0], ["L", x1, y1], ["L", x1 - w, y1], ["Z"]];
    }
    case "bar-h":
      return [["M", x0, cy - size / 4], ["L", x1, cy - size / 4], ["L", x1, cy + size / 4], ["L", x0, cy + size / 4], ["Z"]];
    case "plus":
      return [["M", cx, y0], ["L", cx, y1], ["M", x0, cy], ["L", x1, cy]];
    default:
      return [["M", cx, y0], ["L", cx, y1]];
  }
}

// glyphD returns glyphOps as an SVG path, for the legend and the kind chips.
export function glyphD(shape, cx, cy, size) {
  const n = (v) => String(Math.round(v * 100) / 100);
  let d = "";
  for (const op of glyphOps(shape, cx, cy, size)) {
    if (op[0] === "O") {
      const r = n(op[3]);
      d += "M" + n(op[1] - op[3]) + " " + n(op[2]) + "a" + r + " " + r + " 0 1 0 " + n(2 * op[3]) + " 0a" + r + " " + r + " 0 1 0 " + n(-2 * op[3]) + " 0Z";
    } else if (op[0] === "Z") {
      d += "Z";
    } else {
      d += op[0] + n(op[1]) + " " + n(op[2]);
    }
  }
  return d;
}

// addGlyph adds the outline of a glyph to the current path, so a category draws as one path.
// Only stroked glyphs close their outlines: fill closes them anyway, and closePath on a path of
// thousands of glyphs costs Chrome hundreds of milliseconds.
function addGlyph(ctx, shape, cx, cy, size, closed) {
  for (const op of glyphOps(shape, cx, cy, size)) {
    if (op[0] === "M") {
      ctx.moveTo(op[1], op[2]);
    } else if (op[0] === "L") {
      ctx.lineTo(op[1], op[2]);
    } else if (op[0] === "Z") {
      if (closed) {
        ctx.closePath();
      }
    } else {
      ctx.moveTo(op[1] + op[3], op[2]);
      ctx.arc(op[1], op[2], op[3], 0, 2 * Math.PI);
    }
  }
}

// HIDDEN_KINDS are hidden unless the scheduler events chip is on (ART-079 item 4).
const HIDDEN_KINDS = ["kernel.event", "kernel.defer"];

// namespace returns the text of a kind before its first dot.
export function namespace(kind) {
  const i = kind.indexOf(".");
  return i < 0 ? kind : kind.slice(0, i);
}

// isVisible applies the kind filters of ART-079 item 4.
export function isVisible(r, filters) {
  if (!filters.showEvents && HIDDEN_KINDS.includes(r.kind)) {
    return false;
  }
  return !filters.hiddenNamespaces.has(namespace(r.kind));
}

// axisValue is the x-axis value of record i: its time, or i in sequence mode (ART-079 item 3).
export function axisValue(model, view, i) {
  return view.mode === "seq" ? i : model.records[i].at;
}

// axisExtent returns the [first, last] axis values of the run in mode "time" or "seq".
export function axisExtent(model, mode) {
  if (mode === "seq") {
    return [0, Math.max(1, model.records.length - 1)];
  }
  return [0, Math.max(1, model.end)];
}

// toX maps an axis value to a canvas x coordinate; fromX is its inverse.
export function toX(view, v) {
  return view.gutter + ((v - view.from) / (view.to - view.from)) * (view.width - view.gutter);
}

export function fromX(view, x) {
  return view.from + ((x - view.gutter) / (view.width - view.gutter)) * (view.to - view.from);
}

// timeValue maps a time to an axis value; sequence mode places it at the first record at or after
// it (UI-087). timeX maps it on to x.
export function timeValue(model, mode, at) {
  return mode === "seq" ? firstAtOrAfter(model, at) : at;
}

export function timeX(model, view, at) {
  return toX(view, timeValue(model, view.mode, at));
}

// gutterWidth is the width of the lane-label gutter on a canvas width px wide (ART-079 item 13).
export function gutterWidth(width) {
  return width < NARROW_WIDTH ? GUTTER_NARROW : GUTTER;
}

// laneHeight shares the canvas height among the lanes, LANE_MIN to LANE_MAX px each; lanes that
// do not fit scroll.
export function laneHeight(height, lanes) {
  const h = Math.floor((height - AXIS_HEIGHT) / Math.max(1, lanes));
  return Math.max(LANE_MIN, Math.min(LANE_MAX, h));
}

// laneTop returns the top of lane index li after vertical scrolling; laneY its middle.
export function laneTop(view, li) {
  return AXIS_HEIGHT + li * view.laneH - view.scrollY;
}

export function laneY(view, li) {
  return laneTop(view, li) + view.laneH / 2;
}

// laneIndex maps node IDs to lane indexes. A model's lanes never change, so the map is built once
// per model.
const laneMaps = new WeakMap();
export function laneIndex(model) {
  let m = laneMaps.get(model);
  if (m === undefined) {
    m = new Map();
    model.lanes.forEach((l, i) => m.set(l.id, i));
    laneMaps.set(model, m);
  }
  return m;
}

// firstIndex returns the first record index whose axis value can be v or more. Records are in
// time order (firstAtOrAfter relies on it too), so a loop that starts there and stops at the first
// axis value past the view's end sees every record in range, and only the records near the view.
function firstIndex(model, view, v) {
  return view.mode === "seq" ? Math.max(0, Math.floor(v)) : firstAtOrAfter(model, v);
}

// clampSpan keeps span between the zoom limits of UI-101 in mode "time" or "seq". The smallest span
// never exceeds the fit span, so a short run still fits.
export function clampSpan(model, mode, span) {
  const [e0, e1] = axisExtent(model, mode);
  const ext = e1 - e0;
  const minSpan = Math.min(mode === "seq" ? MIN_SPAN_SEQ : MIN_SPAN_NS, ext * (1 + 2 * FIT_PAD));
  return Math.min(MAX_SPAN_FACTOR * ext, Math.max(minSpan, span));
}

// clampView keeps the span between the zoom limits and the view within PAN_MARGIN of the run
// (UI-101).
export function clampView(model, view) {
  const [e0, e1] = axisExtent(model, view.mode);
  const ext = e1 - e0;
  const span = clampSpan(model, view.mode, view.to - view.from);
  const lo = e0 - PAN_MARGIN * ext;
  const hi = e1 + PAN_MARGIN * ext;
  const from = Math.max(lo, Math.min(view.from, hi - span));
  return { ...view, from, to: from + span };
}

// fitView shows the whole run with FIT_PAD on each side (key 0, UI-101).
export function fitView(model, view) {
  const [e0, e1] = axisExtent(model, view.mode);
  const pad = (e1 - e0) * FIT_PAD;
  return clampView(model, { ...view, from: e0 - pad, to: e1 + pad });
}

// zoomView zooms by factor around canvas x, keeping the value under x in place.
export function zoomView(model, view, x, factor) {
  const center = fromX(view, x);
  const ratio = (x - view.gutter) / (view.width - view.gutter);
  const span = (view.to - view.from) / factor;
  const from = center - ratio * span;
  return clampView(model, { ...view, from, to: from + span });
}

// panView moves the view by dx canvas pixels.
export function panView(model, view, dx) {
  const dv = (dx / (view.width - view.gutter)) * (view.to - view.from);
  return clampView(model, { ...view, from: view.from - dv, to: view.to - dv });
}

// centerOn centers the view on axis value v without changing the zoom.
export function centerOn(model, view, v) {
  const half = (view.to - view.from) / 2;
  return clampView(model, { ...view, from: v - half, to: v + half });
}

// failureView frames the failure for Jump to failure (UI-106): the earliest causal-slice member
// to the failure, which sits at 95 % of the span. Without an earlier member the failure is
// centered in a tenth of the run. The span is limited first and the failure placed after, so the
// limits never move it off 95 % or the center; the pan margin does not apply to the jump. It
// returns null when the model has no failure record.
export function failureView(model, view) {
  const i = model.bySeq.get(model.root);
  if (model.root === 0 || i === undefined) {
    return null;
  }
  const v = axisValue(model, view, i);
  let first = v;
  for (const seq of model.slice) {
    const j = model.bySeq.get(seq);
    if (j !== undefined) {
      first = Math.min(first, axisValue(model, view, j));
    }
  }
  if (first === v) {
    const [e0, e1] = axisExtent(model, view.mode);
    const span = clampSpan(model, view.mode, 0.1 * (e1 - e0));
    return { ...view, from: v - span / 2, to: v + span / 2 };
  }
  const span = clampSpan(model, view.mode, (v - first) * 1.1);
  return { ...view, from: v - 0.95 * span, to: v + 0.05 * span };
}

// visibleIndexes returns the indexes of visible records inside the view, in record order.
export function visibleIndexes(model, view, filters) {
  const out = [];
  for (let i = firstIndex(model, view, view.from); i < model.records.length; i++) {
    const v = axisValue(model, view, i);
    if (v > view.to) {
      break;
    }
    if (v < view.from) {
      continue;
    }
    if (isVisible(model.records[i], filters)) {
      out.push(i);
    }
  }
  return out;
}

// hitTest returns the index of the visible record nearest to (x, y) within HIT_RADIUS, or -1. The
// axis and the gutter cover lanes scrolled or panned under them, so they select nothing.
export function hitTest(model, view, filters, x, y) {
  if (y < AXIS_HEIGHT || x < view.gutter) {
    return -1;
  }
  const lanes = laneIndex(model);
  let best = -1;
  let bestD = HIT_RADIUS * HIT_RADIUS;
  for (const i of visibleIndexes(model, view, filters)) {
    const r = model.records[i];
    const dx = toX(view, axisValue(model, view, i)) - x;
    const dy = laneY(view, lanes.get(r.node) || 0) - y;
    const d = dx * dx + dy * dy;
    if (d <= bestD) {
      best = i;
      bestD = d;
    }
  }
  return best;
}

// sliceEdges returns the happens-before edges among causal-slice members: the cause edge and the
// program-order edge (the previous record of the same node incarnation) when it differs (ART-050).
// A slice seq that is not a record (edited data; ART-060 includes every member) gets no edge, as
// ART-050 ignores missing records. The program-order predecessor is always a record.
export function sliceEdges(model) {
  const edges = [];
  const last = new Map();
  for (const r of model.records) {
    const key = r.node + "#" + r.inc;
    const po = r.node !== 0 ? last.get(key) : undefined;
    if (r.node !== 0) {
      last.set(key, r.seq);
    }
    if (!model.slice.has(r.seq)) {
      continue;
    }
    if (r.cause !== 0 && r.cause < r.seq && model.slice.has(r.cause) && model.bySeq.has(r.cause)) {
      edges.push({ from: r.cause, to: r.seq });
    }
    if (po !== undefined && po !== r.cause && model.slice.has(po)) {
      edges.push({ from: po, to: r.seq });
    }
  }
  return edges;
}

// shownEdges returns the slice edges to draw under filters (ART-079 items 4 and 10), each from and
// to a shown record. An edge between shown records stays. A shown member b also gets an edge from
// each shown member that reaches it through hidden members only, but of b's sources in one run
// only the latest: a run is a node incarnation's members linked by program-order edges, so an
// earlier one reaches b through the later one. Each global record is a run of its own. With more
// than BRIDGE_MAX members the work is not bounded enough for ART-072, so edges with a hidden end
// are only dropped. The view calls it when the filters change, not on every draw. edges come in
// record order, so a hidden member's sources are complete before an edge leaves it.
export function shownEdges(model, edges, filters) {
  const shown = (seq) => isVisible(model.records[model.bySeq.get(seq)], filters);
  if (model.slice.size > BRIDGE_MAX) {
    return edges.filter((e) => shown(e.from) && shown(e.to));
  }
  const run = new Map();
  const last = new Map();
  for (const r of model.records) {
    const key = r.node + "#" + r.inc;
    const po = r.node !== 0 ? last.get(key) : undefined;
    if (r.node !== 0) {
      last.set(key, r.seq);
    }
    if (model.slice.has(r.seq)) {
      run.set(r.seq, po !== undefined && run.has(po) ? run.get(po) : r.seq);
    }
  }
  // sources maps a member to the latest source of each run (run -> seq); direct holds the shown
  // members a shown member has an edge from.
  const sources = new Map();
  const direct = new Map();
  const add = (to, seq) => {
    if (!sources.has(to)) {
      sources.set(to, new Map());
    }
    const latest = sources.get(to);
    const k = run.get(seq);
    if (!latest.has(k) || latest.get(k) < seq) {
      latest.set(k, seq);
    }
  };
  for (const e of edges) {
    if (shown(e.from)) {
      add(e.to, e.from);
      if (shown(e.to)) {
        if (!direct.has(e.to)) {
          direct.set(e.to, new Set());
        }
        direct.get(e.to).add(e.from);
      }
    } else if (sources.has(e.from)) {
      for (const seq of sources.get(e.from).values()) {
        add(e.to, seq);
      }
    }
  }
  const out = [];
  for (const [to, latest] of sources) {
    if (!shown(to)) {
      continue;
    }
    const own = direct.get(to) || new Set();
    for (const from of own) {
      out.push({ from, to });
    }
    for (const from of latest.values()) {
      if (!own.has(from)) {
        out.push({ from, to });
      }
    }
  }
  return out;
}

// ticks returns the axis ticks [{x, label}], at least TICK_MIN_PX apart: seconds with the fewest
// decimals, or "#<seq>" of the record at each index in sequence mode (ART-079 item 3, UI-087).
export function ticks(model, view) {
  const max = Math.max(2, Math.floor((view.width - view.gutter) / TICK_MIN_PX));
  const step = tickStep(view.to - view.from, max);
  const decimals = tickDecimals(step);
  const out = [];
  for (let v = Math.ceil(view.from / step) * step; v <= view.to; v += step) {
    if (view.mode !== "seq") {
      out.push({ x: toX(view, v), label: tickLabel(v, decimals) });
    } else if (v >= 0 && v < model.records.length) {
      out.push({ x: toX(view, v), label: "#" + model.records[v].seq });
    }
  }
  return out;
}

// frame computes what one draw needs: lane positions, the visible records and their x.
function frame(model, view, state) {
  const lanes = laneIndex(model);
  const xs = new Map();
  const vis = [];
  // The search bounds sit one pixel outside the drawn range, so rounding in fromX cannot drop a
  // record that the x test below keeps.
  const last = fromX(view, view.width + 9);
  for (let i = firstIndex(model, view, fromX(view, view.gutter - 9)); i < model.records.length; i++) {
    if (axisValue(model, view, i) > last) {
      break;
    }
    const r = model.records[i];
    if (!isVisible(r, state.filters)) {
      continue;
    }
    const x = toX(view, axisValue(model, view, i));
    if (x < view.gutter - 8 || x > view.width + 8) {
      continue;
    }
    vis.push(i);
    xs.set(r.seq, x);
  }
  const dim = state.sliceOn && model.slice.size > 0;
  const runEnd = view.mode === "seq" ? toX(view, model.records.length - 1) : toX(view, model.end);
  return {
    model,
    view,
    state,
    P: state.colors,
    lanes,
    vis,
    xs,
    dim,
    runEnd,
    bottom: Math.min(view.height, laneTop(view, model.lanes.length)),
    alpha: (seq) => (dim && !model.slice.has(seq) ? SLICE_DIM : 1),
    laneOf: (r) => lanes.get(r.node) || 0,
    pos(seq) {
      const i = model.bySeq.get(seq);
      const r = model.records[i];
      const x = xs.has(seq) ? xs.get(seq) : toX(view, axisValue(model, view, i));
      return { x, y: laneY(view, lanes.get(r.node) || 0) };
    },
    shown: (seq) => isVisible(model.records[model.bySeq.get(seq)], state.filters),
  };
}

// draw paints the run view onto ctx (ART-079 items 2-7 and 10). state holds the kind filters, the
// selected record index (-1 for none), whether the causal slice is on, its edges, the colors and
// fonts read from theme.css, the failure node's ID and the window of a cut trace.
export function draw(ctx, model, view, state) {
  const g = frame(model, view, state);
  ctx.save();
  ctx.clearRect(0, 0, view.width, view.height);
  paintLanes(ctx, g);
  ctx.save();
  ctx.beginPath();
  ctx.rect(view.gutter, AXIS_HEIGHT, view.width - view.gutter, view.height - AXIS_HEIGHT);
  ctx.clip();
  paintOutside(ctx, g);
  paintBands(ctx, g);
  const tickList = ticks(model, view);
  paintGrid(ctx, g, tickList);
  paintArrows(ctx, g);
  paintSliceEdges(ctx, g);
  paintGlyphs(ctx, g);
  paintRecordLabels(ctx, g);
  paintBandLabels(ctx, g);
  const tags = paintOverlay(ctx, g);
  ctx.restore();
  paintGutter(ctx, g);
  paintAxis(ctx, g, tickList, tags);
  ctx.restore();
}

function paintLanes(ctx, g) {
  const { view, P } = g;
  ctx.fillStyle = P.bg;
  ctx.fillRect(0, 0, view.width, view.height);
  ctx.fillStyle = P["lane-alt"];
  g.model.lanes.forEach((_, li) => {
    if (li % 2 === 1) {
      ctx.fillRect(0, laneTop(view, li), view.width, view.laneH);
    }
  });
}

// hatch strokes diagonal lines every gap px across the rect, clipped to it.
function hatch(ctx, x, y, w, hgt, gap) {
  ctx.save();
  ctx.beginPath();
  ctx.rect(x, y, w, hgt);
  ctx.clip();
  ctx.beginPath();
  for (let a = x - hgt; a < x + w; a += gap) {
    ctx.moveTo(a, y + hgt);
    ctx.lineTo(a + hgt, y);
  }
  ctx.stroke();
  ctx.restore();
}

// paintOutside shades the time before t=0 and after the end, and the part of a cut trace before
// its window, where records are missing.
function paintOutside(ctx, g) {
  const { model, view, P, bottom } = g;
  if (model.records.length === 0) {
    return;
  }
  const shade = (a, b) => {
    if (b <= a) {
      return;
    }
    ctx.fillStyle = P.muted;
    ctx.strokeStyle = P.muted;
    ctx.lineWidth = 1;
    ctx.globalAlpha = 0.07;
    ctx.fillRect(a, AXIS_HEIGHT, b - a, bottom - AXIS_HEIGHT);
    ctx.globalAlpha = 0.16;
    hatch(ctx, a, AXIS_HEIGHT, b - a, bottom - AXIS_HEIGHT, 8);
    ctx.globalAlpha = 1;
  };
  const x0 = timeX(model, view, 0);
  shade(view.gutter, Math.min(x0, view.width));
  shade(Math.max(g.runEnd, view.gutter), view.width);
  const w = g.state.window;
  if (w) {
    const xw = timeX(model, view, w.from_ns);
    const a = Math.max(view.gutter, x0);
    if (xw > a) {
      ctx.fillStyle = P.muted;
      ctx.globalAlpha = 0.06;
      ctx.fillRect(a, AXIS_HEIGHT, xw - a, bottom - AXIS_HEIGHT);
      ctx.globalAlpha = 1;
    }
  }
}

// bandBox returns the canvas box of band b; in sequence mode a band open to the end stops at the
// last record.
function bandBox(g, b) {
  const { model, view } = g;
  const li = g.lanes.get(b.lane) || 0;
  const a = Math.max(view.gutter - 2, timeX(model, view, b.from));
  const end = view.mode === "seq" && b.to >= model.end ? g.runEnd : timeX(model, view, b.to);
  return { li, a, z: Math.min(view.width + 2, end), y: laneTop(view, li), hgt: view.laneH };
}

const BAND_COLOR = { down: "down", paused: "paused", fault: "fault", recovery: "muted" };
const BAND_TINT = { down: 0.12, paused: 0.12, fault: 0.07, recovery: 0.08 };

// paintBands draws UI-093's band styles: fault tint and a 3 px stripe at the lane top, down tint
// and hatch, paused tint and dots, and the recovery tint with a dotted line across all lanes.
function paintBands(ctx, g) {
  const { model, view, P } = g;
  for (const b of model.bands) {
    const { a, z, y, hgt } = bandBox(g, b);
    if (z < view.gutter || a > view.width || z - a < 0.5) {
      continue;
    }
    const color = P[BAND_COLOR[b.type]];
    ctx.fillStyle = color;
    ctx.strokeStyle = color;
    ctx.lineWidth = 1;
    ctx.globalAlpha = BAND_TINT[b.type];
    ctx.fillRect(a, y + 1, z - a, hgt - 1);
    if (b.type === "down") {
      ctx.globalAlpha = 0.5;
      hatch(ctx, a, y + 1, z - a, hgt - 1, 6);
    } else if (b.type === "paused") {
      ctx.globalAlpha = 0.75;
      ctx.beginPath();
      for (let x = a + 3; x < z; x += 6) {
        for (let yy = y + 4; yy < y + hgt; yy += 6) {
          ctx.moveTo(x + 0.85, yy);
          ctx.arc(x, yy, 0.85, 0, 2 * Math.PI);
        }
      }
      ctx.fill();
    } else if (b.type === "fault") {
      ctx.globalAlpha = 1;
      ctx.fillRect(a, y + 1, z - a, 3);
    } else {
      const x = Math.round(a) + 0.5;
      ctx.globalAlpha = 1;
      ctx.setLineDash([1, 3]);
      ctx.beginPath();
      ctx.moveTo(x, AXIS_HEIGHT);
      ctx.lineTo(x, g.bottom);
      ctx.stroke();
      ctx.setLineDash([]);
    }
    ctx.globalAlpha = 1;
  }
}

function paintGrid(ctx, g, tickList) {
  const { model, view, P } = g;
  ctx.strokeStyle = P.border;
  ctx.lineWidth = 1;
  ctx.globalAlpha = 0.7;
  ctx.beginPath();
  for (const t of tickList) {
    ctx.moveTo(Math.round(t.x) + 0.5, AXIS_HEIGHT);
    ctx.lineTo(Math.round(t.x) + 0.5, view.height);
  }
  ctx.stroke();
  ctx.globalAlpha = 1;
  ctx.beginPath();
  model.lanes.forEach((_, li) => {
    const y = Math.round(laneTop(view, li + 1)) - 0.5;
    ctx.moveTo(view.gutter, y);
    ctx.lineTo(view.width, y);
  });
  ctx.stroke();
}

// strokeArrows draws a batch of lines {a, b} in one style, each shortened to clear both glyphs,
// with filled heads when head is set.
function strokeArrows(ctx, batch) {
  const ends = [];
  ctx.globalAlpha = batch.alpha;
  ctx.strokeStyle = batch.color;
  ctx.fillStyle = batch.color;
  ctx.lineWidth = 1;
  ctx.setLineDash(batch.dash);
  ctx.beginPath();
  for (const { a, b } of batch.lines) {
    const len = Math.hypot(b.x - a.x, b.y - a.y);
    if (len < 2) {
      continue;
    }
    const ux = (b.x - a.x) / len;
    const uy = (b.y - a.y) / len;
    ctx.moveTo(a.x + ux * 4, a.y + uy * 4);
    ctx.lineTo(b.x - ux * 5, b.y - uy * 5);
    ends.push([b.x - ux * 5, b.y - uy * 5, ux, uy]);
  }
  ctx.stroke();
  ctx.setLineDash([]);
  if (batch.head) {
    ctx.beginPath();
    for (const [ex, ey, ux, uy] of ends) {
      ctx.moveTo(ex, ey);
      ctx.lineTo(ex - ux * 7 - uy * 3, ey - uy * 7 + ux * 3);
      ctx.lineTo(ex - ux * 7 + uy * 3, ey - uy * 7 - ux * 3);
    }
    ctx.fill();
  }
  ctx.globalAlpha = 1;
}

// offscreen reports whether the segment from a to b misses the canvas horizontally.
function offscreen(g, a, b) {
  return Math.max(a.x, b.x) < g.view.gutter - 40 || Math.min(a.x, b.x) > g.view.width + 40;
}

// paintArrows draws message arrows and the dashed in-flight line of each drop (ART-079 item 5),
// batched by style; arrows touching the selection are drawn last, in the accent.
function paintArrows(ctx, g) {
  const { model, state, P } = g;
  const sel = state.selected >= 0 ? model.records[state.selected].seq : 0;
  const batches = new Map();
  const add = (color, dash, head, alpha, a, b) => {
    const key = color + "|" + dash.join(",") + "|" + alpha;
    if (!batches.has(key)) {
      batches.set(key, { color, dash, head, alpha, lines: [] });
    }
    batches.get(key).lines.push({ a, b });
  };
  for (const m of model.messages) {
    if (!g.shown(m.send) || !g.shown(m.recv)) {
      continue;
    }
    const a = g.pos(m.send);
    const b = g.pos(m.recv);
    if (!offscreen(g, a, b)) {
      const touches = sel === m.send || sel === m.recv;
      const alpha = Math.min(g.alpha(m.send), g.alpha(m.recv)) * (touches ? 1 : 0.85);
      add(touches ? P.select : P.arrow, [], true, alpha, a, b);
    }
  }
  for (const d of model.drops) {
    if (d.send === null || !g.shown(d.send) || !g.shown(d.drop)) {
      continue;
    }
    const a = g.pos(d.send);
    const b = g.pos(d.drop);
    if (a.y !== b.y && !offscreen(g, a, b)) {
      add(P.drop, [4, 3], false, Math.min(g.alpha(d.send), g.alpha(d.drop)), a, b);
    }
  }
  const all = [...batches.values()];
  for (const batch of all.filter((x) => x.color !== P.select).concat(all.filter((x) => x.color === P.select))) {
    strokeArrows(ctx, batch);
  }
}

// paintSliceEdges draws the slice edges of state.edges (shownEdges, ART-079 item 10), skipping any
// with a hidden end; an edge along one lane runs as a rail above it, so it does not cover the
// glyphs.
function paintSliceEdges(ctx, g) {
  if (!g.dim) {
    return;
  }
  ctx.strokeStyle = g.P.select;
  ctx.lineWidth = 1.25;
  ctx.globalAlpha = 0.9;
  ctx.setLineDash([3, 2]);
  ctx.beginPath();
  for (const e of g.state.edges) {
    if (!g.shown(e.from) || !g.shown(e.to)) {
      continue;
    }
    const a = g.pos(e.from);
    const b = g.pos(e.to);
    if (offscreen(g, a, b)) {
      continue;
    }
    const lift = a.y === b.y ? 7 : 0;
    ctx.moveTo(a.x, a.y - lift);
    ctx.lineTo(b.x, b.y - lift);
  }
  ctx.stroke();
  ctx.setLineDash([]);
  ctx.globalAlpha = 1;
}

// paintGlyphs draws one glyph per visible record (ART-079 item 4), category by category in
// DRAW_ORDER, each category as one path per opacity: records outside an active slice first, at
// SLICE_DIM, then the rest.
function paintGlyphs(ctx, g) {
  const { model, view, P } = g;
  const groups = new Map();
  for (const c of DRAW_ORDER) {
    groups.set(c, [[], []]);
  }
  for (const i of g.vis) {
    const r = model.records[i];
    const y = laneY(view, g.laneOf(r));
    if (y > AXIS_HEIGHT - 8 && y < view.height + 8) {
      groups.get(category(r, model.root))[g.alpha(r.seq) < 1 ? 0 : 1].push(i);
    }
  }
  for (const c of DRAW_ORDER) {
    const spec = GLYPHS[c];
    const [dimmed, full] = groups.get(c);
    for (const [list, alpha] of [[dimmed, SLICE_DIM], [full, 1]]) {
      if (list.length === 0) {
        continue;
      }
      ctx.globalAlpha = alpha;
      ctx.beginPath();
      for (const i of list) {
        const r = model.records[i];
        addGlyph(ctx, spec.shape, g.xs.get(r.seq), laneY(view, g.laneOf(r)), spec.size, spec.stroke !== 0 || c === "violation");
      }
      if (spec.stroke === 0) {
        ctx.fillStyle = P[spec.color];
        ctx.fill();
      } else {
        ctx.strokeStyle = P[spec.color];
        ctx.lineWidth = spec.stroke;
        ctx.stroke();
      }
      if (c === "violation") {
        ctx.strokeStyle = P.bg;
        ctx.lineWidth = 1;
        ctx.stroke();
      }
    }
  }
  ctx.globalAlpha = 1;
}

// fitText returns text cut with "…" to fit max px, or "" when fewer than minChars would remain.
function fitText(ctx, text, max, minChars) {
  if (max <= 12) {
    return "";
  }
  if (ctx.measureText(text).width <= max) {
    return text;
  }
  let lo = 0;
  let hi = text.length;
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1;
    if (ctx.measureText(text.slice(0, mid) + "…").width <= max) {
      lo = mid;
    } else {
      hi = mid - 1;
    }
  }
  return lo >= Math.min(text.length, minChars) ? text.slice(0, lo) + "…" : "";
}

// backedText writes text on a lane-colored backing, so labels stay readable over marks.
function backedText(ctx, g, li, text, x, y, color, alpha) {
  const w = ctx.measureText(text).width;
  ctx.globalAlpha = 0.9 * alpha;
  ctx.fillStyle = li % 2 === 1 ? g.P["lane-alt"] : g.P.bg;
  ctx.fillRect(x - 3, y - 7, w + 6, 14);
  ctx.globalAlpha = alpha;
  ctx.fillStyle = color;
  ctx.fillText(text, x, y);
  ctx.globalAlpha = 1;
}

// PLANNER_KINDS are the planner's bookkeeping records, which carry no label on the lane.
const PLANNER_KINDS = ["fault.target", "fault.recover"];

// labeled reports whether record r carries its text on the lane (ART-079 item 4): the failure
// record, and fault records that opened no band (banded holds the seqs of those that did; their
// band carries the label).
function labeled(r, banded) {
  return r.kind === "check.violation" || (r.kind.startsWith("fault.") && !banded.has(r.seq) && !PLANNER_KINDS.includes(r.kind));
}

// paintRecordLabels writes the text of labeled records right of the glyph, up to the next
// labeled record on the lane or 240 px.
function paintRecordLabels(ctx, g) {
  const { model, view } = g;
  ctx.font = "11px " + g.state.fonts.sans;
  ctx.textBaseline = "middle";
  ctx.textAlign = "left";
  const banded = new Set();
  for (const b of model.bands) {
    if (b.type === "fault") {
      banded.add(b.seq);
    }
  }
  const byLane = new Map();
  for (const i of g.vis) {
    if (labeled(model.records[i], banded)) {
      const li = g.laneOf(model.records[i]);
      if (!byLane.has(li)) {
        byLane.set(li, []);
      }
      byLane.get(li).push(i);
    }
  }
  for (const [li, list] of byLane) {
    list.forEach((i, k) => {
      const r = model.records[i];
      const x = g.xs.get(r.seq);
      let next = Math.min(view.width, x + 240);
      if (k + 1 < list.length) {
        next = Math.min(next, g.xs.get(model.records[list[k + 1]].seq));
      }
      const text = fitText(ctx, r.text, next - x - 22, 6);
      if (text !== "") {
        backedText(ctx, g, li, text, x + 10, laneY(view, li) - 11, g.P.fg, g.alpha(r.seq));
      }
    });
  }
}

// paintBandLabels writes fault labels at the band start and the down and paused words at its
// foot; recovery is named by its axis tag.
function paintBandLabels(ctx, g) {
  const { model, view } = g;
  for (const b of model.bands) {
    const { li, a, z, y } = bandBox(g, b);
    const left = Math.max(a, view.gutter) + 6;
    if (b.type === "recovery" || z - left < 24) {
      continue;
    }
    if (b.type === "fault") {
      ctx.font = "500 11px " + g.state.fonts.sans;
      const text = fitText(ctx, b.label + (b.to >= model.end ? " (until end)" : ""), z - left - 6, 6);
      if (text !== "") {
        backedText(ctx, g, li, text, left, y + 13, g.P.fg, 1);
      }
    } else {
      ctx.font = "10.5px " + g.state.fonts.sans;
      const text = fitText(ctx, b.label, z - left - 4, 6);
      if (text !== "") {
        backedText(ctx, g, li, text, left, y + view.laneH - 9, g.P.muted, 1);
      }
    }
  }
}

function vline(ctx, x, y0, y1) {
  ctx.beginPath();
  ctx.moveTo(Math.round(x) + 0.5, y0);
  ctx.lineTo(Math.round(x) + 0.5, y1);
  ctx.stroke();
}

// paintOverlay draws the failure line and stars (ART-079 item 7), the window start and the
// selection, and returns the axis tags they need, highest priority first.
function paintOverlay(ctx, g) {
  const { model, view, state, P } = g;
  const tags = [];
  if (model.root !== 0 && model.bySeq.has(model.root)) {
    const p = g.pos(model.root);
    ctx.strokeStyle = P.violation;
    ctx.lineWidth = 1.5;
    ctx.setLineDash([4, 3]);
    vline(ctx, p.x, AXIS_HEIGHT, g.bottom);
    ctx.setLineDash([]);
    const star = (y) => {
      ctx.shadowColor = P.violation;
      ctx.shadowBlur = 10;
      ctx.beginPath();
      addGlyph(ctx, "star5", p.x, y, 13, true);
      ctx.fillStyle = P.violation;
      ctx.fill();
      ctx.shadowBlur = 0;
      ctx.strokeStyle = P.bg;
      ctx.lineWidth = 1;
      ctx.stroke();
    };
    star(p.y);
    if (state.failureNode && g.lanes.has(state.failureNode)) {
      star(laneY(view, g.lanes.get(state.failureNode)));
    }
    tags.push({ x: p.x, text: "failure", color: P.violation });
  }
  if (state.selected >= 0) {
    const r = model.records[state.selected];
    const x = toX(view, axisValue(model, view, state.selected));
    const y = laneY(view, g.laneOf(r));
    ctx.strokeStyle = P.select;
    ctx.globalAlpha = 0.55;
    ctx.lineWidth = 1;
    ctx.setLineDash([2, 3]);
    vline(ctx, x, AXIS_HEIGHT, g.bottom);
    ctx.setLineDash([]);
    ctx.globalAlpha = 1;
    ctx.lineWidth = 2;
    ctx.shadowColor = P.select;
    ctx.shadowBlur = 8;
    ctx.beginPath();
    ctx.arc(x, y, 8, 0, 2 * Math.PI);
    ctx.stroke();
    ctx.shadowBlur = 0;
    tags.push({ x, text: view.mode === "seq" ? "#" + r.seq : formatTime(r.at), color: P.select });
  }
  if (state.window) {
    const x = timeX(model, view, state.window.from_ns);
    ctx.strokeStyle = P.muted;
    ctx.lineWidth = 1;
    ctx.setLineDash([4, 3]);
    vline(ctx, x, AXIS_HEIGHT, g.bottom);
    ctx.setLineDash([]);
    tags.push({ x, text: "window from #" + state.window.from_seq, color: P.muted });
  }
  if (model.records.length > 0 && view.mode === "time") {
    tags.push({ x: g.runEnd, text: "end", color: P.muted });
  }
  for (const b of model.bands) {
    if (b.type === "recovery") {
      tags.push({ x: timeX(model, view, b.from), text: "recovery", color: P.muted });
    }
  }
  return tags;
}

// paintGutter draws the lane labels, "name [tags]" (ART-079 item 2); tags go under the name when
// both do not fit on one line.
function paintGutter(ctx, g) {
  const { model, view, P } = g;
  const mono = g.state.fonts.mono;
  ctx.fillStyle = P.bg;
  ctx.fillRect(0, AXIS_HEIGHT, view.gutter, view.height - AXIS_HEIGHT);
  ctx.textBaseline = "middle";
  ctx.textAlign = "left";
  model.lanes.forEach((lane, li) => {
    const top = laneTop(view, li);
    if (top + view.laneH < AXIS_HEIGHT || top > view.height) {
      return;
    }
    if (li % 2 === 1) {
      ctx.fillStyle = P["lane-alt"];
      ctx.fillRect(0, top, view.gutter, view.laneH);
    }
    const y = laneY(view, li);
    const tags = lane.tags.length > 0 ? "[" + lane.tags.join(",") + "]" : "";
    ctx.font = "500 12px " + mono;
    const nameW = ctx.measureText(lane.name).width;
    ctx.font = "11px " + mono;
    const stacked = tags !== "" && 12 + nameW + 6 + ctx.measureText(tags).width > view.gutter - 8;
    ctx.font = "500 12px " + mono;
    ctx.fillStyle = P.fg;
    ctx.fillText(fitText(ctx, lane.name, view.gutter - 20, 3), 12, stacked ? y - 6 : y);
    if (tags !== "") {
      ctx.font = (stacked ? "10px " : "11px ") + mono;
      ctx.fillStyle = P.muted;
      if (stacked) {
        ctx.fillText(fitText(ctx, tags, view.gutter - 20, 3), 12, y + 7);
      } else {
        ctx.fillText(tags, 12 + nameW + 6, y);
      }
    }
  });
  ctx.strokeStyle = P.border;
  ctx.lineWidth = 1;
  ctx.beginPath();
  model.lanes.forEach((_, li) => {
    const y = Math.round(laneTop(view, li + 1)) - 0.5;
    if (y > AXIS_HEIGHT) {
      ctx.moveTo(0, y);
      ctx.lineTo(view.gutter, y);
    }
  });
  ctx.moveTo(view.gutter - 0.5, AXIS_HEIGHT);
  ctx.lineTo(view.gutter - 0.5, view.height);
  ctx.stroke();
}

// pill draws an axis tag centered on x, or ending or starting at x near the edges.
function pill(ctx, g, t) {
  ctx.fillStyle = g.P.bg;
  ctx.strokeStyle = t.color;
  ctx.lineWidth = 1;
  ctx.beginPath();
  ctx.roundRect(t.a + 0.5, AXIS_HEIGHT / 2 - 9.5, t.b - t.a, 17, 8.5);
  ctx.fill();
  ctx.stroke();
  ctx.fillStyle = t.color;
  ctx.fillText(t.text, t.a + 5.5, AXIS_HEIGHT / 2 - 0.5);
}

// paintAxis draws the axis: the mode name, ticks, tick labels and the tags; a tag hides the tick
// labels and lower-priority tags it overlaps.
function paintAxis(ctx, g, tickList, tags) {
  const { view, P } = g;
  const sans = g.state.fonts.sans;
  const mono = g.state.fonts.mono;
  ctx.fillStyle = P.bg;
  ctx.fillRect(0, 0, view.width, AXIS_HEIGHT);
  ctx.strokeStyle = P.border;
  ctx.lineWidth = 1;
  ctx.beginPath();
  ctx.moveTo(0, AXIS_HEIGHT - 0.5);
  ctx.lineTo(view.width, AXIS_HEIGHT - 0.5);
  ctx.stroke();
  ctx.font = "11px " + sans;
  ctx.fillStyle = P.muted;
  ctx.textBaseline = "middle";
  ctx.textAlign = "left";
  ctx.fillText(view.mode === "seq" ? "sequence" : "time", 12, AXIS_HEIGHT / 2);
  ctx.save();
  ctx.beginPath();
  ctx.rect(view.gutter, 0, view.width - view.gutter, AXIS_HEIGHT);
  ctx.clip();
  ctx.strokeStyle = P.muted;
  ctx.beginPath();
  for (const t of tickList) {
    ctx.moveTo(Math.round(t.x) + 0.5, AXIS_HEIGHT - 6);
    ctx.lineTo(Math.round(t.x) + 0.5, AXIS_HEIGHT);
  }
  ctx.stroke();
  ctx.font = "600 10.5px " + sans;
  const placed = [];
  for (const t of tags) {
    if (t.x < view.gutter - 1 || t.x > view.width + 1) {
      continue;
    }
    const w = ctx.measureText(t.text).width + 10;
    if (t.x > view.width - w / 2 - 4) {
      t.a = t.x - w;
    } else if (t.x < view.gutter + w / 2) {
      t.a = t.x;
    } else {
      t.a = t.x - w / 2;
    }
    t.b = t.a + w;
    if (!placed.some((p) => t.a < p.b + 6 && p.a < t.b + 6)) {
      placed.push(t);
    }
  }
  ctx.font = "11px " + mono;
  ctx.fillStyle = P.muted;
  for (const t of tickList) {
    const a = t.x + 4;
    const b = a + ctx.measureText(t.label).width;
    if (b <= view.width - 4 && !placed.some((p) => a < p.b + 4 && p.a < b + 4)) {
      ctx.fillText(t.label, a, AXIS_HEIGHT / 2 - 1);
    }
  }
  ctx.font = "600 10.5px " + sans;
  for (const t of placed) {
    pill(ctx, g, t);
  }
  ctx.restore();
}
