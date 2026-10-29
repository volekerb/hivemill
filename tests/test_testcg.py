"""T-0681: per-run cgroup caps for `factory test` (scout/testcg.py, config test_cgroup).
Real `factory test` as a subprocess in a temp FACTORY_HOME and a temp git clone. Limit-file tests use a plain temp dir as the
delegated parent; the enforcement test builds a real delegated cgroup under the user's service manager and is skipped with the
reason where that is not possible."""
import os
import subprocess
import sys
import unittest
import uuid
from pathlib import Path

from harness import FACTORY, REPO, FactoryTestCase, git

sys.path.insert(0, str(REPO))
from scout import testcg  # noqa: E402

USER_CG = Path(f"/sys/fs/cgroup/user.slice/user-{os.getuid()}.slice/user@{os.getuid()}.service")


class Base(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.clone = self.env.home / "clone"
        self.clone.mkdir()
        git("init", "-q", "-b", "main", cwd=self.clone)
        (self.clone / "f.txt").write_text("x\n")
        git("add", ".", cwd=self.clone)
        git("commit", "-qm", "init", cwd=self.clone)
        self.env.seed_agent("zz-test-director", "director")

    def cfg(self, k, v):
        self.ok("config", k, v, agent="zz-test-director")

    def ftest(self, *cmd):
        return subprocess.run([sys.executable, str(FACTORY), "test", *cmd], cwd=self.clone, env=self.env.env("zz-test-coder"),
                              capture_output=True, text=True, timeout=60)

    def fake_parent(self, controllers="cpu memory pids"):
        p = self.env.home / "cgparent"
        p.mkdir(exist_ok=True)
        (p / "cgroup.controllers").write_text(controllers + "\n")  # what Delegate= makes available
        (p / "cgroup.subtree_control").write_text("")  # systemd leaves it empty; check_parent must enable them
        self.cfg("test_cgroup", "1")
        self.cfg("test_cgroup_parent", str(p))
        return p


class FailClosed(Base):
    def test_flag_off_by_default_runs_directly(self):
        p = self.ftest("--", "sh", "-c", "exit 7")
        self.assertEqual(p.returncode, 7, p.stderr)

    def test_bad_flag_value_is_error_and_not_run(self):
        self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('test_cgroup','yes')")
        p = self.ftest("touch", "ran")
        self.assertEqual(p.returncode, 3)
        self.assertIn("factory config test_cgroup 0", p.stderr)
        self.assertFalse((self.clone / "ran").exists())

    def test_missing_parent_with_flag_on_does_not_run(self):
        for sb in ("0", "1"):
            self.cfg("test_sandbox", sb)
            self.cfg("test_cgroup", "1")
            self.cfg("test_cgroup_parent", str(self.env.home / "nope"))
            p = self.ftest("touch", "ran")
            self.assertEqual(p.returncode, 3, p.stderr)
            self.assertIn("is missing", p.stderr)
            self.assertIn("factory config test_cgroup 0", p.stderr)
            self.assertFalse((self.clone / "ran").exists(), f"ran uncapped with test_sandbox={sb}")

    def test_default_parent_missing_on_a_host_without_the_unit(self):
        self.cfg("test_cgroup", "1")
        if Path(testcg.default_parent(os.getuid())).is_dir():
            self.skipTest("the factory-tests unit is installed on this host")
        p = self.ftest("touch", "ran")
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertFalse((self.clone / "ran").exists())

    def test_parent_without_delegated_controllers_does_not_run(self):
        self.fake_parent("cpu")
        p = self.ftest("touch", "ran")
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn("does not delegate memory pids", p.stderr)
        self.assertFalse((self.clone / "ran").exists())

    def test_bad_limit_values_do_not_run(self):
        self.fake_parent()
        for k, v in (("test_cpu_quota", "0"), ("test_mem_max", "lots"), ("test_tasks_max", "-1")):
            self.cfg(k, v)
            p = self.ftest("touch", "ran")
            self.assertEqual(p.returncode, 3, f"{k}={v}: {p.stderr}")
            self.assertIn(k, p.stderr)
            self.assertFalse((self.clone / "ran").exists())
            self.cfg(k, {"test_cpu_quota": "400", "test_mem_max": "3G", "test_tasks_max": "512"}[k])


class Limits(Base):
    def test_child_cgroup_gets_caps_and_the_run_is_moved_in(self):
        parent = self.fake_parent()
        self.cfg("test_cpu_quota", "250")
        self.cfg("test_mem_max", "2g")
        self.cfg("test_tasks_max", "99")
        p = self.ftest("--", "sh", "-c", "exit 5")
        self.assertEqual(p.returncode, 5, p.stderr)  # the command's exit code
        kids = [d for d in parent.iterdir() if d.is_dir()]
        self.assertEqual(len(kids), 1, kids)  # one child per run (plain dir here: rmdir cannot drop its files)
        k = kids[0]
        self.assertEqual((k / "cpu.max").read_text(), "250000 100000")
        self.assertEqual((k / "memory.max").read_text(), "2G")
        self.assertEqual((k / "memory.swap.max").read_text(), "0")
        self.assertEqual((k / "pids.max").read_text(), "99")
        self.assertEqual((parent / "cgroup.subtree_control").read_text(), "+cpu +memory +pids")  # enabled by check_parent
        self.assertEqual((k / "cgroup.procs").read_text(), "0")  # the run moved itself in before exec

    def test_defaults_are_400_percent_3g_512(self):
        parent = self.fake_parent()
        self.assertEqual(self.ftest("true").returncode, 0)
        k = next(d for d in parent.iterdir() if d.is_dir())
        self.assertEqual((k / "cpu.max").read_text(), "400000 100000")
        self.assertEqual((k / "memory.max").read_text(), "3G")
        self.assertEqual((k / "pids.max").read_text(), "512")

    def test_works_with_the_sandbox_on(self):
        from scout import nsbox
        ok, why = nsbox.userns_available()
        if not ok or os.environ.get("FACTORY_GATEBOX") == "1":
            self.skipTest(f"user namespaces unavailable: {why}")
        parent = self.fake_parent()
        self.cfg("test_sandbox", "1")
        p = self.ftest("sh", "-c", "exit 4")
        self.assertEqual(p.returncode, 4, p.stderr)
        self.assertEqual(len([d for d in parent.iterdir() if d.is_dir()]), 1)


def delegated_parent():
    """A fresh real cgroup below the user manager with cpu memory pids delegated, or (None, reason)."""
    d = USER_CG / f"t0681-{uuid.uuid4().hex[:8]}"
    try:
        d.mkdir()
    except OSError as e:
        return None, f"cannot create a cgroup under {USER_CG}: {e.strerror}"
    # Like systemd's Delegate=: controllers are available but subtree_control is left empty for factory test to fill.
    avail = (d / "cgroup.controllers").read_text().split()
    if not all(c in avail for c in testcg.CONTROLLERS):
        d.rmdir()
        return None, f"cpu memory pids not available in {d} (have: {' '.join(avail)})"
    return d, ""


class Enforced(Base):
    def test_memory_max_is_enforced(self):
        parent, why = delegated_parent()
        if parent is None:
            self.skipTest(why)
        self.addCleanup(self.rmcg, parent)
        self.cfg("test_cgroup", "1")
        self.cfg("test_cgroup_parent", str(parent))
        self.cfg("test_mem_max", "64M")
        self.assertEqual((parent / "cgroup.subtree_control").read_text().strip(), "")  # nothing pre-enabled
        ok = self.ftest(sys.executable, "-c", "x = bytearray(16 * 1024 * 1024); print('small ok')")
        if ok.returncode == 127 and "cannot move the run into" in ok.stderr and "errno 13" in ok.stderr:
            # e.g. a CI runner whose own cgroup is outside the user manager: the kernel refuses the move; factory test failed closed.
            self.assertNotIn("small ok", ok.stdout)
            self.skipTest(f"this process may not move into cgroups below the user manager: {ok.stderr.strip()}")
        self.assertEqual((ok.returncode, ok.stdout.strip()), (0, "small ok"), ok.stderr)
        big = self.ftest(sys.executable, "-c", "x = bytearray(400 * 1024 * 1024); print('survived')")
        self.assertEqual(big.returncode, 137, (big.stdout, big.stderr))  # SIGKILL from the cgroup OOM killer
        self.assertNotIn("survived", big.stdout)
        self.assertEqual([d for d in parent.iterdir() if d.is_dir()], [], "child cgroups are removed after the run")

    @staticmethod
    def rmcg(parent):
        import time
        for _ in range(50):
            try:
                parent.rmdir()
                return
            except OSError:
                time.sleep(0.1)


class Unit(unittest.TestCase):
    def test_limits_text(self):
        self.assertEqual(testcg.limits("400", "3G", "512")["cpu.max"], "400000 100000")
        self.assertEqual(testcg.limits("50", "512m", "8")["memory.max"], "512M")
        with self.assertRaises(testcg.CgroupError):
            testcg.limits("1.5", "3G", "8")

    def test_default_parent_is_the_unit_cgroup(self):
        self.assertEqual(testcg.default_parent(1000), "/sys/fs/cgroup/user.slice/user-1000.slice/user" "@1000.service/"
                         "factory-tests.slice/factory-tests.service")


if __name__ == "__main__":
    unittest.main()
