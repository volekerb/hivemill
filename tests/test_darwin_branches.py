"""T-1568 (G-049 SC3, ADR 0003 4.1-4.4): the darwin branches of bin/factory_cli.py, run on Linux with sys.platform patched to
"darwin" and a fake subprocess runner fed recorded vm_stat / sysctl / ps / stat output. One test (or more) per spec item."""
import argparse
import builtins
import contextlib
import io
import os
import unittest
from pathlib import Path
from unittest import mock

from harness import FactoryTestCase

VM_STAT = """Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                               10000.
Pages active:                             50000.
Pages inactive:                           20000.
Pages speculative:                         2000.
Pages wired down:                         30000.
"""
# pid ppid uid lstart(5 words) command, as `LC_ALL=C ps -axo pid=,ppid=,uid=,lstart=,command=` prints it
PS = """    1     0     0 Sat Nov 15 08:00:00 2026 /sbin/launchd
  500     1   501 Sat Nov 15 09:30:01 2026 /opt/homebrew/bin/python3 /Users/x/factory/bin/factory serve
  600   500   501 Sat Nov 15 09:31:02 2026 /usr/local/bin/claude --settings /h/settings/coder.json --resume abc
  700     1   501 Sun Nov 16 10:00:00 2026 node /usr/local/bin/claude --session-id s1
"""
START_500 = "Sat Nov 15 09:30:01 2026"


def cp(out="", rc=0):
    return mock.Mock(returncode=rc, stdout=out, stderr="")


class Runner:
    """Fake subprocess.run: answers the darwin tools, records every argv, passes anything else to the real run."""

    def __init__(self, ps=PS, vm=VM_STAT, level="1", stat_t="apfs\n", top="  2048 /usr/bin/big\n"):
        self.ps, self.vm, self.level, self.stat_t, self.top = ps, vm, level, stat_t, top
        self.calls = []
        self.real = __import__("subprocess").run

    def __call__(self, cmd, *a, **kw):
        self.calls.append(list(cmd) if isinstance(cmd, (list, tuple)) else cmd)
        name = cmd[0] if isinstance(cmd, (list, tuple)) else ""
        if name == "ps":
            if "rss=,command=" in cmd:
                return cp(self.top)
            return cp(self.ps, 0) if self.ps is not None else cp("", 1)
        if name == "vm_stat":
            return cp(self.vm, 0) if self.vm is not None else cp("", 1)
        if name == "sysctl":
            return cp(self.level + "\n", 0) if self.level is not None else cp("", 1)
        if name == "stat":
            return cp(self.stat_t, 0)
        if name == "systemctl" or name == "systemd-run":
            raise AssertionError(f"{name} must not run on darwin: {cmd}")
        return self.real(cmd, *a, **kw)

    def named(self, name):
        return [c for c in self.calls if c and c[0] == name]


class DarwinBase(FactoryTestCase):
    @contextlib.contextmanager
    def darwin(self, runner=None, **env):
        runner = runner or Runner()
        with mock.patch.dict(os.environ, env), self.env.load_factory() as mod:
            with mock.patch.object(mod.sys, "platform", "darwin"), mock.patch.object(mod.subprocess, "run", runner):
                yield mod, runner


class DetachedRun(DarwinBase):
    def test_b7_detached_run_skips_systemd_run_on_darwin(self):
        with self.darwin(INVOCATION_ID="x") as (mod, runner):
            popen = mock.Mock()
            with mock.patch.object(mod, "mech_mode", return_value="on"), mock.patch.object(mod, "mech_candidates", return_value=[1]), \
                    mock.patch.object(mod.subprocess, "Popen", popen):
                mod.tick_mech_merge(None)
        self.assertEqual(runner.named("systemd-run"), [])
        self.assertEqual(popen.call_count, 1)
        self.assertTrue(popen.call_args.kwargs["start_new_session"])

    def test_b7_linux_still_uses_systemd_run(self):
        with mock.patch.dict(os.environ, {"INVOCATION_ID": "x"}), self.env.load_factory() as mod:
            run = mock.Mock()
            with mock.patch.object(mod, "mech_mode", return_value="on"), mock.patch.object(mod, "mech_candidates", return_value=[1]), \
                    mock.patch.object(mod.subprocess, "run", run):
                mod.tick_mech_merge(None)
        self.assertEqual(run.call_args.args[0][0], "systemd-run")


class ServePid(DarwinBase):
    def test_serve_pid_file_written_read_and_removed(self):
        with self.darwin() as (mod, _r):
            runner = Runner(ps=f"{os.getpid()} 1 501 {START_500} python3 factory serve\n")
            with mock.patch.object(mod.subprocess, "run", runner):
                seen = {}

                def fake_run(**kw):
                    seen["text"] = mod._serve_pidfile().read_text()
                    seen["pid"] = mod._serve_mainpid()  # protection reads the file on darwin
                    seen["unit"] = mod._serve_unit_pids()

                import web.server as srv
                with mock.patch.object(srv, "run", fake_run):
                    mod.cmd_serve(argparse.Namespace(bind=None, port=None, ttl=None))
                self.assertEqual(seen["text"], f"{os.getpid()} {START_500}\n")  # pid + start time
                self.assertEqual(seen["pid"], os.getpid())
                self.assertEqual(seen["unit"], {os.getpid()})
                self.assertFalse(mod._serve_pidfile().exists())  # removed on clean exit
                self.assertEqual(runner.named("systemctl"), [])

    def test_serve_pid_file_removed_when_serve_raises(self):
        with self.darwin() as (mod, _r):
            import web.server as srv
            with mock.patch.object(srv, "run", side_effect=RuntimeError("boom")):
                with self.assertRaises(RuntimeError):
                    mod.cmd_serve(argparse.Namespace(bind=None, port=None, ttl=None))
            self.assertFalse(mod._serve_pidfile().exists())

    def test_serve_pid_reused_pid_is_not_the_serve(self):
        with self.darwin() as (mod, _r):
            f = mod._serve_pidfile()
            f.parent.mkdir(parents=True, exist_ok=True)
            f.write_text(f"500 Sat Nov 15 01:00:00 2026\n")  # start time differs from ps: the pid was reused
            self.assertIsNone(mod._serve_mainpid())
            self.assertEqual(mod._serve_unit_pids(), set())
            f.write_text(f"500 {START_500}\n")
            self.assertEqual(mod._serve_mainpid(), 500)
            f.write_text("garbage")
            self.assertIsNone(mod._serve_mainpid())

    def test_serve_pid_not_written_on_linux(self):
        with self.env.load_factory() as mod:
            import web.server as srv
            with mock.patch.object(srv, "run", lambda **kw: self.assertFalse(mod._serve_pidfile().exists())):
                mod.cmd_serve(argparse.Namespace(bind=None, port=None, ttl=None))


class Memory(DarwinBase):
    def test_r1_formula_free_inactive_speculative(self):
        with self.darwin() as (mod, runner):
            self.assertEqual(mod.mem_available_mb(), (10000 + 20000 + 2000) * 16384 // 1048576)
        self.assertEqual(runner.named("sysctl")[0][1:], ["-n", "kern.memorystatus_vm_pressure_level"])

    def test_unreadable_vm_stat_closes_the_gate(self):
        for runner in (Runner(vm=None), Runner(level=None), Runner(level="2"), Runner(level="4"), Runner(vm="garbage")):
            with self.darwin(runner) as (mod, _r):
                self.assertEqual(mod.mem_available_mb(), 0)  # 0 < min_mem_available_mb: gate closed

    def test_unreadable_ps_keeps_the_pid(self):
        with self.darwin(Runner(ps=None)) as (mod, _r):
            self.assertIsNone(mod._proc_start(500))
            self.assertIsNone(mod._orphan_stat(500))
            self.assertIsNone(mod._serve_argv(500))
            self.assertIsNone(mod._orphan_ident(500))  # None = the reapers decide nothing for this pid
            self.assertIsNone(mod._pid_comm(Path("/x/500")))
            self.assertIsNone(mod._proc_holds(["/tmp/x"]))  # keep everything
            self.assertIsNone(mod._scan_claude())

    def test_unreadable_reapers_never_signal(self):
        with self.darwin(Runner(ps=None)) as (mod, _r):
            con = mod.db()
            kill = mock.Mock()
            with mock.patch.object(mod, "_serve_kill", kill):
                mod.reap_stray_serves(con)
                mod.reap_orphans(con)
            self.assertEqual(kill.call_count, 0)


class TmpGate(DarwinBase):
    def test_r5_fs_type_from_stat_f_T_private_tmp(self):
        with self.darwin(Runner(stat_t="apfs\n")) as (mod, runner):
            with mock.patch.object(mod, "tmp_dir", lambda: Path("/tmp")), \
                    mock.patch.dict(os.environ, {"FACTORY_TMP_STATVFS": f"{500 * 2**30},{100 * 2**30}"}):
                u = mod.tmp_usage()
        self.assertEqual(u["fstype"], "apfs")
        self.assertEqual(runner.named("stat")[0], ["stat", "-f", "%T", "/private/tmp"])
        self.assertEqual((u["total_mb"], u["free_mb"]), (500 * 1024, 100 * 1024))

    def test_r5_unreadable_stat_is_unknown_fstype_not_a_crash(self):
        r = Runner()
        r.stat_t = ""
        with self.darwin(r, FACTORY_TMP_STATVFS=f"{2**30},{2**29}") as (mod, _r):
            self.assertIsNone(mod.tmp_usage()["fstype"])


class TmpFloor(DarwinBase):
    def gate_with(self, tmp, role=None):
        with self.darwin() as (mod, _r):
            with mock.patch.object(mod, "tmp_usage", return_value=tmp), mock.patch.object(mod, "load1", return_value=0.0):
                return mod.gate(mod.db(), role=role)[:2]

    def tmp(self, free_mb, fstype):
        return {"used_mb": 1, "total_mb": 1 + free_mb, "pct": 1.0, "fstype": fstype, "free_mb": free_mb}

    def test_r5_disk_tmp_below_floor_closes_gate(self):
        ok, why = self.gate_with(self.tmp(10239, "apfs"))
        self.assertFalse(ok)
        self.assertIn("10240 MB floor", why)

    def test_r5_disk_tmp_at_floor_passes_the_tmp_check(self):
        ok, why = self.gate_with(self.tmp(10240, "apfs"))
        self.assertNotIn("floor", why)

    def test_r5_tmpfs_has_no_floor_and_unreadable_tmp_closes(self):
        self.assertNotIn("floor", self.gate_with(self.tmp(5, "tmpfs"))[1])
        ok, why = self.gate_with(None)
        self.assertFalse(ok)
        self.assertIn("unreadable", why)


class ProcIdentity(DarwinBase):
    def test_r9_identity_from_one_ps_snapshot(self):
        with self.darwin() as (mod, runner):
            self.assertEqual(mod._proc_start(500), START_500)
            self.assertEqual(mod._orphan_stat(600)[0], 500)
            self.assertEqual(mod._serve_argv(500)[-1], "serve")
            self.assertEqual(mod._pid_comm(Path("/x/500")), "python3")
            with self.assertRaises(FileNotFoundError):
                mod._pid_comm(Path("/x/999"))  # exited: not in the snapshot
            self.assertEqual(sorted(mod._scan_claude()), [600, 700])
            self.assertEqual(mod._scan_claude()[600][1], 500)
        ps_calls = runner.named("ps")
        self.assertTrue(all(c == ["ps", "-axo", "pid=,ppid=,uid=,lstart=,command="] for c in ps_calls))

    def test_r9_ps_runs_with_lc_all_c(self):
        seen = {}

        def run(cmd, *a, **kw):
            seen["env"] = kw.get("env", {})
            return cp(PS)
        with self.darwin() as (mod, _r), mock.patch.object(mod.subprocess, "run", run):
            mod._proc_start(500)
        self.assertEqual(seen["env"].get("LC_ALL"), "C")

    def test_r13_userns_link_is_none_on_darwin(self):
        with self.darwin() as (mod, _r):
            self.assertIsNone(mod._userns_link(os.getpid()))
            self.assertIsNone(mod._userns_link(1))

    def test_ancestry_worker_walks_ps_parents(self):
        ps = f"""  {os.getpid()}   600   501 {START_500} python3 factory done
  600   1   501 {START_500} claude --settings /h/settings/coder.json
"""
        with self.darwin(Runner(ps=ps)) as (mod, _r):
            self.assertEqual(mod._ancestry_worker(), "coder")


class NoProc(DarwinBase):
    def test_no_proc_read_on_the_darwin_path(self):
        real_open, real_readlink, real_iterdir = builtins.open, os.readlink, Path.iterdir

        def is_proc(p):
            return str(p) == "/proc" or str(p).startswith("/proc/")

        def bad_open(f, *a, **kw):
            if is_proc(f):
                raise AssertionError(f"open of {f} on the darwin path")
            return real_open(f, *a, **kw)

        def bad_readlink(p, *a, **kw):
            if is_proc(p):
                raise AssertionError(f"readlink of {p} on the darwin path")
            return real_readlink(p, *a, **kw)

        def bad_iterdir(self_):
            if is_proc(self_):
                raise AssertionError(f"iterdir of {self_} on the darwin path")
            return real_iterdir(self_)

        with self.darwin(FACTORY_TMP_STATVFS=f"{2**30},{2**29}") as (mod, _r):
            con = mod.db()
            with mock.patch.object(builtins, "open", bad_open), mock.patch.object(io, "open", bad_open), \
                    mock.patch.object(os, "readlink", bad_readlink), mock.patch.object(Path, "iterdir", bad_iterdir):
                mod._userns_link(1)
                mod._proc_start(500)
                mod._orphan_stat(600)
                mod._serve_argv(500)
                mod._scan_claude()
                mod._scan_host_agent_procs()
                mod.total_claude_procs()
                mod.mem_available_mb()
                mod.tmp_usage()
                mod._serve_mainpid()
                mod._serve_unit_pids()
                mod._proc_holds(["/tmp/x"])
                mod._ancestry_worker()
                mod.reap_stray_serves(con)
                mod.reap_orphans(con)

    def test_the_test_would_catch_a_proc_read(self):
        # the guard of the test above must fire: on linux the same call reads /proc
        real_open = builtins.open

        def bad_open(f, *a, **kw):
            if str(f).startswith("/proc/"):
                raise AssertionError("proc read")
            return real_open(f, *a, **kw)
        with self.env.load_factory() as mod, mock.patch.object(builtins, "open", bad_open), mock.patch.object(io, "open", bad_open):
            with self.assertRaises(AssertionError):
                mod.mem_available_mb()


class ResOutput(DarwinBase):
    def test_r8_bsd_ps_form_and_serve_drift_note(self):
        with self.darwin(Runner(top="  2048 /usr/bin/big\n 1024 /usr/bin/small\n"), FACTORY_TMP_STATVFS=f"{2**30},{2**29}") as (mod, runner):
            out = io.StringIO()
            with contextlib.redirect_stdout(out):
                mod.cmd_res(argparse.Namespace())
        self.assertIn(["ps", "-axo", "rss=,command=", "-m"], runner.named("ps"))
        self.assertNotIn("--sort=-rss", str(runner.calls))
        self.assertIn("serve unit drift check: skipped on macOS", out.getvalue())
        self.assertIn("2 MB  /usr/bin/big", out.getvalue())

    def test_serve_unit_drift_tick_hook_skips_on_darwin(self):
        with self.darwin() as (mod, _r):
            with mock.patch.object(mod.Path, "read_bytes", side_effect=AssertionError("read")):
                self.assertIsNone(mod.tick_serve_unit_drift(None))


class NiceCheck(DarwinBase):
    def test_r18_usr_bin_nice_existence_check_is_unchanged(self):
        src = (Path(__file__).resolve().parent.parent / "bin" / "factory_cli.py").read_text()
        for line in (l for l in src.splitlines() if "/usr/bin/nice" in l):
            self.assertNotIn("darwin", line)  # no platform branch: /usr/bin/nice exists on macOS too


class TickDeadline(DarwinBase):
    def run_tick(self, deadline):
        with self.env.load_factory() as mod:
            clock = [100.0]
            ran = []

            def unit(name, cost):
                def f(con, st):
                    ran.append(name)
                    clock[0] += cost
                return f
            mod._tick_u_slow = unit("slow", 5)
            mod._tick_u_after = unit("after", 0)
            mod._tick_u_emit = lambda con, st: ran.append("emit")
            with mock.patch.object(mod, "TICK_WALK_UNITS", ["migrate_db", "slow", "after"]), \
                    mock.patch.object(mod.time, "monotonic", lambda: clock[0]):
                mod.cmd_tick(argparse.Namespace(phases=None, deadline=deadline))
            ev = [dict(r) for r in mod.db().execute("SELECT action,detail FROM events WHERE action='tick-deadline'")]
            return ran, ev

    def test_deadline_aborts_remaining_phases_with_event_and_returns(self):
        ran, ev = self.run_tick("1s")
        self.assertEqual(ran, ["slow"])  # 'after' and 'emit' were not run
        self.assertEqual(len(ev), 1)
        self.assertIn("before phase after", ev[0]["detail"])

    def test_no_flag_no_event_all_phases_run(self):
        ran, ev = self.run_tick(None)
        self.assertEqual(ran, ["slow", "after", "emit"])
        self.assertEqual(ev, [])

    def test_deadline_not_reached_runs_everything(self):
        ran, ev = self.run_tick("1h")
        self.assertEqual(ran, ["slow", "after", "emit"])
        self.assertEqual(ev, [])

    def test_bad_deadline_is_refused_exit_2(self):
        p = self.env.run("tick", "--deadline", "soon")
        self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
        self.assertIn("not a positive duration", p.stderr + p.stdout)

    def test_deadline_flag_hidden_from_help_but_accepted(self):
        p = self.env.run("tick", "--help")
        self.assertNotIn("--deadline", p.stdout)


if __name__ == "__main__":
    unittest.main()
