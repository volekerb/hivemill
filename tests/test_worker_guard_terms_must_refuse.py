"""T-1097 part 2: forms from closed PR 435 (T-0942) that the owner-terms check refuses on main and must keep refusing,
so a later narrowing of the gh quoted-heredoc rule (T-1121) cannot quietly open them. No hook change here.
Through the real hook with a temp FACTORY_HOME/DB and a temp terms fixture in a directory named "factory"; the live terms
file is never read. Each form: exit 2 AND the owner-terms reason in stderr, for coder, reviewer, qa, judge and pm (B8)."""
import json
import os
import shutil
import sqlite3
import tempfile
import unittest

from guard_impl import hook_run

ROLES = ("coder", "reviewer", "qa", "judge", "pm")
GH = "gh pr comment 1 --body-file - <<'EOF'\nfind / -name x\nEOF"  # the approved gh grammar with a plain find body line
FUNC = "gh() { bash; }"
FUNC_EXPORT = "gh() { bash; }; export -f gh"


def sq(payload):
    """The payload as one single-quoted shell word."""
    return "'" + payload.replace("'", "'\\''") + "'"


def dq(payload):
    """The payload as one double-quoted shell word (no expansion characters in the payloads used here)."""
    return '"' + payload + '"'


class TermsMustRefuseTests(unittest.TestCase):
    def setUp(self):
        self.tmp = os.path.realpath(tempfile.mkdtemp(prefix="zz-test-termsmust-"))
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

    def bash(self, cmd, role):
        env = {k: v for k, v in os.environ.items() if k != "SCRUB_TERMS_FILE"}
        env.update(FACTORY_HOME=self.home, HOME=self.tmp, SCRUB_TERMS_FILE=self.terms)
        payload = {"cwd": self.tmp, "tool_name": "Bash", "tool_input": {"command": cmd}}
        return hook_run(json.dumps(payload), ["--role", role], env=env, timeout=60)

    def must_refuse(self, cmd):
        for role in ROLES:
            with self.subTest(role=role, cmd=cmd):
                p = self.bash(cmd, role)
                self.assertEqual(p.returncode, 2, p.stderr)
                self.assertIn("owner-terms", p.stderr)
                self.assertNotIn("zebracorp", p.stderr)

    # --- B5: apostrophe in a comment or a heredoc body flips a naive quote scanner; the brace expansion is real ---
    def brace_line(self):
        return "gh pr view 1 x'--jq'; cat " + self.secrets + "/{s,x}*; echo \"q\""

    def test_b5_form_a_comment_apostrophe(self):
        self.must_refuse("gh pr view 1 # it's\n" + self.brace_line())

    def test_b5_form_b_heredoc_body_apostrophe(self):
        self.must_refuse("gh pr comment 1 --body-file - <<'EOF'\nit's\nEOF\n" + self.brace_line())

    # --- B6: the stdin flag is not a real argument ---
    def test_b6_stdin_flag_text_only_inside_quoted_title(self):
        self.must_refuse("gh pr create --title '--body-file -' <<'EOF'\nfind / -name x\nEOF")
        self.must_refuse("gh pr create --title \"-F -\" <<'EOF'\nfind / -name x\nEOF")

    def test_b6_value_flag_right_before_stdin_flag(self):
        self.must_refuse("gh pr create --title --body-file - <<'EOF'\nfind / -name x\nEOF")
        self.must_refuse("gh pr create --title -F - <<'EOF'\nfind / -name x\nEOF")

    def test_b6_double_dash_before_stdin_flag(self):
        self.must_refuse("gh pr comment 1 -- --body-file - <<'EOF'\nfind / -name x\nEOF")

    # --- B7: the gh quoted heredoc inside an eval or bash -c payload ---
    def b7_forms(self):
        for pre in ("", FUNC + "\n", FUNC_EXPORT + "\n"):
            yield pre + "eval " + dq(GH)
            yield pre + "eval " + sq(GH)
            yield pre + "bash -c " + dq(GH)
            yield pre + "bash -c " + sq(GH)

    def test_b7_eval_and_bash_c_payloads(self):
        for cmd in self.b7_forms():
            self.must_refuse(cmd)

    def test_b7_control_top_level_gh_heredoc_with_find_body(self):
        self.must_refuse(GH)
        self.must_refuse(FUNC + "\n" + GH)


if __name__ == "__main__":
    unittest.main()
