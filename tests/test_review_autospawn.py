"""T-0629 (G-022 #6): review_autospawn: the tick starts reviewer + QA (or one combined agent) when a PR opens;
review_reserved_slots keeps slots for them (docs/review-autospawn.md)."""
import unittest

from harness import SHA_A, FactoryTestCase, gates_summary

TEAM, LEAD = "g-001-a", "lead-g-001-a"
GOOD = "## Gates\n- unittest exit=0 cmd=python3 -m unittest discover -s tests\n"


class Base(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(TEAM, lead=LEAD)
        e.seed_agent(LEAD, "lead", goal="G-001", team=TEAM, status="exited")

    def task(self, tid="T-0001", **kw):
        kw.setdefault("team", TEAM)
        self.env.seed_task(tid, **kw)

    def tick_step(self, gate_open=True, spawn_status="live"):
        calls = []
        with self.env.load_factory() as f:
            con = f.db()

            def fake_spawn(con, role, goal=None, team=None, task=None, name=None, prompt=None, **kw):
                calls.append({"role": role, "team": team, "task": task, "name": name, "prompt": prompt})
                con.execute("INSERT INTO agents(name,role,goal_id,team_id,task_id,status,started) VALUES(?,?,?,?,?,?,'t')",
                            (name, role, goal, team, task, spawn_status))

            f.spawn = fake_spawn
            f.gate = lambda con, **k: (gate_open, "" if gate_open else "closed", {})
            f.review_autospawn_tick(con)
            con.commit()
        return calls

    def on(self, **cfg):
        self.ok("config", "review_autospawn", "on")
        for k, v in cfg.items():
            self.ok("config", k, v)


class AutospawnTests(Base):
    def test_off_by_default_spawns_nothing(self):
        self.task()
        self.assertEqual(self.tick_step(), [])

    def test_on_spawns_reviewer_and_qa_with_fyi(self):
        self.on()
        self.task()
        calls = self.tick_step()
        self.assertEqual([(c["role"], c["name"], c["task"]) for c in calls],
                         [("reviewer", "reviewer-t-0001", "T-0001"), ("qa", "qa-t-0001", "T-0001")])
        self.assertIn("affected.py", calls[0]["prompt"])
        fyi = self.env.sql("SELECT kind, recipient FROM messages WHERE kind='fyi'")
        self.assertEqual([(r["kind"], r["recipient"]) for r in fyi], [("fyi", LEAD)])

    def test_teamless_task_gets_reviewer_qa_and_director_fyi(self):
        self.on()
        self.task("T-0001", team=None)
        self.task("T-0002")
        calls = self.tick_step()
        self.assertEqual([(c["role"], c["team"], c["task"]) for c in calls],
                         [("reviewer", None, "T-0001"), ("qa", None, "T-0001"),
                          ("reviewer", TEAM, "T-0002"), ("qa", TEAM, "T-0002")])
        self.assertIn("director gets an FYI", calls[0]["prompt"])
        self.assertNotIn("lead:None", calls[0]["prompt"])
        self.assertIn(f"lead:{TEAM} gets an FYI", calls[2]["prompt"])
        fyi = self.env.sql("SELECT recipient, task_id FROM messages WHERE kind='fyi' ORDER BY task_id")
        self.assertEqual([(r["recipient"], r["task_id"]) for r in fyi], [("director", "T-0001"), (LEAD, "T-0002")])

    def test_teamless_task_with_director_spawned_review_not_doubled(self):
        self.on()
        self.task("T-0001", team=None)
        self.env.seed_agent("reviewer-t-0001", "reviewer", goal="G-001", task="T-0001")
        self.env.seed_agent("qa-t-0001", "qa", goal="G-001", task="T-0001")
        self.assertEqual(self.tick_step(), [])

    def test_teamless_task_on_inactive_goal_skipped(self):
        self.on()
        self.task("T-0001", team=None)
        self.env.sql("UPDATE goals SET status='paused' WHERE id='G-001'")
        self.assertEqual(self.tick_step(), [])

    def test_team_worker_cap_not_consulted(self):
        self.on()
        self.task()
        for i in range(3):
            self.env.seed_agent(f"zz-test-w{i}", "coder", goal="G-001", team=TEAM, task="T-0009")
        self.assertEqual(len(self.tick_step()), 2)

    def test_closed_gate_spawns_nothing(self):
        self.on()
        self.task()
        self.assertEqual(self.tick_step(gate_open=False), [])

    def test_skips_task_with_review_agent_verdict_or_not_in_review(self):
        self.on()
        self.task("T-0001")
        self.env.seed_agent("qa-t-0001", "qa", goal="G-001", task="T-0001", status="exited")
        self.task("T-0002")
        self.env.sql("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) "
                     "VALUES('T-0002','u','x','qa','pass','s','a','t')")
        self.task("T-0003", status="todo", pr_url=None)
        self.task("T-0004", pr_url=None)
        self.assertEqual(self.tick_step(), [])

    def test_queued_spawn_stops_loop(self):
        self.on()
        self.task("T-0001")
        self.task("T-0002")
        self.assertEqual(len(self.tick_step(spawn_status="queued")), 1)

    def test_partial_start_gets_its_qa_next_tick(self):
        self.on()
        self.task()
        self.env.seed_agent("reviewer-t-0001", "reviewer", goal="G-001", team=TEAM, task="T-0001")
        calls = self.tick_step()
        self.assertEqual([(c["role"], c["name"]) for c in calls], [("qa", "qa-t-0001")])
        self.assertEqual(self.tick_step(), [])

    def test_low_tier_one_combined_agent(self):
        self.on(risk_tiers="on")
        self.task()
        self.env.sql("UPDATE tasks SET tier='T1' WHERE id='T-0001'")
        calls = self.tick_step()
        self.assertEqual([(c["role"], c["name"]) for c in calls], [("reviewer", "combined-t-0001")])
        self.assertIn("--as qa", calls[0]["prompt"])

    def test_low_tier_with_plain_reviewer_gets_qa_and_one_fyi(self):
        self.on(risk_tiers="on")
        self.task()
        self.env.sql("UPDATE tasks SET tier='T1' WHERE id='T-0001'")
        self.env.seed_agent("reviewer-t-0001", "reviewer", goal="G-001", team=TEAM, task="T-0001")
        calls = [self.tick_step() for _ in range(3)]
        self.assertEqual([[(c["role"], c["name"]) for c in cs] for cs in calls], [[("qa", "qa-t-0001")], [], []])
        fyi = self.env.sql("SELECT body FROM messages WHERE kind='fyi'")
        self.assertEqual(len(fyi), 1)
        self.assertIn("qa-t-0001", fyi[0]["body"])
        self.assertNotIn("reviewer-t-0001", fyi[0]["body"])

    def test_tier_ignored_when_risk_tiers_off(self):
        self.on()
        self.task()
        self.env.sql("UPDATE tasks SET tier='T0' WHERE id='T-0001'")
        self.assertEqual(len(self.tick_step()), 2)

    def test_high_tier_gets_two_agents(self):
        self.on(risk_tiers="on")
        self.task()
        self.env.sql("UPDATE tasks SET tier='T3' WHERE id='T-0001'")
        self.assertEqual(len(self.tick_step()), 2)


class CombinedVerdictTests(Base):
    def setUp(self):
        super().setUp()
        self.env.seed_project()
        self.task()
        self.env.seed_agent("combined-t-0001", "reviewer", goal="G-001", task="T-0001")
        self.env.seed_agent("reviewer-t-0001", "reviewer", goal="G-001", task="T-0001")
        self.env.seed_pr()

    def test_combined_agent_records_both_verdicts_on_head(self):
        self.ok("verdict", "T-0001", "--pass", "--summary", "ok", agent="combined-t-0001")
        self.ok("verdict", "T-0001", "--pass", "--as", "qa", "--summary", "ok\n" + GOOD, agent="combined-t-0001")
        rows = self.env.sql("SELECT role, head_sha, verdict FROM verdicts ORDER BY id")
        self.assertEqual([(r["role"], r["head_sha"], r["verdict"]) for r in rows],
                         [("reviewer", SHA_A, "pass"), ("qa", SHA_A, "pass")])

    def test_plain_reviewer_cannot_record_qa(self):
        self.ok("verdict", "T-0001", "--pass", "--as", "qa", "--summary", "ok", agent="reviewer-t-0001")
        self.assertEqual([r["role"] for r in self.env.sql("SELECT role FROM verdicts")], ["reviewer"])

    def test_combined_agent_of_another_task_cannot_record_qa(self):
        self.task("T-0002")
        self.env.seed_agent("combined-t-0002", "reviewer", goal="G-001", task="T-0002")
        self.ok("verdict", "T-0001", "--pass", "--as", "qa", "--summary", "ok", agent="combined-t-0002")
        self.assertEqual([r["role"] for r in self.env.sql("SELECT role FROM verdicts")], ["reviewer"])

    def test_combined_qa_pass_still_needs_gates(self):
        p = self.refused(5, "verdict", "T-0001", "--pass", "--as", "qa", "--summary", "ok", agent="combined-t-0001")
        self.assertIn("Gate line format:", p.stdout + p.stderr)


class ReservedSlotsTests(Base):
    def setUp(self):
        super().setUp()
        self.on(max_live_agents="3", review_reserved_slots="2")
        self.task("T-0009", status="todo", pr_url=None)
        self.env.seed_agent("zz-test-x1", "coder", goal="G-001")
        self.env.sql("UPDATE agents SET pane_id='p-x1' WHERE name='zz-test-x1'")
        self.env.set_herdr(agents=[{"name": "zz-test-x1", "agent": "claude", "pane_id": "p-x1"}])

    def gate(self, role):
        with self.env.load_factory() as f:
            return f.gate(f.db(), role=role)[:2]

    def test_coder_blocked_by_reserved_slots(self):
        ok, why = self.gate("coder")
        self.assertFalse(ok)
        self.assertIn("review_reserved_slots", why)

    def test_reviewer_and_qa_use_reserved_slots(self):
        self.assertTrue(self.gate("reviewer")[0])
        self.assertTrue(self.gate("qa")[0])

    def test_reviewer_still_bound_by_the_global_cap(self):
        self.ok("config", "max_live_agents", "1")
        ok, why = self.gate("reviewer")
        self.assertFalse(ok)
        self.assertIn("agent panes live", why)

    def test_flag_off_coder_unaffected(self):
        self.ok("config", "review_autospawn", "off")
        self.assertTrue(self.gate("coder")[0])

    def test_held_review_slots_are_returned_to_coders(self):
        self.env.seed_agent("zz-test-r1", "reviewer", goal="G-001")
        self.env.sql("UPDATE agents SET pane_id='p-r1' WHERE name='zz-test-r1'")
        self.ok("config", "review_reserved_slots", "1")
        self.env.set_herdr(agents=[{"name": "zz-test-x1", "agent": "claude", "pane_id": "p-x1"},
                                   {"name": "zz-test-r1", "agent": "claude", "pane_id": "p-r1"}])
        self.assertTrue(self.gate("coder")[0])


H1, H2 = "1" * 40, "2" * 40
GREEN = [{"__typename": "CheckRun", "name": "ci", "workflowName": "ci", "status": "COMPLETED", "conclusion": "SUCCESS"}]
PENDING = [{"__typename": "CheckRun", "name": "ci", "workflowName": "ci", "status": "IN_PROGRESS", "conclusion": ""}]


class NewHeadTests(Base):
    """T-0812: a task that had a review round on an older head gets a fresh reviewer + qa once its new head is green."""

    def setUp(self):
        super().setUp()
        self.on()
        self.task()
        self.env.sql("UPDATE tasks SET branch='factory/t-0001' WHERE id='T-0001'")
        self.probes = 0

    def step(self, head, rollup=GREEN, spawn_status="live"):
        calls = []
        with self.env.load_factory() as f:
            con = f.db()

            def fake_spawn(con, role, goal=None, team=None, task=None, name=None, prompt=None, **kw):
                calls.append((role, name))
                con.execute("INSERT INTO agents(name,role,goal_id,team_id,task_id,status,started) VALUES(?,?,?,?,?,?,'t')",
                            (name, role, goal, team, task, spawn_status))

            def probe(url, timeout):
                self.probes += 1
                return {"number": 1, "state": "OPEN", "headRefOid": head, "mergeStateStatus": "CLEAN", "statusCheckRollup": rollup}

            f.spawn = fake_spawn
            f.gate = lambda con, **k: (True, "", {})
            f._ci_remote_head = lambda branch, batch=None: head
            f._ci_wake_probe = probe
            f.review_autospawn_tick(con)
            con.commit()
        return calls

    def verdict(self, head, role="reviewer", verdict="fail"):
        self.env.sql("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) "
                     "VALUES('T-0001','u',?,?,?,'s','a','t')", (head, role, verdict))

    def reviewed_on_h1(self):
        self.env.seed_agent("reviewer-t-0001", "reviewer", goal="G-001", team=TEAM, task="T-0001", status="exited")
        self.env.seed_agent("qa-t-0001", "qa", goal="G-001", team=TEAM, task="T-0001", status="exited")
        self.verdict(H1)

    def test_green_before_in_review_spawns_when_in_review(self):
        self.assertEqual(self.step(H1), [("reviewer", "reviewer-t-0001"), ("qa", "qa-t-0001")])

    def test_second_attempt_new_green_head_spawns_again_once(self):
        self.reviewed_on_h1()
        first = self.step(H2)
        self.assertEqual(first, [("reviewer", "reviewer-t-0001-22222222"), ("qa", "qa-t-0001-22222222")])
        self.env.sql("UPDATE agents SET status='exited'")
        self.assertEqual([self.step(H2) for _ in range(3)], [[], [], []])

    def test_exact_failed_shape_qa_row_and_verdict_on_old_head_new_head_green(self):
        # T-0352 #209 / T-0798 #227 2026-10-30: reviewer FAIL on the old head, qa row exited, coder re-pushed, CI green, no spawn
        self.reviewed_on_h1()
        self.assertEqual(len(self.step(H2)), 2)
        fyi = self.env.sql("SELECT subject FROM messages WHERE kind='fyi'")
        self.assertIn("@22222222", fyi[0]["subject"])

    def test_verdict_on_current_head_no_spawn_and_no_gh_call(self):
        self.reviewed_on_h1()
        self.assertEqual([self.step(H1) for _ in range(3)], [[], [], []])
        self.assertEqual(self.probes, 0)

    def test_live_or_queued_review_agent_no_spawn(self):
        for status in ("live", "queued", "starting"):
            with self.subTest(status=status):
                self.env.sql("DELETE FROM agents WHERE task_id='T-0001'")
                self.env.seed_agent("qa-t-0001", "qa", goal="G-001", team=TEAM, task="T-0001", status="exited")
                self.env.seed_agent("reviewer-t-0001-aaaaaaaa", "reviewer", goal="G-001", team=TEAM, task="T-0001", status=status)
                self.assertEqual([self.step(H2) for _ in range(3)], [[], [], []])

    def test_pending_or_red_head_waits(self):
        self.reviewed_on_h1()
        self.assertEqual(self.step(H2, PENDING), [])
        red = [dict(GREEN[0], conclusion="FAILURE")]
        self.assertEqual(self.step(H2, red), [])
        self.assertEqual(len(self.step(H2)), 2)

    def test_queued_reviewer_still_gets_qa_after_it_exits(self):
        # reviewer-review B1: queued reviewer on the new head, it runs and gives a verdict, qa must still be spawned
        self.reviewed_on_h1()
        self.assertEqual(self.step(H2, spawn_status="queued"), [("reviewer", "reviewer-t-0001-22222222")])
        self.env.sql("UPDATE agents SET status='live' WHERE name='reviewer-t-0001-22222222'")
        self.assertEqual(self.step(H2), [])
        self.env.sql("UPDATE agents SET status='exited' WHERE name='reviewer-t-0001-22222222'")
        self.verdict(H2)
        self.assertEqual(self.step(H2), [("qa", "qa-t-0001-22222222")])
        self.env.sql("UPDATE agents SET status='exited'")
        self.assertEqual([self.step(H2) for _ in range(3)], [[], [], []])

    def test_half_started_round_with_qa_verdict_on_head_does_not_respawn_qa(self):
        # reviewer-review B2: reviewer row on the new head, qa already has a verdict there: nothing is missing
        self.reviewed_on_h1()
        self.env.seed_agent("reviewer-t-0001-22222222", "reviewer", goal="G-001", team=TEAM, task="T-0001", status="exited")
        self.verdict(H2, role="qa", verdict="pass")
        self.assertEqual([self.step(H2) for _ in range(3)], [[], [], []])

    def test_gate_refused_qa_is_retried_next_tick(self):
        self.reviewed_on_h1()
        with self.env.load_factory() as f:
            con = f.db()
            f.spawn = lambda con, role, goal=None, team=None, task=None, name=None, prompt=None, **kw: con.execute(
                "INSERT INTO agents(name,role,goal_id,team_id,task_id,status,started) VALUES(?,?,?,?,?,'exited','t')",
                (name, role, goal, team, task))
            f.gate = lambda con, role=None, **k: (role != "qa", "full", {})
            f._ci_remote_head = lambda branch, batch=None: H2
            f._ci_wake_probe = lambda url, timeout: {"number": 1, "state": "OPEN", "headRefOid": H2, "mergeStateStatus": "CLEAN",
                                                      "statusCheckRollup": GREEN}
            f.review_autospawn_tick(con)
            con.commit()
        self.verdict(H2)
        self.assertEqual(self.step(H2), [("qa", "qa-t-0001-22222222")])
        self.env.sql("UPDATE agents SET status='exited'")
        self.assertEqual([self.step(H2) for _ in range(3)], [[], [], []])


if __name__ == "__main__":
    unittest.main()
