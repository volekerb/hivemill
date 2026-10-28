"""T-0075/T-0094: the tick dequeues by role class (drain first), then aged goal priority, then queued_at, then name;
`factory dequeue` cancels a QUEUED spawn."""
import json
import sys
import time
import datetime as dt
import unittest

from harness import FACTORY, FactoryTestCase


def at(n, mins=0):
    """A queued_at relative to now (aging reads the real clock): n orders rows of the same age, mins ages them."""
    t = dt.datetime.now().astimezone() - dt.timedelta(minutes=mins, seconds=100 - n)
    return t.isoformat(timespec="seconds")


class DequeueBase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal("G-001")
        e.seed_goal("G-003")
        e.sql("UPDATE goals SET priority=3 WHERE id='G-003'")
        e.sql("UPDATE goals SET priority=1 WHERE id='G-001'")
        e.seed_team("g-001-a", lead="zz-lead")
        e.seed_team("g-001-b", lead="zz-lead-b")
        e.seed_task(status="todo", pr_url=None, team="g-001-a")
        e.seed_agent("zz-director", "director", goal="G-001")
        e.seed_agent("zz-lead", "lead", goal="G-001", team="g-001-a")
        e.seed_agent("zz-lead-b", "lead", goal="G-001", team="g-001-b")
        e.seed_agent("zz-coder", "coder", goal="G-001", team="g-001-a")

    def queue(self, name, role, goal, at, team=None):
        """Queue a spawn while herdr is unreachable (the gate is closed), then pin its queued_at."""
        self.env.set_herdr(fail=True)
        args = ["spawn", role, "--name", name]
        if goal:
            args += ["--goal", goal]
        if team:
            args += ["--team", team]
        self.assertIn("QUEUED", self.ok(*args).stdout)
        self.env.sql("UPDATE agents SET queued_at=? WHERE name=?", (at, name))
        self.env.set_herdr(fail=False)

    def status(self, name):
        return self.env.sql("SELECT status FROM agents WHERE name=?", (name,))[0]["status"]

    def starts(self):
        return [c["argv"] for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]]

    def tick_one(self):
        self.ok("config", "max_live_agents", "1")
        self.ok("tick")


class OrderTests(DequeueBase):
    def test_p1_pm_beats_earlier_p3_worker(self):
        self.queue("zz-c3", "pm", "G-003", at(1))
        self.queue("zz-p1", "pm", "G-001", at(2))
        self.tick_one()
        self.assertEqual((self.status("zz-p1"), self.status("zz-c3")), ("live", "queued"))
        self.assertEqual(len(self.starts()), 1)

    def test_lead_beats_p1_worker(self):
        self.queue("zz-p1", "pm", "G-001", at(1))
        self.queue("zz-ld", "lead", "G-003", at(2), team="g-001-a")
        self.tick_one()
        self.assertEqual((self.status("zz-ld"), self.status("zz-p1")), ("live", "queued"))

    def test_judge_beats_p1_non_judge(self):
        self.queue("zz-p1", "pm", "G-001", at(1))
        self.queue("zz-jd", "judge", "G-003", at(2))
        self.tick_one()
        self.assertEqual((self.status("zz-jd"), self.status("zz-p1")), ("live", "queued"))

    def test_same_priority_falls_back_to_queued_at(self):
        self.queue("zz-late", "pm", "G-001", at(9))
        self.queue("zz-early", "pm", "G-001", at(1))
        self.tick_one()
        self.assertEqual((self.status("zz-early"), self.status("zz-late")), ("live", "queued"))

    def test_null_goal_counts_as_priority_2(self):
        self.queue("zz-p3", "pm", "G-003", at(1))
        self.queue("zz-none", "pm", None, at(2))
        self.tick_one()
        self.assertEqual((self.status("zz-none"), self.status("zz-p3")), ("live", "queued"))

    # T-0094: the old 4-class order (director, lead, judge, rest) is now 6 classes. The tests above are unchanged except
    # that their fixed 2026-01-01 queued_at values became at(n), because aging reads the real clock.
    def test_p3_reviewer_beats_p1_coder(self):
        self.queue("zz-co", "coder", "G-001", at(1))
        self.queue("zz-rv", "reviewer", "G-003", at(2))
        self.tick_one()
        self.assertEqual((self.status("zz-rv"), self.status("zz-co")), ("live", "queued"))

    def test_qa_beats_coder(self):
        self.queue("zz-co", "coder", "G-001", at(1))
        self.queue("zz-qa", "qa", "G-003", at(2))
        self.tick_one()
        self.assertEqual((self.status("zz-qa"), self.status("zz-co")), ("live", "queued"))

    def test_judge_beats_reviewer_beats_qa(self):
        self.queue("zz-qa", "qa", "G-001", at(1))
        self.queue("zz-rv", "reviewer", "G-001", at(2))
        self.queue("zz-jd", "judge", "G-003", at(3))
        self.env.sql("UPDATE agents SET status='done' WHERE name IN ('zz-lead','zz-lead-b','zz-coder','zz-director')")
        self.ok("config", "max_live_agents", "1")
        for expect in ("zz-jd", "zz-rv", "zz-qa"):
            self.ok("tick")
            self.assertEqual(self.status(expect), "live", expect)
            self.env.sql("UPDATE agents SET status='done' WHERE name=?", (expect,))

    def test_goal_priority_decides_within_class(self):
        self.queue("zz-r3", "reviewer", "G-003", at(1))
        self.queue("zz-r1", "reviewer", "G-001", at(2))
        self.tick_one()
        self.assertEqual((self.status("zz-r1"), self.status("zz-r3")), ("live", "queued"))

    def test_aged_p3_reviewer_beats_new_p1_reviewer(self):
        self.queue("zz-old", "reviewer", "G-003", at(1, mins=91))
        self.queue("zz-new", "reviewer", "G-001", at(2))
        self.tick_one()
        self.assertEqual((self.status("zz-old"), self.status("zz-new")), ("live", "queued"))

    def test_young_p3_reviewer_does_not_age_past_p1(self):
        self.queue("zz-old", "reviewer", "G-003", at(1, mins=29))
        self.queue("zz-new", "reviewer", "G-001", at(2))
        self.tick_one()
        self.assertEqual((self.status("zz-new"), self.status("zz-old")), ("live", "queued"))

    def test_aged_p3_coder_still_loses_to_any_reviewer(self):
        self.queue("zz-co", "coder", "G-003", at(1, mins=300))
        self.queue("zz-rv", "reviewer", "G-003", at(2))
        self.tick_one()
        self.assertEqual((self.status("zz-rv"), self.status("zz-co")), ("live", "queued"))

    # T-0183: aging never erases priority (see queue_order_key)
    def first(self, *names):
        """Queue nothing more; tick once; the first name must be live and the rest still queued."""
        self.tick_one()
        self.assertEqual([self.status(n) for n in names], ["live"] + ["queued"] * (len(names) - 1))

    def test_aged_p1_beats_older_p3(self):  # (c) P1 waited 30 min, the P3 was queued 2 h before it
        self.queue("zz-p3", "reviewer", "G-003", at(1, mins=180))
        self.queue("zz-p1", "reviewer", "G-001", at(2, mins=31))
        self.first("zz-p1", "zz-p3")

    def test_aged_p1_beats_older_p2(self):  # (c) for P2
        self.env.seed_goal("G-002")
        self.queue("zz-p2", "reviewer", "G-002", at(1, mins=180))
        self.queue("zz-p1", "reviewer", "G-001", at(2, mins=31))
        self.first("zz-p1", "zz-p2")

    def test_p1_waited_30min_beats_p3_waited_3h_p1_far_older_too(self):
        self.queue("zz-p3", "reviewer", "G-003", at(1, mins=235))
        self.queue("zz-p1", "reviewer", "G-001", at(2, mins=200))
        self.first("zz-p1", "zz-p3")

    def test_cap_p3_250min_beats_p1_waited_60min(self):  # the 240-min cap puts the P3 at level 0 with more steps
        self.queue("zz-p1", "reviewer", "G-001", at(1, mins=60))
        self.queue("zz-p3", "reviewer", "G-003", at(2, mins=250))
        self.first("zz-p3", "zz-p1")

    def test_below_cap_p3_200min_loses_to_p1_waited_30min(self):  # (c) still holds under the cap
        self.queue("zz-p3", "reviewer", "G-003", at(1, mins=200))
        self.queue("zz-p1", "reviewer", "G-001", at(2, mins=31))
        self.first("zz-p1", "zz-p3")

    def test_cap_p1_stream_cannot_hold_back_p3_past_cap(self):  # a P1 arrives every 10 min; the P3 is 241 min old
        self.queue("zz-p3", "reviewer", "G-003", at(1, mins=241))
        for i, m in enumerate(range(0, 240, 10)):
            self.queue(f"zz-p1-{i:02d}", "reviewer", "G-001", at(2 + i, mins=m))
        self.first("zz-p3", "zz-p1-00")

    def test_same_time_priority_order_after_aging(self):  # (a) P3 queued a few seconds before P2, both 2 h old
        self.env.seed_goal("G-002")
        self.queue("zz-p3", "reviewer", "G-003", at(1, mins=120))
        self.queue("zz-p2", "reviewer", "G-002", at(2, mins=120))
        self.first("zz-p2", "zz-p3")

    def test_same_time_priority_order_fresh(self):  # (a)
        self.queue("zz-p3", "reviewer", "G-003", at(1))
        self.queue("zz-p1", "reviewer", "G-001", at(2))
        self.first("zz-p1", "zz-p3")

    def test_starvation_bound_p3_beats_fresh_p1(self):  # (b) a P3 queued 61 min ago is at level 1, ahead of a fresh P1
        self.queue("zz-p1", "reviewer", "G-001", at(1))
        self.queue("zz-p3", "reviewer", "G-003", at(2, mins=61))
        self.first("zz-p3", "zz-p1")

    def test_p3_younger_than_bound_loses_to_fresh_p1(self):  # the bound is 60 min, not less
        self.queue("zz-p3", "reviewer", "G-003", at(1, mins=50))
        self.queue("zz-p1", "reviewer", "G-001", at(2))
        self.first("zz-p1", "zz-p3")

    def test_role_class_still_first_for_aged_p1(self):
        self.queue("zz-co", "coder", "G-001", at(1, mins=300))
        self.queue("zz-rv", "reviewer", "G-003", at(2))
        self.first("zz-rv", "zz-co")

    def test_merge_only_refresh_coder_beats_older_normal_coder(self):  # (d)
        self.queue("zz-old", "coder", "G-001", at(1, mins=300))
        self.queue("zz-ref", "coder", "G-003", at(2))
        self.env.sql("UPDATE agents SET spawn_args=json_set(spawn_args,'$.prompt',?) WHERE name='zz-ref'",
                     ("Task T-0001: Merge-Only Refresh of the PR branch",))
        self.first("zz-ref", "zz-old")

    def test_merge_only_refresh_coder_also_beats_older_pm(self):  # (d)
        self.queue("zz-pm", "pm", "G-001", at(1, mins=300))
        self.queue("zz-ref", "coder", "G-003", at(2))
        self.env.sql("UPDATE agents SET spawn_args=json_set(spawn_args,'$.prompt',?) WHERE name='zz-ref'",
                     ("do a merge-only refresh",))
        self.first("zz-ref", "zz-pm")

    def test_pm_prompt_saying_merge_only_refresh_gets_no_boost(self):  # only coders count
        self.queue("zz-old", "coder", "G-001", at(1, mins=300))
        self.queue("zz-pm", "pm", "G-003", at(2))
        self.env.sql("UPDATE agents SET spawn_args=json_set(spawn_args,'$.prompt',?) WHERE name='zz-pm'",
                     ("merge-only refresh",))
        self.first("zz-old", "zz-pm")

    def test_normal_coder_prompt_gets_no_boost(self):
        self.queue("zz-old", "coder", "G-001", at(1, mins=300))
        self.queue("zz-new", "coder", "G-003", at(2))
        self.env.sql("UPDATE agents SET spawn_args=json_set(spawn_args,'$.prompt',?) WHERE name='zz-new'",
                     ("implement the task; merge origin/main first",))
        self.first("zz-old", "zz-new")

    def test_pass_verdict_judge_beats_older_normal_coder(self):  # (e)
        self.env.seed_task("T-0002", status="in_review", team="g-001-a")
        self.queue("zz-co", "coder", "G-001", at(1, mins=300))
        self.queue("zz-jd", "judge", "G-003", at(2))
        self.env.sql("UPDATE agents SET task_id='T-0002' WHERE name='zz-jd'")
        self.env.sql("INSERT INTO verdicts(task_id,role,verdict,summary,created) VALUES('T-0002','reviewer','PASS','ok','t')")
        self.first("zz-jd", "zz-co")


class GoalPriorityTests(DequeueBase):
    """T-0094: --priority has no argparse default, so an explicit 2 is distinguishable from 'not given'."""

    def prio(self, gid):
        return self.env.sql("SELECT priority FROM goals WHERE id=?", (gid,))[0]["priority"]

    def test_edit_priority_2_sets_2(self):
        self.assertEqual(self.prio("G-001"), 1)
        self.ok("goal", "edit", "G-001", "--priority", "2", agent="zz-director")
        self.assertEqual(self.prio("G-001"), 2)

    def test_edit_without_priority_changes_nothing(self):
        self.refused(1, "goal", "edit", "G-001", agent="zz-director")
        self.ok("goal", "edit", "G-001", "--title", "zz-test renamed", agent="zz-director")
        self.assertEqual(self.prio("G-001"), 1)

    def test_new_defaults_to_2_and_explicit_is_kept(self):
        gid = self.ok("goal", "new", "--title", "zz-test default", agent="owner").stdout.strip()
        self.assertEqual(self.prio(gid), 2)
        gid = self.ok("goal", "new", "--title", "zz-test p1", "--priority", "1", agent="owner").stdout.strip()
        self.assertEqual(self.prio(gid), 1)


class DequeueCmdTests(DequeueBase):
    def setUp(self):
        super().setUp()
        self.queue("zz-q", "pm", "G-001", at(1), team="g-001-a")
        self.env.sql("UPDATE agents SET team_id='g-001-a', task_id='T-0001' WHERE name='zz-q'")
        self.env.set_herdr(fail=False)
        self.env.run("config", "max_live_agents", "5")

    def dq(self, *args, agent="zz-director", code=0):
        return self.refused(code, "dequeue", *args, agent=agent)

    def test_director_cancels_queued(self):
        before = self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"]
        ncalls = len(self.env.calls("herdr"))
        self.dq("zz-q", "--reason", "not needed")
        self.assertEqual(self.status("zz-q"), "cancelled")
        ev = self.env.sql("SELECT * FROM events WHERE action='dequeued' AND entity_id='zz-q'")
        self.assertEqual(len(ev), 1)
        self.assertEqual(json.loads(ev[0]["detail"]), {"by": "zz-director", "reason": "not needed"})
        self.assertEqual(len(self.env.calls("herdr")), ncalls)
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"], before)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM messages")[0]["c"], 0)

    def test_same_team_lead_ok_other_team_exit_3(self):
        p = self.dq("zz-q", agent="zz-lead-b", code=3)
        self.assertIn("not in your team", p.stderr)
        self.assertEqual(self.status("zz-q"), "queued")
        self.dq("zz-q", agent="zz-lead")
        self.assertEqual(self.status("zz-q"), "cancelled")

    def test_worker_refused(self):
        p = self.dq("zz-q", agent="zz-coder", code=3)
        self.assertIn("workers may not run `factory dequeue`", p.stderr)
        self.assertEqual(self.status("zz-q"), "queued")

    def test_live_unknown_and_repeat_exit_2(self):
        p = self.dq("zz-coder", code=2)
        self.assertIn("not queued", p.stderr)
        self.dq("zz-nobody", code=2)
        self.dq("zz-q")
        self.dq("zz-q", code=2)

    def test_cancelled_never_started_and_not_counted(self):
        self.dq("zz-q")
        self.ok("tick")
        self.assertEqual(self.starts(), [])
        self.assertEqual(self.status("zz-q"), "cancelled")
        out = self.ok("res").stdout
        self.assertIn("queued 0", out)
        self.assertIn("spawn gate: OPEN", out)
        self.assertNotIn("zz-q", self.ok("agents").stdout)
        self.assertIn("cancelled", self.ok("agents", "--all").stdout)

    def test_gate_ignores_cancelled(self):
        # a cancelled row started seconds ago would hold the single slot if it counted (live or still starting)
        self.env.sql("UPDATE agents SET status='cancelled', started=datetime('now') WHERE name='zz-q'")
        self.env.sql("UPDATE agents SET status='done' WHERE name='zz-coder'")
        self.ok("config", "max_live_agents", "1")
        self.assertIn("agents live 0/1", self.ok("res").stdout)
        self.assertIn("SPAWNED", self.ok("spawn", "pm", "--name", "zz-new", "--goal", "G-001").stdout)

    def test_dequeue_during_earlier_spawn_is_honoured(self):
        # rows A (zz-q, first in order) and B are queued; B is dequeued while A's `agent start` runs
        self.queue("zz-b", "pm", "G-003", at(9), team="g-001-a")
        hook = f"FACTORY_AGENT=zz-director {sys.executable} {FACTORY} dequeue zz-b >/dev/null 2>&1"
        p = self.env.run("tick", extra_env={"FAKE_HERDR_HOOK": hook, "FAKE_HERDR_HOOK_AT": "agent start"})
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual((self.status("zz-q"), self.status("zz-b")), ("live", "cancelled"))
        self.assertEqual([a[2] for a in self.starts()], ["zz-q"])
        self.assertEqual(len(self.env.sql("SELECT 1 FROM events WHERE action='dequeued' AND entity_id='zz-b'")), 1)

    def test_dequeue_during_place_is_refused_not_started_and_cancelled(self):
        # the reviewer's repro: the tick has claimed zz-q and is in place() (`tab create`) when the dequeue runs
        out = self.env.home / "dq.out"
        hook = f"FACTORY_AGENT=zz-director {sys.executable} {FACTORY} dequeue zz-q >{out} 2>&1; echo $? >>{out}"
        p = self.env.run("tick", extra_env={"FAKE_HERDR_HOOK": hook, "FAKE_HERDR_HOOK_AT": "tab create"})
        self.assertEqual(p.returncode, 0, p.stderr)
        res = out.read_text()
        self.assertIn("already starting", res)
        self.assertTrue(res.strip().endswith("2"), res)
        self.assertEqual(self.status("zz-q"), "live")
        self.assertEqual([a[2] for a in self.starts()], ["zz-q"])
        self.assertEqual(self.env.sql("SELECT 1 FROM events WHERE action='dequeued' AND entity_id='zz-q'"), [])

    def test_failed_start_returns_claim_to_queue(self):
        hook = (f"{sys.executable} -c \"import json,os;p=os.environ['FAKE_HERDR_STATE'];s=json.load(open(p));"
                f"s['fail']=True;json.dump(s,open(p,'w'))\"")
        self.env.run("tick", extra_env={"FAKE_HERDR_HOOK": hook, "FAKE_HERDR_HOOK_AT": "tab create"})
        self.assertEqual(self.status("zz-q"), "queued")
        self.assertNotIn("claimed_at", self.env.sql("SELECT spawn_args FROM agents WHERE name='zz-q'")[0]["spawn_args"])
        self.env.set_herdr(fail=False)
        self.dq("zz-q")  # and it can be dequeued again
        self.assertEqual(self.status("zz-q"), "cancelled")

    def test_starting_row_is_refused_by_dequeue_and_recovered_after_ttl(self):
        self.env.sql("UPDATE agents SET status='starting', spawn_args=json_set(spawn_args,'$.claimed_at',?) WHERE name='zz-q'",
                     (time.time(),))
        self.assertIn("already starting", self.dq("zz-q", code=2).stderr)
        self.ok("tick")
        self.assertEqual(self.status("zz-q"), "starting")  # fresh claim: the tick leaves it alone
        self.assertEqual(self.starts(), [])
        self.assertIn("starting", self.ok("agents").stdout)
        self.env.sql("UPDATE agents SET spawn_args=json_set(spawn_args,'$.claimed_at',?) WHERE name='zz-q'", (time.time() - 601,))
        self.ok("tick")  # crash recovery: the stale claim is queued again and started
        self.assertEqual(self.status("zz-q"), "live")
        self.assertEqual([a[2] for a in self.starts()], ["zz-q"])

    def test_up_does_not_relaunch_cancelled_lead_or_director(self):
        e = self.env
        e.sql("UPDATE agents SET status='cancelled', session_id='s' WHERE name IN ('zz-lead','zz-director')")
        e.seed_agent("zz-boss", "boss")
        self.ok("up", "--no-boss", agent="zz-boss")
        started = [a[2] for a in self.starts()]
        self.assertNotIn("zz-lead", started)
        self.assertNotIn("zz-director", started)
        self.assertEqual(self.status("zz-lead"), "cancelled")
        self.assertEqual(self.status("zz-director"), "cancelled")

    def test_respawn_after_cancel(self):
        self.dq("zz-q")
        p = self.ok("spawn", "pm", "--name", "zz-q", "--goal", "G-001")
        self.assertIn("SPAWNED", p.stdout)
        self.assertEqual(self.status("zz-q"), "live")

    def test_requeue_after_cancel(self):
        self.dq("zz-q")
        self.env.set_herdr(fail=True)
        self.assertIn("QUEUED", self.ok("spawn", "pm", "--name", "zz-q", "--goal", "G-001").stdout)
        self.assertEqual(self.status("zz-q"), "queued")

    def test_gate_closed_from_queue_upsert_leaves_cancelled_row_alone(self):
        # the tick selected the queued row, a dequeue cancels it, then the gate-closed from_queue re-queue runs
        s = json.loads(self.env.sql("SELECT spawn_args FROM agents WHERE name='zz-q'")[0]["spawn_args"])
        self.dq("zz-q")
        self.env.set_herdr(fail=True)
        with self.env.load_factory() as ns:
            con = ns.db()
            ns.spawn(con, s["role"], s["goal"], s["team"], s["task"], s["name"], s["cwd"], s["prompt"], s["model"], from_queue=True)
            con.commit()
        self.assertEqual(self.status("zz-q"), "cancelled")

    def test_manual_spawn_of_starting_row_exits_2(self):
        self.env.sql("UPDATE agents SET status='starting', spawn_args=json_set(spawn_args,'$.claimed_at',?) WHERE name='zz-q'",
                     (time.time(),))
        p = self.refused(2, "spawn", "pm", "--name", "zz-q", "--goal", "G-001", agent="zz-director")
        self.assertIn("already starting", p.stderr)
        self.assertEqual(self.status("zz-q"), "starting")
        self.assertEqual(self.starts(), [])

    def test_help_lists_dequeue(self):
        self.assertIn("dequeue", self.ok("--help").stdout)


if __name__ == "__main__":
    unittest.main()
