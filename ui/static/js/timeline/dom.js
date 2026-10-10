// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

import { GLYPHS, glyphD } from "./render.js";

// DOM building blocks of the run view (ART-077): elements, inline SVG icons (UI-194), glyph and
// band swatches. Record texts are user data and reach the DOM only through textContent.

// ART-075 rejects the URL literal anywhere in the bundle, so the namespace is built (UI-049).
const SVG_NS = "http:" + "//www.w3.org/2000/svg";

function addKids(e, kids) {
  for (const k of kids.flat()) {
    if (k !== null && k !== undefined && k !== false) {
      e.append(k);
    }
  }
}

// h creates element tag. attrs: "text" sets textContent, "on<event>" adds a listener, other keys
// are attributes (true sets an empty value; null, undefined and false are skipped). Kids are
// nodes or strings.
export function h(doc, tag, attrs, ...kids) {
  const e = doc.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === null || v === undefined || v === false) {
      continue;
    }
    if (k === "text") {
      e.textContent = v;
    } else if (k.startsWith("on")) {
      e.addEventListener(k.slice(2), v);
    } else {
      e.setAttribute(k, v === true ? "" : String(v));
    }
  }
  addKids(e, kids);
  return e;
}

// svg creates an SVG element with attributes.
export function svg(doc, tag, attrs, ...kids) {
  const e = doc.createElementNS(SVG_NS, tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    e.setAttribute(k, String(v));
  }
  addKids(e, kids);
  return e;
}

// ICONS are 16 x 16 outline icons, drawn with currentColor.
const ICONS = {
  copy: [["rect", { x: 5.5, y: 5.5, width: 8, height: 8, rx: 1.5 }], ["path", { d: "M10.5 5.5V3.5a1 1 0 0 0-1-1h-6a1 1 0 0 0-1 1v6a1 1 0 0 0 1 1h2" }]],
  check: [["path", { d: "M3.5 8.5l3 3 6-7" }]],
  fail: [["circle", { cx: 8, cy: 8, r: 6.25 }], ["path", { d: "M5.9 5.9l4.2 4.2M10.1 5.9l-4.2 4.2" }]],
  pass: [["circle", { cx: 8, cy: 8, r: 6.25 }], ["path", { d: "M5.4 8.3l1.8 1.8 3.4-3.9" }]],
  info: [["circle", { cx: 8, cy: 8, r: 6.25 }], ["path", { d: "M8 7.3v3.6M8 5.1v.1" }]],
  alert: [["path", { d: "M8 2.2l6.2 11H1.8z" }], ["path", { d: "M8 6.4v3.2M8 11.4v.1" }]],
  sun: [["circle", { cx: 8, cy: 8, r: 2.75 }], ["path", { d: "M8 1.5v1.5M8 13v1.5M1.5 8H3M13 8h1.5M3.4 3.4l1.06 1.06M11.54 11.54l1.06 1.06M3.4 12.6l1.06-1.06M11.54 4.46l1.06-1.06" }]],
  moon: [["path", { d: "M13.2 9.6A5.6 5.6 0 0 1 6.4 2.8a5.6 5.6 0 1 0 6.8 6.8z" }]],
  system: [["rect", { x: 2, y: 2.75, width: 12, height: 8.25, rx: 1.25 }], ["path", { d: "M5.75 13.5h4.5M8 11v2.5" }]],
  fit: [["path", { d: "M2.5 6V2.5H6M10 2.5h3.5V6M13.5 10v3.5H10M6 13.5H2.5V10" }]],
  target: [["circle", { cx: 8, cy: 8, r: 5 }], ["circle", { cx: 8, cy: 8, r: 1.6 }], ["path", { d: "M8 1v2M8 13v2M1 8h2M13 8h2" }]],
  plus: [["path", { d: "M8 3.5v9M3.5 8h9" }]],
  minus: [["path", { d: "M3.5 8h9" }]],
  close: [["path", { d: "M4.25 4.25l7.5 7.5M11.75 4.25l-7.5 7.5" }]],
  slice: [["circle", { cx: 3.75, cy: 4, r: 1.75 }], ["circle", { cx: 12.25, cy: 8, r: 1.75 }], ["circle", { cx: 3.75, cy: 12, r: 1.75 }], ["path", { d: "M5.4 4.8l5.2 2.4M5.4 11.2l5.2-2.4" }]],
  empty: [["path", { d: "M1.5 4.5h13M1.5 8h13M1.5 11.5h13" }], ["path", { d: "M8 2v12", "stroke-dasharray": "1.5 2" }]],
};

// icon returns the decorative icon name; the control that holds it carries the accessible name.
export function icon(doc, name) {
  const e = svg(doc, "svg", { viewBox: "0 0 16 16", class: "fl-icon", "aria-hidden": "true", focusable: "false" });
  for (const [tag, attrs] of ICONS[name]) {
    e.append(svg(doc, tag, attrs));
  }
  return e;
}

// glyphIcon returns the UI-088 glyph of category cat in a box px wide, as the canvas draws it.
export function glyphIcon(doc, cat, box) {
  const spec = GLYPHS[cat];
  const e = svg(doc, "svg", { viewBox: "0 0 " + box + " " + box, class: "fl-glyph", "aria-hidden": "true" });
  e.style.color = "var(--fl-" + spec.color + ")";
  const p = svg(doc, "path", { d: glyphD(spec.shape, box / 2, box / 2, spec.size) });
  if (spec.stroke === 0) {
    p.setAttribute("fill", "currentColor");
  } else {
    p.setAttribute("fill", "none");
    p.setAttribute("stroke", "currentColor");
    p.setAttribute("stroke-width", String(spec.stroke));
  }
  e.append(p);
  return e;
}

// bandSwatch returns the legend swatch of a band type: fault, down, paused or recovery.
export function bandSwatch(doc, type) {
  const e = svg(doc, "svg", { viewBox: "0 0 18 10", class: "fl-swatch", "aria-hidden": "true" });
  e.style.color = "var(--fl-" + { fault: "fault", down: "down", paused: "paused", recovery: "muted" }[type] + ")";
  e.append(svg(doc, "rect", { width: 18, height: 10, fill: "currentColor", "fill-opacity": type === "recovery" ? 0.12 : 0.14 }));
  if (type === "fault") {
    e.append(svg(doc, "rect", { width: 18, height: 3, fill: "currentColor" }));
  } else if (type === "down") {
    for (let x = -10; x < 18; x += 5) {
      e.append(svg(doc, "path", { d: "M" + x + " 10L" + (x + 10) + " 0", stroke: "currentColor", "stroke-opacity": 0.6 }));
    }
  } else if (type === "paused") {
    for (let x = 2.5; x < 18; x += 5) {
      e.append(svg(doc, "circle", { cx: x, cy: 2.5, r: 0.9, fill: "currentColor" }), svg(doc, "circle", { cx: x, cy: 7.5, r: 0.9, fill: "currentColor" }));
    }
  } else {
    e.append(svg(doc, "path", { d: "M1 0V10", stroke: "currentColor", "stroke-dasharray": "1 2" }));
  }
  return e;
}

// LINES are the legend's line kinds: color token, dash, and the end mark.
const LINES = {
  message: { color: "arrow", dash: "", end: "head" },
  dropped: { color: "drop", dash: "4 3", end: "cross" },
  failure: { color: "violation", dash: "4 3", end: "" },
  slice: { color: "select", dash: "3 2", end: "" },
};

// lineSwatch returns the legend swatch of a line kind: message, dropped, failure or slice.
export function lineSwatch(doc, kind) {
  const spec = LINES[kind];
  const e = svg(doc, "svg", { viewBox: "0 0 18 10", class: "fl-swatch", "aria-hidden": "true" });
  e.style.color = "var(--fl-" + spec.color + ")";
  const vertical = kind === "failure";
  const line = svg(doc, "path", { d: vertical ? "M9 0V10" : "M1 5H13", stroke: "currentColor", "stroke-width": vertical ? 1.5 : 1.25, fill: "none" });
  if (spec.dash !== "") {
    line.setAttribute("stroke-dasharray", spec.dash);
  }
  e.append(line);
  if (spec.end === "head") {
    e.append(svg(doc, "path", { d: "M12 2L17 5L12 8Z", fill: "currentColor" }));
  } else if (spec.end === "cross") {
    e.append(svg(doc, "path", { d: "M13 2.5l4 5M17 2.5l-4 5", stroke: "currentColor", "stroke-width": 1.5 }));
  }
  return e;
}

// brandMark returns the logo: three bed pairs and the fault line. Theme colors go through CSSOM,
// because presentation attributes do not take var().
export function brandMark(doc) {
  const e = svg(doc, "svg", { viewBox: "0 0 64 64", class: "fl-mark", "aria-hidden": "true" });
  const beds = [[5, 10, "deliver"], [35, 18, "deliver"], [5, 24, "send"], [35, 32, "send"], [5, 38, "paused"], [35, 46, "paused"]];
  for (const [x, y, color] of beds) {
    const bed = svg(doc, "rect", { x, y, width: 24, height: 8, rx: 1.2 });
    bed.style.fill = "var(--fl-" + color + ")";
    e.append(bed);
  }
  const line = svg(doc, "g", {}, svg(doc, "path", { d: "M32 8v48", "stroke-width": 2.5, "stroke-linecap": "round" }));
  for (const cy of [14, 28, 42]) {
    line.append(svg(doc, "circle", { cx: 32, cy, r: 2.6, stroke: "none" }));
  }
  line.style.fill = "var(--fl-fault)";
  line.style.stroke = "var(--fl-fault)";
  e.append(line);
  return e;
}
