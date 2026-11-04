"""T-0972: the tick closes the live pane of a worker agents row that is 'exited' for more than 10 min, per config
exited_pane_reap_mode, and never touches anything else. Real tick as a subprocess, temp FACTORY_HOME, herdr stub."""
import json
import unittest

from harness import FactoryTestCase

OLD = "2026-01-01T00:00:00+00:00"      # long past
FUTURE = "2099-01-01T00:00:00+00:00"   # age is negative: "exited under 10 min ago" without reading the clock


class Base(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal("G-9")
        self.env.seed_task("T-9", "G-9", status="todo", pr_url=None)

    def agent(self, name, role="qa", status="exited", pane="p-1", ended=OLD):
        self.env.seed_agent(name, role, "G-9", None, "T-9", status=status, pane_id=pane)
        self.env.sql("UPDATE agents SET spawn_args='{}', ended=?, session_id=?, cwd=? WHERE name=?",
                     (ended, "s-" + name, "/w/" + name, name))

    def host(self, *pairs):
        """The host lists these (name, pane id) agents and panes."""
        self.env.set_herdr(
            workspaces=[{"workspace_id": "w1", "label": "x"}],
            panes=[{"pane_id": p, "tab_id": "t1", "workspace_id": "w1"} for p in dict.fromkeys(p for _, p in pairs)],
            agents=[{"name": n, "agent": "claude", "pane_id": p, "agent_status": "idle",
                     "agent_session": {"value": "s-" + n}} for n, p in pairs])

    def tick(self, mode="on", clear=True):
        if mode:
            self.env.run("config", "exited_pane_reap_mode", mode)
        if clear:
            self.env.sql("DELETE FROM throttle")
        p = self.env.run("tick")
        self.assertEqual(p.returncode, 0, p.stderr)

    def events(self, action):
        return [dict(r) for r in self.env.sql("SELECT entity_id, detail FROM events WHERE action=?", (action,))]

    def closes(self):
        return [c for c in self.env.calls("herdr") if c["argv"][:2] == ["pane", "close"]]

    def panes(self):
        return [p["pane_id"] for p in json.loads(self.env.herdr_state.read_text())["panes"]]


class Reap(Base):
    def test_exited_worker_pane_past_10_min_is_closed_with_one_event(self):
        self.agent("qa-t-9")
        self.host(("qa-t-9", "p-1"))
        self.tick()
        self.assertEqual(self.panes(), [])
        self.assertEqual(len(self.closes()), 1)
        ev = self.events("exited-pane-closed")
        self.assertEqual([e["entity_id"] for e in ev], ["qa-t-9"])
        self.assertEqual(json.loads(ev[0]["detail"])["pane"], "p-1")

    def test_exited_under_10_min_is_not_closed(self):
        self.agent("qa-t-9", ended=FUTURE)
        self.host(("qa-t-9", "p-1"))
        self.tick()
        self.assertEqual(self.panes(), ["p-1"])
        self.assertEqual(self.closes(), [])
        self.assertEqual(self.events("exited-pane-closed"), [])

    def test_exited_without_ended_time_is_not_closed(self):
        self.agent("qa-t-9", ended=None)
        self.host(("qa-t-9", "p-1"))
        self.tick()
        self.assertEqual(self.panes(), ["p-1"])

    def test_dry_run_default_logs_would_close_once_and_closes_nothing(self):
        self.agent("qa-t-9")
        self.host(("qa-t-9", "p-1"))
        self.tick(mode=None)
        self.tick(mode=None)
        self.assertEqual(self.panes(), ["p-1"])
        self.assertEqual(self.closes(), [])
        self.assertEqual(len(self.events("exited-pane-would-close")), 1)

    def test_off_does_nothing(self):
        self.agent("qa-t-9")
        self.host(("qa-t-9", "p-1"))
        self.tick(mode="off")
        self.assertEqual(self.panes(), ["p-1"])
        self.assertEqual(self.events("exited-pane-would-close"), [])

    def test_throttled_second_run_does_nothing(self):
        self.agent("qa-t-9")
        self.host(("qa-t-9", "p-1"))
        self.env.run("config", "exited_pane_reap_mode", "dry-run")
        self.tick(mode=None)
        self.env.sql("DELETE FROM events WHERE action='exited-pane-would-close'")
        self.tick(mode=None, clear=False)
        self.assertEqual(self.events("exited-pane-would-close"), [])


class Never(Base):
    def assertKept(self, *pane_ids):
        self.assertEqual(self.closes(), [])
        self.assertEqual(sorted(self.panes()), sorted(pane_ids))
        self.assertEqual(self.events("exited-pane-closed"), [])

    def test_pane_without_agents_row_is_kept(self):
        self.host(("owner-shell", "p-1"))
        self.tick()
        self.assertKept("p-1")

    def test_exited_row_with_other_pane_id_is_kept(self):
        """Kept when a live row holds the HOST's pane id (the old name stays; a bare id mismatch no longer keeps it)."""
        self.agent("qa-t-9", pane="p-old")
        self.agent("qa-new", status="live", pane="p-1", ended=None)
        self.host(("qa-t-9", "p-1"))
        self.tick()
        self.assertKept("p-1")

    def test_exited_row_with_stale_pane_id_closes_the_host_pane_not_the_rows(self):
        self.agent("qa-t-9", pane="p-old")
        self.host(("qa-t-9", "p-1"), ("owner-shell", "p-old"))
        self.tick()
        self.assertEqual([c["argv"] for c in self.closes()], [["pane", "close", "p-1"]])
        self.assertEqual(self.panes(), ["p-old"])
        self.assertEqual(json.loads(self.events("exited-pane-closed")[0]["detail"])["pane"], "p-1")

    def test_host_pane_running_another_session_is_kept_in_both_modes(self):
        """Exited qa-old; the host lists qa-old at p-1 but the pane runs qa-new's session: never closed."""
        self.agent("qa-old", pane="p-old")
        self.agent("qa-new", status="live", pane="p-stale", ended=None)
        self.env.set_herdr(
            workspaces=[{"workspace_id": "w1", "label": "x"}],
            panes=[{"pane_id": "p-1", "tab_id": "t1", "workspace_id": "w1"}],
            agents=[{"name": "qa-old", "agent": "claude", "pane_id": "p-1", "agent_status": "idle",
                     "agent_session": {"value": "s-qa-new"}}])
        self.tick()
        self.assertKept("p-1")
        self.tick(mode="dry-run")
        self.assertEqual(self.events("exited-pane-would-close"), [])
        self.assertEqual(self.closes(), [])

    def _no_session_host(self, status="idle", cwd="/w/qa-old"):
        self.env.set_herdr(
            workspaces=[{"workspace_id": "w1", "label": "x"}],
            panes=[{"pane_id": "p-1", "tab_id": "t1", "workspace_id": "w1", "cwd": cwd}],
            agents=[{"name": "qa-old", "agent": "claude", "pane_id": "p-1", "agent_status": status}])

    def _kept_both_modes(self):
        self.tick()
        self.assertKept("p-1")
        self.tick(mode="dry-run")
        self.assertEqual(self.events("exited-pane-would-close"), [])
        self.assertEqual(self.closes(), [])

    def test_no_host_session_with_unlisted_live_row_is_kept_in_both_modes(self):
        """Idle pane in another agent's cwd (live qa-new, stale row pane): not this row's leftover, kept."""
        self.agent("qa-old", pane="p-old")
        self.agent("qa-new", status="live", pane="p-stale", ended=None)
        self._no_session_host(cwd="/w/qa-new")
        self._kept_both_modes()

    def test_no_host_session_with_old_lost_unlisted_row_would_close_in_own_cwd(self):
        """Old lost rows exist (live reality): they no longer stop the reaper for an idle pane in the row's own cwd."""
        self.agent("qa-old", pane="p-old")
        self.agent("qa-lost", status="lost", pane="p-stale", ended=OLD)
        self._no_session_host()
        self.tick(mode="dry-run")
        self.assertEqual([e["entity_id"] for e in self.events("exited-pane-would-close")], ["qa-old"])
        self.assertEqual(self.closes(), [])
        self.tick()
        self.assertEqual([c["argv"] for c in self.closes()], [["pane", "close", "p-1"]])

    def test_no_host_session_working_pane_is_kept_in_both_modes(self):
        """The host calls the pane 'working' (mislabelled or not): never closed, even in the row's own cwd."""
        self.agent("qa-old", pane="p-old")
        self._no_session_host(status="working")
        self._kept_both_modes()

    def test_no_host_session_deleted_cwd_form_would_close(self):
        self.agent("qa-old", pane="p-old")
        self._no_session_host(status="done", cwd="/w/qa-old (deleted)")
        self.tick(mode="dry-run")
        self.assertEqual([e["entity_id"] for e in self.events("exited-pane-would-close")], ["qa-old"])

    def test_no_host_session_row_without_cwd_is_kept_in_both_modes(self):
        self.agent("qa-old", pane="p-old")
        self.env.sql("UPDATE agents SET cwd=NULL WHERE name='qa-old'")
        self._no_session_host()
        self._kept_both_modes()

    def test_no_host_session_with_every_live_row_listed_still_closes(self):
        self.agent("qa-old", pane="p-old")
        self._no_session_host()
        self.tick()
        self.assertEqual([c["argv"] for c in self.closes()], [["pane", "close", "p-1"]])

    def test_stale_pane_id_dry_run_logs_would_close_with_the_host_pane(self):
        self.agent("qa-t-9", pane="p-old")
        self.host(("qa-t-9", "p-1"))
        self.tick(mode=None)
        self.assertEqual(self.closes(), [])
        self.assertEqual(self.panes(), ["p-1"])
        ev = self.events("exited-pane-would-close")
        self.assertEqual(len(ev), 1)
        self.assertEqual(json.loads(ev[0]["detail"])["pane"], "p-1")

    def test_stale_pane_id_under_10_min_is_kept(self):
        self.agent("qa-t-9", pane="p-old", ended=FUTURE)
        self.host(("qa-t-9", "p-1"))
        self.tick()
        self.assertKept("p-1")

    def test_hq_rows_are_kept(self):
        for i, role in enumerate(("boss", "director", "lead", "staff-engineer")):
            self.agent(f"claude-{role}", role=role, pane=f"p-{i}")
        self.host(*[(f"claude-{r}", f"p-{i}") for i, r in enumerate(("boss", "director", "lead", "staff-engineer"))])
        self.tick()
        self.assertKept("p-0", "p-1", "p-2", "p-3")

    def test_live_starting_queued_rows_are_kept(self):
        for i, st in enumerate(("live", "starting", "queued", "stopped", "done", "lost")):
            self.agent(f"qa-{st}", status=st, pane=f"p-{i}")
        # a queued row the tick must not try to spawn: claimed far in the future
        self.env.sql("UPDATE agents SET spawn_args=? WHERE name IN ('qa-queued','qa-starting')", (json.dumps({"role": "qa", "claimed_at": 4e9}),))
        names = [(f"qa-{st}", f"p-{i}") for i, st in enumerate(("live", "starting", "queued", "stopped", "done", "lost"))]
        self.host(*names)
        self.tick()
        # 'done' is reaped by the tick's own loop (pre-existing); the rest are untouched
        self.assertEqual(self.events("exited-pane-closed"), [])
        self.assertEqual(sorted(p for p in self.panes()), sorted(p for n, p in names if n != "qa-done"))

    def test_pane_held_by_a_newer_live_row_is_kept(self):
        self.agent("qa-old")
        self.agent("qa-new", status="live", pane="p-1", ended=None)
        self.host(("qa-old", "p-1"), ("qa-new", "p-1"))
        self.tick()
        self.assertKept("p-1")

    def test_reused_name_now_live_is_kept(self):
        self.agent("qa-t-9", status="live", pane="p-2", ended=None)
        self.host(("qa-t-9", "p-1"))
        self.tick()
        self.assertEqual(self.closes(), [])
        self.assertEqual(self.events("exited-pane-closed"), [])


class Failure(Base):
    def test_failed_close_is_one_event_retried_without_spam_and_tick_survives(self):
        self.agent("qa-t-9")
        self.host(("qa-t-9", "p-1"))
        self.env.set_herdr(pane_close_fail=True)
        self.tick()
        self.tick()
        self.assertEqual(len(self.events("exited-pane-close-failed")), 1)
        self.assertEqual(self.events("exited-pane-closed"), [])
        self.assertEqual(len(self.closes()), 2)  # retried on the second run
        self.env.set_herdr(pane_close_fail=False)
        self.tick()
        self.assertEqual(self.panes(), [])
        self.assertEqual(len(self.events("exited-pane-closed")), 1)
        self.assertEqual(len(self.events("exited-pane-close-failed")), 1)


class StaleRelease(Base):
    """T-0978: release_stale_pane keeps pane_id when the close is not confirmed; name reuse never overwrites it."""

    def row(self, name="qa-t-9"):
        return dict(self.env.sql("SELECT status, pane_id, ended FROM agents WHERE name=?", (name,))[0])

    def spawn(self, name="qa-t-9"):
        return self.env.run("spawn", "qa", "--name", name, "--goal", "G-9", "--task", "T-9", "--cwd", str(self.env.home))

    def test_close_confirmed_releases_row_and_logs_reaped(self):
        self.agent("qa-t-9", status="done")
        self.host(("qa-t-9", "p-1"))
        p = self.spawn()
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(len(self.closes()), 1)
        self.assertEqual([e["entity_id"] for e in self.events("reaped")], ["qa-t-9"])
        self.assertEqual(self.events("stale-pane-close-failed"), [])

    def test_confirmed_close_then_spawn_failure_leaves_row_exited_with_pane_id_null(self):
        self.agent("qa-t-9", status="done")
        self.host(("qa-t-9", "p-1"))
        self.env.set_herdr(fail_cmds=["tab create", "pane split"])  # the spawn dies after the release
        p = self.spawn()
        self.assertNotEqual(p.returncode, 0)
        self.assertEqual(len(self.closes()), 1)
        r = self.row()
        self.assertEqual((r["status"], r["pane_id"]), ("exited", None))
        self.assertEqual([e["entity_id"] for e in self.events("reaped")], ["qa-t-9"])

    def assertNotReleased(self, p):
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("p-1", p.stderr)
        r = self.row()
        self.assertEqual((r["status"], r["pane_id"]), ("exited", "p-1"))
        self.assertEqual(len(self.closes()), 1)
        ev = self.events("stale-pane-close-failed")
        self.assertEqual(len(ev), 1)
        self.assertEqual(json.loads(ev[0]["detail"]), {"agent": "qa-t-9", "pane": "p-1"})
        self.assertEqual(self.events("reaped"), [])

    def test_close_exit_0_but_pane_still_listed_is_not_released(self):
        self.agent("qa-t-9", status="done", ended=None)
        self.host(("qa-t-9", "p-1"))
        self.env.set_herdr(pane_close_noop=True)  # pane_gone is False
        self.assertNotReleased(self.spawn())
        self.assertEqual(self.panes(), ["p-1"])

    def test_close_exit_0_but_host_unreachable_is_not_released(self):
        self.agent("qa-t-9", status="done", ended=None)
        self.host(("qa-t-9", "p-1"))
        self.env.set_herdr(fail_cmds=["pane list", "workspace list"])  # pane_gone is None
        self.assertNotReleased(self.spawn())

    def test_failed_close_keeps_pane_id_logs_once_and_spawn_dies_naming_pane(self):
        self.agent("qa-t-9", status="done", ended=None)
        self.host(("qa-t-9", "p-1"))
        self.env.set_herdr(pane_close_fail=True)
        p = self.spawn()
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("p-1", p.stderr)
        r = self.row()
        self.assertEqual((r["status"], r["pane_id"]), ("exited", "p-1"))
        self.assertIsNotNone(r["ended"])
        ev = self.events("stale-pane-close-failed")
        self.assertEqual(len(ev), 1)
        self.assertEqual(json.loads(ev[0]["detail"]), {"agent": "qa-t-9", "pane": "p-1"})
        self.assertEqual(self.events("reaped"), [])
        self.assertEqual(self.panes(), ["p-1"])

    def test_kept_row_then_matches_the_exited_pane_reaper(self):
        self.agent("qa-t-9", status="done", ended=None)
        self.host(("qa-t-9", "p-1"))
        self.env.set_herdr(pane_close_fail=True)
        self.spawn()
        self.env.set_herdr(pane_close_fail=False)
        self.env.sql("UPDATE agents SET ended=? WHERE name='qa-t-9'", (OLD,))  # past the 10 min age
        self.tick()
        self.assertEqual(self.panes(), [])
        self.assertEqual(len(self.events("exited-pane-closed")), 1)

    def test_exited_row_with_kept_pane_retries_close_and_spawns_on_success(self):
        self.agent("qa-t-9", status="exited")
        self.host(("qa-t-9", "p-1"))
        p = self.spawn()
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(len(self.closes()), 1)
        self.assertEqual([e["entity_id"] for e in self.events("reaped")], ["qa-t-9"])
        self.assertNotEqual(self.row()["pane_id"], "p-1")

    def test_exited_row_with_kept_pane_retry_failing_dies_and_keeps_pane(self):
        self.agent("qa-t-9", status="exited")
        self.host(("qa-t-9", "p-1"))
        self.env.set_herdr(pane_close_fail=True)
        p = self.spawn()
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("p-1", p.stderr)
        self.assertEqual(self.row()["pane_id"], "p-1")
        self.assertEqual(len(self.events("stale-pane-close-failed")), 1)

    def test_exited_row_whose_pane_is_not_listed_under_its_name_is_not_closed(self):
        self.agent("qa-t-9", status="exited")
        self.host(("someone-else", "p-1"))
        p = self.spawn()
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(self.closes(), [])
        self.assertIn("p-1", self.panes())

    def test_gate_closed_spawn_over_a_kept_pane_dies_before_queueing(self):
        self.agent("qa-t-9", status="done", ended=None)
        self.env.sql("UPDATE agents SET spawn_args=? WHERE name='qa-t-9'", ('{"marker": "old"}',))
        self.host(("qa-t-9", "p-1"))
        self.env.set_herdr(pane_close_fail=True)
        self.env.run("config", "max_live_agents", "0")  # gate closed: this spawn would queue
        p = self.spawn()
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("p-1", p.stderr)
        self.assertNotIn("queued spawn is retried", p.stderr)
        r = self.row()
        self.assertEqual((r["status"], r["pane_id"]), ("exited", "p-1"))
        self.assertEqual(self.env.sql("SELECT spawn_args FROM agents WHERE name='qa-t-9'")[0][0], '{"marker": "old"}')

    def test_queued_row_whose_spawn_exits_stays_queued_unclaimed_and_starts_on_a_later_tick(self):
        n = "qa-t-9"
        self.agent(n, status="queued", pane=None, ended=None)
        args = {"role": "qa", "goal": "G-9", "team": None, "task": "T-9", "name": n, "cwd": str(self.env.home),
                "prompt": None, "model": None, "resume": None, "fresh": False, "org": "claude"}
        self.env.sql("UPDATE agents SET spawn_args=? WHERE name=?", (json.dumps(args), n))
        self.host()
        starts = lambda: [c for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]]
        # a real SystemExit from the claimed from-queue spawn: placing the pane fails
        self.env.set_herdr(fail_cmds=["tab create", "pane split"])
        self.env.sql("DELETE FROM throttle")
        t1 = self.env.run("tick")
        self.assertIn("tab create failed", t1.stderr + t1.stdout, (t1.stdout[-500:], t1.stderr[-500:]))
        r = dict(self.env.sql("SELECT status, json_extract(spawn_args,'$.claimed_at') AS c FROM agents WHERE name=?", (n,))[0])
        self.assertEqual(r["status"], "queued")
        self.assertIsNone(r["c"])
        self.assertEqual(starts(), [])
        self.env.set_herdr(fail_cmds=[])  # the cause is fixed: the next tick starts it
        self.env.sql("DELETE FROM throttle")
        t2 = self.env.run("tick")
        self.assertGreaterEqual(len(starts()), 1, (t2.returncode, t2.stderr[-600:], self.row(n)))
        self.assertNotEqual(self.row(n)["status"], "cancelled")

    def test_on_demand_failed_release_claims_nothing_keeps_pane_and_commits(self):
        self.env.seed_agent("claude-director", "director", "G-9")
        self.env.seed_agent("staff-engineer", "staff-engineer", None, None, None, status="done", pane_id="p-1")
        self.env.sql("UPDATE agents SET spawn_args='{}', ended=NULL WHERE name='staff-engineer'")
        self.host(("staff-engineer", "p-1"))
        self.env.set_herdr(pane_close_fail=True)
        p = self.env.run("msg", "staff-engineer", "q", "ctx", "--kind", "request", agent="claude-director")
        r = self.row("staff-engineer")
        self.assertEqual((r["status"], r["pane_id"]), ("exited", "p-1"), p.stderr)
        self.assertEqual(len(self.events("stale-pane-close-failed")), 1)
        # no lock left held: a write from another connection succeeds at once
        self.env.sql("UPDATE agents SET role='staff-engineer' WHERE name='staff-engineer'")
        # the next routed message retries and, once the close works, claims the row
        self.env.set_herdr(pane_close_fail=False)
        self.env.run("msg", "staff-engineer", "q2", "ctx", "--kind", "request", agent="claude-director")
        self.assertNotEqual(self.row("staff-engineer")["pane_id"], "p-1")


if __name__ == "__main__":
    unittest.main()
