"""T-0600: worker-guard refuses reading the owner-terms file (cat/grep/python/redirect/Read/Grep), through the real hook
with a temp FACTORY_HOME/DB and a fixture terms file; the two scanners stay allowed."""
import json
import os
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
HOOK = os.path.join(ROOT, "roles", "hooks", "worker-guard.py")
ROLES = ("coder", "qa", "reviewer", "judge")


class TermsReadTests(unittest.TestCase):
    def setUp(self):
        self.tmp = os.path.realpath(tempfile.mkdtemp(prefix="zz-test-termsread-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.home = os.path.join(self.tmp, "home")
        os.makedirs(os.path.join(self.home, "db"))
        self.secrets = os.path.join(self.tmp, "secrets")
        os.makedirs(self.secrets)
        self.terms = os.path.join(self.secrets, "terms.txt")
        with open(self.terms, "w") as f:
            f.write("zebracorp\n")
        con = sqlite3.connect(os.path.join(self.home, "db", "factory.db"))
        con.execute("CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT NOT NULL)")
        con.execute("INSERT INTO config VALUES ('scrub_terms_file', ?)", (self.terms,))
        con.commit()
        con.close()

    def hook(self, payload, role="coder", env_terms=True):
        env = {k: v for k, v in os.environ.items() if k != "SCRUB_TERMS_FILE"}
        env["FACTORY_HOME"] = self.home
        if env_terms:
            env["SCRUB_TERMS_FILE"] = self.terms
        payload = {"cwd": self.tmp, **payload}
        return subprocess.run([sys.executable, HOOK, "--role", role], input=json.dumps(payload),
                              capture_output=True, text=True, timeout=60, env=env)

    def bash(self, cmd, **kw):
        return self.hook({"tool_name": "Bash", "tool_input": {"command": cmd}}, **kw)

    def tool(self, name, **inp):
        return self.hook({"tool_name": name, "tool_input": inp})

    def refused(self, p, what):
        self.assertEqual(p.returncode, 2, f"should refuse: {what}\n{p.stderr}")
        self.assertIn("owner-terms", p.stderr)
        self.assertNotIn("zebracorp", p.stderr)
        self.assertNotIn(self.terms, p.stderr)

    def test_bash_read_shapes_refused_for_every_worker_role(self):
        shapes = ['cat "$SCRUB_TERMS_FILE"', "cat ${SCRUB_TERMS_FILE}", "head -n1 $SCRUB_TERMS_FILE",
                  f"cat {self.terms}", f"grep -r zebra {self.secrets}/terms.txt", "grep . < $SCRUB_TERMS_FILE",
                  "python3 -c 'print(open(__import__(\"os\").environ[\"SCRUB_TERMS_FILE\"]).read())'",
                  "sed p $SCRUB_TERMS_FILE", "cp $SCRUB_TERMS_FILE /tmp/x", "base64 $SCRUB_TERMS_FILE",
                  "xxd $SCRUB_TERMS_FILE", "strings $SCRUB_TERMS_FILE",
                  "python3 ci/scrub_scan.py x; cat $SCRUB_TERMS_FILE",
                  "python3 ci/scrub_scan.py $(cat $SCRUB_TERMS_FILE)", "echo $SCRUB_TERMS_FILE"]
        for role in ROLES:
            for cmd in shapes:
                self.refused(self.bash(cmd, role=role), f"{role}: {cmd}")

    def test_bash_refused_by_exported_path_even_when_config_is_empty(self):
        con = sqlite3.connect(os.path.join(self.home, "db", "factory.db"))
        con.execute("DELETE FROM config")
        con.commit()
        con.close()
        self.refused(self.bash(f"cat {self.terms}"), "env path only")

    def test_scanners_allowed(self):
        for cmd in ["python3 ci/scrub_scan.py --text hello", "SCRUB_TERMS_FILE=$SCRUB_TERMS_FILE python3 ci/scrub_scan.py x",
                    'python3 ci/scrub_scan.py --terms "$SCRUB_TERMS_FILE" file', "python3 ci/public_text_guard.py --check x",
                    "env SCRUB_TERMS_FILE=/x/y python3 ./ci/scrub_scan.py a", "git status && python3 ci/scrub_scan.py"]:
            p = self.bash(cmd)
            self.assertEqual(p.returncode, 0, f"should allow: {cmd}\n{p.stderr}")

    def test_unrelated_commands_allowed_and_non_worker_roles_unaffected(self):
        self.assertEqual(self.bash("cat README.md").returncode, 0)
        self.assertEqual(self.bash("cat $SCRUB_TERMS_FILE", role="lead").returncode, 0)

    def test_read_and_grep_tools(self):
        self.refused(self.tool("Read", file_path=self.terms), "Read path")
        self.refused(self.tool("Read", file_path=os.path.join(self.secrets, "..", "secrets", "terms.txt")), "Read ..")
        self.refused(self.tool("Read", file_path="$SCRUB_TERMS_FILE"), "Read var")
        self.refused(self.tool("Grep", pattern=".", path=self.terms), "Grep file")
        self.refused(self.tool("Grep", pattern=".", path=self.secrets), "Grep dir holding it")
        link = os.path.join(self.tmp, "link.txt")
        os.symlink(self.terms, link)
        self.refused(self.tool("Read", file_path=link), "Read symlink")
        other = os.path.join(self.tmp, "other.txt")
        with open(other, "w") as f:
            f.write("x")
        self.assertEqual(self.tool("Read", file_path=other).returncode, 0)
        sub = os.path.join(self.tmp, "sub")
        os.makedirs(sub)
        self.assertEqual(self.tool("Grep", pattern="x", path=sub).returncode, 0)


if __name__ == "__main__":
    unittest.main()
