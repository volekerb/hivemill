"""T-0382: the tick reaps orphan processes of ended workers (cwd, claude scratchpad or argv in the task's worktree,
FACTORY_AGENT of an ended agent of that task) per config orphan_reap_mode, and leaves everything else alone.
Fake /proc fixtures + the _serve_kill seam; two tests run a real child."""
import hashlib
import json
import os
import re
import signal
import unittest
from unittest import mock

from harness import FactoryTestCase

GRACE = {"FACTORY_SERVE_KILL_GRACE_S": "0.3", "FACTORY_SERVE_MAINPID": ""}
KEYS = {"pid", "start_time", "argv0", "cmd_sha", "cwd", "worktree", "task", "agent", "attributed_by", "signal"}


class OrphanBase(FactoryTestCase):
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
        self.wt = e.home_dir / "worktrees" / "G-9-T-9"
        self.wt8 = e.home_dir / "worktrees" / "G-9-T-8"
        slug = re.sub(r"[^A-Za-z0-9]", "-", str(e.home_dir / "worktrees")) + "-"
        self.pad = self.tmp / f"claude-{os.getuid()}" / f"{slug}G-9-T-9" / "sess" / "scratchpad"
        for d in (self.wt, self.wt8, self.pad):
            d.mkdir(parents=True)
        self.agent("qa-t-9", "exited")
        self.alive = set()

    def agent(self, name, status, task="T-9", role="qa"):
        self.env.seed_agent(name, role, "G-9", None, task, status=status)

    def fake(self, pid, cwd=None, argv=("python3", "srv.py", "."), agent="qa-t-9", ppid=1, uid=None, start=1000, env=None, pgrp=None):
        d = self.proc / str(pid)
        d.mkdir()
        (d / "cmdline").write_bytes(b"\0".join(x.encode() for x in argv) + b"\0")
        e = dict(env or {})
        if agent is not None:
            e["FACTORY_AGENT"] = agent
        (d / "environ").write_bytes(b"\0".join(f"{k}={v}".encode() for k, v in e.items()) + b"\0")
        (d / "stat").write_text(f"{pid} (x) S {ppid} {pgrp or 1} 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 {start}\n")
        os.symlink(str(cwd or self.wt), d / "cwd")
        self.alive.add(pid)
        return pid

    def run_reaper(self, mode="on", mainpid="", getuid=None, kill_dies=True, clear=True, extra_env=None):
        """reap_orphans against the fake /proc; returns the kill-seam calls. A TERM removes the fake pid unless kill_dies is False."""
        if mode:
            self.env.run("config", "orphan_reap_mode", mode)
        if clear:
            self.env.sql("DELETE FROM throttle")
        sent = []

        def kill(pid, sig):
            sent.append((pid, sig))
            if kill_dies or sig == signal.SIGKILL:
                p = self.proc / str(abs(pid))
                if p.exists():
                    for f in p.iterdir():
                        f.unlink()
                    p.rmdir()
        env = {"FACTORY_SERVE_PROC_DIR": str(self.proc), "FACTORY_TMP_DIR": str(self.tmp), **GRACE,
               "FACTORY_SERVE_MAINPID": mainpid, **(extra_env or {})}
        with mock.patch.dict(os.environ, env), self.env.load_factory() as mod:
            os.environ.update(env)
            mod.WORKTREES = self.env.home_dir / "worktrees"
            patches = [mock.patch.object(mod, "_serve_kill", kill)]
            if getuid:
                patches.append(mock.patch.object(os, "getuid", return_value=getuid))
            for p in patches:
                p.start()
            try:
                mod.reap_orphans(mod.db())
            finally:
                for p in patches:
                    p.stop()
        return sent

    def events(self, action="orphan-reaped"):
        return [dict(r) for r in self.env.sql("SELECT entity_id, detail FROM events WHERE action=?", (action,))]

    def alerts(self):
        return self.env.sql("SELECT body FROM messages WHERE recipient='director' AND subject LIKE 'reaped%'")

    def assertUntouched(self, sent):
        self.assertEqual(sent, [])
        self.assertEqual(self.events(), [])
        self.assertEqual(self.events("orphan-would-reap"), [])


class Cwd(OrphanBase):
    def test_cwd_term_event_and_one_batched_alert(self):
        a, b = self.fake(101), self.fake(102, argv=("sleep", "9"))
        sent = self.run_reaper()
        self.assertEqual(sent, [(101, signal.SIGTERM), (102, signal.SIGTERM)])
        ev = self.events()
        self.assertEqual(len(ev), 2)
        self.assertEqual(json.loads(ev[0]["detail"])["attributed_by"], "cwd")
        self.assertEqual(len(self.alerts()), 1)
        self.assertIn(f"pid {a}", self.alerts()[0]["body"])
        self.assertIn(f"pid {b}", self.alerts()[0]["body"])

    def test_cwd_kill_after_grace_when_term_is_ignored(self):
        self.fake(101)
        sent = self.run_reaper(kill_dies=False)
        self.assertEqual(sent, [(101, signal.SIGTERM), (101, signal.SIGKILL)])
        self.assertEqual(json.loads(self.events()[0]["detail"])["signal"], "KILL")

    def test_cwd_every_ended_status(self):
        for i, st in enumerate(("done", "exited", "lost", "cancelled")):
            with self.subTest(st):
                self.env.sql("DELETE FROM agents")
                self.agent("qa-t-9", st)
                self.fake(200 + i)
                self.assertEqual(self.run_reaper(), [(200 + i, signal.SIGTERM)])

    def test_cwd_group_goes_when_its_leader_passes(self):
        self.fake(300, pgrp=300)
        self.fake(301, pgrp=300, ppid=300)
        sent = self.run_reaper()
        self.assertIn((-300, signal.SIGTERM), sent)

    def test_cwd_group_spared_when_leader_is_not_an_orphan(self):
        self.fake(300, pgrp=300, cwd=self.tmp)
        self.fake(301, pgrp=300, ppid=300)
        self.assertEqual(self.run_reaper(), [(301, signal.SIGTERM)])


class T0319(OrphanBase):
    def serve(self, pid, home):
        return self.fake(pid, argv=("python3", str(self.wt / "factory"), "serve"), env={"FACTORY_HOME": str(home)})

    def test_t0319_demo_serve_reaped_and_tmp_home_removed(self):
        home = self.tmp / "demo-abc"
        home.mkdir()
        (home / "factory.db").write_text("x")
        self.serve(401, home)
        self.assertEqual(self.run_reaper(), [(401, signal.SIGTERM)])
        self.assertFalse(home.exists())
        self.assertEqual(len(self.events("tmp-cleaned")), 1)

    def test_t0319_tmp_home_kept_while_a_process_uses_it(self):
        home = self.tmp / "demo-abc"
        home.mkdir()
        self.serve(401, home)
        self.fake(402, cwd=home, agent=None)  # someone's cwd is in the home
        self.assertEqual(self.run_reaper(), [(401, signal.SIGTERM)])
        self.assertTrue(home.exists())
        self.assertEqual(self.events("tmp-cleaned"), [])


class Scratchpad(OrphanBase):
    def test_scratchpad_attribution_t0458_shape(self):
        self.fake(2305404, cwd=self.pad, ppid=797)
        self.assertEqual(self.run_reaper(), [(2305404, signal.SIGTERM)])
        ev = [json.loads(r["detail"]) for r in self.events()]
        self.assertEqual([e["attributed_by"] for e in ev], ["scratchpad"])
        self.assertEqual(ev[0]["task"], "T-9")

    def test_scratchpad_top_level_tmp_name_is_not_attributed(self):
        top = self.tmp / "qa-t-9-x"
        top.mkdir()
        self.fake(501, cwd=top)
        self.assertUntouched(self.run_reaper())

    def test_scratchpad_of_another_uid_dir_is_not_attributed(self):
        other = self.tmp / "claude-0" / self.pad.parts[-4] / "sess"
        other.mkdir(parents=True)
        self.fake(501, cwd=other)
        self.assertUntouched(self.run_reaper())


class Argv(OrphanBase):
    def test_argv_attribution_and_other_tasks_agent_untouched(self):
        self.fake(601, cwd=self.tmp, argv=("python3", str(self.wt / "srv.py")))
        self.agent("qa-t-8", "exited", "T-8")
        self.fake(602, cwd=self.tmp, argv=("python3", str(self.wt / "srv.py")), agent="qa-t-8")
        self.assertEqual(self.run_reaper(), [(601, signal.SIGTERM)])
        self.assertEqual(json.loads(self.events()[0]["detail"])["attributed_by"], "argv")

    def test_argv_relative_path_resolves_against_cwd(self):
        self.fake(601, cwd=self.wt.parent, argv=("python3", "G-9-T-9/srv.py"))
        self.assertEqual(self.run_reaper(), [(601, signal.SIGTERM)])

    def test_conflict_cwd_and_argv_name_different_tasks(self):
        self.fake(701, cwd=self.wt, argv=("python3", str(self.wt8 / "srv.py")))
        self.assertUntouched(self.run_reaper())

    def test_conflict_scratchpad_and_argv_name_different_tasks(self):
        self.fake(701, cwd=self.pad, argv=("python3", str(self.wt8 / "srv.py")))
        self.assertUntouched(self.run_reaper())


class Never(OrphanBase):
    def test_never_agent_unset(self):
        self.fake(801, agent=None)
        self.assertUntouched(self.run_reaper())

    def test_never_agent_empty(self):
        self.fake(801, agent="")
        self.assertUntouched(self.run_reaper())

    def test_never_agent_owner(self):
        self.fake(801, agent="owner", cwd=self.pad)
        self.assertUntouched(self.run_reaper())

    def test_never_hq_name_director(self):
        self.agent("director", "exited", role="director")
        self.fake(801, agent="director")
        self.assertUntouched(self.run_reaper())

    def test_never_live_agent_of_the_task(self):
        self.agent("coder-t-9", "live", role="coder")
        self.fake(801)
        self.assertUntouched(self.run_reaper())

    def test_never_queued_or_starting_agent_of_the_task(self):
        for st in ("queued", "starting"):
            with self.subTest(st):
                self.env.sql("DELETE FROM agents WHERE name='coder-t-9'")
                self.agent("coder-t-9", st, role="coder")
                self.fake(801 if st == "queued" else 802)
                self.assertUntouched(self.run_reaper())

    def test_never_stopped_agent(self):
        self.env.sql("DELETE FROM agents")
        self.agent("qa-t-9", "stopped")
        self.fake(801)
        self.assertUntouched(self.run_reaper())

    def test_never_other_uid(self):
        self.fake(801)
        self.assertUntouched(self.run_reaper(getuid=os.getuid() + 1))

    def test_never_cwd_and_argv_outside_worktrees_and_scratchpads(self):
        self.fake(801, cwd=self.tmp, argv=("python3", "srv.py"))
        self.fake(802, cwd=self.wt.parent, argv=("python3", "srv.py"))
        self.assertUntouched(self.run_reaper())

    def test_never_top_level_tmp_cwd_naming_the_task(self):
        top = self.tmp / "qa-t-9-x"
        top.mkdir()
        self.fake(801, cwd=top)
        self.assertUntouched(self.run_reaper())

    def test_never_live_home_serve(self):
        live = os.path.realpath(os.path.expanduser("~/factory"))
        self.fake(801, argv=("python3", str(self.wt / "factory"), "serve"), env={"FACTORY_HOME": live})
        env = {"FACTORY_SERVE_PROC_DIR": str(self.proc), **GRACE}
        sent = []
        with mock.patch.dict(os.environ, env), self.env.load_factory() as mod:
            mod.WORKTREES = self.env.home_dir / "worktrees"
            self.env.run("config", "orphan_reap_mode", "on")
            with mock.patch.object(mod, "LIVE_HOMES", {live}), mock.patch.object(mod, "_serve_kill", lambda *a: sent.append(a)):
                mod.reap_orphans(mod.db())
        self.assertUntouched(sent)

    def test_never_unit_mainpid(self):
        self.fake(801)
        self.assertUntouched(self.run_reaper(mainpid="801"))

    def test_never_the_tick_or_its_ancestors(self):
        self.fake(os.getpid(), ppid=os.getppid())
        self.fake(os.getppid(), ppid=1)
        self.assertUntouched(self.run_reaper())

    def test_never_child_of_a_claude_pane(self):
        self.fake(900, argv=("claude", "x"), agent=None, cwd=self.tmp)
        self.fake(901, ppid=900)
        self.assertUntouched(self.run_reaper())

    def test_never_child_of_a_live_agents_process(self):
        self.agent("coder-t-7", "live", "T-8", role="coder")
        self.fake(900, agent="coder-t-7", cwd=self.tmp)
        self.fake(901, ppid=900)
        self.assertUntouched(self.run_reaper())

    def test_never_pid_reuse_start_time_changed_before_the_signal(self):
        self.fake(801)
        with mock.patch.dict(os.environ, {"FACTORY_SERVE_PROC_DIR": str(self.proc), **GRACE}), self.env.load_factory() as mod:
            mod.WORKTREES = self.env.home_dir / "worktrees"
            self.env.run("config", "orphan_reap_mode", "on")
            real, calls = mod._orphan_ident, []

            def ident(pid):
                if pid != 801:
                    return real(pid)
                calls.append(pid)
                return real(pid) if len(calls) == 1 else (["python3", "srv.py", "."], 999999)
            sent = []
            with mock.patch.object(mod, "_orphan_ident", ident), mock.patch.object(mod, "_serve_kill", lambda *a: sent.append(a)):
                mod.reap_orphans(mod.db())
        self.assertUntouched(sent)

    def test_never_more_than_once_per_ten_minutes(self):
        self.run_reaper()
        self.fake(801)
        self.assertUntouched(self.run_reaper(clear=False))


class DryRun(OrphanBase):
    def test_dry_run_is_the_default_one_event_no_kill_no_alert(self):
        self.fake(2305404, cwd=self.pad)
        sent = self.run_reaper(mode=None)
        self.assertEqual(sent, [])
        ev = self.events("orphan-would-reap")
        self.assertEqual(len(ev), 1)
        self.assertEqual(json.loads(ev[0]["detail"])["signal"], "none")
        self.assertEqual(self.alerts(), [])
        self.assertEqual(self.run_reaper(mode=None), [])  # second run, throttle cleared
        self.assertEqual(len(self.events("orphan-would-reap")), 1)
        self.assertEqual(self.events(), [])

    def test_dry_run_new_start_time_logs_again(self):
        self.fake(801, start=1000)
        self.run_reaper(mode=None)
        for f in (self.proc / "801").iterdir():
            f.unlink()
        (self.proc / "801").rmdir()
        self.fake(801, start=2000)
        self.run_reaper(mode=None)
        self.assertEqual(len(self.events("orphan-would-reap")), 2)


    def test_dry_run_start_time_that_extends_a_logged_one_logs_again(self):
        self.fake(801, start=345)
        self.run_reaper(mode=None)
        for f in (self.proc / "801").iterdir():
            f.unlink()
        (self.proc / "801").rmdir()
        self.fake(801, start=3456)
        self.run_reaper(mode=None)
        self.assertEqual(len(self.events("orphan-would-reap")), 2)


class Mode(OrphanBase):
    def test_mode_off_no_scan_no_event(self):
        self.fake(801)
        self.assertUntouched(self.run_reaper(mode="off"))
        self.assertEqual(self.env.sql("SELECT 1 FROM throttle WHERE key='orphanreap'"), [])

    def test_mode_on_reaps(self):
        self.fake(801)
        self.assertEqual(self.run_reaper(mode="on"), [(801, signal.SIGTERM)])

    def test_mode_unknown_acts_as_dry_run(self):
        self.fake(801)
        self.assertEqual(self.run_reaper(mode="banana"), [])
        self.assertEqual(len(self.events("orphan-would-reap")), 1)

    def test_mode_default_row_is_dry_run(self):
        with self.env.load_factory() as mod:
            self.assertEqual(mod.DEFAULT_CONFIG["orphan_reap_mode"], "dry-run")


class Payload(OrphanBase):
    def test_payload_exact_keys_and_cmd_sha(self):
        argv = ("python3", "srv.py", ".")
        self.fake(2305404, cwd=self.pad, argv=argv, start=4242)
        for mode, ev_action, sig in (("dry-run", "orphan-would-reap", "none"), ("on", "orphan-reaped", "TERM")):
            self.run_reaper(mode=mode)
            body = json.loads(self.events(ev_action)[0]["detail"])
            self.assertEqual(set(body), KEYS)
            self.assertEqual(body["cmd_sha"], hashlib.sha256(b"\0".join(x.encode() for x in argv)).hexdigest()[:12])
            self.assertEqual((body["pid"], body["start_time"], body["argv0"], body["task"], body["agent"], body["signal"]),
                             (2305404, 4242, "python3", "T-9", "qa-t-9", sig))
            self.assertEqual(body["worktree"], str(self.env.home_dir / "worktrees" / "G-9-T-9"))


class Digest(OrphanBase):
    def test_digest_builds_with_orphan_events(self):
        self.fake(801)
        self.run_reaper(mode=None)
        self.fake(802)
        self.run_reaper(mode="on")
        self.assertTrue(self.events("orphan-would-reap") and self.events())
        out = self.env.run("digest")
        self.assertEqual(out.returncode, 0, out.stderr)


if __name__ == "__main__":
    unittest.main()
