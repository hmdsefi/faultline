// Run view model (ART-078): parses the embedded trace text and derives lanes, messages, drops and
// bands. Pure and DOM-free.

// parseTrace parses trace.jsonl text into {header, records}.
export function parseTrace(text) {
  const lines = [];
  for (const raw of text.split("\n")) {
    const line = raw.endsWith("\r") ? raw.slice(0, -1) : raw;
    if (line !== "") {
      lines.push(line);
    }
  }
  if (lines.length === 0) {
    return { header: { nodes: [] }, records: [] };
  }
  const header = JSON.parse(lines[0]);
  const records = [];
  for (let i = 1; i < lines.length; i++) {
    const o = JSON.parse(lines[i]);
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

// DURABLE are the fault record kinds that open a fault band (FLT-011, ART-078).
const DURABLE = ["fault.partition", "fault.isolate", "fault.cut", "fault.link", "fault.crash", "fault.pause"];

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

// buildModel derives the run view model from a timeline data object (ART-078).
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
