"""T-0664: tick per-phase wall timers (config tick_phase_timers, log-only). Runs `factory tick` as a subprocess."""
import re
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from harness import FactoryTestCase  # noqa: E402

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


if __name__ == "__main__":
    unittest.main()
