// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

import { formatTime, recordNode } from "./format.js";
import { category } from "./render.js";
import { h, icon, glyphIcon } from "./dom.js";

// Inspector panel and selection line (ART-079 item 8). Record texts are user data: they reach the
// DOM only through textContent.

// EFFECTS_SHOWN caps the effect links; the rest are counted.
export const EFFECTS_SHOWN = 12;

// effectsOf returns the seqs of records whose cause is seq, ascending.
export function effectsOf(model, seq) {
  const out = [];
  for (const r of model.records) {
    if (r.cause === seq) {
      out.push(r.seq);
    }
  }
  return out;
}

// selectionText is the announcement of a selection (UI-176).
export function selectionText(model, i) {
  const r = model.records[i];
  return "Selected seq " + r.seq + ", " + formatTime(r.at) + ", " + recordNode(model, r) + ", " + r.kind + ": " + r.text;
}

// seqLink returns a button that selects record seq, or a note when the record is not in the
// timeline data (a cut trace).
export function seqLink(doc, model, seq, onSelect) {
  const i = model.bySeq.get(seq);
  if (i === undefined) {
    return h(doc, "span", { class: "fl-aside", text: "#" + seq + " (not in this view)" });
  }
  const kind = model.records[i].kind;
  const link = h(doc, "button", { class: "fl-link", type: "button", "aria-label": "Select record " + seq + ", " + kind, text: "#" + seq, onclick: () => onSelect(seq) });
  return h(doc, "span", {}, link, " ", h(doc, "span", { class: "fl-aside", text: kind }));
}

function table(doc, rows) {
  const t = h(doc, "table", { class: "fl-kv", translate: "no" });
  for (const [k, v] of rows) {
    t.append(h(doc, "tr", {}, h(doc, "th", { scope: "row", text: k }), h(doc, "td", {}, v)));
  }
  return h(doc, "div", { class: "fl-well" }, t);
}

function section(doc, title, count, ...kids) {
  return h(doc, "div", { class: "fl-sec" }, h(doc, "h4", {}, title + " ", h(doc, "span", { class: "fl-count", text: String(count) })), ...kids);
}

// renderInspector fills panel with record index i, or a hint when i < 0. actions.select(seq)
// follows a link; actions.clear() clears the selection. It returns the element that takes focus
// after a link or the close button rebuilt the panel: the kind heading or the hint.
export function renderInspector(panel, model, i, actions) {
  const doc = panel.ownerDocument;
  panel.replaceChildren();
  if (i < 0) {
    const hint = h(doc, "p", { class: "fl-hint", tabindex: "-1", text: "Select a record to see its fields, its cause and its effects." });
    panel.append(hint);
    return hint;
  }
  const r = model.records[i];
  const close = h(doc, "button", { class: "fl-round fl-quiet", type: "button", "aria-label": "Clear the selection", onclick: () => actions.clear() }, icon(doc, "close"));
  const heading = h(doc, "h3", { translate: "no", tabindex: "-1", text: r.kind });
  panel.append(h(doc, "div", { class: "fl-insp-head" }, glyphIcon(doc, category(r, model.root), 15), heading, close));
  if (r.text !== "") {
    panel.append(h(doc, "p", { translate: "no", text: r.text }));
  }
  const tags = h(doc, "div", { class: "fl-tags" });
  if (r.seq === model.root) {
    tags.append(h(doc, "span", { class: "fl-pill fl-fail" }, glyphIcon(doc, "violation", 12), "Failure"));
  }
  if (model.slice.has(r.seq)) {
    tags.append(h(doc, "span", { class: "fl-pill fl-info" }, icon(doc, "slice"), "In causal slice"));
  }
  if (tags.childElementCount > 0) {
    panel.append(tags);
  }
  const t = h(doc, "span", {}, formatTime(r.at), " ", h(doc, "span", { class: "fl-aside", text: "(" + r.at + " ns)" }));
  const cause = r.cause === 0 ? h(doc, "span", { class: "fl-aside", text: "none" }) : seqLink(doc, model, r.cause, actions.select);
  panel.append(table(doc, [["seq", String(r.seq)], ["t", t], ["node", recordNode(model, r)], ["cause", cause]]));
  if (r.attrs.length > 0) {
    panel.append(section(doc, "Attributes", r.attrs.length, table(doc, r.attrs)));
  }
  const effects = effectsOf(model, r.seq);
  let box;
  if (effects.length === 0) {
    box = h(doc, "p", { class: "fl-hint", text: "No record names this one as its cause." });
  } else {
    box = h(doc, "div", { class: "fl-links" });
    for (const seq of effects.slice(0, EFFECTS_SHOWN)) {
      box.append(seqLink(doc, model, seq, actions.select));
    }
    if (effects.length > EFFECTS_SHOWN) {
      box.append(h(doc, "span", { class: "fl-aside", text: "and " + (effects.length - EFFECTS_SHOWN) + " more" }));
    }
  }
  panel.append(section(doc, "Effects", effects.length, box));
  return heading;
}

// renderNarration fills the selection line of the control panel.
export function renderNarration(narr, model, i, hasFailure) {
  const doc = narr.ownerDocument;
  if (i < 0) {
    const hint = hasFailure ? "Nothing selected. Click a mark or a fault, or press f to jump to the failure." : "Nothing selected. Click a mark or a fault to inspect it.";
    narr.replaceChildren(h(doc, "span", { class: "fl-hint", text: hint }));
    return;
  }
  const r = model.records[i];
  const line = h(doc, "span", { translate: "no" }, h(doc, "span", { class: "fl-mono", text: formatTime(r.at) }), " · " + recordNode(model, r) + " · ", h(doc, "b", { text: r.kind }), r.text !== "" ? " · " + r.text : "");
  narr.replaceChildren(h(doc, "span", { class: "fl-counter", text: "#" + r.seq }), line);
}
