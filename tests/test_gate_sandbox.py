"""T-0652 (GS2): config gate_sandbox runs `factory gate run`'s gate_cmd and gate_count_cmd under scout/gatebox.py.
Real `factory gate run` as a subprocess in a temp FACTORY_HOME (tests/test_gate_run.py harness); the real namespaces run
only where user namespaces are available (skipped with the reason otherwise). Credentials and the marker are fake."""
import os
import stat
import sys
import unittest
from pathlib import Path
from unittest import mock

from harness import REPO
from test_gate_run import GateBase

sys.path.insert(0, str(REPO))
from scout import gatebox, nsbox  # noqa: E402

USERNS_OK, USERNS_WHY = nsbox.userns_available()
SKIP_MSG = f"user namespaces unavailable: {USERNS_WHY}"


class SandboxBase(GateBase):
    def setUp(self):
        super().setUp()
        self.ok("config", "gate_sandbox", "1", agent="zz-test-director")
        self.marker = self.env.home / "marker.txt"
        self.extra["GATE_MARKER"] = str(self.marker)

    def log(self):
        return Path(self.run_row()["log_path"]).read_text()

    def log_of_last(self):
        return Path(self.env.sql("SELECT log_path FROM gate_runs ORDER BY id DESC LIMIT 1")[0]["log_path"]).read_text()


@unittest.skipUnless(USERNS_OK, SKIP_MSG)
class SandboxedRun(SandboxBase):
    def test_fake_credentials_unreadable_and_recorded_as_sandboxed(self):
        cred = self.env.user_home / ".config" / "gh" / "hosts.yml"
        cred.parent.mkdir(parents=True)
        cred.write_text("FAKE-GH-TOKEN")
        self.set_gate(f"cat {cred}")
        p = self.gate("run", "T-0001")
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        r = self.run_row()
        self.assertEqual(r["status"], "fail")
        self.assertIn("No such file", self.log())
        self.assertNotIn("FAKE-GH-TOKEN", self.log())
        self.assertIn("sandboxed", r["note"])

    def test_passing_command_passes_and_reports_in_the_rw_dirs(self):
        self.set_gate('git rev-parse HEAD > "$GATE_REPORT_DIR/head.txt"; echo hi > "$TMPDIR/t"; test "$HOME" != ""')
        p = self.gate("run", "T-0001")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr + self.log())
        self.assertEqual(self.run_row()["status"], "pass")
        self.assertIn("sandboxed", self.run_row()["note"])

    def test_network_fails(self):
        code = "import socket; socket.create_connection(('127.0.0.1', 9), 2)"
        self.set_gate(f'{sys.executable} -c "{code}"')
        p = self.gate("run", "T-0001")
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        self.assertRegex(self.log(), "refused|unreachable|Errno")

    def test_writes_outside_the_rw_dirs_fail(self):
        for where in ("$FACTORY_MAIN/stray", "$(git rev-parse --git-common-dir)/hooks/stray", "$(git rev-parse --git-common-dir)/config"):
            with self.subTest(where=where):
                self.set_gate(f"echo x > {where}")
                p = self.gate("run", "T-0001")
                self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
                self.assertIn("Read-only file system", self.log_of_last())
        self.assertFalse((REPO / "stray").exists())
        self.assertFalse((self.env.user_home / "stray").exists())

    def test_gate_tree_is_writable_but_the_git_common_dir_is_not(self):
        self.set_gate('echo x > "$PWD/inside" && mkdir -p "$PWD/newdir" && echo y > "$PWD/newdir/f" && test -f "$PWD/inside"')
        p = self.gate("run", "T-0001")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr + self.log())
        self.assertEqual(self.run_row()["status"], "pass")

    def test_var_tmp_write_fails_to_persist(self):
        leak = Path("/var/tmp") / f"zz-test-gate-sandbox-{os.getpid()}"
        self.addCleanup(lambda: leak.unlink(missing_ok=True))
        self.set_gate(f"echo x > {leak}; cat {leak}")
        p = self.gate("run", "T-0001")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)  # the write lands in the box's private tmpfs
        self.assertFalse(leak.exists())

    def test_count_cmd_runs_in_the_box_too(self):
        self.set_gate("true", count="touch $FACTORY_MAIN/x 2>/dev/null || echo 7")  # FACTORY_MAIN is read-only inside the box
        p = self.gate("run", "T-0001")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.run_row()["test_count"], 7)
        self.assertIn("gate_count_cmd in", self.log())

    def test_count_cmd_cannot_read_credentials(self):
        cred = self.env.user_home / "secret.txt"
        cred.write_text("123")
        self.set_gate("true", count=f"cat {cred} || echo 5")
        p = self.gate("run", "T-0001")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.run_row()["test_count"], 5)  # the file was hidden, so cat failed and the fallback printed 5


class FailClosed(SandboxBase):
    def fake_unshare(self):
        f = self.env.stub_bin / "unshare"
        f.write_text("#!/bin/sh\necho 'unshare: unshare failed: Operation not permitted' >&2\nexit 1\n")
        f.chmod(f.stat().st_mode | stat.S_IXUSR)

    def test_userns_unavailable_is_error_and_the_command_never_runs(self):
        self.fake_unshare()
        self.set_gate('touch "$GATE_MARKER"')
        p = self.gate("run", "T-0001")
        self.assertEqual(p.returncode, 5, p.stdout + p.stderr)
        r = self.run_row()
        self.assertEqual(r["status"], "error")
        self.assertIn("not run unsandboxed", r["note"])
        self.assertFalse(self.marker.exists(), "the gate command ran without the sandbox")
        self.assertClean()

    def test_count_cmd_never_runs_unsandboxed_either(self):
        self.fake_unshare()
        self.set_gate("true", count='touch "$GATE_MARKER"; echo 1')
        p = self.gate("run", "T-0001")
        self.assertEqual(p.returncode, 5, p.stdout + p.stderr)
        self.assertFalse(self.marker.exists())

    def test_bad_config_value_is_error_and_the_command_never_runs(self):
        self.ok("config", "gate_sandbox", "yes", agent="zz-test-director")
        self.set_gate('touch "$GATE_MARKER"')
        p = self.gate("run", "T-0001")
        self.assertEqual(p.returncode, 5, p.stdout + p.stderr)
        self.assertIn("gate_sandbox", self.run_row()["note"])
        self.assertFalse(self.marker.exists())


class FlagOff(GateBase):
    def test_default_is_off_and_runs_unboxed(self):
        self.set_gate('touch "$GATE_OUT"')
        self.assertEqual(self.gate("run", "T-0001").returncode, 0)
        self.assertTrue(self.out.exists())
        self.assertNotIn("sandboxed", self.run_row()["note"] or "")

    def test_off_ignores_a_missing_userns(self):
        f = self.env.stub_bin / "unshare"
        f.write_text("#!/bin/sh\nexit 1\n")
        f.chmod(f.stat().st_mode | stat.S_IXUSR)
        self.set_gate("true")
        self.assertEqual(self.gate("run", "T-0001").returncode, 0)


class Wiring(unittest.TestCase):
    def test_runtime_is_passed_explicitly(self):
        sys.path.insert(0, str(REPO / "bin"))
        import importlib.machinery, importlib.util
        loader = importlib.machinery.SourceFileLoader("factory_cli_t0652", str(REPO / "bin" / "factory"))
        spec = importlib.util.spec_from_loader("factory_cli_t0652", loader)
        mod = importlib.util.module_from_spec(spec)
        loader.exec_module(mod)
        box = {"factory_main": "/m", "wt_root": "/w"}
        with mock.patch.object(gatebox, "build", return_value=(["x"], {})) as b, \
                mock.patch.dict(os.environ, {"XDG_RUNTIME_DIR": "/run/user/xyz"}):
            mod.gate_argv("true", "/t", {}, box)
        self.assertEqual(b.call_args.kwargs["runtime"], "/run/user/xyz")


if __name__ == "__main__":
    unittest.main()
