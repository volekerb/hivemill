"""T-1004 (G-018, T-0980 task C): with coder_ci_handoff on, the tick starts the handed-off coder again on a red head of an
in_review PR (resume the recorded session, or a fresh one), once per (task, head), within coder_ci_resume_max and 2 starts a tick
(docs/lead-wake.md#coder-restart-on-red-ci-t-1004)."""
import json
import unittest

from harness import FactoryTestCase

TEAM, LEAD = "g-001-a", "lead-g-001-a"
SHA = "a" * 40
SHA2 = "b" * 40
RUN = "987654"
RED = [{"name": "unittest", "status": "COMPLETED", "conclusion": "FAILURE",
        "detailsUrl": f"https://github.com/zz-test/repo/actions/runs/{RUN}/job/5"},
       {"name": "lint", "status": "COMPLETED", "conclusion": "SUCCESS"}]
GREEN = [{"name": "unittest", "status": "COMPLETED", "conclusion": "SUCCESS"}]
PENDING = [{"name": "unittest", "status": "IN_PROGRESS", "conclusion": ""}]
NOW = 1_800_000_000.0


def pr_url(n):
    return f"https://github.com/zz-test/repo/pull/{n}"


def other_pr_url(n):
    return f"https://github.com/zz-test/other/pull/{n}"


class Base(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(TEAM, lead=LEAD)
        e.seed_agent(LEAD, "lead", goal="G-001", team=TEAM, status="exited")
        self.ok("config", "coder_ci_handoff", "resume")
        self.wt = e.home / "wt"
        self.wt.mkdir()

    def task(self, n=1, coder="done", session="sess-1", worktree=True, status="in_review", url=None, **kw):
        tid = f"T-{n:04d}"
        self.env.seed_task(tid, status=status, pr_url=url or pr_url(n), team=TEAM)
        self.env.sql("UPDATE tasks SET branch=?, worktree=? WHERE id=?", (f"factory/t-{n:04d}", str(self.wt) if worktree else None, tid))
        if coder:
            self.env.seed_agent(f"coder-{tid.lower()}", "coder", goal="G-001", team=TEAM, task=tid, status=coder)
            self.env.sql("UPDATE agents SET session_id=? WHERE name=?", (session, f"coder-{tid.lower()}"))
        return tid

    def tick(self, prs, gate_open=True, real_spawn=False, now=NOW, refuse=()):
        """prs: {pr url: (head sha, rollup, mergeStateStatus)}. Returns the fake spawn calls (empty with real_spawn)."""
        calls = []
        self.probed = getattr(self, "probed", [])
        with self.env.load_factory() as f:
            con = f.db()
            f._ci_wake_probe = lambda url, timeout: self.probed.append(url) or {
                "number": int(url.rsplit("/", 1)[1]), "state": "OPEN", "headRefOid": prs[url][0], "statusCheckRollup": prs[url][1],
                "mergeStateStatus": prs[url][2] if len(prs[url]) > 2 else "CLEAN"}
            f._ci_remote_heads = lambda: {f"factory/t-{int(u.rsplit('/', 1)[1]):04d}": v[0] for u, v in prs.items()}
            f.digest_clock = lambda: f.dt.datetime.fromtimestamp(now, f.dt.timezone.utc)
            if not real_spawn:
                def fake_spawn(con, role, goal=None, team=None, task=None, name=None, prompt=None, resume=None, **kw):
                    if task in refuse:
                        raise SystemExit(1)
                    calls.append({"role": role, "task": task, "name": name, "prompt": prompt, "resume": resume})
                    con.execute("UPDATE agents SET status='live' WHERE name=?", (name,))
                f.spawn = fake_spawn
                f.gate = lambda con, **k: (gate_open, "" if gate_open else "closed", {})
            f.coder_ci_resume_tick(con)
            con.commit()
        return calls

    def mails(self, subject_like, to=None):
        q = "SELECT recipient, subject, body FROM messages WHERE subject LIKE ?"
        return [r for r in self.env.sql(q, (subject_like,)) if to is None or r["recipient"] == to]

    def starts(self):
        return [c["argv"] for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]]


class ResumeTests(Base):
    def test_resume_mode_spawn_resumes_the_session_and_kickoff_names_check_and_run(self):
        self.task()
        calls = self.tick({pr_url(1): (SHA, RED)})
        self.assertEqual([(c["role"], c["task"], c["name"], c["resume"]) for c in calls], [("coder", "T-0001", "coder-t-0001", "sess-1")])
        mail = self.mails("CI red:%", to="coder-t-0001")
        self.assertEqual(len(mail), 1)
        for text in (calls[0]["prompt"], mail[0]["body"]):
            self.assertIn("unittest (run 987654)", text)
            self.assertIn(f"gh run view {RUN} --log-failed", text)
            self.assertIn(SHA[:8], text)
            self.assertNotIn("lint (run", text)  # the passing check is not named as failed
        before = self.env.sql("SELECT attempts FROM tasks WHERE id='T-0001'")[0][0]
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0][0], "in_review")
        self.assertEqual(before, 0)

    def test_real_spawn_argv_has_resume_session_id(self):
        self.task()
        self.tick({pr_url(1): (SHA, RED)}, real_spawn=True)
        argv = self.starts()
        self.assertEqual(len(argv), 1, self.env.calls("herdr"))
        self.assertEqual(argv[0][argv[0].index("--resume") + 1], "sess-1")
        self.assertNotIn("--session-id", argv[0])

    def test_missing_session_or_worktree_starts_fresh_with_the_same_log_hint(self):
        for kw in ({"session": None}, {"worktree": False}):
            with self.subTest(kw=kw):
                self.env.sql("DELETE FROM agents WHERE role='coder'")
                self.env.sql("DELETE FROM tasks")
                self.env.sql("DELETE FROM agents WHERE role='coder'")
                self.env.sql("DELETE FROM throttle")
                self.task(**kw)
                calls = self.tick({pr_url(1): (SHA, RED)})
                self.assertEqual(len(calls), 1)
                self.assertIsNone(calls[0]["resume"])
                self.assertIn(f"gh run view {RUN} --log-failed", calls[0]["prompt"])
                self.assertIn("unittest (run 987654)", calls[0]["prompt"])
                self.assertIn("fresh session", calls[0]["prompt"])
                self.assertEqual(self.mails("CI red:%", to="coder-t-0001"), [])  # spawn() itself sends the fresh kickoff

    def test_fresh_mode_never_resumes(self):
        self.ok("config", "coder_ci_handoff", "fresh")
        self.task()
        calls = self.tick({pr_url(1): (SHA, RED)})
        self.assertEqual(len(calls), 1)
        self.assertIsNone(calls[0]["resume"])

    def test_once_per_head_and_a_new_red_head_starts_again(self):
        self.task()
        self.assertEqual(len(self.tick({pr_url(1): (SHA, RED)})), 1)
        self.env.sql("UPDATE agents SET status='done' WHERE name='coder-t-0001'")
        self.assertEqual(self.tick({pr_url(1): (SHA, RED)}), [])  # same head: no second start
        self.assertEqual(len(self.tick({pr_url(1): (SHA2, RED)})), 1)

    def test_green_or_pending_head_starts_nothing(self):
        self.task()
        for rollup in (GREEN, PENDING):
            self.assertEqual(self.tick({pr_url(1): (SHA, rollup)}), [])

    def test_event_and_lead_fyi_recorded(self):
        self.task()
        self.tick({pr_url(1): (SHA, RED)})
        ev = self.env.sql("SELECT detail FROM events WHERE action='coder-ci-resume' AND entity_id='T-0001'")
        self.assertEqual(json.loads(ev[0][0])["mode"], "resume")
        self.assertEqual(len(self.mails("coder restarted on red CI%", to=LEAD)), 1)


class LimitTests(Base):
    def test_third_red_starts_nothing_and_mails_the_lead_once(self):
        self.task()
        for sha in (SHA, SHA2):
            self.assertEqual(len(self.tick({pr_url(1): (sha, RED)})), 1)
            self.env.sql("UPDATE agents SET status='done' WHERE name='coder-t-0001'")
        self.assertEqual(self.tick({pr_url(1): ("c" * 40, RED)}), [])
        self.assertEqual(self.tick({pr_url(1): ("d" * 40, RED)}), [])
        self.assertEqual(len(self.mails("coder resume limit reached%", to=LEAD)), 1)

    def test_resume_max_is_configurable(self):
        self.ok("config", "coder_ci_resume_max", "1")
        self.task()
        self.assertEqual(len(self.tick({pr_url(1): (SHA, RED)})), 1)
        self.env.sql("UPDATE agents SET status='done' WHERE name='coder-t-0001'")
        self.assertEqual(self.tick({pr_url(1): (SHA2, RED)}), [])
        self.assertEqual(len(self.mails("coder resume limit reached%")), 1)

    def test_five_red_tasks_start_two_per_tick(self):
        for n in range(1, 6):
            self.task(n)
        prs = {pr_url(n): (SHA, RED) for n in range(1, 6)}
        first = self.tick(prs)
        self.assertEqual([c["task"] for c in first], ["T-0001", "T-0002"])
        self.assertEqual([c["task"] for c in self.tick(prs)], ["T-0003", "T-0004"])

    def test_main_red_starts_nothing(self):
        self.ok("config", "red_main_freeze", "1")
        self.env.sql("INSERT INTO config(key,value) VALUES('red_main:zz-test/repo',?)", (json.dumps({"run": "9", "sha": "x", "url": "u", "since": "t"}),))
        self.task()
        self.assertEqual(self.tick({pr_url(1): (SHA, RED)}), [])
        self.env.sql("DELETE FROM config WHERE key='red_main:zz-test/repo'")
        self.assertEqual(len(self.tick({pr_url(1): (SHA, RED)})), 1)  # main green again: the red head is retried

    def test_closed_gate_retries_next_tick(self):
        self.task()
        self.assertEqual(self.tick({pr_url(1): (SHA, RED)}, gate_open=False), [])
        self.assertEqual(len(self.tick({pr_url(1): (SHA, RED)})), 1)


class RefusalTests(Base):
    def test_refused_spawn_does_not_stop_later_red_tasks_and_retries_are_bounded(self):
        self.task(1)
        self.task(2)
        prs = {pr_url(1): (SHA, RED), pr_url(2): (SHA, RED)}
        first = self.tick(prs, refuse=("T-0001",))
        self.assertEqual([c["task"] for c in first], ["T-0002"])  # the refusing first task does not starve the one behind it
        self.env.sql("UPDATE agents SET status='done' WHERE name='coder-t-0002'")
        for _ in range(4):
            self.tick(prs, refuse=("T-0001",))
        errs = self.env.sql("SELECT count(*) FROM events WHERE action='coder-ci-resume-error' AND entity_id='T-0001'")[0][0]
        self.assertEqual(errs, 3)  # bounded: no retry after the third refusal on this head
        self.assertEqual(len(self.mails("coder restart refused%", to=LEAD)), 1)  # one lead mail, not one per tick
        self.assertEqual(len(self.tick(prs)), 0)  # given up on this head even when spawn would now work


class MainRedRepoTests(Base):
    def test_red_main_in_one_repo_does_not_stop_a_task_of_another_repo(self):
        self.ok("config", "red_main_freeze", "1")
        self.env.sql("INSERT INTO config(key,value) VALUES('red_main:zz-test/repo',?)", (json.dumps({"run": "9", "sha": "x", "url": "u", "since": "t"}),))
        self.task(1)
        self.task(2, url=other_pr_url(2))
        calls = self.tick({pr_url(1): (SHA, RED), other_pr_url(2): (SHA, RED)})
        self.assertEqual([c["task"] for c in calls], ["T-0002"])  # repo A (T-0001, sorts first) starts nothing; repo B starts

    def test_red_main_in_the_later_repo_does_not_stop_the_earlier_one(self):
        self.ok("config", "red_main_freeze", "1")
        self.env.sql("INSERT INTO config(key,value) VALUES('red_main:zz-test/other',?)", (json.dumps({"run": "9", "sha": "x", "url": "u", "since": "t"}),))
        self.task(1)
        self.task(2, url=other_pr_url(2))
        calls = self.tick({pr_url(1): (SHA, RED), other_pr_url(2): (SHA, RED)})
        self.assertEqual([c["task"] for c in calls], ["T-0001"])


class SkipTests(Base):
    def test_live_starting_queued_coder_blocked_hold_and_flag_off_start_nothing(self):
        prs = {pr_url(1): (SHA, RED)}
        for st in ("live", "starting", "queued"):
            self.env.sql("DELETE FROM tasks")
            self.env.sql("DELETE FROM agents WHERE role='coder'")
            self.task(coder=st)
            self.assertEqual(self.tick(prs), [], st)
        self.env.sql("DELETE FROM agents WHERE role='coder'")
        self.env.sql("UPDATE tasks SET status='blocked' WHERE id='T-0001'")
        self.assertEqual(self.tick(prs), [])
        self.env.sql("UPDATE tasks SET status='in_review', judge_hold_reason='hold' WHERE id='T-0001'")
        self.assertEqual(self.tick(prs), [])
        self.env.sql("UPDATE tasks SET judge_hold_reason=NULL WHERE id='T-0001'")
        self.ok("config", "coder_ci_handoff", "off")
        self.assertEqual(self.tick(prs), [])
        self.ok("config", "coder_ci_handoff", "resume")
        self.assertEqual(len(self.tick(prs)), 1)  # the same state does start with the flag on

    def test_conflicted_pr_mails_the_lead_and_starts_nothing(self):
        self.task()
        self.assertEqual(self.tick({pr_url(1): (SHA, RED, "DIRTY")}), [])
        self.assertEqual(self.tick({pr_url(1): (SHA, RED, "DIRTY")}), [])
        self.assertEqual(len(self.mails("coder not resumed, PR conflicted%", to=LEAD)), 1)

    def test_conflicted_pr_on_a_cursor_goal_mails_the_cursor_director_not_the_claude_lead(self):
        self.task()
        self.env.sql("UPDATE goals SET org='cursor' WHERE id='G-001'")
        self.env.seed_agent("cursor-director", "director")
        self.env.sql("UPDATE agents SET org='cursor' WHERE name='cursor-director'")
        self.env.sql("UPDATE agents SET org='claude' WHERE name=?", (LEAD,))
        self.assertEqual(self.tick({pr_url(1): (SHA, RED, "DIRTY")}), [])
        self.assertEqual(len(self.mails("coder not resumed, PR conflicted%", to="cursor-director")), 1)
        self.assertEqual(self.mails("coder not resumed, PR conflicted%", to=LEAD), [])

    def test_no_checks_for_over_60_min_mails_the_lead_and_starts_nothing(self):
        self.task()
        self.assertEqual(self.tick({pr_url(1): (SHA, [])}, now=NOW), [])
        self.assertEqual(self.tick({pr_url(1): (SHA, [])}, now=NOW + 3000), [])
        self.assertEqual(self.mails("no CI checks%"), [])
        self.assertEqual(self.tick({pr_url(1): (SHA, [])}, now=NOW + 3700), [])
        self.assertEqual(self.tick({pr_url(1): (SHA, [])}, now=NOW + 3800), [])
        self.assertEqual(len(self.mails("no CI checks%", to=LEAD)), 1)


class FollowUpTests(Base):
    def test_non_coder_in_review_task_with_a_red_pr_starts_and_mails_nothing(self):
        for role in ("qa", "pm"):
            self.env.sql("DELETE FROM tasks")
            self.env.sql("DELETE FROM agents WHERE role='coder'")
            self.task(coder=None)
            self.env.sql("UPDATE tasks SET role=? WHERE id='T-0001'", (role,))
            self.assertEqual(self.tick({pr_url(1): (SHA, RED)}), [], role)
            self.assertEqual(self.mails("coder restarted%"), [])
            self.assertEqual(self.env.sql("SELECT 1 FROM events WHERE action='coder-ci-resume'"), [])

    def test_resume_dropped_after_a_runtime_change_reports_fresh(self):
        self.task()
        self.env.sql("UPDATE agents SET spawn_args=? WHERE name='coder-t-0001'", (json.dumps({"runtime": "cursor"}),))
        calls = self.tick({pr_url(1): (SHA, RED)})
        self.assertEqual(len(calls), 1)
        self.assertIn("fresh session", calls[0]["prompt"])
        ev = self.env.sql("SELECT detail FROM events WHERE action='coder-ci-resume' AND entity_id='T-0001'")
        self.assertEqual(json.loads(ev[0][0])["mode"], "fresh")
        self.assertEqual(len(self.mails("coder restarted on red CI%", to=LEAD)), 1)
        self.assertIn("(fresh)", self.mails("coder restarted on red CI%", to=LEAD)[0]["body"])
        self.assertEqual(self.mails("CI red:%", to="coder-t-0001"), [])  # spawn sends the fresh kickoff itself

    def test_unreadable_closed_dirty_and_no_checks_prs_cost_one_probe_per_window(self):
        cases = {"dirty": (SHA, RED, "DIRTY"), "nochecks": (SHA, [])}
        for label, pr in cases.items():
            with self.subTest(label):
                self.env.sql("DELETE FROM tasks")
                self.env.sql("DELETE FROM agents WHERE role='coder'")
                self.env.sql("DELETE FROM throttle")
                self.probed = []
                self.task()
                self.tick({pr_url(1): pr}, now=NOW)
                self.tick({pr_url(1): pr}, now=NOW + 30)
                self.assertEqual(len(self.probed), 1)
                self.tick({pr_url(1): pr}, now=NOW + 300)  # window over: probed again
                self.assertEqual(len(self.probed), 2)
        for label, state in (("closed", "CLOSED"), ("error", None)):
            with self.subTest(label):
                self.env.sql("DELETE FROM tasks")
                self.env.sql("DELETE FROM agents WHERE role='coder'")
                self.env.sql("DELETE FROM throttle")
                self.probed = []
                self.task()
                self.tick_probe(state, NOW)
                self.tick_probe(state, NOW + 30)
                self.assertEqual(len(self.probed), 1)

    def tick_probe(self, state, now):
        with self.env.load_factory() as f:
            con = f.db()
            def probe(url, timeout):
                self.probed.append(url)
                return OSError("gh down") if state is None else {"number": 1, "state": state, "headRefOid": SHA, "statusCheckRollup": RED}
            f._ci_wake_probe = probe
            f._ci_remote_heads = lambda: {"factory/t-0001": SHA}
            f.digest_clock = lambda: f.dt.datetime.fromtimestamp(now, f.dt.timezone.utc)
            f.coder_ci_resume_tick(con)
            con.commit()

    def test_new_head_on_a_throttled_pr_is_probed_on_the_next_tick(self):
        self.task()
        self.probed = []
        self.tick({pr_url(1): (SHA, [])}, now=NOW)
        self.tick({pr_url(1): (SHA, [])}, now=NOW + 30)
        self.assertEqual(len(self.probed), 1)
        self.tick({pr_url(1): (SHA2, RED)}, now=NOW + 60)  # new head, still inside the window
        self.assertEqual(len(self.probed), 2)
        self.assertEqual(len(self.starts_from_events()), 1)

    def starts_from_events(self):
        return self.env.sql("SELECT 1 FROM events WHERE action='coder-ci-resume' AND entity_id='T-0001'")

    def test_cursor_runtime_with_session_and_worktree_spawns_fresh_without_resume(self):
        self.env.sql("UPDATE goals SET org='cursor' WHERE id='G-001'")
        self.task()
        self.env.sql("UPDATE agents SET spawn_args=? WHERE name='coder-t-0001'", (json.dumps({"runtime": "cursor"}),))
        calls = self.tick({pr_url(1): (SHA, RED)})
        self.assertEqual(len(calls), 1)
        self.assertIsNone(calls[0]["resume"])
        self.assertIn("fresh session", calls[0]["prompt"])


class WorktreeTests(Base):
    def test_handed_off_coders_worktree_survives_the_tick_step(self):
        self.task()
        (self.wt / "keep.txt").write_text("x\n")
        self.tick({pr_url(1): (SHA, GREEN)})
        self.tick({pr_url(1): (SHA, RED)})
        self.assertTrue((self.wt / "keep.txt").exists())
        self.assertEqual(self.env.sql("SELECT worktree FROM tasks WHERE id='T-0001'")[0][0], str(self.wt))


class RealTickTests(Base):
    def test_real_tick_restarts_a_done_coder_on_a_red_pr_and_keeps_its_worktree(self):
        self.task()
        (self.wt / "keep.txt").write_text("x\n")
        self.env.set_gh(prs={pr_url(1): {"number": 1, "state": "OPEN", "headRefOid": SHA, "mergeStateStatus": "CLEAN", "statusCheckRollup": RED}})
        p = self.env.run("tick", inproc=False)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(len(self.env.sql("SELECT 1 FROM events WHERE action='coder-ci-resume' AND entity_id='T-0001'")), 1, p.stdout + p.stderr)
        argv = self.starts()
        self.assertEqual(len(argv), 1, self.env.calls("herdr"))
        self.assertEqual(argv[0][argv[0].index("--resume") + 1], "sess-1")
        self.assertTrue((self.wt / "keep.txt").exists())
        self.assertEqual(self.env.sql("SELECT worktree FROM tasks WHERE id='T-0001'")[0][0], str(self.wt))


if __name__ == "__main__":
    unittest.main()
