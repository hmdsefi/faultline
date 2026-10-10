// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

import { formatTime, shortTime, formatCount, counted, windowBanner } from "./format.js";
import { listFaults } from "./model.js";
import { h, icon, glyphIcon, bandSwatch, lineSwatch, brandMark } from "./dom.js";
import { seqLink } from "./inspect.js";

// The run view's chrome around the timeline (ART-079 items 1 and 7): the header bar, the title
// row, the window banner, the side panels, the cards below the stage, and the error and empty
// states. Every value comes from the timeline data; fields an older or smaller report lacks are
// left out.

// copyButton copies text to the clipboard and falls back to selecting target, the element that
// shows it, and asking for the copy keys (ART-079 item 1). The label says "Copied" until the
// pointer or focus leaves.
export function copyButton(doc, text, target, label, announce) {
  const caption = h(doc, "span", { text: "Copy" });
  const b = h(doc, "button", { class: "fl-chip", type: "button", "aria-label": label }, icon(doc, "copy"), caption);
  const reset = () => b.replaceChildren(icon(doc, "copy"), caption);
  const copied = () => {
    b.replaceChildren(icon(doc, "check"), h(doc, "span", { text: "Copied" }));
    announce(label.replace("Copy", "Copied"));
  };
  const select = () => {
    const range = doc.createRange();
    range.selectNodeContents(target);
    const sel = doc.defaultView.getSelection();
    sel.removeAllRanges();
    sel.addRange(range);
    announce("Selected. Press Ctrl+C or Command+C to copy");
  };
  b.addEventListener("click", () => {
    const clip = doc.defaultView.navigator.clipboard;
    if (clip && clip.writeText) {
      clip.writeText(text).then(copied, select);
    } else {
      select();
    }
  });
  b.addEventListener("blur", reset);
  b.addEventListener("pointerleave", reset);
  return b;
}

// command returns a replay-style pill: the command and its Copy button.
export function command(doc, text, label, announce) {
  const code = h(doc, "code", { translate: "no", text });
  return h(doc, "div", { class: "fl-replay fl-glass" }, code, copyButton(doc, text, code, label, announce));
}

// stats returns a definition list of [term, value] rows; null rows are skipped.
export function stats(doc, rows) {
  const dl = h(doc, "dl", { class: "fl-stats" });
  for (const row of rows) {
    if (row !== null) {
      dl.append(h(doc, "div", {}, h(doc, "dt", { text: row[0] }), h(doc, "dd", {}, row[1])));
    }
  }
  return dl;
}

// label returns a panel heading: an uppercase title and an optional count. Its accessible name
// is in sentence case with a comma before the count, not the CSS capitals run together.
export function label(doc, title, count) {
  return h(doc, "h2", { class: "fl-label", "aria-label": count !== null ? title + ", " + count : title }, title, count !== null ? h(doc, "span", { class: "fl-count", text: count }) : null);
}

// panel returns a side panel or card with an uppercase label.
export function panel(doc, title, count, ...kids) {
  const heading = label(doc, title, count);
  return h(doc, "section", { class: "fl-panel fl-glass", "aria-label": title }, heading, ...kids);
}

// themeButton returns {el, paint} for the button that cycles the theme through host (UI-178);
// paint shows the current theme. Without a host there is no button.
export function themeButton(doc, host) {
  if (!host) {
    return { el: null, paint() {} };
  }
  const el = h(doc, "button", { class: "fl-round", type: "button", "aria-keyshortcuts": "t", onclick: () => host.cycleTheme() });
  const paint = () => {
    const t = host.theme();
    el.replaceChildren(icon(doc, t === "system" ? "system" : t === "light" ? "sun" : "moon"));
    el.setAttribute("aria-label", "Theme: " + t + ". Change theme");
    el.setAttribute("title", "Theme: " + t + " (t)");
  };
  paint();
  return { el, paint };
}

// headerBar returns the pill header: the logo, the breadcrumb (package / test / seed) and the
// faultline version.
export function headerBar(doc, rep, theme) {
  const crumbs = h(doc, "ol", { translate: "no" });
  if (rep.package) {
    crumbs.append(h(doc, "li", { text: rep.package }));
  }
  if (rep.test) {
    crumbs.append(h(doc, "li", { text: rep.test }));
  }
  if (rep.subtest) {
    // An edited report can hold any JSON value here; String keeps the page up.
    const sub = String(rep.subtest);
    const last = rep.test && sub.startsWith(rep.test + "/") ? sub.slice(String(rep.test).length + 1) : sub;
    crumbs.append(h(doc, "li", { "aria-current": "page", text: last }));
  }
  const version = rep.versions && rep.versions.faultline ? h(doc, "span", { class: "fl-pill", title: "faultline version", translate: "no", text: rep.versions.faultline }) : null;
  return h(doc, "header", { class: "fl-top fl-glass" },
    h(doc, "div", { class: "fl-brand" }, brandMark(doc), h(doc, "span", { text: "faultline" })),
    crumbs.childElementCount > 0 ? h(doc, "nav", { class: "fl-crumbs", "aria-label": "Breadcrumb" }, crumbs) : null,
    h(doc, "div", { class: "fl-tools" }, version, theme));
}

// titleRow returns the test name, the status and headline, the run chips and the replay command.
export function titleRow(doc, data, announce) {
  const rep = data.report || {};
  const run = rep.run || {};
  let status = null;
  if (rep.status === "fail" || rep.status === "pass") {
    const fail = rep.status === "fail";
    status = h(doc, "span", { class: "fl-pill fl-status " + (fail ? "fl-fail" : "fl-pass") }, icon(doc, fail ? "fail" : "pass"), fail ? "FAIL" : "PASS");
  }
  let headline = "";
  if (rep.failure && rep.failure.headline) {
    headline = rep.failure.headline;
  } else if (rep.status === "pass" && run.end_ns !== undefined) {
    headline = "No check failed in " + formatTime(run.end_ns) + " of virtual time.";
  }
  const chips = [];
  if (run.end_ns !== undefined) {
    chips.push(shortTime(run.end_ns));
  }
  if (run.events !== undefined) {
    chips.push(counted(run.events, "event"));
  }
  if (data.total !== undefined) {
    chips.push(counted(data.total, "record"));
  }
  if (rep.nodes) {
    chips.push(counted(rep.nodes.length, "node"));
  }
  if (run.stop) {
    chips.push("stop " + run.stop);
  }
  const left = h(doc, "div", {},
    h(doc, "h1", { translate: "no", text: rep.test || data.title || "faultline run" }),
    status || headline !== "" ? h(doc, "div", { class: "fl-sub" }, status, headline !== "" ? h(doc, "p", { class: "fl-headline", text: headline }) : null) : null,
    chips.length > 0 ? h(doc, "div", { class: "fl-sub" }, chips.map((c) => h(doc, "span", { class: "fl-pill", translate: "no", text: c }))) : null);
  const replay = rep.replay && rep.replay.command ? command(doc, rep.replay.command, "Copy replay command", announce) : null;
  return h(doc, "section", { class: "fl-head", "aria-label": "Run" }, left, replay);
}

// banner returns ART-080's banner of a cut trace.
export function banner(doc, data, included) {
  return h(doc, "p", { class: "fl-banner fl-glass fl-info", role: "note" }, icon(doc, "info"), h(doc, "span", { text: windowBanner(data, included) }));
}

// failurePanel shows the failure message and facts, or the result of a passing run.
export function failurePanel(doc, data, model, onSelect) {
  const rep = data.report || {};
  const run = rep.run || {};
  const f = rep.failure;
  if (f) {
    const kids = [];
    if (f.message) {
      kids.push(h(doc, "div", { class: "fl-well" }, h(doc, "pre", { translate: "no", text: f.message })));
    }
    const record = f.record_seq ? (model ? seqLink(doc, model, f.record_seq, onSelect) : "#" + f.record_seq + " (not in this view)") : null;
    kids.push(stats(doc, [
      f.check ? ["Check", f.check] : null,
      f.kind ? ["Kind", f.kind] : null,
      f.at ? ["Time", f.at] : null,
      f.node ? ["Node", f.node] : null,
      f.event !== undefined ? ["Event", String(f.event)] : null,
      record !== null ? ["Record", record] : null,
    ]));
    const rootRec = model && f.record_seq ? model.records[model.bySeq.get(f.record_seq)] : undefined;
    if (rootRec && rootRec.node === 0 && f.node && f.event !== undefined) {
      kids.push(h(doc, "p", { class: "fl-hint", text: "The failure record #" + f.record_seq + " is on the global lane. " + f.node + " ran event " + f.event + ", the last event before it, so " + f.node + "'s lane carries a star too." }));
    }
    return panel(doc, "Failure", null, ...kids);
  }
  const total = data.total !== undefined ? data.total : model ? model.records.length : undefined;
  let hint = "No invariant or final check failed.";
  if (run.stop === "deadline") {
    hint = "No invariant or final check failed, and the run reached its deadline.";
  } else if (run.stop === "idle") {
    hint = "No invariant or final check failed. The run stopped because no events were left to run.";
  }
  return panel(doc, "Result", null, stats(doc, [
    ["Status", (rep.status || "unknown").toUpperCase()],
    run.stop ? ["Stop", run.stop] : null,
    run.end_ns !== undefined ? ["Ended", formatTime(run.end_ns)] : null,
    run.events !== undefined ? ["Events", formatCount(run.events)] : null,
    total !== undefined ? ["Records", formatCount(total)] : null,
    run.recovery_ns !== undefined ? ["Recovery from", formatTime(run.recovery_ns)] : null,
  ]), rep.status === "pass" ? h(doc, "p", { class: "fl-hint", text: hint }) : null);
}

// faultsPanel lists the injected faults with their time spans; a row selects its record. It
// returns the panel and the rows, which the view marks aria-current.
export function faultsPanel(doc, model, onSelect) {
  const faults = listFaults(model);
  const rows = [];
  const list = h(doc, "ul", { class: "fl-list" });
  for (const f of faults) {
    let when = shortTime(f.at);
    if (f.band) {
      when = shortTime(f.band.from) + " to " + (f.band.to >= model.end ? "end" : shortTime(f.band.to));
    }
    const row = h(doc, "button", { class: "fl-row", type: "button", "aria-label": f.text + ", " + when, onclick: () => onSelect(f.seq) },
      glyphIcon(doc, "fault", 13), h(doc, "span", { translate: "no", text: f.text }), h(doc, "time", { text: when }));
    rows.push({ seq: f.seq, row });
    list.append(h(doc, "li", {}, row));
  }
  const body = faults.length > 0 ? h(doc, "div", { class: "fl-scroll" }, list) : h(doc, "p", { class: "fl-hint", text: "No faults in this trace." });
  return { el: panel(doc, "Faults", String(faults.length), body), rows };
}

// reportCard repeats the replay command and lists the run facts and versions.
export function reportCard(doc, data, model, announce) {
  const rep = data.report || {};
  const run = rep.run || {};
  const v = rep.versions || {};
  let records = data.total !== undefined ? formatCount(data.total) : null;
  if (data.window && model) {
    records = formatCount(model.records.length) + " shown of " + formatCount(data.total);
  }
  return panel(doc, "Report", null,
    rep.replay && rep.replay.command ? command(doc, rep.replay.command, "Copy replay command", announce) : null,
    stats(doc, [
      rep.test ? ["Test", rep.test] : null,
      rep.subtest ? ["Subtest", rep.subtest] : null,
      rep.package ? ["Package", rep.package] : null,
      rep.seed ? ["Seed", rep.seed] : null,
      run.end_ns !== undefined ? ["Ended", formatTime(run.end_ns)] : null,
      run.stop ? ["Stop", run.stop] : null,
      run.events !== undefined ? ["Events", formatCount(run.events)] : null,
      records !== null ? ["Records", records] : null,
      run.recovery_ns !== undefined ? ["Recovery from", formatTime(run.recovery_ns)] : null,
      run.attempts !== undefined ? ["Attempts", String(run.attempts)] : null,
      v.faultline ? ["faultline", v.faultline] : null,
      v.go ? ["Go", v.go] : null,
      v.goos && v.goarch ? ["Platform", v.goos + "/" + v.goarch] : null,
      rep.dir ? ["Artifacts", rep.dir] : null,
    ]));
}

// KEYS are the timeline's keys (ART-079 item 9) and the theme key (UI-178).
const KEYS = [
  [["+", "-"], "Zoom in and out"],
  [["←", "→"], "Pan by a tenth of the view"],
  [["↑", "↓"], "Scroll the lanes"],
  [["0"], "Fit the whole run"],
  [["f"], "Jump to the failure"],
  [["t"], "Change the theme"],
  [["Wheel"], "Zoom around the pointer"],
  [["Shift+Wheel"], "Scroll the lanes"],
  [["Drag"], "Pan"],
];

// legendCard explains the bands and lines and lists the keys.
export function legendCard(doc) {
  const lines = h(doc, "ul", { class: "fl-list" });
  const row = (swatch, label, how) => lines.append(h(doc, "li", { class: "fl-row" }, swatch, h(doc, "span", { text: label }), h(doc, "span", { class: "fl-end", text: how })));
  row(bandSwatch(doc, "fault"), "fault active", "stripe and tint");
  row(bandSwatch(doc, "down"), "down", "hatched");
  row(bandSwatch(doc, "paused"), "paused", "dotted");
  row(bandSwatch(doc, "recovery"), "recovery", "dotted line");
  row(lineSwatch(doc, "message"), "message", "send to deliver");
  row(lineSwatch(doc, "dropped"), "dropped in flight", "dashed, cross");
  row(lineSwatch(doc, "failure"), "failure time", "dashed line, star");
  row(lineSwatch(doc, "slice"), "causal slice edge", "others dimmed");
  const keys = h(doc, "dl", { class: "fl-keys" });
  for (const [names, what] of KEYS) {
    keys.append(h(doc, "dt", {}, names.map((k) => h(doc, "kbd", { class: "fl-kbd", text: k }))), h(doc, "dd", { text: what }));
  }
  return panel(doc, "Legend and keys", null,
    h(doc, "div", { class: "fl-sec" }, h(doc, "h3", { text: "Bands and lines" }), lines),
    h(doc, "div", { class: "fl-sec" }, h(doc, "h3", { text: "Keys, with the timeline focused" }), keys));
}

// errorState explains a page that could not read its run data and gives the faultline render
// command that rebuilds it from the artifact files.
export function errorState(doc, rep, err, announce) {
  const dir = rep && rep.dir ? rep.dir : "";
  const how = dir !== "" ? "To rebuild this page from them, run:" : "To rebuild this page from them, run this in the artifact folder:";
  return h(doc, "div", { class: "fl-state fl-error", role: "alert" },
    icon(doc, "alert"),
    h(doc, "h3", { text: "This page could not read its run data" }),
    h(doc, "div", { class: "fl-well" }, h(doc, "pre", { text: String(err) })),
    h(doc, "p", { text: "The file may have been cut short or edited. The same run is in trace.jsonl and timeline.txt in this folder. " + how }),
    command(doc, "faultline render " + (dir !== "" ? dir : "."), "Copy render command", announce));
}

// emptyState stands in for the canvas when the trace holds no records.
export function emptyState(doc) {
  return h(doc, "div", { class: "fl-state" },
    icon(doc, "empty"),
    h(doc, "h3", { text: "No records to show" }),
    h(doc, "p", { text: "This run recorded no trace records, so the timeline is empty. The report and the replay command still apply." }));
}
