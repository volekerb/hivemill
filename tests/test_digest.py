import argparse
import datetime as dt
import unittest
from unittest import mock

from harness import PR_URL, SHA_A, FactoryTestCase


# Every DigestTests digest runs on this frozen clock (12:00 local, far from midnight); seeds are relative to it.
FROZEN = dt.datetime(2026, 10, 24, 12, 0).astimezone()
FROZEN_ENV = {"FACTORY_NOW": FROZEN.isoformat()}


def iso(delta=dt.timedelta(0), base=FROZEN):
    return (base + delta).isoformat(timespec="seconds")


class DigestBase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal(title="zz-test goal")
        e.seed_team(lead="zz-test-lead")
        e.seed_agent("zz-test-director", "director")
        e.seed_agent("zz-test-coder", "coder", goal="G-001", team="g-001-a", task="T-0002")
        e.seed_agent("zz-test-boss", "boss")

    def seed(self, tid, status, title, **cols):
        self.env.seed_task(tid, status=status, pr_url=cols.pop("pr_url", None), team="g-001-a")
        self.env.sql("UPDATE tasks SET title=?, assignee=?, attempts=?, depends_on=? WHERE id=?",
                     (title, cols.get("assignee"), cols.get("attempts", 0), cols.get("depends_on", "[]"), tid))


class DigestTests(DigestBase):
    def digest(self, *args):
        return self.ok("digest", *args, extra_env=FROZEN_ENV)

    def test_harness_passes_tz_to_cli(self):
        # the same instant is 2026-10-24 21:30 in UTC-2 and 2026-10-25 01:30 in UTC+2: the header date shows which TZ the CLI saw
        now = {"FACTORY_NOW": "2026-10-24T23:30:00+00:00"}
        for zone, day in (("Etc/GMT+2", "2026-10-24"), ("Etc/GMT-2", "2026-10-25")):
            with mock.patch.dict("os.environ", {"TZ": zone}):
                self.assertEqual(self.env.env()["TZ"], zone)
                out = self.ok("digest", extra_env=now).stdout
            self.assertTrue(out.startswith(f"# Factory digest {day}\n"), (zone, out[:40]))
        with mock.patch.dict("os.environ", {"TZ": "Etc/GMT+2"}):
            self.assertEqual(self.env.env(extra={"TZ": "UTC"})["TZ"], "UTC")  # extra_env still overrides

    def check_digest_sections(self, now, args, extra_env):
        """Seed events at now-2h / now-1h of a fixed clock and check every section of the digest."""
        e = self.env
        self.seed("T-0001", "done", "zz-test done task", pr_url=PR_URL)
        e.sql("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
              (iso(dt.timedelta(hours=-2), now), "judge", "task", "T-0001", "merged", f"@{SHA_A[:8]}"))
        e.sql("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,created) VALUES(?,?,?,?,?,?)",
              ("T-0001", PR_URL, SHA_A, "judge", "merge", iso(dt.timedelta(hours=-2), now)))
        self.seed("T-0002", "in_progress", "zz-test wip task", assignee="zz-test-coder", attempts=2)
        self.seed("T-0003", "in_review", "zz-test review task", assignee="zz-test-coder", attempts=1)
        self.seed("T-0004", "blocked", "zz-test blocked task")
        self.seed("T-0005", "todo", "zz-test ready task")
        self.seed("T-0006", "todo", "zz-test waiting task", depends_on='["T-0004"]')
        e.sql("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
              (iso(dt.timedelta(hours=-1), now), "judge", "task", "T-0003", "judge-blocked", "checks not green"))
        e.sql("INSERT INTO agents(name,role,goal_id,status) VALUES('zz-test-gone','coder','G-001','lost')")
        e.sql("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
              (iso(dt.timedelta(hours=-1), now), "tick", "agent", "zz-test-gone", "lost", "p1"))
        e.sql("INSERT INTO evaluations(team_id,score,author,created) VALUES('g-001-a',4,'x','t')")
        out = self.ok("digest", *args, extra_env=extra_env).stdout
        sec = {}
        cur = None
        for line in out.splitlines():
            if line.startswith("### "):
                cur = line[4:]
                sec[cur] = []
            elif cur:
                sec[cur].append(line)
        self.assertTrue(out.startswith(f"# Factory digest {now.date().isoformat()}\n"))
        self.assertIn("## G-001 — zz-test goal (active)", out)
        self.assertEqual(list(sec), ["Done", "In progress", "Blocked", "Up next", "Team scores"])
        done = "\n".join(sec["Done"])
        self.assertIn("T-0001", done)
        self.assertIn(PR_URL, done)
        self.assertIn(SHA_A[:8], done)
        prog = "\n".join(sec["In progress"])
        self.assertIn("T-0002", prog)
        self.assertIn("zz-test-coder", prog)
        self.assertIn("attempts 2", prog)
        self.assertIn("T-0003", prog)
        blocked = "\n".join(sec["Blocked"])
        self.assertIn("T-0004", blocked)
        self.assertIn("judge-blocked: checks not green", blocked)
        self.assertIn("zz-test-gone", blocked)
        self.assertIn("1 ready todo", "\n".join(sec["Up next"]))
        teams = "\n".join(sec["Team scores"])
        self.assertIn("| g-001-a | 6 | 1 | 1 | 0 | 1 | 4.0 |", teams)

    def test_digest_sections(self):
        now = dt.datetime.now().astimezone().replace(hour=12, minute=0, second=0, microsecond=0)
        self.check_digest_sections(now, ["--date", now.date().isoformat()], {"FACTORY_NOW": now.isoformat()})

    def fixed_berlin(self, hh, mm):
        fixed = f"2026-10-24T{hh:02d}:{mm:02d}:00+02:00"
        # no --date: the window is the 24 hours before the fixed clock, whatever the local date of now-2h
        self.check_digest_sections(dt.datetime.fromisoformat(fixed), [], {"TZ": "Europe/Berlin", "FACTORY_NOW": fixed})

    def test_digest_sections_at_0030_local(self):
        self.fixed_berlin(0, 30)

    def test_digest_sections_at_1200_local(self):
        self.fixed_berlin(12, 0)

    def test_digest_lists_hq_flagless_once(self):
        e = self.env
        e.seed_goal("G-002", title="zz-test second goal")
        e.sql("UPDATE agents SET flagless=1, flagless_since='2026-10-23T10:00:00+00:00' "
              "WHERE name IN ('zz-test-director','zz-test-boss')")
        e.sql("UPDATE agents SET flagless=1, flagless_since='2026-10-23T11:00:00+00:00' WHERE name='zz-test-coder'")
        out = self.digest().stdout
        for name in ("zz-test-director", "zz-test-boss"):
            self.assertEqual(out.count(f"agent {name} running without role flags"), 1)
        hq = out.split("## HQ")[1]
        self.assertIn("zz-test-director", hq)
        self.assertIn("zz-test-boss", hq)
        self.assertNotIn("zz-test-coder", hq)
        self.assertTrue(out.index("## HQ") > out.index("## G-002"))
        # the goal's own agent stays in that goal's Blocked list
        g1 = out.split("## G-001")[1].split("## G-002")[0]
        self.assertIn("agent zz-test-coder running without role flags", self._section(g1, "Blocked"))
        self.assertNotIn("zz-test-boss", out.split("## G-002")[1].split("## HQ")[0])

    def _block(self, tid, hours=-2, action="judge-blocked", detail="stale sha"):
        self.env.sql("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
                     (iso(dt.timedelta(hours=hours)), "judge", "task", tid, action, detail))

    @staticmethod
    def _section(out, name):
        return out.split(f"### {name}")[1].split("###")[0]

    def test_digest_done_task_with_earlier_judge_block_not_blocked(self):
        e = self.env
        self.seed("T-0001", "done", "zz-test refused then merged", pr_url=PR_URL)
        e.sql("UPDATE tasks SET updated=? WHERE id='T-0001'", (iso(dt.timedelta(hours=-1)),))
        self._block("T-0001", hours=-3)
        e.sql("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
              (iso(dt.timedelta(hours=-1)), "lead", "task", "T-0001", "update", "status=done"))
        out = self.digest("--hours", "24").stdout
        self.assertIn("T-0001", self._section(out, "Done"))
        self.assertNotIn("T-0001", self._section(out, "Blocked"))

    def test_digest_open_task_judge_blocked_listed(self):
        self.seed("T-0001", "in_review", "zz-test refused open", assignee="zz-test-coder")
        self._block("T-0001")
        self._block("T-0001", hours=-1, detail="checks red")
        blocked = self._section(self.digest("--hours", "24").stdout, "Blocked")
        self.assertEqual(blocked.count("T-0001"), 1)
        self.assertIn("judge-blocked: checks red", blocked)

    def test_digest_status_blocked_listed(self):
        self.seed("T-0001", "blocked", "zz-test status blocked")
        self.seed("T-0002", "blocked", "zz-test blocked with event")
        self._block("T-0002", action="merge-failed", detail="gh down")
        blocked = self._section(self.digest("--hours", "24").stdout, "Blocked")
        self.assertIn("T-0001 zz-test status blocked (blocked)", blocked)
        self.assertEqual(blocked.count("T-0002"), 1)
        self.assertIn("merge-failed: gh down", blocked)

    def test_digest_status_blocked_shows_reason(self):
        ev = "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)"
        self.seed("T-0001", "blocked", "zz-test noted")
        self.env.sql(ev, (iso(dt.timedelta(hours=-3)), "lead", "task", "T-0001", "note", "waiting on owner decision"))
        self.env.sql(ev, (iso(dt.timedelta(hours=-2)), "lead", "task", "T-0001", "update", "status=blocked"))
        self.seed("T-0002", "blocked", "zz-test event only")
        self.env.sql(ev, (iso(dt.timedelta(hours=-2)), "lead", "task", "T-0002", "update", "status=blocked — disk full"))
        self.seed("T-0003", "blocked", "zz-test bare")
        blocked = self._section(self.digest("--hours", "24").stdout, "Blocked")
        self.assertIn("T-0001 zz-test noted — blocked: waiting on owner decision", blocked)
        self.assertIn("T-0002 zz-test event only — blocked: disk full", blocked)
        self.assertIn("T-0003 zz-test bare (blocked)", blocked)

    def _reason_case(self, rows):
        ev = "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)"
        self.seed("T-0001", "blocked", "zz-test reason")
        for hours, action, detail in rows:
            self.env.sql(ev, (iso(dt.timedelta(hours=hours)), "lead", "task", "T-0001", action, detail))
        return self._section(self.digest("--hours", "48").stdout, "Blocked")

    def test_digest_blocked_reason_fresh_update_note_beats_older_note(self):
        blocked = self._reason_case([(-30, "note", "Attempt 2: fix item 2"),
                                     (-1, "update", "status=blocked — waiting on owner decision")])
        self.assertIn("blocked: waiting on owner decision", blocked)
        self.assertNotIn("Attempt 2", blocked)

    def test_digest_blocked_reason_newer_note_beats_update_note(self):
        blocked = self._reason_case([(-3, "update", "status=blocked — waiting on owner decision"),
                                     (-1, "note", "owner replied, retrying")])
        self.assertIn("blocked: owner replied, retrying", blocked)
        self.assertNotIn("waiting on owner", blocked)

    def test_digest_blocked_reason_none(self):
        blocked = self._reason_case([(-5, "update", "status=in_review pr=https://x/pull/1"),
                                     (-1, "update", "status=blocked")])
        self.assertIn("T-0001 zz-test reason (blocked)", blocked)
        self.assertNotIn("in_review", blocked)

    def test_digest_done_includes_worker_tasks(self):
        self.seed("T-0001", "done", "zz-test pm plan", pr_url=None)
        self.env.sql("UPDATE tasks SET role='pm', updated=? WHERE id='T-0001'", (iso(dt.timedelta(hours=-1)),))
        self.seed("T-0002", "done", "zz-test old pm plan", pr_url=None)
        self.env.sql("UPDATE tasks SET role='pm', updated=? WHERE id='T-0002'", (iso(dt.timedelta(days=-3)),))
        done = self.digest("--hours", "24").stdout.split("### Done")[1].split("###")[0]
        self.assertIn("T-0001 zz-test pm plan", done)
        self.assertNotIn("T-0002", done)

    def test_digest_no_double_count(self):
        e = self.env
        self.seed("T-0001", "done", "zz-test merged task", pr_url=PR_URL)
        e.sql("UPDATE tasks SET updated=? WHERE id='T-0001'", (iso(dt.timedelta(hours=-1)),))
        e.sql("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
              (iso(dt.timedelta(hours=-1)), "judge", "task", "T-0001", "merged", f"@{SHA_A[:8]}"))
        e.sql("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,created) VALUES(?,?,?,?,?,?)",
              ("T-0001", PR_URL, SHA_A, "judge", "merge", iso(dt.timedelta(hours=-1))))
        done = self.digest("--hours", "24").stdout.split("### Done")[1].split("###")[0]
        self.assertEqual(done.count("T-0001"), 1)
        self.assertIn(PR_URL, done)

    def close_goal(self, closed_h, updated_h=None, event=True, gid="G-001", detail="done"):
        """Mark the goal done: a status event `closed_h` hours before FROZEN, goals.updated `updated_h` before (default same)."""
        e = self.env
        e.sql("UPDATE goals SET status='done', updated=? WHERE id=?",
              (iso(dt.timedelta(hours=-(closed_h if updated_h is None else updated_h))), gid))
        if event:
            e.sql("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
                  (iso(dt.timedelta(hours=-closed_h)), "x", "goal", gid, "status", detail))

    def test_digest_goal_closed_2h_ago_shown_in_full(self):
        self.close_goal(2)
        out = self.digest().stdout
        self.assertIn("## G-001 — zz-test goal (done)", out)
        for sec in ("### Done", "### In progress", "### Blocked"):
            self.assertIn(sec, out)
        self.assertNotIn("Closed earlier", out)

    def test_digest_goal_closed_long_ago_collapsed(self):
        self.close_goal(30, updated_h=1)  # plan/edit bumped updated after the close
        out = self.digest().stdout
        self.assertNotIn("G-001", out)
        self.assertIn("_no active goals_", out)
        wide = self.digest("--hours", "72").stdout
        self.assertNotIn("## G-001", wide)
        self.assertIn("_no active goals_", wide)
        self.assertIn("## Closed earlier", wide)
        line = f"- G-001 — zz-test goal (done, closed {iso(dt.timedelta(hours=-30))})"
        self.assertEqual(wide.count(line), 1)
        self.assertEqual(wide.count("G-001"), 1)

    def test_digest_goal_closed_long_ago_explicit_goal_one_line(self):
        self.close_goal(30, updated_h=1)
        out = self.digest("--goal", "G-001").stdout
        self.assertEqual(out.count("G-001"), 1)
        self.assertIn("## Closed earlier", out)
        self.assertIn("(done, closed ", out)
        self.assertNotIn("### Done", out)

    def test_digest_closed_goal_without_status_event_uses_updated(self):
        self.close_goal(2, event=False)
        self.assertIn("## G-001 — zz-test goal (done)", self.digest().stdout)
        self.close_goal(30, event=False)
        self.assertNotIn("G-001", self.digest().stdout)
        wide = self.digest("--hours", "72").stdout
        self.assertIn(f"- G-001 — zz-test goal (done, closed {iso(dt.timedelta(hours=-30))})", wide)

    def test_digest_cancelled_goal_with_note_collapsed(self):
        self.close_goal(30, updated_h=1, detail="cancelled: dropped")
        self.env.sql("UPDATE goals SET status='cancelled' WHERE id='G-001'")
        self.assertIn("(cancelled, closed", self.digest("--hours", "72").stdout)

    def lost(self, task_id, task_status=None):
        e = self.env
        e.sql("INSERT INTO agents(name,role,goal_id,task_id,status) VALUES('zz-test-gone','coder','G-001',?,'lost')", (task_id,))
        if task_status:
            self.seed(task_id, task_status, "zz-test lost work")
        e.sql("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
              (iso(dt.timedelta(hours=-3)), "tick", "agent", "zz-test-gone", "lost", "p1"))

    def blocked_section(self):
        return self.digest().stdout.split("### Blocked")[1].split("###")[0]

    def test_digest_lost_agent_task_done_no_line(self):
        self.lost("T-0009", "done")
        self.assertNotIn("zz-test-gone", self.blocked_section())

    def test_digest_lost_agent_task_merged_after_no_line(self):
        self.lost("T-0009", "in_review")
        self.env.sql("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,created) VALUES(?,?,?,?,?,?)",
                     ("T-0009", PR_URL, SHA_A, "judge", "merge", iso(dt.timedelta(hours=-2))))
        self.assertNotIn("zz-test-gone", self.blocked_section())

    def test_digest_lost_agent_task_merged_event_after_no_line(self):
        self.lost("T-0009", "in_review")
        self.env.sql("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
                     (iso(dt.timedelta(hours=-2)), "judge", "task", "T-0009", "merged", "@abc"))
        self.assertNotIn("zz-test-gone", self.blocked_section())

    def test_digest_lost_agent_merge_before_lost_keeps_line(self):
        self.lost("T-0009", "in_progress")
        self.env.sql("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,created) VALUES(?,?,?,?,?,?)",
                     ("T-0009", PR_URL, SHA_A, "judge", "merge", iso(dt.timedelta(hours=-5))))
        self.assertIn("agent zz-test-gone lost at", self.blocked_section())

    def test_digest_lost_agent_open_task_keeps_line(self):
        self.lost("T-0009", "in_progress")
        self.assertIn("agent zz-test-gone lost at", self.blocked_section())

    def test_digest_lost_agent_no_task_keeps_line(self):
        self.lost(None)
        self.assertIn("agent zz-test-gone lost at", self.blocked_section())

    def test_digest_empty_sections(self):
        out = self.digest("--goal", "G-001").stdout
        self.assertEqual(out.count("_nothing_"), 3)

    def test_digest_leads_line_once_with_two_goals(self):
        self.env.seed_goal("G-002", title="zz-test goal two")
        out = self.digest().stdout
        self.assertIn("## G-001 ", out)
        self.assertIn("## G-002 ", out)
        self.assertEqual(out.count("Leads: "), 1)
        self.assertLess(out.index("Leads: "), out.index("## G-001 "))

    def test_digest_window(self):
        e = self.env
        self.seed("T-0001", "done", "zz-test old done", pr_url=PR_URL)
        old = iso(dt.timedelta(days=-3))
        e.sql("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
              (old, "judge", "task", "T-0001", "merged", f"@{SHA_A[:8]}"))
        e.sql("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,created) VALUES(?,?,?,?,?,?)",
              ("T-0001", PR_URL, SHA_A, "judge", "merge", old))
        out = self.digest("--hours", "24").stdout
        done = out.split("### Done")[1].split("###")[0]
        self.assertNotIn("T-0001", done)
        wide = self.digest("--hours", "96").stdout.split("### Done")[1].split("###")[0]
        self.assertIn("T-0001", wide)

    def test_digest_send_role(self):
        self.seed("T-0002", "in_progress", "zz-test wip", assignee="zz-test-coder")
        self.refused(3, "digest", "--send", agent="zz-test-coder")
        self.assertEqual(self.env.sql("SELECT count(*) c FROM messages")[0]["c"], 0)
        self.ok("digest", "--send", agent="zz-test-director", extra_env=FROZEN_ENV)
        m = self.env.sql("SELECT * FROM messages")
        self.assertEqual(len(m), 1)
        self.assertEqual((m[0]["sender"], m[0]["recipient"], m[0]["kind"]), ("zz-test-director", "zz-test-boss", "report"))
        self.assertEqual(m[0]["subject"], f"daily digest {FROZEN.date().isoformat()}")
        self.assertIn("# Factory digest", m[0]["body"])

    def test_scorecard_unchanged(self):
        e = self.env
        self.seed("T-0001", "done", "a", attempts=3)
        self.seed("T-0002", "in_progress", "b", attempts=1)
        e.seed_team("g-001-b", lead=None)
        e.sql("INSERT INTO verdicts(task_id,role,verdict,created) VALUES('T-0001','judge','merge','t'),"
              "('T-0002','qa','fail','t')")
        e.sql("INSERT INTO evaluations(team_id,score,author,created) VALUES('g-001-a',3,'x','t'),('g-001-a',4,'x','t')")
        out = self.ok("scorecard").stdout
        self.assertEqual(out, (
            "TEAM     STATUS  TASKS  DONE  MERGED  REJECTIONS  RETRIES  SCORE  LEAD\n"
            "g-001-a  active  2      1     1       1           2        3.5    -   \n"
            "g-001-b  active  0      0     0       0           0               -   \n"
            "\n"
            "director (24h, daytime 08-20): no utilization samples · cap breaches 0 · boss scores -\n"))


class TickDigestTests(DigestBase):
    def setUp(self):
        super().setUp()
        self.env.run("config", "digest_hour", "0")

    def digests(self, day=None):
        """Daily-digest messages, optionally of one date. The tick reads the real clock, so tests never hard-code the date."""
        if day:
            return self.env.sql("SELECT * FROM messages WHERE subject=?", (f"daily digest {day}",))
        return self.env.sql("SELECT * FROM messages WHERE subject LIKE 'daily digest %' ORDER BY id")

    def tick(self):
        """Run a tick; return the set of local dates it may have used (before / after, in case midnight passes mid-call)."""
        before = dt.date.today().isoformat()
        self.ok("tick")
        return {before, dt.date.today().isoformat()}

    def stored(self, key):
        return self.env.sql("SELECT value FROM config WHERE key=?", (key,))[0]["value"]

    def test_tick_digest_once(self):
        dates = self.tick()
        m = self.digests()
        self.assertEqual(len(m), 1)
        self.assertEqual((m[0]["sender"], m[0]["recipient"], m[0]["kind"]), ("director", "zz-test-boss", "report"))
        day = self.stored("last_digest_date")
        self.assertIn(day, dates)
        self.assertEqual(m[0]["subject"], f"daily digest {day}")
        self.tick()
        self.assertEqual(len(self.digests(day)), 1)

    def test_invalid_factory_now_does_not_abort_tick(self):
        p = self.env.run("tick", extra_env={"FACTORY_NOW": "not-a-time"})
        self.assertEqual(p.returncode, 0, f"{p.stdout}\n{p.stderr}")
        self.assertEqual(len(self.digests()), 0)  # build_digest raised ValueError; the tick caught it
        self.assertEqual(len(self.env.sql("SELECT * FROM events WHERE action='digest-failed'")), 1)
        q = self.env.run("digest", extra_env={"FACTORY_NOW": "not-a-time"})
        self.assertEqual(q.returncode, 1)
        self.assertIn("FACTORY_NOW must be an ISO timestamp, got not-a-time", q.stderr)
        with self.env.load_factory() as mod:
            with mock.patch.dict("os.environ", {"FACTORY_NOW": "not-a-time"}):
                with self.assertRaises(ValueError):
                    mod.digest_clock()

    def test_factory_now_error_is_a_value_error(self):
        with self.env.load_factory() as mod:
            self.assertTrue(issubclass(mod.FactoryNowError, ValueError))
            with mock.patch.dict("os.environ", {"FACTORY_NOW": "garbage"}):
                with self.assertRaises(mod.FactoryNowError) as cm:
                    mod.digest_clock()
        self.assertEqual(str(cm.exception), "FACTORY_NOW must be an ISO timestamp, got garbage")
        q = self.env.run("digest", extra_env={"FACTORY_NOW": "garbage"})
        self.assertEqual(q.returncode, 1)
        self.assertIn("FACTORY_NOW must be an ISO timestamp, got garbage", q.stderr)

    def test_other_value_error_in_build_digest_propagates(self):
        with self.env.load_factory() as mod:
            with mock.patch.object(mod, "build_digest", side_effect=ValueError("boom FACTORY_NOW-free")), \
                    mock.patch.object(mod, "db", return_value=mock.Mock()):
                with self.assertRaises(ValueError):
                    mod.cmd_digest(argparse.Namespace(goal=None, date=None, hours=24, send=False))

    def test_tick_digest_not_due(self):
        day = dt.date.today().isoformat()
        self.env.sql("INSERT INTO config(key,value) VALUES('last_digest_date',?)", (day,))
        self.tick()
        self.assertEqual(len(self.digests(day)), 0)  # a digest for the next date is fine if midnight passed mid-tick

    def test_tick_digest_before_hour(self):
        self.env.run("config", "digest_hour", "24")
        self.tick()
        self.assertEqual(len(self.digests()), 0)

    def test_tick_digest_herdr_unreachable(self):
        self.env.set_herdr(fail=True)
        p = self.ok("tick")
        self.assertIn("exporting only", p.stdout)
        self.assertEqual(len(self.digests()), 1)

    def test_tick_digest_failure_isolated(self):
        e = self.env
        with e.load_factory() as mod:  # load_factory closes every DB connection the tick opened
            with mock.patch.object(mod, "build_digest", side_effect=RuntimeError("boom")), \
                    mock.patch.object(mod, "tick_lock", return_value=None):
                mod.cmd_tick(mock.Mock(), locked=True)
        ev = e.sql("SELECT * FROM events WHERE action='digest-failed'")
        self.assertEqual(len(ev), 1)
        self.assertEqual((ev[0]["entity"], ev[0]["entity_id"], ev[0]["detail"]), ("digest", "-", "boom"))
        self.assertEqual(len(self.digests()), 0)
        self.assertTrue((e.home_dir / "exports").exists())  # the tick still exported

    def test_tick_digest_failed_logged_once_per_day(self):
        e = self.env
        before = dt.date.today().isoformat()
        with e.load_factory() as mod:
            with mock.patch.object(mod, "build_digest", side_effect=RuntimeError("boom")), \
                    mock.patch.object(mod, "tick_lock", return_value=None):
                mod.cmd_tick(mock.Mock(), locked=True)
                mod.cmd_tick(mock.Mock(), locked=True)
        dates = {before, dt.date.today().isoformat()}
        failed = len(e.sql("SELECT * FROM events WHERE action='digest-failed'"))
        self.assertTrue(1 <= failed <= len(dates), failed)  # once per local date
        self.assertIn(self.stored("last_digest_fail_date"), dates)


if __name__ == "__main__":
    unittest.main()
