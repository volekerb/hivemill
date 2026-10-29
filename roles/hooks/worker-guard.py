#!/usr/bin/env python3
"""PreToolUse hook (Bash) for worker roles: refuse direct herdr use and factory/herdr unit management.

Incident M86: a coder ran `HOME=<tmp> herdr server stop` and stopped the live herdr server; an env-var
prefix defeats the prefix-matched `Bash(herdr server stop:*)` deny rules. This hook parses the command
instead of matching a prefix.

Protocol: tool-call JSON on stdin. Exit 0 = allow. Exit 2 + stderr reason = block. Any parse problem or
internal error also exits 2 (fail closed); a plain crash would exit 1, which Claude Code treats as allow.

Rule (see README "Worker guard"):
  * A command word is the first word of a simple command after env assignments, redirections and
    shell keywords. Simple commands are split on ; & && || | newline ( ) { } and found inside
    $( ), backticks, `sh -c` / `eval` payloads and the bodies of shell heredocs / shell scripts
    that are read from disk.
  * Block when a command word is `herdr` or a path whose basename is `herdr`, unless that path is
    under a tests/stubs/ directory (the unittest stubs). Block when a command word is dynamic ($VAR,
    glob that matches herdr, brace expansion) since it could expand to herdr.
  * Wrappers (sudo, env, exec, nohup, timeout, xargs, command, busybox, ssh, watch, ...) are checked at
    every later position, so `sudo -u x env A=1 herdr ...` is caught without modelling each option.
  * `herdr` as a plain argument (grep herdr, echo herdr, cat tests/stubs/herdr) is allowed.
  * Same for `claude` (any path with basename claude), at every position; plus npx/bunx/pnpm dlx of
    @anthropic-ai/claude-code and node running claude-code/.../cli.js. `claude` as an argument is fine.
    Reason: only factory spawn starts Claude (lesson M347). No stub exemption.
  * systemctl with a mutating verb on factory-*, herdr* or no unit (daemon-reload) is blocked.
Defence in depth: this parser is layer 1. Layer 2 is a raw-text backstop that runs first, before any
parsing: R1 blocks a line (backslash-continuations joined) that has the word herdr, later a mutating
sequence (`server <word>`, or agent|pane|tab|workspace|worktree start|stop|kill|...), AND an exec-capable
launcher in a command position (find -exec/-execdir/-ok, xargs, env, sudo, nohup, timeout, command, exec,
nice, ionice, setsid, stdbuf, sh/bash -c). Without a launcher the phrase is plain text (grep, git commit -m,
gh comment) and is allowed. R2 blocks re-targeting HERDR_SOCKET_PATH/HERDR_SESSION and naming herdr.sock or
.config/herdr (quoted data in git/gh/grep/rg/cat/less/echo/printf/factory is fine). Worker mode also blocks
pkill/killall and any kill that is not `%job` or `$!` (T-0102, G-008 M1932). R3 blocks copying or
reading the real herdr binary. Layer 3 (bin/factory herdr() pins the socket; worker settings point
HERDR_SOCKET_PATH at a dead path) lives outside this file.
Factory unit commands: a command word `factory` (or a path with that basename, also `python3 <path>/factory`)
whose subcommand is up|tick|relaunch|spawn|clean is blocked, except (a) when the same simple command assigns
FACTORY_HOME=<path> to a path that is not the live home (a temp home for QA), (b) `clean` for `--role cleaner`.
Broad reads (res, agents, scorecard, digest, config, goal list, goal show <not own goal>, task list|ready, team list,
project list) follow the same temp-home rule (temp_home or agent_temp_home), are exempt for --role staff-engineer,
and `agents` for --role cleaner. Own goal = agents.goal_id of $FACTORY_AGENT in the factory DB: $FACTORY_HOME/db/factory.db, else the legacy $FACTORY_HOME/factory.db (read-only,
fail closed).
The role comes from the argv flag `--role <role>`; without it the hook acts as a non-cleaner worker.
Identity belt (T-0036, E1/E2; the real E1 fix is a design decision with the owner, see README "Known gaps"):
the command word `setsid` (also as a wrapper at any position); `disown` in a command line that runs factory;
`unset` / `env -u` of FACTORY_ROLE or HERDR_PANE_ID; `env -i` in a command line that runs factory; an
assignment FACTORY_AGENT=<v> (also export/declare) unless <v> is the worker's own name (env FACTORY_AGENT at
hook time) or the same simple command assigns a temp FACTORY_HOME (static path, or $(mktemp [-d] [-p dir]),
outside the live home and ~/factory). FACTORY_ROLE= / HERDR_PANE_ID= assignments follow the temp-home rule only.
Known gaps are listed in the README ("Known gaps"), tracked in T-0021.
PRIV (T-0057, owner M803): a command word sudo/su/doas/pkexec/docker (or a path with that basename) is blocked
for EVERY agent, through the same forms the herdr rule follows (env prefix, wrappers, find -exec, sh -c,
interpreter heredocs and inline code, $( ), backticks, chains, subshells). As a plain argument the word is data.
`--hq` (boss, director, lead) runs ONLY PRIV: none of the herdr/factory-unit/broad-read/identity rules, and a
dynamic command word is not blocked unless a $( ) command word names a PRIV word literally. Parse trouble in HQ mode
allows (no false positives); worker mode keeps failing closed.
Cleaner branch deletion (T-0057): `git branch -d/-D/--delete` and `git push <remote> --delete|-d|:<ref>` are blocked
for every worker role; --role cleaner may do them only when every target matches ^factory/[A-Za-z0-9._/-]+$, and a
cleaner's other `git push` forms are blocked (the settings no longer deny `git push:*` for it).
"""
import fnmatch
import json
import os
import pwd
import re
import shlex
import sqlite3
import stat
import sys

MAX_DEPTH = 6
FACTORY_UNIT_CMDS = {"up", "tick", "relaunch", "spawn", "clean"}
# broad reads of the live home: subcommand -> second words that are blocked (None = any / no second word)
BROAD_READS = {"res": None, "agents": None, "scorecard": None, "digest": None, "config": None,
               "goal": {"list"}, "task": {"list", "ready"}, "team": {"list"},
               "project": {"list"}, "lesson": {"list"}}
PRIV_WORDS = {"sudo", "su", "doas", "pkexec", "docker"}
FACTORY_BRANCH = re.compile(r"^factory/[A-Za-z0-9._/-]+$")
# wrappers whose options are modelled, so PRIV sees the command they run and not their option values:
# name -> options that take a value (as a separate word)
WRAP_VALUE_OPTS = {
    "env": {"-u", "-C", "--unset", "--chdir"}, "exec": {"-a"}, "nohup": set(), "command": set(), "builtin": set(),
    "setsid": set(), "timeout": {"-s", "-k", "--signal", "--kill-after"}, "nice": {"-n", "--adjustment"},
    "ionice": {"-c", "-n", "-p", "-P", "-u", "--class", "--classdata"}, "stdbuf": {"-i", "-o", "-e"},
    "xargs": {"-I", "-J", "-n", "-P", "-d", "-L", "-l", "-s", "-E", "-a", "-R", "-S", "--max-args", "--max-procs",
              "--delimiter", "--max-lines", "--max-chars", "--arg-file", "--eof", "--process-slot-var"},
    "watch": {"-n", "--interval", "-w"}, "busybox": set(),
}
# wrappers that only copy/link/alias their arguments: a PRIV word after them is data
WRAP_DATA = {"alias", "ln", "cp", "mv", "install", "rsync", "nc", "ncat", "netcat", "socat"}
# a PRIV word inside interpreter code (-c/-e text, heredoc body)
PRIV_CODE = re.compile(r"(?<![\w./-])(?:sudo|doas|pkexec|docker)(?![\w-])|\bsu\b(?=\s+-|\s+root\b)|[\"']su[\"']")
# per-interpreter option model (name -> spec): value = options whose value is the next word (or attached),
# code = options whose value is inline code, flags = option letters that take nothing. An option the model does not
# know makes the script position unknown: callers then fail closed (every later word is a candidate).
PY_FLAGS = set("bBdEhiIOPqsSuvVx?")
INTERP_OPTS = {
    "perl": {"value": {"-I"}, "code": set("eE")},
    "ruby": {"value": {"-r", "-I", "-C", "-E", "-F"}, "code": set("e")},
    "lua": {"value": {"-l"}, "code": set("e")},
    "php": {"value": {"-d", "-c", "-z", "-f", "-F"}, "code": set("rRBE")},
    "node": {"value": {"-r", "--require", "--import", "--loader", "--experimental-loader", "--input-type",
                       "--stack-size"}, "code": set("ep"), "long_code": {"--eval", "--print"}},
}


def derive_live_homes(uid=None):
    """(primary, homes): the live factory home(s). DUPLICATE of derive_live_homes() in bin/factory (this hook must
    not import it); keep the two identical. <pw_dir>/factory is always live; <pw_dir>/.config/factory/live_home
    can only ADD one (an invalid file is ignored). Never read from $HOME or any env var: a worker sets those."""
    uid = os.getuid() if uid is None else uid
    base = pwd.getpwuid(uid).pw_dir
    default = os.path.realpath(os.path.join(base, "factory"))
    homes, primary = {default}, default
    cfgdir = os.path.join(base, ".config", "factory")
    path = os.path.join(cfgdir, "live_home")
    try:
        st, pst = os.lstat(path), os.lstat(cfgdir)
        if (stat.S_ISREG(st.st_mode) and stat.S_ISDIR(pst.st_mode) and st.st_uid == uid
                and not st.st_mode & (stat.S_IWGRP | stat.S_IWOTH)):
            with open(path) as f:
                lines = f.read().splitlines()
            if len(lines) == 1 and os.path.isabs(lines[0]) and "\0" not in lines[0]:
                primary = os.path.realpath(lines[0])
                homes.add(primary)
    except (OSError, UnicodeDecodeError):
        pass  # no file (the usual case) or unreadable: only the default is live
    return primary, homes


LIVE_HOME, LIVE_HOMES = derive_live_homes()
PW_DIR = pwd.getpwuid(os.getuid()).pw_dir
CFG_DIR = os.path.realpath(os.path.join(PW_DIR, ".config", "factory"))  # holds live_home (see derive_live_homes)
# best-effort write detector for CFG_DIR (T-0212): the real guarantee is that the file can only ADD a live home
CFG_WRITERS = {"touch", "mkdir", "rmdir", "rm", "unlink", "shred", "truncate", "mv", "ln", "cp", "install", "tee",
               "chmod", "chown", "chgrp", "chattr", "setfacl", "rsync", "dd", "patch", "mkfifo", "mknod"}
CFG_REMOVERS = {"rm", "rmdir", "unlink", "shred"}  # also hit when they name an ancestor of CFG_DIR (rm -r ~/.config)
# T-0334: files and trees whose content later runs as the box owner (git config keys such as core.fsmonitor, shell init
# files): a worker never writes them; the owner does. Both the spelled and the resolved path count.
PERSIST_NAMES = (".gitconfig", ".bashrc", ".zshrc", ".zshenv", ".profile", ".bash_profile", ".zprofile")
PERSIST_FILES = {p for n in PERSIST_NAMES for p in (os.path.join(PW_DIR, n), os.path.realpath(os.path.join(PW_DIR, n)))} | {"/etc/gitconfig"}
PERSIST_TREES = {os.path.join(PW_DIR, ".config", "git"), os.path.realpath(os.path.join(PW_DIR, ".config", "git"))}
PERSIST_ANCESTORS = {os.path.join(PW_DIR, ".config"), os.path.realpath(os.path.join(PW_DIR, ".config"))}  # ln/mv onto it redirects ~/.config/git
PERSIST_CODE = re.compile(r"(?<![\w-])\.(?:gitconfig|bashrc|zshrc|zshenv|profile|bash_profile|zprofile)(?![\w.-])"
                          r"|\.config/git(?![\w-])|/etc/gitconfig")
CFG_ENV_ASSIGN = re.compile(r"(?<![\w])(?:GIT_CONFIG\w*|HOME|XDG_CONFIG_HOME)=")
PERSIST_EDIT_TOOLS = {"Edit", "Write", "MultiEdit", "NotebookEdit"}
REMOTE_WRITE = {"add", "set-url", "rename", "remove", "rm", "set-head", "set-branches"}
CONFIG_VALUE_OPTS = {"--type", "--default", "--comment", "--blob"}
PERSIST_WHY = ("it would persist config into the live box (git config keys like core.fsmonitor or diff.external, and "
               "shell init files, run later as the tick and other agents); only the owner changes ~/.gitconfig, "
               "~/.config/git and shell rc files")


def brace_expand(t):
    m = re.search(r"\{([^{}]*,[^{}]*)\}", t)
    if not m:
        return [t]
    return [x for alt in m.group(1).split(",") for x in brace_expand(t[:m.start()] + alt + t[m.end():])]


REPO_CFG_WHY = ("every worktree shares the repo's .git/config, so a key such as filter.<x>.smudge, diff.<x>.textconv, "
                "core.hooksPath, core.fsmonitor or core.sshCommand written from a worktree would run as every agent's git; "
                "use `git config --file <file in your scratchpad>` or a clone inside your scratchpad. This guard is "
                "accident prevention, not a security boundary (T-0226)")


def repo_cfg_path(c):
    """T-0226: True when normalised absolute path `c` is repo git config outside the worker's scratchpad: <x>/.git/config*,
    <x>/.git/worktrees/<n>/config*, <x>/.git/info/attributes, or the same files of a bare repo (HEAD and objects/ beside)."""
    if in_scratchpad(c):
        return False
    parts = c.split(os.sep)
    base, parent = parts[-1], parts[-2] if len(parts) > 1 else ""
    if base.startswith("config") and (parent == ".git" or (len(parts) > 3 and parts[-3] == "worktrees" and parts[-4] == ".git")):
        return True
    if base == "attributes" and parent == "info" and len(parts) > 2 and parts[-3] == ".git":
        return True
    if base in ("config", "config.worktree"):
        gd = os.sep.join(parts[:-1])
    elif base == "attributes" and parent == "info":
        gd = os.sep.join(parts[:-2])
    else:
        return False
    return bool(gd) and os.path.isfile(os.path.join(gd, "HEAD")) and os.path.isdir(os.path.join(gd, "objects"))


def persist_path(p, ancestor=False):
    """True when path `p` (absolute; `..` and symlinks resolved, glob patterns matched) is a protected file or inside a
    protected tree."""
    for t in brace_expand(p):
        cands = {os.path.normpath(t)}
        if not GLOB.search(t):
            cands.add(os.path.realpath(t))
        for c in cands:
            if ancestor and c in PERSIST_ANCESTORS:
                return True
            if repo_cfg_path(c):
                return True
            if GLOB.search(c):
                parts = c.strip("/").split("/")
                if any(fnmatch.fnmatchcase(f, c) for f in PERSIST_FILES) or any(
                        len(parts) >= len(d.strip("/").split("/")) and all(
                            fnmatch.fnmatchcase(x, y) for x, y in zip(d.strip("/").split("/"), parts))
                        for d in PERSIST_TREES):
                    return True
            elif c in PERSIST_FILES or any(c == d or under(c, d) for d in PERSIST_TREES):
                return True
    return False


def persist_code(text):
    return bool(PERSIST_CODE.search(text) or PERSIST_CODE.search(re.sub(r"['\"+]", "", text)))


# the rendered settings of the live home(s): a Bash write there re-points the next spawn's deny rules and hook
RUN_SETTINGS = {os.path.realpath(os.path.join(h, "run", "settings")) for h in LIVE_HOMES}
CFG_CODE = re.compile(r"\.config/factory|run/settings|(?<![A-Za-z0-9_])live_home(?![A-Za-z0-9_])")
HOOK_REAL = os.path.realpath(__file__)
SHELLS = {"sh", "bash", "zsh", "dash", "ksh", "ash", "fish", "csh", "tcsh", "rbash"}
INTERPRETERS = re.compile(r"^(python[\d.]*|perl|ruby|node|nodejs|php|lua|deno|bun)$")
# run another command: every later position is checked as a possible command word
WRAPPERS = {
    "sudo", "doas", "pkexec", "su", "runuser", "env", "exec", "nohup", "timeout", "xargs", "command",
    "builtin", "busybox", "nice", "ionice", "stdbuf", "setsid", "chrt", "taskset", "setpriv", "unshare",
    "nsenter", "chroot", "flock", "watch", "script", "unbuffer", "ssh", "tmux", "screen", "parallel", "at",
    "systemd-run", "strace", "ltrace", "gdb", "valgrind", "perf", "fakeroot", "bwrap", "firejail",
    "xvfb-run", "dbus-run-session", "cgexec", "proot", "expect", "alias", "ln", "cp", "mv", "install",
    "rsync", "nc", "ncat", "netcat", "socat",
}
RESERVED = {"if", "then", "else", "elif", "fi", "do", "done", "while", "until", "time", "!", "{", "}",
            "coproc", "function"}
SYSTEMCTL_VERBS = {"enable", "start", "stop", "restart", "reload", "daemon-reload", "daemon-reexec", "disable",
                   "kill", "mask", "unmask", "link", "reenable", "preset", "try-restart", "reload-or-restart",
                   "try-reload-or-restart", "isolate", "edit", "set-property", "revert"}
UNIT_MUTATING = {"start", "stop", "restart", "reload", "try-restart", "reload-or-restart", "enable", "disable",
                 "reenable", "daemon-reload", "daemon-reexec", "edit", "mask", "unmask", "set-property", "revert",
                 "link", "preset", "kill", "reset-failed", "isolate", "set-environment", "unset-environment",
                 "import-environment", "try-reload-or-restart", "preset-all", "add-wants", "add-requires"}
SYSTEMCTL_NOUNIT = {"daemon-reload", "daemon-reexec"}
ASSIGN = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*\+?=")
GLOB = re.compile(r"[*?\[]")
COPIERS = {"cat", "dd", "cp", "install", "rsync", "tee", "ln", "mv", "base64", "xxd", "head", "tail"}
# R1: herdr word, then (same line, any tokens between) a mutating sequence; only fires together with an
# exec-capable launcher in a command position on that line (see launcher_in_command_position)
R1 = re.compile(
    r"(?<!tests/stubs/)\bherdr\b.*?"
    r"(?:\bserver\s+\S|\b(?:agent|pane|tab|workspace|worktree)[\"'\\\s]+"
    r"(?:start|stop|kill|close|prompt|split|new|run|send|create|move|swap|remove|rename)\b)")
# R2: re-targeting the socket/session, or pointing at herdr's config dir / socket file
R2_SET = re.compile(r"\bHERDR_(?:SOCKET_PATH|SESSION)\s*\+?=|\bexport\s+(?:-\w+\s+)*HERDR_(?:SOCKET_PATH|SESSION)\b"
                    r"|\b(?:declare|typeset)\s+-\w*x\w*\s+HERDR_(?:SOCKET_PATH|SESSION)\b")
R2_PATH = re.compile(r"herdr\.sock|\.config/herdr")
# commands that only print/search/record text: a quoted herdr.sock / .config/herdr there is data, not a target
DATA_CMDS = {"git", "gh", "grep", "rg", "cat", "less", "echo", "printf", "factory"}
LAUNCHERS = {"env", "sudo", "nohup", "timeout", "command", "exec", "nice", "ionice", "setsid", "stdbuf", "xargs"}
FIND_EXEC = {"-exec", "-execdir", "-ok", "-okdir"}
R2_UNSET = re.compile(r"\bunset\b[^\n]*\bHERDR_SOCKET_PATH\b|\benv\b[^\n]*\s(?:-u\s*HERDR_SOCKET_PATH|-[A-Za-z]*i[A-Za-z]*|--ignore-environment|--unset[= ]HERDR_SOCKET_PATH)(?:\s|$)")
R3_REDIR = re.compile(r"<\s*[\"']?(?!\S*tests/stubs/)\S*(?:/herdr\b|\$\{?HERDR_BIN_PATH)")
HERDR_WORD = re.compile(r"(?<!tests/stubs/)\bherdr\b")
AWKS = {"awk", "gawk", "mawk", "nawk", "busybox-awk"}
EDITORS = {"vim", "vi", "nvim", "view", "ex", "emacs", "nano", "ed", "less", "more", "man"}
SHELL_LIKE = SHELLS | {"source", ".", "eval"}
IDENT_VARS = ("FACTORY_AGENT", "FACTORY_ROLE", "HERDR_PANE_ID")
SCRUB_VARS = {"FACTORY_ROLE", "HERDR_PANE_ID"}
DECLARERS = {"export", "declare", "typeset", "readonly", "local"}


GIT_VALUE_OPTS = {"-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path", "--super-prefix",
                  "--config-env"}
GIT_READ_ONLY = {"status", "log", "diff", "show", "rev-parse", "ls-files", "ls-tree", "cat-file", "blame", "grep",
                 "describe", "fetch", "shortlog", "rev-list", "show-ref", "merge-base", "ls-remote",
                 "for-each-ref", "diff-tree", "diff-index", "name-rev", "version", "help"}
BRANCH_LIST_LONG = {"--list", "--all", "--remotes"}
BRANCH_PLAIN_LONG = {"--show-current", "--verbose", "--ignore-case", "--color", "--no-color", "--column",
                     "--no-column", "--abbrev", "--no-abbrev", "--quiet"}
BRANCH_VALUE_LONG = {"--contains", "--no-contains", "--merged", "--no-merged", "--points-at", "--sort", "--format"}
CONFIG_READ = {"--get", "--get-all", "--get-regexp", "--get-urlmatch", "--list", "-l", "--show-origin",
               "--show-scope", "--name-only"}
CONFIG_WRITE = {"--unset", "--unset-all", "--add", "--replace-all", "--edit", "-e", "--rename-section",
                "--remove-section"}
SHARED_ADVICE = ("workers never change the shared checkout ~/factory (branch main); work in your own worktree "
                 "(your cwd under ~/factory/worktrees/)")


class Block(Exception):
    pass


class PrivBlock(Block):
    pass


class GitBlock(Block):
    pass


class UnitBlock(PrivBlock):
    """Mutating systemctl (HQ mode) or systemd-run (every mode), T-0102 part D, director M2108."""


def priv_word(w, globs=False):
    """The PRIV word a command word is (basename sudo/su/doas/pkexec/docker), else None. With globs (worker
    mode) a glob basename that may expand to a PRIV word counts too (`/usr/bin/s?do`), as is_herdr does."""
    base = os.path.basename(w.rstrip("/"))
    if base in PRIV_WORDS:
        return base
    if globs and base not in ("[", "[[") and GLOB.search(base):
        return next((p for p in sorted(PRIV_WORDS) if fnmatch.fnmatchcase(p, base)), None)
    return None


def priv_subst(w):
    """The PRIV word named literally inside a $( ) / backtick command word, else None."""
    if not (w.startswith("$(") or w.startswith("`")):
        return None
    m = re.search(r"(?<![\w.-])(sudo|su|doas|pkexec|docker)(?![\w-])", w)
    return m.group(1) if m else None


def priv_in_code(text):
    """The PRIV word named in interpreter code (inline -c/-e text or a heredoc body), else None."""
    m = PRIV_CODE.search(text)
    return re.search(r"sudo|doas|pkexec|docker|su", m.group(0)).group(0) if m else None


def interp_args(name, rest):
    """(script_index, code_index, unknown) in rest for interpreter `name`. Option values are skipped, so
    `python3 -W ignore -c CODE` and `python3 -X dev SCRIPT` find the real positions. An option that is not
    modelled sets unknown: the position cannot be trusted, so callers consider every later word."""
    name = re.sub(r"[\d.]+$", "", name)
    i = 0
    while i < len(rest):
        a = rest[i]
        if a == "--":
            return (i + 1 if i + 1 < len(rest) else None), None, False
        if name not in INTERP_OPTS and name != "python":  # deno, bun, ...: subcommand-based, nothing modelled
            return None, None, True
        if not a.startswith("-") or a == "-":
            return i, None, False
        if name == "python":
            if a.startswith("--"):
                if a in ("--version", "--help", "--help-env", "--help-xoptions", "--help-all"):
                    i += 1
                    continue
                if a.startswith("--check-hash-based-pycs"):
                    i += 1 if "=" in a else 2
                    continue
                return None, None, True
            j = 1
            while j < len(a):
                ch = a[j]
                if ch == "c":
                    if j + 1 < len(a):
                        return None, i, False
                    return None, (i + 1 if i + 1 < len(rest) else None), False
                if ch == "m":
                    return None, None, False
                if ch in "WX":
                    if j + 1 >= len(a):
                        i += 1
                    break
                if ch not in PY_FLAGS:
                    return None, None, True
                j += 1
            i += 1
            continue
        spec = INTERP_OPTS[name]
        if a in spec.get("long_code", ()) or (not a.startswith("--") and len(a) > 1 and a[-1] in spec["code"]
                                               and (a[1:-1].isalpha() or len(a) == 2)):
            return None, (i + 1 if i + 1 < len(rest) else None), False
        i += 2 if a in spec["value"] else 1
    return None, None, False


GIT_CMD_KEYS = {"core.pager", "core.editor", "core.sshcommand", "core.askpass", "core.fsmonitor", "sequence.editor",
                "gpg.program", "diff.external", "credential.helper", "pager.log", "pager.diff"}
SED_E = re.compile(r"(?:^|[;{}]|\d|\$|,)\s*e\s+(.*)|/[gIpmM]*e[gIpmM]*(?:\s|;|$)", re.S)
AWK_RUN = re.compile(r"system\s*\(|\|\s*&?\s*[\"']|[\"']\s*\|")


def strip_prefix(words):
    """Drop env assignments, shell keywords, `time -p` options and `function NAME` before the command word."""
    words = list(words)
    while words and (words[0] in RESERVED or ASSIGN.match(words[0])):
        kw = words.pop(0)
        if kw == "time":
            while words and words[0].startswith("-"):
                words.pop(0)
        elif kw == "function" and words:
            words.pop(0)
    return words


class ClaudeBlock(Block):
    """A `claude` start from a shell (lesson M347): only `factory spawn` starts Claude."""


class DynamicBlock(ClaudeBlock):
    """A command word that could expand to herdr or claude: the message names both rules."""


CLAUDE_PKG = "@anthropic-ai/claude-code"
CLAUDE_CLI = re.compile(r"claude-code/(?:[\w.-]+/)*[\w.-]+\.(?:c|m)?js$")


def is_punct(tok):
    return bool(tok) and all(c in "();<>|&" for c in tok)


def is_stub(path):
    return "/tests/stubs/" in "/" + os.path.normpath(path)


def is_herdr(word):
    """True when the word, as a command, is or may expand to the herdr binary (stub paths excepted)."""
    base = os.path.basename(word.rstrip("/"))
    if base == "herdr":
        return not is_stub(word)
    if base not in ("[", "[[") and GLOB.search(base):
        return fnmatch.fnmatchcase("herdr", base)
    return False


def is_claude(word):
    """True when the word, as a command, is `claude` or a path whose basename is `claude` (no stub exemption)."""
    return os.path.basename(word.rstrip("/")) == "claude"


def under(path, root):
    return path == root or path.startswith(root.rstrip("/") + "/")


def fold_mktemp(toks):
    """Merge `NAME=$ ( mktemp [-d] [-p dir] )` (shlex splits it on the parens) into one token
    `NAME=$(mktemp -d -p dir)` so the assignment value can be judged; the inner words are exactly mktemp
    plus those flags, so they need no command analysis of their own. Anything else is left as it is."""
    out, i = [], 0
    while i < len(toks):
        t = toks[i]
        if ASSIGN.match(t) and t.endswith("=$") and i + 1 < len(toks) and toks[i + 1] == "(":
            j = i + 2
            while j < len(toks) and not is_punct(toks[j]):
                j += 1
            inner = toks[i + 2:j]
            if j < len(toks) and toks[j] == ")" and inner[:1] == ["mktemp"] and \
                    all(not x.startswith("-") or x in ("-d", "-p") for x in inner[1:]):
                out.append(t + "(" + " ".join(inner) + ")")
                i = j + 1
                continue
        out.append(t)
        i += 1
    return out


def is_dynamic(word, strict):
    base = os.path.basename(word.rstrip("/"))
    if "`" in word:
        return True
    if strict:
        return "$" in word or "{" in word
    return base.startswith("$")


# ───────────────────────────── preprocessing ─────────────────────────────

HEREDOC_WORD_END = " \t\n;&|()<>"


def heredoc_delim(cmd, i):
    """Resolve the heredoc whose `<<` starts at cmd[i] the way bash does.

    Returns (strip_tabs, delimiter, end, quoted) or None when the delimiter cannot be resolved with certainty
    (the caller then treats the body as code). A $( ${ $[ or backtick in the word (quoted or not) is such a form:
    bash reads it as a nested expansion, so spaces and parens inside belong to the word. The delimiter is the word after `<<` / `<<-` with bash's quote
    removal: '..' literal, ".." with a backslash dropped before $ ` " \\, an unquoted backslash dropped.
    A body line equals the delimiter exactly (<<- first strips leading tabs from the line)."""
    n = len(cmd)
    j = i + 2
    strip = cmd.startswith("-", j)
    j += strip
    while j < n and cmd[j] in " \t":
        j += 1
    delim, start, quoted = [], j, False
    while j < n and cmd[j] not in HEREDOC_WORD_END:
        c = cmd[j]
        if c == "'":
            k = cmd.find("'", j + 1)
            if k < 0:
                return None
            delim.append(cmd[j + 1:k])
            quoted = True
            j = k + 1
        elif c == '"':
            quoted = True
            j += 1
            while j < n and cmd[j] != '"':
                if cmd[j] == "\\" and j + 1 < n and cmd[j + 1] in '$`"\\':
                    j += 1
                elif cmd[j] == "\\" and j + 1 < n and cmd[j + 1] == "\n":
                    return None
                elif cmd[j] == "`" or (cmd[j] == "$" and cmd[j + 1:j + 2] in ("(", "{", "[")):
                    return None
                delim.append(cmd[j])
                j += 1
            if j >= n:
                return None
            j += 1
        elif c == "\\":
            if j + 1 >= n or cmd[j + 1] == "\n":
                return None
            delim.append(cmd[j + 1])
            quoted = True
            j += 2
        elif c == "$" and cmd[j + 1:j + 2] in ("'", '"'):
            return None  # $'..' / $".." quoting: not resolved here
        elif c == "`" or (c == "$" and cmd[j + 1:j + 2] in ("(", "{", "[")):
            return None  # nested expansion in the word: not resolved here
        else:
            delim.append(c)
            j += 1
    word = "".join(delim)
    if j == start or not word or "\n" in word:
        return None
    return strip, word, j, quoted


def preprocess(cmd, fold_ticks=False, bodies=None):
    """Make newlines separators, drop comments and non-shell heredoc bodies.

    Returns (text, extras): extras are texts to analyze too (bodies of heredocs fed to a shell) and
    text blocks fed to other interpreters (only searched for the word herdr). When bodies is a list, every heredoc
    is appended to it as (the command line up to its newline, the body): the public-text rule reads `-F -` from it.
    """
    out, extras = [], []
    i, n, q = 0, len(cmd), None
    pending, line_start = [], 0
    while i < n:
        c = cmd[i]
        if q == "'":
            out.append(c)
            if c == "'":
                q = None
            i += 1
            continue
        if q == '"':
            if c == "\\" and i + 1 < n:
                out.append(cmd[i:i + 2])
                i += 2
                continue
            out.append(c)
            if c == '"':
                q = None
            i += 1
            continue
        if c == "\\" and i + 1 < n:
            if cmd[i + 1] == "\n":
                i += 2
                continue
            out.append(cmd[i:i + 2])
            i += 2
            continue
        if c in "'\"":
            q = c
            out.append(c)
            i += 1
            continue
        if c == "`" and fold_ticks:
            j = i + 1
            while j < n and cmd[j] != "`":
                j += 2 if cmd[j] == "\\" else 1
            if j < n:  # `x` -> $(x) so the parser sees one substitution
                out.append("$(" + cmd[i + 1:j].replace("\\`", "`") + ")")
                i = j + 1
                continue
        if c == "#" and (not out or out[-1][-1:] in " \t;&|()"):
            while i < n and cmd[i] != "\n":
                i += 1
            continue
        if c == "<" and cmd.startswith("<<", i) and not cmd.startswith("<<<", i):
            r = heredoc_delim(cmd, i)
            if r:
                pending.append((r[0], r[1], r[3]))
                out.append(cmd[i:r[2]])
                i = r[2]
            else:  # unresolved delimiter: no body is skipped, the lines after it are scanned as code
                out.append("<<")
                i += 2
            continue
        if c == "\n":
            head = "".join(out[line_start:])
            out.append(" ; ")
            i += 1
            for strip, delim, quoted in pending:
                body = []
                while i < n:
                    seg = i
                    j = cmd.find("\n", i)
                    line = cmd[i:] if j < 0 else cmd[i:j]
                    i = n if j < 0 else j + 1
                    cmp = line
                    while not quoted and i < n and len(cmp) - len(cmp.rstrip("\\")) & 1:
                        # unquoted body: bash joins backslash-newline before comparing with the terminator
                        j = cmd.find("\n", i)
                        cmp = cmp[:-1] + (cmd[i:] if j < 0 else cmd[i:j])
                        i = n if j < 0 else j + 1
                    if (cmp.lstrip("\t") if strip else cmp) == delim:
                        break
                    body.append(cmd[seg:i].rstrip("\n"))  # every raw line consumed
                    if cmp != line:
                        body.append(cmp)  # what bash feeds the inner command after joining
                body = "\n".join(body)
                if bodies is not None:
                    bodies.append((head, body))
                if re.search(r"\b(%s|eval|source)\b" % "|".join(SHELLS), head):
                    extras.append(("sh", body))
                else:
                    if INTERPRETERS.search(" ".join(re.findall(r"[\w.]+", head))) or \
                            re.search(r"\b(python[\d.]*|perl|ruby|node|php|lua)\b", head):
                        extras.append(("code", body))
                    for wd in re.split(r"[;&|(]", head)[-1].split()[:6]:
                        b = os.path.basename(wd)
                        if INTERPRETERS.match(b):
                            extras.append(("icode", body))
                            break
                        if b in DATA_CMDS | {"tee", "sed", "awk", "wc"}:
                            break
            pending = []
            line_start = len(out)
            continue
        out.append(c)
        i += 1
    if q:
        raise Block("unbalanced quote")
    return "".join(out), extras


def extract_subs(text):
    """Inner texts of live $( ... ) and `...` in a command text. Single-quoted spans are data and skipped;
    inside double quotes both are live. Unquoted $( ) is left to the token parser (it splits on the parens)."""
    subs, i, n, q = [], 0, len(text), None
    while i < n:
        c = text[i]
        if q == "'":
            q = None if c == "'" else q
            i += 1
        elif c == "\\":
            i += 2
        elif c == "'" and q is None:
            q = "'"
            i += 1
        elif c == '"':
            q = None if q == '"' else '"'
            i += 1
        elif text.startswith("$(", i) and q == '"':
            j, depth = i + 2, 1
            while j < n and depth:
                depth += (text[j] == "(") - (text[j] == ")")
                j += 1
            if depth:
                raise Block("unbalanced $( )")
            subs.append(text[i + 2:j - 1])
            i = j
        elif c == "`":
            j = i + 1
            while j < n and text[j] != "`":
                j += 2 if text[j] == "\\" else 1
            if j >= n:
                raise Block("unbalanced backtick")
            subs.append(text[i + 1:j].replace("\\`", "`"))
            i = j + 1
        else:
            i += 1
    return subs


# ───────────────────────────── analysis ─────────────────────────────

class Analyzer:
    def __init__(self, cwd, role=None, hq=False):
        self.cwd = cwd or os.getcwd()
        self.role = role
        self.hq = hq
        self.skip_priv = False
        self.unit_rule = False  # HQ mode: block mutating systemctl (workers keep the factory unit rule)
        self.text = ""
        self.ran_factory = False
        self.scrub_with_factory = None
        self.gcwd = self.cwd  # directory a git command would run in, moved by `cd` (git rule only)
        self.gdyn = False  # True after a `cd` into a directory that is only known at run time
        self.vars = {}  # NAME=value assigned earlier in the command: lets the persist detector resolve $NAME
        self.pt = None  # PublicText rule (G-021), set by check() for the one pass that applies it
        self.ccwd = self.cwd  # like gcwd, for the CFG_DIR write detector; None once a `cd` target is only known at run time

    def live_homes(self):
        homes = set(LIVE_HOMES)
        homes.add(os.path.dirname(os.path.dirname(os.path.dirname(HOOK_REAL))))  # dir of the real bin/factory
        return homes

    def temp_home(self, assigns):
        """True when assigns has FACTORY_HOME=<path> with a static path that is not a live home."""
        vals = [a.split("=", 1)[1] for a in assigns if a.startswith("FACTORY_HOME=")]
        if not vals:
            return False
        v = vals[-1]
        if not v or any(c in v for c in "$`*?[{"):
            return False  # dynamic: could be the live home
        p = os.path.expanduser(v)
        p = p if os.path.isabs(p) else os.path.join(self.cwd, p)
        return os.path.realpath(p) not in self.live_homes()

    def outside_live(self, path):
        p = os.path.expanduser(path)
        p = os.path.realpath(p if os.path.isabs(p) else os.path.join(self.cwd, p))
        return not any(under(p, h) for h in self.live_homes() | LIVE_HOMES | {LIVE_HOME})

    def agent_temp_home(self, assigns):
        """True when assigns has FACTORY_HOME=<path> outside the live home AND ~/factory (worktrees included),
        static or exactly $(mktemp [-d] [-p <static dir outside ~/factory>])."""
        vals = [a.split("=", 1)[1] for a in assigns if a.startswith("FACTORY_HOME=")]
        if not vals or not vals[-1]:
            return False
        v = vals[-1]
        if v.startswith("$(mktemp") and v.endswith(")"):
            ws = v[2:-1].split()
            d, k = None, 1
            while k < len(ws):
                if ws[k] == "-d":
                    k += 1
                elif ws[k] == "-p" and k + 1 < len(ws):
                    d, k = ws[k + 1], k + 2
                else:
                    return False
            if d is None:  # mktemp falls back to $TMPDIR: it must not point into the factory home
                if any(a.startswith("TMPDIR=") for a in assigns):
                    return False
                d = os.environ.get("TMPDIR") or "/tmp"
            return not any(c in d for c in "$`*?[{") and self.outside_live(d)
        if any(c in v for c in "$`*?[{"):
            return False
        return self.outside_live(v)

    def check_identity(self, assigns):
        own = os.environ.get("FACTORY_AGENT") or ""
        for a in assigns:
            name, _, v = a.partition("=")
            name = name.rstrip("+")
            if name not in IDENT_VARS:
                continue
            if name == "FACTORY_AGENT" and own and v == own and v != "owner":
                continue
            if self.agent_temp_home(assigns):
                continue
            raise Block(f"workers may not assign {name} (identity); only FACTORY_AGENT=<own name>, "
                        "or with a temp FACTORY_HOME in the same command")

    def check_scrub(self, name, rest):
        if name == "unset" and any(a in SCRUB_VARS for a in rest):
            raise Block("unset of FACTORY_ROLE/HERDR_PANE_ID")
        if name != "env":
            return
        for j, a in enumerate(rest):
            if not a.startswith("-") and not ASSIGN.match(a):
                break
            if a in ("-u", "--unset") and rest[j + 1:j + 2] and rest[j + 1] in SCRUB_VARS:
                raise Block("env -u FACTORY_ROLE/HERDR_PANE_ID")
            if a.startswith("-u") and a[2:] in SCRUB_VARS or a.startswith("--unset=") and a[8:] in SCRUB_VARS:
                raise Block("env -u FACTORY_ROLE/HERDR_PANE_ID")
            if a == "-" or a == "--ignore-environment" or re.match(r"^-[A-Za-z]*i[A-Za-z]*$", a):
                self.scrub_with_factory = "env -i"

    def factory_unit(self, name, rest, assigns):
        """Block `factory up|tick|relaunch|spawn|clean` (also via python3 <path>/factory) in the live home."""
        if INTERPRETERS.match(name):
            fi = self.factory_script(name, rest)
            if fi is None:
                return
            rest = rest[fi + 1:]
        elif name != "factory":
            return
        nonopt = [a for a in rest if not a.startswith("-")]
        sub = nonopt[0] if nonopt else None
        if self.role == "evaluator":
            self.evaluator_factory(sub, nonopt, assigns)
        if sub in BROAD_READS:
            self.broad_read(sub, rest, assigns)
        if sub not in FACTORY_UNIT_CMDS:
            return
        if sub == "clean" and self.role == "cleaner":
            return
        if self.temp_home(assigns):
            return
        raise Block(f"workers may not run `factory {sub}`")

    def evaluator_factory(self, sub, nonopt, assigns):
        """The evaluator runs in a sandbox with a temp FACTORY_HOME: only whoami, task show, inbox, msg and done,
        and only against a home that is not the live one (the command's own FACTORY_HOME=, else the hook's env)."""
        if not (sub in ("whoami", "inbox", "msg", "done") or nonopt[:2] == ["task", "show"]):
            raise Block(f"the evaluator may not run `factory {sub or ''}`; allowed: whoami, task show, inbox, msg, done")
        if any(a.startswith("FACTORY_HOME=") for a in assigns):
            ok = self.agent_temp_home(assigns)
        else:
            env = os.environ.get("FACTORY_HOME") or ""
            ok = bool(env) and os.path.realpath(env) not in self.live_homes() and self.outside_live(env)
        if not ok:
            raise Block("the evaluator may run factory commands only against its temp FACTORY_HOME, not the live home")

    def factory_script(self, name, rest):
        """Index of the `factory` script word in an interpreter's args, else None. When the options cannot be
        modelled (unknown), any word whose basename is factory counts (fail closed)."""
        si, _, unknown = interp_args(name, rest)
        if unknown:
            return next((k for k, a in enumerate(rest) if os.path.basename(a) == "factory"), None)
        if si is None or os.path.basename(rest[si]) != "factory":
            return None
        return si

    def db_path(self):
        """Same resolution as bin/factory: HOME/db/factory.db, else the legacy HOME/factory.db (no env var but FACTORY_HOME picks the DB)."""
        home = os.environ.get("FACTORY_HOME") or LIVE_HOME
        new, legacy = os.path.join(home, "db", "factory.db"), os.path.join(home, "factory.db")
        return new if os.path.exists(new) or not os.path.exists(legacy) else legacy

    def own_goal(self):
        """The worker's goal: agents.goal_id for $FACTORY_AGENT, read-only; None (fail closed) on any problem."""
        agent = os.environ.get("FACTORY_AGENT")
        if not agent:
            return None
        db = self.db_path()
        try:
            con = sqlite3.connect(f"file:{db}?mode=ro", uri=True, timeout=2)
            try:
                row = con.execute("SELECT goal_id FROM agents WHERE name = ?", (agent,)).fetchone()
            finally:
                con.close()
        except sqlite3.Error:
            return None
        return row[0] if row and row[0] else None

    def broad_read(self, sub, rest, assigns):
        """Block broad factory reads (res, agents, goal list, task list, ...) against the live home for workers.
        `rest` = the tokens after `factory` (options included). Exempt: --role staff-engineer, `agents` for
        --role cleaner, `goal show <own goal>`, a temp FACTORY_HOME.
        Fail closed on options: we do not model which argparse flags take a value (--goal X, --note x) versus
        flags that do not (--all), so any option token (`-x`, `--x`, `--x=v`, `--`) before the action word (or
        before the id of `goal show`) blocks the command; `factory goal --goal G-005 show G-005` is blocked too."""
        if self.role == "staff-engineer":
            return
        seconds = BROAD_READS[sub]
        shown = f"factory {sub}"
        sub_i = next(i for i, a in enumerate(rest) if not a.startswith("-"))
        after = rest[sub_i + 1:]
        if seconds is not None:
            act_i = next((i for i, a in enumerate(after) if not a.startswith("-")), None)
            if act_i is None:
                return  # no action word: argparse usage error, reads nothing
            action = after[act_i]
            if sub_i > 0 or act_i > 0:
                shown += " <options> " + action
            elif sub == "goal" and action == "show":
                gid = after[1] if len(after) > 1 else None
                own = self.own_goal()
                if gid is not None and not gid.startswith("-") and own is not None and gid == own:
                    return
                shown += " show"
            elif action in seconds:
                shown += " " + action
            else:
                return
        if sub == "agents" and self.role == "cleaner":
            return
        if self.temp_home(assigns) or self.agent_temp_home(assigns):
            return
        raise Block(f"workers may not run broad factory reads (`{shown}`) against the live home; use "
                    "factory task show <id> / goal show <own goal>, or set a temp FACTORY_HOME in the same command")

    def script(self, text, depth=0):
        if depth > MAX_DEPTH:
            raise Block("nesting too deep")
        if depth == 0:
            self.text = text
            if not self.hq:  # a redirect whose target is built by a substitution and ends in a protected name
                for m in re.finditer(r">[>|]?\s*[^\s;|&]*(?:\$\(|`)[^;|&\n]*", text):
                    if persist_code(m.group(0)):
                        raise Block(f"a redirect built by a substitution into a protected config/init file: {PERSIST_WHY}")
        text, extras = preprocess(text, self.hq, self.pt.bodies if self.pt else None)
        for kind, body in extras:
            if kind == "sh":
                self.soft(self.script, body, depth + 1)
            elif kind == "icode":
                hit = priv_in_code(body)
                if hit:
                    raise PrivBlock(hit)
                if not self.hq and persist_code(body):
                    raise Block(f"interpreter code naming a protected config/init file: {PERSIST_WHY}")
            elif not self.hq and re.search(r"herdr", body):
                raise Block("herdr in an interpreter heredoc")
        try:
            lex = shlex.shlex(re.sub(r"(?<![\w$])\d+(?=[<>])", " ", text), posix=True, punctuation_chars=True)
            lex.whitespace_split = True
            lex.commenters = ""
            toks = fold_mktemp(list(lex))
        except ValueError as e:
            raise Block(f"cannot parse command ({e})")
        for sub in extract_subs(text):
            self.soft(self.script, sub, depth + 1)
        cmds, cur, piped, i, hs = [], [], False, 0, []
        reds = []  # (index of the command it belongs to, redirect target word)
        stack, dollar = [], False  # saved (cur, piped, hs) of the commands that open a $( )
        while i < len(toks):
            tok = toks[i]
            nxt = toks[i + 1] if i + 1 < len(toks) else None
            if tok == "$" and nxt == "(" and all(ASSIGN.match(w) or w in RESERVED for w in cur):
                depth_p, hit = 0, None
                for t in toks[i + 1:]:
                    if is_punct(t):
                        depth_p += t.count("(") - t.count(")")
                        if depth_p <= 0:
                            break
                    elif hit is None:
                        hit = priv_word(t, not self.hq)
                if hit:
                    raise PrivBlock(hit)
                if not self.hq:
                    raise DynamicBlock("command substitution as the command word could expand to herdr or claude")
                dollar = True
                i += 1
                continue
            if tok.endswith("$") and nxt == "(":
                tok = tok[:-1]
                dollar = True
                if not tok:
                    i += 1
                    continue
            if is_punct(tok):
                if ("<" in tok or ">" in tok) and "(" not in tok:
                    if nxt is not None and not is_punct(nxt):
                        if tok == "<<<":
                            hs.append(nxt)
                        elif ">" in tok and ("&" not in tok or (re.match(r"^[0-9]*(&>>?|>&)$", tok)
                                                                 and not re.match(r"^([0-9]+|-)$", nxt))):
                            reds.append((len(cmds), nxt))  # `>&2` / `>&-` duplicate or close an fd: no file target
                        i += 1
                else:
                    if not self.hq and ("<" in tok or ">" in tok) and \
                            any(os.path.basename(w) in SHELL_LIKE for w in cur):
                        raise Block("shell reading a process substitution: content cannot be inspected")
                    for piece in re.findall(r"\(|\)|[^()]+", tok):
                        if piece == "(" and dollar:
                            stack.append((cur, piped, hs))
                            cur, piped, hs = [], False, []
                        elif piece == ")" and stack:
                            cmds.append((cur, piped, hs))
                            cur, piped, hs = stack.pop()
                            if any(not (ASSIGN.match(w) or w in RESERVED) for w in cur):
                                cur = cur + ["$SUB"]  # the substitution's value is an argument of that command
                        else:
                            cmds.append((cur, piped, hs))
                            cur, piped, hs = [], "|" in piece, []
                        dollar = False
            else:
                cur.append(tok)
            i += 1
        while stack:  # unclosed $( ): analyse what we have
            cmds.append((cur, piped, hs))
            cur, piped, hs = stack.pop()
        cmds.append((cur, piped, hs))
        for n, (words, piped, hstrs) in enumerate(cmds):
            if not self.hq:
                for _, target in [r for r in reds if r[0] == n]:
                    self.cfg_redirect(target)
                    self.persist_redirect(target)
            self.soft(self.cmd, words, depth, piped, False, hstrs)

    def soft(self, fn, *args):
        """Run fn; in HQ mode only a PRIV block counts, so parse trouble never blocks (no false positives)."""
        try:
            fn(*args)
        except (PrivBlock, PublicBlock):
            raise
        except Block:
            if not self.hq:
                raise

    def priv(self, words, depth, piped=False, hstrs=()):
        """PRIV rule. words[0] is a command word: block when it, or the command a wrapper runs, is
        sudo/su/doas/pkexec/docker. In HQ mode also recurse into shells/eval/source/interpreter code
        (worker mode gets those from cmd())."""
        if self.skip_priv:
            return
        if depth > MAX_DEPTH:
            raise Block("nesting too deep")
        words = strip_prefix(words)
        if not words:
            return
        w, rest = words[0], words[1:]
        hit = priv_word(w, not self.hq) or priv_subst(w)
        if hit:
            raise PrivBlock(hit)
        name = os.path.basename(w)
        if name == "systemd-run":
            raise UnitBlock("systemd-run")
        if name == "systemctl" and self.unit_rule:
            verb = next((a for a in rest if not a.startswith("-") and a in UNIT_MUTATING), None)
            if verb:
                raise UnitBlock(f"systemctl {verb}")
        if name == "find":
            for j, a in enumerate(rest):
                if a in FIND_EXEC:
                    end = next((k for k in range(j + 1, len(rest)) if rest[k] in (";", "+")), len(rest))
                    self.priv(rest[j + 1:end], depth + 1)
            return
        if name == "trap" and rest:
            self.soft(self.script, next((a for a in rest if not a.startswith("-")), ""), depth + 1)
        elif name == "git":
            self.git_priv(rest, depth)
        elif name == "sed":
            for a in rest:
                m = SED_E.search(a)
                hit = m and priv_in_code(m.group(1) or a.replace("/", " "))
                if hit:
                    raise PrivBlock(hit)
        elif name in AWKS:
            for a in rest:
                if AWK_RUN.search(a) and priv_in_code(a):
                    raise PrivBlock(priv_in_code(a))
        if name in WRAP_VALUE_OPTS:
            if name == "command" and rest[:1] in (["-v"], ["-V"], ["-pv"], ["-pV"]):
                return
            vopts, i = WRAP_VALUE_OPTS[name], 0
            while i < len(rest):
                a = rest[i]
                if a == "--":
                    i += 1
                    break
                if name == "env" and ASSIGN.match(a):
                    i += 1
                    continue
                if name == "env" and (a in ("-S", "--split-string") or a.startswith("--split-string=")):
                    payload = a.split("=", 1)[1] if "=" in a else (rest[i + 1] if i + 1 < len(rest) else "")
                    self.soft(self.script, payload, depth + 1)
                    i += 1 if "=" in a else 2
                    continue
                if a.startswith("-") and len(a) > 1:
                    i += 2 if a in vopts else 1
                    continue
                break
            if name == "timeout":
                i += 1  # the duration
            sub = rest[i:]
            if name == "watch" and sub and re.search(r"\s", sub[0]):
                self.soft(self.script, " ".join(sub), depth + 1)
            self.priv(sub, depth + 1)
            return
        if name in WRAPPERS and name not in WRAP_DATA:  # other exec wrappers: any later word is suspect
            for a in rest:
                if re.search(r"\s", a):
                    self.soft(self.script, a, depth + 1)
                elif priv_word(a, not self.hq):
                    raise PrivBlock(priv_word(a, not self.hq))
            return
        if not self.hq:
            return
        if name in SHELLS:
            self.soft(self.shell, rest, depth, False, hstrs)
        elif name == "eval":
            if "$" not in " ".join(rest):
                self.soft(self.script, " ".join(rest), depth + 1)
        elif name in ("source", ".") and rest:
            self.soft(self.file, rest[0], depth)
        elif INTERPRETERS.match(name):
            self.interp_priv(name, rest)

    def git_priv(self, rest, depth):
        """git runs shell text from `-c alias.x=!cmd`, `-c core.pager=cmd` and `git config <key> <value>`."""
        pairs = [rest[j + 1] for j in range(len(rest) - 1) if rest[j] == "-c"]
        if "config" in rest:
            k = rest.index("config")
            args = [a for a in rest[k + 1:] if not a.startswith("-")]
            pairs += [f"{a}={b}" for a, b in zip(args, args[1:])] + [a for a in args if "=" in a]
        for kv in pairs:
            key, _, val = kv.partition("=")
            if val.startswith("!"):
                self.soft(self.script, val[1:], depth + 1)
            elif key.lower() in GIT_CMD_KEYS:
                self.soft(self.script, val, depth + 1)

    def interp_priv(self, name, rest):
        _, ci, unknown = interp_args(name, rest)
        if unknown:  # cannot tell which word is the code: look at every code-looking word
            texts = [a for a in rest if re.search(r"[\s\"'();]", a)]
        else:
            texts = [rest[ci]] if ci is not None else []
        for t in texts:
            hit = priv_in_code(t)
            if hit:
                raise PrivBlock(hit)

    def git_homes(self):
        """The shared checkout(s). WORKER_GUARD_LIVE_HOME replaces them (tests only: the hook's own env, which a
        worker cannot change from inside a command)."""
        override = os.environ.get("WORKER_GUARD_LIVE_HOME")
        if override:
            return {os.path.realpath(override)}
        return set(LIVE_HOMES)  # not live_homes(): that adds the hook's own repo root (CI checkout)

    @staticmethod
    def dynamic_dir(path):
        return any(c in path for c in "$`*?[{")

    def git_cd(self, name, rest):
        """Track the directory for the shared-checkout git rule."""
        if name == "popd":
            self.gdyn = True
            return
        args = [a for a in rest if not a.startswith("-") or a == "-"]
        if not args:
            self.gcwd, self.gdyn = os.path.expanduser("~"), False
        else:
            self.gcwd, self.gdyn = self.git_join(self.gcwd, self.gdyn, args[0])

    def cfg_cd(self, name, rest):
        """Track the directory for the CFG_DIR write detector (`~` is the passwd home, never $HOME)."""
        args = [a for a in rest if not a.startswith("-") or a == "-"]
        if name == "popd" or (args and (args[0] == "-" or self.dynamic_dir(args[0]))):
            self.ccwd = None
        elif not args:
            self.ccwd = PW_DIR
        else:
            p = self.cfg_expand(args[0])
            self.ccwd = p if os.path.isabs(p) or self.ccwd is None else os.path.join(self.ccwd, p)

    @staticmethod
    def cfg_expand(w):
        return re.sub(r"^(~|\$HOME|\$\{HOME\})(?=/|$)", lambda m: PW_DIR, w)

    @staticmethod
    def cfg_code(word):
        """True when interpreter code / an editor argument spells a protected path, also split by string
        concatenation or joined pieces (`'live_'+'home'`): quotes, `+`, `,` and spaces are dropped before matching."""
        return bool(CFG_CODE.search(word) or CFG_CODE.search(re.sub(r"[\s'\"+,]", "", word)))

    def cfg_hit(self, word, ancestor=False):
        """True when `word` (or the value of a --opt=value / of=value word) names CFG_DIR or <live>/run/settings or
        something under them (or, with ancestor, a directory that contains one). A word with an expansion we cannot
        resolve counts when it spells `.config/factory` or `run/settings`."""
        for w in {word, word.split("=", 1)[-1]}:
            t = self.cfg_expand(w)
            if any(c in t for c in "$`*?[{"):
                if ".config/factory" in t or "run/settings" in t:
                    return True
                continue
            if not t or (not os.path.isabs(t) and self.ccwd is None):
                continue
            p = os.path.realpath(t if os.path.isabs(t) else os.path.join(self.ccwd, t))
            for d in {CFG_DIR} | RUN_SETTINGS:
                if p == d or under(p, d) or (ancestor and under(d, p)):
                    return True
        return False

    def cfg_redirect(self, target):
        if self.cfg_hit(target):
            raise Block("a redirect into ~/.config/factory (it holds live_home, which decides what counts as the live "
                        "home) or <live>/run/settings (the rendered role settings); only the owner changes them")

    def cfg_write(self, name, rest):
        """Best-effort (T-0212): refuse Bash writes to <pw_dir>/.config/factory, the directory and live_home. Reads
        (cat, grep) stay allowed. NOT a boundary: the file can only ADD a live home and is ignored unless owned by the
        user and not group/other-writable; the Edit/Write deny rules in the settings are the other layer."""
        why = ("it holds live_home, which decides what counts as the live home; only the owner changes it with "
               "`factory init --live-home`")
        if name == "factory" or (INTERPRETERS.match(name) and self.factory_script(name, rest) is not None):
            abbrev = [x for x in rest if re.match(r"^--l[a-z-]*(=|$)", x) and "--live-home".startswith(x.split("=")[0])]
            if "init" in rest and abbrev:  # argparse accepts any unambiguous prefix of the option
                raise Block(f"`factory init --live-home` writes ~/.config/factory: {why}")
        if name in CFG_WRITERS:
            args = [a for a in rest if a != "--"]
            checked = [(j, a) for j, a in enumerate(args)]
            if name in ("cp", "install", "rsync", "dd"):  # reading FROM the config dir is fine: only the destination counts
                checked = [(j, a) for j, a in checked if j == len(args) - 1 or a.startswith("of=")
                           or (j > 0 and args[j - 1] in ("-t", "--target-directory")) or a.startswith("--target-directory=")]
            for j, a in checked:
                anc = name in CFG_REMOVERS or (name == "mv" and j < len(args) - 1)
                if self.cfg_hit(a, anc):
                    raise Block(f"`{name}` on ~/.config/factory: {why}")
        elif (name in ("sed", "perl", "gawk") or name in AWKS) and (name in AWKS or any(
                re.match(r"^-[A-Za-z]*i", a) or a.startswith("--in-place") or a == "inplace" for a in rest)):
            if any(self.cfg_hit(a) or self.cfg_code(a) for a in rest):
                raise Block(f"`{name}` writing ~/.config/factory: {why}")
        elif INTERPRETERS.match(name) or name in EDITORS - {"less", "more", "man", "view"}:
            if any(self.cfg_hit(a) or self.cfg_code(a) for a in rest):
                raise Block(f"`{name}` naming ~/.config/factory (code there cannot be inspected; read it with cat): {why}")

    def persist_hit(self, word, ancestor=False):
        """True when `word` (or the value of --opt=value / of=value) names a protected persistence path (T-0334). A
        word with an expansion we cannot resolve counts when it spells a protected name."""
        for w in {word, word.split("=", 1)[-1]}:
            t = re.sub(r"\$\{?(\w+)\}?", lambda m: self.vars.get(m.group(1), m.group(0)), w)
            t = self.cfg_expand(t)
            if any(c in t for c in "$`"):
                # an expansion we cannot resolve: refuse when it spells a protected name or sits under the home
                if persist_code(t) or t.startswith(("~", PW_DIR)) or "~" in t or "HOME" in t:
                    return True
                continue
            if not t:
                continue
            if not os.path.isabs(t):
                if self.ccwd is None:
                    if persist_code(t):
                        return True
                    continue
                t = os.path.join(self.ccwd, t)
            if persist_path(t, ancestor):
                return True
        return False

    def persist_redirect(self, target):
        if self.persist_hit(target):
            raise Block(f"a redirect into a protected config/init file: {PERSIST_WHY}")

    def persist_write(self, name, rest):
        """T-0334: refuse Bash writes to ~/.gitconfig, ~/.config/git/**, /etc/gitconfig and the shell init files.
        Reads (cat, grep) stay allowed."""
        args = [a for a in rest if a != "--"]
        if name in CFG_WRITERS:
            checked = list(enumerate(args))
            dest_only = name in ("cp", "install", "rsync", "dd") or (
                name == "ln" and any(a == "--symbolic" or (re.match(r"^-[A-Za-z]+$", a) and "s" in a) for a in args))
            if dest_only:  # reading FROM a protected file is fine: only the destination counts
                checked = [(j, a) for j, a in checked if j == len(args) - 1 or a.startswith("of=")
                           or (j > 0 and args[j - 1] in ("-t", "--target-directory")) or a.startswith("--target-directory=")]
            for _, a in checked:
                if self.persist_hit(a, name in ("ln", "mv")):
                    raise Block(f"`{name}` on a protected config/init file: {PERSIST_WHY}")
            if name in ("cp", "mv", "install", "ln", "rsync"):  # a directory destination: the file lands as dest/<source name>
                opts = [a for a in args if not a.startswith("-")]
                dests = [a for j, a in checked if j == len(args) - 1 or (j > 0 and args[j - 1] in ("-t", "--target-directory"))
                         ] + [a.split("=", 1)[1] for a in args if a.startswith("--target-directory=")]
                for d in dests:
                    for src in opts:
                        if src != d and self.persist_hit(d.rstrip("/") + "/" + os.path.basename(src.rstrip("/"))):
                            raise Block(f"`{name}` into a protected directory under a protected file name: {PERSIST_WHY}")
        elif (name in ("sed", "perl", "gawk") or name in AWKS) and (name in AWKS or any(
                re.match(r"^-[A-Za-z]*i", a) or a.startswith("--in-place") or a == "inplace" for a in rest)):
            cand = rest
            if name in AWKS and not any(a == "inplace" for a in rest):  # operands are data files: reading one is fine
                prog = [a for a in rest if not a.startswith("-")][:1] if not any(a.startswith("-f") or a == "--file" for a in rest) else []
                cand = prog
            if any(self.persist_hit(a) or persist_code(a) for a in cand):
                raise Block(f"`{name}` writing a protected config/init file: {PERSIST_WHY}")
        elif INTERPRETERS.match(name) or name in EDITORS - {"less", "more", "man", "view"}:
            if any(self.persist_hit(a) or persist_code(a) for a in rest):
                raise Block(f"`{name}` naming a protected config/init file (code there cannot be inspected; read it "
                            f"with cat): {PERSIST_WHY}")

    @staticmethod
    def config_args_write(args):
        """True when `git config <args>` writes. Like config_mutates, but understands the `get`/`list` words and
        options that take a value (`--type bool key` is a read)."""
        pos, k = [], 0
        while k < len(args):
            a = args[k]
            k += 1
            if a in CONFIG_VALUE_OPTS:
                k += 1
            elif not a.startswith("-"):
                pos.append(a)
        flags = [a for a in args if a.startswith("-")]
        if any(a in CONFIG_WRITE for a in flags):
            return True
        if any(a in CONFIG_READ for a in flags) or pos[:1] in (["get"], ["list"]):
            return False
        return pos[:1] in (["set"], ["unset"], ["edit"]) or len(pos) >= 2

    def git_persist(self, rest, depth):
        """T-0334: `git config --global|--system|-f <protected file>` writes, also behind `git -c ... config` and an
        alias. Read-only forms (`--global --get`, `--global --list`) and repo-local writes stay allowed."""
        i, cwd, aliases, git_dir, work_tree = 0, self.ccwd, [], None, None
        while i < len(rest) and rest[i].startswith("-"):
            o = rest[i]
            val = None
            if o in GIT_VALUE_OPTS and i + 1 < len(rest):
                val, i = rest[i + 1], i + 2
            else:
                if o.startswith("-c") and not o.startswith("--") and len(o) > 2:
                    val = o[2:]
                    o = "-c"
                elif o.startswith(("--git-dir=", "--work-tree=")):
                    o, val = o.split("=", 1)
                i += 1
            if o == "-c" and val and re.match(r"(?i)alias\.[^=]+=", val):
                aliases.append(tuple(val[6:].split("=", 1)))
            elif o == "-C" and val is not None and cwd is not None:
                cwd = None if self.dynamic_dir(val) else os.path.join(cwd, os.path.expanduser(val))
            elif o == "--git-dir":
                git_dir = val
            elif o == "--work-tree":
                work_tree = val
        for alias, body in aliases:
            used = rest[i + 1:] if i < len(rest) and rest[i] == alias else []
            if body.startswith("!"):
                self.script(" ".join([body[1:], *map(shlex.quote, used)]), depth + 1)
            else:
                try:
                    words = shlex.split(body)
                except ValueError as e:
                    raise Block(f"cannot parse git alias ({e})")
                if words[:1] == ["config"]:
                    self.git_persist(["config", *words[1:], *used], depth)
        if i < len(rest) and rest[i] == "remote":
            if rest[i + 1:i + 2] and rest[i + 1] in REMOTE_WRITE:
                self.repo_cfg_refuse(f"git remote {rest[i + 1]}", cwd, git_dir, work_tree, [])
            return
        if i >= len(rest) or rest[i] != "config":
            return
        args, scope, files, k = [], None, [], i + 1
        rest = rest[:]
        while k < len(rest):
            a = rest[k]
            k += 1
            name, eq, val = a.partition("=")
            if a.startswith("--") and len(name) >= 4:
                if "--global".startswith(name) or "--system".startswith(name):
                    scope = name
                    continue
                if "--file".startswith(name) and len(name) >= 5:
                    if eq:
                        files.append(val)
                    elif k < len(rest):
                        files.append(rest[k])
                        k += 1
                    continue
            elif a.startswith("-f") and not a.startswith("--"):
                if len(a) > 2:
                    files.append(a[2:])
                elif k < len(rest):
                    files.append(rest[k])
                    k += 1
                continue
            args.append(a)
        if not self.config_args_write(args):
            return
        if scope:
            raise Block(f"`git config {scope}` write: {PERSIST_WHY}")
        if CFG_ENV_ASSIGN.search(getattr(self, "text", "") or ""):  # GIT_CONFIG=<file> acts as --file, HOME= moves --global
            raise Block("a `git config` write in a command that assigns GIT_CONFIG*, HOME or XDG_CONFIG_HOME (it can "
                        f"redirect the write to a protected file); set them in a separate command or drop them: {PERSIST_WHY}")
        saved = self.ccwd
        self.ccwd = cwd
        try:
            for f in files:
                if self.persist_hit(f):
                    raise Block(f"`git config --file {f}` write: {PERSIST_WHY}")
        finally:
            self.ccwd = saved
        self.repo_cfg_refuse("git config", cwd, git_dir, work_tree, files)

    def repo_cfg_refuse(self, what, cwd, git_dir, work_tree, files):
        """T-0226: a repo-config write (git config, git remote add/...) is allowed only with --file inside the worker's own
        scratchpad, or when the repository is a clone with its own .git inside it. A linked worktree shares the live
        checkout's config, so it never counts. No key allow-list: no role needs a repo-config write. Undecidable: refuse."""
        why = f"`{what}` writes repo config: {REPO_CFG_WHY}"
        if files:
            saved = self.ccwd
            self.ccwd = cwd
            try:
                if all(self.cfg_in_scratch(f) for f in files):
                    return
            finally:
                self.ccwd = saved
            raise GitBlock(why)
        if cwd is None or re.search(r"\bGIT_(?:DIR|WORK_TREE|COMMON_DIR|CONFIG\w*)=", getattr(self, "text", "") or ""):
            raise GitBlock(why)
        for opt in (git_dir, work_tree):
            if opt is not None and self.dynamic_dir(opt):
                raise GitBlock(why)
        if git_dir is not None:
            gd = os.path.realpath(os.path.join(cwd, os.path.expanduser(git_dir)))
            if in_scratchpad(gd) and not os.path.exists(os.path.join(gd, "commondir")):
                return
            raise GitBlock(why)
        p = os.path.realpath(os.path.join(cwd, os.path.expanduser(work_tree))) if work_tree is not None else os.path.realpath(cwd)
        while True:
            g = os.path.join(p, ".git")
            bare = os.path.isfile(os.path.join(p, "HEAD")) and os.path.isdir(os.path.join(p, "objects"))
            if os.path.isdir(g) or (bare and not os.path.exists(os.path.join(p, "commondir"))):
                if in_scratchpad(p):
                    return
                break
            if os.path.isfile(g) or os.path.dirname(p) == p:
                break
            p = os.path.dirname(p)
        raise GitBlock(why)

    def cfg_in_scratch(self, word):
        t = os.path.expanduser(re.sub(r"\$\{?(\w+)\}?", lambda m: self.vars.get(m.group(1), m.group(0)), self.cfg_expand(word)))
        if not t or any(c in t for c in "$`*?[{") or (not os.path.isabs(t) and self.ccwd is None):
            return False
        return in_scratchpad(os.path.realpath(t if os.path.isabs(t) else os.path.join(self.ccwd, t)))

    def git_join(self, cwd, dyn, path):
        if path == "-" or self.dynamic_dir(path):
            return cwd, True
        p = os.path.expanduser(path)
        return (p if os.path.isabs(p) else os.path.join(cwd, p)), False

    def git_top_shared(self, path):
        """Walk up from realpath(path) to the first ancestor holding .git: a FILE is a linked worktree (not
        shared), a DIRECTORY is a main checkout (shared when it is the live home). No git is run."""
        homes = self.git_homes()
        p = os.path.realpath(path)
        while True:
            g = os.path.join(p, ".git")
            if os.path.isdir(g):
                return p in homes
            if os.path.isfile(g):
                return False
            parent = os.path.dirname(p)
            if parent == p:
                return False
            p = parent

    def git_is_shared(self, cwd, dyn, git_dir, work_tree):
        if (git_dir and self.dynamic_dir(git_dir)) or (work_tree and self.dynamic_dir(work_tree)):
            return True
        if git_dir:
            gd = os.path.realpath(os.path.join(cwd, os.path.expanduser(git_dir)))
            for h in self.git_homes():
                top = os.path.join(h, ".git")
                if under(gd, top) and not under(gd, os.path.join(top, "worktrees")):
                    return True
        if work_tree:
            return self.git_top_shared(os.path.join(cwd, os.path.expanduser(work_tree)))
        return dyn or self.git_top_shared(cwd)

    def git_shared(self, words, assigns):
        """Shared-checkout git guard (T-0068): a worker runs no mutating git where the effective directory belongs
        to the shared main checkout. Unknown subcommands count as mutating."""
        rest = words[1:]
        cwd, dyn, git_dir, work_tree = self.gcwd, self.gdyn, None, None
        for a in assigns:
            if a.startswith("GIT_DIR="):
                git_dir = a.split("=", 1)[1]
            elif a.startswith("GIT_WORK_TREE="):
                work_tree = a.split("=", 1)[1]
        i = 0
        cfg = any(x.startswith("GIT_") for x in (a.split("=", 1)[0] for a in assigns)) or bool(
            re.search(r"\bGIT_\w*=", self.text))  # any GIT_*= anywhere (e.g. GIT_TEMPLATE_DIR, GIT_DIR): git init is not loosened when git env or -c is in play
        while i < len(rest) and rest[i].startswith("-"):
            o = rest[i]
            cfg = cfg or o in ("-c", "--config-env") or o.startswith(("-c", "--config-env=", "--exec-path"))
            if o in GIT_VALUE_OPTS and i + 1 < len(rest):
                if o == "-C":
                    cwd, dyn = self.git_join(cwd, dyn, rest[i + 1])
                elif o == "--git-dir":
                    git_dir = rest[i + 1]
                elif o == "--work-tree":
                    work_tree = rest[i + 1]
                i += 2
                continue
            if o.startswith("--git-dir="):
                git_dir = o.split("=", 1)[1]
            elif o.startswith("--work-tree="):
                work_tree = o.split("=", 1)[1]
            i += 1
        if i >= len(rest):
            return
        sub, args = rest[i], rest[i + 1:]
        if not self.git_mutates(sub, args) or not self.git_is_shared(cwd, dyn, git_dir, work_tree):
            return
        if sub == "init" and not (cfg or git_dir or work_tree) and self.git_outside_shared(args, cwd, dyn):
            return  # M5368: init of a scratch dir
        if self.role == "cleaner" and self.git_cleaner_ok(sub, args):
            return
        shown = " ".join(["git", sub, *args])
        raise GitBlock(f"`{shown[:80]}`: {SHARED_ADVICE}")

    def git_outside_shared(self, args, cwd, dyn):
        """T-0303: `git init` is allowed in a shared checkout's cwd only for exactly one literal directory inside
        the worker's own scratchpad. An option or path not understood is unsafe."""
        if dyn:
            return False
        out, k = [], 0
        while k < len(args):
            a = args[k]
            k += 1
            if a == "--":
                out += args[k:]
                break
            name, eq, val = a.partition("=")
            if a in ("-q", "--quiet", "--bare"):
                continue
            if name in ("-b", "--initial-branch"):
                if not eq:
                    if k >= len(args):
                        return False
                    k += 1
            elif a.startswith("-") and a != "-":
                return False  # --separate-git-dir, --template, an unknown option
            else:
                out.append(a)
        if len(out) != 1:
            return False
        target = out[0]
        if not target or self.dynamic_dir(target) or target.startswith("~"):
            return False  # quoted '~' is a literal name to git: not decidable here
        path = os.path.realpath(target if os.path.isabs(target) else os.path.join(cwd, target))
        return in_scratchpad(path)  # M5368: only the worker's own scratchpad, never a shared home, ancestor or worktree

    @staticmethod
    def branch_mutates(args):
        listing = positional = False
        k = 0
        while k < len(args):
            a = args[k]
            k += 1
            if a.startswith("--"):
                n = a.split("=", 1)[0]
                if n in BRANCH_LIST_LONG:
                    listing = True
                elif n in BRANCH_VALUE_LONG:
                    listing = True
                    k += "=" not in a
                elif n not in BRANCH_PLAIN_LONG:
                    return True
            elif a.startswith("-") and len(a) > 1:
                if set(a[1:]) - set("larviq"):
                    return True
                listing = listing or bool(set(a[1:]) & set("lar"))
            else:
                positional = True
        return positional and not listing

    @staticmethod
    def config_mutates(args):
        if any(a in CONFIG_WRITE for a in args) or args[:1] in (["set"], ["unset"], ["edit"]):
            return True
        if any(a in CONFIG_READ for a in args) or args[:1] in (["get"], ["list"]):
            return False
        return len([a for a in args if not a.startswith("-")]) >= 2

    def git_mutates(self, sub, args):
        if sub in GIT_READ_ONLY:
            return False
        if sub.startswith("-"):
            return False  # `git --version`, `git --help`
        first = args[0] if args else None
        if sub == "branch":
            return self.branch_mutates(args)
        if sub == "worktree":
            return first not in (None, "list")
        if sub == "config":
            return self.config_mutates(args)
        if sub == "remote":
            return first not in (None, "-v", "--verbose", "show", "get-url")
        if sub == "notes":
            return first not in (None, "list", "show")
        return True

    @staticmethod
    def git_cleaner_ok(sub, args):
        """The cleaner's hand-removal of stray worktrees: worktree remove (no --force), worktree prune, branch -d."""
        if sub == "worktree" and args[:1] == ["remove"]:
            return not any(a == "--force" or re.match(r"^-[A-Za-z]*f", a) for a in args[1:])
        if sub == "worktree" and args[:1] == ["prune"]:
            return True
        if sub == "branch":
            return args[:1] == ["-d"] and len(args) > 1 and not any(a.startswith("-") for a in args[1:])
        return False

    def git_delete(self, rest):
        """Branch deletion (T-0057): every worker role is refused; --role cleaner may delete only factory/* branches
        (local `git branch -d/-D/--delete`, remote `git push <remote> --delete|-d|:<ref>`); its other pushes are refused."""
        i = 0
        while i < len(rest) and rest[i].startswith("-"):
            i += 2 if rest[i] in ("-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path") else 1
        if i >= len(rest) or rest[i] not in ("branch", "push"):
            return
        sub, args = rest[i], rest[i + 1:]
        if sub == "branch":
            if not any(re.match(r"^-[A-Za-z]*[dD][A-Za-z]*$", a) or a == "--delete" for a in args):
                return
            targets = [a for a in args if not a.startswith("-")]
        else:
            pos, k = [], 0
            while k < len(args):
                a = args[k]
                if a in ("-o", "--push-option", "--repo", "--receive-pack", "--exec"):
                    k += 2
                    continue
                if not a.startswith("-"):
                    pos.append(a)
                k += 1
            flag = any(re.match(r"^-[A-Za-z]*d[A-Za-z]*$", a) or a == "--delete" for a in args)
            colon = any(a.startswith(":") or a.startswith("+:") for a in pos[1:])
            if not flag and not colon:
                if self.role == "cleaner":
                    raise Block("the cleaner may only delete factory/* branches (git push is refused)")
                return
            targets = pos[1:]
            if not flag:
                if not all(a.startswith(":") or a.startswith("+:") for a in targets):
                    raise Block("git push mixing deletes and pushes")
                targets = [a.lstrip("+:") for a in targets]
        if self.role == "cleaner" and targets and all(
                FACTORY_BRANCH.match(t) and ".." not in t for t in targets):
            return
        raise Block("branch deletion is refused (the cleaner may delete only factory/<x> branches, every target "
                    "must match; other roles never)")

    def cmd(self, words, depth, piped, in_loose, hstrs=(), env_assigns=()):
        words = list(words)
        assigns = list(env_assigns)
        stripped = strip_prefix(words)
        assigns += [x for x in words[:len(words) - len(stripped)] if ASSIGN.match(x)]
        words = stripped
        for x in assigns if not words else []:
            if ASSIGN.match(x) and "+=" not in x.split("=", 1)[0] + "=":
                self.vars[x.split("=", 1)[0]] = x.split("=", 1)[1]
        if not words:
            if not self.hq:
                self.check_identity(assigns)
            return
        if self.pt:
            self.pt.command(words, self.hq)
        if not in_loose:
            self.priv(words, depth, piped, hstrs)
            if self.hq:
                return
        w, rest = words[0], words[1:]
        name = os.path.basename(w)
        extra = [x for x in rest if ASSIGN.match(x)] if name in DECLARERS else []
        for x in extra:
            self.vars[x.split("=", 1)[0]] = x.split("=", 1)[1]
        self.check_identity([*assigns, *extra])
        if name == "setsid":
            raise Block("setsid: re-parenting out of the agent process tree")
        if name == "disown":
            self.scrub_with_factory = "disown"
        self.check_scrub(name, rest)
        si = self.factory_script(name, rest) if INTERPRETERS.match(name) else None
        if name == "factory" or si is not None or (
                name in SHELLS and next((os.path.basename(x) for x in rest if not x.startswith("-")), None) == "factory"):
            self.ran_factory = True
        if is_dynamic(w, strict=not in_loose):
            raise DynamicBlock(f"dynamic command word {w!r} could expand to herdr or claude")
        if is_herdr(w):
            raise Block("direct herdr invocation")
        if is_claude(w):
            raise ClaudeBlock("direct claude invocation")
        name = os.path.basename(w)
        if name in ("cd", "pushd", "popd") and not in_loose:
            self.git_cd(name, rest)
            self.cfg_cd(name, rest)
        self.claude_launchers(name, rest)
        self.cfg_write(name, rest)
        self.persist_write(name, rest)
        self.factory_unit(name, rest, assigns)
        if name in COPIERS:  # R3: no copying/encoding/reading the real herdr binary
            for a in rest:
                a = a.split("=", 1)[1] if re.match(r"^(if|of)=", a) else a
                if is_herdr(a) or "HERDR_BIN_PATH" in a:
                    raise Block(f"{name} on the herdr binary")
        if name in SHELLS:
            saved = (self.gcwd, self.gdyn, self.ccwd)  # a cd inside `sh -c` does not move the caller
            try:
                self.shell(rest, depth, piped, hstrs)
            finally:
                self.gcwd, self.gdyn, self.ccwd = saved
        elif name == "eval":
            if "$" in " ".join(rest):
                raise Block("eval of an expansion: content cannot be inspected")
            self.script(" ".join(rest), depth + 1)
        elif name in ("source", "."):
            if rest:
                self.file(rest[0], depth)
        elif name == "systemctl":
            self.systemctl(rest)
        elif INTERPRETERS.match(name):
            self.interp_priv(name, rest)
            self.unittest_full(name, rest)
            for j, a in enumerate(rest[:-1]):
                if (re.match(r"^-[A-Za-z]*[ceEr]$", a) or a == "--eval") and "herdr" in rest[j + 1]:
                    raise Block("herdr in inline interpreter code")
        elif name in AWKS or name in EDITORS:
            if any("herdr" in a for a in rest):
                raise Block(f"herdr in {name} program text (can run shell commands)")
        elif name == "sed":
            for a in rest:
                if "herdr" in a and re.search(r"(?:^|[\s;{}\d$,])e\b|/[gIpmM]*e[gIpmM]*(?:\s|;|$)", a):
                    raise Block("herdr in a sed e command")
        elif name == "git":
            self.git_shared(words, assigns)  # first: its reason names the own-worktree advice
            self.git_delete(rest)
            self.git_persist(rest, depth)
            for j, a in enumerate(rest):
                if "herdr" in a and (rest[j - 1:j] == ["-c"] or "config" in rest[:j] or a.startswith("-c")):
                    raise Block("herdr in git config (alias/pager can run shell commands)")
        elif name in ("pkill", "killall"):
            raise Block(f"{name}: pattern kills hit other agents' processes (G-008 M1932); kill your own job "
                        "with `kill %<job>` or `kill $!`")
        elif name == "kill":
            self.kill(rest)
        elif "/" in w and not in_loose:
            self.exec_path(w, depth)
        if name == "command" and rest[:1] in (["-v"], ["-V"], ["-pv"]):
            return
        if name == "find":
            for j, a in enumerate(rest):
                if a in ("-exec", "-execdir", "-ok", "-okdir"):
                    self.loose(rest[j + 1:], depth)
        elif name in WRAPPERS and not in_loose:
            if name == "xargs":
                self.xargs(rest)
            self.loose(rest, depth, assigns)

    def unittest_full(self, name, rest):
        """T-0642: coder, qa and reviewer run no full suite locally (CI is the full gate). Blocked: `python -m unittest`
        with no test argument, `... discover` unless -p/--pattern names one module (no wildcard). Allowed:
        `python -m unittest tests.test_x` and `unittest discover -s tests -p test_x.py`."""
        if self.role not in ("coder", "qa", "reviewer") or not re.fullmatch(r"python[\d.]*", name):
            return
        args = None
        for i, tok in enumerate(rest):
            mm = re.fullmatch(r"-([A-Za-z]*m)(.*)", tok) if not tok.startswith("--") else None
            if tok == "-m" and rest[i + 1:i + 2]:
                mod, after = rest[i + 1], rest[i + 2:]
            elif mm and mm.group(2):  # attached form: -munittest
                mod, after = mm.group(2), rest[i + 1:]
            else:
                continue
            if mod in ("unittest", "unittest.__main__"):
                args = after
                break
        if args is None:
            return
        advice = ("; CI runs the full suite. Locally run `python3 ci/affected.py --base origin/main --quick --run`, or one "
                  "module: `python3 -m unittest discover -s tests -p test_X.py` (T-0642)")
        if args[:1] == ["discover"]:
            pats = [args[i + 1] for i, a in enumerate(args[:-1]) if a in ("-p", "--pattern")]
            pats += [a.split("=", 1)[1] for a in args if a.startswith("--pattern=")]
            if pats and all(re.fullmatch(r"test_[\w]+\.py", x) for x in pats):  # the last pattern wins, so all must be single-module
                return
            raise Block("`python3 -m unittest discover` without a single-module -p pattern runs the full suite" + advice)
        mods = []
        skip = False
        for a in args:
            if skip:
                skip = False
            elif a in ("-k", "--start-directory", "-s", "-t", "-p"):
                skip = True
            elif not a.startswith("-"):
                mods.append(a)
        if not mods:
            raise Block("`python3 -m unittest` with no test module runs the full suite" + advice)

    def kill(self, rest):
        """Worker mode (G-008 M1932): `kill` may only name the worker's own background job, `%<job>` or `$!`.
        A pid, 0, -1, a negative pid or process group, `$VAR` or a `$( )` could hit another agent's process, and
        a `kill` with no target (`xargs kill`) takes its targets from stdin. `kill -l` lists signals and is fine."""
        targets, sig_seen, i = [], False, 0
        while i < len(rest):
            a = rest[i]
            if a == "--":
                targets += rest[i + 1:]
                break
            if a in ("-l", "-L", "--list", "--table"):
                return
            if a in ("-s", "-n", "--signal"):
                sig_seen = True
                i += 2
                continue
            if a.startswith("--signal="):
                sig_seen = True
            elif re.match(r"^-(?:\d+|[A-Za-z][A-Za-z0-9_+]*)$", a) and not (sig_seen and a[1:].isdigit()):
                sig_seen = True  # the signal; a second -<digits> is a negative pid (a process group)
            else:
                targets.append(a)
            i += 1
        if targets and all(re.match(r"^(?:%\S*|\$!|\$\{!\})$", t) for t in targets):
            return
        raise Block("kill: workers may only kill their own background job (`kill %<job>` or `kill $!`); a pid, 0, "
                    "-1 or a process group could hit another agent (G-008 M1932)")

    def claude_launchers(self, name, rest):
        """npx/bunx/pnpm dlx/npm exec of the Claude Code package, or node running its cli.js."""
        if name in ("npx", "bunx", "pnpx") or (name in ("pnpm", "npm", "yarn") and
                                                any(a in ("dlx", "exec") for a in rest)):
            if any(CLAUDE_PKG in a for a in rest):
                raise ClaudeBlock(f"{name} of {CLAUDE_PKG}")
        elif name in ("node", "nodejs", "bun", "deno"):
            if any(CLAUDE_CLI.search(a) or CLAUDE_PKG in a for a in rest):
                raise ClaudeBlock(f"{name} running the Claude Code cli")

    def loose(self, rest, depth, env_assigns=()):
        for k in range(len(rest)):
            whole = True
            if ASSIGN.match(rest[k]):
                v = rest[k].split("=", 1)[1].lstrip("\\")
                if is_herdr(v):
                    raise Block("herdr assigned as a command (alias/env)")
                if is_claude(v):
                    raise ClaudeBlock("claude assigned as a command (alias/env)")
                if v:
                    # the value may be a whole command line (alias c="claude -p", alias c="ls;claude",
                    # alias c='"claude"'): judge it like a top-level command, quotes and backslashes removed
                    self.script(v, depth + 1)
                    whole = False
            if whole and re.search(r"\s", rest[k]):
                self.script(rest[k], depth + 1)
            self.cmd(rest[k:], depth, False, True,
                     env_assigns=[*env_assigns, *(x for x in rest[:k] if x.startswith("FACTORY_HOME="))])

    def xargs(self, rest):
        repl, j, first = None, 0, None
        while j < len(rest):
            a = rest[j]
            if a in ("-I", "-J"):
                repl = rest[j + 1] if j + 1 < len(rest) else None
                j += 2
                continue
            if a.startswith("-I") and len(a) > 2:
                repl = a[2:]
            elif a in ("-i", "--replace"):
                repl = "{}"
            elif a.startswith("--replace="):
                repl = a[10:]
            elif not a.startswith("-"):
                first = a
                break
            j += 1
        if repl is not None and first == repl:
            raise Block("xargs replacement string used as the command word")

    def shell(self, rest, depth, piped, hstrs=()):
        i = 0
        while i < len(rest):
            a = rest[i]
            if a == "--":
                i += 1
                break
            if a.startswith("-") and len(a) > 1:
                if not a.startswith("--") and "c" in a[1:]:
                    if i + 1 >= len(rest):
                        raise Block("shell -c without a payload: content cannot be inspected")
                    self.script(rest[i + 1], depth + 1)
                    return
                if a in ("-o", "-O", "--rcfile", "--init-file"):
                    i += 1
                i += 1
                continue
            break
        for h in hstrs:
            self.script(h, depth + 1)
        if i < len(rest):
            self.file(rest[i], depth)
        elif hstrs:
            return
        elif piped:
            raise Block("shell reading a pipe: content cannot be inspected")

    def read(self, path):
        p = os.path.expanduser(path)
        if not os.path.isabs(p):
            p = os.path.join(self.cwd, p)
        if not os.path.isfile(p) or os.path.getsize(p) > 1 << 20:
            raise Block(f"cannot inspect script {path}")
        with open(p, encoding="utf-8", errors="replace") as f:
            return f.read()

    def file(self, path, depth):
        if is_dynamic(path, strict=True):
            raise Block(f"dynamic script path {path!r}")
        self.script(self.read(path), depth + 1)

    def exec_path(self, path, depth):
        p = os.path.expanduser(path)
        p = p if os.path.isabs(p) else os.path.join(self.cwd, p)
        if not os.path.isfile(p):
            return
        with open(p, "rb") as f:
            head = f.read(200)
        first = head.split(b"\n", 1)[0]
        if first.startswith(b"#!") and re.search(rb"\b(ba|z|da|k|a)?sh\b", first):
            self.script(self.read(path), depth + 1)

    def systemctl(self, rest):
        args = [a for a in rest if not a.startswith("-")]
        if "isolate" in args:
            raise Block("systemctl isolate")
        verbs = [a for a in args if a in SYSTEMCTL_VERBS]
        if not verbs:
            return
        units = [a for a in args if a not in SYSTEMCTL_VERBS]
        if verbs[0] in SYSTEMCTL_NOUNIT:
            raise Block(f"systemctl {verbs[0]}")
        if not units:
            raise Block(f"systemctl {verbs[0]} without a unit")
        for u in units:
            if re.match(r"^(factory|herdr)", u) or GLOB.search(u) or "$" in u or "`" in u:
                raise Block(f"systemctl {verbs[0]} {u}")


def mask_quotes(text):
    """Same-length copy of text with quoted spans blanked to 'Q' (quotes kept), so splitting on it ignores quoted data."""
    out, q, i = [], None, 0
    while i < len(text):
        c = text[i]
        if q:
            if c == "\\" and q == '"' and i + 1 < len(text):
                out.append("QQ")
                i += 2
                continue
            if c == q:
                q = None
                out.append(c)
            else:
                out.append("Q" if c != "\n" else "\n")
        elif c == "\\" and i + 1 < len(text):
            out.append(text[i:i + 2])
            i += 2
            continue
        else:
            if c in "'\"":
                q = c
            out.append(c)
        i += 1
    return "".join(out)


SEP = re.compile(r"[;&|(){}`\n]+|\$\(")


def segments(text):
    """(start, end, masked words) of each simple-command-ish segment of text, split on unquoted separators."""
    m, pos, res = mask_quotes(text), 0, []
    for sep in list(SEP.finditer(m)) + [None]:
        end = sep.start() if sep else len(m)
        res.append((pos, end, m[pos:end].split()))
        pos = sep.end() if sep else end
    return res


def launcher_in_command_position(line):
    for _, _, words in segments(line):
        while words and (words[0] in RESERVED or ASSIGN.match(words[0])):
            words = words[1:]
        if not words:
            continue
        name = os.path.basename(words[0])
        if name in LAUNCHERS or (name == "find" and FIND_EXEC & set(words)):
            return True
        if name in SHELLS and any(re.match(r"^-[A-Za-z]*c[A-Za-z]*$", w) for w in words[1:]):
            return True
    return False


def r2_path_hit(cmd):
    for start, end, words in segments(cmd):
        if not words or os.path.basename(words[0]) not in DATA_CMDS:
            if R2_PATH.search(cmd[start:end]):
                return True
        elif R2_PATH.search(" ".join(words)):  # unquoted occurrences only
            return True
    return False


def claude_over_priv(cmd, cwd, role):
    """`sudo ... claude` is both a PRIV and a Claude start: raise the Claude block (T-0067 wording) if the
    worker pass finds one with the PRIV rule switched off; the caller re-raises the PrivBlock otherwise."""
    an = Analyzer(cwd, role)
    an.skip_priv = True
    try:
        an.script(cmd)
    except DynamicBlock:
        pass
    except ClaudeBlock:
        raise
    except Block:
        pass


EVAL_HOME_PREFIX = re.compile(r"""\A\s*FACTORY_HOME=(?:"[^"$`\\]*"|'[^']*'|[^\s;&|()<>`$"'\\]+)\s+factory(?=\s)""")


def evaluator_home_text(cmd):
    """Evaluator, closed by default: the token FACTORY_HOME may appear only in one leading
    `FACTORY_HOME=<temp> factory ...` prefix (its value is checked in evaluator_factory). Any other mention
    (export, declare, unset, env -u, read, printf -v, for, mapfile, getopts, eval, bash -c ...) is refused.
    A tripwire against accidental misuse, not a boundary: the sandbox hides the live home (see docs/skill-scout.md)."""
    if "FACTORY_HOME" not in cmd:
        return
    m = EVAL_HOME_PREFIX.match(cmd)
    if m and cmd.count("FACTORY_HOME") == 1:
        return
    raise Block("the evaluator may not mention FACTORY_HOME except as one leading prefix "
                "`FACTORY_HOME=<temp dir> factory <cmd>`; it cannot re-target or unset it")


SESSION_ID = None  # the hook payload's session_id: names the worker's own scratchpad


def in_scratchpad(path):
    """True when realpath `path` is the worker's own scratchpad or inside it: <tmp>/claude-<uid>/<project>/<session>/
    scratchpad. WORKER_GUARD_SCRATCH (the hook's own env, tests only) names it instead. No session: not decidable."""
    override = os.environ.get("WORKER_GUARD_SCRATCH")
    if override:
        return under(path, os.path.realpath(override))
    if not SESSION_ID or not re.fullmatch(r"[\w.-]+", SESSION_ID) or SESSION_ID in (".", ".."):
        return False
    parts = path.split(os.sep)
    return (len(parts) >= 6 and parts[:3] == ["", "tmp", f"claude-{os.getuid()}"] and parts[4] == SESSION_ID
            and parts[5] == "scratchpad" and parts[3] not in ("", ".", ".."))


# ───────────────────────────── public text + frozen repo (G-021 T2) ─────────────────────────────

class PublicBlock(Block):
    """The public-text / frozen-repo rule. Never swallowed by HQ mode (the one HQ rule that blocks on parse trouble)."""


PT_TEXT_FLAGS = {"--title", "-t", "--body", "-b", "--subject", "--comment", "-c", "--notes", "-n", "--message", "-m",
                 "--description", "--field-text"}
PT_FILE_FLAGS = {"--body-file", "-F", "--notes-file", "--file", "--input"}
PT_GH_WRITES = {"pr": {"create", "edit", "comment", "review", "close", "merge"},
                "issue": {"create", "edit", "comment", "close"}, "release": {"create", "edit"}}
PT_GIT_SUBS = {"commit", "tag", "push", "merge", "commit-tree", "notes"}
PT_DYNAMIC = re.compile(r"`|\$[A-Za-z_{(@*#?!0-9$-]")
PT_CAT_HEREDOC = re.compile(r"\A\$\(\s*cat\s+<<-?\s*(['\"]?)(\w+)\1[ \t]*\n(.*?)\n[ \t]*\2[ \t]*\n?\s*\)\Z", re.S)
PT_WRITE_HINT = re.compile(r"\b(?:git\s+(?:-\S+\s+\S+\s+)*(?:commit|tag|push|merge)|gh\s+(?:pr|issue|release|api))\b")
PT_REMOTE = re.compile(r"^(?:https?://(?:[^/@]+@)?[^/]+/|ssh://(?:[^/@]+@)?[^/]+/|git://[^/]+/|[\w.-]+@[^:/]+:)"
                       r"([\w.-]+)/([\w.-]+?)(?:\.git)?/?$")
PT_STDIN_PATH = re.compile(r"^/(?:dev/(?:stdin|fd/\d+)|proc/[^/]+/fd/\d+)$")
PT_MSG_NO_TERMS = "public-text-guard: no owner terms loaded"


class PublicText:
    """Public-text rule for git/gh writes. Off (no work, no subprocess) while public_text_repos and frozen_repos are empty."""

    def __init__(self, cwd, db_path):
        self.cwd = cwd or os.getcwd()
        self.dirs = {self.cwd}  # every directory a `cd` named: a subshell or group may have left it, so each counts
        self.pcwd = self.cwd  # where a git command runs; None after a `cd` whose target is only known at run time
        self.cfg = {}
        try:
            con = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True, timeout=2)
            try:
                self.cfg = dict(con.execute("SELECT key, value FROM config WHERE key IN "
                                            "('public_text_repos','frozen_repos','scrub_terms_file')"))
            finally:
                con.close()
        except sqlite3.Error:
            # No DB file / no config table yet: the rule stays off, as before it existed. Failing closed here would
            # lock every agent out of every git/gh command whenever the DB is briefly locked or missing.
            self.cfg = {}
        self.public = self.slugs("public_text_repos")
        self.frozen = self.slugs("frozen_repos")
        self.active = bool(self.public or self.frozen)
        self.bodies = []  # (head line, body) of every heredoc seen by preprocess
        self.raw = ""  # the command as the hook received it
        self._mod = self._terms = None

    def slugs(self, key):
        return {s.strip().lower() for s in (self.cfg.get(key) or "").split(",") if s.strip()}

    # ── target repo
    def git_out(self, cwd, *args):
        import subprocess
        try:
            r = subprocess.run(["git", "-C", cwd, *args], capture_output=True, text=True, timeout=10)
        except (OSError, subprocess.SubprocessError):
            return None
        return r.stdout.strip() if r.returncode == 0 else None

    def remote_slug(self, cwd, remote, push=False):
        if cwd is None or not os.path.isdir(cwd):
            return None
        url = self.git_out(cwd, "remote", "get-url", *(["--push"] if push else []), remote)
        m = PT_REMOTE.match(url) if url else None
        return f"{m.group(1)}/{m.group(2)}".lower() if m else None

    def target(self, cwd, remote="origin", explicit=None):
        """OWNER/NAME (lower case) of the repo a write goes to, or None when it cannot be told."""
        if explicit:
            m = re.fullmatch(r"(?:[^/]+/)?([\w.-]+)/([\w.-]+?)(?:\.git)?", explicit.strip())
            return f"{m.group(1)}/{m.group(2)}".lower() if m else None
        return self.remote_slug(cwd, remote)

    # ── text of one write
    @staticmethod
    def flag_values(args, text_flags, file_flags):
        """([text values], [file paths]) of the flags in args; --flag=v, -fVALUE and `-f v` forms."""
        texts, files, j = [], [], 0
        while j < len(args):
            a, val, kind = args[j], None, None
            if a.startswith("--") and "=" in a and a.split("=", 1)[0] in text_flags | file_flags:
                a, val = a.split("=", 1)
            elif a in text_flags | file_flags:
                if j + 1 < len(args):
                    val = args[j + 1]
                    j += 1
                else:
                    val = ""
            elif re.fullmatch(r"-[a-zA-Z]+", a) and a != "--":
                letters = a[1:]
                for k, ch in enumerate(letters):  # -am "msg", -mMSG, -bBODY
                    if "-" + ch in text_flags | file_flags:
                        rest = letters[k + 1:]
                        if rest:
                            val = rest.lstrip("=") if rest.startswith("=") else rest
                        elif j + 1 < len(args):
                            val = args[j + 1]
                            j += 1
                        else:
                            val = ""
                        a = "-" + ch
                        break
            if val is not None:
                (texts if a in text_flags else files).append(val)
            j += 1
        return texts, files

    def piece(self, value, label):
        """Text of one flag value: literal, or the body of a whole `$(cat <<'EOF' ... EOF)`; dynamic text blocks."""
        m = PT_CAT_HEREDOC.match(value)
        if m:
            return m.group(3)
        if PT_DYNAMIC.search(value):
            raise PublicBlock(f"dynamic text in {label} cannot be inspected: write the text to a file and pass --body-file/-F")
        return value

    def collect(self, texts, files, head_words):
        out = [self.piece(t, "a text flag") for t in texts]
        for f in files:
            if f == "-" or PT_STDIN_PATH.match(os.path.normpath(f)):  # /dev/stdin, /dev/fd/N, /proc/*/fd/N are the heredoc payload
                found = [b for h, b in self.bodies if all(w in re.split(r"[;&|(]", h)[-1].split() for w in head_words)]
                if not found:
                    raise PublicBlock("stdin text cannot be inspected: write the text to a file and pass --body-file/-F")
                out.extend(found)
                continue
            if not f.strip():
                raise PublicBlock("an empty or process-substitution text file (-F <(...)) cannot be inspected: write the text to a file first and pass --body-file/-F")
            path = f if os.path.isabs(f) else os.path.join(self.cwd if self.pcwd is None else self.pcwd, f)
            out.append(self.body_file_text(f, path, head_words))
        return out

    # ── a body file is read at hook time, so the command must be exactly the one gh/git call
    @staticmethod
    def standalone(raw, head):
        """True when raw is one plain `head ...` command: no chain, pipe, background, substitution, process substitution or
        newline outside quotes, so nothing in the same command can change the file between this check and the write."""
        s = re.sub(r"\d*>&\d+|\d*>\s*/dev/null", " ", raw.strip())  # 2>&1 and 2>/dev/null change no file
        q = None
        i = 0
        while i < len(s):
            c = s[i]
            if q == "'":
                q = None if c == "'" else q
            elif q == '"':
                if c == "\\":
                    i += 1
                elif c == '"':
                    q = None
                elif c == "`" or s.startswith("$(", i):
                    return False
            elif c == "\\":
                if s[i + 1:i + 2] == "\n":
                    return False
                i += 1
            elif c in "'\"":
                q = c
            elif c in ";&|\n`" or s.startswith(("$(", "<(", ">("), i):
                return False
            i += 1
        if q:
            return False
        try:
            words = shlex.split(s)
        except ValueError:
            return False
        words = [w for w in words if not ASSIGN.match(w)] if words and ASSIGN.match(words[0]) else words
        return bool(words) and os.path.basename(words[0]) == head

    def body_file_text(self, f, path, head_words):
        if not self.standalone(self.raw, head_words[0]):
            raise PublicBlock(f"text file {f!r} is read before the command runs, so the command must be only the {head_words[0]} call "
                              "(no ; && || | & $( ` <( >( or newline): write the file in a separate command first")
        real = os.path.realpath(path)
        try:
            if os.path.islink(path) or os.path.islink(real):
                raise PublicBlock(f"text file {f!r} is a symlink: pass a regular --body-file/-F")
            st = os.stat(real)
            if not stat.S_ISREG(st.st_mode):  # a FIFO or device would hang the hook or read the wrong bytes
                raise PublicBlock(f"text file {f!r} is not a regular file: pass a regular --body-file/-F")
            if st.st_nlink > 1:
                raise PublicBlock(f"text file {f!r} has other hard links, so its content may change elsewhere: pass a file with one link")
            with open(real, encoding="utf-8", errors="replace") as fh:
                return fh.read()
        except OSError as e:
            raise PublicBlock(f"cannot read text file {f!r} ({e.strerror}): pass a readable --body-file/-F")

    # ── terms + checker
    def guard(self):
        if self._mod is None:
            import importlib.util
            path = os.path.join(os.path.dirname(os.path.dirname(os.path.dirname(HOOK_REAL))), "ci", "public_text_guard.py")
            try:
                spec = importlib.util.spec_from_file_location("public_text_guard_mod", path)
                mod = importlib.util.module_from_spec(spec)
                spec.loader.exec_module(mod)
            except Exception as e:  # any failure to import the checker fails closed
                raise PublicBlock(f"public-text-guard cannot be loaded ({type(e).__name__}); fix ci/public_text_guard.py")
            self._mod = mod
        if self._terms is None:
            try:
                self._terms = self._mod.load_terms_or_fail(os.environ, self.cfg.get("scrub_terms_file") or None)
            except self._mod.UsageError as e:
                raise PublicBlock(str(e))
        return self._mod, self._terms

    def verdict(self, texts=(), refs=(), pushes=()):
        mod, terms = self.guard()
        hits = []
        for t in texts:
            hits += mod.check_text(t, terms)
        for r in refs:
            hits += mod.check_ref(r, terms)
        for repo, base, head in pushes:
            try:
                hits += mod.check_push(repo, base, head, terms)
            except mod.UsageError as e:
                raise PublicBlock(f"cannot check the push ({e})")
        if hits:
            ids = sorted({h[0] for h in hits})
            raise PublicBlock(f"public text: {len(hits)} hits: {', '.join(ids)}")

    # ── per command
    def also(self, slug, remote, extra):
        """slug plus the repos of the other directories seen in the command (only those that are known repos)."""
        pushed = self.remote_slug(self.pcwd, remote, push=True)  # a pushurl may differ from the fetch url
        base = [slug] + ([pushed] if pushed and pushed != slug else [])
        if not extra:
            return base
        return base + [s for s in (self.remote_slug(d, remote) for d in sorted(self.dirs)) if s]

    def frozen_hit(self, slug, remote="origin", extra=True):
        for s in self.also(slug, remote, extra):
            if s and s in self.frozen:
                raise PublicBlock(f"repo {s} is frozen, G-021; work in the public repo")

    def applies(self, slug, remote="origin", extra=True):
        if not self.public:
            return False
        ss = self.also(slug, remote, extra)
        return ss[0] is None or any(s in self.public for s in ss)

    def cd(self, rest):
        args = [a for a in rest if not a.startswith("-")]
        tgt = args[0] if args else None
        if tgt is None or PT_DYNAMIC.search(tgt) or tgt == "-" or self.pcwd is None:
            self.pcwd = None if tgt else self.pcwd
        else:
            self.pcwd = os.path.normpath(os.path.join(self.pcwd, os.path.expanduser(tgt)))
            self.dirs.add(self.pcwd)

    def command(self, words, hq):
        """Entry from Analyzer.cmd with the command word first (env prefixes already stripped)."""
        if not self.active or not words:
            return
        words = list(words)
        while hq and words and os.path.basename(words[0]) in WRAP_VALUE_OPTS:  # HQ mode does not recurse into wrappers
            name = os.path.basename(words.pop(0))
            while words and (words[0].startswith("-") or ASSIGN.match(words[0]) or
                             (name in ("timeout", "nice") and re.fullmatch(r"[\d.]+[smhd]?", words[0]))):
                opt = words.pop(0)
                if opt in WRAP_VALUE_OPTS[name] and words:
                    words.pop(0)
        if not words:
            return
        name, rest = os.path.basename(words[0]), words[1:]
        if name == "cd":
            return self.cd(rest)
        if hq and name in SHELLS | {"eval"}:
            payload = " ".join(rest) if name == "eval" else next(
                (rest[i + 1] for i, a in enumerate(rest[:-1]) if re.fullmatch(r"-[a-z]*c[a-z]*", a)), None)
            if payload:
                saved = self.pcwd
                try:
                    an = Analyzer(self.cwd, None, True)
                    an.pt = self
                    an.script(payload, 1)
                except PublicBlock:
                    raise
                except Block:
                    if PT_WRITE_HINT.search(payload):
                        raise PublicBlock("a write command to a public repo cannot be parsed; use plain git/gh commands")
                finally:
                    self.pcwd = saved
            return
        if name == "git":
            self.git(rest)
        elif name == "gh":
            self.gh(rest)

    def git(self, args):
        cwd, j, alias = self.pcwd, 0, False
        while j < len(args) and args[j].startswith("-"):
            a = args[j]
            if a == "-C" and j + 1 < len(args) and cwd is not None:
                cwd = os.path.normpath(os.path.join(cwd, args[j + 1]))
                j += 1
            elif a == "-C":
                cwd = None  # -C without a value or from an unknown directory
            elif a in ("--git-dir", "--work-tree") or a.startswith(("--git-dir=", "--work-tree=")):
                val = a.split("=", 1)[1] if "=" in a else (args[j + 1] if j + 1 < len(args) else "")
                if a.startswith("--git-dir") and val and cwd is not None:
                    self.dirs.add(os.path.normpath(os.path.join(cwd, val)))
                j += 0 if "=" in a else 1
            elif a in ("-c", "--namespace", "--exec-path"):
                if a == "-c" and j + 1 < len(args) and args[j + 1].lower().startswith("alias."):
                    alias = True
                j += 1
            j += 1
        if j >= len(args):
            return
        if args[j] not in PT_GIT_SUBS and not alias:
            return
        sub, rest = args[j], args[j + 1:]
        if sub == "tag" and not any(re.fullmatch(r"-[a-zA-Z]*[amsF]|--(?:message|file)(?:=.*)?", a) for a in rest):
            return  # a lightweight local tag is not a write to the remote; its name is checked when it is pushed
        remote = "origin"
        if sub == "push":
            pos = self.push_args(rest)[0]
            remote = pos[0] if pos else "origin"
        url_remote = sub == "push" and bool(remote and re.match(r"[\w.-]+://|[\w.-]+@", remote))
        if sub == "push" and remote and re.match(r"[\w.-]+://|[\w.-]+@", remote):
            m = PT_REMOTE.match(remote)
            slug = "/".join(m.groups()).lower() if m else None
        else:
            slug = self.remote_slug(cwd, remote)
        direct = bool(url_remote)
        self.frozen_hit(slug, remote, not direct)
        if not self.applies(slug, remote, not direct):
            return
        if alias:
            raise PublicBlock("git -c alias.* hides the subcommand from the public-text check: run the real subcommand")
        texts, files = self.flag_values(rest, {"-m", "--message"}, {"-F", "--file"})
        if sub != "push":
            pieces = self.collect(texts, files, ["git", sub])
            if sub == "tag":
                pos = [a for a in rest if not a.startswith("-")]
                refs = pos[:1]
            else:
                refs = []
            self.verdict(pieces, refs)
        else:
            self.push(cwd, remote, rest)

    @staticmethod
    def push_args(rest):
        pos, opts, j, repo = [], [], 0, None
        while j < len(rest):
            a = rest[j]
            if a in ("--repo", "-o", "--push-option", "--receive-pack", "--exec") and j + 1 < len(rest):
                opts.append(a)
                if a == "--repo":
                    repo = rest[j + 1]
                j += 2
                continue
            if a.startswith("--repo="):
                repo = a.split("=", 1)[1]
            if a.startswith("-") and a != "-":
                opts.append(a)
            else:
                pos.append(a)
            j += 1
        if repo is not None:  # --repo replaces the remote: every positional is a refspec
            pos = [repo, *pos]
        return pos, opts

    def push(self, cwd, remote, rest):
        if cwd is None or not os.path.isdir(cwd):
            raise PublicBlock("push from a directory known only at run time: cd to a static path first")
        pos, opts = self.push_args(rest)
        specs = pos[1:]
        delete = any(o in ("--delete", "-d") for o in opts)
        if any(o in ("--all", "--mirror", "--tags") for o in opts) and not specs:
            sel = ["refs/heads"] if "--all" in opts else (["refs/tags"] if "--tags" in opts else ["refs/heads", "refs/tags"])
            names = (self.git_out(cwd, "for-each-ref", "--format=%(refname)", *sel) or "").split()
            specs = [n + ":" + n for n in names]
        if not specs:
            cur = self.git_out(cwd, "symbolic-ref", "--short", "HEAD")
            if not cur:
                raise PublicBlock("push with no refspec from a detached HEAD: name the branch to push")
            specs = [cur]
        refs, pushes = [], []
        for spec in specs:
            s = spec.lstrip("+")
            src, _, dst = s.partition(":") if ":" in s else (s, "", s)
            if delete:
                src, dst = "", s
            dst_name = re.sub(r"^refs/(?:heads|tags)/", "", dst)
            if dst in ("", "HEAD") and src:
                dst_name = self.git_out(cwd, "symbolic-ref", "--short", "HEAD") or src
            if dst_name:
                refs.append(dst_name)
            if not src:
                continue  # a delete publishes no commits
            head = self.git_out(cwd, "rev-parse", "--verify", "--quiet", src + "^{commit}")
            if not head:
                raise PublicBlock(f"cannot resolve push source {src!r}: push an existing branch or commit")
            base = self.git_out(cwd, "rev-parse", "--verify", "--quiet", f"refs/remotes/{remote}/{dst_name}^{{commit}}")
            if not base:
                base = self.git_out(cwd, "merge-base", head, "origin/main")
            if not base:
                raise PublicBlock(f"no base to check the push against: fetch origin so origin/main exists")
            pushes.append((cwd, base, head))
        self.verdict((), refs, pushes)

    def gh(self, args):
        a, j, repo = list(args), 0, None
        # -R/--repo anywhere in the command
        for k, x in enumerate(a):
            if x in ("-R", "--repo") and k + 1 < len(a):
                repo = a[k + 1]
            elif x.startswith("--repo="):
                repo = x.split("=", 1)[1]
            elif re.fullmatch(r"-R\S+", x):
                repo = x[2:]
        pos = [x for x in a if not x.startswith("-")]
        if len(pos) < 2 and not (pos and pos[0] == "api"):
            return
        if pos[0] == "api":
            return self.gh_api(a, repo)
        if pos[0] not in PT_GH_WRITES or pos[1] not in PT_GH_WRITES[pos[0]]:
            return
        slug = self.target(self.pcwd, explicit=repo) if repo else self.remote_slug(self.pcwd, "origin")
        self.frozen_hit(slug, extra=not repo)
        if not self.applies(slug, extra=not repo):
            return
        tf = PT_TEXT_FLAGS - {"--comment", "-c"} if pos[:2] == ["pr", "review"] else PT_TEXT_FLAGS  # a switch there
        texts, files = self.flag_values(a, tf, PT_FILE_FLAGS)
        refs = []
        for k, x in enumerate(a):
            if x in ("--head", "-H") and k + 1 < len(a):
                refs.append(a[k + 1].split(":")[-1])
        if pos[0] == "release":
            refs += pos[2:3]
        pieces = self.collect(texts, files, ["gh", pos[0]])
        for r in refs:
            if PT_DYNAMIC.search(r):
                raise PublicBlock("dynamic ref name cannot be inspected: write the name out")
        self.verdict(pieces, refs)

    def gh_api(self, a, repo):
        method, texts, files, endpoint = None, [], [], None
        j = 0
        while j < len(a):
            x = a[j]
            if x in ("-X", "--method") and j + 1 < len(a):
                method = a[j + 1].upper()
                j += 1
            elif x.startswith("--method="):
                method = x.split("=", 1)[1].upper()
            elif x in ("-f", "-F", "--field", "--raw-field") and j + 1 < len(a):
                kv = a[j + 1]
                j += 1
                k, _, v = kv.partition("=")
                if x in ("-F", "--field") and v.startswith("@"):
                    files.append(v[1:])
                else:
                    texts.append(v)
            elif re.fullmatch(r"--(?:field|raw-field)=.*", x):
                k, _, v = x.split("=", 1)[1].partition("=")
                texts.append(v)
            elif x == "--input" and j + 1 < len(a):
                files.append(a[j + 1])
                j += 1
            elif x.startswith("--input="):
                files.append(x.split("=", 1)[1])
            elif x in ("-H", "--header", "-q", "--jq", "-t", "--template", "--hostname", "--cache", "-R", "--repo") \
                    and j + 1 < len(a):
                j += 1
            elif not x.startswith("-") and x != "api" and endpoint is None:
                endpoint = x
            j += 1
        has_body = bool(texts or files)
        if not (has_body or method in ("POST", "PATCH", "PUT", "DELETE")) or method == "GET":
            return
        m = re.match(r"/?repos/([\w.-]+)/([\w.-]+)", endpoint or "")
        slug = f"{m.group(1)}/{m.group(2)}".lower() if m else (self.target(None, explicit=repo) if repo else
                                                              self.remote_slug(self.pcwd, "origin"))
        self.frozen_hit(slug, extra=not (m or repo))
        if not self.applies(slug, extra=not (m or repo)):
            return
        self.verdict(self.collect(texts, files, ["gh", "api"]) + ([endpoint] if endpoint and not m else []))



def check(cmd, cwd=None, role=None, hq=False):
    pt = PublicText(cwd, Analyzer(cwd).db_path())
    pt.raw = cmd
    try:  # PRIV pass (the whole of HQ mode): parse trouble allows
        an = Analyzer(cwd, role, True)
        an.unit_rule = hq
        an.pt = pt if hq else None
        an.script(cmd)
    except PrivBlock:
        if not hq:
            claude_over_priv(cmd, cwd, role)
        raise
    except PublicBlock:
        raise
    except Block:
        if hq and pt.active and PT_WRITE_HINT.search(cmd):  # the one HQ rule that blocks on parse trouble
            slug = pt.remote_slug(cwd, "origin")
            pt.frozen_hit(slug)
            if pt.applies(slug):
                raise PublicBlock("a write command to a public repo cannot be parsed; use plain git/gh commands")
    if hq:
        return
    if role in TERMS_ROLES:
        terms_bash(cmd, cwd)
    if role == "evaluator":
        evaluator_home_text(cmd)
    # layer 2: raw text, independent of the parser
    if R2_SET.search(cmd) or r2_path_hit(cmd):
        raise Block("R2: re-targeting the herdr socket/session or naming herdr's socket/config path")
    if R2_UNSET.search(cmd) and HERDR_WORD.search(cmd):
        raise Block("R2: unsetting HERDR_SOCKET_PATH in a command that mentions herdr")
    if R3_REDIR.search(cmd):
        raise Block("R3: reading the herdr binary through a redirection")
    for line in re.sub(r"\\\n", " ", cmd).splitlines() or [cmd]:
        if R1.search(line) and launcher_in_command_position(line):
            raise Block("R1: herdr with a mutating subcommand behind an exec-capable launcher")
    an = Analyzer(cwd, role)
    an.pt = pt
    try:
        an.script(cmd)
    except PrivBlock:
        claude_over_priv(cmd, cwd, role)
        raise
    if an.scrub_with_factory and an.ran_factory:
        raise Block(f"{an.scrub_with_factory} in a command line that runs factory (hides the worker from detection)")


# T-0600: spawn exports SCRUB_TERMS_FILE to workers so the scanners can load the owner terms; nobody but those scanners
# may read the file, or one `cat` would put the terms into the transcript.
TERMS_ROLES = {"coder", "qa", "reviewer", "judge", "pm"}
TERMS_VAR = "SCRUB_TERMS_FILE"
TERMS_SCANNER = re.compile(r"(?:^|/)ci/(?:scrub_scan|public_text_guard)\.py$")
TERMS_ENV_ASSIGN = re.compile(r"^[A-Za-z_]\w*=")
TERMS_WHY = ("the owner-terms file may only be read by ci/scrub_scan.py and ci/public_text_guard.py (run them with "
             "SCRUB_TERMS_FILE as spawn exported it); ask your lead if you need a scan")


def terms_paths(cwd):
    """Every spelling of the configured/exported terms path; empty when none is set."""
    out = set()
    try:
        cfg = PublicText(cwd, Analyzer(cwd).db_path()).cfg
    except (OSError, sqlite3.Error):
        cfg = {}
    for v in (cfg.get("scrub_terms_file"), os.environ.get(TERMS_VAR)):
        if v and v.strip():
            v = os.path.expanduser(v.strip())
            out |= {v, os.path.normpath(v), os.path.realpath(v)}
    return out


def terms_segment_ok(seg):
    """A command segment that names the terms file is fine only when it runs one of the two scanners."""
    if re.search(r"`|\$\(|<", seg):
        return False
    try:
        toks = shlex.split(seg)
    except ValueError:
        return False
    while toks and (TERMS_ENV_ASSIGN.match(toks[0]) or toks[0] == "env" or (toks[0].startswith("-") and len(toks) > 1)):
        toks.pop(0)
    if toks and re.fullmatch(r"(?:.*/)?python3?", toks[0]):
        toks.pop(0)
    return bool(toks) and bool(TERMS_SCANNER.search(toks[0]))


def terms_bash(cmd, cwd):
    paths = terms_paths(cwd)
    for seg in re.split(r"[;&|\n]+", re.sub(r"(?:\d*>&\d*-?|&>)", " ", cmd)):
        if (TERMS_VAR in seg or any(p in seg for p in paths)) and not terms_segment_ok(seg):
            raise PublicBlock(f"reading the owner-terms file is refused: {TERMS_WHY}")


def terms_tool(data):
    """Read/Grep tool call that would read the terms file (Grep over a directory holding it counts)."""
    ti = data.get("tool_input")
    ti = ti if isinstance(ti, dict) else {}
    cwd = data.get("cwd") if isinstance(data.get("cwd"), str) else os.getcwd()
    paths = terms_paths(cwd)
    if not paths:
        return 0
    raw = ti.get("file_path") if data.get("tool_name") == "Read" else (ti.get("path") or cwd)
    if not isinstance(raw, str) or not raw:
        sys.stderr.write("worker-guard: blocked (tool call without a readable path, failing closed)\n")
        return 2
    p = os.path.realpath(os.path.join(cwd, os.path.expanduser(raw.replace("${SCRUB_TERMS_FILE}", "$SCRUB_TERMS_FILE"))))
    if TERMS_VAR in raw or p in paths or any(t.startswith(p.rstrip("/") + "/") for t in paths):
        sys.stderr.write(f"worker-guard: blocked ({TERMS_WHY})\n")
        return 2
    return 0


def persist_edit(data):
    """T-0334: an Edit/Write/MultiEdit/NotebookEdit call on a protected config/init file is refused. A payload without a
    readable path cannot be decided and fails closed."""
    ti = data.get("tool_input")
    ti = ti if isinstance(ti, dict) else {}
    path = ti.get("file_path") or ti.get("notebook_path")
    if not isinstance(path, str) or not path:
        sys.stderr.write("worker-guard: blocked (edit tool call without a readable file_path, failing closed)\n")
        return 2
    p = re.sub(r"^(~|\$HOME|\$\{HOME\})(?=/|$)", lambda m: PW_DIR, path)
    cwd = data.get("cwd") if isinstance(data.get("cwd"), str) else os.getcwd()
    if not os.path.isabs(p):
        p = os.path.join(cwd, p)
    if persist_path(p) or ("$" in p and persist_code(p)):
        why = REPO_CFG_WHY if repo_cfg_path(os.path.realpath(p)) or repo_cfg_path(os.path.normpath(p)) else PERSIST_WHY
        sys.stderr.write(f"worker-guard: blocked ({path!r}): {why}. Tell your lead if this is wrong.\n")
        return 2
    return 0


def main():
    role = None
    hq = "--hq" in sys.argv[1:]
    if "--role" in sys.argv[1:-1]:
        role = sys.argv[sys.argv.index("--role") + 1]
    claude = dynamic = False
    try:
        data = json.load(sys.stdin)
        if not isinstance(data, dict):
            raise ValueError("not an object")
        if data.get("tool_name") in PERSIST_EDIT_TOOLS and not hq:
            return persist_edit(data)
        if data.get("tool_name") in ("Read", "Grep") and not hq:
            return terms_tool(data) if role in TERMS_ROLES else 0
        if data.get("tool_name") != "Bash":
            return 0
        cmd = (data.get("tool_input") or {}).get("command")
        if not isinstance(cmd, str):
            raise ValueError("no command string")
        global SESSION_ID
        SESSION_ID = data.get("session_id") if isinstance(data.get("session_id"), str) else None
        check(cmd, data.get("cwd"), role, hq)
        return 0
    except UnitBlock as e:
        sys.stderr.write(f"worker-guard: blocked (`{e}`): mutating systemctl/systemd-run is not allowed for "
                         "factory agents; the owner manages units (M2108)\n")
        return 2
    except PrivBlock as e:
        sys.stderr.write(f"worker-guard: blocked: privilege escalation (`{e}`) is not allowed for factory agents "
                         "(owner M803)\n")
        return 2
    except GitBlock as e:
        sys.stderr.write(f"worker-guard: blocked {e}\n")
        return 2
    except PublicBlock as e:
        sys.stderr.write(f"worker-guard: blocked ({e})\n")
        return 2
    except Block as e:
        why = str(e)
        claude = isinstance(e, ClaudeBlock)
        dynamic = isinstance(e, DynamicBlock)
    except Exception as e:  # fail closed: a crash (exit 1) would let the call through
        why = f"hook error, failing closed: {type(e).__name__}: {e}"
    if dynamic:
        sys.stderr.write(f"worker-guard: blocked ({why}). Workers may not run herdr, and only factory spawn "
                         "starts Claude (lesson M347); use the factory CLI (factory done, factory msg, ...) "
                         "and ask your lead.\n")
        return 2
    if claude:
        sys.stderr.write(f"worker-guard: blocked ({why}). only factory spawn starts Claude (lesson M347); "
                         "ask your lead.\n")
        return 2
    sys.stderr.write(
        f"worker-guard: blocked ({why}). Workers may not run herdr or manage factory/herdr systemd units "
        "directly; use the factory CLI (factory done, factory msg, ...). Tell your lead if this is wrong.\n")
    return 2


if __name__ == "__main__":
    sys.exit(main())
