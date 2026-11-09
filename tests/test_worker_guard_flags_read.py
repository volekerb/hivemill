"""T-0806: `factory flags` prints the live config values (the "current" column, same data as `factory config`), so
workers get it blocked like BROAD_READS. HQ (--hq) is unchanged."""
import json
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from guard_impl import hook_run  # noqa: E402

WORKER_ROLES = ("coder", "qa", "reviewer", "pm", "judge", "cleaner", "scout", "researcher", "evaluator", None)
FORMS = ("factory flags", "factory flags --all", "factory flags --json", "factory flags --all --json",
         "factory --x flags", "cd /x && factory flags --all --json", "zsh -c 'factory flags --json'",
         "bash -c 'factory flags --all'", "python3 ~/factory/bin/factory flags --json", "factory flags | head")


class FlagsReadTests(unittest.TestCase):
    def setUp(self):
        td = tempfile.TemporaryDirectory()
        self.addCleanup(td.cleanup)
        self.home = td.name

    def run_hook(self, cmd, argv, home=None):
        env = {k: v for k, v in os.environ.items() if k not in ("FACTORY_AGENT", "FACTORY_HOME")}
        env["FACTORY_HOME"] = home or self.home
        env["FACTORY_AGENT"] = "coder-t-9"
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}})
        return hook_run(payload, argv, env=env)

    def test_workers_blocked(self):
        for role in WORKER_ROLES:
            for cmd in FORMS:
                with self.subTest(role=role, cmd=cmd):
                    p = self.run_hook(cmd, ["--role", role] if role else [])
                    self.assertEqual(p.returncode, 2, p.stderr)
                    # the evaluator already had its own, stricter refusal
                    self.assertIn("may not run `factory flags`" if role == "evaluator" else "broad factory reads", p.stderr)

    def test_temp_home_allowed(self):
        for cmd in ("factory flags --all --json", "factory flags"):
            with self.subTest(cmd=cmd):
                p = self.run_hook("FACTORY_HOME=/tmp/fh " + cmd, ["--role", "coder"])
                self.assertEqual(p.returncode, 0, p.stderr)

    def test_staff_engineer_unchanged(self):
        # staff-engineer is exempt from every broad read, as for `factory config`
        p = self.run_hook("factory flags --all --json", ["--role", "staff-engineer"])
        self.assertEqual(p.returncode, 0, p.stderr)

    def test_hq_unchanged(self):
        for role in ("boss", "director", "lead"):
            for cmd in ("factory flags", "factory flags --all --json"):
                with self.subTest(role=role, cmd=cmd):
                    p = self.run_hook(cmd, ["--hq", "--role", role])
                    self.assertEqual(p.returncode, 0, p.stderr)

    def test_flags_word_elsewhere_allowed(self):
        for cmd in ("echo factory flags", "factory task show T-1", "grep flags README.md"):
            with self.subTest(cmd=cmd):
                p = self.run_hook(cmd, ["--role", "coder"])
                self.assertEqual(p.returncode, 0, p.stderr)


if __name__ == "__main__":
    unittest.main()
