"""G-004 B2: goal audit and sign-off records."""
import json
import os
import sqlite3
import unittest

from harness import FactoryTestCase, git

WORKER_SETTINGS = "/home/x/factory/roles/settings/coder.json"


class GoalAuditTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_goal("G-002")
        e.seed_task("T-0001", status="done")
        e.seed_task("T-0002", status="cancelled")
        e.seed_task("T-0003", role="pm", status="in_progress", pr_url=None)  # the audit pm's own task
        e.seed_task("T-0009", goal="G-002", role="pm", status="in_progress", pr_url=None)
        e.seed_agent("zz-test-pm", "pm", goal="G-001", task="T-0003")
        e.seed_agent("zz-test-pm2", "pm", goal="G-002", task="T-0009")
        for name, role in (("zz-test-lead", "lead"), ("zz-test-director", "director"), ("zz-test-boss", "boss"),
                           ("zz-test-coder", "coder"), ("zz-test-qa", "qa")):
            e.seed_agent(name, role, goal="G-001")

    def audit(self, agent, flag="--ready", goal="G-001", report="all good"):
        return self.env.run("goal", "audit", goal, flag, "--report", "-", stdin=report, agent=agent)

    def audits(self):
        return self.env.sql("SELECT * FROM audits ORDER BY id")

    def signoff(self, n, agent="zz-test-director", goal="G-001"):
        return self.env.run("goal", "signoff", goal, "--audit", str(n), "--reason", "looks right", agent=agent)

    def ready_audit(self):
        self.env.sql("UPDATE tasks SET status='done' WHERE id='T-0003'")
        self.ok("goal", "audit", "G-001", "--ready", "--report", "-", stdin="ready body", agent="zz-test-pm")

    def test_pm_records_audit_versions_increment(self):
        p = self.ok("goal", "audit", "G-001", "--gaps", "--report", "-", stdin="gap one", agent="zz-test-pm")
        self.assertIn("G-001 audit v1 (gaps)", p.stdout)
        p = self.audit("zz-test-director", "--ready", report="v2 body")
        self.assertEqual(p.returncode, 2)  # T-0003 still open for the director
        self.env.sql("UPDATE tasks SET status='done' WHERE id='T-0003'")
        p = self.ok("goal", "audit", "G-001", "--ready", "--report", "-", stdin="v2 body", agent="zz-test-director")
        self.assertIn("G-001 audit v2 (ready)", p.stdout)
        rows = self.audits()
        self.assertEqual([(r["version"], r["verdict"], r["body"], r["author"]) for r in rows],
                         [(1, "gaps", "gap one", "zz-test-pm"), (2, "ready", "v2 body", "zz-test-director")])
        ev = self.env.sql("SELECT action FROM events WHERE entity='goal' AND entity_id='G-001' AND action LIKE 'audit v%'")
        self.assertEqual([r["action"] for r in ev], ["audit v1", "audit v2"])
        # versions are per goal
        self.ok("goal", "audit", "G-002", "--gaps", "--report", "-", stdin="x", agent="zz-test-pm2")
        self.assertEqual(self.audits()[-1]["version"], 1)

    def test_audit_stores_callers_task_id(self):
        self.ok("goal", "audit", "G-001", "--ready", "--report", "-", stdin="r", agent="zz-test-pm")
        self.ok("goal", "audit", "G-001", "--gaps", "--report", "-", stdin="r", agent="zz-test-director")
        self.assertEqual([r["task_id"] for r in self.audits()], ["T-0003", None])

    def test_audit_requires_ready_or_gaps(self):
        for flags in ((), ("--ready", "--gaps")):
            p = self.refused(2, "goal", "audit", "G-001", *flags, "--report", "-", stdin="r", agent="zz-test-pm")
            self.assertIn("exactly one of --ready or --gaps", p.stderr)
        p = self.refused(2, "goal", "audit", "G-001", "--gaps", "--report", "-", stdin=" ", agent="zz-test-pm")
        self.assertIn("--report is required", p.stderr)
        self.assertEqual(self.audits(), [])

    def test_audit_ready_refused_with_open_tasks_except_callers_own(self):
        self.env.seed_task("T-0004", status="in_review")
        self.env.seed_task("T-0005", status="todo", pr_url=None)
        p = self.refused(2, "goal", "audit", "G-001", "--ready", "--report", "-", stdin="r", agent="zz-test-pm")
        self.assertIn("audit --ready refused: open tasks T-0004(in_review), T-0005(todo)", p.stderr)
        self.assertNotIn("T-0003", p.stderr)
        self.assertEqual(self.audits(), [])
        self.env.sql("UPDATE tasks SET status='done' WHERE id IN ('T-0004','T-0005')")
        self.ok("goal", "audit", "G-001", "--ready", "--report", "-", stdin="r", agent="zz-test-pm")
        # --gaps is never gated by open tasks
        self.env.sql("UPDATE tasks SET status='todo' WHERE id='T-0004'")
        self.ok("goal", "audit", "G-001", "--gaps", "--report", "-", stdin="r", agent="zz-test-pm")

    def test_audit_refused_for_wrong_role(self):
        for who in ("coder", "qa", "lead"):
            p = self.refused(3, "goal", "audit", "G-001", "--gaps", "--report", "-", stdin="r", agent=f"zz-test-{who}")
            self.assertIn(f"role '{who}' may not record a goal audit; allowed: pm (own goal), director", p.stderr)
        self.assertEqual(self.audits(), [])

    def test_audit_refused_for_pm_of_other_goal(self):
        p = self.refused(3, "goal", "audit", "G-001", "--gaps", "--report", "-", stdin="r", agent="zz-test-pm2")
        self.assertIn("zz-test-pm2 works on G-002; the audit is in G-001", p.stderr)
        self.assertEqual(self.audits(), [])

    def test_director_signs_off_latest_ready_audit(self):
        self.ready_audit()
        p = self.ok("goal", "signoff", "G-001", "--audit", "1", "--reason", "all verified", agent="zz-test-director")
        self.assertIn("G-001 audit v1 signed off", p.stdout)
        so = self.env.sql("SELECT * FROM signoffs")
        self.assertEqual(len(so), 1)
        self.assertEqual((so[0]["goal_id"], so[0]["audit_id"], so[0]["author"], so[0]["reason"]),
                         ("G-001", self.audits()[0]["id"], "zz-test-director", "all verified"))
        self.assertEqual(json.loads(so[0]["snapshot"]),
                         {"T-0001": "done", "T-0002": "cancelled", "T-0003": "done"})
        self.assertEqual(len(self.env.sql("SELECT 1 FROM events WHERE action='signoff v1'")), 1)

    def test_signoff_exempts_audit_task_and_owner_may_sign(self):
        self.ok("goal", "audit", "G-001", "--ready", "--report", "-", stdin="r", agent="zz-test-pm")  # T-0003 open
        self.ok("goal", "signoff", "G-001", "--audit", "1", "--reason", "r")  # owner (no FACTORY_AGENT)
        self.assertEqual(json.loads(self.env.sql("SELECT snapshot FROM signoffs")[0]["snapshot"])["T-0003"],
                         "in_progress")

    def test_signoff_refused_for_open_task_other_than_audit_task(self):
        self.ready_audit()
        self.env.seed_task("T-0004", status="todo", pr_url=None)
        p = self.refused(2, "goal", "signoff", "G-001", "--audit", "1", "--reason", "r", agent="zz-test-director")
        self.assertIn("open tasks T-0004(todo)", p.stderr)
        self.assertEqual(self.env.sql("SELECT * FROM signoffs"), [])

    def test_signoff_refused_for_pm_lead_boss(self):
        self.ready_audit()
        for who in ("lead", "boss"):
            p = self.signoff(1, agent=f"zz-test-{who}")
            self.assertEqual(p.returncode, 3, p.stderr)
            self.assertIn("only the director signs off a goal close", p.stderr)
        for who in ("pm", "coder"):  # worker roles are stopped by refuse_worker
            p = self.signoff(1, agent=f"zz-test-{who}", )
            self.assertEqual(p.returncode, 3, p.stderr)
            self.assertIn("workers may not run `factory goal signoff`", p.stderr)
        self.assertEqual(self.env.sql("SELECT * FROM signoffs"), [])

    def test_signoff_refused_for_worker_process_claiming_director(self):
        self.ready_audit()
        pid = os.getpid()
        for n, ppid, argv in ((pid, 1, ["claude", "--settings", WORKER_SETTINGS, "--resume", "s"]),):
            dd = self.env.proc_dir / str(n)
            dd.mkdir(exist_ok=True)
            (dd / "cmdline").write_bytes(b"\0".join(x.encode() for x in argv) + b"\0")
            (dd / "stat").write_text(f"{n} (x) S {ppid} {n} {n} 0 -1 0\n")
        p = self.signoff(1, agent="zz-test-director")
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn("workers may not run `factory goal signoff`", p.stderr)
        self.assertNotIn("only the director", p.stderr)  # refuse_worker ran before require_role
        self.assertEqual(self.env.sql("SELECT * FROM signoffs"), [])

    def test_signoff_refused_for_gaps_audit(self):
        self.ok("goal", "audit", "G-001", "--gaps", "--report", "-", stdin="r", agent="zz-test-pm")
        p = self.signoff(1)
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertIn("audit v1 reports gaps; nothing to sign off", p.stderr)

    def test_signoff_refused_for_non_latest_audit(self):
        self.ready_audit()
        self.ok("goal", "audit", "G-001", "--ready", "--report", "-", stdin="r2", agent="zz-test-pm")
        p = self.signoff(1)
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertIn("audit v1 is not the latest audit of G-001 (latest: v2)", p.stderr)
        self.assertEqual(self.env.sql("SELECT * FROM signoffs"), [])

    def test_signoff_requires_reason(self):
        self.audit("zz-test-pm")
        for extra in ((), ("--reason", "  ")):
            p = self.refused(2, "goal", "signoff", "G-001", "--audit", "1", *extra, agent="zz-test-director")
            self.assertIn("--reason is required", p.stderr)
        self.assertEqual(self.env.sql("SELECT * FROM signoffs"), [])

    def test_signoff_refused_without_audit(self):
        p = self.signoff(1)
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertIn("no audit for G-001: run factory goal audit", p.stderr)

    def test_migrate_creates_audit_tables_on_old_db(self):
        con = sqlite3.connect(self.env.db_path)
        con.execute("DROP TABLE audits")
        con.execute("DROP TABLE signoffs")
        con.commit()
        con.close()
        for _ in range(2):
            self.ok("goal", "show", "G-001")
            names = {r["name"] for r in self.env.sql("SELECT name FROM sqlite_master WHERE type='table'")}
            self.assertLessEqual({"audits", "signoffs"}, names)
        self.ok("goal", "audit", "G-001", "--gaps", "--report", "-", stdin="r", agent="zz-test-pm")

    def test_goal_show_prints_latest_audit_and_signoff(self):
        self.assertNotIn("## audit", self.ok("goal", "show", "G-001").stdout)
        self.ok("goal", "audit", "G-001", "--gaps", "--report", "-", stdin="old gap report", agent="zz-test-pm")
        self.ready_audit()
        out = self.ok("goal", "show", "G-001").stdout
        self.assertIn("## audit v2 ready (zz-test-pm,", out)
        self.assertIn("ready body", out)
        self.assertNotIn("old gap report", out)
        self.assertNotIn("sign-off", out)
        self.ok("goal", "signoff", "G-001", "--audit", "2", "--reason", "verified by director", agent="zz-test-director")
        out = self.ok("goal", "show", "G-001").stdout
        self.assertIn("## sign-off of audit v2 (zz-test-director,", out)
        self.assertIn("verified by director", out)

    def test_export_goal_page_includes_audit_and_signoff(self):
        self.ready_audit()
        self.ok("goal", "signoff", "G-001", "--audit", "1", "--reason", "verified by director", agent="zz-test-director")
        git("init", "-q", cwd=self.env.home_dir)
        self.ok("export", "--no-push")
        page = (self.env.home_dir / "exports/goals/G-001.md").read_text()
        self.assertIn("## Audit (v1, ready, zz-test-pm,", page)
        self.assertIn("ready body", page)
        self.assertIn("### Sign-off (zz-test-director,", page)
        self.assertIn("verified by director", page)

    def test_state_dump_contains_audits_and_signoffs(self):
        self.ready_audit()
        self.ok("goal", "signoff", "G-001", "--audit", "1", "--reason", "r", agent="zz-test-director")
        git("init", "-q", cwd=self.env.home_dir)
        self.ok("export", "--no-push")
        sql = (self.env.home_dir / "state/factory.sql").read_text()
        self.assertIn("audits", sql)
        con = sqlite3.connect(":memory:")
        self.addCleanup(con.close)
        con.executescript(sql)
        self.assertEqual(con.execute("SELECT version,verdict FROM audits").fetchall(), [(1, "ready")])
        self.assertEqual(con.execute("SELECT reason FROM signoffs").fetchall(), [("r",)])


if __name__ == "__main__":
    unittest.main()
