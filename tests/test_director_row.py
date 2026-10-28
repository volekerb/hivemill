"""G-018 D: director row in `factory scorecard --days N` and the digest."""
import datetime as dt
import json
import time
import unittest

from harness import FactoryTestCase

NOW = "2026-10-27T09:00:00"
INS = ("INSERT INTO util_samples(ts,live,workers,leads,judges,hq,other,queued,queued_workers,cap,target,demand,p1_starved,p2_starved) "
       "VALUES(?,?,?,0,0,0,0,?,0,6,?,0,?,'[]')")
LINE = ("director (24h, daytime 08-20): avg workers 9.0 · share 45% · target 10 · median workers with queue 6 · "
        "P1 starved 30 min (longest 30 min G-1) · cap breaches 1 · boss scores 3, 2")


def local(day, h, m):
    return dt.datetime(2026, 10, day, h, m).astimezone().isoformat(timespec="seconds")


class Base(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.run("config", "day_start_hour", "8")
        self.env.run("config", "day_end_hour", "20")

    def sample(self, ts, workers, live=20, queued=0, target=10, p1=()):
        self.env.sql(INS, (ts, live, workers, queued, target, json.dumps(list(p1))))

    def fixture(self):
        for m in range(50, 60):
            self.sample(local(27, 7, m), 0, p1=["G-9"])
        for m in range(60):
            if m < 30:
                self.sample(local(27, 8, m), 6, queued=2, p1=["G-1"])
            else:
                self.sample(local(27, 8, m), 12)
        self.env.seed_agent("zz-test-director", "director")
        self.env.sql("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
                     (local(27, 8, 10), "zz-test-director", "config", "max_live_agents", "set", "8"))
        self.msg("director score 2026-10-26: 3/5")
        self.msg("director score 2026-10-27: 2/5")

    def msg(self, subject):
        self.env.sql("INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES('boss','director','msg',?,'',?)",
                     (subject, local(27, 8, 30)))

    def card(self, *args, **kw):
        out = self.ok("scorecard", *args, extra_env={"FACTORY_NOW": NOW, **kw.get("env", {})}).stdout
        return out.rstrip("\n").split("\n")[-1]


class DirectorRowTests(Base):
    def test_exact_line(self):
        self.fixture()
        self.assertEqual(self.card(), LINE)

    def test_blank_line_before_director(self):
        out = self.ok("scorecard", extra_env={"FACTORY_NOW": NOW}).stdout
        self.assertIn("\n\ndirector (24h", out)

    def test_two_low_scores(self):
        self.fixture()
        self.msg("director score 2026-10-26: 2/5")
        self.msg("director score 2026-10-27: 1/5")
        self.assertTrue(self.card().endswith("boss scores 2, 1 · TWO LOW SCORES"))

    def test_no_scores(self):
        self.fixture()
        self.env.sql("DELETE FROM messages")
        self.assertTrue(self.card().endswith("cap breaches 1 · boss scores -"))

    def test_one_score(self):
        self.env.sql("DELETE FROM messages")
        self.msg("director score 2026-10-27: 4/5")
        self.assertTrue(self.card().endswith("boss scores 4"))

    def test_no_daytime_samples(self):
        self.sample(local(27, 7, 55), 3)
        self.assertIn("no utilization samples · cap breaches 0", self.card())

    def test_gap_ends_episode(self):
        for m in (0, 1, 2):
            self.sample(local(27, 8, m), 1, p1=["G-1"])
        for m in (8, 9, 10, 11, 12, 13):  # 02 -> 08 is a 6-min gap
            self.sample(local(27, 8, m), 1, p1=["G-1"])
        line = self.card()
        # 08:00-08:02 weigh 1+1+5 (the gap is capped at 5), 08:08-08:13 weigh 6: longest is the larger part, not the 13 sum
        self.assertIn("(longest 7 min G-1)", line)
        self.assertIn("P1 starved 13 min", line)

    def test_owner_config_set_not_a_breach(self):
        self.env.sql("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
                     (local(27, 8, 10), "owner", "config", "max_live_agents", "set", "8"))
        self.assertIn("cap breaches 0", self.card())

    def test_days_2_counts_previous_day(self):
        self.sample(local(26, 8, 30), 4)
        self.assertIn("no utilization samples", self.card())
        line = self.card("--days", "2")
        self.assertTrue(line.startswith("director (48h, daytime 08-20): avg workers 4.0"), line)

    def test_days_invalid_exit_2(self):
        self.refused(2, "scorecard", "--days", "0")
        self.refused(2, "scorecard", "--days", "x")

    def test_bad_config_defaults(self):
        self.env.run("config", "day_start_hour", "abc")
        self.assertIn("daytime 08-20", self.card())
        self.env.run("config", "day_start_hour", "20")
        self.env.run("config", "day_end_hour", "8")
        self.assertIn("daytime 08-20", self.card())

    def test_digest_section(self):
        self.fixture()
        self.env.seed_goal()
        out = self.ok("digest", extra_env={"FACTORY_NOW": NOW}).stdout
        self.assertIn("## Director\n\n" + LINE + "\n", out)
        self.assertLess(out.index("## Director"), out.index("## G-001"))

    def test_performance_35_days(self):
        con = self.env.db()
        base = dt.datetime(2026, 9, 22, 0, 0).astimezone()
        rows = [((base + dt.timedelta(minutes=i)).isoformat(timespec="seconds"), 20, 5, 0, 10, "[]") for i in range(35 * 1440)]
        con.executemany(INS, rows)
        con.commit()
        t = time.monotonic()
        self.ok("scorecard", "--days", "2", extra_env={"FACTORY_NOW": NOW})
        self.assertLess(time.monotonic() - t, 2.0)


if __name__ == "__main__":
    unittest.main()
