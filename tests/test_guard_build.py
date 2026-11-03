"""G-016 T-0354: factory builds the Go worker-guard with a stamp, and run/guard_impl follows the DB guard_impl key."""
import contextlib
import io
import os
import shutil
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock

from harness import FactoryTestCase, git

REAL_GO = shutil.which("go") or (str(Path("~/.local/go/bin/go").expanduser()) if Path("~/.local/go/bin/go").expanduser().exists() else None)
MAIN_OK = "package main\n\nfunc main() {}\n"
MAIN_BAD = "package main\n\nfunc main() { this does not compile }\n"
GO_MOD = "module guardtest\n\ngo 1.20\n"


def write_go(repo, main=MAIN_OK):
    d = Path(repo) / "go" / "cmd" / "worker-guard"
    d.mkdir(parents=True, exist_ok=True)
    (d / "main.go").write_text(main)
    (Path(repo) / "go" / "go.mod").write_text(GO_MOD)
    git("add", "-A", cwd=repo)
    git("commit", "-q", "-m", "go", cwd=repo)
    return git("rev-parse", "HEAD:go", cwd=repo)


class GuardBuildTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.tmp = Path(tempfile.mkdtemp(prefix="zz-guard-build-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.repo, self.lib = self.tmp / "repo", self.tmp / "lib"
        self.repo.mkdir()
        git("init", "-q", "-b", "main", cwd=self.repo)

    def git_dir(self):
        """A dir holding only git (a symlink), so a PATH can have git and no go."""
        d = self.tmp / "gitbin"
        if not d.exists():
            d.mkdir()
            for tool in ("git", "nice"):
                (d / tool).symlink_to(shutil.which(tool))
        return d

    @contextlib.contextmanager
    def factory(self, path=None):
        """bin/factory in-process; PATH holds the real go dir (or `path`); stderr captured in self.err."""
        if path is None:  # git and go only: the real PATH may hold other tools
            path = os.pathsep.join(filter(None, [str(Path(REAL_GO).parent) if REAL_GO else "", str(self.git_dir())]))
        with self.env.load_factory() as mod, mock.patch.dict(os.environ, {"PATH": path}):
            self.err = io.StringIO()
            with contextlib.redirect_stderr(self.err):
                yield mod

    @unittest.skipUnless(REAL_GO, "no go toolchain")
    def test_build_creates_binary_and_stamp_then_is_a_noop(self):
        h = write_go(self.repo)
        with self.factory() as f:
            self.assertEqual(f.build_guard_go(self.repo, self.lib), "built")
            self.assertEqual((self.lib / "worker-guard.stamp").read_text().strip(), h)
            self.assertTrue(os.access(self.lib / "worker-guard", os.X_OK))
            before = (self.lib / "worker-guard").stat().st_mtime_ns, (self.lib / "worker-guard.stamp").stat().st_mtime_ns
            t = time.perf_counter()
            self.assertEqual(f.build_guard_go(self.repo, self.lib), "current")
            took = time.perf_counter() - t
            after = (self.lib / "worker-guard").stat().st_mtime_ns, (self.lib / "worker-guard.stamp").stat().st_mtime_ns
        self.assertEqual(before, after)
        self.assertLess(took, 0.05)  # acceptance 5: a stamped tick costs one git call
        self.assertEqual(sorted(p.name for p in self.lib.iterdir()), [".build.lock", "worker-guard", "worker-guard.stamp"])

    @unittest.skipUnless(REAL_GO, "no go toolchain")
    def test_changed_go_tree_rebuilds_and_restamps(self):
        write_go(self.repo)
        with self.factory() as f:
            f.build_guard_go(self.repo, self.lib)
            h2 = write_go(self.repo, MAIN_OK + "\n// changed\n")
            self.assertEqual(f.guard_binary_state(self.repo, self.lib), "stale")
            self.assertEqual(f.build_guard_go(self.repo, self.lib), "built")
            self.assertEqual(f.guard_binary_state(self.repo, self.lib), "ok")
        self.assertEqual((self.lib / "worker-guard.stamp").read_text().strip(), h2)

    def test_no_go_toolchain_skips_without_stamp(self):
        write_go(self.repo)
        with self.factory(path=str(self.git_dir())) as f, mock.patch.dict(os.environ, {"HOME": str(self.tmp / "nohome")}):
            self.assertEqual(f.build_guard_go(self.repo, self.lib), "skipped")
        self.assertIn("guard go build: skipped (no go toolchain)", self.err.getvalue())
        self.assertFalse((self.lib / "worker-guard.stamp").exists())
        self.assertFalse((self.lib / "worker-guard").exists())

    @unittest.skipUnless(REAL_GO, "no go toolchain")
    def test_failed_build_removes_stamp_and_keeps_old_binary(self):
        write_go(self.repo)
        with self.factory() as f:
            f.build_guard_go(self.repo, self.lib)
            old = (self.lib / "worker-guard").read_bytes()
            write_go(self.repo, MAIN_BAD)
            self.assertEqual(f.build_guard_go(self.repo, self.lib), "failed")
            self.assertEqual(f.guard_binary_state(self.repo, self.lib), "missing")
        self.assertFalse((self.lib / "worker-guard.stamp").exists())
        self.assertFalse((self.lib / "worker-guard.new").exists())
        self.assertEqual((self.lib / "worker-guard").read_bytes(), old)
        self.assertIn("guard go build: failed", self.err.getvalue())

    @unittest.skipUnless(REAL_GO, "no go toolchain")
    def test_failed_first_build_leaves_no_binary_and_no_stamp(self):
        write_go(self.repo, MAIN_BAD)
        with self.factory() as f:
            self.assertEqual(f.build_guard_go(self.repo, self.lib), "failed")
        self.assertFalse((self.lib / "worker-guard.stamp").exists())
        self.assertFalse((self.lib / "worker-guard").exists())

    def test_no_go_tree_skips(self):
        (self.repo / "README").write_text("x")
        git("add", "-A", cwd=self.repo)
        git("commit", "-q", "-m", "x", cwd=self.repo)
        with self.factory() as f:
            self.assertEqual(f.build_guard_go(self.repo, self.lib), "skipped")
        self.assertFalse(self.lib.exists())

    # -- guard_impl config and run/guard_impl -----------------------------
    def commit_go(self):
        return write_go(self.env.home_dir)

    def flag(self):
        return (self.env.home_dir / "run" / "guard_impl")

    def test_director_sets_guard_impl_and_file_follows(self):
        h = self.commit_go()
        self.env.seed_agent("dir-1", "director")
        p = self.env.run("config", "guard_impl", "go", agent="dir-1")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(self.flag().read_text(), f"go {h}\n")
        self.assertEqual(oct(self.flag().stat().st_mode & 0o777), "0o644")
        self.assertIn("warning: guard binary is missing", p.stderr)  # still set; the shim uses python
        out = self.env.run("config", agent="dir-1").stdout.splitlines()
        self.assertIn("guard_impl = go", out)
        self.assertTrue(out[-1].startswith("guard binary: "), out[-1])

    def test_coder_is_refused_and_file_unchanged(self):
        h = self.commit_go()
        self.env.seed_agent("dir-1", "director")
        self.env.run("config", "guard_impl", "shadow", agent="dir-1")
        self.env.seed_agent("cod-1", "coder")
        p = self.env.run("config", "guard_impl", "python", agent="cod-1")
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn("may not do this", p.stderr)
        self.assertEqual(self.flag().read_text(), f"shadow {h}\n")
        self.assertEqual(self.env.run("config", "guard_impl", agent="cod-1").stdout.strip(), "shadow")

    def test_unknown_impl_is_exit_2(self):
        self.commit_go()
        self.env.seed_agent("dir-1", "director")
        p = self.env.run("config", "guard_impl", "rust", agent="dir-1")
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertIn("python, go, shadow", p.stderr)
        self.assertFalse(self.flag().exists())

    def test_default_is_python(self):
        self.assertEqual(self.env.run("config", "guard_impl").stdout.strip(), "python")

    def test_no_go_tree_writes_python_dash(self):
        self.env.seed_agent("dir-1", "director")
        self.assertEqual(self.env.run("config", "guard_impl", "go", agent="dir-1").returncode, 0)
        self.assertEqual(self.flag().read_text(), "python -\n")

    def test_write_guard_impl_restores_file_from_db(self):  # acceptance 8: the tick step
        h = self.commit_go()
        self.env.seed_agent("dir-1", "director")
        self.env.run("config", "guard_impl", "shadow", agent="dir-1")
        self.flag().write_text("go x\n")
        with self.factory() as f:
            line = f.write_guard_impl(f.db())
        self.assertEqual(line, f"shadow {h}")
        self.assertEqual(self.flag().read_text(), f"shadow {h}\n")

    def test_tick_rewrites_the_file(self):
        h = self.commit_go()
        (self.env.home_dir / "run").mkdir(exist_ok=True)
        self.flag().write_text("go x\n")
        p = self.env.run("tick", extra_env={"FACTORY_LOADAVG": "999"})  # a loaded box skips the build
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(self.flag().read_text(), f"python {h}\n")


if __name__ == "__main__":
    unittest.main()
