"""T-1028: config idle_coder_reap (off|dry-run|on) + idle_coder_reap_s: the tick stops a coder the host reports idle/done
for over the threshold when its task has a PR, its tree is clean, its HEAD is the pushed PR head, it has no unread mail
and nothing runs in its pane. Real tick as a subprocess, temp FACTORY_HOME, real git worktree + bare origin, herdr and
gh stubs. The clock is FACTORY_NOW: tick 1 starts the idle clock, tick 2 runs 1000 s later (threshold 900)."""
import json
import unittest

from harness import PR_URL, FactoryTestCase, git

T0 = "2026-03-01T10:00:00+00:00"
T1 = "2026-03-01T10:16:40+00:00"   # T0 + 1000 s
T_SHORT = "2026-03-01T10:05:00+00:00"  # T0 + 300 s
BRANCH = "factory/t-0001"


class Base(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        base = e.home / "git"
        base.mkdir()
        self.origin, self.wt = base / "origin.git", base / "wt"
        git("init", "-q", "--bare", "-b", "main", str(self.origin), cwd=base)
        git("clone", "-q", str(self.origin), str(self.wt), cwd=base)
        git("config", "user.email", "zz-test@example.invalid", cwd=self.wt)
        git("config", "user.name", "zz-test", cwd=self.wt)
        (self.wt / "a.txt").write_text("a\n")
        git("add", "a.txt", cwd=self.wt)
        git("commit", "-q", "-m", "a", cwd=self.wt)
        git("push", "-q", "origin", f"HEAD:{BRANCH}", cwd=self.wt)
        e.seed_goal()
        e.seed_task(status="in_review")
        e.sql("UPDATE tasks SET worktree=?, branch=? WHERE id='T-0001'", (str(self.wt), BRANCH))
        e.seed_pr(url=PR_URL, sha=git("rev-parse", "HEAD", cwd=self.wt))
        self.coder("zz-test-coder", "p-1")
        self.run_ok("config", "idle_coder_reap", "on")

    def run_ok(self, *args):
        p = self.env.run(*args)
        self.assertEqual(p.returncode, 0, p.stderr)
        return p

    def coder(self, name, pane, task="T-0001", role="coder"):
        self.env.seed_agent(name, role, "G-001", None, task, pane_id=pane)
        self.env.sql("UPDATE agents SET spawn_args='{}' WHERE name=?", (name,))

    def host(self, *specs):
        """specs: (name, pane, agent_status[, foreground process])."""
        self.env.set_herdr(
            workspaces=[{"workspace_id": "w1", "label": "x"}],
            panes=[{"pane_id": s[1], "tab_id": "t1", "workspace_id": "w1", "fg": s[3] if len(s) > 3 else "claude"}
                   for s in specs],
            agents=[{"name": s[0], "agent": "claude", "pane_id": s[1], "agent_status": s[2],
                     "agent_session": {"value": "s-" + s[0]}} for s in specs])

    def tick(self, when):
        self.env.sql("DELETE FROM throttle WHERE key NOT LIKE 'idlereap%'")
        p = self.env.run("tick", extra_env={"FACTORY_NOW": when})
        self.assertEqual(p.returncode, 0, p.stderr)

    def two_ticks(self, second=T1):
        self.tick(T0)
        self.tick(second)

    def events(self, action):
        return [dict(r) for r in self.env.sql("SELECT entity_id, detail FROM events WHERE action=?", (action,))]

    def stop_orders(self, name="zz-test-coder"):
        return [dict(r) for r in self.env.sql("SELECT subject, sender FROM messages WHERE recipient=? AND subject='STOP'", (name,))]

    def stop_flag(self, name="zz-test-coder"):
        return self.env.sql("SELECT stop_requested_at FROM agents WHERE name=?", (name,))[0][0]

    def assert_not_stopped(self, name="zz-test-coder"):
        self.assertEqual(self.stop_orders(name), [])
        self.assertIsNone(self.stop_flag(name))
        self.assertEqual(self.events("idle-coder-stopped"), [])


class Stops(Base):
    def test_idle_past_threshold_clean_pushed_pr_is_stopped_once_with_note(self):
        for status in ("idle", "done"):
            with self.subTest(status=status):
                self.env.sql("DELETE FROM throttle")
                self.env.sql("DELETE FROM messages")
                self.env.sql("DELETE FROM events")
                self.env.sql("UPDATE agents SET stop_requested_at=NULL WHERE name='zz-test-coder'")
                self.host(("zz-test-coder", "p-1", status))
                self.two_ticks()
                self.assertEqual(len(self.stop_orders()), 1)
                self.assertIsNotNone(self.stop_flag())
                self.assertEqual(len(self.events("idle-coder-stopped")), 1)
                notes = [r for r in self.events("note") if r["entity_id"] == "T-0001"]
                self.assertEqual(len(notes), 1)
                self.assertIn("tick stopped idle coder zz-test-coder", notes[0]["detail"])
                self.tick(T1)  # already stopping: no second order
                self.assertEqual(len(self.stop_orders()), 1)

    def test_in_progress_task_with_pr_is_stopped_too(self):
        self.env.sql("UPDATE tasks SET status='in_progress' WHERE id='T-0001'")
        self.host(("zz-test-coder", "p-1", "idle"))
        self.two_ticks()
        self.assertEqual(len(self.stop_orders()), 1)

    def test_at_most_three_per_tick(self):
        specs = []
        for i in range(2, 6):
            self.env.seed_task(f"T-000{i}", "G-001", status="in_review")
            self.env.sql("UPDATE tasks SET worktree=?, branch=? WHERE id=?", (str(self.wt), BRANCH, f"T-000{i}"))
            self.coder(f"zz-test-coder{i}", f"p-{i}", task=f"T-000{i}")
        names = ["zz-test-coder"] + [f"zz-test-coder{i}" for i in range(2, 6)]
        self.env.sql("UPDATE tasks SET pr_url=? WHERE pr_url IS NOT NULL", (PR_URL,))
        self.host(*[(n, f"p-{i + 1}", "idle") for i, n in enumerate(names)])
        self.two_ticks()
        self.assertEqual(len(self.events("idle-coder-stopped")), 3)
        self.tick(T1)  # the remaining two go on the next tick
        self.assertEqual(len(self.events("idle-coder-stopped")), 5)


class NotStopped(Base):
    def idle_then_check(self, second=T1):
        self.host(("zz-test-coder", "p-1", "idle"))
        self.two_ticks(second)
        self.assert_not_stopped()

    def test_unread_mail(self):
        self.env.sql("INSERT INTO messages(sender,recipient,kind,subject,created) VALUES('x','zz-test-coder','request','hi','t')")
        self.idle_then_check()

    def test_dirty_tree(self):
        (self.wt / "a.txt").write_text("changed\n")
        self.idle_then_check()

    def test_untracked_file(self):
        (self.wt / "new.txt").write_text("x\n")
        self.idle_then_check()

    def test_unpushed_commit(self):
        (self.wt / "b.txt").write_text("b\n")
        git("add", "b.txt", cwd=self.wt)
        git("commit", "-q", "-m", "b", cwd=self.wt)
        self.idle_then_check()

    def test_no_pr(self):
        self.env.sql("UPDATE tasks SET pr_url=NULL WHERE id='T-0001'")
        self.idle_then_check()

    def test_task_not_in_review_or_progress(self):
        self.env.sql("UPDATE tasks SET status='todo' WHERE id='T-0001'")
        self.idle_then_check()

    def test_under_threshold(self):
        self.idle_then_check(T_SHORT)

    def test_shell_running_in_pane(self):
        self.host(("zz-test-coder", "p-1", "idle", "sleep"))
        self.two_ticks()
        self.assert_not_stopped()

    def test_working_agent_resets_the_clock(self):
        self.host(("zz-test-coder", "p-1", "idle"))
        self.tick(T0)
        self.host(("zz-test-coder", "p-1", "working"))
        self.tick("2026-03-01T10:10:00+00:00")
        self.host(("zz-test-coder", "p-1", "idle"))
        self.tick(T1)  # idle again only since 10:16:40 minus nothing: the clock restarted here
        self.assert_not_stopped()

    def test_flag_off(self):
        self.run_ok("config", "idle_coder_reap", "off")
        self.idle_then_check()

    def test_judge_and_reviewer_never_stopped(self):
        for role in ("judge", "reviewer", "qa"):
            self.env.sql("UPDATE agents SET role=? WHERE name='zz-test-coder'", (role,))
            self.env.sql("DELETE FROM throttle")
            self.host(("zz-test-coder", "p-1", "idle"))
            self.two_ticks()
            self.assert_not_stopped()

    def test_pr_head_differs_from_local_head(self):
        self.env.seed_pr(url=PR_URL, sha="0" * 40)
        self.idle_then_check()


class DryRun(Base):
    def test_dry_run_logs_once_and_stops_nothing(self):
        self.run_ok("config", "idle_coder_reap", "dry-run")
        self.host(("zz-test-coder", "p-1", "idle"))
        self.two_ticks()
        self.tick(T1)
        self.assert_not_stopped()
        ev = self.events("idle-coder-would-stop")
        self.assertEqual(len(ev), 1)
        self.assertEqual(json.loads(ev[0]["detail"])["task"], "T-0001")
        self.assertEqual([c for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "send-keys"]], [])


if __name__ == "__main__":
    unittest.main()
