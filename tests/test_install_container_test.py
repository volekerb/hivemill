"""ci/install_container_test.py: it compiles, and its root-refusal and rerun checks accept the right installer
answer and reject the wrong one (the checks run as a fake runner; the real run happens in the install-smoke CI job)."""
import importlib.util
import py_compile
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SCRIPT = ROOT / "ci" / "install_container_test.py"
spec = importlib.util.spec_from_file_location("install_container_test", SCRIPT)
ict = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ict)

TCP4 = """  sl  local_address rem_address   st
   0: 0100007F:223D 00000000:0000 0A 00000000:00000000
   1: 00000000:0050 00000000:0000 0A 00000000:00000000
   2: 0100007F:223D 0100007F:9999 01 00000000:00000000
"""
TCP4_LAN = TCP4.replace("0100007F:223D 00000000", "00000000:223D 00000000", 1)


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
        ict.check_rerun_refused(lambda c: (4, "already installed (x)"), ["x"], lambda: 5)

    def test_wrong_exit_fails(self):
        with self.assertRaises(ict.Fail):
            ict.check_rerun_refused(lambda c: (0, "RESULT PASS"), ["x"], lambda: 5)

    def test_changed_mtime_fails(self):
        ticks = iter([5, 6])
        with self.assertRaises(ict.Fail):
            ict.check_rerun_refused(lambda c: (4, "already installed"), ["x"], lambda: next(ticks))


class Listeners(unittest.TestCase):
    def test_loopback_only(self):
        self.assertEqual(ict.listeners(8765, TCP4, ""), ["0100007F"])

    def test_lan_listener_is_seen(self):
        self.assertIn("00000000", ict.listeners(8765, TCP4_LAN, ""))

    def test_other_ports_and_connections_ignored(self):
        self.assertEqual(ict.listeners(9, TCP4, ""), [])


if __name__ == "__main__":
    unittest.main()
