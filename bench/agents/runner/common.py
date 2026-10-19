"""Config, paths, environment and file helpers shared by the runner."""
import glob
import json
import os
import re
import shutil
import signal
import stat
import subprocess
import sys

ARM_VERSION = {"plain": "plain", "docs": "faultline", "plugin": "faultline",
               "read-plain": "plain", "read-faultline": "faultline"}
# The screen's arms: the variant only (no CLAUDE.md, no plugin), only the tools in read_tools,
# one round.
READ_ARMS = ("read-plain", "read-faultline")


def set_arms(cfg, set_name):
    """The arms a set runs: the screen arms for set screen, else the tier's three arms. Tier 1
    has no screen: its config sets no screen_arms."""
    return cfg.get("screen_arms", []) if set_name == "screen" else cfg["arms"]


def run_cap(cfg, set_name):
    """A run's cap in USD: run_cap_usd_by_set for the set (the screen's $3), else run_cap_usd."""
    return cfg.get("run_cap_usd_by_set", {}).get(set_name, cfg["run_cap_usd"])


def expand(p, base):
    p = os.path.expanduser(os.path.expandvars(p))
    return p if os.path.isabs(p) else os.path.normpath(os.path.join(base, p))


def load_config(path):
    """Reads a tier's config.json. Relative paths resolve against the tier folder that holds it.
    cfg["bench"] is the folder above, bench/agents, which holds both tiers, the runner and the
    hidden checks: the Read deny rules and the forbidden-path scan cover it. workspace_root is
    resolved with realpath (on macOS /tmp is /private/tmp), so the workspaces, the transcript
    folder names the CLI derives from them and the deny rules all use the same path."""
    path = os.path.abspath(path)
    with open(path) as f:
        cfg = json.load(f)
    tier_dir = os.path.dirname(path)
    cfg["bench"] = os.path.dirname(tier_dir)
    cfg["paths"] = {k: expand(v, tier_dir) for k, v in cfg["paths"].items()}
    cfg["paths"]["workspace_root"] = os.path.realpath(cfg["paths"]["workspace_root"])
    return cfg


def variant_dir(cfg, version, bug):
    """The sources of a variant: service/testdata/<version>/<bug>, with no go.mod (prepare.sh
    writes it)."""
    return os.path.join(cfg["paths"]["service"], "testdata", version, bug)


def add_docs(cfg, ws):
    """The docs arm's files: arms/docs/instructions.md as CLAUDE.md, which Claude Code loads, and
    docs/ai-agents.md."""
    docs = os.path.join(cfg["paths"]["arms"], "docs")
    shutil.copy2(os.path.join(docs, "instructions.md"), os.path.join(ws, "CLAUDE.md"))
    os.makedirs(os.path.join(ws, "docs"), exist_ok=True)
    shutil.copy2(os.path.join(docs, "docs", "ai-agents.md"), os.path.join(ws, "docs", "ai-agents.md"))


def child_env(cfg, extra=None):
    """A clean environment: only the names in env_keep pass through from the caller."""
    env = {k: os.environ[k] for k in cfg["env_keep"] if k in os.environ}
    cdir = cfg["paths"].get("claude_config_dir", "")
    if cdir and os.path.realpath(cdir) != os.path.realpath(os.path.expanduser("~/.claude")):
        env["CLAUDE_CONFIG_DIR"] = cdir
    env.update(extra or {})
    return env


def projects_dir(cfg):
    """Where Claude Code writes transcripts: <claude_config_dir>/projects."""
    return os.path.join(cfg["paths"]["claude_config_dir"], "projects")


def project_folders(cfg, ws):
    """The transcript folders Claude Code made for workspace ws (its path with every character
    other than a letter or digit turned into '-')."""
    name = re.sub(r"[^A-Za-z0-9]", "-", os.path.realpath(ws))
    return sorted(p for p in glob.glob(os.path.join(projects_dir(cfg), "*")) if os.path.basename(p) == name)


def leftover_project_folders(cfg):
    """Transcript folders of earlier workspaces under workspace_root; an agent could read them."""
    prefix = re.sub(r"[^A-Za-z0-9]", "-", os.path.realpath(cfg["paths"]["workspace_root"])) + "-"
    return sorted(p for p in glob.glob(os.path.join(projects_dir(cfg), "*")) if os.path.basename(p).startswith(prefix))


def forbidden_patterns(cfg):
    """Strings that mark a touch of the repository, bench/agents or the transcripts of other runs:
    each folder's absolute path, its resolved path when that differs (on macOS /tmp/x is
    /private/tmp/x), and their home forms. A folder inside one already listed adds nothing.
    measures.forbidden_hits matches them only at a path boundary."""
    roots, out = [], []
    for p in (cfg["paths"]["repo"], cfg["bench"], projects_dir(cfg)):
        for q in (os.path.abspath(p), os.path.realpath(p)):
            if not any(q == r or q.startswith(r + "/") for r in roots):
                roots.append(q)
                out += home_forms(q)
    return sorted(set(out), key=lambda s: (len(s), s))


# How a shell command names a path under the home folder, quoted forms included.
HOME_PREFIXES = ("~/", "$HOME/", "${HOME}/", '"$HOME"/', '"${HOME}"/')


def home_forms(p):
    """p, and when it sits under the home folder the same path from there: ~/<rel>, $HOME/<rel>,
    ${HOME}/<rel>, "$HOME"/<rel> and "${HOME}"/<rel>."""
    home = os.path.expanduser("~")
    if not p.startswith(home + "/"):
        return [p]
    rel = p[len(home) + 1:]
    return [p] + [h + rel for h in HOME_PREFIXES]


def plugin_read_rule(cfg):
    """The Read rule for the plugin folder, in the ~/ form when it sits under the home folder."""
    p, home = cfg["paths"]["plugin"], os.path.expanduser("~")
    return "Read(~/%s/**)" % p[len(home) + 1:] if p.startswith(home + "/") else "Read(/%s/**)" % p


def settings_json(cfg, arm):
    """The --settings value: config settings plus a Read deny on the repository and bench/agents,
    and for every arm but plugin on the plugin folder (arm plugin's skills link to files there).
    Every arm runs in bypassPermissions mode, where only deny rules decide, so there is no allow
    list."""
    s = json.loads(json.dumps(cfg["settings"]))
    deny = s.setdefault("permissions", {}).setdefault("deny", [])
    for p in (cfg["paths"]["repo"], cfg["bench"]):
        rule = "Read(/" + os.path.realpath(p) + "/**)"
        if rule not in deny:
            deny.append(rule)
    if arm != "plugin":
        deny.append(plugin_read_rule(cfg))
    return json.dumps(s, separators=(",", ":"))


def kill_group(pgid, sig):
    """Sends sig to a process group; a group that is gone is fine."""
    try:
        os.killpg(pgid, sig)
    except OSError:
        pass


def sh(cmd, cwd=None, env=None, timeout=None, input=None):
    """Runs cmd in its own process group; returns (exit code, combined output). A timeout kills
    the whole group (go test leaves child processes) and gives code 124."""
    p = subprocess.Popen(cmd, cwd=cwd, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
                         stdin=subprocess.PIPE if input is not None else subprocess.DEVNULL, start_new_session=True)
    try:
        out, _ = p.communicate(input, timeout=timeout)
        return p.returncode, out
    except subprocess.TimeoutExpired:
        kill_group(p.pid, signal.SIGKILL)
        try:
            out, _ = p.communicate(timeout=10)
        except subprocess.TimeoutExpired:  # a process outside the group holds the pipe
            p.kill()
            p.wait()
            out = ""
        return 124, (out or "") + "\n[timeout]\n"


def _writable(func, path, _):
    for p in (os.path.dirname(path), path):
        os.chmod(p, stat.S_IWRITE | stat.S_IREAD | stat.S_IEXEC)
    func(path)


def rmtree(path):
    if not os.path.lexists(path):
        return
    if sys.version_info >= (3, 12):
        shutil.rmtree(path, onexc=_writable)
    else:
        shutil.rmtree(path, onerror=_writable)


def copytree(src, dst, ignore=None):
    shutil.copytree(src, dst, symlinks=True, ignore=ignore)


def read_text(path):
    with open(path, encoding="utf-8", errors="replace") as f:
        return f.read()


def write_text(path, text):
    with open(path, "w") as f:
        f.write(text)


def write_json(path, obj):
    tmp = path + ".tmp"
    with open(tmp, "w") as f:
        json.dump(obj, f, indent=1, sort_keys=True)
        f.write("\n")
    os.replace(tmp, path)


def read_json(path, default=None):
    try:
        with open(path) as f:
            return json.load(f)
    except (OSError, ValueError):
        return default
