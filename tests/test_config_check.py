"""T-1228 (G-038 SC2, F-04): `factory config --changed` lists live values that differ from DEFAULT_CONFIG and
`factory config --check` runs the set path's per-key validators over every live value (exit 2 if any is bad)."""
import sqlite3
import unittest

from harness import FactoryTestCase


class ConfigCheck(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_agent("claude-director", "director")
        self.db_exec("DELETE FROM config")  # the harness pre-sets a few keys; start from the defaults

    def db_exec(self, sql, *args):
        con = sqlite3.connect(self.env.db_path)
        con.execute(sql, args)
        con.commit()
        con.close()

    def test_changed_fresh_home(self):
        r = self.env.run("config", "--changed", agent="claude-director")
        self.assertEqual((r.returncode, r.stdout.strip()), (0, "no changes from defaults"), r.stderr)

    def test_changed_lists_one_key(self):
        r = self.env.run("config", "digest_hour", "9", agent="claude-director")
        self.assertEqual(r.returncode, 0, r.stderr)
        r = self.env.run("config", "--changed")
        lines = r.stdout.strip().splitlines()
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(len(lines), 1, r.stdout)
        self.assertIn("digest_hour = 9 (default: 8)", lines[0])

    def test_check_clean_home(self):
        r = self.env.run("config", "--check")
        self.assertEqual((r.returncode, r.stdout), (0, ""), r.stderr)

    def test_check_bad_stored_value(self):
        self.db_exec("INSERT INTO config(key,value) VALUES('guard_impl','bogus')")
        self.db_exec("INSERT INTO config(key,value) VALUES('task_clones','7')")
        r = self.env.run("config", "--check")
        self.assertEqual(r.returncode, 2, r.stderr)
        self.assertIn("guard_impl", r.stdout)
        self.assertIn("task_clones: task_clones must be 0 or 1, not '7'", r.stdout)  # does not stop at the first bad key

    def test_set_path_unchanged(self):
        r = self.env.run("config", "guard_impl", "bogus", agent="claude-director")
        self.assertEqual(r.returncode, 2)
        self.assertEqual(r.stderr.strip(), "factory: guard_impl must be one of python, go, shadow, not 'bogus': "
                                           "fix it with `factory config guard_impl python`")
        r = self.env.run("config", "opus_effort", "bogus", agent="claude-director")
        self.assertEqual(r.returncode, 1)  # this one has always exited 1
        r = self.env.run("config", "guard_impl", " python ", agent="claude-director")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(self.env.run("config", "guard_impl").stdout.strip(), "python")

    def test_flags_refuse_arguments(self):
        for args in (("--changed", "digest_hour"), ("--check", "digest_hour", "9"), ("--changed", "--check")):
            r = self.env.run("config", *args)
            self.assertEqual(r.returncode, 2, args)
            self.assertIn("usage:", r.stderr)


if __name__ == "__main__":
    unittest.main()
