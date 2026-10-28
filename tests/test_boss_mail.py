import json
import os
import subprocess
import sys
import unittest

from harness import FactoryTestCase, REPO, rendered_settings

HOOK = REPO / "roles" / "hooks" / "boss-inbox.py"


class BossTickTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_agent("boss", "boss", pane_id="p-boss")
        e.seed_agent("zz-director", "director", pane_id="p-dir")
        e.seed_agent("zz-lead", "lead", pane_id="p-lead")
        e.set_herdr(agents=[{"name": n, "pane_id": p, "agent_status": "idle", "agent_session": {"value": "s-" + n}}
                            for n, p in (("boss", "p-boss"), ("zz-director", "p-dir"), ("zz-lead", "p-lead"))])

    def mail(self, to, n=1, kind="request"):
        for i in range(n):
            self.env.sql("INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES('x',?,?,?,?,'t')",
                         (to, kind, f"subj{i}", ""))

    def herdr_calls(self, *cmd):
        return [c["argv"] for c in self.env.calls("herdr") if tuple(c["argv"][:len(cmd)]) == cmd]

    def prompts_to(self, name):
        return [a for a in self.herdr_calls("agent", "prompt") if a[2] == name]

    def badge(self, pane):
        a = [a for a in self.herdr_calls("pane", "report-metadata") if a[2] == pane][-1]
        return a[a.index("--display-agent") + 1]

    def test_boss_with_mail_not_prompted_but_badged(self):
        self.mail("boss", 3)
        self.ok("tick")
        self.assertEqual(self.prompts_to("boss"), [])
        self.assertIn("📬3", self.badge("p-boss"))

    def test_boss_no_mail_plain_badge(self):
        self.ok("tick")
        self.assertEqual(self.prompts_to("boss"), [])
        self.assertNotIn("📬", self.badge("p-boss"))

    def test_director_cycle_never_targets_boss(self):
        self.mail("boss", 1)
        self.ok("tick")
        self.assertEqual(self.prompts_to("boss"), [])
        cycle = [a for a in self.herdr_calls("agent", "prompt") if "Director cycle" in a[3]]
        self.assertEqual([a[2] for a in cycle], ["zz-director"])

    def test_director_and_lead_still_nudged(self):
        self.mail("zz-director", 2)
        self.mail("zz-lead", 1)
        self.ok("tick")
        self.assertIn("📬 2 unread", self.prompts_to("zz-director")[0][3])
        self.assertIn("📬 1 unread", self.prompts_to("zz-lead")[0][3])
        self.assertNotIn("📬", self.badge("p-dir"))


class BossHookTests(FactoryTestCase):
    def hook(self, stdin="{}", env=None, path=None):
        e = self.env.env("boss")
        e.update(env or {})
        if path is not None:
            e["PATH"] = path
        return subprocess.run([sys.executable, str(HOOK)], input=stdin, capture_output=True, text=True, env=e, timeout=30)

    def seed(self, subject, body="", kind="report"):
        self.env.sql("INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES('x','boss',?,?,?,'t')",
                     (kind, subject, body))

    def factory_on_path(self):
        d = self.env.home / "fbin"
        d.mkdir(exist_ok=True)
        (d / "factory").write_text(f"#!/bin/sh\nexec {sys.executable} {REPO / 'bin' / 'factory'} \"$@\"\n")
        (d / "factory").chmod(0o755)
        return f"{d}{os.pathsep}{self.env.env()['PATH']}"

    def test_unread_injected_and_marked_read(self):
        self.seed("hello-owner-subject", "details here")
        p = self.hook(path=self.factory_on_path())
        self.assertEqual(p.returncode, 0, p.stderr)
        out = json.loads(p.stdout)["hookSpecificOutput"]
        self.assertEqual(out["hookEventName"], "UserPromptSubmit")
        self.assertIn("Factory inbox", out["additionalContext"])
        self.assertIn("hello-owner-subject", out["additionalContext"])
        self.assertEqual(self.env.sql("SELECT count(*) FROM messages WHERE read_at IS NULL")[0][0], 0)

    def test_empty_inbox_prints_nothing(self):
        p = self.hook(path=self.factory_on_path())
        self.assertEqual((p.returncode, p.stdout), (0, ""))

    def test_factory_failure_prints_nothing(self):
        p = self.hook(env={"FACTORY_HOME": str(self.env.home / "nope" / "x")}, path=self.factory_on_path())
        self.assertEqual((p.returncode, p.stdout), (0, ""))
        p = self.hook(path="/nonexistent")  # no factory on PATH
        self.assertEqual((p.returncode, p.stdout), (0, ""))

    def test_bad_or_empty_stdin(self):
        self.seed("s1")
        for stdin in ("", "{not json", "\x00\x01"):
            p = self.hook(stdin=stdin, path=self.factory_on_path())
            self.assertEqual(p.returncode, 0, repr(stdin))

    def test_truncation(self):
        self.seed("big", "y" * 20000, kind="request")  # reports this long are collapsed
        p = self.hook(path=self.factory_on_path())
        self.assertEqual(p.returncode, 0)
        ctx = json.loads(p.stdout)["hookSpecificOutput"]["additionalContext"]
        self.assertTrue(ctx.endswith("(truncated; run factory inbox --all)"))
        self.assertLess(len(ctx), 8100)


class BossSettingsTests(unittest.TestCase):
    def test_boss_json_hook_and_existing_entries(self):
        # asserts only what T-0093 owns; other PRs may edit the rest of boss.json
        cur = json.loads(rendered_settings("boss").read_text())
        self.assertEqual(cur["env"]["FACTORY_ROLE"], "boss")
        for key in ("allow", "deny"):
            self.assertTrue(cur["permissions"][key], key)
        cmds = [h["command"] for g in cur["hooks"]["UserPromptSubmit"] for h in g["hooks"]]
        self.assertEqual(len([c for c in cmds if c.endswith("roles/hooks/boss-inbox.py")]), 1)
        self.assertTrue(HOOK.exists())


if __name__ == "__main__":
    unittest.main()
