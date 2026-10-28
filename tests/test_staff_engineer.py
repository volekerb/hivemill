import json
import os
import shutil
import sys
import time
import unittest

from harness import FACTORY, REPO, FactoryTestCase, MODELS, _literal, rendered_settings_dir

SETTINGS = rendered_settings_dir()


def settings(role):
    return json.loads((SETTINGS / f"{role}.json").read_text())


class StaffEngineerTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(lead="zz-test-lead")
        e.seed_task("T-0001", role="coder", status="in_progress", team="g-001-a")
        for name, role in (("zz-test-coder", "coder"), ("zz-test-lead", "lead"), ("zz-test-director", "director"),
                           ("zz-test-boss", "boss")):
            e.seed_agent(name, role, goal="G-001", team="g-001-a", task="T-0001")
        e.seed_agent("staff-engineer", "staff-engineer", status="live")
        self.se = "staff-engineer"

    def se_agents(self):
        return self.env.sql("SELECT * FROM agents WHERE role='staff-engineer'")

    def clean_slate(self):
        self.env.sql("DELETE FROM agents WHERE role='staff-engineer'")

    def test_lead_spawns_with_model_and_settings(self):
        self.clean_slate()
        p = self.ok("spawn", "staff-engineer", "--name", "zz-test-se", agent="zz-test-lead")
        self.assertIn("SPAWNED zz-test-se", p.stdout)
        self.assertEqual(MODELS["staff-engineer"], "claude-opus-5-5")
        start = [c["argv"] for c in self.env.calls("herdr") if c["argv"][:3] == ["agent", "start", "zz-test-se"]][0]
        self.assertEqual(start[start.index("--model") + 1], "claude-opus-5-5")
        self.assertEqual(os.path.realpath(start[start.index("--settings") + 1]), str(self.env.home_dir / "run" / "settings" / "staff-engineer.json"))

    def test_coder_cannot_spawn(self):
        self.clean_slate()
        self.refused(3, "spawn", "staff-engineer", "--name", "zz-test-se", agent="zz-test-coder")
        self.assertEqual(self.se_agents(), [])

    def test_refused_commands(self):
        for args in (("verdict", "T-0001", "--pass", "--summary", "ok"), ("judge", "T-0001", "--merge", "--reason", "x"),
                     ("spawn", "coder", "--name", "zz-test-x"), ("config", "max_live_agents", "99"),
                     ("goal", "new", "--title", "zz-test"), ("plan", "G-001", "x"),
                     ("task", "add", "--goal", "G-001", "--title", "x", "--role", "coder", "--spec", "x")):
            with self.subTest(args=args):
                p = self.refused(3, *args, agent=self.se)
                self.assertIn("only the boss creates goals" if args[:2] == ("goal", "new") else "may not do this", p.stderr)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM goals")[0]["c"], 1)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM tasks")[0]["c"], 1)
        self.assertEqual(self.env.sql("SELECT value FROM config WHERE key='max_live_agents'")[0]["value"], "6")

    def test_settings_isolation(self):
        s = settings("staff-engineer")
        deny, env = s["permissions"]["deny"], s["env"]
        for d in ("Bash(gh pr merge:*)", "Bash(git push origin main:*)", "Bash(git push origin HEAD:main:*)",
                  "Bash(git push --force:*)", "Bash(git push -f:*)", "Bash(git push:*)", "Edit", "Write", "NotebookEdit",
                  "Bash(gh pr comment:*)", "Bash(gh pr create:*)", "Bash(gh pr edit:*)", "Bash(gh pr ready:*)",
                  "Bash(gh pr review:*)"):
            self.assertIn(d, deny)
        self.assertEqual(env["FACTORY_ROLE"], "staff-engineer")
        self.assertEqual(env["HERDR_SOCKET_PATH"], settings("reviewer")["env"]["HERDR_SOCKET_PATH"])
        norm = lambda h: json.dumps(h).replace("--role staff-engineer", "--role reviewer")  # noqa: E731
        self.assertEqual(norm(s["hooks"]), norm(settings("reviewer")["hooks"]))

    def test_role_doc_has_no_pr_comments(self):
        doc = (REPO / "roles" / "staff-engineer.md").read_text()
        self.assertNotIn("gh pr comment", doc)
        self.assertIn("factory msg", doc)
        self.assertIn("factory report", doc)

    def test_msg_without_agent_queues_one_spawn(self):
        self.clean_slate()
        self.ok("config", "max_live_agents", "0", agent="zz-test-director")  # close the gate
        p = self.ok("msg", "staff-engineer", "q", "ctx", "--kind", "request", agent="zz-test-lead")
        self.assertIn("→ staff-engineer", p.stdout)
        rows = self.se_agents()
        self.assertEqual([(r["name"], r["status"]) for r in rows], [("staff-engineer", "queued")])
        self.assertEqual(json.loads(rows[0]["spawn_args"])["name"], "staff-engineer")
        self.ok("msg", "staff-engineer", "q2", "ctx", "--kind", "request", agent="zz-test-lead")
        self.assertEqual(len(self.se_agents()), 1)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM events WHERE action='queued' AND entity_id='staff-engineer'")[0]["c"], 1)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM messages WHERE recipient='staff-engineer'")[0]["c"], 2)
        self.assertEqual([c for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]], [])

    def test_non_request_kinds_do_not_spawn(self):
        self.clean_slate()
        for kind in ("msg", "report", "decision", "alert", "escalation"):
            self.ok("msg", "staff-engineer", "ack", "thanks", "--kind", kind, agent="zz-test-lead")
        self.assertEqual(self.se_agents(), [])
        self.assertEqual(self.starts(), [])
        self.assertEqual(self.env.sql("SELECT count(*) c FROM messages WHERE recipient='staff-engineer'")[0]["c"], 5)

    def test_msg_with_open_gate_spawns_fixed_name(self):
        self.clean_slate()
        self.ok("msg", "staff-engineer", "q", "ctx", "--kind", "request", agent="zz-test-lead")
        self.assertEqual([(r["name"], r["status"]) for r in self.se_agents()], [("staff-engineer", "live")])

    def test_msg_to_live_agent_does_not_respawn(self):
        before = len(self.env.calls("herdr"))
        self.ok("msg", "staff-engineer", "q", "ctx", "--kind", "request", agent="zz-test-lead")
        self.assertEqual(len(self.se_agents()), 1)
        self.assertEqual([c for c in self.env.calls("herdr")[before:] if c["argv"][:2] == ["agent", "start"]], [])

    def test_msg_after_done_queues_again(self):
        self.ok("done", "answered", agent=self.se)
        self.assertEqual(self.se_agents()[0]["status"], "done")  # reaped like a worker, not persistent
        self.ok("config", "max_live_agents", "0", agent="zz-test-director")
        self.ok("msg", "staff-engineer", "q", "ctx", "--kind", "request", agent="zz-test-lead")
        self.assertEqual([(r["name"], r["status"]) for r in self.se_agents()], [("staff-engineer", "queued")])

    def closes(self, pane):
        return [c for c in self.env.calls("herdr") if c["argv"][:3] == ["pane", "close", pane]]

    def starts(self):
        return [c for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]]

    def test_msg_after_done_gate_open_closes_old_pane_once(self):
        self.clean_slate()
        self.env.seed_agent("staff-engineer", "staff-engineer", status="done", pane_id="p-old")
        self.ok("msg", "staff-engineer", "q", "ctx", "--kind", "request", agent="zz-test-lead")
        self.assertEqual(len(self.closes("p-old")), 1)
        rows = self.se_agents()
        self.assertEqual([(r["name"], r["status"]) for r in rows], [("staff-engineer", "live")])
        self.assertNotEqual(rows[0]["pane_id"], "p-old")

    def test_msg_after_done_gate_closed_closes_old_pane_once(self):
        self.clean_slate()
        self.env.seed_agent("staff-engineer", "staff-engineer", status="done", pane_id="p-old")
        self.ok("config", "max_live_agents", "0", agent="zz-test-director")
        self.ok("msg", "staff-engineer", "q", "ctx", "--kind", "request", agent="zz-test-lead")
        self.ok("msg", "staff-engineer", "q2", "ctx", "--kind", "request", agent="zz-test-lead")  # repeat: no 2nd claim
        self.assertEqual(len(self.closes("p-old")), 1)
        rows = self.se_agents()
        self.assertEqual([(r["name"], r["status"], r["pane_id"]) for r in rows], [("staff-engineer", "queued", None)])
        self.assertEqual(self.starts(), [])

    def test_repeat_msg_does_not_leave_two_panes(self):
        self.clean_slate()
        self.ok("msg", "staff-engineer", "q", "ctx", "--kind", "request", agent="zz-test-lead")
        self.ok("msg", "staff-engineer", "q2", "ctx", "--kind", "request", agent="zz-test-lead")
        self.assertEqual(len(self.se_agents()), 1)
        self.assertEqual(len(self.starts()), 1)

    def test_msg_stored_and_exit_0_when_spawn_fails(self):
        self.clean_slate()
        roles = self.env.home_dir / "roles"
        roles.unlink()
        shutil.copytree(REPO / "roles", roles)
        (roles / "staff-engineer.md").unlink()  # spawn cannot build the system prompt
        p = self.ok("msg", "staff-engineer", "q", "ctx", "--kind", "request", agent="zz-test-lead")
        self.assertIn("warning", p.stderr)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM messages WHERE recipient='staff-engineer' AND subject='q'")[0]["c"], 1)
        self.assertEqual([r["status"] for r in self.se_agents()], ["queued"])

    def test_worker_cannot_msg_staff_engineer(self):
        p = self.refused(3, "msg", "staff-engineer", "q", "ctx", agent="zz-test-coder")
        self.assertIn("ask your lead", p.stderr)
        self.clean_slate()
        self.refused(3, "msg", "staff-engineer", "q", "ctx", agent="zz-test-coder")
        self.assertEqual(self.se_agents(), [])
        self.assertEqual(self.env.sql("SELECT count(*) c FROM messages WHERE recipient='staff-engineer'")[0]["c"], 0)

    def test_msg_spawn_vs_tick_race_places_one_pane(self):
        self.clean_slate()
        # the tick runs while the msg's spawn is in flight (its first pane-tool call), after the claim committed
        hook = f"{sys.executable} {FACTORY} tick >/dev/null 2>&1"
        p = self.env.run("msg", "staff-engineer", "q", "ctx", "--kind", "request", agent="zz-test-lead",
                         extra_env={"FAKE_HERDR_HOOK": hook})
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(len(self.starts()), 1)
        self.assertEqual([(r["name"], r["status"]) for r in self.se_agents()], [("staff-engineer", "live")])

    def break_spawn(self):
        roles = self.env.home_dir / "roles"
        roles.unlink()
        shutil.copytree(REPO / "roles", roles)
        (roles / "staff-engineer.md").unlink()
        return roles

    def test_tick_retries_failed_msg_spawn_once(self):
        self.clean_slate()
        roles = self.break_spawn()
        self.ok("msg", "staff-engineer", "q", "ctx", "--kind", "request", agent="zz-test-lead")
        self.assertEqual([r["status"] for r in self.se_agents()], ["queued"])
        self.assertNotIn("claimed_at", json.loads(self.se_agents()[0]["spawn_args"]))  # claim released on failure
        shutil.copy(REPO / "roles" / "staff-engineer.md", roles / "staff-engineer.md")
        self.ok("tick")
        self.assertEqual([r["status"] for r in self.se_agents()], ["live"])
        self.assertEqual(len(self.starts()), 1)
        self.ok("tick")
        self.assertEqual(len(self.starts()), 1)
        self.assertEqual(len(self.se_agents()), 1)

    def test_tick_skips_fresh_claim_but_retries_stale_one(self):
        self.clean_slate()
        self.ok("config", "max_live_agents", "0", agent="zz-test-director")
        self.ok("msg", "staff-engineer", "q", "ctx", "--kind", "request", agent="zz-test-lead")
        self.ok("config", "max_live_agents", "6", agent="zz-test-director")

        def claim(at):
            args = json.loads(self.se_agents()[0]["spawn_args"])
            args["claimed_at"] = at
            self.env.sql("UPDATE agents SET spawn_args=? WHERE name='staff-engineer'", (json.dumps(args),))
        claim(time.time())
        self.ok("tick")
        self.assertEqual([r["status"] for r in self.se_agents()], ["queued"])
        claim(time.time() - 3600)  # the claiming msg crashed long ago
        self.ok("tick")
        self.assertEqual([r["status"] for r in self.se_agents()], ["live"])
        self.assertEqual(len(self.starts()), 1)

    def test_direct_spawn_reuse_closes_old_pane_once(self):
        self.clean_slate()
        self.env.seed_agent("staff-engineer", "staff-engineer", status="done", pane_id="p-old")
        self.ok("spawn", "staff-engineer", "--name", "staff-engineer", agent="zz-test-lead")
        self.assertEqual(len(self.closes("p-old")), 1)
        rows = self.se_agents()
        self.assertEqual([(r["name"], r["status"]) for r in rows], [("staff-engineer", "live")])
        self.assertNotEqual(rows[0]["pane_id"], "p-old")
        self.assertEqual(len(self.starts()), 1)

    def test_common_md_workers_go_through_lead(self):
        src = (REPO / "bin" / "factory").read_text()
        callers = [l for l in src.splitlines() if l.startswith("ON_DEMAND_CALLERS")][0]
        for worker in ("pm", "coder", "qa", "reviewer", "judge", "cleaner"):
            self.assertNotIn(f'"{worker}"', callers)
        lines = [l for l in (REPO / "roles" / "_common.md").read_text().splitlines() if "factory msg staff-engineer" in l]
        self.assertTrue(lines)
        for line in lines:
            self.assertIn("Only lead, director and boss", line)
            self.assertIn("Workers ask their lead", line)

    def test_not_persistent(self):
        src = (REPO / "bin" / "factory").read_text()
        line = [l for l in src.splitlines() if l.startswith("PERSISTENT_ROLES")][0]
        self.assertNotIn("staff-engineer", line)

    def test_claim_ttl_outlasts_real_spawn_window(self):
        """Worst-case layout (the goal's workspace is missing): count the herdr calls a real msg-triggered spawn makes
        before the agent starts. A new call in gate()/place() must grow the constant and the TTL with it."""
        timeout, limit, ttl = (_literal(n) for n in ("HERDR_TIMEOUT_S", "CLAIM_WINDOW_MAX_HERDR_CALLS", "CLAIM_TTL_S"))
        self.clean_slate()
        self.env.set_herdr(workspaces=[], tabs=[], panes=[], agents=[])
        before = len(self.env.calls("herdr"))
        self.ok("msg", "staff-engineer", "q", "ctx", "--kind", "request", "--goal", "G-001", agent="zz-test-lead")
        window = []
        for c in self.env.calls("herdr")[before:]:
            if c["argv"][:2] == ["agent", "start"]:
                break
            window.append(c["argv"][:2])
        self.assertEqual(len(self.starts()), 1)
        self.assertIn(["workspace", "create"], window)  # really the worst case: place() built the workspace
        self.assertLessEqual(len(window), limit, window)
        self.assertGreaterEqual(ttl, limit * timeout + 30)
        self.assertGreaterEqual(ttl, len(window) * timeout + 30)
        src = (REPO / "bin" / "factory").read_text()
        self.assertIn("timeout=HERDR_TIMEOUT_S", src)  # herdr()'s default is the constant the bound is built on

    def test_role_file_length_rule(self):
        text = (REPO / "roles" / "staff-engineer.md").read_text()
        self.assertIn("Respect the requested length/format; put extra findings in a factory report, not the reply", text)
        self.assertLessEqual(len(text.splitlines()), 35)


if __name__ == "__main__":
    unittest.main()
