"""T-0664: tick per-phase wall timers (config tick_phase_timers, log-only). Runs `factory tick` as a subprocess."""
import fcntl
import json
import os
import re
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from harness import FactoryTestCase, REPO  # noqa: E402

LINE = re.compile(r"^tick-phases total=\d+\.\d\d( [a-z_]+=\d+(\.\d\d)?)+$")
PHASES = ("migrate_db render_settings herdr_list digest tmp_alert remote_url_alert main_protection tmp_clean red_main "
          "reap_serves reap_orphans scout skilleval pending_nudges reap_loop drift flagless claude_audit stop_fallback gate ci_wake "
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


if __name__ == "__main__":
    unittest.main()
