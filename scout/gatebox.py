"""Gate sandbox profile (T-0632, GS1): run the merge gate's command under the nsbox allow-list mode.

The local gate runs PR code as the owner uid. This module builds the argv (and the env) that confines it: $HOME,
$XDG_RUNTIME_DIR, /tmp and /dev/shm are empty tmpfs, the gate tree, FACTORY_MAIN and the repo's git common dir are
read-only, FACTORY_MAIN's state dirs are masked, and only <wt_root>/{reports,factory-home,tmp} are writable. Own net
namespace (lo only), own pid namespace, capabilities dropped by nsbox. If user namespaces are unavailable this raises
GateboxError: the caller must never fall back to running the command unsandboxed. Not wired into bin/factory yet.
"""
import os
import subprocess
import signal
import sys
from pathlib import Path

try:
    from scout import nsbox
except ImportError:  # imported by path from a script outside the repo root
    sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
    import nsbox

MASKED = ("db", "run", "logs", "worktrees", "exports", "state")  # FACTORY_MAIN subdirs the gate must not see
HIDE_TMP = ("/tmp", "/dev/shm", "/var/tmp")  # /var/tmp is disk-backed and survives the run: PR code must not stage files there
ENV_PASS = ("PATH", "LANG", "TERM", "FACTORY_HOME", "FACTORY_MAIN", "FACTORY_TEST_SLOT_HELD", "GATE_SHARDS",
            "GATE_REPORT_DIR")  # plus LC_*
TMPFS_SIZES = {"/tmp": "64m", "/dev/shm": "16m", "/var/tmp": "16m"}  # memory-backed; the real scratch is the disk-backed <wt_root>/tmp


class GateboxError(RuntimeError):
    """The sandbox cannot be built or started; the command was not run."""


def _real(path, what):
    p = os.path.realpath(path)
    if not os.path.isdir(p):
        raise GateboxError(f"gatebox: {what} {path} is not a directory; fix the gate config, the command was not run")
    return p


def _git_common_dir(tree):
    env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}  # a stray GIT_DIR must not pick the dir
    r = subprocess.run(["git", "-C", tree, "rev-parse", "--path-format=absolute", "--git-common-dir"],
                       capture_output=True, text=True, timeout=30, env=env)
    if r.returncode != 0:
        raise GateboxError(f"gatebox: git rev-parse --git-common-dir failed in {tree}: {r.stderr.strip()}")
    return os.path.realpath(r.stdout.strip())


def _under(path, base):
    return path == base or path.startswith(base.rstrip("/") + "/")


def make_env(env, factory_main, wt_root, home):
    """Allow-list the caller's env; nothing else (GH_TOKEN, SSH_AUTH_SOCK, FACTORY_AGENT, ...) reaches the gate."""
    out = {k: v for k, v in env.items() if k in ENV_PASS or k.startswith("LC_")}
    out.setdefault("FACTORY_MAIN", factory_main)
    out.setdefault("FACTORY_HOME", f"{wt_root}/factory-home")
    out.setdefault("GATE_REPORT_DIR", f"{wt_root}/reports")
    out.update({"HOME": home, "TMPDIR": f"{wt_root}/tmp", "PYTHONDONTWRITEBYTECODE": "1",
                "PYTHONPYCACHEPREFIX": f"{wt_root}/tmp/pycache"})  # py_compile ignores DONTWRITE; the tree is read-only
    return out


def build(cmd, tree, factory_main, wt_root, env, *, home=None, runtime=None, unshare="unshare"):
    """(argv, env) to run the shell command `cmd` in `tree` under the gate profile. Raises GateboxError."""
    ok, why = nsbox.userns_available(unshare)
    if not ok:
        raise GateboxError(f"gatebox: user namespaces unavailable ({why}); the gate must not run unsandboxed")
    tree, main, wt = _real(tree, "gate tree"), _real(factory_main, "FACTORY_MAIN"), os.path.realpath(wt_root)
    home = str(home or nsbox.real_home())
    runtime = str(runtime or os.environ.get("XDG_RUNTIME_DIR") or f"/run/user/{os.getuid()}")
    rw = [f"{wt}/{d}" for d in ("reports", "factory-home", "tmp")]
    try:
        for d in rw:
            os.makedirs(d, exist_ok=True)
    except OSError as e:
        raise GateboxError(f"gatebox: cannot create {e.filename}: {e.strerror}; fix wt_root, the command was not run") from e
    rw = [os.path.realpath(d) for d in rw]
    ro = [tree, main]
    common = _git_common_dir(tree)
    if not _under(common, main) and not _under(common, tree):
        ro.append(common)
    masks = [os.path.join(main, m) for m in MASKED if os.path.isdir(os.path.join(main, m))]
    hide = [home, runtime, *HIDE_TMP]
    try:
        cfg = nsbox.make_cfg(f"{wt}/reports", None, home, mode="allow", net=False, pid=True, unshare=unshare,
                             hide_dirs=hide, hide_files=(), rw=rw, ro=ro, masks=masks, cwd=tree,
                             tmpfs_sizes={d: s for d, s in TMPFS_SIZES.items() if os.path.isdir(d)})
    except ValueError as e:
        raise GateboxError(f"gatebox: {e}") from e
    return nsbox.compose_stage1(cfg, ["bash", "-c", cmd]), make_env(env, main, wt, home)


def run(cmd, tree, factory_main, wt_root, env, timeout=600, **kw):
    """Run `cmd` under the profile; returns nsbox.Contained(returncode, output, timed_out). The tree dies on timeout."""
    argv, benv = build(cmd, tree, factory_main, wt_root, env, **kw)
    p = subprocess.Popen(argv, cwd=os.path.realpath(tree), env=benv, stdin=subprocess.DEVNULL,
                         stdout=subprocess.PIPE, stderr=subprocess.STDOUT, start_new_session=True)
    try:
        out, _ = p.communicate(timeout=timeout)
        timed_out = False
    except subprocess.TimeoutExpired:
        os.killpg(p.pid, signal.SIGKILL)
        out, _ = p.communicate()
        timed_out = True
    return nsbox.Contained(p.returncode, out.decode(errors="replace"), timed_out)
