"""Money. On --resume the CLI reports running totals for the session (total_cost_usd, modelUsage,
duration_api_ms), so a round's spend is the new total minus the total before it. While a round
streams, Meter estimates its spend from each assistant message's usage, priced per model with
rates calibrated from the run's own results (no price table)."""
import glob
import os

import stream

# Weighted tokens: input + 1.25 x cache writes + 0.1 x cache reads + 5 x output.
WEIGHTS = {"input_tokens": 1.0, "cache_creation_input_tokens": 1.25, "cache_read_input_tokens": 0.1,
           "output_tokens": 5.0}
# modelUsage field -> usage field
MODEL_USAGE = {"inputTokens": "input_tokens", "cacheCreationInputTokens": "cache_creation_input_tokens",
               "cacheReadInputTokens": "cache_read_input_tokens", "outputTokens": "output_tokens"}
USAGE_KEYS = list(MODEL_USAGE) + ["costUSD"]


def weighted(usage):
    return sum(w * float(usage.get(k) or 0) for k, w in WEIGHTS.items())


def model_weighted(mu):
    return weighted({u: mu.get(k) or 0 for k, u in MODEL_USAGE.items()})


def rates_of(model_usage):
    """Dollars per weighted token per model: costUSD over the model's weighted tokens."""
    out = {}
    for model, mu in (model_usage or {}).items():
        w = model_weighted(mu)
        if w > 0 and float(mu.get("costUSD") or 0) > 0:
            out[model] = float(mu["costUSD"]) / w
    return out


def _usage(model_usage):
    return {m: {k: float(mu.get(k) or 0) for k in USAGE_KEYS} for m, mu in (model_usage or {}).items()}


ZERO = {"cost": 0.0, "api_ms": 0, "usage": {}}


def totals_of_result(res):
    return {"cost": float(res.get("total_cost_usd") or 0), "api_ms": int(res.get("duration_api_ms") or 0),
            "usage": _usage(res.get("modelUsage"))}


def totals_of_cost_state(rec):
    return {"cost": float(rec.get("totalCostUSD") or 0), "api_ms": int(rec.get("totalAPIDuration") or 0),
            "usage": _usage(rec.get("modelUsage"))}


def minus(now, before):
    """The usage of one round: per model and field, the new total minus the one before."""
    out = {}
    for m, mu in now.items():
        b = before.get(m, {})
        d = {k: mu.get(k, 0) - b.get(k, 0) for k in USAGE_KEYS}
        if any(abs(v) > 1e-12 for v in d.values()):
            out[m] = d
    return out


def add(total, part):
    for m, mu in part.items():
        t = total.setdefault(m, {k: 0.0 for k in USAGE_KEYS})
        for k in USAGE_KEYS:
            t[k] = round(t.get(k, 0) + mu.get(k, 0), 6)
    return total


def session_files(projects, sid):
    """The main transcript of session sid and its subagent transcripts."""
    main = glob.glob(os.path.join(projects, "*", sid + ".jsonl"))
    subs = []
    for f in main:
        subs += glob.glob(os.path.join(f[:-len(".jsonl")], "subagents", "*.jsonl"))
    return sorted(main) + sorted(subs)


def last_cost_state(projects, sids):
    """Running totals from the highest cost-state record in the sessions' transcripts, or None."""
    best = None
    for sid in sids:
        for f in glob.glob(os.path.join(projects, "*", sid + ".jsonl")):
            for e in stream.events(f):
                if e.get("type") == "cost-state":
                    t = totals_of_cost_state(e)
                    if best is None or t["cost"] >= best["cost"]:
                        best = t
    return best


class Tail:
    """The new complete JSON lines of a file that is still being written."""

    def __init__(self, path):
        self.path, self.pos, self.buf = path, 0, b""

    def events(self):
        try:
            with open(self.path, "rb") as f:
                f.seek(self.pos)
                chunk = f.read()
        except OSError:
            return []
        self.pos += len(chunk)
        lines = (self.buf + chunk).split(b"\n")
        self.buf = lines.pop()
        return stream.events_from_lines(l.decode("utf-8", "replace") for l in lines)


class Meter:
    """The spend of one round so far. Each assistant message counts once by message id, with each
    usage field at the largest value any copy showed (streamed copies repeat and carry the output
    tokens known when the message started; the transcript copies carry more). Messages of earlier
    rounds (exclude) do not count. A model without a rate is priced at the highest rate seen."""

    def __init__(self, rates=None, top_rate=None, limit=None, exclude=(), projects=None):
        self.rates, self.limit, self.exclude = dict(rates or {}), limit, set(exclude)
        self.top = top_rate if top_rate is not None else max(self.rates.values(), default=None)
        self.projects, self.msgs, self.tails = projects, {}, {}

    def feed(self, evs):
        for e in evs:
            m = stream.message_of(e)
            u, mid = m.get("usage"), m.get("id")
            if e.get("type") != "assistant" or not mid or not isinstance(u, dict) or mid in self.exclude:
                continue
            model, cur = self.msgs.get(mid, (m.get("model") or "?", {}))
            for k in WEIGHTS:
                cur[k] = max(cur.get(k, 0), int(u.get(k) or 0))
            self.msgs[mid] = (model, cur)

    def poll(self, sid):
        """Reads what the session's transcripts (main and subagents) gained since the last poll."""
        if not self.projects or not sid:
            return
        for f in session_files(self.projects, sid):
            if f not in self.tails:
                self.tails[f] = Tail(f)
            self.feed(self.tails[f].events())

    def ids(self):
        return set(self.msgs)

    def usd(self):
        """The estimate in dollars, or None while no model has a rate."""
        if self.top is None:
            return None
        return sum(weighted(u) * self.rates.get(model, self.top) for model, u in self.msgs.values())

    def over(self):
        usd = self.usd()
        return self.limit is not None and usd is not None and usd > self.limit
