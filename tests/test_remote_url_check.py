"""Tick check: config factory_remote_url must match `git remote get-url origin` of the factory checkout (T-0603)."""
import tempfile
import unittest
from pathlib import Path

from harness import FactoryTestCase, git

OLD = "https://github.com/example-org/old.git"
NEW = "https://github.com/example-org/new.git"


class RemoteUrlCheckTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.checkout = Path(tmp.name).resolve()
        git("init", "-q", "-b", "main", cwd=self.checkout)
        git("remote", "add", "origin", OLD, cwd=self.checkout, inert_remote=True)

    def set_cfg(self, value):
        self.env.sql("INSERT INTO config(key,value) VALUES('factory_remote_url',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", (value,))

    def run_hook(self):
        with self.env.load_factory() as mod:
            mod.LIVE_HOME = str(self.checkout)
            con = mod.db()
            mod.remote_url_alert(con)
            con.commit()

    def alerts(self):
        return self.env.sql("SELECT recipient, subject, body FROM messages WHERE subject LIKE 'factory_remote_url%'")

    def test_match_sends_nothing(self):
        self.set_cfg(OLD)
        self.run_hook()
        self.assertEqual(self.alerts(), [])

    def test_dot_git_or_case_difference_alerts_like_the_guard(self):
        """The git guard compares exactly, so a near match is a refused push and must alert."""
        for want in ("https://github.com/example-org/old", "https://github.com/Example-Org/old.git"):
            self.set_cfg(want)
            self.run_hook()
        self.assertEqual(len(self.alerts()), 2)

    def test_credentials_are_not_copied_into_the_alert(self):
        git("remote", "set-url", "origin", "https://user:s3cret" + chr(64) + "github.com/example-org/old.git", cwd=self.checkout, inert_remote=True)
        self.set_cfg(NEW)
        self.run_hook()
        body = self.alerts()[0]["body"]
        self.assertNotIn("s3cret", body)
        self.assertIn("https://github.com/example-org/old.git", body)

    def test_empty_config_is_not_checked(self):
        self.set_cfg("")
        self.run_hook()
        self.assertEqual(self.alerts(), [])

    def test_mismatch_alerts_the_director_once_and_names_the_fix(self):
        self.set_cfg(NEW)
        self.run_hook()
        self.run_hook()
        rows = self.alerts()
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["recipient"], "director")
        self.assertIn(NEW, rows[0]["body"])
        self.assertIn(OLD, rows[0]["body"])
        self.assertIn("factory config factory_remote_url", rows[0]["body"])

    def test_fixing_it_rearms_the_alert(self):
        self.set_cfg(NEW)
        self.run_hook()
        self.set_cfg(OLD)
        self.run_hook()
        self.set_cfg(NEW)
        self.run_hook()
        self.assertEqual(len(self.alerts()), 2)

    def test_missing_origin_alerts(self):
        git("remote", "remove", "origin", cwd=self.checkout)
        self.set_cfg(OLD)
        self.run_hook()
        self.assertIn("(none)", self.alerts()[0]["body"])


if __name__ == "__main__":
    unittest.main()
