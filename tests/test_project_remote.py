import contextlib
import io
import os
import unittest
from pathlib import Path

from harness import FactoryTestCase, git

EXAMPLE_URL = "https://github.com/example-org/example-project.git"


class ProjectRemoteTests(FactoryTestCase):
    """git_config_refusal allows exactly one https remote per registered project, at its realpath repo_path."""

    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project("example-project")
        e.seed_project("other")
        self.root = (e.home / "projects").resolve()
        self.root.mkdir()
        self.clone = self.make_clone("example-project", EXAMPLE_URL)
        e.sql("UPDATE projects SET repo_path=?, gh_repo='example-org/example-project' WHERE id='example-project'", (str(self.clone),))
        e.sql("UPDATE projects SET repo_path=?, gh_repo='example-org/other' WHERE id='other'", (str(self.make_clone("other", "https://github.com/example-org/other.git")),))

    def make_clone(self, name, url):
        d = self.root / name
        d.mkdir()
        git("init", "-q", "-b", "main", cwd=d)
        git("remote", "add", "origin", url, cwd=d, inert_remote=True)
        git("commit", "-q", "--allow-empty", "-m", "base", cwd=d)
        return d

    def refusal(self, repo):
        """git_config_refusal under the LIVE rule (no temp-home bare-path allowance)."""
        with self.env.load_factory() as mod:
            mod.HOME = Path(mod.LIVE_HOME)
            return mod.git_config_refusal(repo)

    def set_url(self, url, repo=None):
        git("config", "remote.origin.url", url, cwd=repo or self.clone)

    def test_registered_project_remote_is_accepted(self):
        self.assertEqual(self.refusal(self.clone), [])
        self.set_url("https://github.com/example-org/example-project")
        self.assertEqual(self.refusal(self.clone), [])

    def test_worktree_of_the_clone_is_accepted(self):
        wt = self.root / "wt"
        git("worktree", "add", "-q", "-b", "factory/t-1", str(wt), cwd=self.clone)
        self.assertEqual(self.refusal(wt), [])

    def test_unregistered_path_is_refused(self):
        stray = self.make_clone("stray", EXAMPLE_URL)
        self.assertEqual(self.refusal(stray), [f"remote.origin.url={EXAMPLE_URL}"])

    def test_symlink_to_unregistered_clone_is_refused(self):
        stray = self.make_clone("stray", EXAMPLE_URL)
        link = self.root / "link"
        link.symlink_to(stray)
        self.assertEqual(self.refusal(link), [f"remote.origin.url={EXAMPLE_URL}"])

    def test_symlinked_registered_repo_path_is_refused(self):
        link = self.root / "reglink"
        link.symlink_to(self.clone)
        self.env.sql("UPDATE projects SET repo_path=? WHERE id='example-project'", (str(link),))
        self.assertEqual(self.refusal(self.clone), [f"remote.origin.url={EXAMPLE_URL}"])

    def test_other_projects_remote_is_refused(self):
        url = "https://github.com/example-org/other.git"
        self.set_url(url)
        self.assertEqual(self.refusal(self.clone), [f"remote.origin.url={url}"])

    def test_bad_urls_are_refused(self):
        for url in ("git@github.com:example-org/example-project.git", "ssh://git@github.com/example-org/example-project.git",
                    f"file://{self.clone}", "http://github.com/example-org/example-project.git",
                    "https://gitlab.com/example-org/example-project.git", "https://github.com/example-org/example-project/extra.git",
                    "https://github.com/example-org/example-project.git/", "https://github.com/example-org/example-project.git ",
                    "https://github.com/example-org/example-project.gitx", "https://user:pw@github.com/example-org/example-project.git",
                    "https://github.com/EXAMPLE-ORG/example-project.git", "/tmp/example-project.git"):
            with self.subTest(url=url):
                self.set_url(url)
                self.assertEqual(self.refusal(self.clone), [f"remote.origin.url={url}"])

    def test_malformed_gh_repo_allows_nothing(self):
        for gh in ("../example-project", "example-org/example-project.git", "example-org", "example-org/dib sley", ""):
            with self.subTest(gh=gh):
                self.env.sql("UPDATE projects SET gh_repo=? WHERE id='example-project'", (gh,))
                self.assertNotEqual(self.refusal(self.clone), [])

    def test_trailing_newline_gh_repo_allows_nothing(self):
        """`$` matches before a trailing newline; the registered gh_repo must fullmatch."""
        self.env.sql("UPDATE projects SET gh_repo=? WHERE id='example-project'", ("example-org/example-project\n",))
        for url in (EXAMPLE_URL + "\n", "https://github.com/example-org/example-project\n", EXAMPLE_URL):
            with self.subTest(url=url):
                self.set_url(url)
                self.assertNotEqual(self.refusal(self.clone), [])

    def test_relative_repo_path_is_refused(self):
        """A relative repo_path would resolve against the caller's cwd, so any clone could match it."""
        self.env.sql("UPDATE projects SET repo_path='example-project' WHERE id='example-project'")
        cwd = os.getcwd()
        os.chdir(self.root)
        try:
            self.assertEqual(self.refusal(self.clone), [f"remote.origin.url={EXAMPLE_URL}"])
        finally:
            os.chdir(cwd)

    def test_symlink_in_any_component_of_repo_path_is_refused(self):
        parent_link = self.root.parent / "plink"
        parent_link.symlink_to(self.root)
        self.env.sql("UPDATE projects SET repo_path=? WHERE id='example-project'", (str(parent_link / "example-project"),))
        self.assertEqual(self.refusal(self.clone), [f"remote.origin.url={EXAMPLE_URL}"])

    def test_blob_gh_repo_or_repo_path_fails_closed(self):
        for col in ("gh_repo", "repo_path"):
            with self.subTest(col=col):
                self.env.sql(f"UPDATE projects SET {col}=CAST('x' AS BLOB) WHERE id='example-project'")
                self.assertNotEqual(self.refusal(self.clone), [])
                self.env.sql("UPDATE projects SET repo_path=?, gh_repo='example-org/example-project' WHERE id='example-project'", (str(self.clone),))

    def test_factory_repo_is_never_loosened_by_its_project_row(self):
        """The factory's own row (gh_repo example-org/factory) must not allow the no-.git URL: factory stays on factory_remote_url."""
        e = self.env
        e.seed_project("factory")
        e.sql("UPDATE config SET value='https://github.com/example-org/factory.git' WHERE key='factory_remote_url'")
        fac = self.make_clone("factory", "https://github.com/example-org/factory.git")
        e.sql("UPDATE projects SET repo_path=?, gh_repo='example-org/factory' WHERE id='factory'", (str(fac),))
        self.assertEqual(self.refusal(fac), [])
        self.set_url("https://github.com/example-org/factory", fac)
        self.assertEqual(self.refusal(fac), ["remote.origin.url=https://github.com/example-org/factory"])

    def test_refresh_runs_the_project_rule(self):
        """cmd_refresh end to end up to the guard: the registered clone gets past it (and stops later, on the PR base),
        an unregistered one is refused with exit 4."""
        e = self.env
        e.seed_goal("G-002", project="example-project")
        e.seed_task(tid="T-0002", goal="G-002", status="in_review")
        wt = self.root / "wt"
        git("worktree", "add", "-q", "-b", "factory/t-0002", str(wt), cwd=self.clone)
        e.sql("UPDATE tasks SET worktree=?, branch='factory/t-0002' WHERE id='T-0002'", (str(wt),))
        pr = {"state": "OPEN", "headRefName": "factory/t-0002", "headRefOid": "0" * 40, "baseRefName": "dev"}
        with e.load_factory() as mod:
            mod.HOME = Path(mod.LIVE_HOME)
            mod.pr_state = lambda url: pr
            args = type("A", (), {"task": "T-0002"})()
            mod.actor_role = lambda con: "lead"
            err = io.StringIO()
            with self.assertRaises(SystemExit) as cm, contextlib.redirect_stderr(err):
                mod.cmd_refresh(args)
            self.assertIn("PR base is dev", err.getvalue())  # past the git-config guard
            e.sql("UPDATE projects SET repo_path=? WHERE id='example-project'", (str(self.make_clone("stray", EXAMPLE_URL)),))
            err = io.StringIO()
            with self.assertRaises(SystemExit) as cm, contextlib.redirect_stderr(err):
                mod.cmd_refresh(args)
            self.assertEqual(cm.exception.code, 4)
            self.assertIn("refresh refused: git config", err.getvalue())

    def test_other_checks_unchanged(self):
        git("config", "core.sshCommand", "x", cwd=self.clone)
        git("config", "include.path", "x", cwd=self.clone)
        self.assertEqual(self.refusal(self.clone), ["core.sshcommand", "include.path"])

    def test_missing_db_fails_closed(self):
        with self.env.load_factory() as mod:
            mod.HOME = Path(mod.LIVE_HOME)
            mod.DB = self.root / "nope.db"
            self.assertEqual(mod.git_config_refusal(self.clone), [f"remote.origin.url={EXAMPLE_URL}"])

    def test_spawn_guard_uses_the_project_rule(self):
        """prepare_worktree (spawn) on an existing coder worktree: the project's clone passes, an unregistered one is refused."""
        e = self.env
        e.seed_goal("G-002", project="example-project")
        e.seed_task(tid="T-0002", goal="G-002", status="in_progress")
        wt = self.root / "wt"
        git("worktree", "add", "-q", "-b", "factory/t-0002", str(wt), cwd=self.clone)
        e.sql("UPDATE tasks SET worktree=?, branch='factory/t-0002' WHERE id='T-0002'", (str(wt),))
        with e.load_factory() as mod:
            mod.HOME = Path(mod.LIVE_HOME)
            con = mod.db()
            self.assertEqual(mod.prepare_worktree(con, "T-0002", "coder"), str(wt))
            self.assertTrue(mod.git_config_guard(con, str(self.clone), always_event=True))  # refresh/judge/clean path
            stray = self.make_clone("stray", EXAMPLE_URL)
            self.assertFalse(mod.git_config_guard(con, str(stray), always_event=True))
            e.sql("UPDATE projects SET repo_path=? WHERE id='example-project'", (str(stray),))
            with self.assertRaises(SystemExit) as cm:
                mod.prepare_worktree(con, "T-0002", "coder")
            self.assertEqual(cm.exception.code, 4)


if __name__ == "__main__":
    unittest.main()
