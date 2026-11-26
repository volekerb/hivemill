"""T-1633 / T-2129: ci/remote_gate.sh and ci/remote_gate_driver.sh, the remote gate. See the stub comment below."""
import fcntl
import json
import os
import re
import shutil
import signal
import subprocess
import tempfile
import time
import unittest
from pathlib import Path

import harness

ROOT = Path(__file__).resolve().parent.parent

# T-1633: ci/remote_gate.sh, the remote gate. ssh is the process boundary: a stub on PATH runs the "remote" command
# locally under a temp fake remote home, so these run without the remote host. The fake tree is tiny, so every real step of
# the remote driver fails fast; the tests check the contract around the steps (refusals, count, cleanup, lock, env).
GATE_SH = ROOT / "ci" / "remote_gate.sh"
RUN_DIR_RE = r"(?:/var)?/tmp/fg-[A-Za-z0-9]{6}"  # <root>/fg-XXXXXX, root /var/tmp (disk) or /tmp
SSH_STUB = """#!/bin/bash
printf '%s\\n' "$*" >> "$STUB_LOG"
# Optional: run $STUB_FIRST_HOOK once, before the first remote command (simulates a change after the up-front checks).
if [ -n "${STUB_FIRST_HOOK-}" ] && [ ! -e "$STUB_LOG.hooked" ]; then : > "$STUB_LOG.hooked"; bash -c "$STUB_FIRST_HOOK"; fi
while [ $# -gt 0 ]; do case "$1" in -o) shift 2 ;; -*) shift ;; *) break ;; esac; done
shift
cd "$FAKE_REMOTE_HOME"
# Like a real client: the "remote" command is not our pid, and a TERM ends the client (the remote side sees the
# connection drop) without waiting for the remote command.
trap 'exit 143' TERM
HOME="$FAKE_REMOTE_HOME" bash -c "$*" <&0 &
wait "$!"
"""
FAKE_FACTORY = """#!/usr/bin/env python3
import json, os, time
mark = %r
shipped = sorted(os.path.relpath(os.path.join(d, f)) for d, _, fs in os.walk(".") if ".git" not in d for f in fs)
tmp = "%%s.%%d" %% (mark, os.getpid())  # several shards run this at once: write, then rename into place
json.dump({"env": dict(os.environ), "cwd": os.getcwd(),
           "modes": {k: oct(os.stat(p).st_mode & 0o777) for k, p in (("run", os.path.dirname(os.environ["HOME"])),
                                                                      ("tmp", os.environ["TMPDIR"]))}, "files": sorted(os.listdir(".")), "shipped": shipped,
           "cli": open("lib/cli.py").read()}, open(tmp, "w"))
os.replace(tmp, mark)
time.sleep(%d)
"""


def git(tree, *args):
    return harness.git("-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false", *args,
                       cwd=tree)


def processes():
    """{pid: (pgid, cmdline + environ)} of every live (not zombie) process this user can read."""
    out = {}
    for d in Path("/proc").iterdir():
        if not d.name.isdigit():
            continue
        try:
            fields = (d / "stat").read_text().rsplit(") ", 1)[1].split()
            seen = (d / "cmdline").read_bytes() + (d / "environ").read_bytes()
        except OSError:
            continue
        if fields[0] not in ("Z", "X"):
            out[int(d.name)] = (int(fields[2]), seen)
    return out


def groups_of(tmp):
    """Process groups of everything started for the test dir `tmp` (stub ssh, driver, its jobs). The driver is found by
    its HOME (the fake remote home), which survives `env -i` and its own setsid."""
    mark = str(tmp).encode()
    return {pgid for pid, (pgid, seen) in processes().items() if mark in seen and pgid != os.getpgrp()}


def alive_in(groups):
    return sorted(pid for pid, (pgid, _) in processes().items() if pgid in groups)


def kill_groups(tmp):
    """Kill every process group of the test dir; returns the pids still alive afterwards."""
    groups = set()
    for _ in range(50):
        groups |= groups_of(tmp)
        if not alive_in(groups) and not groups_of(tmp):
            return []
        for g in groups:
            try:
                os.killpg(g, signal.SIGKILL)
            except (ProcessLookupError, PermissionError):
                pass
        time.sleep(0.1)
    return alive_in(groups)


def drivers(tmp):
    """Pids of the driver processes of the test dir."""
    mark = str(tmp).encode()
    return sorted(pid for pid, (_, seen) in processes().items() if mark in seen and b"remote_gate_driver.sh" in seen)


LEFT = {}  # {test id: driver pids that did not exit by themselves once the test's gate runs were over}


def tearDownModule():
    if LEFT:
        raise AssertionError(f"driver processes left after the remote gate tests: {LEFT}")


class RemoteGate(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp(prefix="zz-test-remote-gate-"))
        self.procs = []
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.tree, self.remote, stub = self.tmp / "tree", self.tmp / "remote-home", self.tmp / "stub"
        for d in (self.tree, self.remote, stub):
            d.mkdir()
        (stub / "ssh").write_text(SSH_STUB)
        (stub / "ssh").chmod(0o755)
        self.log, self.mark = self.tmp / "ssh.log", self.tmp / "remote-env.json"
        self.env = {**os.environ, "PATH": f"{stub}:{os.environ['PATH']}", "STUB_LOG": str(self.log),
                    "FAKE_REMOTE_HOME": str(self.remote), "GH_TOKEN": "gh-secret-test", "GITHUB_TOKEN": "gh-secret-test",
                    "SSH_AUTH_SOCK": str(self.tmp / "agent.sock"), "FACTORY_AGENT": "claude-coder-x", "GATE_SHARDS": "12",
                    "GATE_REMOTE_HOST": "gate-host"}
        git(self.tree, "init", "-q", "-b", "main")
        (self.tree / "tests").mkdir()
        (self.tree / "tests" / "test_a.py").write_text(
            "import unittest\nclass T(unittest.TestCase):\n    def test_one(self): pass\n    def test_two(self): pass\n")
        git(self.tree, "add", "-A")
        git(self.tree, "commit", "-q", "-m", "base")
        git(self.tree, "update-ref", "refs/remotes/origin/main", "HEAD")
        self.commit_head(sleep=0)

    def commit_head(self, sleep):
        for d in ("ci", "lib"):
            (self.tree / d).mkdir(exist_ok=True)
        (self.tree / "ci" / "shard.py").write_text(FAKE_FACTORY % (str(self.mark), sleep))
        (self.tree / "lib" / "cli.py").write_text("X = 1\n")
        git(self.tree, "add", "-A")
        git(self.tree, "commit", "-q", "-m", f"head sleep {sleep}")

    def tearDown(self):
        # also after a timeout or a failed assert: give up on the gate runs as a runner does (kill their group), see that
        # every driver then ends by itself, and kill whatever is still there
        for p in self.procs:
            if p.poll() is None:
                os.killpg(p.pid, signal.SIGKILL)
            p.wait()
        end = time.monotonic() + 30
        while drivers(self.tmp) and time.monotonic() < end:
            time.sleep(0.2)
        if drivers(self.tmp):
            LEFT[self.id()] = drivers(self.tmp)
        self.assertEqual(kill_groups(self.tmp), [], "processes survived SIGKILL of their group")

    def start(self, script=GATE_SH, *args, **kw):
        """A gate run in its own process group, killed with its driver's group in tearDown."""
        kw = {"stdout": subprocess.PIPE, "stderr": subprocess.PIPE, "text": True, **kw}
        p = subprocess.Popen(["bash", str(script), *args], cwd=self.tree, env=self.env, start_new_session=True, **kw)
        self.procs.append(p)
        self.addCleanup(lambda: [f.close() for f in (p.stdout, p.stderr) if f])
        return p

    def gate(self, *args, script=GATE_SH, timeout=180):
        p = self.start(script, *args)
        try:
            out, err = p.communicate(timeout=timeout)
        except subprocess.TimeoutExpired:
            kill_groups(self.tmp)
            raise
        return subprocess.CompletedProcess(p.args, p.returncode, out, err)

    def wait_for(self, what, cond, timeout=60):
        end = time.monotonic() + timeout
        while not cond():
            self.assertLess(time.monotonic(), end, f"timed out waiting for {what}")
            time.sleep(0.1)

    def hanging_run(self):
        """A gate run whose first test step sleeps: (gate process, its run dir, the driver's groups, the log file)."""
        self.commit_head(sleep=120)
        log = self.tmp / "gate.log"
        with open(log, "w") as f:
            p = self.start(stdout=f, stderr=subprocess.STDOUT)
        self.wait_for("the unittest step", self.mark.exists)
        run_dir = json.loads(self.mark.read_text())["env"]["HOME"].rsplit("/", 1)[0]
        self.addCleanup(shutil.rmtree, run_dir, True)
        groups = {pgid for pid, (pgid, _) in processes().items() if pid in drivers(self.tmp)} - {p.pid}
        self.assertTrue(groups, "no driver group of its own")
        return p, run_dir, groups, log

    def lock_is_free(self):
        with open(self.remote / "factory-gate" / ".lock", "a") as f:
            try:
                fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                return False
        return True

    def assert_driver_ended(self, run_dir, groups, log):
        self.wait_for("the driver to exit", lambda: not alive_in(groups), timeout=30)
        self.assertTrue(self.lock_is_free(), "the gate lock is still held")
        self.assertFalse(os.path.exists(run_dir), log.read_text())

    def test_driver_exits_when_run_dir_removed(self):
        p, run_dir, groups, log = self.hanging_run()
        shutil.rmtree(run_dir, ignore_errors=True)
        self.assert_driver_ended(run_dir, groups, log)
        self.assertNotEqual(p.wait(timeout=60), 0)
        self.assertIn(f"run dir {run_dir} is gone", log.read_text())

    def test_driver_exits_when_parent_dies(self):
        p, run_dir, groups, log = self.hanging_run()
        stub = str(self.tmp / "stub" / "ssh").encode()
        ssh = [pid for pid, (_, seen) in processes().items()
               if stub in seen.split(b"\0")[:2] and b"remote_gate_driver.sh" in seen]
        self.assertTrue(ssh, "the stub ssh of the driver is not running")
        os.kill(p.pid, signal.SIGKILL)  # first, so it cannot remove the run dir for the driver
        p.wait()
        for pid in ssh:
            try:
                os.kill(pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
        self.assert_driver_ended(run_dir, groups, log)
        self.assertIn("(parent ssh or gate script) is gone", log.read_text())

    def test_driver_exits_when_gate_script_dies_in_local_mode(self):
        self.env["GATE_REMOTE_HOST"] = "local"
        self.env["HOME"] = str(self.remote)
        p, run_dir, groups, log = self.hanging_run()
        os.kill(p.pid, signal.SIGKILL)
        p.wait()
        self.assert_driver_ended(run_dir, groups, log)

    def test_driver_waiting_for_the_lock_exits_when_run_dir_removed(self):
        (self.remote / "factory-gate").mkdir()
        log = self.tmp / "gate.log"
        with open(self.remote / "factory-gate" / ".lock", "w") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            with open(log, "w") as f:
                p = self.start(stdout=f, stderr=subprocess.STDOUT)
            self.wait_for("the lock wait", lambda: "waiting" in log.read_text())
            run_dir = re.search(rf"\bdir ({RUN_DIR_RE})\b", log.read_text()).group(1)
            shutil.rmtree(run_dir)
            self.assertNotEqual(p.wait(timeout=40), 0)
        self.assertEqual(drivers(self.tmp), [])
        self.assertFalse(self.mark.exists(), "a step ran")

    def test_no_driver_process_is_left_after_a_run_that_times_out(self):
        self.commit_head(sleep=120)
        p = self.start(stdout=subprocess.DEVNULL, stderr=subprocess.STDOUT)
        self.wait_for("the unittest step", self.mark.exists)
        self.addCleanup(shutil.rmtree, json.loads(self.mark.read_text())["env"]["HOME"].rsplit("/", 1)[0], True)
        with self.assertRaises(subprocess.TimeoutExpired):
            p.wait(timeout=1)
        os.killpg(p.pid, signal.SIGKILL)  # what a runner does at its time limit; the driver has its own group
        self.wait_for("the driver to exit by itself", lambda: not drivers(self.tmp), timeout=40)

    def holder_record(self, pid, since, limit=600):
        start = Path(f"/proc/{pid}/stat").read_text().rsplit(") ", 1)[1].split()[19]
        return f"{pid} {start} {since} {limit}\n"

    def test_stale_lock_is_taken_over(self):
        dead = subprocess.Popen(["true"])
        dead_record = self.holder_record(dead.pid, int(time.time()))  # not yet reaped, so /proc still has it
        dead.wait()
        cases = {"is dead": dead_record,
                 "over the gate limit of 600 s": self.holder_record(os.getpid(), int(time.time()) - 601)}
        (self.remote / "factory-gate").mkdir()
        lockfile = self.remote / "factory-gate" / ".lock"
        for why, record in cases.items():
            with self.subTest(why):
                lockfile.write_text(record)
                self.mark.unlink(missing_ok=True)
                with open(lockfile) as lock:
                    fcntl.flock(lock, fcntl.LOCK_EX)  # still held, as by a stray child of the recorded holder
                    p = self.gate(timeout=90)
                self.assertIn("taking over ~/factory-gate/.lock: holder pid", p.stdout, p.stdout + p.stderr)
                self.assertIn(why, p.stdout)
                self.assertTrue(self.mark.exists(), "the run did not continue after the takeover")
                self.assertNotEqual(lockfile.read_text(), record, "the new holder is not recorded")
                self.assertEqual(self.run_dirs(p.stdout), [])

    def test_takeover_stops_what_is_left_of_the_holder(self):
        (self.remote / "factory-gate").mkdir()
        lockfile = self.remote / "factory-gate" / ".lock"
        cases = {"over the gate limit": ["bash", "-c", "sleep 60 & wait", "remote_gate_driver.sh"],  # a hung driver
                 "is dead": ["bash", "-c", "sleep 60 & exit 0"]}  # a killed driver whose jobs run on
        for why, argv in cases.items():
            with self.subTest(why):
                with open(lockfile, "w") as lock:
                    fcntl.flock(lock, fcntl.LOCK_EX)
                    holder = subprocess.Popen(argv, start_new_session=True, pass_fds=[lock.fileno()])
                    self.addCleanup(holder.wait)
                    self.addCleanup(lambda g=holder.pid: alive_in({g}) and os.killpg(g, signal.SIGKILL))
                    lock.write(self.holder_record(holder.pid, int(time.time()) - 601))
                if why == "is dead":
                    holder.wait()
                p = self.gate(timeout=90)
                self.assertIn(why, p.stdout, p.stdout + p.stderr)
                holder.wait(timeout=10)
                self.assertEqual(alive_in({holder.pid}), [], "the old holder's processes still run")

    def test_driver_records_the_gate_time_limit(self):
        self.env["GATE_TIMEOUT_S"] = "7200"
        p = self.gate()
        self.assertEqual((self.remote / "factory-gate" / ".lock").read_text().split()[3:], ["7200"], p.stdout + p.stderr)

    def test_bad_time_limit_is_refused_before_any_ssh(self):
        for v in ("1h", "0"):
            with self.subTest(v):
                self.env["GATE_TIMEOUT_S"] = v
                p = self.gate()
                self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
                self.assertIn("GATE_TIMEOUT_S", p.stderr)
        self.assertEqual(self.ssh_calls(), [])

    def ssh_calls(self):
        return self.log.read_text().splitlines() if self.log.exists() else []

    def run_dirs(self, out):
        """The run dirs (<root>/fg-XXXXXX, T-1679) a gate run printed that still exist; asserts it printed one."""
        dirs = re.findall(rf"\bdir ({RUN_DIR_RE})\b", out)
        self.assertTrue(dirs, f"no run dir in the output: {out}")
        for d in dirs:
            self.addCleanup(shutil.rmtree, d, True)
        return [d for d in dirs if os.path.exists(d)]

    def test_help_and_bad_arguments(self):
        p = self.gate("--help")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("--count", p.stdout)
        p = self.gate("--bogus")
        self.assertEqual(p.returncode, 2)
        self.assertIn("unknown argument '--bogus'", p.stderr)
        self.assertEqual(self.gate("--count", "--plan").returncode, 2)
        self.assertEqual(self.ssh_calls(), [])

    def test_missing_or_odd_host_is_refused_before_any_ssh(self):
        for host in ("", "-oProxyCommand=x", "a b"):
            self.env["GATE_REMOTE_HOST"] = host
            p = self.gate()
            self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
            self.assertIn("GATE_REMOTE_HOST", p.stderr)
        del self.env["GATE_REMOTE_HOST"]
        self.assertEqual(self.gate().returncode, 2)
        self.assertEqual(self.gate("--count").returncode, 0)  # the count needs no host
        self.assertEqual(self.ssh_calls(), [])

    def test_run_dir_is_short_disk_backed_and_traversable(self):
        # T-1679: unix sockets under the run's TMPDIR must fit 108 bytes: go/internal/sandbox binds
        # <TMPDIR>/<subtest name>NNN/001/p.sock, up to 82 chars after TMPDIR. The run dir is on disk when the host has
        # a disk-backed /var/tmp (a ~1 GB run must not fill a tmpfs /tmp), and go's build temp is inside it.
        p = self.gate()
        self.assertNotEqual(p.returncode, 0, p.stdout + p.stderr)
        seen = json.loads(self.mark.read_text())
        tmpdir = seen["env"]["TMPDIR"]
        self.assertRegex(tmpdir, rf"^{RUN_DIR_RE}/tmp$")
        self.assertLessEqual(len(tmpdir) + 82, 107)
        self.assertEqual(seen["env"]["GOTMPDIR"], tmpdir)
        disk = subprocess.run(["stat", "-f", "-c", "%T", "/var/tmp"], capture_output=True, text=True)
        if disk.returncode == 0 and disk.stdout.strip() not in ("tmpfs", "ramfs"):
            self.assertTrue(tmpdir.startswith("/var/tmp/fg-"), tmpdir)
        # other-x on the run dir and TMPDIR: the subuid sandbox tests run as another uid below TMPDIR
        self.assertEqual(seen["modes"], {"run": "0o711", "tmp": "0o711"})
        run_dir = tmpdir[:-len("/tmp")]
        self.assertIn(f"removed gate-host:{run_dir}", p.stdout)
        self.assertEqual(self.run_dirs(p.stdout), [], "run dir left behind")

    def test_run_root_pick_prefers_disk_and_keeps_socket_paths_short(self):
        # the driver's --pick-root, with `stat` stubbed at the process boundary: any dir named ram* is tmpfs
        stub = self.tmp / "stat-stub"
        stub.mkdir()
        (stub / "stat").write_text('#!/bin/bash\ncase "$5" in */ram*) echo tmpfs ;; *) echo ext2/ext3 ;; esac\n')
        (stub / "stat").chmod(0o755)
        env = {**os.environ, "PATH": f"{stub}:{os.environ['PATH']}"}
        for d in ("ram1", "disk1", "ram2"):
            (self.tmp / d).mkdir()
        ram1, disk1, ram2, gone = (str(self.tmp / d) for d in ("ram1", "disk1", "ram2", "gone"))

        def pick(tail, *roots):
            return subprocess.run(["bash", str(ROOT / "ci" / "remote_gate_driver.sh"), "--pick-root", str(tail), *roots],
                                  env=env, capture_output=True, text=True, timeout=30)

        p = pick(0, gone, ram1, disk1, ram2)  # disk wins over an earlier tmpfs; a missing dir is skipped
        self.assertEqual((p.returncode, p.stdout), (0, disk1 + "\n"), p.stderr)
        self.assertIn(f"{ram1} skipped: tmpfs is RAM-backed", p.stderr)
        p = pick(0, ram1, ram2)  # no disk: the last usable candidate, said out loud
        self.assertEqual((p.returncode, p.stdout), (0, ram2 + "\n"), p.stderr)
        self.assertIn("WARNING: no disk-backed run root", p.stderr)
        # a disk dir whose <root>/fg-XXXXXX/tmp + 82 socket chars passes 107 bytes is not used
        tail = 107 - len("/fg-XXXXXX/tmp") - len(disk1)
        self.assertEqual(pick(tail, disk1, ram1).stdout, disk1 + "\n")
        p = pick(tail + 1, disk1, ram1)
        self.assertEqual(p.stdout, ram1 + "\n", p.stderr)
        self.assertIn("exceeds a unix socket path", p.stderr)
        p = pick(0, gone)
        self.assertEqual((p.returncode, p.stdout), (2, ""))
        self.assertIn("no usable run root", p.stderr)
        self.assertEqual(pick("x", disk1).returncode, 2)
        # the gate's own candidates with the real 82-char socket tail: /var/tmp fits
        self.assertEqual(pick(82, "/var/tmp").stdout, "/var/tmp\n")

    def test_passing_run_exits_zero_and_removes_the_run_dir(self):
        # a main checkout whose driver passes: the run dir still goes (cleanup on success, not only on failure)
        main = self.tmp / "main"
        for f in ("ci/remote_gate.sh", "ci/test_count_gate.py", "tests/gate_shards.py"):
            (main / f).parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(ROOT / f, main / f)
        (main / "ci" / "remote_gate_driver.sh").write_text(
            '#!/bin/bash\n[ "$1" = --pick-root ] && { echo /tmp; exit 0; }\n'
            '[ -d "$1/head/tests" ] || exit 7\necho "stub driver: PASS in $1"\nexit 0\n')
        p = self.gate(script=main / "ci" / "remote_gate.sh")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("stub driver: PASS in /tmp/fg-", p.stdout)
        self.assertRegex(p.stdout, rf"removed gate-host:{RUN_DIR_RE}\n")
        self.assertEqual(self.run_dirs(p.stdout), [], "run dir left behind after a pass")

    def test_job_cap_follows_free_ram_not_only_cpu(self):
        def cap(cores, shards, avail_mib, total_mib):
            mi = self.tmp / "meminfo"
            mi.write_text(f"MemTotal:       {total_mib * 1024} kB\nMemFree:        1 kB\n"
                          f"MemAvailable:   {avail_mib * 1024} kB\n")
            p = subprocess.run(["bash", str(ROOT / "ci" / "remote_gate_driver.sh"), "--jobs", str(cores), str(shards),
                                str(mi)], capture_output=True, text=True, timeout=30)
            self.assertEqual(p.returncode, 0, p.stderr)
            return int(p.stdout.strip())
        self.assertEqual(cap(8, 8, 11000, 11897), 4)   # this box: 8 CPUs, 12 GB (MemTotal 11897 MiB)
        self.assertEqual(cap(8, 8, 5000, 11897), 2)    # other agents hold RAM: fewer jobs
        self.assertEqual(cap(8, 8, 60000, 64000), 8)   # a big host: the CPU cap
        self.assertEqual(cap(8, 3, 60000, 64000), 3)   # GATE_SHARDS below both
        self.assertEqual(cap(8, 8, 900, 11897), 1)     # never 0
        bad = self.tmp / "bad-meminfo"
        bad.write_text("MemTotal: 1 kB\n")
        p = subprocess.run(["bash", str(ROOT / "ci" / "remote_gate_driver.sh"), "--jobs", "8", "8", str(bad)],
                           capture_output=True, text=True, timeout=30)
        self.assertEqual(p.returncode, 2)
        self.assertIn("MemAvailable", p.stderr)

    def test_bad_shard_count_is_refused_before_any_ssh(self):
        for v in ("abc", "0", "-1"):
            self.env["GATE_SHARDS"] = v
            p = self.gate()
            self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
            self.assertIn("GATE_SHARDS", p.stderr)
        self.assertEqual(self.ssh_calls(), [])

    def test_no_merge_base_with_main_is_refused_not_guessed(self):
        git(self.tree, "update-ref", "-d", "refs/remotes/origin/main")
        p = self.gate()
        self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
        self.assertIn("no merge base", p.stderr)
        self.assertEqual(self.ssh_calls(), [])
        base = git(self.tree, "rev-parse", "HEAD^")
        self.env["GATE_BASE"] = base  # an explicit base is used as given
        p = self.gate()
        self.assertIn(f"base {base}", p.stdout)

    def test_dirty_tree_is_refused_before_any_ssh(self):
        (self.tree / "untracked.txt").write_text("x")
        for args in ((), ("--count",)):
            p = self.gate(*args)
            self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
            self.assertIn("refusing", p.stderr)
            self.assertIn("untracked.txt", p.stderr)
        (self.tree / "untracked.txt").unlink()
        (self.tree / "lib" / "cli.py").write_text("X = 2\n")  # tracked, modified
        self.assertEqual(self.gate().returncode, 2)
        self.assertEqual(self.ssh_calls(), [])

    def test_count_prints_the_test_count_as_last_line_without_ssh(self):
        p = self.gate("--count")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(p.stdout.strip().splitlines()[-1], "2")
        self.assertEqual(self.ssh_calls(), [])

    def test_plan_lists_every_step_and_the_header_maps_each(self):
        p = self.gate("--plan")
        self.assertEqual(p.returncode, 0, p.stderr)
        steps = p.stdout.split()
        self.assertIn("unittest", steps)
        self.assertIn("go-test", steps)
        self.assertIn("docs-check", steps)
        header = GATE_SH.read_text().split("set -euo pipefail")[0]
        for s in steps:
            stem = s.rstrip("0123456789").rstrip("-")
            self.assertRegex(header, rf"#   {stem}(-\d\.\.\d)?\s", f"step {s} not mapped to a tests.yml job in the header")
        remote = (ROOT / "ci" / "remote_gate_driver.sh").read_text()
        ran = [ln.split()[1] for ln in remote.splitlines() if ln.startswith("step ")]
        self.assertEqual([s for s in steps if not s.startswith("guard-") or s == "guard-build"], ran)

    def test_failed_run_exits_nonzero_cleans_up_and_ships_no_secrets(self):
        p = self.gate()
        self.assertNotEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("remote-gate-driver: FAIL", p.stdout)
        self.assertRegex(p.stdout, rf"removed gate-host:{RUN_DIR_RE}\n")
        self.assertEqual(self.run_dirs(p.stdout), [], "remote run dir left behind")
        calls = self.ssh_calls()
        self.assertTrue(all("SendEnv=-*" in c and "BatchMode=yes" in c for c in calls), calls)
        seen = json.loads(self.mark.read_text())
        for k in ("GH_TOKEN", "GITHUB_TOKEN", "SSH_AUTH_SOCK", "FACTORY_AGENT"):
            self.assertNotIn(k, seen["env"])
        self.assertNotIn("gh-secret-test", json.dumps(seen))
        # 12 asked: remote_gate.sh caps at 8 (host independent), the driver then caps at the host's cpu count and RAM
        self.assertIn("shards 8", p.stdout)
        ncpu = int(subprocess.run(["nproc"], capture_output=True, text=True, check=True).stdout)
        jobs = int(seen["env"]["GATE_SHARDS"])
        self.assertTrue(1 <= jobs <= min(8, ncpu), jobs)
        self.assertIn(f"jobs {jobs} (", p.stdout)
        run_dir = seen["env"]["HOME"].rsplit("/", 1)[0]
        self.assertRegex(run_dir, rf"^{RUN_DIR_RE}$")
        for k in ("FACTORY_HOME", "TMPDIR"):
            self.assertTrue(seen["env"][k].startswith(run_dir + "/"), seen["env"][k])
        # the head tree is the archive (plus the rebuilt history): no .ssh, .claude, db
        self.assertEqual(sorted(set(seen["files"]) - {".git"}), ["ci", "lib", "tests"])

    def test_head_ships_every_tracked_file_despite_export_attributes(self):
        # git archive would honour the PR's own .gitattributes: drop tests/ and rewrite the export-subst file
        cli = self.tree / "lib" / "cli.py"
        (self.tree / ".gitattributes").write_text("tests/** export-ignore\ntests export-ignore\n"
                                                  "lib/cli.py export-subst\n")
        cli.write_text("X = '$Format:%H$'\n")
        git(self.tree, "add", "-A")
        git(self.tree, "commit", "-q", "-m", "hide tests")
        p = self.gate()
        self.assertIn("remote-gate-driver: FAIL", p.stdout, p.stdout + p.stderr)
        seen = json.loads(self.mark.read_text())
        self.assertEqual(sorted(set(seen["files"]) - {".git"}), [".gitattributes", "ci", "lib", "tests"])
        self.assertIn("tests/test_a.py", seen["shipped"], "export-ignore hid tests/test_a.py from the gate")
        self.assertEqual(seen["cli"], "X = '$Format:%H$'\n", "export-subst rewrote a shipped file")
        self.assertEqual(self.run_dirs(p.stdout), [])

    def test_self_ignored_force_added_file_is_in_the_remote_diff(self):
        # a PR force-adds a file plus a .gitignore rule for it: the rebuilt repo must still diff it (docs-check
        # lints only the files in `git diff gate-base HEAD`)
        diff_out = self.tmp / "remote-diff.txt"
        (self.tree / "ci" / "shard.py").write_text(
            "#!/usr/bin/env python3\nimport subprocess\n"
            f"open({str(diff_out)!r}, 'w').write(subprocess.run(['git', 'diff', '--name-only', 'gate-base', 'HEAD'],"
            " capture_output=True, text=True, check=True).stdout)\n")
        (self.tree / "docs").mkdir()
        (self.tree / "docs" / "hidden.md").write_text("# broken [link](nowhere.md)\n")
        (self.tree / ".gitignore").write_text("docs/hidden.md\n")
        git(self.tree, "add", "-A")
        git(self.tree, "add", "-f", "docs/hidden.md")
        git(self.tree, "commit", "-q", "-m", "self-ignored doc")
        p = self.gate()
        self.assertIn("remote-gate-driver: FAIL", p.stdout, p.stdout + p.stderr)
        self.assertNotIn("rebuilt head repo is not clean", p.stdout + p.stderr)
        diffed = diff_out.read_text().split()
        self.assertIn("docs/hidden.md", diffed, "a self-ignored, force-added file is missing from the remote diff")
        self.assertIn(".gitignore", diffed)
        self.assertEqual(self.run_dirs(p.stdout), [])

    def test_tree_changed_after_the_checks_is_refused_and_nothing_is_shipped(self):
        cases = {"modified": f"echo 'X = 3' > {self.tree}/lib/cli.py",
                 "staged": f"cd {self.tree} && echo y > new.txt && git add new.txt",
                 "deleted": f"rm {self.tree}/tests/test_a.py",
                 "head moved": f"cd {self.tree} && git -c user.name=t -c user.email=t@example.invalid "
                               "-c commit.gpgsign=false commit -q --allow-empty -m moved"}
        start = git(self.tree, "rev-parse", "HEAD")
        for name, hook in cases.items():
            with self.subTest(name):
                git(self.tree, "reset", "-q", "--hard", start)
                git(self.tree, "clean", "-qfd")
                for f in (self.log, Path(f"{self.log}.hooked")):
                    f.unlink(missing_ok=True)
                self.env["STUB_FIRST_HOOK"] = hook
                p = self.gate()
                self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
                self.assertIn("refusing", p.stderr)
                self.assertIn("nothing shipped", p.stderr)
                calls = self.ssh_calls()
                self.assertTrue(calls, "the hook never ran")
                self.assertEqual([c for c in calls if "tar -x" in c], [], "a tree went over ssh")
                self.assertFalse(self.mark.exists(), "a step ran")
                self.assertEqual(self.run_dirs(p.stdout), [], "run dir left behind")

    def test_local_host_runs_the_driver_without_ssh(self):
        self.env["GATE_REMOTE_HOST"] = "local"
        self.env["HOME"] = str(self.remote)  # local mode keeps the lock under the caller's HOME
        p = self.gate()
        self.assertNotEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("remote-gate-driver: FAIL", p.stdout)
        self.assertEqual(self.ssh_calls(), [])
        seen = json.loads(self.mark.read_text())
        self.assertNotIn("GH_TOKEN", seen["env"])
        self.assertRegex(seen["env"]["HOME"], rf"^{RUN_DIR_RE}/home$")
        self.assertEqual(self.run_dirs(p.stdout), [])

    def test_host_named_like_this_machine_runs_locally_without_ssh(self):
        me = subprocess.run(["hostname", "-s"], capture_output=True, text=True).stdout.strip()
        self.env["GATE_REMOTE_HOST"] = me
        self.env["HOME"] = str(self.remote)
        p = self.gate()
        self.assertIn("is this host: running locally, no ssh", p.stdout)
        self.assertEqual(self.ssh_calls(), [])
        self.run_dirs(p.stdout)

    def test_other_host_name_still_goes_through_ssh(self):
        p = self.gate()
        self.assertNotIn("running locally", p.stdout)
        self.assertTrue(self.ssh_calls())
        self.run_dirs(p.stdout)

    def test_run_roots_come_from_the_env(self):
        self.env["GATE_RUN_ROOTS"] = "/nonexistent-gate-root"
        p = self.gate()
        self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
        self.assertIn("no usable run root on gate-host among /nonexistent-gate-root", p.stderr)
        self.env["GATE_RUN_ROOTS"] = "/x;rm"
        self.assertEqual(self.gate().returncode, 2)

    def test_sandbox_note_names_both_branches(self):
        drv = ROOT / "ci" / "remote_gate_driver.sh"
        stub = self.tmp / "nsstub"
        stub.mkdir()
        (stub / "unshare").write_text("#!/bin/sh\necho 'unshare: unshare failed: Operation not permitted' >&2\nexit 1\n")
        (stub / "unshare").chmod(0o755)
        env = {**os.environ, "PATH": f"{stub}:{os.environ['PATH']}"}
        no = subprocess.run(["bash", str(drv), "--sandbox-note"], capture_output=True, text=True, env=env)
        self.assertEqual(no.returncode, 0, no.stderr)
        self.assertIn("user namespaces unavailable", no.stdout)
        self.assertIn("Operation not permitted", no.stdout)
        (stub / "unshare").write_text("#!/bin/sh\nexit 0\n")
        yes = subprocess.run(["bash", str(drv), "--sandbox-note"], capture_output=True, text=True, env=env)
        self.assertIn("user namespaces available", yes.stdout)
        self.assertIn("nsbox-dependent tests run", yes.stdout)

    def test_sigterm_to_the_process_group_removes_the_remote_dir(self):
        self.commit_head(sleep=120)
        p = self.start(stderr=subprocess.STDOUT)
        for _ in range(600):
            if self.mark.exists():
                break
            time.sleep(0.1)
        self.assertTrue(self.mark.exists(), "the run never reached unittest")
        run_dir = json.loads(self.mark.read_text())["env"]["HOME"].rsplit("/", 1)[0]
        self.addCleanup(shutil.rmtree, run_dir, True)
        self.assertTrue(os.path.isdir(run_dir), run_dir)
        os.killpg(p.pid, signal.SIGTERM)
        out, _ = p.communicate(timeout=60)
        self.assertNotEqual(p.returncode, 0, out)
        self.assertEqual(self.run_dirs(out), [], out)
        self.assertFalse(os.path.exists(run_dir), out)

    def test_second_run_waits_for_the_remote_lock(self):
        (self.remote / "factory-gate").mkdir()
        with open(self.remote / "factory-gate" / ".lock", "w") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            # a live holder within its own limit is waited on, also when it has run longer than this waiter's 3600 s
            lock.write(self.holder_record(os.getpid(), int(time.time()) - 3700, limit=7200))
            lock.flush()
            p = self.start(stderr=subprocess.STDOUT)
            seen = []
            for line in p.stdout:
                seen.append(line)
                if "waiting" in line:
                    break
            self.assertIn("another gate run holds", "".join(seen))
            time.sleep(1)
            self.assertFalse(self.mark.exists(), "a step ran while the lock was held")
            fcntl.flock(lock, fcntl.LOCK_UN)
        out, _ = p.communicate(timeout=180)
        self.assertNotEqual(p.returncode, 0)
        self.assertTrue(self.mark.exists(), "the run did not continue after the lock was released")
        self.assertEqual(self.run_dirs("".join(seen) + out), [], out)


if __name__ == "__main__":
    unittest.main()
