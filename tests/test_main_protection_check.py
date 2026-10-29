"""Tick check (R-a, T-0638): the public repo's main must require PRs and enforce_admins; else alert the boss at most hourly."""
import datetime as dt
import json
import unittest

from harness import FactoryTestCase

SLUG = "acme/hive"
GOOD = {"enforce_admins": {"enabled": True}, "required_pull_request_reviews": {"required_approving_review_count": 0}}
NO_ADMINS = {"enforce_admins": {"enabled": False}, "required_pull_request_reviews": {"required_approving_review_count": 0}}
NO_PR = {"enforce_admins": {"enabled": True}}
T0 = dt.datetime(2026, 1, 1, 12, 0, 0).astimezone()


class MainProtectionCheckTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.sql("INSERT INTO config(key,value) VALUES('public_text_repos',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", (SLUG,))

    def gh_answers(self, out=None, exit=0, err=""):
        rule = {"match": [f"repos/{SLUG}/branches/main/protection"], "out": json.dumps(out) if out is not None else "",
                "exit": exit, "err": err}
        self.env.set_gh(api=[rule])

    def tick_at(self, minutes):
        when = (T0 + dt.timedelta(minutes=minutes)).isoformat(timespec="seconds")
        with self.env.load_factory() as mod:
            mod.now = lambda: when
            con = mod.db()
            mod.main_protection_alert(con)
            con.commit()

    def alerts(self):
        return self.env.sql("SELECT recipient, subject, body FROM messages WHERE subject LIKE 'public repo%'")

    def test_compliant_main_sends_nothing(self):
        self.gh_answers(GOOD)
        self.tick_at(0)
        self.assertEqual(self.alerts(), [])

    def test_missing_enforce_admins_alerts_the_boss(self):
        self.gh_answers(NO_ADMINS)
        self.tick_at(0)
        [m] = self.alerts()
        self.assertEqual(m["recipient"], "boss")
        self.assertIn("enforce_admins is off", m["body"])
        self.assertIn(f"ci/publish_repo.py --repo {SLUG}", m["body"])

    def test_missing_pr_requirement_alerts(self):
        self.gh_answers(NO_PR)
        self.tick_at(0)
        [m] = self.alerts()
        self.assertIn("pull requests are not required", m["body"])

    def test_unprotected_branch_alerts(self):
        self.gh_answers(exit=1, err="gh: Branch not protected (HTTP 404)")
        self.tick_at(0)
        [m] = self.alerts()
        self.assertIn("no branch protection at all", m["body"])

    def test_alert_repeats_at_most_hourly(self):
        self.gh_answers(NO_ADMINS)
        for minute in (0, 1, 30, 59):
            self.tick_at(minute)
        self.assertEqual(len(self.alerts()), 1)
        self.tick_at(61)
        self.assertEqual(len(self.alerts()), 2)

    def test_a_gh_failure_is_not_an_alert_but_is_recorded(self):
        self.gh_answers(exit=1, err="gh: network down")
        self.tick_at(0)
        self.assertEqual(self.alerts(), [])
        self.assertEqual(len(self.env.sql("SELECT 1 FROM events WHERE action='main-protection-check-failed'")), 1)

    def test_no_public_repo_configured_is_not_checked(self):
        self.env.sql("UPDATE config SET value='' WHERE key='public_text_repos'")
        self.gh_answers(NO_ADMINS)
        self.tick_at(0)
        self.assertEqual(self.alerts(), [])

    def test_a_frozen_repo_is_not_checked(self):
        self.env.sql("INSERT INTO config(key,value) VALUES('frozen_repos',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", (SLUG,))
        self.gh_answers(NO_ADMINS)
        self.tick_at(0)
        self.assertEqual(self.alerts(), [])


if __name__ == "__main__":
    unittest.main()
