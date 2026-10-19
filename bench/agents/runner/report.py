"""The CSV and the results page, built from <tier>/runs/*/run.json."""
import csv
import glob
import os
import re
import statistics

import common
import measures

COLUMNS = ["set", "run_id", "arm", "bug", "run", "status", "fixed", "fixed_round", "rounds", "wrong_fixes",
           "round_end", "hidden_exit", "cost_usd", "round_cost_usd", "weighted_tokens", "processed_tokens",
           "output_tokens", "weighted_tokens_transcripts", "turns", "wall_s", "go_test_calls",
           "test_runs_after_edit", "failing_test_runs_after_edit", "undo_check_runs", "regression_test", "new_tests",
           "visible_checks_changed", "denials", "isolation", "forbidden_path", "budget_out", "timed_out", "model",
           "cli_version", "faultline_source", "session_ids", "error"]
TOKEN_GAP_PCT = 2.0


def _round_streams(run_dir):
    """stream-r<k>.jsonl of a run folder, in round order."""
    found = glob.glob(os.path.join(run_dir, "stream-r*.jsonl"))
    return sorted(found, key=lambda f: int(re.search(r"stream-r(\d+)\.jsonl$", f).group(1)))


def upgrade(r, run_dir):
    """A run.json written before 2026-10-17 (no weighted_tokens_transcripts key): its token fields
    came from the transcripts. Moves that figure to weighted_tokens_transcripts, takes the tokens
    from its model_usage and counts the test runs after the first edit from its streams."""
    if "weighted_tokens_transcripts" in r:
        return r
    r = dict(r, weighted_tokens_transcripts=r.get("weighted_tokens"))
    if r.get("model_usage"):
        r["weighted_tokens"], r["processed_tokens"], r["output_tokens"] = measures.model_usage_tokens(r["model_usage"])
    else:
        r["weighted_tokens"] = r["processed_tokens"] = r["output_tokens"] = None
    streams = _round_streams(run_dir)
    if streams and r.get("status") == "done":
        r["test_runs_after_edit"], r["failing_test_runs_after_edit"] = measures.test_runs_after_edit(streams)
        r["undo_check_runs"] = measures.undo_check_runs(streams)
    return r


def token_gap(r):
    """How far apart, in percent of the transcripts' figure, the two weighted token counts are; None
    when one is missing."""
    w, t = r.get("weighted_tokens"), r.get("weighted_tokens_transcripts")
    if not isinstance(w, (int, float)) or not isinstance(t, (int, float)) or t <= 0:
        return None
    return 100.0 * abs(w - t) / t


def _load(pattern):
    recs = []
    for f in sorted(glob.glob(pattern)):
        r = common.read_json(f)
        if r and r.get("set"):
            recs.append(upgrade(r, os.path.dirname(f)))
    return recs


def load(cfg):
    return _load(os.path.join(cfg["paths"]["runs"], "*", "run.json"))


def load_retired(cfg):
    """Attempts moved aside when their run was started again; their spend still counts."""
    return _load(os.path.join(cfg["paths"]["runs"], "_retired", "*", "run.json"))


def _cell(v):
    if isinstance(v, bool):
        return "yes" if v else "no"
    if isinstance(v, list):
        return "; ".join(str(x) for x in v)
    if isinstance(v, float):
        return "%.4f" % v if v < 10 else "%.1f" % v
    return "" if v is None else str(v)


def _spread(vals, fmt):
    vals = [v for v in vals if isinstance(v, (int, float))]
    if not vals:
        return "-"
    return "%s (%s to %s)" % (fmt(statistics.median(vals)), fmt(min(vals)), fmt(max(vals)))


def _usd(v):
    return "$%.2f" % v


def _tok(v):
    return "%.1fM" % (v / 1e6)


def _min(v):
    return "%.1f" % (v / 60)


def _num(v):
    return "%g" % v


def write(cfg):
    """Writes results/<set>.csv and results/<set>.md for every set; returns the written paths."""
    recs, retired = load(cfg), load_retired(cfg)
    out_dir = cfg["paths"]["results"]
    os.makedirs(out_dir, exist_ok=True)
    order = {a: i for i, a in enumerate(cfg["arms"] + cfg.get("screen_arms", []))}
    paths = []
    for set_name in sorted({r["set"] for r in recs}):
        rows = sorted((r for r in recs if r["set"] == set_name),
                      key=lambda r: (r["bug"], order.get(r["arm"], 9), r["run"]))
        cpath = os.path.join(out_dir, set_name + ".csv")
        with open(cpath, "w", newline="") as f:
            w = csv.writer(f)
            w.writerow(COLUMNS)
            for r in rows:
                w.writerow([_cell(r.get(c)) for c in COLUMNS])
        mpath = os.path.join(out_dir, set_name + ".md")
        with open(mpath, "w") as f:
            f.write(page(cfg, set_name, rows, [r for r in retired if r["set"] == set_name]))
        paths += [cpath, mpath]
    return paths


def _ordered(first, seen):
    """first, then what else seen holds, sorted: the screen keeps its four candidates in the page
    after the config lists the three it kept."""
    return list(first) + sorted(set(seen) - set(first))


def screen_table(cfg, done):
    """Candidate by version: fixed k of n and the total cost; the last column is filled by hand."""
    L = ["## Screen", "",
         "A candidate passes when at most 1 of its runs, both versions together, fixed it. Touched: runs "
         "whose diff changed the defect's function, filled in by hand.", "",
         "| Candidate | Version | Fixed | Cost | Touched the defect's function |", "|---|---|---|---|---|"]
    for bug in _ordered(cfg["bugs"], {r["bug"] for r in done}):
        for v in ("plain", "faultline"):
            g = [r for r in done if r["bug"] == bug and r.get("version") == v]
            if g:
                L.append("| %s | %s | %d of %d | %s |  |" % (bug, v, sum(1 for r in g if r.get("fixed")), len(g),
                                                           _usd(sum(r.get("cost_usd") or 0 for r in g))))
    return L + [""]


def page(cfg, set_name, rows, retired=()):
    done = [r for r in rows if r.get("status") == "done"]
    other = [r for r in rows if r.get("status") != "done"]
    L = ["# leasekv results, tier %s: %s" % (cfg["tier"], set_name), ""]
    L.append("Built by `bench.py report` from `runs/`. Model: %s. CLI: %s. faultline: %s. Spent: $%.2f." % (
        ", ".join(sorted({r.get("model", "?") for r in rows})),
        ", ".join(sorted({r.get("cli_version", "?") for r in rows})),
        ", ".join(sorted({r.get("faultline_source", "-") for r in rows if r.get("version") == "faultline"})) or "-",
        sum(r.get("cost_usd") or 0 for r in rows)))
    L += ["", "Spent on retired attempts (`runs/_retired/`, %d): $%.2f." % (
        len(retired), sum(r.get("cost_usd") or 0 for r in retired))]
    L += [""] + (screen_table(cfg, done) if set_name == "screen" else [])
    L += ["## Summary per bug and arm", "",
          "Test runs after edit: `go test` calls after the main session's first edit of a non-test `.go` "
          "file (an edit tool or a Bash write); failing: those whose result shows a failing test or a nonzero "
          "exit. Both leave out undo checks: test runs with the change stashed or checked out, which the "
          "Every run table counts as Undo.", "",
          "| Bug | Arm | Fixed | Cost, median (min to max) | Weighted tokens | Wall time, min | Rounds | "
          "Test runs after edit | Failing after edit |",
          "|---|---|---|---|---|---|---|---|---|"]
    for bug in _ordered(cfg["bugs"], {r["bug"] for r in done}):
        for arm in _ordered(common.set_arms(cfg, set_name), {r["arm"] for r in done}):
            g = [r for r in done if r["bug"] == bug and r["arm"] == arm]
            if not g:
                continue
            L.append("| %s | %s | %d of %d | %s | %s | %s | %s | %s | %s |" % (
                bug, arm, sum(1 for r in g if r.get("fixed")), len(g),
                _spread([r.get("cost_usd") for r in g], _usd), _spread([r.get("weighted_tokens") for r in g], _tok),
                _spread([r.get("wall_s") for r in g], _min), _spread([r.get("rounds") for r in g], _num),
                _spread([r.get("test_runs_after_edit") for r in g], _num),
                _spread([r.get("failing_test_runs_after_edit") for r in g], _num)))
    L += ["", "## Every run", "",
          "| Run | Fixed | Round | Wrong fixes | Cost | Weighted | Output | Turns | Wall, min | go test | "
          "Tests after edit | Failing | Undo | Regression test | Visible checks changed | Denials | Isolation | "
          "Forbidden path | Budget out |",
          "|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|"]
    for r in done:
        L.append("| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |" % (
            r["run_id"], _cell(r.get("fixed")), _cell(r.get("fixed_round")) or "-", r.get("wrong_fixes", 0),
            _usd(r.get("cost_usd") or 0), _tok(r.get("weighted_tokens") or 0), _cell(r.get("output_tokens")) or "-",
            r.get("turns", 0), _min(r.get("wall_s") or 0), r.get("go_test_calls", 0),
            _cell(r.get("test_runs_after_edit")) or "-", _cell(r.get("failing_test_runs_after_edit")) or "-",
            _cell(r.get("undo_check_runs")) or "-", r.get("regression_test", "-"), _cell(r.get("visible_checks_changed")) or "none", r.get("denials", 0),
            str(r.get("isolation", "")).replace("|", "/"), _cell(r.get("forbidden_path")), _cell(r.get("budget_out"))))
    gaps = [(r["run_id"], r.get("weighted_tokens"), r.get("weighted_tokens_transcripts"), token_gap(r))
            for r in done if (token_gap(r) or 0) > TOKEN_GAP_PCT]
    L += ["", "## Token cross-check", "",
          "Weighted tokens come from the CLI's `modelUsage`; `weighted_tokens_transcripts` is the runner's "
          "count over the transcripts. Runs where the two differ by more than %g %%:" % TOKEN_GAP_PCT, ""]
    L += ["- %s: modelUsage %d, transcripts %d (%.1f %%)" % g for g in gaps] or ["- none"]
    if other:
        L += ["", "## Runs not counted", "", "| Run | Status | Reason |", "|---|---|---|"]
        for r in other:
            reason = r.get("error") or r.get("isolation") or ""
            L.append("| %s | %s | %s |" % (r["run_id"], r.get("status"), str(reason).replace("|", "/")[:200]))
    L += ["", "## Where faultline did not help", ""]
    notes = os.path.join(cfg["paths"]["results"], set_name + "-notes.md")
    if os.path.exists(notes):
        L.append(common.read_text(notes).strip())
    else:
        L.append("Not written yet. Put the hand-written section in `results/%s-notes.md`; "
                 "`bench.py report` copies it here." % set_name)
    return "\n".join(L) + "\n"
