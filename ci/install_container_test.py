#!/usr/bin/env python3
"""Clean-container test of the hivemill installer. Run it as root inside a bare debian or ubuntu image:

  python3 ci/install_container_test.py --mode head|latest [--workspace DIR] [--sha SHA] [--repo-url URL]

head   installs the checked-out tree (file:// clone of the workspace at --sha).
latest installs the newest release tag of the public repository (--ref latest).

The installer builds hivemill and worker-guard with `go build`, so root's `go` (actions/setup-go puts it on PATH
before this runs) must be on PATH; its directory goes on the plain user's PATH. The newest release may predate the
hivemill-only installer: latest mode shallow-clones that tag first and skips (exit 0) when it has no go/cmd/hivemill.

It installs git, python3 and ca-certificates, checks that the installer refuses root, creates a plain user with a
fake `claude` and a fake `gh`, runs `--check` and `--yes --no-systemd` as that user in a clean environment, and
checks the result: RESULT PASS, a verified herdr download, ~/.local/bin/hivemill and ~/.local/bin/worker-guard
linked, and `hivemill health` answering as the installer's smoke step expects (no database yet after an install).
A second `--yes` must exit 4 and leave the clone untouched. No secrets, no Claude or GitHub login: the fakes only
answer the few commands the installer asks. Standard library only; exit 0 = all checks passed.
"""
import argparse
import os
import pwd
import re
import shlex
import shutil
import stat
import subprocess
import sys
import tempfile

USER = "hmtest"
PUBLIC_URL = "https://github.com/volekerb/hivemill.git"
INSTALLER = "plugins/hivemill/scripts/install.py"
APT_PACKAGES = ["git", "python3", "ca-certificates"]
BUILT = ("hivemill", "worker-guard")  # what the installer builds from the clone and links into ~/.local/bin
TAG_RE = re.compile(r"^v(\d+)\.(\d+)\.(\d+)$")  # release tags, as install.py's resolve_latest matches them

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


def check_rerun_refused(runner, cmd, mtime):
    """A second --yes exits 4 and changes nothing in the clone. `mtime()` -> mtime_ns of a file of the clone."""
    before = mtime()
    rc, out = runner(cmd)
    need(rc == 4, "second --yes exits 4 (got %s)" % rc, out)
    need("nothing was touched" in out, "the rerun says nothing was touched", out)
    need(mtime() == before, "clone mtime unchanged by the rerun")


def check_health(rc, out):
    """`hivemill health` right after an install: rc 0, or rc 1 because there is no database yet (the smoke step)."""
    need(rc == 0 or (rc == 1 and "cannot open database" in out),
         "hivemill health exits 0, or 1 with no database yet (rc=%s)" % rc, out)


def newest_tag(ls_remote_output):
    """The highest ^vX.Y.Z$ tag among `git ls-remote --tags` lines, or None (the sort of install.py's resolve_ref)."""
    best = None
    for ln in ls_remote_output.splitlines():
        parts = ln.split()
        if len(parts) != 2 or not parts[1].startswith("refs/tags/"):
            continue
        name = parts[1][len("refs/tags/"):]
        if name.endswith("^{}"):
            name = name[:-3]
        m = TAG_RE.match(name)
        if m:
            key = tuple(int(x) for x in m.groups())
            if best is None or key > best[0]:
                best = (key, name)
    return best[1] if best else None


def latest_predates_hivemill(repo_url):
    """-> (tag, True when that tag lacks go/cmd/<b>/ for any b in BUILT). Shallow-clones the newest release tag as root."""
    rc, out = run(["git", "ls-remote", "--tags", repo_url], timeout=120)
    need(rc == 0, "git ls-remote --tags %s (rc=%s)" % (repo_url, rc), out)
    tag = newest_tag(out)
    need(tag is not None, "%s has a vX.Y.Z release tag" % repo_url, out)
    with tempfile.TemporaryDirectory() as d:
        dst = os.path.join(d, "release")
        rc, out = run(["git", "clone", "-q", "--depth", "1", "--branch", tag, repo_url, dst], timeout=300)
        need(rc == 0, "shallow clone of %s (rc=%s)" % (tag, rc), out)
        return tag, not all(os.path.isdir(os.path.join(dst, "go", "cmd", b)) for b in BUILT)


def go_bin_dir():
    """The directory of root's real `go` binary (setup-go's tool cache), or Fail when there is no go on PATH."""
    go = shutil.which("go")
    if not go:
        raise Fail("no `go` on PATH: the installer builds hivemill with go build; run actions/setup-go before this script")
    return os.path.dirname(os.path.realpath(go))


def user_path(home, go_dir):
    """The plain user's PATH: its ~/.local/bin, the Go toolchain's bin dir, then the system dirs."""
    return "%s/.local/bin:%s:/usr/local/bin:/usr/bin:/bin" % (home, go_dir)


def write_exec(path, text, uid, gid):
    with open(path, "w") as f:
        f.write(text)
    os.chmod(path, 0o755)
    os.chown(path, uid, gid)


def user_cmd(home, go_dir, argv):
    """argv as the plain user in a clean environment, with the Go toolchain on PATH."""
    env = ["HOME=" + home, "PATH=" + user_path(home, go_dir), "LANG=C.UTF-8"]
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
    if a.mode == "latest":
        tag, old = latest_predates_hivemill(a.repo_url)
        if old:
            print("SKIP: latest release %s predates the hivemill-only installer (no go/cmd/hivemill or go/cmd/worker-guard)" % tag)
            return 0
    go_dir = go_bin_dir()

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
        return run(user_cmd(home, go_dir, argv_))

    rc, out = as_user(["go", "version"])
    need(rc == 0, "%s can run the Go toolchain in %s (rc=%s; it must be readable by every user)" % (USER, go_dir, rc), out)

    # d. check, then install
    rc, out = as_user([py, installer, "--check", "--no-systemd"])
    need(rc == 3 and re.search(r"^missing herdr ", out, re.M) is not None,
         "--check prints the report and exits 3 with only herdr missing (rc=%s)" % rc, out)
    need(re.search(r"^ok claude ", out, re.M) is not None and re.search(r"^ok gh-auth ", out, re.M) is not None,
         "--check sees the fake claude and the fake gh login", out)
    need(re.search(r"^ok go ", out, re.M) is not None, "--check sees the Go toolchain", out)
    if a.mode == "head":
        sha = a.sha or run(["git", "-C", ws, "-c", "safe.directory=" + ws, "rev-parse", "HEAD"])[1].strip()
        need(re.fullmatch(r"[0-9a-f]{40}", sha) is not None, "head sha is %s" % sha)
        # git refuses to read a repository owned by another user (it also drops per-process config when it spawns
        # upload-pack for a local clone), so the plain user clones from its own copy: no git config is changed
        src = os.path.join(home, "workspace-copy")
        rc, out = run(["cp", "-a", ws, src])
        need(rc == 0, "copied the workspace for the plain user", out)
        rc, out = run(["chown", "-R", "%d:%d" % (pw.pw_uid, pw.pw_gid), src])
        need(rc == 0, "gave the copy to %s" % USER, out)
        which = ["--repo-url", "file://" + src, "--ref", sha]
    else:
        which = ["--repo-url", a.repo_url, "--ref", "latest"]
    cmd = [py, installer, "--yes", "--no-systemd"] + which
    print("$ " + " ".join(shlex.quote(c) for c in cmd), flush=True)
    rc, out = as_user(cmd)
    print(out, flush=True)

    # e. result
    last = [ln for ln in out.splitlines() if ln.strip()][-1:] or [""]
    need(rc == 0 and re.match(r"^RESULT PASS", last[0]) is not None, "installer exit 0 and last line RESULT PASS (rc=%s, last=%r)" % (rc, last[0]), out)
    need(re.search(r"^\[ok\] \d+ tools: .*herdr v[\d.]+ \(size\+sha256 verified\)", out, re.M) is not None,
         "herdr downloaded and verified against the pinned size and sha256", out)
    need(os.access(os.path.join(bindir, "herdr"), os.X_OK), "herdr is installed in ~/.local/bin")
    for name in BUILT:
        need(os.access(os.path.join(bindir, name), os.X_OK), "%s is built and linked in ~/.local/bin" % name)
    check_health(*as_user(["hivemill", "health"]))

    # f. rerun
    built = os.path.join(home, "factory", "bin", "hivemill")
    check_rerun_refused(lambda c: as_user(c), [py, installer, "--yes", "--no-systemd"] + which, lambda: os.stat(built).st_mtime_ns)
    print("ALL CHECKS PASSED (%s)" % a.mode)
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Fail as e:
        print("FAIL: %s" % e, file=sys.stderr)
        sys.exit(1)
