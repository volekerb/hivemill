#!/usr/bin/env python3
"""One-command fresh-install test: a throwaway user follows docs/SETUP.md up to `factory up` and the web UI.

  sudo python3 ci/freshbox_test.py [--repo URL] [--ref REF] [--user NAME] [--port N] [--out PATH]
                                   [--herdr-bin PATH] [--max-total-claude N] [--gh-token-file PATH] [--dry-run]

Run it in your own terminal (two interactive logins need a TTY). It creates a user, installs herdr and Claude
Code for that user, follows the docs, and always deletes the user again. It starts REAL Claude agents for
about two minutes. `--dry-run` needs no root and no TTY and only prints the plan. Standard library only;
the runner (root's python3) works on Python 3.8 and newer. See docs/SETUP.md, "Test a fresh install".
"""
import argparse
import datetime as dt
import getpass
import hashlib
import os
import pwd
import re
import shlex
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request

# The publisher sets the real public URL here (or users pass --repo).
DEFAULT_REPO = "https://github.com/example-org/factory.git"
CLAUDE_INSTALLER_URL = "https://claude.ai/install.sh"
USER_AGENT = "factory-freshbox/1.0"
# herdr: official public release asset, pinned. A size or sha256 mismatch aborts before anything is installed.
# Source: https://github.com/herdrdev/herdr (Apache-2.0, homepage https://herdr.dev), raw binaries, no archive.
HERDR_VERSION = "v0.9.5"
HERDR_RELEASE = "https://github.com/herdrdev/herdr/releases/download/" + HERDR_VERSION
HERDR_ASSETS = {  # normalised machine -> (url, size_bytes, sha256), from the GitHub release asset digests
    "x86_64": (HERDR_RELEASE + "/herdr-linux-x86_64", 30285672,
               "14d9e8bc8b4a9e0118d77c32266f76d461f09b40342eb23752960bd89f59929d"),
    "aarch64": (HERDR_RELEASE + "/herdr-linux-aarch64", 27900752,
                "17d7536f2da50859523680ef279dffcac8e4a235f4e46fe8da6c2ec44141897c"),
}
ARCH_ALIAS = {"x86_64": "x86_64", "amd64": "x86_64", "aarch64": "aarch64", "arm64": "aarch64"}
HOME_BASE = "/home"
USER_RE = re.compile(r"^[a-z_][a-z0-9_-]{0,30}$")
CLAUDE_HEADROOM = 4
MIN_TOKEN_LEN = 20  # shorter than any GitHub token; redacting a tiny string would mangle the log
TAIL_LINES = 15
GH_INSTALL_HINT = "install gh: https://github.com/cli/cli/blob/trunk/docs/install_linux.md"

REDACT = [re.compile(p) for p in (
    r"gh[opsur]_[A-Za-z0-9]{20,}", r"gho_[A-Za-z0-9_]{10,}", r"github_pat_[A-Za-z0-9_]{10,}", r"sk-ant-[A-Za-z0-9_\-]{10,}",
    r"(?i)authorization:[^\r\n]*", r"(?<=://)[^/@\s]+@")]


def redact(text):
    for rx in REDACT:
        text = rx.sub("[REDACTED]", text)
    return text


class StepFail(Exception):
    pass


class Inconclusive(Exception):
    """The step could not decide because the host was busy; not a product failure."""


class Interrupted(BaseException):
    pass


class Refused(Exception):
    pass


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


class Host:
    """Everything that touches the machine. Tests replace it; the plan (dry run) never calls it."""

    def euid(self):
        return os.geteuid()

    def isatty(self):
        return sys.stdin.isatty()

    def user_exists(self, name):
        try:
            pwd.getpwnam(name)
            return True
        except KeyError:
            return False

    def pw_dir(self, name):
        return pwd.getpwnam(name).pw_dir

    def ids(self, name):
        p = pwd.getpwnam(name)
        return p.pw_uid, p.pw_gid

    def port_busy(self, port):
        with socket.socket() as s:
            try:
                s.bind(("127.0.0.1", port))
            except OSError:
                return True
        return False

    def lexists(self, path):
        return os.path.lexists(path)

    def which(self, name):
        return shutil.which(name)

    def count_claude(self):
        n = 0
        for d in os.listdir("/proc"):
            if d.isdigit():
                try:
                    with open("/proc/%s/comm" % d) as f:
                        n += f.read().strip() == "claude"
                except OSError:
                    pass
        return n

    def run(self, argv, capture=True, timeout=None, cwd=None, input_text=None):
        """-> (returncode, merged output). capture=False passes the TTY through and returns no output.
        input_text is fed on stdin (never on argv)."""
        try:
            if capture:
                p = subprocess.run(argv, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=timeout, cwd=cwd,
                                   stdin=subprocess.DEVNULL if input_text is None else None, input=input_text,
                                   universal_newlines=True, errors="replace")
                return p.returncode, p.stdout
            return subprocess.run(argv, cwd=cwd).returncode, ""
        except subprocess.TimeoutExpired:
            return 124, "timed out after %ss" % timeout
        except FileNotFoundError as e:
            return 127, str(e)

    def read_token(self, path):
        """The GitHub token for the test user: `-` prompts on the TTY without echo, anything else is a file."""
        if path == "-":
            return getpass.getpass("GitHub token for the test user (input hidden, then Enter): ", stream=sys.stderr)
        with open(path) as f:
            return f.read()

    def popen(self, argv, logpath, cwd=None):
        fd = os.open(logpath, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
        try:
            return subprocess.Popen(argv, stdout=fd, stderr=subprocess.STDOUT, stdin=subprocess.DEVNULL, cwd=cwd)
        finally:
            os.close(fd)

    def download(self, url, dest):
        if not url.startswith("https://"):
            raise StepFail("refusing non-https download %s" % url)
        req = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})  # the default Python-urllib UA gets a 403 from claude.ai
        try:
            with urllib.request.urlopen(req, timeout=60) as r, open(dest, "wb") as f:
                shutil.copyfileobj(r, f)
        except urllib.error.HTTPError as e:
            raise StepFail("HTTP %s for %s" % (e.code, url))
        except urllib.error.URLError as e:
            raise StepFail("cannot fetch %s: %s" % (url, e.reason))

    def http_status(self, url):
        try:
            with urllib.request.urlopen(urllib.request.Request(url, headers={"User-Agent": USER_AGENT}), timeout=10) as r:
                return r.status
        except urllib.error.HTTPError as e:
            return e.code
        except (OSError, ValueError):
            return 0

    def sleep(self, s):
        time.sleep(s)

    def mkdtemp(self):
        d = tempfile.mkdtemp(prefix="freshbox-")
        os.chmod(d, 0o755)
        return d

    def rmtree(self, d):
        shutil.rmtree(d, ignore_errors=False)

    def open_results(self, path, uid, gid):
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        os.fchmod(fd, 0o600)
        os.fchown(fd, uid, gid)
        return os.fdopen(fd, "w")


class Ctx:
    """Shared state of one run. In plan mode `do` only records the argv it would run."""

    def __init__(self, host, args, env, plan, out=None):
        self.host, self.args, self.env, self.plan = host, args, env, plan
        self.out = out or sys.stdout
        self.user = args.user
        self.home = "%s/%s" % (HOME_BASE, args.user)
        self.fh = self.home + "/factory"
        self.tmp = "<tmpdir>" if plan else None
        self.created = False
        self.procs = []
        self.lines = []
        self.results = None
        self.results_path = None
        self.n_steps = 0
        self.failed = []
        self.inconclusive = []
        self.gh_token = None

    # -- logging
    def emit(self, text):
        text = redact(text)
        if self.gh_token:
            text = text.replace(self.gh_token, "[REDACTED]")
        self.lines.append(text)
        try:  # a closed stdout or a full disk must never abort cleanup
            print(text, file=self.out)
            self.out.flush()
            if self.results:
                self.results.write(text + "\n")
                self.results.flush()
        except (OSError, ValueError):
            pass

    def step_line(self, status, n, name, detail=""):
        self.emit("[%s] %s %s: %s" % (status, n, name, detail))

    # -- commands
    def as_user(self, cmd):
        e = ["HOME=" + self.home, "USER=" + self.user, "LOGNAME=" + self.user, "SHELL=/bin/bash",
             "PATH=%s/.local/bin:/usr/local/bin:/usr/bin:/bin" % self.home, "XDG_RUNTIME_DIR=" + self.home + "/.xdg-runtime",
             "LANG=C.UTF-8", "TERM=" + self.env.get("TERM", "xterm")]
        return ["runuser", "-u", self.user, "--", "env", "-i"] + e + list(cmd)

    def do(self, argv, tty=False, timeout=120, cwd=None, check=True, what=None, idle_rc=None, stdin_text=None, stdin_note=None):
        """Run (or plan) one argv. Returns (rc, out). Output tails are logged unless it is a login (tty).
        idle_rc: an rc that only means "nothing to do" (logged as such, not as a failure)."""
        if self.plan:
            self.emit("    $ %s%s%s" % (shlex.join(argv), "   (cwd %s)" % cwd if cwd else "", "   (%s)" % stdin_note if stdin_note else ""))
            return 0, ""
        if stdin_text is None:
            rc, out = self.host.run(argv, capture=not tty, timeout=timeout, cwd=cwd)
        else:
            rc, out = self.host.run(argv, capture=True, timeout=timeout, cwd=cwd, input_text=stdin_text)
        if not tty and idle_rc is not None and rc == idle_rc:
            self.emit("    $ %s -> nothing to clean" % (what or argv[0]))
        elif not tty:
            tail = "\n".join(out.strip().splitlines()[-TAIL_LINES:])
            self.emit("    $ %s -> rc=%s%s" % (what or argv[0], rc, ("\n" + indent(tail)) if tail else ""))
        if check and rc != 0:
            raise StepFail("%s failed (rc=%s)%s" % (what or shlex.join(argv), rc, ": " + out.strip().splitlines()[-1] if out.strip() else ""))
        return rc, out

    def background(self, argv, name):
        if self.plan:
            self.emit("    $ %s   (background)" % shlex.join(argv))
            return None
        p = self.host.popen(argv, os.path.join(self.tmp, name + ".log"), cwd=self.home)
        self.procs.append(p)
        return p

    def note(self, text):
        self.emit("    " + text)


def indent(text):
    return "\n".join("      " + ln for ln in text.splitlines())


# ───────────────────────────── steps ─────────────────────────────

def step_preflight(c):
    a = c.args
    if c.plan:
        c.note("refuse unless euid 0 and stdin is a TTY (exit 2)")
        c.note("refuse if user %r exists, equals $SUDO_USER or root, or port %d is in use" % (a.user, a.port))
        c.note("refuse if the test home %s or the temp dir resolves under the invoking user's home" % c.home)
        return "plan"
    h, env = c.host, c.env
    if h.euid() != 0:
        raise Refused("must run as root: sudo python3 ci/freshbox_test.py")
    if not h.isatty():
        raise Refused("stdin is not a TTY; run it in your own terminal (two logins are interactive)")
    invoker = env.get("SUDO_USER", "")
    if not invoker or invoker == "root":
        raise Refused("SUDO_USER is not set to a normal user; run it with sudo from your own account")
    if a.user in (invoker, "root"):
        raise Refused("--user %s is the invoking user or root; pick a throwaway name" % a.user)
    if h.user_exists(a.user):
        raise Refused("user %s already exists; this tool never touches an existing user (pick another --user)" % a.user)
    for leftover in (c.home, "/var/mail/" + a.user):
        if h.lexists(leftover):
            raise Refused("%s already exists but user %s does not; userdel -r would delete it. Pick another --user" % (leftover, a.user))
    if h.port_busy(a.port):
        raise Refused("port %d is in use; pass --port" % a.port)
    inv_home = os.path.realpath(h.pw_dir(invoker))
    for p in (c.home,):
        if under(os.path.realpath(p), inv_home):
            raise Refused("%s would be under the invoking user's home %s" % (p, inv_home))
    return "root, TTY, user %s free, port %d free" % (a.user, a.port)


def under(path, root):
    return path == root or path.startswith(root.rstrip("/") + "/")


def step_user(c):
    if not c.plan:
        c.tmp = c.host.mkdtemp()
        inv = os.path.realpath(c.host.pw_dir(c.env["SUDO_USER"]))
        if under(os.path.realpath(c.tmp), inv):
            raise StepFail("temp dir %s is under the invoking user's home; set TMPDIR elsewhere" % c.tmp)
    rc = None
    try:
        rc, _ = c.do(["useradd", "-m", "-d", c.home, "-s", "/bin/bash", "--", c.user], what="useradd", check=False)
    finally:
        # preflight proved the user did not exist. rc 0 means this run made it; rc 9 means someone else did, so never
        # touch it. Any other outcome (timeout, signal) with the user present is treated as ours.
        if not c.plan and rc != 9 and c.host.user_exists(c.user):
            c.created = True
    if rc == 9:
        raise StepFail("user appeared after preflight; not touching it")
    if rc != 0:
        raise StepFail("useradd failed (rc=%s)" % rc)
    c.do(["install", "-d", "-m", "700", "-o", c.user, "-g", c.user, c.home + "/.xdg-runtime"], what="install xdg dir")
    return "created %s; linger not enabled: docs/SETUP.md needs it only for the boot units, not for `factory up`" % c.user


def step_herdr(c):
    a = c.args
    dest_dir = c.home + "/.local/bin"
    c.do(["install", "-d", "-m", "755", "-o", c.user, "-g", c.user, c.home + "/.local", dest_dir], what="install bin dir")
    if a.herdr_bin:
        if not c.plan and not os.path.isfile(a.herdr_bin):
            raise StepFail("--herdr-bin %s is not a file" % a.herdr_bin)
        src = a.herdr_bin
        how = "copied the binary from --herdr-bin (NOT checked against the pin), sha256 %s" % ("<sha256>" if c.plan else sha256_file(src))
    else:
        src = "<tmpdir>/herdr" if c.plan else os.path.join(c.tmp, "herdr")
        if c.plan:
            c.note("download pinned herdr %s for this arch (size+sha256 verified) -> %s; x86_64: %s" % (
                HERDR_VERSION, src, HERDR_ASSETS["x86_64"][0]))
        else:
            machine = os.uname().machine
            asset = HERDR_ASSETS.get(ARCH_ALIAS.get(machine, ""))
            if not asset:
                raise StepFail("no pinned herdr release for %s; use --herdr-bin" % machine)
            url, size, sha = asset
            c.host.download(url, src)
            got = os.path.getsize(src)
            if got != size:
                raise StepFail("herdr size mismatch: got %d, pinned %d; nothing installed" % (got, size))
            if sha256_file(src) != sha:
                raise StepFail("herdr sha256 mismatch; nothing installed")
        how = "downloaded herdr %s, size+sha256 verified" % HERDR_VERSION
    c.do(["install", "-m", "755", "-o", c.user, "-g", c.user, src, dest_dir + "/herdr"], what="install herdr")
    return how


def step_claude(c):
    path = "<tmpdir>/claude-install.sh" if c.plan else os.path.join(c.tmp, "claude-install.sh")
    if c.plan:
        c.note("download %s -> %s (https, to a file, sha256 logged; never piped into a shell)" % (CLAUDE_INSTALLER_URL, path))
        digest = "<sha256>"
    else:
        c.host.download(CLAUDE_INSTALLER_URL, path)
        os.chmod(path, 0o644)
        digest = sha256_file(path)
    c.do(c.as_user(["bash", path]), cwd=c.home, timeout=600, what="claude installer")
    return "installer sha256 %s, run as %s" % (digest, c.user)


def step_prereqs(c):
    py = "import sys; sys.exit(0 if sys.version_info >= (3, 11) else 1)"
    missing = []
    for name, cmd in (("python3>=3.11", ["python3", "-c", py]), ("git", ["git", "--version"]), ("gh", ["gh", "--version"]),
                      ("herdr", ["herdr", "--version"]), ("claude", ["claude", "--version"])):
        rc, _ = c.do(c.as_user(cmd), cwd=c.home, check=False, what=name)
        if rc != 0:
            missing.append(name)
    if missing:
        hint = "; " + GH_INSTALL_HINT if "gh" in missing else ""
        raise StepFail("missing or too old for %s: %s%s" % (c.user, ", ".join(missing), hint))
    return "python3>=3.11, git, gh, herdr, claude present"


def step_logins(c):
    tokfile = c.args.gh_token_file
    c.emit(">>> Log in to Claude Code: complete /login, then type /exit")
    c.do(c.as_user(["claude"]), tty=True, cwd=c.home, timeout=None, what="claude")
    if tokfile:
        c.emit(">>> Logging in to GitHub with the token from %s (fed on stdin)" % ("stdin" if tokfile == "-" else tokfile))
        c.do(c.as_user(["gh", "auth", "login", "--with-token"]), cwd=c.home, what="gh auth login --with-token",
             stdin_text=(c.gh_token + "\n") if c.gh_token else None, stdin_note="token on stdin only")
        c.do(c.as_user(["gh", "auth", "setup-git"]), cwd=c.home, what="gh auth setup-git")
        return "claude login, gh auth login --with-token, gh auth setup-git done (token never logged)"
    for line, cmd in (("Log in to GitHub: answer the gh prompts", ["gh", "auth", "login"]),
                      ("Setting up git credentials via gh (so a private --repo clones)", ["gh", "auth", "setup-git"])):
        c.emit(">>> " + line)
        c.do(c.as_user(cmd), tty=True, cwd=c.home, timeout=None, what=" ".join(cmd[:2]))
    return "claude, gh auth login, gh auth setup-git done (output not logged)"


def step_setup(c):
    a, fh, u = c.args, c.fh, c.as_user
    repo = c.home + "/demo-repo"
    c.do(u(["git", "clone", "--", a.repo, fh]), cwd=c.home, timeout=600, what="git clone")
    if a.ref:
        c.do(u(["git", "-C", fh, "fetch", "origin", "--", a.ref]), cwd=c.home, timeout=600, what="git fetch ref")
        c.do(u(["git", "-C", fh, "checkout", "--detach", "FETCH_HEAD"]), cwd=c.home, what="git checkout ref")
    c.do(u(["mkdir", "-p", c.home + "/.local/bin"]), cwd=c.home, what="mkdir bin")
    c.do(u(["ln", "-sf", fh + "/bin/factory", c.home + "/.local/bin/factory"]), cwd=c.home, what="link factory")
    c.do(u(["factory", "--help"]), cwd=c.home, what="factory --help")
    _, out = c.do(u(["factory", "init"]), cwd=c.home, what="factory init")
    if not c.plan and "initialised" not in out:
        raise StepFail("`factory init` did not print an `initialised` line")
    _, out = c.do(u(["ls", fh + "/run/settings"]), cwd=c.home, what="ls run/settings")
    if not c.plan and ".json" not in out:
        raise StepFail("run/settings has no role .json files")
    c.do(u(["git", "init", "-q", "-b", "main", repo]), cwd=c.home, what="git init demo repo")
    c.do(u(["git", "-C", repo, "-c", "user.name=freshbox", "-c", "user.email=freshbox@localhost", "commit", "-q",
            "--allow-empty", "-m", "init"]), cwd=c.home, what="git commit demo repo")
    c.do(u(["factory", "project", "add", "demo", "--name", "Demo", "--repo", repo]), cwd=c.home, what="factory project add")
    return "cloned %s%s; project 'demo' = a fresh git repo at ~/demo-repo" % (a.repo, " @ " + a.ref if a.ref else "")


def step_herdr_server(c):
    c.background(c.as_user(["herdr", "server"]), "herdr-server")
    for _ in range(1 if c.plan else 20):
        rc, _ = c.do(c.as_user(["herdr", "workspace", "list"]), cwd=c.home, check=False, what="herdr workspace list")
        if rc == 0:
            return "herdr server up, `herdr workspace list` exit 0"
        c.host.sleep(1)
    raise StepFail("`herdr workspace list` never exited 0 within 20 s")


def agents_live(out):
    live = set()
    for ln in out.splitlines()[1:]:
        f = ln.split()
        if len(f) >= 3 and f[2] == "live":
            live.add(f[0])
    return live


# gate() reasons in bin/factory that mean "the host is busy"; any other QUEUED reason (unreachable server, /tmp, usage caps) is a real failure
HOST_BUSY = ("claude processes on host", "load1")


def gate_reason(text):
    """The first host-busy `QUEUED <agent>: <why>` line `factory up` / `factory agents` printed, or None."""
    for ln in text.splitlines():
        ln = ln.strip()
        if ln.upper().startswith("QUEUED") and any(h in ln for h in HOST_BUSY):
            return ln
    return None


def busy_hint(cap):
    return ("re-run when the box is quieter, or pass --max-total-claude N with N above the current host claude count "
            "(it was %s when factory up ran)" % cap)


def step_up(c):
    u, a = c.as_user, c.args
    # the cap must reflect the claude processes running NOW: the live factory on this box keeps starting more
    cap = a.max_total_claude if a.max_total_claude is not None else c.host.count_claude() + CLAUDE_HEADROOM
    c.do(u(["factory", "config", "max_total_claude", str(cap)]), cwd=c.home, what="factory config")
    c.emit("    max_total_claude=%s%s" % (cap, "" if a.max_total_claude is not None else " (host claude count now + %d)" % CLAUDE_HEADROOM))
    _, up_out = c.do(u(["factory", "up"]), cwd=c.home, timeout=300, what="factory up")
    reason = gate_reason(up_out)
    polls = 1 if c.plan else 18
    for i in range(polls * 2):  # a queued start gets up to ~3 min in all
        if i >= polls and not reason:
            break
        _, out = c.do(u(["factory", "agents"]), cwd=c.home, what="factory agents")
        if c.plan or {"boss", "director"} <= agents_live(out):
            return ("boss and director live" if not c.plan else "plan") + "; max_total_claude=%s" % cap
        reason = reason or gate_reason(out)
        c.host.sleep(5)
    if reason:
        raise Inconclusive("INCONCLUSIVE (host busy): boss/director not live, gate said: %s. %s" % (reason, busy_hint(cap)))
    raise StepFail("boss/director not live after ~90 s (see the factory agents tail above); max_total_claude was %s. "
                   "If the box is busy, %s" % (cap, busy_hint(cap)))


def step_serve(c):
    a = c.args
    c.background(c.as_user(["factory", "serve", "--bind", "127.0.0.1", "--port", str(a.port), "--ttl", "30"]), "serve")
    if c.plan:
        c.note("GET http://127.0.0.1:%d/ and /goals must be HTTP 200 (urllib)" % a.port)
        return "plan"
    bad = {}
    for path in ("/", "/goals"):
        st = 0
        for _ in range(20):
            st = c.host.http_status("http://127.0.0.1:%d%s" % (a.port, path))
            if st == 200:
                break
            c.host.sleep(1)
        if st != 200:
            bad[path] = st
    if bad:
        raise StepFail("web UI not HTTP 200: %s" % bad)
    return "/ and /goals answered 200"


STEPS = [("preflight", step_preflight), ("create-user", step_user), ("install-herdr", step_herdr),
         ("install-claude", step_claude), ("prerequisites", step_prereqs), ("logins", step_logins),
         ("follow-setup", step_setup), ("herdr-server", step_herdr_server), ("factory-up", step_up),
         ("web-ui", step_serve)]


# ───────────────────────────── cleanup ─────────────────────────────

def cleanup(c):
    """Always runs. Removes only what this run created: the test user (and its home) and its own temp dir."""
    c.emit("--- cleanup")
    if c.plan:
        c.note("stop serve, herdr server (and with it the agents) for %s; kill only processes owned by %s" % (c.user, c.user))
    for p in c.procs:
        try:
            p.terminate()
        except OSError:
            pass
    steps = []
    if c.created or c.plan:
        # the test user's gh login is an OAuth grant of the owner's account: log it out here, so nothing has to be done at github.com afterwards
        steps = [("gh auth logout (test user)", c.as_user(["gh", "auth", "logout", "-h", "github.com"]), None),
                 ("herdr server stop", c.as_user(["herdr", "server", "stop"]), None),
                 ("terminate processes of the user", ["pkill", "-TERM", "-u", c.user], 1),  # rc 1: no process matched
                 ("terminate user session", ["loginctl", "terminate-user", c.user], 1),  # rc 1: user not logged in
                 ("kill leftovers of the user", ["pkill", "-KILL", "-u", c.user], 1)]
    for what, argv, idle_rc in steps:
        if what == "herdr server stop" and not c.plan and not c.procs:
            c.emit("    $ %s -> skipped (not started)" % what)
            continue
        try:
            c.do(argv, check=False, timeout=60, what=what, cwd="/", idle_rc=idle_rc)
        except Exception as e:  # cleanup must go on whatever one command does
            c.emit("    cleanup step %r raised %s: %s" % (what, type(e).__name__, e))
    if c.created or c.plan:
        for _ in range(1 if c.plan else 5):  # killed processes may not be reaped yet
            if not c.plan:
                c.host.sleep(1)
            try:
                c.do(["userdel", "-r", c.user], check=False, timeout=60, what="delete user and home", cwd="/")
            except Exception as e:  # keep going to the verification
                c.emit("    userdel raised %s: %s" % (type(e).__name__, e))
            if c.plan or c.host.run(["id", c.user])[0] != 0:
                break
        if c.plan:
            c.note("verify: `id %s` must fail" % c.user)
            c.note("remove the tool's own temp dir (<tmpdir>) only")
        else:
            rc, _ = c.host.run(["id", c.user])
            c.step_line("PASS" if rc != 0 else "FAIL", "cleanup", "verify-user-gone",
                        "id %s %s" % (c.user, "fails: user is gone" if rc != 0 else "still succeeds: REMOVE IT BY HAND (userdel -r %s)" % c.user))
            if rc == 0:
                c.failed.append("cleanup")
    if c.tmp and not c.plan:
        try:
            c.host.rmtree(c.tmp)
        except OSError as e:
            c.emit("    could not remove temp dir %s: %s" % (c.tmp, e))


# ───────────────────────────── main ─────────────────────────────

def parse(argv, host, env):
    ap = argparse.ArgumentParser(prog="freshbox_test.py", description="One-command fresh-install test (needs root, a TTY and a throwaway user name).")
    ap.add_argument("--repo", default=DEFAULT_REPO, help="git URL to clone (default %(default)s)")
    ap.add_argument("--ref", help="branch, tag, sha or refs/pull/N/head (default: remote default branch)")
    ap.add_argument("--user", default="fbtest", help="throwaway user to create and delete (default %(default)s)")
    ap.add_argument("--port", type=int, default=8799, help="web UI port (default %(default)s)")
    ap.add_argument("--out", help="results file (default ~invoker/factory-freshbox-<UTC ts>.log)")
    ap.add_argument("--herdr-bin", help="copy this herdr binary instead of downloading the pinned release")
    ap.add_argument("--max-total-claude", type=int, help="factory gate cap (default: claude processes running when `factory up` starts + %d)" % CLAUDE_HEADROOM)
    ap.add_argument("--gh-token-file", metavar="PATH", help="file with a short-lived fine-grained GitHub token for the test user (`-` reads stdin, which must still be your terminal: paste the token, then Enter; input is hidden); "
                    "fed to `gh auth login --with-token` on stdin, so no interactive gh login (default: interactive)")
    ap.add_argument("--dry-run", action="store_true", help="print the plan and the exact argv; no root, no TTY, nothing run")
    a = ap.parse_args(argv)
    if not USER_RE.match(a.user) or a.user == "root":
        ap.error("--user must be a plain lowercase account name and not root")
    for name in ("repo", "ref"):
        v = getattr(a, name)
        if v is not None and (v.startswith("-") or "\n" in v):
            ap.error("--%s must not start with '-'" % name)
    if not 1 <= a.port <= 65535:
        ap.error("--port out of range")
    return a


def results_path(a, host, env, now):
    if a.out:
        return a.out
    who = env.get("SUDO_USER") or pwd.getpwuid(os.getuid()).pw_name
    return os.path.join(host.pw_dir(who), "factory-freshbox-%s.log" % now.strftime("%Y%m%dT%H%M%SZ"))


def run_steps(c, start=1):
    for i, (name, fn) in enumerate(STEPS, 1):
        if i < start:
            continue
        c.n_steps = i
        if c.plan:
            c.emit("[PLAN] %d %s" % (i, name))
        try:
            detail = fn(c)
        except StepFail as e:
            c.step_line("FAIL", i, name, str(e))
            c.failed.append(name)
            return
        except Inconclusive as e:
            c.step_line("INCONCLUSIVE", i, name, str(e))
            c.inconclusive.append(name)
            return
        if not c.plan:
            c.step_line("PASS", i, name, detail)


def main(argv=None, host=None, env=None, now=None, out=None):
    host = host or Host()
    env = os.environ if env is None else env
    now = now or dt.datetime.now(dt.timezone.utc)
    a = parse(argv, host, env)
    c = Ctx(host, a, env, a.dry_run, out)
    if a.dry_run:
        c.emit("DRY RUN: nothing is created, downloaded or run. Plan for user %s, port %d:" % (a.user, a.port))
        c.note("results file: %s (mode 600, chowned to $SUDO_USER)" % results_path(a, host, env, now))
        run_steps(c)
        cleanup(c)
        c.emit("RESULT PLAN steps=%d failed=none" % len(STEPS))
        return 0
    try:
        step_preflight(c)
    except Refused as e:
        print("freshbox_test: refused: %s" % e, file=sys.stderr)
        return 2
    invoker = env["SUDO_USER"]
    path = results_path(a, host, env, now)
    if under(os.path.realpath(path), os.path.realpath(c.home)):
        print("freshbox_test: refused: results file would be inside the test home", file=sys.stderr)
        return 2
    if a.gh_token_file:
        try:
            tok = host.read_token(a.gh_token_file).strip()
        except OSError as e:
            print("freshbox_test: refused: cannot read --gh-token-file %s: %s" % (a.gh_token_file, e), file=sys.stderr)
            return 2
        if not tok or any(ch.isspace() for ch in tok):
            print("freshbox_test: refused: --gh-token-file must hold exactly one token (no spaces or newlines inside)", file=sys.stderr)
            return 2
        if len(tok) < MIN_TOKEN_LEN:
            print("freshbox_test: refused: the token is shorter than %d characters, so it is not a GitHub token; "
                  "paste the whole token (redacting a short one would mangle the log)" % MIN_TOKEN_LEN, file=sys.stderr)
            return 2
        c.gh_token = tok
    try:
        uid, gid = host.ids(invoker)
        c.results = host.open_results(path, uid, gid)
    except OSError as e:
        print("freshbox_test: refused: cannot create results file %s: %s" % (path, e), file=sys.stderr)
        return 2
    c.results_path = path
    old = {s: signal.getsignal(s) for s in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP)}

    def on_signal(signum, frame):
        raise Interrupted(signal.Signals(signum).name)
    for s in old:
        signal.signal(s, on_signal)
    rc = 1
    try:
        c.n_steps = 1
        c.emit("freshbox_test: user=%s repo=%s ref=%s port=%d results=%s" % (a.user, a.repo, a.ref or "(default)", a.port, path))
        c.step_line("PASS", 1, "preflight", "root, TTY, user %s free, port %d free" % (a.user, a.port))
        run_steps(c, 2)
        rc = 1 if c.failed else 3 if c.inconclusive else 0
    except Interrupted as e:
        c.emit("[FAIL] interrupted by %s" % e)
        c.failed.append("interrupted")
        rc = 130
    except Exception as e:  # any bug must still reach cleanup and the RESULT line
        c.emit("[FAIL] unexpected %s: %s" % (type(e).__name__, e))
        c.failed.append("exception")
        rc = 1
    finally:
        for s in old:
            signal.signal(s, signal.SIG_IGN)
        try:
            cleanup(c)
        finally:
            verdict = "FAIL" if c.failed else "INCONCLUSIVE" if c.inconclusive else "PASS"
            c.emit("RESULT %s steps=%d failed=%s%s" % (verdict, c.n_steps, ",".join(c.failed) or "none",
                                                        " inconclusive=" + ",".join(c.inconclusive) if c.inconclusive else ""))
            if c.results:
                c.results.close()
            for s, h in old.items():
                signal.signal(s, h)
    return 1 if c.failed and rc in (0, 3) else rc  # a cleanup failure after a clean or inconclusive run still fails


if __name__ == "__main__":
    sys.exit(main())
