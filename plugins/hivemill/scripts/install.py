#!/usr/bin/env python3
"""Non-interactive installer for the hivemill factory. Standard library only; self-contained.

  install.py --check | --dry-run | --yes  [--ref REF|latest] [--repo-url URL] [--project-id ID --project-repo PATH]
             [--project-name NAME] [--project-gh OWNER/NAME] [--max-agents N] [--port N] [--no-systemd]
             [--linger] [--no-scout] [--resume]
  install.py --herdr-app --dry-run | --yes  [--factory-url URL] [--factory-machine NAME] [--factory-web-url URL]
             [--orgs auto|claude|cursor|claude,cursor] [--no-switch] [--no-sidebar]
             (the dashboard for the Herdr app, on the machine you work from: docs/herdr-app-dashboard.md)

It never prompts, never reads stdin and never reads, prints or stores a token. It installs into ~/factory and
~/.local/bin of the invoking user and refuses to run as root.
Exit codes: 0 done, 1 a step failed or a refusal, 2 usage, 3 you must do something (the command is printed),
4 already installed. The last line of a run is `RESULT PASS url=... version=...` or `RESULT FAIL step=...`.
The syntax stays valid on Python 3.8 so an old Python gets a clear message; the factory itself needs 3.11+.
"""
import argparse
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.error
import urllib.request

DEFAULT_REPO_URL = "https://github.com/volekerb/hivemill.git"
USER_AGENT = "hivemill-install/1.0"
CLAUDE_INSTALLER_URL = "https://claude.ai/install.sh"
# herdr: pinned official release (https://github.com/herdrdev/herdr). Kept equal to HERDR_ASSETS of the fresh-box
# test and to docs/SETUP.md by tests/test_hivemill_install.py. machine -> (url, size_bytes, sha256).
HERDR_VERSION = "v0.9.5"
HERDR_RELEASE = "https://github.com/herdrdev/herdr/releases/download/" + HERDR_VERSION
HERDR_ASSETS = {
    "x86_64": (HERDR_RELEASE + "/herdr-linux-x86_64", 30285672,
               "14d9e8bc8b4a9e0118d77c32266f76d461f09b40342eb23752960bd89f59929d"),
    "aarch64": (HERDR_RELEASE + "/herdr-linux-aarch64", 27900752,
                "17d7536f2da50859523680ef279dffcac8e4a235f4e46fe8da6c2ec44141897c"),
}
# gh: pinned official release, sha256 from the release's gh_2.103.3_checksums.txt (https://github.com/cli/cli).
GH_VERSION = "2.103.3"
GH_RELEASE = "https://github.com/cli/cli/releases/download/v%s" % GH_VERSION
GH_ASSETS = {
    "x86_64": (GH_RELEASE + "/gh_%s_linux_amd64.tar.gz" % GH_VERSION, 15031972,
               "4c806978ffce1cec70853302a56d70ac6da4f19bae7adca4e1a8ab00633dd91d"),
    "aarch64": (GH_RELEASE + "/gh_%s_linux_arm64.tar.gz" % GH_VERSION, 13661485,
                "ee32df2323c8847f5fac6cb6b4b69ca75828e9329a8f22f677021f7b3306ce63"),
}
ARCH_ALIAS = {"x86_64": "x86_64", "amd64": "x86_64", "aarch64": "aarch64", "arm64": "aarch64"}
LOOPBACK = "127.0.0.1"
DEFAULT_PORT = 8765
TAIL_LINES = 15
TAG_RE = re.compile(r"^v(\d+)\.(\d+)\.(\d+)$")
VERSION_RE = re.compile(r"hivemill (\d+\.\d+\.\d+)")
APT_HINT = "sudo apt-get install -y git python3"
REDACT = [re.compile(p) for p in (
    r"gh[opsur]_[A-Za-z0-9]{20,}", r"gho_[A-Za-z0-9_]{10,}", r"github_pat_[A-Za-z0-9_]{10,}", r"sk-ant-[A-Za-z0-9_\-]{10,}",
    r"(?i)authorization:[^\r\n]*", r"(?<=://)[^/@\s]+@")]


def redact(text):
    for rx in REDACT:
        text = rx.sub("[REDACTED]", text)
    return text


class StepFail(Exception):
    """A step failed: exit 1, `RESULT FAIL step=<name>`."""


class Stop(Exception):
    """End the run with this exit code and message, no RESULT line (refusals and hand-offs to the user)."""

    def __init__(self, code, msg):
        Exception.__init__(self, msg)
        self.code, self.msg = code, msg


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


class HttpsOnlyRedirect(urllib.request.HTTPRedirectHandler):
    """Refuses any redirect that leaves https, so a pinned-or-not download cannot be downgraded to plain http."""

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        if not newurl.lower().startswith("https://"):
            raise urllib.error.URLError("refusing redirect away from https")
        return super().redirect_request(req, fp, code, msg, headers, newurl)


class Host:
    """Everything that touches the machine. Tests replace it; --check and --dry-run only use the read methods."""

    def __init__(self, home=None, environ=None):
        self.home = home or os.path.expanduser("~")
        self.environ = dict(os.environ if environ is None else environ)

    # -- reads
    def euid(self):
        return os.geteuid()

    def py_version(self):
        return sys.version_info[:3]

    def machine(self):
        return os.uname().machine

    def mem_gib(self):
        with open("/proc/meminfo") as f:
            for ln in f:
                if ln.startswith("MemTotal:"):
                    return int(ln.split()[1]) / (1024.0 * 1024.0)
        raise OSError("no MemTotal in /proc/meminfo")

    def cpu_count(self):
        return os.cpu_count() or 1

    def exists(self, path):
        return os.path.lexists(path)

    def isdir(self, path):
        return os.path.isdir(path)

    def child_env(self):
        env = dict(self.environ)
        env["PATH"] = os.path.join(self.home, ".local", "bin") + os.pathsep + env.get("PATH", os.defpath)
        return env

    def run(self, argv, cwd=None, timeout=None, mutate=False):
        """-> (returncode, merged output). Never reads stdin. `mutate` only labels the call for the tests."""
        try:
            p = subprocess.run(argv, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, stdin=subprocess.DEVNULL,
                               cwd=cwd, timeout=timeout, env=self.child_env(), universal_newlines=True, errors="replace")
            return p.returncode, p.stdout
        except subprocess.TimeoutExpired:
            return 124, "timed out after %ss" % timeout
        except OSError as e:
            return 127, str(e)

    def http_status(self, url):
        try:
            req = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
            with urllib.request.urlopen(req, timeout=10) as r:
                return r.status
        except urllib.error.HTTPError as e:
            return e.code
        except (OSError, ValueError):
            return 0

    def sleep(self, s):
        time.sleep(s)

    # -- writes
    def mkdirs(self, path):
        os.makedirs(path, exist_ok=True)

    def rename(self, src, dst):
        os.rename(src, dst)

    def mkdtemp(self):
        return tempfile.mkdtemp(prefix="hivemill-install-")

    def rmtree(self, path):
        shutil.rmtree(path, ignore_errors=True)

    def download(self, url, dest):
        if not url.startswith("https://"):
            raise StepFail("refusing non-https download %s" % url)
        req = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
        try:
            opener = urllib.request.build_opener(HttpsOnlyRedirect)
            with opener.open(req, timeout=60) as r, open(dest, "wb") as f:
                shutil.copyfileobj(r, f)
        except urllib.error.HTTPError as e:
            raise StepFail("HTTP %s for %s" % (e.code, url))
        except (urllib.error.URLError, OSError) as e:
            raise StepFail("cannot fetch %s: %s" % (url, e))

    def install_file(self, src, dest, mode=0o755):
        tmp = dest + ".part"
        shutil.copyfile(src, tmp)
        os.chmod(tmp, mode)
        os.replace(tmp, dest)

    def extract_member(self, tarball, member, dest):
        with tarfile.open(tarball, "r:gz") as t:
            m = t.getmember(member)
            if not m.isfile():
                raise StepFail("%s in %s is not a regular file" % (member, os.path.basename(tarball)))
            with t.extractfile(m) as src, open(dest, "wb") as out:
                shutil.copyfileobj(src, out)

    def symlink(self, target, link):
        if os.path.islink(link):
            if os.readlink(link) == target:
                return
            raise StepFail("%s already points elsewhere; remove it and re-run with --resume" % link)
        if os.path.lexists(link):
            raise StepFail("%s exists and is not a symlink; move it away and re-run with --resume" % link)
        os.symlink(target, link)

    def popen(self, argv, logpath):
        fd = os.open(logpath, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
        try:
            subprocess.Popen(argv, stdout=fd, stderr=subprocess.STDOUT, stdin=subprocess.DEVNULL,
                             env=self.child_env(), start_new_session=True)
        finally:
            os.close(fd)


class Ctx:
    """State of one run. In plan mode `do` records and prints the argv and runs nothing."""

    def __init__(self, host, args, out, plan):
        self.host, self.args, self.out, self.plan = host, args, out, plan
        self.home = host.home
        self.fh = os.path.join(self.home, "factory")
        self.bindir = os.path.join(self.home, ".local", "bin")
        self.factory = os.path.join(self.bindir, "factory")
        self.n = 0
        self.tmp = None
        self.ref = None
        self.port = args.port
        self.version = None
        self.tail = ""

    def emit(self, text):
        text = redact(text)
        for key in ("GH_TOKEN", "GITHUB_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"):
            val = self.host.environ.get(key, "")
            if len(val) >= 20:
                text = text.replace(val, "[REDACTED]")
        print(text, file=self.out)
        self.out.flush()

    def note(self, text):
        self.emit("    %s" % text)

    def do(self, argv, what, cwd=None, timeout=600, check=True):
        """Run one mutating command. -> (rc, output). A failure with check=True raises StepFail with the redacted tail."""
        if self.plan:
            self.emit("    $ %s" % " ".join(argv))
            return 0, ""
        rc, out = self.host.run(argv, cwd=cwd, timeout=timeout, mutate=True)
        self.tail = out
        if check and rc != 0:
            raise StepFail("%s failed (rc=%s):\n%s" % (what, rc, tail_of(out)))
        return rc, out

    def read(self, argv, cwd=None, timeout=60):
        """Run one read-only command (also in plan mode)."""
        rc, out = self.host.run(argv, cwd=cwd, timeout=timeout, mutate=False)
        return rc, out


def tail_of(text):
    lines = [ln for ln in text.splitlines() if ln.strip()][-TAIL_LINES:]
    return redact("\n".join("    " + ln for ln in lines))


# --------------------------------------------------------------------------------------------- latest tag
def resolve_latest(ls_remote_output):
    """The highest ^vX.Y.Z$ tag among `git ls-remote --tags` lines, or None. Pre-release tags never match."""
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


# --------------------------------------------------------------------------------------------- --check
def check_items(host, no_systemd=False):
    """-> list of (status, name, detail)."""
    items = []

    def add(status, name, detail):
        items.append((status, name, detail))

    v = host.py_version()
    rc, out = host.run(["python3", "--version"])
    m = re.search(r"Python (\d+)\.(\d+)", out) if rc == 0 else None
    if not m:
        add("missing", "python3>=3.11", "python3 not found")
    elif (int(m.group(1)), int(m.group(2))) >= (3, 11):
        add("ok", "python3>=3.11", out.strip())
    else:
        add("missing", "python3>=3.11", "%s is too old; 3.11 or newer needed (this script runs on %d.%d)" % (out.strip(), v[0], v[1]))
    rc, out = host.run(["git", "--version"])
    add("ok" if rc == 0 else "missing", "git", out.strip() if rc == 0 else "not found")
    rc, out = host.run(["gh", "--version"])
    have_gh = rc == 0
    add("ok" if have_gh else "missing", "gh", out.splitlines()[0] if have_gh and out.strip() else "not found")
    if have_gh:
        rc, _ = host.run(["gh", "auth", "status"])
        add("ok" if rc == 0 else "missing", "gh-auth", "logged in" if rc == 0 else "not logged in: run `gh auth login` in your own terminal")
    else:
        add("missing", "gh-auth", "gh is not installed")
    rc, out = host.run(["herdr", "--version"])
    add("ok" if rc == 0 else "missing", "herdr", out.strip() if rc == 0 else "not found")
    rc, out = host.run(["claude", "--version"])
    have_claude = rc == 0
    add("ok" if have_claude else "missing", "claude", out.strip() if have_claude else "not found")
    cred = os.path.join(host.home, ".claude", ".credentials.json")
    if host.exists(cred):
        add("ok", "claude-auth", "credentials file present")
    else:
        add("missing", "claude-auth", "not logged in: run `claude` in your own terminal and complete /login")
    rc, _ = host.run(["systemctl", "--user", "show-environment"])
    if rc == 0:
        add("ok", "systemd-user", "user session reachable")
    else:
        add("warn" if no_systemd else "missing", "systemd-user",
            "no user systemd session; pass --no-systemd to install without it" if not no_systemd else "no user systemd session (--no-systemd)")
    try:
        gib = host.mem_gib()
        add("warn" if gib < 4 else "ok", "ram", "%.1f GiB%s" % (gib, " (below 4 GB)" if gib < 4 else ""))
    except (OSError, ValueError) as e:
        gib = None
        add("warn", "ram", "unknown: %s" % e)
    cpus = host.cpu_count()
    add("warn" if cpus < 2 else "ok", "cpu", "%d CPU%s" % (cpus, " (below 2)" if cpus < 2 else ""))
    rc, out = host.run(["go", "version"])
    add("ok" if rc == 0 else "warn", "go", out.strip() if rc == 0 else "not found; optional, guard_impl defaults to python")
    return items, gib


def suggested_max_agents(gib):
    return min(6, max(2, int(gib))) if gib is not None else 2


def run_check(host, args, out):
    items, gib = check_items(host, args.no_systemd)
    for status, name, detail in items:
        print("%s %s %s" % (status, name, redact(detail)), file=out)
    print("suggested-max-agents %d" % suggested_max_agents(gib), file=out)
    return 3 if any(s == "missing" for s, _, _ in items) else 0


# --------------------------------------------------------------------------------------------- preflight
def preflight(c):
    h, a = c.host, c.args
    if h.euid() == 0:
        raise Stop(1, "refusing to run as root: run this as your normal user (it never uses elevated rights)")
    if h.environ.get("FACTORY_AGENT"):
        raise Stop(1, "FACTORY_AGENT is set: this is an agent pane. Run the installer in your own terminal")
    check_prereqs(c)  # missing git must exit 3 before the first git call below
    if h.exists(c.fh):
        rc, out = c.read(["git", "-C", c.fh, "remote", "get-url", "origin"])
        if rc != 0 or norm_url(out.strip()) != norm_url(a.repo_url):
            raise Stop(1, "%s exists and is not a clone of %s; nothing was touched" % (c.fh, a.repo_url))
        if h.exists(os.path.join(c.fh, "db", "factory.db")):
            if not a.resume:
                raise Stop(4, "already installed (%s/db/factory.db exists); nothing was touched. To finish a partial install pass --resume" % c.fh)
        elif not a.resume:
            raise Stop(4, "%s is already a clone but has no database: a partial install. Re-run with --resume to finish it; nothing was touched" % c.fh)
    elif a.resume:
        c.note("--resume: no clone at %s yet, so this is a normal install" % c.fh)


def norm_url(u):
    u = u.strip().rstrip("/")
    if u.endswith(".git"):
        u = u[:-4]
    return u.lower()


def resolve_ref(c):
    a = c.args
    if a.ref != "latest":
        return a.ref
    rc, out = c.read(["git", "ls-remote", "--tags", a.repo_url], timeout=120)
    if rc != 0:
        raise StepFail("git ls-remote --tags failed (rc=%s):\n%s" % (rc, tail_of(out)))
    tag = resolve_latest(out)
    if not tag:
        raise StepFail("no release tag vX.Y.Z found in %s; pass --ref" % a.repo_url)
    return tag


# --------------------------------------------------------------------------------------------- steps
def check_prereqs(c):
    missing = []
    v = c.host.py_version()
    rc, out = c.read(["python3", "--version"])
    m = re.search(r"Python (\d+)\.(\d+)", out) if rc == 0 else None
    if not m or (int(m.group(1)), int(m.group(2))) < (3, 11):
        missing.append("python3 3.11 or newer")
    rc, _ = c.read(["git", "--version"])
    if rc != 0:
        missing.append("git")
    if missing:
        raise Stop(3, "missing: %s. Run this yourself, then re-run the installer:\n  %s" % (", ".join(missing), APT_HINT))
    return v


def step_prereqs(c):
    v = check_prereqs(c)
    return "python3>=3.11 and git present (installer runs on %d.%d)" % (v[0], v[1])


def fetch_verified(c, name, url, size, sha):
    path = os.path.join(c.tmp, name)
    if c.plan:
        c.note("download %s -> %s, then verify size %d and sha256 %s" % (url, path, size, sha))
        return path
    c.host.download(url, path)
    got = os.path.getsize(path)
    if got != size:
        raise StepFail("%s size mismatch: got %d, pinned %d; nothing installed" % (name, got, size))
    if sha256_file(path) != sha:
        raise StepFail("%s sha256 mismatch; nothing installed" % name)
    return path


def asset_for(table, machine, what):
    asset = table.get(ARCH_ALIAS.get(machine, ""))
    if not asset:
        raise StepFail("no pinned %s release for %s; install it yourself into %s" % (what, machine, "~/.local/bin"))
    return asset


def have(c, argv):
    rc, _ = c.read(argv)
    return rc == 0


def step_tools(c):
    done = []
    machine = c.host.machine()
    if not c.plan:
        c.host.mkdirs(c.bindir)
    if not have(c, ["herdr", "--version"]):
        url, size, sha = asset_for(HERDR_ASSETS, machine, "herdr")
        src = fetch_verified(c, "herdr", url, size, sha)
        if c.plan:
            c.note("install it as %s/herdr" % c.bindir)
        else:
            c.host.install_file(src, os.path.join(c.bindir, "herdr"))
        done.append("herdr %s (size+sha256 verified)" % HERDR_VERSION)
    if not have(c, ["gh", "--version"]):
        url, size, sha = asset_for(GH_ASSETS, machine, "gh")
        src = fetch_verified(c, "gh.tar.gz", url, size, sha)
        member = "gh_%s_linux_%s/bin/gh" % (GH_VERSION, "amd64" if ARCH_ALIAS[machine] == "x86_64" else "arm64")
        if c.plan:
            c.note("extract %s from the tarball and install it as %s/gh" % (member, c.bindir))
        else:
            ex = os.path.join(c.tmp, "gh")
            c.host.extract_member(src, member, ex)
            c.host.install_file(ex, os.path.join(c.bindir, "gh"))
        done.append("gh %s (size+sha256 verified)" % GH_VERSION)
    if not have(c, ["claude", "--version"]):
        path = os.path.join(c.tmp, "claude-install.sh")
        if c.plan:
            c.note("download %s -> %s (to a file, sha256 logged, never piped into a shell)" % (CLAUDE_INSTALLER_URL, path))
            digest = "<sha256>"
        else:
            c.host.download(CLAUDE_INSTALLER_URL, path)
            digest = sha256_file(path)
        c.do(["bash", path], "claude installer")
        done.append("claude installer sha256 %s" % digest)
    return "; ".join(done) if done else "herdr, gh and claude already present"


def step_auth(c):
    todo = []
    if not have(c, ["gh", "auth", "status"]):
        todo.append("Run this in your own terminal: gh auth login")
    if not c.host.exists(os.path.join(c.home, ".claude", ".credentials.json")):
        todo.append("Run this in your own terminal: claude   (then complete /login and /exit)")
    if todo and not c.plan:
        raise Stop(3, "not logged in. Do this, then re-run the installer with --resume:\n  " + "\n  ".join(todo))
    return "gh and Claude are logged in (the installer never reads a token)"


def step_clone(c):
    if c.host.exists(c.fh):
        return "kept the existing clone at %s (--resume: no checkout, no ref change)" % c.fh
    c.ref = c.ref or resolve_ref(c)
    # clone beside ~/factory and rename after the checkout: a failed run never leaves a clone at ~/factory that
    # --resume would take for a finished one (it would keep the default branch instead of the requested ref)
    part = c.fh + ".installing"
    if not c.plan:
        c.host.rmtree(part)
    c.do(["git", "clone", "--", c.args.repo_url, part], "git clone")
    try:
        if c.args.ref == "latest":
            c.do(["git", "-C", part, "checkout", "--detach", "refs/tags/" + c.ref], "git checkout")
        else:
            c.do(["git", "-C", part, "fetch", "origin", "--", c.ref], "git fetch ref")
            c.do(["git", "-C", part, "checkout", "--detach", "FETCH_HEAD"], "git checkout")
    except StepFail:
        c.host.rmtree(part)
        raise
    if c.plan:
        c.emit("    $ mv %s %s" % (part, c.fh))
    else:
        c.host.rename(part, c.fh)
    return "cloned %s at %s" % (c.args.repo_url, c.ref)


def step_link(c):
    if c.plan:
        c.emit("    $ ln -s %s %s" % (os.path.join(c.fh, "bin", "factory"), c.factory))
    else:
        c.host.mkdirs(c.bindir)
        c.host.symlink(os.path.join(c.fh, "bin", "factory"), c.factory)
    on_path = c.bindir in [p.rstrip("/") for p in c.host.environ.get("PATH", "").split(os.pathsep)]
    if not on_path:
        c.emit("    note: %s is not on your PATH. Add this line to your shell profile yourself: export PATH=\"$HOME/.local/bin:$PATH\"" % c.bindir)
        return "linked %s; not on PATH, calling it by absolute path" % c.factory
    return "linked %s" % c.factory


def step_init(c):
    if c.host.exists(os.path.join(c.fh, "db", "factory.db")):
        return "database exists: init skipped"
    _, out = c.do([c.factory, "init"], "factory init")
    if not c.plan and "initialised" not in out:
        raise StepFail("`factory init` did not print an `initialised` line:\n" + tail_of(out))
    return "factory init done"


def step_config(c):
    a = c.args
    db_existed = c.host.exists(os.path.join(c.fh, "db", "factory.db")) and not c.plan
    sets = []
    # the default bind is the default-route address, which would put the UI on the LAN
    if a.resume and db_existed:
        _, cur = c.read([c.factory, "config", "serve_bind"])
        if cur.strip():
            c.note("--resume: serve_bind is already %r, kept" % cur.strip())
        else:
            sets.append(("serve_bind", LOOPBACK))
    else:
        sets.append(("serve_bind", LOOPBACK))
    if a.port is not None:
        sets.append(("serve_port", str(a.port)))
    if a.max_agents is not None:
        sets.append(("max_live_agents", str(a.max_agents)))
    if a.no_scout:
        sets.append(("scout_enabled", "0"))
    for k, v in sets:
        c.do([c.factory, "config", k, v], "factory config " + k)
    if a.port is None and a.resume and db_existed:
        rc, out = c.read([c.factory, "config", "serve_port"])
        if rc == 0 and out.strip().isdigit():
            c.port = int(out.strip())
    return "set " + ", ".join("%s=%s" % kv for kv in sets) if sets else "no config change"


def step_project(c):
    a = c.args
    if not (a.project_id and a.project_repo):
        return "no --project-id/--project-repo: skipped"
    if not c.plan:
        rc, out = c.read([c.factory, "project", "list"])
        if rc == 0 and a.project_id in out.split():
            return "project %s already listed: skipped" % a.project_id
    argv = [c.factory, "project", "add", a.project_id, "--name", a.project_name or a.project_id, "--repo", a.project_repo]
    if a.project_gh:
        argv += ["--gh", a.project_gh]
    c.do(argv, "factory project add")
    return "project %s added" % a.project_id


def step_services(c):
    a = c.args
    if a.no_systemd:
        return services_background(c)
    c.do(["bash", os.path.join(c.fh, "systemd", "install.sh"), "--with-serve"], "systemd install", cwd=c.fh)
    if a.linger:
        rc, out = c.do(["loginctl", "enable-linger"], "loginctl enable-linger", check=False)
        if rc != 0:
            c.emit("    warning: loginctl enable-linger was refused (rc=%s); the factory will not start at boot until you run it with the right to do so" % rc)
    c.do(["systemctl", "--user", "start", "herdr-server.service", "factory-tick.timer", "factory-serve.service",
          "factory-up.service"], "systemctl start")
    return "systemd user units installed and started (the tick timer keeps the factory running)"


def services_background(c):
    logdir = os.path.join(c.home, ".cache", "hivemill-install")
    if c.plan:
        c.emit("    $ mkdir -p %s" % logdir)
        c.emit("    $ (background) herdr server")
    else:
        c.host.mkdirs(logdir)
        c.host.popen(["herdr", "server"], os.path.join(logdir, "herdr-server.log"))
    for _ in range(1 if c.plan else 20):
        if c.plan:
            c.emit("    $ herdr workspace list   (until exit 0, 20 s)")
            break
        rc, _ = c.read(["herdr", "workspace", "list"])
        if rc == 0:
            break
        c.host.sleep(1)
    else:
        raise StepFail("`herdr workspace list` never exited 0 within 20 s (see %s/herdr-server.log)" % logdir)
    if c.plan:
        c.emit("    $ (background) %s serve --bind %s" % (c.factory, LOOPBACK))
    else:
        c.host.popen([c.factory, "serve", "--bind", LOOPBACK], os.path.join(logdir, "factory-serve.log"))
    _, out = c.do([c.factory, "up"], "factory up")
    c.emit("WARNING: no tick timer: agents will not be nudged and queued spawns will not start")
    return "herdr server and factory serve running in the background; factory up ran once"


def agent_status(out):
    rows = {}
    lines = out.splitlines()
    if not lines:
        return rows
    head = lines[0].split()
    if "NAME" not in head or "STATUS" not in head:
        return rows
    ni, si = head.index("NAME"), head.index("STATUS")
    for ln in lines[1:]:
        f = ln.split()
        if len(f) > max(ni, si):
            rows[f[ni]] = f[si]
    return rows


def step_smoke(c):
    if c.plan:
        c.emit("    $ %s --version   (expects 'hivemill X.Y.Z')" % c.factory)
        c.emit("    $ %s agents   (boss and director live, or queued)" % c.factory)
        c.emit("    $ GET http://%s:%d/ and /goals   (expect 200)" % (LOOPBACK, c.port))
        return "plan"
    rc, out = c.read([c.factory, "--version"])
    m = VERSION_RE.search(out)
    if rc != 0 or not m:
        raise StepFail("`factory --version` did not print 'hivemill X.Y.Z':\n" + tail_of(out))
    c.version = m.group(1)
    states = {}
    for _ in range(30):
        rc, out = c.read([c.factory, "agents"])
        states = agent_status(out) if rc == 0 else {}
        if all(states.get(n) in ("live", "queued") for n in ("boss", "director")):
            break
        c.host.sleep(2)
    else:
        raise StepFail("boss/director are not live or queued after 60 s:\n" + tail_of(out))
    detail = ", ".join("%s %s" % (n, states[n]) for n in ("boss", "director"))
    for path in ("/", "/goals"):
        url = "http://%s:%d%s" % (LOOPBACK, c.port, path)
        code = 0
        for _ in range(15):
            code = c.host.http_status(url)
            if code == 200:
                break
            c.host.sleep(2)
        if code != 200:
            raise StepFail("GET %s answered %s, expected 200" % (url, code or "nothing"))
    return "version %s; %s; UI answers 200" % (c.version, detail)


STEPS = [
    ("prereqs", step_prereqs), ("tools", step_tools), ("auth", step_auth), ("clone", step_clone),
    ("link", step_link), ("init", step_init), ("config", step_config), ("project", step_project),
    ("services", step_services), ("smoke", step_smoke),
]


def run_steps(c):
    name = "preflight"
    try:
        try:
            preflight(c)
            name = "prereqs"
            check_prereqs(c)  # git must be known to exist before the first git call (ls-remote)
            name = "resolve-ref"
            c.ref = None if c.host.exists(c.fh) else resolve_ref(c)
            if not c.plan and not c.args.no_systemd:
                rc, _ = c.read(["systemctl", "--user", "show-environment"])
                if rc != 0:
                    raise Stop(3, "no user systemd session here. Either log in to a session that has one, or re-run with "
                                  "--no-systemd (CI, containers: the factory then runs as plain background processes without a tick timer)")
            c.tmp = "<tmpdir>" if c.plan else c.host.mkdtemp()
            for name, fn in STEPS:
                c.n += 1
                c.emit("[%s] %d %s" % ("plan" if c.plan else "run", c.n, name))
                detail = fn(c)
                if not c.plan:
                    c.emit("[ok] %d %s: %s" % (c.n, name, detail))
        finally:
            if c.tmp and not c.plan:
                c.host.rmtree(c.tmp)
    except Stop as s:
        c.emit(s.msg)
        return s.code
    except (StepFail, OSError) as e:  # an OSError from a write (read-only dir, full disk) is a failed step too
        c.emit("[FAIL] %s: %s" % (name, e))
        c.emit("RESULT FAIL step=%s" % name)
        return 1
    if c.plan:
        c.emit("plan only: nothing was changed")
        return 0
    c.emit("RESULT PASS url=http://%s:%d/ version=%s" % (LOOPBACK, c.port, c.version))
    return 0


# --------------------------------------------------------------------------------------------- Herdr app dashboard
# --herdr-app installs the Hivemill dashboard into the Herdr app on the machine you work from (macOS or Linux; it
# may not be the factory's box). It touches only ~/.config/herdr-app/mods/hivemill, ~/.config/herdr-app/layouts/
# hivemill.json and ~/.config/hivemill/dashboard.json. See docs/herdr-app-dashboard.md.
HERDR_APP_MOD = "hivemill"
HERDR_APP_LAYOUT = "hivemill"
HERDR_APP_FILES = ("main.js", "data.js", "mod.json")
HERDR_APP_ORGS = ("claude", "cursor")
BAR = {"mod": HERDR_APP_MOD, "props": {"mode": "bar"}}


def herdr_app_home(host):
    return host.environ.get("HERDR_APP_HOME") or os.path.join(host.home, ".config", "herdr-app")


def herdr_app_source():
    return os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "herdr-app", "mods", HERDR_APP_MOD)


def bar_layout(main, hide_sidebar=False):
    """The app's main layout with its status bar block showing the Hivemill bar. -> (layout, replaced?).
    The block is the node whose content is the shipped `workspace-status` mod (id "status" in Herdr's main)."""
    lay = json.loads(json.dumps(main))
    found = []

    def walk(n):
        if not isinstance(n, dict):
            return
        content = n.get("content")
        if isinstance(content, dict) and (content.get("mod") == "workspace-status" or content.get("mod") == HERDR_APP_MOD):
            n["content"] = dict(BAR)
            found.append(n.get("id"))
        for ch in n.get("children") or []:
            walk(ch)

    walk(lay)
    for ch in lay.get("children") or []:
        if isinstance(ch, dict) and ch.get("id") == "sidebar" and hide_sidebar:
            ch["hidden"] = True  # a picture station: no space or agent names in shots
    if not found:
        work = next((ch for ch in lay.get("children") or [] if isinstance(ch, dict) and ch.get("id") == "work"), lay)
        work.setdefault("children", []).append({"id": "hivemill-bar", "type": "block", "size": {"px": 28}, "content": dict(BAR)})
    return lay, bool(found)


def dashboard_config(existing, args):
    """~/.config/hivemill/dashboard.json: the existing one with only the flags given changed; projects are kept."""
    cfg = dict(existing or {})
    factory = dict(cfg.get("factory") or {})
    factory.setdefault("url", "http://%s:%d" % (LOOPBACK, DEFAULT_PORT))
    if args.factory_url:
        factory["url"] = args.factory_url
    if args.factory_machine is not None:
        factory["machine"] = args.factory_machine or None
    if args.factory_web_url:
        factory["web_url"] = args.factory_web_url
    cfg["factory"] = factory
    if args.orgs:
        cfg["orgs"] = "auto" if args.orgs == "auto" else args.orgs.split(",")
    cfg.setdefault("orgs", "auto")
    cfg.setdefault("projects", [])
    return cfg


def run_herdr_app(host, args, out):
    c = Ctx(host, args, out, plan=args.dry_run)
    app = herdr_app_home(host)
    src = herdr_app_source()
    mod_dir = os.path.join(app, "mods", HERDR_APP_MOD)
    layout_path = os.path.join(app, "layouts", HERDR_APP_LAYOUT + ".json")
    main_path = os.path.join(app, "layouts", "main.json")
    cfg_path = os.path.join(host.home, ".config", "hivemill", "dashboard.json")
    name = "preflight"
    try:
        if host.euid() == 0:
            raise Stop(1, "refusing to run as root: run it as the user who uses the Herdr app")
        if not all(host.exists(os.path.join(src, f)) for f in HERDR_APP_FILES):
            raise Stop(1, "the dashboard files are missing next to install.py (%s); use a full hivemill checkout" % src)
        if not host.exists(main_path):
            raise Stop(3, "the Herdr app is not set up here (no %s). Install the Herdr app, open it once, then re-run "
                          "this command" % main_path)
        with open(main_path) as f:
            main = json.load(f)
        existing = None
        if host.exists(cfg_path):
            with open(cfg_path) as f:
                existing = json.load(f)
        cfg = dashboard_config(existing, args)
        lay, replaced = bar_layout(main, hide_sidebar=args.no_sidebar)

        name = "mod"
        c.emit("[%s] 1 mod: copy %s into %s" % ("plan" if c.plan else "run", ", ".join(HERDR_APP_FILES), mod_dir))
        if not c.plan:
            host.mkdirs(mod_dir)
            for f in HERDR_APP_FILES:
                host.install_file(os.path.join(src, f), os.path.join(mod_dir, f), mode=0o644)
        name = "config"
        c.emit("[%s] 2 config: %s %s (factory %s%s, orgs %s, %d project(s))" % (
            "plan" if c.plan else "run", "update" if existing is not None else "write", cfg_path, cfg["factory"]["url"],
            " via machine " + cfg["factory"]["machine"] if cfg["factory"].get("machine") else "",
            cfg["orgs"] if isinstance(cfg["orgs"], str) else ",".join(cfg["orgs"]), len(cfg["projects"])))
        if not c.plan:
            host.mkdirs(os.path.dirname(cfg_path))
            write_text(cfg_path, json.dumps(cfg, indent=2) + "\n")
        name = "layout"
        c.emit("[%s] 3 layout: write %s from main.json, %s" % (
            "plan" if c.plan else "run", layout_path,
            ("the status bar becomes the Hivemill bar" if replaced else "with the Hivemill bar added under the work area")
            + (", the sidebar hidden" if args.no_sidebar else "")))
        if not c.plan:
            write_text(layout_path, json.dumps(lay, indent=2) + "\n")
        name = "switch"
        if args.no_switch:
            c.emit("[%s] 4 switch: skipped (--no-switch); pick the Hivemill layout with ctrl-shift-l" % ("plan" if c.plan else "run"))
        else:
            c.emit("[%s] 4 switch: show the %s layout if the app is running" % ("plan" if c.plan else "run", HERDR_APP_LAYOUT))
            if not c.plan:
                rc, _ = host.run(["herdr-app", "do", "layout.switch", json.dumps({"name": HERDR_APP_LAYOUT})], timeout=20, mutate=True)
                if rc != 0:
                    c.note("the app is not running or herdr-app is not on PATH; pick the Hivemill layout with ctrl-shift-l")
    except Stop as s:
        c.emit(s.msg)
        return s.code
    except (StepFail, OSError, ValueError) as e:  # ValueError: a main.json or dashboard.json that is not JSON
        c.emit("[FAIL] %s: %s" % (name, e))
        c.emit("RESULT FAIL step=herdr-app-%s" % name)
        return 1
    if c.plan:
        c.emit("plan only: nothing was changed")
        return 0
    c.emit("RESULT PASS herdr-app layout=%s config=%s" % (HERDR_APP_LAYOUT, cfg_path))
    return 0


def write_text(path, text):
    tmp = path + ".part"
    with open(tmp, "w") as f:
        f.write(text)
    os.replace(tmp, path)


# --------------------------------------------------------------------------------------------- CLI
class Parser(argparse.ArgumentParser):
    def error(self, message):
        # argparse quotes rejected values in many messages (unrecognized, invalid, ignored explicit argument): it may
        # be a token, so only the option name survives, never the text after it
        m = re.match(r"argument ([\w/-]+):", message)
        message = "argument %s rejected (values are not echoed)" % m.group(1) if m else "unrecognized option (values are not echoed)"
        raise Stop(2, "usage error: %s\n%s" % (message, self.format_usage().strip()))


def positive(s):
    n = int(s)
    if n < 1:
        raise argparse.ArgumentTypeError("must be at least 1")
    return n


def port_arg(s):
    n = int(s)
    if not 1 <= n <= 65535:
        raise argparse.ArgumentTypeError("must be 1-65535")
    return n


def parse(argv):
    p = Parser(prog="install.py", description="Non-interactive hivemill installer", allow_abbrev=False)
    mode = p.add_argument_group("mode (exactly one)")
    mode.add_argument("--check", action="store_true", help="read-only report")
    mode.add_argument("--dry-run", action="store_true", help="print the ordered plan, change nothing")
    mode.add_argument("--yes", action="store_true", help="do the work")
    p.add_argument("--ref", default="latest")
    p.add_argument("--repo-url", default=DEFAULT_REPO_URL)
    p.add_argument("--project-id")
    p.add_argument("--project-name")
    p.add_argument("--project-repo")
    p.add_argument("--project-gh")
    p.add_argument("--max-agents", type=positive)
    p.add_argument("--port", type=port_arg, default=None)
    p.add_argument("--no-systemd", action="store_true")
    p.add_argument("--linger", action="store_true")
    p.add_argument("--no-scout", action="store_true")
    p.add_argument("--resume", action="store_true")
    dash = p.add_argument_group("Herdr app dashboard (--herdr-app with --dry-run or --yes)")
    dash.add_argument("--herdr-app", action="store_true", help="install the Hivemill dashboard into the Herdr app here")
    dash.add_argument("--factory-url", help="the factory web UI as seen from --factory-machine (default http://127.0.0.1:%d)" % DEFAULT_PORT)
    dash.add_argument("--factory-machine", help="an `hc machine ls` name to reach the factory from ('' clears it)")
    dash.add_argument("--factory-web-url", help="the factory web UI as your browser reaches it, for the Open links")
    dash.add_argument("--orgs", help="auto (default), claude, cursor, or claude,cursor")
    dash.add_argument("--no-switch", action="store_true", help="do not switch the app to the Hivemill layout")
    dash.add_argument("--no-sidebar", action="store_true", help="hide the sidebar in the Hivemill layout (a machine kept for screenshots)")
    a = p.parse_args(argv)
    if sum((a.check, a.dry_run, a.yes)) != 1:
        raise Stop(2, "usage error: pass exactly one of --check, --dry-run, --yes\n" + p.format_usage().strip())
    dash_flags = (a.factory_url, a.factory_machine, a.factory_web_url, a.orgs, a.no_switch, a.no_sidebar)
    if not a.herdr_app and any(x is not None and x is not False for x in dash_flags):
        raise Stop(2, "usage error: --factory-url, --factory-machine, --factory-web-url, --orgs, --no-switch and --no-sidebar need --herdr-app")
    if a.herdr_app:
        if a.check:
            raise Stop(2, "usage error: --herdr-app goes with --dry-run or --yes")
        if a.orgs and a.orgs != "auto" and not set(a.orgs.split(",")) <= set(HERDR_APP_ORGS):
            raise Stop(2, "usage error: --orgs takes auto, claude, cursor or claude,cursor")
        for v in (a.factory_url, a.factory_web_url):
            if v and not re.match(r"^https?://[^\s]+$", v):
                raise Stop(2, "usage error: --factory-url and --factory-web-url must be http(s) URLs")
        if a.factory_machine and not re.match(r"^[A-Za-z0-9][A-Za-z0-9._-]*$", a.factory_machine):
            raise Stop(2, "usage error: --factory-machine must be a machine name from `hc machine ls`")
    if bool(a.project_id) != bool(a.project_repo):
        raise Stop(2, "usage error: --project-id and --project-repo go together")
    if (a.project_name or a.project_gh) and not a.project_id:
        raise Stop(2, "usage error: --project-name and --project-gh need --project-id and --project-repo")
    if a.ref.startswith("-") or a.repo_url.startswith("-"):
        raise Stop(2, "usage error: --ref and --repo-url must not start with '-'")
    return a


def main(argv=None, host=None, out=None):
    out = out or sys.stdout
    argv = sys.argv[1:] if argv is None else argv
    try:
        a = parse(argv)
    except Stop as s:
        print(s.msg, file=sys.stderr)
        return s.code
    except SystemExit as e:  # --help
        return e.code if isinstance(e.code, int) else 0
    host = host or Host()
    if a.herdr_app:
        return run_herdr_app(host, a, out)
    if a.check:
        return run_check(host, a, out)
    c = Ctx(host, a, out, plan=a.dry_run)
    c.port = a.port if a.port is not None else DEFAULT_PORT
    return run_steps(c)


if __name__ == "__main__":
    if sys.version_info < (3, 8):
        sys.exit("install.py needs Python 3.8 or newer to run; the factory itself needs 3.11 or newer")
    sys.exit(main())
