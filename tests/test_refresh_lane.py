"""G-022 refresh_lane (T-0628): with config refresh_lane=on only the head of the merge queue (the oldest BEHIND judge
row) gets its judge started, and so refreshed; the other BEHIND rows stay queued as 'lane: position N'. Off: unchanged.
Real CLI as a subprocess (the tick), gh stub from the harness."""
import json

from harness import FactoryTestCase  # noqa: F401  (DequeueBase builds on it)
from test_dequeue import DequeueBase, at
from test_judge_gate import GREEN

URL = "https://github.com/zz-test/repo/pull/%d"
TASKS = (("T-0002", 2), ("T-0003", 3), ("T-0004", 4))


class RefreshLane(DequeueBase):
    def setUp(self):
        super().setUp()
        for i, (tid, n) in enumerate(TASKS):
            self.env.seed_task(tid, pr_url=URL % n, team="g-001-a")
            self.env.seed_pr(url=URL % n, checks=GREEN)
            self.set_state(URL % n, "BEHIND")
            self.queue_judge(f"zz-jd{n}", tid, i + 1)

    def set_state(self, url, state):
        st = json.loads(self.env.gh_state.read_text())
        st["prs"][url]["mergeStateStatus"] = state
        self.env.gh_state.write_text(json.dumps(st))
        self.env.sql("DELETE FROM config WHERE key=?", ("judge_ci:" + url,))

    def queue_judge(self, name, task, n):
        self.env.set_herdr(fail=True)
        self.assertIn("QUEUED", self.ok("spawn", "judge", "--name", name, "--goal", "G-003", "--task", task).stdout)
        self.env.sql("UPDATE agents SET queued_at=? WHERE name=?", (at(n), name))
        self.env.set_herdr(fail=False)

    def tick(self):
        self.ok("config", "max_live_agents", "10")
        self.ok("tick")

    def events(self, action):
        return {r["entity_id"]: r["detail"] for r in
                self.env.sql("SELECT entity_id, detail FROM events WHERE action=? ORDER BY id", (action,))}

    def prechecks(self):
        """Tasks whose judge spawn went past the lane to the spawn pre-check, where a BEHIND head is refreshed. The
        stub PRs have no task branch, so nothing is pushed; the spawn then re-queues the row with 'T-x PR is BEHIND'
        (judge_hold after the pre-check), which is the trace of exactly the judges that were let through."""
        return sorted({r["detail"].split()[0] for r in self.env.sql(
            "SELECT detail FROM events WHERE action='queued' AND detail LIKE 'T-% PR is BEHIND'")})

    def test_off_every_behind_judge_is_started(self):
        self.tick()
        self.assertEqual(self.prechecks(), ["T-0002", "T-0003", "T-0004"])
        self.assertEqual(self.events("lane-hold"), {})

    def test_on_only_the_queue_head_is_refreshed_and_the_rest_show_their_position(self):
        self.ok("config", "refresh_lane", "on")
        self.tick()
        self.assertEqual(self.prechecks(), ["T-0002"])
        self.assertEqual(self.events("lane-hold"), {"T-0003": "lane: position 2", "T-0004": "lane: position 3"})
        self.assertEqual([self.status(n) for n in ("zz-jd3", "zz-jd4")], ["queued", "queued"])
        self.tick()  # nothing changes, no repeated event
        self.assertEqual(self.prechecks(), ["T-0002"])
        self.assertEqual(len(self.env.sql("SELECT 1 FROM events WHERE action='lane-hold'")), 2)

    def test_on_merge_the_next_one_is_refreshed(self):
        self.ok("config", "refresh_lane", "on")
        self.tick()
        self.env.sql("UPDATE tasks SET status='done' WHERE id='T-0002'")
        self.env.sql("UPDATE agents SET status='exited' WHERE name='zz-jd2'")
        self.tick()
        self.assertEqual(self.prechecks(), ["T-0002", "T-0003"])
        self.assertEqual(self.events("lane-hold")["T-0004"], "lane: position 2")

    def test_on_a_pr_that_is_not_behind_is_not_held(self):
        self.ok("config", "refresh_lane", "on")
        self.set_state(URL % 4, "CLEAN")
        self.tick()
        self.assertEqual(self.prechecks(), ["T-0002"])
        self.assertEqual(self.events("lane-hold"), {"T-0003": "lane: position 2"})
        self.assertEqual(self.status("zz-jd4"), "live")  # up to date: nothing to refresh, so no lane
