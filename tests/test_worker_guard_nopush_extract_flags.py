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

GUARD_IMPL_PYTHON_ONLY = "Go mirror follow-up in G-016"

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
                    f"python3 --some-unknown-opt -m zipfile -e {s}/a.zip {s}/r/.git"], 2)

    def test_f1_f2_benign_neighbours(self):
        s = self.scratch
        self.check(["python3 -Wonce -m json.tool", f"python3 -m zipfile -l {s}/a.zip",
                    f"python3 -Wonce -m zipfile -e {s}/a.zip {s}/clean",
                    f"python3 --check-hash-based-pycs always -m zipfile -l {s}/a.zip"], 0)

    def test_f3_ar_response_file(self):
        self.check(["ar @optsfile a.a", "ar x @optsfile", "ar @opts"], 2)
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
                    f"perl -e 'symlink(\"a\",\"b\")' && unzip {self.zip} -d {s}/clean",
                    f"python3 -c'open(\"x\", \"w\")'; unzip {self.zip} -d {s}/clean",
                    f"perl -e'symlink(\"a\",\"b\")' && unzip {self.zip} -d {s}/clean",
                    f"node --eval='1' && unzip {self.zip} -d {s}/clean",
                    f"deno eval '1' && unzip {self.zip} -d {s}/clean"], 2)
        self.check([f"unzip {self.zip} -d {s}/clean"], 0)


if __name__ == "__main__":
    unittest.main()
