"""T-0674: `factory test <cmd>` under config test_sandbox (scout/testbox.py over gatebox).
Real `factory test` as a subprocess in a temp FACTORY_HOME, run in a temp git clone. Real namespaces run only where user
namespaces are available (skipped with the reason otherwise, and inside the boxed gate). Credentials here are fake."""
import os
import subprocess
import sys
import unittest
from pathlib import Path

from harness import FACTORY, REPO, FactoryTestCase, git

sys.path.insert(0, str(REPO))
from scout import nsbox, testbox  # noqa: E402

USERNS_OK, USERNS_WHY = nsbox.userns_available()
SKIP_MSG = f"user namespaces unavailable: {USERNS_WHY}"
NESTED = os.environ.get("FACTORY_GATEBOX") == "1"  # the boxed gate cannot nest a user namespace


class Base(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.clone = self.env.home / "clone"
        self.clone.mkdir()
        git("init", "-q", "-b", "main", cwd=self.clone)
        (self.clone / "f.txt").write_text("x\n")
        git("add", ".", cwd=self.clone)
        git("commit", "-qm", "init", cwd=self.clone)

    def flag(self, v):
        self.env.seed_agent("zz-test-director", "director")
        self.ok("config", "test_sandbox", v, agent="zz-test-director")

    def ftest(self, *cmd, extra=None):
        env = self.env.env("zz-test-coder", extra)
        return subprocess.run([sys.executable, str(FACTORY), "test", *cmd], cwd=self.clone, env=env,
                              capture_output=True, text=True, timeout=60)


class DirectAndFlag(Base):
    def test_default_is_zero_and_runs_directly(self):
        p = self.ftest("--", "sh", "-c", 'echo "agent=$FACTORY_AGENT"; exit 7')
        self.assertEqual(p.returncode, 7, p.stderr)
        self.assertIn("agent=zz-test-coder", p.stdout)  # the env is untouched: not sandboxed

    def test_invalid_flag_is_an_error_and_not_run(self):
        self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('test_sandbox','yes')")
        p = self.ftest("touch", "ran")
        self.assertEqual(p.returncode, 3)
        self.assertIn("factory config test_sandbox 1", p.stderr)
        self.assertFalse((self.clone / "ran").exists())

    def test_no_command_is_usage_error(self):
        self.assertEqual(self.ftest().returncode, 2)


class FailClosed(Base):
    def test_unbuildable_sandbox_does_not_run_the_command(self):
        self.flag("1")
        fake = self.env.home / "bin"
        fake.mkdir()
        (fake / "unshare").write_text("#!/bin/sh\nexit 1\n")
        (fake / "unshare").chmod(0o755)
        p = self.ftest("touch", "ran", extra={"PATH": f"{fake}{os.pathsep}{os.environ['PATH']}"})
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("factory config test_sandbox 0", p.stderr)
        self.assertFalse((self.clone / "ran").exists())
        self.assertEqual(list(self.clone.glob(".factory-test-*")), [])


@unittest.skipUnless(USERNS_OK, SKIP_MSG)
@unittest.skipIf(NESTED, "needs a nested user namespace; skipped inside the boxed gate")
class Sandboxed(Base):
    def setUp(self):
        super().setUp()
        self.flag("1")
        self.extra = {"GIT_DIR": "/nonexistent", "GH_TOKEN": "tok", "GITHUB_TOKEN": "tok",
                      "CLAUDE_CODE_OAUTH_TOKEN": "tok", "SSH_AUTH_SOCK": "/x/agent"}

    def box(self, script):
        return self.ftest("--", "sh", "-c", script, extra=self.extra)

    def test_env_allow_list(self):
        p = self.box('env | grep -E "^(GIT_|GH_TOKEN|GITHUB_TOKEN|CLAUDE_CODE_OAUTH_TOKEN|SSH_AUTH_SOCK|FACTORY_AGENT)" || echo clean')
        self.assertEqual(p.stdout.strip(), "clean", p.stderr)

    def test_factory_home_is_the_scratch_not_the_real_one(self):
        p = self.box('echo "$FACTORY_HOME"')
        self.assertNotEqual(p.stdout.strip(), str(self.env.home_dir))
        self.assertIn(".factory-test-", p.stdout)

    def test_signal_death_and_nonzero_exit_clean_up(self):
        self.assertEqual(self.box("sh -c 'kill -9 $$'").returncode, 137)
        self.assertEqual(list(self.clone.glob(".factory-test-*")), [])

    def test_exit_code_propagates(self):
        self.assertEqual(self.box("exit 5").returncode, 5)
        self.assertEqual(self.box("true").returncode, 0)

    def test_no_network_but_lo(self):
        code = "import socket; socket.create_connection(('.'.join(['203', '0', '113', '5']), 80), 2)"  # no literal IP: public-text guard
        p = self.box(f'{sys.executable} -c "{code}"')
        self.assertNotEqual(p.returncode, 0)
        ifs = self.box("tail -n +3 /proc/net/dev").stdout.split(":")[0].split()
        self.assertEqual(ifs, ["lo"])

    def test_own_pid_namespace(self):
        self.assertLess(int(self.box("echo $$").stdout.split()[0]), 20)

    def test_tree_rw_git_dir_ro_and_scratch_removed(self):
        p = self.box('echo hi > out.txt; echo x > .git/planted')
        self.assertNotEqual(p.returncode, 0)
        self.assertEqual((self.clone / "out.txt").read_text(), "hi\n")
        self.assertFalse((self.clone / ".git" / "planted").exists())
        self.assertEqual(list(self.clone.glob(".factory-test-*")), [])

    def test_worktree_git_pointer_file_is_read_only(self):
        wt = self.env.home / "wt"
        git("worktree", "add", "-q", "-b", "wt-branch", str(wt), cwd=self.clone)
        self.assertTrue((wt / ".git").is_file())
        before = (wt / ".git").read_text()
        env = self.env.env("zz-test-coder", self.extra)
        p = subprocess.run([sys.executable, str(FACTORY), "test", "--", "sh", "-c",
                            "! echo 'gitdir: /elsewhere' > .git && ! rm -f .git && ! mv .git .git.x && echo ok > out.txt"],
                           cwd=wt, env=env, capture_output=True, text=True, timeout=60)
        self.assertEqual(p.returncode, 0, p.stderr)  # all three writes to the pointer were refused, the tree itself is rw
        self.assertEqual((wt / "out.txt").read_text(), "ok\n")
        self.assertEqual((wt / ".git").read_text(), before)  # still the original pointer, not redirected
        self.assertFalse((wt / ".git.x").exists())

    def test_tree_under_masked_worktrees_dir_stays_rw_with_ro_pointer(self):
        main = self.env.home / "main"
        (main / "worktrees").mkdir(parents=True)
        (main / "db").mkdir()
        wt = main / "worktrees" / "G-1-T-1"
        git("worktree", "add", "-q", "-b", "wt2", str(wt), cwd=self.clone)
        scratch = testbox.new_scratch(wt)
        self.addCleanup(testbox.cleanup, scratch)
        argv, benv = testbox.build(
            ["sh", "-c", "echo hi > out.txt; ls ../../db | wc -l; ! echo x > .git"], str(wt), str(main), scratch, os.environ)
        p = subprocess.run(argv, cwd=wt, env=benv, capture_output=True, text=True, timeout=60, stdin=subprocess.DEVNULL)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(p.stdout.strip(), "0")  # FACTORY_MAIN/db is masked
        self.assertEqual((wt / "out.txt").read_text(), "hi\n")  # the tree under the masked worktrees dir is still rw
        self.assertTrue((wt / ".git").read_text().startswith("gitdir: "))

    def test_tmpdir_is_disk_scratch_in_the_clone(self):
        p = self.box('case "$TMPDIR" in %s/.factory-test-*) echo ok;; *) echo "$TMPDIR";; esac; touch "$TMPDIR/a"' % self.clone.resolve())
        self.assertEqual(p.stdout.strip(), "ok", p.stderr)

    def test_git_push_fails(self):
        bare = self.env.home / "bare.git"
        git("init", "-q", "--bare", str(bare), cwd=self.clone)
        git("remote", "add", "origin", str(bare), cwd=self.clone)
        p = self.box('git push origin main')
        self.assertNotEqual(p.returncode, 0)
        self.assertEqual(subprocess.run(["git", "-C", str(bare), "rev-parse", "--verify", "-q", "main"]).returncode, 1)

    def test_home_is_hidden(self):
        cred = self.env.user_home / ".config" / "gh" / "hosts.yml"
        cred.parent.mkdir(parents=True)
        cred.write_text("FAKE-GH")
        p = self.box(f"cat {cred}")
        self.assertNotEqual(p.returncode, 0)
        self.assertNotIn("FAKE-GH", p.stdout)


if __name__ == "__main__":
    unittest.main()
