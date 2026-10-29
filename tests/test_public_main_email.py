"""G-021 T-0602 layer 2: the tick (export) escalates to the boss, once per sha, when origin/main of a public_text_repos
repo gets a commit whose author or committer email is not noreply. Temp FACTORY_HOME, temp bare origin acme/pub."""
import unittest

from harness import FactoryTestCase
from test_public_repo_safety import CLEAN, run_git

REAL = {"GIT_AUTHOR_NAME": "Real", "GIT_AUTHOR_EMAIL": "real.person@example.com",
        "GIT_COMMITTER_NAME": "Real", "GIT_COMMITTER_EMAIL": "real.person@example.com"}
WEBFLOW = {**CLEAN, "GIT_COMMITTER_NAME": "GitHub", "GIT_COMMITTER_EMAIL": "noreply@github.com"}


class PublicMainEmailTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        self.bare = e.home / "remotes" / "acme" / "pub.git"
        self.bare.parent.mkdir(parents=True)
        run_git(e.home, "init", "-q", "--bare", "-b", "main", str(self.bare))
        run_git(e.home_dir, "remote", "add", "origin", str(self.bare))
        run_git(e.home_dir, "commit", "-q", "--allow-empty", "-m", "base")
        run_git(e.home_dir, "push", "-q", "-u", "origin", "main")
        self.ok("config", "git_push", "0")
        self.ok("config", "public_text_repos", "acme/pub")
        self.wt = e.home / "pushwt"
        run_git(e.home, "clone", "-q", str(self.bare), str(self.wt))

    def push(self, env, name="c", msg=None):
        (self.wt / name).write_text(name)
        run_git(self.wt, "add", name)
        run_git(self.wt, "commit", "-q", "--cleanup=verbatim", "-m", msg or name, env=env)  # keeps CRs in the message
        run_git(self.wt, "push", "-q", "origin", "HEAD:main")
        return run_git(self.wt, "rev-parse", "HEAD")

    def alerts(self):
        return self.env.sql("SELECT recipient, kind, subject, body FROM messages WHERE subject LIKE 'public main%' ORDER BY id")

    def test_noreply_and_web_flow_commits_do_not_alert(self):
        self.push(CLEAN, "a")
        self.push(WEBFLOW, "b")
        self.ok("export")
        self.assertEqual(self.alerts(), [])

    def test_real_author_email_escalates_once_per_sha(self):
        sha = self.push(REAL)
        self.ok("export")
        self.ok("export")  # second tick: same sha, no second message
        a = self.alerts()
        self.assertEqual(len(a), 1, a)
        self.assertEqual((a[0]["recipient"], a[0]["kind"]), ("boss", "escalation"))
        self.assertIn(sha[:8], a[0]["subject"])
        self.assertNotIn("real.person", a[0]["body"])
        sha2 = self.push(REAL, "d")
        self.ok("export")
        self.assertEqual(len(self.alerts()), 2)
        self.assertIn(sha2[:8], self.alerts()[1]["subject"])

    def test_real_committer_email_alone_escalates(self):
        self.push({**CLEAN, "GIT_COMMITTER_EMAIL": "real.person@example.com"})
        self.ok("export")
        self.assertEqual(len(self.alerts()), 1)

    def test_non_noreply_coauthor_trailer_escalates(self):
        sha = self.push(CLEAN, "t", msg="t\n\nCo-authored-by: factory refresh <factory-refresh@localhost>")
        self.push(CLEAN, "u", msg="u\n\nCo-authored-by: volekerb <1+volekerb@users.noreply.github.com>\nCo-authored-by: C <noreply@anthropic.com>")
        self.ok("export")
        a = self.alerts()
        self.assertEqual(len(a), 1, a)
        self.assertIn(sha[:8], a[0]["subject"])
        self.assertIn("Co-authored-by", a[0]["body"])
        self.assertNotIn("localhost", a[0]["body"])

    def test_crlf_and_cr_messages_with_good_trailers_do_not_alert(self):
        good = "Co-authored-by: Dev <1+dev@users.noreply.github.com>"
        self.push(CLEAN, "v", msg=f"v\r\n\r\n{good}\r\nCo-authored-by: C <noreply@anthropic.com>")
        self.push(CLEAN, "w", msg=f"w\r\r{good}")
        self.push(CLEAN, "y", msg=f"y\u2028{good}\x0c{good}\x0bCo-authored-by: C <noreply@anthropic.com>")  # splitlines separators
        self.ok("export")
        self.assertEqual(self.alerts(), [])

    def test_bad_trailer_in_crlf_message_still_escalates(self):
        sha = self.push(CLEAN, "x", msg="x\r\n\r\nCo-authored-by: factory refresh <factory-refresh@localhost>\r\n")
        sha2 = self.push(CLEAN, "z", msg="z\u2028Co-authored-by: factory refresh <factory-refresh@localhost>\x0c")
        self.ok("export")
        a = self.alerts()
        self.assertEqual(len(a), 2, a)
        self.assertEqual({x["subject"][-8:] for x in a}, {sha[:8], sha2[:8]})

    def test_not_a_public_repo_never_alerts(self):
        self.ok("config", "public_text_repos", "acme/other")
        self.push(REAL)
        self.ok("export")
        self.assertEqual(self.alerts(), [])


if __name__ == "__main__":
    unittest.main()
