"""T-0723 (G-016 P0-3b): tick_ci_wake runs its gh pr view probes in a bounded thread pool; results are applied in order."""
import json
import os
import threading
import time
import unittest
from unittest import mock

from harness import FactoryEnv, FactoryTestCase

NOW = "2026-03-01T12:00:00+00:00"
OK = {"name": "unittest", "status": "COMPLETED", "conclusion": "SUCCESS"}
BAD = {"name": "build", "status": "COMPLETED", "conclusion": "FAILURE"}
RUNNING = {"name": "slow", "status": "IN_PROGRESS", "conclusion": ""}
URL = "https://github.com/zz-test/repo/pull/%d"


def sha(n):
    return f"{n:x}" * 40


# (task, team, pr state, checks); None checks = no such PR on GitHub, so gh exits 1
FIXTURE = [("T-0001", "g-001-a", "OPEN", [OK]), ("T-0002", "g-001-a", "OPEN", [BAD]), ("T-0003", "g-001-a", "OPEN", [RUNNING]),
           ("T-0004", "g-001-b", "MERGED", [OK]), ("T-0005", "g-001-b", "OPEN", None), ("T-0006", "g-001-b", "OPEN", [OK])]


def seed(env, sleep=0.0):
    env.seed_goal()
    for team in ("g-001-a", "g-001-b"):
        env.seed_team(team, lead="lead-" + team)
        env.seed_agent("lead-" + team, "lead", goal="G-001", team=team, status="exited")
        env.sql("UPDATE teams SET parked_at='2026-01-01T00:00:00+00:00' WHERE id=?", (team,))
    env.run("config", "lead_parking", "1")
    for i, (tid, team, state, checks) in enumerate(FIXTURE, 1):
        env.seed_task(tid, team=team, pr_url=URL % i)
        if checks is not None:
            env.seed_pr(url=URL % i, sha=sha(i), state=state, checks=checks)
    prs = json.loads(env.gh_state.read_text())["prs"]
    env.set_gh(pr_view_sleep=sleep, prs={u: {**pr, "mergeStateStatus": "CLEAN"} for u, pr in prs.items()})


def tick_ci_wake(env, workers, guard=False):
    """Run tick_ci_wake in-process; returns (wall seconds, [pr view urls in call order])."""
    env.run("config", "ci_wake_workers", str(workers))
    with mock.patch.dict(os.environ, {"FACTORY_NOW": NOW}), env.load_factory() as mod:
        con = mod.db()
        if guard:
            main = threading.get_ident()

            def only_main(name, real):
                def wrapped(*a, **k):
                    if threading.get_ident() != main:
                        raise AssertionError(f"{name} called off the main thread")
                    return real(*a, **k)
                return wrapped

            for name in ("cfg", "event", "send", "_throttle_set", "_throttled"):
                if hasattr(mod, name):
                    setattr(mod, name, only_main(name, getattr(mod, name)))

            class Con:
                def __getattr__(self, attr):
                    if threading.get_ident() != main:
                        raise AssertionError(f"con.{attr} used off the main thread")
                    return getattr(con, attr)
            con_arg = Con()
        else:
            con_arg = con
        t0 = time.monotonic()
        mod.tick_ci_wake(con_arg)
        wall = time.monotonic() - t0
        con.commit()
    views = [c["argv"][2] for c in env.calls("gh") if c["argv"][:2] == ["pr", "view"]]
    return wall, views


def outcome(env):
    return {
        "mails": [tuple(r) for r in env.sql("SELECT recipient, subject, body, task_id FROM messages ORDER BY id")],
        "events": [tuple(r) for r in env.sql("SELECT entity_id, action, detail FROM events WHERE action LIKE 'ci-%' ORDER BY id")],
        "throttle": [tuple(r) for r in env.sql("SELECT key, ts FROM throttle ORDER BY key")],
    }


class CiWakeParallelTests(FactoryTestCase):
    def second_env(self):
        env = FactoryEnv()
        self.addCleanup(env.cleanup)
        return env

    def test_six_slow_probes_overlap(self):
        seed(self.env, sleep=0.5)
        wall, views = tick_ci_wake(self.env, 4)
        self.assertEqual(len(views), 6)
        self.assertLess(wall, 1.8, f"6 x 0.5 s probes took {wall:.2f}s; serial would be >= 3 s")
        print(f"\nci_wake wall, workers=4: {wall:.2f}s", flush=True)

    def test_serial_baseline_is_slower(self):
        seed(self.env, sleep=0.5)
        wall, _ = tick_ci_wake(self.env, 1)
        self.assertGreaterEqual(wall, 3.0)
        print(f"\nci_wake wall, workers=1: {wall:.2f}s", flush=True)

    def test_mails_throttle_events_match_serial_in_content_and_order(self):
        serial, pooled = self.env, self.second_env()
        seed(serial)
        seed(pooled, sleep=0.2)  # probes may finish out of order; applying them must not
        tick_ci_wake(serial, 1)
        tick_ci_wake(pooled, 4)
        a, b = outcome(serial), outcome(pooled)
        self.assertEqual(a, b)
        subjects = [m[1] for m in a["mails"] if m[1].startswith("CI ")]
        self.assertEqual(len(subjects), 3, subjects)  # T-0001 green, T-0002 red, T-0006 green
        self.assertTrue(subjects[0].startswith("CI green: T-0001") and subjects[1].startswith("CI red: T-0002")
                        and subjects[2].startswith("CI green: T-0006"), subjects)
        self.assertEqual([e[0] for e in a["events"] if e[1] == "ci-wake-error"], ["T-0005"])

    def test_one_worker_makes_the_serial_call_sequence(self):
        seed(self.env)
        _, views = tick_ci_wake(self.env, 1)
        self.assertEqual(views, [URL % i for i in range(1, 7)])

    def test_invalid_or_small_worker_counts_run_inline(self):
        for value in ("0", "-3", "junk"):
            env = self.second_env()
            seed(env)
            _, views = tick_ci_wake(env, value)
            self.assertEqual(views, [URL % i for i in range(1, 7)], value)

    def test_nothing_but_gh_runs_off_the_main_thread(self):
        seed(self.env, sleep=0.05)
        _, views = tick_ci_wake(self.env, 4, guard=True)  # an off-thread con/cfg/event/send/throttle use raises
        self.assertEqual(len(views), 6)
        self.assertEqual(len([m for m in outcome(self.env)["mails"] if m[1].startswith("CI ")]), 3)

    def test_gh_counter_counts_every_call(self):
        seed(self.env)
        with mock.patch.dict(os.environ, {"FACTORY_NOW": NOW}), self.env.load_factory() as mod:
            counts = []
            mod._pt_count = lambda k, n=1: counts.append(k)
            self.env.run("config", "ci_wake_workers", "4")
            mod.tick_ci_wake(mod.db())
        self.assertEqual(counts.count("ci_wake_gh"), 6)


if __name__ == "__main__":
    unittest.main()
