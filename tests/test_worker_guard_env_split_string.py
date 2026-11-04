"""T-1037: env --split-string prefixes and -S quoting/escapes (GNU env) must reach check_scrub and the no-push env parser."""
import json
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from guard_impl import hook_run  # noqa: E402

GUARD_IMPL_PYTHON_ONLY = "Go mirror follow-up in G-016"

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

DENY = [
    ("coder", "env --split-s '-u FACTORY_ROLE' ls"), ("coder", "env --split='-u FACTORY_ROLE' ls"),
    ("coder", "env --sp='-u FACTORY_ROLE' ls"), ("coder", "env -S '\"-u\" FACTORY_ROLE' ls"),
    ("coder", "env -S'-u\\_FACTORY_ROLE' ls"), ("coder", "env -S\"-u 'FACTORY_ROLE'\" ls"),
    ("coder", "env -S '-u ${NOPE_T1037}FACTORY_ROLE' ls"), ("qa", "env -S '-u ${NOPE_T1037}GIT_DIR' git status"),
    ("coder", "env -S '-u FACTORY_ROLE\\c ls' ls"),
    ("coder", "env -S '\"-u FACTORY_ROLE' ls"),
    ("qa", "env -S '\"-u\" GIT_DIR' git status"), ("qa", "env -S'-u\\_GIT_DIR' git status"),
    ("qa", "env --split-s '-u GIT_DIR' git status"),
    ("qa", "env -S '-u ${X} git status'"), ("qa", "X=GIT_DIR env -S '-u ${X} git status'"),
    ("qa", "X=GIT_DIR; export X; env -S '-u ${X} git status'"),
    ("coder", "X=FACTORY_ROLE env -S '-u ${X} ls'"), ("coder", "X=ROLE env -S '-u FACTORY_${X} ls'"),
    ("coder", "export X=FACTORY_ROLE; env -S '-u ${X} ls'"),
    ("qa", "env -S '-u ${NOPE} GIT_DIR' git status"), ("qa", "env -S '-${X} GIT_DIR' git status"), ("coder", "env -S '\"-i\"' factory task show T-1"),
    # D: reviewer blocking 1 (expansion in an -S option word)
    ("coder", "X=FACTORY_ROLE env -S '-v -u${X}' ls"), ("coder", "X=ROLE env -S '-v -uFACTORY_${X}' ls"),
    ("coder", "X=FACTORY_ROLE env -S '--debug --unset=${X}' ls"), ("coder", "X=FACTORY_ROLE env -S '-a foo -u${X}' ls"),
    ("coder", "export X=FACTORY_ROLE; env -S '-v -u${X}' ls"), ("coder", "X=uFACTORY_ROLE env -S '-0 -${X}' ls"),
    ("coder", "X=i env -S '-v -${X}' factory task show T-1"), ("coder", "X=-uFACTORY_ROLE env -S '-v ${X}' ls"),
    ("coder", "X=-i env -S '-v ${X}' factory task show T-1"),
    # E: any scanned -S word or -u value with $ or a backtick
    ("coder", 'env -S "-v -u$X" ls'), ("coder", 'env -S "-u $X" ls'), ("coder", 'env -S "-v $X" ls'),
    ("coder", 'env -S "-v -u$(echo FACTORY_ROLE)" ls'), ("coder", 'env --sp="-v -$X" factory task show T-1'),
    ("coder", 'env -S "-v" -u"$X" ls'), ("coder", 'env -v -u"$X" ls'), ("coder", "env -S '-v ${A}${X}' ls"),
    ("coder", "env -S '-v \"${X}\"' ls"), ("coder", "env --sp='-v ${X}' ls"), ("coder", "env -vS'-v ${X}' ls"),
    ("qa", 'env -S "-v $X" git status'),
    # F: comments, \c and quotes in -S strings keep exit 2 (main's parse is the floor)
    ("coder", "env -S '# -u FACTORY_ROLE' ls"), ("coder", "env -S '# -i' factory task show T-1"),
    ("coder", "env -S 'ls \\c -u FACTORY_ROLE'"), ("coder", "env -S 'ls # -u FACTORY_ROLE'"),
    ("coder", "env -S \"x' -u FACTORY_ROLE '\" ls"),
    ("qa", "env -S '# -u GIT_DIR' git status"), ("qa", "env -S '# -i' git status"),
    ("qa", "env -S \"x' -u GIT_DIR '\" git status"),
]
ALLOW = [
    ("qa", "git status"), ("coder", "env -S 'ls -l'"), ("qa", "env -S 'git status'"),
    ("coder", "env -S '\"ls\" -l'"), ("coder", "env --sp='ls -l'"), ("coder", "env -S \"'-u FACTORY_ROLE'\" ls"),
    ("coder", "FOO=$X env ls"),
]


def run_hook(cmd, role):
    env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "WORKER_GUARD_SCRATCH")}
    payload = {"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": ROOT}
    return hook_run(json.dumps(payload), ["--role", role], env=env)


class EnvSplitStringTests(unittest.TestCase):
    def test_deny_forms(self):
        for role, cmd in DENY:
            with self.subTest(role=role, cmd=cmd):
                p = run_hook(cmd, role)
                self.assertEqual(p.returncode, 2, p.stderr)

    def test_allow_forms(self):
        for role, cmd in ALLOW:
            with self.subTest(role=role, cmd=cmd):
                p = run_hook(cmd, role)
                self.assertEqual(p.returncode, 0, p.stderr)


if __name__ == "__main__":
    unittest.main()
