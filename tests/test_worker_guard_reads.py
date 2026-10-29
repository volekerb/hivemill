import json
import os
import pwd
import sqlite3
import subprocess
import sys
import tempfile
import unittest

LIVE_HOME = os.path.join(pwd.getpwuid(os.getuid()).pw_dir, "factory")  # what the hook and bin/factory derive (T-0212)

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
HOOK = os.path.join(ROOT, "roles", "hooks", "worker-guard.py")
AGENT = "coder-t-9"
OWN, OTHER = "G-005", "G-006"
BROAD = ("factory res", "factory agents", "factory scorecard", "factory digest", "factory config",
         "factory goal list", "factory task list", "factory task ready", "factory team list",
         "factory project list")


class ReadsBase(unittest.TestCase):
    def setUp(self):
        self.td = tempfile.TemporaryDirectory()
        self.addCleanup(self.td.cleanup)
        self.home = self.td.name
        os.makedirs(os.path.join(self.home, "db"))
        con = sqlite3.connect(os.path.join(self.home, "db", "factory.db"))
        con.execute("CREATE TABLE agents (name TEXT PRIMARY KEY, role TEXT, goal_id TEXT)")
        con.execute("INSERT INTO agents VALUES (?, 'coder', ?)", (AGENT, OWN))
        con.execute("INSERT INTO agents VALUES ('nogoal', 'coder', NULL)")
        con.commit()
        con.close()

    def run_hook(self, cmd, role="coder", agent=AGENT, home=None):
        env = {k: v for k, v in os.environ.items() if k not in ("FACTORY_AGENT", "FACTORY_HOME")}
        env["FACTORY_HOME"] = home or self.home
        if agent:
            env["FACTORY_AGENT"] = agent
        argv = [sys.executable, HOOK] + (["--role", role] if role else [])
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}})
        return subprocess.run(argv, input=payload, capture_output=True, text=True, timeout=30, env=env)

    def blocked(self, cmd, **kw):
        p = self.run_hook(cmd, **kw)
        self.assertEqual(p.returncode, 2, f"should block: {cmd!r}\n{p.stderr}")
        return p

    def allowed(self, cmd, **kw):
        p = self.run_hook(cmd, **kw)
        self.assertEqual(p.returncode, 0, f"should allow: {cmd!r}\n{p.stderr}")


class BroadReadTests(ReadsBase):
    def test_broad_reads_blocked_and_named(self):
        for cmd in BROAD:
            with self.subTest(cmd=cmd):
                self.assertIn(f"`{cmd}`", self.blocked(cmd).stderr)

    def test_goal_audit_allowed_for_pm(self):
        for flag in ("--ready", "--gaps"):
            with self.subTest(flag=flag):
                self.allowed(f"factory goal audit {OWN} {flag} --report - <<'EOF'\nreport\nEOF", role="pm")
                self.allowed(f"factory goal audit {OWN} {flag} --report -", role="pm")

    def test_goal_list_still_blocked_for_pm_and_coder(self):
        for role in ("pm", "coder"):
            with self.subTest(role=role):
                self.blocked("factory goal list", role=role)

    def test_other_roles_blocked(self):
        for role in ("pm", "qa", "reviewer", "judge", None):
            with self.subTest(role=role):
                self.blocked("factory task list", role=role)

    def test_options_before_action_fail_closed(self):
        # chosen: no argparse modelling; any option token before the action word blocks (incl. own-goal show)
        for cmd in ("factory task --goal G-005 list", "factory task --all list", "factory goal --all list",
                    "factory goal --note x show G-006", "factory goal --goal G-005 show G-005",
                    "factory task --goal=G-005 list", "factory goal --goal=G-005 show G-005",
                    "factory task -- list", "factory goal -- show G-005", "factory -- task list",
                    "factory --x team list", "factory project -x list", "factory task --goal G-005 ready",
                    "factory goal show --title G-005 G-006", "factory goal show -- G-005",
                    "python3 ~/factory/bin/factory task --goal G-005 list"):
            with self.subTest(cmd=cmd):
                self.blocked(cmd)
                self.allowed("FACTORY_HOME=/tmp/fh " + cmd)

    def test_options_after_action_unchanged(self):
        self.allowed("factory goal show G-005 --all")
        self.allowed("factory task show T-1 --goal G-005")
        self.blocked("factory task list --goal G-005")
        self.blocked("factory goal show G-006 --all")

    def test_goal_show(self):
        self.allowed(f"factory goal show {OWN}")
        self.allowed(f"factory goal show {OWN} | head -20")
        self.blocked(f"factory goal show {OTHER}")
        self.blocked("factory goal show $G")
        self.blocked("factory goal show G-*")
        self.blocked("factory goal show")

    def test_full_option_position(self):
        # G-014: `--full` after the id is allowed (own goal only); before the id it stays blocked (fail closed)
        self.allowed(f"factory goal show {OWN} --full")
        self.blocked(f"factory goal show {OTHER} --full")
        self.blocked(f"factory goal show --full {OWN}")
        self.allowed("factory task show T-1 --full")
        self.blocked("factory task list")

    def test_goal_show_fail_closed(self):
        self.blocked(f"factory goal show {OWN}", agent=None)
        self.blocked(f"factory goal show {OWN}", agent="ghost")
        self.blocked(f"factory goal show {OWN}", agent="nogoal")
        self.blocked(f"factory goal show {OWN}", home=os.path.join(self.home, "missing"))
        bad = os.path.join(self.home, "bad")
        os.mkdir(bad)
        os.mkdir(os.path.join(bad, "db"))
        with open(os.path.join(bad, "db", "factory.db"), "w") as f:
            f.write("not a database")
        self.blocked(f"factory goal show {OWN}", home=bad)

    def test_own_goal_wal_db_with_open_writer(self):
        con = sqlite3.connect(os.path.join(self.home, "db", "factory.db"))
        con.execute("PRAGMA journal_mode=WAL")
        con.execute("UPDATE agents SET role = 'coder' WHERE name = ?", (AGENT,))
        try:  # writer stays open with uncommitted data
            self.allowed(f"factory goal show {OWN}")
        finally:
            con.commit()
            con.close()

    def test_fail_closed_leaves_other_commands(self):
        self.allowed("factory inbox", agent=None)
        self.allowed("factory task show T-1", home=os.path.join(self.home, "missing"))

    def test_temp_home_escape(self):
        self.allowed("FACTORY_HOME=/tmp/x factory res")
        self.allowed("FACTORY_HOME=/tmp/x factory task list")
        self.allowed("FACTORY_HOME=$(mktemp -d) factory task list")
        self.blocked("cd /tmp && factory res")
        self.blocked("export FACTORY_HOME=/tmp/x; factory res")
        self.blocked(f"FACTORY_HOME={LIVE_HOME} factory res")
        self.blocked("FACTORY_HOME=$X factory res")

    def test_factory_db_name_is_irrelevant_to_the_hook(self):
        # FACTORY_DB no longer picks any DB (bin/factory ignores it; tests/test_db_layout.py NoOverrideTests),
        # so the hook neither scans nor needs to: only FACTORY_HOME decides temp-home vs live.
        self.allowed("FACTORY_HOME=/tmp/x FACTORY_DB=/tmp/x/db/factory.db factory res")
        self.blocked("FACTORY_DB=/tmp/x/f.db factory res")

    def test_wrapped_forms_blocked(self):
        for cmd in (f"python3 {LIVE_HOME}/bin/factory agents", "env factory res",
                    "bash -c 'factory task list'", "echo $(factory res)", "`factory res`"):
            with self.subTest(cmd=cmd):
                self.blocked(cmd)

    def test_cleaner_keeps_agents(self):
        self.allowed("factory agents", role="cleaner")
        self.blocked("factory agents", role="coder")
        self.blocked("factory res", role="cleaner")

    def test_staff_engineer_exempt(self):
        for cmd in BROAD + (f"factory goal show {OTHER}",):
            with self.subTest(cmd=cmd):
                self.allowed(cmd, role="staff-engineer")


class NoFalsePositiveTests(ReadsBase):
    def test_normal_worker_commands(self):
        cmds = [
            "git status", "git diff", "git log --oneline -5", 'git commit -m "x"',
            "git push -u origin factory/T-0047", "gh pr create --fill", "gh pr view 5", "gh pr checks 5",
            "python3 -m unittest discover -s tests -p test_x.py -v", "pytest -q", "ls -la", "cat README.md",
            "rg factory README.md", 'grep "factory res" README.md', 'git commit -m "factory task list fix"',
            "echo factory agents", 'gh pr create --title t --body "uses factory res and factory task list"',
            "factory task show T-1", "factory task show T-0041", "factory inbox", "factory inbox --all",
            "factory inbox --show M1", "factory inbox --show 1 --full",
            "factory whoami", 'factory msg lead:g "s" "b"', 'factory done "x"',
            'factory verdict T-1 --pass "x"', "factory task update T-1 --pr URL --status in_review",
            "factory task note T-1 - <<'X'\nfactory res\nX",
            "factory task add --title x", "factory plan G-1 - <<'X'\nfoo\nX", "factory report G-1 x",
            f"factory goal show {OWN}",
        ]
        for cmd in cmds:
            with self.subTest(cmd=cmd):
                self.allowed(cmd)


if __name__ == "__main__":
    unittest.main()
