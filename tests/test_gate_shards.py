"""T-0515: tests/gate_shards.py, the parallel sharded suite a project's gate_cmd runs. Uses a tiny fake tree whose
ci/shard.py is the real one and whose tests/ holds three stub modules; the real suite is never run here."""
import fcntl
import json
import os
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
HELPER = ROOT / "tests" / "gate_shards.py"


def make_tree(fail=False):
    d = Path(tempfile.mkdtemp(prefix="zz-test-gate-shards-"))
    (d / "ci").mkdir()
    (d / "tests").mkdir()
    shutil.copy2(ROOT / "ci" / "shard.py", d / "ci" / "shard.py")
    for n in "abc":
        body = "import unittest\nclass T(unittest.TestCase):\n    def test_one(self): pass\n    def test_two(self): pass\n"
        if fail and n == "b":
            body += "    def test_bad(self): self.fail('boom')\n"
        (d / "tests" / f"test_{n}.py").write_text(body)
    return d


def run(tree, shards, report=None):
    env = {**os.environ, "FACTORY_TEST_SLOT_HELD": "1", "GATE_SHARDS": str(shards), "GATE_REPORT_DIR": str(report)}
    return subprocess.run([sys.executable, str(HELPER)], cwd=tree, env=env, capture_output=True, text=True, timeout=120)


class GateShards(unittest.TestCase):
    def setUp(self):
        self.tree = make_tree()
        self.report = Path(tempfile.mkdtemp(prefix="zz-test-gate-report-"))
        for d in (self.tree, self.report):
            self.addCleanup(shutil.rmtree, d, True)

    def test_all_shards_pass_and_counts_aggregate(self):
        p = run(self.tree, 3, self.report)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        summary = json.loads((self.report / "summary.json").read_text())
        self.assertEqual((summary["shards"], summary["tests"]), (3, 6))
        self.assertIn("gate shards: 3 shards, 6 tests", p.stdout)
        self.assertIn("3 modules, 6 tests across 3 shards", p.stdout)

    def test_a_failing_test_in_one_shard_fails_the_run_and_shows_its_log(self):
        shutil.rmtree(self.tree)
        self.tree = make_tree(fail=True)
        self.addCleanup(shutil.rmtree, self.tree, True)
        p = run(self.tree, 3, self.report)
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        self.assertIn("boom", p.stdout)
        self.assertIn("FAIL, shards with a non-zero exit", p.stderr)

    def test_zero_tests_is_a_failure(self):
        for f in (self.tree / "tests").glob("test_*.py"):
            f.write_text("import unittest\n")
        p = run(self.tree, 2, self.report)
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)

    def test_bad_shard_count_is_refused(self):
        self.assertEqual(run(self.tree, 0, self.report).returncode, 2)


# T-1633: ci/remote_gate.sh, the remote gate. ssh is the process boundary: a stub on PATH runs the "remote" command
# locally under a temp fake remote home, so these run without the remote host. The fake tree is tiny, so every real step of
# the remote driver fails fast; the tests check the contract around the steps (refusals, count, cleanup, lock, env).
GATE_SH = ROOT / "ci" / "remote_gate.sh"
SSH_STUB = """#!/bin/bash
printf '%s\\n' "$*" >> "$STUB_LOG"
while [ $# -gt 0 ]; do case "$1" in -o) shift 2 ;; -*) shift ;; *) break ;; esac; done
shift
cd "$FAKE_REMOTE_HOME"
# Like a real client: the "remote" command is not our pid, and a TERM ends the client (the remote side sees the
# connection drop) without waiting for the remote command.
trap 'exit 143' TERM
HOME="$FAKE_REMOTE_HOME" bash -c "$*" <&0 &
wait "$!"
"""
FAKE_FACTORY = """#!/usr/bin/env python3
import json, os, time
json.dump({"env": dict(os.environ), "cwd": os.getcwd(), "files": sorted(os.listdir("."))}, open(%r, "w"))
time.sleep(%d)
"""


def git(tree, *args):
    subprocess.run(["git", "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false", *args],
                   cwd=tree, check=True, capture_output=True)


class RemoteGate(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp(prefix="zz-test-remote-gate-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.tree, self.remote, stub = self.tmp / "tree", self.tmp / "remote-home", self.tmp / "stub"
        for d in (self.tree, self.remote, stub):
            d.mkdir()
        (stub / "ssh").write_text(SSH_STUB)
        (stub / "ssh").chmod(0o755)
        self.log, self.mark = self.tmp / "ssh.log", self.tmp / "remote-env.json"
        self.env = {**os.environ, "PATH": f"{stub}:{os.environ['PATH']}", "STUB_LOG": str(self.log),
                    "FAKE_REMOTE_HOME": str(self.remote), "GH_TOKEN": "gh-secret-test", "GITHUB_TOKEN": "gh-secret-test",
                    "SSH_AUTH_SOCK": str(self.tmp / "agent.sock"), "FACTORY_AGENT": "claude-coder-x", "GATE_SHARDS": "12",
                    "GATE_REMOTE_HOST": "gate-host"}
        git(self.tree, "init", "-q", "-b", "main")
        (self.tree / "tests").mkdir()
        (self.tree / "tests" / "test_a.py").write_text(
            "import unittest\nclass T(unittest.TestCase):\n    def test_one(self): pass\n    def test_two(self): pass\n")
        git(self.tree, "add", "-A")
        git(self.tree, "commit", "-q", "-m", "base")
        git(self.tree, "update-ref", "refs/remotes/origin/main", "HEAD")
        self.commit_head(sleep=0)

    def commit_head(self, sleep):
        (self.tree / "bin").mkdir(exist_ok=True)
        (self.tree / "bin" / "factory").write_text(FAKE_FACTORY % (str(self.mark), sleep))
        (self.tree / "bin" / "factory_cli.py").write_text("X = 1\n")
        git(self.tree, "add", "-A")
        git(self.tree, "commit", "-q", "-m", f"head sleep {sleep}")

    def gate(self, *args, **kw):
        return subprocess.run(["bash", str(GATE_SH), *args], cwd=self.tree, env=self.env, capture_output=True,
                              text=True, timeout=kw.get("timeout", 180))

    def ssh_calls(self):
        return self.log.read_text().splitlines() if self.log.exists() else []

    def run_dirs(self):
        d = self.remote / "factory-gate"
        return sorted(p.name for p in d.iterdir() if p.is_dir()) if d.exists() else []

    def test_help_and_bad_arguments(self):
        p = self.gate("--help")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("--count", p.stdout)
        p = self.gate("--bogus")
        self.assertEqual(p.returncode, 2)
        self.assertIn("unknown argument '--bogus'", p.stderr)
        self.assertEqual(self.gate("--count", "--plan").returncode, 2)
        self.assertEqual(self.ssh_calls(), [])

    def test_missing_or_odd_host_is_refused_before_any_ssh(self):
        for host in ("", "-oProxyCommand=x", "a b"):
            self.env["GATE_REMOTE_HOST"] = host
            p = self.gate()
            self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
            self.assertIn("GATE_REMOTE_HOST", p.stderr)
        del self.env["GATE_REMOTE_HOST"]
        self.assertEqual(self.gate().returncode, 2)
        self.assertEqual(self.gate("--count").returncode, 0)  # the count needs no host
        self.assertEqual(self.ssh_calls(), [])

    def test_bad_shard_count_is_refused_before_any_ssh(self):
        for v in ("abc", "0", "-1"):
            self.env["GATE_SHARDS"] = v
            p = self.gate()
            self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
            self.assertIn("GATE_SHARDS", p.stderr)
        self.assertEqual(self.ssh_calls(), [])

    def test_no_merge_base_with_main_is_refused_not_guessed(self):
        git(self.tree, "update-ref", "-d", "refs/remotes/origin/main")
        p = self.gate()
        self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
        self.assertIn("no merge base", p.stderr)
        self.assertEqual(self.ssh_calls(), [])
        base = subprocess.run(["git", "rev-parse", "HEAD^"], cwd=self.tree, capture_output=True, text=True).stdout.strip()
        self.env["GATE_BASE"] = base  # an explicit base is used as given
        p = self.gate()
        self.assertIn(f"base {base}", p.stdout)

    def test_dirty_tree_is_refused_before_any_ssh(self):
        (self.tree / "untracked.txt").write_text("x")
        for args in ((), ("--count",)):
            p = self.gate(*args)
            self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
            self.assertIn("refusing", p.stderr)
            self.assertIn("untracked.txt", p.stderr)
        (self.tree / "untracked.txt").unlink()
        (self.tree / "bin" / "factory_cli.py").write_text("X = 2\n")  # tracked, modified
        self.assertEqual(self.gate().returncode, 2)
        self.assertEqual(self.ssh_calls(), [])

    def test_count_prints_the_test_count_as_last_line_without_ssh(self):
        p = self.gate("--count")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(p.stdout.strip().splitlines()[-1], "2")
        self.assertEqual(self.ssh_calls(), [])

    def test_plan_lists_every_step_and_the_header_maps_each(self):
        p = self.gate("--plan")
        self.assertEqual(p.returncode, 0, p.stderr)
        steps = p.stdout.split()
        self.assertIn("unittest", steps)
        self.assertIn("go-test", steps)
        self.assertIn("docs-check", steps)
        header = GATE_SH.read_text().split("set -euo pipefail")[0]
        for s in steps:
            stem = s.rstrip("0123456789").rstrip("-")
            self.assertRegex(header, rf"#   {stem}(-\d\.\.\d)?\s", f"step {s} not mapped to a tests.yml job in the header")
        remote = (ROOT / "ci" / "remote_gate_driver.sh").read_text()
        ran = [ln.split()[1] for ln in remote.splitlines() if ln.startswith("step ")]
        self.assertEqual([s for s in steps if not s.startswith("guard-") or s == "guard-build"], ran)

    def test_failed_run_exits_nonzero_cleans_up_and_ships_no_secrets(self):
        p = self.gate()
        self.assertNotEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("remote-gate-driver: FAIL", p.stdout)
        self.assertIn("removed gate-host:~/factory-gate/", p.stdout)
        self.assertEqual(self.run_dirs(), [], "remote run dir left behind")
        calls = self.ssh_calls()
        self.assertTrue(all("SendEnv=-*" in c and "BatchMode=yes" in c for c in calls), calls)
        seen = json.loads(self.mark.read_text())
        for k in ("GH_TOKEN", "GITHUB_TOKEN", "SSH_AUTH_SOCK", "FACTORY_AGENT"):
            self.assertNotIn(k, seen["env"])
        self.assertNotIn("gh-secret-test", json.dumps(seen))
        self.assertEqual(seen["env"]["GATE_SHARDS"], "8")  # 12 asked, capped at 8
        self.assertTrue(seen["env"]["HOME"].startswith(str(self.remote / "factory-gate")), seen["env"]["HOME"])
        self.assertTrue(seen["env"]["FACTORY_HOME"].startswith(str(self.remote / "factory-gate")))
        # the head tree is the archive (plus the rebuilt history): no .ssh, .claude, db
        self.assertEqual(sorted(set(seen["files"]) - {".git"}), ["bin", "tests"])

    def test_sigterm_to_the_process_group_removes_the_remote_dir(self):
        self.commit_head(sleep=120)
        p = subprocess.Popen(["bash", str(GATE_SH)], cwd=self.tree, env=self.env, stdout=subprocess.PIPE,
                             stderr=subprocess.STDOUT, text=True, start_new_session=True)
        self.addCleanup(lambda: p.poll() is None and os.killpg(p.pid, 9))
        for _ in range(600):
            if self.mark.exists():
                break
            time.sleep(0.1)
        self.assertTrue(self.mark.exists(), "the run never reached factory-help")
        self.assertEqual(len(self.run_dirs()), 1)
        os.killpg(p.pid, signal.SIGTERM)
        out, _ = p.communicate(timeout=60)
        self.assertNotEqual(p.returncode, 0, out)
        self.assertEqual(self.run_dirs(), [], out)

    def test_second_run_waits_for_the_remote_lock(self):
        (self.remote / "factory-gate").mkdir()
        with open(self.remote / "factory-gate" / ".lock", "w") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            p = subprocess.Popen(["bash", str(GATE_SH)], cwd=self.tree, env=self.env, stdout=subprocess.PIPE,
                                 stderr=subprocess.STDOUT, text=True, start_new_session=True)
            self.addCleanup(lambda: p.poll() is None and os.killpg(p.pid, 9))
            seen = []
            for line in p.stdout:
                seen.append(line)
                if "waiting" in line:
                    break
            self.assertIn("another gate run holds", "".join(seen))
            time.sleep(1)
            self.assertFalse(self.mark.exists(), "a step ran while the lock was held")
            fcntl.flock(lock, fcntl.LOCK_UN)
        out, _ = p.communicate(timeout=180)
        self.assertNotEqual(p.returncode, 0)
        self.assertTrue(self.mark.exists(), "the run did not continue after the lock was released")
        self.assertEqual(self.run_dirs(), [], out)


if __name__ == "__main__":
    unittest.main()
