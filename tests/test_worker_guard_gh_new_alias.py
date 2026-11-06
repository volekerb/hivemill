"""T-1154: `gh pr new`, `gh issue new` and `gh release new` are gh's aliases of `create`; the worker-guard treats them as
creates (no-push roles, inline scan, frozen-repo and public-text checks). Run through the real hook, temp FACTORY_HOME/DB."""
import json
import os
import shlex
import sqlite3
import tempfile
import unittest

from guard_impl import hook_run

GUARD_IMPL_PYTHON_ONLY = "Go mirror follow-up in G-016"

NP6 = ("qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator")
NEW = ("gh pr new", "gh issue new", "gh release new v1")
CREATE = ("gh pr create", "gh issue create", "gh release create v1")
READS = ("gh pr ls", "gh issue ls", "gh workflow ls", "gh release ls", "gh pr co 1")
# Exit codes of the origin/main hook (a01362e57) for the plain forms, measured through hook_run.
ORIGIN_WRITE = {"coder": 0, "judge": 0, "cleaner": 0, "hq": 0, "researcher": 2}
ORIGIN_READ = {"coder": 0, "judge": 0, "cleaner": 0, "hq": 0}


def wrappers(cmd):
    inner = cmd.replace('"', '\\"')
    return (cmd, cmd.replace("gh ", "gh -R a/b ", 1), cmd.replace("gh ", "gh --repo=a/b ", 1),
            f"bash -c {shlex.quote(cmd)}", f'python3 -c \'import os; os.system("{inner}")\'')


class GhNewAliasTests(unittest.TestCase):
    def setUp(self):
        td = tempfile.TemporaryDirectory()
        self.addCleanup(td.cleanup)
        self.tmp = os.path.realpath(td.name)
        self.home = os.path.join(self.tmp, "factory")
        os.makedirs(os.path.join(self.home, "roles"))
        os.makedirs(os.path.join(self.home, "db"))
        self.terms = os.path.join(self.tmp, "terms.txt")
        with open(self.terms, "w") as f:
            f.write("zebracorp\n")

    def config(self, **kv):
        con = sqlite3.connect(os.path.join(self.home, "db", "factory.db"))
        con.execute("CREATE TABLE IF NOT EXISTS config (key TEXT PRIMARY KEY, value TEXT NOT NULL)")
        con.execute("DELETE FROM config")
        con.executemany("INSERT INTO config VALUES (?, ?)", kv.items())
        con.commit()
        con.close()

    def code(self, cmd, role, hq=False):
        drop = ("WORKER_GUARD_LIVE_HOME", "FACTORY_AGENT", "SCRUB_" + "TERMS_FILE")
        env = {k: v for k, v in os.environ.items() if k not in drop}
        env.update(HOME=self.tmp, WORKER_GUARD_LIVE_HOME=self.home, FACTORY_HOME=self.home, FACTORY_AGENT=f"{role}-x")
        env["SCRUB_" + "TERMS_FILE"] = self.terms
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": self.tmp})
        return hook_run(payload, ["--role", role] + (["--hq"] if hq else []), env=env).returncode

    def test_pr_new_refused_for_no_push_roles(self):
        for w in wrappers("gh pr new"):
            for role in NP6:
                self.assertEqual(self.code(w, role), 2, f"{role}: {w}")

    def test_issue_new_refused_for_no_push_roles(self):
        for w in wrappers("gh issue new"):
            for role in NP6:
                self.assertEqual(self.code(w, role), 2, f"{role}: {w}")

    def test_release_new_refused_for_no_push_roles(self):
        for w in wrappers("gh release new v1"):
            for role in NP6:
                self.assertEqual(self.code(w, role), 2, f"{role}: {w}")

    def test_other_roles_keep_origin_exit_codes(self):
        for c in NEW + CREATE:
            for role in ("coder", "judge", "cleaner", "researcher"):
                self.assertEqual(self.code(c, role), ORIGIN_WRITE[role], f"{role}: {c}")
            self.assertEqual(self.code(c, "coder", hq=True), ORIGIN_WRITE["hq"], f"hq: {c}")

    def test_create_and_reads_keep_origin_exit_codes(self):
        for c in CREATE:
            for role in NP6:
                self.assertEqual(self.code(c, role), 2, f"{role}: {c}")
        for c in READS:
            for role in NP6:
                self.assertEqual(self.code(c, role), 0, f"{role}: {c}")
            for role in ("coder", "judge", "cleaner"):
                self.assertEqual(self.code(c, role), ORIGIN_READ[role], f"{role}: {c}")
            self.assertEqual(self.code(c, "coder", hq=True), ORIGIN_READ["hq"], f"hq: {c}")

    def test_pr_comment_stays_allowed_for_reviewer_and_qa(self):
        for role in ("reviewer", "qa"):
            self.assertEqual(self.code("gh pr comment 1 --body x", role), 0, role)

    def test_frozen_repo_treats_new_like_create(self):
        self.config(frozen_repos="acme/old")
        for new, create in (("gh pr new -R acme/old", "gh pr create -R acme/old"),
                            ("gh issue new -R acme/old", "gh issue create -R acme/old"),
                            ("gh release new -R acme/old v1 --notes x", "gh release create -R acme/old v1 --notes x")):
            for role in ("coder", "qa"):
                want = self.code(create, role)
                self.assertEqual(want, 2, f"{role}: {create}")
                self.assertEqual(self.code(new, role), want, f"{role}: {new}")

    def test_public_text_treats_new_like_create(self):
        self.config(public_text_repos="acme/pub")
        create = "gh pr create -R acme/pub --title t --body 'about zebracorp'"
        new = create.replace("pr create", "pr new")
        self.assertEqual(self.code(create, "judge"), 2)
        self.assertEqual(self.code(new, "judge"), 2)


if __name__ == "__main__":
    unittest.main()
