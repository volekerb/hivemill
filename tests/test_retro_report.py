"""T-1207: retro packet, file validation, and digest section."""
import json
import sqlite3
import unittest

from harness import FactoryTestCase, PR_URL

D = "2026-11-09"
NOW = f"{D}T10:00:00+01:00"
WIN_START = f"{D}T09:00:00+01:00"


class RetroReportTests(FactoryTestCase):
    INPROC = True

    def setUp(self):
        super().setUp()
        self.env.seed_project()
        self.env.seed_goal()
        self.env.seed_task()
        self.env.seed_pr()
        self.env.seed_agent("zz-test-retro", "retro", goal=None, team=None, task=None)

    def _incident(self, cause, kind, task_id="T-0001", created=NOW, verdict_id=None):
        with self.env.load_factory() as mod:
            con = sqlite3.connect(self.env.db_path)
            con.row_factory = sqlite3.Row
            self.addCleanup(con.close)
            mod.record_rejection_incident(con, cause, kind, task_id, verdict_id=verdict_id, ts=created)
            con.commit()

    def test_file_and_digest(self):
        self._incident("bug", "qa")
        body = {
            "window_end": NOW,
            "causes": [{"cause": "bug", "count": 1}],
            "evidence": [{"task_id": "T-0001", "pr_url": PR_URL, "cause": "bug"}],
            "fixes": [{"type": "check", "cause": "bug", "text": "run prcheck before done"}],
        }
        self.ok("retro", "file", "-", agent="zz-test-retro", stdin=json.dumps(body),
                extra_env={"FACTORY_NOW": NOW})
        dig = self.ok("digest", extra_env={"FACTORY_NOW": NOW}).stdout
        self.assertIn("## Retro", dig)
        self.assertIn("T-0001", dig)
        self.assertIn(PR_URL, dig)
        self.assertIn("check —", dig)
        goal_dig = self.ok("digest", "--goal", "G-001", extra_env={"FACTORY_NOW": NOW}).stdout
        self.assertNotIn("## Retro", goal_dig)

    def test_digest_send_includes_heading(self):
        self._incident("bug", "qa")
        body = {
            "window_end": NOW,
            "causes": [{"cause": "bug", "count": 1}],
            "evidence": [{"task_id": "T-0001", "pr_url": PR_URL, "cause": "bug"}],
            "fixes": [{"type": "template", "cause": "bug", "text": "add acceptance item"}],
        }
        self.ok("retro", "file", "-", agent="zz-test-retro", stdin=json.dumps(body),
                extra_env={"FACTORY_NOW": NOW})
        self.env.seed_agent("zz-test-director", "director", goal=None, team=None)
        self.ok("digest", "--send", agent="zz-test-director", extra_env={"FACTORY_NOW": NOW})
        row = self.env.sql("SELECT body FROM messages ORDER BY id DESC LIMIT 1")[0]
        self.assertIn("## Retro", row["body"])

    def test_wrong_cause_count_refused(self):
        self._incident("bug", "qa")
        body = {
            "window_end": NOW,
            "causes": [{"cause": "bug", "count": 9}],
            "evidence": [],
            "fixes": [{"type": "check", "cause": "bug", "text": "x"}],
        }
        before = self.env.sql("SELECT count(*) c FROM retro_reports")[0]["c"]
        self.refused(2, "retro", "file", "-", agent="zz-test-retro", stdin=json.dumps(body))
        self.assertEqual(self.env.sql("SELECT count(*) c FROM retro_reports")[0]["c"], before)

    def test_third_fix_refused(self):
        self._incident("bug", "qa")
        body = {
            "window_end": NOW,
            "causes": [{"cause": "bug", "count": 1}],
            "evidence": [{"task_id": "T-0001", "pr_url": PR_URL, "cause": "bug"}],
            "fixes": [
                {"type": "check", "cause": "bug", "text": "a"},
                {"type": "template", "cause": "bug", "text": "b"},
                {"type": "prompt_rule", "cause": "bug", "text": "c"},
            ],
        }
        self.refused(2, "retro", "file", "-", agent="zz-test-retro", stdin=json.dumps(body))

    def test_bad_fix_type_refused(self):
        self._incident("bug", "qa")
        body = {
            "window_end": NOW,
            "causes": [{"cause": "bug", "count": 1}],
            "evidence": [{"task_id": "T-0001", "pr_url": PR_URL, "cause": "bug"}],
            "fixes": [{"type": "shell", "cause": "bug", "text": "x"}],
        }
        self.refused(2, "retro", "file", "-", agent="zz-test-retro", stdin=json.dumps(body))

    def test_shell_looking_fix_stored_verbatim(self):
        cmd = "$(factory judge T-1 --merge)"
        body = {
            "window_end": WIN_START,
            "causes": [],
            "evidence": [],
            "fixes": [],
        }
        self.ok("retro", "file", "-", agent="zz-test-retro", stdin=json.dumps(body), extra_env={"FACTORY_NOW": WIN_START})
        # empty window — need separate test with nonempty using stored text
        self._incident("other", "gate", created=WIN_START)
        body2 = {
            "window_end": NOW,
            "causes": [{"cause": "other", "count": 1}],
            "evidence": [{"task_id": "T-0001", "pr_url": PR_URL, "cause": "other"}],
            "fixes": [{"type": "check", "cause": "other", "text": cmd}],
        }
        self.ok("retro", "file", "-", agent="zz-test-retro", stdin=json.dumps(body2))
        fixes = json.loads(self.env.sql("SELECT fixes FROM retro_reports ORDER BY id DESC LIMIT 1")[0]["fixes"])
        self.assertEqual(fixes[0]["text"], cmd)

    def test_empty_window_zero_fixes(self):
        body = {"window_end": NOW, "causes": [], "evidence": [], "fixes": []}
        self.ok("retro", "file", "-", agent="zz-test-retro", stdin=json.dumps(body))


FD = "2026-11-11"
FNOW = {"FACTORY_NOW": "2026-11-12T09:00:00+01:00"}
MERGED_9 = "2026-11-12T08:00:00+01:00"
SIG = "GUARD_IMPL=diff|Go mirror"
FIXES = [{"type": "check", "cause": "bug", "text": "run prcheck before done"},
         {"type": "template", "cause": "other", "text": "add acceptance item"}]


class AdoptedFixesTests(FactoryTestCase):
    """T-1695: factory retro adopt / fixes and the pending -> adopted sync."""

    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_task("T-9", status="done", pr_url="https://example.test/pr/9")
        self.env.seed_task("T-8", status="in_review", pr_url="https://example.test/pr/8")
        for n, r in (("zz-test-retro", "retro"), ("zz-test-director", "director"), ("zz-test-qa", "qa"),
                     ("zz-test-coder", "coder"), ("zz-test-pm", "pm")):
            self.env.seed_agent(n, r, goal=None, team=None, task=None)
        self.env.sql("INSERT INTO retro_reports(day, causes, evidence, fixes, author, created) VALUES(?,?,?,?,?,?)",
                     (FD, "[]", "[]", json.dumps(FIXES), "zz-test-retro", f"{FD}T07:00:00+01:00"))
        self.env.sql("INSERT INTO events(ts, actor, entity, entity_id, action) VALUES(?,?,?,?,?)",
                     (MERGED_9, "judge", "task", "T-9", "merged"))

    def adopt(self, who, fix, task, sig=SIG, day=FD, code=0):
        return self.refused(code, "retro", "adopt", "--day", day, "--fix", str(fix), "--task", task, "--signature", sig,
                            agent=who, extra_env=FNOW)

    def rows(self):
        return [dict(r) for r in self.env.sql("SELECT * FROM adopted_fixes ORDER BY id")]

    def test_adopt_merged_task(self):
        p = self.adopt("zz-test-director", 1, "T-9")
        self.assertEqual(p.stdout, f"adopted F-001 (check, bug) T-9 adopted_at {MERGED_9}\n")
        (row,) = self.rows()
        self.assertEqual((row["cause"], row["type"], row["text"], row["status"], row["adopted_at"], row["pr_url"]),
                         ("bug", "check", FIXES[0]["text"], "adopted", MERGED_9, "https://example.test/pr/9"))
        self.assertEqual((row["signature"], row["author"]), (SIG, "zz-test-director"))
        self.assertTrue(self.env.sql("SELECT 1 FROM events WHERE action='fix-adopted' AND entity_id='F-001' AND detail=?",
                                     (f"{FD}#1 T-9",)))

    def test_retro_and_other_roles_refused(self):
        p = self.adopt("zz-test-retro", 1, "T-9", code=3)
        self.assertIn("role 'retro' may not adopt fixes; the retro agent only proposes", p.stderr)
        self.adopt("zz-test-qa", 1, "T-9", code=3)
        self.adopt("zz-test-coder", 1, "T-9", code=3)
        self.assertEqual(self.rows(), [])

    def test_refusals_write_nothing(self):
        self.assertIn("no retro report for 2026-01-01", self.adopt("zz-test-pm", 1, "T-9", day="2026-01-01", code=2).stderr)
        self.assertIn(f"report {FD} has 2 fixes; --fix must be 1..2", self.adopt("zz-test-pm", 3, "T-9", code=2).stderr)
        self.assertIn(f"report {FD} has 2 fixes; --fix must be 1..2", self.adopt("zz-test-pm", 0, "T-9", code=2).stderr)
        self.assertIn("unknown task T-404", self.adopt("zz-test-pm", 1, "T-404", code=2).stderr)
        err = "signature needs 1-10 '|'-separated terms of 3-80 characters"
        self.assertIn(err, self.adopt("zz-test-pm", 1, "T-9", sig="ab", code=2).stderr)
        self.assertIn(err, self.adopt("zz-test-pm", 1, "T-9", sig="|".join(["abc"] * 11), code=2).stderr)
        self.assertIn(err, self.adopt("zz-test-pm", 1, "T-9", sig="abc|  |def", code=2).stderr)
        self.assertEqual(self.rows(), [])
        self.adopt("zz-test-pm", 1, "T-9")
        self.assertIn(f"fix {FD}#1 is already F-001", self.adopt("zz-test-pm", 1, "T-8", code=2).stderr)
        self.assertEqual(len(self.rows()), 1)

    def test_signature_terms_are_stripped(self):
        self.adopt("zz-test-pm", 1, "T-9", sig="  abc  |  def ghi ")
        self.assertEqual(self.rows()[0]["signature"], "abc|def ghi")

    def test_pending_then_tick_sync_with_agent_off(self):
        self.adopt("zz-test-director", 1, "T-9")
        p = self.adopt("zz-test-director", 2, "T-8")
        self.assertEqual(p.stdout, "linked F-002 (template, other) T-8: pending until T-8 merges\n")
        row = self.rows()[1]
        self.assertEqual((row["status"], row["adopted_at"], row["pr_url"]), ("pending", None, None))
        self.assertTrue(self.env.sql("SELECT 1 FROM events WHERE action='fix-linked' AND entity_id='F-002'"))
        merged_8 = "2026-11-12T08:30:00+01:00"
        self.env.sql("INSERT INTO events(ts, actor, entity, entity_id, action) VALUES(?,?,?,?,?)",
                     (merged_8, "judge", "task", "T-8", "merged"))
        self.ok("config", "retro_enabled", "0")
        self.ok("tick", extra_env={"FACTORY_NOW": "2026-11-12T10:00:00+01:00"})
        row = self.rows()[1]
        self.assertEqual((row["status"], row["adopted_at"], row["pr_url"]), ("adopted", merged_8, "https://example.test/pr/8"))
        self.assertTrue(self.env.sql("SELECT 1 FROM events WHERE action='fix-adopted' AND entity_id='F-002' AND detail=?",
                                     (f"{FD}#2 T-8",)))

    def test_fixes_listing(self):
        self.assertEqual(self.ok("retro", "fixes", agent="zz-test-retro").stdout,
                         "no adopted fixes\ncycles: 0 (check/tool: 0)\n")
        self.adopt("zz-test-director", 1, "T-9")
        self.adopt("zz-test-director", 2, "T-8")
        out = self.ok("retro", "fixes", agent="zz-test-retro").stdout.splitlines()
        self.assertEqual(out, [f"F-001 adopted check bug T-9 adopted_at={MERGED_9} checked_at=- sig={SIG}",
                               "F-002 pending template other T-8 adopted_at=- checked_at=- sig=" + SIG,
                               "cycles: 0 (check/tool: 0)"])
        only = self.ok("retro", "fixes", "--status", "pending", agent="zz-test-director").stdout.splitlines()
        self.assertEqual([x.split()[0] for x in only[:-1]], ["F-002"])
        self.refused(3, "retro", "fixes", agent="zz-test-coder")
        self.refused(2, "retro", "fixes", "--status", "bogus", agent="zz-test-pm")

    def test_fixes_sync_pending_on_read(self):
        self.adopt("zz-test-director", 2, "T-8")
        self.env.sql("INSERT INTO events(ts, actor, entity, entity_id, action) VALUES(?,?,?,?,?)",
                     ("2026-11-12T08:30:00+01:00", "judge", "task", "T-8", "merged"))
        self.assertIn("F-001 adopted template other T-8 adopted_at=2026-11-12T08:30:00+01:00",
                      self.ok("retro", "fixes", agent="zz-test-pm").stdout)

    def test_cycles_count(self):
        for i, (st, ty) in enumerate((("held", "check"), ("noise", "template"), ("reopened", "check"), ("adopted", "check")), 1):
            self.env.sql("INSERT INTO adopted_fixes(id, report_day, fix_index, cause, type, text, signature, task_id, status, "
                         "author, created) VALUES(?,?,?,?,?,?,?,?,?,?,?)",
                         (f"F-{i:03d}", FD, i, "bug", ty, "x", "abc", "T-9", st, "a", "t"))
        self.assertEqual(self.ok("retro", "fixes", agent="zz-test-pm").stdout.splitlines()[-1], "cycles: 3 (check/tool: 2)")


if __name__ == "__main__":
    unittest.main()
