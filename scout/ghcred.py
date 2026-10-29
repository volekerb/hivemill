"""Worker GitHub credential (G-010, owner rule R-c): a scoped git credential helper and a gh shim, fail closed.

SECURITY CODE. A worker's `git push` and `gh` calls must use a worker token file, never the owner's
~/.config/gh/hosts.yml or the owner's git credential helper. `prepare_dir` runs in HQ and writes
<root>/<agent>/{gitconfig, bin/gh, gh/}: paths and commands only, no secret. `pane_env` is what the pane adds to its
env (paths only). The helper and the shim read the token at CALL time, so a token swap is a file change.

    ghcred.py git-credential <slug,...> get|store|erase    git credential helper (stdin: key=value lines)
    ghcred.py gh-shim <real_gh> <gh_config_dir> [args...]  exec the real gh with the worker token

Any failure answers with one stderr line that names no token, nothing on stdout, and a nonzero exit, so git and gh
fail instead of falling back to another credential. Stdlib only: it runs as a script under `python -I`.
"""
import os
import pwd
import re
import shlex
import stat
import subprocess
import sys

NO_TOKEN_MSG = "worker gh token unavailable"
HOST = "github.com"
USER = "x-access-token"
SHIM_EXIT = 4
SLUG_RE = re.compile(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+")
NAME_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9_-]{0,63}")
SELF = os.path.abspath(__file__)


def real_home():
    return pwd.getpwuid(os.getuid()).pw_dir


def read_token_file(path):
    """The token in `path`, or None. Regular file, mode 0600, own uid, never through a symlink, non-empty."""
    if not path:
        return None
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    except OSError:
        return None
    with os.fdopen(fd) as f:
        st = os.fstat(f.fileno())
        if not stat.S_ISREG(st.st_mode) or stat.S_IMODE(st.st_mode) != 0o600 or st.st_uid != os.getuid():
            return None
        try:
            tok = f.read().strip()
        except (OSError, UnicodeDecodeError):
            return None
        return tok if tok and tok.isprintable() and not any(c.isspace() for c in tok) else None


def worker_token(env=None):
    """$GH_TOKEN when set (the sandbox case), else the file named by $FACTORY_GH_TOKEN_FILE; None when neither works."""
    env = os.environ if env is None else env
    tok = env.get("GH_TOKEN", "").strip()
    if tok:
        return tok if tok.isprintable() and not any(c.isspace() for c in tok) else None
    return read_token_file(env.get("FACTORY_GH_TOKEN_FILE"))


def _parse_slugs(arg):
    slugs = [s for s in arg.split(",") if s]
    if not slugs or not all(SLUG_RE.fullmatch(s) for s in slugs):
        return None
    return slugs


def credential_request(lines):
    req = {}
    for line in lines:
        k, sep, v = line.rstrip("\n").partition("=")
        if sep:
            req[k] = v
    return req


def _unavailable():
    print(f"{NO_TOKEN_MSG}: check the worker token file named in the factory config", file=sys.stderr)
    return 1


def git_credential(slug_arg, action, stdin, stdout):
    """Exit code of one helper call. store / erase are no-ops; get answers only for an allowed repo."""
    if action in ("store", "erase"):
        return 0
    if action != "get":
        print(f"ghcred: unknown action {action!r}", file=sys.stderr)
        return 1
    slugs = _parse_slugs(slug_arg)
    req = credential_request(stdin)
    path = req.get("path", "")
    allowed = (slugs is not None and req.get("protocol") == "https" and req.get("host") == HOST
               and any(path in (s, s + ".git") for s in slugs))
    if not allowed:
        print("ghcred: no worker credential for this host or repository", file=sys.stderr)
        return 1
    token = worker_token()
    if token is None:
        return _unavailable()
    stdout.write(f"username={USER}\npassword={token}\n")
    return 0


def gh_shim(real_gh, config_dir, args):
    """Exec the real gh with the worker token; SHIM_EXIT without running it when the token is unavailable."""
    token = worker_token()
    if token is None:
        print(f"{NO_TOKEN_MSG}: gh not run", file=sys.stderr)
        return SHIM_EXIT
    env = {k: v for k, v in os.environ.items() if k not in ("GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN")}
    env.update(GH_TOKEN=token, GH_CONFIG_DIR=config_dir, GH_PROMPT_DISABLED="1")
    try:
        os.execve(real_gh, [real_gh, *args], env)
    except OSError as e:
        print(f"ghcred: cannot run the real gh ({e.strerror}); re-run the worker gh setup", file=sys.stderr)
    return SHIM_EXIT


def check_token_path(token_path, home):
    """The resolved token path; ValueError unless it is a regular file directly in <home>/.config/factory."""
    cfg_dir = os.path.realpath(os.path.join(home, ".config", "factory"))
    real = os.path.realpath(token_path)
    if os.path.dirname(real) != cfg_dir or not os.path.isfile(real) or os.path.islink(token_path):
        raise ValueError(f"worker gh token must be a regular file directly in {cfg_dir}, not {token_path}")
    return real


def _git_value(home, key):
    env = {"HOME": home, "PATH": os.environ.get("PATH", "/usr/bin:/bin"), "GIT_CONFIG_NOSYSTEM": "1"}
    r = subprocess.run(["git", "config", "--global", "--get", key], capture_output=True, text=True, env=env,
                       cwd="/", timeout=20)
    return r.stdout.strip() or None


def _gq(value):
    """A git-config double-quoted value."""
    return '"' + value.replace("\\", "\\\\").replace('"', '\\"') + '"'


def _write_atomic(dir_fd, name, data, mode):
    tmp = f".{name}.{os.getpid()}.tmp"
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode, dir_fd=dir_fd)
    try:
        with os.fdopen(fd, "w") as f:
            f.write(data)
        os.chmod(tmp, mode, dir_fd=dir_fd)
        os.rename(tmp, name, src_dir_fd=dir_fd, dst_dir_fd=dir_fd)
    except BaseException:
        try:
            os.unlink(tmp, dir_fd=dir_fd)
        except OSError:
            pass
        raise


def _open_dir(path, parent_fd=None):
    """Create (0700) if needed and open `path` as a directory fd; a symlink or foreign owner is refused."""
    try:
        os.mkdir(path, 0o700, dir_fd=parent_fd)
    except FileExistsError:
        pass
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent_fd)
    if os.fstat(fd).st_uid != os.getuid():
        os.close(fd)
        raise ValueError(f"{path} is not owned by uid {os.getuid()}")
    os.fchmod(fd, 0o700)
    return fd


def prepare_dir(agent, role, token_path, slugs, real_gh, home=None, root=None):
    """Write the per-agent dir and return its path. `role` is for the caller's records; nothing here depends on it."""
    if not NAME_RE.fullmatch(agent):
        raise ValueError(f"bad agent name {agent!r}")
    slugs = list(slugs)
    if not slugs or not all(SLUG_RE.fullmatch(s) for s in slugs):
        raise ValueError(f"worker gh repos must be OWNER/NAME slugs, got {slugs!r}")
    if not os.path.isabs(real_gh):
        raise ValueError(f"real gh must be an absolute path, got {real_gh!r}")
    home = home or real_home()
    check_token_path(token_path, home)
    root = root or os.path.join(home, ".local", "state", "factory", "ghcred")
    os.makedirs(root, mode=0o700, exist_ok=True)
    agent_dir = os.path.join(root, agent)
    helper = f"!{shlex.quote(sys.executable)} -I {shlex.quote(SELF)} git-credential {shlex.quote(','.join(slugs))}"
    lines = ["[user]"]
    for key, val in (("name", _git_value(home, "user.name")), ("email", _git_value(home, "user.email"))):
        if val:
            lines.append(f"\t{key} = {_gq(val)}")
    lines += ["[credential]", "\tuseHttpPath = true", f'[credential "https://{HOST}"]', f"\thelper = {_gq(helper)}", ""]
    shim = ("#!/bin/sh\n"
            f"exec {shlex.quote(sys.executable)} -I {shlex.quote(SELF)} gh-shim {shlex.quote(real_gh)} "
            f"{shlex.quote(os.path.join(agent_dir, 'gh'))} \"$@\"\n")
    root_fd = os.open(root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        dir_fd = _open_dir(agent, root_fd)
    finally:
        os.close(root_fd)
    try:
        _write_atomic(dir_fd, "gitconfig", "\n".join(lines), 0o600)
        bin_fd = _open_dir("bin", dir_fd)
        try:
            _write_atomic(bin_fd, "gh", shim, 0o700)
        finally:
            os.close(bin_fd)
        os.close(_open_dir("gh", dir_fd))
    finally:
        os.close(dir_fd)
    return agent_dir


def pane_env(agent_dir, token_path):
    """The env a pane adds: paths only, never a token."""
    return {
        "PATH": f"{os.path.join(agent_dir, 'bin')}:{os.environ.get('PATH', '/usr/bin:/bin')}",
        "GIT_CONFIG_GLOBAL": os.path.join(agent_dir, "gitconfig"),
        "GIT_CONFIG_NOSYSTEM": "1",
        "GIT_TERMINAL_PROMPT": "0",
        "GH_CONFIG_DIR": os.path.join(agent_dir, "gh"),
        "FACTORY_GH_TOKEN_FILE": str(token_path),
    }


def main(argv):
    if len(argv) == 4 and argv[1] == "git-credential":
        return git_credential(argv[2], argv[3], sys.stdin, sys.stdout)
    if len(argv) >= 4 and argv[1] == "gh-shim":
        return gh_shim(argv[2], argv[3], argv[4:])
    print("usage: ghcred.py git-credential <slug,...> get|store|erase | gh-shim <real_gh> <config_dir> [args...]",
          file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv))
