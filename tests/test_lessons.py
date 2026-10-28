import os
import unittest

from harness import FactoryTestCase

WORKERS = ["pm", "coder", "qa", "reviewer", "judge", "cleaner", "staff-engineer"]
IN_PANE = {"CLAUDECODE": "1", "HERDR_PANE_ID": "w9:p9"}


class LessonTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        for r in ("boss", "director", "lead"):
            self.env.seed_agent(f"zz-test-{r}", r)
        for r in WORKERS:
            self.env.seed_agent(f"zz-test-{r}", r)

    def count(self, table="lessons"):
        return self.env.sql(f"SELECT count(*) c FROM {table}")[0]["c"]

    def add(self, *extra, agent="zz-test-lead"):
        return self.env.run("lesson", "add", "--title", "t", "--roles", "lead,director", "--rule", "r", *extra, agent=agent)

    def new(self, *extra, **kw):
        p = self.add(*extra, **kw)
        self.assertEqual(p.returncode, 0, p.stderr)
        return p.stdout.strip()

    def test_add_as_each_allowed_actor_and_show(self):
        for i, agent in enumerate(["zz-test-boss", "zz-test-director", "zz-test-lead", "owner"], 1):
            p = self.ok("lesson", "add", "--title", "t", "--roles", "lead,director", "--rule", "r",
                        "--source", "M733,T-0016", agent=agent)
            self.assertEqual(p.stdout.strip(), f"L-{i:03d}")
        out = self.ok("lesson", "show", "L-001", agent="zz-test-lead").stdout
        for s in ("title: t", "roles: lead,director", "sources: M733,T-0016", "status: open", "## rule\nr",
                  "## links\n_none_", "## history", "zz-test-boss add: t"):
            self.assertIn(s, out)

    def test_rule_and_what_from_stdin(self):
        self.ok("lesson", "add", "--title", "t", "--roles", "all", "--rule", "-", stdin="multi\nline\n", agent="zz-test-lead")
        self.assertEqual(self.env.sql("SELECT rule FROM lessons")[0]["rule"], "multi\nline")

    def test_workers_refused(self):
        lid = self.new()
        for role in WORKERS:
            with self.subTest(role=role):
                self.assertEqual(self.add(agent=f"zz-test-{role}").returncode, 3)
                p = self.env.run("lesson", "edit", lid, "--title", "x", agent=f"zz-test-{role}")
                self.assertEqual(p.returncode, 3)
        self.assertEqual(self.count(), 1)
        self.assertEqual(self.env.sql("SELECT title FROM lessons")[0]["title"], "t")

    def test_worker_process_claiming_director_refused(self):
        lid = self.new()
        self.env.fake_claude(os.getpid(), argv=("claude", "--settings", "/home/x/factory/roles/settings/coder.json"))
        self.assertEqual(self.add(agent="zz-test-director").returncode, 3)
        p = self.env.run("lesson", "edit", lid, "--title", "x", agent="zz-test-director")
        self.assertEqual(p.returncode, 3)
        self.assertEqual(self.count(), 1)

    def test_unknown_identity(self):
        lid = self.new()
        for extra in ({"CLAUDECODE": "1"}, IN_PANE):
            self.ok("lesson", "list", extra_env=extra)
            self.ok("lesson", "show", lid, extra_env=extra)
            p = self.env.run("lesson", "add", "--title", "t", "--roles", "all", "--rule", "r", extra_env=extra)
            self.assertEqual(p.returncode, 3)
            p = self.env.run("lesson", "edit", lid, "--title", "x", extra_env=extra)
            self.assertEqual(p.returncode, 3)
        self.assertEqual(self.count(), 1)

    def test_validation_exits_2_and_writes_nothing(self):
        bad = [
            ("--title", " ", "--roles", "all", "--rule", "r"),
            ("--title", "t", "--roles", "all", "--rule", " "),
            ("--title", "t", "--roles", "", "--rule", "r"),
            ("--title", "t", "--roles", "lead,nobody", "--rule", "r"),
            ("--title", "t", "--roles", "all", "--rule", "r", "--source", "M733,bogus"),
            ("--title", "t", "--roles", "all", "--rule", "r", "--source", "https://example.com/pull/1"),
            ("--title", "t", "--roles", "all", "--rule", "-", "--what", "-"),
        ]
        for args in bad:
            with self.subTest(args=args):
                p = self.env.run("lesson", "add", *args, stdin="x", agent="zz-test-lead")
                self.assertEqual(p.returncode, 2, p.stderr)
        self.assertIn("valid:", self.env.run("lesson", "add", "--title", "t", "--roles", "x", "--rule", "r",
                                             agent="zz-test-lead").stderr)
        self.assertEqual(self.count(), 0)
        self.ok("lesson", "add", "--title", "t", "--roles", "all", "--rule", "r", "--source",
                "G-003,#11,https://github.com/a/b/pull/12", agent="zz-test-lead")

    def test_edit_appends_sources_and_rejects_nothing(self):
        lid = self.new("--source", "M733,T-0016")
        self.ok("lesson", "edit", lid, "--source", "T-0016,M900", agent="zz-test-lead")
        self.assertEqual(self.env.sql("SELECT sources FROM lessons")[0]["sources"], "M733,T-0016,M900")
        self.ok("lesson", "edit", lid, "--title", "new", "--roles", "pm", "--rule", "r2", "--what", "w",
                agent="zz-test-director")
        row = self.env.sql("SELECT * FROM lessons")[0]
        self.assertEqual((row["title"], row["roles"], row["rule"], row["what"]), ("new", "pm", "r2", "w"))
        self.assertIn("title,roles,rule,what", self.ok("lesson", "show", lid).stdout)
        for args in ((lid,), (lid, "--source", "M733"), (lid, "--title", "new")):
            self.assertEqual(self.env.run("lesson", "edit", *args, agent="zz-test-lead").returncode, 2)
        self.assertEqual(self.env.run("lesson", "edit", "L-099", "--title", "x", agent="zz-test-lead").returncode, 2)
        self.assertEqual(self.env.run("lesson", "edit", lid, "--roles", "bad", agent="zz-test-lead").returncode, 2)
        self.assertEqual(self.env.run("lesson", "show", "L-099").returncode, 2)

    def test_list_filters(self):
        a = self.new()
        self.ok("lesson", "add", "--title", "everyone", "--roles", "all", "--rule", "r", agent="zz-test-lead")
        self.ok("lesson", "add", "--title", "coders", "--roles", "coder", "--rule", "r", agent="zz-test-lead")
        self.env.sql("UPDATE lessons SET status='applied' WHERE id='L-003'")
        self.env.sql("INSERT INTO lesson_links(lesson_id,created) VALUES('L-001','2026-01-01')")
        out = self.ok("lesson", "list").stdout
        self.assertEqual([l.split()[0] for l in out.splitlines()[1:]], [a, "L-002", "L-003"])
        self.assertRegex(out.splitlines()[1], r"L-001\s+open\s+lead,director\s+1\s+0\s+t")

        def ids(*args):
            return [l.split()[0] for l in self.ok("lesson", "list", *args).stdout.splitlines()[1:]]
        self.assertEqual(ids("--status", "open", "--role", "lead"), ["L-001", "L-002"])
        self.assertEqual(ids("--role", "coder"), ["L-002", "L-003"])
        self.assertEqual(ids("--status", "applied"), ["L-003"])
        self.assertEqual(ids("--status", "verified"), [])
        self.assertEqual(self.env.run("lesson", "list", "--status", "bogus").returncode, 2)
        self.assertIn("1", self.ok("lesson", "show", "L-001").stdout)

    def test_only_capture_subcommands(self):
        """An unknown lesson subcommand is a usage error (exit 2); the help lists exactly the supported ones."""
        self.assertEqual(self.env.run("lesson", "bogus").returncode, 2)
        h = self.ok("lesson", "--help").stdout
        self.assertIn("{add,list,show,edit,link,apply,verify,reopen}", h)

    def test_migration_creates_tables_idempotently(self):
        self.env.seed_goal()
        self.env.seed_task("T-0001")
        self.env.sql("DROP TABLE lesson_links")
        self.env.sql("DROP TABLE lessons")
        before = (self.env.sql("SELECT * FROM goals"), self.env.sql("SELECT * FROM tasks"))
        before = [[tuple(r) for r in x] for x in before]
        for _ in range(2):
            self.ok("lesson", "list")
            names = {r["name"] for r in self.env.sql("SELECT name FROM sqlite_master WHERE type='table'")}
            self.assertLessEqual({"lessons", "lesson_links"}, names)
            after = [[tuple(r) for r in self.env.sql(f"SELECT * FROM {t}")] for t in ("goals", "tasks")]
            self.assertEqual(after, before)
        self.assertEqual(self.new(), "L-001")


if __name__ == "__main__":
    unittest.main()
