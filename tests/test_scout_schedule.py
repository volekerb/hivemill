"""G-009 T4: the tick starts the skill scout once per slot (scout/schedule.py) and keeps it within its budgets.
The clock is $FACTORY_NOW, herdr is the stub and no real Claude runs. Spawn, dequeue and stop are the real code."""
import json
import os
import sqlite3
import sys
import threading
import unittest
from unittest import mock

from harness import FactoryTestCase, REPO

sys.path.insert(0, str(REPO))
from scout import schedule  # noqa: E402

D = "2026-10-26"
PRIOR = "2026-10-25"


def at(hhmm, day=D):
    return f"{day}T{hhmm}:00"


class Base(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.ok("config", "scout_enabled", "1")

    def tick(self, now, rc=0):
        p = self.env.run("tick", extra_env={"FACTORY_NOW": now})
        self.assertEqual(p.returncode, rc, f"tick {now}: rc={p.returncode}\n{p.stdout}\n{p.stderr}")
        return p

    def runs(self):
        return [dict(r) for r in self.env.sql("SELECT * FROM scout_runs ORDER BY id")]

    def starts(self):
        return [c["argv"] for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]]

    def agent(self):
        return dict(self.env.sql("SELECT * FROM agents WHERE name='scout'")[0])

    def pin(self, started=None, queued=None):
        """spawn stamps the real clock; the tests run on $FACTORY_NOW, so move the stamps onto it."""
        if started:
            self.env.sql("UPDATE agents SET started=? WHERE name='scout'", (started,))
        if queued:
            self.env.sql("UPDATE agents SET queued_at=? WHERE name='scout'", (queued,))

    def finish(self):
        self.ok("inbox", agent="scout")  # factory done refuses while orders are unread
        self.ok("done", "scanned", agent="scout")

    def events(self, action):
        return self.env.sql("SELECT * FROM events WHERE action=?", (action,))

    def usage_log(self, session, input_=0, output=0, cache_write=0, cache_read=0):
        d = self.env.user_home / ".claude" / "projects" / "-zz-proj"
        d.mkdir(parents=True, exist_ok=True)
        line = {"type": "assistant", "uuid": "u1", "requestId": "r1", "timestamp": "2026-10-26T09:05:00Z",
                "cwd": "/zz", "message": {"id": "m1", "model": "claude-sonnet-5-5", "usage": {
                    "input_tokens": input_, "output_tokens": output, "cache_creation_input_tokens": cache_write,
                    "cache_read_input_tokens": cache_read}}}
        (d / f"{session}.jsonl").write_text(json.dumps(line) + "\n")


class SlotTests(Base):
    def test_first_tick_spawns_once_and_second_tick_in_the_slot_does_nothing(self):
        self.tick(at("09:01"))
        self.assertEqual([a[2] for a in self.starts()], ["scout"])
        (run,) = self.runs()
        self.assertEqual((run["slot"], run["status"], run["agent"]), (f"{D}T09", "running", "scout"))
        self.assertEqual(run["started"][:16], at("09:01")[:16])
        self.assertEqual(self.agent()["role"], "scout")
        self.assertEqual([e["detail"] for e in self.events("scout-slot")], [f"{D}T09"])
        self.pin(started=at("09:01"))
        self.tick(at("09:02"))
        self.assertEqual(len(self.starts()), 1)
        self.assertEqual(len(self.runs()), 1)
        out = self.ok("scout", "runs", agent="owner").stdout
        self.assertIn(f"{D}T09 running", out)
        self.assertIn("started=", out)

    def test_two_connections_racing_for_one_slot_spawn_once(self):
        with mock.patch.dict(os.environ, {"FACTORY_NOW": at("09:01")}), self.env.load_factory() as f:
            go, errors = threading.Barrier(2), []
            # two connections of their own: load_factory closes the ones f.db() hands out, from the main thread
            def connect():
                con = sqlite3.connect(f.DB, timeout=30, isolation_level=None, check_same_thread=False)
                con.row_factory = sqlite3.Row
                return con

            f.db = connect  # spawn() opens its own connections too, inside the threads
            cons = [connect() for _ in range(2)]

            def worker(con):
                try:
                    go.wait(timeout=20)
                    f.scout_tick(con)
                except BaseException as e:  # noqa: BLE001 — reported by the assertion below
                    errors.append(repr(e))

            ts = [threading.Thread(target=worker, args=(con,)) for con in cons]
            for t in ts:
                t.start()
            for t in ts:
                t.join(60)
            for con in cons:
                con.close()
        self.assertEqual(errors, [])
        self.assertEqual(len(self.starts()), 1)
        (run,) = self.runs()
        self.assertEqual((run["slot"], run["status"]), (f"{D}T09", "running"))
        self.assertEqual(len(self.events("scout-slot")), 1)

    def test_gate_closed_queues_then_the_normal_dequeue_starts_it(self):
        self.ok("config", "max_live_agents", "0")
        self.tick(at("09:01"))
        self.assertEqual(self.starts(), [])
        self.assertEqual(self.agent()["status"], "queued")
        self.assertEqual([r["status"] for r in self.runs()], ["queued"])
        self.pin(queued=at("09:01"))
        self.ok("config", "max_live_agents", "6")
        self.tick(at("09:02"))  # the existing tick dequeue starts it
        self.assertEqual([a[2] for a in self.starts()], ["scout"])
        self.assertEqual(self.agent()["status"], "live")
        self.pin(started=at("09:02"))
        self.tick(at("09:03"))  # ...and the next tick's sync sees it live
        (run,) = self.runs()
        self.assertEqual((run["slot"], run["status"]), (f"{D}T09", "running"))
        self.assertEqual(len(self.starts()), 1)

    def test_scout_still_live_at_the_next_slot_records_a_skipped_slot(self):
        self.tick(at("09:01"))
        self.pin(started=at("17:55"))  # inside its budget: only a stop that failed leaves a scout live this long
        self.tick(at("18:00"))
        self.assertEqual(len(self.starts()), 1)
        rows = {r["slot"]: r for r in self.runs()}
        self.assertEqual(rows[f"{D}T18"]["status"], "skipped")
        self.assertEqual(rows[f"{D}T18"]["stop_reason"], "previous run active")
        self.assertEqual(rows[f"{D}T09"]["status"], "running")

    def test_a_box_that_was_down_runs_only_the_latest_slot(self):
        self.env.sql("INSERT INTO scout_runs(slot, agent, status, started, ended) VALUES(?, 'scout', 'done', ?, ?)",
                     (f"{PRIOR}T18", at("18:01", PRIOR), at("18:09", PRIOR)))
        self.tick(at("20:00"))
        self.assertEqual(sorted(r["slot"] for r in self.runs()), [f"{D}T18".replace(D, PRIOR), f"{D}T18"])
        self.assertEqual(len(self.starts()), 1)

    def test_before_the_first_slot_of_the_day_the_slot_is_yesterdays_last(self):
        self.tick(at("08:30"))
        self.assertEqual([r["slot"] for r in self.runs()], [f"{PRIOR}T18"])

    def test_queued_longer_than_the_limit_is_cancelled_and_skipped(self):
        self.ok("config", "max_live_agents", "0")
        self.tick(at("09:01"))
        self.pin(queued=at("09:01"))
        self.tick(at("14:59"))  # 5 h 58 min: still waiting
        self.assertEqual(self.agent()["status"], "queued")
        self.tick(at("15:02"))
        self.assertEqual(self.agent()["status"], "cancelled")
        (run,) = self.runs()
        self.assertEqual((run["status"], run["stop_reason"]), ("skipped", "gate closed > 6h"))
        self.assertEqual(self.starts(), [])
        self.assertEqual(len(self.events("dequeued")), 1)

    def test_disabled_does_nothing(self):
        self.ok("config", "scout_enabled", "0")
        self.tick(at("09:01"))
        self.assertEqual(self.runs(), [])
        self.assertEqual(self.starts(), [])

    def test_herdr_unreachable_starts_nothing(self):
        self.env.set_herdr(fail=True)
        self.tick(at("09:01"))
        self.assertEqual(self.runs(), [])

    def test_a_finished_scout_ends_its_run(self):
        self.tick(at("09:01"))
        self.pin(started=at("09:01"))
        self.finish()
        self.tick(at("09:20"))
        self.assertEqual(self.runs()[0]["status"], "done")
        self.assertEqual(self.runs()[0]["ended"][:16], at("09:20")[:16])

    def _cand(self, n, first_seen):
        self.env.sql("INSERT INTO scout_candidates(id,key,first_seen,last_seen) VALUES(?,?,?,?)",
                     (f"C-{n:04d}", f"gh:o/r{n}", first_seen, first_seen))

    def test_a_finished_run_stores_new_and_triaged_counts_of_its_window(self):
        self.tick(at("09:01"))
        self.pin(started=at("09:01"))
        self.env.sql("UPDATE scout_runs SET started=?", (at("09:01") + "+01:00",))
        self._cand(1, at("08:00") + "+01:00")                      # before the window
        for n, t in ((2, at("09:05") + "+01:00"), (3, at("09:10") + "+01:00"), (4, "2026-10-26T08:15:00-02:00")):  # 10:15Z, after the window
            self._cand(n, t)
        for cid, t in (("C-0002", at("09:06") + "+01:00"), ("C-0003", at("09:07") + "+01:00"), ("C-0001", at("08:30") + "+01:00")):
            self.env.sql("INSERT INTO scout_triage(candidate_id,purpose,target_roles,overlap,recommendation,rationale,created) "
                         "VALUES(?,?,?,?,?,?,?)", (cid, "p", "coder", "none", "watch", "r", t))
        self.finish()
        self.tick(at("09:20"))
        r = self.runs()[0]
        self.assertEqual((r["status"], r["new_candidates"], r["triaged"]), ("done", 2, 2), str(r))
        body = self.env.sql("SELECT body FROM messages WHERE subject LIKE 'skill scout%'")[0]["body"]
        self.assertIn("This run: 2 new candidates, 2 triaged.", body)

    def test_a_skipped_run_keeps_null_counts(self):
        self.tick(at("09:01"))
        self.pin(started=at("17:55"))
        self.tick(at("18:00"))
        skipped = [r for r in self.runs() if r["status"] == "skipped"]
        self.assertEqual(len(skipped), 1)
        self.assertEqual((skipped[0]["new_candidates"], skipped[0]["triaged"]), (None, None))

    def test_a_refused_spawn_fails_the_slot_without_a_retry(self):
        calls = []

        def refuse(*a, **kw):
            calls.append(1)
            raise SystemExit(2)

        with mock.patch.dict(os.environ, {"FACTORY_NOW": at("09:01")}), self.env.load_factory() as f:
            f.spawn = refuse
            f.scout_tick(f.db())
            f.scout_tick(f.db())
        (run,) = self.runs()
        self.assertEqual(run["status"], "failed")
        self.assertIn("spawn failed", run["stop_reason"])
        self.assertEqual(len(calls), 1)


class BudgetTests(Base):
    def spawned(self):
        self.tick(at("09:00"))
        self.pin(started=at("09:00"))
        return self.agent()

    def test_wall_budget_exceeded_force_stops_the_scout(self):
        self.spawned()
        self.tick(at("09:19"))  # 19 min: within 20
        self.assertEqual(self.runs()[0]["status"], "running")
        self.tick(at("09:21"))
        self.assertEqual(self.agent()["status"], "stopped")
        (run,) = self.runs()
        self.assertEqual((run["status"], run["stop_reason"]), ("stopped", "wall"))
        self.assertIsNotNone(run["ended"])
        self.assertEqual([json.loads(e["detail"])["reason"] for e in self.events("stopped")], ["budget"])

    def test_force_stop_is_called_with_reason_budget(self):
        self.spawned()
        calls = []

        def fake(con, name, how, **kw):
            calls.append((name, how))
            con.execute("UPDATE agents SET status='stopped' WHERE name=?", (name,))
            return "stopped", None

        with mock.patch.dict(os.environ, {"FACTORY_NOW": at("09:21")}), self.env.load_factory() as f:
            f.force_stop = fake
            f.scout_tick(f.db())
        self.assertEqual(calls, [("scout", "budget")])
        self.assertEqual(self.runs()[0]["status"], "stopped")

    def test_token_budget_exceeded_force_stops_and_stores_the_tokens(self):
        sid = self.spawned()["session_id"]
        self.usage_log(sid, input_=100000, output=100000, cache_write=100000, cache_read=999999)
        self.tick(at("09:05"))  # 300000: at the cap, not over; cache reads are not counted
        self.assertEqual(self.runs()[0]["status"], "running")
        self.usage_log(sid, input_=100000, output=100000, cache_write=100001, cache_read=999999)
        self.tick(at("09:06"))
        self.assertEqual(self.agent()["status"], "stopped")
        (run,) = self.runs()
        self.assertEqual((run["status"], run["stop_reason"], run["tokens"]), ("stopped", "tokens", 300001))

    def _failed_first_stop_keeps_reason(self, why, first, second):
        """The budget stop fails once; the tick's own retry stops the agent; the run keeps the budget reason."""
        def failing(con, name, how, **kw):
            return "failed:close-failed", None

        with mock.patch.dict(os.environ, {"FACTORY_NOW": at(first)}), self.env.load_factory() as f:
            f.force_stop = failing
            f.scout_tick(f.db())
        self.assertEqual((self.agent()["status"], self.runs()[0]["stop_reason"]), ("live", why))
        self.ok("config", "stop_timeout_s", "0")  # the tick's stop fallback runs on the real clock
        self.tick(at(second))  # the stop fallback stops the agent; the next tick syncs the run
        self.assertEqual(self.agent()["status"], "stopped")
        self.tick(at(second[:-1] + "9"))
        (run,) = self.runs()
        self.assertEqual((run["status"], run["stop_reason"]), ("stopped", why))

    def test_wall_reason_survives_a_failed_first_stop(self):
        self.spawned()
        self._failed_first_stop_keeps_reason("wall", "09:21", "09:22")

    def test_tokens_reason_survives_a_failed_first_stop(self):
        sid = self.spawned()["session_id"]
        self.usage_log(sid, input_=100000, output=100000, cache_write=100001)
        self._failed_first_stop_keeps_reason("tokens", "09:06", "09:07")

    def test_tokens_are_stored_when_the_scout_finishes_normally(self):
        sid = self.spawned()["session_id"]
        self.usage_log(sid, input_=10, output=20, cache_write=30)
        self.finish()
        self.tick(at("09:10"))
        self.assertEqual((self.runs()[0]["status"], self.runs()[0]["tokens"]), ("done", 60))

    def test_budget_config_is_honoured(self):
        self.ok("config", "scout_max_minutes", "5")
        self.spawned()
        self.tick(at("09:06"))
        self.assertEqual(self.runs()[0]["stop_reason"], "wall")


class FailureTests(Base):
    def test_a_failing_scout_tick_never_breaks_the_tick_and_logs_once_a_day(self):
        self.ok("config", "scout_slots", "bad")
        self.tick(at("09:01"))
        self.tick(at("09:05"))
        self.assertEqual(len(self.events("scout-failed")), 1)
        self.assertIn("scout_slots", self.events("scout-failed")[0]["detail"])
        self.assertTrue((self.env.home_dir / "exports").is_dir())  # the tick went on to export
        self.tick(at("09:01", "2026-10-27"))
        self.assertEqual(len(self.events("scout-failed")), 2)
        self.assertEqual(self.runs(), [])


class ConfigAndSlotTests(FactoryTestCase):
    def test_defaults_on_a_fresh_home(self):
        # the harness turns the scout off for unrelated tick tests, so the enabled default is read from the code
        with self.env.load_factory() as f:
            self.assertEqual(f.DEFAULT_CONFIG["scout_enabled"], "1")
        out = self.ok("config").stdout
        for line in ("scout_slots = 09,18", "scout_max_minutes = 20", "scout_max_tokens = 300000", "scout_queue_max_h = 6"):
            self.assertIn(line, out, line)

    def test_slot_arithmetic(self):
        import datetime as dt
        slot = lambda s, h: schedule.current_slot(dt.datetime.fromisoformat(s), schedule.parse_slots(h))  # noqa: E731
        self.assertEqual(slot("2026-10-26T09:00:00", "09,18"), "2026-10-26T09")
        self.assertEqual(slot("2026-10-26T17:59:00", "09,18"), "2026-10-26T09")
        self.assertEqual(slot("2026-10-26T23:59:00", "18,09"), "2026-10-26T18")
        self.assertEqual(slot("2026-10-26T08:59:00", "09,18"), "2026-10-25T18")
        for bad in ("", "x", "9,24", "-1"):
            with self.assertRaises(ValueError, msg=bad):
                schedule.parse_slots(bad)


if __name__ == "__main__":
    unittest.main()
