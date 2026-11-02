"""T-0853: the skilleval positive control (skilleval/controls/marker, `factory skilleval control`).

Proves the with/without comparison can see a loaded skill: a repo-owned, prompt-only plugin that tells the agent to
write SKILL_MARKER.txt, run through the same vendor / queue / sandbox / spawn / collect path as a real candidate.
The CLI tests run bin/factory as a subprocess on a temp FACTORY_HOME; the spawn test uses the herdr stub; nothing
here starts a real claude or needs the network.
"""
import json
import os
import stat
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from harness import FactoryTestCase, REPO  # noqa: E402
from test_skilleval_collect import CollectBase, SHA as OTHER_SHA  # noqa: E402
from test_skilleval_sandbox import SandboxBase  # noqa: E402
from test_skilleval_tick import TickBase, at  # noqa: E402

sys.path.insert(0, str(REPO))
from scout import skilleval as se  # noqa: E402
from scout import vendor as scoutvendor  # noqa: E402

CONTROL_DIR = REPO / "skilleval" / "controls" / "marker"
KEY = "local:skilleval-control"
WORKERS = ("coder", "qa", "reviewer", "judge", "cleaner", "pm", "scout", "evaluator")


def stamp():
    return "2026-10-25T10:00:00+00:00"


class ControlPluginTests(unittest.TestCase):
    def test_control_dir_is_prompt_only(self):
        scoutvendor.assert_prompt_only_tree(CONTROL_DIR)  # raises VendorError otherwise
        files = [p for p in CONTROL_DIR.rglob("*") if p.is_file()]
        self.assertTrue(files)
        self.assertEqual([p for p in files if p.suffix not in (".md", ".json")], [])
        text = (CONTROL_DIR / "skills" / "eval-marker" / "SKILL.md").read_text()
        self.assertEqual(text.count("eval-marker-loaded"), 1)
        self.assertIn("every task", text)

    def test_vendor_local_copies_with_0644_and_is_reusable(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td) / "root"
            sha = scoutvendor.content_sha(CONTROL_DIR)
            self.assertRegex(sha, r"^[0-9a-f]{40}$")
            cand = {"id": "C-0009", "head_sha": sha, "install_risk": "prompt-only", "status": "triaged"}
            res = scoutvendor.vendor_local(cand, root, CONTROL_DIR, se.CONTROL_PLUGIN)
            self.assertFalse(res["reused"])
            self.assertEqual(res["meta"]["prescreen_hits"], [])
            plugin = Path(res["dir"]) / res["plugin"]
            for p in plugin.rglob("*"):
                if p.is_file():
                    self.assertEqual(stat.S_IMODE(p.stat().st_mode), 0o644, p)
            self.assertIsNotNone(scoutvendor.vendored(root, "C-0009", sha))
            self.assertTrue(scoutvendor.is_eval_allowed(cand, root))
            self.assertTrue(scoutvendor.vendor_local(cand, root, CONTROL_DIR, se.CONTROL_PLUGIN)["reused"])

    def test_vendor_local_refuses_a_tainted_source_and_held_candidates(self):
        with tempfile.TemporaryDirectory() as td:
            src = Path(td) / "src"
            (src / "skills" / "x").mkdir(parents=True)
            (src / "skills" / "x" / "SKILL.md").write_text("prose only\n")
            (src / "run.sh").write_text("echo hi\n")
            cand = {"id": "C-0009", "head_sha": "d" * 40, "install_risk": "prompt-only", "status": "triaged"}
            with self.assertRaises(scoutvendor.VendorError):
                scoutvendor.vendor_local(cand, Path(td) / "root", src, "p")
            self.assertFalse((Path(td) / "root").exists())
            with self.assertRaises(scoutvendor.VendorError):
                scoutvendor.vendor_local({**cand, "status": "needs-owner"}, Path(td) / "root", CONTROL_DIR, "p")
            with self.assertRaises(scoutvendor.VendorError):
                scoutvendor.vendor_local({**cand, "install_risk": "scripts"}, Path(td) / "root", CONTROL_DIR, "p")


class ControlCliBase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.root = self.env.home / "evalroot"
        self.ok("config", "skilleval_root", str(self.root), agent="owner")

    def trials(self):
        return [(r["candidate_id"], r["task"], r["condition"]) for r in
                self.env.sql("SELECT * FROM skill_trials ORDER BY id")]


class ControlRoleTests(ControlCliBase):
    def test_workers_are_refused_and_nothing_is_queued(self):
        for role in WORKERS:
            who = f"zz-{role}"
            self.env.seed_agent(who, role)
            with self.subTest(role):
                p = self.env.run("skilleval", "control", agent=who)
                self.assertNotEqual(p.returncode, 0, p.stdout + p.stderr)
                self.assertEqual(self.env.sql("SELECT * FROM scout_candidates"), [])
                self.assertEqual(self.trials(), [])
        self.assertFalse(self.root.exists(), "a refused control must not vendor anything")

    def test_director_lead_owner_queue_with_and_without_trials(self):
        self.env.seed_agent("zz-director", "director")
        self.env.seed_agent("zz-lead", "lead")
        for who, task in (("zz-director", "fix-bug"), ("zz-lead", "add-feature"), ("owner", "review-diff")):
            with self.subTest(who):
                p = self.ok("skilleval", "control", "--tasks", task, agent=who)
                self.assertIn("queued 2 trial(s)", p.stdout)
        rows = self.env.sql("SELECT key, install_risk, head_sha, status FROM scout_candidates")
        self.assertEqual(len(rows), 1)  # one reserved row, reused
        self.assertEqual((rows[0]["key"], rows[0]["install_risk"]), (KEY, "prompt-only"))
        self.assertEqual(rows[0]["head_sha"], scoutvendor.content_sha(CONTROL_DIR))
        cid = self.env.sql("SELECT id FROM scout_candidates")[0]["id"]
        self.assertEqual(sorted(self.trials()), sorted((cid, t, c) for t in ("fix-bug", "add-feature", "review-diff")
                                                       for c in ("with", "without")))

    def test_default_task_is_fix_bug_and_unknown_task_is_refused(self):
        self.ok("skilleval", "control", agent="owner")
        self.assertEqual({t for _c, t, _x in self.trials()}, {"fix-bug"})
        p = self.refused(2, "skilleval", "control", "--tasks", "nope", agent="owner")
        self.assertIn("unknown eval task", p.stderr + p.stdout)


class ControlTrialPathTests(SandboxBase):
    """The control goes through the same sandbox + spawn guards as a real candidate."""

    def setUp(self):
        super().setUp()
        with self.env.load_factory() as f:
            con = f.db()
            self.cand = se.register_control(con, stamp())
            res = scoutvendor.vendor_local(self.cand, self.root, se.CONTROL_DIR, se.CONTROL_PLUGIN)
            con.commit()
        self.cid, self.csha = self.cand["id"], self.cand["head_sha"]
        self.vendored = Path(res["dir"]) / res["plugin"]

    def spawn(self, mod, info, with_plugin=True, **over):
        return mod.spawn(mod.db(), "evaluator", **{**self.spawn_kwargs(info, with_plugin), **over})

    def control_info(self, con):
        row = self.trial_row(con, "with", cid=self.cid, sha=self.csha)
        from scout import sandbox
        return sandbox.build_sandbox(con, row, self.root, home=str(self.home), real_claude=str(self.claude),
                                     token_file=str(self.token))

    def test_spawn_args_carry_plugin_dir_under_the_vendor_root(self):
        with self.env.load_factory() as f:
            info = self.control_info(f.db())
            self.assertEqual(self.spawn(f, info), info["agent"])
        self.check_spawn(info, True)  # exactly one --plugin-dir, under <root>/vendor, no live paths, no token
        self.assertEqual(Path(info["plugin"]).resolve(), self.vendored.resolve())

    def test_planted_script_in_the_vendored_copy_refuses_the_start(self):
        with self.env.load_factory() as f:
            info = self.control_info(f.db())
            (self.vendored / "skills" / "eval-marker" / "run.sh").write_text("echo pwned\n")  # after the build re-check
            with self.assertRaises(SystemExit) as cm:
                self.spawn(f, info)
            self.assertEqual(cm.exception.code, 3)
        self.assertEqual(self.start_calls(), [])

    def test_planted_script_before_the_build_refuses_the_sandbox(self):
        (self.vendored / "skills" / "eval-marker" / "run.sh").write_text("echo pwned\n")
        from scout import sandbox
        with self.env.load_factory() as f:
            con = f.db()
            row = self.trial_row(con, "with", cid=self.cid, sha=self.csha)
            with self.assertRaises(sandbox.SandboxRefused):
                sandbox.build_sandbox(con, row, self.root, home=str(self.home), real_claude=str(self.claude),
                                      token_file=str(self.token))


class ControlCapTests(TickBase):
    def test_control_trials_count_toward_the_daily_cap(self):
        cand = se.register_control(self.con, stamp())
        scoutvendor.vendor_local(cand, self.root, se.CONTROL_DIR, se.CONTROL_PLUGIN)
        new, _ = se.queue_trials(self.con, cand["id"], cand["head_sha"], ["fix-bug", "add-feature"], "zz", "t")
        self.con.execute("UPDATE config SET value='1' WHERE key='skilleval_max_trials_per_day'")
        self.con.commit()
        self.assertEqual(len(new), 4)
        started = []
        for minute in range(10):
            api = self.tick(at(f"10:{minute:02d}"))
            started += api.launched
            self.con.execute("UPDATE skill_trials SET status='passed', ended=? WHERE status='running'", (api.now(),))
        self.assertEqual(len(started), 1, started)
        self.assertEqual(self.status().count("pending"), 3)


class MarkerTests(CollectBase):
    def setUp(self):
        super().setUp()
        self.con.execute("INSERT INTO scout_candidates(id,key,repo,head_sha,install_risk,status,sources,first_seen,last_seen) "
                         "VALUES('C-0002',?,NULL,?,'prompt-only','triaged','[]','t','t')", (KEY, OTHER_SHA))
        self.con.commit()

    def marker_of(self, content=None, cid="C-0002", link=False, cond="with"):
        tid, d, _agent = self.make_trial(cid=cid, cond=cond)
        work = d / "work"
        if link:
            (work / "elsewhere.txt").write_text("eval-marker-loaded")
            (work / "SKILL_MARKER.txt").symlink_to(work / "elsewhere.txt")
        elif content is not None:
            (work / "SKILL_MARKER.txt").write_text(content)
        row, _rc = self.collect(tid)
        return row["check_output"] or ""

    def test_marker_recorded_in_check_output(self):
        self.assertIn("control_marker=1", self.marker_of("eval-marker-loaded"))
        self.assertIn("control_marker=1", self.marker_of("eval-marker-loaded\n"))
        self.assertIn("control_marker=0", self.marker_of(None))
        self.assertIn("control_marker=0", self.marker_of("something else"))
        self.assertIn("control_marker=0", self.marker_of("eval-marker-loaded and more"))
        self.assertIn("control_marker=0", self.marker_of(link=True))  # untrusted work/: no symlink is followed

    def test_marker_is_recorded_for_the_without_trial_too(self):
        self.assertIn("control_marker=0", self.marker_of(None, cond="without"))
        self.assertIn("control_marker=1", self.marker_of("eval-marker-loaded", cond="without"))

    def test_marker_line_survives_a_long_checker_output(self):
        tid, d, _a = self.make_trial(cid="C-0002")
        (d / "work" / "SKILL_MARKER.txt").write_text("eval-marker-loaded")
        from unittest import mock
        from scout import nsbox
        big = nsbox.Contained(0, "x" * 5000, False)
        with mock.patch.object(nsbox, "run_contained", return_value=big), \
                mock.patch.object(nsbox, "userns_available", return_value=(True, "")):
            row = se.collect_trial(self.con, tid, self.root, self.api)
        self.assertTrue(row["check_output"].endswith("control_marker=1"), row["check_output"][-60:])
        self.assertLessEqual(len(row["check_output"]), se.OUTPUT_MAX + 40)

    def test_real_candidates_get_no_marker_line(self):
        out = self.marker_of("eval-marker-loaded", cid="C-0001")
        self.assertNotIn("control_marker", out)


class LoaderCheckTests(unittest.TestCase):
    """Real-shape transcript lines (Claude Code writes plugin skills as `<plugin>:<skill>`)."""

    def invoked(self, line):
        with tempfile.TemporaryDirectory() as td:
            (Path(td) / "s.jsonl").write_text(json.dumps(line) + "\n")
            return se.skill_was_invoked(td, {"eval-marker", "eval-control-marker"}, "/x/vendor/C-0002@abc/eval-control-marker")

    def test_namespaced_skill_tool_use_counts(self):
        line = {"type": "assistant", "message": {"role": "assistant", "model": "claude-sonnet-5-5", "content": [
            {"type": "tool_use", "id": "toolu_01A", "name": "Skill", "input": {"skill": "eval-control-marker:eval-marker"}}]}}
        self.assertEqual(self.invoked(line), 1)

    def test_other_skill_does_not_count(self):
        line = {"type": "assistant", "message": {"role": "assistant", "content": [
            {"type": "tool_use", "id": "toolu_01B", "name": "Skill", "input": {"skill": "other-plugin:other"}}]}}
        self.assertEqual(self.invoked(line), 0)


class ControlNotAFindTests(ControlCliBase):
    def setUp(self):
        super().setUp()
        self.ok("skilleval", "control", agent="owner")
        self.cid = self.env.sql("SELECT id FROM scout_candidates")[0]["id"]
        self.env.sql("INSERT INTO skill_trials(candidate_id,vendor_sha,task,condition,status,queued,ended,passed,check_output) "
                     "VALUES(?,?,?,?,?,?,?,?,?)", (self.cid, "e" * 40, "fix-bug", "with", "passed", "t",
                                                   "2026-10-25T09:40:00+00:00", 1, "control_marker=1"))

    def test_not_in_pending_or_list_new_even_when_status_is_new(self):
        self.env.sql("UPDATE scout_candidates SET status='new' WHERE id=?", (self.cid,))  # worst case: untriaged and new
        for args in (("scout", "pending"), ("scout", "list", "--new")):
            with self.subTest(args):
                p = self.ok(*args, agent="owner")
                self.assertNotIn(KEY, p.stdout + p.stderr)
                self.assertNotIn(self.cid, p.stdout)

    def test_not_in_the_report_finds_or_eval_results(self):
        self.env.sql("INSERT INTO scout_triage(candidate_id,purpose,target_roles,overlap,recommendation,rationale,created) "
                     "VALUES(?,?,?,?,?,?,?)", (self.cid, "p", "coder", "none", "adopt", "r", "2026-10-25T09:30:00+00:00"))
        p = self.ok("scout", "report", "--since", "2026-10-25T00:00:00+00:00", agent="owner")
        self.assertNotIn(KEY, p.stdout)
        self.assertNotIn(self.cid, p.stdout)

    def test_results_label_it_control(self):
        out = self.ok("skilleval", "results", agent="owner").stdout
        self.assertIn(f"{self.cid} control: positive control ({KEY})", out)
        self.assertIn("skill_invoked=- control_marker=1", out)


if __name__ == "__main__":
    unittest.main()
