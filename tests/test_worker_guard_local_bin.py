"""T-0957: ~/.local/bin is first on the tick's PATH, so workers must not write it (a planted git/factory runs with the
tick's credentials). Writes exit 2 (also through a symlink entry, matched on the spelled path); reads and running stay
allowed. Through the real hook entry point; GUARD_IMPL=diff compares Python with Go."""
import json
import os
import pwd
import unittest

from guard_impl import hook_run

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
PW = pwd.getpwuid(os.getuid()).pw_dir
BIN = f"{PW}/.local/bin"
BLOCKED = [
    "echo x > ~/.local/bin/git", "echo x >> ~/.local/bin/go", "echo x > ~/.local/bin/factory",
    "echo x > $HOME/.local/bin/nice", "cp /tmp/x ~/.local/bin/git", "mv /tmp/x ~/.local/bin/git",
    "ln -s /tmp/x ~/.local/bin/git", "ln -sf /tmp/x ~/.local/bin/factory", "rm ~/.local/bin/factory",
    "rm -r ~/.local/bin", "tee ~/.local/bin/git", "cd ~/.local/bin && echo x > git",
    "cd ~/.local/bin && cp /tmp/x git", f"echo x > {BIN}/git", f"cp /tmp/x {BIN}/git",
    f"echo x > {BIN}/../bin/git", "sed -i s/a/b/ ~/.local/bin/git",
    "python3 -c \"open('/h/.local/bin/git','w')\"",
]
ALLOWED = [
    "ls ~/.local/bin", "ls -la ~/.local/bin/factory", "cp ~/.local/bin/x /tmp/x", "~/.local/bin/factory whoami",
    "grep -rn .local/bin docs", "echo x > /usr/local/bin/foo", "echo x > ~/.local/binx/foo",
    f"echo x > {PW}/.local/binx/foo", "git commit -m 'note: ~/.local/bin is on PATH'",
    "factory msg lead 'subj' 'see ~/.local/bin/git'", "export PATH=~/.local/bin:$PATH",
    "cp /tmp/x ~/.local/share/foo",
]


def run(cmd, role="coder"):
    return hook_run(json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": ROOT}), ["--role", role])


class LocalBinGuardTest(unittest.TestCase):
    def test_writes_into_local_bin_are_blocked(self):
        for cmd in BLOCKED:
            r = run(cmd)
            self.assertEqual(r.returncode, 2, f"{cmd}: {r.stderr}")

    def test_block_message_names_local_bin(self):
        for cmd in ("echo x > ~/.local/bin/git", "cp /tmp/x ~/.local/bin/git", "rm ~/.local/bin/factory"):
            self.assertIn("~/.local/bin", run(cmd).stderr, cmd)

    def test_reads_runs_look_alikes_and_text_are_allowed(self):
        for cmd in ALLOWED:
            r = run(cmd)
            self.assertEqual(r.returncode, 0, f"{cmd}: {r.stderr}")

    def test_blocked_for_every_worker_role(self):
        for role in ("coder", "qa", "reviewer", "pm"):
            self.assertEqual(run("echo x > ~/.local/bin/git", role).returncode, 2, role)


if __name__ == "__main__":
    unittest.main()
