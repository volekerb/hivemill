#!/usr/bin/env python3
"""Main-vs-PR differential for the worker-guard hook (G-013 countermeasure A).

Feeds every corpus command to the hook at <base> and to the hook in the working tree, exactly as
roles/settings/*.json invoke it (`--role <role>` for worker mode, `--hq` for HQ mode), and reports each
command whose exit code differs. It also diffs the allow/deny/hook rules of roles/settings/*.json.in against
<base> (SETTINGS-LOOSENED / SETTINGS-TIGHTENED lines). A command is only ever sent to a hook as PreToolUse JSON on stdin; it is
never executed. See docs/guard-diff.md.

--impl go compares the Python hook (reference) with a Go binary (candidate) instead; --strict then also fails on
TIGHTENED and CHANGED lines (the G-016 gate).

Exit: 0 no unlisted LOOSENED/TIMEOUT line, 1 at least one, 2 usage or git error (fail closed).
"""
import argparse
import json
import os
import pwd
import re
import shutil
import sqlite3
import subprocess
import sys
import tempfile
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone

DEFAULT_HOOK = "roles/hooks/worker-guard.py"
DEFAULT_CORPUS = os.path.join(os.path.dirname(os.path.abspath(__file__)), "guard_corpus.json")
BLOCK = 2
TIMEOUT = "timeout"


class UsageError(Exception):
    pass


def git(repo, *args):
    try:
        p = subprocess.run(["git", "-C", repo, *args], capture_output=True, text=True, timeout=60)
    except (OSError, subprocess.TimeoutExpired) as e:
        raise UsageError(f"git {args[0]} failed: {e}; check that git is installed and the repo is reachable")
    if p.returncode != 0:
        raise UsageError(f"git {' '.join(args)} failed: {p.stderr.strip()}; check --base and --repo")
    return p.stdout


def extract_base_ci(repo, base, dest):
    """Write the base ref's ci/*.py (top level) under dest/ci, so the base hook finds ci/public_text_guard.py and the
    ci/scrub_scan.py it loads by path, as the hook at that ref would in a checkout. A base without ci/ writes nothing."""
    try:
        names = git(repo, "ls-tree", "--name-only", "--end-of-options", base, "ci/").split("\n")
    except UsageError:
        return
    for name in names:
        if not (name.startswith("ci/") and name.endswith(".py") and "/" not in name[3:]):
            continue
        os.makedirs(os.path.join(dest, "ci"), exist_ok=True)
        with open(os.path.join(dest, name), "w", encoding="utf-8") as f:
            f.write(git(repo, "show", "--end-of-options", f"{base}:{name}"))


def live_home():
    """The live factory home the hook derives under this user (T-0212); corpus commands spell it @LIVE_HOME@."""
    return os.path.join(pwd.getpwuid(os.getuid()).pw_dir, "factory")


def shared_fixture():
    """A stand-in shared checkout (a directory holding .git) at a fixed temp path; corpus commands spell it @SHARED@
    and the hook is told it is the shared checkout (WORKER_GUARD_LIVE_HOME), so the git rules fire on any machine."""
    path = os.path.join(tempfile.gettempdir(), f"guard-diff-shared-{os.getuid()}", "factory")
    os.makedirs(os.path.join(path, ".git"), exist_ok=True)
    return os.path.realpath(path)


def scratch_fixture():
    """A stand-in worker scratchpad at a fixed temp path; corpus commands spell it @SCRATCH@ and the hook is told it is
    the worker's scratchpad (WORKER_GUARD_SCRATCH), so the git init rule is exercised on any machine."""
    path = os.path.join(tempfile.gettempdir(), f"guard-diff-scratch-{os.getuid()}")
    os.makedirs(path, exist_ok=True)
    path = os.path.realpath(path)
    # T-0809: fixed contents the archive-extract corpus entries (T-0783) depend on: a repo (r/.git), a clean dir (out),
    # a symlink below a dir (linked/l) and a gitfile three levels down (deep/a/b/.git)
    os.makedirs(os.path.join(path, "r", ".git"), exist_ok=True)
    os.makedirs(os.path.join(path, "out"), exist_ok=True)
    os.makedirs(os.path.join(path, "linked"), exist_ok=True)
    try:  # hook_env calls this from several worker threads at once: losing the create race is fine
        os.symlink(os.path.join(path, "r"), os.path.join(path, "linked", "l"))
    except FileExistsError:
        pass
    os.makedirs(os.path.join(path, "deep", "a", "b"), exist_ok=True)
    open(os.path.join(path, "deep", "a", "b", ".git"), "a").close()
    return path


def worktree_fixture():
    """A stand-in coder worktree at a fixed temp path: an entry whose env names a FACTORY_AGENT with a task id runs with
    this as the hook's cwd and as tasks.worktree of that task (what the coder push pin checks, T-0743)."""
    path = os.path.join(tempfile.gettempdir(), f"guard-diff-worktree-{os.getuid()}", "worktrees", "G-000-T-0000")
    os.makedirs(path, exist_ok=True)
    return os.path.realpath(path)


def agent_task(env):
    """The task id (T-NNNN) a corpus entry's env FACTORY_AGENT carries, or None. Deliberately looser than the pin's own
    name regex, so a look-alike agent name still gets a task row and is refused for its name, not for a missing row."""
    m = re.search(r"t-\d+", dict(env or ()).get("FACTORY_AGENT", ""), re.I)
    return m.group(0).upper() if m else None


def hook_env(cmd, env=None):
    """env for one hook run: WORKER_GUARD_LIVE_HOME is set only for a command that names the @SHARED@ fixture,
    WORKER_GUARD_SCRATCH only for one that names the @SCRATCH@ fixture."""
    out = {}
    if shared_fixture() in cmd:
        out["WORKER_GUARD_LIVE_HOME"] = shared_fixture()
    if scratch_fixture() in cmd:
        out["WORKER_GUARD_SCRATCH"] = scratch_fixture()
    return {**(os.environ if env is None else env), **out} if out else env


def expand(cmd):
    if "@SHARED@" in cmd and "@LIVE_HOME@" in cmd:
        raise UsageError(f"bad corpus command {cmd!r}: @SHARED@ replaces the live home for the hook, so it cannot be "
                         "mixed with @LIVE_HOME@ (the live home would be unguarded); use one token")
    cmd = cmd.replace("@LIVE_HOME@", live_home())
    cmd = cmd.replace("@SHARED@", shared_fixture()) if "@SHARED@" in cmd else cmd
    return cmd.replace("@SCRATCH@", scratch_fixture()) if "@SCRATCH@" in cmd else cmd


def entry_item(e):
    """(cmd, role), (cmd, role, seed) or (cmd, role, seed-or-None, env): role is the corpus entry's own `role` tag (e.g.
    "evaluator"), or None for the --role default. seed is the entry's `config` dict (config rows the hook reads from the
    factory DB), as a tuple of sorted (key, value) pairs. env is the entry's `env` dict (extra hook environment, e.g.
    FACTORY_AGENT; T-0871), likewise sorted pairs. An entry with neither keeps the 2-tuple, a seeded one the 3-tuple."""
    if isinstance(e, str):
        return expand(e), None
    if isinstance(e, dict) and isinstance(e.get("cmd"), str):
        role = e.get("role")
        if role is not None and not (isinstance(role, str) and role):
            raise UsageError(f"bad corpus entry {e!r}: 'role' must be a non-empty string")
        seed = env = None
        if "config" in e:
            seed = e["config"]
            if not (isinstance(seed, dict) and seed and all(isinstance(k, str) and k and isinstance(v, str)
                                                             for k, v in seed.items())):
                raise UsageError(f"bad corpus entry {e!r}: 'config' must be a non-empty object of string keys to string values")
            seed = tuple(sorted(seed.items()))
        if "env" in e:
            env = e["env"]
            if not (isinstance(env, dict) and env and all(isinstance(k, str) and k and isinstance(v, str)
                                                           for k, v in env.items())):
                raise UsageError(f"bad corpus entry {e!r}: 'env' must be a non-empty object of string keys to string values")
            env = tuple(sorted(env.items()))
        if env:
            return expand(e["cmd"]), role, seed, env
        return (expand(e["cmd"]), role, seed) if seed else (expand(e["cmd"]), role)
    raise UsageError(f"bad corpus entry {e!r}: expected a string or an object with a string 'cmd'")


def load_corpus(path):
    try:
        with open(path, encoding="utf-8") as f:
            data = json.load(f)
    except (OSError, ValueError) as e:
        raise UsageError(f"cannot read corpus {path}: {e}")
    if not isinstance(data, dict):
        raise UsageError(f"corpus {path} must be a JSON object with must_block and must_allow lists")
    cmds = []
    for key in ("must_block", "must_allow"):
        entries = data.get(key, [])
        if not isinstance(entries, list):
            raise UsageError(f"corpus {path}: {key} must be a list")
        cmds += [entry_item(e) for e in entries]
    return cmds


def load_allow(path):
    if not path:
        return set()
    try:
        with open(path, encoding="utf-8") as f:
            text = f.read()
    except OSError as e:
        raise UsageError(f"cannot read --allow-loosen {path}: {e}")
    if path.endswith(".json"):
        try:
            data = json.loads(text)
        except ValueError as e:
            raise UsageError(f"--allow-loosen {path} is not valid JSON: {e}")
        if not isinstance(data, list) or not all(isinstance(x, str) for x in data):
            raise UsageError(f"--allow-loosen {path} must be a JSON list of command strings")
        return set(data)
    return {ln for ln in text.splitlines() if ln}


def seed_label(seed):
    return "config=" + ",".join(f"{k}:{v}" for k, v in seed)


def make_seed_home(parent, seed, n, worktrees=None):
    """A fresh temp FACTORY_HOME under parent holding db/factory.db with the factory's config table (same DDL as the
    factory CLI schema), seeded with exactly `seed` ((key, value) pairs). worktrees ({"T-0001": path}) adds the tasks
    rows the coder push pin resolves the own worktree from. Returns the home path."""
    home = os.path.join(parent, f"seed-{n}")
    os.makedirs(os.path.join(home, "db"))
    con = sqlite3.connect(os.path.join(home, "db", "factory.db"))
    try:
        with con:
            con.execute("CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT NOT NULL)")
            con.executemany("INSERT INTO config(key, value) VALUES (?, ?)", list(seed))
            if worktrees:
                con.execute("CREATE TABLE tasks (id TEXT PRIMARY KEY, worktree TEXT)")
                con.executemany("INSERT INTO tasks(id, worktree) VALUES (?, ?)", list(worktrees.items()))
    finally:
        con.close()
    # T-0767: the Go guard reads the DB rows from run/guard-export.json (what the tick writes), not the DB: same rows
    os.makedirs(os.path.join(home, "run"))
    doc = {"version": 1, "generated_at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
           "config": dict(seed), "agent_goals": {}, "task_worktrees": dict(worktrees or {})}
    with open(os.path.join(home, "run", "guard-export.json"), "w") as fh:
        json.dump(doc, fh)
    return home


def run_hook(hook, argv_extra, cmd, timeout, env=None, exe=None, cwd=None):
    """Run the Python hook, or the executable `exe` (a Go candidate) when given, on one command."""
    payload = {"tool_name": "Bash", "tool_input": {"command": cmd}}
    if cwd:
        payload["cwd"] = cwd
    payload = json.dumps(payload)
    env = hook_env(cmd, env)
    try:
        argv = [*exe, *argv_extra] if exe else [sys.executable, hook, *argv_extra]
        p = subprocess.run(argv, input=payload, capture_output=True,
                           text=True, timeout=timeout, env=env)
    except subprocess.TimeoutExpired:
        return TIMEOUT
    return p.returncode


def compare(base_hook, pr_hook, items, modes, role, timeout, temp_home, jobs=4, pr_exe=None, pr_extra=(), roles=None):
    """Return (lines, loosened_cmds, bad_cmds, tightened): report lines and the commands that fail the gate.

    items are (cmd, role-or-None). A role-tagged item runs in worker mode only, under its own role; the
    evaluator hook rules need a temp FACTORY_HOME in the hook's environment, so any evaluator run gets one.
    pr_exe/pr_extra run an executable (with extra argv) as the PR side instead of the Python hook. roles (a
    list) runs each untagged item in worker mode once per role and labels worker lines `worker role=<role>`."""
    work = []
    for c, r, *sd in items:
        seed = sd[0] if sd else None
        xenv = sd[1] if len(sd) > 1 else None
        if r is None:
            for m in modes:
                work += [(m, x, c, seed, xenv) for x in roles] if roles and m == "worker" else [(m, role, c, seed, xenv)]
        elif "worker" in modes:
            work.append(("worker", r, c, seed, xenv))
    work = list(dict.fromkeys(work))
    seed_tmp = tempfile.TemporaryDirectory(prefix="guard-diff-seed-")
    try:
        # one temp home per distinct (config seed, task of the entry's FACTORY_AGENT); a task adds its tasks row
        specs = dict.fromkeys((w[3] or (), agent_task(w[4])) for w in work if w[3] or agent_task(w[4]))
        seed_homes = {sp: make_seed_home(seed_tmp.name, sp[0], i, {sp[1]: worktree_fixture()} if sp[1] else None)
                      for i, sp in enumerate(specs)}
    except BaseException:
        seed_tmp.cleanup()
        raise

    def one(item):
        m, r, c, seed, xenv = item
        argv = ["--role", r] if m == "worker" else ["--hq"]
        env = {**os.environ, **dict(xenv)} if xenv else None  # T-0871: the entry's own env (e.g. FACTORY_AGENT)
        task = agent_task(xenv)
        cwd = worktree_fixture() if task else None  # a task's agent runs from its own worktree fixture
        if seed or task:  # a seeded entry runs both sides against its own temp home (its config rows), never the live DB
            env = {**(env or os.environ), "FACTORY_HOME": seed_homes[(seed or (), task)]}
        elif m == "worker" and r == "evaluator":
            env = {**(env or os.environ), "FACTORY_HOME": temp_home}
        if roles:
            label = "hq" if m == "hq" else f"worker role={r}"
        else:
            label = m if m == "hq" or r == role else f"{m}:{r}"
        if seed:
            label += " " + seed_label(seed)
        if xenv:
            label += " env=" + ",".join(f"{k}:{v}" for k, v in xenv)
        return (label, c, run_hook(base_hook, argv, c, timeout, env, cwd=cwd),
                run_hook(pr_hook, [*argv, *pr_extra], c, timeout, env, pr_exe, cwd=cwd))

    try:
        with ThreadPoolExecutor(max_workers=jobs) as ex:
            results = list(ex.map(one, work))
    finally:
        seed_tmp.cleanup()
    lines, loosened, bad = [], [], []
    tight = 0
    for m, c, b, p in results:
        if b == p and TIMEOUT not in (b, p):
            continue
        if TIMEOUT in (b, p):
            kind = "TIMEOUT"
            bad.append(c)
        elif b == BLOCK:
            kind = "LOOSENED"
            loosened.append(c)
        elif p == BLOCK:
            kind = "TIGHTENED"
            tight += 1
        else:
            kind = "CHANGED"
        lines.append(f"{kind} base={b} pr={p} {m} {c}")
    return lines, loosened, bad, tight


SETTINGS_DIR = "roles/settings"
OWNER_HOME = re.compile(r"(/?)/ho" r"me/[^/\s\"]+/factory")
OWNER_HOME_DIR = re.compile(r"(/?)/ho" r"me/[^/\s\"]+")


def normalise_rule(rule):
    """A rule from a pre-template `roles/settings/<role>.json` on the base ref, spelled like the templates
    (`/@FACTORY_LIVE_HOME@` in Edit/Write rules, `@FACTORY_LIVE_HOME@` elsewhere), so the two compare."""
    rule = OWNER_HOME.sub(lambda m: ("/" if m.group(1) else "") + "@FACTORY_LIVE_HOME@", rule)
    return OWNER_HOME_DIR.sub(lambda m: ("/" if m.group(1) else "") + "@FACTORY_HOME_DIR@", rule)


class SettingsShapeError(UsageError):
    """A settings file whose JSON has the wrong shape; never treated as "no rules" (fail closed)."""


class AutoRule(str):
    """An autoMode entry rendered `<key>: <text>` or `<key>=<json>`; carries the full key so a key containing a colon
    or equals is never cut short. Equality and hash include the key."""

    def __new__(cls, key, text):
        self = super().__new__(cls, text)
        self.key = key
        return self

    def __eq__(self, other):
        return str.__eq__(self, other) and getattr(other, "key", self.key) == self.key

    def __ne__(self, other):
        return not self == other

    def __hash__(self):
        return hash((self.key, str(self)))


def _shape(name, key, value, want):
    kinds = {"object": dict, "list": list}
    if not isinstance(value, kinds[want]):
        raise SettingsShapeError(f"settings {name}: {key} must be a JSON {want}, got {type(value).__name__}; fix the file")
    return value


def settings_rules(text, legacy=False, name="settings"):
    """{(kind, rule)} of one settings JSON text: the allow/deny lists, every hook as
    `<event>|<matcher>|<type>|<command>` (a missing matcher or type is an empty field), each hook's timeout as its own
    `hook-timeout` entry `<that text>|timeout=<N>`, every permissions.ask rule, every autoMode entry as `<key>: <text>`
    (a non-list value as `<key>=<json>`) and every env entry as `K=V`. A wrong shape raises SettingsShapeError
    (a UsageError naming <name> and the key). A missing key is fine; null is not."""
    d = json.loads(text)
    _shape(name, "top level", d, "object")
    fix = normalise_rule if legacy else (lambda x: x)
    out = set()
    perms = _shape(name, "permissions", d.get("permissions", {}), "object")
    for kind in ("allow", "deny", "ask"):
        for rule in _shape(name, f"permissions.{kind}", perms.get(kind, []), "list"):
            if not isinstance(rule, str):
                raise SettingsShapeError(f"settings {name}: permissions.{kind} entries must be strings; fix the file")
            out.add((kind, fix(rule)))
    for event, groups in _shape(name, "hooks", d.get("hooks", {}), "object").items():
        for group in _shape(name, f"hooks.{event}", groups, "list"):
            _shape(name, f"hooks.{event}[]", group, "object")
            for h in _shape(name, f"hooks.{event}[].hooks", group.get("hooks", []), "list"):
                _shape(name, f"hooks.{event}[].hooks[]", h, "object")
                ident = "|".join((event, group.get("matcher", ""), h.get("type", ""), fix(h.get("command", ""))))
                out.add(("hook", ident))
                timeout = h.get("timeout")
                out.add(("hook-timeout", f"{ident}|timeout={'' if timeout is None else json.dumps(timeout)}"))
    for key, value in _shape(name, "autoMode", d.get("autoMode", {}), "object").items():
        if isinstance(value, list):
            for item in value:
                out.add(("autoMode", AutoRule(key, f"{key}: {fix(item) if isinstance(item, str) else json.dumps(item, sort_keys=True)}")))
        else:
            out.add(("autoMode", AutoRule(key, f"{key}={json.dumps(value, sort_keys=True)}")))
    for key, value in _shape(name, "env", d.get("env", {}), "object").items():
        out.add(("env", f"{key}={fix(str(value))}"))
    return out


def base_settings_roles(repo, base):
    """Roles that have a roles/settings/<role>.json.in or legacy <role>.json on <base> (none if the dir is absent)."""
    out = git(repo, "ls-tree", "-z", "--full-tree", "--name-only", "--end-of-options", base, SETTINGS_DIR + "/")
    roles = set()
    for path in out.split("\0"):
        name = os.path.basename(path)
        for suffix in (".json.in", ".json"):
            if name.endswith(suffix):
                roles.add(name[:-len(suffix)])
                break
    return roles


def automode_key(rule):
    """The full autoMode key of an AutoRule entry."""
    return rule.key


def compare_settings(repo, top, base):
    """Diff the rules of every roles/settings/<role>.json.in against <base>, over the union of the roles on base and
    in the tree. Returns (lines, loosened): a removed deny, ask, hook, hook timeout, autoMode or env entry, an added allow,
    or a removed template is LOOSENED; a removed allow or an added deny, ask or hook is TIGHTENED; an added env,
    autoMode or hook-timeout entry is SETTINGS-CHANGED. A timeout line of a hook that exists on only one side is
    dropped (the hook line itself reports the add or removal)."""
    sdir = os.path.join(top, SETTINGS_DIR)
    tree_roles = set()
    if os.path.isdir(sdir):
        tree_roles = {n[:-len(".json.in")] for n in os.listdir(sdir) if n.endswith(".json.in")}
    lines, loosened = [], []
    for role in sorted(tree_roles | base_settings_roles(repo, base)):
        name = f"{role}.json.in"
        if role not in tree_roles:
            lines.append(f"SETTINGS-LOOSENED {role} template removed")
            loosened.append(f"{role} template removed")
            continue
        try:
            with open(os.path.join(sdir, name), encoding="utf-8") as f:
                new = settings_rules(f.read(), name=name)
        except (OSError, ValueError) as e:
            raise UsageError(f"cannot read settings template {name}: {e}")
        try:
            old, legacy = settings_rules(git(repo, "show", "--end-of-options", f"{base}:{SETTINGS_DIR}/{name}"),
                                         name=f"{base}:{name}"), False
        except SettingsShapeError:
            raise
        except UsageError:
            try:  # the base predates the templates: its role file holds the owner's absolute paths
                old, legacy = settings_rules(git(repo, "show", "--end-of-options", f"{base}:{SETTINGS_DIR}/{role}.json"),
                                             True, name=f"{base}:{role}.json"), True
            except SettingsShapeError:
                raise
            except (UsageError, ValueError):
                lines.append(f"SETTINGS-NEW {role} (no {SETTINGS_DIR}/{role}.json[.in] on {base})")
                continue
        except ValueError as e:
            raise UsageError(f"base {name} is not valid JSON: {e}")
        old_hooks = {r for k, r in old if k == "hook"}
        new_hooks = {r for k, r in new if k == "hook"}

        def paired(kind, rule):  # a timeout line only counts for a hook present on both sides
            return kind != "hook-timeout" or rule.rsplit("|timeout=", 1)[0] in (old_hooks & new_hooks)
        removed_automode_keys = {automode_key(r) for k, r in old - new if k == "autoMode"}
        for kind, rule in sorted(x for x in old - new if paired(*x)):
            lines.append(f"SETTINGS-{'TIGHTENED' if kind == 'allow' else 'LOOSENED'} {role} {kind} removed: {rule}")
            if kind != "allow":
                loosened.append(f"{role} {kind} removed: {rule}")
        for kind, rule in sorted(x for x in new - old if paired(*x)):
            word = {"allow": "LOOSENED", "env": "CHANGED", "autoMode": "CHANGED", "hook-timeout": "CHANGED"}.get(kind, "TIGHTENED")
            lines.append(f"SETTINGS-{word} {role} {kind} added: {rule}")
            if kind == "allow" or (kind == "autoMode" and automode_key(rule) not in removed_automode_keys):
                loosened.append(f"{role} {kind} added: {rule}")  # no removed entry under the same key: fail closed
    return lines, loosened


ROLE_CALL = re.compile(r"worker-guard\.py --role ([A-Za-z0-9_-]+)")


def template_roles(top):
    """Roles whose roles/settings/<role>.json.in calls the hook with --role, derived from the templates."""
    sdir = os.path.join(top, SETTINGS_DIR)
    roles = set()
    try:
        names = sorted(os.listdir(sdir))
        for name in names:
            if name.endswith(".json.in"):
                with open(os.path.join(sdir, name), encoding="utf-8") as f:
                    roles.update(ROLE_CALL.findall(f.read()))
    except OSError as e:
        raise UsageError(f"cannot read settings templates in {sdir}: {e}; --roles all needs roles/settings/*.json.in")
    if not roles:
        raise UsageError(f"no roles/settings/*.json.in under {top} calls worker-guard.py --role; cannot derive --roles all")
    return sorted(roles)


def go_candidate(top, go_bin, tmp):
    """Path of the Go candidate: --go-bin, or `go build` of go/cmd/worker-guard into tmp. Fails closed (UsageError)."""
    if go_bin:
        if not (os.path.isfile(go_bin) and os.access(go_bin, os.X_OK)):
            raise UsageError(f"--go-bin {go_bin} is not an executable file; pass a built worker-guard binary")
        return os.path.abspath(go_bin)
    go = shutil.which("go")
    if not go:
        raise UsageError("go not found on PATH; install Go or pass --go-bin PATH (--impl go cannot run without a candidate)")
    out = os.path.join(tmp, "worker-guard")
    # never let go.mod's `toolchain` line trigger a download (ADR 0001): local toolchain only, no network
    env = dict(os.environ, GOTOOLCHAIN="local", GOFLAGS="-mod=readonly", GOPROXY="off")
    try:
        p = subprocess.run([go, "build", "-o", out, "./cmd/worker-guard"], cwd=os.path.join(top, "go"),
                           capture_output=True, text=True, timeout=600, env=env)
    except (OSError, subprocess.TimeoutExpired) as e:
        raise UsageError(f"go build failed to run: {e}; check the Go toolchain")
    if p.returncode != 0:
        raise UsageError(f"go build ./go/cmd/worker-guard failed: {p.stderr.strip()}; fix the Go build first")
    return out


def load_refused():
    """M16816: run locally (CI unset) at load1 >= cap, only one suite box-wide; returns the refusal text or None."""
    sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "tests"))
    import _slot
    return _slot.local_run_gate()


def main(argv=None):
    ap = argparse.ArgumentParser(description="Diff the base worker-guard hook against the working-tree hook.")
    ap.add_argument("--base", help="git ref holding the base hook, e.g. origin/main (required unless --impl go)")
    ap.add_argument("--impl", choices=("python", "go"), default="python",
                    help="python: base ref vs working tree hook (default); go: working-tree Python hook vs the Go binary")
    ap.add_argument("--go-bin", help="--impl go: prebuilt worker-guard binary (default: go build ./go/cmd/worker-guard)")
    ap.add_argument("--roles", choices=("all",), help="all: worker mode under every role whose settings template calls the hook")
    ap.add_argument("--strict", action="store_true", help="TIGHTENED and CHANGED lines also exit 1 (the G-016 gate)")
    ap.add_argument("--hook", default=DEFAULT_HOOK, help="hook under test (default %(default)s)")
    ap.add_argument("--corpus", action="append", help="corpus JSON file (repeatable; default ci/guard_corpus.json)")
    ap.add_argument("--mode", choices=("worker", "hq", "both"), default="both")
    ap.add_argument("--role", default="coder", help="role for worker mode (default %(default)s)")
    ap.add_argument("--allow-loosen", help="file of commands whose LOOSENED line is planned (one per line, or a .json list)")
    ap.add_argument("--repo", default=".", help="git repo holding --base (default current directory)")
    ap.add_argument("--timeout", type=float, default=5.0, help="seconds per hook call (default %(default)s)")
    try:
        a = ap.parse_args(argv)
    except SystemExit as e:
        return 2 if e.code else 0  # --help and usage errors are never load-gated
    refused = load_refused()
    if refused:
        sys.stderr.write(f"guard-diff: {refused}\n")
        return 5  # same code as ci/affected.py EXIT_LOAD
    go = a.impl == "go"
    try:
        if go and a.allow_loosen is not None:
            raise UsageError("no --allow-loosen with --impl go (the gate is zero loosened and zero tightened)")
        if not go and (a.go_bin or a.strict):
            raise UsageError("--go-bin and --strict need --impl go")
        if not go and not a.base:
            raise UsageError("--base is required (except with --impl go)")
        top = git(a.repo, "rev-parse", "--show-toplevel").strip()
        if not go and a.base.startswith("-"):
            raise UsageError(f"--base {a.base!r} looks like an option; pass a git ref such as origin/main")
        hook = os.path.abspath(a.hook)
        if not os.path.isfile(hook):
            raise UsageError(f"hook {a.hook} not found; pass --hook <path>")
        rel = os.path.relpath(os.path.realpath(hook), os.path.realpath(top))
        if rel.startswith(".."):
            rel = DEFAULT_HOOK  # hook copy outside the repo (scratch experiment): compare with main's hook
        base_src = None if go else git(a.repo, "show", "--end-of-options", f"{a.base}:{rel}")
        roles = template_roles(top) if a.roles else None
        cmds = []
        for path in a.corpus or [DEFAULT_CORPUS]:
            cmds += load_corpus(path)
        cmds = list(dict.fromkeys(cmds))
        if not cmds:
            raise UsageError("corpus has no commands; nothing to compare")
        allow = load_allow(a.allow_loosen)
    except UsageError as e:
        sys.stderr.write(f"guard-diff: {e}\n")
        return 2
    modes = ["worker", "hq"] if a.mode == "both" else [a.mode]
    with tempfile.TemporaryDirectory(prefix="guard-diff-") as tmp:
        if go:
            try:
                gobin = go_candidate(top, a.go_bin, tmp)
            except UsageError as e:
                sys.stderr.write(f"guard-diff: {e}\n")
                return 2
            temp_home = os.path.join(tmp, "home")
            os.makedirs(temp_home)
            lines, loosened, bad, tight = compare(hook, hook, cmds, modes, a.role, a.timeout, temp_home,
                                                  pr_exe=[gobin], pr_extra=["--py-hook", hook], roles=roles)
            return report(lines, loosened, bad, tight, len(cmds), strict=a.strict, go=True)
        # mirror the repo layout: the hook derives its live home from its own path (dirname^3)
        base_hook = os.path.join(tmp, "base", rel)
        os.makedirs(os.path.dirname(base_hook))
        with open(base_hook, "w", encoding="utf-8") as f:
            f.write(base_src)
        try:
            extract_base_ci(a.repo, a.base, os.path.join(tmp, "base"))
        except UsageError as e:
            sys.stderr.write(f"guard-diff: {e}\n")
            return 2
        temp_home = os.path.join(tmp, "home")
        os.makedirs(temp_home)
        lines, loosened, bad, tight = compare(base_hook, hook, cmds, modes, a.role, a.timeout, temp_home)
    try:
        s_lines, s_loosened = compare_settings(a.repo, top, a.base)
    except UsageError as e:
        sys.stderr.write(f"guard-diff: {e}\n")
        return 2
    for ln in lines + s_lines:
        print(ln)
    unplanned = [c for c in loosened + s_loosened if c not in allow] + bad
    loosened += s_loosened
    tight += sum(1 for ln in s_lines if ln.startswith("SETTINGS-TIGHTENED"))
    print(f"guard-diff: {len(cmds)} commands, {len(loosened)} loosened, {tight} tightened"
          + (f", {len(bad)} timeout" if bad else ""))
    return 1 if unplanned else 0


def report(lines, loosened, bad, tight, n, strict, go):
    """Print the --impl go report; exit code 1 on LOOSENED/TIMEOUT, and with strict on TIGHTENED/CHANGED too."""
    for ln in lines:
        print(ln)
    print("settings diff: skipped (--impl go)")
    changed = sum(1 for ln in lines if ln.startswith("CHANGED "))
    print(f"guard-diff: {n} commands, {len(loosened)} loosened, {tight} tightened"
          + (f", {changed} changed" if changed else "") + (f", {len(bad)} timeout" if bad else ""))
    return 1 if loosened or bad or (strict and (tight or changed)) else 0


if __name__ == "__main__":
    sys.exit(main())
