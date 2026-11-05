"""T-1064: real `git config` reads that #411 refused stay allowed, through the exact table only. Every case runs the hook
as invoked (hook_run); no git runs here (tests/test_worker_guard_config_oracle.py runs real git against the corpus)."""
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import test_worker_guard_config_oracle as oracle  # noqa: E402

GUARD_IMPL_PYTHON_ONLY = "Go mirror follow-up in G-016"
ALL_ROLES = ("coder", "reviewer", "qa", "pm", "judge", "cleaner")
EVERY_ROLE_READS = ["git config --get alias.x --show-origin", "git config --get alias.x --null",
                    "git config --get alias.x --local", "git config --get alias.x --bool", "git config --get alias.x --int",
                    "git config --get alias.x --path", "git config --get alias.x --includes",
                    "git config --get alias.x --global", "git config --get alias.x --system"]
WIDE_ROLE_READS = ["git config list --show-origin", "git config list --name-only", "git config get --all alias.x",
                   "git config get --show-origin alias.x", "git config get --regexp alias", "git config --get-color color.x",
                   "git config --get-colorbool color.ui"]
WIDE_ROLES = ("coder", "judge", "cleaner")
REFUSED = ["git config alias.x push --get", "git config alias.x push --null", "git config alias.x push --bool",
           "git config alias.x --bool push", "git config --regexp alias", "git config --regexp alias.x push",
           "git config set alias.x push --local", "git config --get alias.x --unset", "git config --get alias.x --add",
           "git config --get-color color.x --unset", "git config --get-colorbool color.ui --edit",
           "git config --get alias.x --show-origi", "git config --get alias.x -- --null",
           "git config --get-color color.x a b", "git config list --show-origin x", "git config --get alias.x --null --replace-all"]


class ConfigReadsTests(oracle.ConfigOracleTests):
    def test_every_role_reads(self):
        for role in ALL_ROLES:
            for cmd in EVERY_ROLE_READS:
                with self.subTest(role=role, cmd=cmd):
                    p = self.hook(cmd, role)
                    self.assertEqual(p.returncode, 0, p.stderr)

    def test_wide_roles_read(self):
        for role in WIDE_ROLES:
            for cmd in WIDE_ROLE_READS:
                with self.subTest(role=role, cmd=cmd):
                    p = self.hook(cmd, role)
                    self.assertEqual(p.returncode, 0, p.stderr)

    def test_narrow_roles_keep_refusing_wide_forms(self):
        for role in ("reviewer", "qa", "pm"):
            for cmd in WIDE_ROLE_READS:
                with self.subTest(role=role, cmd=cmd):
                    self.assertEqual(self.hook(cmd, role).returncode, 2)

    def test_near_misses_refused(self):
        for role in ALL_ROLES:
            for cmd in REFUSED:
                with self.subTest(role=role, cmd=cmd):
                    self.assertEqual(self.hook(cmd, role).returncode, 2)

    def test_xargs_trailing_modifier_read(self):
        p = self.hook("echo alias.x | xargs git config --get --show-origin")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(self.hook("echo alias.x push | xargs git config --get-color --unset").returncode, 2)

    def test_coder_type_and_value_forms(self):
        # M23762: both are in the oracle CORPUS; the PR body records the measured git result. The coder keeps both allowed
        for cmd in ("git config -t bool alias.x", "git config --value x alias.y"):
            with self.subTest(cmd=cmd):
                p = self.hook(cmd)
                self.assertEqual(p.returncode, 0, p.stderr)


# the inherited oracle tests run once, in their own module
for _n in [n for n in dir(oracle.ConfigOracleTests) if n.startswith("test_") and n not in vars(ConfigReadsTests)]:
    setattr(ConfigReadsTests, _n, None)

if __name__ == "__main__":
    unittest.main()
