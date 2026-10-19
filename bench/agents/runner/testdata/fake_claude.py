#!/usr/bin/env python3
"""A fake claude CLI for the runner tests. It writes a canned stream-json run (init, Bash go test
calls, a result with cost, turns, duration, usage, denials and session id), a transcript with a
cost-state record and a subagent transcript, and edits the workspace. Like the real CLI (2.1.299),
a resumed call reports running totals for the session (total_cost_usd, modelUsage,
duration_api_ms, restored from the transcript's last cost-state record); num_turns and duration_ms
are per call. Each call costs $0.25.

- round 1 fixes kv.go and adds a regression test, unless the prompt holds FAKE:SLOW, in which case
  round 1 makes a wrong change and the resumed round 2 fixes it; the first message also holds an Edit
  of kv.go, and after a wrong change the first go test result fails;
- FAKE:WEAKEN in the prompt also changes the visible check helper;
- FAKE:FORBIDDEN in the prompt adds a Bash call that reads $FAKE_REPO;
- FAKE_MCP=1 lists an MCP server in init and then hangs (the runner must kill it);
- FAKE_MODE: never-fix (every round makes a wrong change, after 0.3 s), hang (starts two sleeping
  grandchildren, writes their pids to $FAKE_PIDS and waits; SIGINT writes a cost-state record of
  $0.10 and exits), spend (round 1 makes a wrong change; the resumed round streams costly messages,
  each twice, until SIGINT), hook (a hook_started event before init), late-hook (one after init),
  cat-runs (the probe's Bash cat calls run: Read deny rules do not cover Bash), read-runs (the
  probe's Read calls run: a CLI that ignores the deny rules), tools-ignored (a CLI that ignores
  --tools and --disallowedTools: init lists every tool and they all run), sub-runs (init lists only
  the allowed tools, but the subagent tool still starts a subagent whose Bash runs);
- --tools and --disallowedTools: like CLI 2.1.299, init lists TOOLS without --tools, else the tools of
  TOOLS + SEARCH that --tools names, less what --disallowedTools names; without Bash a run calls Glob,
  Grep, a Read of the workspace folder (EISDIR, as on the real CLI) and of kv.go, edits kv.go and
  calls no command (the screen's read arms); the settings' Read deny rules cover Read, Glob and
  Grep;
- FAKE_FORK=1: a resumed call reports a new session id (and writes its transcript under it);
- FAKE_MODEL: the model the init message and the usage name, instead of --model.
"""
import fnmatch
import glob
import json
import os
import re
import signal
import subprocess
import sys
import time
import uuid

TOOLS = ("Task", "Bash", "CronCreate", "Edit", "Monitor", "Read", "SendMessage", "Skill", "ToolSearch", "Workflow",
         "Write")
SEARCH = ("Glob", "Grep")  # not in the default list; --tools can name them
USAGE = {"input_tokens": 10, "cache_creation_input_tokens": 100, "cache_read_input_tokens": 1000, "output_tokens": 50}
MU = {"input_tokens": "inputTokens", "cache_creation_input_tokens": "cacheCreationInputTokens",
      "cache_read_input_tokens": "cacheReadInputTokens", "output_tokens": "outputTokens"}


def arg(args, name):
    return args[args.index(name) + 1] if name in args else None


def write(f, obj):
    f.write(json.dumps(obj) + "\n")
    f.flush()


class Session:
    """The transcript of one session and the running totals its cost-state records carry."""

    def __init__(self, cwd, sid, resumed, fork):
        cfg_dir = os.environ.get("CLAUDE_CONFIG_DIR", os.path.expanduser("~/.claude"))
        self.pdir = os.path.join(cfg_dir, "projects", re.sub(r"[^A-Za-z0-9]", "-", cwd))
        os.makedirs(self.pdir, exist_ok=True)
        self.prev = {"cost": 0.0, "api": 0, "usage": {}}
        if resumed:
            for line in open(os.path.join(self.pdir, sid + ".jsonl")):
                e = json.loads(line)
                if e.get("type") == "cost-state":
                    self.prev = {"cost": e["totalCostUSD"], "api": e["totalAPIDuration"], "usage": e["modelUsage"]}
        self.sid = str(uuid.uuid5(uuid.NAMESPACE_URL, sid)) if resumed and fork else sid
        self.tr = open(os.path.join(self.pdir, self.sid + ".jsonl"), "a")
        self.calls = {}  # model -> usage summed over this call's messages

    def count(self, model, usage):
        c = self.calls.setdefault(model, {k: 0 for k in MU})
        for k in MU:
            c[k] += usage.get(k, 0)

    def totals(self, cost, api_ms):
        usage = json.loads(json.dumps(self.prev["usage"]))
        for model, c in self.calls.items():
            t = usage.setdefault(model, {v: 0 for v in MU.values()} | {"costUSD": 0.0})
            for k, v in MU.items():
                t[v] += c[k]
        if self.calls:
            first = sorted(self.calls)[0]
            usage[first]["costUSD"] = round(usage[first]["costUSD"] + cost, 6)
        return round(self.prev["cost"] + cost, 6), self.prev["api"] + api_ms, usage

    def close(self, cost, api_ms):
        total, api, usage = self.totals(cost, api_ms)
        write(self.tr, {"type": "cost-state", "sessionId": self.sid, "totalCostUSD": total, "totalAPIDuration": api,
                        "startTime": 1792171854168, "modelUsage": usage})
        self.tr.close()
        return total, api, usage


def main():
    args = sys.argv[1:]
    if args[:1] == ["--version"]:
        print("2.1.299 (fake)")
        return 0
    if args[:2] == ["auth", "status"]:
        print(json.dumps({"loggedIn": True}))
        return 0
    if os.environ.get("FAKE_LOG"):
        with open(os.environ["FAKE_LOG"], "a") as f:
            f.write(json.dumps(args) + "\n")
    assert args[0] == "-p", "the prompt must sit right after -p"
    prompt, cwd = args[1], os.getcwd()
    sid = arg(args, "--session-id") or arg(args, "--resume")
    resumed = "--resume" in args
    plugin = arg(args, "--plugin-dir")
    model = arg(args, "--model")
    model = os.environ.get("FAKE_MODEL") or (model if model.startswith("claude-") else "claude-" + model)
    mode = os.environ.get("FAKE_MODE", "")
    out = sys.stdout

    def emit(obj):
        write(out, obj)

    plugins = [{"name": "cc-plugin-telemetry", "path": "builtin", "source": "cc-plugin-telemetry@builtin"}]
    skills, agents = ["debug", "simplify"], ["Explore", "general-purpose", "Plan"]
    if plugin:
        plugins.insert(0, {"name": "faultline", "path": plugin, "source": "faultline@inline"})
        skills += ["faultline:faultline", "faultline:pin", "faultline:replay"]
        agents += ["faultline:triage"]
    mcp = [{"name": "stray", "status": "connected"}] if os.environ.get("FAKE_MCP") else []
    off = set((arg(args, "--disallowedTools") or "").split(","))
    only = set(arg(args, "--tools").split(",")) if arg(args, "--tools") else set(TOOLS)
    tools = list(TOOLS) if mode == "tools-ignored" else [t for t in sorted(TOOLS + SEARCH, key=lambda t: (
        t not in ("Task", "Bash"), t)) if t in only and t not in off]
    if mode == "hook":
        emit({"type": "system", "subtype": "hook_started", "hook_name": "SessionStart:startup", "session_id": sid})
    emit({"type": "system", "subtype": "init", "cwd": cwd, "session_id": sid, "mcp_servers": mcp,
          "model": model, "permissionMode": arg(args, "--permission-mode"), "plugins": plugins, "skills": skills,
          "agents": agents, "tools": tools, "claude_code_version": "2.1.299"})
    if mcp or mode == "hook":
        time.sleep(60 if mcp else 10)
        return 0
    if prompt == "/context":
        md = os.path.join(cwd, "CLAUDE.md")
        table = "\n### Memory Files\n\n| Type | Path | Tokens |\n|---|---|---|\n| Project | %s | 1k |\n" % md
        emit({"type": "result", "subtype": "success", "is_error": False, "total_cost_usd": 0, "num_turns": 0,
              "session_id": sid, "result": "## Context Usage\n" + (table if os.path.exists(md) else "")})
        return 0
    if mode == "late-hook":
        emit({"type": "system", "subtype": "hook_started", "hook_name": "PostToolUse:Bash", "session_id": sid})
    s = Session(cwd, sid, resumed, os.environ.get("FAKE_FORK") == "1")
    if prompt.startswith("This is a permissions check.") or prompt == "Reply with the single word OK." \
            or prompt.startswith("Use the Task tool"):
        return probe_reply(args, prompt, s, model, emit, cwd, tools)
    settings = json.loads(arg(args, "--settings") or "{}")
    if mode == "hang":
        return hang(s, model, emit)
    if mode == "spend" and resumed:
        return spend(s, model, emit)

    def record(f, obj):
        f.write(json.dumps(obj) + "\n")

    record(s.tr, {"type": "user", "sessionId": s.sid, "timestamp": "2026-10-16T10:00:00Z",
                  "message": {"role": "user", "content": prompt}})
    cmds = ["go test ./...", "FAULTLINE_SEED=1 go test -run TestLeaseKV ."]
    if "Bash" not in tools:  # a read arm: no command
        cmds = []
    if "FAKE:FORBIDDEN" in prompt and cmds:
        cmds.append("cat %s/go.mod" % os.environ.get("FAKE_REPO", "/nonexistent"))
    wrong = mode in ("never-fix", "spend") or ("FAKE:SLOW" in prompt and not resumed)
    denied_id = None
    for i, c in enumerate(cmds + ["curl https://example.com"] if cmds else []):
        tid = "toolu_" + uuid.uuid4().hex[:12]
        denied_id = tid
        content = [{"type": "tool_use", "id": tid, "name": "Bash", "input": {"command": c}}]
        if i == 0:
            content.insert(0, {"type": "tool_use", "id": "toolu_edit" + tid, "name": "Edit",
                               "input": {"file_path": os.path.join(cwd, "kv.go")}})
        msg = {"id": "msg_" + uuid.uuid4().hex[:12], "role": "assistant", "model": model, "usage": USAGE,
               "content": content}
        s.count(model, USAGE)
        emit({"type": "assistant", "message": msg, "session_id": s.sid})
        record(s.tr, {"type": "assistant", "requestId": "req_" + tid, "sessionId": s.sid, "message": msg})
        if c.startswith("curl"):  # the real CLI's denial event carries a text message
            emit({"type": "system", "subtype": "permission_denied", "tool_name": "Bash", "tool_use_id": tid,
                  "message": "Permission to use Bash has been denied.", "session_id": s.sid})
        if i == 0:
            emit({"type": "user", "message": {"role": "user", "content": [
                {"type": "tool_result", "tool_use_id": "toolu_edit" + tid, "content": "updated"}]}, "session_id": s.sid})
        text, err = ("--- FAIL: TestLeaseKV\nFAIL\texample.com/leasekv\t0.1s", True) if wrong and i == 0 else ("ok", False)
        emit({"type": "user", "message": {"role": "user", "content": [
            {"type": "tool_result", "tool_use_id": tid, "content": text, "is_error": err}]}, "session_id": s.sid})
    denials = [] if cmds else read_arm_calls(s, model, emit, record, tools, cwd, settings, arg(args, "--permission-mode"))
    if "Task" in tools:  # a subagent; a read arm has no subagent tool
        os.makedirs(os.path.join(s.pdir, s.sid, "subagents"), exist_ok=True)
        sub = open(os.path.join(s.pdir, s.sid, "subagents", "agent-%s.jsonl" % uuid.uuid4().hex[:8]), "w")
        smsg = {"id": "msg_" + uuid.uuid4().hex[:12], "role": "assistant", "model": model, "usage": USAGE,
                "content": [{"type": "tool_use", "id": "toolu_" + uuid.uuid4().hex[:12], "name": "Bash",
                             "input": {"command": "cd sub && go test -count=1 ./..."}}]}
        s.count(model, USAGE)
        # usage that modelUsage holds and no transcript records (in the real CLI, subagent output the
        # transcripts undercount): weighted tokens from modelUsage are 100 higher per call
        s.count("claude-sonnet-5-5", {"output_tokens": 20})
        record(sub, {"type": "user", "message": {"role": "user", "content": "explore"}})
        record(sub, {"type": "assistant", "requestId": "req_sub", "message": smsg})
        sub.close()

    kv = os.path.join(cwd, "kv.go")
    src = open(kv).read()
    if wrong:
        src = src.replace("package leasekv", "package leasekv\n\n// attempt")
        time.sleep(0.3 if mode == "never-fix" else 0)
    else:
        src = src.replace("Fixed = false", "Fixed = true")
        with open(os.path.join(cwd, "regress_test.go"), "w") as f:
            f.write('package leasekv\n\nimport "testing"\n\nfunc TestLeaseKVRegression(t *testing.T) {\n'
                    '\tif !Fixed {\n\t\tt.Fatal("the bug is back")\n\t}\n}\n')
    with open(kv, "w") as f:
        f.write(src)
    if "FAKE:WEAKEN" in prompt:
        t = os.path.join(cwd, "kv_test.go")
        text = open(t).read()
        with open(t, "w") as f:
            f.write(text.replace("return n > 0", "return true"))
    final = {"id": "msg_" + uuid.uuid4().hex[:12], "role": "assistant", "model": model, "usage": USAGE,
             "content": [{"type": "text", "text": "Fixed the bug."}]}
    s.count(model, USAGE)
    emit({"type": "assistant", "message": final, "session_id": s.sid})
    record(s.tr, {"type": "assistant", "requestId": "req_final_" + final["id"], "sessionId": s.sid, "message": final})
    total, api, usage = s.close(0.25, 1000)
    emit({"type": "result", "subtype": "success", "is_error": False, "duration_ms": 1200, "duration_api_ms": api,
          "num_turns": 4, "result": "Fixed the bug.", "session_id": s.sid, "total_cost_usd": total,
          "modelUsage": usage, "usage": USAGE, "permission_denials": [
              {"tool_name": "Bash", "tool_use_id": denied_id, "tool_input": {"command": "curl https://example.com"}}]
          if cmds else denials})
    return 0


def hang(s, model, emit):
    """Two grandchildren: one in this process group whose parent has exited (only a group kill
    reaches it) and one in its own session (only the kill of every descendant reaches it)."""
    sh = subprocess.run(["sh", "-c", "sleep 120 >/dev/null 2>&1 & echo $!"], stdout=subprocess.PIPE, text=True)
    own = subprocess.Popen(["sleep", "120"], start_new_session=True, stdout=subprocess.DEVNULL,
                           stderr=subprocess.DEVNULL)
    with open(os.environ["FAKE_PIDS"], "w") as f:
        json.dump([int(sh.stdout.strip()), own.pid], f)

    def on_int(*_):
        s.count(model, USAGE)
        s.close(0.10, 500)
        os._exit(130)

    signal.signal(signal.SIGINT, on_int)
    emit({"type": "assistant", "message": {"id": "msg_hang", "role": "assistant", "model": model, "usage": USAGE,
                                           "content": [{"type": "text", "text": "thinking"}]}, "session_id": s.sid})
    time.sleep(8)  # no result: a runner that never kills the round fails the test quickly
    return 0


def spend(s, model, emit):
    """Costly messages, each streamed twice (the second copy with more output tokens), until
    SIGINT; no cost-state record, so the runner must use its own estimate."""
    signal.signal(signal.SIGINT, lambda *_: os._exit(130))
    first = dict(USAGE)
    more = dict(USAGE, output_tokens=750)
    for i in range(200):  # then no result: a runner that never stops the round fails quickly
        mid = "msg_spend_%d" % i
        for u in (first, more):
            emit({"type": "assistant", "message": {"id": mid, "role": "assistant", "model": model, "usage": u,
                                                   "content": [{"type": "text", "text": "x"}]}, "session_id": s.sid})
        time.sleep(0.05)
    return 0


def _rule_paths(rules):
    out = []
    for r in rules:
        m = re.match(r"^Read\((.*)/\*\*\)$", r)
        if m:
            p = m.group(1)
            out.append(os.path.expanduser(p) if p.startswith("~") else p[1:] if p.startswith("//") else p)
    return out


def allowed(path, settings, cwd, mode):
    """The CLI's rule order as the probe relies on it: deny, then (bypassPermissions) everything
    else, or allow, then the workspace."""
    perms = settings.get("permissions", {})
    under = lambda p: path == p or path.startswith(p.rstrip("/") + "/")  # noqa: E731
    if any(under(p) for p in _rule_paths(perms.get("deny", []))):
        return False
    if mode == "bypassPermissions":
        return True
    return any(under(p) for p in _rule_paths(perms.get("allow", []))) or under(cwd)


def text(path):
    with open(path, errors="replace") as f:
        return f.read()


def file_tool(name, inp, tools, cwd, settings, mode):
    """(result, is_error, denied) of a Read, Glob or Grep call as on CLI 2.1.299: a tool init left
    out fails, the settings' Read deny rules cover all three, and a Read of a folder fails (EISDIR)."""
    path = inp.get("file_path") or inp.get("path") or cwd
    if name not in tools:
        return "Error: No such tool available: " + name, True, False
    if not allowed(path, settings, cwd, mode):
        return "Permission to use %s has been denied." % name, True, True
    if name == "Read":
        if os.path.isdir(path):
            return "Error: EISDIR: illegal operation on a directory, read '%s'" % path, True, False
        return text(path), False, False
    files = sorted(f for f in glob.glob(os.path.join(path, "**", "*"), recursive=True) if os.path.isfile(f))
    if name == "Glob":
        files = [f for f in files if fnmatch.fnmatch(os.path.relpath(f, path), inp["pattern"])]
    else:
        files = [f for f in files if inp["pattern"] in text(f)]
    return "\n".join(files) or "No files found", False, False


def read_arm_calls(s, model, emit, record, tools, cwd, settings, mode):
    """A read arm's calls in one message: Glob and Grep of the workspace, a Read of the folder and
    of kv.go, and the Edit of kv.go. Returns the permission denials."""
    calls = [("Glob", {"pattern": "*.go", "path": cwd}), ("Grep", {"pattern": "Fixed", "path": cwd}),
             ("Read", {"file_path": cwd}), ("Read", {"file_path": os.path.join(cwd, "kv.go")}),
             ("Edit", {"file_path": os.path.join(cwd, "kv.go")})]
    ids = ["toolu_" + uuid.uuid4().hex[:12] for _ in calls]
    msg = {"id": "msg_" + uuid.uuid4().hex[:12], "role": "assistant", "model": model, "usage": USAGE,
           "content": [{"type": "tool_use", "id": i, "name": n, "input": inp} for i, (n, inp) in zip(ids, calls)]}
    s.count(model, USAGE)
    emit({"type": "assistant", "message": msg, "session_id": s.sid})
    record(s.tr, {"type": "assistant", "requestId": "req_" + ids[0], "sessionId": s.sid, "message": msg})
    denials = []
    for tid, (name, inp) in zip(ids, calls):
        out, err, denied = ("updated", False, False) if name == "Edit" else file_tool(name, inp, tools, cwd,
                                                                                       settings, mode)
        if denied:
            denials.append({"tool_name": name, "tool_use_id": tid, "tool_input": inp})
        emit({"type": "user", "message": {"role": "user", "content": [
            {"type": "tool_result", "tool_use_id": tid, "content": out, "is_error": err}]}, "session_id": s.sid})
    return denials


def read_reply(prompt, s, model, emit, tools, cwd):
    """The read-arm check: tries Bash, Monitor and the subagent tool, then Globs the workspace when
    the prompt asks for it. A tool the init left out fails, except the subagent tool under
    FAKE_MODE=sub-runs; one that runs prints $PROBE_TOKEN."""
    token = os.environ.get("PROBE_TOKEN", "")
    sub_runs = os.environ.get("FAKE_MODE") == "sub-runs"
    for name in ("Bash", "Monitor", "Task"):
        tid = "toolu_" + uuid.uuid4().hex[:12]
        s.count(model, USAGE)
        emit({"type": "assistant", "message": {"id": "msg_" + tid, "role": "assistant", "model": model, "usage": USAGE,
                                               "content": [{"type": "tool_use", "id": tid, "name": name,
                                                            "input": {"command": "echo $PROBE_TOKEN"}}]},
              "session_id": s.sid})
        runs = name in tools or (name == "Task" and sub_runs)
        out = token if runs and name != "Task" else "Error: No such tool available: " + name
        if runs and name == "Task":
            sub = {"id": "msg_sub_" + tid, "role": "assistant", "model": model, "usage": USAGE,
                   "content": [{"type": "tool_use", "id": "toolu_sub", "name": "Bash", "input": {"command": "echo"}}]}
            s.count(model, USAGE)
            emit({"type": "assistant", "message": sub, "parent_tool_use_id": tid, "session_id": s.sid})
            os.makedirs(os.path.join(s.pdir, s.sid, "subagents"), exist_ok=True)
            with open(os.path.join(s.pdir, s.sid, "subagents", "agent-read.jsonl"), "w") as f:
                write(f, {"type": "assistant", "requestId": "req_sub", "message": sub})
                write(f, {"type": "user", "message": {"role": "user", "content": [
                    {"type": "tool_result", "tool_use_id": "toolu_sub", "content": token}]}})
            out = "the subagent replied"
        emit({"type": "user", "message": {"role": "user", "content": [
            {"type": "tool_result", "tool_use_id": tid, "content": out, "is_error": not runs}]}, "session_id": s.sid})
    if "Glob tool" in prompt:
        tid = "toolu_glob"
        s.count(model, USAGE)
        emit({"type": "assistant", "message": {"id": "msg_glob", "role": "assistant", "model": model, "usage": USAGE,
                                               "content": [{"type": "tool_use", "id": tid, "name": "Glob",
                                                            "input": {"pattern": "*"}}]}, "session_id": s.sid})
        out, err, _ = file_tool("Glob", {"pattern": "*"}, tools, cwd, {}, "bypassPermissions")
        emit({"type": "user", "message": {"role": "user", "content": [
            {"type": "tool_result", "tool_use_id": tid, "content": out, "is_error": err}]}, "session_id": s.sid})
    total, api, usage = s.close(0.002, 300)
    emit({"type": "result", "subtype": "success", "is_error": False, "num_turns": 4, "result": "DONE",
          "session_id": s.sid, "total_cost_usd": total, "duration_api_ms": api, "modelUsage": usage,
          "permission_denials": []})
    return 0


def probe_reply(args, prompt, s, model, emit, cwd, tools=()):
    """Answers the probe's paid calls: runs each listed command, reads each listed file and denies
    what the --settings rules deny; starts a subagent when asked."""
    if "PROBE_TOKEN" in prompt:
        return read_reply(prompt, s, model, emit, tools, cwd)
    settings = json.loads(arg(args, "--settings") or "{}")
    calls = [("Bash", {"command": m.group(1)}) for m in re.finditer(r"^\d+\. (.+)$", prompt, re.M)]
    m = re.search(r"Read tool on (\S+) and on (\S+)\.", prompt)
    if m:
        calls += [("Read", {"file_path": m.group(1)}), ("Read", {"file_path": m.group(2)})]
    m = re.search(r"Use the Read tool on (\S+)\. Then use the Bash tool to run: (cat \S+)", prompt)
    if m:
        calls += [("Read", {"file_path": m.group(1)}), ("Bash", {"command": m.group(2)})]
    cost = 0.01 if re.search(r"^\d+\. ", prompt, re.M) else 0.003 if m else 0.002
    denials = []
    for name, inp in calls:
        tid = "toolu_" + uuid.uuid4().hex[:12]
        s.count(model, USAGE)
        # like the real CLI, run and show a command without a leading cd to the working folder, and
        # keep what the model sent in wire_tool_inputs
        wire, cd = {}, "cd %s && " % cwd
        if inp.get("command", "").startswith(cd):
            wire, inp = {tid: dict(inp)}, dict(inp, command=inp["command"][len(cd):])
        emit({"type": "assistant", "message": {"id": "msg_" + tid, "role": "assistant", "model": model, "usage": USAGE,
                                               "content": [{"type": "tool_use", "id": tid, "name": name, "input": inp}]},
              "session_id": s.sid, "wire_tool_inputs": wire})
        path = inp.get("file_path") or (inp["command"].split()[1] if inp["command"].startswith("cat /") else "")
        if (name, os.environ.get("FAKE_MODE")) in (("Bash", "cat-runs"), ("Read", "read-runs")):
            continue
        if path and not allowed(path, settings, cwd, arg(args, "--permission-mode")):
            denials.append({"tool_name": name, "tool_use_id": tid, "tool_input": inp})
    if prompt.startswith("Use the Task tool"):
        cost, task = 0.004, "toolu_task"
        s.count(model, USAGE)
        emit({"type": "assistant", "message": {"id": "msg_task", "role": "assistant", "model": model, "usage": USAGE,
                                               "content": [{"type": "tool_use", "id": task, "name": "Task",
                                                            "input": {"subagent_type": "probe-helper"}}]},
              "session_id": s.sid})
        sub = {"id": "msg_sub", "role": "assistant", "model": model, "usage": USAGE,
               "content": [{"type": "tool_use", "id": "toolu_ls", "name": "Bash", "input": {"command": "ls"}}]}
        s.count(model, USAGE)
        emit({"type": "assistant", "message": sub, "parent_tool_use_id": task, "session_id": s.sid})
        os.makedirs(os.path.join(s.pdir, s.sid, "subagents"), exist_ok=True)
        with open(os.path.join(s.pdir, s.sid, "subagents", "agent-probe.jsonl"), "w") as f:
            write(f, {"type": "assistant", "requestId": "req_sub", "message": sub})
    total, api, usage = s.close(cost, 300)
    emit({"type": "result", "subtype": "success", "is_error": False, "num_turns": len(calls) + 1,
          "result": "DONE" if calls else "OK", "session_id": s.sid, "total_cost_usd": total, "duration_api_ms": api,
          "modelUsage": usage, "permission_denials": denials})
    return 0


if __name__ == "__main__":
    sys.exit(main())
