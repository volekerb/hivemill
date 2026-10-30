"""T-0637: tests that spawn git must never reach the real repo through an inherited GIT_DIR (it once wrote
core.bare=true and user.email=t@example.com into the shared .git/config)."""
import ast
import json
import os
import re
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import _gitenv

ISO = {"GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_SYSTEM": os.devnull, "GIT_CONFIG_NOSYSTEM": "1"}
PANE_CONFIG = {"GIT_CONFIG_COUNT": "2", "GIT_CONFIG_KEY_0": "a.b", "GIT_CONFIG_VALUE_0": "1", "GIT_CONFIG_KEY_1": "c.d",
               "GIT_CONFIG_VALUE_1": "2", "GIT_CONFIG_PARAMETERS": "'credential.helper'='x'", "GIT_ASKPASS": "/bin/true"}
AT = chr(64)  # no literal email-looking text in the fixtures (scrub gate)
TESTS = Path(__file__).resolve().parent
REPO = TESTS.parent
SPAWNS_GIT = re.compile(r"""\[\s*['"]git['"]|['"]git['"]|def git\b|_git\(|\.git\(""")
# git-heavy modules that finish in seconds; the rest are covered by the static check below.
RUN_WITH_GIT_DIR = ["test_affected.py", "test_changelog.py", "test_scrub_clean.py", "test_worker_guard_repocfg.py"]


def snapshot(git_dir):
    refs = subprocess.run(["git", "--git-dir", str(git_dir), "for-each-ref"], capture_output=True, text=True,
                          check=True).stdout
    return (Path(git_dir) / "config").read_bytes(), refs


class ScrubTest(unittest.TestCase):
    def test_drops_repo_pointing_vars_keeps_identity(self):
        env = {"PATH": "/bin", "GIT_DIR": "/x", "GIT_WORK_TREE": "/y", "GIT_COMMON_DIR": "/z", "GIT_INDEX_FILE": "/i",
               "GIT_AUTHOR_NAME": "a", "GIT_COMMITTER_EMAIL": "c@example.invalid", "GIT_CONFIG_GLOBAL": "/elsewhere/cfg"}
        out = _gitenv.scrub(env)
        for k in ("GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE"):
            self.assertNotIn(k, out)
        self.assertEqual(out["PATH"], "/bin")
        self.assertEqual(out["GIT_AUTHOR_NAME"], "a")
        self.assertEqual(out["GIT_COMMITTER_EMAIL"], "c@example.invalid")
        self.assertNotIn("GIT_CONFIG_GLOBAL", out)

    def test_isolate_config_points_user_and_system_config_at_devnull(self):
        out = _gitenv.scrub({"GIT_CONFIG_GLOBAL": "/elsewhere/cfg"}, isolate_config=True)
        self.assertEqual(out["GIT_CONFIG_GLOBAL"], os.devnull)
        self.assertEqual(out["GIT_CONFIG_SYSTEM"], os.devnull)

    def test_process_env_has_no_repo_pointing_vars(self):
        self.assertEqual(_gitenv.leaked(os.environ), [])

    def test_leaked_names_repo_pointing_vars_not_identity_or_allowlist(self):
        env = {"GIT_DIR": "/x", "GIT_INDEX_FILE": "/i", "GIT_AUTHOR_NAME": "a", "GIT_EDITOR": "true", "PATH": "/bin"}
        self.assertEqual(_gitenv.leaked(env), ["GIT_DIR", "GIT_INDEX_FILE"])

    def test_config_isolation_values_are_not_leaks_but_other_config_is(self):
        iso = _gitenv.scrub({}, isolate_config=True)
        self.assertEqual(_gitenv.leaked(iso), [])
        self.assertEqual(_gitenv.leaked({"GIT_CONFIG_GLOBAL": "/elsewhere/cfg"}), ["GIT_CONFIG_GLOBAL"])
        # isolation value checked per key (T-0801 nit): devnull is not a valid NOSYSTEM, "1" is not a valid GLOBAL
        self.assertEqual(_gitenv.leaked({"GIT_CONFIG_NOSYSTEM": os.devnull}), ["GIT_CONFIG_NOSYSTEM"])
        self.assertEqual(_gitenv.leaked({"GIT_CONFIG_GLOBAL": "1"}), ["GIT_CONFIG_GLOBAL"])
        r = self.import_gitenv(**iso)
        self.assertEqual(r.returncode, 0, r.stderr)

    def import_gitenv(self, **extra):
        """Import _gitenv in a child with a GIT_*-free env plus `extra`; stdout is the child's GIT_* env as JSON."""
        code = "import os, json, _gitenv; print(json.dumps({k: v for k, v in os.environ.items() if k.startswith('GIT_')}))"
        env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
        return subprocess.run([sys.executable, "-c", code], cwd=TESTS, capture_output=True, text=True,
                              env=dict(env, **extra))

    def test_pane_config_and_credential_env_is_normalised_not_refused(self):
        with tempfile.TemporaryDirectory() as d:
            cfg = Path(d) / "ghcred.cfg"
            cfg.write_text("[credential]\n\thelper = x\n")
            table = dict(PANE_CONFIG, GIT_CONFIG_GLOBAL=str(cfg), GIT_CONFIG_SYSTEM=str(cfg), GIT_CONFIG_NOSYSTEM="0",
                         GIT_CONFIG=str(cfg))
            for k, v in table.items():
                r = self.import_gitenv(**{k: v})
                self.assertEqual(r.returncode, 0, "%s: %s" % (k, r.stderr))
                self.assertEqual(json.loads(r.stdout), ISO, k)
            r = self.import_gitenv(GIT_TERMINAL_PROMPT="0", GIT_EDITOR="true", **table)
            self.assertEqual(r.returncode, 0, r.stderr)
            self.assertEqual(json.loads(r.stdout), dict(ISO, GIT_TERMINAL_PROMPT="0", GIT_EDITOR="true"))

    def test_repo_pointing_vars_still_raise_alone_and_mixed_with_pane_config(self):
        for k in ("GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
                  "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE", "GIT_CEILING_DIRECTORIES",
                  "GIT_DISCOVERY_ACROSS_FILESYSTEM", "GIT_SOMETHING_UNKNOWN"):
            for extra in ({}, dict(PANE_CONFIG, GIT_CONFIG_GLOBAL="/elsewhere/cfg")):
                r = self.import_gitenv(**{k: "/x", **extra})
                self.assertNotEqual(r.returncode, 0, k)
                self.assertIn("GitEnvLeak", r.stderr, k)
                self.assertIn(k, r.stderr)

    def test_importing_with_git_dir_set_raises(self):
        r = subprocess.run([sys.executable, "-c", "import _gitenv"], cwd=TESTS, capture_output=True, text=True,
                           env=dict(os.environ, GIT_DIR="/x/.git"))
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("GitEnvLeak", r.stderr)
        self.assertIn("GIT_DIR", r.stderr)


class RemoteGuardTest(unittest.TestCase):
    """R-b (M11068): no test may reach a real or network remote."""

    def test_network_and_foreign_remotes_are_refused(self):
        for url in ("https://github.com/volekerb/hivemill.git", "git" + AT + "github.com:volekerb/hivemill.git",
                    "ssh://git" + AT + "github.com/volekerb/hivemill", "/srv/someone/factory", "relative/path", str(REPO)):
            with self.assertRaises(_gitenv.RemoteRefused, msg=url):
                _gitenv.assert_local_remote(url, root=tempfile.gettempdir())

    def test_local_path_under_tmp_is_accepted(self):
        with tempfile.TemporaryDirectory() as d:
            _gitenv.assert_local_remote(str(Path(d) / "origin.git"))
            _gitenv.assert_local_remote("file://" + d + "/origin.git")

    def test_harness_git_refuses_push_clone_and_remote_add_to_network(self):
        import harness
        with tempfile.TemporaryDirectory() as d:
            for args in (("clone", "https://github.com/volekerb/hivemill.git", d + "/c"),
                         ("remote", "add", "origin", "git" + AT + "github.com:volekerb/hivemill.git"),
                         ("push", "https://github.com/volekerb/hivemill.git", "HEAD:main")):
                with self.assertRaises(_gitenv.RemoteRefused, msg=str(args)):
                    harness.git(*args, cwd=d)

    def test_harness_git_refuses_push_when_origin_is_not_under_tmp(self):
        import harness
        with tempfile.TemporaryDirectory() as d:
            subprocess.run(["git", "init", "-q", d], check=True)
            subprocess.run(["git", "-C", d, "remote", "add", "origin", "https://github.com/volekerb/hivemill.git"], check=True)
            with self.assertRaises(_gitenv.RemoteRefused):
                harness.git("push", "origin", "HEAD:main", cwd=d)

    def test_set_gh_refuses_a_real_repo_url(self):
        import harness
        env = None
        with self.assertRaises(_gitenv.RemoteRefused):
            harness.FactoryEnv.set_gh(env, repo_url="https://github.com/volekerb/hivemill.git")

    def test_inert_remote_opt_in_only_in_the_url_parsing_modules(self):
        users = sorted(p.name for p in TESTS.glob("test_*.py") if p.name != Path(__file__).name
                       and "inert_remote=True" in p.read_text())
        self.assertEqual(users, ["test_fresh_export.py", "test_git_hardening.py", "test_project_remote.py", "test_public_repo_safety.py",
                         "test_remote_url_check.py", "test_skilleval_guard_copy.py",
                         "test_worker_guard_public_text.py"])  # skilleval_guard_copy: the copied public-text guard only matches github URLs

    def test_inert_remote_does_not_excuse_a_network_push(self):
        import harness
        with tempfile.TemporaryDirectory() as d:
            subprocess.run(["git", "init", "-q", d], check=True)
            harness.git("remote", "add", "origin", "https://github.com/example-org/x.git", cwd=d, inert_remote=True)
            with self.assertRaises(_gitenv.RemoteRefused):
                harness.git("push", "origin", "HEAD:main", cwd=d, inert_remote=True)


NOREPLY = re.compile(r"([\w.+-]+)@users\.noreply\.github\.com")
OWNER_NOREPLY_LOCAL = re.compile(r"(\d+\+)?volekerb")  # the owner's own noreply is the only allowed one


class NoRoutableIdentityTest(unittest.TestCase):
    """M11099: a test identity under *@users.noreply.github.com maps to a real GitHub user and shows up as a contributor
    of the public repo. Tests use zz-test@example.invalid; the owner's own noreply is allowed where behaviour needs one."""

    def test_no_other_users_noreply_address_in_tests(self):
        bad = []
        for p in sorted(TESTS.rglob("*")):
            if p.is_file() and p.suffix in (".py", ".json", ".sh", ".txt") and p.name != Path(__file__).name:
                for n, line in enumerate(p.read_text(errors="replace").splitlines(), 1):
                    bad += ["%s:%d %s" % (p.relative_to(REPO), n, m.group(0)) for m in NOREPLY.finditer(line)
                            if not OWNER_NOREPLY_LOCAL.fullmatch(m.group(1))]
        self.assertEqual(bad, [], "use zz-test@example.invalid (or the owner's noreply where a test needs one)")

    def test_pattern_flags_other_users_and_passes_owner(self):
        self.assertFalse(OWNER_NOREPLY_LOCAL.fullmatch("dev"))
        self.assertFalse(OWNER_NOREPLY_LOCAL.fullmatch("1+dev"))
        self.assertTrue(OWNER_NOREPLY_LOCAL.fullmatch("1+volekerb"))
        self.assertTrue(OWNER_NOREPLY_LOCAL.fullmatch("volekerb"))


class EveryGitModuleUsesTheHelperTest(unittest.TestCase):
    def test_modules_that_spawn_git_import_harness_or_gitenv(self):
        missing = []
        for p in sorted(TESTS.glob("test_*.py")):
            text = p.read_text()
            if p.name == Path(__file__).name or not SPAWNS_GIT.search(text):
                continue
            if not re.search(r"^\s*(import harness|from harness|import _gitenv)", text, re.M):
                missing.append(p.name)
        self.assertEqual(missing, [], "add `import _gitenv  # noqa: F401` to these test modules")


SUBPROCESS_CALLS = {"run", "Popen", "check_output", "check_call", "call"}
NETWORK_VERBS = {"push", "fetch", "pull", "clone", "ls-remote", "submodule"}
# (file, reason): the only modules allowed to spawn network-verb git outside harness.git.
RAW_GIT_ALLOWLIST = {
    "test_git_env_hermetic.py": "negative control: shows an unguarded git does reach the remote/GIT_DIR",
    "test_worker_guard_git.py": "string-only hook fakes; no process is spawned",
    "test_worker_guard_git_data.py": "string-only hook fakes; no process is spawned",
    "test_worker_guard_git_reads.py": "string-only hook fakes; no process is spawned",
}


def _subprocess_aliases(tree):
    """(module names, function names) through which subprocess is reachable: `import subprocess as sp`, `from subprocess import run as r`."""
    mods, funcs = {"subprocess"}, {}
    for n in ast.walk(tree):
        if isinstance(n, ast.Import):
            mods |= {a.asname or a.name for a in n.names if a.name == "subprocess"}
        elif isinstance(n, ast.ImportFrom) and n.module == "subprocess":
            funcs.update({a.asname or a.name: a.name for a in n.names if a.name in SUBPROCESS_CALLS})
    return mods, funcs


def _scope_nodes(scope):
    """Every node under a def (or the module) in source order, nested defs and lambdas included: a closure
    forwarding the outer def's args is still that def forwarding them."""
    return sorted((n for n in ast.walk(scope) if n is not scope),
                  key=lambda n: (getattr(n, "lineno", 0), getattr(n, "col_offset", 0)))


def _argv_elts(expr, binds):
    """Elements of an argv expression: a list/tuple literal, a Name bound to one in this scope, or a `+` concatenation.
    A non-literal operand is kept as one opaque element so the wrapper check still sees the names inside it."""
    if isinstance(expr, (ast.List, ast.Tuple)):
        return list(expr.elts)
    if isinstance(expr, ast.Name):
        return binds.get(expr.id)
    if isinstance(expr, ast.BinOp) and isinstance(expr.op, ast.Add):
        left = _argv_elts(expr.left, binds)
        if left is None:
            return None
        right = _argv_elts(expr.right, binds)
        return left + (right if right is not None else [expr.right])
    return None


def _track(n, binds):
    """Update `binds` for a simple `x = [...]` or `x += ...` statement, in source order."""
    if isinstance(n, ast.Assign) and len(n.targets) == 1 and isinstance(n.targets[0], ast.Name):
        elts = _argv_elts(n.value, binds)
        if elts is None:
            binds.pop(n.targets[0].id, None)
        else:
            binds[n.targets[0].id] = elts
    elif isinstance(n, ast.AugAssign) and isinstance(n.op, ast.Add) and isinstance(n.target, ast.Name):
        if n.target.id in binds:
            more = _argv_elts(n.value, binds)
            binds[n.target.id] = binds[n.target.id] + (more if more is not None else [n.value])


def _git_argv(call, aliases=None, binds=None):
    """The argv elements of a subprocess call when the argv starts with the literal "git", else None."""
    mods, funcs = aliases or ({"subprocess"}, {})
    f = call.func
    if isinstance(f, ast.Attribute) and isinstance(f.value, ast.Name) and f.value.id in mods:
        name = f.attr
    elif isinstance(f, ast.Name):
        name = funcs.get(f.id)
    else:
        name = None
    if name not in SUBPROCESS_CALLS or not call.args:
        return None
    elts = _argv_elts(call.args[0], binds or {})
    if elts and isinstance(elts[0], ast.Constant) and elts[0].value == "git":
        return elts
    return None


def _scope_git_calls(scope, aliases, binds):
    """Subprocess git calls under one scope, each judged with the bindings in force where it is written."""
    found = []
    for n in _scope_nodes(scope):
        _track(n, binds)
        if isinstance(n, ast.Call) and (argv := _git_argv(n, aliases, binds)):
            found.append((n, argv))
    return found


def _git_calls(tree):
    """(enclosing def or None, call, git argv elements) for every subprocess git spawn, in any supported spelling.
    A call is reported for the module and for every def around it."""
    aliases = _subprocess_aliases(tree)
    found = [(None, c, a) for c, a in _scope_git_calls(tree, aliases, {})]
    top = [n for n in tree.body if isinstance(n, (ast.Assign, ast.AugAssign))]
    for fn in ast.walk(tree):
        if isinstance(fn, (ast.FunctionDef, ast.AsyncFunctionDef)):
            params = {a.arg for a in fn.args.args + fn.args.kwonlyargs + fn.args.posonlyargs}
            params |= {a.arg for a in (fn.args.vararg, fn.args.kwarg) if a}
            binds = {}
            for n in top:  # module-level bindings written before the def, minus shadowing params
                if n.lineno >= fn.lineno:
                    break
                _track(n, binds)
            for k in params:
                binds.pop(k, None)
            found += [(fn, c, a) for c, a in _scope_git_calls(fn, aliases, binds)]
    return found


def raw_git_violations(source):
    """Static (AST) scan: network-verb git via raw subprocess, and every def that forwards its args into a subprocess git argv."""
    out = []
    tree = ast.parse(source)
    calls = _git_calls(tree)
    for _, node, argv in calls:
        words = [e.value for e in argv if isinstance(e, ast.Constant) and isinstance(e.value, str)]
        if NETWORK_VERBS & set(words) or ("remote" in words and {"add", "set-url"} & set(words)):
            out.append("line %d: raw subprocess git %s; use harness.git" % (node.lineno, " ".join(words[1:])))
    # A def that forwards its own args into a git argv is a wrapper and is banned whatever verb, caller or module hands it
    # (6c-iii(2)); a literal network verb in a git argv is caught above even in a def that forwards nothing.
    for fn, _, argv in calls:
        if fn is None:
            continue
        params = {a.arg for a in fn.args.args + fn.args.kwonlyargs + fn.args.posonlyargs} - {"self", "cls"}
        params |= {a.arg for a in (fn.args.vararg, fn.args.kwarg) if a}
        if any(isinstance(n, ast.Name) and n.id in params for e in argv for n in ast.walk(e)):
            out.append("line %d: def %s wraps subprocess git; use harness.git" % (fn.lineno, fn.name))
    return sorted(set(out))


class RawGitRemoteGuardTest(unittest.TestCase):
    """R-b 6(c): no test spawns network-verb git outside harness.git, which vets every remote URL."""

    def test_no_test_module_runs_raw_network_git_or_wraps_subprocess_git(self):
        bad = []
        for p in sorted(TESTS.glob("*.py")):
            if p.name in ("harness.py", "_gitenv.py") or p.name in RAW_GIT_ALLOWLIST:
                continue
            bad += ["%s %s" % (p.name, v) for v in raw_git_violations(p.read_text())]
        self.assertEqual(bad, [], "route these through harness.git(*args, cwd=..., env=...)")

    def test_planted_violations_are_flagged(self):
        pushes = "import subprocess\nsubprocess.run(['git', '-C', 'x', 'push', 'origin', 'HEAD:main'])\n"
        wrapper = ("import subprocess\ndef run_git(cwd, *a):\n    return subprocess.run(['git', *a], cwd=cwd)\n"
                   "run_git('x', 'push', 'origin', 'HEAD:main')\n")
        remote = "import subprocess\nsubprocess.check_output(('git', 'remote', 'add', 'o', 'https://x/y'))\n"
        for src in (pushes, wrapper, remote):
            self.assertTrue(raw_git_violations(src), src)

    def test_wrapper_is_flagged_whoever_calls_it_and_with_whatever_verb(self):
        # cross-module: module A only ever calls run_git("status"); module B imports it and pushes to a network URL.
        mod_a = ("import subprocess\ndef run_git(cwd, *a):\n    return subprocess.run(['git', *a], cwd=cwd)\n"
                 "run_git('x', 'status')\n")
        mod_b = "from mod_a import run_git\nrun_git('x', 'push', 'https://github.com/x/y', 'HEAD:main')\n"
        self.assertTrue(raw_git_violations(mod_a), "the def itself is flagged, so the importer needs no scan")
        self.assertEqual(raw_git_violations(mod_b), [], "mod_b spawns nothing itself; mod_a's def is the violation")
        # variable verb: the call site carries no literal network verb, the wrapper def is still flagged.
        variable = ("import subprocess\ndef g(*a):\n    return subprocess.run(['git', *a])\n"
                    "verb = 'push'\ng('x', verb, 'https://github.com/x/y')\n")
        self.assertTrue(raw_git_violations(variable))
        # named parameter forwarded into the argv (not *args): the def is still a wrapper.
        named = ("import subprocess\ndef g(cwd, verb, url):\n    return subprocess.run(['git', verb, url], cwd=cwd)\n"
                 "g('x', 'push', 'https://github.com/x/y')\n")
        self.assertTrue(raw_git_violations(named))
        nested = "import subprocess\ndef g(cwd, url):\n    return subprocess.run(['git', 'push', f'{url}'], cwd=cwd)\n"
        self.assertTrue(raw_git_violations(nested))

    def test_non_literal_argv_forms_are_flagged(self):
        # T-0694: each spelling below hid a git argv from the old literal-list-only scan.
        forms = {
            "name-bound push": "import subprocess\ncmd = ['git', 'push', 'origin']\nsubprocess.run(cmd)\n",
            "name-bound augmented": "import subprocess\ndef f():\n    cmd = ['git']\n    cmd += ['fetch']\n    subprocess.run(cmd)\n",
            "name-bound wrapper": "import subprocess\ndef g(*a):\n    cmd = ['git', *a]\n    return subprocess.run(cmd)\n",
            "concat push": "import subprocess\nsubprocess.run(['git'] + ['push', 'origin'])\n",
            "concat wrapper": "import subprocess\ndef g(*a):\n    return subprocess.run(['git'] + list(a))\n",
            "augmented wrapper": "import subprocess\ndef g(*a):\n    c = ['git']\n    c += list(a)\n    subprocess.run(c)\n",
            "from-import": "from subprocess import run\nrun(['git', 'push', 'origin'])\n",
            "from-import as": "from subprocess import check_output as co\nco(['git', 'clone', 'x'])\n",
            "import as": "import subprocess as sp\nsp.run(['git', 'pull'])\n",
            "import as wrapper": "import subprocess as sp\ndef g(v):\n    sp.Popen(['git', v])\n",
        }
        for label, src in forms.items():
            self.assertTrue(raw_git_violations(src), label)

    def test_scoping_edge_cases(self):
        flagged = {
            "lambda": "import subprocess\nf = lambda u: subprocess.run(['git', 'push', u])\n",
            "closure wrapper": "import subprocess\ndef o(v):\n    def i():\n        subprocess.run(['git', v])\n    i()\n",
            "rebind after call": "import subprocess\ncmd = ['git', 'push']\nsubprocess.run(cmd)\ncmd = []\n",
        }
        for label, src in flagged.items():
            self.assertTrue(raw_git_violations(src), label)
        late = "import subprocess\nc = ['git', 'status']\nsubprocess.run(c)\nc += ['push']\n"
        self.assertEqual(raw_git_violations(late), [], "an extension after the call is not part of the call")
        shadow = "import subprocess\ncmd = ['git', 'push']\ndef f(cmd):\n    pass\ncmd = ['ls']\n"
        self.assertEqual(raw_git_violations(shadow), [])

    def test_non_literal_argv_local_git_and_non_git_are_not_flagged(self):
        clean = (
            "import subprocess\ncmd = ['git', 'status']\nsubprocess.run(cmd)\n"
            "subprocess.run(['git'] + ['log'])\n"
            "from subprocess import run\nrun(['ls', 'push'])\n"
            "import subprocess as sp\nsp.run(['echo', 'git', 'push'])\n"
            "def f():\n    cmd = ['ls']\n    cmd += ['push']\n    subprocess.run(cmd)\n"
        )
        self.assertEqual(raw_git_violations(clean), [])

    def test_local_non_network_git_is_not_flagged(self):
        src = "import subprocess\nsubprocess.run(['git', 'rev-parse', 'HEAD'], cwd='x')\nsubprocess.run(['rm', '-rf', 'y'])\n"
        self.assertEqual(raw_git_violations(src), [])
        src = "import subprocess\ndef g(cwd):\n    return subprocess.run(['git', 'status'], cwd=cwd)\ng('x')\n"
        self.assertEqual(raw_git_violations(src), [])


class RemoteUrlsPassthroughTest(unittest.TestCase):
    def setUp(self):
        import harness
        self.h = harness
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.d = self.tmp.name
        self.h.git("init", "-q", cwd=self.d)

    def test_push_refused_when_only_the_pushurl_is_a_network_url(self):
        self.h.git("remote", "add", "origin", self.d + "/o.git", cwd=self.d)
        self.h.git("config", "remote.origin.pushurl", "https://github.com/volekerb/hivemill.git", cwd=self.d)
        with self.assertRaises(_gitenv.RemoteRefused):
            self.h.git("push", "origin", "HEAD:main", cwd=self.d)

    def test_fetch_refused_when_insteadof_rewrites_a_local_url_to_network(self):
        self.h.git("remote", "add", "origin", self.d + "/o.git", cwd=self.d)
        self.h.git("config", "url.https://github.com/.insteadOf", self.d + "/", cwd=self.d)
        with self.assertRaises(_gitenv.RemoteRefused):
            self.h.git("fetch", "origin", cwd=self.d)

    def test_local_clone_of_excuses_only_a_clone_of_a_local_directory(self):
        with self.assertRaises(_gitenv.RemoteRefused):
            self.h.git("push", "origin", "HEAD:main", cwd=self.d, local_clone_of=self.d)
        with self.assertRaises(_gitenv.RemoteRefused):
            self.h.git("clone", "https://github.com/volekerb/hivemill.git", self.d + "/c", cwd=self.d,
                       local_clone_of=self.d)

    def test_env_passthrough_reaches_git_and_refuses_repo_pointing_vars(self):
        self.h.git("-c", "user.name=n", "commit", "-q", "--allow-empty", "-m", "m", cwd=self.d,
                   env={"GIT_AUTHOR_EMAIL": "a@example.invalid", "GIT_COMMITTER_EMAIL": "a@example.invalid"})
        self.assertEqual(self.h.git("log", "-1", "--format=%ae", cwd=self.d), "a@example.invalid")
        with self.assertRaises(_gitenv.GitEnvLeak):
            self.h.git("status", cwd=self.d, env={"GIT_DIR": "/x"})


class InheritedGitDirTest(unittest.TestCase):
    """Run real test modules with GIT_DIR pointing at a scratch repo; its config and refs must not change."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.scratch = Path(self.tmp.name) / "scratch.git"
        env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_DIR")}
        subprocess.run(["git", "init", "-q", "--bare", "-b", "main", str(self.scratch)], check=True, env=env)

    def run_module(self, name):
        env = dict(os.environ, GIT_DIR=str(self.scratch), GIT_WORK_TREE=self.tmp.name)
        r = subprocess.run([sys.executable, "-m", "unittest", "discover", "-s", "tests", "-p", name], cwd=REPO,
                           env=env, capture_output=True, text=True, timeout=600)
        self.assertNotEqual(r.returncode, 0, name + " ran with GIT_DIR set instead of refusing")
        self.assertIn("GitEnvLeak", r.stderr, name)

    def test_modules_refuse_inherited_git_dir_and_leave_scratch_repo_untouched(self):
        before = snapshot(self.scratch)
        for name in RUN_WITH_GIT_DIR:
            self.run_module(name)
        self.assertEqual(snapshot(self.scratch), before)

    def test_unscrubbed_git_does_write_to_inherited_git_dir(self):
        """Control: without the scrub the same inherited GIT_DIR is written to, so the test above can fail."""
        before = snapshot(self.scratch)
        env = dict(os.environ, GIT_DIR=str(self.scratch))
        subprocess.run(["git", "-C", self.tmp.name, "config", "user.email", "t@example.com"], env=env, check=True)
        self.assertNotEqual(snapshot(self.scratch), before)


if __name__ == "__main__":
    unittest.main()
