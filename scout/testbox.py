"""Test-run sandbox profile (T-0674): `factory test <cmd>` for coders and qa, the gatebox profile with a read-write cwd tree.

Same confinement as scout/gatebox.py (own net namespace with lo only, own pid namespace, caps dropped, $HOME / runtime dir /
/tmp / /dev/shm / /var/tmp hidden, env allow-list, FACTORY_MAIN and the git common dir read-only, FACTORY_MAIN state dirs masked).
The per-run scratch dir (TMPDIR, FACTORY_HOME) is disk-backed and lives under the tree. Any failure to build raises
TestboxError: the caller must never fall back to running the command unsandboxed. Does not touch scout/nsbox.py.
"""
import os
import shlex
import shutil
import sys
import tempfile

try:
    from scout import gatebox
except ImportError:  # imported by path from a script outside the repo root
    sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
    import gatebox

DROP_ENV = ("FACTORY_HOME", "GATE_SHARDS", "GATE_REPORT_DIR")  # the live state dir and gate knobs never reach a test run


class TestboxError(RuntimeError):
    """The sandbox cannot be built; the command was not run."""

    __test__ = False


def new_scratch(tree):
    """A fresh disk-backed per-run dir under `tree`; the caller removes it with cleanup()."""
    try:
        return tempfile.mkdtemp(prefix=".factory-test-", dir=os.path.realpath(tree))
    except OSError as e:
        raise TestboxError(f"testbox: cannot create the scratch dir under {tree}: {e.strerror}; the command was not run") from e


def cleanup(scratch):
    shutil.rmtree(scratch, ignore_errors=True)


def build(argv, tree, factory_main, scratch, env, **kw):
    """(argv, env) to run the command `argv` (a list) in `tree` under the test profile. Raises TestboxError."""
    benv = {k: v for k, v in env.items() if k not in DROP_ENV}
    try:
        return gatebox.build(shlex.join(argv), tree, factory_main, scratch, benv, **kw)
    except gatebox.GateboxError as e:
        raise TestboxError(str(e).replace("gatebox:", "testbox:", 1)) from e
