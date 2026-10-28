import json
import os
import subprocess
import sys
import unittest

from harness import rendered_settings

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
HOOK = os.path.join(ROOT, "roles", "hooks", "worker-guard.py")
SKILL = os.path.join(ROOT, "roles", "plugins", "coder", "coder-skills", "skills", "ci-wait", "SKILL.md")


def hook(command, background):
    ti = {"command": command}
    if background:
        ti["run_in_background"] = True
    payload = json.dumps({"tool_name": "Bash", "tool_input": ti})
    return subprocess.run([sys.executable, HOOK, "--role", "coder"], input=payload,
                          capture_output=True, text=True, timeout=30)


class CiWaitGuardTests(unittest.TestCase):
    def test_background_watch_allowed_for_coder(self):
        p = hook("gh pr checks 1 --watch --fail-fast", True)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(hook("pkill claude", True).returncode, 2)  # control: the hook is active

    def test_merge_still_denied_by_coder_settings(self):
        # gh pr merge is a settings deny rule (not a hook rule): the settings, not the hook, keep it blocked
        with open(rendered_settings("coder")) as f:
            perms = json.load(f)["permissions"]
        self.assertIn("Bash(gh pr merge:*)", perms["deny"])
        self.assertNotIn("Bash(gh pr merge:*)", perms.get("allow", []))
        self.assertNotIn("Bash(gh pr checks:*)", perms["deny"])

    def test_skill_has_exact_call_and_rules(self):
        with open(SKILL) as f:
            text = f.read()
        for needle in ("run_in_background: true", "gh pr checks <url> --watch --fail-fast",
                       "Do not poll", "sleep", "Never more than one watch per PR", "Kill only your own"):
            self.assertIn(needle, text)


if __name__ == "__main__":
    unittest.main()
