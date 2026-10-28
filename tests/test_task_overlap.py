import subprocess
import unittest

from harness import FactoryTestCase


def git(*args, cwd):
    subprocess.run(["git", "-c", "user.name=t", "-c", "user.email=t@t", *args], cwd=cwd, check=True,
                   capture_output=True, text=True)


class TaskOverlapTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        self.repo = e.home / "proj-repo"
        (self.repo / "docs").mkdir(parents=True)
        (self.repo / "bin").mkdir()
        for f in ("docs/x.md", "docs/skill-scout.md", "bin/factory"):
            (self.repo / f).write_text("x\n")
        git("init", "-q", "-b", "main", cwd=self.repo)
        git("add", "-A", cwd=self.repo)
        git("commit", "-q", "-m", "init", cwd=self.repo)
        e.seed_project()
        e.sql("UPDATE projects SET repo_path=? WHERE id='zz-test-proj'", (str(self.repo),))
        e.seed_goal(project="zz-test-proj")
        e.seed_goal("G-002", project="zz-test-proj")

    def add(self, spec, goal="G-001", after=None):
        args = ["task", "add", "--goal", goal, "--title", "t", "--spec", spec]
        if after:
            args += ["--after", after]
        p = self.ok(*args)
        self.assertEqual(p.stdout.strip(), self.env.sql("SELECT max(id) m FROM tasks")[0]["m"])
        return p

    def events(self):
        return [r["detail"] for r in self.env.sql("SELECT detail FROM events WHERE action='file-overlap'")]

    def test_same_goal_overlap_warns_on_stderr_only(self):
        first = self.add("edit docs/x.md").stdout.strip()
        p = self.add("also docs/x.md")
        self.assertIn(f"warning: {first} (todo) also names docs/x.md; chain with --after {first}", p.stderr)
        self.assertEqual(self.events(), [first])

    def test_after_the_other_task_is_quiet(self):
        first = self.add("edit docs/x.md").stdout.strip()
        p = self.add("also docs/x.md", after=first)
        self.assertEqual(p.stderr, "")
        self.assertEqual(self.events(), [])

    def test_transitive_order_is_quiet(self):
        a = self.add("edit docs/x.md").stdout.strip()
        b = self.add("unrelated", after=a).stdout.strip()
        p = self.add("also docs/x.md", after=b)
        self.assertEqual(p.stderr, "")

    def test_other_goal_gets_a_note_naming_the_goal(self):
        other = self.add("edit bin/factory", goal="G-002").stdout.strip()
        p = self.add("touch bin/factory")
        self.assertIn(f"note: {other} (goal G-002, todo) also names bin/factory; name it in the spec", p.stderr)
        self.assertNotIn("warning:", p.stderr)

    def test_closed_tasks_do_not_warn(self):
        for status in ("done", "cancelled"):
            self.env.sql("DELETE FROM tasks")
            first = self.add("edit docs/x.md").stdout.strip()
            self.env.sql("UPDATE tasks SET status=? WHERE id=?", (status, first))
            self.assertEqual(self.add("also docs/x.md").stderr, "")

    def test_non_files_do_not_warn(self):
        spec = "see origin/main and factory/t-0001, https://x/y, and nope/missing.txt"
        self.add(spec)
        p = self.add(spec)
        self.assertEqual(p.stderr, "")
        self.assertEqual(self.events(), [])

    def test_new_file_in_tracked_dir_counts(self):
        first = self.add("create `docs/new.md`.").stdout.strip()
        p = self.add("also docs/new.md:12")
        self.assertIn(f"warning: {first}", p.stderr)

    def test_update_spec_checks_and_update_without_spec_does_not(self):
        first = self.add("edit docs/x.md").stdout.strip()
        second = self.add("nothing").stdout.strip()
        p = self.ok("task", "update", second, "--title", "renamed")
        self.assertEqual(p.stderr, "")
        p = self.ok("task", "update", second, "--spec", "now docs/x.md")
        self.assertEqual(p.stdout.strip(), f"{second} updated")
        self.assertIn(f"warning: {first} (todo) also names docs/x.md", p.stderr)
        self.assertEqual(self.events(), [first])

    def test_project_without_repo_path_is_quiet(self):
        self.env.sql("UPDATE projects SET repo_path=NULL")
        self.add("edit docs/x.md")
        self.assertEqual(self.add("also docs/x.md").stderr, "")

    def test_missing_repo_dir_is_quiet(self):
        self.env.sql("UPDATE projects SET repo_path=?", (str(self.env.home / "gone"),))
        self.add("edit docs/x.md")
        self.assertEqual(self.add("also docs/x.md").stderr, "")

    def test_more_than_ten_lines_are_capped(self):
        for _ in range(12):
            self.add("edit docs/x.md", goal="G-002")
        lines = self.add("docs/x.md").stderr.splitlines()
        self.assertEqual(len(lines), 11)
        self.assertEqual(lines[-1], "... and 2 more")

    def test_g009_replay_names_skill_scout_doc(self):
        self.add("docs in docs/skill-scout.md, never new README sections")
        p = self.add("docs/skill-scout.md if behaviour visible")
        self.assertIn("also names docs/skill-scout.md", p.stderr)


    def test_leading_dot_slash_is_normalised(self):
        (self.repo / "roles").mkdir()
        (self.repo / "roles/pm.md").write_text("x\n")
        git("add", "-A", cwd=self.repo)
        git("commit", "-q", "-m", "roles", cwd=self.repo)
        first = self.add("edit ./roles/pm.md").stdout.strip()
        p = self.add("edit roles/pm.md")
        self.assertEqual(p.stderr.count("warning:"), 1)
        self.assertIn(f"warning: {first} (todo) also names roles/pm.md;", p.stderr)

    def test_prefix_and_glob_tokens_are_skipped(self):
        for spec in ("add tests/test_ files", "add tests/test_*.py files"):
            (self.repo / "tests").mkdir(exist_ok=True)
            (self.repo / "tests/keep.py").write_text("x\n")
            git("add", "-A", cwd=self.repo)
            git("commit", "-q", "--allow-empty", "-m", "tests", cwd=self.repo)
            self.add(spec)
            p = self.add(spec)
            self.assertEqual(p.stderr, "", spec)
        self.assertEqual(self.events(), [])

    def test_real_new_file_with_extension_still_counts(self):
        (self.repo / "tests").mkdir()
        (self.repo / "tests/keep.py").write_text("x\n")
        git("add", "-A", cwd=self.repo)
        git("commit", "-q", "-m", "tests", cwd=self.repo)
        first = self.add("create tests/test_brand_new.py").stdout.strip()
        p = self.add("create tests/test_brand_new.py")
        self.assertEqual(p.stderr.count("warning:"), 1)
        self.assertIn(f"warning: {first} (todo) also names tests/test_brand_new.py;", p.stderr)


if __name__ == "__main__":
    unittest.main()
