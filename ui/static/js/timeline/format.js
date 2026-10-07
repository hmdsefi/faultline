// Formatting helpers of the run view (ART-077): time labels, tick steps, attr values, banner text.
// DOM-free, so node --test can import it.

const NS_PER_S = 1000000000;

// formatTime formats virtual nanoseconds like kernel.Time.String: seconds with nine decimals.
export function formatTime(ns) {
  const sign = ns < 0 ? "-" : "";
  const abs = Math.abs(ns);
  const whole = Math.floor(abs / NS_PER_S);
  const frac = String(abs % NS_PER_S).padStart(9, "0");
  return sign + String(whole) + "." + frac + "s";
}

// tickStep returns the smallest 1-2-5 x 10^k nanosecond step that splits span into at most
// maxTicks intervals (ART-079 item 3).
export function tickStep(span, maxTicks) {
  const limit = Math.max(1, maxTicks);
  let pow = 1;
  for (;;) {
    for (const m of [1, 2, 5]) {
      const step = m * pow;
      if (span / step <= limit) {
        return step;
      }
    }
    pow *= 10;
  }
}

// tickDecimals returns the fewest decimals (0-9) of seconds that distinguish ticks step ns apart.
export function tickDecimals(step) {
  for (let d = 0; d < 9; d++) {
    const unit = 10 ** (9 - d);
    if (step % unit === 0) {
      return d;
    }
  }
  return 9;
}

// tickLabel formats ns as seconds with the given number of decimals, for example "1.25s".
export function tickLabel(ns, decimals) {
  const sign = ns < 0 ? "-" : "";
  const abs = Math.abs(ns);
  const whole = String(Math.floor(abs / NS_PER_S));
  if (decimals === 0) {
    return sign + whole + "s";
  }
  const frac = String(abs % NS_PER_S).padStart(9, "0").slice(0, decimals);
  return sign + whole + "." + frac + "s";
}

// formatAttrValue writes an attr value bare when it is a simple token, else JSON-quoted, as
// timeline.txt does (ART-040).
export function formatAttrValue(v) {
  return /^[A-Za-z0-9._:/@%+,#|-]+$/.test(v) ? v : JSON.stringify(v);
}

// laneLabel returns "name [tags]" (ART-079 item 2).
export function laneLabel(lane) {
  return lane.tags.length > 0 ? lane.name + " [" + lane.tags.join(",") + "]" : lane.name;
}

// recordNode returns "name#inc" for a node record and "global" for node 0.
export function recordNode(model, r) {
  if (r.node === 0) {
    return "global";
  }
  const lane = model.lanes.find((l) => l.id === r.node);
  return (lane ? lane.name : "node" + r.node) + "#" + r.inc;
}

// windowBanner returns the banner of a truncated timeline (ART-080).
export function windowBanner(data, included) {
  const w = data.window;
  return "Showing " + included + " of " + data.total + " records: every record from t=" +
    formatTime(w.from_ns) + " (#" + w.from_seq + ") to the end, plus " + w.extra +
    " earlier fault, check, node-lifecycle and causal-slice records. The complete trace is in trace.jsonl and timeline.txt.";
}
