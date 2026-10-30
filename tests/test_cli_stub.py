"""bin/factory is a stub that imports bin/factory_cli.py (so CPython caches its bytecode); both entry paths must keep working."""
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from harness import FACTORY, FACTORY_SRC  # noqa: E402


class CliStub(unittest.TestCase):
    def test_help_via_stub(self):
        p = subprocess.run([sys.executable, str(FACTORY), "--help"], capture_output=True, text=True)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("usage: factory", p.stdout)

    def test_help_via_symlink_from_another_dir(self):
        with tempfile.TemporaryDirectory() as d:
            link = Path(d) / "factory"
            link.symlink_to(FACTORY)
            p = subprocess.run([sys.executable, str(link), "--help"], capture_output=True, text=True, cwd=d)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("usage: factory", p.stdout)

    def test_stub_is_tiny_and_the_code_lives_in_the_module(self):
        self.assertLess(len(FACTORY.read_text().splitlines()), 20)
        self.assertGreater(len(FACTORY_SRC.read_text().splitlines()), 9000)

    def test_stub_fails_loudly_when_the_module_is_missing(self):
        with tempfile.TemporaryDirectory() as d:
            (Path(d) / "factory").write_text(FACTORY.read_text())
            p = subprocess.run([sys.executable, str(Path(d) / "factory"), "--help"], capture_output=True, text=True)
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("factory_cli", p.stderr)


if __name__ == "__main__":
    unittest.main()
