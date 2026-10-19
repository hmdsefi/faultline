"""Reads Claude Code stream-json output and checks a run's init message against its arm."""
import glob
import json
import os
import re

import common


def events_from_lines(lines):
    out = []
    for line in lines:
        line = line.strip()
        if not line.startswith("{"):
            continue
        try:
            out.append(json.loads(line))
        except ValueError:
            continue
    return out


def events(path):
    try:
        with open(path, encoding="utf-8", errors="replace") as f:
            return events_from_lines(f)
    except OSError:
        return []


def message_of(e):
    """An event's message object; {} when there is none or it is text (system events such as
    permission_denied carry a string message)."""
    m = e.get("message")
    return m if isinstance(m, dict) else {}


def init_of(evs):
    for e in evs:
        if e.get("type") == "system" and e.get("subtype") == "init":
            return e
    return None


def result_of(evs):
    res = [e for e in evs if e.get("type") == "result"]
    return res[-1] if res else None


def hooks_of(evs):
    return [e.get("hook_name") or e.get("hook_event") or "?" for e in evs
            if e.get("type") == "system" and str(e.get("subtype", "")).startswith("hook_started")]


def tool_uses(evs):
    """(id, name, input) of every tool_use block in assistant messages."""
    out = []
    for e in evs:
        msg = message_of(e)
        if e.get("type") != "assistant" or not isinstance(msg.get("content"), list):
            continue
        for b in msg["content"]:
            if isinstance(b, dict) and b.get("type") == "tool_use":
                out.append((b.get("id"), b.get("name"), b.get("input") or {}))
    return out


def text_of(evs):
    parts = []
    for e in evs:
        if e.get("type") == "result" and isinstance(e.get("result"), str):
            parts.append(e["result"])
    return "\n".join(parts)


def memory_files(context_text):
    """Paths in the 'Memory Files' table that /context prints."""
    out, on = [], False
    for line in context_text.splitlines():
        if line.startswith("### "):
            on = line.strip() == "### Memory Files"
            continue
        if on and line.startswith("|"):
            cells = [c.strip() for c in line.strip("|").split("|")]
            if len(cells) >= 2 and cells[1].startswith("/"):
                out.append(cells[1])
    return out


def _names(d, key):
    return [x if isinstance(x, str) else x.get("name", "?") for x in d.get(key) or []]


def user_items(home=None):
    """Names of your user-level skills, agents and commands (~/.claude/skills, agents, commands)."""
    base = os.path.join(home or os.path.expanduser("~"), ".claude")
    names = set()
    for sub in ("skills", "agents", "commands"):
        for p in glob.glob(os.path.join(base, sub, "*")):
            names.add(re.sub(r"\.md$", "", os.path.basename(p)))
    return names


def plugin_items(plugin_dir):
    """(name, skills, agents) a plugin folder declares."""
    try:
        with open(os.path.join(plugin_dir, ".claude-plugin", "plugin.json")) as f:
            name = json.load(f)["name"]
    except (OSError, ValueError, KeyError):
        return None, set(), set()
    skills = {name + ":" + os.path.basename(os.path.dirname(p))
              for p in glob.glob(os.path.join(plugin_dir, "skills", "*", "SKILL.md"))}
    agents = {name + ":" + os.path.basename(p)[:-3] for p in glob.glob(os.path.join(plugin_dir, "agents", "*.md"))}
    return name, skills, agents


def check_init(init, hooks, arm, cfg, user=None):
    """Returns (ok, problems, notes). Problems refuse the run; notes are recorded only.

    Allowed: built-in plugins and their items, the CLI's built-in skills and agents (the baseline in
    config isolation_baseline), and for arm plugin the faultline plugin with all its skills and
    agents. Refused: any MCP server, any hook, any other plugin, any item namespaced by another
    plugin, and any user-level skill, agent or command (also in slash_commands) that is not also a
    built-in name. Tools: a read arm (the screen) is refused when its tool list holds anything
    outside read_tools; every other arm when it lacks Bash.
    """
    if init is None:
        return False, ["no init message"], []
    problems, notes = [], []
    base = cfg.get("isolation_baseline", {})
    base_items = set(base.get("skills", [])) | set(base.get("agents", []))
    user = user_items() if user is None else user
    pdir = cfg["paths"]["plugin"]
    pname, pskills, pagents = plugin_items(pdir) if arm == "plugin" else (None, set(), set())
    for m in init.get("mcp_servers") or []:
        problems.append("mcp server " + (m.get("name", "?") if isinstance(m, dict) else str(m)))
    for h in hooks:
        problems.append("hook " + str(h))
    allowed_ns = set()
    for p in init.get("plugins") or []:
        name, src, path = p.get("name", "?"), p.get("source", ""), p.get("path", "")
        if path == "builtin" or src.endswith("@builtin"):
            allowed_ns.add(name)
        elif pname and name == pname and os.path.realpath(path) == os.path.realpath(pdir):
            allowed_ns.add(name)
        else:
            problems.append("plugin " + name)
    skills, agents = _names(init, "skills"), _names(init, "agents")
    for item in skills + agents:
        if ":" in item:
            if item.split(":", 1)[0] not in allowed_ns:
                problems.append("item " + item)
        elif item in user and item not in base_items:
            problems.append("user item " + item)
        elif base_items and item not in base_items:
            notes.append("unlisted " + item)
    for name in init.get("slash_commands") or []:
        if ":" in name:
            if name.split(":", 1)[0] not in allowed_ns:
                problems.append("command " + name)
        elif name in user and name not in base_items:
            problems.append("user command " + name)
    tools = set(_names(init, "tools"))
    if arm in common.READ_ARMS:
        problems += ["read arm has tool " + t for t in sorted(tools - set(cfg["read_tools"]))]
    elif "Bash" not in tools:
        problems.append("arm %s lacks Bash" % arm)
    if arm == "plugin":
        if pname not in allowed_ns:
            problems.append("arm plugin missing plugin " + str(pname))
        for item in sorted((pskills - set(skills)) | (pagents - set(agents))):
            problems.append("arm plugin missing " + item)
    problems = list(dict.fromkeys(problems))
    return not problems, problems, sorted(set(notes))
