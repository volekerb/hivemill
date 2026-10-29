"""Per-run cgroup caps for `factory test` (T-0681, docs/test-cgroups.md).

The owner installs a user unit whose cgroup is delegated (Delegate=cpu memory pids) and holds no process; each run gets a
child cgroup below it, made by writing cgroupfs (agents cannot run systemd-run). Any problem raises CgroupError: the caller
must not run the command uncapped.
"""
import errno
import os
import re
import time

CONTROLLERS = ("cpu", "memory", "pids")
PERIOD_US = 100000
UNIT_SUBPATH = "factory-tests.slice/factory-tests.service"
MEM_RE = re.compile(r"[1-9][0-9]*[KMG]?$")


class CgroupError(RuntimeError):
    """The cgroup cannot be set up; the command was not run."""


def default_parent(uid):
    return f"/sys/fs/cgroup/user.slice/user-{uid}.slice/user@{uid}.service/{UNIT_SUBPATH}"


def _positive(name, v):
    if not v.strip().isdigit() or int(v) < 1:
        raise CgroupError(f"config {name} is {v!r}, not a positive integer; fix it with `factory config {name} <n>`, the command was not run")
    return int(v)


def limits(cpu_quota, mem_max, tasks_max):
    """Validated {file: content} for the child cgroup from the three config strings."""
    pct = _positive("test_cpu_quota", cpu_quota)
    tasks = _positive("test_tasks_max", tasks_max)
    mem = mem_max.strip().upper()
    if not MEM_RE.match(mem):
        raise CgroupError(f"config test_mem_max is {mem_max!r}, not a size like 3G or 2048M; fix it with `factory config test_mem_max 3G`, the command was not run")
    return {"cpu.max": f"{pct * PERIOD_US // 100} {PERIOD_US}", "memory.max": mem, "memory.swap.max": "0",
            "memory.oom.group": "1", "pids.max": str(tasks)}


def check_parent(parent):
    if not parent or not os.path.isabs(parent):
        raise CgroupError(f"test_cgroup=1: parent cgroup {parent!r} is not an absolute path; the command was not run (rollback: factory config test_cgroup 0)")
    if not os.path.isdir(parent):
        raise CgroupError(f"test_cgroup=1: parent cgroup {parent} is missing; the owner must install the unit in docs/test-cgroups.md; "
                          "the command was not run uncapped (rollback: factory config test_cgroup 0)")
    if not os.access(parent, os.W_OK | os.X_OK):
        raise CgroupError(f"test_cgroup=1: parent cgroup {parent} is not writable by this user (not delegated?); see docs/test-cgroups.md; "
                          "the command was not run uncapped (rollback: factory config test_cgroup 0)")
    try:
        with open(os.path.join(parent, "cgroup.subtree_control")) as f:
            have = f.read().split()
    except OSError as e:
        raise CgroupError(f"test_cgroup=1: cannot read {parent}/cgroup.subtree_control: {e.strerror}; the command was not run uncapped") from e
    missing = [c for c in CONTROLLERS if c not in have]
    if missing:
        raise CgroupError(f"test_cgroup=1: parent cgroup {parent} does not delegate {' '.join(missing)}; check Delegate= in docs/test-cgroups.md; "
                          "the command was not run uncapped")


def create(parent, name, lim):
    """mkdir parent/name and write the limits; returns the child path. Removes the dir again on any failure."""
    check_parent(parent)
    path = os.path.join(parent, name)
    try:
        os.mkdir(path)
    except OSError as e:
        raise CgroupError(f"test_cgroup=1: cannot create cgroup {path}: {e.strerror}; the command was not run uncapped") from e
    try:
        for fn, val in lim.items():
            with open(os.path.join(path, fn), "w") as f:
                f.write(val)
    except OSError as e:
        destroy(path)
        raise CgroupError(f"test_cgroup=1: cannot set {fn}={val} in {path}: {e.strerror}; the command was not run uncapped") from e
    return path


def enter_fd(path):
    """An open fd on the child's cgroup.procs; a forked child writes b'0' to it (preexec_fn) to move itself in."""
    try:
        return os.open(os.path.join(path, "cgroup.procs"), os.O_WRONLY | os.O_CREAT, 0o600)  # O_CREAT only matters for the plain-dir parent in tests
    except OSError as e:
        destroy(path)
        raise CgroupError(f"test_cgroup=1: cannot open {path}/cgroup.procs: {e.strerror}; the command was not run uncapped") from e


def destroy(path):
    """Kill whatever is left in the cgroup and remove it. Best effort: a leftover empty dir is harmless."""
    try:
        with open(os.path.join(path, "cgroup.kill"), "w") as f:
            f.write("1")
    except OSError:
        pass  # no cgroup.kill (old kernel / plain dir): the run's process group was already signalled
    for _ in range(50):
        try:
            os.rmdir(path)
            return
        except FileNotFoundError:
            return
        except OSError as e:
            if e.errno != errno.EBUSY:
                return  # e.g. ENOTEMPTY in a plain dir: nothing more to do
            time.sleep(0.1)  # processes still exiting
