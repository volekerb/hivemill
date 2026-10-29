import unittest

from harness import FactoryTestCase, git

DIRECTOR = "zz-test-director"
NOREPLY = "11260070+volekerb@users.noreply.github.com"


class ProjectUpdateLocationTests(FactoryTestCase):
    """`factory project update --gh/--repo` (T-0550): director/owner only, validated, with an old -> new event."""

    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_agent(DIRECTOR, "director")
        e.seed_agent("zz-test-coder", "coder")
        e.seed_agent("zz-test-boss", "boss")
        self.repo = e.home / "clone"
        self.repo.mkdir()
        git("init", "-q", "-b", "main", cwd=self.repo)

    def row(self):
        return self.env.sql("SELECT gh_repo, repo_path FROM projects WHERE id='zz-test-proj'")[0]

    def events(self):
        return [r["detail"] for r in self.env.sql(
            "SELECT detail FROM events WHERE entity='project' AND action='update' ORDER BY id")]

    def test_sets_gh_and_repo_and_records_events(self):
        old = self.row()
        self.ok("project", "update", "zz-test-proj", "--gh", "example-org/example-repo", "--repo", str(self.repo), agent=DIRECTOR)
        self.assertEqual(self.row()["gh_repo"], "example-org/example-repo")
        self.assertEqual(self.row()["repo_path"], str(self.repo))
        self.assertIn(f"gh_repo: {old['gh_repo']!r} -> 'example-org/example-repo'", self.events())
        self.assertIn(f"repo_path: {old['repo_path']!r} -> {str(self.repo)!r}", self.events())

    def test_bad_gh_shape_is_refused(self):
        before = self.row()
        for bad in ("noslash", "a/b/c", "a b/c", "a/", "/b", "a/b\n"):
            self.refused(2, "project", "update", "zz-test-proj", "--gh", bad, agent=DIRECTOR)
        self.assertEqual(dict(self.row()), dict(before))
        self.assertEqual(self.events(), [])

    def test_repo_must_exist_and_be_a_git_repo(self):
        before = self.row()
        plain = self.env.home / "plain"
        plain.mkdir()
        self.refused(2, "project", "update", "zz-test-proj", "--repo", str(plain), agent=DIRECTOR)
        self.refused(2, "project", "update", "zz-test-proj", "--repo", str(self.env.home / "missing"), agent=DIRECTOR)
        self.assertEqual(dict(self.row()), dict(before))

    def test_non_director_is_refused(self):
        before = self.row()
        for who in ("zz-test-coder", "zz-test-boss"):
            self.refused(3, "project", "update", "zz-test-proj", "--gh", "example-org/example-repo", agent=who)
            self.refused(3, "project", "update", "zz-test-proj", "--repo", str(self.repo), agent=who)
        self.assertEqual(dict(self.row()), dict(before))
        self.assertEqual(self.events(), [])

    def test_empty_update_names_the_new_flags(self):
        p = self.refused(1, "project", "update", "zz-test-proj", agent=DIRECTOR)
        self.assertIn("--gh", p.stderr + p.stdout)


class GitConfigIdentityTests(FactoryTestCase):
    """git_config_refusal allows repo-local user.name and a noreply user.email, nothing else (T-0550)."""

    def setUp(self):
        super().setUp()
        self.h = self.env.home_dir
        git("commit", "-q", "--allow-empty", "-m", "base", cwd=self.h)

    def refusal(self):
        with self.env.load_factory() as mod:
            return mod.git_config_refusal(self.h)

    def test_name_and_noreply_email_are_allowed(self):
        git("config", "user.name", "someone", cwd=self.h)
        git("config", "user.email", NOREPLY, cwd=self.h)
        self.assertEqual(self.refusal(), [])

    def test_other_email_is_refused(self):
        git("config", "user.name", "someone", cwd=self.h)
        for bad in ("someone" + "@" + "example.invalid", "volekerb" + "@users.noreply.github.com.evil.io", "noreply"):
            git("config", "user.email", bad, cwd=self.h)
            self.assertEqual(self.refusal(), ["user.email"], bad)

    def test_include_is_still_refused(self):
        git("config", "user.name", "someone", cwd=self.h)
        git("config", "user.email", NOREPLY, cwd=self.h)
        git("config", "include.path", "/nonexistent", cwd=self.h)
        self.assertEqual(self.refusal(), ["include.path"])


if __name__ == "__main__":
    unittest.main()
