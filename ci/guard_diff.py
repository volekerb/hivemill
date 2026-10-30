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
    return os.path.realpath(path)


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
    """(cmd, role) or (cmd, role, seed): role is the corpus entry's own `role` tag (e.g. "evaluator"), or None for the
    --role default. seed is the entry's `config` dict (config rows the hook reads from the factory DB), as a tuple of
    sorted (key, value) pairs; an unseeded entry stays a 2-tuple."""
    if isinstance(e, str):
        return expand(e), None
    if isinstance(e, dict) and isinstance(e.get("cmd"), str):
        role = e.get("role")
        if role is not None and not (isinstance(role, str) and role):
            raise UsageError(f"bad corpus entry {e!r}: 'role' must be a non-empty string")
        if "config" not in e:
            return expand(e["cmd"]), role
        seed = e["config"]
        if not (isinstance(seed, dict) and seed and all(isinstance(k, str) and k and isinstance(v, str)
                                                         for k, v in seed.items())):
            raise UsageError(f"bad corpus entry {e!r}: 'config' must be a non-empty object of string keys to string values")
        return expand(e["cmd"]), role, tuple(sorted(seed.items()))
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


def make_seed_home(parent, seed, n):
    """A fresh temp FACTORY_HOME under parent holding db/factory.db with the factory's config table (same DDL as the
    factory CLI schema), seeded with exactly `seed` ((key, value) pairs). Returns the home path."""
    home = os.path.join(parent, f"seed-{n}")
    os.makedirs(os.path.join(home, "db"))
    con = sqlite3.connect(os.path.join(home, "db", "factory.db"))
    try:
        with con:
            con.execute("CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT NOT NULL)")
            con.executemany("INSERT INTO config(key, value) VALUES (?, ?)", list(seed))
    finally:
        con.close()
    return home


def run_hook(hook, argv_extra, cmd, timeout, env=None, exe=None):
    """Run the Python hook, or the executable `exe` (a Go candidate) when given, on one command."""
    payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}})
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
        if r is None:
            for m in modes:
                work += [(m, x, c, seed) for x in roles] if roles and m == "worker" else [(m, role, c, seed)]
        elif "worker" in modes:
            work.append(("worker", r, c, seed))
    work = list(dict.fromkeys(work))
    seed_tmp = tempfile.TemporaryDirectory(prefix="guard-diff-seed-")
    seed_homes = {s: make_seed_home(seed_tmp.name, s, i)
                  for i, s in enumerate(dict.fromkeys(w[3] for w in work if w[3]))}

    def one(item):
        m, r, c, seed = item
        argv = ["--role", r] if m == "worker" else ["--hq"]
        env = None
        if seed:  # a seeded entry runs both sides against its own temp home (its config rows), never the live DB
            env = {**os.environ, "FACTORY_HOME": seed_homes[seed]}
        elif m == "worker" and r == "evaluator":
            env = {**os.environ, "FACTORY_HOME": temp_home}
        if roles:
            label = "hq" if m == "hq" else f"worker role={r}"
        else:
            label = m if m == "hq" or r == role else f"{m}:{r}"
        if seed:
            label += " " + seed_label(seed)
        return (label, c, run_hook(base_hook, argv, c, timeout, env),
                run_hook(pr_hook, [*argv, *pr_extra], c, timeout, env, pr_exe))

    with ThreadPoolExecutor(max_workers=jobs) as ex:
        results = list(ex.map(one, work))
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


def settings_rules(text, legacy=False):
    """{(kind, rule)} for the allow/deny lists plus the hook commands of one settings JSON text."""
    d = json.loads(text)
    out = set()
    for kind in ("allow", "deny"):
        for rule in d.get("permissions", {}).get(kind, []):
            out.add((kind, normalise_rule(rule) if legacy else rule))
    for group in d.get("hooks", {}).get("PreToolUse", []):
        for h in group.get("hooks", []):
            cmd = h.get("command", "")
            out.add(("hook", normalise_rule(cmd) if legacy else cmd))
    return out


def compare_settings(repo, top, base):
    """Diff the rules of every roles/settings/<role>.json.in in the working tree against <base>. Returns
    (lines, loosened): a removed deny, an added allow or a removed hook is LOOSENED; the reverse is TIGHTENED."""
    sdir = os.path.join(top, SETTINGS_DIR)
    if not os.path.isdir(sdir):
        return [], []
    lines, loosened = [], []
    for name in sorted(os.listdir(sdir)):
        if not name.endswith(".json.in"):
            continue
        role = name[:-len(".json.in")]
        try:
            with open(os.path.join(sdir, name), encoding="utf-8") as f:
                new = settings_rules(f.read())
        except (OSError, ValueError) as e:
            raise UsageError(f"cannot read settings template {name}: {e}")
        try:
            old, legacy = settings_rules(git(repo, "show", "--end-of-options", f"{base}:{SETTINGS_DIR}/{name}")), False
        except UsageError:
            try:  # the base predates the templates: its role file holds the owner's absolute paths
                old, legacy = settings_rules(git(repo, "show", "--end-of-options", f"{base}:{SETTINGS_DIR}/{role}.json"),
                                             True), True
            except (UsageError, ValueError):
                lines.append(f"SETTINGS-NEW {role} (no {SETTINGS_DIR}/{role}.json[.in] on {base})")
                continue
        except ValueError as e:
            raise UsageError(f"base {name} is not valid JSON: {e}")
        for kind, rule in sorted(old - new):
            lines.append(f"SETTINGS-{'LOOSENED' if kind in ('deny', 'hook') else 'TIGHTENED'} {role} {kind} removed: {rule}")
            if kind in ("deny", "hook"):
                loosened.append(f"{role} {kind} removed: {rule}")
        for kind, rule in sorted(new - old):
            lines.append(f"SETTINGS-{'LOOSENED' if kind == 'allow' else 'TIGHTENED'} {role} {kind} added: {rule}")
            if kind == "allow":
                loosened.append(f"{role} {kind} added: {rule}")
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
        return 2 if e.code else 0
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
