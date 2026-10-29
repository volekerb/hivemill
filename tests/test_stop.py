"""T-0035: `factory stop` — a real STOP for busy workers (message, Esc, wait, nudge, hard fallback)."""
import argparse
import io
import json
import os
import shutil
import subprocess
import sys
import time
import unittest
from unittest import mock
from datetime import datetime, timedelta

from harness import FACTORY, FactoryTestCase

SID = "sess-zz-coder"
OLD = "2026-01-01T00:00:00+00:00"


def ago(seconds):
    return (datetime.now().astimezone() - timedelta(seconds=seconds)).isoformat(timespec="seconds")


class StopBase(FactoryTestCase):
    INPROC = True
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team("g-001-a", lead="zz-lead")
        e.seed_team("g-001-b", lead="zz-lead-b")
        e.seed_task(status="in_progress", team="g-001-a")
        e.seed_agent("zz-director", "director", goal="G-001")
        e.seed_agent("zz-boss", "boss")
        e.seed_agent("zz-lead", "lead", goal="G-001", team="g-001-a")
        e.seed_agent("zz-lead-b", "lead", goal="G-001", team="g-001-b")
        e.seed_agent("zz-coder", "coder", goal="G-001", team="g-001-a", task="T-0001", pane_id="p1",
                     started=ago(3600))
        e.seed_agent("zz-coder-b", "coder", goal="G-001", team="g-001-b", pane_id="p2", started=ago(3600))
        e.sql("UPDATE agents SET session_id=? WHERE name='zz-coder'", (SID,))
        self.herdr_agents(["zz-coder", "p1"], ["zz-coder-b", "p2"])

    def herdr_agents(self, *specs):
        self.env.set_herdr(
            workspaces=[{"workspace_id": "w1", "label": "w"}],
            panes=[{"pane_id": p, "tab_id": "t1", "workspace_id": "w1"} for _, p in specs],
            agents=[{"name": n, "agent": "claude", "pane_id": p, "agent_status": "working"} for n, p in specs])

    def row(self, name="zz-coder"):
        return self.env.sql("SELECT * FROM agents WHERE name=?", (name,))[0]

    def herdr_verbs(self):
        return [tuple(c["argv"][:2]) for c in self.env.calls("herdr")]

    def events(self, action, name="zz-coder"):
        return self.env.sql("SELECT * FROM events WHERE action=? AND entity_id=?", (action, name))

    def msgs(self, rcpt, like="%"):
        return self.env.sql("SELECT * FROM messages WHERE recipient=? AND subject LIKE ?", (rcpt, like))

    def stop(self, *args, agent="zz-lead", code=0, extra_env=None):
        p = self.env.run("stop", *args, agent=agent, extra_env=extra_env)
        self.assertEqual(p.returncode, code, f"stop {args}: rc={p.returncode}\n{p.stdout}\n{p.stderr}")
        return p

    def tick(self, extra_env=None):
        p = self.env.run("tick", extra_env=extra_env)
        self.assertEqual(p.returncode, 0, f"{p.stdout}\n{p.stderr}")
        return p

    def age_stop(self, seconds=3600, name="zz-coder"):
        self.env.sql("UPDATE agents SET stop_requested_at=? WHERE name=?", (ago(seconds), name))


class StopTests(StopBase):
    def test_stop_sends_decision_esc_wait_prompt_in_order(self):
        self.stop("zz-coder", "--reason", "scope changed")
        m = self.msgs("zz-coder", "STOP")
        self.assertEqual(len(m), 1)
        self.assertEqual((m[0]["kind"], m[0]["sender"], m[0]["read_at"]), ("decision", "zz-lead", None))
        self.assertIn("scope changed", m[0]["body"])
        self.assertIn("Stop work now. Push any work in progress to your branch", m[0]["body"])
        self.assertIn("factory done '<stopped: pushed sha|nothing; state>'", m[0]["body"])
        ev = self.events("stop-requested")
        self.assertEqual(json.loads(ev[0]["detail"]), {"by": "zz-lead", "reason": "scope changed"})
        self.assertTrue(self.row()["stop_requested_at"])
        calls = [c["argv"] for c in self.env.calls("herdr")]
        i_esc = calls.index(["agent", "send-keys", "zz-coder", "esc"])
        wait = next(i for i, c in enumerate(calls) if c[:3] == ["agent", "wait", "zz-coder"])
        self.assertEqual([c for c in calls[wait] if c.startswith("--")],
                         ["--until", "--until", "--until", "--timeout"])
        self.assertEqual(calls[wait][-1], "30000")
        prompt = next(i for i, c in enumerate(calls) if c[:3] == ["agent", "prompt", "zz-coder"])
        self.assertLess(i_esc, wait)
        self.assertLess(wait, prompt)
        self.assertEqual(calls[prompt][3], "📬 STOP from zz-lead: run factory inbox now and follow the STOP order.")
        self.assertEqual(self.row()["status"], "live")

    def test_stop_dry_run_changes_nothing(self):
        before = (self.env.sql("SELECT count(*) c FROM messages")[0]["c"], self.env.sql("SELECT count(*) c FROM events")[0]["c"])
        p = self.stop("zz-coder", "--dry-run")
        self.assertIn("send-keys zz-coder esc", p.stdout)
        self.assertIn("herdr agent wait zz-coder", p.stdout)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM messages")[0]["c"], before[0])
        self.assertEqual(self.env.sql("SELECT count(*) c FROM events")[0]["c"], before[1])
        self.assertIsNone(self.row()["stop_requested_at"])
        self.assertEqual(self.env.calls("herdr"), [])

    def test_lead_may_stop_own_team_worker_only(self):
        p = self.stop("zz-coder-b", code=3)
        self.assertIn("not in your team", p.stderr)
        self.assertEqual(self.env.calls("herdr"), [])
        self.assertIsNone(self.row("zz-coder-b")["stop_requested_at"])
        self.stop("zz-coder")
        self.assertTrue(self.row()["stop_requested_at"])

    def test_director_may_stop_any_worker_not_hq(self):
        self.stop("zz-coder-b", agent="zz-director")
        self.assertTrue(self.row("zz-coder-b")["stop_requested_at"])
        for hq in ("zz-lead", "zz-boss", "zz-director"):
            p = self.stop(hq, agent="zz-director", code=3)
            self.assertIn("workers only", p.stderr)
            self.assertIsNone(self.row(hq)["stop_requested_at"])
        self.stop("zz-coder", agent="owner")
        self.assertTrue(self.row()["stop_requested_at"])

    def test_worker_may_not_stop(self):
        p = self.stop("zz-coder-b", agent="zz-coder", code=3)
        self.assertIn("workers may not run `factory stop`", p.stderr)
        self.assertIsNone(self.row("zz-coder-b")["stop_requested_at"])

    def test_unknown_and_non_live_agent_exit_2(self):
        self.stop("zz-nobody", agent="zz-director", code=2)
        self.env.sql("UPDATE agents SET status='exited' WHERE name='zz-coder'")
        self.stop("zz-coder", agent="zz-director", code=2)

    def test_tick_force_stops_after_timeout(self):
        self.env.run("config", "max_live_agents", "2")
        self.stop("zz-coder")
        self.assertIn("agents live 2/2", self.env.run("res").stdout)
        self.age_stop(200)
        self.tick()
        self.assertEqual(self.row()["status"], "live")  # not yet
        self.age_stop(301)
        self.tick()
        self.assertIn(("pane", "close"), self.herdr_verbs())
        self.assertIn(["pane", "close", "p1"], [c["argv"] for c in self.env.calls("herdr")])
        r = self.row()
        self.assertEqual(r["status"], "stopped")
        self.assertTrue(r["ended"])
        self.assertEqual(json.loads(self.events("stopped")[0]["detail"]), {"reason": "timeout", "pane": "p1"})
        self.assertIn("agents live 1/2", self.env.run("res").stdout)  # slot freed
        m = self.msgs("zz-lead", "agent zz-coder force-stopped")
        self.assertEqual(len(m), 1)
        self.assertRegex(m[0]["body"], r"force-stopped after \d+s; its branch holds whatever it pushed")
        self.assertIn("gh pr list --head factory/t-0001", m[0]["body"])
        self.assertIn("`factory task show T-0001` before reassigning", m[0]["body"])
        self.assertIn("zz-coder", self.env.run("agents", "--all").stdout)
        self.assertNotIn("zz-coder ", self.env.run("agents").stdout)

    def test_stop_now_closes_immediately(self):
        p = self.stop("zz-coder", "--now", agent="zz-director")
        self.assertIn("stopped now", p.stdout)
        self.assertEqual(self.row()["status"], "stopped")
        self.assertEqual(len(self.msgs("zz-coder", "STOP")), 1)  # the order is still sent, for the record
        verbs = self.herdr_verbs()
        self.assertIn(("pane", "close"), verbs)
        self.assertNotIn(("agent", "send-keys"), verbs)
        self.assertNotIn(("agent", "wait"), verbs)
        self.assertNotIn(("agent", "prompt"), verbs)
        self.assertEqual(json.loads(self.events("stopped")[0]["detail"])["reason"], "now")

    def test_done_after_stop_clears_flag_and_no_force_close(self):
        self.stop("zz-coder")
        self.env.sql("UPDATE agents SET stop_attempt_at=?, stop_failed_kind='close-failed' WHERE name='zz-coder'", (OLD,))
        self.assertIn("unread orders", self.env.run("done", "x", agent="zz-coder").stderr)  # must read the order first
        self.ok("done", "stopped: nothing; clean", agent="zz-coder")
        r = self.row()
        self.assertEqual((r["status"], r["stop_requested_at"], r["stop_attempt_at"], r["stop_failed_kind"]),
                         ("done", None, None, None))
        self.tick()
        self.assertEqual(self.events("stopped"), [])
        self.assertEqual(self.row()["status"], "exited")  # the normal reap

    def test_done_refused_when_not_live(self):
        for status in ("stopped", "done", "lost"):
            self.env.sql("UPDATE agents SET status=? WHERE name='zz-coder'", (status,))
            p = self.refused(2, "done", "again", agent="zz-coder")
            self.assertIn("not live", p.stderr)
        self.assertEqual(self.msgs("zz-lead", "zz-coder finished%"), [])

    def test_migration_adds_stop_requested_at_idempotent(self):
        cols = ("stop_requested_at", "stop_attempt_at", "stop_failed_kind")
        for c in cols:
            self.env.sql(f"ALTER TABLE agents DROP COLUMN {c}")
        for _ in range(2):
            self.ok("agents", "--all")
            names = {r["name"] for r in self.env.sql("PRAGMA table_info(agents)")}
            self.assertTrue(set(cols) <= names)
        self.stop("zz-coder")  # and the new columns work
        self.assertTrue(self.row()["stop_requested_at"])


class StopFailureTests(StopBase):
    def test_stop_failed_when_pane_close_fails_keeps_counted(self):
        self.env.set_herdr(pane_close_fail=True)
        p = self.stop("zz-coder", "--now", agent="zz-director", code=1, extra_env={"FACTORY_STOP_CONFIRM_S": "0.2"})
        self.assertIn("close-failed", p.stderr)
        self.assertIn("still counted", p.stderr)
        self.assertEqual(self.row()["status"], "live")
        d = json.loads(self.events("stop-failed")[0]["detail"])
        self.assertEqual((d["name"], d["pane"], d["reason"], d["pids"]), ("zz-coder", "p1", "close-failed", []))
        self.assertIn("agents live 2/", self.env.run("res").stdout)

    def test_stop_failed_when_process_alive_keeps_counted(self):
        self.env.fake_claude(4242, argv=("claude", "--resume", SID))  # survives a 'successful' pane close
        p = self.stop("zz-coder", "--now", agent="zz-director", code=1, extra_env={"FACTORY_STOP_CONFIRM_S": "0.2"})
        self.assertIn("process-alive", p.stderr)
        self.assertEqual(self.row()["status"], "live")
        self.assertIn(("pane", "close"), self.herdr_verbs())
        d = json.loads(self.events("stop-failed")[0]["detail"])
        self.assertEqual((d["reason"], d["pids"]), ("process-alive", [4242]))

    def test_stopped_only_after_close_and_no_process(self):
        fast = {"FACTORY_STOP_CONFIRM_S": "0.2"}
        self.env.set_herdr(pane_close_fail=True)
        self.env.fake_claude(4242, argv=("claude", "--session-id", SID))
        self.stop("zz-coder", "--now", agent="zz-director", code=1, extra_env=fast)
        self.assertEqual(self.row()["status"], "live")
        self.env.set_herdr(pane_close_fail=False)  # the close works, the process is still there
        self.env.sql("UPDATE agents SET stop_requested_at=NULL WHERE name='zz-coder'")  # a repeat stop keeps its deadline
        self.stop("zz-coder", "--now", agent="zz-director", code=1, extra_env=fast)
        self.assertEqual(self.row()["status"], "live")
        shutil.rmtree(self.env.proc_dir / "4242")  # now both hold
        self.herdr_agents(["zz-coder", "p1"], ["zz-coder-b", "p2"])
        self.env.sql("UPDATE agents SET stop_requested_at=NULL WHERE name='zz-coder'")
        self.stop("zz-coder", "--now", agent="zz-director", extra_env=fast)
        self.assertEqual(self.row()["status"], "stopped")

    def test_stop_retry_throttled_and_message_once_per_kind(self):
        fast = {"FACTORY_STOP_CONFIRM_S": "0.2"}
        self.env.fake_claude(4242, argv=("claude", "--resume", SID))
        self.stop("zz-coder")
        self.age_stop()
        failed = lambda: len(self.events("stop-failed"))
        alerts = lambda: len(self.msgs("zz-lead", "stop of zz-coder failed%"))
        self.tick(fast)
        self.assertEqual((failed(), alerts()), (1, 1))
        m = self.msgs("zz-lead", "stop of zz-coder failed%")[0]
        self.assertIn("process-alive", m["body"])
        self.assertIn("still counted", m["body"])
        self.assertIn("gh pr list --head factory/t-0001", m["body"])
        self.tick(fast)  # within stop_timeout_s of the attempt: no retry
        self.assertEqual((failed(), alerts()), (1, 1))
        self.env.sql("UPDATE agents SET stop_attempt_at=? WHERE name='zz-coder'", (ago(301),))
        self.tick(fast)  # retried, same kind: no new message
        self.assertEqual((failed(), alerts()), (2, 1))
        self.env.sql("UPDATE agents SET stop_attempt_at=? WHERE name='zz-coder'", (ago(301),))
        self.herdr_agents(["zz-coder", "p1"], ["zz-coder-b", "p2"])  # the pane is back, and cannot be closed
        self.env.set_herdr(pane_close_fail=True)
        self.tick(fast)  # the kind changed: one more message
        self.assertEqual((failed(), alerts()), (3, 2))
        self.assertEqual(self.row()["status"], "live")


class StopHardeningTests(StopBase):
    fast = {"FACTORY_STOP_CONFIRM_S": "0.2"}

    def null_sid(self):
        self.env.sql("UPDATE agents SET session_id=NULL WHERE name='zz-coder'")

    def test_force_stop_null_session_fails_closed(self):
        self.null_sid()
        p = self.stop("zz-coder", "--now", agent="zz-director", code=1, extra_env=self.fast)
        self.assertIn("process-unknown", p.stderr)
        self.assertIn("The pane was NOT closed", p.stderr)
        self.assertIn("end it by hand or relaunch it", p.stderr)
        self.assertIn("NOT closed", json.loads(self.events("stop-failed")[0]["detail"])["note"])
        self.assertEqual(self.row()["status"], "live")
        self.assertEqual(self.row()["stop_failed_kind"], "process-unknown")
        self.assertEqual(json.loads(self.events("stop-failed")[0]["detail"])["reason"], "process-unknown")
        self.assertNotIn(("pane", "close"), self.herdr_verbs())
        self.assertIn("agents live 2/", self.env.run("res").stdout)  # still counted
        # the tick retries the same way and sends no second message of that kind
        self.age_stop()
        self.tick(self.fast)
        self.assertEqual(len(self.msgs("zz-director", "stop of zz-coder failed%")), 1)
        self.assertEqual(self.row()["status"], "live")

    def test_force_stop_null_session_uses_herdr_agent_session(self):
        self.null_sid()
        self.env.set_herdr(agents=[{"name": "zz-coder", "agent": "claude", "pane_id": "p1", "agent_status": "working",
                                    "agent_session": {"value": SID}}])
        self.env.fake_claude(4242, argv=("claude", "--resume", SID))
        p = self.stop("zz-coder", "--now", agent="zz-director", code=1, extra_env=self.fast)
        self.assertIn("process-alive", p.stderr)  # the session was found, so the process check ran
        shutil.rmtree(self.env.proc_dir / "4242")
        self.env.sql("UPDATE agents SET stop_requested_at=NULL WHERE name='zz-coder'")
        self.env.set_herdr(agents=[{"name": "zz-coder", "agent": "claude", "pane_id": "p1", "agent_status": "working",
                                    "agent_session": {"value": SID}}])
        self.stop("zz-coder", "--now", agent="zz-director", extra_env=self.fast)
        self.assertEqual(self.row()["status"], "stopped")

    def test_force_stop_proc_unreadable_fails_closed(self):
        gone = str(self.env.home / "no-such-proc-dir")
        p = self.stop("zz-coder", "--now", agent="zz-director", code=1,
                      extra_env={**self.fast, "FACTORY_PROC_DIR": gone})
        self.assertIn("process-unknown", p.stderr)
        self.assertIn("The pane was NOT closed", p.stderr)
        self.assertEqual(self.row()["status"], "live")
        self.assertNotIn(("pane", "close"), self.herdr_verbs())
        self.assertEqual(json.loads(self.events("stop-failed")[0]["detail"])["reason"], "process-unknown")

    def test_force_stop_proc_lost_after_close_says_pane_closed(self):
        self.stop("zz-coder")
        self.age_stop()
        self.env.set_herdr(agents=[])
        with self.env.load_factory() as f:
            with mock.patch.object(f, "_scan_claude", side_effect=[{}, None]), \
                    mock.patch.object(f, "pane_gone", return_value=True):
                con = f.db()
                try:
                    self.assertEqual(f.force_stop(con, "zz-coder", "timeout"),
                                     ("failed:process-unknown", f._UNKNOWN_AFTER))
                finally:
                    con.close()
        note = json.loads(self.events("stop-failed")[0]["detail"])["note"]
        self.assertIn("The pane was closed but the process state is unknown", note)
        self.assertIn("end it by hand or relaunch it", note)
        self.assertEqual(self.row()["status"], "live")

    def test_stop_ignores_non_live_null_sid_rows(self):
        for st in ("exited", "done", "lost"):
            self.env.sql("UPDATE agents SET status=?, session_id=NULL, stop_requested_at=NULL WHERE name='zz-coder-b'", (st,))
            self.stop("zz-coder-b", agent="zz-director", code=2)
            self.stop("zz-coder-b", "--now", agent="zz-director", code=2)
            self.tick(self.fast)
            self.assertIn(self.row("zz-coder-b")["status"], (st, "exited"))  # the tick may reap a done row
        self.assertEqual(self.events("stop-failed", "zz-coder-b"), [])
        self.assertEqual(self.events("stopped", "zz-coder-b"), [])

    def test_force_stop_rechecks_status_before_close(self):
        # the worker runs `factory done` after the pane lookup, before the close
        hook = (f"{sys.executable} -c \"import sqlite3; c=sqlite3.connect('{self.env.db_path}'); "
                "c.execute(\\\"UPDATE agents SET status='done' WHERE name='zz-coder'\\\"); c.commit()\"")
        p = self.stop("zz-coder", "--now", agent="zz-director", extra_env={**self.fast, "FAKE_HERDR_HOOK": hook})
        self.assertIn("finished meanwhile", p.stdout)
        self.assertEqual(self.row()["status"], "done")
        self.assertNotIn(("pane", "close"), self.herdr_verbs())
        self.assertEqual(self.events("stop-failed"), [])
        self.assertEqual(self.events("stopped"), [])

    def test_stop_now_failure_messages_caller(self):
        self.env.set_herdr(pane_close_fail=True)
        self.stop("zz-coder", "--now", agent="zz-director", code=1, extra_env=self.fast)
        m = self.msgs("zz-director", "stop of zz-coder failed%")
        self.assertEqual(len(m), 1)
        self.assertEqual((m[0]["kind"], m[0]["sender"]), ("alert", "zz-director"))
        self.assertIn("close-failed", m[0]["body"])
        self.age_stop()
        self.tick(self.fast)  # the tick's retry has the same kind: no second message
        self.assertEqual(len(self.msgs("zz-director", "stop of zz-coder failed%")), 1)

    def test_repeat_stop_keeps_original_deadline(self):
        self.stop("zz-coder")
        self.env.sql("UPDATE agents SET stop_requested_at=?, stop_attempt_at=?, stop_failed_kind='close-failed' "
                     "WHERE name='zz-coder'", (ago(100), ago(50)))
        before = dict(self.row())
        n_msgs, n_calls = len(self.msgs("zz-coder", "STOP")), len(self.env.calls("herdr"))
        for who in ("zz-lead", "zz-director"):
            p = self.stop("zz-coder", agent=who)
            self.assertIn(f"already stopping since {before['stop_requested_at']}; the hard fallback runs at", p.stdout)
            self.assertNotIn("--now is ignored", p.stdout)
        p = self.stop("zz-coder", agent="zz-director")
        self.assertIn("(retrying; last attempt " + before["stop_attempt_at"] + ")", p.stdout)
        # next fallback = the later of request/last attempt + stop_timeout_s (default 300), not request + 300
        due = (datetime.fromisoformat(before["stop_attempt_at"]) + timedelta(seconds=300)).isoformat(timespec="seconds")
        self.assertIn(f"the hard fallback runs at {due}", p.stdout)
        after = self.row()
        for col in ("stop_requested_at", "stop_attempt_at", "stop_failed_kind"):
            self.assertEqual(after[col], before[col])
        self.assertEqual(len(self.msgs("zz-coder", "STOP")), n_msgs)
        self.assertEqual(len(self.env.calls("herdr")), n_calls)  # no Esc, wait or prompt
        # --now escalates: the order and the flag stay, the hard fallback runs (the pane closes, the agent stops)
        p = self.stop("zz-coder", "--now", agent="zz-director", extra_env={"FACTORY_STOP_CONFIRM_S": "0.2"})
        self.assertNotIn("--now is ignored", p.stdout)
        self.assertIn("stopped now", p.stdout)
        self.assertEqual(self.row()["status"], "stopped")
        self.assertEqual(self.row()["stop_requested_at"], before["stop_requested_at"])
        self.assertEqual(len(self.msgs("zz-coder", "STOP")), n_msgs)


class StopRaceTests(StopBase):
    def test_respawn_clears_stop_flag(self):
        self.stop("zz-coder", "--now", agent="zz-director")
        self.assertEqual(self.row()["status"], "stopped")
        self.ok("spawn", "coder", "--goal", "G-001", "--team", "g-001-a", "--task", "T-0001", "--name", "zz-coder",
                "--cwd", str(self.env.home), agent="zz-director")
        r = self.row()
        self.assertEqual((r["status"], r["stop_requested_at"], r["stop_attempt_at"], r["stop_failed_kind"]),
                         ("live", None, None, None))

    def test_respawn_via_queue_clears_stop_flag(self):
        self.stop("zz-coder", "--now", agent="zz-director")
        self.env.run("config", "max_live_agents", "0")  # gate closed: the spawn is queued over the stopped row
        self.ok("spawn", "coder", "--goal", "G-001", "--name", "zz-coder", "--cwd", str(self.env.home),
                agent="zz-director")
        r = self.row()
        self.assertEqual((r["status"], r["stop_requested_at"], r["stop_attempt_at"]), ("queued", None, None))

    def test_fallback_does_not_overwrite_done(self):
        self.stop("zz-coder")
        self.env.sql("UPDATE agents SET status='done', summary='finished' WHERE name='zz-coder'")  # flag still set
        n = len(self.env.calls("herdr"))
        with self.env.load_factory() as f:
            self.assertEqual(f.force_stop(f.db(), "zz-coder", "timeout"), ("skip", None))
        self.assertEqual(len(self.env.calls("herdr")), n)  # no pane close
        self.assertEqual((self.row()["status"], self.row()["summary"]), ("done", "finished"))
        self.assertEqual(self.events("stopped"), [])
        self.assertEqual(self.msgs("zz-lead", "agent zz-coder force-stopped"), [])

    def test_tick_does_not_mark_stopping_agent_lost(self):
        self.herdr_agents(["zz-coder-b", "p2"])  # herdr no longer lists zz-coder
        self.stop("zz-coder")
        self.tick()
        self.assertEqual(self.row()["status"], "live")
        self.assertEqual(self.events("lost"), [])
        self.assertEqual(self.msgs("director", "agent zz-coder vanished"), [])
        self.env.fake_claude(4242, argv=("claude", "--resume", SID))
        self.age_stop()
        self.tick({"FACTORY_STOP_CONFIRM_S": "0.2"})  # counted while its process lives
        self.assertEqual(self.row()["status"], "live")
        self.assertEqual(json.loads(self.events("stop-failed")[0]["detail"])["reason"], "process-alive")
        shutil.rmtree(self.env.proc_dir / "4242")
        self.env.sql("UPDATE agents SET stop_attempt_at=NULL WHERE name='zz-coder'")
        self.tick()
        self.assertEqual(self.row()["status"], "stopped")  # pane already gone, no process left

    def test_stop_aborts_steps_when_agent_done(self):
        hook = (f"{sys.executable} -c \"import sqlite3; c=sqlite3.connect('{self.env.db_path}'); "
                "c.execute(\\\"UPDATE agents SET status='done' WHERE name='zz-coder'\\\"); c.commit()\"")
        p = self.stop("zz-coder", extra_env={"FAKE_HERDR_HOOK": hook})
        self.assertIn("no longer live", p.stdout)
        verbs = self.herdr_verbs()
        self.assertIn(("agent", "send-keys"), verbs)  # the first step was already in flight
        self.assertNotIn(("agent", "wait"), verbs)
        self.assertNotIn(("agent", "prompt"), verbs)

    def test_stop_holds_no_transaction_during_wait(self):
        self.env.set_herdr(wait_sleep=4)
        proc = subprocess.Popen([sys.executable, str(FACTORY), "stop", "zz-coder"], env=self.env.env("zz-lead"),
                                cwd=self.env.home, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        try:
            end = time.time() + 20
            while ("agent", "wait") not in self.herdr_verbs():
                self.assertLess(time.time(), end, "stop never reached its wait")
                time.sleep(0.05)
            t0 = time.time()
            self.env.sql("UPDATE agents SET summary='written during the wait' WHERE name='zz-coder'")
            self.assertLess(time.time() - t0, 2)  # not blocked behind a stop transaction
            self.assertIsNone(proc.poll())  # the stop was still waiting
        finally:
            proc.communicate(timeout=30)

    def test_dequeue_waits_after_stop_same_worktree(self):
        e = self.env
        wt = str(e.home / "wt")
        e.sql("UPDATE agents SET cwd=? WHERE name='zz-coder'", (wt,))
        self.stop("zz-coder", "--now", agent="zz-director")
        self.assertEqual(self.row()["status"], "stopped")
        args = {"role": "coder", "goal": "G-001", "team": "g-001-a", "task": "T-0001", "name": "zz-succ", "cwd": wt,
                "prompt": None, "model": None, "resume": None}
        e.sql("INSERT INTO agents(name,role,goal_id,team_id,task_id,status,spawn_args,queued_at) "
              "VALUES('zz-succ','coder','G-001','g-001-a','T-0001','queued',?,?)", (json.dumps(args), ago(10)))
        self.tick()
        self.assertEqual(self.row("zz-succ")["status"], "queued")  # waits: the stopped agent may still be writing
        e.sql("UPDATE agents SET ended=? WHERE name='zz-coder'", (ago(200),))
        self.tick()
        self.assertEqual(self.row("zz-succ")["status"], "live")


class StopEscalationTests(StopBase):
    """T-0048: `factory stop --now` on an agent that is already stopping goes straight to the hard fallback."""
    fast = {"FACTORY_STOP_CONFIRM_S": "0.2"}

    def setUp(self):
        super().setUp()
        self.stop("zz-coder")  # the plain stop: one STOP message, stop_requested_at set
        self.before = dict(self.row())
        self.n_calls = len(self.env.calls("herdr"))

    def assert_failed(self, p, kind):
        self.assertIn(kind, p.stderr)
        self.assertEqual(self.row()["status"], "live")
        self.assertEqual(self.row()["stop_requested_at"], self.before["stop_requested_at"])
        self.assertEqual(len(self.msgs("zz-coder", "STOP")), 1)
        self.assertEqual(json.loads(self.events("stop-escalated")[0]["detail"]), {"by": "zz-director"})
        self.assertEqual(json.loads(self.events("stop-failed")[0]["detail"])["reason"], kind)
        self.assertEqual(self.row()["stop_failed_kind"], kind)

    def test_escalation_closes_pane(self):
        p = self.stop("zz-coder", "--now", agent="zz-director", extra_env=self.fast)
        self.assertIn("stopped now", p.stdout)
        self.assertEqual(self.row()["status"], "stopped")
        self.assertIn(("pane", "close"), self.herdr_verbs())
        self.assertEqual(json.loads(self.events("stop-escalated")[0]["detail"]), {"by": "zz-director"})
        self.assertEqual(json.loads(self.events("stopped")[0]["detail"])["reason"], "now")
        self.assertEqual(len(self.msgs("zz-coder", "STOP")), 1)
        self.assertEqual(self.row()["stop_requested_at"], self.before["stop_requested_at"])
        self.assertEqual(len(self.events("stop-requested")), 1)

    def test_escalation_close_failed(self):
        self.env.set_herdr(pane_close_fail=True)
        p = self.stop("zz-coder", "--now", agent="zz-director", code=1, extra_env=self.fast)
        self.assert_failed(p, "close-failed")

    def test_escalation_process_alive(self):
        self.env.fake_claude(4242, argv=("claude", "--resume", SID))
        p = self.stop("zz-coder", "--now", agent="zz-director", code=1, extra_env=self.fast)
        self.assert_failed(p, "process-alive")

    def test_escalation_process_unknown(self):
        gone = str(self.env.home / "no-such-proc-dir")
        p = self.stop("zz-coder", "--now", agent="zz-director", code=1,
                      extra_env={**self.fast, "FACTORY_PROC_DIR": gone})
        self.assert_failed(p, "process-unknown")
        self.assertNotIn(("pane", "close"), self.herdr_verbs())

    def test_escalation_permissions_unchanged(self):
        p = self.stop("zz-coder", "--now", agent="zz-lead-b", code=3)
        self.assertIn("not in your team", p.stderr)
        p = self.stop("zz-coder", "--now", agent="zz-coder-b", code=3)
        self.assertIn("workers may not run `factory stop`", p.stderr)
        self.env.sql("UPDATE agents SET status='exited' WHERE name='zz-coder'")
        self.stop("zz-coder", "--now", agent="zz-director", code=2)
        self.assertEqual(self.events("stop-escalated"), [])
        self.assertEqual(len(self.env.calls("herdr")), self.n_calls)

    def test_escalation_dry_run_changes_nothing(self):
        n_events = self.env.sql("SELECT count(*) c FROM events")[0]["c"]
        p = self.stop("zz-coder", "--now", "--dry-run", agent="zz-director")
        self.assertIn("escalates to the hard fallback now", p.stdout)
        self.assertEqual(dict(self.row()), self.before)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM events")[0]["c"], n_events)
        self.assertEqual(len(self.env.calls("herdr")), self.n_calls)
        self.assertEqual(len(self.msgs("zz-coder", "STOP")), 1)

    def test_stale_attempt_gives_skip_and_no_close(self):
        seen = self.row()["stop_attempt_at"]
        self.env.sql("UPDATE agents SET stop_attempt_at=? WHERE name='zz-coder'", (ago(1),))  # the tick got there first
        with self.env.load_factory() as f:
            con = f.db()
            try:
                self.assertEqual(f.force_stop(con, "zz-coder", "now", attempt_seen=seen), ("skip", None))
            finally:
                con.close()
        self.assertEqual(self.row()["status"], "live")
        self.assertEqual(len(self.env.calls("herdr")), self.n_calls)
        self.assertEqual(self.events("stopped"), [])
        self.assertEqual(self.events("stop-failed"), [])

    def test_cmd_stop_skips_when_attempt_races_the_read(self):
        # the row changes between cmd_stop's read and force_stop's compare-and-set
        with self.env.load_factory() as f:
            real = f.force_stop

            def racing(con, name, how, attempt_seen=None):
                con.execute("UPDATE agents SET stop_attempt_at=? WHERE name=?", (ago(1), name))
                return real(con, name, how, attempt_seen=attempt_seen)
            os.environ["FACTORY_AGENT"], os.environ["FACTORY_ROLE"] = "zz-director", "director"
            out = io.StringIO()
            try:
                with mock.patch.object(f, "force_stop", racing), mock.patch("sys.stdout", out):
                    f.cmd_stop(argparse.Namespace(agent="zz-coder", now=True, dry_run=False, reason=None))
            finally:
                os.environ.pop("FACTORY_AGENT", None)
                os.environ.pop("FACTORY_ROLE", None)
        self.assertIn("another stop attempt ran meanwhile; nothing was closed", out.getvalue())
        self.assertEqual(self.row()["status"], "live")
        self.assertEqual(len(self.env.calls("herdr")), self.n_calls)
        self.assertEqual(self.events("stopped"), [])


class ForceStopReturnTests(StopBase):
    """T-0049: force_stop returns (result, note); the --now claim is atomic."""
    fast = {"FACTORY_STOP_CONFIRM_S": "0.2"}

    def call(self, **patches):
        with self.env.load_factory() as f:
            con = f.db()
            try:
                with mock.patch.multiple(f, **patches):
                    return f.force_stop(con, "zz-coder", "now"), f
            finally:
                con.close()

    def test_returns_stopped_none(self):
        self.stop("zz-coder")
        with self.env.load_factory() as f:
            with mock.patch.object(f, "_scan_claude", return_value={}), mock.patch.object(f, "pane_gone", return_value=True):
                con = f.db()
                try:
                    self.assertEqual(f.force_stop(con, "zz-coder", "now"), ("stopped", None))
                finally:
                    con.close()

    def test_returns_unknown_before_note(self):
        self.stop("zz-coder")
        (res, f) = self.call(_scan_claude=mock.Mock(return_value=None))
        self.assertEqual(res, ("failed:process-unknown", f._UNKNOWN_BEFORE))
        self.assertEqual(json.loads(self.events("stop-failed")[0]["detail"])["note"], f._UNKNOWN_BEFORE)

    def test_returns_unknown_after_note(self):
        self.stop("zz-coder")
        (res, f) = self.call(_scan_claude=mock.Mock(side_effect=[{}, None]), pane_gone=mock.Mock(return_value=True))
        self.assertEqual(res, ("failed:process-unknown", f._UNKNOWN_AFTER))

    def test_other_failures_have_no_note(self):
        self.stop("zz-coder")
        (res, _f) = self.call(_scan_claude=mock.Mock(return_value={}), pane_gone=mock.Mock(return_value=False))
        self.assertEqual(res, ("failed:close-failed", None))

    def test_skip_returns_none_note(self):
        self.stop("zz-coder")
        with self.env.load_factory() as f:
            con = f.db()
            try:
                self.assertEqual(f.force_stop(con, "zz-coder", "now", attempt_seen="stale"), ("skip", None))
            finally:
                con.close()

    def test_two_interleaved_callers_close_one_pane(self):
        self.stop("zz-coder")
        a_seen = self.row()["stop_attempt_at"]  # both read the row before either claims
        results = []
        with self.env.load_factory() as f:
            closes = []
            real = f.herdr

            def herdr(*args, **kw):
                if args[:2] == ("pane", "close"):
                    closes.append(args)
                    if len(closes) == 1:  # the second caller runs while the first is mid-close
                        con2 = f.db()
                        try:
                            results.append(f.force_stop(con2, "zz-coder", "now", attempt_seen=a_seen))
                        finally:
                            con2.close()
                return real(*args, **kw)
            con = f.db()
            try:
                with mock.patch.object(f, "herdr", herdr), mock.patch.object(f, "_scan_claude", return_value={}):
                    results.append(f.force_stop(con, "zz-coder", "now", attempt_seen=a_seen))
            finally:
                con.close()
        self.assertEqual(len(closes), 1)
        self.assertEqual(sorted(r[0] for r in results), ["skip", "stopped"])

    def test_cmd_stop_second_caller_does_not_reset_claim(self):
        # the other caller sets the flag and claims the attempt between our read and our flag UPDATE
        claim = ago(1)
        with self.env.load_factory() as f:
            real_cfg = f.cfg

            def cfg(con, key):
                con.execute("UPDATE agents SET stop_requested_at=?, stop_attempt_at=? WHERE name='zz-coder'", (ago(2), claim))
                return real_cfg(con, key)
            os.environ["FACTORY_AGENT"], os.environ["FACTORY_ROLE"] = "zz-director", "director"
            out = io.StringIO()
            try:
                with mock.patch.object(f, "cfg", cfg), mock.patch("sys.stdout", out):
                    f.cmd_stop(argparse.Namespace(agent="zz-coder", now=True, dry_run=False, reason=None))
            finally:
                os.environ.pop("FACTORY_AGENT", None)
                os.environ.pop("FACTORY_ROLE", None)
        self.assertIn("another stop is already in progress; nothing was closed", out.getvalue())
        self.assertEqual(self.row()["stop_attempt_at"], claim)
        self.assertNotIn(("pane", "close"), self.herdr_verbs())


if __name__ == "__main__":
    unittest.main()
