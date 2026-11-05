"""T-1097: in `git config <value option> --get al*` the action flag is the VALUE of the option (git writes: key al..., value
the next match), so the T-1063 glob check must not treat it as a read action. Refused for coder, judge and cleaner (exit 0
on origin/main); reviewer, qa and pm already refuse (unchanged). Literal reads stay allowed. Hook probes run with cwd in a
LINKED worktree built here."""
import json
import os
import sqlite3
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from guard_impl import hook_run  # noqa: E402
from harness import git  # noqa: E402

GUARD_IMPL_PYTHON_ONLY = "Go mirror follow-up in G-016"
TASK = "t-1097"
WRITERS = ("coder", "judge", "cleaner")
OTHERS = ("reviewer", "qa", "pm")
TEXT = "is built from an expansion, so it could be a write; write the arguments literally (T-1063)"

CH = "git config --comment --get"
# origin/main exit 0 for the writers (measured, task note), exit 2 on this head. Acceptance 2a: value option before the
# action flag.
VALUE_OPT = [
    "git config --comment --get al*", "git config --file --get al*", "git config -f --get al*", "git config --type --get al*",
    "git config --default --get al*", "git config --blob --get al*", "git config --value --get al*",
    "git config -t --get al*", "git config --comment --get a[l]*", "git config --comment --get a?ias.x",
    "git config -c a=b --comment --get al*", "git config --comment --list al*", "git config --comment --get-all al*",
    "git config --fil --get al*",
]
# 2b: extended glob (^ ~ #), with the option set on the same line or not
EXTENDED = [
    f"setopt extendedglob; {CH} ^z", f"setopt extendedglob; {CH} a~b", f"setopt extendedglob; {CH} al#",
    f"setopt extended_glob; {CH} ^z", f"setopt EXTENDEDGLOB; {CH} ^z", f"set -o extendedglob; {CH} ^z",
    f"emulate zsh -o extendedglob; {CH} ^z", f"emulate -L zsh -o extended_glob; {CH} ^z",
    f"setopt extendedglob && {CH} ^z", f"zsh -o extendedglob -c '{CH} ^z'", f"eval 'setopt extendedglob; {CH} ^z'",
    f"{CH} ^z", f"{CH} a~b", f"{CH} x#",
]
# 2c: zsh glob at any depth (quoting, eval, wrappers, comment or heredoc apostrophes)
ALT = "(alias.x|y)"
DEPTH = [
    f"eval '{CH} {ALT}'", f'eval "{CH} {ALT}"', f"eval git config --comment --get '{ALT}'", f"eval '{CH} <->'",
    f"builtin eval '{CH} {ALT}'", f"command eval '{CH} {ALT}'", f"zsh -c '{CH} {ALT}'",
    f"# it's\n{CH} {ALT}", f"{CH} {ALT}\n# it's", f"cat <<EOF\nit's\nEOF\n{CH} {ALT}",
]
# 2d: no literal key
NO_KEY = [
    "git config (alias.x|y)", "git config <->", "git config ^z", "git config a~b", "git config al#",
    "git config --comment c (alias.x|y)", "git config --comment c ^z", "git config --type bool (alias.x|y)",
    "git config --file (.git/config|alias.x) y", "git config --file --get (alias.x|y)", f"{CH} (alias.x|y)",
    f"{CH} <->", f"{CH} <1-2>", "git config --type --get (alias.x|y)", "git config -f --get (a|b)",
]
# 2e: changed from attempt 2
NOW_REFUSED = [f"{CH} 'x y'", f"{CH} '(a|b)'", "git config"]
OLDER = ["git config --com --get al*", "git config --unknown-opt --get al*"]
READS = [
    "git config --get alias.x", "git config --get al*", "git config --comment c --get alias.x",
    "git config --file f --get a.b", "git config --get (a|b)", "git config --get <->",
    "git config --comment c --get (alias.x|y)", "git config --comment '(a|b)' --get alias.x",
    "git config --get-regexp ^alias", "git config --list", "git config -l", "git config --global --list",
    "git config get user.name", "git config list", "git config user.name", "git config core.bare < /dev/null",
    "git config user.name 2>/dev/null", "git config --default -1 --get a.b", "git config --type=bool core.bare",
    "git config --file=f a.b", "git config --comment c --get al*", "git config --file=f --get al*",
    "git config --type bool --get core.bare",
    # blocker 3: a parenthesis elsewhere in the script is not the git config command's own argument
    "x=$(git config user.name)", "echo $(git config user.name)", "(git config user.name)",
    "git config user.name; (ls)", "git config user.name && echo $(date)", "f() { ls; }; git config user.name",
    "for i in $(ls); do git config user.name; done",
]
TEXT_SWALLOW = "follows a value option"
TEXT_NOKEY = "no literal key is given"


class ConfigValueGlobTests(unittest.TestCase):
    def setUp(self):
        td = tempfile.TemporaryDirectory()
        self.addCleanup(td.cleanup)
        self.tmp = os.path.realpath(td.name)
        self.home = os.path.join(self.tmp, "factory")
        for d in (os.path.join(self.home, "roles"), os.path.join(self.home, "db")):
            os.makedirs(d)
        self.main = os.path.join(self.home, "main-checkout")
        self.wt = os.path.join(self.home, "worktrees", "G-010-T-1097")
        os.makedirs(self.main)
        os.makedirs(os.path.dirname(self.wt))
        git("init", "-q", "-b", "main", self.main, cwd=self.tmp)
        git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "i", cwd=self.main)
        git("worktree", "add", "-q", "-b", "factory/t-1097", self.wt, cwd=self.main)
        con = sqlite3.connect(os.path.join(self.home, "db", "factory.db"))
        con.execute("CREATE TABLE tasks (id TEXT PRIMARY KEY, worktree TEXT)")
        con.execute("INSERT INTO tasks VALUES (?, ?)", (TASK.upper(), self.wt))
        con.commit()
        con.close()

    def run_hook(self, cmd, role):
        env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "FACTORY_AGENT")}
        env.update(HOME=self.tmp, WORKER_GUARD_LIVE_HOME=self.home, FACTORY_HOME=self.home,
                   FACTORY_AGENT=f"coder-{TASK}" if role == "coder" else f"{role}-x")
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": self.wt})
        return hook_run(payload, ["--role", role], env=env)

    def check(self, cmds, code, roles, text=None):
        for role in roles:
            for c in cmds:
                with self.subTest(role=role, cmd=c):
                    p = self.run_hook(c, role)
                    self.assertEqual(p.returncode, code, p.stderr)
                    if text:
                        self.assertIn(text, p.stderr)

    def test_linked_worktree_fixture(self):
        self.assertTrue(os.path.isfile(os.path.join(self.wt, ".git")), "cwd must be a linked worktree")

    def test_value_option_then_action_flag_refused_for_writers(self):
        self.check(VALUE_OPT, 2, WRITERS)

    def test_swallowed_action_flag_reason(self):
        self.check([f"{CH} (alias.x|y)", "git config --file --get x.y"], 2, WRITERS, text=TEXT_SWALLOW)

    def test_extended_glob_refused_for_writers(self):
        self.check(EXTENDED, 2, WRITERS)

    def test_glob_at_any_depth_refused_for_writers(self):
        self.check([c.replace("\\n", "\n") for c in DEPTH], 2, WRITERS)

    def test_no_literal_key_refused_for_writers(self):
        self.check(NO_KEY, 2, WRITERS)

    def test_no_key_reason(self):
        self.check(["git config (alias.x|y)", "git config ^z"], 2, WRITERS, text=TEXT_NOKEY)

    def test_changed_from_attempt_2_refused(self):
        self.check(NOW_REFUSED, 2, WRITERS)

    def test_abbreviation_and_unknown_option_stay_refused(self):
        self.check(OLDER, 2, WRITERS + OTHERS)

    def test_other_roles_exit_as_on_main(self):
        self.check(VALUE_OPT + EXTENDED + NO_KEY + NOW_REFUSED, 2, OTHERS)

    def test_literal_reads_allowed_for_writers(self):
        self.check(READS, 0, WRITERS)

    def test_literal_reads_allowed_for_other_roles(self):
        self.check(READS[:9], 0, OTHERS)


if __name__ == "__main__":
    unittest.main()
