"""T-0116: collecting a skill trial (scout/skilleval.py) and `factory skilleval collect|list|results`.

Unit tests call collect_trial with a fake api (force_stop, now) and nsbox.run_contained mocked, so they need no
namespaces and run everywhere. The integration class runs the real contained checker and skips with the reason where
user namespaces are restricted. No real claude: transcripts are fixtures, the evaluator "finished" in a seeded temp DB.
"""
import json
import os
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import time
import types
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
from harness import FactoryTestCase, FIXTURES, REPO  # noqa: E402

sys.path.insert(0, str(REPO))
from scout import nsbox, sandbox  # noqa: E402
from scout import skilleval as se  # noqa: E402

FX = FIXTURES / "skilleval"
SHA = "c" * 40
UNSHARE = os.environ.get("NSBOX_TEST_UNSHARE", "unshare")
USERNS_OK, USERNS_WHY = nsbox.userns_available(UNSHARE)
SKIP_MSG = f"user namespaces unavailable: {USERNS_WHY}"
NOW = "2026-10-25T10:10:00+00:00"
START = "2026-10-25T10:00:00+00:00"
DONE = "2026-10-25T10:05:00+00:00"
PASS_OUT = "PASS: all tests pass\n"


class FakeApi:
    def __init__(self, result="stopped"):
        self.stopped = []
        self.result = result

    def now(self):
        return NOW

    def force_stop(self, con, name, how, attempt_seen=None):
        flag = con.execute("SELECT stop_requested_at FROM agents WHERE name=?", (name,)).fetchone()[0]
        self.stopped.append((name, how, flag))
        if self.result == "stopped":
            con.execute("UPDATE agents SET status='stopped' WHERE name=?", (name,))
        return self.result, None if self.result == "stopped" else "pane still open"


class ClaimingApi(FakeApi):
    """Like the real force_stop: the claim is a compare-and-set on stop_attempt_at, a stale attempt_seen is a skip."""
    def __init__(self, fail_first=1):
        super().__init__()
        self.fail_first = fail_first
        self.calls = 0

    def force_stop(self, con, name, how, attempt_seen=None):
        if con.execute("UPDATE agents SET stop_attempt_at=? WHERE name=? AND status='live' AND stop_requested_at IS NOT NULL "
                       "AND stop_attempt_at IS ?", (NOW, name, attempt_seen)).rowcount != 1:
            return "skip", None
        self.calls += 1
        if self.calls <= self.fail_first:
            return "failed:pane-open", "pane still open"
        con.execute("UPDATE agents SET status='stopped' WHERE name=?", (name,))
        return "stopped", None


class CollectBase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.root = self.env.home / "evalroot"
        self.con = self.env.db()
        self.addCleanup(self.con.close)
        self.con.execute("INSERT INTO scout_candidates(id,key,repo,head_sha,install_risk,status,sources,first_seen,last_seen) "
                         "VALUES('C-0001','gh:o/r','o/r',?,'prompt-only','new','[]','t','t')", (SHA,))
        self.con.commit()
        plugin = self.root / "vendor" / f"C-0001@{SHA}"
        (plugin / "plugin" / "skills" / "demo-skill").mkdir(parents=True)
        (plugin / "plugin" / "skills" / "demo-skill" / "SKILL.md").write_text("---\nname: demo-skill\n---\nProse only.\n")
        (plugin / "VENDOR.json").write_text(json.dumps(
            {"candidate": "C-0001", "sha": SHA, "plugin": "plugin", "copied": ["skills/demo-skill/SKILL.md"],
             "skipped": [], "prescreen_hits": []}))
        self.api = FakeApi()

    def make_trial(self, *, cond="with", task="fix-bug", solved=True, transcript="transcript-skill.jsonl", done=True,
                   live="live", cid="C-0001"):
        """A running trial: sandbox dir with work/ (solution applied or not), temp DB, transcript; a live agent row."""
        self.con.execute("DELETE FROM skill_trials WHERE candidate_id=? AND vendor_sha=? AND task=? AND condition=?",
                         (cid, SHA, task, cond))  # subTests reuse one key; each gets a fresh row id
        tid = self.con.execute(
            "INSERT INTO skill_trials(candidate_id,vendor_sha,task,condition,status,queued,started) "
            "VALUES(?,?,?,?,'running','t',?)", (cid, SHA, task, cond, START)).lastrowid
        d = self.root / "trials" / sandbox.trial_name(tid)
        agent = f"eval-{sandbox.trial_name(tid)}"
        for sub in ("fh", "home", "cfg/projects/p", "bin"):
            (d / sub).mkdir(parents=True)
        shutil.copytree(sandbox.TASKS_DIR / task / "repo", d / "work")
        if solved:
            for f in (sandbox.TASKS_DIR / task / "solution").iterdir():
                shutil.copy(f, d / "work" / f.name)
        if transcript:
            shutil.copy(FX / transcript, d / "cfg" / "projects" / "p" / "s.jsonl")
        se.fh_db(d / "fh").parent.mkdir(parents=True, exist_ok=True)
        t = sqlite3.connect(se.fh_db(d / "fh"))
        t.execute("CREATE TABLE agents(name TEXT PRIMARY KEY, status TEXT, ended TEXT)")
        t.execute("INSERT INTO agents VALUES(?,?,?)", (agent, "done" if done else "live", DONE if done else None))
        t.commit()
        t.close()
        if live:
            self.con.execute("INSERT INTO agents(name,role,status,started) VALUES(?,?,?,?)", (agent, "evaluator", live, START))
        self.con.execute("UPDATE skill_trials SET agent=?, sandbox=? WHERE id=?", (agent, str(d), tid))
        self.con.commit()
        return tid, d, agent

    def trial(self, tid):
        return self.con.execute("SELECT * FROM skill_trials WHERE id=?", (tid,)).fetchone()

    def agent(self, name):
        return self.con.execute("SELECT * FROM agents WHERE name=?", (name,)).fetchone()

    def collect(self, tid, reason=None, contained=None, userns=(True, ""), **kw):
        """collect_trial with the checker mocked: returns (row, run_contained mock)."""
        rc = mock.Mock(return_value=contained or nsbox.Contained(0, PASS_OUT, False))
        with mock.patch.object(nsbox, "run_contained", rc), mock.patch.object(nsbox, "userns_available", return_value=userns):
            row = se.collect_trial(self.con, tid, self.root, self.api, reason, **kw)
        self.con.commit()
        return row, rc


class CollectTests(CollectBase):
    def test_passed_records_metrics_and_cleans_up(self):
        tid, d, agent = self.make_trial()
        row, rc = self.collect(tid)
        self.assertEqual((row["status"], row["passed"]), ("passed", 1))
        self.assertEqual((row["tokens_in"], row["tokens_out"], row["cache_read"], row["cache_write"]), (110, 55, 1000, 250))
        self.assertEqual(row["wall_s"], 300)
        self.assertEqual(row["skill_invoked"], 1)
        self.assertEqual(row["check_output"], PASS_OUT)
        self.assertLessEqual(len(row["check_output"].encode()), 2048)
        self.assertEqual(row["ended"], NOW)
        self.assertEqual(self.agent(agent)["status"], "stopped", "stopped first (the tick reaps it), not left live")
        self.assertFalse(d.exists(), "sandbox must be deleted on passed")
        self.assertEqual(len(self.api.stopped), 1)

    def test_check_output_keeps_last_2kb(self):
        tid, d, _ = self.make_trial()
        row, _ = self.collect(tid, contained=nsbox.Contained(0, "A" * 5000 + "TAIL", False))
        self.assertEqual(len(row["check_output"].encode()), 2048)
        self.assertTrue(row["check_output"].endswith("TAIL"))

    def test_skill_invoked_variants(self):
        for transcript, want in (("transcript-skill.jsonl", 1), ("transcript-read.jsonl", 1), ("transcript-none.jsonl", 0)):
            with self.subTest(transcript):
                tid, _, _ = self.make_trial(transcript=transcript)
                row, _ = self.collect(tid)
                self.assertEqual(row["skill_invoked"], want)
                self.assertEqual((row["tokens_in"], row["tokens_out"]), (110, 55))

    def test_without_condition_has_null_skill_invoked(self):
        tid, _, _ = self.make_trial(cond="without", cid="C-0001")
        row, _ = self.collect(tid)
        self.assertIsNone(row["skill_invoked"])
        self.assertEqual(row["status"], "passed")

    def test_failed_solution_is_failed_and_sandbox_deleted(self):
        tid, d, agent = self.make_trial(solved=False)
        row, _ = self.collect(tid, contained=nsbox.Contained(1, "FAIL: tests failed\n", False))
        self.assertEqual((row["status"], row["passed"]), ("failed", 0))
        self.assertIn("FAIL", row["check_output"])
        self.assertFalse(d.exists())

    def test_timeout_and_over_budget_stop_first_and_keep_sandbox(self):
        for reason in ("timeout", "over-budget"):
            with self.subTest(reason):
                tid, d, agent = self.make_trial(done=False)
                row, rc = self.collect(tid, reason=reason)
                self.assertEqual(self.api.stopped[-1], (agent, "now", NOW), "force_stop after the stop flag was set")
                self.assertEqual((row["status"], row["passed"]), (reason, 0))
                self.assertTrue(d.is_dir(), "sandbox kept for T9")
                self.assertEqual(rc.call_count, 1, "force_stop comes before the checker, which still runs once")
                self.assertEqual(self.agent(agent)["status"], "stopped")

    def test_cancelled_stops_skips_checker_and_deletes(self):
        tid, d, agent = self.make_trial(done=False)
        row, rc = self.collect(tid, reason="cancelled")
        self.assertEqual(row["status"], "cancelled")
        self.assertEqual(self.api.stopped[0][0], agent)
        rc.assert_not_called()
        self.assertFalse(d.exists())

    def test_done_path_stops_the_evaluator_before_staging_and_checking(self):
        tid, d, agent = self.make_trial(done=True)
        seen = []

        def fake(trial_dir, argv, **kw):  # runs when the checker would run: the evaluator must already be stopped
            seen.append((len(self.api.stopped), self.agent(agent)["status"], (d / "work" / "check.py").exists()))
            return nsbox.Contained(0, PASS_OUT, False)
        rc = mock.Mock(side_effect=fake)
        with mock.patch.object(nsbox, "run_contained", rc), mock.patch.object(nsbox, "userns_available", return_value=(True, "")):
            row = se.collect_trial(self.con, tid, self.root, self.api)
        self.assertEqual(seen, [(1, "stopped", True)])
        self.assertEqual(self.api.stopped, [(agent, "now", NOW)], "force_stop on the done path too")
        self.assertEqual(row["status"], "passed")
        self.assertEqual(self.agent(agent)["status"], "stopped")

    def test_failed_force_stop_runs_no_checker_and_leaves_trial_and_agent_live(self):
        for reason in (None, "timeout", "over-budget", "cancelled"):
            with self.subTest(reason=reason):
                self.api = FakeApi("failed:pane-open")
                tid, d, agent = self.make_trial(done=reason is None)
                with self.assertRaises(se.CollectError) as cm:
                    self.collect(tid, reason=reason)
                self.assertIn("failed:pane-open", str(cm.exception))
                self.assertEqual(self.trial(tid)["status"], "running", "no terminal row, a retry is possible")
                self.assertIn("checker not run", self.trial(tid)["check_output"])
                self.assertEqual(self.agent(agent)["status"], "live", "row not forced to done")
                self.assertTrue(d.is_dir())
                self.assertFalse((d / "work" / "check.py").exists(), "nothing staged")
                self.api = FakeApi()  # the retry succeeds once the stop works
                row, rc = self.collect(tid, reason=reason)
                self.assertIn(row["status"], ("passed", "cancelled", "timeout", "over-budget"))

    def test_collect_retry_reattempts_a_stop_that_failed(self):
        self.api = ClaimingApi(fail_first=1)
        tid, d, agent = self.make_trial()
        with self.assertRaises(se.CollectError):
            self.collect(tid)
        self.assertIsNotNone(self.agent(agent)["stop_attempt_at"], "the failed stop left its attempt mark")
        row, _ = self.collect(tid)
        self.assertEqual(self.api.calls, 2, "the retry re-attempted the stop instead of skipping")
        self.assertEqual(row["status"], "passed")
        self.assertEqual(self.agent(agent)["status"], "stopped")

    def test_background_evaluator_cannot_forge_a_pass(self):
        """The evaluator keeps running and would make check.py exit 0; the unconfirmed stop means it is never run."""
        self.api = FakeApi("failed:process-alive")
        tid, d, agent = self.make_trial(solved=False, done=True)
        (d / "work" / "check.py").write_text("raise SystemExit(0)\n")  # what the live evaluator wrote
        with self.assertRaises(se.CollectError):
            self.collect(tid, contained=nsbox.Contained(0, PASS_OUT, False))
        row = self.trial(tid)
        self.assertEqual((row["status"], row["passed"]), ("running", None))

    def test_concurrent_collect_of_one_trial_is_refused(self):
        import fcntl
        tid, d, _ = self.make_trial()
        lockdir = self.root / "locks"
        lockdir.mkdir(parents=True, exist_ok=True)
        with open(lockdir / f"{sandbox.trial_name(tid)}.lock", "w") as lk:
            fcntl.flock(lk, fcntl.LOCK_EX)
            with self.assertRaisesRegex(se.CollectError, "another process"):
                self.collect(tid)
        self.assertEqual(self.trial(tid)["status"], "running")
        self.assertTrue(d.is_dir())
        self.assertEqual(self.collect(tid)[0]["status"], "passed")

    def test_lost_row_without_temp_db_done_is_error(self):
        for live in (None, "stopped"):
            with self.subTest(live=live):
                tid, d, _ = self.make_trial(done=False, live=live)
                row, rc = self.collect(tid)
                self.assertEqual(row["status"], "error")
                self.assertIn("exited without finishing", row["check_output"])
                rc.assert_not_called()
                self.assertTrue(d.is_dir())

    def test_still_running_without_reason_is_refused_and_changes_nothing(self):
        tid, d, agent = self.make_trial(done=False)
        with self.assertRaises(se.CollectError):
            self.collect(tid)
        self.assertEqual(self.trial(tid)["status"], "running")
        self.assertEqual(self.agent(agent)["status"], "live")

    def test_second_collect_is_a_no_op(self):
        tid, d, agent = self.make_trial()
        first, rc = self.collect(tid)
        before = dict(self.trial(tid))
        agent_before = dict(self.agent(agent))
        second, rc2 = self.collect(tid)
        self.assertEqual(dict(second), before)
        self.assertEqual(dict(self.agent(agent)), agent_before)
        rc2.assert_not_called()

    def test_second_collect_of_a_timeout_does_not_stop_again(self):
        tid, d, _ = self.make_trial(done=False)
        self.collect(tid, reason="timeout")
        n = len(self.api.stopped)
        self.collect(tid, reason="timeout")
        self.assertEqual(len(self.api.stopped), n)

    def test_hanging_checker_is_failed_with_timeout_note(self):
        tid, _, _ = self.make_trial()
        row, rc = self.collect(tid, contained=nsbox.Contained(-9, "partial\n", True))
        self.assertEqual((row["status"], row["passed"]), ("failed", 0))
        self.assertIn("exceeded 60 s", row["check_output"])
        self.assertEqual(rc.call_args.kwargs["timeout"], 60)

    def test_unknown_trial_and_pending_trial(self):
        with self.assertRaises(se.CollectError):
            self.collect(999)
        tid = self.con.execute("INSERT INTO skill_trials(candidate_id,vendor_sha,task,condition,status,queued) "
                               "VALUES('C-0001',?,'fix-bug','without','pending','t')", (SHA,)).lastrowid
        with self.assertRaises(se.CollectError):
            self.collect(tid)

    def test_hostile_transcript_entries_are_dropped_not_followed(self):
        tid, d, _ = self.make_trial()
        proj = d / "cfg" / "projects"
        os.symlink("/etc/passwd", proj / "link.jsonl")
        os.mkfifo(proj / "p" / "fifo.jsonl")  # a parser opening this would block forever
        row, _ = self.collect(tid, contained=nsbox.Contained(0, "ok", False))
        self.assertEqual((row["status"], row["tokens_in"]), ("passed", 110))


class ContainmentTests(CollectBase):
    def test_checker_goes_only_through_run_contained(self):
        tid, d, _ = self.make_trial()
        work = str(d / "work")
        home = str(d / "home")
        trials = str(d.parent)
        boom = mock.Mock(side_effect=AssertionError("check.py must not run outside run_contained"))
        with mock.patch.object(se, "subprocess", create=True, new=boom), \
                mock.patch.object(subprocess, "run", boom), mock.patch.object(subprocess, "Popen", boom), \
                mock.patch.object(os, "system", boom):
            row, rc = self.collect(tid)
        self.assertEqual(row["status"], "passed")
        rc.assert_called_once()
        args, kw = rc.call_args
        self.assertEqual(args[0], str(d))
        self.assertEqual(args[1][-1], "check.py")
        self.assertEqual(kw["cwd"], work)
        self.assertEqual(kw["timeout"], 60)
        self.assertEqual(kw["env"], {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "HOME": home})
        self.assertEqual(kw["trials_root"], trials)
        self.assertFalse(kw.get("net"), "no network for the checker")
        self.assertNotIn("token", json.dumps(kw["env"]).lower())

    def test_hidden_tests_and_check_are_staged_before_the_run(self):
        tid, d, _ = self.make_trial()
        seen = {}

        def fake(trial_dir, argv, **kw):
            seen["files"] = sorted(p.name for p in Path(kw["cwd"]).iterdir())
            return nsbox.Contained(0, "ok", False)
        with mock.patch.object(nsbox, "run_contained", fake), mock.patch.object(nsbox, "userns_available", return_value=(True, "")):
            se.collect_trial(self.con, tid, self.root, self.api)
        self.assertIn("check.py", seen["files"])
        self.assertIn("test_taskdeps_hidden.py", seen["files"])

    def test_planted_symlinks_in_work_do_not_redirect_the_staging(self):
        tid, d, _ = self.make_trial()
        outside = self.env.home / "outside"
        outside.mkdir()
        os.symlink(outside / "victim.py", d / "work" / "check.py")
        os.symlink(outside / "victim.py", d / "work" / "test_taskdeps_hidden.py")
        row, _ = self.collect(tid)
        self.assertEqual(row["status"], "passed")
        self.assertEqual(list(outside.iterdir()), [], "nothing was written through a planted symlink")

    def test_no_userns_means_error_and_check_py_never_ran(self):
        tid, d, _ = self.make_trial()
        row, rc = self.collect(tid, userns=(False, "restricted by test"))
        self.assertEqual(row["status"], "error")
        self.assertIn("user namespaces unavailable", row["check_output"])
        self.assertIn("restricted by test", row["check_output"])
        rc.assert_not_called()
        self.assertFalse((d / "work" / "check.py").exists(), "check.py was not even staged")
        self.assertTrue(d.is_dir())

    def test_skilleval_has_no_direct_exec_of_check_py(self):
        src = (REPO / "scout" / "skilleval.py").read_text()
        for bad in ("subprocess", "os.system", "os.exec", "os.popen", "runpy", "exec(", "eval("):
            self.assertNotIn(bad, src, f"scout/skilleval.py must not use {bad}")


class ProxyTests(CollectBase):
    def test_collect_stops_the_trial_proxy(self):
        tid, d, _ = self.make_trial()
        (d / "egress.py").write_text("import time\ntime.sleep(60)\n")
        p = subprocess.Popen([sys.executable, str(d / "egress.py")], start_new_session=True)  # looks like our proxy
        self.addCleanup(lambda: (p.poll() is None) and p.kill())
        (d / "proxy.pid").write_text(f"{p.pid}\n")
        for _ in range(100):  # wait for the exec: before it the cmdline is still ours and the script is not yet open
            if b"egress.py" in Path(f"/proc/{p.pid}/cmdline").read_bytes():
                break
            time.sleep(0.05)
        time.sleep(0.3)
        row, _ = self.collect(tid)
        self.assertEqual(row["status"], "passed")
        for _ in range(100):
            if p.poll() is not None:
                break
            time.sleep(0.05)
        self.assertIsNotNone(p.poll(), "the trial proxy process must be gone after collect")

    def test_stop_proxy_is_called_before_the_checker(self):
        tid, d, _ = self.make_trial(done=False)
        order = []
        with mock.patch.object(nsbox, "stop_proxy", lambda t: order.append("stop_proxy")), \
                mock.patch.object(nsbox, "userns_available", return_value=(True, "")), \
                mock.patch.object(nsbox, "run_contained", lambda *a, **k: order.append("check") or nsbox.Contained(0, "", False)):
            se.collect_trial(self.con, tid, self.root, self.api, "timeout")
        self.assertEqual(order, ["stop_proxy", "check"])


class CliTests(CollectBase):
    def setUp(self):
        super().setUp()
        self.ok("config", "skilleval_root", str(self.root), agent="owner")
        self.env.seed_agent("zz-lead", "lead")

    def test_workers_are_refused_allowed_roles_get_past_the_guard(self):
        for who, role in (("zz-coder", "coder"), ("zz-scout", "scout"), ("zz-eval", "evaluator"), ("zz-qa", "qa")):
            self.env.seed_agent(who, role)
            with self.subTest(role):
                p = self.refused(3, "skilleval", "collect", "1", agent=who)
                self.assertIn("may not", p.stderr + p.stdout)
        for who in ("owner", "zz-lead"):
            with self.subTest(who):
                p = self.refused(2, "skilleval", "collect", "999", agent=who)  # past the role guard: unknown trial
                self.assertIn("no trial 999", p.stderr + p.stdout)
        self.env.seed_agent("zz-director", "director")
        self.refused(2, "skilleval", "collect", "999", agent="zz-director")

    def test_collect_usage_errors(self):
        self.refused(2, "skilleval", "collect", agent="owner")
        self.refused(2, "skilleval", "collect", "x/../y", agent="owner")

    def test_results_lines_and_summary(self):
        rows = [("C-0001", "fix-bug", "with", "passed", 1, 110, 55, 300, 1),
                ("C-0001", "fix-bug", "without", "failed", 0, 200, 100, 500, None),
                ("C-0001", "add-feature", "with", "timeout", 0, 400, 200, 600, 0),
                ("C-0001", "add-feature", "without", "passed", 1, 20, 10, 100, None)]
        for cid, task, cond, st, p, ti, to, w, inv in rows:
            self.con.execute("INSERT INTO skill_trials(candidate_id,vendor_sha,task,condition,status,queued,passed,tokens_in,"
                             "tokens_out,wall_s,skill_invoked) VALUES(?,?,?,?,?,'t',?,?,?,?,?)",
                             (cid, SHA, task, cond, st, p, ti, to, w, inv))
        self.con.execute("INSERT INTO scout_candidates(id,key,kind,status,first_seen,last_seen) VALUES('C-0002','k','unknown','new','t','t')")
        self.con.execute("INSERT INTO skill_trials(candidate_id,vendor_sha,task,condition,status,queued) "
                         "VALUES('C-0002','d','fix-bug','with','running','t')")
        self.con.commit()
        out = self.ok("skilleval", "results", "--candidate", "C-0001", agent="owner").stdout.splitlines()
        self.assertEqual(len(out), 5, out)
        self.assertEqual(out[0], "C-0001 add-feature with: trial 3 status=timeout passed=0 tokens=600 wall_s=600 skill_invoked=0")
        self.assertEqual(out[1], "C-0001 add-feature without: trial 4 status=passed passed=1 tokens=30 wall_s=100 skill_invoked=-")
        self.assertEqual(out[2], "C-0001 fix-bug with: trial 1 status=passed passed=1 tokens=165 wall_s=300 skill_invoked=1")
        self.assertEqual(out[3], "C-0001 fix-bug without: trial 2 status=failed passed=0 tokens=300 wall_s=500 skill_invoked=-")
        self.assertEqual(out[4], "C-0001 summary: with: passed 1/2 tasks, median tokens 382, median wall_s 450; "
                                 "without: passed 1/2 tasks, median tokens 165, median wall_s 300")
        every = self.ok("skilleval", "results", agent="zz-lead").stdout
        self.assertIn("C-0002 summary", every)
        self.assertIn("C-0002 fix-bug with: trial 5 status=running passed=- tokens=- wall_s=- skill_invoked=-", every)
        listed = self.ok("skilleval", "list", "--candidate", "C-0002", agent="owner").stdout.splitlines()
        self.assertEqual(len(listed), 2)  # T-0112: list shows the candidate summary too
        self.assertIn("status=running", listed[0])
        self.assertTrue(listed[1].startswith("C-0002 summary:") and listed[1].endswith("; running 1"), listed[1])
        self.assertEqual(self.ok("skilleval", "results", "--candidate", "C-9999", agent="owner").stdout.strip(), "no trials")

    def test_collect_through_the_cli_with_a_live_unstoppable_agent_runs_no_checker(self):
        """Real bin/factory force_stop (no session id for the row, so the stop cannot be confirmed): exit 2."""
        tid, d, agent = self.make_trial()
        p = self.env.run("skilleval", "collect", str(tid), agent="owner")
        self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
        self.assertIn("checker not run", p.stderr)
        self.assertFalse((d / "work" / "check.py").exists())
        self.assertEqual(self.trial(tid)["status"], "running")
        self.assertEqual(self.agent(agent)["status"], "live")

    def test_collect_through_the_cli_with_userns_unavailable_is_error(self):
        tid, d, agent = self.make_trial(live=None)
        p = self.env.run("skilleval", "collect", str(tid), agent="owner", extra_env={"NSBOX_TEST_UNSHARE": "/bin/false"})
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("status=error", p.stdout)
        self.assertFalse((d / "work" / "check.py").exists())


CHECK_PY = '''import os, socket, sys
real = {home!r}
def listing(p):
    try:
        return sorted(os.listdir(p))
    except OSError:
        return []
try:
    s = socket.create_connection(("1.1.1.1", 443), timeout=3)
    s.close()
    fetch = "ok"
except OSError:
    fetch = "failed"
claude, factory = listing(real + "/.claude"), listing(real + "/factory")
env = sorted(k for k in os.environ if "TOKEN" in k or "API_KEY" in k or "PROXY" in k.upper())
print(f"fetch={{fetch}} claude={{claude}} factory={{factory}} env={{env}}")
sys.exit(0 if fetch == "failed" and not claude and not factory and not env else 1)
'''


@unittest.skipUnless(USERNS_OK, SKIP_MSG)
class ContainedCheckerIntegration(CollectBase):
    """The real run_contained: the checker has no network and sees ~/.claude and ~/factory empty."""

    def setUp(self):
        super().setUp()
        self.tasks = self.env.home / "tasks" / "probe"
        (self.tasks / "repo").mkdir(parents=True)
        (self.tasks / "check.py").write_text(CHECK_PY.format(home=nsbox.real_home()))
        patch = mock.patch.object(sandbox, "TASKS_DIR", self.tasks.parent)
        patch.start()
        self.addCleanup(patch.stop)

    def run_real(self, **kw):
        tid, d, _ = self.make_trial()
        return tid, d, se.collect_trial(self.con, tid, self.root, self.api, unshare=UNSHARE, **kw)

    def make_trial(self, **kw):  # the probe task has no repo/solution to copy
        tid = self.con.execute("INSERT INTO skill_trials(candidate_id,vendor_sha,task,condition,status,queued,started) "
                               "VALUES('C-0001',?,'probe','without','running','t',?)", (SHA, START)).lastrowid
        d = self.root / "trials" / sandbox.trial_name(tid)
        agent = f"eval-{sandbox.trial_name(tid)}"
        for sub in ("fh", "home", "cfg/projects/p", "work"):
            (d / sub).mkdir(parents=True)
        se.fh_db(d / "fh").parent.mkdir(parents=True, exist_ok=True)
        t = sqlite3.connect(se.fh_db(d / "fh"))
        t.execute("CREATE TABLE agents(name TEXT PRIMARY KEY, status TEXT, ended TEXT)")
        t.execute("INSERT INTO agents VALUES(?,?,?)", (agent, "done", DONE))
        t.commit()
        t.close()
        self.con.execute("INSERT INTO agents(name,role,status,started) VALUES(?,?,?,?)", (agent, "evaluator", "live", START))
        self.con.execute("UPDATE skill_trials SET agent=?, sandbox=? WHERE id=?", (agent, str(d), tid))
        self.con.commit()
        return tid, d, agent

    def test_checker_sees_no_network_and_empty_secret_dirs(self):
        print(f"\ncontained checker integration: running (userns ok, home {nsbox.real_home()})", file=sys.stderr)
        _, _, row = self.run_real()
        self.assertEqual(row["status"], "passed", row["check_output"])
        self.assertIn("fetch=failed claude=[] factory=[] env=[]", row["check_output"])


if __name__ == "__main__":
    unittest.main()
