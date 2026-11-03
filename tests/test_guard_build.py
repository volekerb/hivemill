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

class GuardBuildHardeningTests(GuardBuildTests.__bases__[0]):
    """Attempt 2: non-live home, symlinked temp, tick budget/order/backoff, stamp order (reviewer B1-B4)."""

    def setUp(self):
        super().setUp()
        self.tmp = Path(tempfile.mkdtemp(prefix="zz-guard-hard-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.repo, self.lib = self.tmp / "repo", self.tmp / "lib"
        self.repo.mkdir()
        git("init", "-q", "-b", "main", cwd=self.repo)
        self.fakebin = self.tmp / "fakebin"
        self.fakebin.mkdir()
        for tool in ("git", "nice", "sh", "sleep", "cat", "echo"):
            if shutil.which(tool):
                (self.fakebin / tool).symlink_to(shutil.which(tool))
        self.rec, self.pidf, self.calls = self.tmp / "rec", self.tmp / "pid", self.tmp / "calls"

    def fake_go(self, body):
        """A `go` that logs each call; body is shell run for `go build -o <out>`."""
        script = self.fakebin / "go"
        script.write_text('#!/bin/sh\necho call >> "$FAKE_CALLS"\nout=""\nwhile [ $# -gt 0 ]; do [ "$1" = -o ] && out="$2"; shift; done\n' + body + "\n")
        script.chmod(0o755)

    @contextlib.contextmanager
    def factory(self, home=None):
        env = {"PATH": str(self.fakebin), "HOME": str(home or self.tmp / "home"), "FAKE_CALLS": str(self.calls),
               "FAKE_REC": str(self.rec), "FAKE_PID": str(self.pidf), "FAKE_LIB": str(self.lib), "FAKE_RUN": str(self.env.home_dir / "run")}
        with self.env.load_factory() as mod, mock.patch.dict(os.environ, env), mock.patch.object(mod, "load1", return_value=0.0):
            self.err = io.StringIO()
            with contextlib.redirect_stderr(self.err):
                yield mod

    def ncalls(self):
        return len(self.calls.read_text().split()) if self.calls.exists() else 0

    def alive(self, pid):
        try:
            return Path(f"/proc/{pid}/stat").read_text().rsplit(")", 1)[1].split()[0] != "Z"
        except OSError:
            return False

    OK_BUILD = 'echo bin > "$out"'

    # B1 / acceptance 9
    def test_non_live_home_never_builds_into_default_lib(self):
        write_go(self.env.home_dir)
        self.fake_go(self.OK_BUILD)
        home = self.tmp / "home"
        home.mkdir()
        with self.factory(home) as f:
            self.assertFalse(f.home_is_live())
            self.assertEqual(f.build_guard_go(), "skipped")
            f.guard_tick(f.db())
        self.assertFalse((home / ".local").exists())
        self.assertEqual(self.ncalls(), 0)
        self.assertRegex((self.env.home_dir / "run" / "guard_impl").read_text(), r"^python [0-9a-f]{40}\n$")

    # B2 / acceptance 10
    def test_symlink_at_fixed_temp_name_is_not_followed(self):
        h = write_go(self.env.home_dir)
        run = self.env.home_dir / "run"
        run.mkdir(exist_ok=True)
        sentinel = self.tmp / "sentinel"
        sentinel.write_text("keep")
        sentinel.chmod(0o600)
        (run / "guard_impl.new").symlink_to(sentinel)
        with self.factory() as f:
            f.write_guard_impl(f.db())
        self.assertEqual((sentinel.read_text(), oct(sentinel.stat().st_mode & 0o777)), ("keep", "0o600"))
        flag = run / "guard_impl"
        self.assertFalse(flag.is_symlink())
        self.assertEqual((flag.read_text(), oct(flag.stat().st_mode & 0o777)), (f"python {h}\n", "0o644"))
        self.assertEqual(sorted(p.name for p in run.iterdir() if p.name.startswith("guard_impl")), ["guard_impl", "guard_impl.new"])

    # B3 budget / acceptance 11
    def test_tick_budget_kills_the_process_group_and_marks_failure(self):
        h = write_go(self.repo)
        self.fake_go('sleep 300 &\necho $! > "$FAKE_PID"\nsleep 300')
        with self.factory() as f:
            self.assertLess(f.GUARD_TICK_BUILD_TIMEOUT_S, 240)
            self.assertEqual(f.GUARD_BUILD_TIMEOUT_S, 300)
            with mock.patch.object(f, "GUARD_TICK_BUILD_TIMEOUT_S", 1):
                t = time.monotonic()
                self.assertEqual(f.build_guard_go(self.repo, self.lib, tick=True), "failed")
                self.assertLess(time.monotonic() - t, 20)
        child = int(self.pidf.read_text())
        for _ in range(50):
            if not self.alive(child):
                break
            time.sleep(0.1)
        self.assertFalse(self.alive(child))
        self.assertFalse((self.lib / "worker-guard.stamp").exists())
        self.assertEqual((self.lib / "worker-guard.failed").read_text().strip(), h)

    # B3 order / acceptance 12
    def test_flag_holds_new_srchash_during_the_build(self):
        write_go(self.env.home_dir)
        self.fake_go('cat "$FAKE_RUN/guard_impl" > "$FAKE_REC"\n' + self.OK_BUILD)
        with self.factory() as f:
            h2 = write_go(self.env.home_dir, MAIN_OK + "\n// next\n")
            f.guard_tick(f.db(), src=self.env.home_dir, lib=self.lib)
        self.assertEqual(self.rec.read_text(), f"python {h2}\n")
        self.assertEqual((self.lib / "worker-guard.stamp").read_text().strip(), h2)

    # B3 backoff / acceptance 13
    def test_failed_build_backs_off_on_the_tick_only(self):
        h = write_go(self.repo)
        self.fake_go("exit 1")
        marker = self.lib / "worker-guard.failed"
        with self.factory() as f:
            self.assertEqual(f.build_guard_go(self.repo, self.lib, tick=True), "failed")
            self.assertEqual(self.ncalls(), 1)
            self.assertEqual(f.build_guard_go(self.repo, self.lib, tick=True), "skipped")
            self.assertEqual(self.ncalls(), 1)
            old = time.time() - 31 * 60
            os.utime(marker, (old, old))
            self.assertEqual(f.build_guard_go(self.repo, self.lib, tick=True), "failed")  # marker aged out
            self.assertEqual(self.ncalls(), 2)
            self.assertEqual(f.build_guard_go(self.repo, self.lib, tick=True), "skipped")
            self.assertEqual(f.build_guard_go(self.repo, self.lib), "failed")  # `factory up` ignores it
            self.assertEqual(self.ncalls(), 3)
            write_go(self.repo, MAIN_OK + "\n// new\n")
            self.assertEqual(f.build_guard_go(self.repo, self.lib, tick=True), "failed")  # new srchash builds at once
            self.assertEqual(self.ncalls(), 4)
            self.fake_go(self.OK_BUILD)
            self.assertEqual(f.build_guard_go(self.repo, self.lib), "built")
        self.assertFalse(marker.exists())
        self.assertNotEqual(h, (self.lib / "worker-guard.stamp").read_text().strip())

    # D6 / acceptance 14
    def test_no_go_line_on_up_path_only(self):
        write_go(self.repo)
        with self.factory() as f:
            self.assertEqual(f.build_guard_go(self.repo, self.lib, tick=True), "skipped")
            self.assertNotIn("no go toolchain", self.err.getvalue())
            self.assertEqual(f.build_guard_go(self.repo, self.lib), "skipped")
            self.assertIn("guard go build: skipped (no go toolchain)", self.err.getvalue())

    # B4 / acceptance 15 (the C4 mutant: stamp written before the build must fail this)
    def test_stamp_is_old_or_absent_during_the_build(self):
        old = write_go(self.repo)
        self.fake_go(self.OK_BUILD)
        with self.factory() as f:
            self.assertEqual(f.build_guard_go(self.repo, self.lib), "built")
            new = write_go(self.repo, MAIN_OK + "\n// v2\n")
            self.fake_go('cat "$FAKE_LIB/worker-guard.stamp" > "$FAKE_REC"\n' + self.OK_BUILD)
            self.assertEqual(f.build_guard_go(self.repo, self.lib), "built")
        self.assertEqual(self.rec.read_text().strip(), old)
        self.assertEqual((self.lib / "worker-guard.stamp").read_text().strip(), new)

    def test_failed_binary_rename_never_stamps_the_new_hash(self):
        write_go(self.repo)
        self.fake_go(self.OK_BUILD)
        with self.factory() as f:
            real = os.replace

            def replace(a, b, *args, **kw):
                if str(b).endswith("worker-guard"):
                    raise OSError("disk full")
                return real(a, b, *args, **kw)
            with mock.patch.object(os, "replace", replace):
                self.assertEqual(f.build_guard_go(self.repo, self.lib), "failed")
        self.assertFalse((self.lib / "worker-guard.stamp").exists())
        self.assertFalse((self.lib / "worker-guard").exists())


if __name__ == "__main__":
    unittest.main()
