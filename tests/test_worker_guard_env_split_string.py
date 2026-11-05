"""T-1037: every env -S / --split-string form (any prefix, any cluster) is denied for all worker roles; forms without -S stay allowed."""
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
    ("coder", 'env -S "-v" -u"$X" ls'), ("coder", "env -S '-v ${A}${X}' ls"),
    ("coder", "env -S '-v \"${X}\"' ls"), ("coder", "env --sp='-v ${X}' ls"), ("coder", "env -vS'-v ${X}' ls"),
    ("qa", 'env -S "-v $X" git status'),
    # F: comments, \c and quotes in -S strings keep exit 2 (main's parse is the floor)
    ("coder", "env -S '# -u FACTORY_ROLE' ls"), ("coder", "env -S '# -i' factory task show T-1"),
    ("coder", "env -S 'ls \\c -u FACTORY_ROLE'"), ("coder", "env -S 'ls # -u FACTORY_ROLE'"),
    ("coder", "env -S \"x' -u FACTORY_ROLE '\" ls"),
    ("qa", "env -S '# -u GIT_DIR' git status"), ("qa", "env -S '# -i' git status"),
    ("qa", "env -S \"x' -u GIT_DIR '\" git status"),
]
# T-1037 (M24043): env -S / --split-string is denied for every worker role; the string is never parsed.
ROLES = ("coder", "qa", "reviewer", "pm", "judge", "cleaner", "staff-engineer", "scout", "evaluator", "researcher")
ALL_ROLE_FORMS = [
    "env -S 'ls'", "env -S ls", "env -Sls", "env -vS ls", "env -iS ls", "env -0S ls", "env -aS ls",
    "env --debug -S ls", "env -a foo -S ls", "env -u FOO -S ls", "env --unset=FOO --sp=ls",
    "/usr/bin/env -S ls", "FOO=1 env -S ls", "env FOO=1 -S ls", "timeout 5 env -S ls", "nice env -S ls",
    "command env -S ls", "zsh -c 'env -S ls'",
]
PREFIXES = ["--" + "split-string"[:n] for n in range(1, len("split-string") + 1)]
DENY += [
    ("coder", "env -S 'ls -l'"), ("coder", "env --sp='ls -l'"), ("coder", "env -S '\"ls\" -l'"),
    ("coder", "env -S \"'-u FACTORY_ROLE'\" ls"), ("qa", "env -S 'git status'"),
]
ALLOW = [
    ("coder", "env ls"), ("qa", "env ls"), ("coder", "env ls -S"), ("qa", "env ls -S"),
    ("coder", "env -a foo ls"), ("qa", "env -a foo ls"), ("coder", "env -C /tmp ls"), ("qa", "env -C /tmp ls"),
    ("coder", "env FOO=1 ls"), ("qa", "env FOO=1 ls"), ("coder", "FOO=$X env ls"), ("coder", "env -u FOO ls"),
    ("coder", "env -uSOMEVAR ls"), ("coder", "env --unset=SOMEVAR ls"), ("qa", "git status"),
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

    def test_deny_all_roles_and_prefixes(self):
        forms = list(ALL_ROLE_FORMS)
        for pre in PREFIXES:  # --s .. --split-string
            forms += ["env %s=ls" % pre, "env %s ls" % pre]
        for role in ROLES:
            for cmd in forms:
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
