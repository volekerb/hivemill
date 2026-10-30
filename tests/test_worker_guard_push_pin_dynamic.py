import unittest

import test_worker_guard_push_pin as pin

TASK, OWN = pin.TASK, pin.OWN


class DynamicSubcommandTests(unittest.TestCase):
    """T-0775: a coder's git subcommand must be a literal word; hiding `push` behind an expansion fails closed."""

    # reuse the PushPinTests fixture (temp home, DB row, real clone) without inheriting its test cases
    setUp = pin.PushPinTests.setUp
    write_db = pin.PushPinTests.write_db
    run_hook = pin.PushPinTests.run_hook

    def test_dynamic_subcommand_refused(self):
        for cmd in (
            "p=push; git $p origin main", 'p=push; git "$p" origin HEAD:main', "git ${p:-push} origin main",
            "git $(echo push) origin main", "git `echo push` origin main", 'x=(push origin main); git "${x[@]}"',
            "git -C . $p origin main", "p=push; git -C . $p origin main", 'f(){ git "$@"; }; f push origin main',
            "alias gp='git $p'; gp origin main", 'git "$1" origin main', "git $* origin main",
        ):
            with self.subTest(cmd=cmd):
                p = self.run_hook(cmd)
                self.assertEqual(p.returncode, 2, cmd)
                self.assertIn("hide the git subcommand", p.stderr)

    def test_literal_subcommands_allowed(self):
        for cmd in (
            f"git push origin {OWN}", "git status", "git log -1", f"git -C {self.wt} commit -m x",
            "git diff $(git merge-base origin/main HEAD)..HEAD", "git merge-tree --write-tree origin/main HEAD",
        ):
            with self.subTest(cmd=cmd):
                self.assertEqual(self.run_hook(cmd).returncode, 0, cmd)

    def test_other_roles_unchanged(self):
        for role in ("qa", "reviewer", "pm", "cleaner", "judge"):
            p = self.run_hook("p=push; git $p origin main", role=role, agent=f"{role}-{TASK}")
            self.assertNotIn("hide the git subcommand", p.stderr, role)

    def test_existing_blocks_unchanged(self):
        for cmd in ("eval git push origin main", "alias gp='git push'; gp origin main", "git push origin main"):
            with self.subTest(cmd=cmd):
                self.assertEqual(self.run_hook(cmd).returncode, 2, cmd)



if __name__ == "__main__":
    unittest.main()
