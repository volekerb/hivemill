"""Test harness: runs bin/factory as a subprocess against a throwaway FACTORY_HOME.

Safety: the temp home is its own git repo outside ~/factory and the checkout, HOME is a temp
dir, and `herdr` / `gh` on PATH are the stubs from tests/stubs. The live DB is never touched.
"""
import ast
import contextlib
import io
import importlib.machinery
import importlib.util
import json
import os
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import time
import traceback
import warnings
import unittest
from pathlib import Path

import _gitenv  # hermetic git: tests/_gitenv.py
import _slot

REPO = Path(__file__).resolve().parent.parent
FACTORY = REPO / "bin" / "factory"  # the stub: run it
FACTORY_SRC = REPO / "bin" / "factory_cli.py"  # the code: read or import it
STUBS = Path(__file__).resolve().parent / "stubs"
FIXTURES = Path(__file__).resolve().parent / "fixtures"


def _literal(name):
    """Read a module-level literal from bin/factory without importing it (no side effects)."""
    for node in ast.parse(FACTORY_SRC.read_text()).body:
        if isinstance(node, ast.Assign) and any(getattr(t, "id", None) == name for t in node.targets):
            return ast.literal_eval(node.value)
    raise KeyError(name)


MODELS = _literal("MODELS")
ROLES = _literal("ROLES")
PR_URL = "https://github.com/zz-test/repo/pull/1"


def pr_body(words=0):
    """A PR body that follows .github/pull_request_template.md; `words` pads the Summary to that many words outside <details>."""
    pad = " ".join(["word"] * words)
    return ("## Summary\nWhat changed. " + pad + "\n\n## Why\nT-0001 motivation.\n\n## Changes\n- one change\n\n"
            "## Test evidence\n`python3 -m unittest` -> OK\n\n## Risk / rollback\nRevert this PR.\n\n"
            "<details><summary>Acceptance walk</summary>\n\n## Acceptance walk\n1. met - cmd -> out\n</details>\n")


VALID_PR_BODY = pr_body()
SHA_A = "a" * 40
SHA_B = "b" * 40


def gates_summary(text="ok"):
    """A QA PASS summary carrying a valid `## Gates` section (factory verdict refuses a QA PASS without one)."""
    return f"{text}\n\n## Gates\n- unittest exit=0 cmd=python3 -m unittest discover -s tests -v\n"


# Commit identity comes from the environment: the config allow-list refuses user.* in repo config.
for _k, _v in (("NAME", "zz-test"), ("EMAIL", "zz-test@example.invalid")):
    for _who in ("AUTHOR", "COMMITTER"):
        os.environ.setdefault(f"GIT_{_who}_{_k}", _v)


def ancestor_pids(me, n=2, start=2001):
    """n fake ancestor pids, never the test process's own pid (CI shards run with small pids)."""
    return [x for x in range(start, start + n + 1) if x != me][:n]


_OPTS_WITH_VALUE = {"-b", "--branch", "--depth", "-o", "--origin", "--reference", "-c", "--config", "-j", "--push-option"}
_REMOTE_CMDS = {"push", "fetch", "pull", "ls-remote", "clone"}


def _remote_urls(name, cwd, env):
    """Every URL git would use for remote `name`: fetch url(s), pushurl(s) and insteadOf rewrites (get-url applies them)."""
    urls = []
    for flags in (["--push", "--all"], ["--all"]):
        out = subprocess.run(["git", "remote", "get-url", *flags, name], cwd=cwd, capture_output=True, text=True,
                             check=True, env=env).stdout
        urls += out.split()
    return urls


def _subcommand(args):
    """args without leading `-c key=value` pairs."""
    args = tuple(args)
    while args[:1] == ("-c",):
        args = args[2:]
    return args


def _check_remotes(args, cwd, env=None):
    """Refuse a git call whose remote is not a local path under the test tmp dir (R-b: fixtures once reached public main)."""
    env = {**os.environ, **(env or {})}
    pos, skip = [], False
    for a in args:
        if skip:
            skip = False
        elif a in _OPTS_WITH_VALUE:
            skip = True
        elif not a.startswith("-"):
            pos.append(a)
    if not pos:
        return
    if pos[0] == "remote" and len(pos) >= 4 and pos[1] in ("add", "set-url"):
        _gitenv.assert_local_remote(pos[3])
    elif pos[0] == "submodule":
        urls = [pos[2]] if pos[1:2] == ["add"] and len(pos) > 2 else []
        if (Path(cwd) / ".gitmodules").is_file():
            out = subprocess.run(["git", "config", "-f", ".gitmodules", "--get-regexp", r"^submodule\..*\.url$"], cwd=cwd,
                                 capture_output=True, text=True, env=env).stdout
            urls += [line.split(None, 1)[1] for line in out.splitlines() if " " in line]
        for url in urls:
            _gitenv.assert_local_remote(url)
    elif pos[0] in _REMOTE_CMDS:
        if "--all" in args and pos[0] in ("fetch", "push"):
            names = subprocess.run(["git", "remote"], cwd=cwd, capture_output=True, text=True, check=True,
                                   env=env).stdout.split()
            targets = [u for n in names for u in _remote_urls(n, cwd, env)]
        else:
            target = pos[1] if len(pos) > 1 else "origin"
            if pos[0] == "clone" or _gitenv.URL_LIKE.match(target) or "/" in target:
                targets = [target]
            else:
                targets = _remote_urls(target, cwd, env)
        for url in targets:
            _gitenv.assert_local_remote(url)


def git(*args, cwd, env=None, inert_remote=False, local_clone_of=None, check=True):
    """Run git in cwd. `env` is merged over the scrubbed process env (identity vars etc.); a repo-pointing GIT_* in it is
    refused. `inert_remote=True` lets `remote add|set-url` record a fake network URL for tests that only parse
    the configured URL; it never excuses a push/fetch/clone (those are refused). `local_clone_of=<dir>` lets `clone` read an
    existing local directory outside the tmp dir (a read-only source, e.g. this checkout); it excuses nothing else."""
    bad = [k for k in _gitenv.leaked(env or {}) if k not in _gitenv.CONFIG_ISOLATION]
    if bad:
        raise _gitenv.GitEnvLeak("harness.git env= must not set %s" % ", ".join(bad))
    if local_clone_of is not None:
        if args[:1] != ("clone",) or str(local_clone_of) not in args or not Path(local_clone_of).is_dir():
            raise _gitenv.RemoteRefused("local_clone_of is only for `clone` of an existing local directory")
    elif not (inert_remote and _subcommand(args)[:2] in (("remote", "add"), ("remote", "set-url"))):
        _check_remotes(args, cwd, env)
    p = subprocess.run(["git", *args], cwd=cwd, capture_output=True, text=True, env={**os.environ, **(env or {})})
    if check and p.returncode != 0:
        raise AssertionError(f"git {args}: {p.stderr}")
    return p.stdout.strip()


_RENDERED = {}


def rendered_settings_dir():
    """A temp dir holding every role's settings rendered from roles/settings/<role>.json.in with the real
    render function (live home derived exactly as bin/factory and the hook do). One render per test process."""
    if "dir" not in _RENDERED:
        d = Path(tempfile.mkdtemp(prefix="rendered-settings-"))
        import atexit
        atexit.register(shutil.rmtree, d, True)
        old, os.environ["FACTORY_HOME"] = os.environ.get("FACTORY_HOME"), str(REPO)
        try:
            loader = importlib.machinery.SourceFileLoader("factory_render_settings", str(FACTORY_SRC))
            mod = importlib.util.module_from_spec(importlib.util.spec_from_loader(loader.name, loader))
            loader.exec_module(mod)
        finally:
            if old is None:
                os.environ.pop("FACTORY_HOME", None)
            else:
                os.environ["FACTORY_HOME"] = old
        mod.render_all_settings(d)
        _RENDERED["dir"] = d
    return _RENDERED["dir"]


def rendered_settings(role):
    """Path of the rendered settings file for `role` (ends in /<role>.json like a spawned agent's --settings)."""
    return rendered_settings_dir() / f"{role}.json"


_TEMPLATE = {}


def _tree_key(root):
    """Cheap fingerprint of a tree: relative path, size and mtime of every file."""
    return tuple((str(f.relative_to(root)), f.stat().st_size, f.stat().st_mtime_ns)
                 for f in sorted(Path(root).rglob("*")) if f.is_file())


def _template_key():
    st = FACTORY.stat()
    return (st.st_mtime_ns, st.st_size, _tree_key(REPO / "roles"))


def _template_home():
    """The initialised factory home (git repo, DB, config rows), built once per test process and copied per test.
    Rebuilt when bin/factory or roles/ change. Removed at exit."""
    key = _template_key()
    if _TEMPLATE.get("key") != key:
        old = _TEMPLATE.pop("env", None)
        if old:
            old.cleanup()
        env = FactoryEnv(_build_template=True)
        con = sqlite3.connect(env.db_path)
        try:
            con.execute("PRAGMA wal_checkpoint(TRUNCATE)")
        finally:
            con.close()
        if not _TEMPLATE:
            import atexit
            atexit.register(lambda: _TEMPLATE.get("env") and _TEMPLATE["env"].cleanup())
        _TEMPLATE.update(key=key, env=env)
    return _TEMPLATE["env"].home_dir


def assert_hermetic(home_dir, stub_bin):
    """Raise unless this process would run the CLI against the temp home with the stub binary first on PATH:
    FACTORY_HOME inside the temp dir (never ~/factory) and the first agent-multiplexer binary on PATH being the stub (T-0647)."""
    tmp = Path(tempfile.gettempdir()).resolve()
    raw = os.environ.get("FACTORY_HOME", "")
    home = Path(raw).expanduser().resolve()
    if not raw or tmp not in home.parents or home != Path(home_dir).resolve():
        raise RuntimeError(f"FACTORY_HOME={raw!r} is not the temp test home {home_dir}; "
                           "a test would act on a real factory home. Use FactoryEnv.")
    found = shutil.which("herdr")
    if not found or Path(found).resolve().parent != Path(stub_bin).resolve():
        raise RuntimeError(f"the binary on PATH is {found!r}, not the stub in {stub_bin}; a test would reach the real one. "
                           "Put the harness stub_bin first on PATH.")


_FACTORY_CODE = None  # bin/factory_cli.py compiled once per process, for FactoryEnv.run_inproc


class FactoryEnv:
    def __init__(self, _build_template=False):
        self.inproc = False  # FactoryTestCase.INPROC: route run() through run_inproc
        self.home = Path(tempfile.mkdtemp(prefix="zz-test-factory-")).resolve()
        live = Path("~/factory").expanduser().resolve()
        for bad in (live, REPO):
            if bad in (self.home, *self.home.parents):  # not an assert: must survive python -O
                shutil.rmtree(self.home, ignore_errors=True)
                raise RuntimeError(f"temp home {self.home} is inside {bad}")
        self.user_home = self.home / "userhome"
        self.stub_bin = self.home / "stubbin"
        self.home_dir = self.home / "factory"
        self.db_path = self.home_dir / "db" / "factory.db"
        # fake /proc: the box's real claude processes must not decide whether a gate test passes
        self.proc_dir = self.home / "fakeproc-host"
        for d in (self.user_home, self.stub_bin, self.home_dir, self.proc_dir):
            d.mkdir()
        # herdr is a copy: the worker gate exempts only a real tests/stubs path (tests/test_worker_gate.py)
        shutil.copy2(STUBS / "herdr", self.stub_bin / "herdr")
        (self.stub_bin / "gh").symlink_to(STUBS / "gh")
        self.herdr_state = self.home / "herdr.json"
        self.gh_state = self.home / "gh.json"
        self.calls_file = self.home / "calls.jsonl"
        self.auto_sha = True  # add --sha <PR head> to `verdict --pass`; tests/test_done_verdict_guards.py turns it off
        self.head_sha = None  # set by use_git_head(); seed_pr's default head
        self.calls_file.touch()
        self.herdr_state.write_text("{}")
        self.gh_state.write_text("{}")
        # the CLI launcher the tick's freshness check expects: ~/.local/bin/factory -> <home>/bin/factory
        # (user_home is outside the template copy, so it is created for every env)
        (self.user_home / ".local" / "bin").mkdir(parents=True)
        (self.user_home / ".local" / "bin" / "factory").symlink_to(self.home_dir / "bin" / "factory")

        try:
            if _build_template or os.environ.get("FACTORY_TEST_NO_TEMPLATE"):
                self._init_home()
            else:
                shutil.copytree(_template_home(), self.home_dir, symlinks=True, dirs_exist_ok=True)
        except BaseException:
            self.cleanup()  # setUp failed, so addCleanup never runs
            raise

    def _init_home(self):
        git("init", "-q", "-b", "main", cwd=self.home_dir)
        (self.home_dir / "roles").symlink_to(REPO / "roles")
        (self.home_dir / "run").mkdir()
        self.run("init")
        self.run("config", "git_push", "0")
        # existing push tests exercise the state-branch push; tests/test_state_push.py drops this row to test the default
        self.run("config", "state_push", "1")
        # the box's real free memory must not decide whether a spawn test passes
        self.run("config", "min_mem_available_mb", "1")
        # the morning digest must not depend on the wall clock in unrelated tick tests
        self.run("config", "digest_hour", "24")
        # nor may the tick start a scout in unrelated tests (tests/test_scout_schedule.py turns it on)
        self.run("config", "scout_enabled", "0")

    # -- running the CLI -------------------------------------------------
    def env(self, agent=None, extra=None):
        env = {
            "PATH": f"{self.stub_bin}{os.pathsep}{os.environ.get('PATH', '/usr/bin:/bin')}",
            "HOME": str(self.user_home),
            **{k: v for k, v in os.environ.items() if k.startswith(("GIT_AUTHOR_", "GIT_COMMITTER_"))},
            "FACTORY_HOME": str(self.home_dir),
            "FAKE_HERDR_STATE": str(self.herdr_state),
            "FAKE_GH_STATE": str(self.gh_state),
            "FAKE_CALLS": str(self.calls_file),
            "GIT_CONFIG_NOSYSTEM": "1",
            "PYTHONDONTWRITEBYTECODE": "1",
            "LC_ALL": "C.UTF-8",
            "FACTORY_PROC_DIR": str(self.proc_dir),
            "FACTORY_TMP_STATVFS": "1000000000,500000000",  # /tmp reads 50% full whatever the host says
            "FACTORY_TMP_DIR": str(self.home / "faketmp"),
            "FACTORY_LOADAVG": "0",  # the box's real load must not decide whether a spawn test passes
            "FACTORY_KICKOFF_DELAY_S": "0",  # spawn() waits 2 s for the TUI; no test needs to
            "FACTORY_SHELL_SETTLE_S": "0.01",  # export_in_shell waits 1 s for the shell
            "FACTORY_RELAUNCH_POLL_S": "0.02",  # relaunch polls 0.5 s for the closed pane / dead process
            "FACTORY_STOP_CONFIRM_S": "0.2",  # force_stop polls the host up to 10 s for the pane/process to go
        }
        if "TZ" in os.environ:  # a TZ override in the parent must reach the CLI (clock-independence tests)
            env["TZ"] = os.environ["TZ"]
        if agent:
            env["FACTORY_AGENT"] = agent
        env.update(extra or {})
        return env

    def run(self, *args, agent=None, stdin=None, extra_env=None, inproc=None):
        if self.auto_sha and args[:1] == ("verdict",) and "--pass" in args and "--sha" not in args:
            args += self.pr_head_args(agent, extra_env)
        if self.inproc if inproc is None else inproc:
            return self.run_inproc(*args, agent=agent, stdin=stdin, extra_env=extra_env)
        return subprocess.run([sys.executable, str(FACTORY), *args], input=stdin, capture_output=True,
                              text=True, env=self.env(agent, extra_env), cwd=self.home, timeout=60)

    def run_inproc(self, *args, agent=None, stdin=None, extra_env=None):
        """`run` without the interpreter start-up: bin/factory main() runs in this process, in a fresh module namespace
        (compiled once), with os.environ, cwd, argv and stdio swapped for this env and restored afterwards. For tests that
        make many CLI calls and do not test process-level behaviour: pid/ppid identity (ancestor lookup), signals, the
        real exit status of the interpreter. Those stay on subprocess (`inproc=False`)."""
        global _FACTORY_CODE
        if _FACTORY_CODE is None:
            _FACTORY_CODE = compile(FACTORY_SRC.read_text(), str(FACTORY_SRC), "exec")
        env = self.env(agent, extra_env)
        saved = (dict(os.environ), os.getcwd(), sys.argv, sys.stdin, sys.path[:])
        out, err, rc = io.StringIO(), io.StringIO(), 0
        try:
            os.environ.clear()
            os.environ.update(env)
            time.tzset()  # the CLI's TZ, not the test runner's
            os.chdir(self.home)
            sys.argv = [str(FACTORY), *args]
            sys.stdin = io.StringIO(stdin or "")
            ns = {"__name__": "factory_inproc", "__file__": str(FACTORY_SRC), "__builtins__": __builtins__}
            with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err), warnings.catch_warnings():
                warnings.simplefilter("ignore", ResourceWarning)  # a fresh interpreter hides them; unittest shows them
                try:
                    exec(_FACTORY_CODE, ns)
                    ns["main"]()
                except SystemExit as e:
                    if e.code is None or isinstance(e.code, int):
                        rc = e.code or 0
                    else:
                        print(e.code, file=sys.stderr)
                        rc = 1
                except Exception:
                    traceback.print_exc()
                    rc = 1
        finally:
            os.environ.clear()
            os.environ.update(saved[0])
            time.tzset()
            os.chdir(saved[1])
            sys.argv, sys.stdin, sys.path[:] = saved[2], saved[3], saved[4]
        return subprocess.CompletedProcess([sys.executable, str(FACTORY), *args], rc, out.getvalue(), err.getvalue())

    def pr_head_args(self, agent, extra_env):
        """`--sha <PR head>` read through whatever gh the test put on PATH (some tests bring their own stub)."""
        p = subprocess.run(["gh", "pr", "view", PR_URL, "--json", "headRefOid"], capture_output=True, text=True,
                           env=self.env(agent, extra_env), cwd=self.home, timeout=60)
        try:
            return ("--sha", json.loads(p.stdout)["headRefOid"])
        except (ValueError, KeyError):
            return ()  # no PR seeded: the verdict command fails on its own terms

    # -- state for seeding / assertions ---------------------------------
    def db(self):
        con = sqlite3.connect(self.db_path)
        con.row_factory = sqlite3.Row
        return con

    def sql(self, query, args=()):
        con = self.db()
        try:
            rows = con.execute(query, args).fetchall()
            con.commit()
            return rows
        finally:
            con.close()

    def seed_project(self, pid="zz-test-proj", auto_merge=1):
        self.sql("INSERT INTO projects(id,name,auto_merge,created) VALUES(?,?,?,'t')", (pid, pid, auto_merge))

    def seed_goal(self, gid="G-001", project=None, title="zz-test goal"):
        self.sql("INSERT INTO goals(id,project_id,title,status,created,updated) VALUES(?,?,?,'active','t','t')",
                 (gid, project, title))

    def seed_team(self, tid="g-001-a", goal="G-001", lead=None):
        self.sql("INSERT INTO teams(id,goal_id,lead_agent,created) VALUES(?,?,?,'t')", (tid, goal, lead))

    def seed_task(self, tid="T-0001", goal="G-001", role="coder", status="in_review", pr_url=PR_URL, team=None,
                  spec="zz-test spec", acceptance="1. zz-test"):
        self.sql("INSERT INTO tasks(id,goal_id,team_id,title,role,status,pr_url,spec,acceptance,created,updated) "
                 "VALUES(?,?,?,?,?,?,?,?,?,'t','t')", (tid, goal, team, "zz-test task", role, status, pr_url, spec, acceptance))

    def seed_agent(self, name, role, goal=None, team=None, task=None, status="live", started="2026-01-01T00:00:00+00:00",
                   pane_id=None):
        self.sql("INSERT INTO agents(name,role,goal_id,team_id,task_id,status,started,pane_id) VALUES(?,?,?,?,?,?,?,?)",
                 (name, role, goal, team, task, status, started, pane_id))

    def use_git_head(self):
        """Back the default PR head with a real commit on a bare remote whose main it contains (refs/pull/1/head),
        and point the gh stub's repo_url at it: `factory judge --merge` verifies the head contains origin/main with
        a real git fetch. seed_pr then defaults to this sha instead of SHA_A."""
        remote, wt = self.home / "head-origin.git", self.home / "head-wt"
        git("init", "-q", "--bare", "-b", "main", str(remote), cwd=self.home)
        git("clone", "-q", str(remote), str(wt), cwd=self.home)
        git("config", "user.email", "zz-test@example.invalid", cwd=wt)
        git("config", "user.name", "zz-test", cwd=wt)
        for name, branch in (("base.txt", None), ("feature.txt", "feature")):
            if branch:
                git("push", "-q", "origin", "HEAD:main", cwd=wt)
                git("checkout", "-q", "-b", branch, cwd=wt)
            (wt / name).write_text(name + "\n")
            git("add", name, cwd=wt)
            git("commit", "-q", "-m", name, cwd=wt)
        git("push", "-q", "origin", "HEAD:refs/pull/1/head", cwd=wt)
        self.head_sha = git("rev-parse", "HEAD", cwd=wt)
        self.set_gh(repo_url=str(remote))
        return self.head_sha

    def seed_pr(self, url=PR_URL, sha=None, state="OPEN", draft=False, mergeable="MERGEABLE", checks=None, body=None):
        sha = sha or self.head_sha or SHA_A
        st = json.loads(self.gh_state.read_text())
        st.setdefault("prs", {})[url] = {
            "number": 1, "state": state, "headRefOid": sha, "headRefName": "factory/t-0001",
            "baseRefName": "main", "mergeable": mergeable, "isDraft": draft, "url": url,
            "statusCheckRollup": [] if checks is None else checks}
        if body is not None:
            st["prs"][url]["body"] = body
        self.gh_state.write_text(json.dumps(st))

    def set_gh(self, **kw):
        if kw.get("repo_url"):
            _gitenv.assert_local_remote(kw["repo_url"])
        st = json.loads(self.gh_state.read_text())
        st.update(kw)
        self.gh_state.write_text(json.dumps(st))

    def set_herdr(self, **kw):
        st = json.loads(self.herdr_state.read_text())
        st.update(kw)
        self.herdr_state.write_text(json.dumps(st))

    def write_usage(self, five_hour=0, seven_day=0, age_s=0):
        f = self.user_home / ".claude" / "usage-limits.json"
        f.parent.mkdir(exist_ok=True)
        f.write_text(json.dumps({"five_hour_pct": five_hour, "seven_day_pct": seven_day,
                                 "updated_at": time.time() - age_s}))

    def fake_claude(self, pid, ppid=1, argv=("claude",), proc_dir=None):
        """A fake /proc/<pid>/{cmdline,stat} for a claude process whose parent is `ppid`."""
        d = (proc_dir or self.proc_dir) / str(pid)
        d.mkdir()
        (d / "cmdline").write_bytes(b"\0".join(x.encode() for x in argv) + b"\0")
        (d / "stat").write_text(f"{pid} (claude) S {ppid} {pid} {pid} 0 -1 0\n")

    @contextlib.contextmanager
    def load_factory(self):
        """bin/factory imported in-process with this env (stubs on PATH, temp FACTORY_HOME), for unit-level calls."""
        # agent identity vars of the process running the tests (an agent pane) must not leak in
        keys = set(self.env()) | {"FACTORY_AGENT", "FACTORY_ROLE", "CLAUDECODE", "HERDR_PANE_ID"}
        saved = {k: os.environ.get(k) for k in keys}
        for k in keys - set(self.env()):
            os.environ.pop(k, None)
        os.environ.update(self.env())
        try:
            assert_hermetic(self.home_dir, self.stub_bin)
            loader = importlib.machinery.SourceFileLoader("factory_under_test", str(FACTORY_SRC))
            spec = importlib.util.spec_from_loader(loader.name, loader)
            mod = importlib.util.module_from_spec(spec)
            loader.exec_module(mod)
            opened, real_db = [], mod.db
            mod.db = lambda *a, **k: opened.append(real_db(*a, **k)) or opened[-1]
            try:
                # in-process spawn() prints SPAWNED/QUEUED lines: keep them out of the test runner's output (T-0647)
                with contextlib.redirect_stdout(io.StringIO()):
                    yield mod
            finally:
                for con in opened:
                    con.close()
        finally:
            for k, v in saved.items():
                if v is None:
                    os.environ.pop(k, None)
                else:
                    os.environ[k] = v

    def calls(self, tool=None):
        rows = [json.loads(l) for l in self.calls_file.read_text().splitlines() if l.strip()]
        return [r for r in rows if tool is None or r["tool"] == tool]

    def cleanup(self):
        shutil.rmtree(self.home, ignore_errors=True)


class FactoryTestCase(unittest.TestCase):
    def __init_subclass__(cls, **kw):
        super().__init_subclass__(**kw)
        _slot.note_module(cls.__module__)  # a full-suite run waits here, at load time, for a host-wide test slot

    INPROC = False  # True: env.run() calls bin/factory in-process (see FactoryEnv.run_inproc); inproc=False opts one call out

    def setUp(self):
        self.env = FactoryEnv()
        self.env.inproc = self.INPROC
        self.addCleanup(self.env.cleanup)

    def ok(self, *args, **kw):
        p = self.env.run(*args, **kw)
        self.assertEqual(p.returncode, 0, f"{args}: rc={p.returncode}\n{p.stdout}\n{p.stderr}")
        return p

    def refused(self, code, *args, **kw):
        p = self.env.run(*args, **kw)
        self.assertEqual(p.returncode, code, f"{args}: rc={p.returncode}\n{p.stdout}\n{p.stderr}")
        return p
