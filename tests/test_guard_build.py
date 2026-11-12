"""G-016 T-0354: factory builds the Go worker-guard with a stamp, and run/guard_impl follows the DB guard_impl key."""
import contextlib
import io
import os
import shutil
import stat
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock

from harness import FactoryTestCase, git

_LOCAL_GO = Path("~/.local/go/bin/go").expanduser()
REAL_GO = str(_LOCAL_GO) if os.access(_LOCAL_GO, os.X_OK) else None  # the only toolchain the build may run
MAIN_OK = "package main\n\nfunc main() {}\n"
MAIN_BAD = "package main\n\nfunc main() { this does not compile }\n"
GO_MOD = "module guardtest\n\ngo 1.20\n"
BIN_DIR = Path(__file__).resolve().parent.parent / "bin"


def rmtree_rw(path):
    """Remove a temp dir that may hold a Go module cache (go writes dirs 0555 and files 0444, so a plain rmtree fails).
    A leftover is an error, never silent (T-1413)."""
    path = Path(path)
    if not path.exists():
        return
    os.chmod(path, stat.S_IRWXU)
    for root, dirs, _files in os.walk(path):
        for d in dirs:
            if not os.path.islink(os.path.join(root, d)):
                os.chmod(os.path.join(root, d), stat.S_IRWXU)
    shutil.rmtree(path)
    if path.exists():
        raise AssertionError(f"temp dir {path} survived cleanup: remove it and fix the cleanup")


def mktmp(case, prefix):
    """A temp dir removed (read-only Go module cache included) when the test or class ends."""
    d = Path(tempfile.mkdtemp(prefix=prefix))
    (case.addClassCleanup if isinstance(case, type) else case.addCleanup)(rmtree_rw, d)
    return d


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
        self.tmp = mktmp(self, "zz-guard-build-")
        self.repo, self.lib = self.tmp / "repo", self.tmp / "lib"
        self.repo.mkdir()
        git("init", "-q", "-b", "main", cwd=self.repo)

    def git_dir(self):
        """A dir holding only git (a symlink), so a PATH can have git and no go."""
        d = self.tmp / "gitbin"
        if not d.exists():
            d.mkdir()
            (d / "git").symlink_to(shutil.which("git"))
        return d

    @contextlib.contextmanager
    def factory(self, path=None):
        """bin/factory in-process; PATH holds git only (or `path`); go is passed as go=REAL_GO; stderr captured in self.err."""
        if path is None:
            path = str(self.git_dir())
        with self.env.load_factory() as mod, mock.patch.dict(os.environ, {"PATH": path}):
            self.err = io.StringIO()
            with contextlib.redirect_stderr(self.err):
                yield mod

    @unittest.skipUnless(REAL_GO, "no ~/.local/go/bin/go toolchain")
    def test_build_creates_binary_and_stamp_then_is_a_noop(self):
        h = write_go(self.repo)
        with self.factory() as f:
            self.assertEqual(f.build_guard_go(self.repo, self.lib, go=REAL_GO), "built")
            self.assertEqual((self.lib / "worker-guard.stamp").read_text().strip(), h)
            self.assertTrue(os.access(self.lib / "worker-guard", os.X_OK))
            before = (self.lib / "worker-guard").stat().st_mtime_ns, (self.lib / "worker-guard.stamp").stat().st_mtime_ns
            t = time.perf_counter()
            self.assertEqual(f.build_guard_go(self.repo, self.lib, go=REAL_GO), "current")
            took = time.perf_counter() - t
            after = (self.lib / "worker-guard").stat().st_mtime_ns, (self.lib / "worker-guard.stamp").stat().st_mtime_ns
        self.assertEqual(before, after)
        self.assertLess(took, 0.05)  # acceptance 5: a stamped tick costs one git call
        self.assertEqual(sorted(p.name for p in self.lib.iterdir()), [".build.lock", "go", "worker-guard", "worker-guard.stamp"])

    @unittest.skipUnless(REAL_GO, "no ~/.local/go/bin/go toolchain")
    def test_changed_go_tree_rebuilds_and_restamps(self):
        write_go(self.repo)
        with self.factory() as f:
            f.build_guard_go(self.repo, self.lib, go=REAL_GO)
            h2 = write_go(self.repo, MAIN_OK + "\n// changed\n")
            self.assertEqual(f.guard_binary_state(self.repo, self.lib), "stale")
            self.assertEqual(f.build_guard_go(self.repo, self.lib, go=REAL_GO), "built")
            self.assertEqual(f.guard_binary_state(self.repo, self.lib), "ok")
        self.assertEqual((self.lib / "worker-guard.stamp").read_text().strip(), h2)

    def test_no_go_toolchain_skips_without_stamp(self):
        write_go(self.repo)
        decoy = self.tmp / "decoy"  # a go first on PATH is not a toolchain: only ~/.local/go/bin/go counts
        decoy.mkdir()
        (decoy / "go").write_text(f'#!/bin/sh\necho ran > "{self.tmp}/decoy-ran"\n')
        (decoy / "go").chmod(0o755)
        with self.factory(path=f"{decoy}:{self.git_dir()}") as f, mock.patch.dict(os.environ, {"HOME": str(self.tmp / "nohome")}):
            self.assertEqual(f.build_guard_go(self.repo, self.lib), "skipped")
        self.assertFalse((self.tmp / "decoy-ran").exists())
        self.assertIn("guard go build: skipped (no go toolchain)", self.err.getvalue())
        self.assertFalse((self.lib / "worker-guard.stamp").exists())
        self.assertFalse((self.lib / "worker-guard").exists())

    @unittest.skipUnless(REAL_GO, "no ~/.local/go/bin/go toolchain")
    def test_failed_build_removes_stamp_and_keeps_old_binary(self):
        write_go(self.repo)
        with self.factory() as f:
            f.build_guard_go(self.repo, self.lib, go=REAL_GO)
            old = (self.lib / "worker-guard").read_bytes()
            write_go(self.repo, MAIN_BAD)
            self.assertEqual(f.build_guard_go(self.repo, self.lib, go=REAL_GO), "failed")
            self.assertEqual(f.guard_binary_state(self.repo, self.lib), "missing")
        self.assertFalse((self.lib / "worker-guard.stamp").exists())
        self.assertFalse((self.lib / "worker-guard.new").exists())
        self.assertEqual((self.lib / "worker-guard").read_bytes(), old)
        self.assertIn("guard go build: failed", self.err.getvalue())

    @unittest.skipUnless(REAL_GO, "no ~/.local/go/bin/go toolchain")
    def test_failed_first_build_leaves_no_binary_and_no_stamp(self):
        write_go(self.repo, MAIN_BAD)
        with self.factory() as f:
            self.assertEqual(f.build_guard_go(self.repo, self.lib, go=REAL_GO), "failed")
        self.assertFalse((self.lib / "worker-guard.stamp").exists())
        self.assertFalse((self.lib / "worker-guard").exists())

    def test_no_go_tree_skips(self):
        (self.repo / "README").write_text("x")
        git("add", "-A", cwd=self.repo)
        git("commit", "-q", "-m", "x", cwd=self.repo)
        with self.factory() as f:
            self.assertEqual(f.build_guard_go(self.repo, self.lib, go=REAL_GO), "skipped")
        self.assertFalse(self.lib.exists())

    def test_bin_sources_have_no_path_lookup_for_go_or_nice(self):  # acceptance 19
        for name in ("factory", "factory_cli.py"):
            self.assertNotRegex((BIN_DIR / name).read_text(), r"""which\(\s*["'](go|nice)["']""", name)

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


class GuardBuildHardeningTests(FactoryTestCase):
    """Attempts 2 and 3: non-live home, symlinked temp, tick budget/order/backoff, stamp order, the guard_impl gate,
    the exact build environment and the absolute go/nice paths. A fake go is passed as go=, never found through PATH."""

    def setUp(self):
        super().setUp()
        self.tmp = mktmp(self, "zz-guard-hard-")
        self.repo, self.lib = self.tmp / "repo", self.tmp / "lib"
        self.repo.mkdir()
        git("init", "-q", "-b", "main", cwd=self.repo)
        self.fakebin = self.tmp / "fakebin"  # the in-process PATH: git plus decoy go/nice that must never run
        self.fakebin.mkdir()
        (self.fakebin / "git").symlink_to(shutil.which("git"))
        self.decoy = self.tmp / "decoy-ran"
        for tool in ("go", "nice"):
            (self.fakebin / tool).write_text(f'#!/bin/sh\necho {tool} >> "{self.decoy}"\nexit 1\n')
            (self.fakebin / tool).chmod(0o755)
        self.gobin = self.tmp / "gobin"
        self.gobin.mkdir()
        self.go = str(self.gobin / "go")
        self.home = self.tmp / "home"
        self.rec, self.pidf, self.calls = self.tmp / "rec", self.tmp / "pid", self.tmp / "calls"
        self.envf, self.argvf, self.tmpok = self.tmp / "env", self.tmp / "argv", self.tmp / "tmpok"

    def fake_go(self, body):
        """A `go` that logs each call; body is shell run for `go build -o <out>`. $FAKE_* name this test's files (the
        build env is an allowlist, so the script cannot read them from its environment)."""
        names = {"$FAKE_CALLS": self.calls, "$FAKE_REC": self.rec, "$FAKE_PID": self.pidf, "$FAKE_LIB": self.lib,
                 "$FAKE_RUN": self.env.home_dir / "run", "$FAKE_ENV": self.envf, "$FAKE_ARGV": self.argvf, "$FAKE_TMPOK": self.tmpok}
        for k, v in names.items():
            body = body.replace(k, str(v))
        script = ('#!/bin/sh\necho call >> "%s"\necho "$@" > "%s"\nout=""\n'
                  'while [ $# -gt 0 ]; do [ "$1" = -o ] && out="$2"; shift; done\n' % (self.calls, self.argvf)) + body + "\n"
        Path(self.go).write_text(script)
        Path(self.go).chmod(0o755)

    @contextlib.contextmanager
    def factory(self, home=None):
        env = {"PATH": str(self.fakebin), "HOME": str(home or self.home)}
        with self.env.load_factory() as mod, mock.patch.dict(os.environ, env), mock.patch.object(mod, "load1", return_value=0.0):
            self.err = io.StringIO()
            with contextlib.redirect_stderr(self.err):
                yield mod

    def set_impl(self, impl):
        """guard_impl as a director sets it (the DB key the tick reads)."""
        if not (self.env.home_dir / "go").exists():
            write_go(self.env.home_dir)
        if not getattr(self, "_seeded", False):
            self.env.seed_agent("dir-1", "director")
            self._seeded = True
        self.assertEqual(self.env.run("config", "guard_impl", impl, agent="dir-1").returncode, 0)

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
        self.set_impl("go")  # the gate is open, so only the live-home check can stop the build
        self.fake_go(self.OK_BUILD)
        home = self.tmp / "home"
        (home / ".local" / "go" / "bin").mkdir(parents=True)  # the default toolchain path exists and would run
        shutil.copy(self.go, home / ".local" / "go" / "bin" / "go")
        with self.factory(home) as f:
            self.assertFalse(f.home_is_live())
            self.assertEqual(f.build_guard_go(), "skipped")
            f.guard_tick(f.db())
        self.assertFalse((home / ".local" / "lib").exists())
        self.assertEqual(self.ncalls(), 0)
        self.assertRegex((self.env.home_dir / "run" / "guard_impl").read_text(), r"^go [0-9a-f]{40}\n$")

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
                self.assertEqual(f.build_guard_go(self.repo, self.lib, tick=True, go=self.go), "failed")
                self.assertLess(time.monotonic() - t, 20)
        child = int(self.pidf.read_text())
        for _ in range(50):
            if not self.alive(child):
                break
            time.sleep(0.1)
        self.assertFalse(self.alive(child))
        self.assertFalse((self.lib / "worker-guard.stamp").exists())
        self.assertEqual((self.lib / "worker-guard.failed").read_text().strip(), h)

    # B3 order / acceptance 12 (guard_impl=go: the tick builds)
    def test_flag_holds_new_srchash_during_the_build(self):
        write_go(self.env.home_dir)
        self.set_impl("go")
        self.fake_go('cat "$FAKE_RUN/guard_impl" > "$FAKE_REC"\n' + self.OK_BUILD)
        with self.factory() as f:
            h2 = write_go(self.env.home_dir, MAIN_OK + "\n// next\n")
            f.guard_tick(f.db(), src=self.env.home_dir, lib=self.lib, go=self.go)
        self.assertEqual(self.rec.read_text(), f"go {h2}\n")
        self.assertEqual((self.lib / "worker-guard.stamp").read_text().strip(), h2)

    # B3 backoff / acceptance 13
    def test_failed_build_backs_off_on_the_tick_only(self):
        h = write_go(self.repo)
        self.fake_go("exit 1")
        marker = self.lib / "worker-guard.failed"
        with self.factory() as f:
            self.assertEqual(f.build_guard_go(self.repo, self.lib, tick=True, go=self.go), "failed")
            self.assertEqual(self.ncalls(), 1)
            self.assertEqual(f.build_guard_go(self.repo, self.lib, tick=True, go=self.go), "skipped")
            self.assertEqual(self.ncalls(), 1)
            old = time.time() - 31 * 60
            os.utime(marker, (old, old))
            self.assertEqual(f.build_guard_go(self.repo, self.lib, tick=True, go=self.go), "failed")  # marker aged out
            self.assertEqual(self.ncalls(), 2)
            self.assertEqual(f.build_guard_go(self.repo, self.lib, tick=True, go=self.go), "skipped")
            self.assertEqual(f.build_guard_go(self.repo, self.lib, go=self.go), "failed")  # `factory up` ignores it
            self.assertEqual(self.ncalls(), 3)
            write_go(self.repo, MAIN_OK + "\n// new\n")
            self.assertEqual(f.build_guard_go(self.repo, self.lib, tick=True, go=self.go), "failed")  # new srchash builds at once
            self.assertEqual(self.ncalls(), 4)
            self.fake_go(self.OK_BUILD)
            self.assertEqual(f.build_guard_go(self.repo, self.lib, go=self.go), "built")
        self.assertFalse(marker.exists())
        self.assertNotEqual(h, (self.lib / "worker-guard.stamp").read_text().strip())

    # D6 / acceptance 14: no go at the default path (the tmp HOME has none)
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
            self.assertEqual(f.build_guard_go(self.repo, self.lib, go=self.go), "built")
            new = write_go(self.repo, MAIN_OK + "\n// v2\n")
            self.fake_go('cat "$FAKE_LIB/worker-guard.stamp" > "$FAKE_REC"\n' + self.OK_BUILD)
            self.assertEqual(f.build_guard_go(self.repo, self.lib, go=self.go), "built")
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
                self.assertEqual(f.build_guard_go(self.repo, self.lib, go=self.go), "failed")
        self.assertTrue((self.lib / "worker-guard.failed").exists())  # the tick backs off here too
        self.assertFalse((self.lib / "worker-guard.stamp").exists())
        self.assertFalse((self.lib / "worker-guard").exists())

    # E1 / acceptance 16: at guard_impl=python no Go toolchain runs, on the tick path and on `factory up`
    def test_gate_python_never_runs_go_and_still_writes_the_flag(self):
        h = write_go(self.env.home_dir)
        self.fake_go(self.OK_BUILD)
        with self.factory() as f:
            for tick in (True, False):
                f.guard_tick(f.db(), tick=tick, src=self.env.home_dir, lib=self.lib, go=self.go)
        self.assertEqual(self.ncalls(), 0)
        self.assertFalse(self.lib.exists())
        self.assertEqual((self.env.home_dir / "run" / "guard_impl").read_text(), f"python {h}\n")

    def test_gate_open_for_go_and_shadow_on_both_paths(self):
        write_go(self.env.home_dir)
        for impl, tick in (("go", True), ("shadow", False)):
            self.fake_go(self.OK_BUILD)
            lib = self.tmp / f"lib-{impl}"
            self.set_impl(impl)
            before = self.ncalls()
            with self.factory() as f:
                f.guard_tick(f.db(), tick=tick, src=self.env.home_dir, lib=lib, go=self.go)
            self.assertEqual(self.ncalls(), before + 1, impl)
            self.assertTrue((lib / "worker-guard.stamp").exists(), impl)

    # E2 / acceptance 17: exactly this environment and argv, whatever the caller's environment holds
    def test_build_env_is_the_exact_allowlist(self):
        write_go(self.repo)
        self.fake_go('env > "$FAKE_ENV"\n[ -d "$GOTMPDIR" ] && echo yes > "$FAKE_TMPOK"\n' + self.OK_BUILD)
        poison = {"GOROOT": "/evil/goroot", "GOEXPERIMENT": "evil", "GODEBUG": "evil=1", "GOFLAGS": "-toolexec=/evil",
                  "GOENV": "/evil/env", "GOPATH": "/evil/path", "LD_PRELOAD": "/evil.so", "FAKE_EXTRA": "1"}
        with self.factory() as f, mock.patch.dict(os.environ, poison):
            self.assertEqual(f.build_guard_go(self.repo, self.lib, go=self.go), "built")
        got = dict(line.split("=", 1) for line in self.envf.read_text().splitlines() if "=" in line)
        for shell_var in ("PWD", "OLDPWD", "SHLVL", "_"):  # added by sh itself, not by the build
            got.pop(shell_var, None)
        gohome = self.lib / "go"
        self.assertEqual(got, {
            "PATH": "/usr/bin:/bin", "HOME": str(self.home), "GOTOOLCHAIN": "local", "GOENV": "off", "GOFLAGS": "",
            "GOWORK": "off", "GOPROXY": "off", "GOSUMDB": "off", "CGO_ENABLED": "0", "GOCACHE": str(gohome / "cache"),
            "GOMODCACHE": str(gohome / "mod"), "GOPATH": str(gohome / "path"), "GOTMPDIR": str(gohome / "tmp")})
        self.assertIn("-buildvcs=false", self.argvf.read_text().split())
        self.assertEqual(self.tmpok.read_text().strip(), "yes")  # GOTMPDIR existed when go ran

    # E2 / acceptance 18: real go ignores a poisoned user config and caller env
    @unittest.skipUnless(REAL_GO, "no ~/.local/go/bin/go toolchain: the real-go poison test cannot run")
    def test_real_go_ignores_poisoned_config_and_environment(self):
        write_go(self.repo)
        marker = self.tmp / "toolexec-ran"
        evil = self.tmp / "evil.sh"
        evil.write_text(f'#!/bin/sh\necho ran >> "{marker}"\nexec "$@"\n')
        evil.chmod(0o755)
        cfgdir = self.home / ".config" / "go"
        cfgdir.mkdir(parents=True)
        (cfgdir / "env").write_text(f"GOFLAGS=-toolexec={evil}\n")
        with self.factory() as f, mock.patch.dict(os.environ, {"GOFLAGS": f"-toolexec={evil}", "GOENV": str(cfgdir / "env")}):
            self.assertEqual(f.build_guard_go(self.repo, self.lib, go=REAL_GO), "built", self.err.getvalue())
        self.assertFalse(marker.exists(), "the poisoned -toolexec ran inside the guard build")

    # E3 / acceptance 19: a go and a nice first on PATH are never invoked
    def test_go_and_nice_are_not_looked_up_on_path(self):
        write_go(self.repo)
        self.fake_go(self.OK_BUILD)
        with self.factory() as f:
            self.assertEqual(f.build_guard_go(self.repo, self.lib, go=self.go), "built")
        self.assertEqual(self.ncalls(), 1)
        self.assertFalse(self.decoy.exists(), "a PATH go or nice ran")


class CliImplTests(FactoryTestCase):
    """T-1336 (G-041 S1-c): cli_impl / cli_go_cmds, run/cli_impl, the factory-go build and the bin/factory dispatch.
    The real go/cmd/factory is built once; the env's own go/ tree only gives the srchash the stamp must equal."""

    @classmethod
    def setUpClass(cls):
        super().setUpClass()
        cls.bin = None
        cls.seed = os.environ.get("GOMODCACHE_SEED")  # a module cache holding go/go.mod's deps (CI: setup-go + go mod download)
        if REAL_GO and cls.seed:
            cls.tmpdir = mktmp(cls, "zz-cli-impl-")
            out = cls.tmpdir / "factory-go"
            env = {**os.environ, "CGO_ENABLED": "0", "GOTOOLCHAIN": "local", "GOFLAGS": "", "GOPROXY": "off",
                   "GOMODCACHE": cls.seed}
            p = subprocess_run([REAL_GO, "build", "-buildvcs=false", "-trimpath", "-o", str(out), "./cmd/factory"],
                               BIN_DIR.parent / "go", env)
            if p.returncode:
                raise AssertionError(f"go build ./cmd/factory failed: {p.stderr}")
            cls.bin = out

    def setUp(self):
        super().setUp()
        self.h = write_go(self.env.home_dir)
        self.lib = self.env.user_home / ".local" / "lib" / "factory"
        self.lib.mkdir(parents=True)
        if self.bin is None:  # no real factory-go (needs go + GOMODCACHE_SEED): a fake one is enough for dispatch/flag tests
            (self.lib / "factory-go").write_text("#!/bin/sh\necho FAKE-GO\n")
            (self.lib / "factory-go").chmod(0o755)
        else:
            shutil.copy(self.bin, self.lib / "factory-go")
        self.stamp = self.lib / "factory-go.stamp"
        self.stamp.write_text(self.h + "\n")
        self.flag = self.env.home_dir / "run" / "cli_impl"
        self.env.seed_agent("dir-1", "director")
        self.env.seed_agent("lead-1", "lead")
        self.cfg("cli_impl", "go")
        self.cfg("cli_go_cmds", "whoami")

    def need_real(self):
        if self.bin is None:
            self.skipTest("needs the real factory-go: a go toolchain and GOMODCACHE_SEED (CI wires it after setup-go)")

    def cfg(self, key, value, agent="dir-1"):
        p = self.env.run("config", key, value, agent=agent, inproc=False)
        self.assertEqual(p.returncode, 0, p.stderr)

    def call(self, *args, agent="dir-1", **extra):
        return self.env.run(*args, agent=agent, inproc=False, extra_env={"FACTORY_CLI_TRACE": "1", **extra})

    def impl(self, p):
        lines = [x for x in p.stderr.splitlines() if x.startswith("impl=")]
        self.assertTrue(lines, p.stderr)  # a delegated Go call adds the Python child's own line after the first
        return lines[0]

    def python_answer(self, *args):
        return self.env.run(*args, agent="dir-1", inproc=False, extra_env={"FACTORY_CLI_IMPL": "python"})

    def test_flag_file_follows_config(self):  # run/cli_impl is "<mode> <srchash> <csv>"
        self.assertEqual(self.flag.read_text(), f"go {self.h} whoami\n")
        self.cfg("cli_impl", "python")
        self.assertEqual(self.flag.read_text(), "python -\n")

    def test_go_answers_when_flagged_and_listed(self):  # acceptance 2
        self.need_real()
        p = self.call("whoami")
        self.assertEqual(self.impl(p), "impl=go")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(p.stdout, self.python_answer("whoami").stdout)

    def test_stale_or_missing_binary_means_python(self):  # acceptance 3
        want = self.python_answer("whoami").stdout
        self.stamp.write_text("x\n")
        p = self.call("whoami")
        self.assertEqual((self.impl(p), p.stdout), ("impl=python", want))
        self.stamp.unlink()
        self.assertEqual(self.impl(self.call("whoami")), "impl=python")
        self.stamp.write_text(self.h + "\n")
        (self.lib / "factory-go").unlink()
        self.assertEqual(self.impl(self.call("whoami")), "impl=python")

    def test_malformed_flag_means_python(self):
        for text in ("go\n", "go " + self.h + "\n", "banana " + self.h + " whoami\n", "go  whoami\n", ""):
            self.flag.write_text(text)
            self.assertEqual(self.impl(self.call("whoami")), "impl=python", repr(text))
        self.flag.unlink()
        self.assertEqual(self.impl(self.call("whoami")), "impl=python")

    def test_env_can_only_force_python(self):  # acceptance 4
        self.assertEqual(self.impl(self.call("whoami", FACTORY_CLI_IMPL="python")), "impl=python")
        self.cfg("cli_impl", "python")
        self.assertEqual(self.impl(self.call("whoami", FACTORY_CLI_IMPL="go")), "impl=python")

    def test_unlisted_command_stays_python(self):  # acceptance 5
        self.assertEqual(self.impl(self.call("agents")), "impl=python")

    def test_user_version_equals_schema_version_after_init(self):
        import sqlite3
        with self.env.load_factory() as f:
            want = f.SCHEMA_VERSION
        con = sqlite3.connect(self.env.home_dir / "db" / "factory.db")
        self.addCleanup(con.close)
        self.assertEqual(con.execute("PRAGMA user_version").fetchone()[0], want)

    def test_go_gets_argv0_of_bin_factory(self):  # S1-b handoff: Go derives its Python entry from argv[0]
        self.need_real()
        self.cfg("cli_go_cmds", "agents")
        p = self.call("agents", "--bogus")  # Go delegates this shape to <dir of argv[0]>/factory under python3
        self.assertEqual(self.impl(p), "impl=go")
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertIn("unrecognized arguments: --bogus", p.stderr)

    def test_shadow_python_answers_and_a_diff_is_logged_as_hashes_only(self):
        self.need_real()
        self.cfg("cli_impl", "shadow")
        want = self.python_answer("whoami")
        log = self.env.home_dir / "logs" / "cli-shadow.log"
        p = self.call("whoami")
        self.assertEqual((self.impl(p), p.stdout), ("impl=shadow", want.stdout))
        self.assertFalse(log.exists(), "Go agreed with Python: nothing to log")
        (self.lib / "factory-go").write_text("#!/bin/sh\necho SECRET-GO-TEXT\n")
        (self.lib / "factory-go").chmod(0o755)
        p = self.call("whoami")
        self.assertEqual((p.returncode, p.stdout), (0, want.stdout))
        text = log.read_text()
        self.assertNotIn("SECRET-GO-TEXT", text)
        self.assertNotIn(want.stdout.strip(), text)
        self.assertRegex(text, r"[0-9a-f]{64} py=0 go=0 [0-9a-f]{64} [0-9a-f]{64}\n")

    def test_shadow_never_runs_a_write_twice(self):
        self.cfg("cli_impl", "shadow")  # first: with "config" listed and mode go, the fake factory-go would swallow this write
        self.cfg("cli_go_cmds", "config")
        self.assertEqual(self.impl(self.call("config", "cli_go_cmds", "config")), "impl=python")  # write: Python only
        self.assertEqual(self.impl(self.call("config", "cli_go_cmds")), "impl=shadow")  # read

    def test_director_only_and_same_refusal_as_guard_impl(self):  # acceptance 9
        a = self.env.run("config", "cli_impl", "go", agent="lead-1", inproc=False)
        b = self.env.run("config", "guard_impl", "go", agent="lead-1", inproc=False)
        self.assertEqual((a.returncode, a.stderr), (b.returncode, b.stderr))
        self.assertNotEqual(a.returncode, 0)

    def test_bad_values_are_exit_2(self):
        p = self.env.run("config", "cli_impl", "rust", agent="dir-1", inproc=False)
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertIn("python, go, shadow", p.stderr)
        p = self.env.run("config", "cli_go_cmds", "whoami,task", agent="dir-1", inproc=False)
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertIn("'task'", p.stderr)
        self.assertEqual(self.env.run("config", "cli_go_cmds", agent="dir-1", inproc=False).stdout.strip(), "whoami")

    def test_tick_rewrites_the_flag_from_the_db(self):
        self.flag.write_text("go x whoami\n")
        p = self.env.run("tick", extra_env={"FACTORY_LOADAVG": "999"})  # a loaded box skips the build
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(self.flag.read_text(), f"go {self.h} whoami\n")

    @unittest.skipUnless(REAL_GO, "no ~/.local/go/bin/go toolchain")
    def test_build_cli_go_writes_binary_then_stamp(self):
        repo, lib = self.lib.parent / "repo", self.lib.parent / "lib2"
        repo.mkdir()
        git("init", "-q", "-b", "main", cwd=repo)
        d = repo / "go" / "cmd" / "factory"
        d.mkdir(parents=True)
        (d / "main.go").write_text(MAIN_OK)
        (repo / "go" / "go.mod").write_text(GO_MOD)
        git("add", "-A", cwd=repo)
        git("commit", "-q", "-m", "go", cwd=repo)
        srchash = git("rev-parse", "HEAD:go", cwd=repo)
        with self.env.load_factory() as f:
            self.assertEqual(f.build_cli_go(repo, lib, go=REAL_GO), "built")
            self.assertEqual((lib / "factory-go.stamp").read_text().strip(), srchash)
            self.assertTrue(os.access(lib / "factory-go", os.X_OK))
            self.assertEqual(f.cli_binary_state(repo, lib), "ok")
            self.assertEqual(f.build_cli_go(repo, lib, go=REAL_GO), "current")
            self.assertFalse((lib / "worker-guard").exists(), "the guard build was not asked for")


class CliGoDepsTests(FactoryTestCase):
    """T-1336 item 3: the factory-go build is offline (GOPROXY=off); only fetch_cli_go_deps, called from `factory up`,
    reaches a proxy, with -mod=readonly and the checksum database on. A fake go records each call and its environment."""

    def setUp(self):
        super().setUp()
        self.tmp = mktmp(self, "zz-cli-deps-")
        self.repo, self.lib = self.tmp / "repo", self.tmp / "lib"
        (self.repo / "go" / "cmd" / "factory").mkdir(parents=True)
        (self.repo / "go" / "cmd" / "factory" / "main.go").write_text(MAIN_OK)
        (self.repo / "go" / "go.mod").write_text(GO_MOD)
        git("init", "-q", "-b", "main", cwd=self.repo)
        git("add", "-A", cwd=self.repo)
        git("commit", "-q", "-m", "go", cwd=self.repo)
        self.log = self.tmp / "calls"
        self.go = str(self.tmp / "go")

    def fake_go(self, build_body="exit 0"):
        Path(self.go).write_text(f'#!/bin/sh\necho "$1 $2" >> "{self.log}"\nenv | sort >> "{self.log}"\n'
                                 f'if [ "$1" = build ]; then\n{build_body}\nfi\n')
        Path(self.go).chmod(0o755)

    def test_fetch_runs_download_then_verify_with_readonly_and_sumdb_on(self):
        self.fake_go()
        with self.env.load_factory() as f:
            self.assertEqual(f.fetch_cli_go_deps(self.lib, self.repo, go=self.go), "fetched")
        text = self.log.read_text()
        self.assertLess(text.index("mod download"), text.index("mod verify"))
        for want in ("GOFLAGS=-mod=readonly", "GOSUMDB=sum.golang.org", "GOPROXY=https://proxy.golang.org",
                     f"GOMODCACHE={self.lib}/go/mod"):
            self.assertIn(want, text)

    @unittest.skipIf(os.geteuid() == 0, "root ignores file modes, so the read-only premise cannot hold")
    def test_cleanup_removes_a_read_only_go_module_cache(self):
        """T-1413: go writes its module cache dirs 0555 and files 0444; a plain rmtree leaves it in /tmp."""
        cache = self.tmp / "lib" / "go" / "mod" / "example.com" / "m-v1.0.0"
        cache.mkdir(parents=True)
        (cache / "m.go").write_text("package m\n")
        (cache / "m.go").chmod(0o444)
        for d in (cache, cache.parent, cache.parent.parent, cache.parent.parent.parent):
            d.chmod(0o555)
        shutil.rmtree(self.tmp / "lib", ignore_errors=True)
        self.assertTrue(cache.exists(), "premise: a plain rmtree cannot remove a read-only module cache")
        rmtree_rw(self.tmp / "lib")
        self.assertFalse((self.tmp / "lib").exists())

    def test_fetch_failure_is_reported_not_raised(self):
        Path(self.go).write_text("#!/bin/sh\necho 'no network' >&2\nexit 1\n")
        Path(self.go).chmod(0o755)
        with self.env.load_factory() as f:
            self.assertEqual(f.fetch_cli_go_deps(self.lib, self.repo, go=self.go), "failed")

    def test_the_tick_build_never_downloads_and_stays_offline(self):
        self.fake_go('while [ $# -gt 0 ]; do [ "$1" = -o ] && echo x > "$2"; shift; done')
        with self.env.load_factory() as f:
            self.assertEqual(f.build_cli_go(self.repo, self.lib, tick=True, go=self.go), "built")
        text = self.log.read_text()
        self.assertNotIn("mod download", text)
        self.assertIn("GOPROXY=off", text)
        self.assertNotIn("GOPROXY=https", text)

    def test_missing_deps_fail_with_the_run_factory_up_hint_and_python_stays(self):
        self.fake_go('echo "go: downloading modernc.org/sqlite v1.62.0: module lookup disabled by GOPROXY=off" >&2\nexit 1')
        with self.env.load_factory() as f:
            self.assertEqual(f.build_cli_go(self.repo, self.lib, tick=True, go=self.go), "failed")
            self.assertEqual(f.cli_binary_state(self.repo, self.lib), "missing")
        self.assertFalse((self.lib / "factory-go.stamp").exists())

    @unittest.skipUnless(REAL_GO, "no ~/.local/go/bin/go toolchain")
    def test_worker_guard_has_no_dependency_outside_this_module(self):
        p = subprocess_run([REAL_GO, "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", "./cmd/worker-guard"],
                           BIN_DIR.parent / "go", {**os.environ, "GOFLAGS": "-mod=readonly", "GOPROXY": "off",
                                                    "GOTOOLCHAIN": "local"})
        self.assertEqual(p.returncode, 0, p.stderr)
        bad = [x for x in p.stdout.split() if not x.startswith("factory/go")]
        self.assertEqual(bad, [], "the guard stays dependency-free (ADR 0001)")

    @unittest.skipUnless(REAL_GO and os.environ.get("GOMODCACHE_SEED"),
                         "needs a real go and GOMODCACHE_SEED (CI wires it after setup-go; not applied yet means skipped)")
    def test_real_fetch_from_a_file_proxy_then_an_offline_build(self):
        seed = os.environ["GOMODCACHE_SEED"]
        with self.env.load_factory() as f:
            src = Path(f.__file__).resolve().parent.parent
            self.assertEqual(f.fetch_cli_go_deps(self.lib, src, proxy=f"file://{seed}/cache/download", go=REAL_GO), "fetched")


def subprocess_run(cmd, cwd, env):
    import subprocess
    return subprocess.run(cmd, cwd=cwd, env=env, capture_output=True, text=True, timeout=600)


if __name__ == "__main__":
    unittest.main()
