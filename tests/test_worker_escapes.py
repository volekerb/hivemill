"""T-0036: close the worker -> live-herdr escapes found in review of PR #23 (E2, plus nits 2 and 3)."""
import os
import pwd
import unittest
from pathlib import Path
from unittest import mock

from harness import STUBS, ancestor_pids
from test_worker_gate import WORKER_ROLES, WorkerGateBase

LIVE = Path(os.path.join(pwd.getpwuid(os.getuid()).pw_dir, "factory"))  # what bin/factory derives (T-0212)
WORKER_SETTINGS = "/home/x/factory/roles/settings/coder.json"


class WorkerEscapeTests(WorkerGateBase):
    def worker_by_ancestry(self, settings=WORKER_SETTINGS):
        """The test process is a descendant of a worker claude (the CLI subprocess starts at its parent)."""
        me = os.getpid()
        (bash,) = ancestor_pids(me, 1)
        self.proc(me, bash, ["python3"])
        self.proc(bash, 1, ["claude", "--settings", settings, "--resume", "s"])

    def cli_worker_ancestry(self, settings=WORKER_SETTINGS):
        self.proc(os.getpid(), 1, ["claude", "--settings", settings, "--resume", "s"])

    def wrapper_stub(self):
        """E2: a worker-made wrapper under a */tests/stubs/ dir (the Edit tool is not seen by the Bash hook)."""
        d = self.env.home / "w" / "tests" / "stubs"
        d.mkdir(parents=True)
        (d / "herdr").write_text(f'#!/bin/sh\nexec python3 "{STUBS / "herdr"}" "$@"\n')
        (d / "herdr").chmod(0o755)
        return f"{d}{os.pathsep}{os.environ['PATH']}"

    def in_home(self, home, argv, path):
        with self.env.load_factory() as f:
            f.HOME = home
            with mock.patch.dict(os.environ, {"FACTORY_AGENT": "zz-test-director", "PATH": path,
                                                  "FACTORY_ROLE": "coder"}):  # live home ignores the fake /proc (T-1322)
                try:
                    return None, f.worker_stub_exempt(), f.herdr(*argv)
                except SystemExit as x:
                    return x.code, f.worker_stub_exempt(), None

    def test_herdr_gate_stub_in_live_home_refused(self):
        self.worker_by_ancestry()
        code, exempt, _ = self.in_home(LIVE, ("agent", "start", "x"), self.wrapper_stub())
        self.assertEqual(code, 3)
        self.assertFalse(exempt)
        self.assertEqual(len(self.denied_events()), 1)
        self.assertEqual(self.herdr_argvs(), [])  # the stand-in was never called

    def test_stub_exempt_requires_temp_home(self):
        self.worker_by_ancestry()
        path = self.wrapper_stub()
        code, exempt, _ = self.in_home(self.env.home_dir, ("agent", "start", "x"), path)
        self.assertIsNone(code)
        self.assertTrue(exempt)
        self.assertEqual(self.herdr_argvs(), [["agent", "start", "x"]])
        # refuse_worker uses the same predicate: the live home refuses, the temp home does not
        for home, want in ((LIVE, 3), (self.env.home_dir, None)):
            with self.env.load_factory() as f, mock.patch.dict(os.environ, {"PATH": path, "FACTORY_ROLE": "coder"}):
                f.HOME = home
                try:
                    f.refuse_worker("up")
                    got = None
                except SystemExit as x:
                    got = x.code
            self.assertEqual(got, want, home)

    def snapshot(self):
        return [[tuple(r) for r in self.env.sql(f"SELECT * FROM {t}")]
                for t in ("agents", "events", "messages", "teams")]

    def test_team_new_refused_for_worker_spoofing_director(self):
        self.cli_worker_ancestry()
        before = self.snapshot()
        p = self.refused(3, "team", "new", "--goal", "G-001", "--scope", "x", agent="zz-test-director")
        self.assertIn("workers may not run `factory team new`", p.stderr)
        p = self.refused(3, "team", "new", "--goal", "G-001", "--scope", "x", agent="zz-test-director",
                         extra_env={"FACTORY_PROC_DIR": "/proc/nonexistent", "FACTORY_ROLE": "coder"})
        self.assertEqual(self.snapshot(), before)
        self.assertEqual(self.herdr_argvs(), [])

    def test_team_close_refused_for_worker(self):
        self.cli_worker_ancestry()
        before = self.snapshot()
        self.refused(3, "team", "close", "g-001-a", agent="zz-test-director")
        self.refused(3, "team", "close", "g-001-a", agent="zz-test-coder", extra_env={"FACTORY_ROLE": "coder"})
        self.assertEqual(self.snapshot(), before)

    def test_team_new_and_close_still_work_for_director(self):
        self.ok("team", "new", "--goal", "G-001", "--scope", "x", agent="zz-test-director")
        self.assertTrue(self.herdr_argvs())
        self.ok("team", "close", "g-001-b", agent="zz-test-director")
        self.ok("team", "list", agent="zz-test-coder", extra_env={"FACTORY_ROLE": "coder"})

    def test_msg_to_staff_engineer_from_worker_no_inprocess_spawn(self):
        self.env.sql("DELETE FROM agents WHERE role='staff-engineer'")  # none live: a --kind request would spawn it
        self.cli_worker_ancestry()
        before = self.snapshot()
        for who, extra in (("zz-test-director", {}), ("zz-test-lead", {}),
                           ("zz-test-coder", {"FACTORY_PROC_DIR": "/proc/nonexistent", "FACTORY_ROLE": "coder"})):
            with self.subTest(who=who):
                p = self.refused(3, "msg", "staff-engineer", "q", "body", agent=who, extra_env=extra)
                self.assertIn("may not message staff-engineer", p.stderr)
        self.assertEqual(self.snapshot(), before)
        self.assertEqual(self.herdr_argvs(), [])

    def test_msg_to_staff_engineer_from_lead_still_spawns(self):
        self.env.sql("DELETE FROM agents WHERE role='staff-engineer'")
        p = self.ok("msg", "staff-engineer", "q", "body", "--kind", "request", agent="zz-test-lead")
        self.assertIn("→ staff-engineer", p.stdout)
        self.assertTrue(self.herdr_argvs())

    def test_herdr_socket_dead_for_worker_without_db(self):
        self.env.db_path.unlink()
        for ext in ("-wal", "-shm"):
            (self.env.db_path.parent / f"factory.db{ext}").unlink(missing_ok=True)
        with self.env.load_factory() as f:
            with mock.patch.dict(os.environ, {"FACTORY_ROLE": "coder", "FACTORY_AGENT": "zz-test-coder"}):
                self.assertEqual(f.herdr_socket(), f.DEAD_SOCKET)
        with self.env.load_factory() as f:  # a non-worker keeps the default
            self.assertEqual(f.herdr_socket(), os.path.join(str(self.env.user_home), ".config/herdr/herdr.sock"))

    def test_hq_full_flag_shape_not_worker(self):
        # the CURRENT live HQ/leads: python -> zsh -> claude with the full flag set and its role settings file
        for role in ("lead", "director", "boss"):
            with self.subTest(role=role):
                d = self.env.home / f"proc-{role}"
                d.mkdir()
                me = os.getpid()
                zsh, cl = ancestor_pids(me, start=3001)
                for pid, ppid, argv in (
                        (me, zsh, ["python3"]), (zsh, cl, ["zsh"]),
                        (cl, 1, ["claude", "--model", "m", "--permission-mode", "auto", "--settings",
                                   f"{LIVE}/roles/settings/{role}.json", "--append-system-prompt-file", "x",
                                   "--resume", "s"])):
                    p = d / str(pid)
                    p.mkdir()
                    (p / "cmdline").write_bytes(b"\0".join(x.encode() for x in argv) + b"\0")
                    (p / "stat").write_text(f"{pid} (x) S {ppid} {pid} {pid} 0 -1 0\n")
                env = {"FACTORY_ROLE": role, "CLAUDECODE": "1", "FACTORY_PROC_DIR": str(d),
                       "FACTORY_AGENT": f"zz-test-{role}"}
                before = len(self.herdr_argvs())
                with self.env.load_factory() as f:
                    with mock.patch.dict(os.environ, env):
                        self.assertIsNone(f.caller_is_worker())
                        f.refuse_worker("up")
                        f.herdr("agent", "start", "x")
                self.assertEqual(len(self.herdr_argvs()), before + 1)
        self.assertEqual(self.denied_events(), [])

    def test_every_worker_role_refused_team_new_by_env_alone(self):
        for role in WORKER_ROLES:
            with self.subTest(role=role):
                self.refused(3, "team", "new", "--goal", "G-001", "--scope", "x", agent="zz-test-director",
                             extra_env={"FACTORY_ROLE": role})


class ProcDirLiveHomeTests(WorkerGateBase):
    """T-1322: $FACTORY_PROC_DIR is honoured only off the live home; on it /proc is read and the attempt is recorded."""

    def fake_proc(self, name, argv):
        d = self.env.home / name
        (d / str(os.getpid())).mkdir(parents=True)
        (d / str(os.getpid()) / "cmdline").write_bytes(b"\0".join(x.encode() for x in argv) + b"\0")
        (d / str(os.getpid()) / "stat").write_text(f"{os.getpid()} (x) S 1 1 1 0 -1 0\n")
        return d

    def proc_events(self):
        return self.env.sql("SELECT * FROM events WHERE action='proc-dir-ignored'")

    def test_temp_home_honours_fake_proc_dir(self):
        with self.env.load_factory() as f:
            with mock.patch.dict(os.environ, {"FACTORY_PROC_DIR": "/some/fake"}):
                self.assertFalse(f.home_is_live())
                self.assertEqual(f.proc_root(), Path("/some/fake"))
        self.assertEqual(self.proc_events(), [])

    def test_live_home_ignores_fake_proc_dir_and_records_one_event(self):
        with self.env.load_factory() as f:
            f.LIVE_HOMES = f.LIVE_HOMES | {str(self.env.home_dir.resolve())}
            with mock.patch.dict(os.environ, {"FACTORY_PROC_DIR": "/some/fake", "FACTORY_AGENT": "zz-test-coder"}):
                self.assertTrue(f.home_is_live())
                self.assertEqual(f.proc_root(), Path("/proc"))
                self.assertEqual(f.proc_root(), Path("/proc"))
            with mock.patch.dict(os.environ, {"FACTORY_PROC_DIR": ""}):
                self.assertEqual(f.proc_root(), Path("/proc"))
        ev = self.proc_events()
        self.assertEqual(len(ev), 1)
        self.assertEqual(ev[0]["actor"], "zz-test-coder")
        self.assertIn("/some/fake", ev[0]["detail"])

    def test_serve_proc_dir_honoured_off_live_home(self):
        with self.env.load_factory() as f:
            with mock.patch.dict(os.environ, {"FACTORY_SERVE_PROC_DIR": "/some/fake"}):
                self.assertEqual(f._serve_proc_root(), Path("/some/fake"))
        self.assertEqual(self.proc_events(), [])

    def test_live_home_serve_proc_dir_ignored_and_one_event(self):
        with self.env.load_factory() as f:
            f.LIVE_HOMES = f.LIVE_HOMES | {str(self.env.home_dir.resolve())}
            with mock.patch.dict(os.environ, {"FACTORY_SERVE_PROC_DIR": "/some/fake", "FACTORY_AGENT": "zz-test-coder"}):
                self.assertEqual(f._serve_proc_root(), Path("/proc"))
                self.assertEqual(f._serve_proc_root(), Path("/proc"))
            with mock.patch.dict(os.environ, {"FACTORY_SERVE_PROC_DIR": ""}):
                self.assertEqual(f._serve_proc_root(), Path("/proc"))
        ev = self.proc_events()
        self.assertEqual(len(ev), 1)
        self.assertIn("FACTORY_SERVE_PROC_DIR", ev[0]["detail"])
        self.assertIn("/some/fake", ev[0]["detail"])

    def test_live_home_forged_hq_ancestry_still_a_worker(self):
        real = self.fake_proc("realproc", ["claude", "--settings", WORKER_SETTINGS])
        forged = self.fake_proc("forged", ["claude", "--resume", "lead-session"])
        with self.env.load_factory() as f:
            f.LIVE_HOMES = f.LIVE_HOMES | {str(self.env.home_dir.resolve())}
            real_path = f.Path
            f.Path = lambda p, *a: real_path(real if str(p) == "/proc" else p, *a)
            with mock.patch.dict(os.environ, {"FACTORY_PROC_DIR": str(forged), "FACTORY_AGENT": "zz-test-coder"}):
                self.assertEqual(f._ancestry_worker(), "coder")
                with self.assertRaises(SystemExit) as x:
                    f.refuse_worker("up")
                self.assertEqual(x.exception.code, 3)
            f.LIVE_HOMES = set()  # temp home: the forged dir is the one read
            with mock.patch.dict(os.environ, {"FACTORY_PROC_DIR": str(forged)}):
                self.assertIsNone(f._ancestry_worker())


if __name__ == "__main__":
    unittest.main()
