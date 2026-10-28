"""T-0516: the local merge gate wired into the judge. A project with a non-empty gate_cmd is in local mode: cmd_judge,
judge_pr_ready and the tick read gate_runs; any other project keeps the GitHub-checks path (test_judge*.py). Gate runs
are rows seeded straight into gate_runs, except in the tick launch tests, where a stub gate_cmd (`true`) really runs."""
import json
import os
import stat
import time
import unittest

import test_judge_refresh as tjr
from harness import PR_URL, FactoryTestCase, gates_summary
from test_judge_gate import GREEN
from test_dequeue import DequeueBase, at

LEAD = "zz-test-lead"


def seed_run(env, head, status="pass", main="m" * 40, test_count=None, main_count=None, pid=None, task="T-0001", note=None):
    env.sql("INSERT INTO gate_runs(project_id,task_id,pr_url,kind,head_sha,main_sha,status,exit_code,test_count,main_count,cmd,pid,note,created) "
            "VALUES('zz-test-proj',?,?,'pr',?,?,?,?,?,?,'true',?,?,'2026-01-01T00:00:00+00:00')",
            (task, PR_URL, head, main, status, 0 if status == "pass" else 1, test_count, main_count, pid, note))


def runs(env):
    return env.sql("SELECT * FROM gate_runs ORDER BY id")


class LocalJudge(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj")
        e.seed_task()
        for name, role in (("zz-test-reviewer", "reviewer"), ("zz-test-qa", "qa"), ("zz-test-judge", "judge")):
            e.seed_agent(name, role, goal="G-001", task="T-0001")
        self.head = e.use_git_head()
        e.seed_pr(checks=GREEN)
        self.ok("config", "judge_ci_wait_s", "0")
        self.ok("verdict", "T-0001", "--pass", "--summary", "ok", agent="zz-test-reviewer")
        self.ok("verdict", "T-0001", "--pass", "--summary", gates_summary(), agent="zz-test-qa")
        e.sql("UPDATE projects SET gate_cmd='true' WHERE id='zz-test-proj'")

    def judge(self):
        return self.env.run("judge", "T-0001", "--merge", "--reason", "zz-test", agent="zz-test-judge")

    def merges(self):
        return [c for c in self.env.calls("gh") if c["argv"][:2] == ["pr", "merge"]]

    def refused(self, text):
        p = self.judge()
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn(text, p.stderr)
        self.assertEqual(self.merges(), [])
        return p

    def test_pass_on_this_head_merges(self):
        seed_run(self.env, self.head)
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(len(self.merges()), 1)

    def test_no_required_github_check_needed_in_local_mode(self):
        self.ok("config", "required_checks", "unittest")
        self.env.seed_pr(checks=[])
        seed_run(self.env, self.head)
        self.assertEqual(self.judge().returncode, 0)

    def test_github_mode_unchanged_without_gate_cmd(self):
        self.env.sql("UPDATE projects SET gate_cmd='' WHERE id='zz-test-proj'")
        self.assertEqual(self.judge().returncode, 0)  # green checks, no gate run, merges
        self.assertEqual(runs(self.env), [])

    def test_no_run_refuses_and_enqueues_one(self):
        self.refused("local gate run #1 queued on")
        r = runs(self.env)
        self.assertEqual([(x["status"], x["head_sha"], x["task_id"]) for x in r], [("queued", self.head, "T-0001")])

    def test_running_refuses_and_does_not_enqueue_another(self):
        seed_run(self.env, self.head, status="running", pid=os.getpid())
        self.refused("local gate run #1 running")
        self.assertEqual(len(runs(self.env)), 1)

    def test_fail_refuses(self):
        seed_run(self.env, self.head, status="fail")
        self.refused("local gate run #1 fail")

    def test_error_refuses(self):
        seed_run(self.env, self.head, status="error", note="no slot")
        self.refused("error on")

    def test_pass_never_carries_across_a_head_change(self):
        seed_run(self.env, "d" * 40)
        self.refused("local gate run #2 queued on")  # the pass of another head does not count; this head is queued
        self.assertEqual([r["status"] for r in runs(self.env)], ["pass", "queued"])

    def test_failing_github_check_still_refuses_in_local_mode(self):
        seed_run(self.env, self.head)
        self.env.seed_pr(checks=[{"name": "unittest", "status": "COMPLETED", "conclusion": "FAILURE"}])
        self.refused("checks not green")

    def test_pass_on_older_main_is_fine_when_head_contains_main(self):
        seed_run(self.env, self.head, main="e" * 40)  # head contains the current main, so the tested tree does too
        self.assertEqual(self.judge().returncode, 0)

    def test_pass_on_older_main_refused_when_head_lacks_main(self):
        # cmd_judge refreshes a stale head before it gets here, so the refusal is asserted on local_gate_problems itself
        seed_run(self.env, self.head, main="e" * 40)
        with self.env.load_factory() as mod:
            con = mod.db()
            self.assertEqual(mod.local_gate_problems(con, {"id": "zz-test-proj"}, PR_URL, self.head, "f" * 40, True), [])
            self.assertEqual(mod.local_gate_problems(con, {"id": "zz-test-proj"}, PR_URL, self.head, "e" * 40, False), [])
            why = mod.local_gate_problems(con, {"id": "zz-test-proj"}, PR_URL, self.head, "f" * 40, False)
            self.assertEqual(len(why), 1)
            self.assertIn("main is now ffffffff and the head does not contain it", why[0])
            self.assertEqual(mod.local_gate_problems(con, {"id": "zz-test-proj"}, PR_URL, self.head, "f" * 40, False, behind_ok=True), [])

    def test_count_drop_refused_without_tests_removed(self):
        seed_run(self.env, self.head, test_count=90, main_count=100)
        self.refused("test count dropped 100 -> 90")

    def test_count_drop_allowed_with_tests_removed_line(self):
        seed_run(self.env, self.head, test_count=90, main_count=100)
        self.env.seed_pr(checks=GREEN, body="Removed a dead module.\ntests-removed: the module is gone\n")
        self.assertEqual(self.judge().returncode, 0)

    def test_equal_or_higher_count_passes(self):
        seed_run(self.env, self.head, test_count=100, main_count=100)
        self.assertEqual(self.judge().returncode, 0)


class LocalJudgeRefresh(tjr.JudgeRefreshTests):
    """A stale head is refreshed by the judge; in local mode it enqueues a gate run for the new head and exits requeued."""

    def setUp(self):
        super().setUp()
        self.env.sql("UPDATE projects SET gate_cmd='true' WHERE id='zz-test-proj'")

    def set_wait(self, secs):
        self.env.sql("INSERT INTO config(key,value) VALUES('judge_ci_wait_s',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", (str(secs),))

    def test_stale_head_refreshed_enqueues_gate_and_requeues_without_waiting(self):
        main = self.main_commit()
        self.set_wait(600)  # the requeue must not wait
        t0 = time.monotonic()
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertLess(time.monotonic() - t0, 60)
        self.assertIn("REQUEUED", p.stdout)
        new = self.remote_ref(f"refs/heads/{BRANCH}")
        self.assertNotEqual(new, self.head)
        self.assertEqual([(r["status"], r["head_sha"]) for r in runs(self.env)], [("queued", new)])
        self.assertEqual(self.merges(), [])
        self.assertEqual(self.events("judge-requeued")[0]["detail"], f"gate run #1 queued for {new[:8]}")

    def test_old_head_pass_does_not_clear_the_refreshed_head(self):
        seed_run(self.env, self.head, main=self.base)
        self.main_commit()
        p = self.judge()
        self.assertIn("REQUEUED", p.stdout)
        self.set_wait(0)  # no runner here: do not wait for the queued run
        p = self.judge()  # the new head has only the queued run
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("local gate run #2 queued", p.stderr)
        self.assertEqual(self.merges(), [])


BRANCH = tjr.BRANCH
for _n in dir(tjr.JudgeRefreshTests):  # only this file's cases: the inherited ones are test_judge_refresh.py's own
    if _n.startswith("test_") and _n not in LocalJudgeRefresh.__dict__:
        setattr(LocalJudgeRefresh, _n, None)


class TickGate(DequeueBase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.sql("UPDATE goals SET project_id='zz-test-proj' WHERE id='G-003'")
        e.sql("UPDATE projects SET gate_cmd='true', gh_repo='zz-test/repo' WHERE id='zz-test-proj'")
        e.seed_agent("zz-test-director", "director")
        e.seed_task("T-0002", goal="G-003", pr_url=PR_URL, team="g-001-a")
        self.head = e.use_git_head()
        e.seed_pr(checks=GREEN)
        self.xenv = {"FACTORY_GATE_SLOT_WAIT_S": "0", "FACTORY_GATE_POLL_S": "0"}

    def tick(self):
        return self.env.run("tick", extra_env=self.xenv)

    def queue_judge(self):
        self.env.set_herdr(fail=True)
        self.assertIn("QUEUED", self.ok("spawn", "judge", "--name", "zz-jd", "--goal", "G-003", "--task", "T-0002").stdout)
        self.env.sql("UPDATE agents SET queued_at=? WHERE name='zz-jd'", (at(1),))
        self.env.set_herdr(fail=False)
        self.ok("config", "max_live_agents", "2")

    def wait_status(self, run_id, want, secs=90):
        end = time.monotonic() + secs
        while time.monotonic() < end:
            st = self.env.sql("SELECT status FROM gate_runs WHERE id=?", (run_id,))[0]["status"]
            if st == want:
                return
            time.sleep(0.2)
        self.fail(f"gate run #{run_id} is {st}, wanted {want}")

    def queued_row(self, task="T-0002"):
        self.env.sql("INSERT INTO gate_runs(project_id,task_id,pr_url,kind,head_sha,status,cmd,created) "
                     "VALUES('zz-test-proj',?,?,'pr',?,'queued','true','2026-01-01T00:00:00+00:00')", (task, PR_URL, self.head))

    # -- judge_pr_ready / judge_hold -------------------------------------
    def test_pass_row_starts_the_judge(self):
        seed_run(self.env, self.head, task="T-0002")
        self.queue_judge()
        self.assertEqual(self.tick().returncode, 0)
        self.assertEqual(self.status("zz-jd"), "live")

    def test_running_row_holds_the_judge(self):
        seed_run(self.env, self.head, status="running", pid=os.getpid(), task="T-0002")
        self.queue_judge()
        self.tick()
        self.assertEqual(self.status("zz-jd"), "queued")

    def test_no_row_enqueues_one_run_and_holds_the_judge(self):
        self.env.sql("UPDATE projects SET gate_cmd='false' WHERE id='zz-test-proj'")  # the run it launches fails: no pass to start on
        self.queue_judge()
        self.tick()
        self.assertEqual(self.status("zz-jd"), "queued")
        self.assertEqual(len(runs(self.env)), 1)
        self.assertEqual(runs(self.env)[0]["head_sha"], self.head)

    def test_fail_row_sends_one_message_to_the_lead_even_over_two_ticks(self):
        seed_run(self.env, self.head, status="fail", task="T-0002")
        self.queue_judge()
        self.tick()
        self.tick()
        self.assertEqual(self.status("zz-jd"), "queued")
        msgs = self.env.sql("SELECT recipient,subject FROM messages WHERE subject LIKE 'local gate fail%'")
        self.assertEqual([(m["recipient"], m["subject"]) for m in msgs], [("zz-lead", f"local gate fail: T-0002 @{self.head[:8]}")])

    def test_github_mode_project_never_touches_gate_runs(self):
        self.env.sql("UPDATE projects SET gate_cmd='' WHERE id='zz-test-proj'")
        self.queue_judge()
        self.tick()
        self.assertEqual((self.status("zz-jd"), runs(self.env)), ("live", []))

    # -- the tick launches queued runs -----------------------------------
    def test_tick_launches_a_queued_run_which_runs_to_pass_on_the_exact_head(self):
        self.queued_row()
        self.assertEqual(self.tick().returncode, 0)
        self.wait_status(1, "pass")
        r = runs(self.env)[0]
        self.assertEqual((r["head_sha"], r["exit_code"], r["task_id"]), (self.head, 0, "T-0002"))
        self.assertEqual(len(runs(self.env)), 1)

    def fake_systemd_run(self):
        """A systemd-run on PATH that logs its argv and runs the command after `--` detached (what the transient unit does)."""
        d = self.env.home / "fakebin"
        d.mkdir(exist_ok=True)
        f = d / "systemd-run"
        f.write_text('#!/bin/bash\nprintf "%s\\n" "$*" >> "$SYSTEMD_RUN_LOG"\nwhile [ "$1" != "--" ]; do shift; done; shift\n'
                     'setsid "$@" </dev/null >/dev/null 2>&1 &\n')
        f.chmod(f.stat().st_mode | stat.S_IEXEC)
        log = self.env.home / "systemd-run.log"
        self.xenv.update({"INVOCATION_ID": "zz-test", "PATH": f"{d}:{self.env.stub_bin}:{os.environ['PATH']}", "SYSTEMD_RUN_LOG": str(log)})
        return log

    def test_under_systemd_the_runner_gets_its_own_transient_unit(self):
        log = self.fake_systemd_run()
        self.queued_row()
        self.assertEqual(self.tick().returncode, 0)
        line = log.read_text().strip()
        self.assertIn("--user --collect --quiet --unit factory-gate-1 -- ", line)
        self.assertIn("gate run --run-id 1", line)
        self.wait_status(1, "pass")  # the unit's runner ran the gate on the exact head
        self.assertEqual(runs(self.env)[0]["head_sha"], self.head)

    def test_failed_systemd_run_leaves_the_run_queued_for_the_next_tick(self):
        d = self.env.home / "fakebin"
        d.mkdir()
        f = d / "systemd-run"
        f.write_text("#!/bin/bash\nexit 1\n")
        f.chmod(f.stat().st_mode | stat.S_IEXEC)
        self.xenv.update({"INVOCATION_ID": "zz-test", "PATH": f"{d}:{self.env.stub_bin}:{os.environ['PATH']}"})
        self.queued_row()
        self.assertEqual(self.tick().returncode, 0)
        self.assertEqual(self.env.sql("SELECT status,pid FROM gate_runs WHERE id=1")[0]["status"], "queued")

    def test_tick_respects_gate_max_parallel(self):
        seed_run(self.env, self.head, status="running", pid=os.getpid(), task="T-0001")
        self.queued_row()
        self.tick()
        row = self.env.sql("SELECT status,pid FROM gate_runs WHERE id=2")[0]
        self.assertEqual((row["status"], row["pid"]), ("queued", None))
        self.ok("config", "gate_max_parallel", "2")
        self.tick()
        self.wait_status(2, "pass")

    def test_dead_runner_row_is_reaped_but_an_unlaunched_queued_row_is_not(self):
        self.ok("config", "gate_max_parallel", "0")  # cap clamps to 1; the running row below fills it
        seed_run(self.env, self.head, status="running", pid=os.getpid(), task="T-0001")
        self.queued_row()
        self.tick()
        self.assertEqual(self.env.sql("SELECT status FROM gate_runs WHERE id=2")[0]["status"], "queued")
        self.env.sql("UPDATE gate_runs SET pid=99999999 WHERE id=1")
        self.tick()
        self.assertEqual(self.env.sql("SELECT status,note FROM gate_runs WHERE id=1")[0]["status"], "error")


if __name__ == "__main__":
    unittest.main()
