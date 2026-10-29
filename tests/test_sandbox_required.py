"""G-010 T-0511: the tasks.sandbox_required flag. spawn refuses a flagged task without --sandbox; every other spawn path
(tick dispatch, queue dequeue, relaunch) forces the sandbox; the flag is lead/director/boss/owner-only."""
import json
import re
import unittest

from harness import FactoryTestCase
from test_sandbox_spawn import prep, ROLE_NAMES

T = "T-0001"
PROJ = "zz-proj"
TEAM, LEAD = "g-001-a", "zz-lead"


class Base(FactoryTestCase):
    def setUp(self):
        super().setUp()
        prep(self.env)  # tokens, stubs, goal G-001, team g-001-a (lead zz-lead), worktrees w-<role>
        self.env.seed_agent(LEAD, "lead", goal="G-001", team=TEAM, status="exited")

    def task(self, role="reviewer", flag=1, worktree=True, status="todo", tid=T):
        """A specced task of goal G-001 in a project with a repo path; worktree = w-<role> from prep()."""
        self.env.sql("INSERT OR IGNORE INTO projects(id,name,repo_path,created) VALUES(?,?,?,'t')",
                     (PROJ, PROJ, str(self.env.home / "repo")))
        self.env.sql("UPDATE goals SET project_id=? WHERE id='G-001'", (PROJ,))
        self.env.seed_task(tid, role=role, status=status, pr_url=None, team=TEAM)
        wt = str(self.env.home_dir / "worktrees" / f"w-{role}") if worktree else None
        self.env.sql("UPDATE tasks SET sandbox_required=?, worktree=? WHERE id=?", (flag, wt, tid))

    def row(self, tid=T):
        return self.env.sql("SELECT * FROM tasks WHERE id=?", (tid,))[0]

    def agent(self, name):
        rows = self.env.sql("SELECT * FROM agents WHERE name=?", (name,))
        return rows[0] if rows else None

    def events(self, action):
        return self.env.sql("SELECT entity_id, detail FROM events WHERE action=? ORDER BY id", (action,))

    def mux(self, *words):
        return [c["argv"] for c in self.env.calls("herdr") if c["argv"][:len(words)] == list(words)]

    def spawn(self, role, *extra, name=None, task=T):
        return self.env.run("spawn", role, "--name", name or f"zz-{role}", "--goal", "G-001", "--team", TEAM,
                            "--task", task, *extra)

    def placing(self, since):
        """Calls since `since` that place a pane or start an agent (the read-only `agent list` is not one)."""
        bad = (["pane", "split"], ["tab", "create"], ["agent", "start"], ["pane", "run"], ["workspace", "create"])
        return [c["argv"] for c in self.env.calls("herdr")[since:] if c["argv"][:2] in bad]

    def worktree_dirs(self):
        return sorted(p.name for p in (self.env.home_dir / "worktrees").iterdir())


class MigrationTests(Base):
    def test_old_db_gets_the_column_once_with_default_0(self):
        self.task(flag=0)
        con = self.env.db()
        try:
            con.execute("ALTER TABLE tasks DROP COLUMN sandbox_required")
            con.commit()
        finally:
            con.close()
        for _ in range(2):
            self.ok("task", "show", T)
        info = [r for r in self.env.sql("PRAGMA table_info(tasks)") if r["name"] == "sandbox_required"]
        self.assertEqual(len(info), 1)
        self.assertEqual(self.row()["sandbox_required"], 0)


class CliTests(Base):
    def test_show_prints_the_line_only_while_the_flag_is_set(self):
        self.task(flag=0)
        self.assertNotIn("sandbox_required", self.ok("task", "show", T).stdout)
        self.ok("task", "update", T, "--sandbox-required", "1", agent=LEAD)
        self.assertIn("\nsandbox_required: 1\n", self.ok("task", "show", T).stdout)
        self.ok("task", "update", T, "--sandbox-required", "0", agent=LEAD)
        self.assertNotIn("sandbox_required", self.ok("task", "show", T).stdout)

    def test_only_lead_director_boss_owner_may_set_it(self):
        self.task(flag=1)
        for role in ("pm", "coder", "reviewer"):
            self.env.seed_agent(f"zz-{role}-x", role, goal="G-001", team=TEAM)
            self.refused(3, "task", "update", T, "--sandbox-required", "0", agent=f"zz-{role}-x")
            self.assertEqual(self.row()["sandbox_required"], 1, role)
        self.env.sql("UPDATE agents SET status='live' WHERE name=?", (LEAD,))
        self.ok("task", "update", T, "--sandbox-required", "0", agent=LEAD)
        self.assertEqual(self.row()["sandbox_required"], 0)
        self.assertEqual([e["detail"] for e in self.events("sandbox-required")], ["0"])

    def test_task_add_flag_and_role_check(self):
        self.env.seed_agent("zz-pm", "pm", goal="G-001", team=TEAM)
        add = ["task", "add", "--goal", "G-001", "--title", "t", "--spec", "s"]
        tid = self.ok(*add, "--role", "coder", "--sandbox-required", "1", agent="zz-pm").stdout.strip()
        self.assertIn("sandbox_required: 1", self.ok("task", "show", tid).stdout)
        n = len(self.env.sql("SELECT id FROM tasks"))
        p = self.refused(2, *add, "--role", "judge", "--sandbox-required", "1", agent="zz-pm")
        self.assertIn("judge cannot be sandboxed; allowed: coder, qa, reviewer", p.stderr)
        self.assertEqual(len(self.env.sql("SELECT id FROM tasks")), n)

    def test_update_to_1_on_a_judge_task_is_refused(self):
        self.task(role="judge", flag=0)
        self.refused(2, "task", "update", T, "--sandbox-required", "1", agent=LEAD)
        self.assertEqual(self.row()["sandbox_required"], 0)


class ManualSpawnTests(Base):
    def test_refused_without_sandbox_and_leaves_nothing_behind(self):
        for role in ("reviewer", "qa", "coder"):
            self.task(role=role, tid=T) if role == "reviewer" else self.env.sql(
                "UPDATE tasks SET role=?, worktree=? WHERE id=?",
                (role, str(self.env.home_dir / "worktrees" / f"w-{role}"), T))
            calls, dirs, agents = len(self.env.calls("herdr")), self.worktree_dirs(), self.env.sql("SELECT name FROM agents")
            p = self.spawn(role, name=ROLE_NAMES[role])
            self.assertEqual(p.returncode, 3, (role, p.stderr))
            self.assertIn(f"{T} is sandbox_required: rerun with --sandbox", p.stderr)
            self.assertEqual(len(self.env.calls("herdr")), calls, role)
            self.assertEqual(self.worktree_dirs(), dirs)
            self.assertEqual(self.env.sql("SELECT name FROM agents"), agents)
        self.assertEqual(len(self.events("sandbox-required-refused")), 3)

    def test_refused_even_when_the_role_is_in_sandbox_roles(self):
        self.task()
        self.ok("config", "sandbox_roles", "reviewer")
        self.assertEqual(self.spawn("reviewer", name="zz-reviewer").returncode, 3)

    def test_allowed_with_sandbox_in_the_task_worktree(self):
        self.task()
        p = self.spawn("reviewer", "--sandbox", name="zz-reviewer")
        self.assertEqual(p.returncode, 0, p.stderr)
        row = self.agent("zz-reviewer")
        self.assertEqual(row["status"], "live")
        self.assertTrue(json.loads(row["spawn_args"])["sandbox"])
        self.assertEqual(row["cwd"], self.row()["worktree"])
        self.assertNotEqual(row["cwd"], str(self.env.home_dir))

    def test_no_worktree_is_a_cwd_refusal_never_the_main_checkout(self):
        self.task(worktree=False)
        calls = len(self.env.calls("herdr"))
        p = self.spawn("reviewer", "--sandbox", name="zz-reviewer")
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn("sandbox refused", p.stderr)
        self.assertEqual(self.placing(calls), [])
        self.assertEqual(self.env.sql("SELECT name FROM agents WHERE cwd=? AND status='live'", (str(self.env.home_dir),)), [])

    def test_judge_spawn_is_not_affected_by_the_flag(self):
        self.task(role="coder")
        p = self.spawn("judge", name="zz-judge")
        self.assertNotIn("sandbox_required", p.stderr)
        row = self.agent("zz-judge")
        self.assertIsNotNone(row, p.stderr)
        self.assertNotIn("sandbox_optin", json.loads(row["spawn_args"]))

    def test_unflagged_task_spawns_as_before(self):
        self.task(flag=0)
        p = self.spawn("reviewer", name="zz-reviewer")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertNotIn("sandbox", json.loads(self.agent("zz-reviewer")["spawn_args"]))


class AutoStartTests(Base):
    def park(self):
        self.env.sql("UPDATE teams SET lead_agent=?, parked_at='2026-01-01T00:00:00+00:00' WHERE id=?", (LEAD, TEAM))
        self.ok("config", "lead_parking", "1")
        self.ok("config", "lead_less_dispatch", "1")

    def test_tick_dispatch_starts_the_worker_sandboxed_in_the_task_worktree(self):
        self.task()
        self.park()
        self.ok("tick")
        row = self.agent("reviewer-t-0001")
        self.assertEqual(row["status"], "live")
        args = json.loads(row["spawn_args"])
        self.assertTrue(args["sandbox_optin"] and args["sandbox"])
        self.assertEqual(row["cwd"], self.row()["worktree"])

    def test_queued_row_keeps_the_optin_and_the_dequeue_starts_it_sandboxed(self):
        self.task()
        self.env.set_herdr(fail=True)
        self.assertIn("QUEUED", self.ok("spawn", "reviewer", "--name", "zz-q", "--goal", "G-001", "--team", TEAM,
                                        "--task", T, "--sandbox").stdout)
        self.assertTrue(json.loads(self.agent("zz-q")["spawn_args"])["sandbox_optin"])
        self.env.set_herdr(fail=False)
        self.ok("tick")
        row = self.agent("zz-q")
        self.assertEqual(row["status"], "live")
        self.assertTrue(json.loads(row["spawn_args"])["sandbox"])

    def test_a_row_queued_before_the_flag_was_set_is_started_sandboxed(self):
        self.task(flag=0)
        self.env.set_herdr(fail=True)
        self.assertIn("QUEUED", self.spawn("reviewer", name="zz-q").stdout)
        self.assertNotIn("sandbox_optin", json.loads(self.agent("zz-q")["spawn_args"]))
        self.env.sql("UPDATE tasks SET sandbox_required=1 WHERE id=?", (T,))
        self.env.set_herdr(fail=False)
        self.ok("tick")
        row = self.agent("zz-q")
        self.assertEqual(row["status"], "live")
        args = json.loads(row["spawn_args"])
        self.assertTrue(args["sandbox_optin"] and args["sandbox"])
        self.assertEqual(row["cwd"], self.row()["worktree"])

    def test_dispatch_with_a_failed_preflight_never_starts_unsandboxed(self):
        self.task()
        self.park()
        (self.env.user_home / ".config" / "factory" / "worker-oauth-token").unlink()
        calls = len(self.env.calls("herdr"))
        self.ok("tick")
        self.assertEqual(self.placing(calls), [])
        row = self.agent("reviewer-t-0001")
        self.assertNotEqual(row["status"] if row else None, "live")
        self.assertTrue(self.events("sandbox-refused"))


class RelaunchTests(Base):
    def test_flagged_task_relaunch_refused_before_the_old_pane_closes(self):
        self.task(flag=0)
        self.assertEqual(self.spawn("reviewer", name="zz-reviewer").returncode, 0)
        self.assertNotIn("sandbox_optin", json.loads(self.agent("zz-reviewer")["spawn_args"]))
        self.env.sql("UPDATE tasks SET sandbox_required=1 WHERE id=?", (T,))
        (self.env.stub_bin / "unshare").write_text("#!/bin/sh\nexit 1\n")
        p = self.env.run("relaunch", "zz-reviewer", "--force")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("sandbox refused", p.stderr)
        self.assertEqual(self.mux("pane", "close"), [])
        self.assertEqual(self.agent("zz-reviewer")["status"], "live")

    def test_flagged_task_relaunch_with_a_passing_preflight_is_sandboxed(self):
        self.task(flag=0)
        self.assertEqual(self.spawn("reviewer", name="zz-reviewer").returncode, 0)
        self.env.sql("UPDATE tasks SET sandbox_required=1 WHERE id=?", (T,))
        self.ok("relaunch", "zz-reviewer", "--force")
        args = json.loads(self.agent("zz-reviewer")["spawn_args"])
        self.assertTrue(args["sandbox_optin"] and args["sandbox"])


if __name__ == "__main__":
    unittest.main()
