import contextlib
import importlib.machinery
import importlib.util
import io
import json
import os
import pwd
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock
from concurrent.futures import ThreadPoolExecutor
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))  # `-m unittest tests.test_guard_diff` has no tests/ on the path
import _gitenv  # noqa: F401  (hermetic git: tests/_gitenv.py)
import harness
from guard_impl import hook_run, impl

LIVE_HOME = os.path.join(pwd.getpwuid(os.getuid()).pw_dir, "factory")  # what the hook and bin/factory derive (T-0212)

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TOOL = os.path.join(ROOT, "ci", "guard_diff.py")
CORPUS = os.path.join(ROOT, "ci", "guard_corpus.json")
HOOK = os.path.join(ROOT, "roles", "hooks", "worker-guard.py")


def _in_git_work_tree():
    p = subprocess.run(["git", "rev-parse", "--show-toplevel"], cwd=ROOT, capture_output=True, text=True)
    return p.returncode == 0 and os.path.realpath(p.stdout.strip()) == os.path.realpath(ROOT)


IN_GIT_WORK_TREE = _in_git_work_tree()
sys.path.insert(0, os.path.join(ROOT, "ci"))
import guard_diff  # noqa: E402

HOOK_REL = "roles/hooks/worker-guard.py"

BAD = "danger-cmd"
# stub hook: blocks (exit 2) BAD, allows everything else; the PR variants differ from it
STUB_BASE = (
    "import json, sys\n"
    "c = json.load(sys.stdin)['tool_input']['command']\n"
    "sys.exit(2 if c == %r else 0)\n" % BAD)
STUB_LOOSE = "import sys\nsys.exit(0)\n"
STUB_TIGHT = "import sys\nsys.exit(2)\n"
STUB_SLOW = "import time\ntime.sleep(30)\n"


def git(repo, *args):
    harness.git("-c", "user.name=t", "-c", "user.email=t@example.com", *args, cwd=repo)


class GuardDiffCase(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.repo = os.path.join(self._tmp.name, "repo")
        os.makedirs(os.path.join(self.repo, "roles", "hooks"))
        self.hook = os.path.join(self.repo, HOOK_REL)
        self.corpus = os.path.join(self._tmp.name, "corpus.json")
        with open(self.corpus, "w") as f:
            json.dump({"must_block": [BAD], "must_allow": ["ls -la"]}, f)
        self.write_hook(STUB_BASE)
        git(self.repo, "init", "-q", "-b", "main")
        git(self.repo, "add", ".")
        git(self.repo, "commit", "-q", "-m", "base")

    def write_hook(self, src):
        with open(self.hook, "w") as f:
            f.write(src)

    def run_tool(self, *extra, base="main"):
        argv = [sys.executable, TOOL, "--base", base, "--repo", self.repo, "--hook", self.hook,
                "--corpus", self.corpus, *extra]
        return subprocess.run(argv, capture_output=True, text=True, timeout=600)


class GuardDiffTests(GuardDiffCase):
    def test_loosened_command_exit_1(self):
        self.write_hook(STUB_LOOSE)
        p = self.run_tool("--mode", "worker")
        self.assertEqual(p.returncode, 1, p.stderr)
        self.assertIn(f"LOOSENED base=2 pr=0 worker {BAD}", p.stdout)
        self.assertIn("guard-diff: 2 commands, 1 loosened, 0 tightened", p.stdout)

    def test_identical_hook_exit_0(self):
        p = self.run_tool("--mode", "both")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("0 loosened", p.stdout)
        self.assertNotIn("LOOSENED", p.stdout)

    def test_allow_loosen_file_exit_0(self):
        self.write_hook(STUB_LOOSE)
        allow = os.path.join(self._tmp.name, "allow.txt")
        with open(allow, "w") as f:
            f.write(BAD + "\n")
        p = self.run_tool("--mode", "worker", "--allow-loosen", allow)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("LOOSENED", p.stdout)  # still reported, just planned

    def test_allow_loosen_needs_exact_text(self):
        self.write_hook(STUB_LOOSE)
        allow = os.path.join(self._tmp.name, "allow.json")
        with open(allow, "w") as f:
            json.dump([BAD + " "], f)
        self.assertEqual(self.run_tool("--mode", "worker", "--allow-loosen", allow).returncode, 1)
        with open(allow, "w") as f:
            json.dump([BAD], f)
        self.assertEqual(self.run_tool("--mode", "worker", "--allow-loosen", allow).returncode, 0)

    def test_tightened_reported_but_exit_0(self):
        self.write_hook(STUB_TIGHT)
        p = self.run_tool("--mode", "worker")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("TIGHTENED base=0 pr=2 worker ls -la", p.stdout)
        self.assertIn("1 tightened", p.stdout)

    def test_bad_base_exit_2(self):
        p = self.run_tool(base="no-such-ref")
        self.assertEqual(p.returncode, 2)
        self.assertIn("guard-diff:", p.stderr)
        self.assertNotIn("0 loosened", p.stdout)

    def test_bad_corpus_exit_2(self):
        with open(self.corpus, "w") as f:
            f.write("not json")
        self.assertEqual(self.run_tool().returncode, 2)

    def test_bad_usage_exit_2(self):
        p = subprocess.run([sys.executable, TOOL], capture_output=True, text=True, timeout=30)
        self.assertEqual(p.returncode, 2)

    def test_timeout_reported_exit_1(self):
        self.write_hook(STUB_SLOW)
        p = self.run_tool("--mode", "worker", "--timeout", "0.5")
        self.assertEqual(p.returncode, 1, p.stderr)
        self.assertIn("TIMEOUT base=2 pr=timeout worker " + BAD, p.stdout)

    def test_hook_copy_outside_repo_compared_with_main_hook(self):
        copy = os.path.join(self._tmp.name, "copy.py")
        with open(copy, "w") as f:
            f.write(STUB_LOOSE)
        argv = [sys.executable, TOOL, "--base", "main", "--repo", self.repo, "--hook", copy,
                "--corpus", self.corpus, "--mode", "hq"]
        p = subprocess.run(argv, capture_output=True, text=True, timeout=600)
        self.assertEqual(p.returncode, 1, p.stderr)
        self.assertIn(f"LOOSENED base=2 pr=0 hq {BAD}", p.stdout)

    def test_corpus_command_is_never_executed(self):
        marker = os.path.join(self._tmp.name, "marker")
        cmd = f"touch {marker}"
        with open(self.corpus, "w") as f:
            json.dump({"must_block": [cmd], "must_allow": []}, f)
        self.assertEqual(self.run_tool().returncode, 0)
        self.assertFalse(os.path.exists(marker))

    def test_real_hook_unchanged_against_itself(self):
        # the repo hook compared with a base holding the same bytes: exit 0, nothing loosened
        with open(HOOK) as f:
            self.write_hook(f.read())
        git(self.repo, "add", ".")
        git(self.repo, "commit", "-q", "-m", "real hook")
        with open(self.corpus, "w") as f:
            json.dump({"must_block": ["sudo ls", "herdr x"], "must_allow": ["ls"]}, f)
        p = self.run_tool("--mode", "both")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("3 commands, 0 loosened, 0 tightened", p.stdout)


# real hook with the evaluator temp-home guard in evaluator_factory() disabled (scratch copy for the tests)
def real_hook_text():
    with open(HOOK) as f:
        return f.read()


_HOOK_MOD = []


def _payload(cmd, xenv):
    """The hook payload the way guard_diff builds it: an entry whose env names a task runs from the worktree fixture."""
    d = {"tool_name": "Bash", "tool_input": {"command": cmd}}
    if guard_diff.agent_task(xenv):
        d["cwd"] = guard_diff.worktree_fixture()
    return json.dumps(d)


def hook_rc_subprocess(cmd, mode, role="coder", home=None, xenv=None):
    args = ["--role", role] if mode == "worker" else ["--hq"]
    env = {**os.environ, "FACTORY_HOME": home, **dict(xenv or ())} if home else None
    env = guard_diff.hook_env(cmd, env)
    p = hook_run(_payload(cmd, xenv), list(args), env=env)
    return p.returncode


def hook_rc(cmd, mode, role="coder", home=None, xenv=None):
    """Exit code of the real hook's main(), run in-process: the corpus sanity checks make ~500 calls and a subprocess each
    costs ~40 ms of startup. CorpusSanityTests.check compares a sample with hook_rc_subprocess."""
    if impl() != "python":  # GUARD_IMPL=go|diff: the real binaries decide, never the in-process Python module
        return hook_rc_subprocess(cmd, mode, role, home, xenv)
    if not _HOOK_MOD:
        loader = importlib.machinery.SourceFileLoader("worker_guard_under_test", HOOK)
        mod = importlib.util.module_from_spec(importlib.util.spec_from_loader(loader.name, loader))
        loader.exec_module(mod)
        _HOOK_MOD.append(mod)
    mod = _HOOK_MOD[0]
    env = {**os.environ, "FACTORY_HOME": home, **dict(xenv or ())} if home else None
    env = guard_diff.hook_env(cmd, env)
    argv = ["worker-guard.py"] + (["--role", role] if mode == "worker" else ["--hq"])
    payload = io.StringIO(_payload(cmd, xenv))
    mod.SESSION_ID = None
    with mock.patch.dict(os.environ, env if env is not None else os.environ, clear=env is not None), \
            mock.patch.object(sys, "argv", argv), mock.patch.object(sys, "stdin", payload), \
            contextlib.redirect_stderr(io.StringIO()):
        return mod.main()


class EvaluatorRunTests(GuardDiffCase):
    """--role evaluator needs a temp FACTORY_HOME and a base copy whose path mirrors the repo layout."""

    def commit_real_hook(self):
        self.write_hook(real_hook_text())
        git(self.repo, "add", ".")
        git(self.repo, "commit", "-q", "-m", "real hook")

    def test_real_hook_against_itself_under_evaluator_role(self):
        self.commit_real_hook()
        # Full ci/guard_corpus.json has hundreds of evaluator-tagged rows; guard-diff compares base vs PR for
        # each (worker parallelism still times caller work × per-hook timeout) and can exceed this test's
        # 600s subprocess cap (T-1038). Same coverage with a tiny corpus.
        with open(self.corpus, "w", encoding="utf-8") as f:
            json.dump({"must_block": ["sudo ls", "herdr x"], "must_allow": ["ls"]}, f)
        p = self.run_tool("--mode", "worker", "--role", "evaluator")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn(" 0 loosened, 0 tightened", p.stdout)

    def test_removed_evaluator_home_check_is_loosened(self):
        self.commit_real_hook()
        src = real_hook_text()
        line = "ok = bool(env) and os.path.realpath(env) not in self.live_homes() and self.outside_live(env)"
        self.assertEqual(src.count(line), 1)
        hook = src.replace("        if not ok:\n            raise Block(\"the evaluator may run factory",
                           "        if False:\n            raise Block(\"the evaluator may run factory", 1)
        self.assertNotEqual(hook, src)
        self.write_hook(hook)
        with open(self.corpus, "w", encoding="utf-8") as f:
            json.dump(
                {"must_block": [{"cmd": "FACTORY_HOME=@LIVE_HOME@ factory inbox", "role": "evaluator"}], "must_allow": []},
                f,
            )
        p = self.run_tool("--mode", "worker", "--role", "evaluator")
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        self.assertIn("LOOSENED base=2 pr=0 worker", p.stdout)

    def test_default_run_covers_role_tagged_entries(self):
        # no --role flag: an evaluator-tagged entry still runs, under the evaluator role with a temp home
        self.commit_real_hook()
        cmd = f"FACTORY_HOME={LIVE_HOME} factory inbox"
        with open(self.corpus, "w") as f:
            json.dump({"must_block": [{"cmd": cmd, "role": "evaluator"}], "must_allow": []}, f)
        self.write_hook(real_hook_text().replace("        if not ok:\n            raise Block(\"the evaluator may run factory",
                                                 "        if False:\n            raise Block(\"the evaluator may run factory", 1)
                        .replace("ok = self.agent_temp_home(assigns)", "ok = True", 1))
        p = self.run_tool("--mode", "both")
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        self.assertIn(f"LOOSENED base=2 pr=0 worker:evaluator {cmd}", p.stdout)

    def test_base_starting_with_dash_exit_2(self):
        for base in ("--output=/x", "-p"):
            argv = [sys.executable, TOOL, f"--base={base}", "--repo", self.repo, "--hook", self.hook,
                    "--corpus", self.corpus]
            p = subprocess.run(argv, capture_output=True, text=True, timeout=600)
            self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
            self.assertIn("looks like an option", p.stderr)
            self.assertFalse(os.path.exists("/x"))


class GoImplTests(GuardDiffCase):
    """--impl go: the Python hook (reference) against a stub candidate binary given with --go-bin."""

    def stub(self, body):
        path = os.path.join(self._tmp.name, "cand")
        with open(path, "w") as f:
            f.write(f"#!{sys.executable}\n" + body)
        os.chmod(path, 0o755)
        return path

    def run_go(self, body, *extra):
        argv = [sys.executable, TOOL, "--impl", "go", "--go-bin", self.stub(body), "--repo", self.repo,
                "--hook", self.hook, "--corpus", self.corpus, "--mode", "worker", *extra]
        return subprocess.run(argv, capture_output=True, text=True, timeout=600)

    def test_identical_candidate_exit_0_strict(self):
        p = self.run_go(STUB_BASE, "--strict")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("settings diff: skipped (--impl go)", p.stdout)
        self.assertIn("2 commands, 0 loosened, 0 tightened", p.stdout)

    def test_loosening_candidate_exit_1(self):
        p = self.run_go(STUB_LOOSE)
        self.assertEqual(p.returncode, 1, p.stderr)
        self.assertIn(f"LOOSENED base=2 pr=0 worker {BAD}", p.stdout)

    def test_tightening_candidate_exit_0_without_strict_1_with(self):
        p = self.run_go(STUB_TIGHT)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("TIGHTENED base=0 pr=2 worker ls -la", p.stdout)
        p = self.run_go(STUB_TIGHT, "--strict")
        self.assertEqual(p.returncode, 1, p.stderr)

    def test_changed_exit_code_fails_only_strict(self):
        # allow-side crash (exit 1) while the block (exit 2) is kept: CHANGED, neither LOOSENED nor TIGHTENED
        body = ("import json, sys\nc = json.load(sys.stdin)['tool_input']['command']\n"
                "sys.exit(2 if c == %r else 1)\n" % BAD)
        p = self.run_go(body)
        self.assertEqual(p.returncode, 0, p.stdout)
        self.assertIn("CHANGED base=0 pr=1 worker ls -la", p.stdout)
        self.assertEqual(self.run_go(body, "--strict").returncode, 1)

    def test_loosened_and_tightened_both_reported(self):
        body = ("import json, sys\nc = json.load(sys.stdin)['tool_input']['command']\n"
                "sys.exit(0 if c == %r else 2)\n" % BAD)
        p = self.run_go(body)
        self.assertEqual(p.returncode, 1)
        self.assertIn("LOOSENED", p.stdout)
        self.assertIn("TIGHTENED", p.stdout)

    def test_candidate_gets_py_hook_and_role(self):
        log = os.path.join(self._tmp.name, "argv.log")
        self.run_go("import sys\nopen(%r, 'a').write(' '.join(sys.argv[1:]) + '\\n')\nsys.exit(0)\n" % log)
        with open(log) as f:
            lines = set(f.read().splitlines())
        self.assertEqual(lines, {f"--role coder --py-hook {os.path.abspath(self.hook)}"})

    def test_template_roles_reads_both_hook_forms_and_still_fails_closed(self):
        """T-0356 C1: the shim form `sh <live>/roles/hooks/worker-guard --role X` is a role call too; no call is an error."""
        sdir = os.path.join(self.repo, "roles", "settings")
        os.makedirs(sdir)
        for name, cmd in (("a", "sh @FACTORY_LIVE_HOME@/roles/hooks/worker-guard --role coder"),
                          ("b", "python3 @FACTORY_LIVE_HOME@/roles/hooks/worker-guard.py --role qa"),
                          ("c", "python3 @FACTORY_LIVE_HOME@/roles/hooks/worker-guard.py --hq")):
            with open(os.path.join(sdir, name + ".json.in"), "w") as f:
                f.write('{"command": "%s"}' % cmd)
        self.assertEqual(guard_diff.template_roles(self.repo), ["coder", "qa"])
        for name in ("a", "b"):
            os.remove(os.path.join(sdir, name + ".json.in"))
        with self.assertRaises(guard_diff.UsageError) as cm:
            guard_diff.template_roles(self.repo)
        self.assertIn("cannot derive --roles all", str(cm.exception))

    def test_template_roles_of_the_real_templates_are_the_worker_roles(self):
        self.assertEqual(guard_diff.template_roles(harness.REPO),
                         sorted(("cleaner", "coder", "evaluator", "judge", "pm", "qa", "researcher", "reviewer", "scout", "staff-engineer")))

    def test_roles_all_derived_from_templates(self):
        sdir = os.path.join(self.repo, "roles", "settings")
        os.makedirs(sdir)
        for role, form in (("alpha", "python3 x/roles/hooks/worker-guard.py"), ("beta-two", "sh x/roles/hooks/worker-guard")):
            with open(os.path.join(sdir, role + ".json.in"), "w") as f:  # T-0356: the python3 form (HQ) and the sh shim form
                f.write('{"command": "%s --role %s"}' % (form, role))
        with open(os.path.join(sdir, "norole.json.in"), "w") as f:
            f.write('{"command": "python3 x/roles/hooks/worker-guard.py --hq"}')
        p = self.run_go(STUB_TIGHT, "--roles", "all", "--mode", "both")
        self.assertEqual(p.returncode, 0, p.stderr)
        roles = {ln.split("worker role=")[1].split()[0] for ln in p.stdout.splitlines() if "worker role=" in ln}
        self.assertEqual(roles, {"alpha", "beta-two"})
        self.assertIn(" hq ls -la", p.stdout)

    def test_allow_loosen_refused(self):
        p = self.run_go(STUB_BASE, "--allow-loosen", os.devnull)
        self.assertEqual(p.returncode, 2)
        self.assertIn("no --allow-loosen with --impl go", p.stderr)

    def test_missing_go_bin_exit_2(self):
        argv = [sys.executable, TOOL, "--impl", "go", "--go-bin", "/nonexistent/worker-guard", "--repo", self.repo,
                "--hook", self.hook, "--corpus", self.corpus]
        p = subprocess.run(argv, capture_output=True, text=True, timeout=600)
        self.assertEqual(p.returncode, 2)
        self.assertIn("--go-bin", p.stderr)

    def test_go_build_env_pins_local_toolchain(self):
        # fake `go` on PATH records the env it is built with and drops a stub candidate at -o
        fakebin = os.path.join(self._tmp.name, "fakebin")
        os.makedirs(fakebin)
        envlog = os.path.join(self._tmp.name, "goenv.json")
        cand = self.stub(STUB_BASE)
        fake = os.path.join(fakebin, "go")
        with open(fake, "w") as f:
            f.write(f"#!{sys.executable}\nimport json, os, shutil, sys\n"
                    f"json.dump({{k: os.environ.get(k) for k in ('GOTOOLCHAIN', 'GOFLAGS', 'GOPROXY')}}, open({envlog!r}, 'w'))\n"
                    f"shutil.copy({cand!r}, sys.argv[sys.argv.index('-o') + 1])\n")
        os.chmod(fake, 0o755)
        env = dict(os.environ, PATH=fakebin + os.pathsep + os.environ["PATH"], GOTOOLCHAIN="auto")
        os.makedirs(os.path.join(self.repo, "go"), exist_ok=True)
        argv = [sys.executable, TOOL, "--impl", "go", "--repo", self.repo, "--hook", self.hook,
                "--corpus", self.corpus, "--mode", "worker", "--strict"]
        p = subprocess.run(argv, capture_output=True, text=True, timeout=600, env=env)
        self.assertEqual(p.returncode, 0, p.stderr + p.stdout)
        with open(envlog) as f:
            self.assertEqual(json.load(f), {"GOTOOLCHAIN": "local", "GOFLAGS": "-mod=readonly", "GOPROXY": "off"})

    def test_no_go_on_path_exit_2(self):
        argv = [sys.executable, TOOL, "--impl", "go", "--repo", self.repo, "--hook", self.hook, "--corpus", self.corpus]
        env = {k: v for k, v in os.environ.items() if k != "PATH"}
        only_git = os.path.join(self._tmp.name, "only-git")
        os.makedirs(only_git)
        os.symlink(shutil.which("git"), os.path.join(only_git, "git"))
        env["PATH"] = only_git  # git for the repo lookup, no go
        p = subprocess.run(argv, capture_output=True, text=True, timeout=600, env=env)
        self.assertEqual(p.returncode, 2, p.stdout)
        self.assertIn("go", p.stderr)

    def test_build_failure_exit_2(self):
        os.makedirs(os.path.join(self.repo, "go"))
        fake = os.path.join(self._tmp.name, "bin")
        os.makedirs(fake)
        with open(os.path.join(fake, "go"), "w") as f:
            f.write("#!/bin/sh\necho 'boom: syntax error' >&2\nexit 1\n")
        os.chmod(os.path.join(fake, "go"), 0o755)
        argv = [sys.executable, TOOL, "--impl", "go", "--repo", self.repo, "--hook", self.hook, "--corpus", self.corpus]
        p = subprocess.run(argv, capture_output=True, text=True, timeout=600,
                           env={**os.environ, "PATH": fake + os.pathsep + os.environ["PATH"]})
        self.assertEqual(p.returncode, 2, p.stdout)
        self.assertIn("go build", p.stderr)
        self.assertIn("boom", p.stderr)

    def test_base_still_required_without_impl_go(self):
        p = subprocess.run([sys.executable, TOOL, "--repo", self.repo], capture_output=True, text=True, timeout=30)
        self.assertEqual(p.returncode, 2)
        self.assertIn("--base is required", p.stderr)

    def test_strict_needs_impl_go(self):
        self.assertEqual(self.run_tool("--strict").returncode, 2)


class CorpusSanityTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        with open(CORPUS) as f:
            cls.corpus = json.load(f)

    def check(self, key, want):
        jobs = []
        for e in self.corpus[key]:
            cmd, modes = (e, ["worker"]) if isinstance(e, str) else (e["cmd"], e.get("modes", ["worker"]))
            cmd = guard_diff.expand(cmd)
            role = None if isinstance(e, str) else e.get("role")
            if role:
                modes = ["worker"]  # a role-tagged entry is a worker-mode check under that role
            elif key == "must_allow":
                modes = ["worker", "hq"]
            it = guard_diff.entry_item(e)  # a seeded entry (T-0762) runs against its own seeded temp home
            seed, xenv = (it[2] if len(it) > 2 else None), (it[3] if len(it) > 3 else None)
            jobs += [(cmd, m, role or "coder", seed, xenv) for m in modes]
        with tempfile.TemporaryDirectory(prefix="zz-test-gdhome-") as home:
            specs = dict.fromkeys((j[3] or (), guard_diff.agent_task(j[4])) for j in jobs
                                  if j[3] or guard_diff.agent_task(j[4]))
            homes = {sp: guard_diff.make_seed_home(home, sp[0], i,
                                                   {sp[1]: guard_diff.worktree_fixture()} if sp[1] else None)
                     for i, sp in enumerate(specs)}
            hm = [homes.get((j[3] or (), guard_diff.agent_task(j[4])), home) for j in jobs]
            got = [hook_rc(j[0], j[1], j[2], h, j[4]) for j, h in zip(jobs, hm)]
            for i in range(0, len(jobs), 17):  # the real entry point on a sample of the same jobs
                j = jobs[i]
                self.assertEqual(hook_rc_subprocess(j[0], j[1], j[2], hm[i], j[4]), got[i], j)
        wrong = [(j, g) for j, g in zip(jobs, got) if g != want]
        self.assertEqual(wrong, [], f"{key}: expected exit {want} on the current hook")
        return len(jobs)

    def test_corpus_size(self):
        self.assertGreaterEqual(len(self.corpus["must_block"]), 150)
        self.assertGreaterEqual(len(self.corpus["must_allow"]), 30)

    def test_corpus_has_evaluator_class(self):
        tagged = [e for k in ("must_block", "must_allow") for e in self.corpus[k]
                  if isinstance(e, dict) and e.get("role") == "evaluator"]
        self.assertGreaterEqual(len(tagged), 25)
        text = " ".join(e["cmd"] for e in tagged)
        for form in ("declare -x", "typeset", "read FACTORY_HOME", "printf -v", "for FACTORY_HOME", "mapfile",
                     "readarray", "getopts", "readonly", "export FACTORY_HOME", "env -u", "unset FACTORY_HOME"):
            self.assertIn(form, text)

    def test_must_block_all_exit_2(self):
        self.assertGreaterEqual(self.check("must_block", 2), 150)

    def test_must_allow_all_exit_0(self):
        self.assertGreaterEqual(self.check("must_allow", 0), 30)

    @unittest.skipUnless(IN_GIT_WORK_TREE, "ROOT is not a git work tree (archive tree); the corpus check needs git")
    def test_real_corpus_against_origin_main_hook_is_clean(self):
        # the shipped corpus against the hook in this checkout, base = HEAD: the hook is the same file
        # T-0745: an empty FACTORY_HOME, else the live DB's public_text_repos makes the hook block corpus commit text
        with tempfile.TemporaryDirectory(prefix="zz-test-gdhome-") as home:
            p = subprocess.run([sys.executable, TOOL, "--base", "HEAD", "--repo", ROOT, "--mode", "worker"],
                               capture_output=True, text=True, timeout=600, env={**os.environ, "FACTORY_HOME": home})
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("0 loosened", p.stdout)



class SeedTests(GuardDiffCase):
    """T-0762: a corpus entry's `config` seed gives both hook sides a temp FACTORY_HOME whose config table holds it."""

    # stub hook: blocks `uvx pytest` only when the config table of $FACTORY_HOME's DB has test_sandbox=1
    STUB_CFG = (
        "import json, os, sqlite3, sys\n"
        "c = json.load(sys.stdin)['tool_input']['command']\n"
        "rc = 0\n"
        "try:\n"
        "    con = sqlite3.connect('file:%s?mode=ro' % os.path.join(os.environ.get('FACTORY_HOME', '/nonexistent'), 'db', 'factory.db'), uri=True)\n"
        "    rc = 2 if c == 'uvx pytest' and con.execute(\"SELECT value FROM config WHERE key='test_sandbox'\").fetchone() == ('1',) else 0\n"
        "except sqlite3.Error:\n"
        "    pass\n"
        "sys.exit(rc)\n")
    STUB_ENV = "import json, os, sys\nsys.exit(2 if os.environ.get('FACTORY_HOME', '').startswith(%r) else 0)\n"

    def write_corpus(self, block=(), allow=("ls -la",)):
        with open(self.corpus, "w") as f:
            json.dump({"must_block": list(block), "must_allow": list(allow)}, f)

    def seeded(self, cmd="uvx pytest", **seed):
        return {"cmd": cmd, "config": seed or {"test_sandbox": "1"}}

    def test_seeded_entry_runs_with_the_seeded_home(self):
        self.write_hook(self.STUB_CFG)
        git(self.repo, "add", "."); git(self.repo, "commit", "-q", "-m", "cfg")
        self.write_corpus(block=[self.seeded(), "uvx pytest"])
        p = self.run_tool("--mode", "worker")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        # a PR hook that ignores the seed is LOOSENED only for the seeded entry, never for the plain one
        self.write_hook("import sys\nsys.exit(0)\n")
        p = self.run_tool("--mode", "worker")
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        self.assertEqual(p.stdout.count("LOOSENED"), 1, p.stdout)

    def test_seed_without_value_one_allows(self):
        self.write_hook(self.STUB_CFG)
        git(self.repo, "add", "."); git(self.repo, "commit", "-q", "-m", "cfg")
        self.write_corpus(block=[], allow=[self.seeded(test_sandbox="0")])
        p = self.run_tool("--mode", "worker")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("0 loosened, 0 tightened", p.stdout)

    def test_loosened_line_names_the_seed(self):
        self.write_hook(self.STUB_CFG)
        git(self.repo, "add", "."); git(self.repo, "commit", "-q", "-m", "cfg")
        self.write_hook("import sys\nsys.exit(0)\n")
        self.write_corpus(block=[self.seeded()])
        p = self.run_tool("--mode", "worker")
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        self.assertIn("LOOSENED base=2 pr=0 worker config=test_sandbox:1 uvx pytest", p.stdout)

    def test_bad_seed_shapes_exit_2(self):
        for bad in ([], {}, {"test_sandbox": 1}, {"": "1"}, "test_sandbox=1", {"k": None}):
            self.write_corpus(block=[{"cmd": "uvx pytest", "config": bad}])
            p = self.run_tool()
            self.assertEqual(p.returncode, 2, f"{bad!r}: {p.stdout}{p.stderr}")
            self.assertIn("'config' must be", p.stderr)

    def test_unseeded_entries_keep_the_environment(self):
        # a stub that blocks when FACTORY_HOME is a seed home: unseeded entries must never see one
        self.write_hook(self.STUB_ENV % os.path.join(tempfile.gettempdir(), "guard-diff-seed-"))
        git(self.repo, "add", "."); git(self.repo, "commit", "-q", "-m", "env")
        self.write_corpus(block=[], allow=["ls -la", "uvx pytest"])
        p = self.run_tool("--mode", "both")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("0 loosened, 0 tightened", p.stdout)
        self.assertEqual(guard_diff.entry_item("ls"), ("ls", None))
        self.assertEqual(guard_diff.entry_item({"cmd": "ls", "role": "qa"}), ("ls", "qa"))

    def test_seeded_entry_in_compare_uses_a_fresh_home_with_exactly_the_seed(self):
        self.write_hook(self.STUB_CFG)
        items = [("uvx pytest", None, (("test_sandbox", "1"),)), ("uvx pytest", None)]
        with tempfile.TemporaryDirectory() as t:
            lines, loose, bad, tight = guard_diff.compare(self.hook, self.hook, items, ["worker"], "coder", 10, t)
        self.assertEqual((lines, loose, bad, tight), ([], [], [], 0))
        with tempfile.TemporaryDirectory() as t:
            home = guard_diff.make_seed_home(t, (("a", "1"), ("b", "2")), 0)
            import sqlite3
            con = sqlite3.connect(os.path.join(home, "db", "factory.db"))
            self.assertEqual(con.execute("SELECT key, value FROM config ORDER BY key").fetchall(), [("a", "1"), ("b", "2")])
            con.close()

    def test_refresh_guard_export_if_stale_rewrites_old_generated_at(self):
        import threading
        from datetime import datetime, timedelta, timezone
        with tempfile.TemporaryDirectory() as t:
            home = guard_diff.make_seed_home(t, (("test_sandbox", "1"),), 0)
            path = os.path.join(home, "run", "guard-export.json")
            with open(path, encoding="utf-8") as fh:
                doc = json.load(fh)
            doc["generated_at"] = (datetime.now(timezone.utc) - timedelta(minutes=20)).isoformat(timespec="seconds")
            with open(path, "w", encoding="utf-8") as fh:
                json.dump(doc, fh)
            guard_diff.refresh_guard_export_if_stale(home, threading.Lock())
            with open(path, encoding="utf-8") as fh:
                fresh = json.load(fh)
            self.assertLess(guard_diff._export_age_seconds(fresh), guard_diff._GUARD_EXPORT_REFRESH_AFTER)

    def test_refresh_guard_export_if_stale_leaves_fresh_timestamp(self):
        import threading
        with tempfile.TemporaryDirectory() as t:
            home = guard_diff.make_seed_home(t, (("test_sandbox", "1"),), 0)
            path = os.path.join(home, "run", "guard-export.json")
            with open(path, encoding="utf-8") as fh:
                before = fh.read()
            guard_diff.refresh_guard_export_if_stale(home, threading.Lock())
            with open(path, encoding="utf-8") as fh:
                after = fh.read()
            self.assertEqual(before, after)


class EnvTests(GuardDiffCase):
    """T-0871: a corpus entry's `env` map reaches both hook sides; an entry without it gets no injected FACTORY_AGENT."""

    # stub hook: blocks `ls` only when its environment carries FACTORY_AGENT=probe-agent
    STUB_AGENT = ("import json, os, sys\n"
                  "c = json.load(sys.stdin)['tool_input']['command']\n"
                  "sys.exit(2 if c == 'ls' and os.environ.get('FACTORY_AGENT') == 'probe-agent' else 0)\n")

    def stub(self, name, src):
        path = os.path.join(self.repo, name)
        with open(path, "w") as f:
            f.write(src)
        return path

    def compare(self, base, pr, items):
        with tempfile.TemporaryDirectory() as t:
            return guard_diff.compare(base, pr, items, ["worker"], "coder", 10, t)

    def test_env_reaches_both_hook_sides(self):
        agent, loose = self.stub("agent.py", self.STUB_AGENT), self.stub("loose.py", STUB_LOOSE)
        env_item = guard_diff.entry_item({"cmd": "ls", "env": {"FACTORY_AGENT": "probe-agent"}})
        self.assertEqual(env_item, ("ls", None, None, (("FACTORY_AGENT", "probe-agent"),)))
        # PR side: a PR hook that stops blocking it is LOOSENED, and only the entry carrying the env is
        lines, lo, bad, tight = self.compare(agent, loose, [env_item, ("ls", None)])
        self.assertEqual((len(lines), lo, bad, tight), (1, ["ls"], [], 0))
        self.assertIn("worker env=FACTORY_AGENT:probe-agent ls", lines[0])
        # base side: the same entry is seen as blocking by the base hook, so TIGHTENED when only the PR blocks it
        lines, lo, bad, tight = self.compare(loose, agent, [env_item])
        self.assertEqual((len(lines), lo, bad, tight), (1, [], [], 1))
        # both sides identical: clean
        self.assertEqual(self.compare(agent, agent, [env_item, ("ls", None)]), ([], [], [], 0))

    def test_evaluator_entry_keeps_its_env(self):
        agent, loose = self.stub("agent.py", self.STUB_AGENT), self.stub("loose.py", STUB_LOOSE)
        item = guard_diff.entry_item({"cmd": "ls", "role": "evaluator", "env": {"FACTORY_AGENT": "probe-agent"}})
        lines, lo, bad, tight = self.compare(agent, loose, [item])
        self.assertEqual((len(lines), lo, bad, tight), (1, ["ls"], [], 0))

    def test_entry_without_env_gets_no_injected_agent(self):
        self.assertEqual(guard_diff.entry_item("ls"), ("ls", None))
        self.assertEqual(guard_diff.entry_item({"cmd": "ls", "role": "qa"}), ("ls", "qa"))
        marker = os.path.join(self._tmp.name, "agent.txt")
        hook = self.stub("rec.py", "import os\nopen(%r, 'a').write(os.environ.get('FACTORY_AGENT', 'unset') + '\\n')\n" % marker)
        with mock.patch.dict(os.environ):
            os.environ.pop("FACTORY_AGENT", None)
            self.compare(hook, hook, [("ls", None)])
        with open(marker) as f:
            self.assertEqual(set(f.read().split()), {"unset"})

    def test_agent_task_runs_from_its_own_worktree_fixture(self):
        # stub: allows only when the payload cwd is the worktree the DB records for task T-1
        hook = self.stub("wt.py",
            "import json, os, sqlite3, sys\n"
            "d = json.load(sys.stdin)\n"
            "con = sqlite3.connect('file:%s?mode=ro' % os.path.join(os.environ['FACTORY_HOME'], 'db', 'factory.db'), uri=True)\n"
            "row = con.execute(\"SELECT worktree FROM tasks WHERE id = 'T-1'\").fetchone()\n"
            "sys.exit(0 if row and d.get('cwd') == row[0] else 2)\n")
        item = guard_diff.entry_item({"cmd": "ls", "env": {"FACTORY_AGENT": "claude-coder-t-1"}})
        self.assertEqual(self.compare(hook, hook, [item]), ([], [], [], 0))
        loose = self.stub("loose.py", STUB_LOOSE)
        self.assertEqual(self.compare(loose, hook, [item]), ([], [], [], 0))  # hook allows it too: same verdict

    def test_malformed_env_fails_load_corpus(self):
        for bad in ("claude-coder-t-1", [], {}, {"FACTORY_AGENT": 1}, {"FACTORY_AGENT": None}, {"": "x"}):
            path = os.path.join(self._tmp.name, "raw-corpus.json")
            with open(path, "w") as f:
                json.dump({"must_block": [{"cmd": "ls", "env": bad}], "must_allow": []}, f)
            with self.assertRaises(guard_diff.UsageError, msg=repr(bad)) as cm:
                guard_diff.load_corpus(path)
            self.assertIn("'env' must be a non-empty object of string keys to string values", str(cm.exception))

    def test_malformed_env_exit_2(self):
        with open(self.corpus, "w") as f:
            json.dump({"must_block": [{"cmd": "ls", "env": {"FACTORY_AGENT": 1}}], "must_allow": []}, f)
        p = self.run_tool("--mode", "worker")
        self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
        self.assertIn("'env' must be", p.stderr)


class CorpusTokenTests(unittest.TestCase):
    def test_shared_and_live_home_cannot_mix(self):
        with self.assertRaises(guard_diff.UsageError):
            guard_diff.expand("cd @SHARED@ && ls @LIVE_HOME@")

    def test_scratch_token_expands_and_sets_hook_env(self):
        cmd = guard_diff.expand("git init @SCRATCH@/r")
        self.assertNotIn("@SCRATCH@", cmd)
        self.assertEqual(guard_diff.hook_env(cmd, {})["WORKER_GUARD_SCRATCH"], guard_diff.scratch_fixture())
        self.assertEqual(guard_diff.hook_env("git init /x", {}), {})

    def test_scratch_fixture_survives_concurrent_first_use(self):
        # T-0825: hook_env runs in compare()'s worker threads; on a fresh temp dir two threads both used to try to
        # create linked/l (main run 41140552489). Barrier forces every thread past the setup before any creates it.
        import threading
        n = 8
        barrier = threading.Barrier(n)
        real_symlink = os.symlink

        def synced_symlink(src, dst, *a, **kw):
            try:
                barrier.wait(timeout=5)
            except threading.BrokenBarrierError:
                pass
            return real_symlink(src, dst, *a, **kw)

        with tempfile.TemporaryDirectory() as t, mock.patch.object(tempfile, "tempdir", t), \
                mock.patch.object(os, "symlink", synced_symlink), ThreadPoolExecutor(max_workers=n) as ex:
            paths = [f.result() for f in [ex.submit(guard_diff.scratch_fixture) for _ in range(n)]]
            self.assertEqual(len(set(paths)), 1)
            self.assertTrue(os.path.islink(os.path.join(paths[0], "linked", "l")))


_LOAD_PIN = {}


def setUpModule():
    """T-0905 (M16816): local runs are load-gated; pin load1 low so these tests do not depend on the box's real load."""
    _LOAD_PIN["old"] = os.environ.get("FACTORY_LOADAVG")
    os.environ["FACTORY_LOADAVG"] = "0"


def tearDownModule():
    if _LOAD_PIN["old"] is None:
        os.environ.pop("FACTORY_LOADAVG", None)
    else:
        os.environ["FACTORY_LOADAVG"] = _LOAD_PIN["old"]


QA_TEMPLATE = os.path.join(ROOT, "roles", "settings", "qa.json.in")


class SettingsCompareTests(GuardDiffCase):
    """T-0327: the settings compare covers hook event+matcher+type, env and the template set."""

    def setUp(self):
        super().setUp()
        self.sdir = os.path.join(self.repo, "roles", "settings")
        self.tpl = os.path.join(self.sdir, "qa.json.in")

    def commit_base(self, edit=None):
        """Commit a copy of the repo's qa template (optionally edited) on main, then restore a clean tree."""
        os.makedirs(self.sdir, exist_ok=True)
        with open(QA_TEMPLATE, encoding="utf-8") as f:
            d = json.load(f)
        if edit:
            edit(d)
        self.write_tpl(d)
        git(self.repo, "add", ".")
        git(self.repo, "commit", "-q", "-m", "settings")

    def write_tpl(self, d):
        with open(self.tpl, "w", encoding="utf-8") as f:
            json.dump(d, f, indent=2)

    def edit_tree(self, edit):
        with open(self.tpl, encoding="utf-8") as f:
            d = json.load(f)
        edit(d)
        self.write_tpl(d)

    def settings(self, *extra):
        return self.run_tool("--mode", "worker", *extra)

    def settings_lines(self, p):
        return [ln for ln in p.stdout.splitlines() if ln.startswith("SETTINGS-")]

    @staticmethod
    def drop_edit_group(d):
        d["hooks"]["PreToolUse"] = [g for g in d["hooks"]["PreToolUse"] if g["matcher"] != "Edit|Write|MultiEdit|NotebookEdit"]

    def test_b1_deleted_matcher_group_is_loosened(self):
        self.commit_base()
        self.edit_tree(self.drop_edit_group)
        p = self.settings()
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        self.assertRegex(p.stdout, r"(?m)^SETTINGS-LOOSENED qa hook removed: PreToolUse\|Edit\|Write")

    def test_b1_narrowed_matcher_is_loosened_and_tightened(self):
        self.commit_base()

        def narrow(d):
            for g in d["hooks"]["PreToolUse"]:
                if g["matcher"] == "Bash":
                    g["matcher"] = "Bash(git:*)"
        self.edit_tree(narrow)
        p = self.settings()
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        lines = self.settings_lines(p)
        self.assertEqual(len([ln for ln in lines if ln.startswith("SETTINGS-LOOSENED qa hook")]), 1, lines)
        self.assertEqual(len([ln for ln in lines if ln.startswith("SETTINGS-TIGHTENED qa hook")]), 1, lines)

    def test_b2_non_pretooluse_event_removed_is_loosened(self):
        def add_stop(d):
            d["hooks"]["Stop"] = [{"hooks": [{"type": "command", "command": "python3 guard-stop.py"}]}]
        self.commit_base(add_stop)
        self.edit_tree(lambda d: d["hooks"].pop("Stop"))
        p = self.settings()
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        self.assertRegex(p.stdout, r"(?m)^SETTINGS-LOOSENED qa hook removed: Stop\|")

    def test_b3_env_removed_is_loosened(self):
        self.commit_base()
        self.edit_tree(lambda d: d["env"].pop("CLAUDE_CODE_DISABLE_ADVISOR_TOOL"))
        p = self.settings()
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        self.assertRegex(p.stdout, r"(?m)^SETTINGS-LOOSENED qa env removed: CLAUDE_CODE_DISABLE_ADVISOR_TOOL=")

    def test_b3_env_value_changed_is_loosened(self):
        self.commit_base()
        self.edit_tree(lambda d: d["env"].__setitem__("FACTORY_ROLE", "coder"))
        p = self.settings()
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        self.assertIn("SETTINGS-LOOSENED qa env removed: FACTORY_ROLE=qa", p.stdout)
        self.assertIn("SETTINGS-CHANGED qa env added: FACTORY_ROLE=coder", p.stdout)

    def test_b3_env_new_key_is_shown_not_counted(self):
        self.commit_base()
        self.edit_tree(lambda d: d["env"].__setitem__("NEW_KEY", "1"))
        p = self.settings()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertRegex(p.stdout, r"(?m)^SETTINGS-CHANGED qa env added: NEW_KEY=1")
        self.assertIn("0 loosened", p.stdout)

    def test_b4_deleted_template_is_loosened(self):
        self.commit_base()
        git(self.repo, "rm", "-q", "roles/settings/qa.json.in")
        p = self.settings()
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        self.assertRegex(p.stdout, r"(?m)^SETTINGS-LOOSENED qa template removed")

    def test_b4_deleted_template_is_loosened_with_repo_subdir(self):
        """--repo <repo>/<subdir>: ls-tree must be --full-tree, else the base role set is empty and this fails open."""
        sub = os.path.join(self.repo, "subdir")
        os.makedirs(sub)
        with open(os.path.join(sub, "keep"), "w", encoding="utf-8") as f:
            f.write("x\n")
        self.commit_base()
        git(self.repo, "rm", "-q", "roles/settings/qa.json.in")
        argv = [sys.executable, TOOL, "--base", "main", "--repo", sub, "--hook", self.hook,
                "--corpus", self.corpus, "--mode", "worker"]
        p = subprocess.run(argv, capture_output=True, text=True, timeout=600)
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        self.assertRegex(p.stdout, r"(?m)^SETTINGS-LOOSENED qa template removed")

    def test_b4_deleted_settings_dir_is_loosened(self):
        self.commit_base()
        shutil.rmtree(self.sdir)
        p = self.settings()
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        self.assertRegex(p.stdout, r"(?m)^SETTINGS-LOOSENED qa template removed")

    def test_base_without_settings_dir_is_not_an_error(self):
        os.makedirs(self.sdir)
        shutil.copy(QA_TEMPLATE, self.tpl)
        p = self.settings()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("SETTINGS-NEW qa", p.stdout)

    def test_unchanged_template_prints_no_settings_line(self):
        self.commit_base()
        p = self.settings()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.settings_lines(p), [])

    def test_legacy_base_has_no_false_loosened(self):
        # base: pre-template qa.json with the owner's absolute paths; tree: the template with tokens
        with open(QA_TEMPLATE, encoding="utf-8") as f:
            text = f.read()
        legacy = text.replace("/@FACTORY_LIVE_HOME@", "/" + LIVE_HOME).replace("@FACTORY_LIVE_HOME@", LIVE_HOME)
        legacy = legacy.replace("@FACTORY_HOME_DIR@", os.path.dirname(LIVE_HOME))
        os.makedirs(self.sdir)
        with open(os.path.join(self.sdir, "qa.json"), "w", encoding="utf-8") as f:
            f.write(legacy)
        git(self.repo, "add", ".")
        git(self.repo, "commit", "-q", "-m", "legacy")
        git(self.repo, "rm", "-q", "roles/settings/qa.json")
        os.makedirs(self.sdir, exist_ok=True)
        shutil.copy(QA_TEMPLATE, self.tpl)
        p = self.settings()
        self.assertNotIn("SETTINGS-LOOSENED", p.stdout)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)

    def test_allow_loosen_honours_new_hook_text(self):
        self.commit_base()
        self.edit_tree(self.drop_edit_group)
        first = self.settings()
        texts = [ln[len("SETTINGS-LOOSENED "):] for ln in first.stdout.splitlines() if ln.startswith("SETTINGS-LOOSENED")]
        self.assertEqual(len(texts), 1, first.stdout)
        allow = os.path.join(self._tmp.name, "allow.txt")
        with open(allow, "w") as f:
            f.write("\n".join(texts) + "\n")
        p = self.settings("--allow-loosen", allow)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("SETTINGS-LOOSENED", p.stdout)


class SettingsFollowUpTests(GuardDiffCase):
    """T-0944: permissions.ask, autoMode, hook timeout and non-ASCII role names in the settings compare."""

    def setUp(self):
        super().setUp()
        self.sdir = os.path.join(self.repo, "roles", "settings")
        self.tpl = os.path.join(self.sdir, "qa.json.in")

    commit_base = SettingsCompareTests.commit_base
    write_tpl = SettingsCompareTests.write_tpl
    edit_tree = SettingsCompareTests.edit_tree
    settings = SettingsCompareTests.settings
    settings_lines = SettingsCompareTests.settings_lines

    @staticmethod
    def seed(d):
        d.setdefault("permissions", {})["ask"] = ["Bash(rm -rf *)", "Bash(git push *)"]
        d["autoMode"] = {"environment": ["$defaults", "env text"], "allow": ["allow text"]}
        for g in d["hooks"]["PreToolUse"]:
            for h in g["hooks"]:
                h["timeout"] = 10

    def test_removed_ask_rule_is_loosened(self):
        self.commit_base(self.seed)
        self.edit_tree(lambda d: d["permissions"]["ask"].remove("Bash(rm -rf *)"))
        p = self.settings()
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        self.assertIn("SETTINGS-LOOSENED qa ask removed: Bash(rm -rf *)", p.stdout)
        self.assertIn("1 loosened", p.stdout)

    def test_added_ask_rule_is_tightened(self):
        self.commit_base(self.seed)
        self.edit_tree(lambda d: d["permissions"]["ask"].append("Bash(curl *)"))
        p = self.settings()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("SETTINGS-TIGHTENED qa ask added: Bash(curl *)", p.stdout)
        self.assertIn("1 tightened", p.stdout)

    def test_changed_automode_is_loosened_and_changed(self):
        self.commit_base(self.seed)
        self.edit_tree(lambda d: d["autoMode"]["allow"].__setitem__(0, "wider allow text"))
        p = self.settings()
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        self.assertIn("SETTINGS-LOOSENED qa autoMode removed: allow: allow text", p.stdout)
        self.assertIn("SETTINGS-CHANGED qa autoMode added: allow: wider allow text", p.stdout)

    def test_changed_hook_timeout_is_loosened_keeps_hook_text(self):
        self.commit_base(self.seed)
        self.edit_tree(lambda d: d["hooks"]["PreToolUse"][0]["hooks"][0].__setitem__("timeout", 99))
        p = self.settings()
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        lines = self.settings_lines(p)
        self.assertEqual(len([ln for ln in lines if ln.startswith("SETTINGS-LOOSENED qa hook-timeout removed: ")]), 1, lines)
        self.assertEqual(len([ln for ln in lines if "hook-timeout added: " in ln and ln.startswith("SETTINGS-CHANGED")]), 1, lines)
        self.assertFalse([ln for ln in lines if " hook removed: " in ln or " hook added: " in ln], lines)

    def test_removed_hook_does_not_double_report_timeout(self):
        self.commit_base(self.seed)
        self.edit_tree(self.drop_edit_group)
        p = self.settings()
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        self.assertRegex(p.stdout, r"(?m)^SETTINGS-LOOSENED qa hook removed: PreToolUse\|Edit\|Write")
        self.assertNotIn("hook-timeout", p.stdout)

    drop_edit_group = staticmethod(SettingsCompareTests.drop_edit_group)

    def test_non_ascii_role_template_removed_is_loosened(self):
        self.commit_base()
        extra = os.path.join(self.sdir, "q\u00e4.json.in")
        shutil.copy(self.tpl, extra)
        git(self.repo, "add", ".")
        git(self.repo, "commit", "-q", "-m", "non-ascii role")
        os.remove(extra)
        git(self.repo, "add", "-A")
        p = self.settings()
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        self.assertIn("SETTINGS-LOOSENED q\u00e4 template removed", p.stdout)

    def test_pure_automode_addition_is_counted_and_added_hook_has_no_timeout_line(self):
        self.commit_base(self.seed)

        def edit(d):
            d["autoMode"]["allow"].append("brand new allow text")
            d["hooks"]["Stop"] = [{"hooks": [{"type": "command", "command": "true", "timeout": 5}]}]
        self.edit_tree(edit)
        p = self.settings()
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        self.assertIn("SETTINGS-CHANGED qa autoMode added: allow: brand new allow text", p.stdout)
        self.assertIn("SETTINGS-TIGHTENED qa hook added: Stop||command|true", p.stdout)
        self.assertNotIn("hook-timeout", p.stdout)

    def test_automode_removal_and_addition_under_different_keys_counts_both(self):
        self.commit_base(self.seed)

        def edit(d):
            d["autoMode"]["environment"].remove("env text")
            d["autoMode"]["allow"].append("wide allow text")
        self.edit_tree(edit)
        p = self.settings()
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        self.assertIn("2 loosened", p.stdout)
        allow = os.path.join(self._tmp.name, "allow.txt")
        with open(allow, "w", encoding="utf-8") as f:
            f.write("qa autoMode removed: environment: env text\n")
        p = self.settings("--allow-loosen", allow)
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)  # the unrelated added allow entry is still unplanned
        self.assertIn("qa autoMode added: allow: wide allow text", p.stdout)

    def test_malformed_shapes_fail_closed_with_one_line_error(self):
        cases = [
            ("autoMode", lambda d: d.__setitem__("autoMode", None)),
            ("autoMode", lambda d: d.__setitem__("autoMode", ["x"])),
            ("permissions.ask", lambda d: d["permissions"].__setitem__("ask", None)),
            ("permissions.allow", lambda d: d["permissions"].__setitem__("allow", None)),
            ("permissions.deny", lambda d: d["permissions"].__setitem__("deny", "Bash(*)")),
            ("env", lambda d: d.__setitem__("env", None)),
            ("env", lambda d: d.__setitem__("env", ["A=1"])),
        ]
        self.commit_base(self.seed)
        for key, edit in cases:
            with self.subTest(key=key, edit=edit):
                self.edit_tree(edit)
                p = self.settings()
                self.assertNotEqual(p.returncode, 0, p.stdout + p.stderr)
                self.assertNotIn("Traceback", p.stdout + p.stderr)
                self.assertIn("qa.json.in", p.stderr)
                self.assertIn(f"{key} must be a JSON", p.stderr)
                self.assertEqual(len(p.stderr.strip().splitlines()), 1, p.stderr)
                self.assertNotIn("no change", p.stdout.lower())
                git(self.repo, "checkout", "-q", "--", ".")

    def test_hook_field_types_and_newline_event_fail_closed_on_one_line(self):
        def hook_edit(field, value, where):
            def edit(d):
                grp = d["hooks"]["PreToolUse"][0]
                (grp if where == "group" else grp["hooks"][0])[field] = value
            return edit

        def newline_event(d):
            d["hooks"]["Pre\nTool"] = [{"hooks": None}]

        cases = [("matcher", hook_edit("matcher", v, "group")) for v in (None, 5, ["a"])]
        cases += [("matcher", lambda d: d["hooks"]["PreToolUse"].append({"matcher": None}))]
        cases += [("type", hook_edit("type", v, "hook")) for v in (None, 5)]
        cases += [("command", hook_edit("command", v, "hook")) for v in (None, 5, ["a"])]
        cases.append(("Pre\\nTool", newline_event))
        self.commit_base(self.seed)
        for key, edit in cases:
            with self.subTest(key=key, edit=edit):
                self.edit_tree(edit)
                p = self.settings()
                self.assertNotEqual(p.returncode, 0, p.stdout + p.stderr)
                self.assertNotIn("Traceback", p.stdout + p.stderr)
                self.assertIn("qa.json.in", p.stderr)
                self.assertIn(key, p.stderr)
                self.assertEqual(len(p.stderr.strip().splitlines()), 1, p.stderr)
                self.assertNotIn("no change", p.stdout.lower())
                git(self.repo, "checkout", "-q", "--", ".")

    def test_missing_hook_matcher_type_command_still_work(self):
        self.commit_base(self.seed)

        def edit(d):
            d["hooks"]["Stop"] = [{"hooks": [{}]}]
        self.edit_tree(edit)
        p = self.settings()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("SETTINGS-TIGHTENED qa hook added: Stop|||", p.stdout)

    def test_malformed_base_settings_fail_closed(self):
        self.commit_base(lambda d: d["permissions"].__setitem__("ask", None))
        p = self.settings()
        self.assertNotEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertNotIn("Traceback", p.stdout + p.stderr)
        self.assertIn("permissions.ask must be a JSON list", p.stderr)

    def test_added_automode_key_with_colon_or_equals_is_loosened(self):
        self.commit_base(self.seed)
        for new_key in ("environment:prod", "environment=prod"):
            with self.subTest(new_key=new_key):

                def edit(d):
                    d["autoMode"]["environment"].remove("env text")
                    d["autoMode"][new_key] = ["wide text"]
                self.edit_tree(edit)
                p = self.settings()
                self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
                self.assertIn("2 loosened", p.stdout)
                git(self.repo, "checkout", "-q", "--", ".")

    def test_unchanged_template_with_new_keys_prints_no_settings_line(self):
        self.commit_base(self.seed)
        p = self.settings()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.settings_lines(p), [])

    def test_allow_loosen_honours_new_texts(self):
        self.commit_base(self.seed)

        def edit(d):
            d["permissions"]["ask"].pop()
            d["autoMode"]["allow"][0] = "other"
            d["hooks"]["PreToolUse"][0]["hooks"][0]["timeout"] = 99
        self.edit_tree(edit)
        first = self.settings()
        self.assertEqual(first.returncode, 1, first.stdout + first.stderr)
        texts = [ln[len("SETTINGS-LOOSENED "):] for ln in first.stdout.splitlines() if ln.startswith("SETTINGS-LOOSENED")]
        self.assertEqual(len(texts), 3, first.stdout)
        allow = os.path.join(self._tmp.name, "allow.txt")
        with open(allow, "w", encoding="utf-8") as f:
            f.write("\n".join(texts) + "\n")
        p = self.settings("--allow-loosen", allow)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)


if __name__ == "__main__":
    unittest.main()


STUB_LOADS_CI = '''import importlib.util, os, sys
sys.stdin.read()
path = os.path.join(os.path.dirname(os.path.dirname(os.path.dirname(os.path.realpath(__file__)))), "ci", "public_text_guard.py")
try:
    spec = importlib.util.spec_from_file_location("ptg", path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
except Exception:
    sys.exit(2)
sys.exit(0)
'''


class BaseCiExtractTests(GuardDiffCase):
    """T-0875: the base hook is extracted with the base ref's ci/ files, so it can load public-text-guard."""

    def setUp(self):
        super().setUp()
        ci = os.path.join(self.repo, "ci")
        os.makedirs(ci)
        with open(os.path.join(ci, "scrub_scan.py"), "w") as f:
            f.write("MARK = 1\n")
        with open(os.path.join(ci, "public_text_guard.py"), "w") as f:
            f.write("import importlib.util, os\n"
                    "_p = os.path.join(os.path.dirname(os.path.realpath(__file__)), 'scrub_scan.py')\n"
                    "_s = importlib.util.spec_from_file_location('s', _p)\n"
                    "scrub = importlib.util.module_from_spec(_s)\n_s.loader.exec_module(scrub)\n")
        self.write_hook(STUB_LOADS_CI)
        git(self.repo, "add", ".")
        git(self.repo, "commit", "-q", "-m", "base with ci")

    def test_base_hook_loads_public_text_guard(self):
        p = self.run_tool("--mode", "both")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("0 loosened, 0 tightened", p.stdout)
        self.assertNotIn("LOOSENED", p.stdout)

    def test_base_without_ci_still_runs(self):
        git(self.repo, "rm", "-q", "-r", "ci")
        git(self.repo, "commit", "-q", "-m", "drop ci")
        p = self.run_tool("--mode", "worker")
        self.assertIn(p.returncode, (0, 1), p.stderr)
        self.assertNotIn("guard-diff:", p.stderr)
