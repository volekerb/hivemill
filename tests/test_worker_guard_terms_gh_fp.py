"""T-1121: the owner-terms brace check no longer fires on a brace inside a single-quoted grep/egrep/rg/jq pattern or a gh
--jq/-q/--template value (terms_brace_view, depth 0 only). Everything else stays refused.
Through the real hook with a temp FACTORY_HOME/DB and a fixture terms file in a directory named "factory"."""
import json
import os
import shutil
import sqlite3
import tempfile
import unittest
from guard_impl import hook_run

GUARD_IMPL_PYTHON_ONLY = "Go mirror follow-up in G-016"

ROLES = ("coder", "qa", "reviewer", "judge", "pm")


class TermsGhFpTests(unittest.TestCase):
    def setUp(self):
        self.tmp = os.path.realpath(tempfile.mkdtemp(prefix="zz-test-termsgh-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.home = os.path.join(self.tmp, "home")
        os.makedirs(os.path.join(self.home, "db"))
        self.secrets = os.path.join(self.tmp, "factory")  # parent dir name "factory", like the live path
        os.makedirs(self.secrets)
        self.terms = os.path.join(self.secrets, "scrub-terms.txt")
        with open(self.terms, "w") as f:
            f.write("zebracorp\n")
        con = sqlite3.connect(os.path.join(self.home, "db", "factory.db"))
        con.execute("CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT NOT NULL)")
        con.execute("INSERT INTO config VALUES ('scrub_terms_file', ?)", (self.terms,))
        con.commit()
        con.close()
        self.work = os.path.join(self.tmp, "work")  # cwd: a sibling of the terms directory, like a worktree
        os.makedirs(self.work)
        self.brace = self.secrets + "/{s,x}*"

    def bash(self, cmd, role="coder"):
        env = {k: v for k, v in os.environ.items() if k not in ("SCRUB_TERMS_FILE", "WORD")}
        env.update(FACTORY_HOME=self.home, HOME=self.tmp, SCRUB_TERMS_FILE=self.terms)
        payload = {"cwd": self.work, "tool_name": "Bash", "tool_input": {"command": cmd}}
        return hook_run(json.dumps(payload), ["--role", role], env=env, timeout=60)

    def allowed(self, cmd):
        for role in ROLES:
            p = self.bash(cmd, role)
            self.assertEqual(p.returncode, 0, f"{role} should pass: {cmd}\nrc={p.returncode} {p.stderr}")

    def refused(self, cmd):
        for role in ROLES:
            p = self.bash(cmd, role)
            self.assertTrue(p.returncode == 2 and "owner-terms" in p.stderr,
                            f"{role} should be refused by owner-terms: {cmd}\nrc={p.returncode} {p.stderr}")
            self.assertNotIn("zebracorp", p.stderr)

    # --- item 1: the complete loosening ---
    def test_loosened_forms_pass(self):
        for cmd in (
            "grep -nE 'by [0-9]{1,3}' roles/factory.md",
            "grep -rnE 'factory/t-[0-9]+ [0-9a-f]{7,40}' docs tests",
            "gh pr list --head factory/t-1 --json number,title --jq '.[]|{n:.number,t:.title}'",
            "gh pr view 1 --json a,b -q '{a:.a,b:.b}' factory/x",
            "gh pr view 1 --json a --template '{{.a}},' factory/x",
            "egrep 'a{1,2}' roles/factory.md",
            "rg -n 'a{1,2}' roles/factory.md",
            "jq -r '{a:.a,b:.b}' factory.json",
            "gh pr list --head factory/t-1 --json number --jq '.[]|{n:.number,m:1}' | grep 'a{1,2}'",
            "grep -rn 'a{1,2}' docs $WORD",
        ):
            with self.subTest(cmd=cmd):
                self.allowed(cmd)

    # --- item 2: earlier must-refuse forms ---
    def test_b5_and_m25470_forms_refused(self):
        b = "cat " + self.brace
        self.refused("gh pr view 1 # it's\ngh pr view 1 x'--jq'; " + b + '; echo "q"')
        self.refused("gh pr comment 1 --body-file - <<'EOF'\nit's\nEOF\ngh pr view 1 x'--jq'; " + b + '; echo "q"')
        self.refused("gh pr view 1 ${X:-\"it's\"} x'--jq'; " + b + '; echo "q"')
        self.refused('gh pr view 1 "${X:-"a"}" x\'--jq\'; ' + b)
        self.refused('gh pr view 1 "${X#"a"}" x\'--jq\'; ' + b)
        self.refused("gh pr view 1 \"${X#'a'}\" x'--jq'; " + b)
        self.refused("X=a/b; gh pr view 1 ${X//a/b} x'--jq'; " + b)

    def test_b6_stdin_flag_text_in_title_refused(self):
        self.refused("gh pr create --title '--body-file -' <<'EOF'\n" + self.brace + "\nEOF")
        self.refused("gh pr create --title '--body-file -' <<'EOF'\nfind / -name x " + self.brace + "\nEOF")

    def test_b7_eval_and_bash_c_refused(self):
        body = "gh pr comment 1 --body-file - <<'EOF'\nx\nEOF\n" + self.brace
        for pre in ("", "gh() { bash; }; ", "gh() { bash; }; export -f gh; "):
            self.refused(pre + "eval '" + body + "'")
            self.refused(pre + "bash -c '" + body + "'")

    # --- item 3: give-up and head forms ---
    def test_giveup_forms_refused(self):
        for cmd in (
            "gh pr view 1 --jq '{a,b}' # factory",
            "gh pr view 1 --jq='{a,b}' factory/x",
            "gh pr view 1 --jq '{a,b}' && git rev-list --count HEAD && factory res",
            "grep 'a{1,2}' factory/x <<'EOF'\nx\nEOF",
            "grep $'a{1,2}' factory/x",
            "grep \"$(echo x)\" 'a{1,2}' factory/x",
            "grep 'a{1,2}' `echo factory/x`",
            "grep 'a{1,2}' factory/x\nls",
            "grep 'a{1,2}' $X[0] factory/x",
            "grep 'a{1,2}' factory/x > out",
            "grep 'a{1,2}' < factory/x",
            "grep 'a{1,2}' factory/x & ls",
            "grep 'a{1,2}' factory/x |& cat",
            "grep 'a{1,2}' factory/x;",
            "grep 'a{1,2}' factory/x ;; ls",
        ):
            with self.subTest(cmd=cmd):
                self.refused(cmd)

    def test_other_heads_refused(self):
        for head in ("fgrep", "/usr/bin/grep", "./grep", "eval", "bash -c", "sh -c", "env grep", "VAR=x grep", "xargs", "cat",
                     "python3"):
            with self.subTest(head=head):
                self.refused(head + " 'a{1,2}' factory/x")
        self.refused("grep x factory/x | sh 'a{1,2}'")
        self.refused("gh api 'a{1,2}' --jq '{a,b}' factory/x")
        self.refused("gh -R a/b pr view 1 --jq '{a,b}' factory/x")
        self.refused("gh pr create --jq '{a,b}' factory/x")
        self.refused("gh pr edit 1 --jq '{a,b}' factory/x")

    def test_non_pattern_words_refused(self):
        for cmd in (
            "rg -g '{s,x}*' a . factory",
            "rg --glob '{s,x}*' a . factory",
            "rg a -g '{s,x}*' . factory",
            "grep --include='{s,x}' a . factory",
            "jq --arg k '{a,b}' . factory.json",
            "grep a 'b{1,2}' factory/x",
        ):
            with self.subTest(cmd=cmd):
                self.refused(cmd)

    def test_shell_braces_refused(self):
        for cmd in (
            "grep 'a' " + self.brace,
            "grep 'a' " + self.secrets + "/te'r'{a,b}",
            "grep 'a' " + self.secrets + "/{'a',b}",
            "grep 'a{1,2}' " + self.secrets + "/{s,x}*",
        ):
            with self.subTest(cmd=cmd):
                self.refused(cmd)

    def test_depth_stays_original(self):
        self.refused("eval \"grep -E 'a{1,2}' " + self.secrets + "/x\"")
        self.refused("bash -c \"grep -E 'a{1,2}' " + self.secrets + "/x\"")
        self.refused("eval 'grep -E \"a{1,2}\" " + self.secrets + "/x'")
        self.refused("bash -c 'grep \"a\" " + self.secrets + "/{s,x}'")

    # --- item 4: real reads stay refused ---
    def test_real_terms_reads_refused(self):
        t, d = self.terms, self.secrets
        for cmd in (
            f"grep -E 'z{{1,3}}' {t}",
            f"rg 'z{{1,3}}' {t}",
            f"jq '{{a:1,b:2}}' {t}",
            f"grep -rE 'z{{1,3}}' {d}",
            f"rg 'z{{1,3}}' {d}",
            "grep '${!x} {1,2}' factory/x",
        ):
            with self.subTest(cmd=cmd):
                self.refused(cmd)


if __name__ == "__main__":
    unittest.main()
