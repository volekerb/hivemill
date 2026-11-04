"""T-1014 (G-022 SC3): the tick's optional PR probes reuse one `gh pr view` per PR and head for pr_view_ttl_s, and are skipped
(one alert per hour) while the GraphQL quota is below gh_quota_min. Merge-gate reads never use the cache and never skip."""
import datetime as dt
import json

from harness import FactoryTestCase, PR_URL

LEAD = "lead-g-001-a"
TEAM = "g-001-a"
BRANCH = "factory/t-0001"
SHA1, SHA2 = "a" * 40, "b" * 40
RUNNING = {"name": "slow", "status": "IN_PROGRESS", "conclusion": ""}  # never green: nothing is mailed, so every tick probes
T0 = 1_800_000_000  # a fixed epoch: the tick's clock


class Base(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(TEAM, lead=LEAD)
        e.seed_agent(LEAD, "lead", goal="G-001", team=TEAM, status="exited")
        e.seed_agent("zz-director", "director")
        e.seed_task("T-0001", status="in_review", team=TEAM)
        e.sql("UPDATE tasks SET branch=? WHERE id='T-0001'", (BRANCH,))
        e.sql("UPDATE teams SET parked_at='2026-01-01T00:00:00+00:00' WHERE id=?", (TEAM,))
        self.ok("config", "lead_parking", "1")
        self.ok("config", "pr_view_ttl_s", "120")

    def step(self, now, head=SHA1, sha=SHA1, fail=False):
        """One tick_ci_wake pass at `now` seconds; returns the gh probes made. gh itself is the only mocked boundary: the
        probe function is replaced by a counter that answers a running PR on `sha` (or an error), origin's head is `head`."""
        probes = []
        with self.env.load_factory() as f:
            con = f.db()
            f.digest_clock = lambda: dt.datetime.fromtimestamp(now, dt.timezone.utc)
            f._ci_remote_heads = lambda: {BRANCH: head}

            def probe(url, timeout):
                probes.append(url)
                if fail:
                    return OSError("gh pr view exit 1: boom")
                return {"number": 1, "state": "OPEN", "headRefOid": sha, "mergeStateStatus": "CLEAN", "statusCheckRollup": [RUNNING]}

            f._ci_wake_probe = probe
            f.tick_ci_wake(con)
            con.commit()
        return probes

    def quota_stub(self, **rule):
        self.env.set_gh(api=[{"match": ["graphql"], **rule}])

    def quota_calls(self):
        return [c for c in self.env.calls("gh") if c["argv"][:2] == ["api", "graphql"]]


class PrViewCacheTests(Base):
    def test_same_head_two_passes_inside_ttl_one_call(self):
        self.assertEqual(len(self.step(T0)), 1)
        self.assertEqual(self.step(T0 + 60), [])  # the next tick: served from the cache
        self.assertEqual(len(self.step(T0 + 121)), 1)  # TTL over: a fresh call

    def test_new_head_inside_ttl_is_a_fresh_call(self):
        self.assertEqual(len(self.step(T0)), 1)
        self.assertEqual(len(self.step(T0 + 30, head=SHA2, sha=SHA2)), 1)
        self.assertEqual(self.step(T0 + 60, head=SHA2, sha=SHA2), [])

    def test_unknown_origin_head_is_a_miss(self):
        self.assertEqual(len(self.step(T0)), 1)
        with self.env.load_factory() as f:
            con = f.db()
            self.assertIsNone(f._pr_cache_get(con, PR_URL, T0 + 10, None))
            self.assertIsNotNone(f._pr_cache_get(con, PR_URL, T0 + 10, SHA1))

    def test_ttl_zero_probes_every_pass(self):
        self.ok("config", "pr_view_ttl_s", "0")
        self.assertEqual(len(self.step(T0)), 1)
        self.assertEqual(len(self.step(T0 + 30)), 1)

    def test_failed_probe_is_not_cached_and_logs_an_error(self):
        self.assertEqual(len(self.step(T0, fail=True)), 1)
        self.assertEqual(len(self.step(T0 + 30)), 1)  # the error was not remembered as an answer
        self.assertEqual(self.env.sql("SELECT count(*) c FROM events WHERE action='ci-wake-error'")[0]["c"], 1)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM config WHERE key LIKE 'prview:%'")[0]["c"], 1)

    def test_merge_gate_never_reads_the_probe_cache(self):
        """A green PR cached by a tick probe does not let `factory judge --merge` pass when gh itself fails."""
        self.env.sql("INSERT INTO config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
                     ("prview:" + PR_URL, json.dumps({"at": T0, "head": SHA1, "pr": {
                         "number": 1, "state": "OPEN", "headRefOid": SHA1, "mergeStateStatus": "CLEAN",
                         "statusCheckRollup": [{"name": "ok", "status": "COMPLETED", "conclusion": "SUCCESS"}]}})))
        self.assertEqual(self.step(T0 + 60), [])  # the row is a valid hit for the tick probes (no gh call) ...
        self.env.seed_pr(sha=SHA1, checks=[{"name": "ok", "status": "COMPLETED", "conclusion": "SUCCESS"}])
        self.env.set_gh(pr_view_exit=1)
        self.env.seed_agent("zz-judge", "judge", goal="G-001", task="T-0001")
        p = self.env.run("judge", "T-0001", "--merge", "--sha", SHA1, agent="zz-judge")
        self.assertNotEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual([c for c in self.env.calls("gh") if c["argv"][:2] == ["pr", "merge"]], [])
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"], "in_review")


class QuotaTests(Base):
    def setUp(self):
        super().setUp()
        self.ok("config", "gh_quota_min", "300")

    def alerts(self):
        return self.env.sql("SELECT subject FROM messages WHERE subject LIKE 'gh quota low%'")

    def test_low_quota_skips_the_probe_and_alerts_once_per_hour(self):
        self.quota_stub(out={"data": {"rateLimit": {"remaining": 12, "limit": 5000, "resetAt": "2026-11-04T08:21:46Z"}}})
        self.assertEqual(self.step(T0), [])
        self.assertEqual(self.step(T0 + 60), [])
        self.assertEqual(self.step(T0 + 120), [])
        self.assertEqual(len(self.alerts()), 1)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM events WHERE action='gh-quota-low'")[0]["c"], 1)
        self.assertEqual(len(self.quota_calls()), 3)  # one read per tick (>= 50 s apart), never more
        self.assertEqual(len(self.step(T0 + 3601)), 0)
        self.assertEqual(len(self.alerts()), 2)  # a new hour, a new alert

    def test_quota_read_once_per_tick_even_with_several_probes(self):
        self.quota_stub(out={"data": {"rateLimit": {"remaining": 12, "limit": 5000, "resetAt": "x"}}})
        self.env.seed_task("T-0002", status="in_review", team=TEAM, pr_url=PR_URL.replace("/1", "/2"))
        self.env.sql("UPDATE tasks SET branch='factory/t-0002' WHERE id='T-0002'")
        self.step(T0)
        self.assertEqual(len(self.quota_calls()), 1)

    def test_red_pr_alert_probe_skipped_without_throttle_mark(self):
        self.quota_stub(out={"data": {"rateLimit": {"remaining": 1, "limit": 5000, "resetAt": "x"}}})
        self.ok("config", "red_pr_owner_alert", "1")
        probes = []
        with self.env.load_factory() as f:
            con = f.db()
            f.digest_clock = lambda: dt.datetime.fromtimestamp(T0, dt.timezone.utc)
            f._ci_remote_heads = lambda: {BRANCH: SHA1}
            f._ci_wake_probe = lambda url, timeout: probes.append(url) or {}
            f.tick_red_pr_owner(con)
            con.commit()
        self.assertEqual(probes, [])
        self.assertEqual(self.env.sql("SELECT count(*) c FROM throttle WHERE key LIKE 'red-pr-probe:%'")[0]["c"], 0)
        self.assertEqual(len(self.alerts()), 1)

    def test_enough_quota_probes_normally(self):
        self.quota_stub(out={"data": {"rateLimit": {"remaining": 4000, "limit": 5000, "resetAt": "x"}}})
        self.assertEqual(len(self.step(T0)), 1)
        self.assertEqual(self.alerts(), [])

    def test_rate_limit_error_on_the_read_counts_as_exhausted(self):
        self.quota_stub(exit=1, err="GraphQL: API rate limit already exceeded for user ID 1")
        self.assertEqual(self.step(T0), [])
        self.assertEqual(len(self.alerts()), 1)

    def test_unreadable_quota_is_not_low(self):
        self.quota_stub(exit=1, err="network unreachable")
        self.assertEqual(len(self.step(T0)), 1)
        self.assertEqual(self.alerts(), [])

    def test_cache_hit_needs_no_quota_read(self):
        self.ok("config", "gh_quota_min", "0")
        self.step(T0)
        self.ok("config", "gh_quota_min", "300")
        self.quota_stub(out={"data": {"rateLimit": {"remaining": 1, "limit": 5000, "resetAt": "x"}}})
        self.assertEqual(self.step(T0 + 60), [])
        self.assertEqual(self.quota_calls(), [])  # answered from the cache: no probe, so no quota read

    def test_merge_gate_reads_are_not_skipped_while_quota_is_low(self):
        """judge_pr_ready (the judge's CI check) still calls gh with the quota at 0; gh decides, the quota signal does not."""
        self.quota_stub(out={"data": {"rateLimit": {"remaining": 0, "limit": 5000, "resetAt": "x"}}})
        self.step(T0)  # quota now known to be low
        self.env.seed_pr(sha=SHA1, checks=[{"name": "ok", "status": "COMPLETED", "conclusion": "SUCCESS"}])
        with self.env.load_factory() as f:
            con = f.db()
            ready, why = f.judge_pr_ready(con, PR_URL)
        self.assertEqual((ready, why), (True, "CI green"))
        views = [c for c in self.env.calls("gh") if c["argv"][:2] == ["pr", "view"]]
        self.assertEqual(len(views), 1)
