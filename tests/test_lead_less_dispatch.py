"""T-0470 (G-018 E1): lead-less dispatch: the tick spawns workers for ready tasks of parked teams (docs/lead-less-dispatch.md)."""
import datetime as dt
import json
import unittest

from harness import FactoryEnv, FactoryTestCase

TEAM, LEAD = "g-001-a", "lead-g-001-a"
PARKED = "2026-01-01T00:00:00+00:00"
NOW = "2026-01-01T13:00:00+00:00"


class Base(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(TEAM, lead=LEAD)
        e.seed_agent(LEAD, "lead", goal="G-001", team=TEAM, status="exited")
        e.sql("UPDATE teams SET parked_at=? WHERE id=?", (PARKED, TEAM))
        self.ok("config", "lead_parking", "1")

    def task(self, tid, **kw):
        kw.setdefault("status", "todo")
        kw.setdefault("team", TEAM)
        kw.setdefault("pr_url", None)
        self.env.seed_task(tid, **kw)

    def mode(self, v="1", **cfg):
        self.ok("config", "lead_less_dispatch", v)
        for k, x in cfg.items():
            self.ok("config", k, str(x))

    def dispatch(self, spawn_status="live", gate_open=True, fail=(), shadow_ok=False, at=NOW, patch=None):
        """Run the real dispatch_tick with spawn()/gate() stubbed at the process boundary; returns the spawn calls."""
        calls = []
        with self.env.load_factory() as f:
            con = f.db()

            def fake_spawn(con, role, goal=None, team=None, task=None, name=None, prompt=None, **kw):
                calls.append({"role": role, "goal": goal, "team": team, "task": task, "name": name, "prompt": prompt})
                if task in fail:
                    raise SystemExit(2)
                con.execute("INSERT INTO agents(name,role,goal_id,team_id,task_id,status,started) VALUES(?,?,?,?,?,?,'t')",
                            (name, role, goal, team, task, spawn_status))

            f.spawn = fake_spawn
            f.gate = lambda con, **k: (gate_open, "" if gate_open else "closed", {})
            f.os.environ["FACTORY_NOW"] = at
            for k, v in (patch or {}).items():
                setattr(f, k, v)
            try:
                f.dispatch_tick(con)
            finally:
                del f.os.environ["FACTORY_NOW"]
            con.commit()
        return calls

    def candidates(self):
        with self.env.load_factory() as f:
            return [t["id"] for t in f.dispatch_candidates(f.db())]

    def events(self, action):
        return self.env.sql("SELECT entity, entity_id, detail FROM events WHERE action=? ORDER BY id", (action,))


class SelectionTests(Base):
    def test_ready_coder_selected(self):
        self.task("T-0001")
        self.assertEqual(self.candidates(), ["T-0001"])

    def test_deps_not_done(self):
        self.task("T-0001", status="in_progress")
        self.task("T-0002")
        self.env.sql("UPDATE tasks SET depends_on='[\"T-0001\"]' WHERE id='T-0002'")
        self.assertEqual(self.candidates(), [])
        self.env.sql("UPDATE tasks SET status='done' WHERE id='T-0001'")
        self.assertEqual(self.candidates(), ["T-0002"])

    def test_coder_without_acceptance(self):
        self.task("T-0001", acceptance="  ")
        self.assertEqual(self.candidates(), [])

    def test_pm_without_acceptance_ok_but_not_without_spec(self):
        self.task("T-0001", role="pm", acceptance=None)
        self.task("T-0002", role="pm", acceptance=None, spec="")
        self.assertEqual(self.candidates(), ["T-0001"])

    def test_judge_never(self):
        self.task("T-0001", role="judge")
        self.assertEqual(self.candidates(), [])

    def test_team_not_parked(self):
        self.task("T-0001")
        self.env.sql("UPDATE teams SET parked_at=NULL")
        self.assertEqual(self.candidates(), [])

    def test_lead_waking(self):
        self.task("T-0001")
        self.env.sql("UPDATE agents SET status='queued' WHERE name=?", (LEAD,))
        self.assertEqual(self.candidates(), [])

    def test_goal_not_active(self):
        self.task("T-0001")
        for st in ("planning", "blocked"):
            self.env.sql("UPDATE goals SET status=?", (st,))
            self.assertEqual(self.candidates(), [], st)

    def test_task_with_live_agent(self):
        self.task("T-0001")
        self.env.seed_agent("coder-t-0001", "coder", goal="G-001", team=TEAM, task="T-0001", status="queued")
        self.assertEqual(self.candidates(), [])

    def test_order_goal_priority_first(self):
        self.env.seed_goal("G-002")
        self.env.sql("UPDATE goals SET priority=1 WHERE id='G-002'")
        self.env.sql("UPDATE goals SET priority=2 WHERE id='G-001'")
        self.env.seed_team("g-002-a", goal="G-002", lead="lead-g-002-a")
        self.env.seed_agent("lead-g-002-a", "lead", goal="G-002", team="g-002-a", status="exited")
        self.env.sql("UPDATE teams SET parked_at=?", (PARKED,))
        self.task("T-0001")
        self.task("T-0002", goal="G-002", team="g-002-a")
        self.mode("1", dispatch_max_per_tick=1)
        calls = self.dispatch()
        self.assertEqual([c["task"] for c in calls], ["T-0002"])


class SpawnTests(Base):
    def test_cap_per_tick_and_call_shape(self):
        for i in (1, 2, 3):
            self.task(f"T-000{i}")
        self.mode("1", dispatch_max_per_tick=2, dispatch_max_team_workers=5)
        calls = self.dispatch()
        self.assertEqual([c["name"] for c in calls], ["coder-t-0001", "coder-t-0002"])
        self.assertEqual((calls[0]["role"], calls[0]["goal"], calls[0]["team"], calls[0]["task"]),
                         ("coder", "G-001", TEAM, "T-0001"))
        self.assertIn("Never call the advisor tool", calls[0]["prompt"])

    def test_team_worker_cap(self):
        self.env.seed_goal("G-002")
        self.env.seed_team("g-002-a", goal="G-002", lead="lead-g-002-a")
        self.env.seed_agent("lead-g-002-a", "lead", goal="G-002", team="g-002-a", status="exited")
        self.env.sql("UPDATE teams SET parked_at=?", (PARKED,))
        for n in ("a", "b"):
            self.env.seed_agent(f"coder-{n}", "coder", goal="G-001", team=TEAM)
        self.task("T-0001")
        self.task("T-0002", goal="G-002", team="g-002-a")
        self.mode("1")
        self.assertEqual([c["task"] for c in self.dispatch()], ["T-0002"])

    def test_team_cap_within_one_tick(self):
        for i in (1, 2, 3):
            self.task(f"T-000{i}")
        self.mode("1", dispatch_max_per_tick=5, dispatch_max_team_workers=2)
        self.assertEqual(len(self.dispatch()), 2)

    def test_closed_gate_spawns_nothing(self):
        self.task("T-0001")
        self.mode("1")
        before = self.env.sql("SELECT count(*) c FROM agents")[0]["c"]
        self.assertEqual(self.dispatch(gate_open=False), [])
        self.assertEqual(self.env.sql("SELECT count(*) c FROM agents")[0]["c"], before)

    def test_queued_result_stops_loop(self):
        for i in (1, 2):
            self.task(f"T-000{i}")
        self.mode("1")
        self.assertEqual(len(self.dispatch(spawn_status="queued")), 1)
        self.assertEqual(json.loads(self.events("dispatch")[0]["detail"])["status"], "queued")

    def test_second_run_spawns_nothing(self):
        self.task("T-0001")
        self.mode("1")
        self.assertEqual(len(self.dispatch()), 1)
        self.assertEqual(self.dispatch(), [])

    def test_spawn_refusal_continues(self):
        for i in (1, 2):
            self.task(f"T-000{i}")
        self.mode("1")
        calls = self.dispatch(fail=("T-0001",))
        self.assertEqual([c["task"] for c in calls], ["T-0001", "T-0002"])
        self.assertEqual([r["entity_id"] for r in self.events("dispatch-error")], ["T-0001"])
        self.assertEqual([r["entity_id"] for r in self.events("dispatch")], ["T-0002"])

    def at(self, minutes):
        return (dt.datetime.fromisoformat(NOW) + dt.timedelta(minutes=minutes)).isoformat()

    def test_dispatch_error_throttled_per_task_per_hour(self):
        self.task("T-0001")
        self.mode("1")
        for m in range(60):
            calls = self.dispatch(fail=("T-0001",), at=self.at(m))
            self.assertEqual(len(calls), 1)  # still retried every tick
        self.assertEqual([r["entity_id"] for r in self.events("dispatch-error")], ["T-0001"])
        self.dispatch(fail=("T-0001",), at=self.at(61))
        self.assertEqual([r["entity_id"] for r in self.events("dispatch-error")], ["T-0001", "T-0001"])

    def test_dispatch_error_throttle_key_is_per_task(self):
        for i in (1, 2):
            self.task(f"T-000{i}")
        self.mode("1")
        for m in (0, 1, 30):
            self.dispatch(fail=("T-0001", "T-0002"), at=self.at(m))
        self.assertEqual(sorted(r["entity_id"] for r in self.events("dispatch-error")), ["T-0001", "T-0002"])
        self.dispatch(fail=("T-0001", "T-0002"), at=self.at(61))
        self.assertEqual(len(self.events("dispatch-error")), 4)

    def test_bad_integer_falls_back(self):
        for i in (1, 2, 3):
            self.task(f"T-000{i}")
        self.mode("1", dispatch_max_per_tick="x", dispatch_max_team_workers="y")
        self.assertEqual(len(self.dispatch()), 2)


class MailTests(Base):
    def test_fyi_mail_after_dispatch(self):
        self.task("T-0001")
        self.mode("1")
        self.dispatch()
        rows = self.env.sql("SELECT recipient, kind, sender, subject, task_id FROM messages")
        self.assertEqual([tuple(r) for r in rows], [(LEAD, "fyi", "tick", "dispatch: coder-t-0001 started on T-0001", "T-0001")])
        p = self.env.run("inbox", agent=LEAD)
        self.assertIn("[fyi]", p.stdout)

    def test_fyi_not_a_msg_kind(self):
        p = self.env.run("msg", "x", "y", "z", "--kind", "fyi")
        self.assertEqual(p.returncode, 2)

    def wake(self, kind):
        self.env.sql("INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES('x',?,?,'s','b','t')", (LEAD, kind))
        self.env.seed_agent("zz-director", "director")
        self.ok("tick")
        return self.env.sql("SELECT detail FROM events WHERE action='lead-wake'")

    def test_fyi_does_not_wake(self):
        self.assertEqual(self.wake("fyi"), [])

    def test_report_wakes(self):
        rows = self.wake("report")
        self.assertEqual(len(rows), 1)
        self.assertRegex(json.loads(rows[0]["detail"])["trigger"], r"^M\d+$")


class OffAndShadowTests(Base):
    def test_off_values(self):
        self.task("T-0001")
        for v in ("0", "yes"):
            self.mode(v)
            self.assertEqual(self.dispatch(), [])
        self.assertEqual(self.events("dispatch") + self.events("dispatch-shadow"), [])

    def test_default_off_and_res_unchanged(self):
        p = self.ok("config")
        self.assertRegex(p.stdout, r"lead_less_dispatch\s*=?\s*0")
        self.assertNotIn("lead-less dispatch", self.ok("res").stdout)

    def test_shadow(self):
        self.task("T-0001")
        self.mode("shadow")
        self.assertEqual(self.dispatch(), [])
        self.assertEqual(self.dispatch(), [])
        ev = self.events("dispatch-shadow")
        self.assertEqual(len(ev), 1)
        self.assertEqual(json.loads(ev[0]["detail"]), {"role": "coder", "team": TEAM, "name": "coder-t-0001"})
        self.assertEqual(self.env.sql("SELECT count(*) c FROM messages")[0]["c"], 0)
        self.assertIn("lead-less dispatch: shadow", self.ok("res").stdout)


    def test_shadow_reaches_tasks_behind_throttled_ones(self):
        for i in (1, 2, 3):
            self.task(f"T-000{i}")
        self.mode("shadow", dispatch_max_per_tick=2)
        self.dispatch()
        self.dispatch()
        self.assertEqual(sorted(r["entity_id"] for r in self.events("dispatch-shadow")), ["T-0001", "T-0002", "T-0003"])


class RealTickTests(Base):
    def test_tick_spawns_for_real_and_mails_lead(self):
        self.env.seed_agent("zz-director", "director")
        self.task("T-0001")
        self.mode("1")
        self.ok("tick")
        a = self.env.sql("SELECT name, role, team_id, task_id FROM agents WHERE task_id='T-0001'")
        self.assertEqual([tuple(r) for r in a], [("coder-t-0001", "coder", TEAM, "T-0001")])
        self.assertEqual(self.env.sql("SELECT kind FROM messages WHERE sender='tick' AND kind='fyi'")[0]["kind"], "fyi")
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name=?", (LEAD,))[0]["status"], "exited")  # no wake
        self.ok("tick")
        self.assertEqual(self.env.sql("SELECT count(*) c FROM agents WHERE task_id='T-0001'")[0]["c"], 1)

    def test_candidates_raising_is_logged_and_tick_completes(self):
        self.env.seed_agent("zz-director", "director")
        self.task("T-0001")
        self.env.sql("UPDATE tasks SET depends_on='not json' WHERE id='T-0001'")
        self.mode("1")
        self.ok("tick")
        ev = self.env.sql("SELECT entity, entity_id FROM events WHERE action='dispatch-error'")
        self.assertEqual([tuple(r) for r in ev], [("tick", "dispatch")])


def _later(minutes):
    h, m = divmod(13 * 60 + minutes, 60)
    return f"2026-01-01T{h:02d}:{m:02d}:00+00:00"


class SpecFillTests(Base):
    def unspec(self, *ids, **kw):
        for i in ids:
            self.task(i, spec="", acceptance=None, **kw)

    def pm_tasks(self):
        return self.env.sql("SELECT id, team_id, title, spec, acceptance, depends_on FROM tasks WHERE role='pm' ORDER BY id")

    def filed(self, n=1, **kw):
        self.mode("1")
        self.dispatch(**kw)
        self.assertEqual(len(self.pm_tasks()), n)
        return self.pm_tasks()

    def test_files_pm_task(self):
        self.unspec("T-0001", "T-0002")
        (r,) = self.filed()
        self.assertEqual((r["team_id"], r["title"], r["depends_on"]), (TEAM, "Spec ready queue G-001: T-0001, T-0002", "[]"))
        ev = self.env.sql("SELECT actor, detail FROM events WHERE action='add' AND entity_id=?", (r["id"],))
        self.assertEqual([e["actor"] for e in ev], ["tick"])
        g = self.events("spec-fill")
        self.assertEqual((g[0]["entity"], g[0]["entity_id"]), ("goal", "G-001"))
        self.assertEqual(json.loads(g[0]["detail"]), {"task": r["id"], "tasks": ["T-0001", "T-0002"]})

    def test_ready_specced_task_blocks(self):
        self.unspec("T-0001")
        self.task("T-0002")
        self.filed(0)

    def test_lead_live_blocks(self):
        self.unspec("T-0001")
        self.env.sql("UPDATE agents SET status='live' WHERE name=?", (LEAD,))
        self.filed(0)

    def test_lead_queued_blocks(self):
        self.unspec("T-0001")
        self.env.sql("UPDATE agents SET status='queued' WHERE name=?", (LEAD,))
        self.filed(0)

    def test_team_not_parked_blocks(self):
        self.unspec("T-0001")
        self.env.sql("UPDATE teams SET parked_at=NULL")
        self.filed(0)

    def test_goal_planning_blocks(self):
        self.unspec("T-0001")
        self.env.sql("UPDATE goals SET status='planning'")
        self.filed(0)

    def test_open_pm_task_blocks(self):
        self.unspec("T-0001")
        self.task("T-0009", role="pm", spec="")
        self.filed(1)  # only the seeded one

    def test_pm_agent_blocks(self):
        self.unspec("T-0001")
        self.env.seed_agent("pm-x", "pm", goal="G-001", team=TEAM)
        self.filed(0)

    def test_spec_without_acceptance_counts(self):
        self.task("T-0001", spec="has spec", acceptance="")
        (r,) = self.filed()
        self.assertIn("T-0001", r["title"])

    def test_nothing_unspecced_no_task(self):
        self.task("T-0001", status="in_progress")
        self.filed(0)

    def test_max_five_ready_first(self):
        self.unspec(*[f"T-000{i}" for i in range(1, 8)])
        self.env.sql("UPDATE tasks SET depends_on='[\"T-0099\"]' WHERE id IN ('T-0001','T-0002')")
        (r,) = self.filed()
        self.assertEqual(r["title"], "Spec ready queue G-001: T-0003, T-0004, T-0005, T-0006, T-0007")

    def test_same_tick_dispatch_and_mail(self):
        self.unspec("T-0001", "T-0002")
        self.mode("1")
        calls = self.dispatch()
        (r,) = self.pm_tasks()
        self.assertEqual([(c["role"], c["name"], c["task"]) for c in calls], [("pm", f"pm-{r['id'].lower()}", r["id"])])
        m = self.env.sql("SELECT recipient, kind, sender, subject FROM messages")
        self.assertEqual([tuple(x) for x in m],
                         [(LEAD, "fyi", "tick", f"spec-fill: {r['id']} filed for T-0001, T-0002"),
                          (LEAD, "fyi", "tick", f"dispatch: pm-{r['id'].lower()} started on {r['id']}")])

    def test_no_spam_and_throttle(self):
        self.unspec("T-0001", "T-0002")
        self.mode("1")
        for _ in range(3):
            self.dispatch()
        self.assertEqual(len(self.pm_tasks()), 1)
        self.env.sql("UPDATE tasks SET status='done' WHERE role='pm'")
        self.env.sql("UPDATE agents SET status='exited' WHERE role='pm'")
        self.dispatch(at=_later(30))
        self.assertEqual(len(self.pm_tasks()), 1)
        self.dispatch(at=_later(61))
        self.assertEqual(len(self.pm_tasks()), 2)

    def test_error_rolls_back(self):
        self.unspec("T-0001")
        self.mode("1")

        def boom(*a, **k):
            raise RuntimeError("boom")

        with self.env.load_factory() as f:
            con = f.db()
            f.send = boom
            f.os.environ["FACTORY_NOW"] = NOW
            try:
                with self.assertRaises(RuntimeError):
                    f.spec_fill_tick(con)
            finally:
                del f.os.environ["FACTORY_NOW"]
            con.commit()
        self.assertEqual(self.pm_tasks(), [])
        self.assertEqual(self.events("spec-fill") + self.events("add"), [])

    def test_team_without_lead_agent_no_task(self):
        self.unspec("T-0001")
        self.env.sql("UPDATE teams SET lead_agent=NULL")
        self.filed(0)

    def test_send_exit_rolls_back(self):
        self.unspec("T-0001")
        self.mode("1")
        with self.env.load_factory() as f:
            con = f.db()
            f.send = lambda *a, **k: f.die("no lead")
            f.os.environ["FACTORY_NOW"] = NOW
            try:
                with self.assertRaises(SystemExit):
                    f.spec_fill_tick(con)
            finally:
                del f.os.environ["FACTORY_NOW"]
            con.commit()
        self.assertEqual(self.pm_tasks(), [])
        self.assertEqual(self.events("add"), [])

    def test_next_id_error_logged_tick_completes(self):
        self.unspec("T-0001")
        self.env.seed_agent("zz-director", "director")
        self.mode("1")
        with self.env.load_factory() as f:
            con = f.db()
            f.next_id = lambda *a, **k: (_ for _ in ()).throw(RuntimeError("no id"))
            f.os.environ["FACTORY_NOW"] = NOW
            try:
                try:
                    f.dispatch_tick(con)
                except Exception as e:  # the real tick catches this in cmd_tick
                    f.event(con, "tick", "dispatch", "dispatch-error", str(e))
            finally:
                del f.os.environ["FACTORY_NOW"]
            con.commit()
        self.assertEqual(self.pm_tasks(), [])
        self.assertEqual(len(self.events("dispatch-error")), 1)

    def test_real_tick_files_and_spawns_pm(self):
        self.unspec("T-0001")
        self.env.seed_agent("zz-director", "director")
        self.mode("1")
        self.ok("tick")
        (r,) = self.pm_tasks()
        self.assertEqual(self.env.sql("SELECT name FROM agents WHERE task_id=?", (r["id"],))[0]["name"], f"pm-{r['id'].lower()}")

    def test_mode_off(self):
        self.unspec("T-0001")
        self.mode("0")
        self.dispatch()
        self.assertEqual(self.pm_tasks(), [])
        self.assertEqual(self.events("spec-fill") + self.events("spec-fill-shadow"), [])

    def test_shadow(self):
        self.unspec("T-0001")
        self.mode("shadow")
        self.dispatch()
        self.dispatch()
        self.assertEqual(self.pm_tasks(), [])
        self.assertEqual(self.env.sql("SELECT count(*) c FROM messages")[0]["c"], 0)
        ev = self.events("spec-fill-shadow")
        self.assertEqual(len(ev), 1)
        self.assertEqual((ev[0]["entity"], json.loads(ev[0]["detail"])), ("goal", {"team": TEAM, "tasks": ["T-0001"]}))
        self.dispatch(at=_later(61))
        self.assertEqual(len(self.events("spec-fill-shadow")), 2)

    def test_generated_text(self):
        self.unspec("T-0001")
        (r,) = self.filed()
        for sub in ("Never call the advisor tool.", "pm-skills:acceptance-template", "T-0001", f"lead {LEAD} of {TEAM} is parked"):
            self.assertIn(sub, r["spec"])
        self.assertTrue(r["acceptance"].startswith("1. Each listed task"))


if __name__ == "__main__":
    unittest.main()
