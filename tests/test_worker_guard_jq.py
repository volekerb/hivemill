"""T-0836: gh --jq values with braces must not trip the owner-terms brace rule when the parent dir is named factory.
Real reads and non-gh shell brace expansion next to the terms file stay refused (through guard_impl.hook_run)."""
import json
import os
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import unittest

from guard_impl import ROOT, hook_run

GUARD_IMPL_PYTHON_ONLY = "T-0836: terms_brace_view cd/pushd-prefix fix is Python-only; Go terms check has no jq blanking"

HOOK = os.path.join(ROOT, "roles", "hooks", "worker-guard.py")
GH_JQ_CMD = "cd /tmp/factory && gh pr view 242 --jq '{title:.title,state:.state}'"
ROLES_GH = ("coder", "qa", "reviewer", "judge")
ROLES_READ = (*ROLES_GH, "pm")

# Removing this loop in a scratch copy must make GH_JQ_CMD fail (C4).
GH_JQ_BLANK_BLOCK = """        if head == "gh":
            for k in range(1, len(words) - 1):
                if txt[k] in ("--jq", "-q", "--template") and whole(k + 1):
                    blank.append(words[k + 1][:2])"""


class WorkerGuardJqTests(unittest.TestCase):
    def setUp(self):
        self.tmp = os.path.realpath(tempfile.mkdtemp(prefix="zz-test-jq0836-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.home = os.path.join(self.tmp, "home")
        os.makedirs(os.path.join(self.home, "db"))
        self.secrets = os.path.join(self.tmp, "factory")
        os.makedirs(self.secrets)
        self.terms = os.path.join(self.secrets, "scrub-terms.txt")
        with open(self.terms, "w") as f:
            f.write("zebracorp\n")
        con = sqlite3.connect(os.path.join(self.home, "db", "factory.db"))
        con.execute("CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT NOT NULL)")
        con.execute("INSERT INTO config VALUES ('scrub_terms_file', ?)", (self.terms,))
        con.commit()
        con.close()
        self.work = os.path.join(self.tmp, "work")
        os.makedirs(self.work)
        self.brace = self.secrets + "/{s,x}*"

    def bash(self, cmd, role="coder"):
        env = {k: v for k, v in os.environ.items() if k != "SCRUB_TERMS_FILE"}
        env.update(FACTORY_HOME=self.home, HOME=self.tmp, SCRUB_TERMS_FILE=self.terms)
        payload = {"cwd": self.work, "tool_name": "Bash", "tool_input": {"command": cmd}}
        return hook_run(json.dumps(payload), ["--role", role], env=env, timeout=60)

    def test_gh_jq_with_braces_allowed(self):
        for role in ROLES_GH:
            with self.subTest(role=role):
                p = self.bash(GH_JQ_CMD, role=role)
                self.assertEqual(p.returncode, 0, f"{role}: {GH_JQ_CMD}\n{p.stderr}")

    def test_cat_owner_terms_refused(self):
        for role in ROLES_READ:
            with self.subTest(role=role):
                p = self.bash(f"cat {self.terms}", role=role)
                self.assertEqual(p.returncode, 2, p.stderr)
                self.assertIn("owner-terms", p.stderr)
                self.assertNotIn("zebracorp", p.stderr)

    def test_shell_brace_not_gh_jq_refused(self):
        cmd = "grep 'a' " + self.brace
        for role in ROLES_GH:
            with self.subTest(role=role, cmd=cmd):
                p = self.bash(cmd, role=role)
                self.assertEqual(p.returncode, 2, p.stderr)
                self.assertIn("owner-terms", p.stderr)

    def test_c4_without_gh_jq_blank_refuses(self):
        with open(HOOK, encoding="utf-8") as f:
            text = f.read()
        self.assertIn(GH_JQ_BLANK_BLOCK, text)
        mutated = text.replace(GH_JQ_BLANK_BLOCK, "        if head == \"gh\":\n            pass  # C4: gh --jq blank removed", 1)
        self.assertNotEqual(mutated, text)
        scratch = os.path.join(self.tmp, "wg_mut.py")
        with open(scratch, "w", encoding="utf-8") as f:
            f.write(mutated)
        env = {k: v for k, v in os.environ.items() if k != "SCRUB_TERMS_FILE"}
        env.update(FACTORY_HOME=self.home, HOME=self.tmp, SCRUB_TERMS_FILE=self.terms)
        payload = json.dumps({"cwd": self.work, "tool_name": "Bash", "tool_input": {"command": GH_JQ_CMD}})
        p = subprocess.run(
            [sys.executable, scratch, "--role", "coder"],
            input=payload,
            capture_output=True,
            text=True,
            timeout=60,
            env=env,
            cwd=self.work,
        )
        self.assertEqual(p.returncode, 2, f"mutant should refuse gh --jq braces\n{p.stderr}")
        self.assertIn("owner-terms", p.stderr)


if __name__ == "__main__":
    unittest.main()
