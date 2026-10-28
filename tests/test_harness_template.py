"""The FactoryEnv template: one initialised home per process, copied per test."""
import os
import statistics
import subprocess
import time
import unittest
from unittest import mock

import harness
from harness import FactoryEnv


def made(**env):
    with mock.patch.dict(os.environ, env):
        e = FactoryEnv()
    return e


class TemplateTest(unittest.TestCase):
    def mk(self, **env):
        e = made(**env)
        self.addCleanup(e.cleanup)
        return e

    def test_independent_dbs(self):
        a, b = self.mk(), self.mk()
        a.sql("INSERT INTO config(key, value) VALUES ('zz_only_a', '1')")
        self.assertEqual(a.sql("SELECT value FROM config WHERE key='zz_only_a'")[0][0], "1")
        self.assertEqual(b.sql("SELECT * FROM config WHERE key='zz_only_a'"), [])
        self.assertNotEqual(a.db_path, b.db_path)

    def test_config_rows_match_old_path(self):
        new, old = self.mk(), self.mk(FACTORY_TEST_NO_TEMPLATE="1")
        q = "SELECT * FROM config ORDER BY key"
        rows = lambda e: [tuple(r) for r in e.sql(q)]
        self.assertTrue(rows(old))
        self.assertEqual(rows(new), rows(old))

    def test_no_wal_in_copy(self):
        e = self.mk()
        names = [p.name for p in e.db_path.parent.iterdir()]
        self.assertIn("factory.db", names)
        self.assertEqual([n for n in names if n.endswith(("-wal", "-shm"))], [])

    def test_template_rebuilds_when_factory_changes(self):
        self.mk()
        before = harness._TEMPLATE["env"]
        key = harness._template_key()
        with mock.patch.object(harness, "_template_key", return_value=key + ("changed",)):
            self.mk()
            self.assertIsNot(harness._TEMPLATE["env"], before)
            self.assertFalse(before.home.exists())
        self.mk()  # key back to the real one: rebuilt again, still usable
        self.assertTrue(harness._TEMPLATE["env"].home_dir.exists())

    def test_template_reused_when_unchanged(self):
        self.mk()
        t = harness._TEMPLATE["env"]
        self.mk()
        self.assertIs(harness._TEMPLATE["env"], t)

    def test_git_repos_independent(self):
        a, b = self.mk(), self.mk()
        (a.home_dir / "x.txt").write_text("x")
        subprocess.run(["git", "add", "x.txt"], cwd=a.home_dir, check=True)
        self.assertIn("x.txt", harness.git("status", "--short", cwd=a.home_dir))
        self.assertNotIn("x.txt", harness.git("status", "--short", cwd=b.home_dir))
        self.assertTrue(harness.git("rev-parse", "--git-dir", cwd=b.home_dir))

    def test_construction_is_fast(self):
        self.mk()  # warm the template
        times = []
        for _ in range(20):
            t = time.perf_counter()
            e = self.mk()
            times.append(time.perf_counter() - t)
        print(f"FactoryEnv median {statistics.median(times):.3f}s")
        self.assertLess(statistics.median(times), 0.10)


if __name__ == "__main__":
    unittest.main()
