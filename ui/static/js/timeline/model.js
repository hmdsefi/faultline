// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Run view model (ART-078): parses the embedded trace text and derives the lanes, the seq index,
// messages, drops, bands, the failure root and the causal slice. Pure and DOM-free.

// parseLine parses line n of the trace, naming the line in the error.
function parseLine(n, line) {
  try {
    return JSON.parse(line);
  } catch (err) {
    throw new SyntaxError("trace line " + n + ": " + err.message);
  }
}

// parseTrace parses trace.jsonl text into {header, records}. A record without a number seq and at
// or a string kind throws: the view could not place it, and the page shows its error card.
export function parseTrace(text) {
  const lines = [];
  text.split("\n").forEach((raw, i) => {
    const line = raw.endsWith("\r") ? raw.slice(0, -1) : raw;
    if (line !== "") {
      lines.push([i + 1, line]);
    }
  });
  if (lines.length === 0) {
    return { header: { nodes: [] }, records: [] };
  }
  const header = parseLine(...lines[0]);
  const records = [];
  for (let i = 1; i < lines.length; i++) {
    const o = parseLine(...lines[i]);
    if (o === null || typeof o !== "object" || !Number.isFinite(o.seq) || !Number.isFinite(o.at) || typeof o.kind !== "string") {
      throw new Error("trace line " + lines[i][0] + ": a record needs a number seq and at and a string kind");
    }
    records.push({
      seq: o.seq,
      at: o.at,
      node: o.node || 0,
      inc: o.inc || 0,
      kind: o.kind,
      cause: o.cause || 0,
      text: o.text || "",
      attrs: o.attrs || [],
    });
  }
  return { header, records };
}

// attr returns the value of attribute key of record r, or undefined.
export function attr(r, key) {
  for (const [k, v] of r.attrs) {
    if (k === key) {
      return v;
    }
  }
  return undefined;
}

// DURABLE are the fault kinds that open a band whenever they apply; fault.sync_fail and
// fault.disk_capacity open one only when n is not 0 (FLT-011, ART-078).
const DURABLE = ["fault.partition", "fault.isolate", "fault.cut", "fault.link", "fault.crash", "fault.pause"];

// opensFaultBand reports whether r opens a fault band: an applied durable fault.
function opensFaultBand(r) {
  if (!r.kind.startsWith("fault.") || attr(r, "effect") !== "applied") {
    return false;
  }
  if (DURABLE.includes(r.kind)) {
    return true;
  }
  return (r.kind === "fault.sync_fail" || r.kind === "fault.disk_capacity") && attr(r, "n") !== "0";
}

// closeAt returns the at of the first record after index i that matches, else end.
function closeAt(records, i, end, matches) {
  for (let j = i + 1; j < records.length; j++) {
    if (matches(records[j])) {
      return records[j].at;
    }
  }
  return end;
}

// buildModel derives the run view model from a timeline data object (ART-078):
//
//   {lanes: [{id, name, tags}], records, bySeq, end, messages: [{send, recv}],
//    drops: [{send, drop}], bands: [{lane, from, to, type, label, seq}], root, slice}
//
// Times (end, from, to) are integer ns. Fields that name a record hold its seq: messages, drops,
// bands[].seq, root and slice. bySeq maps a seq to its index in records, and firstAtOrAfter
// returns such an index. "None" is null for drops[].send, and 0 for root and for the seq of the
// recovery band. band.lane is a node ID (0 is global), not an index into lanes. slice is a Set of
// seqs; data.slice is the {root, seqs, cap, truncated} object it comes from.
export function buildModel(data) {
  const { header, records } = parseTrace(data.trace);
  const nodes = (header.nodes || []).slice().sort((a, b) => a.id - b.id);
  const lanes = [{ id: 0, name: "global", tags: [] }];
  for (const n of nodes) {
    lanes.push({ id: n.id, name: n.name, tags: n.tags || [] });
  }
  const bySeq = new Map();
  records.forEach((r, i) => bySeq.set(r.seq, i));
  const run = (data.report && data.report.run) || {};
  const lastAt = records.length > 0 ? records[records.length - 1].at : 0;
  const end = Math.max(lastAt, run.end_ns || 0);

  const sends = new Map();
  for (const r of records) {
    if (r.kind === "net.send" || r.kind === "net.send_raw") {
      const m = attr(r, "msg");
      if (m !== undefined) {
        sends.set(m, r.seq);
      }
    }
  }
  const messages = [];
  const drops = [];
  for (const r of records) {
    if (r.kind === "net.deliver") {
      const s = sends.get(attr(r, "msg"));
      if (s !== undefined) {
        messages.push({ send: s, recv: r.seq });
      }
    } else if (r.kind === "net.drop") {
      const s = sends.get(attr(r, "msg"));
      drops.push({ send: s === undefined ? null : s, drop: r.seq });
    }
  }

  const bands = [];
  records.forEach((r, i) => {
    if (r.kind === "kernel.crash") {
      const to = closeAt(records, i, end, (x) => x.kind === "kernel.boot" && x.node === r.node);
      bands.push({ lane: r.node, from: r.at, to, type: "down", label: "down", seq: r.seq });
    } else if (r.kind === "kernel.pause") {
      const to = closeAt(records, i, end, (x) => (x.kind === "kernel.resume" || x.kind === "kernel.crash") && x.node === r.node);
      bands.push({ lane: r.node, from: r.at, to, type: "paused", label: "paused", seq: r.seq });
    } else if (opensFaultBand(r)) {
      const id = attr(r, "id");
      const to = closeAt(records, i, end, (x) => {
        const u = x.kind.startsWith("fault.") ? attr(x, "undoes") : undefined;
        return u !== undefined && u.split(",").includes(id);
      });
      bands.push({ lane: r.node, from: r.at, to, type: "fault", label: r.text, seq: r.seq });
    }
  });
  if (run.recovery_ns !== undefined) {
    bands.push({ lane: 0, from: run.recovery_ns, to: end, type: "recovery", label: "recovery", seq: 0 });
  }
  bands.sort((a, b) => a.from - b.from || a.seq - b.seq);

  let root = 0;
  if (data.slice) {
    root = data.slice.root;
  } else if (data.report && data.report.failure && data.report.failure.record_seq) {
    root = data.report.failure.record_seq;
  }
  const slice = new Set(data.slice ? data.slice.seqs : []);
  return { lanes, records, bySeq, end, messages, drops, bands, root, slice };
}

// firstAtOrAfter returns the index of the first record with at >= t, or records.length. Sequence
// mode places times with it (UI-087).
export function firstAtOrAfter(model, t) {
  let lo = 0;
  let hi = model.records.length;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (model.records[mid].at < t) {
      lo = mid + 1;
    } else {
      hi = mid;
    }
  }
  return lo;
}

// NOT_FAULTS are fault.* kinds that end a fault or report on the planner rather than inject one
// (FLT-070), so the Faults panel leaves them out.
const NOT_FAULTS = ["fault.heal", "fault.heal_link", "fault.link_reset", "fault.restart", "fault.resume",
  "fault.error", "fault.suppressed", "fault.target", "fault.skip", "fault.recover"];

// listFaults returns the injected faults in record order, each with its fault band when it opened
// one: {seq, at, text, band}.
export function listFaults(model) {
  const bands = new Map();
  for (const b of model.bands) {
    if (b.type === "fault") {
      bands.set(b.seq, b);
    }
  }
  const out = [];
  for (const r of model.records) {
    if (r.kind.startsWith("fault.") && !NOT_FAULTS.includes(r.kind)) {
      out.push({ seq: r.seq, at: r.at, text: r.text, band: bands.get(r.seq) || null });
    }
  }
  return out;
}
