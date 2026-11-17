"""T-1028: config idle_coder_reap (off|dry-run|on) + idle_coder_reap_s: the tick stops a coder the host reports idle/done
for over the threshold when its task has a PR, its tree is clean, its HEAD is the pushed PR head, it has no unread mail
and nothing runs in its pane. Real tick as a subprocess, temp FACTORY_HOME, real git worktree + bare origin, herdr and
gh stubs. The clock is FACTORY_NOW: tick 1 starts the idle clock, tick 2 runs 1000 s later (threshold 900)."""
import datetime as dt
import json
import os
import re
import unittest

from harness import PR_URL, FactoryTestCase, git

T0 = "2026-03-01T10:00:00+00:00"
T1 = "2026-03-01T10:16:40+00:00"   # T0 + 1000 s
T_SHORT = "2026-03-01T10:05:00+00:00"  # T0 + 300 s
BRANCH = "factory/t-0001"


class Base(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        base = e.home / "git"
        base.mkdir()
        self.origin, self.wt = base / "origin.git", base / "wt"
        git("init", "-q", "--bare", "-b", "main", str(self.origin), cwd=base)
        git("clone", "-q", str(self.origin), str(self.wt), cwd=base)
        git("config", "user.email", "zz-test@example.invalid", cwd=self.wt)
        git("config", "user.name", "zz-test", cwd=self.wt)
        (self.wt / "a.txt").write_text("a\n")
        git("add", "a.txt", cwd=self.wt)
        git("commit", "-q", "-m", "a", cwd=self.wt)
        git("push", "-q", "origin", f"HEAD:{BRANCH}", cwd=self.wt)
        e.seed_goal()
        e.seed_task(status="in_review")
        e.sql("UPDATE tasks SET worktree=?, branch=? WHERE id='T-0001'", (str(self.wt), BRANCH))
        e.seed_pr(url=PR_URL, sha=git("rev-parse", "HEAD", cwd=self.wt))
        self.coder("zz-test-coder", "p-1")
        self.run_ok("config", "idle_coder_reap", "on")
        self.proc = e.home / "fakeproc-idle"
        self.proc.mkdir()
        self.fake_proc(100, 1, ["claude"], agent="zz-test-coder")

    def fake_proc(self, pid, ppid, argv, agent=None):
        """A fake /proc/<pid>/{cmdline,stat,environ}; a claude process carries FACTORY_AGENT."""
        d = self.proc / str(pid)
        d.mkdir()
        (d / "cmdline").write_bytes(b"\0".join(x.encode() for x in argv) + b"\0")
        (d / "stat").write_text(f"{pid} ({argv[0]}) S {ppid} {pid} {pid} 0 -1 " + " ".join(["0"] * 30) + "\n")
        (d / "environ").write_bytes((f"FACTORY_AGENT={agent}\0" if agent else "X=1\0").encode())

    def run_ok(self, *args):
        p = self.env.run(*args)
        self.assertEqual(p.returncode, 0, p.stderr)
        return p

    def coder(self, name, pane, task="T-0001", role="coder"):
        self.env.seed_agent(name, role, "G-001", None, task, pane_id=pane)
        self.env.sql("UPDATE agents SET spawn_args='{}' WHERE name=?", (name,))

    def host(self, *specs):
        """specs: (name, pane, agent_status[, foreground process])."""
        self.env.set_herdr(
            workspaces=[{"workspace_id": "w1", "label": "x"}],
            panes=[{"pane_id": s[1], "tab_id": "t1", "workspace_id": "w1", "fg": s[3] if len(s) > 3 else "claude"}
                   for s in specs],
            agents=[{"name": s[0], "agent": "claude", "pane_id": s[1], "agent_status": s[2],
                     "agent_session": {"value": "s-" + s[0]}} for s in specs])

    def tick(self, when):
        self.env.sql("DELETE FROM throttle WHERE key NOT LIKE 'idlereap%'")
        p = self.env.run("tick", extra_env={"FACTORY_NOW": when, "FACTORY_SERVE_PROC_DIR": str(self.proc)})
        self.assertEqual(p.returncode, 0, p.stderr)

    def two_ticks(self, second=T1):
        self.tick(T0)
        self.tick(second)

    def events(self, action):
        return [dict(r) for r in self.env.sql("SELECT entity_id, detail FROM events WHERE action=?", (action,))]

    def stop_orders(self, name="zz-test-coder"):
        return [dict(r) for r in self.env.sql("SELECT subject, sender FROM messages WHERE recipient=? AND subject='STOP'", (name,))]

    def stop_flag(self, name="zz-test-coder"):
        return self.env.sql("SELECT stop_requested_at FROM agents WHERE name=?", (name,))[0][0]

    def assert_not_stopped(self, name="zz-test-coder"):
        self.assertEqual(self.stop_orders(name), [])
        self.assertIsNone(self.stop_flag(name))
        self.assertEqual(self.events("idle-coder-stopped"), [])


class Stops(Base):
    def test_idle_past_threshold_clean_pushed_pr_is_stopped_once_with_note(self):
        for status in ("idle", "done"):
            with self.subTest(status=status):
                self.env.sql("DELETE FROM throttle")
                self.env.sql("DELETE FROM messages")
                self.env.sql("DELETE FROM events")
                self.env.sql("UPDATE agents SET stop_requested_at=NULL WHERE name='zz-test-coder'")
                self.host(("zz-test-coder", "p-1", status))
                self.two_ticks()
                self.assertEqual(len(self.stop_orders()), 1)
                self.assertIsNotNone(self.stop_flag())
                self.assertEqual(len(self.events("idle-coder-stopped")), 1)
                notes = [r for r in self.events("note") if r["entity_id"] == "T-0001"]
                self.assertEqual(len(notes), 1)
                self.assertIn("tick stopped idle coder zz-test-coder", notes[0]["detail"])
                self.tick(T1)  # already stopping: no second order
                self.assertEqual(len(self.stop_orders()), 1)

    def test_in_progress_task_with_pr_is_stopped_too(self):
        self.env.sql("UPDATE tasks SET status='in_progress' WHERE id='T-0001'")
        self.host(("zz-test-coder", "p-1", "idle"))
        self.two_ticks()
        self.assertEqual(len(self.stop_orders()), 1)

    def test_at_most_three_per_tick(self):
        specs = []
        for i in range(2, 6):
            self.env.seed_task(f"T-000{i}", "G-001", status="in_review")
            self.env.sql("UPDATE tasks SET worktree=?, branch=? WHERE id=?", (str(self.wt), BRANCH, f"T-000{i}"))
            self.coder(f"zz-test-coder{i}", f"p-{i}", task=f"T-000{i}")
            self.fake_proc(100 + i, 1, ["claude"], agent=f"zz-test-coder{i}")
        names = ["zz-test-coder"] + [f"zz-test-coder{i}" for i in range(2, 6)]
        self.env.sql("UPDATE tasks SET pr_url=? WHERE pr_url IS NOT NULL", (PR_URL,))
        self.host(*[(n, f"p-{i + 1}", "idle") for i, n in enumerate(names)])
        self.two_ticks()
        self.assertEqual(len(self.events("idle-coder-stopped")), 3)
        self.tick(T1)  # the remaining two go on the next tick
        self.assertEqual(len(self.events("idle-coder-stopped")), 5)


class Shells(Base):
    """B1: a Bash-tool shell runs in its own session, so the pane foreground misses it; the claude process tree is walked.
    Every shell protects the coder, a CI-watch shell included (it ends on its own)."""

    def shell_run(self, cmd, via_child=False):
        self.fake_proc(101, 100, ["bash", "-c", cmd])
        if via_child:
            self.fake_proc(102, 101, ["sleep", "30"])
        self.host(("zz-test-coder", "p-1", "idle"))
        self.two_ticks()

    def test_ci_watch_shells_protect_too(self):
        for i, cmd in enumerate(("gh run watch 123 --exit-status", "gh pr checks 398 --watch --fail-fast",
                                 "until gh run list --branch b | grep -q completed; do sleep 30; done")):
            with self.subTest(cmd=cmd):
                self.env.sql("DELETE FROM throttle")
                self.fake_proc_reset()
                self.shell_run(cmd, via_child=bool(i))
                self.assert_not_stopped()

    def test_other_shells_protect(self):
        for cmd in ("python3 -m unittest tests.test_x", "make build", "sleep 900",
                    "gh run watch 1 && python3 deploy.py", "gh run watch 1; rm -rf x", "gh run list > out.txt",
                    "gh pr merge 1"):
            with self.subTest(cmd=cmd):
                self.env.sql("DELETE FROM throttle")
                self.fake_proc_reset()
                self.shell_run(cmd)
                self.assert_not_stopped()

    def test_bare_shell_protects(self):
        self.fake_proc(101, 100, ["bash"])
        self.host(("zz-test-coder", "p-1", "idle"))
        self.two_ticks()
        self.assert_not_stopped()

    def test_no_claude_process_found_is_not_stopped(self):
        self.fake_proc_reset(keep_claude=False)
        self.host(("zz-test-coder", "p-1", "idle"))
        self.two_ticks()
        self.assert_not_stopped()

    def fake_proc_reset(self, keep_claude=True):
        import shutil
        for d in self.proc.iterdir():
            if d.name != "100" or not keep_claude:
                shutil.rmtree(d)


class NotStopped(Base):
    def idle_then_check(self, second=T1):
        self.host(("zz-test-coder", "p-1", "idle"))
        self.two_ticks(second)
        self.assert_not_stopped()

    def test_unread_mail(self):
        self.env.sql("INSERT INTO messages(sender,recipient,kind,subject,created) VALUES('x','zz-test-coder','request','hi','t')")
        self.idle_then_check()

    def test_dirty_tree(self):
        (self.wt / "a.txt").write_text("changed\n")
        self.idle_then_check()

    def test_untracked_file(self):
        (self.wt / "new.txt").write_text("x\n")
        self.idle_then_check()

    def test_unpushed_commit(self):
        (self.wt / "b.txt").write_text("b\n")
        git("add", "b.txt", cwd=self.wt)
        git("commit", "-q", "-m", "b", cwd=self.wt)
        self.idle_then_check()

    def test_no_pr(self):
        self.env.sql("UPDATE tasks SET pr_url=NULL WHERE id='T-0001'")
        self.idle_then_check()

    def test_task_not_in_review_or_progress(self):
        self.env.sql("UPDATE tasks SET status='todo' WHERE id='T-0001'")
        self.idle_then_check()

    def test_under_threshold(self):
        self.idle_then_check(T_SHORT)

    def test_shell_running_in_pane(self):
        self.host(("zz-test-coder", "p-1", "idle", "sleep"))
        self.two_ticks()
        self.assert_not_stopped()

    def test_working_agent_resets_the_clock(self):
        self.host(("zz-test-coder", "p-1", "idle"))
        self.tick(T0)
        self.host(("zz-test-coder", "p-1", "working"))
        self.tick("2026-03-01T10:10:00+00:00")
        self.host(("zz-test-coder", "p-1", "idle"))
        self.tick(T1)  # idle only since the last working tick: the clock restarted, so 900 s have not passed
        self.assert_not_stopped()

    def test_flag_off(self):
        self.run_ok("config", "idle_coder_reap", "off")
        self.idle_then_check()

    def test_judge_and_reviewer_never_stopped(self):
        for role in ("judge", "reviewer", "qa"):
            self.env.sql("UPDATE agents SET role=? WHERE name='zz-test-coder'", (role,))
            self.env.sql("DELETE FROM throttle")
            self.host(("zz-test-coder", "p-1", "idle"))
            self.two_ticks()
            self.assert_not_stopped()

    def test_pr_head_differs_from_local_head(self):
        self.env.seed_pr(url=PR_URL, sha="0" * 40)
        self.idle_then_check()


class DryRun(Base):
    def test_dry_run_logs_once_and_stops_nothing(self):
        self.run_ok("config", "idle_coder_reap", "dry-run")
        self.host(("zz-test-coder", "p-1", "idle"))
        self.two_ticks()
        self.tick(T1)
        self.assert_not_stopped()
        ev = self.events("idle-coder-would-stop")
        self.assertEqual(len(ev), 1)
        self.assertEqual(json.loads(ev[0]["detail"])["task"], "T-0001")
        self.assertEqual([c for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "send-keys"]], [])




# ───────────── T-1622: idle-progress watchdog (config idle_watchdog off|shadow|on, idle_watchdog_min) ─────────────
WD_T0 = dt.datetime(2026, 3, 1, 10, 0, 0, tzinfo=dt.timezone.utc)
WD_CLAUDE, WD_CURSOR = "pm-g-001-a", "cursor-pm-g-002-a"


class WatchdogBase(FactoryTestCase):
    """Real spawn and tick as subprocesses on a temp FACTORY_HOME (HOME is a temp dir too), herdr stub, fake /proc. The
    clock is FACTORY_TEST_NOW; transcript and worktree times are set with os.utime relative to it. The claude agent's cwd
    is a `git init` dir without commits, so no wall-clock commit time leaks into the progress signal."""

    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal("G-001")
        e.seed_goal("G-002")
        e.sql("UPDATE goals SET org='cursor' WHERE id='G-002'")
        e.seed_team("g-002-a", goal="G-002", lead="cursor-lead-g-002-a")
        e.seed_agent("director", "director")
        e.seed_task("T-0001", "G-001", role="pm", status="in_progress", pr_url=None)
        e.seed_task("T-0002", "G-002", role="pm", status="in_progress", pr_url=None, team="g-002-a")
        fake = e.stub_bin / "agent"
        fake.write_text("#!/bin/sh\necho CURSOR_OK\n")
        fake.chmod(0o755)
        self.proc = e.home / "fakeproc-wd"
        self.proc.mkdir()
        self.cwd = {}
        for n in (WD_CLAUDE, WD_CURSOR):
            d = e.home / f"wd-{n}"
            d.mkdir()
            if n == WD_CLAUDE:  # cursor refuses a git work tree as its policy workspace: its cwd stays a plain dir
                git("init", "-q", str(d), cwd=e.home)
            self.cwd[n] = d
        self.ok("config", "idle_watchdog", "on", agent="owner")

    def at(self, minutes):
        return {"FACTORY_TEST_NOW": (WD_T0 + dt.timedelta(minutes=minutes)).isoformat(),
                "FACTORY_SERVE_PROC_DIR": str(self.proc)}

    def epoch(self, minutes):
        return (WD_T0 + dt.timedelta(minutes=minutes)).timestamp()

    def spawn(self, cursor=False, read_kickoff=True):
        name = WD_CURSOR if cursor else WD_CLAUDE
        where = ("--goal", "G-002", "--team", "g-002-a", "--task", "T-0002") if cursor else ("--goal", "G-001", "--task", "T-0001")
        self.ok("spawn", "pm", *where, "--cwd", str(self.cwd[name]), "--name", name, agent="director", extra_env=self.at(0))
        if not cursor:
            self.claude_proc(name)
        if read_kickoff:
            self.ok("inbox", agent=name, extra_env=self.at(0))
        self.env.calls_file.write_text("")
        return name

    def claude_proc(self, name, pid=100):
        d = self.proc / str(pid)
        d.mkdir()
        (d / "cmdline").write_bytes(b"claude\0")
        (d / "stat").write_text(f"{pid} (claude) S 1 {pid} {pid} 0 -1 " + " ".join(["0"] * 30) + "\n")
        (d / "environ").write_bytes(f"FACTORY_AGENT={name}\0".encode())

    def tick(self, minutes):
        return self.ok("tick", extra_env=self.at(minutes))

    def row(self, name):
        return self.env.sql("SELECT * FROM agents WHERE name=?", (name,))[0]

    def steps(self, action, name):
        return [json.loads(r["detail"]) for r in self.env.sql(
            "SELECT detail FROM events WHERE action=? AND entity_id=? ORDER BY id", (action, name))]

    def nudges(self, name):
        return [a for a in (c["argv"] for c in self.env.calls("herdr"))
                if a[:3] == ["agent", "prompt", name] and "Factory watchdog" in " ".join(a[3:])]

    def alerts(self, name):
        return self.env.sql("SELECT recipient, subject, body FROM messages WHERE kind='alert' AND sender='tick' "
                            "AND subject LIKE ? ORDER BY id", (f"{name}:%",))

    def host_calls(self, *cmd):
        return [c["argv"] for c in self.env.calls("herdr") if c["argv"][:len(cmd)] == list(cmd)]

    def notes(self, task):
        return [r["detail"] for r in self.env.sql("SELECT detail FROM events WHERE entity='task' AND entity_id=? "
                                                  "AND action='note' AND detail LIKE 'idle watchdog:%'", (task,))]

    def org_director(self, org):
        with self.env.load_factory() as mod:
            return mod.oname(mod.db(), org, "director")

    def directors(self):
        """The claude org's director name (prefix depends on org_prefix_default) and cursor's."""
        with self.env.load_factory() as mod:
            con = mod.db()
            return {mod.oname(con, "claude", "director"), mod.oname(con, "cursor", "director")}

    def claude_transcript(self, name, minutes):
        r = self.row(name)
        d = self.env.user_home / ".claude" / "projects" / re.sub(r"[^A-Za-z0-9]", "-", r["cwd"])
        d.mkdir(parents=True, exist_ok=True)
        f = d / f"{r['session_id']}.jsonl"
        f.write_text('{"type":"assistant"}\n')
        os.utime(f, (self.epoch(minutes), self.epoch(minutes)))

    def cursor_transcript(self, name, minutes, lines):
        ws = json.loads(self.row(name)["spawn_args"]).get("cursor_workspace") or self.row(name)["cwd"]
        d = self.env.user_home / ".cursor" / "projects" / re.sub(r"[^A-Za-z0-9]+", "-", ws).strip("-") / "agent-transcripts"
        d = d / "run-1"  # the real layout (T-1621): agent-transcripts/<ID>/<ID>.jsonl
        d.mkdir(parents=True, exist_ok=True)
        f = d / "run-1.jsonl"
        f.write_text("".join(json.dumps(x) + "\n" for x in lines))
        os.utime(f, (self.epoch(minutes), self.epoch(minutes)))


class WatchdogClaude(WatchdogBase):
    def test_defaults_shadow_and_30_min(self):
        with self.env.load_factory() as mod:
            self.assertEqual((mod.DEFAULT_CONFIG["idle_watchdog"], mod.DEFAULT_CONFIG["idle_watchdog_min"]), ("shadow", "30"))

    def test_nudge_once_then_relaunch_once_then_alert_both_directors_once(self):
        name = self.spawn()
        self.tick(29)
        self.assertEqual(self.nudges(name), [])
        self.tick(31)
        self.assertEqual(len(self.nudges(name)), 1)
        self.assertIn("task T-0001 is not done", self.nudges(name)[0][3])
        self.assertEqual([s["n"] for s in self.steps("idle-watchdog-nudge", name)], [1])
        self.assertEqual(len(self.notes("T-0001")), 1)
        self.tick(40)  # 9 min after the nudge: no second nudge, no relaunch yet
        self.assertEqual(len(self.nudges(name)), 1)
        self.assertEqual(self.host_calls("pane", "close"), [])
        old = self.row(name)
        self.tick(47)  # 16 min after the nudge (> W/2)
        self.assertEqual(len(self.host_calls("pane", "close")), 1)
        self.assertEqual(len(self.host_calls("agent", "start")), 1)
        self.assertEqual(len(self.steps("idle-watchdog-relaunch", name)), 1)
        self.assertEqual(self.steps("idle-watchdog-relaunch", name)[0]["old_sid"], old["session_id"])
        new = self.row(name)
        self.assertEqual((new["status"], new["task_id"]), ("live", "T-0001"))
        self.assertNotEqual(new["session_id"], old["session_id"])
        self.ok("inbox", agent=name, extra_env=self.at(47))  # the fresh pane reads its kickoff, then stalls again
        self.tick(70)
        self.assertEqual(self.alerts(name), [])
        self.tick(78)  # 31 min after the relaunch
        al = self.alerts(name)
        self.assertEqual({a["recipient"] for a in al}, self.directors())
        self.assertEqual(len(al), 2)
        self.assertIn("1 nudge(s), 1 fresh relaunch", al[0]["body"])
        for m in (100, 200, 400):  # idempotent: nothing more, ever, on this task attempt
            self.tick(m)
        self.assertEqual(len(self.alerts(name)), 2)
        self.assertEqual(len(self.nudges(name)), 1)
        self.assertEqual(len(self.host_calls("agent", "start")), 1)

    def assert_clock_reset(self, touch):
        name = self.spawn()
        touch(name)  # progress at minute 20
        self.tick(31)
        self.assertEqual(self.nudges(name), [])
        self.assertEqual(self.steps("idle-watchdog-nudge", name), [])
        self.tick(51)  # 31 min after that progress
        self.assertEqual(len(self.nudges(name)), 1)

    def test_worktree_change_resets_the_clock(self):
        def touch(name):
            f = self.cwd[name] / "new.txt"
            f.write_text("x\n")
            os.utime(f, (self.epoch(20), self.epoch(20)))
        self.assert_clock_reset(touch)

    def test_transcript_turn_resets_the_clock(self):
        self.assert_clock_reset(lambda name: self.claude_transcript(name, 20))

    def test_own_db_write_resets_the_clock(self):
        self.assert_clock_reset(lambda name: self.ok("task", "note", "T-0001", "working on it", agent=name,
                                                     extra_env=self.at(20)))

    def test_shadow_records_steps_without_pane_action_or_messages(self):
        self.ok("config", "idle_watchdog", "shadow", agent="owner")
        name = self.spawn()
        for m in (31, 47, 78):
            self.tick(m)
        self.assertEqual([s["mode"] for s in self.steps("idle-watchdog-nudge", name)], ["shadow"])
        self.assertEqual([s["mode"] for s in self.steps("idle-watchdog-relaunch", name)], ["shadow"])
        self.assertEqual([s["mode"] for s in self.steps("idle-watchdog-alert", name)], ["shadow"])
        self.assertEqual(self.nudges(name), [])
        self.assertEqual(self.host_calls("pane", "close"), [])
        self.assertEqual(self.alerts(name), [])
        self.assertEqual(self.notes("T-0001"), [])

    def test_shadow_steps_do_not_count_after_the_flip_to_on(self):
        self.ok("config", "idle_watchdog", "shadow", agent="owner")
        name = self.spawn()
        for m in (31, 47, 78):  # the whole shadow walk: nudge, relaunch and alert rows, no action
            self.tick(m)
        self.assertEqual([s["mode"] for s in self.steps("idle-watchdog-alert", name)], ["shadow"])
        self.ok("config", "idle_watchdog", "on", agent="owner")
        self.tick(80)  # still idle since minute 0: the real nudge, not skipped for the shadow alert
        self.assertEqual(len(self.nudges(name)), 1)
        self.assertEqual(self.host_calls("pane", "close"), [])  # not relaunched on the strength of the shadow nudge
        self.tick(90)
        self.assertEqual(self.host_calls("pane", "close"), [])
        self.tick(96)  # 16 min after the real nudge
        self.assertEqual(len(self.host_calls("pane", "close")), 1)
        self.assertEqual(len(self.host_calls("agent", "start")), 1)
        self.ok("inbox", agent=name, extra_env=self.at(96))
        self.tick(120)
        self.assertEqual(self.alerts(name), [])
        self.tick(127)  # 31 min after the real relaunch
        self.assertEqual({a["recipient"] for a in self.alerts(name)}, self.directors())
        for m in (150, 200, 400):
            self.tick(m)
        self.assertEqual(len(self.nudges(name)), 1)
        self.assertEqual(len(self.host_calls("agent", "start")), 1)
        self.assertEqual(len(self.alerts(name)), 2)
        self.assertEqual([s["mode"] for s in self.steps("idle-watchdog-nudge", name)], ["shadow", "on"])
        self.assertEqual([s["mode"] for s in self.steps("idle-watchdog-relaunch", name)], ["shadow", "on"])
        self.assertEqual([s["mode"] for s in self.steps("idle-watchdog-alert", name)], ["shadow", "on"])

    def test_off_does_nothing(self):
        self.ok("config", "idle_watchdog", "off", agent="owner")
        name = self.spawn()
        self.tick(31)
        self.assertEqual(self.steps("idle-watchdog-nudge", name), [])

    def test_blocked_task_is_skipped(self):
        name = self.spawn()
        self.env.sql("UPDATE tasks SET status='blocked', waits='owner' WHERE id='T-0001'")
        for m in (31, 47, 78, 200):
            self.tick(m)
        self.assertEqual(self.nudges(name), [])
        self.assertEqual(self.env.sql("SELECT 1 FROM events WHERE action LIKE 'idle-watchdog-%'"), [])

    def test_hold_in_the_spec_is_alerted_not_nudged(self):
        name = self.spawn()
        self.env.sql("UPDATE tasks SET spec='HOLD: wait for PR 703 to merge' WHERE id='T-0001'")
        self.tick(31)
        self.tick(60)
        self.assertEqual(self.nudges(name), [])
        al = self.alerts(name)  # W3: a hold is escalated to the org director only
        self.assertEqual([a["recipient"] for a in al], [self.org_director("claude")])
        self.assertIn("named hold", al[0]["body"])

    def test_relaunched_pane_that_never_reads_its_kickoff_is_still_alerted(self):
        name = self.spawn()
        self.tick(31)
        self.tick(47)
        self.assertEqual(len(self.steps("idle-watchdog-relaunch", name)), 1)
        self.claude_proc(name, pid=102)  # the fresh pane runs, but never runs factory inbox
        self.tick(70)
        self.assertEqual(self.alerts(name), [])
        self.tick(78)
        al = self.alerts(name)
        self.assertEqual({a["recipient"] for a in al}, self.directors())
        self.assertIn("never read its kickoff", al[0]["body"])
        self.tick(120)
        self.assertEqual(len(self.alerts(name)), 2)
        self.assertEqual(len(self.host_calls("agent", "start")), 1)

    def test_progress_after_the_nudge_relaunches_after_half_w_not_w(self):
        name = self.spawn()
        self.tick(31)
        self.assertEqual(len(self.nudges(name)), 1)
        self.ok("task", "note", "T-0001", "on it", agent=name, extra_env=self.at(32))
        self.tick(46)  # 14 min after that write: not yet W/2
        self.assertEqual(self.host_calls("pane", "close"), [])
        self.tick(48)  # 16 min after the write, 17 after the nudge
        self.assertEqual(len(self.steps("idle-watchdog-relaunch", name)), 1)
        self.assertEqual(len(self.host_calls("pane", "close")), 1)

    def test_relaunch_waits_while_the_spawn_gate_load_cap_is_hit(self):
        name = self.spawn()
        self.tick(31)
        self.ok("tick", extra_env={**self.at(47), "FACTORY_LOADAVG": "999"})
        self.assertEqual(self.host_calls("pane", "close"), [])  # never close a pane spawn would only queue
        self.assertEqual(self.steps("idle-watchdog-relaunch", name), [])
        self.assertTrue(self.env.sql("SELECT 1 FROM events WHERE action='idle-watchdog-relaunch-deferred' AND entity_id=?",
                                     (name,)))
        self.assertEqual(self.row(name)["status"], "live")
        self.ok("tick", extra_env={**self.at(50), "FACTORY_LOADAVG": "0.1"})
        self.assertEqual(len(self.host_calls("pane", "close")), 1)
        self.assertEqual(self.row(name)["status"], "live")

    def test_shell_under_the_agent_blocks_the_nudge(self):
        name = self.spawn()
        d = self.proc / "101"
        d.mkdir()
        (d / "cmdline").write_bytes(b"bash\0-c\0gh run watch 1\0")
        (d / "stat").write_text("101 (bash) S 100 101 101 0 -1 " + " ".join(["0"] * 30) + "\n")
        (d / "environ").write_bytes(b"X=1\0")
        self.tick(31)
        self.assertEqual(self.nudges(name), [])

    def test_unread_kickoff_is_left_to_the_kickoff_check(self):
        name = self.spawn(read_kickoff=False)
        self.tick(31)
        self.assertEqual(self.env.sql("SELECT 1 FROM events WHERE action LIKE 'idle-watchdog-%' AND entity_id=?", (name,)), [])


class WatchdogCursor(WatchdogBase):
    TEXT = "I pushed the branch. " + "x" * 400 + " Shall I open the PR now?"

    def test_ended_turn_two_nudges_ten_min_apart_then_alert_with_the_final_text(self):
        name = self.spawn(cursor=True)
        self.cursor_transcript(name, 5, [{"role": "user", "message": {"content": [{"type": "text", "text": "kickoff"}]}},
                                         {"role": "assistant", "message": {"content": [{"type": "text", "text": self.TEXT}]}},
                                         {"type": "turn_ended", "status": "success"}])
        self.tick(14)  # the turn ended 9 min ago
        self.assertEqual(self.nudges(name), [])
        self.tick(16)
        self.assertEqual(len(self.nudges(name)), 1)
        self.assertIn("your turn ended 11 min ago", self.nudges(name)[0][3])
        self.assertIn("factory msg cursor-director", self.nudges(name)[0][3])
        enters = self.host_calls("agent", "send-keys", name, "Enter")
        self.assertEqual(len(enters), 1)  # T-1599's submit: prompt, then Enter
        self.tick(20)  # 4 min after nudge 1
        self.assertEqual(len(self.nudges(name)), 1)
        self.tick(27)
        self.assertEqual(len(self.nudges(name)), 2)
        self.tick(38)
        self.assertEqual(len(self.nudges(name)), 2)  # never a third
        al = self.alerts(name)  # W3: one escalation to the org director, not both
        self.assertEqual([a["recipient"] for a in al], ["cursor-director"])
        self.assertIn(repr(self.TEXT[-300:]), al[0]["body"])
        for m in (60, 120):
            self.tick(m)
        self.assertEqual(len(self.alerts(name)), 1)
        self.assertEqual(self.host_calls("pane", "close"), [])  # the cursor ended-turn path never relaunches

    def test_ended_turn_with_error_alerts_the_org_director_at_once_without_nudge(self):
        name = self.spawn(cursor=True)
        err = "Claude Opus 5.5 is not included in your weekly usage"
        self.cursor_transcript(name, 1, [{"type": "turn_ended", "status": "error", "error": err}])
        self.tick(2)
        self.tick(40)
        self.assertEqual(self.nudges(name), [])
        al = self.alerts(name)
        self.assertEqual([a["recipient"] for a in al], ["cursor-director"])
        self.assertIn(err, al[0]["body"])

    def test_error_text_under_an_unknown_key_is_still_quoted(self):
        name = self.spawn(cursor=True)
        self.cursor_transcript(name, 1, [{"type": "turn_ended", "status": "error", "detail": "usage cap reached"}])
        self.tick(2)
        self.assertIn("usage cap reached", self.alerts(name)[0]["body"])

    def test_hold_with_an_ended_turn_is_alerted_not_nudged(self):
        name = self.spawn(cursor=True)
        self.env.sql("UPDATE tasks SET judge_hold_reason='lane slot pending' WHERE id='T-0002'")
        self.cursor_transcript(name, 0, [{"type": "turn_ended", "status": "success"}])
        self.tick(11)
        self.assertEqual(self.nudges(name), [])
        al = self.alerts(name)
        self.assertEqual([a["recipient"] for a in al], ["cursor-director"])
        self.assertIn("named hold", al[0]["body"])

    def test_no_transcript_after_a_read_kickoff_takes_the_generic_nudge(self):
        name = self.spawn(cursor=True)
        self.tick(29)
        self.assertEqual(self.nudges(name), [])
        self.tick(31)
        self.assertEqual(len(self.nudges(name)), 1)
        self.assertEqual(self.steps("idle-watchdog-nudge", name)[0]["runtime"], "cursor")

    def test_no_transcript_and_unread_kickoff_is_t1599s(self):
        name = self.spawn(cursor=True, read_kickoff=False)
        self.tick(31)
        self.assertEqual(self.steps("idle-watchdog-nudge", name), [])
        self.assertTrue(self.env.sql("SELECT 1 FROM events WHERE action='kickoff-resent' AND entity_id=?", (name,)))

    def test_transcript_mid_turn_resets_the_clock(self):
        name = self.spawn(cursor=True)
        self.cursor_transcript(name, 20, [{"role": "assistant", "message": {"content": "running tests"}}])
        self.tick(31)
        self.assertEqual(self.nudges(name), [])
        self.tick(51)
        self.assertEqual(len(self.nudges(name)), 1)

    def test_worktree_change_resets_the_clock(self):
        name = self.spawn(cursor=True)
        wt = self.env.home / "wd-cursor-tree"  # the task tree; the policy workspace stays outside git
        git("init", "-q", str(wt), cwd=self.env.home)
        self.env.sql("UPDATE agents SET cwd=? WHERE name=?", (str(wt), name))
        f = wt / "new.txt"
        f.write_text("x\n")
        os.utime(f, (self.epoch(20), self.epoch(20)))
        self.tick(31)
        self.assertEqual(self.nudges(name), [])
        self.tick(51)
        self.assertEqual(len(self.nudges(name)), 1)

    def test_own_db_write_resets_the_clock(self):
        name = self.spawn(cursor=True)
        self.ok("task", "note", "T-0002", "working on it", agent=name, extra_env=self.at(20))
        self.tick(31)
        self.assertEqual(self.nudges(name), [])
        self.tick(51)
        self.assertEqual(len(self.nudges(name)), 1)

    def test_stall_without_an_ended_turn_nudges_relaunches_then_alerts_both_directors_once(self):
        name = self.spawn(cursor=True)
        self.tick(31)
        self.assertEqual(len(self.nudges(name)), 1)
        self.assertEqual(len(self.host_calls("agent", "send-keys", name, "Enter")), 1)
        old = self.row(name)
        self.tick(47)
        self.assertEqual(len(self.host_calls("pane", "close")), 1)
        self.assertEqual(len(self.steps("idle-watchdog-relaunch", name)), 1)
        new = self.row(name)
        self.assertEqual((new["status"], new["task_id"]), ("live", "T-0002"))
        self.assertNotEqual(new["session_id"], old["session_id"])
        self.ok("inbox", agent=name, extra_env=self.at(47))
        self.tick(78)
        al = self.alerts(name)
        self.assertEqual({a["recipient"] for a in al}, self.directors())
        for m in (100, 300):
            self.tick(m)
        self.assertEqual(len(self.alerts(name)), 2)
        self.assertEqual(len(self.host_calls("pane", "close")), 1)
        self.assertEqual(len(self.nudges(name)), 1)


    def test_relaunched_cursor_pane_unread_gets_no_second_relaunch_and_is_alerted(self):
        name = self.spawn(cursor=True)
        self.tick(31)
        self.tick(47)
        self.assertEqual(len(self.steps("idle-watchdog-relaunch", name)), 1)
        for m in (55, 60, 65, 70, 78, 90, 120):  # the new kickoff is never read
            self.tick(m)
        self.assertEqual(len(self.host_calls("pane", "close")), 1)  # T-1599 does not relaunch it again
        self.assertEqual(self.steps("kickoff-relaunch", name), [])
        al = [a for a in self.alerts(name) if "idle" in a["subject"]]
        self.assertEqual({a["recipient"] for a in al}, self.directors())

    def test_separator_twin_project_dir_is_not_read(self):
        name = self.spawn(cursor=True)
        ws = json.loads(self.row(name)["spawn_args"]).get("cursor_workspace") or self.row(name)["cwd"]
        twin = re.sub(r"[^A-Za-z0-9]", "", ws)  # same letters and digits, no separators: another agent's dir
        d = self.env.user_home / ".cursor" / "projects" / twin / "agent-transcripts" / "x"
        d.mkdir(parents=True)
        f = d / "x.jsonl"
        f.write_text(json.dumps({"type": "turn_ended", "status": "error", "error": "not mine"}) + "\n")
        os.utime(f, (self.epoch(1), self.epoch(1)))
        self.tick(2)
        self.assertEqual(self.alerts(name), [])
        self.assertEqual(self.steps("idle-watchdog-error-alert", name), [])

    def test_symlinked_transcript_dir_is_not_followed(self):
        name = self.spawn(cursor=True)
        ws = json.loads(self.row(name)["spawn_args"]).get("cursor_workspace") or self.row(name)["cwd"]
        elsewhere = self.env.home / "elsewhere" / "x"
        elsewhere.mkdir(parents=True)
        f = elsewhere / "x.jsonl"
        f.write_text(json.dumps({"type": "turn_ended", "status": "error", "error": "via symlink"}) + "\n")
        os.utime(f, (self.epoch(1), self.epoch(1)))
        proj = self.env.user_home / ".cursor" / "projects" / re.sub(r"[^A-Za-z0-9]+", "-", ws).strip("-")
        proj.mkdir(parents=True)
        (proj / "agent-transcripts").symlink_to(self.env.home / "elsewhere")
        self.tick(2)
        self.assertEqual(self.steps("idle-watchdog-error-alert", name), [])


class WatchdogDoneRefused(WatchdogBase):
    """W5: a refused factory done is recorded as done-refused (with the refusing check) and alerted at once."""

    def test_refused_done_is_recorded_and_alerted_once(self):
        e = self.env
        wt = e.home / "wd-coder"
        git("init", "-q", "-b", "main", str(wt), cwd=e.home)
        git("config", "user.email", "zz-test@example.invalid", cwd=wt)
        git("config", "user.name", "zz-test", cwd=wt)
        (wt / "a.txt").write_text("a\n")
        git("add", "a.txt", cwd=wt)
        git("commit", "-q", "-m", "a", cwd=wt)
        (wt / "a.txt").write_text("changed\n")  # uncommitted tracked change: done refuses
        e.seed_task("T-0003", "G-001", status="in_progress", pr_url=None)
        e.sql("UPDATE tasks SET worktree=?, branch='factory/t-0003' WHERE id='T-0003'", (str(wt),))
        e.seed_agent("zz-test-coder", "coder", "G-001", None, "T-0003", pane_id="p-9", started=WD_T0.isoformat())
        e.sql("UPDATE agents SET spawn_args='{}', cwd=? WHERE name='zz-test-coder'", (str(wt),))
        e.set_herdr(agents=[{"name": "zz-test-coder", "agent": "claude", "pane_id": "p-9", "agent_status": "idle"}])
        p = e.run("done", "finished", agent="zz-test-coder", extra_env=self.at(5))
        self.assertEqual(p.returncode, 3, p.stderr)
        ref = self.steps("done-refused", "zz-test-coder")
        self.assertEqual([(r["gate"], r["code"]) for r in ref], [("coder-checks", 3)])
        self.assertEqual(ref[0]["why"], "commit (a WIP commit is fine) and push, then run factory done again")
        self.tick(6)
        self.tick(7)
        al = self.alerts("zz-test-coder")
        self.assertEqual(len(al), 1)
        self.assertIn("coder-checks", al[0]["subject"])
        self.assertIn("commit (a WIP commit is fine) and push", al[0]["body"])

    def test_unread_orders_refusal_is_not_recorded(self):
        self.env.seed_agent("zz-test-qa", "qa", "G-001", None, "T-0001", pane_id="p-8", started=WD_T0.isoformat())
        self.env.sql("INSERT INTO messages(sender,recipient,kind,subject,created) VALUES('lead','zz-test-qa','request','do x','t')")
        self.refused(3, "done", "PASS", agent="zz-test-qa", extra_env=self.at(1))
        self.assertEqual(self.steps("done-refused", "zz-test-qa"), [])

    def test_done_that_passes_records_no_refusal(self):
        self.env.seed_agent("zz-test-qa", "qa", "G-001", None, "T-0001", pane_id="p-8", started=WD_T0.isoformat())
        self.ok("done", "PASS", agent="zz-test-qa", extra_env=self.at(1))
        self.assertEqual(self.steps("done-refused", "zz-test-qa"), [])


if __name__ == "__main__":
    unittest.main()
