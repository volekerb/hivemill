#!/usr/bin/env python3
"""Clean-container test of the hivemill installer. Run it as root inside a bare debian or ubuntu image:

  python3 ci/install_container_test.py --mode head|latest [--workspace DIR] [--sha SHA] [--repo-url URL]

head   installs the checked-out tree (file:// clone of the workspace at --sha).
latest installs the newest release tag of the public repository (--ref latest).

It installs git, python3 and ca-certificates, checks that the installer refuses root, creates a plain user with a
fake `claude` and a fake `gh`, runs `--check` and `--yes --no-systemd` as that user in a clean environment, and
checks the result: RESULT PASS, a verified herdr download, boss and director listed, the UI answering 200 on
loopback only. A second `--yes` must exit 4 and leave the database untouched. No secrets, no Claude or GitHub
login: the fakes only answer the few commands the installer asks. Standard library only; exit 0 = all checks passed.
"""
import argparse
import os
import pwd
import re
import shlex
import stat
import subprocess
import sys
import time
import urllib.error
import urllib.request

USER = "hmtest"
PORT = 8765
PUBLIC_URL = "https://github.com/volekerb/hivemill.git"
INSTALLER = "plugins/hivemill/scripts/install.py"
APT_PACKAGES = ["git", "python3", "ca-certificates"]
LISTEN = "0A"  # TCP_LISTEN in /proc/net/tcp
LOOPBACK_V4 = "0100007F"  # 127.0.0.1 as /proc/net/tcp prints it
LOOPBACK_V6 = "00000000000000000000000001000000"  # ::1

# Kept as written files: tests/stubs is on the test harness PATH and a `claude` there would change other tests.
FAKE_CLAUDE = """#!/bin/sh
# fake claude: answers --version and otherwise stays alive like an agent pane would
if [ "$1" = "--version" ]; then
  echo "2.0.0 (Claude Code)"
  exit 0
fi
while true; do sleep 30; done
"""
FAKE_GH = """#!/bin/sh
# fake gh: logged in, never talks to GitHub
case "$1" in
  --version) echo "gh version 2.0.0 (fake)"; exit 0 ;;
  auth) [ "$2" = "status" ] && { echo "Logged in (fake)"; exit 0; } ;;
esac
echo "fake gh: unsupported: $*" >&2
exit 1
"""


class Fail(Exception):
    """A check of this test failed; the message says which and shows the evidence."""


def run(argv, timeout=900, env=None):
    """-> (returncode, merged output). Never reads stdin."""
    try:
        p = subprocess.run(argv, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, stdin=subprocess.DEVNULL,
                           timeout=timeout, env=env, universal_newlines=True, errors="replace")
    except subprocess.TimeoutExpired:
        return 124, "timed out after %ss: %s" % (timeout, " ".join(argv))
    except OSError as e:
        return 127, "%s: %s" % (argv[0], e)
    return p.returncode, p.stdout


def need(cond, what, out=""):
    if not cond:
        tail = "\n".join(out.splitlines()[-25:])
        raise Fail("%s%s" % (what, "\n--- output (tail) ---\n" + tail if tail else ""))
    print("ok: %s" % what, flush=True)


def check_root_refusal(runner, cmd):
    """The installer must refuse to run as root: exit 1 and say so. `runner(cmd)` -> (rc, output)."""
    rc, out = runner(cmd)
    need(rc == 1, "installer run as root exits 1 (got %s)" % rc, out)
    need("refusing to run as root" in out, "the root refusal names the reason", out)


def check_rerun_refused(runner, cmd, db_mtime):
    """A second --yes exits 4 and leaves the database file alone. `db_mtime()` -> mtime_ns."""
    before = db_mtime()
    rc, out = runner(cmd)
    need(rc == 4, "second --yes exits 4 (got %s)" % rc, out)
    need("already installed" in out, "the rerun says already installed", out)
    need(db_mtime() == before, "database mtime unchanged by the rerun")


def listeners(port, tcp4_text, tcp6_text):
    """Local addresses (hex, as in /proc/net/tcp) of the sockets listening on `port`."""
    found = []
    for text in (tcp4_text, tcp6_text):
        for ln in text.splitlines()[1:]:
            f = ln.split()
            if len(f) > 3 and f[3] == LISTEN:
                addr, _, p = f[1].rpartition(":")
                if int(p, 16) == port:
                    found.append(addr)
    return found


def read_file(path):
    try:
        with open(path) as f:
            return f.read()
    except OSError:
        return ""


def check_loopback_only(port):
    # no iproute2 in a bare image and the spec forbids more packages: /proc/net/tcp is what `ss -ltn` prints from
    found = listeners(port, read_file("/proc/net/tcp"), read_file("/proc/net/tcp6"))
    need(bool(found), "something listens on port %d (got %r)" % (port, found))
    need(all(a in (LOOPBACK_V4, LOOPBACK_V6) for a in found), "port %d listens on loopback only (got %r)" % (port, found))


def http_status(url):
    try:
        with urllib.request.urlopen(url, timeout=10) as r:
            return r.status
    except urllib.error.HTTPError as e:
        return e.code
    except (OSError, ValueError):
        return 0


def write_exec(path, text, uid, gid):
    with open(path, "w") as f:
        f.write(text)
    os.chmod(path, 0o755)
    os.chown(path, uid, gid)


def user_cmd(home, argv, safe_dir):
    """argv as the plain user in a clean environment. The git safe.directory entry is passed through the environment
    (command scope, this process tree only): git refuses a workspace owned by another user and the global git config
    stays untouched."""
    env = ["HOME=" + home, "PATH=%s/.local/bin:/usr/local/bin:/usr/bin:/bin" % home, "LANG=C.UTF-8",
           "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=safe.directory", "GIT_CONFIG_VALUE_0=" + safe_dir,
           "GIT_CONFIG_KEY_1=safe.directory", "GIT_CONFIG_VALUE_1=" + safe_dir + "/.git"]
    return ["runuser", "-u", USER, "--", "env", "-i"] + env + argv


def main(argv=None):
    ap = argparse.ArgumentParser(description="installer smoke test for a bare container (run as root)")
    ap.add_argument("--mode", choices=("head", "latest"), required=True)
    ap.add_argument("--workspace", default=os.getcwd())
    ap.add_argument("--sha", default="")
    ap.add_argument("--repo-url", default=PUBLIC_URL)
    a = ap.parse_args(argv)
    ws = os.path.realpath(a.workspace)
    installer = os.path.join(ws, INSTALLER)
    py = sys.executable or "python3"

    if os.geteuid() != 0:
        raise Fail("run this as root inside a throwaway container; it creates a user and installs packages")
    env = dict(os.environ, DEBIAN_FRONTEND="noninteractive")
    for step in (["apt-get", "update", "-qq"], ["apt-get", "install", "-y", "-qq", "--no-install-recommends"] + APT_PACKAGES):
        rc, out = run(step, env=env)
        need(rc == 0, "%s (rc=%s)" % (" ".join(step[:2]), rc), out)

    # b. root refusal
    check_root_refusal(lambda c: run(c), [py, installer, "--yes", "--no-systemd"])  # refused before any step

    # c. plain user with the two fakes
    rc, out = run(["useradd", "-m", "-s", "/bin/bash", USER])
    need(rc == 0, "created user %s" % USER, out)
    pw = pwd.getpwnam(USER)
    home = pw.pw_dir
    bindir = os.path.join(home, ".local", "bin")
    os.makedirs(bindir, exist_ok=True)
    os.makedirs(os.path.join(home, ".claude"), exist_ok=True)
    for d in (os.path.join(home, ".local"), bindir, os.path.join(home, ".claude")):
        os.chown(d, pw.pw_uid, pw.pw_gid)
    write_exec(os.path.join(bindir, "claude"), FAKE_CLAUDE, pw.pw_uid, pw.pw_gid)
    write_exec(os.path.join(bindir, "gh"), FAKE_GH, pw.pw_uid, pw.pw_gid)
    cred = os.path.join(home, ".claude", ".credentials.json")  # the installer only checks that the file exists
    with open(cred, "w") as f:
        f.write("{}\n")
    os.chown(cred, pw.pw_uid, pw.pw_gid)
    os.chmod(cred, stat.S_IRUSR | stat.S_IWUSR)

    def as_user(argv_):
        return run(user_cmd(home, argv_, ws))

    proj = os.path.join(home, "demo-project")
    for g in (["git", "init", "-q", proj],
              ["git", "-C", proj, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "init"]):
        rc, out = as_user(g)
        need(rc == 0, "project repo: %s" % " ".join(g[:4]), out)

    # d. check, then install
    rc, out = as_user([py, installer, "--check", "--no-systemd"])
    need(rc == 3 and re.search(r"^missing herdr ", out, re.M) is not None,
         "--check prints the report and exits 3 with only herdr missing (rc=%s)" % rc, out)
    need(re.search(r"^ok claude ", out, re.M) is not None and re.search(r"^ok gh-auth ", out, re.M) is not None,
         "--check sees the fake claude and the fake gh login", out)
    if a.mode == "head":
        sha = a.sha or run(["git", "-C", ws, "-c", "safe.directory=" + ws, "rev-parse", "HEAD"])[1].strip()
        need(re.fullmatch(r"[0-9a-f]{40}", sha) is not None, "head sha is %s" % sha)
        which = ["--repo-url", "file://" + ws, "--ref", sha]
    else:
        which = ["--repo-url", a.repo_url, "--ref", "latest"]
    cmd = [py, installer, "--yes", "--no-systemd", "--no-scout", "--project-id", "demo", "--project-name", "Demo",
           "--project-repo", proj] + which
    print("$ " + " ".join(shlex.quote(c) for c in cmd), flush=True)
    rc, out = as_user(cmd)
    print(out, flush=True)

    # e. result
    last = [ln for ln in out.splitlines() if ln.strip()][-1:] or [""]
    need(rc == 0 and re.match(r"^RESULT PASS", last[0]) is not None, "installer exit 0 and last line RESULT PASS (rc=%s, last=%r)" % (rc, last[0]), out)
    need(re.search(r"^\[ok\] \d+ tools: .*herdr v[\d.]+ \(size\+sha256 verified\)", out, re.M) is not None,
         "herdr downloaded and verified against the pinned size and sha256", out)
    need(os.access(os.path.join(bindir, "herdr"), os.X_OK), "herdr is installed in ~/.local/bin")
    rc, out = as_user(["factory", "agents"])
    need(rc == 0 and re.search(r"\bboss\b", out) is not None and re.search(r"\bdirector\b", out) is not None,
         "factory agents lists boss and director", out)
    code = http_status("http://127.0.0.1:%d/" % PORT)
    need(code == 200, "GET / answers 200 (got %s)" % code)
    check_loopback_only(PORT)

    # f. rerun
    db = os.path.join(home, "factory", "db", "factory.db")
    check_rerun_refused(lambda c: as_user(c), [py, installer, "--yes", "--no-systemd"], lambda: os.stat(db).st_mtime_ns)
    print("ALL CHECKS PASSED (%s)" % a.mode)
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Fail as e:
        print("FAIL: %s" % e, file=sys.stderr)
        sys.exit(1)
