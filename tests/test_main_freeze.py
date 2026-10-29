"""T-0639 (G-021 R-d): the tick freezes merging on a non-PR commit on public main and never syncs ~/factory to it."""
import json
import os
import subprocess
import unittest

from harness import FactoryTestCase, git

NOREPLY = "noreply@github.com"


class MainFreeze(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj", title="zz-test goal")
        e.seed_team(lead="zz-test-lead")
        e.seed_task("T-0001", status="in_review")
        e.seed_agent("zz-test-director", "director")
        e.seed_agent("zz-test-judge", "judge", goal="G-001", task="T-0001")
        self.bare = e.home / "origin.git"
        git("init", "-q", "--bare", "-b", "main", str(self.bare), cwd=e.home)
        git("remote", "add", "origin", str(self.bare), cwd=e.home_dir)
        git("commit", "-q", "--allow-empty", "-m", "base", cwd=e.home_dir)
        git("push", "-q", "-u", "origin", "main", cwd=e.home_dir)
        self.ok("config", "git_push", "1")
        self.ok("config", "public_text_repos", f"{self.bare.parent.name}/origin")
        self.api = []

    def push(self, name, subject, committer):
        """Push a commit to origin main as `committer`; returns its sha."""
        other = self.env.home / "other"
        if not other.exists():
            git("clone", "-q", "-b", "main", str(self.bare), str(other), cwd=self.env.home)
        (other / name).write_text("x\n")
        git("add", name, cwd=other)
        env = {**os.environ, "GIT_COMMITTER_EMAIL": committer, "GIT_COMMITTER_NAME": "c",
               "GIT_AUTHOR_EMAIL": "a@users.noreply.github.com", "GIT_AUTHOR_NAME": "a"}
        subprocess.run(["git", "commit", "-q", "-m", subject], cwd=other, env=env, check=True)
        git("push", "-q", "origin", "main", cwd=other)
        return git("rev-parse", "HEAD", cwd=other)

    def pr(self, n, merged, sha):
        self.set_api(n, {"out": {"merged": merged, "merge_commit_sha": sha}})

    def set_api(self, n, rule):
        self.api = [r for r in self.api if r["match"] != [f"pulls/{n}$"]] + [{"match": [f"pulls/{n}$"], **rule}]
        self.env.gh_state.write_text(json.dumps({"api": self.api}))

    def tick(self, **kw):
        self.env.sql("DELETE FROM config WHERE key='main_check_ts'")  # the 50 s throttle is not under test here
        return self.ok("tick", **kw)

    def live_has(self, name):
        return (self.env.home_dir / name).exists()

    def freeze(self):
        rows = self.env.sql("SELECT value FROM config WHERE key='main_freeze'")
        return rows[0]["value"] if rows else None

    def test_squash_of_merged_pr_passes_and_syncs(self):
        sha = self.push("a.txt", "Add a (#7)", NOREPLY)
        self.pr(7, True, sha)
        self.tick()
        self.assertIsNone(self.freeze())
        self.assertTrue(self.live_has("a.txt"))

    def test_direct_push_freezes_alerts_boss_and_director_at_night_and_is_not_synced(self):
        sha = self.push("leak.txt", "fixture commit", "x@example.com")
        self.tick(extra_env={"FACTORY_NOW": "2026-10-29T03:00:00"})
        self.assertEqual(self.freeze(), sha)
        msgs = self.env.sql("SELECT recipient,kind,subject FROM messages WHERE subject LIKE 'MERGE FREEZE%'")
        self.assertEqual(sorted(m["recipient"] for m in msgs), ["boss", "director"])
        self.assertEqual({m["kind"] for m in msgs}, {"escalation"})
        self.assertEqual(len(self.env.sql("SELECT 1 FROM events WHERE action='main-freeze'")), 1)
        self.assertFalse(self.live_has("leak.txt"))

    def test_alert_is_sent_once(self):
        self.push("leak.txt", "fixture commit", "x@example.com")
        self.tick()
        self.tick()
        self.assertEqual(len(self.env.sql("SELECT 1 FROM messages WHERE subject LIKE 'MERGE FREEZE%'")), 2)  # boss + director, once

    def test_judge_refuses_while_frozen(self):
        sha = self.push("leak.txt", "fixture commit", "x@example.com")
        self.tick()
        p = self.refused(4, "judge", "T-0001", "--merge", "--reason", "r", agent="zz-test-judge")
        self.assertIn(f"non-PR push on main: {sha}", p.stderr)

    def test_judge_not_refused_without_freeze(self):
        p = self.env.run("judge", "T-0001", "--merge", "--reason", "r", agent="zz-test-judge")
        self.assertNotIn("non-PR push on main", p.stderr)

    def test_squash_lookalikes_freeze(self):
        for i, (subject, committer, merged, merge_sha) in enumerate([
                ("Add (#7)", "x@example.com", True, None),   # right subject, wrong committer
                ("Add a thing", NOREPLY, True, None),        # no (#N)
                ("Add (#7)", NOREPLY, False, None),          # PR not merged
                ("Add (#7)", NOREPLY, True, "f" * 40)]):     # merged, but a different commit
            with self.subTest(subject=subject, committer=committer, merged=merged):
                sha = self.push(f"f{i}.txt", subject, committer)
                self.pr(7, merged, merge_sha or sha)
                self.tick()
                self.assertEqual(self.freeze(), sha)
                self.ok("main-unfreeze", "--reason", "verified", agent="zz-test-director")

    def test_gh_failure_neither_freezes_nor_syncs(self):
        sha = self.push("a.txt", "Add a (#7)", NOREPLY)
        self.set_api(7, {"exit": 1, "err": "gh: boom"})
        self.tick()
        self.assertIsNone(self.freeze())
        self.assertFalse(self.live_has("a.txt"))
        self.pr(7, True, sha)
        self.tick()
        self.assertTrue(self.live_has("a.txt"))

    def test_no_sync_at_all_while_frozen(self):
        good = self.push("good.txt", "Good (#1)", NOREPLY)
        self.pr(1, True, good)
        self.push("bad.txt", "direct", "x@example.com")
        self.tick()
        self.assertFalse(self.live_has("good.txt"))  # frozen: the checkout stays where it was until a director verifies main
        self.assertFalse(self.live_has("bad.txt"))
        self.assertNotEqual(git("rev-parse", "main", cwd=self.env.home_dir), good)

    def test_only_director_or_boss_clears_the_freeze(self):
        sha = self.push("leak.txt", "fixture commit", "x@example.com")
        self.tick()
        self.refused(3, "main-unfreeze", "--reason", "r", agent="zz-test-judge")
        self.assertEqual(self.freeze(), sha)
        self.tick()
        self.assertEqual(self.freeze(), sha)  # a tick never clears it
        self.ok("main-unfreeze", "--reason", "main verified by hand", agent="zz-test-director")
        self.assertIsNone(self.freeze())
        p = self.env.run("judge", "T-0001", "--merge", "--reason", "r", agent="zz-test-judge")
        self.assertNotIn("non-PR push on main", p.stderr)

    def test_unfreeze_without_freeze_refuses(self):
        self.refused(1, "main-unfreeze", "--reason", "r", agent="zz-test-director")

    def test_judge_reject_is_allowed_while_frozen(self):
        self.push("leak.txt", "fixture commit", "x@example.com")
        self.tick()
        self.env.seed_pr()
        p = self.env.run("judge", "T-0001", "--reject", "--reason", "r", agent="zz-test-judge")
        self.assertNotIn("non-PR push on main", p.stderr)
        self.assertIn("REJECTED", p.stdout)

    def test_origin_not_public_is_not_checked(self):
        self.ok("config", "public_text_repos", "")
        self.push("leak.txt", "fixture commit", "x@example.com")
        self.tick()
        self.assertIsNone(self.freeze())
        self.assertTrue(self.live_has("leak.txt"))


if __name__ == "__main__":
    unittest.main()
