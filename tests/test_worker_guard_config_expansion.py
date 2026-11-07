"""T-1063: `git config` with an expansion-built argument is refused for coder, judge and cleaner (worker mode), as
reviewer, qa and pm already do. Literal reads stay allowed; HQ mode is unchanged. Hook probes run with cwd in a LINKED
worktree built here, so they work in CI."""
import json
import os
import sqlite3
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from guard_impl import hook_run  # noqa: E402
from harness import git  # noqa: E402

TASK = "t-1063"
WRITERS = ("coder", "judge", "cleaner")
TEXT = "is built from an expansion, so it could be a write; write the arguments literally (T-1063)"

REFUSED = [
    "git config $(echo alias.x push)", "git config $X", 'git config "$X"', "git config ${X}", 'git config "${X[@]}"',
    'git config "$@"', "git config {alias.x,push}", "git config al*", "git config $(echo --get alias.x)",
    "git config --get $(echo alias.x)", "git config --get $KEY", "git config --get alias.$K", "git config --list $X",
    "git config --global $X", "git config --get $X --global", "git config --file $X", "git config -f $X --get a.b",
    "git -C . config $X", "git -c a.b=c config $X", "git --no-pager config $X",
    "git config $(echo --global alias.x push)", "git config --file $(echo .git/config alias.x push)",
]
ALIAS_FORM = "git -c alias.a='config $X' a"  # exits 2 with the older public-text refusal, not the T-1063 text
# quoted literals and regexps with a brace or a non-final dollar are refused too (fail-closed)
TIGHTENED_EXTRA = ["git config '$X'", "git config --get-regexp 'a{1}'", "git config --get-regexp 'a$b'"]
READS = [
    "--get alias.x", "--get user.name", "--list", "get alias.x", "user.name", "--get alias.x --show-origin",
    "--type bool --get core.bare", "--file f --get a.b", r"--get-regexp '^remote\..*\.url$'", "--get-regexp 'alias.*'",
    r"--get-regexp '^alias\.'", r"get --regexp '^alias\.'", "--get al*",
]


class ConfigExpansionTests(unittest.TestCase):
    def setUp(self):
        td = tempfile.TemporaryDirectory()
        self.addCleanup(td.cleanup)
        self.tmp = os.path.realpath(td.name)
        self.home = os.path.join(self.tmp, "factory")
        for d in (os.path.join(self.home, "roles"), os.path.join(self.home, "db")):
            os.makedirs(d)
        self.main = os.path.join(self.home, "main-checkout")
        self.wt = os.path.join(self.home, "worktrees", "G-010-T-1063")
        os.makedirs(self.main)
        os.makedirs(os.path.dirname(self.wt))
        git("init", "-q", "-b", "main", self.main, cwd=self.tmp)
        git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "i", cwd=self.main)
        git("worktree", "add", "-q", "-b", "factory/t-1063", self.wt, cwd=self.main)
        con = sqlite3.connect(os.path.join(self.home, "db", "factory.db"))
        con.execute("CREATE TABLE tasks (id TEXT PRIMARY KEY, worktree TEXT)")
        con.execute("INSERT INTO tasks VALUES (?, ?)", (TASK.upper(), self.wt))
        con.commit()
        con.close()

    def run_hook(self, cmd, role, hq=False):
        env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "FACTORY_AGENT")}
        env.update(HOME=self.tmp, WORKER_GUARD_LIVE_HOME=self.home, FACTORY_HOME=self.home,
                   FACTORY_AGENT=f"coder-{TASK}" if role == "coder" else f"{role}-x")
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": self.wt})
        return hook_run(payload, ["--role", role] + (["--hq"] if hq else []), env=env)

    def check(self, cmds, code, roles=WRITERS, text=True, hq=False):
        for role in roles:
            for c in cmds:
                with self.subTest(role=role, cmd=c):
                    p = self.run_hook(c, role, hq)
                    self.assertEqual(p.returncode, code, p.stderr)
                    if code == 2 and text:
                        self.assertIn(TEXT, p.stderr)

    def test_linked_worktree_fixture(self):
        self.assertTrue(os.path.isfile(os.path.join(self.wt, ".git")), "cwd must be a linked worktree")

    def test_expansion_forms_refused_for_writers(self):
        self.check(REFUSED, 2)

    def test_alias_form_refused(self):
        self.check([ALIAS_FORM], 2, text=False)

    def test_stderr_invariant_main_refused_forms_keep_main_text(self):
        # forms main already refuses must not carry the T-1063 text (keeps Go and Python stderr equal)
        for c in ("git config --file=$HOME/.config/git/config a.b c", "git config $K push"):
            with self.subTest(cmd=c):
                p = self.run_hook(c, "coder")
                self.assertEqual(p.returncode, 2, p.stderr)
                self.assertNotIn("T-1063", p.stderr)

    def test_scratch_write_with_expansion_token_refused(self):
        scratch = os.path.join(self.tmp, "scratch")
        os.makedirs(scratch)
        env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "FACTORY_AGENT")}
        env.update(HOME=self.tmp, WORKER_GUARD_LIVE_HOME=self.home, FACTORY_HOME=self.home,
                   WORKER_GUARD_SCRATCH=scratch, FACTORY_AGENT=f"coder-{TASK}")

        def run(c):
            payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": c}, "cwd": self.wt})
            return hook_run(payload, ["--role", "coder"], env=env)
        ok = run(f"git config --file {scratch}/f a.b c")
        self.assertEqual(ok.returncode, 0, ok.stderr)
        for c in (f"git config --file {scratch}/f a.b $V", f"git config --file {scratch}/f $(echo --file .git/config) a.b c"):
            with self.subTest(cmd=c):
                p = run(c)
                self.assertEqual(p.returncode, 2, p.stderr)

    def test_tightened_extra_forms_refused(self):
        self.check(TIGHTENED_EXTRA, 2)

    def test_literal_reads_allowed(self):
        self.check(["git config " + r for r in READS], 0)

    def test_glob_position(self):
        self.check(["git config al*"], 2)
        self.check(["git config al* --get k"], 2, text=False)  # main already refuses it: main stderr kept
        self.check(["git config --get al*"], 0)

    def test_other_roles_still_refuse(self):
        self.check(REFUSED, 2, roles=("reviewer", "qa", "pm"), text=False)
        # unchanged from origin/main: their older no-push check already refuses a token containing `$` (exit 2)
        self.check([r"git config --get-regexp '^remote\..*\.url$'"], 2, roles=("reviewer", "qa", "pm"), text=False)

    # Exits measured on origin/main (abdc5027c's parent 1723dd6b1 + merge) through the same hook, reviewer=qa=pm:
    # --get-regexp 'alias.*' 0, --get-regexp '^alias\.' 0, ALIAS_FORM 2.
    def test_other_roles_get_regexp_glob_unchanged(self):
        self.check(["git config --get-regexp 'alias.*'"], 0, roles=("reviewer", "qa", "pm"))

    def test_other_roles_get_regexp_anchored_unchanged(self):
        self.check([r"git config --get-regexp '^alias\.'"], 0, roles=("reviewer", "qa", "pm"))

    def test_other_roles_alias_form_unchanged(self):
        self.check([ALIAS_FORM], 2, roles=("reviewer", "qa", "pm"), text=False)

    def test_value_option_glob_before_action_flag_refused(self):
        # tightened for every non-HQ role: origin/main exits 0 for all of these, head exits 2
        cmds = ["git config --file al* --get k", "git config -f al* --get k", "git config --file=al* --get k"]
        self.check(cmds, 2, roles=WRITERS + ("reviewer", "qa", "pm"), text=False)

    def test_hq_unchanged(self):
        self.check(["git config --get $KEY", r"git config --get-regexp '^remote\..*\.url$'"], 0, roles=("lead",), hq=True)


if __name__ == "__main__":
    unittest.main()
