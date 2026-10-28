"""T-0102 part D: HQ mode refuses mutating systemctl and systemd-run (M2108); worker mode refuses systemd-run."""
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from test_priv_guard import Base  # noqa: E402

VERBS = ["start", "stop", "restart", "reload", "try-restart", "reload-or-restart", "enable", "disable", "reenable",
         "daemon-reload", "daemon-reexec", "edit", "mask", "unmask", "set-property", "revert", "link", "preset",
         "kill", "reset-failed", "isolate", "set-environment", "unset-environment", "import-environment"]
READ_ONLY = ["status", "is-active", "is-enabled", "is-failed", "list-units", "list-unit-files", "list-timers", "cat",
             "show", "help"]
MSG = "mutating systemctl/systemd-run is not allowed for factory agents; the owner manages units (M2108)"


class HqUnitTests(Base):
    def test_every_mutating_verb_blocked_with_and_without_user(self):
        self.assertEqual(len(VERBS), 24)
        for verb in VERBS:
            for flag in ("--user ", ""):
                cmd = f"systemctl {flag}{verb} factory-serve"
                with self.subTest(cmd=cmd):
                    self.assertIn(MSG, self.blocked(cmd, mode="hq").stderr)

    def test_systemd_run_blocked_in_hq(self):
        for cmd in ("systemd-run --user sleep 1", "systemd-run x", "/usr/bin/systemd-run --scope true"):
            with self.subTest(cmd=cmd):
                self.assertIn(MSG, self.blocked(cmd, mode="hq").stderr)

    def test_forms_env_wrappers_shells_substitutions(self):
        forms = [
            "env A=1 systemctl --user restart factory-tick.timer", "A=1 systemctl --user stop x",
            "timeout 5 systemctl --user start x", "nohup systemctl restart x", "xargs systemctl restart",
            "bash -c 'systemctl --user restart x'", "sh -c \"bash -c 'systemctl stop x'\"",
            "echo $(systemctl --user restart x)", "echo `systemctl --user start factory-serve`",
            "echo hi; systemctl --user start factory-serve", "true && systemctl --user restart x",
            "(systemctl --user restart x)", "{ systemctl --user stop x; }", "systemctl --user -- start x",
            "find . -exec systemctl restart x \\;", "if true; then systemctl --user start x; fi",
            "systemctl --user start factory-serve | cat", "env systemd-run --user true",
            "VAR=`systemctl --user start factory-serve`",
            "git commit -m \"$(systemctl --user start factory-serve)\"",
        ]
        for cmd in forms:
            with self.subTest(cmd=cmd):
                self.blocked(cmd, mode="hq")

    def test_unquoted_backticks_in_data_command(self):
        # the G-008 accident: backticks in an unquoted argument run the command
        self.blocked("factory msg lead x `systemctl --user start factory-serve`", mode="hq")
        self.blocked("echo done `systemctl --user restart factory-tick.timer`", mode="hq")

    def test_read_only_and_text_allowed(self):
        for verb in READ_ONLY:
            for flag in ("--user ", ""):
                with self.subTest(verb=verb, user=flag):
                    self.allowed(f"systemctl {flag}{verb} factory-tick.timer", mode="hq")
        for cmd in ("systemctl --user", "systemctl --user list-timers", "systemctl --user status factory-tick.timer",
                    "systemctl --user list-timers --all | grep factory",
                    'grep "systemctl --user start" docs/x.md', "echo 'systemd-run is blocked'",
                    "git commit -m \"no systemctl restart\"", "cat docs/systemd-run.md",
                    "factory report - <<'EOF'\nthe owner ran `systemctl --user restart factory-serve` (ok)\nEOF",
                    "gh pr create --body-file - <<'EOF'\nsystemd-run (never) and systemctl start\nEOF"):
            with self.subTest(cmd=cmd):
                self.allowed(cmd, mode="hq")

    def test_worker_mode_systemd_run_blocked(self):
        for cmd in ("systemd-run --user sleep 1", "env systemd-run x", "bash -c 'systemd-run x'",
                    "echo `systemd-run --user x`"):
            with self.subTest(cmd=cmd):
                self.assertIn(MSG, self.blocked(cmd, mode="worker").stderr)
        self.allowed("grep systemd-run docs/x.md", mode="worker")

    def test_worker_mode_keeps_factory_unit_rule_only(self):
        self.blocked("systemctl --user restart factory-tick.timer", mode="worker")
        self.blocked("systemctl --user restart herdr-server", mode="worker")
        self.allowed("systemctl --user restart other.service", mode="worker")
        self.allowed("systemctl --user status factory-tick.timer", mode="worker")


if __name__ == "__main__":
    unittest.main()
