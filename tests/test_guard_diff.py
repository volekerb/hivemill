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
        return subprocess.run(argv, capture_output=True, text=True, timeout=120)


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
        p = subprocess.run(argv, capture_output=True, text=True, timeout=60)
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


def hook_rc_subprocess(cmd, mode, role="coder", home=None):
    args = ["--role", role] if mode == "worker" else ["--hq"]
    env = {**os.environ, "FACTORY_HOME": home} if home else None
    env = guard_diff.hook_env(cmd, env)
    p = subprocess.run([sys.executable, HOOK, *args],
                       input=json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}}),
                       capture_output=True, text=True, timeout=30, env=env)
    return p.returncode


def hook_rc(cmd, mode, role="coder", home=None):
    """Exit code of the real hook's main(), run in-process: the corpus sanity checks make ~500 calls and a subprocess each
    costs ~40 ms of startup. CorpusSanityTests.check compares a sample with hook_rc_subprocess."""
    if not _HOOK_MOD:
        loader = importlib.machinery.SourceFileLoader("worker_guard_under_test", HOOK)
        mod = importlib.util.module_from_spec(importlib.util.spec_from_loader(loader.name, loader))
        loader.exec_module(mod)
        _HOOK_MOD.append(mod)
    mod = _HOOK_MOD[0]
    env = {**os.environ, "FACTORY_HOME": home} if home else None
    env = guard_diff.hook_env(cmd, env)
    argv = ["worker-guard.py"] + (["--role", role] if mode == "worker" else ["--hq"])
    payload = io.StringIO(json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}}))
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
        p = self.run_tool("--mode", "worker", "--role", "evaluator", "--corpus", CORPUS)
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
        p = self.run_tool("--mode", "worker", "--role", "evaluator", "--corpus", CORPUS)
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
            p = subprocess.run(argv, capture_output=True, text=True, timeout=60)
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
        return subprocess.run(argv, capture_output=True, text=True, timeout=120)

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

    def test_roles_all_derived_from_templates(self):
        sdir = os.path.join(self.repo, "roles", "settings")
        os.makedirs(sdir)
        for role in ("alpha", "beta-two"):
            with open(os.path.join(sdir, role + ".json.in"), "w") as f:
                f.write('{"command": "python3 x/roles/hooks/worker-guard.py --role %s"}' % role)
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
        p = subprocess.run(argv, capture_output=True, text=True, timeout=60)
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
        p = subprocess.run(argv, capture_output=True, text=True, timeout=120, env=env)
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
        p = subprocess.run(argv, capture_output=True, text=True, timeout=60, env=env)
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
        p = subprocess.run(argv, capture_output=True, text=True, timeout=60,
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
            jobs += [(cmd, m, role or "coder") for m in modes]
        with tempfile.TemporaryDirectory(prefix="zz-test-gdhome-") as home:
            got = [hook_rc(j[0], j[1], j[2], home) for j in jobs]
            for i in range(0, len(jobs), 17):  # the real entry point on a sample of the same jobs
                j = jobs[i]
                self.assertEqual(hook_rc_subprocess(j[0], j[1], j[2], home), got[i], j)
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
                               capture_output=True, text=True, timeout=300, env={**os.environ, "FACTORY_HOME": home})
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("0 loosened", p.stdout)



class CorpusTokenTests(unittest.TestCase):
    def test_shared_and_live_home_cannot_mix(self):
        with self.assertRaises(guard_diff.UsageError):
            guard_diff.expand("cd @SHARED@ && ls @LIVE_HOME@")

    def test_scratch_token_expands_and_sets_hook_env(self):
        cmd = guard_diff.expand("git init @SCRATCH@/r")
        self.assertNotIn("@SCRATCH@", cmd)
        self.assertEqual(guard_diff.hook_env(cmd, {})["WORKER_GUARD_SCRATCH"], guard_diff.scratch_fixture())
        self.assertEqual(guard_diff.hook_env("git init /x", {}), {})

if __name__ == "__main__":
    unittest.main()
