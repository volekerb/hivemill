import contextlib
import importlib.machinery
import importlib.util
import io
import json
import os
import pwd
import subprocess
import sys
import tempfile
import unittest
from unittest import mock
from concurrent.futures import ThreadPoolExecutor

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
    subprocess.run(["git", "-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.com", *args],
                   check=True, capture_output=True, text=True)


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
            got = [hook_rc(j[0], j[1], j[2], home if j[2] == "evaluator" else None) for j in jobs]
            for i in range(0, len(jobs), 17):  # the real entry point on a sample of the same jobs
                j = jobs[i]
                self.assertEqual(hook_rc_subprocess(j[0], j[1], j[2], home if j[2] == "evaluator" else None), got[i], j)
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
        p = subprocess.run([sys.executable, TOOL, "--base", "HEAD", "--repo", ROOT, "--mode", "worker"],
                           capture_output=True, text=True, timeout=300)
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
