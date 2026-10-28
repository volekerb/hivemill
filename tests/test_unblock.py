"""G-006 A9 (L-021): the blocked alert goes to the worker's lead, `factory unblock` denies a permission dialog once
(Esc + nudge, the worker keeps running), and the prompt files carry the rule."""
import json
import subprocess
import sys
import time
import unittest
from pathlib import Path

from harness import FACTORY, FactoryTestCase

ROOT = Path(__file__).resolve().parent.parent
LONG_AGO = time.time() - 3600


class UnblockBase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team("g-001-a", lead="zz-lead")
        e.seed_team("g-001-b", lead="zz-lead-b")
        e.seed_task(status="in_progress", team="g-001-a")
        e.seed_agent("zz-boss", "boss", pane_id="p-boss")
        e.seed_agent("zz-director", "director", goal="G-001", pane_id="p-dir")
        e.seed_agent("zz-lead", "lead", goal="G-001", team="g-001-a", pane_id="p-lead")
        e.seed_agent("zz-lead-b", "lead", goal="G-001", team="g-001-b", pane_id="p-leadb")
        e.seed_agent("zz-coder", "coder", goal="G-001", team="g-001-a", task="T-0001", pane_id="p-c")
        e.seed_agent("zz-coder-b", "coder", goal="G-001", team="g-001-b", pane_id="p-cb")
        e.seed_agent("zz-staff", "staff-engineer", goal="G-001", pane_id="p-staff")
        self.status = {}

    def herdr_status(self, **status):
        """Set the herdr-reported agent_status per agent name (default idle)."""
        self.status.update(status)
        names = {"zz-boss": "p-boss", "zz-director": "p-dir", "zz-lead": "p-lead", "zz-lead-b": "p-leadb",
                 "zz-coder": "p-c", "zz-coder-b": "p-cb", "zz-staff": "p-staff"}
        self.env.set_herdr(
            workspaces=[{"workspace_id": "w1", "label": "w"}],
            panes=[{"pane_id": p, "tab_id": "t1", "workspace_id": "w1"} for p in names.values()],
            agents=[{"name": n, "agent": "claude", "pane_id": p, "agent_status": self.status.get(n, "idle")}
                    for n, p in names.items()])

    def block(self, *names, since=LONG_AGO):
        for n in names:
            self.env.sql("UPDATE agents SET blocked_since=? WHERE name=?", (since, n))

    def tick(self):
        return self.ok("tick")

    def alerts(self, subject):
        return self.env.sql("SELECT recipient, sender, kind, body, goal_id, task_id FROM messages "
                            "WHERE kind='alert' AND subject=?", (subject,))

    def row(self, name="zz-coder"):
        return self.env.sql("SELECT * FROM agents WHERE name=?", (name,))[0]

    def unblock(self, *args, agent="zz-lead", code=0):
        p = self.env.run("unblock", *args, agent=agent)
        self.assertEqual(p.returncode, code, f"unblock {args}: rc={p.returncode}\n{p.stdout}\n{p.stderr}")
        return p

    def counts(self):
        return (self.env.sql("SELECT count(*) c FROM messages")[0]["c"],
                self.env.sql("SELECT count(*) c FROM events")[0]["c"], len(self.env.calls("herdr")))


class BlockedAlertRouting(UnblockBase):
    def test_team_worker_alert_goes_to_lead_once(self):
        self.herdr_status(**{"zz-coder": "blocked"})
        self.block("zz-coder")
        self.tick()
        al = self.alerts("zz-coder blocked >10 min")
        self.assertEqual(len(al), 1)
        self.assertEqual((al[0]["recipient"], al[0]["sender"], al[0]["goal_id"], al[0]["task_id"]),
                         ("zz-lead", "tick", "G-001", "T-0001"))
        self.assertIn("L-021", al[0]["body"])
        self.assertIn('factory unblock zz-coder --reason "..."', al[0]["body"])
        self.assertIn("factory stop zz-coder", al[0]["body"])
        self.assertIn("blocked-worker", al[0]["body"])
        self.assertEqual(self.row()["blocked_alerted"], 1)
        self.tick()
        self.assertEqual(len(self.alerts("zz-coder blocked >10 min")), 1)

    def test_pane_tail_follows_the_hint(self):
        self.herdr_status(**{"zz-coder": "blocked"})
        self.block("zz-coder")
        self.tick()
        body = self.alerts("zz-coder blocked >10 min")[0]["body"]
        self.assertIn("Skill: blocked-worker.\n", body)
        self.assertGreater(len(body), body.index("Skill: blocked-worker.\n") + len("Skill: blocked-worker.\n"))

    def test_not_alerted_before_blocked_alert_s(self):
        self.herdr_status(**{"zz-coder": "blocked"})
        self.block("zz-coder", since=time.time() - 30)
        self.tick()
        self.assertEqual(self.alerts("zz-coder blocked >10 min"), [])

    def test_first_blocked_tick_only_records_blocked_since(self):
        self.herdr_status(**{"zz-coder": "blocked"})
        self.tick()
        self.assertIsNotNone(self.row()["blocked_since"])
        self.assertEqual(self.alerts("zz-coder blocked >10 min"), [])

    def test_teamless_worker_goes_to_director_without_lead_hint_lost(self):
        self.herdr_status(**{"zz-staff": "blocked"})
        self.block("zz-staff")
        self.tick()
        al = self.alerts("zz-staff blocked >10 min")
        self.assertEqual([a["recipient"] for a in al], ["zz-director"])
        self.assertIn("factory unblock zz-staff", al[0]["body"])

    def test_lead_goes_to_director_without_worker_hint(self):
        self.herdr_status(**{"zz-lead": "blocked"})
        self.block("zz-lead")
        self.tick()
        al = self.alerts("zz-lead blocked >10 min")
        self.assertEqual([a["recipient"] for a in al], ["zz-director"])
        self.assertNotIn("factory unblock", al[0]["body"])

    def test_director_goes_to_boss(self):
        self.herdr_status(**{"zz-director": "blocked"})
        self.block("zz-director")
        self.tick()
        self.assertEqual([a["recipient"] for a in self.alerts("zz-director blocked >10 min")], ["zz-boss"])

    def test_team_without_lead_falls_back_to_director(self):
        self.env.sql("UPDATE teams SET lead_agent=NULL WHERE id='g-001-a'")
        self.herdr_status(**{"zz-coder": "blocked", "zz-coder-b": "blocked"})
        self.block("zz-coder", "zz-coder-b")
        self.tick()  # must not abort: the other agent in the same tick is still processed
        self.assertEqual([a["recipient"] for a in self.alerts("zz-coder blocked >10 min")], ["zz-director"])
        self.assertEqual([a["recipient"] for a in self.alerts("zz-coder-b blocked >10 min")], ["zz-lead-b"])

    def test_dead_lead_falls_back_to_director(self):
        self.env.sql("UPDATE agents SET status='exited' WHERE name='zz-lead'")
        self.herdr_status(**{"zz-coder": "blocked"})
        self.block("zz-coder")
        self.tick()
        self.assertEqual([a["recipient"] for a in self.alerts("zz-coder blocked >10 min")], ["zz-director"])


class UnblockTests(UnblockBase):
    def setUp(self):
        super().setUp()
        self.herdr_status(**{"zz-coder": "blocked", "zz-coder-b": "blocked"})
        self.block("zz-coder", "zz-coder-b")

    def test_lead_unblocks_own_blocked_coder(self):
        p = self.unblock("zz-coder", "--reason", "L-021: read of /etc denied")
        self.assertIn("UNBLOCK order sent to zz-coder", p.stdout)
        m = self.env.sql("SELECT * FROM messages WHERE recipient='zz-coder' AND subject='UNBLOCK'")
        self.assertEqual(len(m), 1)
        self.assertEqual((m[0]["kind"], m[0]["sender"], m[0]["read_at"]), ("decision", "zz-lead", None))
        self.assertTrue(m[0]["body"].startswith("L-021: read of /etc denied\n"))
        self.assertIn("denied once (Esc) by zz-lead", m[0]["body"])
        self.assertIn("never pick an option that changes user settings (L-021)", m[0]["body"])
        ev = self.env.sql("SELECT detail FROM events WHERE action='unblock' AND entity_id='zz-coder'")
        self.assertEqual(json.loads(ev[0]["detail"]), {"by": "zz-lead", "reason": "L-021: read of /etc denied"})
        calls = [c["argv"] for c in self.env.calls("herdr")]
        self.assertEqual([c[:3] for c in calls], [["agent", "send-keys", "zz-coder"], ["agent", "wait", "zz-coder"],
                                                  ["agent", "prompt", "zz-coder"]])
        self.assertEqual(calls[2][3], "📬 UNBLOCK from zz-lead: run factory inbox now and continue your task.")
        self.assertFalse([c for c in calls if c[0] == "pane"], "no pane close")
        r = self.row()
        self.assertIsNone(r["stop_requested_at"])
        self.assertEqual(r["status"], "live")
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"], "in_progress")

    def test_exactly_one_keystroke_and_it_is_esc(self):
        self.unblock("zz-coder", "--reason", "x")
        keys = [c["argv"] for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "send-keys"]]
        self.assertEqual(keys, [["agent", "send-keys", "zz-coder", "esc"]])
        verbs = [tuple(c["argv"][:2]) for c in self.env.calls("herdr")]
        self.assertEqual(verbs, [("agent", "send-keys"), ("agent", "wait"), ("agent", "prompt")])

    def test_director_may_unblock_any_team(self):
        self.unblock("zz-coder-b", "--reason", "x", agent="zz-director")
        self.assertEqual(len(self.env.sql("SELECT 1 FROM messages WHERE recipient='zz-coder-b' AND subject='UNBLOCK'")), 1)

    def test_other_teams_worker_refused(self):
        p = self.unblock("zz-coder-b", "--reason", "x", code=3)
        self.assertIn("not in your team", p.stderr)
        self.assertEqual(self.env.calls("herdr"), [])

    def test_hq_targets_refused_with_l019(self):
        for target in ("zz-boss", "zz-director", "zz-lead-b"):
            p = self.unblock(target, "--reason", "x", code=3, agent="zz-director")
            self.assertIn("L-019", p.stderr)
        p = self.unblock("zz-lead", "--reason", "x", code=3, agent="zz-lead")
        self.assertIn("L-019", p.stderr)
        self.assertEqual(self.env.calls("herdr"), [])

    def test_worker_caller_refused(self):
        self.unblock("zz-coder", "--reason", "x", code=3, agent="zz-coder")
        self.assertEqual(self.env.calls("herdr"), [])

    def test_not_blocked_worker_exit_2_names_stop(self):
        self.env.sql("UPDATE agents SET blocked_since=NULL WHERE name='zz-coder'")
        p = self.unblock("zz-coder", "--reason", "x", code=2)
        self.assertIn("is not blocked; use factory stop for a busy worker", p.stderr)
        self.assertEqual(self.env.calls("herdr"), [])

    def test_not_live_worker_exit_2(self):
        self.env.sql("UPDATE agents SET status='exited' WHERE name='zz-coder'")
        self.unblock("zz-coder", "--reason", "x", code=2)

    def test_unknown_agent_exit_2(self):
        self.unblock("zz-nobody", "--reason", "x", code=2)

    def test_reason_required(self):
        p = self.env.run("unblock", "zz-coder", agent="zz-lead")
        self.assertEqual(p.returncode, 2)
        self.assertIn("--reason", p.stderr)

    def test_dry_run_changes_nothing(self):
        before = self.counts()
        p = self.unblock("zz-coder", "--reason", "x", "--dry-run")
        self.assertIn("DRY RUN", p.stdout)
        self.assertIn("send-keys zz-coder esc", p.stdout)
        self.assertEqual(self.counts(), before)
        self.assertEqual(self.env.calls("herdr"), [])

    def test_cli_help(self):
        top = self.env.run("--help").stdout
        self.assertIn("deny a blocked worker's permission dialog once (Esc + nudge); the worker keeps running",
                      " ".join(top.split()))
        h = self.env.run("unblock", "--help").stdout
        for word in ("agent", "--reason", "--dry-run"):
            self.assertIn(word, h)


class ClaudeAuditAlertText(UnblockBase):
    """T-0064 follow-ups on the claude-audit alert (same tick alert code)."""
    SESS = "sess-audit"

    def setUp(self):
        super().setUp()
        self.env.sql("UPDATE agents SET session_id=? WHERE name='zz-coder'", (self.SESS,))
        self.herdr_status()
        st = json.loads(self.env.herdr_state.read_text())
        for a in st["agents"]:
            if a["name"] == "zz-coder":
                a["agent_session"] = {"value": self.SESS}
        self.env.set_herdr(agents=st["agents"])

    def audit(self, argv):
        self.env.fake_claude(100, argv=argv)
        self.tick()
        ev = self.env.sql("SELECT detail FROM events WHERE action='claude-outside-spawn'")
        al = self.env.sql("SELECT body FROM messages WHERE kind='alert' AND subject LIKE 'claude %'")
        return ev, al

    def test_resume_only_alert_has_resume_specific_text(self):
        ev, al = self.audit(("claude", "--settings", "s.json", "--resume", self.SESS))
        self.assertEqual(len(al), 1)
        self.assertNotIn("Only factory spawn starts Claude.", al[0]["body"])
        self.assertIn("stale role prompt; fix with factory relaunch zz-coder --when-idle", al[0]["body"])

    def test_outside_spawn_alert_keeps_original_text(self):
        self.env.fake_claude(100, argv=("claude", "--session-id", self.SESS))
        self.env.fake_claude(101, ppid=100, argv=("bash",))
        self.env.fake_claude(102, ppid=101, argv=("claude", "-p", "x"))
        self.tick()
        al = self.env.sql("SELECT body FROM messages WHERE kind='alert' AND subject LIKE 'claude %'")
        self.assertIn("Only factory spawn starts Claude.", al[0]["body"])

    def test_long_argv_keeps_resume_pair_in_event_and_alert(self):
        argv = ("claude", *[f"--flag{i}" for i in range(12)], "--resume", self.SESS)
        ev, al = self.audit(argv)
        self.assertEqual(json.loads(ev[0]["detail"])["argv"][-2:], ["--resume", self.SESS])
        self.assertIn(f"--resume {self.SESS}", al[0]["body"])


class PromptRules(unittest.TestCase):
    def test_common_rule_under_standing_rules(self):
        text = (ROOT / "roles/_common.md").read_text()
        head, _, rules = text.partition("## Standing rules (lessons)")
        self.assertIn("pick No. (L-021, M1767)", rules)
        self.assertNotIn("pick No. (L-021, M1767)", head)

    def test_denied_stays_denied_in_every_form(self):
        text = (ROOT / "roles/_common.md").read_text()
        self.assertIn("systemd-run", text)
        self.assertIn("L-023", text)
        self.assertEqual(text.count("L-023"), 1)

    def test_pm_trim(self):
        text = (ROOT / "roles/pm.md").read_text()
        self.assertIn("### The audit does NOT redo\nPaper check of records only: follow the audit-scope skill.", text)
        self.assertNotIn("re-run QA's commands", text)

    def test_blocked_worker_skill(self):
        text = (ROOT / "roles/plugins/lead/factory-lead/skills/blocked-worker/SKILL.md").read_text()
        for word in ("factory unblock", "factory stop", "L-021", "M1767", "Use when"):
            self.assertIn(word, text)

    def test_worker_brief_has_l023_line(self):
        text = (ROOT / "roles/plugins/lead/factory-lead/skills/worker-brief/SKILL.md").read_text()
        self.assertIn("a denied command stays denied in every form; stop and report (L-023)", text)


if __name__ == "__main__":
    unittest.main()
