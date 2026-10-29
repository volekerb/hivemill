"""T-0178: the guard resolves a heredoc delimiter the way bash does, and fails closed when it cannot.
Differential: each case runs in real bash (harmless marker file in a temp dir) and through the hook as a
subprocess. Invariant: whenever bash runs the marker line, the hook must have blocked the command."""
import json
import os
import subprocess
import sys
import tempfile
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
HOOK = os.path.join(ROOT, "roles", "hooks", "worker-guard.py")
# a herdr session env assignment as a command prefix: the hook refuses it, bash just runs touch
PAYLOAD = "HERDR_" + "SESSION=x touch MARK"


def hook(cmd, hq=False):
    argv = [sys.executable, HOOK] + (["--hq"] if hq else [])
    payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}})
    return subprocess.run(argv, input=payload, capture_output=True, text=True, timeout=30).returncode


def bash_runs_marker(cmd, prelude=""):
    with tempfile.TemporaryDirectory() as d:
        subprocess.run(["bash", "-c", prelude + cmd], cwd=d, stdin=subprocess.DEVNULL, capture_output=True, timeout=30)
        return os.path.exists(os.path.join(d, "MARK"))


def case(opener, *body):
    """The command line `opener`, the heredoc body lines (the last one ends it in bash), then PAYLOAD."""
    return "\n".join([opener, *body, PAYLOAD, "tail"]) + "\n"


# bash ends the heredoc at the last body line, then runs PAYLOAD
RUNS_IN_BASH = [
    ("reviewer repro E$F", case('cat <<"E\\$F"', "x", "E$F")),
    ("reviewer repro E\\\\F", case('cat <<"E\\\\F"', "x", "E\\F")),
    ("reviewer repro E`F", case('cat <<"E\\`F"', "x", "E`F")),
    ("partial quote", case("cat <<'E'OF", "x", "EOF")),
    ("partial dquote", case('cat <<E"O"F', "x", "EOF")),
    ("backslash escaped", case("cat <<\\EOF", "x", "EOF")),
    ("escaped mid word", case("cat <<E\\OF", "x", "EOF")),
    ("single quoted backslash", case("cat <<'E\\F'", "x", "E\\F")),
    ("dquote escaped quote", case('cat <<"E\\"F"', "x", 'E"F')),
    ("dash tab terminator", case("cat <<-'EOF'", "x", "\t\tEOF")),
    ("dash plain", case("cat <<-EOF", "x", "\tEOF")),
    ("two heredocs", case("cat <<'A' <<'B'", "a", "A", "b", "B")),
    ("space before word", case("cat << 'EOF'", "x", "EOF")),
    ("dquote dollar", case('cat <<"E$F"', "x", "E$F")),
    ("nested comsub in word", case("cat <<$(a b)", "x", "$(a b)")),
    ("nested param expansion in word", case("cat <<${a:-b c}", "x", "${a:-b c}")),
    ("nested backtick in word", case("cat <<`a b`", "x", "`a b`")),
    ("dquoted comsub in word", case('cat <<"$(a b)"', "x", "$(a b)")),
    ("dquoted backtick in word", case('cat <<"`a b`"', "x", "`a b`")),
    ("backslash-newline joins terminator", case("cat <<EOF", "x", "EO\\", "F")),
    ("backslash-newline joins terminator in dash", case("cat <<-EOF", "x", "\tEO\\", "F")),
    ("partial quote then lookalike", case("cat <<'E'OF", "EOF ", "E'OF", "EOF")),
]

# the real terminator comes after lookalike lines; the payload after it is code in bash
LOOKALIKES = [
    ("trailing space", case("cat <<'EOF'", "x", "EOF ", "more", "EOF")),
    ("leading space", case("cat <<'EOF'", "x", " EOF", "more", "EOF")),
    ("longer word", case("cat <<'EOF'", "x", "EOFX", "more", "EOF")),
    ("other case", case("cat <<'EOF'", "x", "eof", "more", "EOF")),
    ("dash with spaces", case("cat <<-'EOF'", "x", "  EOF", "more", "EOF")),
    ("quoted trailing space", case("cat <<'EOF '", "x", "EOF", "EOF ")),
    ("quoted delimiter does not join backslash-newline", case("cat <<'EOF'", "x", "EO\\", "F", "more", "EOF")),
    ("even backslashes do not join", case("cat <<EOF", "x", "EO\\\\", "more", "EOF")),
    ("second delimiter inside first body", case("cat <<'A' <<'B'", "a", "B", "A", "b", "B")),
]

# legitimate heredocs: bash runs nothing extra and the hook keeps allowing them
BENIGN = [
    "cat <<EOF\nhello\nEOF\n",
    "cat <<'EOF'\nhello $(date) `x`\nEOF\n",
    'cat <<"EOF"\nhello\nEOF\n',
    "cat <<-EOF\n\thello\n\tEOF\n",
    'cat <<"E\\$F"\nhello\nE$F\n',
    "cat <<'E'OF\nhello\nEOF\n",
    "cat <<\\EOF\nhello\nEOF\n",
    "cat <<'A' <<'B'\na\nA\nb\nB\n",
]


class HeredocDifferential(unittest.TestCase):
    def test_bash_available(self):
        self.assertTrue(bash_runs_marker("touch MARK"))
        self.assertFalse(bash_runs_marker("true"))

    def test_hook_blocks_whatever_bash_runs(self):
        for name, cmd in RUNS_IN_BASH:
            with self.subTest(name):
                self.assertTrue(bash_runs_marker(cmd), f"corpus case must make bash run the marker: {cmd!r}")
                self.assertEqual(hook(cmd), 2, f"bash runs the marker but the hook allowed: {cmd!r}")

    def test_hq_mode_blocks_privilege_word_bash_runs(self):
        # HQ runs the privilege pass only. In the bash probe the privilege word is a shell function that
        # touches the marker, so nothing real runs.
        prelude = "su" + "do() { touch MARK; }\n"
        for name, cmd in RUNS_IN_BASH + LOOKALIKES:
            cmd = cmd.replace(PAYLOAD, "su" + "do -n true")
            with self.subTest(name):
                self.assertTrue(bash_runs_marker(cmd, prelude), f"bash must run the privilege line: {cmd!r}")
                self.assertEqual(hook(cmd, hq=True), 2, f"bash runs the privilege line but the hook allowed: {cmd!r}")

    def test_lookalike_terminators_do_not_end_the_body(self):
        for name, cmd in LOOKALIKES:
            with self.subTest(name):
                self.assertTrue(bash_runs_marker(cmd), f"corpus case must make bash run the marker: {cmd!r}")
                self.assertEqual(hook(cmd), 2, f"bash runs the marker but the hook allowed: {cmd!r}")

    def test_benign_heredocs_stay_allowed(self):
        for cmd in BENIGN:
            with self.subTest(cmd):
                self.assertFalse(bash_runs_marker(cmd))
                self.assertEqual(hook(cmd), 0, f"legitimate heredoc was blocked: {cmd!r}")

    def test_unresolvable_forms_fail_closed(self):
        for name, cmd in (
            ("ansi-c quoting", f"cat <<$'EOF'\nx\nEOF\n{PAYLOAD}\n"),
            ("backslash newline", f"cat <<E\\\nOF\nx\nEOF\n{PAYLOAD}\n"),
            ("unterminated quote", f"cat <<'EOF\nx\nEOF\n{PAYLOAD}\n"),
            ("missing word", f"cat <<\n{PAYLOAD}\n"),
        ):
            with self.subTest(name):
                self.assertEqual(hook(cmd), 2, f"unresolvable delimiter must be scanned as code: {cmd!r}")


def lines(*parts):
    return "\n".join(parts) + "\n"


# T-0178 attempt 3 (reviewer B3): bash feeds the inner shell the body with backslash-newline joined; every
# line of it is scanned. Each body line that bash runs touches the marker.
SHELL_FED = [
    ("bash heredoc, bare backslash line", lines("bash <<EOF", "\\", PAYLOAD, "EOF")),
    ("bash -s heredoc", lines("bash -s <<EOF", ":\\", "", PAYLOAD, "EOF")),
    ("sh heredoc", lines("sh <<EOF", "\\", PAYLOAD, "EOF")),
    ("bash heredoc, command continued", lines("bash <<EOF", "true \\", "; " + PAYLOAD, "EOF")),
    ("bash heredoc, three raw lines", lines("bash <<EOF", ":\\", "\\", "; " + PAYLOAD, "EOF")),
    ("bash heredoc, joined terminator lookalike", lines("bash <<EOF", ":", "EO\\", "F", PAYLOAD)),
]


class ShellFedBodies(unittest.TestCase):
    def check_both_modes(self, name, cmd, hq_probe=True):
        self.assertTrue(bash_runs_marker(cmd), f"{name}: bash must run the marker: {cmd!r}")
        self.assertEqual(hook(cmd), 2, f"{name}: worker mode allowed: {cmd!r}")
        priv = cmd.replace(PAYLOAD, "su" + "do -n true")
        # dash (sh) does not import the exported function, so the bash probe is skipped for the sh case
        self.assertTrue(not hq_probe or bash_runs_marker(priv, "su" + "do() { touch MARK; }\nexport -f su" + "do\n"), f"{name}: bash must run it: {priv!r}")
        self.assertEqual(hook(priv, hq=True), 2, f"{name}: hq mode allowed: {priv!r}")

    def test_shell_fed_continuation_lines_are_scanned(self):
        for name, cmd in SHELL_FED:
            with self.subTest(name):
                self.check_both_modes(name, cmd, hq_probe=not cmd.startswith("sh "))

    def test_joined_terminator_ends_body_and_next_lines_are_code(self):
        # EO + backslash, then F ends the <<EOF body; PAYLOAD after it is plain code
        for name, cmd in (("cat", lines("cat <<EOF", "x", "EO\\", "F", PAYLOAD)),
                          ("python3", lines("python3 - <<EOF", "x = 1", "EO\\", "F", PAYLOAD))):
            with self.subTest(name):
                self.check_both_modes(name, cmd)

    def test_interpreter_word_split_by_continuation_is_joined(self):
        word = "her" + "dr"
        cmd = lines("python3 - <<EOF", "import os", f"os.system('{word[:3]}\\", f"{word[3:]} x')", "EOF")
        self.assertEqual(hook(cmd), 2, f"word split across backslash-newline in an unquoted interpreter body: {cmd!r}")
        quoted = cmd.replace("<<EOF", "<<'EOF'")
        benign = lines("python3 - <<EOF", "x = 1", "y = \\", "2", "EOF")
        # a quoted delimiter does not join, so the split word is not one word: same result as main (allowed)
        self.assertEqual(hook(quoted), 0, f"quoted delimiter body is data, as on main: {quoted!r}")
        self.assertEqual(hook(benign), 0, f"benign continuation was blocked: {benign!r}")

    def test_unresolved_delimiter_lines_are_code(self):
        self.check_both_modes("unresolved", lines("cat <<$'EOF'", "x", "EOF", PAYLOAD))

    def test_heredoc_in_double_quoted_comsub_with_continuation(self):
        self.check_both_modes("comsub", lines('x="$(bash <<EOF', "\\", PAYLOAD, "EOF", ')"'))

    def test_unterminated_heredoc_takes_rest_as_body(self):
        self.check_both_modes("unterminated", lines("bash <<EOF", "\\", PAYLOAD))
        self.assertEqual(hook(lines("cat <<EOF", "x", "y")), 0)

    def test_two_heredocs_first_continuation_second_shell_fed(self):
        self.check_both_modes("two heredocs", lines("cat <<A; bash <<B", "a\\", "b", "A", PAYLOAD, "B"))


SUDO = "su" + "do id"


NLS = "\n"
MUST_BLOCK = [
    f"$( (true)#){NLS}{SUDO})", f"$({{ true;}}#){NLS}{SUDO})",
    f"$(case a in a) {SUDO};; esac)", f"$(echo $'\\')' ; {SUDO})", f'$(echo "\\")" ; {SUDO})',
    f'$(echo "$(echo ")")" ; {SUDO})', f"$(echo `echo )` ; {SUDO})",
    f"$(echo ${{x/)/y}} ; {SUDO})", f"$(echo ${{x#)}} ; {SUDO})", f"$(echo ${{x:-)}} ; {SUDO})",
    f"$(cat <<X{NLS}){NLS}X{NLS}{SUDO})",
    f"$((echo hi); {SUDO})", f'$(echo $"x" ; {SUDO})',
]
MUST_ALLOW = [
    'cat <<EOF\n$(git rev-parse --short HEAD) $(date +%s%N) "$x" ${HOME} $(( 1 + 2 ))\nEOF',
    "cat <<EOF\nok $(echo 'a)b;c' \"d e\") $(basename $(pwd))\nEOF",
    "cat <<EOF\n${x}# and it's fine\nEOF",
    "cat <<EOF\nprose (with parens) it's a # note, ok\nEOF",
    "cat <<'EOF'\n$(echo ${x/)/y} ; case a in a) b;; esac) `x` {\nEOF",
]


class SubstitutionGaps(unittest.TestCase):
    """T-0199: `<<` in arithmetic is an operator, `EOF)` ends a heredoc inside $( ), and an unquoted heredoc
    body is scanned for $( ) and backticks (bash runs them). Both modes must block the privilege word."""

    def both(self, cmd, want):
        self.assertEqual(hook(cmd), want, f"worker mode: {cmd!r}")
        self.assertEqual(hook(cmd, hq=True), want, f"hq mode: {cmd!r}")

    def test_g1_arithmetic_shift_then_guarded_line(self):
        for cmd in (f"echo $((1<<2))\n{SUDO}", f"x=$((1<<EOF))\n{SUDO}", f"(( x = 1<<2 ))\n{SUDO}",
                    f"let x=1<<2\n{SUDO}", f"for ((i=0;i<1<<2;i++)); do :; done\n{SUDO}"):
            with self.subTest(cmd):
                self.both(cmd, 2)

    def test_g2_terminator_inside_comsub(self):
        self.both(f"y=$(cat <<EOF\nhi\nEOF)\n{SUDO}", 2)
        self.both(f"y=$(cat <<-EOF\n\thi\n\tEOF)\n{SUDO}", 2)

    def test_g3_unquoted_body_is_expanded(self):
        for cmd in (f"cat <<EOF\n`{SUDO}`\nEOF", f"cat <<EOF\n$({SUDO})\nEOF", f"cat <<-EOF\n\t$({SUDO})\n\tEOF",
                    f"cat <<EOF\n$(( $({SUDO}) ))\nEOF", f"cat <<EOF\n${{x:-$({SUDO})}}\nEOF"):
            with self.subTest(cmd):
                self.both(cmd, 2)

    def test_self_review_shapes(self):
        # let / (( )) after a keyword, brace or wrapper; DELIM) after a case pattern or inside <( )
        for cmd in (f"if true; then let x=1<<EOF; fi\n{SUDO}\nEOF", f"{{ let x=1<<EOF; }}\n{SUDO}\nEOF",
                    f"{{ ((x=1<<EOF)); }}\n{SUDO}\nEOF", f"time let x=1<<EOF\n{SUDO}\nEOF",
                    f"echo $(case a in a) true;; esac; cat <<EOF\nx\nEOF)\n{SUDO}\nEOF",
                    f"cat <(cat <<EOF\nx\nEOF)\n{SUDO}\nEOF"):
            with self.subTest(cmd):
                self.both(cmd, 2)

    def test_review_b1_terminator_then_space_or_tab_paren(self):
        # bash ends the heredoc at `EOF )` / `EOF<TAB>)` inside $( ); the code after it runs
        for term in ("EOF )", "EOF\t)", "EOF  \t )"):
            with self.subTest(term):
                self.both(f"x=$(cat <<EOF\nhi\n{term}\n{SUDO}\n)", 2)

    def test_review_b2_paren_in_comment_inside_comsub(self):
        # a `)` inside a comment in $( ) closes nothing; the next line is still code
        self.both(f"cat <<EOF\n$(# )\n{SUDO})\nEOF", 2)
        self.both(f"cat <<EOF\n$(echo a # )\n{SUDO})\nEOF", 2)

    def test_review_b2_hash_after_close_paren_or_brace_is_a_comment(self):
        # bash starts a comment after `)` and `}` too, so the `)` in it closes nothing and the next line is code
        for cmd in (f"cat <<EOF\n$( (true)#)\n{SUDO})\nEOF", f"cat <<EOF\n$({{ true;}}#)\n{SUDO})\nEOF"):
            with self.subTest(cmd):
                self.both(cmd, 2)

    def test_review_b3_close_paren_scan_fails_closed(self):
        # case patterns, $'..', a backslash or $( ) inside "..", and backticks hide or move the closing paren
        shapes = ["$(case a in a) %s;; esac)", "$(echo $'\\')' ; %s)", '$(echo "\\")" ; %s)',
                  '$(echo "$(echo ")")" ; %s)', "$(echo `echo )` ; %s)"]
        for shape in shapes:
            cmd = "cat <<EOF\n" + shape % SUDO + "\nEOF"
            with self.subTest(cmd):
                self.both(cmd, 2)

    def test_hash_after_param_expansion_is_not_a_comment(self):
        # `${x}#` is a word, so this `)` still closes the substitution and the heredoc body is plain data
        self.both("cat <<EOF\n$(echo ${x}#)\nEOF", 0)
        self.both("cat <<EOF\n$(echo $x#b)\nEOF", 0)

    def test_hash_inside_word_is_not_a_comment(self):
        self.both("cat <<EOF\n$(echo a#b)\nEOF", 0)

    def test_same_line_still_blocked(self):
        self.both(f"echo $((1<<2)); {SUDO}", 2)

    def test_quoted_and_escaped_bodies_stay_data(self):
        for cmd in (f"cat <<'EOF'\n`{SUDO}` and $({SUDO})\nEOF", f'cat <<"EOF"\n$({SUDO})\nEOF',
                    f"cat <<\\EOF\n$({SUDO})\nEOF", f"cat <<EOF\n\\$({SUDO}) \\`{SUDO}\\`\nEOF"):
            with self.subTest(cmd):
                self.assertEqual(hook(cmd), 0, f"data body was blocked: {cmd!r}")

    def test_no_false_positives(self):
        for cmd in ("t0=$(date +%s%N)", "a=1 b=$(date +%s) true", "echo $((1<<2))", "echo $((1<<2)) x",
                    "git commit -m \"$(cat <<'EOF'\nmsg\nEOF\n)\"",
                    "cat <<EOF\nplain $(date +%s) and `hostname`\nEOF",
                    "y=$(cat <<EOF\nhi\nEOF\n)\necho done"):
            with self.subTest(cmd):
                self.both(cmd, 0)

    def test_v2_must_block_shapes(self):
        """T-0587: every unmodelled or hostile shape in an unquoted body is rc 2 in worker and hq mode."""
        for shape in MUST_BLOCK:
            with self.subTest(shape):
                self.both(f"cat <<EOF\n{shape}\nEOF", 2)

    def test_v2_must_block_shapes_really_run_in_bash(self):
        """The shapes are not hypothetical: bash runs the marker line (harmless touch in a scratch dir)."""
        for shape in MUST_BLOCK:
            if shape.startswith("$({"):
                continue  # `}#` is one word in bash 5.2 (syntax error, nothing runs); blocked anyway, fail closed
            cmd = f"cat <<EOF\n{shape.replace(SUDO, 'touch MARK')}\nEOF"
            with self.subTest(shape):
                self.assertTrue(bash_runs_marker(cmd), f"bash did not run the marker for {cmd!r}")

    def test_v2_must_allow_shapes(self):
        for cmd in MUST_ALLOW:
            with self.subTest(cmd):
                self.both(cmd, 0)

    def test_v2_unmodelled_shape_prints_the_message_and_workaround(self):
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": "cat <<EOF\n$(echo ${x/)/y})\nEOF"}})
        for argv in ([], ["--role", "coder"], ["--hq"]):
            r = subprocess.run([sys.executable, HOOK] + argv, input=payload, capture_output=True, text=True, timeout=30)
            self.assertEqual(r.returncode, 2)
            self.assertIn("unquoted heredoc body has a $( ) the guard cannot parse; quote the delimiter (<<'EOF') "
                          "or compute the value before the heredoc", r.stderr)

    def test_v2_arithmetic_shift_in_body_fails_closed(self):
        self.both("cat <<EOF\n$((1<<2))\nEOF", 2)

    def test_v2_structure(self):
        with open(HOOK) as f:
            src = f.read()
        self.assertNotIn("unknown_end", src)
        self.assertIn("class HeredocBlock(Block)", src)

    def test_unbalanced_body_fails_closed(self):
        self.assertEqual(hook("cat <<EOF\n$(date\nEOF"), 2)
        self.assertEqual(hook("cat <<EOF\n`date\nEOF"), 2)


if __name__ == "__main__":
    unittest.main()
