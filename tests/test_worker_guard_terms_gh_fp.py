"""T-0942: the owner-terms check no longer refuses (1) a single-quoted gh --jq/-q/--template value holding a comma brace
next to the word "factory", and (2) the body of a quoted-delimiter heredoc feeding gh. Everything else stays refused.
Through the real hook with a temp FACTORY_HOME/DB and a fixture terms file in a directory named "factory"."""
import json
import os
import shutil
import sqlite3
import tempfile
import unittest
from guard_impl import hook_run

GUARD_IMPL_PYTHON_ONLY = "Go mirror follow-up in G-016"

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
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

    def bash(self, cmd, role="coder"):
        env = {k: v for k, v in os.environ.items() if k != "SCRUB_TERMS_FILE"}
        env.update(FACTORY_HOME=self.home, HOME=self.tmp, SCRUB_TERMS_FILE=self.terms)
        payload = {"cwd": self.tmp, "tool_name": "Bash", "tool_input": {"command": cmd}}
        return hook_run(json.dumps(payload), ["--role", role], env=env, timeout=60)

    def owner_terms_refusal(self, p):
        return p.returncode == 2 and "owner-terms" in p.stderr

    def allowed(self, cmd):
        for role in ROLES:
            p = self.bash(cmd, role)
            self.assertFalse(self.owner_terms_refusal(p), f"{role} must not hit the owner-terms rule: {cmd}\n{p.stderr}")

    def refused(self, cmd):
        for role in ("coder", "reviewer"):
            p = self.bash(cmd, role)
            self.assertTrue(self.owner_terms_refusal(p), f"{role} should be refused by owner-terms: {cmd}\nrc={p.returncode} {p.stderr}")
            self.assertNotIn("zebracorp", p.stderr)

    # --- FP1: gh --jq / -q / --template braces ---
    def test_gh_jq_single_quoted_braces_allowed(self):
        self.allowed("gh pr list --head factory/t-1 --json a,b --jq '.[]|{a,b}'")
        self.allowed("gh pr view 1 --json a,b --jq '{a,b}' # factory")

    def test_gh_q_and_template_and_equals_forms_allowed(self):
        self.allowed("gh pr view 1 --json a,b -q '{a,b}' # factory")
        self.allowed("gh pr view 1 --json a,b --template '{a,b}' # factory")
        self.allowed("gh pr view 1 --json a,b --jq='{a,b}' # factory")
        self.allowed("cd factory && gh pr view 1 --json a,b --jq '{a,b}'")

    def test_brace_forms_still_refused(self):
        self.refused("cat ~/.config/factory/{s,x}*")
        self.refused("python3 -c 'print(\"{a,b}\")' # factory")
        self.refused("xargs sh -c 'cat {a,b}' # factory")
        self.refused('gh pr view 1 --jq "{a,b}" # factory')
        self.refused("gh pr view 1 --jq '{a,b}' ; cat ~/.config/factory/{s,x}*")
        self.refused("gh pr view 1 --jq '{a,b}'{c,d} # factory")
        self.refused("gh pr view 1 --body \"--jq '{a,b}'\" # factory")

    # --- FP2: gh quoted-delimiter heredoc bodies ---
    def test_gh_quoted_heredoc_body_not_scanned(self):
        for body in ("find / -name x", "tar cf x.tar ~", "rg x ~/.config", "tar is refused"):
            self.allowed(f"gh pr comment 324 --body-file - <<'EOF'\n{body}\nEOF")
        self.allowed("cd ~ && gh pr comment 324 --body-file - <<'EOF'\ntar is refused\nEOF")
        self.allowed('gh pr comment 324 --body-file - <<"EOF"\nfind / -name x\nEOF')
        self.allowed("gh pr comment 324 --body-file - <<-'EOF'\n\tfind / -name x\n\tEOF")

    def test_heredoc_not_to_gh_or_unquoted_still_scanned(self):
        self.refused("bash <<'EOF'\ncat $SCRUB_TERMS_FILE\nEOF")
        self.refused(f"python3 - <<'EOF'\nopen('{self.terms}').read()\nEOF")
        self.refused("gh pr comment 1 --body-file - <<EOF\n$(cat $SCRUB_TERMS_FILE)\nEOF")
        self.refused("gh pr comment 1 --body-file - <<'EOF'\nx\nEOF\n; cat $SCRUB_TERMS_FILE")
        self.refused("gh pr comment 1 --body-file - <<'EOF'; cat $SCRUB_TERMS_FILE\nx\nEOF")
        self.refused("cat <<'EOF' | gh pr comment 1 --body-file -\nfind / -name x\nEOF")

    def test_path_named_gh_is_not_trusted(self):
        self.refused("./gh pr comment 1 --body-file - <<'EOF'\ncat $SCRUB_TERMS_FILE\nEOF")
        self.refused("/tmp/x/gh pr view 1 --jq '{a,b}' # factory")

    def test_heredoc_skip_is_not_fooled_by_quotes_or_comments(self):
        self.refused("gh pr comment 1 -b \"<<'EOF'\"\ncat $SCRUB_TERMS_FILE\nEOF")
        self.refused("gh pr comment 1 -b x # <<'EOF'\ncat $SCRUB_TERMS_FILE\nEOF")
        self.refused("gh pr comment 1 --body-file - <<'EOF'\ncat $SCRUB_TERMS_FILE\n")  # unterminated: body stays scanned
        self.refused("gh pr comment 1 --body-file - <<'A' <<'B'\nx\nA\ncat $SCRUB_TERMS_FILE\nB")

    # --- T-0942 attempt 2: a line the shell runs as a command stays scanned (PR 435 comment 6657905109) ---
    def scanned(self, cmd):
        """The line must still reach the owner-terms scan: coder gets the owner-terms refusal; reviewer is refused (it may be
        another rule first, e.g. for a PATH assignment)."""
        p = self.bash(cmd, "coder")
        self.assertTrue(self.owner_terms_refusal(p), f"coder should be refused by owner-terms: {cmd!r}\nrc={p.returncode} {p.stderr}")
        p = self.bash(cmd, "reviewer")
        self.assertEqual(p.returncode, 2, f"reviewer should be refused: {cmd!r}\n{p.stderr}")

    def test_b1_here_string_stays_scanned(self):
        self.scanned("gh pr view 1 <<<'EOF'\nfind / -name x\nEOF")
        self.scanned('gh pr view 1 <<<"EOF"\nfind / -name x\nEOF')

    def test_b1_delimiter_suffix_stays_scanned(self):
        self.scanned("gh pr view 1 <<'EOF'X\nEOFX\nfind / -name x\nEOF")
        self.scanned("gh pr view 1 <<\\EOF'X'\nEOFX\nfind / -name x\nEOF")

    def test_b1_ansi_c_string_stays_scanned(self):
        self.scanned("gh pr comment 1 -b $'\\'<<\\EOF' #'\nfind / -name x\nEOF")

    def test_b2_redefined_gh_stays_scanned(self):
        self.scanned("gh() { bash; }; gh pr view 1 <<'EOF'\nfind / -name x\nEOF")
        self.scanned("function gh { bash; }\ngh pr view 1 <<'EOF'\nfind / -name x\nEOF")
        self.scanned("alias gh=bash\ngh pr view 1 <<'EOF'\nfind / -name x\nEOF")

    def test_b2_path_change_stays_scanned(self):
        self.scanned("PATH=/tmp/x gh pr view 1 <<'EOF'\nfind / -name x\nEOF")
        self.scanned("export PATH=/tmp/x\ngh pr view 1 <<'EOF'\nfind / -name x\nEOF")
        self.scanned("PATH=/tmp/x\ngh pr view 1 <<'EOF'\nfind / -name x\nEOF")

    def test_b2_other_redefinition_stays_scanned(self):
        for pre in ("hash -p /tmp/x/gh gh", "source /tmp/x.sh", ". /tmp/x.sh", "eval 'gh() { bash; }'"):
            self.scanned(f"{pre}\ngh pr view 1 <<'EOF'\nfind / -name x\nEOF")

    def test_other_path_setters_and_arithmetic_shift_stay_scanned(self):
        self.scanned("printf -v PATH /tmp/x\ngh pr view 1 <<'EOF'\nfind / -name x\nEOF")
        self.scanned("mapfile PATH < /dev/null\ngh pr view 1 <<'EOF'\nfind / -name x\nEOF")
        self.scanned("gh x $[1<<\"2\" ]\nfind / -name x\n2")

    def test_plain_quoted_gh_heredoc_still_allowed(self):
        self.allowed("gh pr comment 324 --body-file - <<'EOF'\nfind / -name x\nEOF")
        self.allowed("gh pr comment 324 --body-file - <<'EOF' # note\nfind / -name x\nEOF")
        self.allowed("gh pr comment 324 --body-file - <<'EOF';\nfind / -name x\nEOF")


if __name__ == "__main__":
    unittest.main()
