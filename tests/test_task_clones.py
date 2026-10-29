import json
import shutil
import unittest
import unittest.mock
from pathlib import Path

from harness import FactoryTestCase, git


class TaskCloneTests(FactoryTestCase):
    """task_clones=1: a new coder task dir is a standalone `git clone --shared`; off is today's worktree."""

    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj", title="zz-test clone goal")
        e.seed_agent("zz-test-director", "director")
        self.h = e.home_dir
        self.bare = e.home / "origin.git"
        git("init", "-q", "--bare", "-b", "main", str(self.bare), cwd=e.home)
        git("remote", "add", "origin", str(self.bare), cwd=self.h)
        git("commit", "-q", "--allow-empty", "-m", "base", cwd=self.h)
        git("push", "-q", "-u", "origin", "main", cwd=self.h)
        e.sql("UPDATE projects SET repo_path=? WHERE id='zz-test-proj'", (str(self.h),))
        e.seed_task(goal="G-001", status="in_progress")
        self.marker = e.home / "marker"
        self.wt = self.h / "worktrees" / "G-001-T-0001"

    def hook(self, d, name):
        (d / "hooks").mkdir(exist_ok=True)
        f = d / "hooks" / name
        f.write_text(f"#!/bin/sh\necho ran >> '{self.marker}'\n")
        f.chmod(0o755)

    def prepare(self, task="T-0001", role="coder"):
        with self.env.load_factory() as mod:
            con = mod.db()
            try:
                out = mod.prepare_worktree(con, task, role)
                con.commit()
                return 0, out
            except SystemExit as ex:
                return ex.code, None

    def findings(self, task="T-0001"):
        with self.env.load_factory() as mod:
            con = mod.db()
            return mod.coder_done_findings(con, {"task_id": task}, True)

    def refused_events(self):
        return [json.loads(r["detail"]) for r in self.env.sql("SELECT detail FROM events WHERE action='git-config-refused'")]

    def make_clone(self):
        self.ok("config", "task_clones", "1")
        self.assertEqual(self.prepare()[0], 0)
        return self.wt

    def test_config_validates_zero_or_one_and_defaults_off(self):
        self.assertIn("task_clones = 0", self.ok("config").stdout)
        self.refused(2, "config", "task_clones", "2", agent="zz-test-director")
        self.refused(2, "config", "task_clones", "yes", agent="zz-test-director")
        self.assertIn("task_clones = 0", self.ok("config").stdout)
        self.ok("config", "task_clones", "1", agent="zz-test-director")
        self.assertIn("task_clones = 1", self.ok("config").stdout)

    def test_flag_off_makes_a_linked_worktree(self):
        self.assertEqual(self.prepare()[1], str(self.wt))
        self.assertTrue((self.wt / ".git").is_file())
        self.assertIn(str(self.wt), git("worktree", "list", cwd=self.h))

    def test_flag_on_makes_a_standalone_clone(self):
        wt = self.make_clone()
        self.assertTrue((wt / ".git").is_dir())
        alt = (wt / ".git" / "objects" / "info" / "alternates").read_text().strip()
        self.assertEqual(alt, str(self.h / ".git" / "objects"))
        self.assertEqual(git("remote", "get-url", "origin", cwd=wt), str(self.bare))
        self.assertEqual(git("rev-parse", "--abbrev-ref", "HEAD", cwd=wt), "factory/t-0001")
        self.assertEqual(git("rev-parse", "HEAD", cwd=wt), git("rev-parse", "origin/main", cwd=wt))
        self.assertEqual(git("rev-parse", "origin/main", cwd=wt), git("rev-parse", "refs/remotes/origin/main", cwd=self.h))
        self.assertNotIn(str(wt), git("worktree", "list", cwd=self.h))
        row = self.env.sql("SELECT worktree, branch FROM tasks WHERE id='T-0001'")[0]
        self.assertEqual((row["worktree"], row["branch"]), (str(wt), "factory/t-0001"))

    def test_clone_origin_refs_come_from_the_repo_not_its_local_branches(self):
        git("commit", "-q", "--allow-empty", "-m", "local only", cwd=self.h)  # local main ahead of origin/main
        wt = self.make_clone()
        self.assertEqual(git("rev-parse", "origin/main", cwd=wt), git("rev-parse", "origin/main", cwd=self.h))
        self.assertNotEqual(git("rev-parse", "origin/main", cwd=wt), git("rev-parse", "main", cwd=self.h))

    def test_planted_config_in_clone_refuses_respawn_and_runs_nothing(self):
        wt = self.make_clone()
        script = self.env.home / "evil.sh"
        script.write_text(f"#!/bin/sh\necho ran >> '{self.marker}'\n")
        script.chmod(0o755)
        for key in ("core.fsmonitor", "filter.x.smudge"):
            with self.subTest(key=key):
                self.env.sql("DELETE FROM config WHERE key='last_git_config_refused_ts'")
                git("config", key, str(script), cwd=wt)
                if key.startswith("filter"):
                    git("config", "filter.x.required", "true", cwd=wt)
                self.assertEqual(self.prepare()[0], 4)
                self.assertFalse(self.marker.exists())
                self.assertTrue(any(k.startswith(key) for ev in self.refused_events() for k in ev["keys"]))
                git("config", "--unset-all", key, cwd=wt)
                if key.startswith("filter"):
                    git("config", "--unset", "filter.x.required", cwd=wt)

    def test_config_planted_during_clone_creation_refuses_and_removes_the_clone(self):
        """A template dir's config file lands in the new clone: the guard on the clone runs before any checkout."""
        tpl = self.env.home / "tpl"
        tpl.mkdir()
        script = self.env.home / "evil.sh"
        script.write_text(f"#!/bin/sh\necho ran >> '{self.marker}'\n")
        script.chmod(0o755)
        (tpl / "config").write_text(f"[core]\n\tfsmonitor = {script}\n")
        self.ok("config", "task_clones", "1")
        with unittest.mock.patch.dict("os.environ", {"GIT_TEMPLATE_DIR": str(tpl)}):
            self.assertEqual(self.prepare()[0], 4)
        self.assertFalse(self.marker.exists())
        self.assertFalse(self.wt.exists())
        self.assertEqual(self.env.sql("SELECT worktree FROM tasks WHERE id='T-0001'")[0]["worktree"], None)
        self.assertTrue(any("core.fsmonitor" in k for ev in self.refused_events() for k in ev["keys"]))
        self.assertEqual(self.prepare()[0], 0)  # without the template: the clone is made

    def test_clone_origin_rule_follows_the_tasks_own_project_not_alternates(self):
        """Live-home rule (file protocol never, https only): the clone's origin must be its own task's project URL;
        the worker-writable alternates file cannot pick another project's."""
        wt = self.make_clone()
        other = self.env.home / "other-repo"
        other.mkdir()
        self.env.sql("UPDATE projects SET gh_repo='zz/proj' WHERE id='zz-test-proj'")
        self.env.sql("INSERT INTO projects(id,name,repo_path,gh_repo,base_branch,created) VALUES('zz-other','o',?,'zz/other','main','t')",
                     (str(other),))
        alt = wt / ".git" / "objects" / "info" / "alternates"
        good = alt.read_text()

        def refusal(origin, alternates=good):
            git("config", "remote.origin.url", origin, cwd=wt)
            alt.write_text(alternates)
            with self.env.load_factory() as mod:
                mod.HOME = Path(mod.LIVE_HOME)  # the project repo is the temp home here; the live one is never touched
                mod.git_file_protocol = lambda: "never"
                return mod.git_config_refusal(wt)
        self.assertEqual(refusal("https://github.com/zz/proj.git"), [])
        self.assertEqual(refusal("https://github.com/zz/proj"), [])
        bad = ["remote.origin.url=https://github.com/zz/other.git"]
        self.assertEqual(refusal("https://github.com/zz/other.git"), bad)
        self.assertEqual(refusal("https://github.com/zz/other.git", f"{other}/.git/objects\n"), bad)  # tampered alternates
        self.assertEqual(refusal("https://github.com/zz/other.git", good + f"{other}/.git/objects\n"), bad)
        self.assertEqual(refusal("https://github.com/zz/proj.git", f"{other}/.git/objects\n"),
                         ["remote.origin.url=https://github.com/zz/proj.git"])  # alternates no longer match the task's repo

    def test_clean_clone_respawns_and_switches(self):
        wt = self.make_clone()
        git("checkout", "-q", "--detach", cwd=wt)
        self.assertEqual(self.prepare(), (0, str(wt)))
        self.assertEqual(git("rev-parse", "--abbrev-ref", "HEAD", cwd=wt), "factory/t-0001")
        self.assertEqual(self.refused_events(), [])

    def test_planted_hooks_never_run_from_hq(self):
        wt = self.make_clone()
        for h in ("post-checkout", "reference-transaction", "post-merge", "pre-commit"):
            self.hook(wt / ".git", h)
        (wt / "dirty.txt").write_text("x\n")
        self.assertEqual(self.prepare()[0], 0)  # switch path
        self.findings()  # done guard
        self.assertFalse(self.marker.exists(), "a planted hook ran from HQ")
        git("checkout", "-q", "--detach", cwd=wt)  # control: plain git does run the planted hook
        self.assertTrue(self.marker.exists())

    def test_legacy_worktree_is_kept_with_the_flag_on(self):
        self.assertEqual(self.prepare()[1], str(self.wt))  # flag off: legacy worktree
        self.ok("config", "task_clones", "1")
        self.assertEqual(self.prepare(), (0, str(self.wt)))
        self.assertTrue((self.wt / ".git").is_file())  # not replaced by a clone
        self.assertIn(str(self.wt), git("worktree", "list", cwd=self.h))

    def test_done_guard_findings_match_between_layouts(self):
        self.env.seed_task(tid="T-0002", goal="G-001", status="in_progress")
        self.assertEqual(self.prepare("T-0001")[0], 0)  # worktree
        self.ok("config", "task_clones", "1")
        self.assertEqual(self.prepare("T-0002")[0], 0)  # clone
        self.assertTrue((self.h / "worktrees" / "G-001-T-0002" / ".git").is_dir())

        def norm(f, t):
            d, u, up, ci = f
            return d, u, up and up.replace(f"HEAD {git('rev-parse', 'HEAD', cwd=self.h / 'worktrees' / f'G-001-{t}')[:9]}", "HEAD X").replace(t.lower(), "t"), ci
        for step in ("clean", "dirty", "commit"):
            for t in ("T-0001", "T-0002"):
                wt = self.h / "worktrees" / f"G-001-{t}"
                if step == "dirty":
                    (wt / "a.txt").write_text(t)
                    git("add", "a.txt", cwd=wt)
                    (wt / "u.txt").write_text("u")
                elif step == "commit":
                    git("-c", "user.email=zz@example.invalid", "-c", "user.name=zz", "commit", "-q", "-m", "c", cwd=wt)
            a, b = self.findings("T-0001"), self.findings("T-0002")
            self.assertEqual((a[0], a[1], bool(a[2])), (b[0], b[1], bool(b[2])), step)
            self.assertEqual(norm(a, "T-0001"), norm(b, "T-0002"), step)
            if step == "dirty":
                self.assertEqual((a[0], a[1]), (["a.txt"], ["u.txt"]))
            if step == "commit":
                self.assertIsNotNone(a[2])

    def test_clean_skips_a_clone_and_leaves_it(self):
        wt = self.make_clone()
        self.env.sql("UPDATE tasks SET status='cancelled', pr_url=NULL, branch='factory/t-0001' WHERE id='T-0001'")
        self.env.seed_agent("zz-test-cleaner", "cleaner", goal="G-001")
        for args in (("clean", "--dry-run"), ("clean",)):
            p = self.ok(*args, agent="zz-test-cleaner")
            self.assertIn("per-task clone", p.stdout)
            self.assertNotIn("removed", p.stdout.replace("0 removed", ""))
            self.assertTrue(wt.is_dir())
        self.assertEqual(git("rev-parse", "--abbrev-ref", "HEAD", cwd=wt), "factory/t-0001")

    def test_no_direct_git_calls_outside_the_helper(self):
        src = (Path(__file__).resolve().parent.parent / "bin" / "factory").read_text().splitlines()
        hits = [l.strip() for l in src if '["git"' in l or "['git'" in l]
        self.assertEqual(len(hits), 1, hits)  # only git_argv builds a git command line


if __name__ == "__main__":
    unittest.main()
