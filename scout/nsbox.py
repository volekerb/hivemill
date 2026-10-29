"""Eval sandbox, design D: the per-trial claude shim, the namespace wrapper and the contained runner.

Stage 1 is `unshare -r -n -m --pid ...` running `nsbox.py --setup` as root-in-namespace: it brings lo up, starts the
loopback forwarder (caps dropped), hides secret dirs under tmpfs, then execs stage 2: `unshare --map-user=<outer uid>`
that gives the real uid back and drops every capability before the real claude (or the checker) is exec'd. Every ~ path
is resolved from the passwd entry, never from $HOME (inside the pane HOME is the trial dir). Stdlib + util-linux only.
"""
import collections
import ctypes
import fcntl
import json
import codecs
import os
import pwd
import re
import shutil
import signal
import socket
import stat
import struct
import subprocess
import sys
import time
import traceback
from pathlib import Path

try:
    from scout import egress
except ImportError:  # run as a script by path (stage 1 setup, python -I): egress.py sits next to this file
    sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
    import egress

HIDE_DIRS = ("~/.claude", "~/.config", "~/.ssh", "~/.gnupg", "~/factory", "$XDG_RUNTIME_DIR")
HIDE_FILES = ("~/.git-credentials", "~/.netrc")
# allow mode masks all of /run (container-engine socket, ssh-unix-local/socket, the user bus); only these come back,
# read-only: /etc/resolv.conf links to /run/systemd/resolve/stub-resolv.conf, so DNS in a net=True profile needs that dir
RUN_MASK = "/run"
RUN_KEEP_RO = ("/run/systemd/resolve",)
TOKEN_FILE = "~/.config/factory/eval-oauth-token"
NO_TOKEN_MSG = "owner decision D3 pending: no eval token"
SCRUB_ENV = ("SSH_AUTH_SOCK", "DBUS_SESSION_BUS_ADDRESS")
NET_ENV = ("CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN",
           "HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy")
ENV_PASS = ("PATH", "LANG", "TERM", "TMPDIR")  # plus LC_*; run_contained(env=None) starts from only these
# what the evaluator's claude may inherit from the shim caller on top of ENV_PASS; anything else is dropped
CLAUDE_ENV_PASS = ("CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "DISABLE_AUTOUPDATER")
# the factory identity an evaluator's shim passes on from the pane; FACTORY_HOME, FACTORY_ROLE, CLAUDE_CONFIG_DIR and
# HOME are not taken from the pane at all: the shim's baked "env_set" wins (see write_shim)
EVAL_ENV_PASS = ("FACTORY_AGENT",)
PROXY_URL = "http://127.0.0.1:3128"
MS_BIND, MS_REMOUNT, MS_RDONLY = 4096, 32, 1
TMPFS_SIZE = "8m"  # size= of every hide/mask tmpfs unless an allow-mode caller asks for more (memory-backed)
_SIZE = re.compile(r"([0-9]+)([kKmMgG%]?)")
_UNIT = {"": 1, "k": 1 << 10, "m": 1 << 20, "g": 1 << 30}
TMPFS_MAX_BYTES = 16 << 30  # ceiling for one tmpfs size= (16g); see _size_ok
DROP_CAPS = ["--no-new-privs", "--bounding-set=-all", "--inh-caps=-all", "--ambient-caps=-all"]

Contained = collections.namedtuple("Contained", "returncode output timed_out")


def _size_ok(v):
    """A tmpfs size= value: k/m/g bytes with 0 < bytes <= TMPFS_MAX_BYTES, or N% of RAM with 1 <= N <= 100.

    The kernel's memparse wraps in 64 bits, so 17179869184g or 18446744073709551616 become 0, and size=0 is an
    UNLIMITED tmpfs. Python ints do not wrap, so the explicit ceiling refuses those; other suffixes (t, p, e) are refused."""
    m = _SIZE.fullmatch(str(v))
    if not m:
        return False
    n, unit = int(m.group(1)), m.group(2)
    if unit == "%":
        return 1 <= n <= 100
    return 0 < n * _UNIT[unit.lower()] <= TMPFS_MAX_BYTES


def real_home():
    return pwd.getpwuid(os.getuid()).pw_dir


def userns_available(unshare="unshare"):
    """(ok, reason): can this box run the stage-1 namespaces unprivileged?"""
    try:
        r = subprocess.run([unshare, "-r", "-n", "-m", "--pid", "--fork", "--mount-proc", "true"],
                           capture_output=True, text=True, timeout=20)
    except (OSError, subprocess.TimeoutExpired) as e:
        return False, f"cannot run {unshare}: {e}"
    if r.returncode != 0:
        return False, f"unshare -r -n -m failed (user namespaces restricted?): {r.stderr.strip()}"
    return True, "ok"


SUB_UID, SUB_GID = 1, 1  # subuid mode: the box runs as inside uid/gid 1 = host ids from the /etc/subuid and /etc/subgid ranges; no owner group


def _subid_range(path, user, uid):
    """(start, count) of the largest `name:start:count` line of /etc/subuid or /etc/subgid for this user (by name or
    number); (0, 0) when there is none or the file is unreadable."""
    best = (0, 0)
    try:
        lines = Path(path).read_text().splitlines()
    except OSError:
        return best
    for ln in lines:
        f = ln.split(":")
        if len(f) == 3 and f[0] in (user, str(uid)) and f[1].isdigit() and f[2].isdigit() and int(f[2]) > best[1]:
            best = (int(f[1]), int(f[2]))
    return best


def subuid_available(unshare="unshare"):
    """(ok, reason): can this box map a subordinate uid range (newuidmap/newgidmap installed, subuid/subgid ranges)?
    Fails closed: any doubt is (False, why); the caller must not fall back to the owner uid."""
    for tool in ("newuidmap", "newgidmap"):
        if not shutil.which(tool):
            return False, f"{tool} not found (install the uidmap package)"
    user = pwd.getpwuid(os.getuid()).pw_name
    for f in ("/etc/subuid", "/etc/subgid"):
        if _subid_range(f, user, os.getuid())[1] < 2:
            return False, f"no subordinate id range for {user} in {f} (usermod --add-subuids/--add-subgids)"
    ok, why = userns_available(unshare)
    if not ok:
        return False, why
    try:
        r = subprocess.run([sys.executable, "-I", os.path.abspath(__file__), "--launch", json.dumps({"unshare": shutil.which(unshare) or unshare, "probe": True}), "--", "true"],
                           capture_output=True, text=True, timeout=20)
    except (OSError, subprocess.TimeoutExpired) as e:
        return False, f"subuid probe could not run: {e}"
    if r.returncode != 0:
        return False, f"subuid mapping probe failed: {r.stderr.strip() or r.returncode}"
    return True, "ok"


def _expand(entry, home, runtime):
    return entry.replace("~", home, 1).replace("$XDG_RUNTIME_DIR", runtime) if entry.startswith(("~", "$")) else entry


def make_cfg(trial_dir, real_claude=None, home=None, trials_root=None, hide_dirs=HIDE_DIRS, hide_files=HIDE_FILES,
             vendor_dir=None, keep=(), net=False, forward=False, unshare="unshare", cwd=None, *,
             mode="deny", rw=(), ro=(), masks=(), pid=True, keep_env=(), tmpfs_size=TMPFS_SIZE, tmpfs_sizes=None,
             run_mask=True, subuid=False, copy=None):
    """Everything setup needs, as plain JSON-able data with absolute paths.

    The keyword-only args are the allow-list mode (docs/scout-nsbox.md#allow-list-mode). At their defaults the dict
    is exactly the deny-mode dict of old: the extra keys (mode, rw, ro, masks, pid, tmpfs_size, tmpfs_sizes) appear only
    when non-default. `subuid=True` runs the box as a subordinate host uid (see compose_stage1); allow mode only. `copy` ({"tree", "size", "mkdirs"}, subuid only) replaces the tree with a private tmpfs copy owned by the box uid (see _copy_tree). `tmpfs_size` is the size= of every allow-mode hide/mask tmpfs, `tmpfs_sizes` maps a hidden tree
    (or mask) to its own size; tmpfs is memory-backed, so keep them small. In allow mode /run is masked too (see
    RUN_MASK) unless `run_mask=False`."""
    if not _size_ok(tmpfs_size) or any(not _size_ok(v) for v in (tmpfs_sizes or {}).values()):
        raise ValueError(f"tmpfs size must be a positive number like 8m, 512k or 1g (0 means unlimited), at most 16g or 100%, "
                         f"got {tmpfs_size!r} / {tmpfs_sizes!r}")
    if mode not in ("deny", "allow"):
        raise ValueError(f"mode must be 'deny' or 'allow', got {mode!r}")
    home = str(home or real_home())
    trial = os.path.abspath(trial_dir)
    runtime = os.environ.get("XDG_RUNTIME_DIR") or f"/run/user/{os.getuid()}"
    claude = os.path.abspath(real_claude) if real_claude else None
    keeps = [trial, *keep] + ([claude, os.path.realpath(claude)] if claude else [])
    cfg = {
        "unshare": shutil.which(unshare) or unshare, "setpriv": shutil.which("setpriv") or "setpriv",
        "python": sys.executable, "nsbox": os.path.abspath(__file__), "egress": os.path.abspath(egress.__file__),
        "uid": os.getuid(), "gid": os.getgid(), "net": net, "trial": trial,
        "trials_root": os.path.abspath(trials_root) if trials_root else os.path.dirname(trial),
        "forward_sock": os.path.join(trial, "proxy.sock") if forward else None,
        "hide_dirs": [_expand(d, home, runtime) for d in hide_dirs],
        "hide_files": [_expand(f, home, runtime) for f in hide_files],
        "keep": [os.path.abspath(k) for k in keeps], "keep_ro": [os.path.abspath(vendor_dir)] if vendor_dir else [],
        "claude": claude, "cwd": os.path.abspath(cwd) if cwd else None,
        "scrub": [k for k in list(SCRUB_ENV) + ([] if forward else list(NET_ENV)) if k not in keep_env],
        "env_set": {"HTTPS_PROXY": PROXY_URL, "https_proxy": PROXY_URL, "NO_PROXY": "", "no_proxy": ""} if forward else {},
    }
    if not pid and forward:
        # the forwarder only dies with the pid namespace (--kill-child); without one it would outlive the run
        raise ValueError("pid=False cannot be combined with forward=True: the egress forwarder would outlive the "
                         "run; use pid=True, or net=True without forward")
    if mode == "allow":
        if vendor_dir or keep:
            raise ValueError("vendor_dir and keep are deny-mode args and would be silently dropped in allow mode; "
                             "list the paths in ro/rw instead")
        for p in [*rw, *ro]:
            if os.path.islink(p):
                raise ValueError(f"rw/ro path is a symlink: {p}; list its realpath {os.path.realpath(p)} instead")
            bad = _symlink_parent(os.path.abspath(p))
            if bad:
                raise ValueError(f"rw/ro path {p} has a symlink parent {bad}; list its realpath "
                                 f"{os.path.realpath(p)} instead")
        # nothing is implicitly writable: trial/keep/cwd are not bound back, only what the caller lists in rw/ro
        # (plus the claude link and its target, read-only, so stage 2 can exec it)
        cfg["mode"] = "allow"
        cfg["rw"] = [os.path.abspath(p) for p in rw]
        cfg["ro"] = [os.path.abspath(p) for p in [*ro, *([claude, os.path.realpath(claude)] if claude else [])]]
        if run_mask:  # `run_mask=False` is for tests that count mounts; no profile turns it off
            if RUN_MASK not in cfg["hide_dirs"]:
                cfg["hide_dirs"].append(RUN_MASK)
            cfg["ro"] += [k for k in RUN_KEEP_RO if os.path.isdir(k) and k not in cfg["ro"]]
        for p in cfg["ro"][len(ro):]:  # the claude link may be a link itself, its parents may not
            bad = _symlink_parent(p)
            if bad:
                raise ValueError(f"claude path {p} has a symlink parent {bad}")
        cfg["keep"] = cfg["keep_ro"] = []
    elif rw or ro or masks:
        raise ValueError("rw, ro and masks need mode='allow'")
    if mode != "allow" and (tmpfs_size != TMPFS_SIZE or tmpfs_sizes):
        raise ValueError("tmpfs_size and tmpfs_sizes need mode='allow'; deny mode keeps size=" + TMPFS_SIZE)
    if tmpfs_size != TMPFS_SIZE:
        cfg["tmpfs_size"] = str(tmpfs_size)
    if tmpfs_sizes:
        cfg["tmpfs_sizes"] = {os.path.abspath(k): str(v) for k, v in tmpfs_sizes.items()}
    if masks:
        cfg["masks"] = [os.path.abspath(m) for m in masks]
    if not pid:
        cfg["pid"] = False
    if subuid:
        if mode != "allow":
            raise ValueError("subuid needs mode='allow'")
        cfg["subuid"] = True
        if copy:
            cfg["copy"] = {"tree": os.path.abspath(copy["tree"]), "size": str(copy["size"]),
                           "mkdirs": [os.path.abspath(d) for d in copy.get("mkdirs", [])]}
            if not _size_ok(cfg["copy"]["size"]):
                raise ValueError(f"copy size must be like 512m, got {copy['size']!r}")
    elif copy:
        raise ValueError("copy needs subuid=True")
    return cfg


def compose_stage2(cfg, args):
    """Final argv: real uid back, then every capability dropped. The target is claude, or the caller's argv."""
    cmd = [cfg["claude"], *args] if cfg["claude"] else list(args)
    if cfg.get("subuid"):  # stage 1 is inside root (= the owner's host uid): become the subordinate uid, no owner groups
        return [cfg["setpriv"], f"--reuid={SUB_UID}", f"--regid={SUB_GID}", "--clear-groups", *DROP_CAPS, *cmd]
    return [cfg["unshare"], f"--map-user={cfg['uid']}", f"--map-group={cfg['gid']}", "--keep-caps",
            cfg["setpriv"], *DROP_CAPS, *cmd]


def compose_stage1(cfg, args):
    net = [] if cfg["net"] else ["-n"]
    pid = ["--pid", "--fork", "--kill-child", "--mount-proc"] if cfg.get("pid", True) else []
    if cfg.get("subuid"):
        # unshare -r would map the one owner uid only; the launcher builds the user namespace itself and has newuidmap
        # map inside 0 -> owner, inside 1.. -> the subuid range, then execs the same unshare minus -r
        return [cfg["python"], "-I", cfg["nsbox"], "--launch", json.dumps(cfg), "--", *args]
    return [cfg["unshare"], "-r", *net, "-m", *pid, "--propagation", "private", cfg["python"], "-I", cfg["nsbox"], "--setup", json.dumps(cfg), "--", *args]


# ---- stage 1 setup (root-in-namespace) -------------------------------------------------------------------------

def _mount(src, target, fstype=None, flags=0, data=None):
    libc = ctypes.CDLL(None, use_errno=True)
    if libc.mount(src.encode() if src else None, target.encode(), fstype.encode() if fstype else None,
                  ctypes.c_ulong(flags), data.encode() if data else None) != 0:
        e = ctypes.get_errno()
        raise OSError(e, f"mount {src} -> {target}: {os.strerror(e)}")


def _lo_up():
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as s:
        fcntl.ioctl(s, 0x8914, struct.pack("16sH14x", b"lo", 0x41))  # SIOCSIFFLAGS: IFF_UP | IFF_RUNNING


def _under(path, base):
    return path == base or path.startswith(base.rstrip("/") + "/")


def _start_forwarder(cfg):
    """Loopback forwarder as a child with no capabilities, listening before any dir gets hidden."""
    p = subprocess.Popen([cfg["setpriv"], *DROP_CAPS, cfg["python"], "-I", cfg["egress"], "forward", cfg["forward_sock"]],
                         cwd="/", stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env={})
    for _ in range(100):
        try:
            socket.create_connection(("127.0.0.1", 3128), timeout=0.2).close()
            return
        except OSError:
            if p.poll() is not None:
                break
            time.sleep(0.05)
    sys.exit("nsbox: forwarder did not start")


def _real(path):
    """realpath of the parent + the last name, so a symlinked leaf (the claude link) stays a link."""
    return os.path.join(os.path.realpath(os.path.dirname(path)), os.path.basename(path))


def _sibling_trials(cfg):
    """realpath of every other trial dir under trials_root. Both sides are resolved: a symlinked trials_root (or a
    trial given by another spelling) must not make the own trial look like a sibling, nor a sibling look like own."""
    root, own = cfg["trials_root"], os.path.realpath(cfg["trial"])
    if not os.path.isdir(root):
        return []
    found = (os.path.realpath(e.path) for e in os.scandir(root) if e.is_dir(follow_symlinks=False))
    return [p for p in found if p != own]


def _hide_and_keep(cfg):
    """Hide the secret dirs; returns the (real) dirs hidden and the kept paths, for the cwd decision."""
    dirs = [os.path.realpath(d) for d in cfg["hide_dirs"] if os.path.isdir(d)]
    dirs = [d for d in dirs if d != "/"]
    dirs += _sibling_trials(cfg)
    dirs = [d for d in dict.fromkeys(dirs) if not any(o != d and _under(d, o) for o in dirs)]
    # open what must stay visible BEFORE its parent is hidden; a bind from /proc/self/fd survives the tmpfs
    saved = []
    keeps = [_real(k) for k in cfg["keep"] + cfg["keep_ro"]]
    ro = [_real(k) for k in cfg["keep_ro"]]
    for k in keeps:
        if any(_under(k, d) for d in dirs) and os.path.exists(k):
            if os.path.islink(k):
                saved.append((k, None, os.readlink(k)))
            else:
                saved.append((k, os.open(k, os.O_PATH), None))
    for d in dirs:
        _mount("tmpfs", d, "tmpfs", 0, f"mode=0755,size={TMPFS_SIZE}")
    for k, fd, link in saved:
        os.makedirs(os.path.dirname(k), exist_ok=True)
        if link is not None:
            os.symlink(link, k)
            continue
        if stat.S_ISDIR(os.fstat(fd).st_mode):
            os.makedirs(k, exist_ok=True)
        else:
            Path(k).touch()
        _mount(f"/proc/self/fd/{fd}", k, None, MS_BIND)
        os.close(fd)
    for k in ro:  # read-only: re-bind keeping the locked nosuid/nodev/noexec flags
        _mount(k, k, None, MS_BIND)
        _mount(None, k, None, MS_BIND | MS_REMOUNT | MS_RDONLY | (os.statvfs(k).f_flag & 0x3E))
    for f in cfg["hide_files"]:
        if os.path.isfile(f) or os.path.islink(f):
            _mount("/dev/null", f, None, MS_BIND)
    return dirs, keeps


def _symlink_parent(path):
    """The first ancestor of an absolute path that is a symlink, else None (the leaf itself is not looked at)."""
    cur = "/"
    for name in [c for c in path.split("/") if c][:-1]:
        cur = os.path.join(cur, name)
        if os.path.islink(cur):
            return cur
    return None


def _open_chain(path):
    """Open a bind source component by component: openat(O_PATH|O_NOFOLLOW) from /. Returns (fd, None), or
    (None, target) when the leaf is a symlink (the documented claude link). A symlink in any parent component, even one
    swapped in after make_cfg checked, exits non-zero naming it; a missing source exits naming the path."""
    names = [c for c in path.split("/") if c]
    cur, walked = os.open("/", os.O_PATH | os.O_DIRECTORY), ""
    try:
        for i, name in enumerate(names):
            walked += "/" + name
            try:
                nxt = os.open(name, os.O_PATH | os.O_NOFOLLOW, dir_fd=cur)
            except FileNotFoundError:
                sys.exit(f"nsbox: bind source missing: {path}")
            except OSError as e:
                sys.exit(f"nsbox: cannot open bind source component {walked}: {e}")
            mode = os.fstat(nxt).st_mode
            if i == len(names) - 1:
                if stat.S_ISLNK(mode):
                    try:
                        return None, os.readlink(name, dir_fd=cur)
                    finally:
                        os.close(nxt)
                return nxt, None
            if stat.S_ISLNK(mode) or not stat.S_ISDIR(mode):
                os.close(nxt)
                sys.exit(f"nsbox: bind source {path} has a symlink or non-directory parent component: {walked}")
            os.close(cur)
            cur = nxt
        return os.dup(cur), None  # path was "/"
    finally:
        os.close(cur)


def _open_target(path, leaf=None):
    """Open a mount target (or a symlink's parent) component by component with openat(O_PATH|O_NOFOLLOW) from /, creating
    missing directories with mkdirat on the chain fd, never through a resolved path. `leaf` is "dir" or "file" to create
    a missing leaf that way, "link-parent" to return the fd of the parent directory, or None to
    require the leaf to exist. A symlink or non-directory in any parent component, a symlink leaf, or a failing create
    exits non-zero naming the component; the caller mounts onto /proc/self/fd/<fd>, so a swap after this walk cannot
    redirect the mount."""
    names = [c for c in path.split("/") if c]
    cur, walked = os.open("/", os.O_PATH | os.O_DIRECTORY), ""
    try:
        for i, name in enumerate(names):
            walked += "/" + name
            last = i == len(names) - 1
            if last and leaf == "link-parent":
                return os.dup(cur)
            try:
                try:
                    nxt = os.open(name, os.O_PATH | os.O_NOFOLLOW, dir_fd=cur)
                except FileNotFoundError:
                    if last and leaf is None:
                        sys.exit(f"nsbox: mount target missing: {path}")
                    if not last or leaf == "dir":
                        os.mkdir(name, 0o755, dir_fd=cur)
                    else:
                        os.close(os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o644, dir_fd=cur))
                    nxt = os.open(name, os.O_PATH | os.O_NOFOLLOW, dir_fd=cur)
            except OSError as e:
                sys.exit(f"nsbox: cannot open or create mount target component {walked}: {e}")
            mode = os.fstat(nxt).st_mode
            if stat.S_ISLNK(mode) or (not last and not stat.S_ISDIR(mode)):
                os.close(nxt)
                sys.exit(f"nsbox: mount target {path} has a symlink or non-directory component: {walked}")
            os.close(cur)
            cur = nxt
        return os.dup(cur)
    finally:
        os.close(cur)


def _bind_onto(src_fd, path, kind, leaf):
    """Bind /proc/self/fd/<src_fd> onto `path` through a no-follow chain; ro keeps the locked nosuid/nodev/noexec flags."""
    tfd = _open_target(path, leaf)
    try:
        _mount(f"/proc/self/fd/{src_fd}", f"/proc/self/fd/{tfd}", None, MS_BIND)
    finally:
        os.close(tfd)
    if kind == "ro":
        mfd = _open_target(path)  # the mount itself: the new top of the target, again no-follow
        try:
            fdp = f"/proc/self/fd/{mfd}"
            _mount(None, fdp, None, MS_BIND | MS_REMOUNT | MS_RDONLY | (os.statvfs(fdp).f_flag & 0x3E))
        finally:
            os.close(mfd)


def _depth(path):
    return len([c for c in path.split("/") if c])


def _allow_view(cfg):
    """Allow-list view: tmpfs over every hide tree, then bind back only what the caller listed, shorter paths first
    (an ro parent before its rw child, a mask after its parent and before its re-bound children). Any source that is
    missing or any failing mount exits non-zero before exec: never a partial view."""
    rw = [os.path.abspath(k) for k in cfg["rw"]]  # not _real: a parent symlink is refused below, never followed
    ro = [os.path.abspath(k) for k in cfg["ro"]]
    both = set(rw) & set(ro)
    if both:
        sys.exit(f"nsbox: path listed both rw and ro: {sorted(both)[0]}")
    masks = [os.path.abspath(m) for m in cfg.get("masks", [])]  # not realpath: a symlink in a mask path is refused
    sizes = {os.path.realpath(k): v for k, v in cfg.get("tmpfs_sizes", {}).items()}

    def tmpfs_opts(path):
        return f"mode=0755,size={sizes.get(path, cfg.get('tmpfs_size', TMPFS_SIZE))}"
    # not realpath: a symlink in a hide dir path is refused below, never followed
    dirs = [os.path.abspath(d) for d in cfg["hide_dirs"] if os.path.isdir(d) or os.path.islink(d)]
    dirs = [d for d in dirs if d != "/"]
    dirs = [d for d in dict.fromkeys(dirs) if not any(o != d and _under(d, o) for o in dirs)]
    for m in masks:
        if not os.path.isdir(m):
            sys.exit(f"nsbox: mask dir missing or not a directory: {m}")
        os.close(_open_target(m))  # refuse a symlink in the path now; the mount below walks it again
    entries = []  # (path, kind, fd, link); fds are opened BEFORE any tmpfs, a bind from /proc/self/fd survives it
    dir_fds = []
    try:
        for d in dirs:  # no-follow, so a parent swapped to a symlink after make_cfg cannot redirect the tmpfs
            fd = _open_target(d)
            dir_fds.append(fd)
            if not stat.S_ISDIR(os.fstat(fd).st_mode):
                sys.exit(f"nsbox: hide dir {d} is not a directory any more")
        for k, kind in [*((k, "rw") for k in rw), *((k, "ro") for k in ro)]:
            fd, link = _open_chain(k)
            entries.append((k, kind, fd, link))
        entries += [(m, "mask", None, None) for m in masks]
        entries.sort(key=lambda e: _depth(e[0]))  # stable: equal depth keeps the caller's order
        try:
            for d, dfd in zip(dirs, dir_fds):
                _mount("tmpfs", f"/proc/self/fd/{dfd}", "tmpfs", 0, tmpfs_opts(d))
            for k, kind, fd, link in entries:
                if kind == "mask":
                    mfd = _open_target(k)
                    try:
                        _mount("tmpfs", f"/proc/self/fd/{mfd}", "tmpfs", 0, tmpfs_opts(k))
                    finally:
                        os.close(mfd)
                    continue
                if link is not None:  # a symlink leaf stays a link; only recreate it where a tmpfs ate it
                    pfd, name = _open_target(k, "link-parent"), os.path.basename(k)
                    try:
                        try:
                            os.stat(name, dir_fd=pfd, follow_symlinks=False)
                        except FileNotFoundError:
                            os.symlink(link, name, dir_fd=pfd)
                    finally:
                        os.close(pfd)
                    continue
                is_dir = stat.S_ISDIR(os.fstat(fd).st_mode)
                _bind_onto(fd, k, kind, "dir" if is_dir else "file")
        except OSError as e:
            sys.exit(f"nsbox: allow view failed: {e}")
    finally:
        for _, _, fd, _ in entries:
            if fd is not None:
                os.close(fd)
        for fd in dir_fds:
            os.close(fd)
    for f in cfg["hide_files"]:
        f = os.path.abspath(f)
        if os.path.isfile(f) or os.path.islink(f):
            ffd = _open_target(f)  # no-follow: a symlink in any component, leaf included, exits naming it
            try:
                if stat.S_ISDIR(os.fstat(ffd).st_mode):
                    sys.exit(f"nsbox: hide file {f} is a directory now")
                _mount("/dev/null", f"/proc/self/fd/{ffd}", None, MS_BIND)
            except OSError as e:
                sys.exit(f"nsbox: allow view failed: {e}")
            finally:
                os.close(ffd)
    return [*dirs, *masks], [*rw, *ro]


def _settle_cwd(cfg, want, dirs, keeps):
    """The inherited cwd pins the pre-mount dentry, so `..` and /proc/1/cwd would reach the real tree. Re-resolve it
    through the new mounts when it is visible, else land in the trial dir."""
    # allow mode has no implicit trial dir: the fallback is the first rw path
    fallback = (cfg["rw"] or ["/"])[0] if cfg.get("mode") == "allow" else cfg["trial"]
    hidden = any(_under(want, d) for d in dirs) and not any(_under(want, k) for k in keeps)
    try:
        os.chdir(fallback if hidden or not want else want)
    except OSError:
        os.chdir(fallback)


def tree_bytes(path):
    """Bytes of the regular files under `path` (links not followed); sizes the tmpfs copy."""
    total = 0
    for root, _dirs, files in os.walk(path):
        for f in files:
            try:
                st = os.lstat(os.path.join(root, f))
            except FileNotFoundError:
                continue
            total += st.st_size if stat.S_ISREG(st.st_mode) else 0
    return total


def _copy_tree(cfg):
    """Subuid mode: put a private tmpfs over the tree, fill it from the read-only original, and give it to the box uid.
    Nothing the run writes reaches the host, and no host file owned by the owner is writable from the box. The old view
    stays reachable through the fd opened before the tmpfs goes on. Any failure exits non-zero before exec."""
    c = cfg["copy"]
    tree = c["tree"]
    try:
        src_fd = _open_target(tree)
        dst_fd = _open_target(tree)
    except OSError as e:
        sys.exit(f"nsbox: copy of {tree} failed: {e}")
    try:
        _mount("tmpfs", f"/proc/self/fd/{dst_fd}", "tmpfs", 0, f"mode=0755,uid={SUB_UID},gid={SUB_GID},size={c['size']}")
        def keep(_d, names):  # sockets, fifos and devices are not copied
            return [n for n in names if not (stat.S_ISREG(m := os.lstat(os.path.join(_d, n)).st_mode) or stat.S_ISDIR(m) or stat.S_ISLNK(m))]
        shutil.copytree(f"/proc/self/fd/{src_fd}", tree, symlinks=True, dirs_exist_ok=True, ignore=keep)
        for d in c["mkdirs"]:
            os.makedirs(d, exist_ok=True)
        for root, dirs, files in os.walk(tree):
            for n in [*dirs, *files]:
                os.lchown(os.path.join(root, n), SUB_UID, SUB_GID)
    except (OSError, shutil.Error) as e:
        sys.exit(f"nsbox: copy of {tree} into the private tmpfs failed: {e}; the command was not run")
    finally:
        os.close(src_fd)
        os.close(dst_fd)


def setup(cfg, args):
    if not cfg["net"]:
        _lo_up()
    if cfg["forward_sock"]:
        _start_forwarder(cfg)
    try:
        want = os.path.realpath(cfg.get("cwd") or os.getcwd())
    except OSError:
        want = None
    dirs, keeps = _allow_view(cfg) if cfg.get("mode", "deny") == "allow" else _hide_and_keep(cfg)
    if cfg.get("copy"):
        _copy_tree(cfg)
    _settle_cwd(cfg, want, dirs, keeps)
    for k in cfg["scrub"]:
        os.environ.pop(k, None)
    os.environ.update(cfg["env_set"])
    argv = compose_stage2(cfg, args)
    os.execv(argv[0], argv)


# ---- outside: shim, proxy, contained runner --------------------------------------------------------------------

def read_token(path):
    """The token, or exit 3. Must be a regular file, mode 0600, owned by us; never logged."""
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    except OSError:
        _fail3(path)
    with os.fdopen(fd) as f:
        st = os.fstat(f.fileno())
        if not stat.S_ISREG(st.st_mode) or stat.S_IMODE(st.st_mode) != 0o600 or st.st_uid != os.getuid():
            _fail3(path)
        return f.read().strip()


def _fail3(path):
    print(f"{NO_TOKEN_MSG} ({path}: must exist, be a regular file, mode 0600, owned by uid {os.getuid()})", file=sys.stderr)
    sys.exit(3)


def start_proxy(trial_dir, cfg, allow=egress.ALLOW, max_life=egress.MAX_LIFE, upstream=None, watch_pid=None):
    sock = os.path.join(trial_dir, "proxy.sock")
    arg = {"sock": sock, "allow": allow, "watch_pid": watch_pid or os.getpid(), "max_life": max_life,
           "upstream": upstream or {}}
    p = subprocess.Popen([cfg["python"], "-I", cfg["egress"], "serve", json.dumps(arg)], start_new_session=True,
                         stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env={})
    Path(trial_dir, "proxy.pid").write_text(f"{p.pid}\n")
    for _ in range(100):
        if os.path.exists(sock):
            return p.pid
        time.sleep(0.05)
    stop_proxy(trial_dir)
    sys.exit("nsbox: proxy did not start")


def stop_proxy(trial_dir):
    """Kill the pid in proxy.pid if it is still our proxy. Idempotent."""
    pidfile = Path(trial_dir, "proxy.pid")
    try:
        pid = int(pidfile.read_text())
        cmdline = Path(f"/proc/{pid}/cmdline").read_bytes()
    except (OSError, ValueError):
        pid = cmdline = None
    if pid and b"egress.py" in cmdline and egress.pid_alive(pid):
        os.kill(pid, signal.SIGTERM)
        for _ in range(50):
            if not egress.pid_alive(pid):
                break
            time.sleep(0.05)
        else:
            os.kill(pid, signal.SIGKILL)
    pidfile.unlink(missing_ok=True)


START_LOG_CAP = 64 * 1024
_TOKEN_SHAPE = re.compile(r"sk-ant-[A-Za-z0-9_-]+")


def redact(text, token=None, shape=True):
    """Text with the whole OAuth token (when known) and, with `shape`, anything shaped like one replaced."""
    if token:
        text = text.replace(token, "[redacted-token]")
    return _TOKEN_SHAPE.sub("[redacted-token]", text) if shape else text


def open_log_dir(trial_dir, create=False):
    """fd of <trial>/log opened without following a symlink at the trial dir or at log/ (claude can write both), or
    None. The caller closes it."""
    flags = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW
    try:
        tfd = os.open(trial_dir, flags)
    except OSError:
        return None
    try:
        if create:
            try:
                os.mkdir("log", 0o700, dir_fd=tfd)
            except FileExistsError:
                pass
        return os.open("log", flags, dir_fd=tfd)
    except OSError:
        return None
    finally:
        os.close(tfd)


def start_log_append(trial_dir, text, token=None, limit=START_LOG_CAP):
    """Append `text` (exact token redacted; shapes are redacted when the log is displayed) to <trial>/log/start.log, never growing it past `limit` bytes (what does not fit is
    dropped: no trim, no rewrite). Returns False when something was dropped. Never raises: the log is a diagnostic
    and must not turn a start into a different failure. Symlinks (planted by the sandboxed claude) are never
    followed; anything but a regular file is refused."""
    dfd = fd = None
    try:
        data = redact(text, token, shape=False).encode("utf-8", "replace")
        dfd = open_log_dir(trial_dir, create=True)
        if dfd is None:
            return False
        fd = os.open("start.log", os.O_WRONLY | os.O_APPEND | os.O_CREAT | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600, dir_fd=dfd)
        if not stat.S_ISREG(os.fstat(fd).st_mode):
            return False
        room = max(0, limit - os.fstat(fd).st_size)
        if room:
            os.write(fd, data[:room])
        return len(data) <= room
    except OSError:
        return False
    finally:
        for f in (fd, dfd):
            if f is not None:
                os.close(f)


def _stamp():
    return time.strftime("%Y-%m-%dT%H:%M:%S%z")


TEE_READ = 4096
TEE_RESERVE = 2048  # of the cap, kept for the records after stderr (the exit status) once stderr has filled its share


def _tee_stderr(pipe, trial_dir, token):
    """Copy the stage-1 stderr to our own stderr and (redacted) into start.log. Reads fixed-size chunks (the
    sandbox controls this stream, newline-free or not): memory stays O(chunk + token). Only the exact token is redacted here
    (no shape matching on this stream; token shapes are redacted when the log is displayed): the last len(token)-1
    characters are held back so a token split across two reads is still redacted whole. Past its share of the cap the log gets one
    marker and nothing more, but the stream is drained to EOF so claude never blocks on a full pipe."""
    fd = pipe.fileno()
    keep = len(token) - 1 if token else 0
    dec = codecs.getincrementaldecoder("utf-8")("replace")
    carry = ""
    full = False

    def to_log(data):
        nonlocal full
        if full or not data:
            return
        if not start_log_append(trial_dir, data, token, START_LOG_CAP - TEE_RESERVE):
            full = True
            start_log_append(trial_dir, f"\n{_stamp()} [truncated]\n", token)

    while True:
        try:
            chunk = os.read(fd, TEE_READ)
        except InterruptedError:
            continue
        except OSError:
            break
        if not chunk:
            break
        try:
            os.write(2, chunk)
        except OSError:
            pass
        buf = redact(carry + dec.decode(chunk), token, shape=False)  # a whole token is gone; a partial one is in the last keep chars
        cut = max(len(buf) - keep, 0)
        carry = buf[cut:]
        to_log(buf[:cut])
    to_log(carry + dec.decode(b"", final=True))


class _StopBeforeStart(Exception):
    def __init__(self, signo):
        super().__init__(signo)
        self.signo = signo


def shim_main(shim_cfg, args):
    """What bin/claude does: token, proxy outside, then stage 1 as a child (stdin/stdout inherited, stderr teed into
    <trial>/log/start.log with its exit status). Never returns. Every failure on the way is logged there too."""
    cfg = shim_cfg["ns"]
    trial = cfg["trial"]
    token = None
    proc = None
    # SIGINT already reaches stage 1 through the shared tty process group: forwarding it too would double it
    signal.signal(signal.SIGINT, signal.SIG_IGN)
    pending = []  # a stop that arrives before stage 1 exists must still stop the start

    def on_stop(n, _f):
        if proc is not None:
            if proc.poll() is None:
                proc.send_signal(n)
        else:
            pending.append(n)

    # stage 1 gets the same stop/diagnostic signals the shim gets (SIGINT excepted, see above)
    for sig in (signal.SIGTERM, signal.SIGHUP, signal.SIGQUIT, signal.SIGUSR1, signal.SIGUSR2):
        signal.signal(sig, on_stop)
    try:
        token = read_token(shim_cfg["token_file"])
        if pending:  # a stop during read_token must not start the proxy
            raise _StopBeforeStart(pending[0])
        start_proxy(trial, cfg, shim_cfg["proxy_allow"], shim_cfg["proxy_max_life"], shim_cfg["upstream"])
        passed = ENV_PASS + CLAUDE_ENV_PASS + (EVAL_ENV_PASS if shim_cfg.get("eval_env") else ())
        env = {k: v for k, v in os.environ.items() if k in passed or k.startswith("LC_")}
        env["HOME"] = cfg["trial"]
        env.update(shim_cfg.get("env_set") or {})
        env["CLAUDE_CODE_OAUTH_TOKEN"] = token
        argv = compose_stage1(cfg, args)
        if pending:
            raise _StopBeforeStart(pending[0])
        start_log_append(trial, f"{_stamp()} stage1 exec {argv[0]}\n", token)
        proc = subprocess.Popen(argv, env=env, stderr=subprocess.PIPE,  # SIG_IGN would be inherited: undo it for the child
                                preexec_fn=lambda: signal.signal(signal.SIGINT, signal.SIG_DFL))
        if pending:  # arrived between the check and Popen returning
            proc.send_signal(pending[0])
    except _StopBeforeStart as e:
        stop_proxy(trial)
        start_log_append(trial, f"{_stamp()} stopped by signal {e.signo} before stage 1 started\n", token)
        sys.exit(128 + e.signo)
    except BaseException as e:  # SystemExit from read_token/start_proxy too: record it, keep its exit status
        start_log_append(trial, f"{_stamp()} start failed: {type(e).__name__}: {e}\n{traceback.format_exc()}", token)
        raise
    _tee_stderr(proc.stderr, trial, token)
    rc = proc.wait()
    start_log_append(trial, f"{_stamp()} stage1 exited rc={rc}\n", token)
    sys.exit(128 - rc if rc < 0 else rc)


SHIM = """#!{python} -I
import json, sys
sys.path.insert(0, {repo!r})
from scout import nsbox
nsbox.shim_main(json.loads({cfg!r}), sys.argv[1:])
"""


def write_shim(trial_dir, real_claude, token_file=None, proxy_allow=egress.ALLOW, home=None, trials_root=None,
               hide_dirs=HIDE_DIRS, vendor_dir=None, upstream=None, proxy_max_life=egress.MAX_LIFE,
               factory_src=None, unshare="unshare", ns=None, shim_extra=None):
    """Write <trial>/bin/claude (0755) and the read-only factory CLI snapshot <trial>/bin/factory (+ scout/).
    `upstream` maps allow-listed host:port to a real address (test seam). `ns` is a ready nsbox cfg (e.g. an
    allow-list one) used instead of building the deny-mode one; `shim_extra` is merged into the shim's baked cfg
    (eval_env, env_set). Returns the shim path."""
    home = str(home or real_home())
    trial = Path(trial_dir).resolve()
    real_claude = os.path.abspath(real_claude)
    if os.path.basename(real_claude) != "claude":
        raise ValueError(f"real_claude must be an absolute path whose basename is claude, got {real_claude}")
    repo = Path(__file__).resolve().parent.parent
    if ns is None:
        ns = make_cfg(trial, real_claude, home, trials_root, hide_dirs, vendor_dir=vendor_dir, forward=True, unshare=unshare)
    shim_cfg = {"ns": ns, "token_file": os.path.abspath(token_file or _expand(TOKEN_FILE, home, "")),
                "proxy_allow": proxy_allow, "proxy_max_life": proxy_max_life, "upstream": upstream or {},
                **(shim_extra or {})}
    bin_dir = trial / "bin"
    bin_dir.mkdir(parents=True, exist_ok=True)
    shim = bin_dir / "claude"
    shim.write_text(SHIM.format(python=sys.executable, repo=str(repo), cfg=json.dumps(shim_cfg)))
    shim.chmod(0o755)
    src = Path(factory_src) if factory_src else repo / "bin" / "factory"
    shutil.copy2(src, bin_dir / "factory")
    shutil.copy2(src.resolve().parent / "factory_cli.py", bin_dir / "factory_cli.py")  # the stub imports it
    # bin/factory_cli.py imports scout/ from <its dir>/..: snapshot that package and web/dbpath.py too
    shutil.copytree(src.resolve().parent.parent / "scout", trial / "scout", dirs_exist_ok=True,
                    ignore=shutil.ignore_patterns("__pycache__"))
    (trial / "web").mkdir(exist_ok=True)  # and the DB-path helper (web/dbpath.py, stdlib only), not the web UI
    for name in ("__init__.py", "dbpath.py"):
        shutil.copy2(src.resolve().parent.parent / "web" / name, trial / "web" / name)
    return shim


def _refuse_hidden_cwd(cwd, cfg):
    """cwd is bound back in full, so one equal to, above or inside a hide dir (or a sibling trial) would expose it."""
    want, trial = os.path.realpath(cwd), os.path.realpath(cfg["trial"])
    if _under(want, trial):
        return
    hidden = [os.path.realpath(d) for d in cfg["hide_dirs"]]
    hidden += _sibling_trials(cfg)
    for d in hidden:
        if _under(want, d) or _under(d, want):
            raise ValueError(f"cwd {cwd} is equal to, above or inside the hidden dir {d}; use the trial dir "
                             f"{trial} or a path under it")


def _contained_cfg(trial_dir, cwd, home, trials_root, hide_dirs, vendor_dir, net, unshare):
    """The allow-list cfg of run_contained (docs/scout-nsbox.md#run_contained): tmpfs over the real home and the runtime
    dir (plus any extra hide_dirs the caller names; /tmp stays as before), bound back only the trial dir (rw), the cwd
    when it lies outside the trial (rw, as before) and the vendored plugin dir (ro). Siblings stay hidden even when
    the trials root lies outside every hide tree."""
    home = os.path.realpath(home or real_home())
    runtime = os.environ.get("XDG_RUNTIME_DIR") or f"/run/user/{os.getuid()}"
    extra = [] if hide_dirs == HIDE_DIRS else [_expand(d, home, runtime) for d in hide_dirs]
    hide = [os.path.realpath(d) for d in (home, runtime, *extra) if os.path.isdir(d)]
    trial = os.path.realpath(trial_dir)
    root = os.path.realpath(trials_root) if trials_root else os.path.dirname(trial)
    rw = [trial]
    if cwd and not _under(os.path.realpath(cwd), trial):
        rw.append(os.path.realpath(cwd))
    masks = [] if any(_under(root, h) for h in hide) else [root]
    return make_cfg(trial, None, home, root, mode="allow", hide_dirs=hide, hide_files=(), rw=rw,
                    ro=[os.path.realpath(vendor_dir)] if vendor_dir else [], masks=masks, net=net, unshare=unshare,
                    cwd=cwd)


def run_contained(trial_dir, argv, cwd=None, env=None, timeout=60, net=False, home=None, trials_root=None,
                  hide_dirs=HIDE_DIRS, vendor_dir=None, unshare="unshare", env_extra=None):
    """Run argv inside the same hiding as the shim: no proxy, no forwarder, no token. net=True keeps the host
    network (off by default). env=None means an allow-list (ENV_PASS, LC_*, HOME=trial dir, env_extra), never the
    caller's whole environment. Returns Contained(returncode, output, timed_out); the tree dies on timeout."""
    cfg = _contained_cfg(trial_dir, cwd, home, trials_root, hide_dirs, vendor_dir, net, unshare)
    if cwd:
        _refuse_hidden_cwd(cwd, cfg)
    if env is None:
        env = {k: v for k, v in os.environ.items() if k in ENV_PASS or k.startswith("LC_")}
        env.update({"HOME": cfg["trial"], **(env_extra or {})})
    p = subprocess.Popen(compose_stage1(cfg, argv), cwd=cwd, env=dict(env),
                         stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                         start_new_session=True)
    try:
        out, _ = p.communicate(timeout=timeout)
        timed_out = False
    except subprocess.TimeoutExpired:
        os.killpg(p.pid, signal.SIGKILL)  # --kill-child takes the pid namespace down with its init
        out, _ = p.communicate()
        timed_out = True
    return Contained(p.returncode, out.decode(errors="replace"), timed_out)


def launch(cfg, args):
    """Subuid stage 0: new user namespace, id maps via newuidmap/newgidmap, then exec the stage-1 unshare inside it.
    Exits non-zero (never runs `args`) when any step fails. Returns nothing: it exits with the child's status."""
    uid, gid = os.getuid(), os.getgid()
    user = pwd.getpwuid(uid).pw_name
    ranges = {"newuidmap": _subid_range("/etc/subuid", user, uid), "newgidmap": _subid_range("/etc/subgid", user, uid)}
    if min(c for _, c in ranges.values()) < 2:
        sys.exit("nsbox: no subordinate id range in /etc/subuid or /etc/subgid; the command was not run")
    ready_r, ready_w = os.pipe()
    go_r, go_w = os.pipe()
    pid = os.fork()
    if pid == 0:
        try:
            os.close(ready_r), os.close(go_w)
            os.unshare(os.CLONE_NEWUSER)
            os.write(ready_w, b"1")
            if os.read(go_r, 1) != b"1":
                os._exit(125)
            net = [] if cfg.get("net") else ["-n"]
            pidns = ["--pid", "--fork", "--kill-child", "--mount-proc"] if cfg.get("pid", True) else []
            if cfg.get("probe"):
                argv = [cfg["unshare"], "-m", "--pid", "--fork", "--mount-proc", "true"]
            else:
                argv = [cfg["unshare"], *net, "-m", *pidns, "--propagation", "private", cfg["python"], "-I", cfg["nsbox"],
                        "--setup", json.dumps(cfg), "--", *args]
            os.execv(argv[0], argv)
        except OSError as e:
            sys.stderr.write(f"nsbox: subuid child failed: {e}\n")
            os._exit(126)
    os.close(ready_w), os.close(go_r)
    failed = None
    if os.read(ready_r, 1) != b"1":
        failed = "child could not create a user namespace"
    else:
        for tool, own in (("newuidmap", uid), ("newgidmap", gid)):
            start, count = ranges[tool]
            r = subprocess.run([tool, str(pid), "0", str(own), "1", "1", str(start), str(count)],
                               capture_output=True, text=True)
            if r.returncode != 0:
                failed = f"{tool} failed: {r.stderr.strip() or r.returncode}"
                break
    if failed:
        os.close(go_w)  # child reads EOF and exits 125
        os.waitpid(pid, 0)
        sys.exit(f"nsbox: subuid mapping failed: {failed}; the command was not run")
    os.write(go_w, b"1")
    for s in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
        signal.signal(s, lambda sig, _f: os.kill(pid, sig))
    while True:
        try:
            _, st = os.waitpid(pid, 0)
            break
        except InterruptedError:
            continue
    rc = os.waitstatus_to_exitcode(st)
    sys.exit(rc if rc >= 0 else 128 - rc)


if __name__ == "__main__":
    if len(sys.argv) >= 4 and sys.argv[1] == "--launch" and sys.argv[3] == "--":
        launch(json.loads(sys.argv[2]), sys.argv[4:])
    if len(sys.argv) < 4 or sys.argv[1] != "--setup" or sys.argv[3] != "--":
        sys.exit("usage: nsbox.py --setup <cfg json> -- ARGS...")
    setup(json.loads(sys.argv[2]), sys.argv[4:])
