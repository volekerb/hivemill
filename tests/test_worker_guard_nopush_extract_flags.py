"""T-0808 (follow-up of T-0783): NO_PUSH_ROLES extraction bypasses. F1 python short options that take a value (-W, -X) before
-m; F2 long options before -m; F3 `ar @file`; F4 `python -c` code that extracts; F5 an inline interpreter writer earlier on
the line counts as a filesystem change before a tar/unzip extraction. Benign neighbours stay allowed."""
import json
import os
import shutil
import tempfile
import unittest
import zipfile
import _gitenv  # noqa: F401  (hermetic git: tests/_gitenv.py)
from guard_impl import hook_run

NO_PUSH = ("qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator")


class ExtractFlagTests(unittest.TestCase):
    def setUp(self):
        self.tmp = os.path.realpath(tempfile.mkdtemp(prefix="zz-test-extractflags-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.home = os.path.join(self.tmp, "live")
        self.scratch = os.path.join(self.tmp, "scratch")
        os.makedirs(self.home)
        os.makedirs(os.path.join(self.scratch, "r", ".git"))  # a target that already holds .git
        os.makedirs(os.path.join(self.scratch, "clean"))
        self.zip = os.path.join(self.scratch, "a.zip")
        with zipfile.ZipFile(self.zip, "w") as z:
            z.writestr("f", "x")

    def run_cmd(self, cmd, role):
        env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "WORKER_GUARD_SCRATCH")}
        env.update(WORKER_GUARD_LIVE_HOME=self.home, WORKER_GUARD_SCRATCH=self.scratch)
        payload = {"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": self.scratch}
        return hook_run(json.dumps(payload), ["--role", role], env=env)

    def check(self, cmds, want):
        for role in NO_PUSH:
            for c in cmds:
                with self.subTest(role=role, cmd=c):
                    p = self.run_cmd(c, role)
                    self.assertEqual(p.returncode, want, f"want {want} ({role}): {c!r}\n{p.stderr}")

    def test_f1_short_option_values_before_m(self):
        s = self.scratch
        self.check([f"python3 -Wonce -m zipfile -e {s}/a.zip {s}/r/.git",
                    f"python3 -Xpycache_prefix=x -m zipfile -e {s}/a.zip {s}/r/.git",
                    f"python3 -W once -m zipfile -e {s}/a.zip {s}/r/.git",
                    f"python3 -X dev -m zipfile -e {s}/a.zip {s}/r/.git"], 2)

    def test_f2_long_options_before_m(self):
        s = self.scratch
        self.check([f"python3 --check-hash-based-pycs always -m zipfile -e {s}/a.zip {s}/r/.git",
                    f"python3 --check-hash-based-pycs=always -m zipfile -e {s}/a.zip {s}/r/.git",
                    f"python3 --some-unknown-opt -m zipfile -e {s}/a.zip {s}/r/.git"], 2)

    def test_b1_empty_long_option_value(self):
        """`--opt=` carries its own empty value; it must not swallow the next argument."""
        s, z, h = self.scratch, self.zip, "--check-hash-based-pycs="
        self.check([f"python3 {h} -m zipfile -e {z} {s}/r/.git",
                    f"python3 {h} -c 'import zipfile; zipfile.ZipFile(\"a.zip\").extractall(\".\")'",
                    f"python3 {h} -c 'open(\"x\",\"w\")'; unzip {z} -d {s}/clean",
                    f"python3 {h} -c x; unzip {z} -d {s}/clean",
                    f"python3 {h} -Bc x; unzip {z} -d {s}/clean"], 2)
        self.check([f"python3 {h} x.py; unzip {z} -d {s}/clean"], 0)

    def test_b2_unicode_whitespace_before_paren(self):
        """Python's unicode \\s: U+2028, U+001C, U+0085, U+00A0 count as whitespace; U+200B does not."""
        self.check([f"python3 -c 'z.extract{c}(1)'" for c in ("\u2028", "\x1c", "\x85", "\xa0")], 2)
        self.check(["python3 -c 'z.extract\u200b(1)'"], 0)

    def test_f1_f2_benign_neighbours(self):
        s = self.scratch
        self.check(["python3 -Wonce -m json.tool", "python3 --check-hash-based-pycs=always -m json.tool", f"python3 -m zipfile -l {s}/a.zip",
                    f"python3 -Wonce -m zipfile -e {s}/a.zip {s}/clean",
                    f"python3 --check-hash-based-pycs always -m zipfile -l {s}/a.zip"], 0)

    def test_f3_ar_response_file(self):
        self.check(["ar @optsfile a.a", "ar x @optsfile", "ar @opts", "ar --target @opts a.a", "ar --plugin @opts a.a",
                    "ar --output @opts a.a"], 2)
        self.check(["ar t a.a"], 0)

    def test_f4_python_c_extraction(self):
        s = self.scratch
        self.check([f"python3 -c \"import zipfile; zipfile.ZipFile('{s}/a.zip').extractall('{s}/r/.git')\"",
                    "python3 -c 'import tarfile; tarfile.open(\"a.tar\").extract(\"x\")'",
                    "python3 -c 'import shutil; shutil.unpack_archive(\"a.zip\", \"r/.git\")'",
                    "python -c 'import zipfile as z; z.ZipFile(\"a.zip\").extractall(\"o\")'"], 2)
        self.check(["python3 -c 'print(1)'"], 0)

    def test_f5_inline_writer_before_extraction(self):
        s = self.scratch
        self.check([f"python3 -c 'import os; os.symlink(\"{s}/r/.git\", \"{s}/clean/l\")' && unzip {self.zip} -d {s}/clean",
                    f"python3 -c 'open(\"x\", \"w\")'; unzip {self.zip} -d {s}/clean",
                    f"python3 -c'open(\"x\", \"w\")'; unzip {self.zip} -d {s}/clean"], 2)
        self.check([f"unzip {self.zip} -d {s}/clean"], 0)

    def test_f5_inline_program_without_option(self):
        s, z = self.scratch, self.zip
        self.check([f"python3 - <<'E'\nopen('x', 'w')\nE\nunzip {z} -d {s}/clean",
                    f"python3 <<'E'\nopen('x', 'w')\nE\nunzip {z} -d {s}/clean",
                    f"echo x | python3; unzip {z} -d {s}/clean",
                    f"python3 -Bc 'open(\"x\", \"w\")'; unzip {z} -d {s}/clean"], 2)

    def test_f5_benign_option_values_allowed(self):
        s, z = self.scratch, self.zip
        self.check([f"python3 -Wonce -m json.tool f ; unzip {z} -d {s}/clean",
                    f"python3 -Wignore -m json.tool f ; unzip {z} -d {s}/clean",
                    f"python3 -Xdev -m json.tool f ; unzip {z} -d {s}/clean",
                    f"node -type x ; unzip {z} -d {s}/clean"], 0)

    def test_b1_explicit_stdin_program_non_python(self):
        s, z = self.scratch, self.zip
        self.check([f"node - <<'E'\n1\nE\nunzip {z} -d {s}/clean",
                    f"ruby - <<'E'\n1\nE\nunzip {z} -d {s}/clean",
                    f"lua - <<'E'\n1\nE\nunzip {z} -d {s}/clean",
                    f"php - <<'E'\n1\nE\nunzip {z} -d {s}/clean",
                    f"node <<'E'\n1\nE\nunzip {z} -d {s}/clean",
                    f"echo 1 | ruby; unzip {z} -d {s}/clean"], 2)

    def test_non_python_option_forms_not_tightened(self):
        """Same as origin/main: refiled to T-0953."""
        s, z = self.scratch, self.zip
        self.check([f"{c} ; unzip {z} -d {s}/clean" for c in (
            "node x.js -c", "node -r x.js", "ruby -c x.rb", "ruby -E utf-8 x.rb", "node -print x",
            "node -e code", "php -r code")], 0)

    def test_b2_version_and_help_run_no_program(self):
        s, z = self.scratch, self.zip
        self.check([f"python3 {o} ; unzip {z} -d {s}/clean" for o in ("--version", "-V", "-h", "--help")], 0)


if __name__ == "__main__":
    unittest.main()
