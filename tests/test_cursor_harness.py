"""Tests for scout/cursor_harness.py policy generation and parse_pilot_roles (not in test_cursor_pilot.py)."""
import importlib
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from harness import REPO

HOOKS = REPO / "roles" / "hooks"


def load_harness():
    return importlib.import_module("scout.cursor_harness")


class ParsePilotRolesTests(unittest.TestCase):
    def test_coder_and_qa_allowed(self):
        h = load_harness()
        self.assertEqual(h.parse_pilot_roles("coder"), ["coder"])
        self.assertEqual(h.parse_pilot_roles("qa"), ["qa"])

    def test_evaluator_rejected(self):
        h = load_harness()
        with self.assertRaises(ValueError):
            h.parse_pilot_roles("evaluator")

    def test_unknown_role_rejected(self):
        h = load_harness()
        with self.assertRaises(ValueError):
            h.parse_pilot_roles("not-a-role")

    def test_pilot_roles_equals_cursor_roles_without_evaluator(self):
        h = load_harness()
        self.assertEqual(h.PILOT_ROLES, h.CURSOR_ROLES)
        self.assertNotIn("evaluator", h.PILOT_ROLES)


class HooksJsonShapeTests(unittest.TestCase):
    def test_materialized_hooks_fail_closed_and_pretooluse_matcher(self):
        h = load_harness()
        settings = {"permissions": {"deny": ["Bash(sudo:*)"], "allow": []}}
        with tempfile.TemporaryDirectory(prefix="zz-cursor-hooks-") as tmp:
            h.materialize_cursor_policy(
                tmp,
                "coder",
                hooks_dir=HOOKS,
                claude_settings=settings,
                system_prompt="# coder\n",
            )
            hooks = json.loads((Path(tmp) / ".cursor" / "hooks.json").read_text())
            for event, entries in hooks["hooks"].items():
                for entry in entries:
                    self.assertTrue(
                        entry.get("failClosed"),
                        f"{event} hook entry must set failClosed true: {entry!r}",
                    )
            matcher = hooks["hooks"]["preToolUse"][0].get("matcher")
            self.assertEqual(matcher, "Shell|Write|Delete|Read|Grep|StrReplace|EditNotebook")


class TrailerEnvTests(unittest.TestCase):
    """T-1624: harness env for cursor workers has no CURSOR_AGENT."""

    def test_scrub_drops_cursor_agent_keeps_rest(self):
        h = load_harness()
        out = h.scrub_worker_env({"CURSOR_AGENT": "1", "FACTORY_ROLE": "coder", "PATH": "/bin"})
        self.assertNotIn("CURSOR_AGENT", out)
        self.assertEqual(out, {"FACTORY_ROLE": "coder", "PATH": "/bin"})

    def test_unset_argv_wraps_agent(self):
        h = load_harness()
        self.assertEqual(h.unset_trailer_env_argv(["agent", "--print"]),
                         ["env", "-u", "CURSOR_AGENT", "agent", "--print"])

    def test_commit_has_no_cursor_trailer_when_env_scrubbed(self):
        import subprocess
        h = load_harness()
        trailer = "Co-authored-by: Cursor <cursoragent" + "@" + "cursor.com>"
        with tempfile.TemporaryDirectory() as td:
            gitenv = {"PATH": os.environ["PATH"], "HOME": td, "CURSOR_AGENT": "1",
                      "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t" + "@" + "example.test",
                      "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t" + "@" + "example.test"}
            # fake git wrapper: appends the Cursor trailer exactly when CURSOR_AGENT is set, like Cursor's shell does
            bindir = Path(td) / "bin"
            bindir.mkdir()
            real_git = subprocess.run(["which", "git"], capture_output=True, text=True, check=True).stdout.strip()
            wrapper = bindir / "git"
            wrapper.write_text(
                "#!/bin/sh\nif [ \"$1\" = commit ] && [ -n \"${CURSOR_AGENT:-}\" ]; then\n"
                f"  exec {real_git} \"$@\" -m '' -m '{trailer}'\nfi\n"
                f"exec {real_git} \"$@\"\n")
            wrapper.chmod(0o755)
            repo = Path(td) / "r"
            repo.mkdir()
            for env, expect in ((gitenv, True), (h.scrub_worker_env(gitenv), False)):
                env = {**env, "PATH": f"{bindir}{os.pathsep}{env['PATH']}"}
                subprocess.run(["git", "init", "-q"], cwd=repo, env=env, check=True)
                (repo / "f").write_text(str(expect))
                subprocess.run(["git", "add", "f"], cwd=repo, env=env, check=True)
                subprocess.run(["git", "commit", "-q", "-m", "msg"], cwd=repo, env=env, check=True)
                body = subprocess.run(["git", "log", "-1", "--format=%B"], cwd=repo, env=env,
                                      capture_output=True, text=True, check=True).stdout
                self.assertEqual(trailer in body, expect)


if __name__ == "__main__":
    unittest.main()
