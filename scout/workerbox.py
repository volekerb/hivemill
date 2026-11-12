"""Worker profile (G-010): the per-agent claude shim and the allow-list view a sandboxed worker runs in.

SECURITY CODE. The network-ON and env pass-through choices of the worker profile live here (not in nsbox.py).
`prepare_agent` runs in HQ and writes <WORKERS_ROOT>/<agent>/{bin/claude, gitconfig, claude/}. The shim it writes is
what the pane's `claude` resolves to. It runs OUTSIDE the namespace as the owner, reads the two token files, builds
the env from an allow-list and execs nsbox stage 1 (`unshare -r -m`, no pid namespace, network on), which hides the
home, the runtime dir, /tmp and /dev/shm, binds back only what is listed below, drops every capability and execs the
real claude by absolute path. The whole chain is exec-only: claude keeps the shim's pid, process group and session.

Everything the worker can write (agent_dir/claude, db_dir, optionally the cwd) is untrusted from outside: the shim,
the gitconfig and agent_dir/bin are only ever written with temp file + rename, and nothing read back from
agent_dir/claude is followed through a symlink. Every ~ is resolved from the passwd entry, never from $HOME.
"""
import json
import os
import re
import shlex
import stat
import subprocess
import sys
import tempfile
from pathlib import Path

try:
    from scout import ghcred, nsbox
except ImportError:  # run as a script by path
    sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
    from scout import ghcred, nsbox

# pm's cwd is ~/factory, which the cwd rule refuses (plan 7.5: documented). researcher (G-029) is always sandboxed: it runs
# third-party code in its per-task research dir (HQ passes the research root; allowed for that role only) with the read-only token.
SANDBOXABLE = {"reviewer", "qa", "coder", "researcher"}
SLUG = re.compile(r"[A-Za-z0-9][A-Za-z0-9_-]{0,63}")

_HOME = nsbox.real_home()
WORKERS_ROOT = f"{_HOME}/.local/state/factory/workers"
OAUTH_TOKEN = f"{_HOME}/.config/factory/worker-oauth-token"
GH_RO = f"{_HOME}/.config/factory/gh-worker-ro-token"
GH_RW = f"{_HOME}/.config/factory/gh-worker-rw-token"
ROLE_GH = {"reviewer": GH_RO, "qa": GH_RO, "coder": GH_RW, "researcher": GH_RO}
_GH_KEY = {"reviewer": "gh_ro", "qa": "gh_ro", "coder": "gh_rw", "researcher": "gh_ro"}  # same mapping, for token_dir overrides

# What the worker's claude may inherit from the shim caller (the pane); anything else is dropped. LC_* also passes.
#   PATH, LANG, TERM            basics (PATH starts with the agent's bin/ so the pane's `claude` is the shim)
#   nsbox.CLAUDE_ENV_PASS       the same three claude knobs the evaluator gets
#   FACTORY_AGENT, FACTORY_ROLE identity for the factory CLI
#   FACTORY_HOME                where the CLI finds the code and the db dir
#   HERDR_PANE_ID               identity recovery in bin/factory _identity (the pane id only; the sockets are hidden)
# Never passed: SSH_AUTH_SOCK, DBUS_SESSION_BUS_ADDRESS, TMPDIR, proxies, API keys, any other caller variable.
WORKER_ENV_PASS = ("PATH", "LANG", "TERM", *nsbox.CLAUDE_ENV_PASS,
                   "FACTORY_AGENT", "FACTORY_ROLE", "FACTORY_HOME", "HERDR_PANE_ID")
# Private scratch of a worker (Claude scratchpad, test temp files, clones) lives in the hide tmpfs; 8m ENOSPCs. tmpfs is
# memory-backed but only allocates what is written, so these are caps, not reservations: the box has ~5.7 GB RAM + 4 GB
# swap, a worker pays at most 512m + 64m when it fills both. The tmp_max_pct gate only watches the host /tmp, never these
# per-namespace tmpfs, so these caps are the only bound.
TMP_SIZE, HOME_SIZE = "512m", "64m"
TERMS_NAME = "scrub-terms.txt"
KEEP_ENV = ("CLAUDE_CODE_OAUTH_TOKEN", "GH_TOKEN")  # exempt from nsbox's scrub list (net=True, forward=False)

SEED = {"hasCompletedOnboarding": True, "theme": "dark", "projects": {}}
PROJECT_SEED = {"hasTrustDialogAccepted": True, "hasCompletedProjectOnboarding": True}
CLAUDE_JSON_MAX = 16 << 20  # a larger .claude.json is refused, never truncated and reseeded (worker state would be lost)


def _paths(home, token_dir=None, workers_root=None):
    tdir = token_dir or f"{home}/.config/factory"
    return {"workers": workers_root or f"{home}/.local/state/factory/workers",
            "oauth": f"{tdir}/worker-oauth-token", "gh_ro": f"{tdir}/gh-worker-ro-token",
            "gh_rw": f"{tdir}/gh-worker-rw-token"}


def check_agent_name(agent):
    if not isinstance(agent, str) or not SLUG.fullmatch(agent):
        raise ValueError(f"agent name {agent!r} is not a slug ([A-Za-z0-9][A-Za-z0-9_-]*, max 64): it becomes a path")
    return agent


def _under(path, base):
    return path == base or path.startswith(base.rstrip("/") + "/")


def _refuse_cwd(cwd, home, factory_home, agent_dir, hide, research_root=None):
    """T-0127's rule for the allow view: a cwd equal to or above home, factory_home, the agent dir or a hide tree
    would expose it; one inside a hide tree is hidden. Inside factory_home only worktrees/ qualifies: a writable cwd
    under bin/, roles/ or db/ would make code that HQ runs writable."""
    want = os.path.realpath(cwd)
    if research_root:  # a researcher's cwd is a directory strictly below the research root HQ names, nothing else
        root = os.path.realpath(research_root)
        if want != root and _under(want, root) and not _under(want, agent_dir) and not _under(want, factory_home):
            return want
    for guarded in (home, factory_home, agent_dir, *hide):
        if _under(guarded, want):
            raise ValueError(f"cwd {cwd} is equal to or above {guarded}, which the worker profile hides or protects; "
                             f"use a worktree under {factory_home}/worktrees")
    if _under(want, factory_home):
        if want == f"{factory_home}/worktrees" or not _under(want, f"{factory_home}/worktrees"):
            raise ValueError(f"cwd {cwd} is inside {factory_home} but not under its worktrees/ dir")
        return want
    for tree in hide:
        if _under(want, tree):
            raise ValueError(f"cwd {cwd} is inside the hidden tree {tree}; use a worktree under {factory_home}/worktrees")
    return want


GO_ROOT_REL = ".local/go"
GO_MODCACHE_REL = (".cache/go/path/pkg/mod", ".local/lib/factory/go/mod")  # first real dir wins; the second is T-1336's private cache
GO_DIR = "go"  # per-agent dir name under <agent dir>: cache, tmp, path


def go_paths(home, agent_dir, go_root=None, go_modcache=None):
    """The Go toolchain facts for one worker, or None when there is no usable Go (then the sandbox is as before).

    Paths are realpath'd: CI makes ~/.local/go a symlink and nsbox refuses a symlink in ro."""
    root = os.path.realpath(go_root or f"{home}/{GO_ROOT_REL}")
    exe = os.path.join(root, "bin", "go")
    if not (os.path.isfile(exe) and os.access(exe, os.X_OK)):
        return None
    mod = None
    for cand in ([go_modcache] if go_modcache else [f"{home}/{rel}" for rel in GO_MODCACHE_REL]):
        if os.path.isdir(cand):
            mod = os.path.realpath(cand)
            break
    return {"root": root, "modcache": mod, "dir": f"{agent_dir}/{GO_DIR}"}


def go_env_for(go):
    """The env HQ sets for Go inside: offline, local toolchain, per-agent cache and tmp on disk."""
    env = {"GOROOT": go["root"], "GOCACHE": f"{go['dir']}/cache", "GOTMPDIR": f"{go['dir']}/tmp",
           "GOPATH": f"{go['dir']}/path", "GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off",
           "GOFLAGS": "-mod=readonly", "GOENV": "off", "GOWORK": "off"}
    if go["modcache"]:
        env["GOMODCACHE"] = go["modcache"]
    return env


def _hide_trees(home, runtime, hide_extra=()):
    return [os.path.realpath(d) for d in (home, runtime, "/tmp", "/dev/shm", *hide_extra) if os.path.isdir(d)]


def make_cfg(agent_dir, cwd, factory_home, db_dir, real_claude, rw_cwd, *, home, runtime, hide_extra=(), research_root=None, go=None):
    """The nsbox allow-list cfg for one worker. All paths are resolved here, in HQ, never inside."""
    home = os.path.realpath(home)
    factory_home = os.path.realpath(factory_home)
    db_dir = os.path.realpath(db_dir)
    agent_dir = str(agent_dir)
    hide = _hide_trees(home, runtime, hide_extra)
    want = _refuse_cwd(cwd, home, factory_home, agent_dir, hide, research_root)
    claude = os.path.abspath(real_claude)
    install = os.path.realpath(claude)
    ro = [factory_home, f"{agent_dir}/bin", f"{agent_dir}/gitconfig"]
    share = f"{home}/.local/share/claude"
    if _under(install, share) and os.path.isdir(os.path.dirname(install)):
        ro.append(os.path.dirname(install))
    rw = [db_dir, f"{agent_dir}/claude"]
    if go:  # toolchain and module cache read-only (outside home they are already visible); one rw dir per agent
        for d in (go["root"], go["modcache"]):
            if d and _under(d, home):
                ro.append(d)
        rw.append(go["dir"])
    (rw if rw_cwd else ro).append(want)
    masks = [f"{factory_home}/worktrees"] if os.path.isdir(f"{factory_home}/worktrees") else []
    cfg = nsbox.make_cfg(f"{agent_dir}/claude", claude, home, mode="allow", net=True, pid=False, keep_env=KEEP_ENV,
                         hide_dirs=hide, hide_files=(), rw=rw, ro=ro, masks=masks, cwd=want,
                         tmpfs_sizes={os.path.realpath("/tmp"): TMP_SIZE, home: HOME_SIZE})
    # the factory CLI is a symlink under the hidden home: list the link in ro, like the claude link.
    # make_cfg refuses symlinks in ro (it would bind a dangling link), so it is appended here. Inside, the link is
    # recreated in the writable home tmpfs (not bound); the host link is untouched.
    link = f"{home}/.local/bin/factory"
    if os.path.islink(link):
        cfg["ro"].append(link)
    # worker-guard reads the owner's scrub terms (SCRUB_TERMS_FILE, default this path) and fails closed without them:
    # bind exactly that one file read-only when it exists. Never its directory (it holds the tokens).
    terms = f"{home}/.config/factory/{TERMS_NAME}"
    if os.path.islink(terms):
        raise ValueError(f"{terms} is a symlink; replace it with the regular file (the worker profile will not bind a "
                         f"link or follow it out of ~/.config/factory)")
    if os.path.isfile(terms):
        cfg["ro"].append(terms)
    return cfg


def build_env(caller_env, cfg_home, config_dir, gitconfig, oauth, gh, go_env=None):
    """The allow-list env for stage 1: WORKER_ENV_PASS and LC_* from the caller, plus the values set here."""
    env = {k: v for k, v in caller_env.items() if k in WORKER_ENV_PASS or k.startswith("LC_")}
    env.update({"HOME": cfg_home, "CLAUDE_CONFIG_DIR": config_dir, "GIT_CONFIG_GLOBAL": gitconfig,
                "DISABLE_AUTOUPDATER": "1", "CLAUDE_CODE_OAUTH_TOKEN": oauth, "GH_TOKEN": gh})
    if go_env:  # set by HQ, never passed through from the caller; goroot/bin goes last so the claude shim stays first
        env.update(go_env)
        path = env.get("PATH")
        env["PATH"] = f"{path}:{go_env['GOROOT']}/bin" if path else f"{go_env['GOROOT']}/bin:/usr/bin:/bin"
    return env


def shim_main(shim_cfg, args):
    """What <agent dir>/bin/claude does, outside the namespace: tokens (exit 3 when bad), env, exec stage 1."""
    oauth = nsbox.read_token(shim_cfg["oauth_token"])
    gh = nsbox.read_token(shim_cfg["gh_token"])
    for tok, path in ((oauth, shim_cfg["oauth_token"]), (gh, shim_cfg["gh_token"])):
        if not tok:
            nsbox._fail3(path)
    env = build_env(os.environ, shim_cfg["home"], shim_cfg["config_dir"], shim_cfg["gitconfig"], oauth, gh,
                    shim_cfg.get("go_env"))
    argv = nsbox.compose_stage1(shim_cfg["ns"], args)
    os.execve(argv[0], argv, env)


SHIM = """#!{python} -I
import json, sys
sys.path.insert(0, {repo!r})
from scout import workerbox
workerbox.shim_main(json.loads({cfg!r}), sys.argv[1:])
"""


def _atomic_write(directory, name, data, mode):
    """Temp file (O_EXCL, never a followed name) + rename over `name`: a symlink at `name` is replaced, not followed.
    A directory at `name` makes the rename fail, which is a refusal."""
    fd, tmp = tempfile.mkstemp(dir=directory, prefix=f".{name}.")
    try:
        with os.fdopen(fd, "wb") as f:
            f.write(data)
            os.fchmod(f.fileno(), mode)
        os.replace(tmp, os.path.join(directory, name))
    except OSError as e:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise ValueError(f"cannot write {os.path.join(directory, name)}: {e}; remove whatever is there and retry") from e


def _q(value):
    """A git config value in double quotes."""
    if "\n" in value or "\r" in value or "\0" in value:
        raise ValueError("git config value with a newline or NUL")
    return '"' + value.replace("\\", "\\\\").replace('"', '\\"') + '"'


def gh_helper(repo, slugs):
    """The scoped git credential helper (scout/ghcred.py, the one unsandboxed workers use): answers only for `slugs`,
    with $GH_TOKEN, which stays in the sandbox env."""
    slugs = list(slugs or ())
    if not slugs or not all(ghcred.SLUG_RE.fullmatch(s) for s in slugs):
        raise ValueError(f"worker gh repos must be OWNER/NAME slugs, got {slugs!r}; set config worker_gh_repos")
    script = os.path.join(str(repo), "scout", "ghcred.py")
    return f"!{shlex.quote(sys.executable)} -I {shlex.quote(script)} git-credential {shlex.quote(','.join(slugs))}"


def render_gitconfig(helper, name=None, email=None):
    out = ["[user]"] if name or email else []
    if name:
        out.append(f"\tname = {_q(name)}")
    if email:
        out.append(f"\temail = {_q(email)}")
    out += ["[credential]", "\tuseHttpPath = true", '[credential "https://github.com"]', f"\thelper = {_q(helper)}"]
    return "\n".join(out) + "\n"


def owner_identity(home, cwd=None):
    """user.name / user.email from the owner's global git config at generation time (None when unset).
    Resolved in `cwd` so the owner's includeIf gitdir rules apply; a missing or non-directory cwd falls back to "/"."""
    cwd = cwd if cwd and os.path.isdir(cwd) else "/"
    env = {"HOME": home, "PATH": os.environ.get("PATH", "/usr/bin:/bin"), "GIT_CONFIG_NOSYSTEM": "1"}
    vals = []
    for key in ("user.name", "user.email"):
        # --includes is required: git leaves includes off under --global, so includeIf would never match
        r = subprocess.run(["git", "config", "--global", "--includes", "--get", key], capture_output=True, text=True,
                           env=env, cwd=cwd, timeout=20)
        vals.append(r.stdout.strip() or None)
    return tuple(vals)


def _open_dir(path):
    """The directory as an fd, refusing a symlink or a foreign owner."""
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    if os.fstat(fd).st_uid != os.getuid():
        os.close(fd)
        raise ValueError(f"{path} is not owned by uid {os.getuid()}")
    return fd


def _seed_claude_json(config_dir, cwd):
    """Seed <config_dir>/.claude.json once; on later calls only add the cwd project entry when it is missing.
    The file is worker-writable: never followed (O_NOFOLLOW), a symlink / junk / non-regular file is replaced by a
    fresh seed via temp + rename (which replaces a link itself, never its target)."""
    path = os.path.join(config_dir, ".claude.json")
    cur = None
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    except FileNotFoundError:
        pass
    except OSError:  # a symlink (ELOOP) or anything else we will not read
        pass
    else:
        try:
            if stat.S_ISREG(os.fstat(fd).st_mode):  # a directory or fifo is never read
                with os.fdopen(fd, "rb", closefd=False) as f:
                    raw = f.read(CLAUDE_JSON_MAX + 1)
                    if len(raw) > CLAUDE_JSON_MAX:
                        raise OverflowError(path)
                    cur = json.loads(raw)
        except ValueError:
            cur = None
        except OverflowError:
            raise ValueError(f"{path} is larger than {CLAUDE_JSON_MAX} bytes; it was left untouched. "
                             "Inspect or trim it (or move it aside to reseed) and retry") from None
        finally:
            os.close(fd)
    if not isinstance(cur, dict) or not isinstance(cur.get("projects", {}), dict):
        cur = json.loads(json.dumps(SEED))
    else:
        have = cur["projects"].get(cwd) if "projects" in cur else None
        if isinstance(have, dict) and have.get("hasTrustDialogAccepted") is True:
            return
        # T-1291: an entry that exists without trust (Claude rewrote it, or a pre-existing stub) used to stop the seed, so
        # the dialog showed: keep its other keys, set the trust flags, touch no other project.
        if isinstance(have, dict):
            cur["projects"][cwd] = {**have, **PROJECT_SEED}
            _atomic_write(config_dir, ".claude.json", (json.dumps(cur, indent=1) + "\n").encode(), 0o600)
            return
    cur.setdefault("projects", {})[cwd] = dict(PROJECT_SEED)
    _atomic_write(config_dir, ".claude.json", (json.dumps(cur, indent=1) + "\n").encode(), 0o600)


def clone_path_refusal(path, worktrees):
    """None when `path` may carry a pre-registered trust entry, else the reason: it must lie strictly under the
    worktrees/ directory (by realpath), not be worktrees/ itself, and have no symlink in any component."""
    try:
        path, worktrees = os.path.abspath(str(path)), os.path.abspath(str(worktrees))
        if os.path.realpath(worktrees) != worktrees:
            return "worktrees/ itself is or sits behind a symlink"
        if os.path.realpath(path) != path:
            return "the clone path has a symlink component"
    except (OSError, ValueError) as e:
        return f"path unreadable: {e}"
    if path == worktrees or not path.startswith(worktrees + os.sep):
        return "the clone path is not under worktrees/"
    return None


def _read_owner_json(home):
    """(raw bytes, (ino, mtime_ns, size), parsed dict) of <home>/.claude.json; ValueError (with the reason) when it is
    missing, a symlink, not a regular file, not ours, too large, not valid JSON or not an object. Never followed."""
    path = os.path.join(home, ".claude.json")
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    except FileNotFoundError:
        raise ValueError(f"{path} is missing") from None
    except OSError as e:
        raise ValueError(f"{path} is unreadable or a symlink: {e.strerror}") from None
    try:
        st = os.fstat(fd)
        if not stat.S_ISREG(st.st_mode):
            raise ValueError(f"{path} is not a regular file")
        if st.st_uid != os.getuid():
            raise ValueError(f"{path} is not owned by uid {os.getuid()}")
        with os.fdopen(fd, "rb", closefd=False) as f:
            raw = f.read(CLAUDE_JSON_MAX + 1)
    finally:
        os.close(fd)
    if len(raw) > CLAUDE_JSON_MAX:
        raise ValueError(f"{path} is larger than {CLAUDE_JSON_MAX} bytes")
    try:
        cur = json.loads(raw)
    except ValueError:
        raise ValueError(f"{path} is not valid JSON") from None
    if not isinstance(cur, dict) or not isinstance(cur.get("projects", {}), dict):
        raise ValueError(f"{path} is not a JSON object with a projects object")
    return (st.st_ino, st.st_mtime_ns, st.st_size), cur


def _edit_owner_projects(home, key, add):
    """Add (add=True, only when missing) or remove (only when present) projects[key] in <home>/.claude.json, merged onto
    a fresh read and written via temp + rename at mode 0600. A file changed between read and write is re-read once, then
    given up. Returns "added" | "removed" | "unchanged"; ValueError with the reason when nothing may be written."""
    path = os.path.join(home, ".claude.json")
    for _ in range(2):
        ident, cur = _read_owner_json(home)
        projects = cur.get("projects", {})
        if (key in projects) == add:
            return "unchanged"
        if add:
            cur.setdefault("projects", {})[key] = dict(PROJECT_SEED)
        else:
            del projects[key]
        try:
            st = os.lstat(path)
        except OSError as e:
            raise ValueError(f"{path} vanished: {e.strerror}") from None
        if (st.st_ino, st.st_mtime_ns, st.st_size) != ident:
            continue  # a live Claude process rewrote it since our read: merge onto the new content
        _atomic_write(home, ".claude.json", (json.dumps(cur, indent=1) + "\n").encode(), 0o600)
        return "added" if add else "removed"
    raise ValueError(f"{path} changed twice while editing; the dialog may show once")


def set_clone_trust(home, worktrees, clone, add=True):
    """Pre-register (add) or drop workspace trust for one factory clone in the owner's ~/.claude.json (T-1220). Scope
    first: refused unless the clone is strictly under `worktrees` with no symlink. Returns ("added"|"removed"|"unchanged",
    None) or ("refused", reason); never raises for those, never touches another key."""
    why = clone_path_refusal(clone, worktrees)
    if why:
        return "refused", why
    try:
        return _edit_owner_projects(home, os.path.abspath(str(clone)), add), None
    except ValueError as e:
        return "refused", str(e)


def check_cwd(agent, cwd, factory_home, *, home=None, token_dir=None, workers_root=None, runtime=None, hide_extra=(), research_root=None):
    """The cwd rule of prepare_agent, alone: no side effects. Raises ValueError("cwd ...") for a refused cwd.
    HQ's preflight calls it so a refusal comes before any pane is touched."""
    check_agent_name(agent)
    home = os.path.realpath(home or nsbox.real_home())
    p = _paths(home, token_dir, workers_root)
    runtime = runtime or f"/run/user/{os.getuid()}"
    _refuse_cwd(cwd, home, os.path.realpath(factory_home), str(Path(p["workers"]).resolve() / agent),
                _hide_trees(home, runtime, hide_extra), research_root)


def prepare_agent(agent, role, cwd, factory_home, db_dir, real_claude, rw_cwd, *, home=None, token_dir=None,
                  workers_root=None, runtime=None, hide_extra=(), identity=None, repo=None, gh_token_path=None, gh_slugs=None,
                  research_root=None, go_root=None, go_modcache=None):
    """Create/refresh <workers_root>/<agent> and return it. See the module doc.

    The keyword-only args are test seams; HQ passes none of them: home defaults to the passwd home, token_dir to
    <home>/.config/factory, workers_root to <home>/.local/state/factory/workers, runtime to /run/user/<uid>.
    `hide_extra` adds tmpfs trees on top of the fixed four; `identity` is (user.name, user.email).
    `repo` pins the checkout the shim imports from (HQ passes factory_home); default: the checkout this file is in.
    `gh_token_path` is the role's token file from config gh_token_ro / gh_token_rw (default: the fixed file of the role
    in token_dir); `gh_slugs` is the OWNER/NAME list the credential helper answers for (config worker_gh_repos): without
    it there is no helper and the call is refused. `research_root` (researcher only) is the one tree under the hidden
    home a cwd may sit in; it is ignored for every other role.
    `go_root` / `go_modcache` are test seams for the Go toolchain (default ~/.local/go and the first existing module
    cache); with no usable go the sandbox is exactly as without Go."""
    if role != "researcher":
        research_root = None
    check_agent_name(agent)
    if role not in SANDBOXABLE:
        raise ValueError(f"role {role!r} is not sandboxable; one of {sorted(SANDBOXABLE)}")
    if os.path.basename(os.path.abspath(real_claude)) != "claude":
        raise ValueError(f"real_claude must be a path whose basename is claude, got {real_claude}")
    home = os.path.realpath(home or nsbox.real_home())
    p = _paths(home, token_dir, workers_root)
    runtime = runtime or f"/run/user/{os.getuid()}"
    root = Path(p["workers"])
    # refuse a bad cwd before anything is created or written
    check_cwd(agent, cwd, factory_home, home=home, token_dir=token_dir, workers_root=workers_root, runtime=runtime,
              hide_extra=hide_extra, research_root=research_root)
    root.mkdir(parents=True, exist_ok=True, mode=0o700)
    agent_dir = root.resolve() / agent
    if agent_dir.parent != root.resolve():
        raise ValueError(f"agent dir {agent_dir} escapes {root}")
    for d in (agent_dir, agent_dir / "bin"):
        d.mkdir(mode=0o700, exist_ok=True)
        if d.is_symlink() or d.stat().st_uid != os.getuid():
            raise ValueError(f"{d} must be a real directory owned by uid {os.getuid()}")
    repo = Path(repo).resolve() if repo else Path(__file__).resolve().parent.parent
    helper = gh_helper(repo, gh_slugs)
    name, email = identity if identity is not None else owner_identity(home, cwd)
    _atomic_write(str(agent_dir), "gitconfig", render_gitconfig(helper, name, email).encode(), 0o644)  # before make_cfg: it refuses a symlink there
    go = go_paths(home, agent_dir, go_root, go_modcache)
    if go:  # worker-writable: refuse a symlink or foreign owner before anything is created below it
        for rel in (GO_DIR, f"{GO_DIR}/cache", f"{GO_DIR}/tmp"):
            d = agent_dir / rel
            try:
                d.mkdir(mode=0o700, exist_ok=True)  # a dangling symlink raises FileExistsError
                os.close(_open_dir(str(d)))
            except OSError as e:
                raise ValueError(f"{d} must be a real directory owned by uid {os.getuid()} ({e.strerror}); remove it") from e
    cfg = make_cfg(agent_dir, cwd, factory_home, db_dir, real_claude, rw_cwd, home=home, runtime=runtime,
                   hide_extra=hide_extra, research_root=research_root, go=go)
    config_dir = agent_dir / "claude"
    config_dir.mkdir(mode=0o700, exist_ok=True)
    cfd = _open_dir(str(config_dir))  # symlink / foreign owner => refuse, before anything is written below it
    os.close(cfd)
    shim_cfg = {"ns": cfg, "home": home, "config_dir": str(config_dir), "gitconfig": f"{agent_dir}/gitconfig",
                "oauth_token": p["oauth"], "gh_token": gh_token_path or p[_GH_KEY[role]],
                "role": role}
    if go:
        shim_cfg["go_env"] = go_env_for(go)
    shim = SHIM.format(python=sys.executable, repo=str(repo), cfg=json.dumps(shim_cfg))
    _atomic_write(str(agent_dir / "bin"), "claude", shim.encode(), 0o755)
    _seed_claude_json(str(config_dir), cfg["cwd"])
    return agent_dir
