"""T-0112: `factory skilleval queue|cancel`, and the tick scheduler skilleval_tick (scout/skilleval.py).

The scheduler tests call scout.skilleval.tick in-process with a fake api: $FACTORY_NOW-style injected clock, a launch
that only writes rows and dirs (the real launch needs user namespaces), nsbox.run_contained mocked, no real claude.
The CLI tests (queue, cancel, config, the tick entry point) run bin/factory as a subprocess against a temp FACTORY_HOME.
"""
import datetime as dt
import json
import sqlite3
import sys
import threading
import types
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
from harness import FactoryTestCase, FACTORY, FACTORY_SRC, REPO  # noqa: E402
from test_skilleval_collect import CollectBase, FakeApi, PASS_OUT, SHA  # noqa: E402

sys.path.insert(0, str(REPO))
from scout import nsbox, sandbox  # noqa: E402
from scout import skilleval as se  # noqa: E402

DAY = "2026-10-25"  # the day of the make_trial fixtures (started 10:00)
NEXT_DAY = "2026-10-26"
KEYS = {"skilleval_max_trials_per_day": "6", "skilleval_max_concurrent": "1", "skilleval_trial_minutes": "15",
        "skilleval_trial_max_tokens": "300000", "skilleval_keep_failed_h": "48"}


def at(hhmm, day=DAY):
    return dt.datetime.fromisoformat(f"{day}T{hhmm}:00+00:00").astimezone()


class TickApi(FakeApi):
    """The api object bin/factory hands to scout.skilleval.tick, with a fake launch and an injectable clock."""

    def __init__(self, con, when, gate="live"):
        super().__init__()
        self.con, self.when, self.gate = con, when, gate
        self.launched, self.events = [], []
        self.home, self.unshare, self.home_dir = "/nonexistent-home", "unshare", "/nonexistent-home"

    def clock(self):
        return self.when

    def now(self):
        return self.when.isoformat(timespec="seconds")

    def cfg(self, con, key):
        return con.execute("SELECT value FROM config WHERE key=?", (key,)).fetchone()[0]

    def event(self, con, entity, entity_id, action, detail=None):
        self.events.append((action, detail))

    def launch(self, con, row, root, home, unshare):
        self.launched.append(row["id"])
        name = sandbox.trial_name(row["id"])
        d = Path(root) / "trials" / name
        for sub in ("fh", "work", "home", "cfg/projects/p"):
            (d / sub).mkdir(parents=True, exist_ok=True)
        agent = f"eval-{name}"
        con.execute("INSERT INTO agents(name,role,status,started) VALUES(?,?,?,?)",
                    (agent, "evaluator", self.gate, self.now()))
        con.execute("UPDATE skill_trials SET status='running', agent=?, sandbox=?, started=? WHERE id=? AND status='starting'",
                    (agent, str(d), self.now(), row["id"]))


class TickBase(CollectBase):
    def setUp(self):
        super().setUp()
        for p in (mock.patch.object(sandbox, "check_token"), mock.patch.object(sandbox, "check_userns"),
                  mock.patch.object(nsbox, "userns_available", return_value=(True, "")),
                  mock.patch.object(nsbox, "run_contained", return_value=nsbox.Contained(0, PASS_OUT, False))):
            p.start()
            self.addCleanup(p.stop)
        self.con.execute("INSERT INTO config(key,value) VALUES('skilleval_root',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
                         (str(self.root),))
        self.con.commit()

    def tick(self, when, gate="live"):
        api = TickApi(self.con, when, gate)
        se.tick(self.con, api)
        self.con.commit()
        return api

    def queue(self, tasks=("fix-bug", "add-feature", "review-diff"), cid="C-0001"):
        new, _ = se.queue_trials(self.con, cid, SHA, list(tasks), "zz", "t")
        self.con.commit()
        return new

    def status(self):
        return [r[0] for r in self.con.execute("SELECT status FROM skill_trials ORDER BY id")]

    def cand_status(self, cid="C-0001"):
        return self.con.execute("SELECT status FROM scout_candidates WHERE id=?", (cid,)).fetchone()[0]

    def big_log(self, d, input_=0, output=0, cache_write=0, cache_read=0):
        line = {"type": "assistant", "timestamp": "2026-10-26T10:00:00Z", "requestId": "rb", "uuid": "rb",
                "message": {"id": "mb", "model": "claude-sonnet-5-5", "usage": {
                    "input_tokens": input_, "output_tokens": output, "cache_creation_input_tokens": cache_write,
                    "cache_read_input_tokens": cache_read}}}
        (d / "cfg" / "projects" / "p" / "big.jsonl").write_text(json.dumps(line) + "\n")


class QueueModelTests(TickBase):
    def test_trials_of_a_task_are_back_to_back_with_then_without(self):
        self.assertEqual(len(self.queue()), 6)
        rows = self.con.execute("SELECT task, condition FROM skill_trials ORDER BY id").fetchall()
        self.assertEqual([(r[0], r[1]) for r in rows],
                         [(t, c) for t in ("fix-bug", "add-feature", "review-diff") for c in ("with", "without")])
        self.assertEqual(self.cand_status(), "queued-eval")


class SchedulerTests(TickBase):
    def test_cap_is_enforced_across_ticks_and_resets_next_day(self):
        self.assertEqual(len(self.queue(tasks=("t1", "t2", "t3", "t4"))), 8)
        started = []
        for minute in range(30):  # many ticks on day D; each started trial finishes, so only the daily cap holds back
            api = self.tick(at(f"10:{minute:02d}"))
            started += api.launched
            self.con.execute("UPDATE skill_trials SET status='passed', ended=? WHERE status='running'", (api.now(),))
        self.assertEqual(len(started), 6, started)
        self.assertEqual(self.status().count("pending"), 2)
        started_next = []
        for minute in range(5):
            api = self.tick(at(f"09:{minute:02d}", NEXT_DAY))
            started_next += api.launched
            self.con.execute("UPDATE skill_trials SET status='passed', ended=? WHERE status='running'", (api.now(),))
        self.assertEqual(len(started_next), 2)
        self.assertEqual(self.status().count("pending"), 0)

    def test_second_trial_waits_while_one_runs(self):
        self.queue(tasks=("t1",))
        self.assertEqual(len(self.tick(at("10:00")).launched), 1)
        self.assertEqual(len(self.tick(at("10:01")).launched), 0)
        self.assertEqual(self.status(), ["running", "pending"])

    def test_max_concurrent_two_starts_one_per_tick(self):
        self.con.execute("UPDATE config SET value='2' WHERE key='skilleval_max_concurrent'")
        self.queue(tasks=("t1", "t2"))
        counts = [len(self.tick(at(f"10:0{i}")).launched) for i in range(3)]
        self.assertEqual(counts, [1, 1, 0])  # never two in one tick, never above the limit

    def test_gate_closed_queued_agent_counts_as_running(self):
        self.queue(tasks=("t1",))
        self.assertEqual(len(self.tick(at("10:00"), gate="queued").launched), 1)
        tid = self.con.execute("SELECT id FROM skill_trials WHERE status='running'").fetchone()[0]
        for minute in range(1, 40):  # a queued agent has no wall clock and no tokens: it is neither started again nor collected
            api = self.tick(at(f"10:{minute:02d}"), gate="queued")
            self.assertEqual(api.launched, [])
            self.assertEqual(api.stopped, [])
        self.assertEqual(self.trial(tid)["status"], "running")

    def test_done_in_temp_db_is_collected_with_the_checker_result(self):
        tid, d, agent = self.make_trial(done=True)
        api = self.tick(at("10:10"))
        row = self.trial(tid)
        self.assertEqual((row["status"], row["passed"]), ("passed", 1))
        self.assertIn(("collect", f"trial {tid} passed"), [(a, x) for a, x in api.events])  # collect_trial's own event
        self.assertFalse(d.exists())

    def test_failed_checker_is_recorded_failed(self):
        tid, _, _ = self.make_trial(done=True)
        nsbox.run_contained.return_value = nsbox.Contained(1, "FAIL: 1 test\n", False)
        self.tick(at("10:10"))
        self.assertEqual((self.trial(tid)["status"], self.trial(tid)["passed"]), ("failed", 0))

    def test_lost_live_row_is_an_error(self):
        tid, _, agent = self.make_trial(done=False, live="lost")
        self.tick(at("10:10"))
        row = self.trial(tid)
        self.assertEqual(row["status"], "error")
        self.assertIn("exited without finishing", row["check_output"])

    def test_missing_agent_row_is_an_error(self):
        tid, _, _ = self.make_trial(done=False, live=None)
        self.tick(at("10:10"))
        self.assertEqual(self.trial(tid)["status"], "error")

    def test_wall_timeout_force_stops_and_records_timeout(self):
        tid, d, agent = self.make_trial(done=False)  # started 10:00, live, not done
        api = self.tick(at("10:14"))
        self.assertEqual(self.trial(tid)["status"], "running", "14 min is inside the 15 min budget")
        self.assertEqual(api.stopped, [])
        api = self.tick(at("10:16"))
        row = self.trial(tid)
        self.assertEqual((row["status"], row["passed"]), ("timeout", 0))
        self.assertEqual([s[0] for s in api.stopped], [agent])
        self.assertEqual(self.agent(agent)["status"], "stopped")
        self.assertTrue(row["check_output"].startswith("[timeout]"))
        self.assertTrue(d.exists(), "a timeout sandbox is kept for the keep_failed_h window")

    def test_token_overrun_force_stops_and_records_over_budget(self):
        tid, d, agent = self.make_trial(done=False, transcript=None)
        self.big_log(d, input_=200000, output=100000, cache_write=1)  # 300001 > 300000
        api = self.tick(at("10:05"))
        row = self.trial(tid)
        self.assertEqual((row["status"], row["passed"]), ("over-budget", 0))
        self.assertEqual([s[0] for s in api.stopped], [agent])

    def test_exactly_at_the_cap_and_cache_reads_do_not_count(self):
        tid, d, _ = self.make_trial(done=False, transcript=None)
        self.big_log(d, input_=200000, output=100000, cache_read=5000000)  # 300000 counted, reads ignored
        api = self.tick(at("10:05"))
        self.assertEqual(self.trial(tid)["status"], "running")
        self.assertEqual(api.stopped, [])

    def test_failed_stop_leaves_the_trial_running_for_the_next_tick(self):
        tid, _, agent = self.make_trial(done=False)
        with mock.patch.object(FakeApi, "force_stop", lambda s, con, n, h, attempt_seen=None: ("failed", "pane open")):
            api = self.tick(at("10:20"))
        self.assertEqual(self.trial(tid)["status"], "running")
        self.assertTrue([e for e in api.events if e[0] == "collect-retry"])
        self.tick(at("10:21"))  # the stop works now
        self.assertEqual(self.trial(tid)["status"], "timeout")

    def test_blocked_start_leaves_the_queue_pending_and_says_so_once(self):
        self.queue(tasks=("t1",))
        with mock.patch.object(sandbox, "check_token", side_effect=sandbox.SandboxRefused("owner decision D3 pending")):
            a1 = self.tick(at("10:00"))
            self.tick(at("10:01"))
        self.assertEqual(self.status(), ["pending", "pending"])
        self.assertEqual(a1.launched, [])
        self.assertEqual([e[0] for e in a1.events], ["skilleval-blocked"])

    def test_refused_start_marks_that_trial_error_and_the_next_tick_goes_on(self):
        self.queue(tasks=("t1",))
        calls = []

        class Refusing(TickApi):
            def launch(s, con, row, root, home, unshare):
                calls.append(row["id"])
                if len(calls) == 1:
                    raise sandbox.SandboxRefused("preflight failed")
                super().launch(con, row, root, home, unshare)
        for minute in ("10:00", "10:01"):
            api = Refusing(self.con, at(minute))
            se.tick(self.con, api)
        self.assertEqual(self.status(), ["error", "running"])


class ClaimRaceTests(TickBase):
    def other(self):
        con = self.env.db()
        self.addCleanup(con.close)
        return con

    def test_gate_wait_does_not_count_toward_the_trial_wall(self):
        self.queue(tasks=("t1",))
        self.tick(at("10:00"), gate="queued")
        for minute in range(1, 21):  # queued at a closed gate for 20 min, over the 15 min wall
            self.assertEqual(self.tick(at(f"10:{minute:02d}"), gate="queued").stopped, [])
        self.con.execute("UPDATE agents SET status='live', started=? WHERE role='evaluator'", (at("10:21").isoformat(),))
        self.con.commit()
        api = self.tick(at("10:22"))  # one live minute: not a timeout
        self.assertEqual(api.stopped, [])
        self.assertEqual(self.status()[0], "running")
        api = self.tick(at("10:37"))  # 16 live minutes: now it is
        self.assertEqual(len(api.stopped), 1)
        self.assertEqual(self.status()[0], "timeout")

    def test_two_connections_racing_to_claim_one_pending_trial_one_wins(self):
        self.queue(tasks=("t1",))
        tid = self.con.execute("SELECT min(id) FROM skill_trials").fetchone()[0]
        cons, won, go = [], [], threading.Barrier(2)
        for _ in range(2):  # the factory DB is autocommit (isolation_level None); one connection per thread
            c = sqlite3.connect(self.env.db_path, timeout=30, isolation_level=None, check_same_thread=False)
            c.row_factory = sqlite3.Row
            self.addCleanup(c.close)
            cons.append(c)

        def worker(c):
            go.wait(timeout=20)
            won.append(se.claim(c, tid, "2026-10-25T10:00:00+00:00"))
        ts = [threading.Thread(target=worker, args=(c,)) for c in cons]
        for t in ts:
            t.start()
        for t in ts:
            t.join(60)
        self.assertEqual(sorted(won), [False, True])
        self.assertEqual(self.trial(tid)["status"], "starting")

    def test_cancel_after_a_claim_does_not_cancel_and_a_claim_after_a_cancel_loses(self):
        self.queue(tasks=("t1",))
        a, b = [r[0] for r in self.con.execute("SELECT id FROM skill_trials ORDER BY id")]
        self.assertTrue(se.claim(self.other(), a, "2026-10-25T10:00:00+00:00"))
        lines, errors = se.cancel(self.con, str(a), self.root, TickApi(self.con, at("10:00")))
        self.assertEqual(lines, [])
        self.assertIn("starting", errors[0])
        self.assertEqual(self.trial(a)["status"], "starting")
        se.cancel(self.con, str(b), self.root, TickApi(self.con, at("10:00")))
        self.assertFalse(se.claim(self.other(), b, "2026-10-25T10:00:00+00:00"))
        self.assertEqual(self.trial(b)["status"], "cancelled")

    def test_a_trial_cancelled_between_pick_and_claim_is_not_started(self):
        self.queue(tasks=("t1",))
        first = self.con.execute("SELECT min(id) FROM skill_trials").fetchone()[0]
        real, other = se.claim, self.other()

        def cancel_then_claim(con, tid, stamp):
            if tid == first:
                se.cancel(other, str(tid), self.root, TickApi(other, at("10:00")))
            return real(con, tid, stamp)
        with mock.patch.object(se, "claim", cancel_then_claim):
            api = self.tick(at("10:00"))
        self.assertEqual(api.launched, [])
        self.assertEqual(self.status()[0], "cancelled")

    def test_a_starting_trial_counts_toward_max_concurrent(self):
        self.queue(tasks=("t1",))
        first = self.con.execute("SELECT min(id) FROM skill_trials").fetchone()[0]
        self.assertTrue(se.claim(self.con, first, "2026-10-25T10:00:00+00:00"))
        self.assertEqual(self.tick(at("10:01")).launched, [])

    def test_an_unexpected_start_error_marks_the_trial_error_and_the_queue_moves_on(self):
        self.queue(tasks=("t1",))
        calls = []

        class Boom(TickApi):
            def launch(s, con, row, root, home, unshare):
                calls.append(row["id"])
                if len(calls) == 1:
                    raise OSError("disk full")
                super().launch(con, row, root, home, unshare)
        for minute in ("10:00", "10:01"):
            se.tick(self.con, Boom(self.con, at(minute)))
        self.assertEqual(self.status(), ["error", "running"])
        self.assertIn("OSError", self.con.execute("SELECT check_output FROM skill_trials ORDER BY id").fetchone()[0])

    def test_a_trial_stuck_in_starting_is_errored_after_15_minutes(self):
        self.queue(tasks=("t1",))
        first = self.con.execute("SELECT min(id) FROM skill_trials").fetchone()[0]
        se.claim(self.con, first, at("10:00").isoformat(timespec="seconds"))
        self.tick(at("10:10"))
        self.assertEqual(self.trial(first)["status"], "starting")
        self.tick(at("10:16"))
        self.assertEqual(self.trial(first)["status"], "error")

    def test_a_blocker_that_returns_after_a_resolved_period_is_reported_again(self):
        self.queue(tasks=("t1",))
        refuse = mock.patch.object(sandbox, "check_token", side_effect=sandbox.SandboxRefused("no eval token"))
        with refuse:
            a1 = self.tick(at("10:00"))
        self.assertEqual([e[0] for e in a1.events], ["skilleval-blocked"])
        self.con.execute("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES('t','x','skilleval','-','try','ok')")
        self.con.commit()  # a launch went through in between: the blocker was resolved
        with refuse:
            a2 = self.tick(at("10:05"))
        self.assertEqual([e[0] for e in a2.events], ["skilleval-blocked"])


class CleanupTests(TickBase):
    def trial_with_dir(self, status, ended, name_id=None):
        tid = self.con.execute("INSERT INTO skill_trials(candidate_id,vendor_sha,task,condition,status,queued,ended) "
                               "VALUES('C-0001',?,?,?,?,'t',?)", (SHA, f"t{name_id}", "with", status, ended)).lastrowid
        d = self.root / "trials" / sandbox.trial_name(tid)
        (d / "work").mkdir(parents=True)
        self.con.commit()
        return tid, d

    def test_old_failed_sandboxes_and_orphans_go_and_fresh_ones_stay(self):
        _, old_timeout = self.trial_with_dir("timeout", "2026-10-24T09:00:00+00:00", 1)    # 49 h before the tick
        _, old_error = self.trial_with_dir("error", "2026-10-24T09:00:00+00:00", 2)
        _, old_over = self.trial_with_dir("over-budget", "2026-10-24T09:00:00+00:00", 3)
        _, fresh = self.trial_with_dir("timeout", "2026-10-26T09:00:00+00:00", 4)          # 1 h
        _, running = self.trial_with_dir("running", None, 5)
        orphan = self.root / "trials" / "tr9999"
        (orphan / "work").mkdir(parents=True)
        other = self.root / "trials" / "notes"
        other.mkdir()
        (self.root / "locks").mkdir(exist_ok=True)
        self.tick(at("10:00", NEXT_DAY))
        self.assertFalse(old_timeout.exists())
        self.assertFalse(old_error.exists())
        self.assertFalse(old_over.exists())
        self.assertFalse(orphan.exists())
        self.assertTrue(fresh.exists())
        self.assertTrue(other.exists(), "only trNNNN dirs are ours to delete")
        self.assertTrue((self.root / "locks").exists())
        self.assertEqual(self.con.execute("SELECT status FROM skill_trials WHERE id=1").fetchone()[0], "timeout")


class CandidateStatusTests(TickBase):
    def test_candidate_becomes_evaluated_after_its_last_trial_is_terminal(self):
        self.queue(tasks=("t1",))
        self.con.execute("UPDATE skill_trials SET status='passed' WHERE id=(SELECT min(id) FROM skill_trials)")
        self.con.execute("UPDATE config SET value='0' WHERE key='skilleval_max_trials_per_day'")  # nothing new starts
        self.tick(at("10:00"))
        self.assertEqual(self.status(), ["passed", "pending"])
        self.assertEqual(self.cand_status(), "queued-eval", "one trial is still pending")
        self.con.execute("UPDATE skill_trials SET status='failed' WHERE status='pending'")
        self.tick(at("10:01"))
        self.assertEqual(self.cand_status(), "evaluated")

    def test_a_candidate_held_back_is_not_flipped(self):
        self.queue(tasks=("t1",))
        self.con.execute("UPDATE skill_trials SET status='passed'")
        self.con.execute("UPDATE scout_candidates SET status='needs-owner' WHERE id='C-0001'")
        self.tick(at("10:00"))
        self.assertEqual(self.cand_status(), "needs-owner")


class CancelTests(TickBase):
    def test_cancel_candidate_stops_the_running_trial_and_cancels_pending_ones(self):
        tid, d, agent = self.make_trial(done=False)
        self.queue(tasks=("t1",))
        api = TickApi(self.con, at("10:05"))
        lines, errors = se.cancel(self.con, "C-0001", self.root, api)
        self.con.commit()
        self.assertEqual(errors, [])
        self.assertEqual(len(lines), 3)
        self.assertEqual(self.trial(tid)["status"], "cancelled")
        self.assertEqual([s[0] for s in api.stopped], [agent])
        self.assertEqual(set(self.status()), {"cancelled"})
        self.assertFalse(d.exists())
        nsbox.run_contained.assert_not_called()  # a cancelled trial is not checked

    def test_cancel_by_trial_id_touches_only_that_trial(self):
        self.queue(tasks=("t1",))
        first = self.con.execute("SELECT min(id) FROM skill_trials").fetchone()[0]
        se.cancel(self.con, str(first), self.root, TickApi(self.con, at("10:00")))
        self.assertEqual(self.status(), ["cancelled", "pending"])

    def test_cancel_unknown_target_is_refused(self):
        for target in ("tr9999", "C-9999", "nonsense"):
            with self.subTest(target), self.assertRaises(se.CollectError):
                se.cancel(self.con, target, self.root, TickApi(self.con, at("10:00")))

    def test_failed_stop_is_reported_and_the_trial_stays_running(self):
        tid, _, _ = self.make_trial(done=False)
        with mock.patch.object(FakeApi, "force_stop", lambda s, con, n, h, attempt_seen=None: ("failed", "pane open")):
            lines, errors = se.cancel(self.con, str(tid), self.root, TickApi(self.con, at("10:00")))
        self.assertEqual(lines, [])
        self.assertEqual(len(errors), 1)
        self.assertEqual(self.trial(tid)["status"], "running")


class SummaryTests(TickBase):
    def test_results_and_list_show_pending_and_the_candidate_summary(self):
        self.queue(tasks=("t1",))
        self.con.execute("UPDATE skill_trials SET status='passed', tokens_in=100, tokens_out=50, wall_s=60 "
                         "WHERE condition='with'")
        for fn in (se.result_lines, se.list_lines):
            lines = fn(self.con)
            self.assertEqual(len(lines), 3)
            self.assertIn("status=pending", lines[1])
            self.assertEqual(lines[2], "C-0001 summary: with: passed 1/1 tasks, median tokens 150, median wall_s 60; "
                                       "without: passed 0/0 tasks, median tokens -, median wall_s -; pending 1")


class CliBase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        self.root = e.home / "evalroot"
        self.ok("config", "skilleval_root", str(self.root), agent="owner")
        e.sql("INSERT INTO scout_candidates(id,key,repo,head_sha,install_risk,status,sources,first_seen,last_seen) "
              "VALUES('C-0001','gh:o/r','o/r',?,'prompt-only','triaged','[]','t','t')", (SHA,))
        plugin = self.root / "vendor" / f"C-0001@{SHA}"
        (plugin / "plugin" / "skills" / "s").mkdir(parents=True)
        (plugin / "plugin" / "skills" / "s" / "SKILL.md").write_text("# s\nJust prose.\n")
        (plugin / "VENDOR.json").write_text(json.dumps(
            {"candidate": "C-0001", "sha": SHA, "plugin": "plugin", "copied": ["skills/s/SKILL.md"], "skipped": [],
             "prescreen_hits": []}))

    def trials(self):
        return [dict(r) for r in self.env.sql("SELECT * FROM skill_trials ORDER BY id")]

    def cand(self):
        return self.env.sql("SELECT status FROM scout_candidates WHERE id='C-0001'")[0]["status"]


class QueueCliTests(CliBase):
    def test_queue_creates_six_pending_trials_for_three_tasks_and_a_requeue_is_a_noop(self):
        out = self.ok("skilleval", "queue", "C-0001", agent="owner").stdout
        rows = self.trials()
        self.assertEqual(len(rows), 6)
        self.assertEqual({r["status"] for r in rows}, {"pending"})
        self.assertEqual({r["task"] for r in rows}, {"fix-bug", "add-feature", "review-diff"})
        self.assertEqual({r["vendor_sha"] for r in rows}, {SHA})
        self.assertEqual(self.cand(), "queued-eval")
        self.assertIn("queued 6 trial(s)", out)
        again = self.ok("skilleval", "queue", "C-0001", agent="owner").stdout
        self.assertIn("already queued", again)
        self.assertEqual(len(self.trials()), 6)
        self.assertEqual(len([l for l in again.splitlines() if "status=pending" in l]), 6, "prints the existing trials")

    def test_tasks_option_limits_the_queue_and_rejects_unknown_names(self):
        self.ok("skilleval", "queue", "C-0001", "--tasks", "fix-bug", agent="owner")
        self.assertEqual({r["task"] for r in self.trials()}, {"fix-bug"})
        p = self.refused(2, "skilleval", "queue", "C-0001", "--tasks", "no-such-task", agent="owner")
        self.assertIn("unknown eval task", p.stderr)
        self.assertEqual(len(self.trials()), 2)

    def test_non_prompt_only_and_held_back_candidates_are_refused(self):
        for risk, status in (("scripts", "triaged"), ("prompt-only,hooks", "triaged"), ("unknown", "new"),
                             ("prompt-only", "needs-owner"), ("prompt-only", "dismissed")):
            with self.subTest(risk=risk, status=status):
                self.env.sql("UPDATE scout_candidates SET install_risk=?, status=? WHERE id='C-0001'", (risk, status))
                p = self.refused(2, "skilleval", "queue", "C-0001", agent="owner")
                self.assertIn("not eligible", p.stderr)
                self.assertEqual(self.trials(), [])
                self.assertEqual(self.cand(), status)

    def test_vendor_refusal_propagates_and_queues_nothing(self):
        (self.root / "vendor" / f"C-0001@{SHA}" / "VENDOR.json").unlink()  # not vendored: T6 must fetch, and cannot here
        p = self.env.run("skilleval", "queue", "C-0001", agent="owner")
        self.assertNotEqual(p.returncode, 0, p.stdout)
        print("VENDOR:", p.returncode, p.stderr.strip()[:200])
        self.assertEqual(self.trials(), [])

    def test_unknown_candidate_and_bad_id(self):
        self.refused(2, "skilleval", "queue", "C-9999", agent="owner")
        self.refused(2, "skilleval", "queue", "nonsense", agent="owner")

    def test_director_lead_boss_owner_allowed(self):
        for r in ("director", "lead", "boss"):
            self.env.seed_agent(f"zz-{r}", r)
        for who in ("zz-director", "zz-lead", "zz-boss", "owner"):
            self.ok("skilleval", "queue", "C-0001", "--tasks", "fix-bug", agent=who)
        self.assertEqual(len(self.trials()), 2)

    def test_workers_including_scout_and_evaluator_are_refused(self):
        roles = ("coder", "qa", "reviewer", "pm", "judge", "cleaner", "scout", "evaluator")
        for r in roles:
            self.env.seed_agent(f"zz-{r}", r)
        for r in roles:
            for action in (("queue", "C-0001"), ("cancel", "C-0001")):
                with self.subTest(role=r, action=action[0]):
                    self.refused(3, "skilleval", *action, agent=f"zz-{r}")
        self.assertEqual(self.trials(), [])


class CancelCliTests(CliBase):
    def test_cancel_candidate_cancels_pending_trials(self):
        self.ok("skilleval", "queue", "C-0001", "--tasks", "fix-bug", agent="owner")
        out = self.ok("skilleval", "cancel", "C-0001", agent="owner").stdout
        self.assertEqual({r["status"] for r in self.trials()}, {"cancelled"})
        self.assertEqual(len([l for l in out.splitlines() if "status=cancelled" in l]), 2)

    def test_cancel_one_trial(self):
        self.ok("skilleval", "queue", "C-0001", "--tasks", "fix-bug", agent="owner")
        first = self.trials()[0]["id"]
        self.ok("skilleval", "cancel", str(first), agent="owner")
        self.assertEqual([r["status"] for r in self.trials()], ["cancelled", "pending"])

    def test_cancel_unknown_is_refused(self):
        self.refused(2, "skilleval", "cancel", "C-9999", agent="owner")
        self.refused(2, "skilleval", "cancel", "tr0099", agent="owner")


class VoidCliTests(CliBase):
    def add(self, status, cond="without", task="fix-bug"):
        self.env.sql("INSERT INTO skill_trials(candidate_id,vendor_sha,task,condition,status,queued,passed,wall_s,tokens_in,"
                     "tokens_out,agent) VALUES('C-0001',?,?,?,?,'t',0,900,120,30,'eval-tr0001')", (SHA, task, cond, status))
        return self.trials()[-1]["id"]

    def events(self):
        return self.env.sql("SELECT * FROM events WHERE action='void'")

    def test_void_a_timeout_trial_keeps_the_row_and_records_reason_and_why(self):
        tid = self.add("timeout")
        before = self.trials()[0]
        out = self.ok("skilleval", "void", str(tid), "--reason", "infra", "--why", "T-0718 hook bug", agent="owner").stdout
        self.assertIn("status=voided", out)
        after = self.trials()[0]
        self.assertEqual(after["status"], "voided")
        self.assertEqual((after["void_reason"], after["void_why"]), ("infra", "T-0718 hook bug"))
        for k in ("passed", "wall_s", "tokens_in", "tokens_out", "agent", "task", "condition"):
            self.assertEqual(after[k], before[k], k)
        (ev,) = self.events()
        self.assertIn("was timeout", ev["detail"])
        self.assertIn("T-0718 hook bug", ev["detail"])
        for verb in ("list", "results"):
            shown = self.ok("skilleval", verb, agent="owner").stdout
            self.assertIn("status=voided", shown)
            self.assertIn("void_reason=infra", shown)
            self.assertIn("T-0718 hook bug", shown)

    def test_void_accepts_the_trNNNN_form_and_every_finished_status(self):
        self.env.seed_agent("lead-zz", "lead")
        for st in ("passed", "failed", "timeout", "over-budget", "error"):
            tid = self.add(st, task=f"t-{st}")
            self.ok("skilleval", "void", f"tr{tid:04d}", "--reason", "infra", "--why", "x", agent="lead-zz")
        self.assertEqual({r["status"] for r in self.trials()}, {"voided"})

    def test_void_of_an_unfinished_trial_is_refused_and_leaves_the_row(self):
        for st in ("pending", "running", "starting", "cancelled", "voided"):
            tid = self.add(st, task=f"t-{st}")
            before = self.trials()[-1]
            p = self.refused(2, "skilleval", "void", str(tid), "--reason", "infra", "--why", "x", agent="owner")
            self.assertIn(f"is {st}", p.stderr)
            self.assertEqual(self.trials()[-1], before)
        self.assertEqual(self.events(), [])

    def test_void_needs_reason_infra_and_a_why(self):
        tid = self.add("timeout")
        before = self.trials()
        self.refused(2, "skilleval", "void", str(tid), "--why", "x", agent="owner")
        self.refused(2, "skilleval", "void", str(tid), "--reason", "timeout", "--why", "x", agent="owner")
        self.refused(2, "skilleval", "void", str(tid), "--reason", "infra", agent="owner")
        self.refused(2, "skilleval", "void", str(tid), "--reason", "infra", "--why", "  ", agent="owner")
        self.refused(2, "skilleval", "void", "tr0099", "--reason", "infra", "--why", "x", agent="owner")
        self.assertEqual(self.trials(), before)

    def test_collect_still_refuses_reason_infra(self):
        tid = self.add("timeout")
        self.refused(2, "skilleval", "collect", str(tid), "--reason", "infra", agent="owner")
        self.assertEqual(self.trials()[0]["status"], "timeout")

    def test_workers_and_the_boss_cannot_void(self):
        tid = self.add("timeout")
        for r in ("coder", "qa", "reviewer", "pm"):
            self.env.seed_agent(f"zz-{r}", r)
            self.refused(3, "skilleval", "void", str(tid), "--reason", "infra", "--why", "x", agent=f"zz-{r}")
        self.refused(3, "skilleval", "void", str(tid), "--reason", "infra", "--why", "x", agent="boss")
        self.assertEqual(self.trials()[0]["status"], "timeout")
        self.assertEqual(self.events(), [])

    def test_summaries_exclude_a_voided_trial(self):
        a = self.add("timeout", task="fix-bug")
        self.add("passed", task="add-feature")
        self.ok("skilleval", "void", str(a), "--reason", "infra", "--why", "x", agent="owner")
        summary = self.ok("skilleval", "results", agent="owner").stdout.splitlines()[-1]
        self.assertIn("without: passed 1/1 tasks", summary)
        self.assertIn("voided 1", summary)

    def test_an_old_skill_trials_table_gains_the_void_columns_without_losing_rows(self):
        tid = self.add("timeout")
        con = sqlite3.connect(self.env.db_path)
        con.executescript("ALTER TABLE skill_trials DROP COLUMN void_reason; ALTER TABLE skill_trials DROP COLUMN void_why;")
        con.commit()
        con.close()
        self.ok("skilleval", "void", str(tid), "--reason", "infra", "--why", "after migration", agent="owner")
        row = self.trials()[0]
        self.assertEqual((row["status"], row["void_why"], row["tokens_in"]), ("voided", "after migration", 120))


class EntryPointTests(CliBase):
    def tick(self, now="2026-10-26T10:00:00"):
        p = self.env.run("tick", extra_env={"FACTORY_NOW": now})
        self.assertEqual(p.returncode, 0, f"{p.stdout}\n{p.stderr}")
        return p

    def events(self, action):
        return self.env.sql("SELECT * FROM events WHERE action=?", (action,))

    def test_config_shows_the_five_keys_with_the_plan_defaults(self):
        out = self.ok("config", agent="owner").stdout
        for k, v in KEYS.items():
            self.assertIn(f"{k} = {v}", out)

    def test_cmd_tick_calls_skilleval_tick_once_right_after_scout_tick(self):
        src = FACTORY_SRC.read_text()
        body = src[src.index("def cmd_tick"):]
        body = body[:body.index("\ndef ", 1)]
        self.assertEqual(body.count("skilleval_tick(con)"), 1)
        self.assertIn("    scout_tick(con)\n    skilleval_tick(con)\n", body)

    def test_an_exception_in_skilleval_tick_does_not_break_the_tick_and_is_reported_once_a_day(self):
        self.ok("config", "skilleval_trial_minutes", "abc", agent="owner")  # int() raises inside skilleval_tick
        self.tick()
        self.tick("2026-10-26T10:01:00")
        failed = self.events("skilleval-failed")
        self.assertEqual(len(failed), 1)
        self.assertIn("abc", failed[0]["detail"])
        self.tick("2026-10-27T10:00:00")  # next local day: reported again
        self.assertEqual(len(self.events("skilleval-failed")), 2)

    def test_tick_with_a_pending_trial_and_no_token_leaves_it_pending_and_reports_once(self):
        self.ok("skilleval", "queue", "C-0001", "--tasks", "fix-bug", agent="owner")
        self.tick()
        self.tick("2026-10-26T10:01:00")
        self.assertEqual([r["status"] for r in self.trials()], ["pending", "pending"])
        self.assertEqual(len(self.events("skilleval-blocked")), 1)
        self.assertEqual(self.events("skilleval-failed"), [])
        self.assertEqual(self.env.sql("SELECT * FROM agents WHERE role='evaluator'"), [])


if __name__ == "__main__":
    unittest.main()
