"""T-0227: `factory res` shows /tmp usage, the gate closes at tmp_max_pct, the tick alerts once per crossing and removes
the scratchpads of finished tasks (only that exact path pattern, under <tmp>/claude-<uid>/)."""
import os
import re
import time
import unittest

from harness import FactoryTestCase

FULL = "1000000000,50000000"     # 95% used
ROOMY = "1000000000,500000000"   # 50% used


class TmpBase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_task(status="todo", pr_url=None)
        self.base = e.home / "faketmp" / f"claude-{os.getuid()}"
        self.base.mkdir(parents=True)
        self.slug = re.sub(r"[^A-Za-z0-9]", "-", str(e.home_dir / "worktrees")) + "-"

    def pad(self, goal="G-001", task="T-0001", age_h=2, files=1, name=None):
        d = self.base / (name or f"{self.slug}{goal}-{task}")
        d.mkdir(parents=True, exist_ok=True)
        for i in range(files):
            (d / f"f{i}").write_text("x" * 1000)
        t = time.time() - age_h * 3600
        os.utime(d, (t, t))
        return d

    def tick(self, statvfs=ROOMY):
        return self.ok("tick", extra_env={"FACTORY_TMP_STATVFS": statvfs})

    def finish(self, status="done"):
        self.env.sql("UPDATE tasks SET status=? WHERE id='T-0001'", (status,))

    def cleaned(self):
        return self.env.sql("SELECT entity_id, detail FROM events WHERE action='tmp-cleaned'")


class ResAndGate(TmpBase):
    def test_res_line(self):
        out = self.ok("res", extra_env={"FACTORY_TMP_STATVFS": "1048576000,524288000"}).stdout
        self.assertRegex(out, r"tmp used 500 MB / 1000 MB \(50%\)")
        self.assertIn("gate closes at 90%", out)

    def test_res_names_filesystem_of_real_tmp(self):
        out = self.ok("res", extra_env={"FACTORY_TMP_STATVFS": "", "FACTORY_TMP_DIR": "/tmp"}).stdout
        self.assertRegex(out, r"tmp used \d+ MB / \d+ MB \(\d+%\)( on \w+)?")

    def test_gate_closed_at_threshold_with_reason(self):
        out = self.ok("res", extra_env={"FACTORY_TMP_STATVFS": FULL}).stdout
        self.assertIn("spawn gate: CLOSED", out)
        self.assertRegex(out, r"CLOSED — /tmp \d+/\d+ MB used \(95%\) ≥ 90%")

    def test_spawn_queued_with_reason(self):
        self.env.set_herdr(agents=[])
        p = self.ok("spawn", "coder", "--goal", "G-001", "--task", "T-0001", "--name", "zz-test-c1",
                    extra_env={"FACTORY_TMP_STATVFS": FULL})
        self.assertIn("QUEUED", p.stdout)
        self.assertIn("/tmp", p.stdout)
        self.assertEqual([r["status"] for r in self.env.sql("SELECT status FROM agents WHERE name='zz-test-c1'")], ["queued"])

    def test_gate_open_below_threshold_and_config_key(self):
        self.assertIn("spawn gate: OPEN", self.ok("res", extra_env={"FACTORY_TMP_STATVFS": "1000000000,150000000"}).stdout)  # 85%
        self.ok("config", "tmp_max_pct", "80")
        self.assertIn("CLOSED", self.ok("res", extra_env={"FACTORY_TMP_STATVFS": "1000000000,150000000"}).stdout)

    def test_unreadable_tmp_does_not_close_gate(self):
        out = self.ok("res", extra_env={"FACTORY_TMP_STATVFS": "", "FACTORY_TMP_DIR": str(self.env.home / "nope")}).stdout
        self.assertIn("tmp unknown", out)
        self.assertIn("spawn gate: OPEN", out)


class CleanerExempt(TmpBase):
    """T-0610: at 91% /tmp only the cleaner (which frees /tmp) is exempt from the tmp gate; owner caps still apply."""
    F91 = "1000000000,90000000"  # 91% used

    def spawn(self, role, name):
        self.env.set_herdr(agents=[])
        return self.ok("spawn", role, "--goal", "G-001", "--name", name,
                       *(["--task", "T-0001"] if role != "cleaner" else []),
                       extra_env={"FACTORY_TMP_STATVFS": self.F91})

    def status(self, name):
        return [r["status"] for r in self.env.sql("SELECT status FROM agents WHERE name=?", (name,))]

    def test_cleaner_spawns_coder_queues(self):
        self.assertIn("QUEUED", self.spawn("coder", "zz-test-c1").stdout)
        self.assertEqual(self.status("zz-test-c1"), ["queued"])
        self.assertNotIn("QUEUED", self.spawn("cleaner", "zz-test-cl1").stdout)
        self.assertNotEqual(self.status("zz-test-cl1"), ["queued"])

    def test_cleaner_still_counts_against_agent_cap(self):
        self.ok("config", "max_live_agents", "0")
        self.assertIn("QUEUED", self.spawn("cleaner", "zz-test-cl1").stdout)
        self.assertEqual(self.status("zz-test-cl1"), ["queued"])

    def test_cleaner_still_held_by_memory_floor(self):
        self.ok("config", "min_mem_available_mb", "100000000")
        p = self.spawn("cleaner", "zz-test-cl1")
        self.assertIn("QUEUED", p.stdout)
        self.assertIn("MemAvailable", p.stdout)

    def test_res_explains_cleaner_passes(self):
        out = self.ok("res", extra_env={"FACTORY_TMP_STATVFS": self.F91}).stdout
        self.assertIn("spawn gate: CLOSED", out)
        self.assertRegex(out, r"cleaner gate: OPEN — exempt from the /tmp gate")

    def test_res_no_cleaner_line_when_tmp_roomy(self):
        self.assertNotIn("cleaner gate", self.ok("res", extra_env={"FACTORY_TMP_STATVFS": ROOMY}).stdout)

    def test_tick_starts_queued_cleaner_behind_queued_coder(self):
        self.spawn("coder", "zz-test-c1")
        self.spawn("cleaner", "zz-test-cl1")
        self.env.sql("UPDATE agents SET status='queued', pane_id=NULL WHERE name='zz-test-cl1'")
        self.tick(statvfs=self.F91)
        self.assertEqual(self.status("zz-test-c1"), ["queued"])
        self.assertNotEqual(self.status("zz-test-cl1"), ["queued"])

    def test_tick_does_not_regate_every_row_once_held(self):
        # each gate() lists agents; a held queue must not pay that per queued row (it runs under the tick lock)
        def list_calls(n_coders):
            for i in range(n_coders):
                self.spawn("coder", f"zz-test-c{n_coders}-{i}")
            self.spawn("cleaner", f"zz-test-cl{n_coders}")
            self.env.sql("UPDATE agents SET status='queued', pane_id=NULL WHERE name=?", (f"zz-test-cl{n_coders}",))
            before = len(self.env.calls("herdr"))
            self.tick(statvfs=self.F91)
            self.assertEqual(self.status(f"zz-test-c{n_coders}-0"), ["queued"])
            self.assertNotEqual(self.status(f"zz-test-cl{n_coders}"), ["queued"])  # cleaner behind the held queue starts
            return len([c for c in self.env.calls("herdr")[before:] if c["argv"][:2] == ["agent", "list"]])
        few = list_calls(1)
        self.assertEqual(list_calls(5), few)  # 5 more queued coders behind the held gate add no agent-list calls


class Alert(TmpBase):
    def alerts(self):
        return self.env.sql("SELECT subject FROM messages WHERE sender='tick' AND subject LIKE '/tmp%'")

    def test_one_alert_per_crossing(self):
        self.env.seed_agent("zz-director", "director", goal="G-001")
        self.tick(ROOMY)
        self.assertEqual(self.alerts(), [])
        self.tick(FULL)
        self.tick(FULL)
        self.assertEqual(len(self.alerts()), 1)
        self.tick(ROOMY)   # re-arms
        self.tick(FULL)
        self.assertEqual(len(self.alerts()), 2)


class Clean(TmpBase):
    def test_deletes_finished_old_scratchpad_and_logs_event(self):
        d = self.pad(files=3)
        self.finish("done")
        self.tick()
        self.assertFalse(d.exists())
        ev = self.cleaned()
        self.assertEqual([e["entity_id"] for e in ev], ["T-0001"])
        self.assertIn("mb", ev[0]["detail"])
        self.assertIn(str(d), ev[0]["detail"])

    def test_cancelled_task_is_cleaned(self):
        d = self.pad()
        self.finish("cancelled")
        self.tick()
        self.assertFalse(d.exists())

    def test_keeps_unfinished_task(self):
        for st in ("todo", "in_progress", "in_review", "blocked"):
            d = self.pad()
            self.finish(st)
            self.env.sql("DELETE FROM throttle")
            self.tick()
            self.assertTrue(d.exists(), st)

    def test_keeps_recent(self):
        d = self.pad(age_h=0.5)
        self.finish()
        self.tick()
        self.assertTrue(d.exists())

    def test_keeps_when_agent_live_starting_queued(self):
        self.finish()
        d = self.pad()
        for i, st in enumerate(("live", "starting", "queued")):
            self.env.seed_agent(f"zz-test-a{i}", "coder", goal="G-001", task="T-0001", status=st)
            self.env.sql("UPDATE agents SET spawn_args='{\"claimed_at\": 9999999999}' WHERE name=?", (f"zz-test-a{i}",))  # a msg holds the claim: the tick leaves the row alone
            self.env.sql("DELETE FROM throttle")
            self.tick()
            self.assertTrue(d.exists(), st)
            self.env.sql("UPDATE agents SET status='exited' WHERE name=?", (f"zz-test-a{i}",))
        self.env.sql("DELETE FROM throttle")
        self.tick()
        self.assertFalse(d.exists())

    def test_never_hq_dir_or_other_names(self):
        hq = self.pad(name=self.slug.replace("-worktrees-", ""))
        other = self.pad(name="qa-t-0001")
        odd = self.pad(name=f"{self.slug}G-001-T-0001-extra")
        unknown = self.pad(task="T-9999")
        self.finish()
        self.tick()
        for d in (hq, other, odd, unknown):
            self.assertTrue(d.exists(), d.name)

    def test_symlink_pointing_outside_is_left_alone(self):
        outside = self.env.home / "outside"
        outside.mkdir()
        (outside / "keep").write_text("keep")
        link = self.base / f"{self.slug}G-001-T-0001"
        link.symlink_to(outside)
        self.finish()
        self.tick()
        self.assertTrue(link.is_symlink())
        self.assertEqual((outside / "keep").read_text(), "keep")
        self.assertEqual(self.cleaned(), [])

    def test_symlink_inside_scratchpad_not_followed(self):
        outside = self.env.home / "outside"
        outside.mkdir()
        (outside / "keep").write_text("keep")
        d = self.pad()
        (d / "lnk").symlink_to(outside)
        t = time.time() - 7200
        os.utime(d, (t, t))
        self.finish()
        self.tick()
        self.assertFalse(d.exists())
        self.assertEqual((outside / "keep").read_text(), "keep")

    def test_base_symlink_is_ignored(self):
        real = self.env.home / "realbase"
        real.mkdir()
        (real / f"{self.slug}G-001-T-0001").mkdir()
        self.base.rmdir()
        self.base.symlink_to(real)
        self.finish()
        self.tick()
        self.assertTrue((real / f"{self.slug}G-001-T-0001").exists())

    def test_runs_at_most_hourly(self):
        self.finish()
        self.tick()
        d = self.pad()
        self.tick()
        self.assertTrue(d.exists())
        self.env.sql("DELETE FROM throttle")
        self.tick()
        self.assertFalse(d.exists())


if __name__ == "__main__":
    unittest.main()
