"""T-0606: when /tmp is over tmp_sweep_pct the tick deletes the scratch dirs and QA homes of agents that are all
finished for >= 30 min; live, recent, unknown and unowned dirs stay; one event per sweep; res shows it; one alert."""
import datetime as dt
import os
import re
import unittest

from harness import FactoryTestCase

FULL = "1000000000,50000000"     # 95% used
OVER = "1000000000,150000000"    # 85%: over the sweep line, under the gate line
ROOMY = "1000000000,500000000"   # 50%


def ago(minutes):
    return (dt.datetime.now().astimezone() - dt.timedelta(minutes=minutes)).isoformat(timespec="seconds")


class Sweep(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_task(status="in_review", pr_url=None)
        self.tmp = e.home / "faketmp"
        self.base = self.tmp / f"claude-{os.getuid()}"
        self.base.mkdir(parents=True)
        self.slug = re.sub(r"[^A-Za-z0-9]", "-", str(e.home_dir / "worktrees")) + "-"

    def dir(self, parent, name, kb=100):
        d = parent / name
        d.mkdir(parents=True, exist_ok=True)
        (d / "blob").write_text("x" * 1000 * kb)
        return d

    def pad(self, task="T-0001"):
        return self.dir(self.base, f"{self.slug}G-001-{task}")

    def agent(self, name, status, ended=None, task="T-0001"):
        self.env.seed_agent(name, "coder", goal="G-001", task=task, status=status)
        self.env.sql("UPDATE agents SET ended=?, spawn_args='{\"claimed_at\": 9999999999}' WHERE name=?", (ended, name))

    def tick(self, statvfs=OVER):
        return self.ok("tick", extra_env={"FACTORY_TMP_STATVFS": statvfs})

    def swept(self):
        return self.env.sql("SELECT detail FROM events WHERE action='tmp-swept'")

    def test_finished_agent_dir_removed_with_one_event(self):
        d, qa = self.pad(), self.dir(self.tmp, "qa-t-0001-abc")
        self.agent("zz-test-c1", "done", ago(45))
        self.tick()
        self.assertFalse(d.exists())
        self.assertFalse(qa.exists())
        ev = self.swept()
        self.assertEqual(len(ev), 1)
        self.assertRegex(ev[0]["detail"], r'"dirs": 2')
        self.assertIn('"mb"', ev[0]["detail"])

    def test_stopped_and_lost_count_as_finished(self):
        d = self.pad()
        self.agent("zz-test-c1", "stopped", ago(31))
        self.agent("zz-test-c2", "lost", ago(60))
        self.tick()
        self.assertFalse(d.exists())

    def test_live_starting_queued_agent_keeps_dir(self):
        d = self.pad()
        self.agent("zz-test-c1", "done", ago(120))
        for i, st in enumerate(("live", "starting", "queued")):
            self.agent(f"zz-test-l{i}", st)
            self.tick()
            self.assertTrue(d.exists(), st)
            self.env.sql("UPDATE agents SET status='done', ended=? WHERE name=?", (ago(120), f"zz-test-l{i}"))
        self.tick()
        self.assertFalse(d.exists())
        self.assertEqual(self.swept()[0]["detail"].count("dirs"), 1)

    def test_recently_finished_keeps_dir(self):
        d = self.pad()
        self.agent("zz-test-c1", "done", ago(10))
        self.tick()
        self.assertTrue(d.exists())
        self.assertEqual(self.swept(), [])

    def test_unknown_dirs_kept(self):
        self.agent("zz-test-c1", "done", ago(120))
        keep = [self.dir(self.base, self.slug.replace("-worktrees-", "")),      # HQ scratch
                self.dir(self.base, f"{self.slug}G-001-T-0001-extra"),
                self.pad("T-9999"),                                              # task with no agent rows
                self.dir(self.tmp, "qa-nothing-here"),
                self.dir(self.tmp, "other-t-0001"),
                self.dir(self.home_wt(), "G-001-T-0001")]                        # a worktree
        self.tick()
        for d in keep:
            self.assertTrue(d.exists(), d.name)

    def home_wt(self):
        w = self.env.home_dir / "worktrees"
        w.mkdir(exist_ok=True)
        return w

    def test_unreadable_ended_keeps_dir(self):
        d = self.pad()
        self.agent("zz-test-c1", "done", None)
        self.tick()
        self.assertTrue(d.exists())

    def test_symlink_not_followed(self):
        outside = self.env.home / "outside"
        outside.mkdir()
        (outside / "keep").write_text("k")
        link = self.base / f"{self.slug}G-001-T-0001"
        link.symlink_to(outside)
        self.agent("zz-test-c1", "done", ago(120))
        self.tick()
        self.assertTrue(link.is_symlink())
        self.assertEqual((outside / "keep").read_text(), "k")

    def test_below_threshold_nothing_removed(self):
        d = self.pad()
        self.agent("zz-test-c1", "done", ago(120))
        self.tick(ROOMY)
        self.assertTrue(d.exists())

    def test_threshold_is_configurable(self):
        d = self.pad()
        self.agent("zz-test-c1", "done", ago(120))
        self.ok("config", "tmp_sweep_pct", "90")
        self.tick(OVER)
        self.assertTrue(d.exists())

    def test_res_shows_last_sweep(self):
        out = self.ok("res").stdout
        self.assertIn("never swept", out)
        self.pad()
        self.agent("zz-test-c1", "done", ago(120))
        self.tick()
        out = self.ok("res").stdout
        self.assertRegex(out, r"tmp sweep \(over 80%\): last \S+ freed [\d.]+ MB in 1 dirs")

    def test_still_over_max_alerts_director_once(self):
        self.env.seed_agent("zz-director", "director", goal="G-001")
        subj = "SELECT subject FROM messages WHERE sender='tick' AND subject LIKE 'tmp sweep:%'"
        self.tick(OVER)
        self.assertEqual(self.env.sql(subj), [])
        self.tick(FULL)
        self.tick(FULL)
        self.assertEqual(len(self.env.sql(subj)), 1)
        self.tick(OVER)   # re-arms
        self.tick(FULL)
        self.assertEqual(len(self.env.sql(subj)), 2)


if __name__ == "__main__":
    unittest.main()
