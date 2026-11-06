"""T-1156: gh repo create|new|fork|sync, label create, secret set, variable set are gh writes refused for no-push roles,
and a separate `-R x` / `--repo x` before the group no longer hides a frozen-repo or public-text write.
Run through the real hook, temp FACTORY_HOME/DB."""
import json
import os
import shlex
import sqlite3
import tempfile
import unittest

from guard_impl import hook_run

GUARD_IMPL_PYTHON_ONLY = "Go mirror follow-up in G-016"

NP6 = ("qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator")
OTHERS = ("coder", "judge", "cleaner", "researcher")
REPO = ("gh repo create a/b", "gh repo new a/b", "gh repo fork a/b", "gh repo sync")
LABEL = ("gh label create x",)
SECRET = ("gh secret set X",)
VARIABLE = ("gh variable set X",)
KEEP = ("gh repo view a/b", "gh repo list", "gh repo clone a/b", "gh repo set-default a/b", "gh label list",
        "gh secret list", "gh variable list", "gh variable get X")
# Exit codes of the origin/main hook (63e03599d) for the plain forms, measured through hook_run.
ORIGIN_PLAIN = {"coder": 0, "judge": 0, "cleaner": 0, "hq": 0, "researcher": 2}
ORIGIN_KEEP = {"coder": 0, "judge": 0, "cleaner": 0, "hq": 0, "researcher": 0}
ORIGIN_KEEP_RESEARCHER = {"gh repo view a/b": 0, "gh repo list": 0, "gh repo clone a/b": 2, "gh repo set-default a/b": 2,
                          "gh label list": 2, "gh secret list": 2, "gh variable list": 2, "gh variable get X": 2}
# researcher python3 -c os.system forms of label create, secret set, variable set: exit 0 on origin/main, 2 on the head
# (INLINE_GH_WRITE now names them: tightened).
RESEARCHER_PY_MAIN, RESEARCHER_PY_HEAD = 0, 2


def wrappers(cmd):
    inner = cmd.replace('"', '\\"')
    return (cmd, cmd.replace("gh ", "gh -R a/b ", 1), cmd.replace("gh ", "gh --repo=a/b ", 1),
            f"bash -c {shlex.quote(cmd)}", f'python3 -c \'import os; os.system("{inner}")\'')


class GhRepoGroupsTests(unittest.TestCase):
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

    def refused(self, cmds):
        for c in cmds:
            for w in wrappers(c):
                for role in NP6:
                    self.assertEqual(self.code(w, role), 2, f"{role}: {w}")

    def test_repo_writes_refused_for_no_push_roles(self):
        self.refused(REPO)

    def test_label_create_refused_for_no_push_roles(self):
        self.refused(LABEL)

    def test_secret_set_refused_for_no_push_roles(self):
        self.refused(SECRET)

    def test_variable_set_refused_for_no_push_roles(self):
        self.refused(VARIABLE)

    def test_other_roles_keep_origin_exit_codes(self):
        for c in REPO + LABEL + SECRET + VARIABLE:
            for role in OTHERS:
                self.assertEqual(self.code(c, role), ORIGIN_PLAIN.get(role, 0), f"{role}: {c}")
            self.assertEqual(self.code(c, "coder", hq=True), ORIGIN_PLAIN["hq"], f"hq: {c}")

    def test_inline_repo_verbs_stay_refused_for_no_push_roles(self):
        # INLINE_GH_WRITE keeps its bare `repo` word (accepted false positive on view): exit 2 on origin/main and head.
        for c in ("gh repo delete a/b --yes", "gh repo edit a/b", "gh repo view a/b"):
            for role in NP6:
                self.assertEqual(self.code(wrappers(c)[4], role), 2, f"{role}: {c}")

    def test_researcher_python_forms_of_label_secret_variable(self):
        for c in LABEL + SECRET + VARIABLE:
            self.assertEqual(self.code(wrappers(c)[4], "researcher"), RESEARCHER_PY_HEAD, c)

    def test_reads_keep_origin_exit_codes(self):
        for c in KEEP:
            for role in NP6:
                self.assertEqual(self.code(c, role), 0, f"{role}: {c}")
            for role in OTHERS:
                self.assertEqual(self.code(c, role), ORIGIN_KEEP_RESEARCHER[c] if role == "researcher" else ORIGIN_KEEP.get(role, 0), f"{role}: {c}")
            self.assertEqual(self.code(c, "coder", hq=True), ORIGIN_KEEP["hq"], f"hq: {c}")
        for role in ("reviewer", "qa"):
            self.assertEqual(self.code("gh pr comment 1 --body x", role), 0, role)

    def test_frozen_repo_r_first_equals_trailing(self):
        self.config(frozen_repos="acme/old")
        pairs = (("gh -R acme/old pr create", "gh pr create -R acme/old"),
                 ("gh --repo acme/old pr create", "gh pr create --repo acme/old"),
                 ("gh -R acme/old pr new", "gh pr new -R acme/old"),
                 ("gh -R acme/old issue create", "gh issue create -R acme/old"),
                 ("gh -R acme/old release create v1", "gh release create -R acme/old v1"),
                 ("gh -R acme/old pr merge 1", "gh pr merge 1 -R acme/old"))
        for first, trailing in pairs:
            for role in ("qa", "coder", "judge", "cleaner", "researcher"):
                want = self.code(trailing, role)
                self.assertEqual(want, 2, f"{role}: {trailing}")
                self.assertEqual(self.code(first, role), want, f"{role}: {first}")
            self.assertEqual(self.code(first, "coder", hq=True), 2, f"hq: {first}")

    def test_frozen_repo_r_first_read_keeps_origin_code(self):
        self.config(frozen_repos="acme/old")
        for role in ("coder", "judge", "cleaner"):
            self.assertEqual(self.code("gh -R acme/old pr view 1", role), 0, role)
        self.assertEqual(self.code("gh -R acme/old pr view 1", "coder", hq=True), 0)
        self.assertEqual(self.code("gh -R acme/old pr view 1", "qa"), 0)
        self.assertEqual(self.code("gh -R acme/old pr view 1", "researcher"), 2)

    def test_public_text_r_first_equals_trailing(self):
        self.config(public_text_repos="acme/pub")
        trailing = "gh pr create -R acme/pub --title t --body 'about zebracorp'"
        first = "gh -R acme/pub pr create --title t --body 'about zebracorp'"
        self.assertEqual(self.code(trailing, "judge"), 2)
        self.assertEqual(self.code(first, "judge"), 2)
        clean = "gh -R acme/pub pr create --title t --body clean"
        self.assertEqual(self.code(clean, "judge"), self.code(clean.replace("-R acme/pub pr create", "pr create -R acme/pub"), "judge"))


if __name__ == "__main__":
    unittest.main()
