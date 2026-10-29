import json
import os
import pwd
import subprocess
import sys
import tempfile
import unittest

from harness import REPO, FactoryTestCase, MODELS, ROLES, rendered_settings
from test_worker_guard import HOOK, run_hook

HOME_DIR = pwd.getpwuid(os.getuid()).pw_dir
LIVE_HOME = os.path.join(HOME_DIR, "factory")  # what the hook and bin/factory derive (T-0212)

# the qa.json deny baseline both new roles must keep
BASELINE = [
    "Bash(gh pr merge:*)", "Bash(sqlite3:*)", "Bash(herdr server stop:*)", "Bash(herdr agent start:*)",
    "Bash(git push origin main:*)", "Bash(git push --force:*)", "Bash(git push -f:*)",
    "Bash(rm -rf ~/factory:*)", "Edit(/" + LIVE_HOME + "/factory.db*)",
    "Write(/" + LIVE_HOME + "/exports/**)", "Edit(/" + LIVE_HOME + "/state/**)",
    "Write(/" + LIVE_HOME + "/bin/**)", "Edit(/" + LIVE_HOME + "/roles/**)",
]
NEW = ("scout", "evaluator")


def settings(role):
    return json.loads(rendered_settings(role).read_text())


class RoleTableTests(unittest.TestCase):
    def test_roles_and_models(self):
        for r in NEW:
            self.assertIn(r, ROLES)
            self.assertEqual(MODELS[r], "claude-sonnet-5-5")

    def test_not_persistent(self):
        src = (REPO / "bin" / "factory_cli.py").read_text()
        line = next(x for x in src.splitlines() if x.startswith("PERSISTENT_ROLES"))
        for r in NEW:
            self.assertNotIn(f'"{r}"', line)

    def test_prompts_exist_and_fit_budget(self):
        common = (REPO / "roles" / "_common.md").stat().st_size
        for r in NEW:
            md = REPO / "roles" / f"{r}.md"
            self.assertTrue(md.is_file(), r)
            self.assertLessEqual(common + md.stat().st_size, 12000, r)
        self.assertIn("| scout |", (REPO / "roles" / "_common.md").read_text())
        self.assertIn("| evaluator |", (REPO / "roles" / "_common.md").read_text())


class SettingsTests(unittest.TestCase):
    def test_baseline_hook_and_socket(self):
        for r in NEW:
            d = settings(r)
            for rule in BASELINE:
                self.assertIn(rule, d["permissions"]["deny"], f"{r}: {rule}")
            self.assertEqual(d["env"]["FACTORY_ROLE"], r)
            self.assertTrue(d["env"]["HERDR_SOCKET_PATH"].startswith("/proc/nonexistent/"))
            cmd = d["hooks"]["PreToolUse"][0]["hooks"][0]["command"]
            self.assertTrue(cmd.endswith(f"worker-guard.py --role {r}"), cmd)

    def test_scout_rules(self):
        d = settings("scout")["permissions"]
        for rule in ("Edit", "Write", "NotebookEdit", "WebFetch", "WebSearch", "Bash(gh pr:*)", "Bash(gh issue:*)",
                     "Bash(curl:*)", "Bash(wget:*)", "Bash(git commit:*)", "Bash(git push:*)", "Bash(git merge:*)"):
            self.assertIn(rule, d["deny"])
        self.assertIn("Bash(gh api:*)", d["allow"])
        self.assertIn("Bash(factory:*)", d["allow"])

    def test_evaluator_rules(self):
        d = settings("evaluator")["permissions"]
        for rule in ("WebFetch", "WebSearch", "Bash(curl:*)", "Bash(wget:*)", "Bash(gh:*)", "Bash(pip:*)",
                     "Bash(npm:*)", "Bash(npx:*)", "Bash(ssh:*)", "Bash(scp:*)", "Bash(git push:*)",
                     "Bash(herdr:*)", "Edit(/" + LIVE_HOME + "/**)", "Write(/" + LIVE_HOME + "/**)",
                     "Edit(/" + HOME_DIR + "/.claude/**)", "Write(/" + HOME_DIR + "/.claude/**)"):
            self.assertIn(rule, d["deny"])
        for rule in ("Edit", "Write"):  # the sandbox itself stays writable
            self.assertNotIn(rule, d["deny"])


class SpawnTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_team(lead="zz-test-lead")
        for n, r in (("zz-test-director", "director"), ("zz-test-lead", "lead"), ("zz-test-boss", "boss")):
            self.env.seed_agent(n, r, goal="G-001", team="g-001-a")

    def test_scout_spawn_director_and_owner(self):
        for who, name in (("zz-test-director", "zz-test-s1"), (None, "zz-test-s2")):
            kw = {"agent": who} if who else {}
            self.assertIn(f"SPAWNED {name}", self.ok("spawn", "scout", "--name", name, **kw).stdout)
        starts = {c["argv"][2]: c["argv"] for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]}
        argv = starts["zz-test-s1"]
        self.assertNotIn("--plugin-dir", argv)
        self.assertEqual(argv[argv.index("--model") + 1], "claude-sonnet-5-5")
        self.assertTrue(argv[argv.index("--settings") + 1].endswith("run/settings/scout.json"))

    def test_scout_spawn_refused_for_lead_and_boss(self):
        for who in ("zz-test-lead", "zz-test-boss"):
            self.refused(3, "spawn", "scout", "--name", "zz-test-sx", agent=who)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM agents WHERE name='zz-test-sx'")[0]["c"], 0)

    def test_evaluator_spawn_refused_for_everyone(self):
        for who in ("zz-test-director", "zz-test-lead", "zz-test-boss", None):
            kw = {"agent": who} if who else {}
            p = self.refused(3, "spawn", "evaluator", "--name", "zz-test-ev", **kw)
            self.assertIn("factory skilleval try", p.stderr)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM agents WHERE name='zz-test-ev'")[0]["c"], 0)


class RefusalTests(FactoryTestCase):
    COMMANDS = (
        ("spawn", "coder", "--name", "zz-test-x"),
        ("tick",),
        ("judge", "T-0001", "--merge", "--reason", "x"),
        ("verdict", "T-0001", "--pass", "--summary", "x"),
        ("plan", "G-001", "x"),
        ("task", "add", "G-001", "--title", "x", "--role", "coder"),
        ("lesson", "add", "--title", "x", "--rule", "y"),
        ("goal", "new", "--title", "x"),
    )

    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_team(lead="zz-test-lead")
        self.env.seed_task("T-0001", role="coder", status="in_review", team="g-001-a")
        for r in NEW:
            self.env.seed_agent(f"zz-test-{r}", r)

    def test_new_roles_refused(self):
        for r in NEW:
            for cmd in self.COMMANDS:
                with self.subTest(role=r, cmd=cmd[0]):
                    self.refused(3, *cmd, agent=f"zz-test-{r}")
        self.assertEqual(self.env.sql("SELECT count(*) c FROM goals")[0]["c"], 1)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM tasks")[0]["c"], 1)

    def test_scout_done_without_task_or_goal(self):
        self.env.seed_agent("zz-test-director", "director")
        p = self.ok("done", "scouted", agent="zz-test-scout")
        self.assertIn("done; summary sent to director", p.stdout)
        self.assertEqual(self.env.sql("SELECT status,summary FROM agents WHERE name='zz-test-scout'")[0]["status"], "done")


class GuardTests(unittest.TestCase):
    def test_herdr_and_claude_blocked_per_role(self):
        for r in NEW:
            for cmd in ("herdr pane list", "env HOME=x herdr server stop", "claude -p hi", "/usr/bin/claude"):
                with self.subTest(role=r, cmd=cmd):
                    self.assertEqual(run_hook(cmd, role=r).returncode, 2)

    def test_scout_runs_factory_scout(self):
        for cmd in ("factory scout fetch", "factory scout pending", "factory done x", "factory whoami"):
            p = run_hook(cmd, role="scout")
            self.assertEqual(p.returncode, 0, f"{cmd}\n{p.stderr}")
        self.assertEqual(run_hook("factory spawn coder", role="scout").returncode, 2)

    def _evaluator(self, cmd, home):
        env = {k: v for k, v in os.environ.items() if k != "FACTORY_HOME"}
        if home:
            env["FACTORY_HOME"] = home
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}})
        return subprocess.run([sys.executable, HOOK, "--role", "evaluator"], input=payload, env=env,
                              capture_output=True, text=True, timeout=30)

    def test_evaluator_factory_only_against_temp_home(self):
        with tempfile.TemporaryDirectory(prefix="zz-test-evhome-") as tmp:
            for cmd in ("factory done 'did it'", "factory task show T-0001", "factory whoami", "factory inbox",
                        "factory msg lead hi there"):
                p = self._evaluator(cmd, tmp)
                self.assertEqual(p.returncode, 0, f"{cmd}\n{p.stderr}")
            self.assertEqual(self._evaluator(f"FACTORY_HOME={tmp} factory done x", LIVE_HOME).returncode, 0)
            for cmd in ("factory scout pending", "factory goal show G-001", "factory task update T-0001 --status done"):
                self.assertEqual(self._evaluator(cmd, tmp).returncode, 2, cmd)
        live = LIVE_HOME
        for home in (live, None):
            self.assertEqual(self._evaluator("factory done x", home).returncode, 2, home)
        self.assertEqual(self._evaluator(f"FACTORY_HOME={LIVE_HOME} factory done x", "/tmp").returncode, 2)

    def test_evaluator_factory_home_token_closed_by_default(self):
        live = LIVE_HOME
        with tempfile.TemporaryDirectory(prefix="zz-test-evhome-") as tmp:
            for cmd in (f"export FACTORY_HOME={live}; factory done x",
                        f"declare -x FACTORY_HOME={live}; factory msg director hi",
                        f"FACTORY_HOME={live}; export FACTORY_HOME; factory inbox",
                        f"FACTORY_HOME={live}; factory inbox",
                        "unset FACTORY_HOME; factory msg director hi",
                        "env -u FACTORY_HOME factory inbox",
                        f"env FACTORY_HOME={live} factory inbox",
                        f"readonly FACTORY_HOME={live}; factory inbox",
                        f"FACTORY_HOME={live} factory inbox",
                        f"FACTORY_HOME={live}/worktrees/x factory inbox",
                        f"eval 'export FACTORY_HOME={live}'; factory inbox",
                        f"bash -c 'export FACTORY_HOME={live}; factory inbox'",
                        f"read FACTORY_HOME <<< {live}; factory inbox",
                        f"printf -v FACTORY_HOME %s {live}; factory inbox",
                        f"for FACTORY_HOME in {live}; do factory done x; done",
                        f"mapfile -t FACTORY_HOME <<< {live}; factory inbox",
                        "getopts a FACTORY_HOME -a; factory inbox",
                        f"export {{FACTORY_HOME,Y}}={live}; factory inbox",
                        f"FACTORY_HOME={tmp} factory inbox; export FACTORY_HOME={live}",
                        f"FACTORY_HOME={tmp} factory inbox && FACTORY_HOME={live} factory inbox",
                        f"echo FACTORY_HOME; factory inbox",
                        f"FACTORY_HOME=$FACTORY_HOME factory inbox"):
                p = self._evaluator(cmd, tmp)
                self.assertEqual(p.returncode, 2, f"{cmd}\n{p.stderr}")
            for cmd in (f"FACTORY_HOME={tmp} factory inbox", f"FACTORY_HOME={tmp} factory done 'did it'", "factory done x"):
                p = self._evaluator(cmd, tmp)
                self.assertEqual(p.returncode, 0, f"{cmd}\n{p.stderr}")

    def test_other_roles_may_mention_factory_home(self):
        with tempfile.TemporaryDirectory(prefix="zz-test-evhome-") as tmp:
            self.assertEqual(run_hook(f"export FACTORY_HOME={tmp}; echo hi", role="scout").returncode, 0)


class EvaluatorHomeRuntimeTests(FactoryTestCase):
    """bin/factory second layer: an evaluator never opens the live (or an unset) home."""

    def _run(self, home, **kw):
        env = self.env.env("zz-test-ev", {"FACTORY_ROLE": "evaluator"})
        env.pop("FACTORY_HOME", None)
        if home is not None:
            env["FACTORY_HOME"] = home
        return subprocess.run([sys.executable, str(REPO / "bin" / "factory"), "whoami"], capture_output=True,
                              text=True, env=env, cwd=self.env.home, timeout=60)

    def test_evaluator_refused_on_live_unset_or_empty_home(self):
        live = LIVE_HOME
        link = os.path.join(self.env.home, "live-link")
        os.symlink(live, link)
        for home in (None, "", live, live + "/", link, link + "/", live + "/../factory"):
            p = self._run(home)
            self.assertEqual(p.returncode, 3, f"{home!r}\n{p.stdout}{p.stderr}")
            self.assertIn("evaluator", p.stderr)

    def test_evaluator_works_with_temp_home(self):
        p = self._run(str(self.env.home_dir))
        self.assertEqual(p.returncode, 0, p.stderr)

    def test_other_roles_unaffected(self):
        env = self.env.env("zz-test-c", {"FACTORY_ROLE": "coder"})
        env["FACTORY_HOME"] = LIVE_HOME + "-nonexistent-zz"
        p = subprocess.run([sys.executable, str(REPO / "bin" / "factory"), "whoami"], capture_output=True,
                           text=True, env=env, cwd=self.env.home, timeout=60)
        self.assertNotEqual(p.returncode, 3, p.stderr)


if __name__ == "__main__":
    unittest.main()
