"""T-0964: env value options (-a/-C/-P/-u, --argv0/--chdir/--unset prefixes) and -S forms must not hide a later
-u/-i/--unset from the no-push env parser or from check_scrub. A word starting with `-` is always an option, never a value."""
import json
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from guard_impl import hook_run  # noqa: E402

GUARD_IMPL_PYTHON_ONLY = "Go mirror follow-up in G-016"

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# (role, command) that must exit 2
DENY = [
    # no-push roles
    ("qa", "env --argv0 foo --uns GIT_DIR git status"), ("qa", "env -a foo -u GIT_DIR git status"),
    ("qa", "env --argv0 foo --ignore-env git status"), ("qa", "env --argv0 foo -i git status"),
    ("qa", "env --argv0 foo --uns PATH ls"), ("qa", "env --argv0 foo --xyz ls"),
    ("qa", "env --argv0 foo --unset FACTORY_ROLE git status"), ("qa", "env --chdir -i git status"),
    ("qa", "env --chdir --ignore-environment git status"), ("qa", "env --argv0 --uns GIT_DIR git status"),
    ("qa", "env --chd /tmp -u GIT_DIR git status"), ("reviewer", "env -a foo -u GIT_DIR git status"),
    # coder, check_scrub
    ("coder", "env -a x -u FACTORY_ROLE ls"), ("coder", "env --chdir /tmp --unset FACTORY_ROLE ls"),
    ("coder", "env -C /tmp -u FACTORY_ROLE ls"), ("coder", "env -u FOO -u FACTORY_ROLE ls"),
    ("coder", "env --unset FOO -u FACTORY_ROLE ls"), ("coder", "env -vuFACTORY_ROLE ls"),
    ("coder", "env --uns=FACTORY_ROLE ls"), ("coder", "env --ignore-e factory task show T-1"),
    # -S / --split-string / -P
    ("qa", "env -S'-u GIT_DIR' git status"), ("qa", "env -vS'-u GIT_DIR' git status"),
    ("qa", "env --split-string='-i' git status"), ("qa", "env -P /bin -u GIT_DIR git status"),
    ("qa", "env -P -i git status"), ("qa", "env -P/bin -i git status"),
    ("coder", "env -S'-u FACTORY_ROLE' ls"), ("coder", "env -vS'-u FACTORY_ROLE' ls"),
    ("coder", "env --split-string='-u FACTORY_ROLE' ls"), ("coder", "env -P /bin -u FACTORY_ROLE ls"),
    ("coder", "env -P/bin -u FACTORY_ROLE ls"), ("coder", "env -P -i factory task show T-1"),
    # reviewer/QA FAIL @544c3a02: an -S string must not end the scan, a `u` after S in a cluster counts, a dash-leading
    # dynamic -u value is still denied
    ("coder", "env -Senv -u FACTORY_ROLE ls"), ("coder", "env --split-string=env -u FACTORY_ROLE ls"),
    ("coder", "env -Senv -i factory task show T-1"), ("coder", "env -Sfoo -u FACTORY_ROLE ls"),
    ("coder", "env -vSfoo -u FACTORY_ROLE ls"), ("coder", "env -Sfoo --unset=FACTORY_ROLE ls"),
    ("coder", "env --split-string=foo -u FACTORY_ROLE ls"), ("coder", "env -Sfoo -i factory task show T-1"),
    ("coder", "env --split-string=foo -i factory task show T-1"), ("coder", "env -Sfoo - factory task show T-1"),
    ("coder", "env -Sfoo --ignore-environment factory task show T-1"),
    ("qa", "env -vSuGIT_DIR git status"), ("reviewer", "env -vSuGIT_DIR git status"),
    ("qa", "env -vSu GIT_DIR git status"), ("qa", "env -aSuGIT_DIR git status"),
    ("qa", "env -u -$X git status"), ("qa", "env --unset -$X git status"), ("qa", "env -xu -$X git status"),
]
ALLOW = [
    ("qa", "env -a foo ls"), ("qa", "env -C /tmp ls"), ("coder", "env -a foo ls"), ("coder", "env -C /tmp ls"),
    ("coder", "env -u FOO ls"), ("coder", "env -P /bin ls"), ("coder", "env --argv0 foo ls"),
    # attempt 3: an `i` in text attached to -S or after `u` is not -i for no-push roles
    ("qa", "env -Sprintf ls"), ("qa", "env -S'git status'"), ("reviewer", "env -S'git status'"),
    ("qa", "env -ubig git status"), ("qa", "env -uxi git status"),
]
# T-0826 REGRESSION LIST: exit 2 on origin/main and on head
REGRESSION = [
    ("coder", "env --argv0 -i factory task show T-1"), ("coder", "env --chdir -i factory task show T-1"),
    ("coder", "env --argv0 - factory task show T-1"), ("coder", "env --chdir - factory task show T-1"),
    ("coder", "env --argv0 -u FACTORY_ROLE ls"), ("coder", "env --chdir -u FACTORY_ROLE ls"),
    ("coder", "env --argv0 --unset FACTORY_ROLE ls"), ("coder", "env --chdir --unset=FACTORY_ROLE ls"),
    ("coder", "env -u -i factory task show T-1"), ("coder", "env -u - factory task show T-1"),
    ("coder", "env --unset -i factory task show T-1"), ("coder", "env --uns -i factory task show T-1"),
    ("coder", "env --unset -u FACTORY_ROLE ls"), ("coder", "env -u -u FACTORY_ROLE ls"),
    ("coder", "env -ui factory task show T-1"), ("coder", "env -uxi factory task show T-1"),
    ("coder", "env -vuxi factory task show T-1"), ("coder", "env -Cui factory task show T-1"),
    ("coder", "env -Suxi factory task show T-1"),
    ("qa", "env --argv0 -i git status"), ("qa", "env --arg -i git status"),
    ("qa", "env --argv0 -u GIT_DIR git status"), ("qa", "env --argv0 --unset=GIT_DIR git status"),
    ("qa", "env --argv0 --ignore-environment git status"), ("qa", "env --argv0 - git status"),
    ("qa", "env --uns -i git status"), ("qa", "env --uns -u GIT_DIR git status"),
    ("reviewer", "env --argv0 -i git status"), ("reviewer", "env --argv0 -u GIT_DIR git status"),
]


def run_hook(cmd, role):
    env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "WORKER_GUARD_SCRATCH")}
    payload = {"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": ROOT}
    return hook_run(json.dumps(payload), ["--role", role], env=env)


class EnvValueOptionTests(unittest.TestCase):
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


def _regression(role, cmd):
    def t(self):
        p = run_hook(cmd, role)
        self.assertEqual(p.returncode, 2, p.stderr)
    return t


class T0826RegressionTests(unittest.TestCase):
    """One test per REGRESSION LIST form (generated: test_regression_<n>_<role>)."""


for _n, (_role, _cmd) in enumerate(REGRESSION):
    setattr(T0826RegressionTests, f"test_regression_{_n:02d}_{_role}", _regression(_role, _cmd))


if __name__ == "__main__":
    unittest.main()
