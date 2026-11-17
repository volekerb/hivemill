"""T-0664: tick per-phase wall timers (config tick_phase_timers, log-only). Runs `factory tick` as a subprocess."""
import contextlib
import fcntl
import io
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from unittest import mock  # noqa: E402
from harness import FactoryTestCase, PR_URL, REPO, cli_go_bin, git  # noqa: E402

LINE = re.compile(r"^tick-phases total=\d+\.\d\d( [a-z_]+=\d+(\.\d\d)?)+$")
PHASES = ("migrate_db render_settings herdr_list digest tmp_alert remote_url_alert main_protection tmp_clean red_main "
          "reap_serves reap_orphans scout skilleval pending_nudges reap_loop drift flagless claude_audit stop_fallback gate hold_expiry ci_wake "
          "wake_leads dequeue dispatch start_confirm nudge sidebar export freshness recycle relaunches util").split()
COUNTERS = "dequeue_spawns dequeue_spawn_s ci_wake_ls_remote ci_wake_gh export_git".split()


class TickPhaseTimers(FactoryTestCase):
    def tick(self):
        p = self.ok("tick")
        return [l for l in p.stdout.splitlines() if l.startswith("tick-phases")], p

    def phase_rows(self):
        return self.env.sql("SELECT detail FROM events WHERE action='tick-phases'")

    def test_off_by_default(self):
        lines, p = self.tick()
        self.assertEqual(lines, [])
        self.assertNotIn("tick-phases", p.stdout + p.stderr)
        self.assertEqual(self.phase_rows(), [])

    def test_on_one_line_and_one_event_with_every_key(self):
        self.ok("config", "tick_phase_timers", "1")
        lines, _ = self.tick()
        self.assertEqual(len(lines), 1, lines)
        self.assertRegex(lines[0], LINE)
        keys = [w.split("=")[0] for w in lines[0].split()[1:]]
        for k in PHASES + COUNTERS:
            self.assertIn(k, keys)
        # phases appear in run order
        order = [k for k in keys if k in PHASES]
        self.assertEqual(order, [k for k in PHASES if k in order])
        rows = self.phase_rows()
        self.assertEqual([r["detail"] for r in rows], lines)

    def test_reap_orphans_is_its_own_phase(self):
        self.ok("config", "tick_phase_timers", "1")
        lines, _ = self.tick()
        keys = [w.split("=")[0] for w in lines[0].split()[1:]]
        self.assertEqual(keys[keys.index("reap_serves") + 1], "reap_orphans")

    def test_herdr_unreachable_path(self):
        self.ok("config", "tick_phase_timers", "1")
        self.env.set_herdr(fail=True)
        lines, p = self.tick()
        self.assertEqual(len(lines), 1, lines)
        self.assertRegex(lines[0], LINE)
        for k in ("migrate_db", "herdr_list", "drift", "export"):
            self.assertIn(f" {k}=", lines[0])
        self.assertIn("herdr unreachable", p.stdout)
        self.assertEqual(len(self.phase_rows()), 1)

    def test_off_and_on_make_the_same_calls(self):
        def calls_of_one_tick():
            self.env.calls_file.write_text("")
            self.ok("tick")
            return [(c["tool"], c["argv"]) for c in self.env.calls()]
        self.env.seed_goal()
        self.env.seed_agent("zz-boss", "boss", pane_id="p9")
        self.env.set_herdr(agents=[{"name": "zz-boss", "pane_id": "p9", "agent_status": "idle"}])
        calls_of_one_tick()  # settle first-tick effects
        off = calls_of_one_tick()
        self.ok("config", "tick_phase_timers", "1")
        on = calls_of_one_tick()
        self.assertTrue(off)
        self.assertEqual(on, off)

    def test_lap_order_matches_tick_units(self):
        """T-1383: lap keys follow TICK_UNITS lap column for units that ran."""
        sys.path.insert(0, str(REPO / "bin"))
        import factory_cli  # noqa: E402
        self.ok("config", "tick_phase_timers", "1")
        lines, _ = self.tick()
        laps_want = [lap for _, lap, _ in factory_cli.TICK_UNITS if lap]
        keys = [part.split("=")[0] for part in lines[0].split()[1:] if "=" in part]
        got = [k for k in keys if k in laps_want]
        self.assertEqual(got, laps_want)


class TickPhasesHandoff(FactoryTestCase):
    def hold_lock(self):
        p = self.env.home_dir / "run" / "tick.lock"
        p.parent.mkdir(parents=True, exist_ok=True)
        self.lf = open(p, "w")
        fcntl.flock(self.lf, fcntl.LOCK_EX)

    def handoff(self, state=None):
        h = self.env.home_dir / "run" / "tick-handoff.json"
        h.write_text(json.dumps({"v": 1, "pid": os.getpid(), "timers": True, "state": state or {}}) + "\n")
        return h

    def phases(self, spec, handoff=None, extra=None):
        env = {**self.env.env("owner"), **(extra or {})}
        if handoff:
            env["FACTORY_TICK_HANDOFF"] = str(handoff)
        return subprocess.run(
            [sys.executable, str(REPO / "bin" / "factory"), "tick", "--phases", spec],
            cwd=self.env.home, capture_output=True, text=True, env=env, timeout=120)

    def test_hygiene_block(self):
        self.hold_lock()
        h = self.handoff({"agents": [{"name": "x", "pane_id": "p1"}]})
        self.env.calls_file.write_text("")
        p = self.phases("hygiene", h)
        self.assertEqual(p.returncode, 0, p.stderr + p.stdout)
        doc = json.loads(h.read_text())
        lap_names = [x[0] for x in doc["laps"]]
        self.assertEqual(lap_names[:2], ["digest", "tmp_alert"])
        self.assertEqual(lap_names[-1], "judge_ci_prune")
        self.assertFalse(doc["stop"])
        self.assertFalse(any(c.get("tool") == "agent" and "list" in c.get("argv", []) for c in self.env.calls()))

    def test_refuse_no_handoff(self):
        self.hold_lock()
        p = self.phases("hygiene")
        self.assertEqual(p.returncode, 2)
        self.assertIn("factory: tick --phases", p.stderr)

    def test_refuse_herdr_list_missing_past_prune(self):
        self.hold_lock()
        h = self.handoff({})
        p = self.phases("side", h)
        self.assertEqual(p.returncode, 2)
        self.assertIn("herdr list missing", p.stderr)

    def test_refuse_herdr_null_past_prune(self):
        self.hold_lock()
        h = self.handoff({"agents": None})
        p = self.phases("side", h)
        self.assertEqual(p.returncode, 2)
        self.assertIn("herdr list missing", p.stderr)

    def test_offline_only_ok_with_agents_null(self):
        self.hold_lock()
        h = self.handoff({"agents": None})
        p = self.phases("offline", h)
        self.assertEqual(p.returncode, 0, p.stderr + p.stdout)
        self.assertNotIn("herdr list missing", p.stderr)

    def test_agent_unit_still_refused_with_agents_null(self):
        self.hold_lock()
        h = self.handoff({"agents": None})
        p = self.phases("offline,scout", h)
        self.assertEqual(p.returncode, 2)
        self.assertIn("herdr list missing", p.stderr)

    def test_refuse_handoff_outside_run(self):
        self.hold_lock()
        outside = self.env.home / "tick-handoff.json"
        outside.write_text(json.dumps({"v": 1, "pid": os.getppid(), "state": {}}))
        p = self.phases("hygiene", outside)
        self.assertEqual(p.returncode, 2)
        self.assertIn("factory: tick --phases", p.stderr)

    def test_refuse_handoff_pid_not_ppid(self):
        self.hold_lock()
        h = self.handoff({"agents": [{"name": "x", "pane_id": "p1"}]})
        wrong = 1 if os.getppid() != 1 else 2
        h.write_text(json.dumps({"v": 1, "pid": wrong, "timers": True, "state": {"agents": []}}) + "\n")
        p = self.phases("hygiene", h)
        self.assertEqual(p.returncode, 2)
        self.assertIn("factory: tick --phases", p.stderr)
    def test_refuse_lock_not_held(self):
        h = self.handoff({"agents": []})
        p = self.phases("hygiene", h)
        self.assertEqual(p.returncode, 2)
        self.assertIn("factory: tick --phases", p.stderr)

    def test_refuse_unknown_unit(self):
        self.hold_lock()
        p = self.phases("nope", self.handoff({}))
        self.assertEqual(p.returncode, 2)
        self.assertIn("unknown unit", p.stderr)

    def test_refuse_out_of_order(self):
        self.hold_lock()
        p = self.phases("side,hygiene", self.handoff({"agents": []}))
        self.assertEqual(p.returncode, 2)
        self.assertIn("out of order", p.stderr)

    def test_refuse_gap(self):
        self.hold_lock()
        p = self.phases("hygiene,reap", self.handoff({"agents": []}))
        self.assertEqual(p.returncode, 2)
        self.assertIn("gap", p.stderr)

    def test_refuse_emit_not_last(self):
        self.hold_lock()
        p = self.phases("tail,util", self.handoff({}))
        self.assertEqual(p.returncode, 2)
        self.assertRegex(p.stderr, r"emit is only allowed last|out of order")

    def test_refuse_worker_identity(self):
        self.hold_lock()
        env = {**self.env.env("zz-coder", extra={"FACTORY_ROLE": "coder"}), "FACTORY_TICK_HANDOFF": str(self.handoff({}))}
        p = subprocess.run([sys.executable, str(REPO / "bin" / "factory"), "tick", "--phases", "hygiene"],
                           cwd=self.env.home, capture_output=True, text=True, env=env, timeout=60)
        self.assertEqual(p.returncode, 3)

    def test_tick_help_unchanged(self):
        wt = tempfile.mkdtemp(prefix="zz-tick-help-")
        self.addCleanup(lambda: subprocess.run(["git", "worktree", "remove", "-f", wt], cwd=REPO, capture_output=True))
        git = subprocess.run(["git", "worktree", "add", "-q", wt, "origin/main"], cwd=REPO, capture_output=True, text=True)
        self.assertEqual(git.returncode, 0, git.stderr)
        wt_path = Path(wt)
        a = subprocess.run([sys.executable, str(REPO / "bin" / "factory"), "tick", "--help"],
                             env=self.env.env(), capture_output=True)
        b = subprocess.run([sys.executable, str(wt_path / "bin" / "factory"), "tick", "--help"],
                           env=self.env.env(), capture_output=True)
        self.assertEqual(a.stdout, b.stdout)
        self.assertEqual(a.stderr, b.stderr)

    def test_hold_expiry_unit_without_factory_go_keeps_the_hold(self):
        """T-1610/T-1631: no factory-go in the test home: exit 0, the one stderr line, one lap, hold kept."""
        seed_held_task(self.env)
        self.hold_lock()
        h = self.handoff({"agents": []})
        p = self.phases("hold_expiry", h)
        self.assertEqual(p.returncode, 0, p.stderr + p.stdout)
        self.assertEqual(p.stdout, "")
        self.assertEqual(p.stderr.strip(), "hold_expiry: factory-go missing; holds kept")
        self.assertEqual([x[0] for x in json.loads(h.read_text())["laps"]], ["hold_expiry"])
        self.assertEqual(held_reason(self.env), HOLD)

    def test_hold_expiry_rides_in_the_flow_block(self):
        """T-1610: the row sits directly before mech_merge, inside the flow block the Go driver names."""
        self.hold_lock()
        h = self.handoff({"agents": []})
        p = self.phases("flow", h)
        self.assertEqual(p.returncode, 0, p.stderr + p.stdout)
        laps = [x[0] for x in json.loads(h.read_text())["laps"]]
        self.assertEqual(laps, ["stop_fallback", "gate", "hold_expiry", "mech_merge", "ci_wake", "wake_leads"])


HOLD = "wait for PR 703"


def seed_held_task(env):
    """An in_review PR task with reviewer PASS and QA PASS and a manual judge hold set long ago (2026-01-01): the
    fixture the Go unit would expire."""
    env.seed_goal()
    env.seed_task()
    for role in ("reviewer", "qa"):
        env.sql("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) "
                "VALUES('T-0001',?,'a',?,'PASS','zz-test','zz-test','t')", (PR_URL, role))
    env.sql("UPDATE tasks SET judge_hold_reason=? WHERE id='T-0001'", (HOLD,))
    env.sql("INSERT INTO events(ts,actor,entity,entity_id,action,detail) "
            "VALUES('2026-01-01T00:00:00+00:00','zz-lead','task','T-0001','judge-hold-set',?)", (HOLD,))


def held_reason(env):
    return env.sql("SELECT judge_hold_reason FROM tasks WHERE id='T-0001'")[0][0]


class TickHoldExpiryPython(FactoryTestCase):
    """T-1610: hold_expiry is a Go tick unit. The all-Python tick (tick_impl=python) walks its row without an
    implementation: it must not crash and must keep the hold."""

    def test_python_tick_keeps_the_hold_and_does_not_crash(self):
        seed_held_task(self.env)
        self.assertEqual(self.env.sql("SELECT value FROM config WHERE key='tick_impl'")[0][0], "python")
        p = self.env.run("tick", inproc=False)
        self.assertEqual(p.returncode, 0, p.stderr + p.stdout)
        self.assertNotIn("Traceback", p.stdout + p.stderr)
        self.assertNotIn("hold-expir", p.stdout + p.stderr)
        self.assertIn("hold_expiry: factory-go missing; holds kept", p.stderr)
        self.assertEqual(held_reason(self.env), HOLD)
        self.assertEqual(self.env.sql("SELECT COUNT(*) FROM events WHERE action='judge-hold-expired'")[0][0], 0)

    def test_python_tick_laps_the_unit_before_mech_merge(self):
        self.ok("config", "tick_phase_timers", "1")
        p = self.env.run("tick", inproc=False)
        self.assertEqual(p.returncode, 0, p.stderr + p.stdout)
        line = [l for l in p.stdout.splitlines() if l.startswith("tick-phases")][0]
        keys = [w.split("=")[0] for w in line.split()[1:]]
        self.assertEqual(keys[keys.index("gate"):keys.index("mech_merge") + 1], ["gate", "hold_expiry", "mech_merge"])

    def test_unit_row_sits_directly_before_mech_merge(self):
        sys.path.insert(0, str(REPO / "bin"))
        import factory_cli  # noqa: E402
        names = [u[0] for u in factory_cli.TICK_UNITS]
        self.assertEqual(names.count("hold_expiry"), 1)
        self.assertEqual(names[names.index("hold_expiry") + 1], "mech_merge")
        self.assertEqual(factory_cli.TICK_UNITS[names.index("hold_expiry")], ("hold_expiry", "hold_expiry", "flow"))

    def test_config_default_is_120_and_not_an_owner_cap(self):
        sys.path.insert(0, str(REPO / "bin"))
        import factory_cli  # noqa: E402
        self.assertEqual(factory_cli.DEFAULT_CONFIG["judge_hold_expiry_min"], "120")
        self.assertNotIn("judge_hold_expiry_min", factory_cli.OWNER_CAP_KEYS)
        self.assertEqual(self.env.sql("SELECT value FROM config WHERE key='judge_hold_expiry_min'")[0][0], "120")

    def test_judge_hold_help_names_the_expiry(self):
        p = subprocess.run([sys.executable, str(REPO / "bin" / "factory"), "task", "update", "--help"],
                           env=self.env.env(), capture_output=True, text=True, timeout=60)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(" ".join(p.stdout.split()).count("boss-approved M<id>"), 1)
        self.assertIn("judge_hold_expiry_min", p.stdout)


FAKE_FAIL = "#!/bin/sh\necho boom >&2\nexit 1\n"


class TickHoldExpiryGo(FactoryTestCase):
    """T-1631: the Python tick runs `factory-go hold-expiry` in the hold_expiry unit. The real factory-go is built once
    (harness cli_go_bin), stamped as guard_tick stamps it, and run against the temp home."""

    def lib(self):
        d = self.env.user_home / ".local" / "lib" / "factory"
        d.mkdir(parents=True, exist_ok=True)
        return d

    def install(self, binary=None, script=None, stamp=None):
        """Commit a go/ tree in the test home, then put factory-go (the real binary, or a script) and its stamp in the lib dir."""
        home = self.env.home_dir
        (home / "go").mkdir(exist_ok=True)
        (home / "go" / "x.go").write_text("package x\n")
        git("add", "-A", cwd=home)
        git("-c", "user.name=zz", "-c", "user.email=zz@example.invalid", "commit", "-q", "-m", "go", cwd=home)
        h = git("rev-parse", "HEAD:go", cwd=home)
        dst = self.lib() / "factory-go"
        if script is not None:
            dst.write_text(script)
        else:
            try:
                src = binary or cli_go_bin()
            except RuntimeError as e:
                if "go not found on PATH" in str(e) or "go.mod requires go >=" in str(e):
                    self.skipTest(str(e))  # unittest shards without setup-go cannot build factory-go
                raise
            shutil.copy2(src, dst)
        dst.chmod(0o755)
        (self.lib() / "factory-go.stamp").write_text((stamp or h) + "\n")

    def tick(self):
        self.ok("config", "tick_phase_timers", "1")
        p = self.env.run("tick", inproc=False)
        self.assertEqual(p.returncode, 0, p.stderr + p.stdout)
        self.assertRegex(p.stdout, r"tick-phases .* hold_expiry=")
        return p

    def test_python_tick_expires_a_hold_through_go(self):
        seed_held_task(self.env)
        self.install()
        self.assertEqual(self.env.sql("SELECT value FROM config WHERE key='tick_impl'")[0][0], "python")
        p = self.tick()
        self.assertIsNone(held_reason(self.env))
        q = lambda a: self.env.sql("SELECT detail FROM events WHERE action=? AND entity_id='T-0001'", (a,))
        self.assertEqual(len(q("judge-hold-expired")), 1)
        clear = q("judge-hold-clear")
        self.assertEqual(len(clear), 1)
        self.assertTrue(clear[0][0].startswith("expired after"), clear)
        msgs = self.env.sql("SELECT subject FROM messages WHERE kind='report' AND subject LIKE 'judge hold expired: T-0001 PR #%'")
        self.assertEqual(len(msgs), 1)
        self.assertIn("judge-hold-expired T-0001:", p.stdout)

    def test_missing_and_stale_binary_keep_the_hold(self):
        seed_held_task(self.env)
        p = self.tick()
        self.assertIn("hold_expiry: factory-go missing; holds kept", p.stderr)
        self.install(stamp="0" * 40)
        p = self.tick()
        self.assertIn("hold_expiry: factory-go stale; holds kept", p.stderr)
        self.assertEqual(held_reason(self.env), HOLD)
        self.assertEqual(self.env.sql("SELECT COUNT(*) FROM events WHERE action='judge-hold-expired'")[0][0], 0)

    def test_failing_binary_keeps_the_hold_and_the_tick_goes_on(self):
        seed_held_task(self.env)
        self.install(script=FAKE_FAIL)
        p = self.tick()
        self.assertIn("hold_expiry: factory-go hold-expiry failed (", p.stderr)
        self.assertEqual(held_reason(self.env), HOLD)

    def test_hanging_binary_times_out_without_raising(self):
        sys.path.insert(0, str(REPO / "bin"))
        import factory_cli  # noqa: E402
        lib = Path(tempfile.mkdtemp(prefix="zz-hold-lib-"))
        self.addCleanup(shutil.rmtree, lib, True)
        (lib / "factory-go").write_text("#!/bin/sh\nexec sleep 30\n")
        (lib / "factory-go").chmod(0o755)
        con = mock.MagicMock()
        err = io.StringIO()
        with mock.patch.object(factory_cli, "cfg", return_value="120"), \
                mock.patch.object(factory_cli, "cli_binary_state", return_value="ok"), \
                mock.patch.object(factory_cli, "guard_lib_dir", return_value=lib), \
                mock.patch.object(factory_cli, "HOLD_EXPIRY_TIMEOUT", 1), \
                mock.patch.object(factory_cli, "_pt_lap"), contextlib.redirect_stderr(err):
            factory_cli._tick_u_hold_expiry(con, {})
        self.assertIn("hold_expiry: factory-go hold-expiry failed (", err.getvalue())

    def test_off_switch_starts_no_subprocess(self):
        seed_held_task(self.env)
        calls = self.env.home_dir.parent / "fake-calls"
        self.install(script=f'#!/bin/sh\necho x >> {calls}\nexit 0\n')
        self.ok("config", "judge_hold_expiry_min", "0")
        p = self.tick()
        self.assertFalse(calls.exists())
        self.assertNotIn("hold_expiry:", p.stderr)
        self.assertEqual(held_reason(self.env), HOLD)

    def test_dispatch_stub_does_not_route_hold_expiry(self):
        self.env.run("config", "cli_impl", "python")
        with self.env.load_factory() as f:
            line = f.write_cli_impl(f.db())
        self.assertEqual(line, "python -")
        self.assertNotIn("hold-expiry", (self.env.home_dir / "run" / "cli_impl").read_text())

    def test_cli_build_follows_the_expiry_switch(self):
        for value, builds in (("120", 1), ("0", 0)):
            with self.subTest(judge_hold_expiry_min=value), self.env.load_factory() as f:
                self.ok("config", "judge_hold_expiry_min", value)
                with mock.patch.object(f, "load1", return_value=0.0), \
                        mock.patch.object(f, "build_cli_go", return_value="built") as b:
                    f.guard_tick(f.db())
                self.assertEqual(b.call_count, builds)

    def test_python_has_no_second_rule(self):
        src = (REPO / "bin" / "factory_cli.py").read_text()
        a = src.index("def _tick_u_hold_expiry")
        body = src[a:src.index("def _tick_u_mech_merge")]
        for term in ("verdicts", "judge-hold-set", "boss-approved", "judge_hold_reason"):
            self.assertNotIn(term, body)


if __name__ == "__main__":
    unittest.main()
