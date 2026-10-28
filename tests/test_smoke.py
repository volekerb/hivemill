import py_compile
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from harness import FACTORY, STUBS, FactoryTestCase


class SmokeTests(unittest.TestCase):
    def test_py_compile(self):
        with tempfile.TemporaryDirectory() as d:  # cfile in a temp dir: nothing is written into the repo
            py_compile.compile(str(FACTORY), cfile=str(Path(d) / "factory.pyc"), doraise=True)

    def test_help_exits_zero(self):
        p = subprocess.run([sys.executable, str(FACTORY), "--help"], capture_output=True, text=True,
                           env={"PYTHONDONTWRITEBYTECODE": "1", "PATH": "/usr/bin:/bin", "HOME": tempfile.gettempdir(),
                                "FACTORY_HOME": tempfile.gettempdir() + "/zz-test-no-such-home"})
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("usage: factory", p.stdout)


class CliTests(FactoryTestCase):
    def test_whoami_owner_and_agent(self):
        self.assertIn("owner", self.ok("whoami").stdout)
        self.env.seed_agent("zz-test-pm", "pm")
        self.assertIn("role=pm", self.ok("whoami", agent="zz-test-pm").stdout)

    def test_no_database_refused(self):
        self.env.db_path.unlink()
        p = self.refused(1, "agents")
        self.assertIn("run `factory init`", p.stderr)

    def test_stubs_are_first_on_path(self):
        self.ok("res")
        self.assertTrue(self.env.calls("herdr"), "herdr stub was never called")
        path = self.env.env()["PATH"].split(":")
        self.assertEqual(path[0], str(self.env.stub_bin))
        self.assertEqual((self.env.stub_bin / "herdr").read_bytes(), (STUBS / "herdr").read_bytes())
        self.assertEqual((self.env.stub_bin / "gh").resolve().parent.name, "stubs")


if __name__ == "__main__":
    unittest.main()
