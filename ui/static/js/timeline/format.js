// Formatting helpers of the run view (ART-077): time labels, tick steps, counts, lane labels, the
// window banner and the theme cycle. DOM-free, so node --test can import it.

const NS_PER_S = 1000000000;
const NS_PER_MS = 1000000;

// formatTime formats integer virtual nanoseconds like kernel.Time.String: seconds with nine
// decimals.
export function formatTime(ns) {
  const sign = ns < 0 ? "-" : "";
  const abs = Math.abs(ns);
  const whole = Math.floor(abs / NS_PER_S);
  const frac = String(abs % NS_PER_S).padStart(9, "0");
  return sign + String(whole) + "." + frac + "s";
}

// shortTime formats ns rounded to the millisecond, for chips, spans and the overview track.
export function shortTime(ns) {
  return tickLabel(Math.round(ns / NS_PER_MS) * NS_PER_MS, 3);
}

// tickStep returns the smallest 1-2-5 x 10^k step that splits span into at most maxTicks
// intervals (ART-079 item 3). A span that is not finite, or a maxTicks that is not a number,
// would never end the search, so it throws instead (UI §7.3).
export function tickStep(span, maxTicks) {
  if (!Number.isFinite(span) || typeof maxTicks !== "number" || Number.isNaN(maxTicks)) {
    throw new Error("tickStep: span must be finite and maxTicks a number, got " + span + " and " + maxTicks);
  }
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

// tickLabel formats integer ns as seconds with the given number of decimals, for example "1.25s".
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

// formatCount writes n with thousands separators. Seqs, IDs and nanoseconds stay raw: they are
// copied into searches and code.
export function formatCount(n) {
  return String(n).replace(/\B(?=(\d{3})+(?!\d))/g, ",");
}

// counted returns "1 record", "1,949 records".
export function counted(n, noun) {
  return formatCount(n) + " " + noun + (n === 1 ? "" : "s");
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

// THEMES is the order the theme button and the t key cycle through (UI-178).
export const THEMES = ["system", "light", "dark"];

// nextTheme returns the theme after t; an unknown value starts the cycle again.
export function nextTheme(t) {
  return THEMES[(THEMES.indexOf(t) + 1) % THEMES.length];
}
