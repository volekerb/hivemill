"""T-0629 (G-022 #6): review_autospawn: the tick starts reviewer + QA (or one combined agent) when a PR opens;
review_reserved_slots keeps slots for them (docs/review-autospawn.md)."""
import json
import unittest

from harness import SHA_A, FactoryTestCase, gates_summary

TEAM, LEAD = "g-001-a", "lead-g-001-a"
GOOD = "## Gates\n- unittest exit=0 cmd=python3 -m unittest discover -s tests\n"


class Base(FactoryTestCase):
    gate_open = True  # step() helpers read it; a test sets False to close the spawn gate

    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(TEAM, lead=LEAD)
        e.seed_agent(LEAD, "lead", goal="G-001", team=TEAM, status="exited")
        e.seed_pr()  # gh pr view gate for _tick_pr_probe (status comes from stubbed _ci_wake_probe in tick_step)

    def task(self, tid="T-0001", **kw):
        kw.setdefault("team", TEAM)
        self.env.seed_task(tid, **kw)

    def tick_step(self, gate_open=True, spawn_status="live", stub_pr=True, heads=None, now=None):
        calls = []
        with self.env.load_factory() as f:
            con = f.db()
            if stub_pr:  # a healthy open PR; the T-0815 skip tests use the gh stub instead
                f._ci_wake_probe = lambda url, timeout: {"number": 1, "state": "OPEN", "headRefOid": SHA_A, "statusCheckRollup": []}
            f._ci_remote_heads = lambda: heads
            if now is not None:  # seconds since a fixed epoch: the tick's clock
                f.digest_clock = lambda: f.dt.datetime.fromtimestamp(now, f.dt.timezone.utc)

            def fake_spawn(con, role, goal=None, team=None, task=None, name=None, prompt=None, **kw):
                calls.append({"role": role, "team": team, "task": task, "name": name, "prompt": prompt})
                con.execute("INSERT OR REPLACE INTO agents(name,role,goal_id,team_id,task_id,status,started) VALUES(?,?,?,?,?,?,'t')",
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
        self.assertIn("live-run: pending AC<n>", calls[0]["prompt"])
        self.assertIn("live-run: done AC<n>", calls[0]["prompt"])

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
                con.execute("INSERT OR REPLACE INTO agents(name,role,goal_id,team_id,task_id,status,started) VALUES(?,?,?,?,?,?,'t')",
                            (name, role, goal, team, task, spawn_status))

            def probe(url, timeout):
                self.probes += 1
                return {"number": 1, "state": "OPEN", "headRefOid": head, "mergeStateStatus": "CLEAN", "statusCheckRollup": rollup}

            f.spawn = fake_spawn
            f.gate = lambda con, **k: (self.gate_open, "" if self.gate_open else "closed", {})
            f._ci_remote_head = lambda branch, batch=None: head
            f._ci_remote_heads = lambda: {"factory/t-0001": head}
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

    def rerun_waits(self):
        return [json.loads(r["detail"]) for r in self.env.sql(
            "SELECT detail FROM events WHERE entity='task' AND entity_id='T-0001' AND action='review-autospawn-wait' ORDER BY id")]

    def test_pending_ci_on_a_new_head_logs_one_ci_wait(self):
        # T-1098: the re-run pass used to skip silently while the new head was not green
        self.reviewed_on_h1()
        self.assertEqual([self.step(H2, rollup=PENDING) for _ in range(3)], [[], [], []])
        self.assertEqual([(w["kind"], w["head"]) for w in self.rerun_waits()], [("ci", H2[:8])])

    def test_live_coder_on_a_new_head_logs_a_coder_wait(self):
        self.reviewed_on_h1()
        self.env.seed_agent("coder-t-0001", "coder", goal="G-001", team=TEAM, task="T-0001", status="live")
        self.assertEqual(self.step(H2), [])
        self.assertEqual([w["kind"] for w in self.rerun_waits()], ["coder"])

    def test_closed_gate_on_a_new_head_logs_a_gate_wait(self):
        self.reviewed_on_h1()
        self.gate_open = False
        self.assertEqual(self.step(H2), [])
        self.assertEqual([w["kind"] for w in self.rerun_waits()], ["gate"])
        self.assertIn("spawn gate closed for reviewer", self.rerun_waits()[0]["why"])

    def test_green_new_head_with_a_started_round_logs_no_wait(self):
        self.reviewed_on_h1()
        self.assertEqual(len(self.step(H2)), 2)
        self.env.sql("UPDATE agents SET status='exited'")
        self.step(H2)
        self.assertEqual(self.rerun_waits(), [])

    def test_green_before_in_review_spawns_when_in_review(self):
        self.assertEqual(self.step(H1), [("reviewer", "reviewer-t-0001"), ("qa", "qa-t-0001")])

    def test_second_attempt_new_green_head_spawns_again_once(self):
        self.reviewed_on_h1()
        first = self.step(H2)
        self.assertEqual(first, [("reviewer", "reviewer-t-0001-22222222"), ("qa", "qa-t-0001-22222222")])
        self.env.sql("UPDATE agents SET status='exited'")
        self.assertEqual(self.step(H2), first)  # T-1600: both ended with no verdict: one replacement each, same names
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
        self.verdict(H1, role="qa")  # both roles have a verdict on H1 (a reviewer verdict alone no longer closes the head, T-1048)
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
        self.assertEqual(self.step(H2), [("qa", "qa-t-0001-22222222")])  # T-1600: the one replacement of the qa that ended with no verdict
        self.env.sql("UPDATE agents SET status='exited'")
        self.assertEqual([self.step(H2) for _ in range(3)], [[], [], []])

    def test_half_started_round_with_qa_verdict_on_head_does_not_respawn_qa(self):
        # reviewer-review B2: reviewer row on the new head, qa already has a verdict there: nothing is missing
        self.reviewed_on_h1()
        self.env.seed_agent("reviewer-t-0001-22222222", "reviewer", goal="G-001", team=TEAM, task="T-0001", status="exited")
        self.verdict(H2, role="qa", verdict="pass")
        # T-1600: the reviewer row ended with no verdict, so it gets its one replacement; the qa (verdict on the head) never does
        self.assertEqual([self.step(H2) for _ in range(3)], [[("reviewer", "reviewer-t-0001-22222222")], [], []])
        self.env.sql("UPDATE agents SET status='exited'")
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
        self.assertEqual(self.step(H2), [("qa", "qa-t-0001-22222222")])  # T-1600: the one replacement of the qa that ended with no verdict
        self.env.sql("UPDATE agents SET status='exited'")
        self.assertEqual([self.step(H2) for _ in range(3)], [[], [], []])

    def test_gate_refused_qa_still_sends_fyi_for_started_reviewer_once(self):
        self.reviewed_on_h1()
        with self.env.load_factory() as f:
            con = f.db()
            f.spawn = lambda con, role, goal=None, team=None, task=None, name=None, prompt=None, **kw: con.execute(
                "INSERT INTO agents(name,role,goal_id,team_id,task_id,status,started) VALUES(?,?,?,?,?,'live','t')",
                (name, role, goal, team, task))
            f.gate = lambda con, role=None, **k: (role != "qa", "full", {})
            f._ci_remote_head = lambda branch, batch=None: H2
            f._ci_wake_probe = lambda url, timeout: {"number": 1, "state": "OPEN", "headRefOid": H2, "mergeStateStatus": "CLEAN",
                                                      "statusCheckRollup": GREEN}
            f.review_autospawn_tick(con)
            con.commit()
        self.assertEqual([r["name"] for r in self.env.sql("SELECT name FROM agents WHERE name LIKE '%22222222'")],
                         ["reviewer-t-0001-22222222"])
        fyi = self.env.sql("SELECT subject, body FROM messages WHERE kind='fyi'")
        self.assertEqual(len(fyi), 1)
        self.assertIn("@22222222", fyi[0]["subject"])
        self.assertIn("reviewer-t-0001-22222222", fyi[0]["body"])
        self.assertNotIn("qa-t-0001-22222222", fyi[0]["body"])


DEAD_URL = "https://github.com/volekerb/factory/pull/262"
HIVEMILL_URL = "https://github.com/volekerb/hivemill/pull/9"


class SkipTests(Base):
    """T-0815: no reviewer/qa for a PR on another repo, one gh cannot resolve or that is closed/merged, or whose head branch is gone."""

    def setUp(self):
        super().setUp()
        self.on()
        self.env.sql("INSERT INTO projects(id,name,gh_repo,created) VALUES('hm','hm','volekerb/hivemill','t')")
        self.env.sql("UPDATE goals SET project_id='hm' WHERE id='G-001'")
        self.heads = {"factory/t-0001": H1}

    def spawns_over_three_ticks(self):
        calls = []
        for _ in range(3):
            calls += self.tick_step(stub_pr=False, heads=self.heads)
        return calls

    def skip_events(self):
        return [r["detail"] for r in self.env.sql("SELECT detail FROM events WHERE action='review-autospawn-skip' AND entity_id='T-0001'")]

    def status(self):
        return self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"]

    def dead_task(self, url=HIVEMILL_URL, branch="factory/t-0001"):
        self.task(pr_url=url)
        self.env.sql("UPDATE tasks SET branch=? WHERE id='T-0001'", (branch,))

    def seed(self, url=HIVEMILL_URL, **kw):
        self.env.seed_pr(url=url, **kw)

    def assert_skipped_once(self, text):
        self.assertEqual(self.spawns_over_three_ticks(), [])
        events = self.skip_events()
        self.assertEqual(len(events), 1)
        self.assertIn(text, events[0])
        self.assertEqual(self.status(), "in_review")

    def test_a_pr_on_another_repo_is_skipped_once(self):
        self.dead_task(url=DEAD_URL)
        self.seed(url=DEAD_URL)  # open on GitHub and its branch exists: only the repo name is wrong
        self.assert_skipped_once("volekerb/factory")
        self.assertEqual(self.env.calls("gh"), [])  # decided from the url, no gh call

    def test_a_origin_slug_is_the_reference_without_a_project_gh_repo(self):
        self.env.sql("UPDATE goals SET project_id=NULL WHERE id='G-001'")
        self.dead_task(url=DEAD_URL)
        self.seed(url=DEAD_URL)
        with self.env.load_factory() as f:
            f._origin_urls = lambda: ["https://github.com/volekerb/hivemill.git"]
            f._ci_remote_heads = lambda: self.heads
            f.spawn = lambda *a, **k: self.fail("spawned")
            f.gate = lambda con, **k: (True, "", {})
            con = f.db()
            f.review_autospawn_tick(con)
            con.commit()
        self.assertEqual(len(self.skip_events()), 1)

    def test_pr_url_with_a_trailing_path_is_not_a_repo_mismatch(self):
        url = HIVEMILL_URL + "/files"
        self.dead_task(url=url)
        self.seed(url=url)
        self.assertEqual(len(self.tick_step(stub_pr=False, heads=self.heads)), 2)
        self.assertEqual(self.skip_events(), [])

    def test_b_unresolvable_pr_is_skipped_once(self):
        self.dead_task()  # no PR in the gh stub state: gh exits 1
        self.assert_skipped_once("gh cannot resolve")

    def test_b_closed_or_merged_pr_is_skipped_once(self):
        for state in ("CLOSED", "MERGED"):
            with self.subTest(state=state):
                self.env.sql("DELETE FROM tasks")
                self.env.sql("DELETE FROM events")
                self.env.sql("DELETE FROM throttle")
                self.dead_task()
                self.seed(state=state)
                self.assert_skipped_once(state)

    def test_c_head_branch_absent_is_skipped_once(self):
        self.dead_task()
        self.seed()
        self.heads = {"factory/t-0099": H2}
        self.assert_skipped_once("factory/t-0001")

    def test_failing_gh_fails_closed(self):
        self.dead_task()
        self.seed()
        self.env.set_gh(pr_view_exit=1)
        self.assertEqual(self.spawns_over_three_ticks(), [])
        self.assertEqual(self.status(), "in_review")

    def test_unreadable_remote_fails_closed_without_skip_event(self):
        self.dead_task()
        self.seed()
        self.heads = None  # ls-remote failed
        self.assertEqual(self.spawns_over_three_ticks(), [])
        self.assertEqual(self.skip_events(), [])
        self.assertEqual(len(self.env.sql("SELECT 1 FROM events WHERE action='review-autospawn-error'")), 3)

    def test_normal_hivemill_pr_still_gets_reviewer_and_qa(self):
        self.dead_task()
        self.seed()
        calls = self.tick_step(stub_pr=False, heads=self.heads)
        self.assertEqual([(c["role"], c["name"]) for c in calls], [("reviewer", "reviewer-t-0001"), ("qa", "qa-t-0001")])
        self.assertEqual(self.skip_events(), [])

    def test_rerun_pass_skips_the_same_dead_prs(self):
        for label, url, seed, heads in (("repo", DEAD_URL, True, {"factory/t-0001": H2}),
                                        ("closed", HIVEMILL_URL, "CLOSED", {"factory/t-0001": H2}),
                                        ("branch", HIVEMILL_URL, True, {})):
            with self.subTest(label=label):
                self.env.sql("DELETE FROM tasks")
                self.env.sql("DELETE FROM events")
                self.env.sql("DELETE FROM throttle")
                self.env.sql("DELETE FROM verdicts")
                self.dead_task(url=url)
                self.env.sql("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) "
                             "VALUES('T-0001','u',?,'reviewer','fail','s','a','t')", (H1,))
                self.seed(url=url, sha=H2, checks=GREEN, **({"state": seed} if isinstance(seed, str) else {}))
                self.heads = heads
                self.assert_skipped_once("")


if __name__ == "__main__":
    unittest.main()


class ThrottleTests(SkipTests):
    """T-0816: a dead task is probed at most once per max(director_cycle_s, 1h) unless its url/branch change; a transient gh failure
    is a probe-error event (hourly), not the daily skip slot."""
    T0 = 1_800_000_000

    def gh_views(self):
        return [c for c in self.env.calls("gh") if c["argv"][:2] == ["pr", "view"]]

    def tick_at(self, off):
        return self.tick_step(stub_pr=False, heads=self.heads, now=self.T0 + off)

    def events(self, action):
        return self.env.sql("SELECT detail FROM events WHERE action=? AND entity_id='T-0001'", (action,))

    def test_dead_task_probed_once_per_window(self):
        self.dead_task()  # no PR on GitHub: gh says no pull request found
        for off in (0, 900, 1800, 3500):
            self.assertEqual(self.tick_at(off), [])
        self.assertEqual(len(self.gh_views()), 1)
        self.assertEqual(len(self.skip_events()), 1)
        self.assertEqual(self.tick_at(3600), [])
        self.assertEqual(len(self.gh_views()), 2)

    def test_window_is_director_cycle_when_larger(self):
        self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('director_cycle_s','7200')")
        self.dead_task()
        for off in (0, 3600, 7100):
            self.tick_at(off)
        self.assertEqual(len(self.gh_views()), 1)
        self.tick_at(7200)
        self.assertEqual(len(self.gh_views()), 2)

    def test_changed_pr_url_or_branch_probes_again(self):
        self.dead_task()
        self.tick_at(0)
        self.tick_at(60)
        self.assertEqual(len(self.gh_views()), 1)
        self.env.sql("UPDATE tasks SET branch='factory/t-0001b' WHERE id='T-0001'")
        self.tick_at(120)
        self.assertEqual(len(self.gh_views()), 2)
        url2 = HIVEMILL_URL + "0"
        self.env.sql("UPDATE tasks SET pr_url=? WHERE id='T-0001'", (url2,))
        self.tick_at(180)
        self.assertEqual(len(self.gh_views()), 3)

    def test_revived_task_after_window_gets_review(self):
        self.dead_task()
        self.tick_at(0)
        self.seed()  # the PR shows up
        self.assertEqual(self.tick_at(60), [])  # still inside the window
        self.assertEqual(len(self.tick_at(3600)), 2)

    def test_transient_gh_failure_is_a_probe_error_not_a_skip(self):
        self.dead_task()
        self.seed()
        self.env.set_gh(pr_view_exit=1)
        self.assertEqual(self.tick_at(0), [])
        self.assertEqual(self.tick_at(60), [])
        self.assertEqual(len(self.events("review-autospawn-probe-error")), 1)
        self.assertEqual(self.skip_events(), [])
        self.assertEqual(self.status(), "in_review")
        self.tick_at(3600)  # hourly again
        self.assertEqual(len(self.events("review-autospawn-probe-error")), 2)

    def test_rest_pull_503_after_gate_ok_is_probe_error_then_review(self):
        """gh pr view succeeds but `gh api .../pulls/N` returns HTTP 503: probe-error, no skip/dead key; next tick starts review."""
        self.dead_task()
        self.seed()
        st = json.loads(self.env.gh_state.read_text())
        st["api"] = [{"match": ["repos/volekerb/hivemill/pulls/9"], "exit": 1, "err": "gh: HTTP 503"}]
        self.env.gh_state.write_text(json.dumps(st))
        self.assertEqual(self.tick_at(0), [])
        self.assertEqual(self.skip_events(), [])
        self.assertEqual(len(self.events("review-autospawn-probe-error")), 1)
        self.assertEqual(self.env.sql("SELECT 1 FROM throttle WHERE key LIKE 'review-dead:%'"), [])
        self.tick_at(60)  # not dead-throttled: gh is called again despite the earlier probe error
        self.assertEqual(len(self.gh_views()), 2)
        st.pop("api", None)
        self.env.gh_state.write_text(json.dumps(st))
        self.assertEqual(len(self.tick_at(120)), 2)
        self.assertEqual(self.skip_events(), [])

    def test_skip_event_still_fires_after_a_probe_error(self):
        self.dead_task()  # gh has no such PR
        self.env.set_gh(pr_view_exit=1)
        self.assertEqual(self.tick_at(0), [])
        self.assertEqual(self.skip_events(), [])
        self.env.set_gh(pr_view_exit=0)
        self.assertEqual(self.tick_at(60), [])
        self.assertEqual(len(self.skip_events()), 1)
        self.assertEqual(len(self.events("review-autospawn-probe-error")), 1)


class ReviewOrderingTests(Base):
    """T-1048: no review while a coder is live/queued/starting (prcheck does not gate it); a head missing one role gets it;
    a rerun-round worker that ended with no verdict is alerted once per head; a verdict is bound to the head the worker was started on."""

    def setUp(self):
        Base.setUp(self)
        self.on()
        self.task()
        self.env.sql("UPDATE tasks SET branch='factory/t-0001' WHERE id='T-0001'")
        self.probes = 0

    step, verdict, reviewed_on_h1 = NewHeadTests.step, NewHeadTests.verdict, NewHeadTests.reviewed_on_h1

    def coder(self, status):
        self.env.seed_agent("coder-t-0001", "coder", goal="G-001", team=TEAM, task="T-0001", status=status)

    def prcheck(self, head, ok):
        import json
        self.env.sql("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES('t','x','task','T-0001','prcheck',?)",
                     (json.dumps({"head": head, "ok": ok, "failed": [] if ok else ["fresh"]}),))

    def test_coder_live_starting_or_queued_blocks_first_review(self):
        for status in ("live", "starting", "queued"):
            with self.subTest(status=status):
                self.env.sql("DELETE FROM agents")
                self.coder(status)
                self.assertEqual([self.step(H1) for _ in range(2)], [[], []])
        self.env.sql("UPDATE agents SET status='exited'")
        self.assertEqual(self.step(H1), [("reviewer", "reviewer-t-0001"), ("qa", "qa-t-0001")])

    def test_coder_live_blocks_new_head_round_then_both_start_once(self):
        self.reviewed_on_h1()
        self.coder("live")
        self.assertEqual(self.step(H2), [])
        self.env.sql("UPDATE agents SET status='exited'")
        self.assertEqual(self.step(H2), [("reviewer", "reviewer-t-0001-22222222"), ("qa", "qa-t-0001-22222222")])
        self.env.sql("UPDATE agents SET status='exited'")
        self.verdict(H2)
        self.verdict(H2, role="qa")
        self.assertEqual([self.step(H2) for _ in range(3)], [[], [], []])

    def test_prcheck_not_ok_does_not_hold_review(self):
        self.prcheck(H1, False)
        self.assertEqual(self.step(H1), [("reviewer", "reviewer-t-0001"), ("qa", "qa-t-0001")])

    def test_prcheck_not_ok_with_no_coder_starts_review_once_and_sends_no_alert(self):
        self.prcheck(H1, False)
        self.assertEqual(self.step(H1), [("reviewer", "reviewer-t-0001"), ("qa", "qa-t-0001")])
        self.assertEqual([self.step(H1) for _ in range(2)], [[], []])
        self.assertEqual(self.env.sql("SELECT 1 FROM messages WHERE kind='request'"), [])
        self.assertEqual(self.env.sql("SELECT 1 FROM events WHERE action='review-prcheck-held'"), [])

    def test_coder_live_with_prcheck_not_ok_still_blocks_without_alert(self):
        self.prcheck(H1, False)
        self.coder("live")
        self.assertEqual(self.step(H1), [])
        self.assertEqual(self.env.sql("SELECT 1 FROM messages WHERE kind='request'"), [])

    def test_prcheck_not_ok_on_another_head_does_not_block(self):
        self.prcheck(H1, False)
        self.assertEqual(len(self.step(H2)), 2)

    def test_closed_gate_retries_next_tick_without_duplicates(self):
        calls = []
        with self.env.load_factory() as f:
            con = f.db()
            f.gate = lambda con, **k: (False, "closed", {})
            f.spawn = lambda *a, **k: calls.append(k)
            f._ci_remote_heads = lambda: {"factory/t-0001": H1}
            f._ci_wake_probe = lambda url, timeout: {"number": 1, "state": "OPEN", "headRefOid": H1, "statusCheckRollup": GREEN}
            f.review_autospawn_tick(con)
        self.assertEqual(calls, [])
        self.assertEqual(self.step(H1), [("reviewer", "reviewer-t-0001"), ("qa", "qa-t-0001")])
        self.env.sql("UPDATE agents SET status='exited'")
        self.verdict(H1)
        self.verdict(H1, role="qa")
        self.assertEqual(self.step(H1), [])

    def test_head_with_reviewer_verdict_and_no_qa_gets_the_qa(self):
        # case (b)/(c): reviewer verdict on the head, the qa row ended with no verdict: the old pass called the head done
        self.reviewed_on_h1()
        self.verdict(H2)
        self.assertEqual(self.step(H2), [("qa", "qa-t-0001-22222222")])
        self.env.sql("UPDATE agents SET status='exited'")
        self.assertEqual(self.step(H2), [("qa", "qa-t-0001-22222222")])  # T-1600: its one replacement
        self.env.sql("UPDATE agents SET status='exited'")
        self.assertEqual(self.step(H2), [])

    def test_qa_ended_without_verdict_on_head_one_alert_per_head(self):
        self.reviewed_on_h1()
        self.verdict(H2)
        self.step(H2)
        self.env.sql("UPDATE agents SET status='exited'")
        self.assertEqual(self.step(H2), [("qa", "qa-t-0001-22222222")])  # T-1600: first ending: one replacement, no mail
        self.assertEqual(self.env.sql("SELECT 1 FROM messages WHERE kind='request'"), [])
        self.env.sql("UPDATE agents SET status='exited'")
        for _ in range(3):
            self.assertEqual(self.step(H2), [])
        mails = self.env.sql("SELECT recipient, subject FROM messages WHERE kind='request'")
        self.assertEqual([(m["recipient"]) for m in mails], [LEAD])
        self.assertIn("qa ended with no verdict", mails[0]["subject"])
        self.assertEqual(len(self.env.sql("SELECT 1 FROM events WHERE action='review-no-verdict'")), 1)
        self.assertEqual(self.step(H1.replace("1", "3")), [("reviewer", "reviewer-t-0001-33333333"), ("qa", "qa-t-0001-33333333")])

    def test_fresh_verdict_of_a_lead_run_round_waits_for_its_other_half(self):
        import datetime as dt
        self.reviewed_on_h1()
        self.env.sql("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) "
                     "VALUES('T-0001','u',?,'reviewer','pass','s','a',?)", (H2, dt.datetime.now(dt.timezone.utc).isoformat()))
        self.assertEqual(self.step(H2), [])

    def spawned_verdict_env(self, spawn_head, now_head):
        self.env.seed_pr(sha=now_head)
        self.env.sql("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES('2026-01-02T00:00:00+00:00','tick','task','T-0001','review-autospawn',?)",
                     ('{"agent": "qa-t-0001", "role": "qa", "sha": "%s"}' % spawn_head,))
        self.env.seed_agent("qa-t-0001", "qa", goal="G-001", team=TEAM, task="T-0001", status="live")

    def run_verdict(self, *args):
        a = list(args)
        if "--fail" in a and "--cause" not in a:
            a += ("--cause", "bug")
        return self.env.run("verdict", "T-0001", *a, "--summary", GOOD, agent="qa-t-0001")

    def test_pass_naming_a_head_other_than_the_one_started_on_is_refused(self):
        self.spawned_verdict_env(SHA_A, "b" * 40)
        r = self.run_verdict("--pass", "--sha", "b" * 40)
        self.assertEqual(r.returncode, 5, r.stderr)
        self.assertIn("started you on head", r.stderr)
        self.assertEqual(self.env.sql("SELECT 1 FROM verdicts"), [])

    def test_fail_without_sha_is_recorded_on_the_head_the_worker_started_on(self):
        self.spawned_verdict_env(SHA_A, "b" * 40)
        r = self.run_verdict("--fail")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual([v["head_sha"] for v in self.env.sql("SELECT head_sha FROM verdicts")], [SHA_A])


class PrefixedNameTests(Base):
    """T-1048 review fixes: spawn() stores the org-prefixed name (claude-qa-t-0001-22222222); lookups, alerts and verdict binding must use it."""

    def setUp(self):
        Base.setUp(self)
        self.on(org_prefix_default="1")
        self.task()
        self.env.sql("UPDATE tasks SET branch='factory/t-0001' WHERE id='T-0001'")

    verdict, reviewed_on_h1 = NewHeadTests.verdict, NewHeadTests.reviewed_on_h1

    def pstep(self, head):
        """step() with a spawn stub that stores the prefixed name, as the real spawn() does."""
        calls = []
        with self.env.load_factory() as f:
            con = f.db()

            def fake_spawn(con, role, goal=None, team=None, task=None, name=None, prompt=None, **kw):
                calls.append((role, name))
                con.execute("INSERT OR REPLACE INTO agents(name,role,goal_id,team_id,task_id,status,started) VALUES(?,?,?,?,?,?,'t')",
                            (f.oname(con, None, name), role, goal, team, task, "live"))

            f.spawn = fake_spawn
            f.gate = lambda con, **k: (True, "", {})
            f._ci_remote_head = lambda branch, batch=None: head
            f._ci_remote_heads = lambda: {"factory/t-0001": head}
            f._ci_wake_probe = lambda url, timeout: {"number": 1, "state": "OPEN", "headRefOid": head, "mergeStateStatus": "CLEAN", "statusCheckRollup": GREEN}
            f.review_autospawn_tick(con)
            con.commit()
        return calls

    def test_b1_prefixed_qa_ended_without_verdict_is_not_respawned_and_alerts_once(self):
        self.reviewed_on_h1()
        self.verdict(H2)
        self.assertEqual(self.pstep(H2), [("qa", "claude-qa-t-0001-22222222")])
        self.assertEqual(len(self.env.sql("SELECT 1 FROM agents WHERE name='claude-qa-t-0001-22222222'")), 1)
        self.env.sql("UPDATE agents SET status='exited'")
        # T-1600: one replacement under the same prefixed name, found by that name; after it also ends: no third agent, one alert
        self.assertEqual(self.pstep(H2), [("qa", "claude-qa-t-0001-22222222")])
        self.assertEqual(len(self.env.sql("SELECT 1 FROM agents WHERE name='claude-qa-t-0001-22222222'")), 1)
        self.assertEqual(self.env.sql("SELECT 1 FROM events WHERE action='review-no-verdict'"), [])
        self.env.sql("UPDATE agents SET status='exited'")
        self.assertEqual([self.pstep(H2) for _ in range(4)], [[], [], [], []])
        self.assertEqual(len(self.env.sql("SELECT 1 FROM events WHERE action='review-no-verdict'")), 1)
        self.assertEqual(len(self.env.sql("SELECT 1 FROM messages WHERE kind='request' AND subject LIKE 'review autospawn: qa ended%'")), 1)

    def test_b3_prefixed_rerun_worker_pass_on_a_moved_head_is_refused(self):
        self.reviewed_on_h1()
        self.assertEqual(len(self.pstep(H2)), 2)
        self.env.seed_pr(sha="b" * 40)
        r = self.env.run("verdict", "T-0001", "--pass", "--sha", "b" * 40, "--summary", GOOD, agent="claude-qa-t-0001-22222222")
        self.assertEqual(r.returncode, 5, r.stderr)
        self.assertIn("started you on head", r.stderr)
        self.assertEqual(self.env.sql("SELECT 1 FROM verdicts WHERE role='qa'"), [])

    def test_b2_respawned_same_name_worker_pass_on_the_current_head_is_accepted(self):
        # the tick started claude-qa-t-0001 on SHA_A (event 2026-01-02); the lead later respawned the same name (started 2026-01-03)
        self.env.seed_pr(sha="b" * 40)
        self.env.sql("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES('2026-01-02T00:00:00+00:00','tick','task','T-0001','review-autospawn',?)",
                     ('{"agent": "claude-qa-t-0001", "role": "qa", "sha": "%s"}' % SHA_A,))
        self.env.seed_agent("claude-qa-t-0001", "qa", goal="G-001", team=TEAM, task="T-0001", status="live", started="2026-01-03T00:00:00+00:00")
        r = self.env.run("verdict", "T-0001", "--pass", "--sha", "b" * 40, "--summary", GOOD, agent="claude-qa-t-0001")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual([v["head_sha"] for v in self.env.sql("SELECT head_sha FROM verdicts")], ["b" * 40])


class WaitEventTests(Base):
    """T-1098: a first-round pass that starts nothing says why, once per task, head and kind (it used to skip silently)."""

    def waits(self):
        return [json.loads(r["detail"]) for r in self.env.sql(
            "SELECT detail FROM events WHERE entity='task' AND entity_id='T-0001' AND action='review-autospawn-wait' ORDER BY id")]

    def test_closed_gate_logs_one_wait_event_per_hour(self):
        self.on()
        self.task()
        self.assertEqual(self.tick_step(gate_open=False, now=1_800_000_000), [])
        self.assertEqual(self.tick_step(gate_open=False, now=1_800_000_060), [])
        self.assertEqual([(w["kind"], w["head"]) for w in self.waits()], [("gate", SHA_A[:8])])
        self.assertIn("spawn gate closed for reviewer", self.waits()[0]["why"])
        self.assertEqual(self.tick_step(gate_open=False, now=1_800_000_000 + 3601), [])
        self.assertEqual(len(self.waits()), 2)

    def test_live_coder_logs_a_coder_wait(self):
        self.on()
        self.task()
        self.env.seed_agent("coder-t-0001", "coder", goal="G-001", team=TEAM, task="T-0001", status="live")
        self.assertEqual(self.tick_step(now=1_800_000_000), [])
        self.assertEqual([w["kind"] for w in self.waits()], ["coder"])

    def test_pending_ci_under_handoff_logs_a_ci_wait(self):
        self.on(coder_ci_handoff="resume")
        self.task()
        self.env.seed_pr(checks=[{"__typename": "CheckRun", "name": "ci", "status": "IN_PROGRESS", "conclusion": ""}])
        self.assertEqual(self.tick_step(stub_pr=False, now=1_800_000_000), [])
        self.assertEqual([w["kind"] for w in self.waits()], ["ci"])


class RespawnTests(Base):
    """T-1600 (G-058 F10): a tick-started reviewer/qa that ended with no verdict on the head gets ONE replacement through the same
    gates (event review-respawn, no mail); a second ending is the old alert. A tick-started reviewer/qa live longer than
    review_verdict_timeout_min with no verdict gets one stop request."""

    QA2 = "qa-t-0001-22222222"
    T0 = "2026-01-01T00:00:00+00:00"
    T0_EPOCH = 1767225600  # T0 as seconds since the epoch

    def setUp(self):
        Base.setUp(self)
        self.on()
        self.task()
        self.env.sql("UPDATE tasks SET branch='factory/t-0001' WHERE id='T-0001'")
        self.probes = 0

    step, verdict, reviewed_on_h1 = NewHeadTests.step, NewHeadTests.verdict, NewHeadTests.reviewed_on_h1

    def events(self, action):
        return [json.loads(r["detail"]) for r in self.env.sql(
            "SELECT detail FROM events WHERE entity='task' AND entity_id='T-0001' AND action=? ORDER BY id", (action,))]

    def requests(self):
        return self.env.sql("SELECT recipient, subject, body FROM messages WHERE kind='request' ORDER BY id")

    def qa_ended_without_verdict(self):
        """Reviewer verdict on H2, the tick's qa on H2 started and ended with no qa verdict."""
        self.reviewed_on_h1()
        self.verdict(H2, verdict="pass")
        self.assertEqual(self.step(H2), [("qa", self.QA2)])
        self.env.sql("UPDATE agents SET status='exited'")

    def test_respawn_once_with_event_and_no_message(self):
        self.qa_ended_without_verdict()
        mails = len(self.env.sql("SELECT 1 FROM messages"))
        self.assertEqual(self.step(H2), [("qa", self.QA2)])  # exactly one replacement, same head
        self.assertEqual(self.events("review-respawn"), [{"agent": self.QA2, "role": "qa", "head": "22222222"}])
        self.assertEqual(len(self.env.sql("SELECT 1 FROM messages")), mails)  # no fyi, no request
        self.assertEqual(self.events("review-no-verdict"), [])
        self.assertEqual([self.step(H2) for _ in range(3)], [[], [], []])  # the live replacement is not doubled
        self.assertEqual(len(self.events("review-respawn")), 1)

    def test_replacement_verdict_completes_the_round_and_the_judge_starts(self):
        self.qa_ended_without_verdict()
        self.assertEqual(self.step(H2), [("qa", self.QA2)])
        self.env.seed_pr(sha=H2)
        r = self.env.run("verdict", "T-0001", "--pass", "--sha", H2, "--summary", GOOD, agent=self.QA2)  # the replacement's own verdict
        self.assertEqual(r.returncode, 0, r.stderr)
        self.env.sql("UPDATE agents SET status='done'")
        self.assertEqual([(v["role"], v["verdict"], v["head_sha"]) for v in self.env.sql(
            "SELECT role, verdict, head_sha FROM verdicts WHERE head_sha=? ORDER BY id", (H2,))],
            [("reviewer", "pass", H2), ("qa", "pass", H2)])
        self.assertEqual(self.step(H2), [])  # round complete: nothing more to start, no alert
        self.assertEqual(self.requests(), [])
        spawned = []
        with self.env.load_factory() as f:
            con = f.db()
            f.gate = lambda con, **k: (True, "", {})
            f._ci_wake_probe = lambda url, timeout: {"number": 1, "state": "OPEN", "headRefOid": H2, "statusCheckRollup": GREEN,
                                                      "mergeStateStatus": "CLEAN"}
            f.judge_pr_ready = lambda con, url, behind_ok=False: (True, "CI green")
            f.gh_json = lambda *a, **k: {"url": "https://example.invalid/zz/repo"}
            f.spawn = lambda con, role, goal=None, team=None, task=None, name=None, **kw: spawned.append((role, name))
            f.judge_autospawn_tick(con)
            con.commit()
        self.assertEqual(spawned, [("judge", "judge-t-0001")])  # the judge path proceeds as for a normal round

    def test_replacement_also_ends_without_verdict_no_third_agent_one_alert(self):
        self.qa_ended_without_verdict()
        self.assertEqual(self.step(H2), [("qa", self.QA2)])
        self.env.sql("UPDATE agents SET status='exited'")
        self.assertEqual([self.step(H2) for _ in range(4)], [[], [], [], []])  # no third agent
        self.assertEqual(self.events("review-no-verdict"), [{"agent": self.QA2, "role": "qa", "head": "22222222"}])
        self.assertEqual(len(self.events("review-respawn")), 1)
        mails = self.requests()
        self.assertEqual([m["recipient"] for m in mails], [LEAD])
        self.assertEqual(mails[0]["subject"], "review autospawn: qa ended with no verdict on T-0001 @22222222")
        self.assertIn("The tick starts no further qa on this head: spawn one or decide.", mails[0]["body"])

    def test_head_alerted_before_the_rule_is_not_respawned(self):
        # a head whose lead was already told "the tick starts no further qa" (alert sent before T-1600) keeps that promise
        self.qa_ended_without_verdict()
        self.env.sql("INSERT INTO throttle(key,ts) VALUES(?,'1')", (f"review-noverdict:T-0001:{H2}:qa",))
        self.assertEqual([self.step(H2) for _ in range(2)], [[], []])
        self.assertEqual(self.events("review-respawn"), [])

    def test_closed_gate_holds_the_replacement_until_it_opens(self):
        self.qa_ended_without_verdict()
        self.gate_open = False
        self.assertEqual([self.step(H2) for _ in range(2)], [[], []])
        self.assertEqual([(w["kind"], w["head"]) for w in self.events("review-autospawn-wait")], [("gate", "22222222")])
        self.assertEqual(self.events("review-respawn"), [])
        self.assertEqual(self.requests(), [])  # a held replacement is not "ended twice": no alert
        self.gate_open = True
        self.assertEqual(self.step(H2), [("qa", self.QA2)])
        self.assertEqual(len(self.events("review-respawn")), 1)

    # --- review_verdict_timeout_min ---

    def tick_started(self, name="reviewer-t-0001", role="reviewer", head=SHA_A):
        """A live agent the tick started on head at T0 (its review-autospawn event names it)."""
        self.env.seed_agent(name, role, goal="G-001", team=TEAM, task="T-0001", status="live", started=self.T0)
        self.env.sql("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES('2026-01-01T00:00:01+00:00','tick','task','T-0001','review-autospawn',?)",
                     (json.dumps({"agent": name, "role": role, "sha": head}),))

    def stops(self):
        return [r["name"] for r in self.env.sql("SELECT name FROM agents WHERE stop_requested_at IS NOT NULL ORDER BY name")]

    def at(self, minutes):
        self.tick_step(now=self.T0_EPOCH + minutes * 60)

    def test_reviewer_live_past_the_timeout_gets_one_stop_request(self):
        self.tick_started()
        self.env.seed_agent("qa-t-0001", "qa", goal="G-001", team=TEAM, task="T-0001", status="live", started=self.T0)  # the lead's: no event
        self.at(46)
        self.at(47)
        self.assertEqual(self.stops(), ["reviewer-t-0001"])  # never the qa the tick did not start
        stop = self.env.sql("SELECT recipient, kind, body FROM messages WHERE subject='STOP'")
        self.assertEqual([(m["recipient"], m["kind"]) for m in stop], [("reviewer-t-0001", "decision")])  # one request, not one per tick
        self.assertIn("review_verdict_timeout_min: live 46 min (> 45)", stop[0]["body"])
        self.assertEqual(self.events("review-verdict-timeout"),
                         [{"agent": "reviewer-t-0001", "role": "reviewer", "head": "aaaaaaaa", "live_min": 46, "limit_min": 45}])
        self.assertEqual(len(self.env.sql("SELECT 1 FROM events WHERE entity='agent' AND action='stop-requested'")), 1)

    def test_reviewer_live_under_the_timeout_is_not_stopped(self):
        self.tick_started()
        self.at(44)
        self.assertEqual(self.stops(), [])
        self.assertEqual(self.env.sql("SELECT 1 FROM messages WHERE subject='STOP'"), [])

    def test_timeout_zero_is_off(self):
        self.ok("config", "review_verdict_timeout_min", "0")
        self.tick_started()
        self.at(300)
        self.assertEqual(self.stops(), [])
        self.assertEqual(self.env.sql("SELECT 1 FROM messages WHERE subject='STOP'"), [])

    def test_overdue_reviewer_with_its_verdict_recorded_is_not_stopped(self):
        self.tick_started()
        self.verdict(SHA_A)
        self.at(46)
        self.assertEqual(self.stops(), [])

    def fast_check_live(self):
        """G-070: fast_lane=on, T-0001 in the fast lane, the tick's fast check started on SHA_A at T0."""
        self.ok("config", "fast_lane", "on")
        self.env.sql("INSERT INTO config(key,value) VALUES('lane:T-0001','fast')")
        self.tick_started(name="fastcheck-t-0001-aaaaaaaa", role="qa")

    def lane(self):
        return self.env.sql("SELECT value FROM config WHERE key='lane:T-0001'")[0]["value"]

    def test_fast_check_past_its_timeout_is_stopped_and_the_task_escalates(self):
        self.fast_check_live()
        self.at(11)  # fast_lane_check_timeout_s 600 by default, far under review_verdict_timeout_min 45
        self.assertEqual(self.stops(), ["fastcheck-t-0001-aaaaaaaa"])
        stop = self.env.sql("SELECT body FROM messages WHERE subject='STOP'")
        self.assertIn("fast_lane_check_timeout_s: fast check live 660 s (> 600)", stop[0]["body"])
        self.assertEqual(self.lane(), "normal")
        esc = self.env.sql("SELECT detail FROM events WHERE action='lane-escalated' AND entity_id='T-0001'")
        self.assertEqual(len(esc), 1)
        self.assertIn("fast check timed out after 600 s with no verdict", esc[0]["detail"])

    def test_fast_check_under_its_timeout_or_with_a_verdict_keeps_running(self):
        self.fast_check_live()
        self.at(9)
        self.assertEqual(self.stops(), [])
        self.env.seed_pr(sha=SHA_A)
        r = self.env.run("verdict", "T-0001", "--pass", "--sha", SHA_A, "--summary", GOOD, agent="fastcheck-t-0001-aaaaaaaa")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.at(30)
        self.assertEqual(self.stops(), [])
        self.assertEqual(self.lane(), "fast")

    def test_timeout_key_default_and_not_an_owner_cap(self):
        with self.env.load_factory() as f:
            self.assertEqual(f.DEFAULT_CONFIG["review_verdict_timeout_min"], "45")
            self.assertNotIn("review_verdict_timeout_min", f.OWNER_CAP_KEYS)


class DependsOnTests(Base):
    """T-1323: no reviewer/qa autospawn while a depends_on task is not done or the task is blocked."""

    def dep(self, dep_status):
        self.task("T-0009", status=dep_status, pr_url=None)
        self.task("T-0001")
        self.env.sql("UPDATE tasks SET depends_on=? WHERE id='T-0001'", (json.dumps(["T-0009"]),))

    def skips(self):
        return self.env.sql("SELECT detail FROM events WHERE entity_id='T-0001' AND action='review-autospawn-skip'")

    def test_not_done_dependency_blocks_and_names_it(self):
        self.on()
        self.dep("in_progress")
        self.assertEqual(self.tick_step(now=1_800_000_000), [])
        self.assertIn("T-0009", self.skips()[0]["detail"])

    def test_done_dependency_lets_autospawn_happen(self):
        self.on()
        self.dep("done")
        calls = self.tick_step(now=1_800_000_000)
        self.assertEqual([c["role"] for c in calls], ["reviewer", "qa"])
        self.assertEqual(self.skips(), [])

    def test_cancelled_dependency_lets_autospawn_happen(self):
        # T-1692: a cancelled dep never finishes; it must not skip review forever
        self.on()
        self.dep("cancelled")
        calls = self.tick_step(now=1_800_000_000)
        self.assertEqual([c["role"] for c in calls], ["reviewer", "qa"])
        self.assertEqual(self.skips(), [])

    def test_blocked_task_is_not_a_candidate(self):
        # T-1692: the candidates query only selects in_review, so a blocked task never reaches the tick (no dead skip branch)
        self.on()
        self.task("T-0001", status="blocked")
        with self.env.load_factory() as f:
            con = f.db()
            self.assertEqual([r["id"] for r in f.review_candidates(con)], [])
        self.assertEqual(self.tick_step(now=1_800_000_000), [])


FACTORY_URL = "https://github.com/acme/factory/pull/9"
WIDGET_URL = "https://github.com/acme/widgetapp/pull/7"
OTHER_URL = "https://github.com/acme/other/pull/8"


class ForeignRepoTests(Base):
    """T-1675: the head-branch check of review/qa autospawn asks the task's PR repo, not the factory origin."""

    def setUp(self):
        super().setUp()
        self.on()
        self.env.sql("INSERT INTO projects(id,name,gh_repo,created) VALUES('dib','dib','acme/widgetapp','t')")
        self.env.sql("INSERT INTO projects(id,name,gh_repo,created) VALUES('oth','oth','acme/other','t')")
        self.env.sql("INSERT INTO projects(id,name,gh_repo,created) VALUES('hm','hm','acme/factory','t')")
        self.remotes = {}  # ls-remote target -> {branch: sha} or None (unreadable)
        self.ls = []  # (target, repo, ref pattern) per ls-remote call

    def seed(self, tid, url, project, branch=None):
        n = int(tid.split("-")[1])
        goal = f"G-{project}"
        if not self.env.sql("SELECT 1 FROM goals WHERE id=?", (goal,)):
            self.env.seed_goal(goal, project=project)
        self.env.seed_task(tid, goal=goal, pr_url=url, team=TEAM)
        self.env.sql("UPDATE tasks SET branch=? WHERE id=?", (branch or f"factory/t-{n:04d}", tid))
        self.env.seed_pr(url=url)

    def run_tick(self, rerun=False):
        calls = []
        with self.env.load_factory() as f:
            con = f.db()
            real = f._git_rc

            def fake_git(args, timeout=60, env=None, input=None, repo=None):
                if args[:1] == ["ls-remote"]:
                    self.ls.append((args[2], repo, args[-1]))
                    heads = self.remotes.get(args[2])
                    if heads is None:
                        return 128, "", "unreadable"
                    pat = args[-1]
                    out = [f"{sha}\trefs/heads/{b}" for b, sha in heads.items()
                           if pat == f"refs/heads/{b}" or (pat.endswith("/*") and b.startswith(pat[len("refs/heads/"):-1]))]
                    return 0, "\n".join(out), ""
                return real(args, timeout=timeout, env=env, input=input, repo=repo)

            f._git_rc = fake_git
            f._origin_urls = lambda: ["https://github.com/acme/factory.git"]
            f._ci_wake_probe = lambda url, timeout: {"number": 1, "state": "OPEN", "headRefOid": H1, "statusCheckRollup": GREEN}
            f.spawn = lambda con, role, goal=None, team=None, task=None, name=None, prompt=None, **kw: (
                calls.append((role, task)),
                con.execute("INSERT INTO agents(name,role,goal_id,team_id,task_id,status,started) VALUES(?,?,?,?,?,'live','t')",
                            (name, role, goal, team, task)))
            f.gate = lambda con, **k: (True, "", {})
            (f.review_rerun_tick if rerun else f.review_autospawn_tick)(con)
            con.commit()
        return calls

    def events(self, action):
        return [r["detail"] for r in self.env.sql("SELECT detail FROM events WHERE action=?", (action,))]

    DIB = "https://github.com/acme/widgetapp.git"

    def test_foreign_repo_branch_found_on_project_remote_spawns_review(self):
        self.seed("T-0001", WIDGET_URL, "dib")
        self.remotes = {"origin": {}, self.DIB: {"factory/t-0001": H1}}
        self.assertEqual(sorted(c[0] for c in self.run_tick()), ["qa", "reviewer"])
        self.assertEqual(self.events("review-autospawn-skip"), [])
        self.assertEqual(self.ls, [(self.DIB, None, "refs/heads/factory/*")])  # https form: no repo_path checkout

    def test_rerun_reads_foreign_head_from_the_project_remote(self):
        self.seed("T-0001", WIDGET_URL, "dib")
        self.env.seed_agent("reviewer-t-0001", "reviewer", goal="G-001", team=TEAM, task="T-0001", status="exited")
        self.env.seed_agent("qa-t-0001", "qa", goal="G-001", team=TEAM, task="T-0001", status="exited")
        self.env.sql("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) "
                     "VALUES('T-0001','u',?,'reviewer','fail','s','a','t')", (H1,))
        self.remotes = {"origin": {}, self.DIB: {"factory/t-0001": H2}}
        self.run_tick(rerun=True)
        self.assertEqual(self.events("review-autospawn-skip"), [])
        self.assertEqual(self.ls[0][0], self.DIB)  # head_known: the head came from widgetapp, no origin lookup

    def test_factory_repo_keeps_one_origin_ls_remote_for_several_prs(self):
        self.seed("T-0001", FACTORY_URL, "hm")
        self.seed("T-0002", FACTORY_URL.replace("/9", "/10"), "hm")
        self.seed("T-0003", FACTORY_URL.replace("/9", "/11"), "hm")
        self.remotes = {"origin": {"factory/t-0001": H1, "factory/t-0002": H1, "factory/t-0003": H1}}
        self.assertEqual(len(self.run_tick()), 6)
        self.assertEqual(self.ls, [("origin", None, "refs/heads/factory/*")])  # one call, origin, in HOME

    def test_one_ls_remote_per_foreign_repo(self):
        self.seed("T-0001", WIDGET_URL, "dib")
        self.seed("T-0002", WIDGET_URL.replace("/7", "/70"), "dib")
        self.seed("T-0003", OTHER_URL, "oth")
        oth = "https://github.com/acme/other.git"
        self.remotes = {self.DIB: {"factory/t-0001": H1, "factory/t-0002": H1}, oth: {"factory/t-0003": H1}}
        self.assertEqual(len(self.run_tick()), 6)
        self.assertEqual(sorted(c[0] for c in self.ls), sorted([self.DIB, oth]))

    def test_unreadable_foreign_remote_fails_closed(self):
        self.seed("T-0001", WIDGET_URL, "dib")
        self.remotes = {"origin": {"factory/t-0001": H1}, self.DIB: None}
        self.assertEqual(self.run_tick(), [])
        self.assertEqual(self.events("review-autospawn-skip"), [])
        self.assertEqual(len(self.events("review-autospawn-error")), 1)

    def test_project_checkout_is_used_when_its_origin_is_the_pr_repo(self):
        import tempfile
        from pathlib import Path
        d = Path(tempfile.mkdtemp(prefix="zz-test-proj-"))
        (d / ".git").mkdir()
        self.env.sql("UPDATE projects SET repo_path=? WHERE id='dib'", (str(d),))
        self.seed("T-0001", WIDGET_URL, "dib")
        self.remotes = {"origin": {"factory/t-0001": H1}}
        with self.env.load_factory() as f:
            con = f.db()
            real = f._git_rc
            f._git_rc = lambda args, **kw: (0, "ssh://github.com/acme/widgetapp.git", "") if args[:2] == ["remote", "get-url"] and kw.get("repo") == str(d) else real(args, **kw)
            t = con.execute("SELECT * FROM tasks WHERE id='T-0001'").fetchone()
            b = f._RepoBatch()
            f._origin_urls = lambda: ["https://github.com/acme/factory.git"]
            sub, kw = f._review_remote(con, t, b)
        self.assertEqual(kw, {"remote": (str(d), "origin")})

    def test_project_without_gh_repo_keeps_origin(self):
        self.seed("T-0001", FACTORY_URL, "hm")
        self.env.sql("UPDATE projects SET gh_repo=NULL WHERE id='hm'")
        self.remotes = {"origin": {"factory/t-0001": H1}}
        self.assertEqual(len(self.run_tick()), 2)
        self.assertEqual(self.ls, [("origin", None, "refs/heads/factory/*")])


class GateReviewOnPushTests(NewHeadTests):
    """T-1828: with projects.gate_cmd set, review starts on push without waiting for GitHub checks (first round and rerun);
    gate_push_tick queues one gate run per pushed head and notes the result on the task. Empty gate_cmd: unchanged."""

    def setUp(self):
        super().setUp()
        self.env.seed_project()
        self.env.sql("UPDATE goals SET project_id='zz-test-proj' WHERE id='G-001'")
        self.set_gate("true")

    # inherited NewHeadTests cases that assert the empty-gate_cmd wait; the same behaviour is asserted without gate_cmd below
    test_pending_ci_on_a_new_head_logs_one_ci_wait = None
    test_pending_or_red_head_waits = None

    def set_gate(self, cmd):
        self.env.sql("UPDATE projects SET gate_cmd=? WHERE id='zz-test-proj'", (cmd,))

    def push_tick(self, head=H1):
        with self.env.load_factory() as f:
            con = f.db()
            f._ci_remote_head = lambda branch, batch=None, remote=None: head
            f.gate_push_tick(con)
            con.commit()

    def gate_rows(self):
        return self.env.sql("SELECT * FROM gate_runs ORDER BY id")

    def notes(self):
        return [r["detail"] for r in self.env.sql(
            "SELECT detail FROM events WHERE entity='task' AND entity_id='T-0001' AND action='note' ORDER BY id")]

    def test_first_round_starts_with_pending_checks_under_handoff(self):
        self.ok("config", "coder_ci_handoff", "resume")
        self.assertEqual([r for r, _ in self.step(H1, rollup=PENDING)], ["reviewer", "qa"])

    def test_first_round_still_waits_without_gate_cmd(self):
        self.ok("config", "coder_ci_handoff", "resume")
        self.set_gate("")
        self.assertEqual(self.step(H1, rollup=PENDING), [])
        self.assertEqual([w["kind"] for w in self.rerun_waits()], ["ci"])

    def test_rerun_starts_on_a_new_head_with_pending_checks(self):
        self.reviewed_on_h1()
        self.assertEqual(sorted(r for r, _ in self.step(H2, rollup=PENDING)), ["qa", "reviewer"])

    def test_rerun_still_waits_without_gate_cmd(self):
        self.reviewed_on_h1()
        self.set_gate("")
        self.assertEqual(self.step(H2, rollup=PENDING), [])

    def test_live_coder_still_holds_review_with_gate_cmd(self):
        self.ok("config", "coder_ci_handoff", "resume")
        self.env.seed_agent("coder-t-0001", "coder", goal="G-001", team=TEAM, task="T-0001", status="live")
        self.assertEqual(self.step(H1, rollup=PENDING), [])

    def test_push_queues_one_gate_run_and_notes_it(self):
        self.push_tick()
        self.push_tick()
        rows = self.gate_rows()
        self.assertEqual([(r["head_sha"], r["status"], r["kind"], r["task_id"]) for r in rows], [(H1, "queued", "pr", "T-0001")])
        self.assertEqual(len(self.notes()), 1)
        self.assertIn(H1, self.notes()[0])

    def test_new_head_queues_a_second_run_once_the_first_finished(self):
        self.push_tick(H1)
        self.env.sql("UPDATE gate_runs SET status='pass' WHERE id=1")
        self.push_tick(H2)
        self.assertEqual([(r["head_sha"], r["status"]) for r in self.gate_rows()], [(H1, "pass"), (H2, "queued")])

    def test_new_head_while_an_older_run_is_active_adds_no_row_and_no_repeated_note(self):
        self.push_tick(H1)
        self.env.sql("UPDATE gate_runs SET status='running' WHERE id=1")
        for _ in range(3):
            self.push_tick(H2)
        self.assertEqual([(r["head_sha"], r["status"]) for r in self.gate_rows()], [(H1, "running")])
        self.assertEqual([n for n in self.notes() if H2 in n], [])

    def test_no_gate_run_without_gate_cmd(self):
        self.set_gate("")
        self.push_tick()
        self.assertEqual(self.gate_rows(), [])
        self.assertEqual(self.notes(), [])

    def test_finished_run_is_noted_once_with_sha_and_log_path(self):
        self.env.sql("INSERT INTO gate_runs(project_id,task_id,pr_url,kind,head_sha,status,cmd,log_path,note,created) "
                     "VALUES('zz-test-proj','T-0001','u','pr',?,'fail','true','/x/logs/gate/7.log','exit 1','t')", (H1,))
        self.push_tick(H1)
        self.push_tick(H1)
        notes = [n for n in self.notes() if "local gate FAIL" in n]
        self.assertEqual(len(notes), 1)
        self.assertIn(H1, notes[0])
        self.assertIn("/x/logs/gate/7.log", notes[0])
