"""Per-run measures: tokens, go test calls, regression tests, visible checks, forbidden paths."""
import filecmp
import glob
import json
import os
import re
import shutil

import common
import stream

# go test in any form: after a separator, a quote or a path (/usr/local/go/bin/go), and with go -C dir
GO_TEST = re.compile(r"(^|[\s;&|(`'\"/])go(\s+-C\s*\S+)?\s+test\b")
TEST_NAME = re.compile(r"^(Test|Fuzz|Example|Benchmark)\w*$")
EDIT_TOOLS = ("Edit", "Write", "MultiEdit")
# what a failing go test prints; a nonzero exit shows as is_error on the tool result
FAILED_TEST = re.compile(r"--- FAIL|FAIL\t|violated|\bpanic:")
# a Bash command that writes a file: a Python write, sed -i, perl -i, a redirect or tee into a .go file
BASH_WRITE = re.compile(r"\bsed\s+(-\w+\s+)*-i|\bperl\s+-\w*i|\.write\(|\bwrite_text\(|>\s*[\w./-]+\.go\b|"
                        r"\btee\s+(-a\s+)?[\w./-]+\.go\b")
GO_FILE = re.compile(r"[\w./-]+\.go\b")
# a deliberate undo-the-fix check: the change is stashed or checked out before the test runs, until
# git stash pop or apply (or the next edit) puts it back
UNDO_START = re.compile(r"\bgit\s+(stash(\s+(push|save)\b|\s+-|\s*$|\s*[;&|)])|checkout\s+(\S+\s+)?--\s+\S+\.go|"
                        r"restore\s+(\S+\s+)*\S+\.go)")
UNDO_END = re.compile(r"\bgit\s+stash\s+(pop|apply)\b")


def bash_writes_service(command):
    """Whether a Bash command writes a non-test .go file (the edit tools are the usual way)."""
    return bool(BASH_WRITE.search(command)) and any(not p.endswith("_test.go") for p in GO_FILE.findall(command))


def transcript_files(tdir):
    return sorted(glob.glob(os.path.join(tdir, "*.jsonl")) + glob.glob(os.path.join(tdir, "*", "subagents", "*.jsonl")))


def transcript_tokens(path, seen):
    """(weighted, processed, output) tokens of one transcript file. Each API call counts once, by
    (message id, request id), also when an earlier file in seen held it:

      processed = input + cache writes + cache reads + output
      weighted  = input + 1.25 x cache writes + 0.1 x cache reads + 5 x output

    Weighted tokens follow the price of each kind of token, so they track cost better; processed
    tokens are mostly cache reads."""
    w, p, out = 0.0, 0.0, 0
    for e in stream.events(path):
        m = stream.message_of(e)
        u = m.get("usage")
        key = (m.get("id"), e.get("requestId"))
        if not isinstance(u, dict) or not u or key in seen:
            continue
        seen.add(key)
        i, cw = u.get("input_tokens") or 0, u.get("cache_creation_input_tokens") or 0
        cr, o = u.get("cache_read_input_tokens") or 0, u.get("output_tokens") or 0
        w += i + 1.25 * cw + 0.1 * cr + 5 * o
        p += i + cw + cr + o
        out += o
    return w, p, out


def tokens(tdir):
    """(weighted, processed, output) tokens of a run's transcripts, the main session's and its
    subagents', each API call counted once (transcript_tokens). Weighted and processed are summed
    per file, then over the files, and rounded at the end."""
    seen, w, p, out = set(), 0.0, 0.0, 0
    for f in transcript_files(tdir):
        fw, fp, fo = transcript_tokens(f, seen)
        w, p, out = w + fw, p + fp, out + fo
    return round(w), round(p), out


def model_usage_tokens(model_usage):
    """(weighted, processed, output) tokens of a modelUsage-shaped dict, every model summed. The
    runner's model_usage is the sum of each round's difference of the CLI's running totals, so it
    holds the subagents too. Weighted is rounded down, in integer arithmetic (20 x weighted)."""
    w20 = p = out = 0
    for mu in (model_usage or {}).values():
        i, cw, cr, o = (int(round(float(mu.get(k) or 0))) for k in
                        ("inputTokens", "cacheCreationInputTokens", "cacheReadInputTokens", "outputTokens"))
        w20 += 20 * i + 25 * cw + 2 * cr + 100 * o
        p += i + cw + cr + o
        out += o
    return w20 // 20, p, out


def _result_text(content):
    """The text of a tool_result block's content: a string or a list of text blocks."""
    if isinstance(content, list):
        return "\n".join(str(b.get("text", "")) if isinstance(b, dict) else str(b) for b in content)
    return "" if content is None else str(content)


def go_test_runs_after_edit(stream_files):
    """(command, failed, result text) of each test run after the main session's first edit of a
    non-test .go file, undo checks left out (undo_check_runs counts them)."""
    return _runs_after_edit(stream_files)[0]


def undo_check_runs(stream_files):
    """Test runs after the first edit that ran with the change stashed or checked out: the agent
    checking that its test fails without the fix. They are not wrong fixes."""
    return len(_runs_after_edit(stream_files)[1])


def _runs_after_edit(stream_files):
    """(runs, undo runs), each a list of (command, failed, result text), after the main session's
    first edit of a non-test .go file: an Edit, Write or MultiEdit, or a Bash command that writes
    one (bash_writes_service). From the round streams in order. A test run is a Bash call that runs
    go test and was not denied; it failed when its tool result is an error (nonzero exit) or shows
    --- FAIL, FAIL<tab>, violated or panic:. An undo run is a test run in a command that stashes or
    checks out the change (UNDO_START), or one that follows such a command before git stash pop or
    apply or the next edit. An edit that was denied or errored does not count; subagent events
    (parent_tool_use_id set) do not count."""
    evs = [e for f in stream_files for e in stream.events(f)]
    results, denied = {}, set()
    for e in evs:
        if e.get("type") == "system" and e.get("subtype") == "permission_denied":
            denied.add(e.get("tool_use_id"))
        if e.get("type") == "result":
            denied |= {d.get("tool_use_id") for d in e.get("permission_denials") or []}
        content = stream.message_of(e).get("content")
        if e.get("type") != "user" or not isinstance(content, list):
            continue
        for b in content:
            if isinstance(b, dict) and b.get("type") == "tool_result":
                results[b.get("tool_use_id")] = (bool(b.get("is_error")), _result_text(b.get("content")))
    main = [e for e in evs if not e.get("parent_tool_use_id")]
    edited, undo, runs, undo_runs = False, False, [], []
    for tid, name, inp in stream.tool_uses(main):
        if tid in denied:
            continue
        failed_call = results.get(tid, (False, ""))[0]
        path, command = str(inp.get("file_path", "")), str(inp.get("command", ""))
        tool_edit = name in EDIT_TOOLS and path.endswith(".go") and not path.endswith("_test.go")
        bash_edit = name == "Bash" and bash_writes_service(command)
        if (tool_edit or bash_edit) and not failed_call:
            edited, undo = True, False
        if not edited or name != "Bash":
            continue
        start = UNDO_START.search(command)
        if GO_TEST.search(command):
            err, text = results.get(tid, (False, ""))
            run = (command, err or bool(FAILED_TEST.search(text)), text)
            (undo_runs if undo or start else runs).append(run)
        if start:
            undo = not UNDO_END.search(command, start.end())
        elif UNDO_END.search(command):
            undo = False
    return runs, undo_runs


def test_runs_after_edit(stream_files):
    """(test_runs_after_edit, failing_test_runs_after_edit) of a run, undo checks left out."""
    runs = go_test_runs_after_edit(stream_files)
    return len(runs), sum(1 for _, failed, _ in runs if failed)


def go_test_calls(stream_files, tdir):
    """Bash tool calls that run go test, from the streams and every transcript, counted once by id."""
    seen = {}
    for f in list(stream_files) + transcript_files(tdir):
        for tid, name, inp in stream.tool_uses(stream.events(f)):
            if name == "Bash" and GO_TEST.search(str(inp.get("command", ""))):
                seen[tid or (f, len(seen))] = inp.get("command")
    return len(seen)


# A character that continues a path segment. After a pattern it makes a longer name
# (/tmp/faultline-bench is not /tmp/faultline), so the pattern does not match there.
PATH_SEGMENT = r"[\w.@+~%=#-]"


def spellings(s):
    """s as text holds it: plain, JSON-escaped (a quote as \\") and with / escaped as \\/."""
    j = json.dumps(s, ensure_ascii=False)[1:-1]
    return sorted({s, j, j.replace("/", "\\/")})


def forbidden_hits(files, patterns, allow=(), boundary=True):
    """'<file name>: <pattern>' for each pattern a file holds, in any of its spellings. With
    boundary, a pattern counts only at a path boundary: followed by /, the end of the text or a
    character that cannot continue a path segment, such as a quote, a space, a backslash or ).
    Without it (the probe's token), any occurrence counts. Strings in allow (the run's own
    transcript folder) are removed first."""
    end = "(?!" + PATH_SEGMENT + ")" if boundary else ""
    regs = [(p, re.compile("(?:" + "|".join(re.escape(x) for x in spellings(p)) + ")" + end)) for p in patterns]
    hits = []
    for f in files:
        try:
            text = common.read_text(f)
        except OSError:
            continue
        for a in sorted(allow, key=len, reverse=True):
            for x in spellings(a):
                text = text.replace(x, "")
        for p, r in regs:
            if r.search(text):
                hits.append(os.path.basename(f) + ": " + p)
    return sorted(set(hits))


def tree_files(d):
    """Every file under d, .git excluded."""
    out = []
    for root, dirs, files in os.walk(d):
        dirs[:] = [x for x in dirs if x != ".git"]
        out += [os.path.join(root, f) for f in files]
    return sorted(out)


# ---- regression tests ----

def test_names(d, env, timeout):
    rc, out = common.sh(["go", "test", "-list", ".", "./..."], cwd=d, env=env, timeout=timeout)
    if rc != 0:
        return None, out
    return {l.strip() for l in out.splitlines() if TEST_NAME.match(l.strip())}, out


def _agent_test_files(original, final):
    """Relative paths of *_test.go files and testdata files that are new or changed in final."""
    out = []
    for root, dirs, files in os.walk(final):
        dirs[:] = [d for d in dirs if d != ".git"]
        rel_root = os.path.relpath(root, final)
        in_testdata = "testdata" in rel_root.split(os.sep)
        for name in files:
            if not (name.endswith("_test.go") or in_testdata):
                continue
            rel = os.path.normpath(os.path.join(rel_root, name))
            o = os.path.join(original, rel)
            if not os.path.exists(o) or not filecmp.cmp(o, os.path.join(final, rel), shallow=False):
                out.append(rel)
    return sorted(out)


def _classify(rc, out):
    if rc == 124:
        return "timeout"
    if rc == 0:
        return "pass"
    if "[build failed]" in out or "[setup failed]" in out or "build failed" in out:
        return "build"
    return "fail"


def _run_new(d, new, runs, env, tmp, t, log_prefix):
    pattern = "^(" + "|".join(new) + ")$"
    kinds = []
    for i in range(runs):
        rc, out = common.sh(["go", "test", "-count=1", "-run", pattern, "./..."], cwd=d,
                            env=dict(env, TMPDIR=tmp), timeout=t)
        with open("%s-%d.log" % (log_prefix, i + 1), "w") as f:
            f.write(out)
        kinds.append(_classify(rc, out))
    return kinds


def regression(cfg, version, original, final, scratch, env, log_prefix):
    """Runs the agent's new tests against the original variant with the agent's test files and,
    when one of those runs fails, the same number of times against a copy of the final workspace.

    Returns (value, new test names). value: none; protects (a run on the original failed and every
    run on the final workspace passed); fails-on-both (a run failed on each); passes; build-error;
    timeout; final-build-error or original-build-error when that tree does not list its tests."""
    t = cfg.get("go_test_timeout_s", 900)
    env = dict(env, GOWORK="off")
    final_names, out = test_names(final, env, t)
    if final_names is None:
        common.write_text(log_prefix + "-list.log", out)
        return "final-build-error", []
    orig_names, out = test_names(original, env, t)
    if orig_names is None:
        common.write_text(log_prefix + "-list.log", out)
        return "original-build-error", []
    new = sorted(n for n in final_names - orig_names if n.startswith("Test"))
    if not new:
        return "none", []
    mod = os.path.join(scratch, "m")
    common.copytree(original, mod, ignore=shutil.ignore_patterns(".git"))
    for rel in _agent_test_files(original, final):
        dst = os.path.join(mod, rel)
        os.makedirs(os.path.dirname(dst), exist_ok=True)
        shutil.copy2(os.path.join(final, rel), dst)
    tmp = os.path.join(scratch, "tmp")
    os.makedirs(tmp, exist_ok=True)
    runs = 1 if version == "faultline" else int(cfg.get("regression_plain_runs", 5))
    kinds = _run_new(mod, new, runs, env, tmp, t, log_prefix)
    if "fail" in kinds:
        fin = os.path.join(scratch, "f")
        common.copytree(final, fin, ignore=shutil.ignore_patterns(".git"))
        on_fix = _run_new(fin, new, runs, env, tmp, t, log_prefix + "-final")
        if all(k == "pass" for k in on_fix):
            return "protects", new
        return ("timeout" if "timeout" in on_fix and "fail" not in on_fix else "fails-on-both"), new
    if all(k == "build" for k in kinds):
        return "build-error", new
    if "timeout" in kinds:
        return "timeout", new
    return "passes", new


# ---- visible checks ----

def strip_comments(src):
    out, i, n = [], 0, len(src)
    while i < n:
        c = src[i]
        if src.startswith("//", i):
            j = src.find("\n", i)
            i = n if j < 0 else j
        elif src.startswith("/*", i):
            j = src.find("*/", i + 2)
            i = n if j < 0 else j + 2
            out.append(" ")
        elif c in "\"'`":
            j = i + 1
            while j < n and src[j] != c:
                if c != "`" and src[j] == "\\":
                    j += 1
                elif c != "`" and src[j] == "\n":
                    break
                j += 1
            out.append(src[i:j + 1])
            i = j + 1
        else:
            out.append(c)
            i += 1
    return "".join(out)


DECL = re.compile(r"^(func|var|const|type)\b")
FUNC = re.compile(r"^func\s*(\(([^)]*)\))?\s*([A-Za-z_]\w*)")
SINGLE = re.compile(r"^(var|const|type)\s+([A-Za-z_]\w*(\s*,\s*[A-Za-z_]\w*)*)")
IDENT = re.compile(r"[A-Za-z_]\w*")


def decls(src):
    """Top-level declarations of a Go file: {key: (normalized text, declared names)}."""
    chunks, cur = [], None
    for line in strip_comments(src).splitlines():
        if DECL.match(line):
            cur = [line]
            chunks.append(cur)
        elif re.match(r"^(import|package)\b", line):
            cur = None
        elif cur is not None:
            cur.append(line)
    out = {}
    for ch in chunks:
        head, text = ch[0], " ".join(" ".join(ch).split())
        m = FUNC.match(head)
        if m:
            recv = IDENT.findall(re.sub(r"\[.*\]", "", m.group(2) or ""))
            key = (recv[-1] + "." if recv else "") + m.group(3)
            if key == "init":  # a file may hold several
                key = "init#%d" % (sum(1 for k in out if k.startswith("init#")) + 1)
            out[key] = (text, [key])
            continue
        m = SINGLE.match(head)
        if m:
            names = [x.strip() for x in m.group(2).split(",")]
        else:
            names = [IDENT.match(l.strip()).group(0) for l in ch[1:]
                     if re.match(r"^\t[A-Za-z_]", l) and IDENT.match(l.strip())]
            names = [x for x in names if x not in ("func", "var", "const", "type")]
        key = head.split()[0] + " " + ",".join(names)
        out[key] = (text, names)
    return out


RUNS_FIRST = re.compile(r"^(TestMain|init#\d+)$")


def _test_decls(d, rel_dir):
    """Declarations of a directory's test files. A package may hold many init functions: they are
    keyed init#1, init#2, ... in file order."""
    out, inits = {}, 0
    for f in sorted(glob.glob(os.path.join(d, rel_dir, "*_test.go"))):
        for k, v in decls(common.read_text(f)).items():
            if k.startswith("init#"):
                inits += 1
                k = "init#%d" % inits
            out[k] = v
    return out


def visible_checks(original, final):
    """Names of the original test functions, and the test-file code they reach, that the final
    workspace changed or removed. Comments and whitespace do not count."""
    dirs = set()
    for root, ds, files in os.walk(original):
        ds[:] = [x for x in ds if x != ".git" and x != "testdata"]
        if any(f.endswith("_test.go") for f in files):
            dirs.add(os.path.relpath(root, original))
    changed = []
    for rel in sorted(dirs):
        o, f = _test_decls(original, rel), _test_decls(final, rel)
        by_name = {}
        for key, (_, names) in o.items():
            for n in names:
                by_name[n.split(".")[-1]] = by_name.get(n.split(".")[-1], set()) | {key}
        todo = [k for k in o if re.match(r"^Test\w*$", k)]
        reach = set()
        while todo:
            k = todo.pop()
            if k in reach:
                continue
            reach.add(k)
            for ident in set(IDENT.findall(o[k][0])):
                todo.extend(by_name.get(ident, ()))
        prefix = "" if rel == "." else rel + "/"
        for k in sorted(reach):
            if k not in f:
                changed.append(prefix + k + " (removed)")
            elif f[k][0] != o[k][0]:
                changed.append(prefix + k)
        # TestMain and init run whatever the tests reach: a new or changed one changes the checks
        for k in sorted(set(f) | set(o)):
            if not RUNS_FIRST.match(k) or k in reach:
                continue
            if k not in o:
                changed.append(prefix + k + " (new)")
            elif k not in f:
                changed.append(prefix + k + " (removed)")
            elif f[k][0] != o[k][0]:
                changed.append(prefix + k)
    return changed
