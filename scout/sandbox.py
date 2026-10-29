"""Evaluator trial sandbox (G-009 T8a): the on-disk layout of one skill trial and the fail-closed checks around it.

SECURITY CODE. `build_sandbox` creates <skilleval_root>/trials/<trial-id>/ with work/ (the task repo, a fresh git
repo), fh/ (a temp FACTORY_HOME with its own DB), home/ (HOME holding only a comment-only rc file),
cfg/ (CLAUDE_CONFIG_DIR), role/ (read-only claude settings + system prompt, written by spawn), bin/claude (the
nsbox shim) and a read-only factory CLI snapshot.
The evaluator only ever runs through that shim (nsbox allow-list view: the real home, the runtime dir, /tmp and
/dev/shm are hidden; only the trial dir, the one vendored plugin and the claude install are bound back).

Nothing here reads the eval token: `check_token` only stats the file. Only the shim opens it, inside the pane.
Third-party files are copied as data and never executed.
"""
import json
import os
import shutil
import sqlite3
import stat
import subprocess
import sys
from pathlib import Path

try:
    from scout import nsbox
    from scout import vendor as scoutvendor
    from web import dbpath
except ImportError:  # run as a script by path
    sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
    from scout import nsbox
    from scout import vendor as scoutvendor
    from web import dbpath

REPO = Path(__file__).resolve().parent.parent
TASKS_DIR = REPO / "skilleval" / "tasks"
EVAL_AGENT_ROLE = "evaluator"
SEED_GOAL, SEED_TASK = "G-001", "T-0001"
DEAD_SOCKET = "/proc/nonexistent/eval.sock"
PREFLIGHT_TIMEOUT_S = 30

# An empty HOME makes interactive zsh run zsh-newuser-install, which eats the first key of the pane launch line.
HOME_RC = (".zshrc", "# factory skilleval trial: suppress zsh-newuser-install\n")

# The one seed for <trial>/cfg/.claude.json (plan v3 §7.5, M1613). Add only keys a startup dialog demands.
CLAUDE_JSON_SEED = {"hasCompletedOnboarding": True, "theme": "dark", "projects": {}}
CLAUDE_PROJECT_SEED = {"hasTrustDialogAccepted": True, "hasCompletedProjectOnboarding": True}

# env keys the live spawn may hand the evaluator through env_extra; anything else is refused
EVAL_ENV_KEYS = ("FACTORY_HOME", "HOME", "CLAUDE_CONFIG_DIR", "PATH",
                 "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "DISABLE_AUTOUPDATER")


class SandboxRefused(Exception):
    """The sandbox cannot be built or is not safe to start; `try` maps it to exit 3 and spawns nothing."""


def trial_name(trial_id):
    return f"tr{int(trial_id):04d}"


def check_token(path):
    """lstat only (never open): a regular file, mode 0600, owned by us. The value is read by the shim alone."""
    try:
        st = os.lstat(path)
    except OSError:
        st = None
    if st is None or not stat.S_ISREG(st.st_mode) or stat.S_IMODE(st.st_mode) != 0o600 or st.st_uid != os.getuid():
        raise SandboxRefused(f"{nsbox.NO_TOKEN_MSG} ({path}: must exist, be a regular file, mode 0600, owned by "
                             f"uid {os.getuid()})")


def check_userns(unshare="unshare"):
    ok, why = nsbox.userns_available(unshare)
    if not ok:
        raise SandboxRefused(f"user namespaces unavailable, no unsandboxed fallback: {why}")


def claude_json(work):
    seed = json.loads(json.dumps(CLAUDE_JSON_SEED))
    seed["projects"][os.path.realpath(work)] = dict(CLAUDE_PROJECT_SEED)
    return seed


def _git(cwd, *args):
    env = {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null",
           "GIT_TERMINAL_PROMPT": "0", "LC_ALL": "C.UTF-8"}
    p = subprocess.run(["git", "-C", str(cwd), "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", *args],
                       capture_output=True, text=True, env=env, timeout=60)
    if p.returncode != 0:
        raise SandboxRefused(f"git {args[0]} failed in {cwd}: {p.stderr.strip()}")


def _seed_fh(fh, task_dir, meta, agent):
    """A temp FACTORY_HOME: `factory init` as a subprocess (own python, own DB), then rows for the one task."""
    env = {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "FACTORY_HOME": str(fh), "HOME": str(fh),
           "LC_ALL": "C.UTF-8", "PYTHONDONTWRITEBYTECODE": "1"}
    p = subprocess.run([sys.executable, str(REPO / "bin" / "factory"), "init"], env=env, capture_output=True,
                       text=True, timeout=60)
    if p.returncode != 0:
        raise SandboxRefused(f"factory init in {fh} failed: {p.stderr.strip()}")
    spec = (task_dir / "task.md").read_text()
    ts = "1970-01-01T00:00:00+00:00"
    con = sqlite3.connect(dbpath.resolve(fh))
    try:
        con.executemany("INSERT INTO config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
                        [("herdr_socket", DEAD_SOCKET), ("git_push", "0")])
        con.execute("INSERT INTO goals(id,title,status,created,updated) VALUES(?,?,'active',?,?)",
                    (SEED_GOAL, "eval", ts, ts))
        con.execute("INSERT INTO tasks(id,goal_id,title,role,status,spec,acceptance,created,updated) "
                    "VALUES(?,?,?,?, 'todo',?,?,?,?)",
                    (SEED_TASK, SEED_GOAL, meta.get("title") or meta["id"], meta["role"], spec, "see the task", ts, ts))
        con.execute("INSERT INTO agents(name,role,goal_id,task_id,status,started) VALUES(?,?,?,?,'live',?)",
                    (agent, EVAL_AGENT_ROLE, SEED_GOAL, SEED_TASK, ts))
        con.execute("INSERT INTO messages(sender,recipient,kind,subject,body,goal_id,task_id,created) "
                    "VALUES('factory',?,'request',?,?,?,?,?)",
                    (agent, f"kickoff: evaluator {SEED_TASK}", f"Your task is {SEED_TASK}: run `factory task show {SEED_TASK}`.",
                     SEED_GOAL, SEED_TASK, ts))
        con.commit()
    finally:
        con.close()


def eval_ns_cfg(trial, *, home, real_claude, vendor_plugin=None, unshare="unshare"):
    """The allow-list nsbox cfg for one trial; make_cfg runs exactly once, here."""
    trial = os.path.realpath(trial)
    home = os.path.realpath(home)
    runtime = os.environ.get("XDG_RUNTIME_DIR") or f"/run/user/{os.getuid()}"
    hide = [os.path.realpath(d) for d in (home, runtime, "/tmp", "/dev/shm") if os.path.isdir(d)]
    claude = os.path.abspath(real_claude)
    ro = [f"{trial}/bin", f"{trial}/scout", f"{trial}/role"]
    install = os.path.realpath(claude)
    share = f"{home}/.local/share/claude"
    if install.startswith(share + "/") and os.path.isdir(os.path.dirname(install)):
        ro.append(os.path.dirname(install))
    if vendor_plugin:
        ro.append(os.path.realpath(vendor_plugin))
    trials_root = os.path.dirname(trial)
    masks = []  # siblings stay hidden even when the trials root lies outside every hide tree
    if not any(trials_root == h or trials_root.startswith(h.rstrip("/") + "/") for h in hide):
        masks.append(trials_root)
    return nsbox.make_cfg(trial, claude, home, trials_root, mode="allow", forward=True, unshare=unshare,
                          hide_dirs=hide, hide_files=(), rw=[trial], ro=ro, masks=masks, cwd=f"{trial}/work")


def build_sandbox(con, trial, root, *, home=None, real_claude=None, token_file=None, unshare="unshare",
                  factory_src=None):
    """Create trials/<tr id>/ for the skill_trials row `trial`. Refuses (SandboxRefused) before creating anything
    when the token file is missing or not 0600. Returns {"dir", "work", "plugin", "agent", "ns", "shim"}."""
    home = str(home or nsbox.real_home())
    token_file = token_file or f"{home}/.config/factory/eval-oauth-token"
    check_token(token_file)
    real_claude = real_claude or f"{home}/.local/bin/claude"
    cand = con.execute("SELECT * FROM scout_candidates WHERE id=?", (trial["candidate_id"],)).fetchone()
    if cand is None:
        raise SandboxRefused(f"no candidate {trial['candidate_id']}")
    plugin = None
    if trial["condition"] == "with":
        if not scoutvendor.is_eval_allowed(dict(cand), root):
            raise SandboxRefused(f"{cand['id']} is not eligible for evaluation (not prompt-only, held back, or not vendored)")
        meta = scoutvendor.vendored(root, cand["id"], trial["vendor_sha"])
        if not meta:
            raise SandboxRefused(f"{cand['id']}@{trial['vendor_sha']} has no valid vendored copy; run factory skilleval vendor")
        plugin = str(Path(scoutvendor.vendor_dir(root, cand["id"], trial["vendor_sha"]), meta["plugin"]).resolve())
    task_dir = TASKS_DIR / trial["task"]
    try:
        meta = json.loads((task_dir / "meta.json").read_text())
        (task_dir / "task.md").read_text()
    except (OSError, ValueError) as e:
        raise SandboxRefused(f"unknown eval task {trial['task']!r}: {e}")
    name = trial_name(trial["id"])
    trials_root = Path(root).resolve() / "trials"
    d = trials_root / name
    agent = f"eval-{name}"
    if d.exists():
        shutil.rmtree(d)  # a retried trial starts clean
    try:
        for sub in ("work", "fh", "home", "cfg", "role", "bin"):
            (d / sub).mkdir(parents=True)
        (d / "home" / HOME_RC[0]).write_text(HOME_RC[1])
        os.chmod(d / "home" / HOME_RC[0], 0o644)
        shutil.copytree(task_dir / "repo", d / "work", dirs_exist_ok=True)  # data only; nothing is run
        _git(d / "work", "init", "-q")
        _git(d / "work", "config", "user.name", "eval")
        _git(d / "work", "config", "user.email", "eval@localhost")
        _git(d / "work", "add", "-A")
        _git(d / "work", "commit", "-q", "-m", "task baseline")
        _seed_fh(d / "fh", task_dir, meta, agent)
        (d / "cfg" / ".claude.json").write_text(json.dumps(claude_json(d / "work"), indent=2))
        ns = eval_ns_cfg(d, home=home, real_claude=real_claude, vendor_plugin=plugin, unshare=unshare)
        env_set = {"HOME": str(d / "home"), "FACTORY_HOME": str(d / "fh"), "FACTORY_ROLE": EVAL_AGENT_ROLE,
                   "CLAUDE_CONFIG_DIR": str(d / "cfg")}
        shim = nsbox.write_shim(d, real_claude, token_file=token_file, home=home, trials_root=trials_root, ns=ns,
                                factory_src=factory_src, unshare=unshare,
                                shim_extra={"eval_env": True, "env_set": env_set})
    except BaseException:
        shutil.rmtree(d, ignore_errors=True)
        raise
    return {"dir": str(d), "work": str(d / "work"), "plugin": plugin, "agent": agent, "ns": ns, "shim": str(shim)}


def preflight(info):
    """Run the real view (same cfg, no proxy, no token) around `claude --version`. Any hide/bind failure or a
    claude that is not reachable inside gives SandboxRefused: the mounts happen in the pane, so this is the only
    place a failure can be seen before an agent is started."""
    ns = dict(info["ns"], forward_sock=None)
    env = {k: v for k, v in os.environ.items() if k in nsbox.ENV_PASS}
    env["HOME"] = str(Path(info["dir"]) / "home")
    try:
        p = subprocess.run(nsbox.compose_stage1(ns, ["--version"]), env=env, stdin=subprocess.DEVNULL,
                           capture_output=True, text=True, timeout=PREFLIGHT_TIMEOUT_S, cwd=info["work"])
    except (OSError, subprocess.TimeoutExpired) as e:
        raise SandboxRefused(f"sandbox preflight could not run: {e}")
    if p.returncode != 0:
        raise SandboxRefused(f"sandbox preflight failed (exit {p.returncode}); nothing was started: "
                             f"{(p.stderr or p.stdout).strip()[:400]}")
