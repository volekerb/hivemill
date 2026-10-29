"""T-0615: reap_agent_tests reaps an ended agent's own local test runners the moment it ends (done / stop), under
config orphan_reap_mode; alert_test_orphans tells the director about old test runs without a live owner.
Fake /proc root + the _serve_kill seam; no real process is signalled."""
import argparse
import json
import os
import re
import signal
import time
import unittest
from unittest import mock

from harness import FactoryTestCase

GRACE = {"FACTORY_SERVE_KILL_GRACE_S": "0.3", "FACTORY_SERVE_MAINPID": ""}
KEYS = {"pid", "start_time", "argv0", "cmd_sha", "cwd", "worktree", "task", "agent", "attributed_by", "signal", "trigger"}
ALERT_KEYS = {"pid", "start_time", "age_s", "argv0", "cmd_sha", "cwd", "agent", "agent_status"}
RUNNER = ("python3", "ci/affected.py", "--run")
CLK = os.sysconf("SC_CLK_TCK")


class Base(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal("G-9")
        e.seed_task("T-9", "G-9", status="todo", pr_url=None)
        e.seed_task("T-8", "G-9", status="todo", pr_url=None)
        self.proc = e.home / "fakeproc"
        self.proc.mkdir()
        self.tmp = e.home / "faketmp"
        self.tmp.mkdir()
        self.btime = int(time.time()) - 100000
        (self.proc / "stat").write_text(f"cpu 1 2 3\nbtime {self.btime}\n")
        self.wt = e.home_dir / "worktrees" / "G-9-T-9"
        self.wt8 = e.home_dir / "worktrees" / "G-9-T-8"
        slug = re.sub(r"[^A-Za-z0-9]", "-", str(e.home_dir / "worktrees")) + "-"
        self.pad = self.tmp / f"claude-{os.getuid()}" / f"{slug}G-9-T-9" / "sess" / "scratchpad"
        for d in (self.wt, self.wt8, self.pad):
            d.mkdir(parents=True)
        self.agent("coder-t-9", "exited", role="coder")

    def agent(self, name, status, task="T-9", role="coder"):
        self.env.seed_agent(name, role, "G-9", None, task, status=status)

    def fake(self, pid, cwd=None, argv=RUNNER, agent="coder-t-9", ppid=1, start=1000, pgrp=None):
        d = self.proc / str(pid)
        d.mkdir()
        (d / "cmdline").write_bytes(b"\0".join(x.encode() for x in argv) + b"\0")
        env = b"" if agent is None else f"FACTORY_AGENT={agent}".encode() + b"\0"
        (d / "environ").write_bytes(env)
        (d / "stat").write_text(f"{pid} (x) S {ppid} {pgrp or 1} 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 {start}\n")
        os.symlink(str(cwd or self.wt), d / "cwd")
        return pid

    def drop(self, pid):
        p = self.proc / str(pid)
        for f in p.iterdir():
            f.unlink()
        p.rmdir()

    def patched(self, fn, getuid=None, extra=None):
        """Run fn(mod, sent) with the fake proc root, worktrees and a kill seam that removes a TERMed pid."""
        sent = []

        def kill(pid, sig):
            sent.append((pid, sig))
            if sig == signal.SIGKILL or self.kill_dies:
                if (self.proc / str(abs(pid))).exists():
                    self.drop(abs(pid))
        env = {"FACTORY_SERVE_PROC_DIR": str(self.proc), "FACTORY_TMP_DIR": str(self.tmp), **GRACE, **(extra or {})}
        with mock.patch.dict(os.environ, env), self.env.load_factory() as mod:
            os.environ.update(env)
            mod.WORKTREES = self.env.home_dir / "worktrees"
            patches = [mock.patch.object(mod, "_serve_kill", kill)]
            if getuid:
                patches.append(mock.patch.object(os, "getuid", return_value=getuid))
            for p in patches:
                p.start()
            try:
                fn(mod)
            finally:
                for p in patches:
                    p.stop()
        return sent

    kill_dies = True

    def reap(self, name="coder-t-9", trigger="done", mode="on", **kw):
        if mode:
            self.env.run("config", "orphan_reap_mode", mode)
        return self.patched(lambda mod: mod.reap_agent_tests(mod.db(), name, trigger), **kw)

    def alert(self, **kw):
        self.env.sql("DELETE FROM throttle")
        return self.patched(lambda mod: mod.alert_test_orphans(mod.db()), **kw)

    def events(self, action):
        return [dict(r) for r in self.env.sql("SELECT entity_id, detail FROM events WHERE action=?", (action,))]

    def director_msgs(self):
        return self.env.sql("SELECT sender, kind, body FROM messages WHERE recipient='director'")

    def aged(self, minutes):
        """start ticks that make a fake pid `minutes` old right now."""
        return int((time.time() - self.btime - minutes * 60) * CLK)

    def assertUntouched(self, sent):
        self.assertEqual(sent, [])
        for a in ("agent-tests-reaped", "agent-tests-would-reap", "agent-tests-reap-failed"):
            self.assertEqual(self.events(a), [])


class Matcher(Base):
    def test_matcher_true_for_test_runners(self):
        for argv in (["python3", "ci/affected.py", "--run"], ["python3", "-m", "unittest", "discover", "-s", "tests"],
                     ["python3.12", "-m", "pytest", "-q"], ["pytest", "x"], ["py.test"],
                     ["python3", "/x/bin/factory", "test", "--", "y"], ["python3", "/x/bin/factory_cli.py", "test"]):
            with self.subTest(argv):
                self.patched(lambda mod, a=argv: self.assertTrue(mod._is_test_runner(a)))

    def test_matcher_false_for_everything_else(self):
        for argv in (["python3", "bin/factory", "serve"], ["node", "next-server"], ["claude"], ["python3", "srv.py", "."],
                     ["bash"], ["python3", "-m", "http.server"], [], ["python3"], ["vim", "ci/affected.py"],
                     ["python3", "bin/factory", "tick"]):
            with self.subTest(argv):
                self.patched(lambda mod, a=argv: self.assertFalse(mod._is_test_runner(a)))


class Never(Base):
    def test_never_agent_unset(self):
        self.fake(101, agent=None)
        self.assertUntouched(self.reap())

    def test_never_agent_empty(self):
        self.fake(101, agent="")
        self.assertUntouched(self.reap())

    def test_never_agent_owner(self):
        self.fake(101, agent="owner")
        self.assertUntouched(self.reap())

    def test_never_a_different_live_agent(self):
        self.agent("qa-t-9", "live", role="qa")
        self.fake(101, agent="qa-t-9")
        self.assertUntouched(self.reap())

    def test_never_a_different_ended_agent(self):
        self.agent("qa-t-9", "exited", role="qa")
        self.fake(101, agent="qa-t-9")
        self.assertUntouched(self.reap())

    def test_never_hq_agent_even_when_ended(self):
        for name, role in (("director", "director"), ("lead-x", "lead"), ("boss", "boss"), ("staff-engineer", "staff-engineer")):
            with self.subTest(name):
                self.agent(name, "exited", role=role)
                self.fake(101, agent=name)
                self.assertUntouched(self.reap(name=name))
                self.drop(101)

    def test_never_agent_not_yet_exited_for_done(self):
        for st in ("live", "starting", "queued", "done", "stopped"):
            with self.subTest(st):
                self.env.sql("UPDATE agents SET status=? WHERE name='coder-t-9'", (st,))
                self.fake(101)
                self.assertUntouched(self.reap())
                self.drop(101)

    def test_never_agent_not_stopped_for_stop(self):
        self.fake(101)
        self.assertUntouched(self.reap(trigger="stop"))  # status is 'exited'

    def test_never_cwd_and_argv_outside_worktrees_and_scratchpads(self):
        self.fake(101, cwd=self.tmp, argv=("python3", "-m", "unittest", "discover", "-s", "tests"))
        self.assertUntouched(self.reap())

    def test_never_attributed_to_another_task(self):
        self.fake(101, cwd=self.wt8)
        self.assertUntouched(self.reap())

    def test_never_conflicting_attribution(self):
        self.fake(101, argv=("python3", str(self.wt8 / "ci" / "affected.py"), "--run"))
        self.assertUntouched(self.reap())

    def test_never_other_uid(self):
        self.fake(101)
        self.assertUntouched(self.reap(getuid=os.getuid() + 1))

    def test_never_non_test_argv_of_the_ended_agent(self):
        self.fake(101, argv=("python3", "/x/bin/factory", "serve"))
        self.fake(102, argv=("python3", "srv.py", "."))
        self.assertUntouched(self.reap())

    def test_never_the_caller_or_its_ancestors(self):
        self.fake(os.getpid(), ppid=os.getppid())
        self.fake(os.getppid(), ppid=1)
        self.assertUntouched(self.reap())

    def test_never_unit_mainpid(self):
        self.fake(801)
        self.assertUntouched(self.reap(extra={"FACTORY_SERVE_MAINPID": "801"}))

    def test_never_child_of_a_claude_pane(self):
        self.fake(900, argv=("claude", "x"), agent=None, cwd=self.tmp)
        self.fake(901, ppid=900)
        self.assertUntouched(self.reap())

    def test_never_pid_reuse_start_time_changed_before_the_signal(self):
        self.fake(801)
        self.env.run("config", "orphan_reap_mode", "on")
        calls, sent = [], []

        def run(mod):
            real = mod._orphan_ident

            def ident(pid):
                if pid != 801:
                    return real(pid)
                calls.append(pid)
                return real(pid) if len(calls) == 1 else (list(RUNNER), 999999)
            with mock.patch.object(mod, "_orphan_ident", ident), mock.patch.object(mod, "_serve_kill", lambda *a: sent.append(a)):
                mod.reap_agent_tests(mod.db(), "coder-t-9", "done")
        self.patched(run)
        self.assertEqual(sent, [])
        self.assertEqual(self.events("agent-tests-reaped"), [])

    def test_never_pid_reuse_argv_changed_before_the_signal(self):
        self.fake(801)
        self.env.run("config", "orphan_reap_mode", "on")
        calls, sent = [], []

        def run(mod):
            real = mod._orphan_ident

            def ident(pid):
                if pid != 801:
                    return real(pid)
                calls.append(pid)
                return real(pid) if len(calls) == 1 else (["sleep", "9"], real(pid)[1])
            with mock.patch.object(mod, "_orphan_ident", ident), mock.patch.object(mod, "_serve_kill", lambda *a: sent.append(a)):
                mod.reap_agent_tests(mod.db(), "coder-t-9", "done")
        self.patched(run)
        self.assertEqual(sent, [])


class On(Base):
    def test_on_done_term_then_kill_after_grace(self):
        self.fake(101)
        self.kill_dies = False
        sent = self.reap()
        self.assertEqual(sent, [(101, signal.SIGTERM), (101, signal.SIGKILL)])
        ev = self.events("agent-tests-reaped")
        self.assertEqual(len(ev), 1)
        d = json.loads(ev[0]["detail"])
        self.assertEqual((d["attributed_by"], d["trigger"], d["signal"], d["agent"]), ("cwd", "done", "KILL", "coder-t-9"))
        self.assertEqual(self.director_msgs(), [])

    def test_on_done_term_only_when_it_exits(self):
        self.fake(101)
        sent = self.reap()
        self.assertEqual(sent, [(101, signal.SIGTERM)])
        self.assertEqual(json.loads(self.events("agent-tests-reaped")[0]["detail"])["signal"], "TERM")

    def test_on_stop_scratchpad(self):
        self.env.sql("UPDATE agents SET status='stopped' WHERE name='coder-t-9'")
        self.fake(101, cwd=self.pad, argv=("pytest", "-q"))
        sent = self.reap(trigger="stop")
        self.assertEqual(sent, [(101, signal.SIGTERM)])
        d = json.loads(self.events("agent-tests-reaped")[0]["detail"])
        self.assertEqual((d["attributed_by"], d["trigger"]), ("scratchpad", "stop"))
        self.assertEqual(self.director_msgs(), [])

    def test_on_group_goes_when_its_leader_passes(self):
        self.fake(300, argv=("python3", "-m", "unittest", "discover"), pgrp=300)
        self.fake(301, pgrp=300, ppid=300)
        sent = self.reap()
        self.assertIn((-300, signal.SIGTERM), sent)

    def test_on_group_spared_when_leader_is_not_a_test_runner(self):
        self.fake(300, argv=("bash",), pgrp=300)
        self.fake(301, pgrp=300, ppid=300)
        sent = self.reap()
        self.assertEqual(sent, [(301, signal.SIGTERM)])

    def test_on_group_spared_when_leader_belongs_to_another_agent(self):
        self.fake(300, agent="owner", pgrp=300)
        self.fake(301, pgrp=300, ppid=300)
        sent = self.reap()
        self.assertEqual(sent, [(301, signal.SIGTERM)])


class ExitingClaude(Base):
    """T-0738: a runner whose only claude ancestor is the ended agent's own (still exiting) claude is reaped."""

    def own_claude(self, pid=900, minutes=30, agent="coder-t-9", ppid=1):
        t = time.time()
        iso = lambda x: time.strftime("%Y-%m-%dT%H:%M:%S+00:00", time.gmtime(x))
        self.env.sql("UPDATE agents SET started=?, ended=? WHERE name='coder-t-9'", (iso(t - 3600), iso(t)))
        self.fake(pid, argv=("claude", "x"), agent=agent, cwd=self.tmp, start=self.aged(minutes), ppid=ppid)

    def test_on_own_exiting_claude_ancestor_is_reaped(self):
        self.own_claude()
        self.fake(901, ppid=900)
        self.kill_dies = False
        self.assertEqual(self.reap(), [(901, signal.SIGTERM), (901, signal.SIGKILL)])
        self.assertEqual(len(self.events("agent-tests-reaped")), 1)

    def test_never_other_live_claude_ancestor(self):
        self.own_claude(agent="coder-t-8")
        self.fake(901, ppid=900)
        self.assertUntouched(self.reap())

    def test_never_same_name_claude_started_outside_the_agent_window(self):
        for minutes in (-10, 120):  # started after it ended / before it started: a later or reused claude
            with self.subTest(minutes=minutes):
                self.own_claude(minutes=minutes)
                self.fake(901, ppid=900)
                self.assertUntouched(self.reap())
                self.drop(901)
                self.drop(900)

    def test_never_own_claude_under_a_pane_multiplexer(self):
        self.fake(899, argv=("her" + "dr", "x"), agent=None, cwd=self.tmp)
        self.own_claude(ppid=899)
        self.fake(901, ppid=900)
        self.assertUntouched(self.reap())


class DryRun(Base):
    def test_dry_run_default_one_event_no_kill_and_no_second_event(self):
        self.fake(101)
        sent = self.reap(mode=None)
        self.assertEqual(sent, [])
        ev = self.events("agent-tests-would-reap")
        self.assertEqual(len(ev), 1)
        self.assertEqual(json.loads(ev[0]["detail"])["signal"], "none")
        self.assertEqual(self.reap(mode=None), [])
        self.assertEqual(len(self.events("agent-tests-would-reap")), 1)
        self.assertEqual(self.events("agent-tests-reaped"), [])

    def test_dry_run_new_start_time_logs_again(self):
        self.fake(101)
        self.reap(mode=None)
        self.drop(101)
        self.fake(101, start=2000)
        self.reap(mode=None)
        self.assertEqual(len(self.events("agent-tests-would-reap")), 2)

    def test_dry_run_off_no_scan_no_event(self):
        self.fake(101)
        self.assertUntouched(self.reap(mode="off"))

    def test_dry_run_unknown_value_acts_as_dry_run(self):
        self.fake(101)
        self.assertEqual(self.reap(mode="banana"), [])
        self.assertEqual(len(self.events("agent-tests-would-reap")), 1)


class Payload(Base):
    def test_payload_keys_are_the_orphan_keys_plus_trigger(self):
        self.fake(101)
        self.reap()
        d = json.loads(self.events("agent-tests-reaped")[0]["detail"])
        self.assertEqual(set(d), KEYS)
        self.assertEqual((d["pid"], d["start_time"], d["argv0"], d["task"]), (101, 1000, "python3", "T-9"))
        self.assertEqual(len(d["cmd_sha"]), 12)


class Swallow(Base):
    def test_callsite_reap_internal_failure_is_recorded_not_raised(self):
        self.fake(101)
        self.env.run("config", "orphan_reap_mode", "on")

        def run(mod):
            with mock.patch.object(mod, "_orphan_skip", side_effect=RuntimeError("boom")):
                mod.reap_agent_tests(mod.db(), "coder-t-9", "done")
        self.patched(run)
        self.assertEqual(len(self.events("agent-tests-reap-failed")), 1)


class Callsite(Base):
    def force_stop_fixture(self, stop_requested=True):
        self.env.sql("DELETE FROM agents WHERE name='coder-t-9'")
        self.env.seed_agent("coder-t-9", "coder", "G-9", None, "T-9", status="live", pane_id="p1")
        self.env.sql("UPDATE agents SET session_id='sid-1', stop_requested_at=? WHERE name='coder-t-9'",
                     ("2026-01-01T00:00:00+00:00" if stop_requested else None,))

    def force_stop(self, hook, scan=lambda: {}, **kw):
        out = []

        def run(mod):
            with mock.patch.object(mod, "_scan_claude", scan), mock.patch.object(mod, "pane_gone", return_value=True), \
                    mock.patch.object(mod, "reap_agent_tests", hook):
                out.append(mod.force_stop(mod.db(), "coder-t-9", "now"))
        self.patched(run, **kw)
        return out[0]

    def test_callsite_force_stop_stopped_calls_hook_with_stop(self):
        self.force_stop_fixture()
        hook = mock.Mock()
        self.assertEqual(self.force_stop(hook), ("stopped", None))
        self.assertEqual([c.args[1:] for c in hook.call_args_list], [("coder-t-9", "stop")])

    def test_callsite_force_stop_reaps_for_real_with_status_stopped(self):
        self.force_stop_fixture()
        self.fake(101)
        self.env.run("config", "orphan_reap_mode", "on")
        sent = []
        out = []

        def run(mod):
            with mock.patch.object(mod, "_scan_claude", lambda: {}), mock.patch.object(mod, "pane_gone", return_value=True), \
                    mock.patch.object(mod, "_serve_kill", lambda pid, sig: sent.append((pid, sig))):
                out.append(mod.force_stop(mod.db(), "coder-t-9", "now"))
        self.patched(run)
        self.assertEqual(out[0], ("stopped", None))
        self.assertEqual(sent[0], (101, signal.SIGTERM))
        self.assertEqual(json.loads(self.events("agent-tests-reaped")[0]["detail"])["trigger"], "stop")

    def test_callsite_force_stop_skip_and_failed_do_not_call_hook(self):
        self.force_stop_fixture(stop_requested=False)
        hook = mock.Mock()
        self.assertEqual(self.force_stop(hook), ("skip", None))
        self.force_stop_fixture()
        res = self.force_stop(hook, scan=lambda: None)
        self.assertTrue(res[0].startswith("failed:"), res)
        hook.assert_not_called()

    def test_callsite_force_stop_hook_raising_is_one_event_and_same_result(self):
        self.force_stop_fixture()
        res = self.force_stop(mock.Mock(side_effect=RuntimeError("boom")))
        self.assertEqual(res, ("stopped", None))
        ev = self.events("agent-tests-reap-failed")
        self.assertEqual(len(ev), 1)
        self.assertIn("boom", ev[0]["detail"])

    def tick(self, hook=None):
        self.env.sql("UPDATE agents SET status='done' WHERE name='coder-t-9'")
        calls = []

        def run(mod):
            seen = lambda con, name, trig: (calls.append((name, trig, con.execute(
                "SELECT status FROM agents WHERE name=?", (name,)).fetchone()["status"])), hook() if hook else None)
            with mock.patch.object(mod, "reap_agent_tests", seen):
                mod.cmd_tick(argparse.Namespace(), locked=True)
        self.patched(run)
        return calls

    def test_callsite_tick_calls_hook_after_status_is_exited(self):
        self.assertEqual(self.tick(), [("coder-t-9", "done", "exited")])

    def test_callsite_tick_hook_raising_is_one_event_and_tick_completes(self):
        def boom():
            raise RuntimeError("boom")
        self.assertEqual(len(self.tick(boom)), 1)
        self.assertEqual(len(self.events("agent-tests-reap-failed")), 1)
        self.assertEqual(len(self.env.sql("SELECT 1 FROM events WHERE action='reaped' AND entity_id='coder-t-9'")), 1)

    def test_callsite_tick_end_to_end_dry_run_event(self):
        self.fake(101)
        self.env.sql("UPDATE agents SET status='done' WHERE name='coder-t-9'")
        p = self.env.run("tick", extra_env={"FACTORY_SERVE_PROC_DIR": str(self.proc), **GRACE})
        self.assertEqual(p.returncode, 0, p.stderr)
        ev = self.events("agent-tests-would-reap")
        self.assertEqual(len(ev), 1)
        self.assertEqual(json.loads(ev[0]["detail"])["trigger"], "done")


class Alert(Base):
    def setUp(self):
        super().setUp()
        self.agent("qa-t-9", "exited", role="qa")

    def test_alert_old_runner_of_ended_agent(self):
        self.fake(101, agent="qa-t-9", start=self.aged(31))
        sent = self.alert()
        self.assertEqual(sent, [])
        ev = self.events("test-orphan-alert")
        self.assertEqual(len(ev), 1)
        d = json.loads(ev[0]["detail"])
        self.assertEqual(set(d), ALERT_KEYS)
        self.assertEqual((d["pid"], d["agent"], d["agent_status"]), (101, "qa-t-9", "exited"))
        self.assertTrue(31 * 60 <= d["age_s"] < 32 * 60)
        m = self.director_msgs()
        self.assertEqual(len(m), 1)
        self.assertEqual((m[0]["sender"], m[0]["kind"]), ("tick", "alert"))
        self.assertIn("pid 101", m[0]["body"])
        self.assertIn("qa-t-9", m[0]["body"])
        self.assertEqual(self.events("agent-tests-reaped"), [])

    def test_alert_unknown_agent_counts_as_no_live_owner(self):
        self.fake(101, agent="ghost", start=self.aged(31))
        self.alert()
        self.assertEqual(json.loads(self.events("test-orphan-alert")[0]["detail"])["agent_status"], "unknown")

    def test_alert_nothing_for_a_young_runner(self):
        self.fake(101, agent="qa-t-9", start=self.aged(29))
        self.assertEqual(self.alert(), [])
        self.assertEqual((self.events("test-orphan-alert"), self.director_msgs()), ([], []))

    def test_alert_nothing_while_the_owner_is_live_starting_or_queued(self):
        for st in ("live", "starting", "queued"):
            with self.subTest(st):
                self.env.sql("UPDATE agents SET status=? WHERE name='qa-t-9'", (st,))
                self.fake(101, agent="qa-t-9", start=self.aged(31))
                self.alert()
                self.assertEqual((self.events("test-orphan-alert"), self.director_msgs()), ([], []))
                self.drop(101)

    def test_alert_nothing_for_unset_empty_or_owner(self):
        for i, a in enumerate((None, "", "owner")):
            self.fake(101 + i, agent=a, start=self.aged(31))
        self.alert()
        self.assertEqual((self.events("test-orphan-alert"), self.director_msgs()), ([], []))

    def test_alert_nothing_for_a_non_test_process(self):
        self.fake(101, agent="qa-t-9", argv=("python3", "srv.py", "."), start=self.aged(31))
        self.alert()
        self.assertEqual((self.events("test-orphan-alert"), self.director_msgs()), ([], []))

    def test_alert_off_when_zero_or_not_an_integer(self):
        self.fake(101, agent="qa-t-9", start=self.aged(31))
        for v in ("0", "soon", "-5"):
            with self.subTest(v):
                self.env.run("config", "test_orphan_alert_s", v)
                self.alert()
                self.assertEqual((self.events("test-orphan-alert"), self.director_msgs()), ([], []))

    def test_alert_once_per_pid_and_start_time_and_throttled(self):
        self.fake(101, agent="qa-t-9", start=self.aged(31))
        self.alert()
        self.alert()  # throttle cleared: still no second event or message
        self.assertEqual((len(self.events("test-orphan-alert")), len(self.director_msgs())), (1, 1))
        self.patched(lambda mod: mod.alert_test_orphans(mod.db()))  # throttled
        self.assertEqual(len(self.director_msgs()), 1)

    def test_alert_throttle_blocks_a_new_runner_for_ten_minutes(self):
        self.fake(101, agent="qa-t-9", start=self.aged(31))
        self.alert()
        self.fake(102, agent="qa-t-9", start=self.aged(32))
        self.patched(lambda mod: mod.alert_test_orphans(mod.db()))  # throttle row still fresh
        self.assertEqual(len(self.events("test-orphan-alert")), 1)
        self.env.sql("UPDATE throttle SET ts='0' WHERE key='testorphanalert'")
        self.patched(lambda mod: mod.alert_test_orphans(mod.db()))  # aged past 10 min
        self.assertEqual(len(self.events("test-orphan-alert")), 2)

    def test_alert_two_runners_one_batched_message_and_new_start_time_alerts_again(self):
        self.fake(101, agent="qa-t-9", start=self.aged(31))
        self.fake(102, agent="qa-t-9", start=self.aged(40))
        self.alert()
        self.assertEqual(len(self.events("test-orphan-alert")), 2)
        m = self.director_msgs()
        self.assertEqual(len(m), 1)
        self.assertTrue("pid 101" in m[0]["body"] and "pid 102" in m[0]["body"])
        self.drop(101)
        self.fake(101, agent="qa-t-9", start=self.aged(35))  # same pid, new start time
        self.alert()
        self.assertEqual(len(self.events("test-orphan-alert")), 3)

    def test_alert_other_uid_is_skipped(self):
        self.fake(101, agent="qa-t-9", start=self.aged(31))
        self.alert(getuid=os.getuid() + 1)
        self.assertEqual(self.events("test-orphan-alert"), [])

    def test_alert_missing_btime_is_silent_and_never_breaks(self):
        self.fake(101, agent="qa-t-9", start=self.aged(31))
        (self.proc / "stat").unlink()
        self.assertEqual(self.alert(), [])
        self.assertEqual(self.events("test-orphan-alert"), [])

    def test_alert_is_called_by_the_tick(self):
        self.fake(101, agent="qa-t-9", start=self.aged(31))
        p = self.env.run("tick", extra_env={"FACTORY_SERVE_PROC_DIR": str(self.proc), **GRACE})
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(len(self.events("test-orphan-alert")), 1)

    def test_alert_default_config_is_1800(self):
        self.patched(lambda mod: self.assertEqual(mod.cfg(mod.db(), "test_orphan_alert_s"), "1800"))


class Digest(Base):
    def test_digest_builds_with_the_new_events(self):
        self.fake(101)
        self.reap(mode=None)
        self.reap(mode="on")
        self.fake(102, agent="qa-t-9", start=self.aged(31))
        self.agent("qa-t-9", "exited", role="qa")
        self.alert()
        self.env.sql("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES('2026-01-01T00:00:00+00:00','tick','agent','coder-t-9','agent-tests-reap-failed','x')")
        for a in ("agent-tests-would-reap", "agent-tests-reaped", "agent-tests-reap-failed", "test-orphan-alert"):
            self.assertTrue(self.events(a), a)
        out = self.env.run("digest")
        self.assertEqual(out.returncode, 0, out.stderr)


if __name__ == "__main__":
    unittest.main()
