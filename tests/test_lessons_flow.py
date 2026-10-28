import datetime as dt
import json
import unittest

from harness import PR_URL, FactoryTestCase, gates_summary

GREEN = [{"name": "unittest", "status": "COMPLETED", "conclusion": "SUCCESS"}]
WORKERS = ["pm", "coder", "qa", "reviewer", "judge", "cleaner", "staff-engineer"]
OLD_PR = "https://github.com/zz-test/zz-test/pull/11"
FROZEN = dt.datetime(2026, 10, 24, 12, 0).astimezone()
NOW_ENV = {"FACTORY_NOW": FROZEN.isoformat()}


def iso(delta):
    return (FROZEN + delta).isoformat(timespec="seconds")


class FlowBase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj")
        e.seed_task()
        for r in ("boss", "director", "lead"):
            e.seed_agent(f"zz-test-{r}", r)
        for r in WORKERS:
            e.seed_agent(f"zz-test-{r}", r, goal="G-001", task="T-0001")
        e.use_git_head()
        e.seed_pr(checks=GREEN)

    def new(self, title="t"):
        return self.ok("lesson", "add", "--title", title, "--roles", "lead,director", "--rule", "r",
                       agent="zz-test-lead").stdout.strip()

    def lesson(self, lid="L-001"):
        return self.env.sql("SELECT * FROM lessons WHERE id=?", (lid,))[0]

    def merge(self, tid="T-0001"):
        self.ok("verdict", tid, "--pass", "--summary", "ok", agent="zz-test-reviewer")
        self.ok("verdict", tid, "--pass", "--summary", gates_summary(), agent="zz-test-qa")
        return self.env.run("judge", tid, "--merge", "--reason", "zz-test", agent="zz-test-judge")

    def msgs(self, subject="role files changed"):
        return self.env.sql("SELECT * FROM messages WHERE subject LIKE ? ORDER BY id", (subject + "%",))


class LifecycleTests(FlowBase):
    def test_lesson_help_lists_actions(self):
        out = self.ok("lesson", "--help").stdout
        for w in ("add", "list", "show", "edit", "link", "apply", "verify", "reopen"):
            self.assertIn(w, out)

    def test_amendments(self):
        lid = self.new()
        p = self.env.run("lesson", "add", "L-009", "--title", "t", "--roles", "all", "--rule", "r", agent="zz-test-lead")
        self.assertEqual(p.returncode, 2)
        self.assertIn("add takes no id", p.stderr)
        p = self.env.run("lesson", "list", "--role", "bogus", agent="zz-test-lead")
        self.assertEqual(p.returncode, 2)
        self.ok("lesson", "list", "--role", "all", agent="zz-test-lead")
        p = self.env.run("lesson", "show", agent="zz-test-lead")
        self.assertEqual(p.returncode, 2)
        self.assertIn("usage: factory lesson show", p.stderr)
        self.assertNotIn("None", p.stderr)
        self.assertEqual(lid, "L-001")

    def test_link_pr_merged_then_apply(self):
        lid = self.new()
        self.env.seed_pr(OLD_PR, state="MERGED")
        st = json.loads(self.env.gh_state.read_text())
        st["prs"][OLD_PR]["mergedAt"] = "2026-10-01T10:00:00Z"
        self.env.gh_state.write_text(json.dumps(st))
        self.ok("lesson", "link", lid, "--pr", OLD_PR, "--via", "prompt", agent="zz-test-lead")
        self.assertEqual(self.env.sql("SELECT merged_at FROM lesson_links")[0]["merged_at"], "2026-10-01T10:00:00Z")
        p = self.env.run("lesson", "link", lid, "--pr", OLD_PR, agent="zz-test-lead")
        self.assertEqual(p.returncode, 2)
        self.ok("lesson", "apply", lid, "--note", "done", agent="zz-test-lead")
        self.assertEqual(self.lesson()["status"], "applied")
        self.assertTrue(self.lesson()["applied_at"])
        self.assertEqual(self.refused(2, "lesson", "apply", lid, agent="zz-test-lead").returncode, 2)

    def test_apply_unmerged_and_no_links(self):
        lid = self.new()
        p = self.env.run("lesson", "apply", lid, agent="zz-test-lead")
        self.assertEqual(p.returncode, 2)
        self.assertIn("no links", p.stderr)
        self.ok("lesson", "link", lid, "--task", "T-0001", agent="zz-test-lead")
        p = self.env.run("lesson", "apply", lid, agent="zz-test-lead")
        self.assertEqual(p.returncode, 2)
        self.assertIn("T-0001", p.stderr)
        self.assertEqual(self.lesson()["status"], "open")
        self.assertEqual(self.env.run("lesson", "link", lid, "--task", "T-9999", agent="zz-test-lead").returncode, 2)
        self.assertEqual(self.env.run("lesson", "link", lid, "--task", "T-0001", agent="zz-test-lead").returncode, 2)

    def test_apply_rechecks_task_link_via_verdict(self):
        lid = self.new()
        self.ok("lesson", "link", lid, "--task", "T-0001", agent="zz-test-lead")
        self.env.sql("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,created) VALUES(?,?,?,?,?,?)",
                     ("T-0001", PR_URL, "a" * 40, "judge", "merge", "2026-10-02T00:00:00+00:00"))
        self.ok("lesson", "apply", lid, agent="zz-test-lead")
        self.assertEqual(self.lesson()["status"], "applied")

    def test_link_done_merged_task_stores_merged_at(self):
        lid = self.new()
        self.env.sql("UPDATE tasks SET status='done' WHERE id='T-0001'")
        self.env.sql("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,created) VALUES(?,?,?,?,?,?)",
                     ("T-0001", PR_URL, "a" * 40, "judge", "merge", "2026-10-02T00:00:00+00:00"))
        self.ok("lesson", "link", lid, "--task", "T-0001", agent="zz-test-lead")
        row = self.env.sql("SELECT * FROM lesson_links")[0]
        self.assertEqual((row["merged_at"], row["pr_url"]), ("2026-10-02T00:00:00+00:00", PR_URL))

    def test_verify_and_reopen(self):
        lid = self.new()
        p = self.env.run("lesson", "verify", lid, "--evidence", "e", agent="zz-test-director")
        self.assertEqual(p.returncode, 2)  # open
        self.env.sql("UPDATE lessons SET status='applied', applied_at='2026-10-01T00:00:00+00:00'")
        self.assertEqual(self.env.run("lesson", "verify", lid, "--evidence", "e", agent="zz-test-lead").returncode, 3)
        self.assertEqual(self.lesson()["status"], "applied")
        self.ok("lesson", "verify", lid, "--evidence", "seen in M1", agent="zz-test-director")
        r = self.lesson()
        self.assertEqual((r["status"], r["verified_by"], r["evidence"]), ("verified", "zz-test-director", "seen in M1"))
        self.assertTrue(r["verified_at"])
        self.ok("lesson", "reopen", lid, "--reason", "happened again", "--source", "M99", agent="zz-test-lead")
        r = self.lesson()
        self.assertEqual((r["status"], r["recurrences"], r["verified_at"], r["verified_by"], r["evidence"]),
                         ("open", 1, None, None, None))
        self.assertIn("M99", r["sources"])
        self.assertIn("recurrence: happened again", self.ok("lesson", "show", lid, agent="zz-test-lead").stdout)
        self.assertEqual(self.env.run("lesson", "reopen", lid, "--reason", "x", agent="zz-test-lead").returncode, 2)  # open
        for ag in ("zz-test-boss", "owner"):
            self.env.sql("UPDATE lessons SET status='applied'")
            self.ok("lesson", "verify", lid, "--evidence", "ok", agent=ag)

    def test_workers_refused(self):
        lid = self.new()
        for role in WORKERS:
            ag = f"zz-test-{role}"
            for args in (("link", lid, "--task", "T-0001"), ("apply", lid), ("verify", lid, "--evidence", "e"),
                         ("reopen", lid, "--reason", "r")):
                with self.subTest(role=role, action=args[0]):
                    self.assertEqual(self.env.run("lesson", *args, agent=ag).returncode, 3)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM lesson_links")[0]["c"], 0)
        self.assertEqual(self.lesson()["status"], "open")


class JudgeHookTests(FlowBase):
    def test_link_then_merge_applies(self):
        lid = self.new()
        self.ok("lesson", "link", lid, "--task", "T-0001", "--via", "hook", agent="zz-test-lead")
        p = self.merge()
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertRegex(p.stdout, r"^T-0001 MERGED @ \w{8}\n$")
        link = self.env.sql("SELECT * FROM lesson_links")[0]
        self.assertTrue(link["merged_at"])
        self.assertEqual(link["pr_url"], PR_URL)
        r = self.lesson()
        self.assertEqual(r["status"], "applied")
        self.assertTrue(r["applied_at"])
        self.assertIn("auto: T-0001", self.ok("lesson", "show", lid, agent="zz-test-lead").stdout)

    def test_merge_output_same_with_and_without_lesson(self):
        plain = self.merge()
        self.env.sql("UPDATE tasks SET status='in_review' WHERE id='T-0001'")
        self.env.sql("DELETE FROM verdicts")
        lid = self.new()
        self.ok("lesson", "link", lid, "--task", "T-0001", agent="zz-test-lead")
        withl = self.merge()
        self.assertEqual((plain.returncode, plain.stdout), (withl.returncode, withl.stdout))

    def test_unrelated_merge_does_not_apply_other_lesson(self):
        lid = self.new()
        self.env.seed_task("T-0002", status="done", pr_url=OLD_PR)
        self.env.sql("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,created) VALUES(?,?,?,?,?,?)",
                     ("T-0002", OLD_PR, "a" * 40, "judge", "merge", "2026-10-02T00:00:00+00:00"))
        self.ok("lesson", "link", lid, "--task", "T-0002", agent="zz-test-lead")
        self.assertTrue(self.env.sql("SELECT merged_at FROM lesson_links")[0]["merged_at"])
        self.assertEqual(self.merge().returncode, 0)
        self.assertEqual(self.lesson()["status"], "open")

    def test_half_linked_lesson_not_applied(self):
        lid = self.new()
        self.ok("lesson", "link", lid, "--task", "T-0001", agent="zz-test-lead")
        self.env.seed_task("T-0002", pr_url=OLD_PR)
        self.ok("lesson", "link", lid, "--task", "T-0002", agent="zz-test-lead")
        self.assertEqual(self.merge().returncode, 0)
        self.assertEqual(self.lesson()["status"], "open")

    def test_hook_failure_does_not_fail_merge(self):
        lid = self.new()
        self.ok("lesson", "link", lid, "--task", "T-0001", agent="zz-test-lead")
        self.env.set_gh(files_fail=True)
        p = self.merge()
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("T-0001 MERGED", p.stdout)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM verdicts WHERE verdict='merge'")[0]["c"], 1)
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"], "done")
        self.assertEqual(len(self.env.sql("SELECT * FROM events WHERE action='post-merge-hook-failed'")), 1)

    def merge_with_files(self, *files):
        self.env.seed_pr(checks=GREEN)
        st = json.loads(self.env.gh_state.read_text())
        st["prs"][PR_URL]["files"] = [{"path": f} for f in files]
        self.env.gh_state.write_text(json.dumps(st))
        p = self.merge()
        self.assertEqual(p.returncode, 0, p.stderr)

    def test_roles_changed_lead(self):
        self.merge_with_files("roles/lead.md", "tests/x.py")
        lead = [m for m in self.msgs() if m["recipient"] == "zz-test-lead"]
        self.assertEqual(len(lead), 1)
        self.assertEqual((lead[0]["kind"], lead[0]["sender"]), ("request", "factory"))
        self.assertIn("roles/lead.md", lead[0]["body"])
        self.assertIn("relaunched with --resume", lead[0]["body"])
        self.assertEqual([m for m in self.msgs() if m["recipient"] == "zz-test-boss"], [])
        self.assertEqual(len([m for m in self.msgs() if m["recipient"] == "zz-test-director"]), 1)
        d = self.msgs("role files changed: stale agents")
        self.assertEqual(len(d), 1)
        self.assertIn("factory relaunch zz-test-lead --when-idle", d[0]["body"])
        self.assertTrue(self.env.sql("SELECT * FROM events WHERE action='roles-changed'"))

    def test_roles_changed_director_notifies_boss(self):
        self.merge_with_files("roles/director.md")
        self.assertEqual(len([m for m in self.msgs() if m["recipient"] == "zz-test-director"]), 2)  # notice + stale list
        boss = self.msgs("role files changed: director stale")
        self.assertEqual(len(boss), 1)
        self.assertEqual(boss[0]["recipient"], "zz-test-boss")
        self.assertIn("factory relaunch zz-test-director --when-idle", boss[0]["body"])

    def test_common_md_hits_all_persistent(self):
        self.merge_with_files("roles/_common.md")
        names = {m["recipient"] for m in self.msgs() if m["subject"] == "role files changed: relaunch pending"}
        self.assertEqual(names, {"zz-test-boss", "zz-test-director", "zz-test-lead"})

    def test_tests_only_no_messages(self):
        self.merge_with_files("tests/test_x.py", "roles/coder.md")
        self.assertEqual(self.msgs(), [])
        self.assertEqual(self.env.sql("SELECT * FROM events WHERE action='roles-changed'"), [])


class CollapseTests(FlowBase):
    PR2 = "https://github.com/zz-test/zz-test/pull/2"

    def seed_files(self, url, files):
        st = json.loads(self.env.gh_state.read_text())
        st["prs"][url]["files"] = [{"path": f} for f in files]
        self.env.gh_state.write_text(json.dumps(st))

    def merge_with_files(self, *files):
        self.seed_files(PR_URL, files)
        p = self.merge()
        self.assertEqual(p.returncode, 0, p.stderr)

    def merge2(self, *files):
        """Merge a second task (T-0002, PR #2) changing `files`."""
        self.env.seed_task("T-0002", pr_url=self.PR2)
        self.env.seed_pr(url=self.PR2, checks=GREEN)
        self.seed_files(self.PR2, files)
        p = self.merge("T-0002")
        self.assertEqual(p.returncode, 0, p.stderr)

    def two_merges(self, *files):
        self.merge_with_files(*files)
        self.merge2(*files)

    def to(self, who, subject):
        return [m for m in self.msgs(subject) if m["recipient"] == f"zz-test-{who}"]

    def test_lead_notice_collapsed(self):
        self.two_merges("roles/lead.md")
        n = self.to("lead", "role files changed: relaunch pending")
        self.assertEqual(len(n), 1)
        self.assertIsNone(n[0]["read_at"])
        self.assertIn(PR_URL, n[0]["body"])
        self.assertIn(self.PR2, n[0]["body"])
        self.assertEqual(n[0]["body"].count("- roles/lead.md"), 1)
        self.assertEqual(n[0]["body"].count("Your running prompt is stale"), 1)
        self.assertEqual(len(self.env.sql("SELECT * FROM events WHERE action='roles-changed'")), 2)

    def test_read_notice_not_collapsed(self):
        self.merge_with_files("roles/lead.md")
        first = self.to("lead", "role files changed: relaunch pending")[0]
        self.env.sql("UPDATE messages SET read_at='2026-01-01T00:00:00+00:00' WHERE id=?", (first["id"],))
        self.merge2("roles/lead.md")
        n = self.to("lead", "role files changed: relaunch pending")
        self.assertEqual(len(n), 2)
        self.assertEqual(n[0]["body"], first["body"])
        self.assertNotIn(self.PR2, n[0]["body"])
        self.assertIn(self.PR2, n[1]["body"])

    def test_director_and_boss_collapsed(self):
        self.two_merges("roles/director.md", "roles/lead.md")
        d = self.to("director", "role files changed: stale agents")
        self.assertEqual(len(d), 1)
        self.assertEqual(d[0]["body"].count("factory relaunch zz-test-lead --when-idle"), 1)
        self.assertEqual(d[0]["body"].count("factory relaunch zz-test-director --when-idle"), 1)
        self.assertIn(self.PR2, d[0]["body"])
        b = self.to("boss", "role files changed: director stale")
        self.assertEqual(len(b), 1)
        self.assertIn(PR_URL, b[0]["body"])
        self.assertIn(self.PR2, b[0]["body"])

    def seed_notice(self, who, subject, body):
        self.env.sql("INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES('factory',?,'request',?,?,"
                     "'2026-01-01T00:00:00+00:00')", (f"zz-test-{who}", subject, body))

    def test_boss_notice_keeps_every_agents_relaunch_line(self):
        self.seed_notice("boss", "role files changed: director stale",
                         "PR: https://x/pull/9\nChanged files:\n- roles/lead.md\nLive agents:\n"
                         "- zz-test-lead (lead): factory relaunch zz-test-lead --when-idle\n"
                         "- zz-test-boss (boss): factory relaunch zz-test-boss --when-idle\nRun it.")
        self.merge_with_files("roles/director.md")
        b = self.to("boss", "role files changed: director stale")
        self.assertEqual(len(b), 1)
        for who in ("zz-test-lead", "zz-test-boss", "zz-test-director"):
            self.assertEqual(b[0]["body"].count(f"factory relaunch {who} --when-idle"), 1, b[0]["body"])
        self.assertIn("https://x/pull/9", b[0]["body"])

    def test_legacy_boss_line_not_duplicated(self):
        self.seed_notice("boss", "role files changed: director stale",
                         "T-0009 merged (https://x/pull/9) and changed role files:\n- roles/director.md\n"
                         "Live agents with a stale prompt:\n- zz-test-director (director): factory relaunch zz-test-director --when-idle\n"
                         "Run: factory relaunch director --when-idle")
        self.merge_with_files("roles/director.md")
        b = self.to("boss", "role files changed: director stale")
        self.assertEqual(b[0]["body"].count("factory relaunch zz-test-director --when-idle"), 1, b[0]["body"])

    def test_old_format_director_notice_parsed_on_first_collapse(self):
        old = (f"T-0009 merged (https://x/pull/9) and changed role files:\n- roles/lead.md\n"
               "Live agents with a stale prompt:\n"
               "- zz-test-lead (lead): factory relaunch zz-test-lead --when-idle\n"
               "- zz-test-boss (boss): factory relaunch zz-test-boss --when-idle")
        self.seed_notice("director", "role files changed: stale agents", old)
        self.merge_with_files("roles/director.md")
        d = self.to("director", "role files changed: stale agents")
        self.assertEqual(len(d), 1)
        for line in ("https://x/pull/9", PR_URL, "- roles/lead.md", "- roles/director.md",
                     "factory relaunch zz-test-lead --when-idle", "factory relaunch zz-test-boss --when-idle",
                     "factory relaunch zz-test-director --when-idle"):
            self.assertIn(line, d[0]["body"])

    def test_non_persistent_role_sends_nothing(self):
        self.two_merges("roles/coder.md")
        self.assertEqual(self.msgs(), [])

    def test_helper_failure_does_not_fail_merge(self):
        self.env.sql("CREATE TRIGGER zz_fail BEFORE INSERT ON messages BEGIN SELECT RAISE(ABORT, 'zz-fail'); END")
        self.seed_files(PR_URL, ["roles/lead.md"])
        p = self.merge()
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"], "done")
        self.assertEqual(len(self.env.sql("SELECT * FROM events WHERE action='post-merge-hook-failed'")), 1)


class DigestTests(FlowBase):
    def digest(self, *args):
        return self.ok("digest", *args, extra_env=NOW_ENV, agent="zz-test-director").stdout

    def test_no_lessons_no_section(self):
        self.assertNotIn("## Lessons", self.digest())

    def test_lessons_section(self):
        e = self.env
        for i, title in enumerate(("new one", "applied one", "verified one", "old one"), 1):
            self.new(title)
        e.sql("UPDATE lessons SET created=?", (iso(dt.timedelta(hours=-2)),))
        e.sql("UPDATE lessons SET created=? WHERE id='L-004'", (iso(dt.timedelta(days=-9)),))
        e.sql("UPDATE lessons SET status='applied', applied_at=? WHERE id='L-002'", (iso(dt.timedelta(hours=-1)),))
        e.sql("UPDATE lessons SET status='verified', verified_at=?, evidence='saw it\nwork' WHERE id='L-003'",
              (iso(dt.timedelta(hours=-1)),))
        e.sql("INSERT INTO lesson_links(lesson_id,pr_url,created) VALUES('L-002',?,'t')", (OLD_PR,))
        out = self.digest()
        sec = out.split("## Lessons")[1]
        new = sec.split("### New")[1].split("### Applied")[0]
        app = sec.split("### Applied")[1].split("### Verified")[0]
        ver = sec.split("### Verified")[1]
        self.assertIn("- L-001 [lead,director] new one (open)", new)
        self.assertNotIn("L-004", new)
        self.assertIn(f"- L-002 applied one — {OLD_PR}", app)
        self.assertIn("- L-003 verified one — saw it work", ver)
        self.assertIn("Open: 2 · applied, awaiting verification: 1", ver)
        self.assertNotIn("## Lessons", self.digest("--goal", "G-001"))


if __name__ == "__main__":
    unittest.main()
