"""T-0031: workers may never mutate the live herdr through bin/factory (herdr() role gate)."""
import os
import pwd
import shutil
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from harness import STUBS, VALID_PR_BODY, FactoryTestCase, ancestor_pids, gates_summary

LIVE_HOME = os.path.join(pwd.getpwuid(os.getuid()).pw_dir, "factory")  # what the hook and bin/factory derive (T-0212)

WORKER_ROLES = ("pm", "coder", "qa", "reviewer", "judge", "cleaner", "staff-engineer")
MUTATING = [("agent", "start", "x"), ("pane", "close", "p1"), ("pane", "split", "p1"), ("pane", "run", "p1", "ls"),
            ("tab", "create"), ("tab", "rename", "t1", "x"), ("workspace", "create"), ("server", "stop"),
            ("bogus", "verb")]
PR = "https://github.com/zz-test/repo/pull/1"
GREEN = [{"name": "unittest", "status": "COMPLETED", "conclusion": "SUCCESS"}]
ALLOWED = [("agent", "list"), ("agent", "read", "x"), ("pane", "list"), ("pane", "process-info", "--pane", "p1"),
           ("tab", "list", "--workspace", "w1"), ("workspace", "list"), ("pane", "report-metadata", "p1"),
           ("workspace", "report-metadata", "w1"), ("agent", "prompt", "x", "hi")]


class WorkerGateBase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(lead="zz-test-lead")
        e.seed_task("T-0001", role="coder", status="in_progress", team="g-001-a")
        for name, role in (("zz-test-lead", "lead"), ("zz-test-director", "director"), ("zz-test-boss", "boss")):
            e.seed_agent(name, role, goal="G-001", team="g-001-a")
        for r in WORKER_ROLES:
            e.seed_agent(f"zz-test-{r}", r, goal="G-001", team="g-001-a", task="T-0001")

    def herdr_call(self, argv, agent, extra=None, check=True):
        """Run one herdr() call in-process as `agent`; returns (exit code or None, result)."""
        env = {k: v for k, v in {"FACTORY_AGENT": agent, **(extra or {})}.items() if v is not None}
        with self.env.load_factory() as f:
            with mock.patch.dict(os.environ, env):
                try:
                    return None, f.herdr(*argv, check=check)
                except SystemExit as x:
                    return x.code, None

    def herdr_argvs(self):
        return [c["argv"] for c in self.env.calls("herdr")]

    def denied_events(self):
        return self.env.sql("SELECT * FROM events WHERE action='herdr-denied'")

    def proc(self, pid, ppid, argv, comm="x"):
        d = self.env.proc_dir / str(pid)
        d.mkdir()
        (d / "cmdline").write_bytes(b"\0".join(x.encode() for x in argv) + b"\0")
        (d / "stat").write_text(f"{pid} ({comm}) S {ppid} {pid} {pid} 0 -1 0\n")


class HerdrGateTests(WorkerGateBase):
    def test_herdr_denied_for_each_worker_role_on_each_mutating_verb(self):
        for role in WORKER_ROLES:
            for argv in MUTATING:
                with self.subTest(role=role, verb=argv):
                    before = len(self.denied_events())
                    code, _ = self.herdr_call(argv, f"zz-test-{role}", {"FACTORY_ROLE": role}, check=False)
                    self.assertEqual(code, 3)
                    self.assertEqual(len(self.denied_events()), before + 1)
        self.assertEqual(self.herdr_argvs(), [])

    def test_herdr_allowed_verbs_pass_for_workers(self):
        for argv in ALLOWED:
            with self.subTest(verb=argv):
                code, _ = self.herdr_call(argv, "zz-test-coder", {"FACTORY_ROLE": "coder"})
                self.assertIsNone(code)
                self.assertEqual(self.herdr_argvs()[-1], list(argv))
        self.assertEqual(self.denied_events(), [])

    def test_worker_detected_by_process_ancestry(self):
        me = os.getpid()
        bash, cl = ancestor_pids(me)
        self.proc(me, bash, ["python3"])
        self.proc(bash, cl, ["bash"])
        self.proc(cl, 1, ["claude", "--settings", "/home/x/factory/roles/settings/coder.json", "--resume", "s"])
        code, _ = self.herdr_call(("pane", "close", "p1"), "zz-test-director", check=False)
        self.assertEqual(code, 3)
        self.assertEqual(self.herdr_argvs(), [])
        self.assertEqual(len(self.denied_events()), 1)

    def test_flagless_lead_ancestry_not_worker(self):
        me = os.getpid()
        bash, cl = ancestor_pids(me)
        self.proc(me, bash, ["python3"])
        self.proc(bash, cl, ["bash"])
        self.proc(cl, 1, ["claude", "--resume", "sess-lead"])
        code, _ = self.herdr_call(("agent", "start", "x"), "zz-test-lead")
        self.assertIsNone(code)
        self.assertEqual(self.herdr_argvs(), [["agent", "start", "x"]])

    def test_worker_pane_spoofing_director_denied(self):
        self.env.sql("UPDATE agents SET pane_id='p9' WHERE name='zz-test-coder'")
        code, _ = self.herdr_call(("pane", "close", "p1"), "zz-test-director", {"HERDR_PANE_ID": "p9"}, check=False)
        self.assertEqual(code, 3)
        self.assertEqual(self.herdr_argvs(), [])
        self.assertEqual(len(self.denied_events()), 1)
        self.assertEqual(len(self.env.sql("SELECT * FROM events WHERE action='identity-spoof'")), 1)

    def test_worker_temp_home_real_herdr_mutating_refused(self):
        real = self.env.home / "realbin"
        real.mkdir()
        shutil.copy2(STUBS / "herdr", real / "herdr")  # a "real" herdr: not under tests/stubs
        for argv in MUTATING[:3]:
            with self.subTest(verb=argv):
                path = f"{real}{os.pathsep}{os.environ['PATH']}"
                code, _ = self.herdr_call(argv, "zz-test-coder", {"FACTORY_ROLE": "coder", "PATH": path})
                self.assertEqual(code, 3)
        self.assertEqual(self.herdr_argvs(), [])
        self.assertEqual(len(self.denied_events()), 3)

    def test_worker_temp_home_default_socket_is_dead(self):
        with self.env.load_factory() as f:
            with mock.patch.dict(os.environ, {"FACTORY_ROLE": "coder", "FACTORY_AGENT": "zz-test-coder"}):
                self.assertEqual(f.herdr_socket(), "/proc/nonexistent/worker-no-herdr.sock")
        with self.env.load_factory() as f:
            with mock.patch.dict(os.environ, {"FACTORY_AGENT": ""}):
                os.environ.pop("FACTORY_AGENT")
                os.environ.pop("FACTORY_ROLE", None)
                self.assertEqual(f.herdr_socket(), os.path.join(str(self.env.user_home), ".config/herdr/herdr.sock"))

    def test_worker_temp_home_configured_live_socket_is_dead(self):
        import pwd
        real = pwd.getpwuid(os.getuid()).pw_dir
        user = pwd.getpwuid(os.getuid()).pw_name
        link = Path(tempfile.mkdtemp()) / "cfg-link"  # a symlinked parent dir of the live socket dir
        self.addCleanup(shutil.rmtree, str(link.parent), True)
        link.symlink_to(Path(real) / ".config")
        worker = {"FACTORY_ROLE": "coder", "FACTORY_AGENT": "zz-test-coder"}
        spellings = ["~/.config/herdr/herdr.sock", f"{real}/.config/herdr/herdr.sock",
                     f"{real}/.config/herdr/../herdr/herdr.sock", f"{real}//.config/./herdr/herdr.sock",
                     f"~{user}/.config/herdr/herdr.sock", f"~{user}//.config/herdr/../herdr/herdr.sock",
                     f"{real}/.config/herdr/../../.config/herdr/herdr.sock", f"{link}/herdr/herdr.sock",
                     f"{link}/herdr//herdr.sock/"]
        for val in spellings:
            with self.subTest(val=val):
                self.ok("config", "herdr_socket", val)
                with self.env.load_factory() as f:
                    with mock.patch.dict(os.environ, worker):
                        self.assertEqual(f.herdr_socket(), "/proc/nonexistent/worker-no-herdr.sock")
        self.ok("config", "herdr_socket", f"{real}/.config/herdr/other.sock")  # a different path stays honoured
        with self.env.load_factory() as f:
            with mock.patch.dict(os.environ, worker):
                self.assertEqual(f.herdr_socket(), f"{real}/.config/herdr/other.sock")
        self.ok("config", "herdr_socket", f"{real}/.config/herdr/herdr.sock")  # a non-worker keeps it
        with self.env.load_factory() as f:
            os.environ.pop("FACTORY_AGENT", None)
            os.environ.pop("FACTORY_ROLE", None)
            self.assertEqual(f.herdr_socket(), f"{real}/.config/herdr/herdr.sock")

    def test_worker_temp_home_explicit_socket_honoured(self):
        self.ok("config", "herdr_socket", "/run/user/1000/custom.sock")
        with self.env.load_factory() as f:
            with mock.patch.dict(os.environ, {"FACTORY_ROLE": "coder", "FACTORY_AGENT": "zz-test-coder"}):
                self.assertEqual(f.herdr_socket(), "/run/user/1000/custom.sock")

    def test_stub_exemption_only_for_real_stub(self):
        # a real tests/stubs path: allowed
        stubs = str(STUBS)
        code, _ = self.herdr_call(("pane", "close", "p1"), "zz-test-coder",
                                  {"FACTORY_ROLE": "coder", "PATH": f"{stubs}{os.pathsep}{os.environ['PATH']}"})
        self.assertIsNone(code)
        self.assertEqual(self.herdr_argvs(), [["pane", "close", "p1"]])
        # a tests/stubs/herdr symlink to a "real" herdr outside stubs: denied
        real = self.env.home / "realbin"
        real.mkdir()
        shutil.copy2(STUBS / "herdr", real / "herdr")
        fake_stubs = self.env.home / "x" / "tests" / "stubs"
        fake_stubs.mkdir(parents=True)
        (fake_stubs / "herdr").symlink_to(real / "herdr")
        code, _ = self.herdr_call(("pane", "close", "p2"), "zz-test-coder",
                                  {"FACTORY_ROLE": "coder", "PATH": f"{fake_stubs}{os.pathsep}{os.environ['PATH']}"})
        self.assertEqual(code, 3)
        self.assertEqual(len(self.herdr_argvs()), 1)
        # a hardlink of the real herdr inside a tests/stubs dir, the real dir also on PATH (same inode): denied
        link_stubs = self.env.home / "y" / "tests" / "stubs"
        link_stubs.mkdir(parents=True)
        os.link(real / "herdr", link_stubs / "herdr")
        path = os.pathsep.join([str(link_stubs), str(real), os.environ["PATH"]])
        code, _ = self.herdr_call(("pane", "close", "p3"), "zz-test-coder", {"FACTORY_ROLE": "coder", "PATH": path})
        self.assertEqual(code, 3)
        self.assertEqual(len(self.herdr_argvs()), 1)


    def test_worker_command_exempt_only_with_stub_and_temp_home(self):
        stubs = f"{STUBS}{os.pathsep}{os.environ['PATH']}"
        real = self.env.home / "realbin"
        real.mkdir()
        shutil.copy2(STUBS / "herdr", real / "herdr")
        real_path = f"{real}{os.pathsep}{os.environ['PATH']}"
        env = {"FACTORY_AGENT": "zz-test-coder", "FACTORY_ROLE": "coder"}

        def run(path, home=None):
            with self.env.load_factory() as f:
                with mock.patch.dict(os.environ, {**env, "PATH": path}):
                    if home:
                        f.HOME = home
                    try:
                        f.refuse_worker("up")
                        return None
                    except SystemExit as x:
                        return x.code

        self.assertIsNone(run(stubs))  # stub + temp home
        self.assertEqual(run(stubs, Path(LIVE_HOME)), 3)  # stub + live home
        self.assertEqual(run(real_path), 3)  # real herdr + temp home


class WorkerCommandTests(WorkerGateBase):
    def worker(self, role="coder"):
        return {"agent": f"zz-test-{role}", "extra_env": {"FACTORY_ROLE": role}}

    def snapshot(self):
        return [self.env.sql(f"SELECT * FROM {t}") and [tuple(r) for r in self.env.sql(f"SELECT * FROM {t}")]
                for t in ("agents", "events", "messages", "tasks")]

    def test_worker_tick_up_relaunch_spawn_refused_without_db_change(self):
        before = self.snapshot()
        for args in (("tick",), ("up",), ("relaunch", "zz-test-lead"), ("spawn", "pm", "--name", "zz-test-x")):
            for who in ("zz-test-coder", "zz-test-director"):  # a spoofed director is still a worker
                with self.subTest(args=args, who=who):
                    p = self.refused(3, *args, agent=who, extra_env={"FACTORY_ROLE": "coder"})
                    self.assertIn("workers may not run", p.stderr)
        self.assertEqual(self.snapshot(), before)
        self.assertEqual(self.herdr_argvs(), [])

    def test_lead_and_owner_paths_unchanged(self):
        p = self.ok("spawn", "pm", "--name", "zz-test-pm2", agent="zz-test-lead")
        self.assertIn("SPAWNED zz-test-pm2", p.stdout)
        self.ok("tick")
        self.ok("up")
        self.assertEqual(self.denied_events(), [])

    def test_worker_msg_then_owner_tick_nudges(self):
        e = self.env
        e.sql("UPDATE agents SET pane_id='px0', session_id='s-lead' WHERE name='zz-test-lead'")
        e.set_herdr(agents=[{"name": "zz-test-lead", "agent": "claude", "pane_id": "px0", "agent_status": "idle",
                             "agent_session": {"value": "s-lead"}}])
        self.ok("msg", "lead:g-001-a", "hello", "body", **self.worker())
        self.assertEqual(self.herdr_argvs(), [])
        self.ok("tick")
        prompts = [a for a in self.herdr_argvs() if a[:2] == ["agent", "prompt"]]
        self.assertTrue(prompts, self.herdr_argvs())

    def test_worker_done_inbox_verdict_report_task_note_still_work(self):
        e = self.env
        e.seed_project()
        e.sql("UPDATE goals SET project_id='zz-test-proj' WHERE id='G-001'")
        e.use_git_head()
        e.seed_pr(checks=GREEN, body=VALID_PR_BODY)
        for role in ("coder", "qa", "reviewer", "judge"):
            self.ok("inbox", **self.worker(role))
            self.ok("agents", **self.worker(role))
            self.ok("res", **self.worker(role))
            self.ok("task", "show", "T-0001", **self.worker(role))
            self.ok("task", "list", **self.worker(role))
            self.ok("digest", **self.worker(role))
        self.ok("task", "note", "T-0001", "a note", **self.worker())
        self.ok("task", "update", "T-0001", "--status", "in_review", "--pr", PR, **self.worker())
        self.ok("verdict", "T-0001", "--pass", "--summary", gates_summary(), **self.worker("qa"))
        self.ok("verdict", "T-0001", "--pass", "--summary", "ok", **self.worker("reviewer"))
        self.ok("report", "headline", "body", **self.worker("judge"))
        self.ok("judge", "T-0001", "--merge", "--reason", "green", **self.worker("judge"))
        for role in ("coder", "qa", "reviewer", "judge"):
            self.ok("done", "finished", **self.worker(role))
        self.assertEqual(self.denied_events(), [])
        self.assertTrue(all(a[:2] == ["agent", "list"] for a in self.herdr_argvs()), self.herdr_argvs())

    def test_flagless_live_shape_lead_spawn_and_director_relaunch(self):
        # the live HQ/leads: no FACTORY_AGENT, no --settings, identity recovered from the pane id
        e = self.env
        e.sql("UPDATE agents SET pane_id='pl', session_id='s-lead' WHERE name='zz-test-lead'")
        e.sql("UPDATE agents SET pane_id='pd', session_id='s-dir' WHERE name='zz-test-director'")
        live = {"CLAUDECODE": "1"}
        p = self.ok("spawn", "pm", "--name", "zz-test-pm2", extra_env={**live, "HERDR_PANE_ID": "pl"})
        self.assertIn("SPAWNED zz-test-pm2", p.stdout)
        self.ok("relaunch", "zz-test-lead", "--dry-run", extra_env={**live, "HERDR_PANE_ID": "pd"})
        self.assertEqual(self.denied_events(), [])

    def test_cleaner_may_clean_other_workers_may_not(self):
        for role in WORKER_ROLES:
            if role == "cleaner":
                continue
            with self.subTest(role=role):
                p = self.refused(3, "clean", "--dry-run", **self.worker(role))
                self.assertIn("only the cleaner", p.stderr)
        p = self.ok("clean", "--dry-run", **self.worker("cleaner"))
        self.assertNotIn("may not", p.stderr)


if __name__ == "__main__":
    unittest.main()
