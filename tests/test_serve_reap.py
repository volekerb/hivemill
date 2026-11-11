"""T-0286: the tick reaps stray `factory serve` processes of ended agents (argv + home/cwd both checked), and
`factory serve --ttl` makes a serve on a non-live home exit by itself."""
import os
import pwd
import re
import signal
import subprocess
import sys
import time
import unittest
from unittest import mock

from harness import FactoryTestCase

sys.path.insert(0, str(__import__("pathlib").Path(__file__).resolve().parent.parent))
from web import server  # noqa: E402

SLEEPER = "import signal, time\n{ign}\nwhile True:\n    time.sleep(0.1)\n"


class ServeReapBase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_task(status="todo", pr_url=None)
        self.tmp = e.home / "faketmp"
        self.base = self.tmp / f"claude-{os.getuid()}"
        self.base.mkdir(parents=True)
        self.slug = re.sub(r"[^A-Za-z0-9]", "-", str(e.home_dir / "worktrees")) + "-"
        self.py = e.home / "python3"
        os.symlink(sys.executable, self.py)
        self.procs = []
        self.addCleanup(self.stop_all)

    def stop_all(self):
        for p in self.procs:
            if p.poll() is None:
                p.kill()
            p.wait()

    def pad(self, task="T-0001", goal="G-001"):
        d = self.base / f"{self.slug}{goal}-{task}"
        (d / "bin").mkdir(parents=True, exist_ok=True)
        return d

    def start(self, pad, script="factory", args=("serve",), home=None, cwd=None, ignore_term=False):
        f = pad / "bin" / script
        f.write_text(SLEEPER.format(ign="signal.signal(signal.SIGTERM, signal.SIG_IGN)" if ignore_term else ""))
        env = {**os.environ, "FACTORY_HOME": str(home or pad / "home")}
        p = subprocess.Popen([str(self.py), str(f), *args], env=env, cwd=cwd or pad)
        self.procs.append(p)
        time.sleep(0.3)  # let the interpreter exec so /proc shows the final argv
        return p

    def tick(self, mainpid=""):
        return self.ok("tick", extra_env={"FACTORY_SERVE_PROC_DIR": "/proc", "FACTORY_SERVE_MAINPID": mainpid,
                                          "FACTORY_SERVE_KILL_GRACE_S": "1"})

    def ended(self, status="exited"):
        self.env.seed_agent("coder-t-0001", "coder", "G-001", None, "T-0001", status=status)

    def events(self, action="stray-serve-reaped"):
        return self.env.sql("SELECT entity_id, detail FROM events WHERE action=?", (action,))


class Reap(ServeReapBase):
    def test_ended_agents_serve_is_termed_with_event_and_pad_removed(self):
        pad = self.pad()
        self.ended()
        p = self.start(pad)
        self.tick()
        self.assertEqual(p.wait(5), -signal.SIGTERM)
        ev = self.events()
        self.assertEqual(len(ev), 1)
        self.assertEqual(ev[0]["entity_id"], "coder-t-0001")
        self.assertRegex(ev[0]["detail"], rf'"pid": {p.pid}')
        self.assertIn('"signal": "TERM"', ev[0]["detail"])
        self.assertIn(str(pad / "home"), ev[0]["detail"])
        self.assertFalse(pad.exists())
        self.assertEqual(len(self.events("tmp-cleaned")), 1)

    def test_sigkill_only_when_term_is_ignored(self):
        pad = self.pad()
        self.ended()
        p = self.start(pad, ignore_term=True)
        self.tick()
        self.assertEqual(p.wait(5), -signal.SIGKILL)
        self.assertIn('"signal": "KILL"', self.events()[0]["detail"])

    def test_cwd_alone_in_ended_pad_is_enough_when_home_is_a_scratch_dir(self):
        pad = self.pad()
        self.ended()
        other = self.env.home / "elsewhere"
        other.mkdir()
        p = self.start(pad, home=self.env.home / "elsewhere", cwd=pad)
        self.tick()
        self.assertEqual(p.wait(5), -signal.SIGTERM)
        del other

    def test_qa_style_tmp_dir_naming_the_task_is_covered(self):
        self.ended()
        d = self.tmp / "qa-t-0001-abc"
        (d / "bin").mkdir(parents=True)
        p = self.start(d)
        self.tick()
        self.assertEqual(p.wait(5), -signal.SIGTERM)
        self.assertFalse(d.exists())

    def test_live_agents_serve_is_untouched(self):
        pad = self.pad()
        self.ended("live")
        p = self.start(pad)
        self.tick()
        self.assertIsNone(p.poll())
        self.assertEqual(self.events(), [])
        self.assertTrue(pad.exists())

    def test_unreaped_done_agent_counts_as_not_ended(self):
        pad = self.pad()
        self.ended("done")
        p = self.start(pad)
        self.tick()
        self.assertIsNone(p.poll())

    def test_only_terminal_statuses_count_as_ended(self):
        for st, want in (("queued", False), ("starting", False), ("live", False), ("done", False),
                         ("exited", True), ("lost", True), ("stopped", False), ("cancelled", True)):
            with self.subTest(st):
                self.env.sql("DELETE FROM agents")
                self.ended(st)
                with self.env.load_factory() as mod:
                    self.assertEqual(mod._task_agent_ended(mod.db(), "T-0001")[0], want)

    def test_task_with_no_agent_row_is_not_ours_to_judge(self):
        pad = self.pad()
        p = self.start(pad)
        self.tick()
        self.assertIsNone(p.poll())

    def test_live_factory_home_is_untouched_even_with_cwd_in_ended_pad(self):
        pad = self.pad()
        self.ended()
        live = os.path.join(pwd.getpwuid(os.getuid()).pw_dir, "factory")
        p = self.start(pad, home=live)
        self.tick()
        self.assertIsNone(p.poll())
        self.assertEqual(self.events(), [])

    def test_unit_mainpid_is_untouched(self):
        pad = self.pad()
        self.ended()
        p = self.start(pad)
        self.tick(mainpid=str(p.pid))
        self.assertIsNone(p.poll())

    def test_argv_mismatch_is_untouched(self):
        pad = self.pad()
        self.ended()
        a = self.start(pad, script="other")             # not .../factory
        b = self.start(pad, args=("status",))           # not `serve`
        self.tick()
        self.assertIsNone(a.poll())
        self.assertIsNone(b.poll())
        self.assertEqual(self.events(), [])

    def test_pad_kept_when_another_process_uses_it(self):
        pad = self.pad()
        self.ended()
        holder = subprocess.Popen(["sleep", "30"], cwd=pad)
        self.procs.append(holder)
        p = self.start(pad)
        self.tick()
        self.assertEqual(p.wait(5), -signal.SIGTERM)
        self.assertTrue(pad.exists())
        self.assertEqual(self.events("tmp-cleaned"), [])

    def test_runs_at_most_once_per_ten_minutes(self):
        self.ended()
        self.tick()
        pad = self.pad()
        p = self.start(pad)
        self.tick()
        self.assertIsNone(p.poll())

    def test_pid_reuse_between_scan_and_signal_is_untouched(self):
        pad = self.pad()
        self.ended()
        p = self.start(pad)
        env = {"FACTORY_SERVE_PROC_DIR": "/proc", "FACTORY_SERVE_MAINPID": "", "FACTORY_SERVE_KILL_GRACE_S": "1"}
        with mock.patch.dict(os.environ, env), self.env.load_factory() as mod:
            real, calls = mod._serve_argv, []

            def argv(pid):
                if pid != p.pid:
                    return real(pid)
                calls.append(pid)
                return real(pid) if len(calls) == 1 else ["/usr/bin/python3", "/x/unrelated"]
            sent = []
            with mock.patch.object(mod, "_serve_argv", argv), mock.patch.object(mod, "_serve_kill", lambda *a: sent.append(a)):
                con = mod.db()
                mod.reap_stray_serves(con)
        self.assertEqual(sent, [])
        self.assertIsNone(p.poll())
        self.assertEqual(self.events(), [])


class Ttl(FactoryTestCase):
    def test_default_ttl_by_home(self):
        self.env.seed_goal()
        with self.env.load_factory() as mod:
            self.assertEqual(mod.serve_ttl(None), 60)      # test home is not live: default applies
            self.assertEqual(mod.serve_ttl(0), 0)          # explicit 0 = never
            self.assertEqual(mod.serve_ttl(5), 5)
            with mock.patch.object(mod, "LIVE_HOMES", {os.path.realpath(mod.HOME)}):
                self.assertEqual(mod.serve_ttl(None), 0)   # the live home never gets a TTL
                self.assertEqual(mod.serve_ttl(7), 7)

    def test_watch_ttl_stops_the_server_after_the_ttl(self):
        httpd, state, slept = mock.Mock(), {"ttl": False}, []
        server.watch_ttl(httpd, state, 300, sleep=slept.append)
        self.assertEqual(slept, [300])
        self.assertTrue(state["ttl"])
        httpd.shutdown.assert_called_once_with()

    def test_serve_flag_is_accepted_and_validated(self):
        p = self.env.run("serve", "--ttl", "x")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("--ttl", p.stderr)


if __name__ == "__main__":
    unittest.main()


class UnitProcessNeverSignalled(FactoryTestCase):
    """T-0858: the unit's own `factory serve` is never signalled, even when systemctl cannot say its MainPID."""
    UNIT_CG = "0::/user.slice/user-1000.slice/user" "@1000.service/app.slice/factory-serve.service\n"

    def fake_serve(self, proc, pid, pad, cgroup):
        d = proc / str(pid)
        d.mkdir()
        (d / "cmdline").write_bytes(b"python3\0/x/bin/factory\0serve\0")
        (d / "environ").write_bytes(f"FACTORY_HOME={pad}/home\0".encode())
        (d / "cgroup").write_text(cgroup)
        os.symlink(str(pad), d / "cwd")

    def reap(self, mainpid, cgroup, live=False):
        e = self.env
        e.seed_goal()
        e.seed_task(status="todo", pr_url=None)
        e.seed_agent("coder-t-0001", "coder", "G-001", None, "T-0001", status="exited")
        tmp, proc = e.home / "faketmp", e.home / "fakeproc"
        proc.mkdir()
        slug = re.sub(r"[^A-Za-z0-9]", "-", str(e.home_dir / "worktrees")) + "-"
        pad = tmp / f"claude-{os.getuid()}" / f"{slug}G-001-T-0001"
        pad.mkdir(parents=True)
        self.fake_serve(proc, 4242, pad, cgroup)
        env = {"FACTORY_SERVE_PROC_DIR": str(proc), "FACTORY_TMP_DIR": str(tmp), "FACTORY_SERVE_MAINPID": mainpid,
               "FACTORY_SERVE_KILL_GRACE_S": "0.1"}
        sent = []
        with mock.patch.dict(os.environ, env), self.env.load_factory() as mod:
            mod.WORKTREES = e.home_dir / "worktrees"
            if live:
                mod.LIVE_HOMES = mod.LIVE_HOMES | {str(e.home_dir.resolve())}
            with mock.patch.object(mod, "_serve_kill", lambda pid, sig: sent.append((pid, sig))):
                mod.reap_stray_serves(mod.db())
        return sent

    def test_control_a_stray_serve_outside_the_unit_is_signalled(self):
        self.assertEqual(self.reap("", "0::/user.slice/other.service\n")[0], (4242, signal.SIGTERM))

    def test_live_home_forged_serve_proc_dir_cannot_steer_the_reaper(self):
        """T-1329: on the live home the forged dir is ignored (real /proc has no pid 4242), so nothing is signalled."""
        self.assertEqual(self.reap("", "0::/user.slice/other.service\n", live=True), [])

    def test_unit_cgroup_is_untouched_when_mainpid_is_unknown(self):
        self.assertEqual(self.reap("", self.UNIT_CG), [])

    def test_mainpid_is_untouched(self):
        self.assertEqual(self.reap("4242", "0::/user.slice/other.service\n"), [])
