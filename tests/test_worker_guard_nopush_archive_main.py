"""T-0945 (follow-up of T-0808 review): `python3 -m zipfile.__main__` / `-m tarfile.__main__` run the same CLI as
`-m zipfile` / `-m tarfile`, so NO_PUSH_ROLES extraction into a .git target is refused; listing and other modules stay allowed."""
import json
import os
import shutil
import tempfile
import unittest
import _gitenv  # noqa: F401  (hermetic git: tests/_gitenv.py)
from guard_impl import hook_run

GUARD_IMPL_PYTHON_ONLY = "Go mirror follow-up in G-016"

NO_PUSH = ("qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator")


class ArchiveMainTests(unittest.TestCase):
    def setUp(self):
        self.tmp = os.path.realpath(tempfile.mkdtemp(prefix="zz-test-archivemain-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.home = os.path.join(self.tmp, "live")
        self.scratch = os.path.join(self.tmp, "scratch")
        os.makedirs(self.home)
        os.makedirs(os.path.join(self.scratch, "r", ".git"))
        os.makedirs(os.path.join(self.scratch, "clean"))

    def check(self, cmds, want):
        env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "WORKER_GUARD_SCRATCH")}
        env.update(WORKER_GUARD_LIVE_HOME=self.home, WORKER_GUARD_SCRATCH=self.scratch)
        for role in NO_PUSH:
            for c in cmds:
                with self.subTest(role=role, cmd=c):
                    payload = {"tool_name": "Bash", "tool_input": {"command": c}, "cwd": self.scratch}
                    p = hook_run(json.dumps(payload), ["--role", role], env=env)
                    self.assertEqual(p.returncode, want, f"want {want} ({role}): {c!r}\n{p.stderr}")

    def test_zipfile_main_extract_into_git_refused(self):
        s = self.scratch
        self.check([f"python3 -m zipfile.__main__ -e {s}/a.zip {s}/r/.git",
                    f"python3 -Wonce -m zipfile.__main__ -e {s}/a.zip {s}/r/.git",
                    f"python3 --some-unknown-opt -m zipfile.__main__ -e {s}/a.zip {s}/r/.git"], 2)

    def test_tarfile_main_extract_into_git_refused(self):
        s = self.scratch
        self.check([f"python3 -m tarfile.__main__ -e {s}/a.tar {s}/r/.git",
                    f"python3 -Im tarfile.__main__ -e {s}/a.tar {s}/r/.git"], 2)

    def test_benign_forms_allowed(self):
        s = self.scratch
        self.check(["python3 -m json.tool", f"python3 -m zipfile.__main__ -l {s}/a.zip",
                    f"python3 -m tarfile.__main__ -l {s}/a.tar", f"python3 -m zipfile -l {s}/a.zip",
                    f"python3 -m zipfile.__main__ -e {s}/a.zip {s}/clean"], 0)


if __name__ == "__main__":
    unittest.main()
