"""T-0370: tasks record who a blocked task waits on (owner | internal); task show/list, res and digest show it."""
import datetime as dt
import json
import os
import sqlite3
import textwrap
import unittest
import unittest.mock

from harness import FactoryTestCase

FROZEN = dt.datetime(2026, 10, 27, 12, 0).astimezone()


class TaskWaitsTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(lead="zz-test-lead")
        for tid in ("T-0001", "T-0002", "T-0003"):
            e.seed_task(tid, status="in_progress", pr_url=None, team="g-001-a")

    def waits(self, tid):
        return self.env.sql("SELECT waits FROM tasks WHERE id=?", (tid,))[0]["waits"]

    def test_default_is_internal_and_explicit_owner_sets(self):
        self.ok("task", "update", "T-0001", "--status", "blocked", "--reason", "queue full")
        self.ok("task", "update", "T-0002", "--status", "blocked", "--reason", "needs owner key", "--waits", "owner")
        self.assertEqual((self.waits("T-0001"), self.waits("T-0002")), ("internal", "owner"))
        ev = self.env.sql("SELECT detail FROM events WHERE entity_id='T-0002' AND action='update'")[0]["detail"]
        self.assertIn("waits=owner", ev)
        self.assertIn("needs owner key", ev)

    def test_show_and_list_print_waits_for_blocked_only(self):
        self.ok("task", "update", "T-0002", "--status", "blocked", "--waits", "owner")
        self.assertIn("waits: owner", self.ok("task", "show", "T-0002").stdout)
        self.assertNotIn("waits:", self.ok("task", "show", "T-0001").stdout)
        rows = {l.split()[0]: l for l in self.ok("task", "list").stdout.splitlines()[1:]}
        self.assertRegex(rows["T-0002"], r"blocked\s+owner")
        self.assertNotIn("owner", rows["T-0001"])
        self.assertIn("WAITS", self.ok("task", "list").stdout.splitlines()[0])

    def test_clearing_waits_by_leaving_blocked_or_flipping(self):
        self.ok("task", "update", "T-0002", "--status", "blocked", "--waits", "owner")
        self.ok("task", "update", "T-0002", "--waits", "internal")        # explicit flip while blocked
        self.assertEqual(self.waits("T-0002"), "internal")
        self.ok("task", "update", "T-0002", "--waits", "owner")
        self.ok("task", "update", "T-0002", "--status", "in_progress")    # unblock resets
        self.assertEqual(self.waits("T-0002"), "internal")
        self.ok("task", "update", "T-0002", "--status", "blocked")        # re-block without --waits: internal
        self.assertEqual(self.waits("T-0002"), "internal")

    def test_reblock_without_waits_keeps_existing_value(self):
        self.ok("task", "update", "T-0002", "--status", "blocked", "--waits", "owner")
        self.ok("task", "update", "T-0002", "--status", "blocked", "--reason", "still waiting")
        self.assertEqual(self.waits("T-0002"), "owner")

    def test_waits_refused_when_not_blocked_or_bad_value(self):
        p = self.refused(2, "task", "update", "T-0001", "--waits", "owner")
        self.assertIn("--status blocked", p.stderr)
        self.assertEqual(self.waits("T-0001"), "internal")
        self.refused(2, "task", "update", "T-0001", "--status", "blocked", "--waits", "nobody")
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"], "in_progress")

    def test_res_counts_owner_vs_internal(self):
        self.ok("task", "update", "T-0001", "--status", "blocked")
        self.ok("task", "update", "T-0002", "--status", "blocked", "--waits", "owner")
        self.ok("task", "update", "T-0003", "--status", "blocked")
        self.assertIn("blocked tasks: 1 owner-blocked · 2 internal-blocked", self.ok("res").stdout)

    def test_res_blocked_line_after_workers_line(self):
        lines = self.ok("res").stdout.splitlines()
        i = next(n for n, l in enumerate(lines) if l.startswith("workers live "))
        self.assertTrue(any(l.startswith("blocked tasks: ") for l in lines[i + 1:]), lines)

    def test_digest_counts_and_tags(self):
        self.ok("task", "update", "T-0001", "--status", "blocked", "--reason", "queue")
        self.ok("task", "update", "T-0002", "--status", "blocked", "--reason", "need key", "--waits", "owner")
        out = self.ok("digest", "--date", FROZEN.date().isoformat(), "--hours", "24",
                      extra_env={"FACTORY_NOW": FROZEN.isoformat()}).stdout
        self.assertIn("Blocked tasks: 1 owner-blocked, 1 internal-blocked", out)
        self.assertIn("T-0002 zz-test task — blocked (owner): need key", out)
        self.assertIn("T-0001 zz-test task — blocked: queue", out)


class MigrateWaitsTests(FactoryTestCase):
    def test_old_db_gets_waits_column_default_internal(self):
        e = self.env
        e.seed_goal()
        e.seed_task("T-0001", status="blocked", pr_url=None)
        e.sql("ALTER TABLE tasks DROP COLUMN waits")
        self.assertNotIn("waits", [r["name"] for r in e.sql("PRAGMA table_info(tasks)")])
        self.ok("task", "show", "T-0001")                  # any command opens the DB through migrate()
        self.assertEqual(e.sql("SELECT waits FROM tasks WHERE id='T-0001'")[0]["waits"], "internal")
        self.assertIn("waits: internal", self.ok("task", "show", "T-0001").stdout)

    def test_old_db_gets_block_on_column_null(self):
        e = self.env
        e.seed_goal()
        e.seed_task("T-0001", status="todo", pr_url=None)
        cols = [r["name"] for r in e.sql("PRAGMA table_info(tasks)")]
        if "block_on" in cols:
            e.sql("ALTER TABLE tasks DROP COLUMN block_on")
        self.assertNotIn("block_on", [r["name"] for r in e.sql("PRAGMA table_info(tasks)")])
        self.ok("task", "show", "T-0001")
        self.assertIsNone(e.sql("SELECT block_on FROM tasks WHERE id='T-0001'")[0]["block_on"])


BLOCK_EVAL_STUB = textwrap.dedent("""\
#!/usr/bin/env python3
import json, re, sys

RE_TASK = re.compile(r"^T-[0-9]+$")
RE_OWNER = re.compile(r"^M[0-9]+$")
RE_PR = re.compile(r"^#[0-9]+$")
RE_GOAL = re.compile(r"^G-[0-9]+$")
ALLOWED_CHECKS = frozenset(("gh-worker-ro-token-mode-600", "gh-worker-rw-token-mode-600"))

def refuse():
    sys.exit(2)

def parse_token(token):
    idx = token.find(":")
    if idx <= 0:
        refuse()
    typ, rest = token[:idx], token[idx + 1:]
    if not rest:
        refuse()
    if typ == "task":
        if not RE_TASK.match(rest):
            refuse()
        return {"type": "task", "arg": rest}
    if typ == "pr":
        j = rest.find(":")
        if j <= 0:
            refuse()
        arg, form = rest[:j], rest[j + 1:]
        if form not in ("merged", "closed") or not RE_PR.match(arg):
            refuse()
        return {"type": "pr", "arg": arg, "form": form}
    if typ == "main":
        if rest != "green":
            refuse()
        return {"type": "main", "arg": rest}
    if typ == "owner":
        if not RE_OWNER.match(rest):
            refuse()
        return {"type": "owner", "arg": rest}
    if typ == "check":
        if rest not in ALLOWED_CHECKS:
            refuse()
        return {"type": "check", "arg": rest}
    if typ == "goal":
        j = rest.find(":")
        if j <= 0:
            refuse()
        arg, form = rest[:j], rest[j + 1:]
        if form not in ("done", "released") or not RE_GOAL.match(arg):
            refuse()
        return {"type": "goal", "arg": arg, "form": form}
    refuse()

if __name__ == "__main__":
    if len(sys.argv) != 2 or sys.argv[1] != "parse":
        refuse()
    out = [parse_token(line.strip()) for line in sys.stdin if line.strip()]
    json.dump(out, sys.stdout)
    sys.stdout.write("\\n")
""")


class TaskBlockTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team()
        for tid in ("T-0001", "T-0002", "T-0003"):
            e.seed_task(tid, status="in_progress", pr_url=None)
        stub = e.home_dir / "bin" / "block-eval-stub"
        stub.parent.mkdir(parents=True, exist_ok=True)
        stub.write_text(BLOCK_EVAL_STUB)
        stub.chmod(0o755)
        self.stub_env = {"FACTORY_BLOCK_EVAL": str(stub)}

    def test_block_records_two_tokens(self):
        self.ok("task", "block", "T-0001", "--on", "main:green", "--on", "pr:#1:merged", extra_env=self.stub_env)
        row = self.env.sql("SELECT status, block_on, waits FROM tasks WHERE id='T-0001'")[0]
        self.assertEqual((row["status"], row["waits"]), ("blocked", "internal"))
        conds = json.loads(row["block_on"])
        self.assertEqual(len(conds), 2)
        show = self.ok("task", "show", "T-0001").stdout
        self.assertIn("block_on: main:green pr:#1:merged", show)
        ev = self.env.sql("SELECT detail FROM events WHERE entity_id='T-0001' AND action='update' ORDER BY id DESC")[0]
        self.assertIn("status=blocked — main:green pr:#1:merged", ev["detail"])

    def test_unknown_token_stores_nothing(self):
        self.refused(2, "task", "block", "T-0001", "--on", "check:rm-rf", extra_env=self.stub_env)
        row = self.env.sql("SELECT status, block_on FROM tasks WHERE id='T-0001'")[0]
        self.assertEqual((row["status"], row["block_on"]), ("in_progress", None))

    def test_blank_or_multiline_token_stores_nothing(self):
        for bad in ("", "   ", "main:green\ntask:T-0002"):
            self.refused(2, "task", "block", "T-0001", "--on", bad, extra_env=self.stub_env)
        row = self.env.sql("SELECT status, block_on FROM tasks WHERE id='T-0001'")[0]
        self.assertEqual((row["status"], row["block_on"]), ("in_progress", None))

    def test_depends_on_is_not_copied(self):
        self.ok("task", "update", "T-0001", "--after", "T-0002")
        deps = json.loads(self.env.sql("SELECT depends_on FROM tasks WHERE id='T-0001'")[0]["depends_on"])
        self.assertEqual(deps, ["T-0002"])
        self.ok("task", "block", "T-0001", "--on", "task:T-0999", extra_env=self.stub_env)
        row = self.env.sql("SELECT depends_on, block_on FROM tasks WHERE id='T-0001'")[0]
        self.assertEqual(json.loads(row["depends_on"]), ["T-0002"])
        conds = json.loads(row["block_on"])
        self.assertEqual([c["arg"] for c in conds], ["T-0999"])

    def test_live_home_refuses_block_eval_override(self):
        with self.env.load_factory() as f:
            f.LIVE_HOMES = f.LIVE_HOMES | {str(self.env.home_dir.resolve())}
            with unittest.mock.patch.dict(os.environ, {"FACTORY_BLOCK_EVAL": self.stub_env["FACTORY_BLOCK_EVAL"]}):
                with self.assertRaises(SystemExit) as cm:
                    f.block_eval_parse(["main:green"])
            self.assertEqual(cm.exception.code, 2)


class BlockMigrateTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team()
        e.seed_agent("zz-test-director", "director")
        for tid in ("T-0001", "T-0002"):
            e.seed_task(tid, status="in_progress", pr_url=None)
        stub = e.home_dir / "bin" / "block-eval-stub"
        stub.parent.mkdir(parents=True, exist_ok=True)
        stub.write_text(BLOCK_EVAL_STUB)
        stub.chmod(0o755)
        self.stub_env = {"FACTORY_BLOCK_EVAL": str(stub)}

    def flag(self, v="1"):
        self.ok("config", "block_conditions", v, agent="zz-test-director")

    def block(self, tid, note):
        self.ok("task", "update", tid, "--status", "blocked", "--note", note)

    def migrate(self, *args):
        return self.ok("task", "block-migrate", *args, agent="zz-test-director", extra_env=self.stub_env).stdout

    def row(self, tid):
        return self.env.sql("SELECT status, block_on, waits FROM tasks WHERE id=?", (tid,))[0]

    def test_held_until_merges(self):
        self.flag()
        self.block("T-0001", "held until #408 merges")
        out = self.migrate()
        self.assertIn("convert T-0001 pr:#408:merged", out)
        r = self.row("T-0001")
        self.assertEqual(r["status"], "blocked")
        self.assertEqual(json.loads(r["block_on"]), [{"type": "pr", "arg": "#408", "form": "merged"}])

    def test_until_closed_and_single_token(self):
        self.flag()
        self.block("T-0001", "until #7 closed")
        self.block("T-0002", "owner:M12")
        self.migrate()
        self.assertEqual(json.loads(self.row("T-0001")["block_on"])[0]["form"], "closed")
        r = self.row("T-0002")
        self.assertEqual((r["status"], r["waits"]), ("blocked", "owner"))
        self.assertIsNotNone(r["block_on"])

    def test_prose_stays_blocked(self):
        self.flag()
        self.block("T-0001", "waiting for the owner to think about it")
        self.assertIn("keep T-0001", self.migrate())
        r = self.row("T-0001")
        self.assertEqual((r["status"], r["block_on"]), ("blocked", None))

    def test_foreign_slug_note_stays_text(self):
        self.flag()
        self.block("T-0001", "held until #1 merges in example-org/other-repo")
        self.assertIn("keep T-0001", self.migrate())
        r = self.row("T-0001")
        self.assertEqual((r["status"], r["block_on"]), ("blocked", None))

    def test_depends_on_is_not_a_condition(self):
        self.flag()
        self.ok("task", "update", "T-0001", "--after", "T-0002")
        self.block("T-0001", "queue")
        self.assertIn("keep T-0001", self.migrate())
        r = self.env.sql("SELECT depends_on, block_on FROM tasks WHERE id='T-0001'")[0]
        self.assertIsNone(r["block_on"])
        self.assertEqual(json.loads(r["depends_on"]), ["T-0002"])

    def test_dry_run_flag_off_writes_nothing(self):
        self.block("T-0001", "held until #408 merges")
        self.block("T-0002", "queue")
        before = self.env.sql("SELECT COUNT(*) n FROM events")[0]["n"]
        out = self.migrate("--dry-run")
        self.assertIn("convert T-0001", out)
        self.assertIn("keep T-0002", out)
        self.assertIsNone(self.row("T-0001")["block_on"])
        self.assertEqual(self.env.sql("SELECT COUNT(*) n FROM events")[0]["n"], before)

    def test_flag_off_refuses_write(self):
        self.block("T-0001", "held until #408 merges")
        self.refused(2, "task", "block-migrate", agent="zz-test-director", extra_env=self.stub_env)
        self.assertIsNone(self.row("T-0001")["block_on"])


if __name__ == "__main__":
    unittest.main()
