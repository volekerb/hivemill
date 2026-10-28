"""Hot factory commands must not import the scout modules (T-0345): scout.sources alone costs ~26 ms of urllib/ssl."""
import os
import re
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
FACTORY = ROOT / "bin" / "factory"
SCOUT_LINE = re.compile(r"\| +(scout|workerbox)(\.|\s*$)", re.M)


def importtime(*args, init=False):
    with tempfile.TemporaryDirectory() as home:
        env = {k: v for k, v in os.environ.items() if k not in ("FACTORY_AGENT", "FACTORY_ROLE", "CLAUDECODE", "HERDR_PANE_ID")}
        env["FACTORY_HOME"] = home
        if init:
            r = subprocess.run([sys.executable, str(FACTORY), "init"], capture_output=True, text=True,
                               env={**env, "FACTORY_AGENT": "owner"},
                               timeout=60)
            assert r.returncode == 0, r.stderr
        return subprocess.run([sys.executable, "-X", "importtime", str(FACTORY), *args],
                              capture_output=True, text=True, env=env, timeout=60)


class LazyScoutImports(unittest.TestCase):
    def test_hot_commands_import_no_scout_module(self):
        for args in (["--help"], ["whoami"], ["inbox"]):
            with self.subTest(args=args):
                r = importtime(*args)
                self.assertIn("import time:", r.stderr)
                self.assertEqual(SCOUT_LINE.findall(r.stderr), [], r.stderr[-800:])

    def test_hot_commands_on_an_initialised_db_import_no_scout_module(self):
        for args in (["whoami"], ["inbox"], ["task", "list"]):
            with self.subTest(args=args):
                r = importtime(*args, init=True)
                self.assertIn("import time:", r.stderr)
                self.assertEqual(SCOUT_LINE.findall(r.stderr), [], r.stderr[-800:])

    def test_scout_tables_list_matches_the_ddl(self):
        sys.path.insert(0, str(ROOT))
        from scout import db as scoutdb
        ddl = set(re.findall(r"CREATE TABLE IF NOT EXISTS (\w+)", scoutdb.SCOUT_DDL))
        code = ("import importlib.machinery, importlib.util\n"
                "l = importlib.machinery.SourceFileLoader('fc', %r)\n"
                "m = importlib.util.module_from_spec(importlib.util.spec_from_loader('fc', l))\n"
                "l.exec_module(m)\nprint(sorted(m.SCOUT_TABLES))\n" % str(FACTORY))
        with tempfile.TemporaryDirectory() as home:
            r = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True,
                               env={**os.environ, "FACTORY_HOME": home, "PYTHONPATH": str(ROOT)}, timeout=60)
        self.assertEqual(r.stdout.strip(), str(sorted(ddl)), r.stderr[-400:])

    def test_scout_help_still_works_and_loads_scout_lazily(self):
        r = importtime("scout", "--help")
        self.assertEqual(r.returncode, 0, r.stderr[-400:])
        self.assertIn("usage:", r.stdout)

    def test_scout_names_resolve_and_patch_through_to_the_module(self):
        code = (
            "import importlib.machinery, importlib.util, sys\n"
            "sys.path.insert(0, %r)\n"
            "l = importlib.machinery.SourceFileLoader('factory_cli', %r)\n"
            "m = importlib.util.module_from_spec(importlib.util.spec_from_loader('factory_cli', l))\n"
            "l.exec_module(m)\n"
            "assert not [k for k in sys.modules if k.startswith('scout')], 'scout imported at load'\n"
            "assert m.scoutsandbox.nsbox is sys.modules['scout.nsbox']\n"
            "m.scoutsandbox.preflight = 'patched'\n"
            "assert sys.modules['scout.sandbox'].preflight == 'patched'\n"
            "assert m.workerbox.SANDBOXABLE and m.scoutdb.SCOUT_DDL\n"
            "print('ok')\n" % (str(ROOT), str(FACTORY)))
        with tempfile.TemporaryDirectory() as home:
            r = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True,
                               env={**os.environ, "FACTORY_HOME": home}, timeout=60)
        self.assertEqual(r.stdout.strip(), "ok", r.stderr[-600:])


if __name__ == "__main__":
    unittest.main()
