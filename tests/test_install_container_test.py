"""ci/install_container_test.py: it compiles, its root-refusal, rerun and health checks accept the right installer
answer and reject the wrong one (the checks run as a fake runner; the real run happens in the install-smoke CI job),
and its pure helpers pick the newest release tag and put the Go toolchain on the plain user's PATH."""
import importlib.util
import os
import py_compile
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parent.parent
SCRIPT = ROOT / "ci" / "install_container_test.py"
spec = importlib.util.spec_from_file_location("install_container_test", SCRIPT)
ict = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ict)

class CompileTest(unittest.TestCase):
    def test_compiles(self):
        with tempfile.TemporaryDirectory() as d:
            py_compile.compile(str(SCRIPT), cfile=str(Path(d) / "x.pyc"), doraise=True)

    def test_usage_error_without_mode(self):
        p = subprocess.run([sys.executable, str(SCRIPT)], capture_output=True, text=True)
        self.assertEqual(p.returncode, 2)


class RootRefusal(unittest.TestCase):
    def test_exit_1_with_reason_passes(self):
        ict.check_root_refusal(lambda c: (1, "refusing to run as root: run this as your normal user"), ["x"])

    def test_exit_0_fails(self):
        with self.assertRaises(ict.Fail):
            ict.check_root_refusal(lambda c: (0, "RESULT PASS"), ["x"])

    def test_exit_1_for_another_reason_fails(self):
        with self.assertRaises(ict.Fail):
            ict.check_root_refusal(lambda c: (1, "RESULT FAIL step=tools"), ["x"])


class Rerun(unittest.TestCase):
    def test_exit_4_and_same_mtime_passes(self):
        ict.check_rerun_refused(lambda c: (4, "x is already a clone but has no database: a partial install. Re-run with --resume to finish it; nothing was touched"), ["x"], lambda: 5)

    def test_wrong_exit_fails(self):
        with self.assertRaises(ict.Fail):
            ict.check_rerun_refused(lambda c: (0, "RESULT PASS"), ["x"], lambda: 5)

    def test_changed_mtime_fails(self):
        ticks = iter([5, 6])
        with self.assertRaises(ict.Fail):
            ict.check_rerun_refused(lambda c: (4, "nothing was touched"), ["x"], lambda: next(ticks))


class Health(unittest.TestCase):
    def test_rc_0_passes(self):
        ict.check_health(0, "last good pass 1s ago; drain false")

    def test_no_database_yet_passes(self):
        ict.check_health(1, "hivemill: cannot open database: no such file")

    def test_rc_1_for_another_reason_fails(self):
        with self.assertRaises(ict.Fail):
            ict.check_health(1, "last good pass 2h0m0s ago; drain false")

    def test_crash_fails(self):
        with self.assertRaises(ict.Fail):
            ict.check_health(2, "cannot open database")


LS_REMOTE = """aaa\trefs/tags/v0.4.9
bbb\trefs/tags/v0.10.0
ccc\trefs/tags/v0.10.0^{}
ddd\trefs/tags/v0.11.0-rc1
eee\trefs/tags/v0.9.12
fff\trefs/heads/v9.9.9
"""


class NewestTag(unittest.TestCase):
    def test_numeric_not_lexical_order(self):
        self.assertEqual(ict.newest_tag(LS_REMOTE), "v0.10.0")

    def test_prerelease_and_branches_ignored(self):
        self.assertEqual(ict.newest_tag("a\trefs/tags/v1.0.0-rc1\nb\trefs/heads/v2.0.0\n"), None)

    def test_empty(self):
        self.assertIsNone(ict.newest_tag(""))


class GoOnPath(unittest.TestCase):
    def test_user_path_has_go_dir_after_local_bin(self):
        parts = ict.user_path("/srv/u", "/opt/go/bin").split(":")
        self.assertEqual(parts[:2], ["/srv/u/.local/bin", "/opt/go/bin"])
        self.assertIn("/usr/bin", parts)

    def test_user_cmd_clean_env_with_go_dir(self):
        cmd = ict.user_cmd("/srv/u", "/opt/go/bin", ["go", "version"])
        self.assertEqual(cmd[-2:], ["go", "version"])
        self.assertIn("-i", cmd)
        self.assertIn("PATH=" + ict.user_path("/srv/u", "/opt/go/bin"), cmd)

    def test_go_bin_dir_resolves_the_symlink(self):
        with tempfile.TemporaryDirectory() as d:
            real = Path(d) / "toolcache" / "go" / "bin"
            real.mkdir(parents=True)
            (real / "go").write_text("#!/bin/sh\n")
            (real / "go").chmod(0o755)
            link = Path(d) / "shims"
            link.mkdir()
            (link / "go").symlink_to(real / "go")
            with mock.patch.object(ict.shutil, "which", return_value=str(link / "go")):
                self.assertEqual(ict.go_bin_dir(), os.path.realpath(str(real)))

    def test_no_go_fails_clearly(self):
        with mock.patch.object(ict.shutil, "which", return_value=None):
            with self.assertRaisesRegex(ict.Fail, "setup-go"):
                ict.go_bin_dir()


if __name__ == "__main__":
    unittest.main()
