"""T-0515: local merge gate runner. `factory gate run` runs projects.gate_cmd from the DB in a detached worktree at the
exact head sha, holds a test slot, and records the outcome in gate_runs. Stub gate commands (true/false/sleep) in a
temp git repo; the real suite is never run here."""
import fcntl
import os
import subprocess
import time
import unittest
from pathlib import Path

from harness import FactoryTestCase, PR_URL, SHA_B, git

DIRECTOR = "zz-test-director"


class GateBase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_agent(DIRECTOR, "director")
        e.seed_project()
        e.sql("UPDATE projects SET gh_repo='zz-test/repo' WHERE id='zz-test-proj'")
        e.seed_goal(project="zz-test-proj")
        e.seed_task()
        self.head = e.use_git_head()
        e.seed_pr()
        self.out = e.home / "out.txt"
        self.out2 = e.home / "out2.txt"
        self.extra = {"FACTORY_GATE_SLOT_WAIT_S": "0", "FACTORY_GATE_POLL_S": "0", "GATE_OUT": str(self.out), "GATE_OUT2": str(self.out2)}
        self.slots = e.user_home / ".cache" / "factory-test-slots"
        self.wt_root = e.user_home / ".cache" / "factory-gate"

    def set_gate(self, cmd, count=None, timeout=None):
        args = ["project", "update", "zz-test-proj", "--gate-cmd", cmd]
        if count is not None:
            args += ["--gate-count-cmd", count]
        if timeout is not None:
            args += ["--gate-timeout-s", str(timeout)]
        self.ok(*args, agent=DIRECTOR)

    def gate(self, *args, agent=DIRECTOR, extra=None):
        return self.env.run("gate", *args, agent=agent, extra_env={**self.extra, **(extra or {})})

    def dead_pid(self):
        """The pid of a child that has exited and been reaped."""
        p = subprocess.Popen(["true"])
        p.wait()
        return p.pid

    def run_row(self, run_id=1):
        return self.env.sql("SELECT * FROM gate_runs WHERE id=?", (run_id,))[0]

    def assertClean(self):
        """No leftover worktree: neither on disk nor registered in the repo."""
        self.assertEqual(list(self.wt_root.glob("*/*")) if self.wt_root.exists() else [], [])
        self.assertEqual(len(git("worktree", "list", cwd=self.env.home_dir).splitlines()), 1)


class GateRun(GateBase):
    def test_pass_records_exit_sha_and_log_and_runs_at_the_exact_head(self):
        self.set_gate('git rev-parse HEAD > "$GATE_OUT"; pwd >> "$GATE_OUT"; echo gate-output')
        p = self.gate("run", "T-0001")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        sha, cwd = self.out.read_text().split()
        self.assertEqual(sha, self.head)
        self.assertTrue(cwd.startswith(str(self.wt_root)), cwd)
        r = self.run_row()
        self.assertEqual((r["status"], r["exit_code"], r["head_sha"], r["kind"], r["task_id"]), ("pass", 0, self.head, "pr", "T-0001"))
        self.assertEqual(r["main_sha"], git("rev-parse", "refs/heads/main", cwd=self.env.home / "head-origin.git"))
        self.assertIn("gate-output", Path(r["log_path"]).read_text())
        self.assertClean()

    def test_fail_records_the_exit_code(self):
        self.set_gate("exit 3")
        p = self.gate("run", "T-0001")
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        r = self.run_row()
        self.assertEqual((r["status"], r["exit_code"]), ("fail", 3))
        self.assertClean()

    def test_the_command_comes_from_the_db_not_the_pr_tree(self):
        # the PR head carries feature.txt only; a gate_cmd of `false` fails whatever the tree says
        self.set_gate("false")
        self.assertEqual(self.gate("run", "T-0001").returncode, 1)
        self.assertEqual(self.run_row()["cmd"], "false")

    def test_child_has_no_agent_identity_and_a_main_checkout_path(self):
        self.set_gate('echo "[${FACTORY_AGENT-unset}][${GH_TOKEN-unset}][${FACTORY_TEST_SLOT_HELD}]" > "$GATE_OUT"; echo "$FACTORY_HOME" >> "$GATE_OUT"; test -f "$FACTORY_MAIN/ci/test_count_gate.py"')
        p = self.gate("run", "T-0001")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        first, home = self.out.read_text().split()
        self.assertEqual(first, "[unset][unset][1]")
        self.assertTrue(home.startswith(str(self.wt_root)) and home != str(self.env.home_dir), home)  # a scratch home, not the live one

    def test_gh_token_is_not_passed_to_the_command(self):
        self.set_gate('echo "${GH_TOKEN-unset}${GITHUB_TOKEN-unset}" > "$GATE_OUT"')
        self.assertEqual(self.gate("run", "T-0001", extra={"GH_TOKEN": "secret", "GITHUB_TOKEN": "secret"}).returncode, 0)
        self.assertEqual(self.out.read_text().strip(), "unsetunset")

    def test_changed_gate_cmd_misses_the_cache(self):
        self.set_gate('echo x >> "$GATE_OUT"')
        self.assertEqual(self.gate("run", "T-0001").returncode, 0)
        self.set_gate('echo y >> "$GATE_OUT"')
        self.assertEqual(self.gate("run", "T-0001").returncode, 0)
        self.assertEqual(self.out.read_text().split(), ["x", "y"])

    def test_count_cmd_timeout_does_not_hang_on_a_grandchild(self):
        self.set_gate("true", count="sleep 300 & wait")
        t0 = time.monotonic()
        p = self.gate("run", "T-0001", extra={"FACTORY_GATE_COUNT_TIMEOUT_S": "1"})
        self.assertEqual(p.returncode, 5, p.stdout + p.stderr)
        self.assertLess(time.monotonic() - t0, 30)
        self.assertIn("timed out", self.run_row()["note"])
        self.assertClean()

    def test_runner_holds_a_slot_while_the_command_runs(self):
        probe = ("import fcntl,glob,sys\n"
                 "n=0\n"
                 "for f in glob.glob(sys.argv[1]+'/slot-*.lock'):\n"
                 "    fh=open(f,'a+')\n"
                 "    try: fcntl.flock(fh, fcntl.LOCK_SH|fcntl.LOCK_NB)\n"
                 "    except BlockingIOError: n+=1\n"
                 "print(n)")
        (self.env.home / "probe.py").write_text(probe)
        self.set_gate(f'python3 {self.env.home}/probe.py {self.slots} > "$GATE_OUT"')
        self.assertEqual(self.gate("run", "T-0001").returncode, 0)
        self.assertEqual(self.out.read_text().strip(), "1")
        # and the slot is free again afterwards
        self.assertEqual(self.env.run("res", agent=DIRECTOR).stdout.count("0 full suites running"), 1)

    def test_no_slot_is_an_error_and_the_command_never_runs(self):
        self.slots.mkdir(parents=True)
        held = [open(self.slots / f"slot-{i}.lock", "a+") for i in range(2)]
        self.addCleanup(lambda: [f.close() for f in held])
        for f in held:
            fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
        self.set_gate('echo ran > "$GATE_OUT"')
        p = self.gate("run", "T-0001")
        self.assertEqual(p.returncode, 5, p.stdout + p.stderr)
        r = self.run_row()
        self.assertEqual(r["status"], "error")
        self.assertIn("no slot", r["note"])
        self.assertFalse(self.out.exists())
        self.assertClean()

    def test_timeout_kills_the_process_group_and_is_an_error(self):
        self.set_gate('sleep 300 & echo $! > "$GATE_OUT"; wait', timeout=1)
        p = self.gate("run", "T-0001")
        self.assertEqual(p.returncode, 5, p.stdout + p.stderr)
        r = self.run_row()
        self.assertEqual(r["status"], "error")
        self.assertIn("timed out", r["note"])
        pid = int(self.out.read_text())
        for _ in range(50):  # the killed sleep is reaped by init a moment later
            try:
                os.kill(pid, 0)
            except ProcessLookupError:
                break
            time.sleep(0.1)
        else:
            self.fail(f"background child {pid} survived the timeout")
        self.assertClean()

    def test_gh_pr_view_failure_is_exit_5_error_and_the_command_never_runs(self):
        self.env.set_gh(prs={})  # `gh pr view` now fails: no such PR
        self.set_gate('echo ran > "$GATE_OUT"')
        p = self.gate("run", "T-0001")
        self.assertEqual(p.returncode, 5, p.stdout + p.stderr)
        r = self.run_row()
        self.assertEqual(r["status"], "error")
        self.assertIn("gh pr view", r["note"])
        self.assertFalse(self.out.exists())
        self.assertClean()

    def test_head_mismatch_is_an_error_and_the_command_never_runs(self):
        self.env.seed_pr(sha=SHA_B)  # GitHub says SHA_B; the fetched refs/pull/1/head is another commit
        self.set_gate('echo ran > "$GATE_OUT"')
        p = self.gate("run", "T-0001")
        self.assertEqual(p.returncode, 5, p.stdout + p.stderr)
        self.assertIn("cannot verify head and main", self.run_row()["note"])
        self.assertFalse(self.out.exists())
        self.assertClean()

    def test_counts_head_and_main_and_caches_main_per_sha(self):
        self.set_gate("true", count='echo x >> "$GATE_OUT2"; echo 7')
        self.assertEqual(self.gate("run", "T-0001").returncode, 0)
        r = self.run_row(1)
        self.assertEqual((r["test_count"], r["main_count"]), (7, 7))
        self.assertEqual(len(self.out2.read_text().split()), 2)  # head + main
        self.assertEqual(self.gate("run", "T-0001", "--no-cache").returncode, 0)
        self.assertEqual(len(self.out2.read_text().split()), 3)  # main's count was reused
        self.assertEqual(self.run_row(2)["main_count"], 7)

    def test_unreadable_count_is_an_error_not_a_pass(self):
        self.set_gate("true", count="echo not-a-number")
        p = self.gate("run", "T-0001")
        self.assertEqual(p.returncode, 5, p.stdout + p.stderr)
        r = self.run_row()
        self.assertEqual(r["status"], "error")
        self.assertIn("not an integer", r["note"])
        self.assertClean()

    def test_without_a_count_cmd_show_says_so(self):
        self.set_gate("true")
        self.assertEqual(self.gate("run", "T-0001").returncode, 0)
        self.assertIsNone(self.run_row()["test_count"])
        self.assertIn("not counted (project has no gate_count_cmd)", self.gate("show", "T-0001").stdout)

    def test_main_run_gates_the_verified_main_sha(self):
        self.set_gate('git rev-parse HEAD > "$GATE_OUT"', count="echo 5")
        p = self.gate("run", "--main", "zz-test-proj")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        main = git("rev-parse", "refs/heads/main", cwd=self.env.home / "head-origin.git")
        self.assertEqual(self.out.read_text().strip(), main)
        r = self.run_row()
        self.assertEqual((r["kind"], r["task_id"], r["head_sha"], r["main_sha"], r["test_count"], r["main_count"]),
                         ("main", None, main, main, 5, 5))
        self.assertClean()

    def test_no_gate_cmd_refuses_and_records_nothing(self):
        p = self.gate("run", "T-0001")
        self.assertEqual(p.returncode, 1)
        self.assertIn("has no gate_cmd", p.stderr)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM gate_runs")[0]["c"], 0)

    def test_run_needs_exactly_one_target(self):
        self.set_gate("true")
        self.assertEqual(self.gate("run").returncode, 2)
        self.assertEqual(self.gate("run", "T-0001", "--main", "zz-test-proj").returncode, 2)

    def test_worker_roles_are_refused(self):
        self.set_gate("true")
        for role in ("coder", "judge", "qa", "reviewer"):
            self.env.seed_agent(f"zz-{role}", role)
            p = self.gate("run", "T-0001", agent=f"zz-{role}")
            self.assertEqual(p.returncode, 3, f"{role}: {p.stdout}{p.stderr}")
        self.assertEqual(self.env.sql("SELECT count(*) c FROM gate_runs")[0]["c"], 0)

    def test_gate_max_parallel_blocks_a_second_run(self):
        self.set_gate('echo ran > "$GATE_OUT"')
        self.env.sql("INSERT INTO gate_runs(project_id,kind,status,pid,created) VALUES('zz-test-proj','main','running',?,'t')",
                     (os.getpid(),))  # a live runner (this test process)
        p = self.gate("run", "T-0001")
        self.assertEqual(p.returncode, 5, p.stdout + p.stderr)
        self.assertIn("no gate capacity", self.run_row(2)["note"])
        self.assertFalse(self.out.exists())
        self.ok("config", "gate_max_parallel", "2", agent=DIRECTOR)
        self.assertEqual(self.gate("run", "T-0001", "--no-cache").returncode, 0)

    def test_a_dead_runner_row_is_reaped_and_does_not_block(self):
        self.set_gate("true")
        self.env.sql("INSERT INTO gate_runs(project_id,kind,status,pid,created) VALUES('zz-test-proj','main','running',?,'t')", (self.dead_pid(),))
        self.env.sql("INSERT INTO gate_runs(project_id,kind,status,pid,created) VALUES('zz-test-proj','pr','queued',?,'t')", (self.dead_pid(),))
        self.assertEqual(self.gate("run", "T-0001").returncode, 0)
        for i in (1, 2):  # the dead running row and the dead queued row
            old = self.run_row(i)
            self.assertEqual((old["status"], old["note"]), ("error", "runner died (pid gone)"))

    def test_a_pass_for_the_same_head_and_main_is_reused_without_running(self):
        self.set_gate('echo x >> "$GATE_OUT"', count="echo 9")
        self.assertEqual(self.gate("run", "T-0001").returncode, 0)
        self.assertEqual(self.gate("run", "T-0001").returncode, 0)
        self.assertEqual(self.out.read_text().split(), ["x"])  # the command ran once
        r = self.run_row(2)
        self.assertEqual((r["status"], r["test_count"], r["main_count"], r["head_sha"]), ("pass", 9, 9, self.head))
        self.assertIn("cached: pass of run #1", r["note"])
        self.assertEqual(self.gate("run", "T-0001", "--no-cache").returncode, 0)  # --no-cache runs it again
        self.assertEqual(self.out.read_text().split(), ["x", "x"])

    def test_fail_and_error_are_never_cached(self):
        self.set_gate('echo x >> "$GATE_OUT"; exit 1')
        self.assertEqual(self.gate("run", "T-0001").returncode, 1)
        self.assertEqual(self.gate("run", "T-0001").returncode, 1)
        self.assertEqual(self.out.read_text().split(), ["x", "x"])

    def test_a_new_main_sha_misses_the_cache(self):
        self.set_gate('echo x >> "$GATE_OUT"')
        self.assertEqual(self.gate("run", "T-0001").returncode, 0)
        self.env.sql("UPDATE gate_runs SET main_sha=? WHERE id=1", ("c" * 40,))
        self.assertEqual(self.gate("run", "T-0001").returncode, 0)
        self.assertEqual(self.out.read_text().split(), ["x", "x"])

    def test_shard_summary_is_recorded_and_counts_when_no_count_cmd(self):
        self.set_gate('mkdir -p "$GATE_REPORT_DIR"; echo \'{"shards":4,"tests":123,"wall_s":7,"cpu_s":21}\' > "$GATE_REPORT_DIR/summary.json"')
        self.assertEqual(self.gate("run", "T-0001").returncode, 0)
        r = self.run_row()
        self.assertEqual(r["test_count"], 123)
        self.assertIn("shards=4 tests=123 wall=7s cpu=21s", r["note"])

    def test_shards_env_is_capped_by_config_and_free_cpu(self):
        self.set_gate('echo "$GATE_SHARDS" > "$GATE_OUT"')
        self.assertEqual(self.gate("run", "T-0001").returncode, 0)
        self.assertEqual(self.out.read_text().strip(), "4")  # default gate_shards
        self.ok("config", "gate_shards", "2", agent=DIRECTOR)
        self.assertEqual(self.gate("run", "T-0001", "--no-cache").returncode, 0)
        self.assertEqual(self.out.read_text().strip(), "2")
        self.ok("config", "gate_shards", "8", agent=DIRECTOR)
        self.assertEqual(self.gate("run", "T-0001", "--no-cache", extra={"FACTORY_LOADAVG": "100"}).returncode, 0)
        self.assertEqual(self.out.read_text().strip(), "1")  # load gate: no free CPU, still one shard

    def test_status_mirror_is_written_only_when_enabled(self):
        self.set_gate("true")
        self.env.set_gh(api=[{"match": ["statuses"], "out": ""}])
        self.assertEqual(self.gate("run", "T-0001").returncode, 0)
        posts = [c for c in self.env.calls("gh") if c["argv"][:1] == ["api"] and "statuses" in " ".join(c["argv"])]
        self.assertEqual(posts, [])
        self.ok("config", "gate_post_status", "1", agent=DIRECTOR)
        self.assertEqual(self.gate("run", "T-0001", "--no-cache").returncode, 0)
        posts = [c["argv"] for c in self.env.calls("gh") if c["argv"][:1] == ["api"] and "statuses" in " ".join(c["argv"])]
        self.assertEqual(len(posts), 1)
        self.assertIn(f"repos/zz-test/repo/statuses/{self.head}", posts[0])
        self.assertIn("state=success", posts[0])
        self.assertIn("context=factory/local-gate", posts[0])


class GateShowListRes(GateBase):
    def test_show_by_task_and_id_and_list_by_project(self):
        self.set_gate("true")
        self.assertEqual(self.gate("run", "T-0001").returncode, 0)
        self.assertEqual(self.gate("run", "T-0001", "--no-cache").returncode, 0)
        by_task = self.gate("show", "T-0001").stdout
        self.assertIn("gate run #2", by_task)
        self.assertIn("status=pass", by_task)
        self.assertIn(self.head, by_task)
        self.assertIn("gate run #1", self.gate("show", "1").stdout)
        self.assertEqual(self.gate("show", "99").returncode, 1)
        lst = self.gate("list", "--project", "zz-test-proj").stdout.splitlines()
        self.assertEqual(len(lst), 3)  # header + 2 runs
        self.assertEqual(self.gate("list", "--project", "nope").stdout.strip(), "(none)")

    def test_show_and_list_work_for_any_known_role_but_run_does_not(self):
        self.env.seed_agent("zz-coder", "coder")
        self.assertEqual(self.gate("list", agent="zz-coder").returncode, 0)

    def test_res_lists_running_gate_runs(self):
        self.assertIn("gate runs: 0 running (max 1)", self.env.run("res", agent=DIRECTOR).stdout)
        self.env.sql("INSERT INTO gate_runs(project_id,task_id,kind,status,pid,created) VALUES('zz-test-proj','T-0001','pr','running',?,'t')",
                     (os.getpid(),))
        out = self.env.run("res", agent=DIRECTOR).stdout
        self.assertIn("gate runs: 1 running (max 1) · #1 T-0001", out)


class ProjectGateColumns(GateBase):
    def test_defaults_and_director_update(self):
        row = self.env.sql("SELECT gate_cmd,gate_count_cmd,gate_timeout_s FROM projects WHERE id='zz-test-proj'")[0]
        self.assertEqual(tuple(row), ("", "", 3600))
        self.set_gate("make test", count="echo 3", timeout=120)
        row = self.env.sql("SELECT gate_cmd,gate_count_cmd,gate_timeout_s FROM projects WHERE id='zz-test-proj'")[0]
        self.assertEqual(tuple(row), ("make test", "echo 3", 120))
        listed = self.ok("project", "list").stdout
        self.assertIn("make test", listed)

    def test_gate_columns_are_director_only_but_required_checks_keeps_boss(self):
        self.env.seed_agent("zz-boss", "boss")
        for flag, val in (("--gate-cmd", "x"), ("--gate-count-cmd", "x"), ("--gate-timeout-s", "5")):
            p = self.env.run("project", "update", "zz-test-proj", flag, val, agent="zz-boss")
            self.assertEqual(p.returncode, 3, f"{flag}: {p.stderr}")
        self.assertEqual(self.env.sql("SELECT gate_cmd FROM projects")[0]["gate_cmd"], "")
        self.ok("project", "update", "zz-test-proj", "--required-checks", "a", agent="zz-boss")

    def test_bad_timeout_and_empty_update_are_refused(self):
        self.assertEqual(self.env.run("project", "update", "zz-test-proj", "--gate-timeout-s", "0", agent=DIRECTOR).returncode, 2)
        self.assertEqual(self.env.run("project", "update", "zz-test-proj", agent=DIRECTOR).returncode, 1)

    def test_gate_config_defaults(self):
        out = self.ok("config").stdout
        for k, v in (("gate_max_parallel", "1"), ("gate_post_status", "0"), ("gate_retry_error", "1")):
            self.assertIn(f"{k} = {v}\n", out)

    def test_old_db_without_the_columns_is_migrated(self):
        self.env.sql("DROP TABLE gate_runs")
        self.env.sql("ALTER TABLE projects DROP COLUMN gate_cmd")
        self.env.sql("ALTER TABLE projects DROP COLUMN gate_count_cmd")
        self.env.sql("ALTER TABLE projects DROP COLUMN gate_timeout_s")
        self.ok("project", "list")  # any command opens the DB through migrate()
        self.assertEqual(self.env.sql("SELECT count(*) c FROM gate_runs")[0]["c"], 0)
        self.assertEqual(tuple(self.env.sql("SELECT gate_cmd,gate_timeout_s FROM projects")[0]), ("", 3600))


if __name__ == "__main__":
    unittest.main()
