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
Factory unit commands: a command word `factory` (or a path with that basename, also `python3 <path>/factory`, `<path>/factory_cli.py`, `-m factory_cli`)
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
Coder push pin (T-0689): --role coder may run only `git push origin HEAD:factory/<own task id>` (id from FACTORY_AGENT=
coder-t-NNNN; optional `git -C <dir>`); any other refspec, remote, URL, force form, --all/--mirror/--tags/--delete,
option, -c override or GIT_CONFIG_* assignment is blocked, as is a push when the task id is unknown.
T-0743: the hook cwd and any -C target must be the worktree recorded for the task in the factory DB (read-only);
an unresolvable task/worktree refuses.
"""
import collections
import fnmatch
import glob
import json
import os
import pwd
import re
import shlex
import sqlite3
import stat
import sys
import urllib.parse

MAX_DEPTH = 6
FACTORY_UNIT_CMDS = {"up", "tick", "relaunch", "spawn", "clean"}
# broad reads of the live home: subcommand -> second words that are blocked (None = any / no second word)
BROAD_READS = {"res": None, "agents": None, "scorecard": None, "digest": None, "config": None,
               "flags": None,
               "goal": {"list"}, "task": {"list", "ready"}, "team": {"list"},
               "project": {"list"}, "lesson": {"list"}}
PRIV_WORDS = {"sudo", "su", "doas", "pkexec", "docker"}
FACTORY_BRANCH = re.compile(r"^factory/[A-Za-z0-9._/-]+$")
GIT_PIN_FLAGS = {"-p", "--paginate", "-P", "--no-pager", "--no-replace-objects", "--bare", "--literal-pathspecs",
                 "--glob-pathspecs", "--noglob-pathspecs", "--icase-pathspecs", "--no-optional-locks",
                 "--no-lazy-fetch", "--no-advice", "--html-path", "--man-path", "--info-path", "--version",
                 "--help", "-v", "-h"}
# wrappers whose options are modelled, so PRIV sees the command they run and not their option values:
# name -> options that take a value (as a separate word)
WRAP_VALUE_OPTS = {
    "env": {"-u", "-C", "--unset", "--chdir"}, "exec": {"-a"}, "nohup": set(), "command": set(), "builtin": set(),
    "setsid": set(), "timeout": {"-s", "-k", "--signal", "--kill-after"}, "nice": {"-n", "--adjustment"},
    "ionice": {"-c", "-n", "-p", "-P", "-u", "--class", "--classdata"}, "stdbuf": {"-i", "-o", "-e"},
    "xargs": {"-I", "-J", "-n", "-P", "-d", "-L", "-s", "-E", "-a", "-R", "-S", "--max-args", "--max-procs",
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
# T-1035: `git config` read classifier. Git parses options with parse-options (unique abbreviations, value options that
# eat the next word, `--`, short bundles); the guard does NOT mirror that. It proves a command is a read from an exact
# table, and anything it cannot prove is a write (fail closed).
CONFIG_FLAGS = {"--get", "--get-all", "--get-regexp", "--get-urlmatch", "--get-color", "--get-colorbool", "--list", "-l",
                "--show-origin", "--show-scope", "--name-only", "--all", "--regexp", "--global", "--local", "--system",
                "--worktree", "--null", "-z", "--bool", "--int", "--bool-or-int", "--bool-or-str", "--path",
                "--expiry-date", "--includes", "--no-includes"}
# T-1064: exact table of the output/scope modifiers that may also FOLLOW a positional (`--get alias.x --show-origin`,
# `list --show-origin`, `get --all alias.x`). Action flags and everything else stay refused after a positional, so
# `alias.x push --get` stays a refusal. `--regexp` is a `get` subcommand option: config_is_read accepts it only there.
CONFIG_TRAILING = {"--show-origin", "--show-scope", "--name-only", "--all", "--regexp", "--global", "--local", "--system",
                   "--worktree", "--null", "-z", "--bool", "--int", "--bool-or-int", "--bool-or-str", "--path",
                   "--expiry-date", "--includes", "--no-includes"}
CONFIG_PARSE_VALUE_OPTS = {"--type", "-t", "--default", "--comment", "--blob", "--value", "--file", "-f"}
# Mirrored by the Go port through go/internal/guard/testdata/frontend_golden.json (ci/guard_golden.py CONST_SETS); the
# Python classifier no longer reads them. Values stay as they were until the Go mirror follow-up (G-016).
# T-1064 v2: config_parse adds this marker to flags when an option follows the first positional.
CONFIG_LATE = "\0late"
CONFIG_VALUE_OPTS = {"--type", "--default", "--comment", "--blob"}
CONFIG_READ = {"--get", "--get-all", "--get-regexp", "--get-urlmatch", "--list", "-l", "--show-origin", "--show-scope",
               "--name-only"}
CONFIG_WRITE = {"--unset", "--unset-all", "--add", "--replace-all", "--edit", "-e", "--rename-section", "--remove-section"}
CONFIG_ACTION_FLAGS = {"--get", "--get-all", "--get-regexp", "--get-urlmatch", "--get-color", "--get-colorbool", "--list", "-l"}
CONFIG_WORD_WRITE = {"set", "unset", "edit", "rename-section", "remove-section"}


def config_parse(args):
    """(flags, positionals) of `git config <args>`, or None when a token is outside the exact table (an abbreviation,
    `--`, a short bundle, an unknown option, a value option without its value) or a positional precedes a flag."""
    flags, pos, k = [], [], 0
    while k < len(args):
        a = args[k]
        k += 1
        if a in CONFIG_FLAGS and (not pos or a in CONFIG_TRAILING):
            flags.append(a)
            if pos:
                flags.append(CONFIG_LATE)
        elif a in CONFIG_PARSE_VALUE_OPTS or (a.startswith("--") and a.partition("=")[0] in CONFIG_PARSE_VALUE_OPTS and "=" in a):
            if pos:
                flags.append(CONFIG_LATE)
            if "=" not in a:
                if k >= len(args):
                    return None
                k += 1
        elif a.startswith("-") and a != "-":
            return None
        else:
            pos.append(a)
    return flags, pos


def config_is_read(args):
    """True only when `git config <args>` is PROVEN a read: exact-table options only, no write flag, and either one
    action flag, a leading get/list word, or no action with at most one positional."""
    parsed = config_parse(args)
    if parsed is None:
        return False
    flags, pos = parsed
    if "--regexp" in flags and pos[:1] != ["get"]:
        return False
    actions = [f for f in flags if f in CONFIG_ACTION_FLAGS]
    if len(actions) > 1:
        return False
    if actions:
        return len(pos) <= 2
    if CONFIG_LATE in flags and pos[:1] not in (["get"], ["list"]):
        return False  # git's legacy parser stops at the first non-option: a later option token is the VALUE, a write
    if pos[:1] == ["get"]:
        return len(pos) <= 3
    if pos[:1] == ["list"]:
        return len(pos) == 1
    return len(pos) <= 1 and pos[:1] != ["set"] and not (pos and pos[0] in CONFIG_WORD_WRITE)


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
# T-1200: a -c remote.<name>.url=... on push overrides the shared origin for one command; config that rewrites origin belongs in the DB.
LIVE_REMOTE_CFG = re.compile(
    r"(?i)^(remote\.\w+\.(url|pushurl|fetch)|url\..+\.(insteadof|pushinsteadof)|include\.path|includeif\..+\.path)=")
_A = chr(64)
PUSH_URL_CRED = re.compile(rf"^(?:https?|git|ssh)://[^/\s]+:[^/\s]+{_A}|^[^/\s]+:[^/\s]+{_A}[\w.-]+:")
FACTORY_URL_SHAPE = re.compile(r"https://github\.com/(?!\.\.?/)[A-Za-z0-9_.-]+/(?!\.\.?\.git)[A-Za-z0-9_.-]+\.git")


def normalize_push_url(url):
    """Compare two https factory remotes literally; strip one trailing slash only."""
    return (url or "").rstrip("/")


def push_url_has_credentials(url):
    """True when url embeds user:password or user:token before the host (the secret is never echoed)."""
    if not url or url in ("origin", "-"):
        return False
    if PUSH_URL_CRED.search(url):
        return True
    if "://" in url:
        body = url.split("://", 1)[1]
        return body.startswith("@") or (":" in body.split("/", 1)[0] and "@" in body.split("/", 1)[0])
    return bool(re.match(r"^[^/\s]+@[^/\s]+:", url))


def repo_cfg_path(c, scratch_ok=True):
    """T-0226: True when normalised absolute path `c` is repo git config outside the worker's scratchpad: <x>/.git/config*,
    <x>/.git/worktrees/<n>/config*, <x>/.git/info/attributes, or the same files of a bare repo (HEAD and objects/ beside).
    T-0770: scratch_ok=False (NO_PUSH_ROLES) drops the scratchpad exemption: a scratchpad repo's config runs as git too."""
    if scratch_ok and in_scratchpad(c):
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


def git_hooks_path(p):
    """T-0755: True when `p` is <x>/.git/hooks, <x>/.git/info or anything under them (a hook or a pager there runs as
    git, so a read-only role never writes it). T-0770: the scratchpad is not exempt."""
    for c in {os.path.normpath(p), os.path.realpath(p)}:
        parts = c.split(os.sep)
        if any(a == ".git" and b in ("hooks", "info") for a, b in zip(parts, parts[1:])):
            return True
    return False


def persist_path(p, ancestor=False, scratch_ok=True):
    """True when path `p` (absolute; `..` and symlinks resolved, glob patterns matched) is a protected file or inside a
    protected tree."""
    for t in brace_expand(p):
        cands = {os.path.normpath(t)}
        if not GLOB.search(t):
            cands.add(os.path.realpath(t))
        for c in cands:
            if ancestor and c in PERSIST_ANCESTORS:
                return True
            if repo_cfg_path(c, scratch_ok):
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


def gitfile_path(p):
    """T-0783: True when `p` (absolute) is itself named `.git`: a `.git` file `gitdir: <dir>` moves the repo config to a
    path no <x>/.git/config rule matches, so NO_PUSH_ROLES never write one (scratchpad included)."""
    return any(os.path.basename(c.rstrip(os.sep)) == ".git" for c in {os.path.normpath(p), os.path.realpath(p)})


def persist_code(text):
    return bool(PERSIST_CODE.search(text) or PERSIST_CODE.search(re.sub(r"['\"+]", "", text)))


# the rendered settings of the live home(s): a Bash write there re-points the next spawn's deny rules and hook
RUN_SETTINGS = {os.path.realpath(os.path.join(h, "run", "settings")) for h in LIVE_HOMES}
# T-0353: <live>/run/guard_impl (the guard_impl flag the sh shim reads) and ~/.local/lib/factory (the Go guard binary
# and its stamp): a Bash write there flips the implementation or swaps the binary, so they are protected like
# RUN_SETTINGS and CFG_DIR (writers, redirects, cd + write); reads stay allowed.
RUN_GUARD_IMPL = {os.path.realpath(os.path.join(h, "run", "guard_impl")) for h in LIVE_HOMES}
# T-1336: <live>/run/cli_impl (the cli_impl flag bin/factory reads before it execs factory-go) is protected the same way
RUN_CLI_IMPL = {os.path.realpath(os.path.join(h, "run", "cli_impl")) for h in LIVE_HOMES}
LIB_DIR = os.path.realpath(os.path.join(PW_DIR, ".local", "lib", "factory"))
# T-0957: ~/.local/bin is first on the tick's and factory-up's PATH, so a file a worker plants there (git, factory, go)
# runs with their credentials. Protected like LIB_DIR, but matched on the path AS WRITTEN too: its entries are
# symlinks (factory, claude) whose realpath is outside the tree. Reads and running stay allowed.
BIN_DIR = os.path.join(PW_DIR, ".local", "bin")
BIN_DIRS = {BIN_DIR, os.path.realpath(BIN_DIR)}
# T-0883: run/guard-export.json (what the Go guard decides own goal, push pin, sandbox and terms file from) and its
# atomic-rename temp sibling guard-export.json.tmp<pid>: a Bash write there forges the guard's own decisions
RUN_EXPORT = {os.path.realpath(os.path.join(h, "run", "guard-export.json")) for h in LIVE_HOMES}
EXPORT_VERBS = {"cp", "mv", "ln", "install", "rsync", "rm", "rmdir", "unlink", "shred", "chmod", "chown", "truncate",
                "touch", "dd", "tar", "tee"}


def _ancestors(path):
    out = set()
    while path != os.path.dirname(path):
        path = os.path.dirname(path)
        out.add(path)
    return out


EXPORT_ANCESTORS = set().union(*(_ancestors(d) for d in RUN_EXPORT))  # run/, the live home, ~, its parents, /
# T-0925: protected trees for workers, one more layer over the cfg_* detector: ~/.local/go (the Go toolchain: its go.env
# applies GOFLAGS even with GOENV=off) and ~/.config/go (go env -w), the guard trees above, and four trees INSIDE the live
# checkout (go/, roles/hooks, roles/settings, bin/: the tick builds, runs and renders from them). Only these four, not the
# whole live checkout. Reads and runs stay allowed; worktrees are not under them.
PROT_LIVE_SUBDIRS = ("go", "roles/hooks", "roles/settings", "bin")
PROT_MARKERS = (".local/go", ".config/go", ".local/lib/factory", ".config/factory", "roles/hooks", "roles/settings",
                "run/settings", "run/guard_impl", "run/cli_impl")  # an expansion we cannot resolve that spells one of these counts
PROT_WHY = ("it is a protected tree for workers (~/.local/go and ~/.config/go: the Go toolchain and `go env -w`; ~/.local/bin: first on the tick's PATH; "
            "~/.config/factory, ~/.local/lib/factory, <live>/run/settings, <live>/run/guard_impl: the guard; <live>/go, "
            "<live>/bin, <live>/roles/hooks, <live>/roles/settings: what the tick builds, runs and renders); only the owner "
            "and the tick change them. Work in your worktree")


def prot_trees():
    """Absolute paths of the protected trees, each spelled and resolved. The live homes are LIVE_HOMES, or the
    WORKER_GUARD_LIVE_HOME override (tests only); never live_homes(): that adds the hook's own repo root (a CI checkout
    or a coder's worktree copy), which a coder may write."""
    override = os.environ.get("WORKER_GUARD_LIVE_HOME")
    homes = {os.path.realpath(override)} if override else set(LIVE_HOMES)
    base = [CFG_DIR, LIB_DIR, os.path.join(PW_DIR, ".config", "factory"), os.path.join(PW_DIR, ".local", "lib", "factory"),
            os.path.join(PW_DIR, ".local", "go"), os.path.join(PW_DIR, ".config", "go")] + sorted(BIN_DIRS)  # T-0957 family
    for h in homes:
        base += [os.path.join(h, "run", "settings"), os.path.join(h, "run", "guard_impl"), os.path.join(h, "run", "cli_impl")]
        base += [os.path.join(h, *sub.split("/")) for sub in PROT_LIVE_SUBDIRS]
    return sorted({q for b in base for q in (os.path.normpath(b), os.path.realpath(b))})


def prot_path(p, ancestor=False):
    """True when absolute, glob-free path `p` (spelled or resolved) is a protected tree, inside one, or (ancestor) holds one."""
    trees = prot_trees()
    return any(c == d or under(c, d) or (ancestor and under(d, c)) for c in {os.path.normpath(p), os.path.realpath(p)}
               for d in trees)


FIND_READERS = {"ls", "cat", "grep", "wc", "stat", "file"}


def prot_dest_indexes(args):
    """B1: indexes of the words that can be the destination of cp/install/ln -s/rsync/dd. Options may trail the
    destination (`cp a dest -f`, `install a dest -m 755`), so the last word is not enough: the last non-option word
    counts, and so does the last non-option word before the trailing run of options and their values, when at least
    two non-option words precede that run (a source and a destination)."""
    n = len(args)
    isopt = [a.startswith("-") and len(a) > 1 for a in args]
    nonopt = [j for j in range(n) if not isopt[j]]
    res = {n - 1} if n else set()
    if nonopt:
        res.add(nonopt[-1])
    t = n
    while t > 0 and (isopt[t - 1] or (t > 1 and isopt[t - 2])):  # the trailing run: options, each with one value word
        t -= 1
    pre = [j for j in nonopt if j < t]
    if len(pre) >= 2:
        res.add(pre[-1])
    return res


def long_opt(opt, *names):
    """B5: GNU getopt accepts a unique prefix of a long option (`--dir=X` for --directory). True when `opt` (a `--word`,
    without any =value) is one of `names` or a prefix of one, at least one letter long after the dashes; an ambiguous
    prefix counts too."""
    return opt.startswith("--") and len(opt) >= 3 and any(n.startswith(opt) for n in names)


def target_dir_values(args):
    """N2: the -t/--target-directory values of cp/install/ln, in every spelling: `-t V`, `-tV`, a cluster ending at t
    (`-rt V`, `-rtV`), a long prefix (`--t V`, `--target-dir=V`). A cluster is walked left to right and stops at a
    letter that takes a value (g m o S: `install -groot` has no target), so the letters after it are not options."""
    out, k = [], 0
    while k < len(args):
        a = args[k]
        k += 1
        if a.startswith("--"):
            opt, eq, val = a.partition("=")
            if long_opt(opt, "--target-directory"):
                if eq:
                    out.append(val)
                elif k < len(args):
                    out.append(args[k])
                    k += 1
        elif a.startswith("-") and len(a) > 1:
            for i, ch in enumerate(a[1:], 1):
                if ch == "t":
                    if a[i + 1:]:
                        out.append(a[i + 1:])
                    elif k < len(args):
                        out.append(args[k])
                        k += 1
                    break
                if ch in "gmoS":
                    break
    return out


TAR_VALUE_LETTERS = "fCgTXFLbHVIKN"  # tar --help: the short options that take a value


def tar_mode_letters(word, old):
    """N2: the letters of a tar bundle that can be modes. `-czf~/x.tar`: up to and including the first value letter (the
    rest is that option's value). The old style first word (`tar fc a.tar`) takes its values from the following words,
    so the whole word is scanned."""
    letters = word[1:]
    if old:
        return letters
    for i, ch in enumerate(letters):
        if ch in TAR_VALUE_LETTERS:
            return letters[:i + 1]
    return letters


def prot_glob(p, ancestor=False):
    """True when glob pattern `p` (absolute) can match a protected tree, a path inside one or (ancestor) a path holding
    one: each component is fnmatched against the tree's, so `~/.local/fact*/x` and `<live>/rol*/hooks/y` match. B6: a
    `**` component stands for any number of components (zero included)."""
    pat = os.path.normpath(p).strip("/").split("/")

    def m(i, j, parts):
        if j == len(parts):
            return True  # the whole tree is matched: the pattern names it or something inside it
        if i == len(pat):
            return ancestor
        if pat[i] == "**":
            return m(i + 1, j, parts) or m(i, j + 1, parts)
        return fnmatch.fnmatchcase(parts[j], pat[i]) and m(i + 1, j + 1, parts)

    return any(m(0, 0, d.strip("/").split("/")) for d in prot_trees())


def cd_home_factory_word(w):
    """`$HOME/factory` and `${HOME}/factory` expand to the passwd-home factory checkout for ccwd tracking."""
    factory = os.path.join(PW_DIR, "factory")
    if w in ("$HOME/factory", "${HOME}/factory"):
        return factory
    m = re.match(r"^\$(?:\{HOME\}|HOME)(/.*)?$", w)
    if not m or not m.group(1):
        return None
    tail = os.path.normpath(os.path.join("/", m.group(1).lstrip("/")))
    if tail == "/factory":
        return factory
    return None


def tilde_expand(w, cwd):
    """A leading `~`, `$HOME`, `${HOME}` is the passwd home (never $HOME). T-0983: `~name` is that user's passwd home and
    `~+` is `cwd`. A word that cannot be resolved (no such user, `~-`, a `~+` with an unknown cwd) is returned unchanged,
    as on main, so every caller joins it to the cwd as before; tilde_unres says which words those are."""
    w = re.sub(r"^(~|\$HOME|\$\{HOME\})(?=/|$)", lambda m: PW_DIR, w)
    m = re.match(r"^~([^/$`\\'\"]*)(?=/|$)", w)
    if not m or not m.group(1):
        return w
    name, rest = m.group(1), w[m.end():]
    if name == "+" and cwd is not None:
        return cwd + rest
    if name not in ("+", "-"):
        try:
            return pwd.getpwnam(name).pw_dir + rest
        except (KeyError, ValueError):
            pass
    return w


def tilde_unres(w, cwd):
    """T-0983: True when w starts with a `~` form tilde_expand cannot resolve (no such user, `~-`, `~+` with an unknown
    cwd). Only rule A's extra tightening asks (prot_hit, cfg_hit, the cd tracker, the Edit path)."""
    w = re.sub(r"^(~|\$HOME|\$\{HOME\})(?=/|$)", lambda m: PW_DIR, w)
    m = re.match(r"^~([^/$`\\'\"]*)(?=/|$)", w)
    if not m or not m.group(1):
        return False
    name = m.group(1)
    if name == "+":
        return cwd is None
    if name == "-":
        return True
    try:
        pwd.getpwnam(name)
    except (KeyError, ValueError):
        return True
    return False


def prot_next_names(p):
    """T-0983 (form B): the names an entry created directly inside directory `p` (absolute; may be a glob) would need to
    have to be, or hold, a protected tree: the component after `p` on the way to each protected tree that lies strictly
    below it. None when that cannot be told (a `**` in `p`): any name counts."""
    out = set()
    cands = {os.path.normpath(p)} | (set() if GLOB.search(p) else {os.path.realpath(p)})
    for c in cands:
        pat = c.strip("/").split("/") if c != "/" else []
        if "**" in pat:
            return None
        for d in prot_trees():
            parts = d.strip("/").split("/")
            if len(parts) > len(pat) and all(fnmatch.fnmatchcase(x, y) for x, y in zip(parts, pat)):
                out.add(parts[len(pat)])
    return out


def prot_edit_path(p, spelled=False):
    """persist_edit side (T-0925 gap g): p is a file_path of Write/Edit/MultiEdit/NotebookEdit. `spelled`: judge the
    names it spells only (an unresolvable `~name/...`)."""
    if spelled or any(c in p for c in "$`"):
        return any(m in p for m in PROT_MARKERS) or bool(re.search(r"\.local/bin(?![A-Za-z0-9_])", p))
    return prot_path(p)


def retro_roles_trees():
    return tuple(os.path.join(h, "roles") for h in LIVE_HOMES)


def retro_roles_code(text):
    norm = text.replace("\\", "/")
    for tree in retro_roles_trees():
        rel = tree.replace("\\", "/")
        if rel in norm:
            return True
    for h in LIVE_HOMES:
        hr = re.escape(h.replace("\\", "/"))
        if not re.search(hr, norm):
            continue
        if re.search(hr + r"\)?/roles(?:/|$)", norm):
            return True
        if re.search(hr + r"/rol[^;]*;[^)]*es\)", norm):
            return True
        if re.search(r"%s/roles(?:/|$)", norm):
            return True
    if "tory/roles" in norm:
        return True
    return bool(re.search(r"(?:^|/)(?:\S+/)?factory/roles(?:/|$)", norm))


def _retro_path_expr(text):
    s = text.strip()
    s = re.sub(r"^>[>|]?\s*", "", s)
    if len(s) >= 2 and s[0] == s[-1] and s[0] in "\"'":
        s = s[1:-1]
    return s


def _retro_quote_unquote(word):
    s = word.strip()
    if len(s) >= 2 and s[0] == s[-1] and s[0] in "\"'":
        return s[1:-1]
    return s


def _retro_split_shell_words(text):
    """Split `text` into words like bash for simple literals; stop at top-level ; | & newline."""
    words, cur, quote, i, n = [], [], None, 0, len(text)
    while i < n:
        c = text[i]
        if quote == "'":
            cur.append(c)
            if c == "'":
                quote = None
            i += 1
        elif quote == '"':
            if c == "\\" and i + 1 < n:
                cur.append(c)
                i += 1
                c = text[i]
            cur.append(c)
            if c == '"':
                quote = None
            i += 1
        elif c == "'":
            quote = "'"
            cur.append(c)
            i += 1
        elif c == '"':
            quote = '"'
            cur.append(c)
            i += 1
        elif c in ";|&\n":
            if cur:
                words.append("".join(cur))
                cur = []
            i += 1
        elif c in " \t":
            if cur:
                words.append("".join(cur))
                cur = []
            i += 1
        else:
            cur.append(c)
            i += 1
    if cur:
        words.append("".join(cur))
    return words


def _retro_printf_segment(text):
    """Static result of one `printf …` segment, or None when it is not printf-only."""
    words = _retro_split_shell_words(text.strip())
    if len(words) < 2 or words[0] != "printf":
        return None
    args = [_retro_quote_unquote(w) for w in words[1:]]
    if not args:
        return None
    fmt, rest = args[0], args[1:]
    if fmt == "%s":
        return "".join(rest)
    if fmt == "%s/%s" and len(rest) == 2:
        return rest[0].rstrip("/") + "/" + rest[1].lstrip("/")
    return None


def _retro_resolve_path_expr(expr):
    """Resolve a redirect substitution target without running a shell (G-036 SC3)."""
    if not expr:
        return None
    s = expr.strip()
    unwrapped = False
    if s.startswith("$(") and s.endswith(")"):
        s = s[2:-1]
        unwrapped = True
    elif len(s) >= 2 and s[0] == s[-1] and s[0] == "`":
        s = s[1:-1]
        unwrapped = True
    if not unwrapped:
        if not any(c in s for c in "$`"):
            return s
        return None
    parts = []
    seg, quote, i, n, depth = [], None, 0, len(s), 0
    while i < n:
        c = s[i]
        if quote == "'":
            seg.append(c)
            if c == "'":
                quote = None
            i += 1
        elif quote == '"':
            if c == "\\" and i + 1 < n:
                seg.append(c)
                i += 1
                c = s[i]
            seg.append(c)
            if c == '"':
                quote = None
            i += 1
        elif c == "'":
            quote = "'"
            seg.append(c)
            i += 1
        elif c == '"':
            quote = '"'
            seg.append(c)
            i += 1
        elif c == "(":
            depth += 1
            seg.append(c)
            i += 1
        elif c == ")":
            depth -= 1
            seg.append(c)
            i += 1
        elif c == ";" and depth == 0 and quote is None:
            parts.append("".join(seg))
            seg = []
            i += 1
        else:
            seg.append(c)
            i += 1
    parts.append("".join(seg))
    out = []
    for p in parts:
        got = _retro_printf_segment(p)
        if got is None:
            return None
        out.append(got)
    merged = "".join(out)
    return merged if merged and "\n" not in merged else None


def _retro_roles_subst_resolved(resolved, text, ccwd=None):
    if resolved is None:
        return None
    rp = resolved
    if not os.path.isabs(rp):
        if ccwd is None:
            return True
        rp = os.path.normpath(os.path.join(ccwd, rp))
    if retro_roles_path(rp, spelled=False):
        return True
    return retro_roles_code(text)


def retro_roles_subst(text, ccwd=None):
    """Spellings that name a live roles tree, else resolve $(`…`) targets; unresolved counts as under roles (fail closed)."""
    if any(c in text for c in "$`"):
        hit = _retro_roles_subst_resolved(_retro_resolve_path_expr(_retro_path_expr(text)), text, ccwd)
        if hit is not None:
            return hit
        return True
    return retro_roles_code(text)


def retro_subst_redirect_frags(text):
    """Redirect targets with command substitution; quoted targets may contain `;` inside $( )."""
    frags = []
    for m in re.finditer(r'>[>|]?\s*"([^"]*(?:\$\(|`)[^"]*)"', text):
        frags.append('> "' + m.group(1) + '"')
    for m in re.finditer(r">[>|]?\s*'([^']*(?:\$\(|`)[^']*)'", text):
        frags.append("> '" + m.group(1) + "'")
    for m in re.finditer(r">[>|]?\s*[^\s;|&\"']*(?:\$\(|`)[^;|&\n]*", text):
        frags.append(m.group(0))
    return frags


def retro_roles_glob(p, ancestor=False):
    """True when glob pattern `p` can match <live>/roles or a path under it (G-036 SC3)."""
    pat = os.path.normpath(p).strip("/").split("/")

    def m(i, j, parts):
        if j == len(parts):
            return True
        if i == len(pat):
            return ancestor
        if pat[i] == "**":
            return m(i + 1, j, parts) or m(i, j + 1, parts)
        return fnmatch.fnmatchcase(parts[j], pat[i]) and m(i + 1, j + 1, parts)

    return any(m(0, 0, d.strip("/").split("/")) for d in retro_roles_trees())


def retro_roles_path(p, spelled=False):
    """True when `p` is under <live>/roles/ for any live home (G-036 SC3, role retro only)."""
    if spelled or any(c in p for c in "$`"):
        return retro_roles_code(p)
    for t in brace_expand(p):
        if GLOB.search(t):
            if retro_roles_glob(t):
                return True
            continue
        cands = {os.path.normpath(t)}
        try:
            cands.add(os.path.realpath(t))
        except OSError:
            pass
        for c in cands:
            if any(c == d or under(c, d) for d in retro_roles_trees()):
                return True
    return False


def tar_dirs(args):
    """-C/--directory targets of tar, also in a bundle (`-xCf DIR a.tar`, `xfC a.tar DIR`): every letter C or f takes a
    word, so the glued rest and the next words are all candidates (over-approximate; options are skipped)."""
    out, k = [], 0
    while k < len(args):
        a = args[k]
        k += 1
        if a.startswith("--"):
            opt, eq, val = a.partition("=")
            if long_opt(opt, "--directory"):
                if eq:
                    out.append(val)
                elif k < len(args):
                    out.append(args[k])
                    k += 1
        elif a.startswith("-") and "C" in a[1:]:
            letters = a[1:]
            rem = letters[letters.index("C") + 1:]
            if rem:
                out.append(rem)
            n = sum(1 for ch in letters if ch in "Cf")
            out += [w for w in args[k:k + n] if not w.startswith("-")]
    return out


def unzip_dirs(args):
    """-d targets of unzip, also bundled (`-qd DIR`, `-qodDIR`)."""
    out = []
    for k, a in enumerate(args):
        if a.startswith("-") and not a.startswith("--") and "d" in a[1:]:
            rem = a[a.index("d") + 1:]
            if rem:
                out.append(rem)
            elif k + 1 < len(args):
                out.append(args[k + 1])
    return out


def opt_values(args, letters="", longs=()):
    """Values of the options named in `longs` (exact word, as --output X, --output=X, -o X) and of the single-dash
    letters in `letters` (curl -o X, -oX, -sSLo X: glued, or the next word), in the order they appear."""
    out, k = [], 0
    while k < len(args):
        a = args[k]
        k += 1
        opt, eq, val = a.partition("=")
        if opt in longs or long_opt(opt, *longs):
            if eq:
                out.append(val)
            elif k < len(args):
                out.append(args[k])
                k += 1
        elif a.startswith("-") and not a.startswith("--") and len(a) > 1:
            for i, ch in enumerate(a[1:], 1):
                if ch in letters:
                    if a[i + 1:]:
                        out.append(a[i + 1:])
                    elif k < len(args):
                        out.append(args[k])
                        k += 1
                    break
    return out


CFG_CODE = re.compile(r"\.config/factory|\.local/lib/factory|\.local/bin(?![A-Za-z0-9_])|run/guard_impl|run/cli_impl|run/settings|guard-export|(?<![A-Za-z0-9_])live_home(?![A-Za-z0-9_])")
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
    r"(?<!tests/stubs/)\b(?:herdr|hc|herdr-core)\b.*?"
    r"(?:\bserver\s+\S|\b(?:agent|pane|tab|workspace|worktree)[\"'\\\s]+"
    r"(?:start|stop|kill|close|prompt|split|new|run|send|create|move|swap|remove|rename)\b)")
# R2: re-targeting the socket/session, or pointing at herdr's config dir / socket file
R2_SET = re.compile(
    r"\b(?:HERDR_(?:SOCKET_PATH|SESSION)|HC_SOCKET)\s*\+?=|"
    r"\bexport\s+(?:-\w+\s+)*(?:HERDR_(?:SOCKET_PATH|SESSION)|HC_SOCKET)\b|"
    r"\b(?:declare|typeset)\s+-\w*x\w*\s+(?:HERDR_(?:SOCKET_PATH|SESSION)|HC_SOCKET)\b")
R2_SET_SHELL = re.compile(
    r"\bexport\s+(?:-\w+\s+)*(?:HERDR_(?:SOCKET_PATH|SESSION)|HC_SOCKET)\b|"
    r"\b(?:declare|typeset)\s+-\w*x\w*\s+(?:HERDR_(?:SOCKET_PATH|SESSION)|HC_SOCKET)\b")
R2_SET_VAR = re.compile(r"\b(?:HERDR_(?:SOCKET_PATH|SESSION)|HC_SOCKET)\s*\+?=")
R2_PATH = re.compile(r"herdr\.sock|\.config/herdr")
# commands that only print/search/record text: a quoted herdr.sock / .config/herdr there is data, not a target
FACTORY_NAMES = ("factory", "factory_cli.py", "factory_cli")  # script basenames that run the factory CLI
DATA_CMDS = {"git", "gh", "grep", "rg", "cat", "less", "echo", "printf", "factory"}
LAUNCHERS = {"env", "sudo", "nohup", "timeout", "command", "exec", "nice", "ionice", "setsid", "stdbuf", "xargs"}
FIND_EXEC = {"-exec", "-execdir", "-ok", "-okdir"}
R2_UNSET = re.compile(
    r"\bunset\b[^\n]*\b(?:HERDR_SOCKET_PATH|HC_SOCKET)\b|"
    r"\benv\b[^\n]*\s(?:-u\s*(?:HERDR_SOCKET_PATH|HC_SOCKET)|-[A-Za-z]*i[A-Za-z]*|--ignore-environment|--unset[= ](?:HERDR_SOCKET_PATH|HC_SOCKET))(?:\s|$)")
R3_REDIR = re.compile(r"<\s*[\"']?(?!\S*tests/stubs/)\S*(?:/(?:herdr|hc|herdr-core)\b|\$\{?HERDR_BIN_PATH)")
HOST_CLI_WORD = re.compile(r"(?<!tests/stubs/)\b(?:herdr|hc|herdr-core)\b")
HERDR_WORD = HOST_CLI_WORD  # legacy name for R2 unset + host mention check
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
# T-0755: read-only git allow-list for NO_PUSH_ROLES (git_allow); anything not listed fails closed
GIT_OPTS_ALLOWED = {"-P", "--no-pager", "--no-optional-locks", "--version", "--help"}
GIT_ALLOW_PLAIN = (GIT_READ_ONLY - {"fetch", "ls-remote"}) | {"merge-tree"}
GIT_FORBID_LONG = ("--ext-diff", "--output")
GIT_CONFIG_READ_ONLY = {"--get", "--get-all", "--get-regexp", "--list", "-l"}
GIT_FETCH_FLAGS = {"--all", "--prune", "-p", "--tags", "-t", "--no-tags", "-q", "--quiet", "-v", "--verbose", "-n",
                   "--dry-run", "-f", "--force", "--no-recurse-submodules", "--prune-tags", "--unshallow"}
GIT_LSREMOTE_FLAGS = {"--heads", "--refs", "--symref", "--exit-code", "--get-url"}
GIT_WORD = re.compile(r"(?:^|[\s/;&|])git(?:-\S*)?(?:\s|$)")
GIT_CONFIG_ENV = re.compile(r"^(GIT_CONFIG_COUNT|GIT_CONFIG_KEY_.*|GIT_CONFIG_VALUE_.*|GIT_CONFIG_PARAMETERS)\+?=")
GIT_SENSITIVE_ENV = re.compile(r"^(HOME|XDG_CONFIG_HOME|PATH|path|PAGER|GIT_\w*)(\+?=.*)?$")  # T-0788: zsh `path` is tied to PATH
PROTECTED_NAME = re.compile(r"^(HOME|XDG_CONFIG_HOME|PATH|path|PAGER|GIT_\w*)$")  # T-0788: zsh `path` is tied to PATH
PARAM_DEFAULT = re.compile(r"\$\{(!?[A-Za-z_]\w*):{0,2}=")  # ${VAR:=x} / ${VAR=x} assign VAR (T-0771)
# T-0808 F2: python long options that may precede -m (value-taking ones named separately); any other fails closed
PY_LONG_FLAG = {"--version", "--help", "--help-env", "--help-xoptions", "--help-all"}
PY_LONG_VALUE = {"--check-hash-based-pycs"}
# T-0808 F5: an inline interpreter program may write anything, so it counts as a filesystem change. Non-python: no program
# argument at all or an explicit `-` (program on stdin); option forms (-e/-r ...) are not tightened (T-0953). Python: py_inline_code.


def py_inline_code(args):
    """T-0808 F5: True when a python command line carries its program inline: -c (bare or in a cluster such as -Bc, attached
    code too), `-` / no program at all (heredoc or pipe on stdin). -m module and a script file are not inline."""
    k = 0
    while k < len(args) and args[k].startswith("-") and len(args[k]) > 1:
        cl = args[k]
        k += 1
        if cl.startswith("--"):
            if cl in PY_LONG_FLAG:
                return False  # --version/--help print and exit: no program runs (as on origin/main)
            if cl.partition("=")[0] in PY_LONG_VALUE and "=" not in cl:
                k += 1
            continue
        for i, ch in enumerate(cl[1:], 1):
            if ch == "c":
                return True
            if ch in "mVh?":
                return False  # -m module, or -V/-h/-? print and exit
            if ch in "WXQ":
                k += 0 if cl[i + 1:] else 1
                break
    return k >= len(args) or args[k] == "-"


def inline_interpreter(name, rest):
    """T-0808 F5: an interpreter invocation whose program is inline (option, stdin heredoc/pipe)."""
    if not INTERPRETERS.match(name):
        return False
    if re.match(r"^python[\d.]*$", name):
        return py_inline_code(rest)
    return not rest or rest[0] == "-"


FS_MUTATORS = {"ln", "mv", "cp", "install", "chmod", "chown", "rsync", "dd", "tee", "touch", "truncate", "sed", "perl", "patch"}
# T-0805 rule E: the only options an extracting tar/bsdtar/unzip may carry, matched exactly (no prefix matching: GNU tar
# accepts --xfo= for --xform=); anything else can rewrite, add or re-read member names so the listing no longer matches
TAR_SHORT_OK = set("xfvzjJCopk")
TAR_LONG_FLAG = {"--extract", "--gzip", "--bzip2", "--xz", "--verbose", "--no-same-owner", "--keep-old-files"}
TAR_LONG_VALUE = {"--file", "--directory"}
UNZIP_FLAG_OK = {"-o", "-n", "-q", "-qq"}
ARCHIVE_ENV_NAMES = ("TAR_OPTIONS", "POSIXLY_CORRECT", "UNZIP", "UNZIPOPT", "UNZIP_OPTS", "TAR_READER_OPTIONS")
ARCHIVE_ENV_RE = re.compile(r"\b(?:" + "|".join(ARCHIVE_ENV_NAMES) + r")\b")
UNZIP_LISTING = {"-l", "-v", "-t", "-p", "-c", "-z", "-Z"}
ARCHIVE_SIZE_MAX = 256 << 20  # T-0805: largest archive file the hook lists before refusing
ARCHIVE_WALK_MAX = 20000  # T-0783: entries archive_target walks below an extraction dir before refusing
NO_PUSH_ROLES = {"qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator", "researcher", "retro"}
RESEARCHER = "researcher"
RETRO = "retro"
RETRO_FACTORY_CMDS = frozenset({"whoami", "inbox", "done", "retro"})
RETRO_ROLE_WHY = ("the retro agent may not edit files under <live>/roles/; role prompts and hooks are changed only "
                  "by the owner and the tick")
RESEARCH_GH_GROUPS = {"pr", "issue", "release", "repo", "run"}
RESEARCH_GH_READ = {"view", "list", "diff", "status", "checks"}
RESEARCH_FACTORY = {"whoami", "inbox", "msg", "done", "report"}
CLONE_FLAGS = {"--single-branch", "--no-tags", "--filter=blob:none", "--quiet", "-q"}
CLONE_BRANCH = re.compile(r"[A-Za-z0-9][A-Za-z0-9._/-]*")
CLONE_URL = re.compile(r"https://(?:[A-Za-z0-9-]+\.)+[A-Za-z]{2,}/[A-Za-z0-9._~+/-]+")
# T-0789: inline interpreter code (python -c, perl -e, node -e/-p, ruby -e) of a NO_PUSH_ROLES role may not launch a
# push or a gh write. INCOMPLETE BY DESIGN, defense in depth only: string obfuscation ("pu" + "sh", base64, chr())
# beats it, and the code is never run or parsed, only scanned. The security boundary is the G-010 sandbox plus the
# worker PAT (R-c, T-0640 and children), not this rule. Accepted false positive: python3 -c "print('git push')".
INLINE_CODE_INTERPS = re.compile(r"^(python[\d.]*|perl|ruby|node|nodejs)$")
INLINE_GIT_PUSH = (re.compile(r"\bgit\b"), re.compile(r"\bpush\b"))  # both word tokens, case-sensitive
# known false positives (accepted): `gh repo view`, and a `-X POST` text after an unrelated `gh api` in the same code;
# optional gh global flags (-R a/b) may sit before the subcommand
INLINE_GH_WRITE = re.compile(
    r"\bgh\b(?:\W+-{1,2}[A-Za-z][\w-]*(?:\W+[\w./:-]+)?)*?\W+(?:pr\W+(?:merge|create|new|edit|close|reopen|ready|review|lock|unlock|update-branch|revert)\b|issue\W+(?:create|new|edit|close|reopen|comment|delete|transfer|pin|unpin|lock|unlock|develop)\b|workflow\W+(?:run|enable|disable)\b|repo\b|label\W+create\b|secret\W+set\b|variable\W+set\b|release\W+(?:create|new|edit|delete|upload|delete-asset)\b|api\b[\s\S]*?(?<!\w)(?:-X|--method)\W*(?i:POST|PUT|PATCH|DELETE)\b)")
# T-0995: the issue, workflow and pr write verbs are in INLINE_GH_WRITE above; `gh pr comment` is NOT (reviewer and qa post
# their summary with it, so the inline scan cannot tell their use from a pm's); see GH_NOPUSH_WRITE_VERBS.
# T-0881: `gh api` with a body flag defaults to POST, so it is a gh write unless the method is GET. Same scan, same limits.
INLINE_GH_API_BODY = re.compile(
    r"\bgh\b(?:\W+-{1,2}[A-Za-z][\w-]*(?:\W+[\w./:-]+)?)*?\W+api\b[\s\S]*?(?<![\w-])(?:-f|-F|--field|--raw-field|--input)(?![\w-])")
INLINE_GH_GET = re.compile(r"(?<![\w-])(?:-X|--method)\W*(?i:GET)\b")


def inline_gh_api_body(text):
    """T-0881: True when some `gh ... api ... -f|-F|--field|--raw-field|--input` in the inline code has no GET method of
    its own: each gh command is cut at the next `gh` word, so a `-X GET` on another gh call exempts nothing."""
    parts = re.split(r"(?=\bgh\b)", text)
    return any(INLINE_GH_API_BODY.search(c) and not INLINE_GH_GET.search(c) for c in parts if c.lstrip().startswith("gh"))


# T-0995: gh subcommand writes refused for NO_PUSH_ROLES. `pr comment` is refused too, except for the roles in
# GH_PR_COMMENT_ROLES (roles/reviewer.md and roles/qa.md post their summary with it). Read forms stay allowed.
GH_NOPUSH_WRITE_VERBS = {
    "issue": ("create", "new", "edit", "close", "reopen", "comment", "delete", "transfer", "pin", "unpin", "lock", "unlock", "develop"),
    "workflow": ("run", "enable", "disable"),
    "pr": ("create", "new", "edit", "close", "reopen", "ready", "review", "merge", "lock", "unlock", "update-branch", "revert", "comment"),
    "repo": ("create", "new", "fork", "sync"),  # T-1156 (`new` is gh's alias of `create`)
    "label": ("create",),
    "secret": ("set",),
    "variable": ("set",),
}
GH_PR_COMMENT_ROLES = {"reviewer", "qa"}

SHARED_ADVICE = ("workers never change the shared checkout ~/factory (branch main); work in your own worktree "
                 "(your cwd under ~/factory/worktrees/)")


class Block(Exception):
    pass


class PrivBlock(Block):
    pass


class GitBlock(Block):
    pass


class RepoProtBlock(PrivBlock):
    """gh commands that change branch protection or repository settings (R-a, T-0638), every role."""


class UnitBlock(PrivBlock):
    """Mutating systemctl (HQ mode) or systemd-run (every mode), T-0102 part D, director M2108."""


GH_VALUE_OPTS = {"-H", "--header", "-q", "--jq", "-t", "--template", "--hostname", "--cache", "-R", "--repo",
                 "-f", "--raw-field", "-F", "--field", "--input", "-X", "--method", "-p", "--preview"}
GH_MUTATING = {"PUT", "PATCH", "POST", "DELETE"}
GH_REPO_VERBS = {"edit", "rename", "archive", "unarchive", "delete"}
GH_PROTECTED = re.compile(r"(?:^|/)repos/[^/\s]+/[^/\s]+/(?:branches/.+/protection(?:/|\?|$)|rulesets(?:/|\?|$)"
                          r"|rules/branches)")
GH_GRAPHQL_SETTINGS = re.compile(r"(?:update|create|delete)(?:BranchProtectionRule|Repository|RepositoryRuleset)\b"
                                 r"|(?:un)?archiveRepository\b", re.I)
GH_REPO_ROOT = re.compile(r"(?:^|/)repos/[^/\s]+/[^/\s?]+/?(?:\?.*)?$")


def gh_parse(args, headers=None):
    """(positionals, method or None, body flag) of `gh <args>` as gh's pflag reads it: long options (--method=POST),
    short clusters (-iX DELETE, -XPUT, -X=PUT), the last -X/--method wins; -f/-F/--field/--raw-field/--input set body
    (gh api then uses POST). With a `headers` list, every -H/--header value is appended to it (T-1030). Shared by
    gh_repo_change and the researcher gh api rule (T-0874)."""
    pos, method, body, j = [], None, False, 0

    def note(opt, val):
        nonlocal method, body
        if opt in ("-X", "--method"):
            method = val.upper()
        elif opt in ("-f", "--raw-field", "-F", "--field", "--input"):
            body = True
        elif opt in ("-H", "--header") and headers is not None:
            headers.append(val)

    while j < len(args):
        x = args[j]
        if x.startswith("--") and len(x) > 2:
            opt, eq, val = x.partition("=")
            if opt in GH_VALUE_OPTS:
                if not eq:
                    val = args[j + 1] if j + 1 < len(args) else ""
                    j += 1
                note(opt, val)
        elif x.startswith("-") and len(x) > 1:  # a short-flag cluster as pflag reads it: -iX DELETE, -XPUT, -X=PUT
            for k in range(1, len(x)):
                opt = "-" + x[k]
                if opt in GH_VALUE_OPTS:
                    val = x[k + 1:]
                    if val.startswith("="):
                        val = val[1:]
                    elif not val:
                        val = args[j + 1] if j + 1 < len(args) else ""
                        j += 1
                    note(opt, val)
                    break
        else:
            pos.append(x)
        j += 1
    return pos, method, body


def gh_api_is_get(args):
    """True when `gh <args>` is a `gh api` call whose method resolves to GET: no -X/--method or an explicit GET (any
    spelling or case), and no body flag (which makes gh use POST)."""
    pos, method, body = gh_parse(args)
    return pos[:1] == ["api"] and not body and method in (None, "GET")


def gh_repo_change(args):
    """Why `gh <args>` changes branch protection or repository settings (R-a, T-0638), else None: `gh repo
    edit|rename|archive|unarchive|delete`, and `gh api` with a mutating method (or a body, which makes gh use POST)
    on repos/<o>/<r>, its branches/*/protection or its rulesets. A read (GET, no body) is fine."""
    pos, method, body = gh_parse(args)
    if pos[:1] == ["repo"] and pos[1:2] and pos[1] in GH_REPO_VERBS:
        return f"gh repo {pos[1]}"
    if pos[:2] in (["alias", "set"], ["alias", "import"]):  # a stored alias would run the change later, unseen
        return f"gh {pos[0]} {pos[1]}"
    if pos[:1] != ["api"] or len(pos) < 2:
        return None
    endpoint = urllib.parse.unquote(re.sub(r"#.*", "", pos[1])).lower()
    if endpoint == "graphql":  # mutations of protection or settings; a query read from a file cannot be inspected
        if GH_GRAPHQL_SETTINGS.search(" ".join(args)) or any(re.search(r"=@|--input", a) for a in args):
            return "gh api graphql (settings mutation or a query read from a file)"
        return None
    if method is None and not body:
        return None
    if method is not None and method not in GH_MUTATING:
        return None
    if GH_PROTECTED.search(endpoint) or GH_REPO_ROOT.search(endpoint):
        return f"gh api {method or 'POST'} {endpoint}"
    return None


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


ENV_LONG_OK = ("--null", "--debug", "--block-signal", "--default-signal", "--ignore-signal",
               "--list-signal-handling", "--help", "--version")
ENV_VALUE_LONG = ("--argv0", "--chdir")  # T-0826: classified "value" so unique prefixes are not "unknown"; since T-0964 the value is the next word unless `=value`


def env_long(a):
    """T-0826: classify a GNU env long option (GNU accepts any unique prefix): "unset" (a prefix of --unset), "ignore"
    (a prefix of --ignore-environment that is not an exact other long option), "value" (a prefix of --argv0/--chdir:
    its value is the next word unless `=value`), "split" (--s .. --split-string: T-1037, workers may not use it), "ok" (an exact
    harmless long option) or "unknown" (fail closed). `a` may carry an `=value`."""
    name = a.split("=", 1)[0]
    if len(name) < 3:
        return "unknown"
    if "--split-string".startswith(name):
        return "split"
    if "--unset".startswith(name):
        return "unset"
    if any(v.startswith(name) for v in ENV_VALUE_LONG):
        return "value"
    if name in ENV_LONG_OK:
        return "ok"
    if "--ignore-environment".startswith(name):
        return "ignore"
    return "unknown"


def env_options(words):
    """T-0964/T-1037: yield (kind, value) for the GNU env options before the command word: ("split", opt) for -S or
    --split-string (any unique prefix, with or without `=value`; an `S` before the first `u` of a short cluster, attached
    or not) and then stop, because the string is never parsed; ("clear", opt) for -i/-/--ignore-env* (an `i` in a short
    cluster before any `u`), ("clear-attached", opt) for an `i` after `u` (check_scrub counts it, no-push ignores it),
    ("unset", name), ("unknown", opt) for a long option this guard does not model. Hard rule: a word starting with - is
    always parsed as an option, never consumed as a value. A -u/--unset value that starts with - is still reported
    (peeked, not consumed) so a dynamic `-$X` stays denied as on main. -a/-C/-P and --argv0/--chdir take the next word
    only when it exists and does not start with `-`. The first word that is neither an option nor an assignment ends the scan."""
    q = collections.deque(words)

    def val():
        return q.popleft() if q and not q[0].startswith("-") else ""

    def uval():
        if q and q[0].startswith("-"):
            return q[0]
        return q.popleft() if q else ""

    while q:
        a = q.popleft()
        if a == "--" or ASSIGN.match(a):
            continue
        if a == "-":
            yield "clear", a
        elif a.startswith("--"):
            kind = env_long(a)
            if kind == "split":
                yield "split", a
                return
            if kind == "ignore":
                yield "clear", a
            elif kind == "unknown":
                yield "unknown", a
            elif kind == "unset":
                yield "unset", a.split("=", 1)[1] if "=" in a else uval()
            elif "=" not in a and kind == "value":
                val()
        elif a.startswith("-"):
            if "S" in a[1:].split("u", 1)[0]:
                yield "split", a
                return
            scan = a[1:].split("u", 1)[0]  # main's no-push rule
            if "i" in scan:
                yield "clear", a
            elif "i" in a[1:]:
                yield "clear-attached", a  # check_scrub only (spec hard rule)
            for k, c in enumerate(a[1:], 1):
                if c == "u":
                    yield "unset", a[k + 1:] or uval()
                    break
                if c in "aCP":  # as on main, a `u` later in the cluster still counts; a trailing one takes the next word
                    if k == len(a) - 1:
                        val()
                    continue
        else:
            return


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
    """True when the word is or may expand to herdr, hc or herdr-core (stub paths excepted)."""
    base = os.path.basename(word.rstrip("/"))
    if base in ("herdr", "hc", "herdr-core"):
        return not is_stub(word)
    if base not in ("[", "[[") and GLOB.search(base):
        for target in ("herdr", "hc", "herdr-core"):
            if fnmatch.fnmatchcase(target, base):
                return True
        return False
    return False


def is_claude(word):
    """True when the word, as a command, is `claude` or a path whose basename is `claude` (no stub exemption)."""
    return os.path.basename(word.rstrip("/")) == "claude"


def under(path, root):
    return path == root or path.startswith(root.rstrip("/") + "/")


def _fold_assign_inner(inner):
    """Only fold assignment $( ) that the guard models as a static value (mktemp, printf)."""
    if not inner:
        return False
    if any("<" in t or ">" in t for t in inner):
        return False
    if inner[0] == "mktemp":
        return all(not x.startswith("-") or x in ("-d", "-p") for x in inner[1:])
    if inner[0] == "printf":
        return all("$" not in x and "`" not in x and ";" not in x for x in inner[1:])
    return False


def fold_mktemp(toks):
    """Merge `NAME=$ ( … )` (shlex splits on the parens) into `NAME=$(…)` for mktemp and static printf values."""
    out, i = [], 0
    while i < len(toks):
        t = toks[i]
        if ASSIGN.match(t) and t.endswith("=$") and i + 1 < len(toks) and toks[i + 1] == "(":
            depth, j = 1, i + 2
            merged = False
            while j < len(toks) and depth:
                tok = toks[j]
                if tok == "(":
                    depth += 1
                    j += 1
                elif depth and tok.startswith(")"):
                    depth -= 1
                    tail = tok[1:]
                    j += 1
                    if depth == 0:
                        inner = toks[i + 2:j - 1]
                        if _fold_assign_inner(inner):
                            out.append(t[:-1] + "$(" + " ".join(inner) + ")")
                            if tail:
                                out.append(tail)
                            i = j
                            merged = True
                        break
                else:
                    j += 1
            if merged:
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


def preprocess(cmd, fold_ticks=False, bodies=None, slots=None):
    """Make newlines separators, drop comments and non-shell heredoc bodies.

    Returns (text, extras): extras are texts to analyze too (bodies of heredocs fed to a shell) and
    text blocks fed to other interpreters (only searched for the word herdr). When bodies is a list, every heredoc
    is appended to it as (the command line up to its newline, the body): the public-text rule reads `-F -` from it.
    When slots is a list, one [body, strip] list per `<<` in text order is appended; a resolved heredoc's slot holds its body and whether it is `<<-`.
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
                slot = [None, r[0]]  # body, then whether this is a real `<<-`
                if slots is not None:
                    slots.append(slot)
                pending.append((r[0], r[1], r[3], slot))
                out.append(cmd[i:r[2]])
                i = r[2]
            else:  # unresolved delimiter: no body is skipped, the lines after it are scanned as code
                if slots is not None:
                    slots.append([None, False])
                out.append("<<")
                i += 2
            continue
        if c == "\n":
            head = "".join(out[line_start:])
            out.append(" ; ")
            i += 1
            for strip, delim, quoted, slot in pending:
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
                slot[0] = body
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

XARGS_GIT_TOKEN = re.compile(r'(?:^|[\s/;&|(`\x27"])git(?:-[a-z][a-z-]*)?(?=\Z|[\s;&|)`\x27"])')


class Analyzer:
    def __init__(self, cwd, role=None, hq=False):
        self.cwd = cwd or os.getcwd()
        self.role = role
        self.hq = hq
        self.skip_priv = False
        self.shell_assigns = []  # env assignments of the shell command being analysed (T-0331)
        self.unit_rule = False  # HQ mode: block mutating systemctl (workers keep the factory unit rule)
        self.text = ""
        self.ran_factory = False
        self.may_create_before = False
        self.may_create = False  # T-0805: an earlier command on this line may have created a file (an archive to extract)
        self.fs_changed = False  # T-0783: an earlier command on this line may have changed the filesystem
        self.scrub_with_factory = None
        self.gcwd = self.cwd  # directory a git command would run in, moved by `cd` (git rule only)
        self.gdyn = False  # True after a `cd` into a directory that is only known at run time
        self.vars = {}  # NAME=value assigned earlier in the command: lets the persist detector resolve $NAME
        self.pt = None  # PublicText rule (G-021), set by check() for the one pass that applies it
        self.ccwd = self.cwd  # like gcwd, for the CFG_DIR write detector; None once a `cd` target is only known at run time
        self.cwd_unres = False  # N3: True after a `cd ~nosuchuser`: ccwd stays the known path, but the real one is not known

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

    def scratch_homes(self, assigns):
        """T-0331: True when BOTH HOME and FACTORY_HOME are static absolute paths whose realpath is inside the worker's own
        scratchpad. Each comes from this command's assignments, else an earlier assignment/export in the same command
        line, else the hook's environment; unset, relative, dynamic, `~`, a symlink or `..` out of the scratchpad, the
        live home and another agent's scratchpad all fail. The command line must also be the lone form (lone_command):
        nothing in it (unset, env -u, a subshell, another assignment) may change what is read here."""
        if not self.lone_command():
            return False
        for name in ("HOME", "FACTORY_HOME"):
            vals = [a.split("=", 1)[1] for a in assigns if a.startswith(name + "=")]
            v = vals[-1] if vals else self.vars.get(name, os.environ.get(name))
            if not v or any(c in v for c in "$`*?[{~\\\"'") or not os.path.isabs(v):
                return False
            if not in_scratchpad(os.path.realpath(v)):
                return False
        return True

    def install_dry_run(self, args, assigns):
        """T-0331: args (after the shell word and its options) is exactly `systemd/install.sh --dry-run`, run by a coder/qa
        with both homes in the scratchpad, and the script is byte-for-byte the install.sh of a live checkout (which a worker
        cannot write). Its body is then not parsed: `--dry-run` only echoes, and a parse of its case arms and `"$@"` trips
        the glob and dynamic-word rules. Any edited copy goes through the normal script inspection (and is refused)."""
        if self.role not in ("coder", "qa") or len(args) != 2 or args[1] != "--dry-run":
            return False
        if is_dynamic(args[0], strict=True) or not self.scratch_homes(assigns):
            return False
        p = os.path.expanduser(args[0])
        p = os.path.realpath(p if os.path.isabs(p) else os.path.join(self.cwd, p))
        if os.path.basename(p) != "install.sh" or os.path.basename(os.path.dirname(p)) != "systemd" or not os.path.isfile(p):
            return False
        override = os.environ.get("WORKER_GUARD_LIVE_HOME")
        homes = {os.path.realpath(override)} if override else set(LIVE_HOMES)  # as prot_trees(): never the hook's own repo root
        try:
            with open(p, "rb") as f:
                mine = f.read(1 << 20)
            for h in sorted(homes):
                live = os.path.join(h, "systemd", "install.sh")
                if os.path.isfile(live) and not os.path.islink(live):
                    with open(live, "rb") as f:
                        if f.read(1 << 20) == mine:
                            return True
        except OSError:
            return False
        return False

    def lone_command(self):
        """T-0331: the whole command line is one command, optionally after plain `HOME=v` / `FACTORY_HOME=v` /
        `export HOME=v FACTORY_HOME=v;` statements, and the command itself is the literal word `factory` or `bash` after
        only HOME= / FACTORY_HOME= prefixes. Nothing before it can edit what it runs or what the guard read (no redirect,
        pipe, chain, substitution, subshell, unset, env, other assignment such as PATH or BASH_ENV). Printable ASCII only,
        so the regexes agree with the Go RE2 mirror."""
        text = getattr(self, "text", "") or ""
        if not all(" " <= c < "\x7f" or c == "\t" for c in text) or any(c in text for c in "|&<>`$(){}\"'\\"):
            return False
        *pre, last = text.split(";")
        if not all(re.fullmatch(r"[ \t]*(export[ \t]+)?((HOME|FACTORY_HOME)=[^ \t]*[ \t]*)+", x) for x in pre):
            return False
        words = last.split()
        while words and re.fullmatch(r"(HOME|FACTORY_HOME)=[^ \t]*", words[0]):
            words = words[1:]
        return bool(words) and words[0] in ("factory", "bash")

    def scratch_form(self, words, assigns):
        """T-0331: words is exactly one of the commands a qa/coder may run with HOME in the scratchpad: the real `factory`
        (not a script merely named so, not a path) `init`, or `bash` (no other shell) running systemd/install.sh."""
        if self.role not in ("coder", "qa") or not words:
            return False
        if words[0] == "factory":
            return words[1:2] == ["init"] and self.scratch_homes(assigns)  # strict: a flag value must not shift the match
        if words[0] == "bash":
            return self.install_dry_run(words[1:], assigns)  # no shell option (-s runs stdin, the file is then an argument)
        return False

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
        for kind, v in env_options(rest):  # T-0964: value options no longer end the scan
            if kind == "split":
                raise Block("env -S/--split-string: workers may not use it; write the command without env -S (T-1037)")
            if kind == "unset" and v in SCRUB_VARS:
                raise Block("env -u FACTORY_ROLE/HERDR_PANE_ID")
            if kind in ("clear", "clear-attached"):
                self.scrub_with_factory = "env -i"

    def factory_unit(self, name, rest, assigns):
        """Block `factory up|tick|relaunch|spawn|clean` (also via python3 <path>/factory) in the live home."""
        if INTERPRETERS.match(name):
            si, ci, _ = interp_args(name, rest)
            stdin_code = (si is None or rest[si] == "-") and ci is None and not any(re.match(r"^-[A-Za-z]*m", a) for a in rest)
            if (ci is not None and "factory_cli" in rest[ci]) or (stdin_code and "factory_cli" in self.text):
                raise Block("inline code naming factory_cli runs the factory CLI around the guard; run `factory <cmd>`")
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
        if self.role == RETRO:
            try:
                self.retro_factory(sub, nonopt, assigns)
            except AttributeError:
                pass
        if self.role in ("coder", "qa") and not self.hq and sub == "init" \
                and not self.scratch_homes(assigns):
            raise Block(f"workers may run `factory init` only with HOME and FACTORY_HOME both "
                        "set (same command or exported earlier) to absolute paths inside your own scratchpad")
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

    def retro_factory(self, sub, nonopt, assigns):
        """G-036 SC3: retro may run only whoami, inbox, done and retro (packet, file, ...); no merge, config or gate."""
        if sub not in RETRO_FACTORY_CMDS:
            allowed = ", ".join(sorted(RETRO_FACTORY_CMDS))
            raise Block(f"the retro agent may not run `factory {sub or ''}`; allowed: {allowed}")

    def factory_script(self, name, rest):
        """Index of the `factory` script word in an interpreter's args, else None. bin/factory is a stub for
        bin/factory_cli.py (T-0660), so that basename and `-m factory_cli` count too. When the options cannot be
        modelled (unknown), any word whose basename is factory counts (fail closed)."""
        for k, a in enumerate(rest):
            if k + 1 < len(rest) and re.match(r"^-[A-Za-z]*m$", a) and os.path.basename(rest[k + 1]) in FACTORY_NAMES:
                return k + 1
            if re.match(r"^-[A-Za-z]*mfactory_cli(\.py)?$", a):
                return k
        si, _, unknown = interp_args(name, rest)
        if unknown:
            return next((k for k, a in enumerate(rest) if os.path.basename(a) in FACTORY_NAMES), None)
        if si is None or os.path.basename(rest[si]) not in FACTORY_NAMES:
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

    def lead_team_goals(self):
        """Goal ids for teams whose lead_agent is $FACTORY_AGENT; None when not lead or the DB is unreadable."""
        if self.role != "lead":
            return None
        agent = os.environ.get("FACTORY_AGENT")
        if not agent:
            return None
        db = self.db_path()
        try:
            con = sqlite3.connect(f"file:{db}?mode=ro", uri=True, timeout=2)
            try:
                rows = con.execute(
                    "SELECT DISTINCT goal_id FROM teams WHERE lead_agent = ? AND goal_id IS NOT NULL",
                    (agent,),
                ).fetchall()
            finally:
                con.close()
        except sqlite3.Error:
            return None
        return {r[0] for r in rows if r[0]}

    GOAL_ACTIONS = frozenset({"new", "list", "show", "edit", "status", "audit", "signoff", "close", "org"})
    GOAL_SHOW_BOOL_OPTS = frozenset({"--", "--all", "--full", "--ready", "--gaps"})

    def _factory_rest_cmd(self, words):
        """Tokens after `factory` in words, or None when this is not a factory invocation."""
        if not words:
            return None
        w, rest = words[0], words[1:]
        name = os.path.basename(w)
        if name == "factory":
            return rest
        if INTERPRETERS.match(name):
            fi = self.factory_script(name, rest)
            if fi is None:
                return None
            return rest[fi + 1:]
        return None

    def _lead_factory_goal_words(self, words):
        """True when words invoke `factory goal ...` (HQ lead panes still run goal-show broad_read)."""
        rest_cmd = self._factory_rest_cmd(words)
        if not rest_cmd:
            return False
        nonopt = [a for a in rest_cmd if not a.startswith("-")]
        return bool(nonopt) and nonopt[0] == "goal"

    def _lead_goal_show_index(self, rest):
        """Index of `show` in a `goal` rest vector, skipping valued/boolean flags before the action."""
        if not rest or rest[0] != "goal":
            return None
        i = 1
        while i < len(rest):
            t = rest[i]
            if t == "show":
                return i
            if t.startswith("-"):
                if t in self.GOAL_SHOW_BOOL_OPTS:
                    i += 1
                    continue
                if "=" in t:
                    i += 1
                    continue
                if i + 1 >= len(rest):
                    return None
                if rest[i + 1] in self.GOAL_ACTIONS:
                    i += 1
                    continue
                i += 2
                continue
            return None
        return None

    def _lead_factory_goal_show_words(self, words):
        """True when words invoke `factory goal show` (not list or other goal subcommands)."""
        rest_cmd = self._factory_rest_cmd(words)
        return self._lead_goal_show_index(rest_cmd) is not None

    def broad_read(self, sub, rest, assigns):
        """Block broad factory reads (res, agents, goal list, task list, ...) against the live home for workers.
        `rest` = the tokens after `factory` (options included). Exempt: --role staff-engineer, `agents` for
        --role cleaner, `goal show <own goal>`, lead `goal show` for goals their teams own, a temp FACTORY_HOME.
        `factory config <key> [value]` is not a broad read (the CLI enforces who may change config; T-1208 keeps coders
        allowed at the hook).
        Fail closed on options: we do not model which argparse flags take a value (--goal X, --note x) versus
        flags that do not (--all), so any option token (`-x`, `--x`, `--x=v`, `--`) before the action word (or
        before the id of `goal show`) blocks the command; `factory goal --goal G-005 show G-005` is blocked too."""
        if self.role == "staff-engineer":
            return
        nonopt = [a for a in rest if not a.startswith("-")]
        if sub == "config" and len(nonopt) > 1 and self.role == "coder":
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
                if gid is not None and not gid.startswith("-"):
                    if own is not None and gid == own:
                        return
                    if self.role == "lead":
                        team_goals = self.lead_team_goals()
                        if team_goals is not None and gid in team_goals:
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
                red_frags = retro_subst_redirect_frags(text) if self.role == RETRO else [
                    m.group(0) for m in re.finditer(r">[>|]?\s*[^\s;|&]*(?:\$\(|`)[^;|&\n]*", text)
                ]
                for frag in red_frags:
                    if persist_code(frag):
                        raise Block(f"a redirect built by a substitution into a protected config/init file: {PERSIST_WHY}")
                    if self.role == RETRO and retro_roles_subst(frag, self.ccwd):
                        raise Block(f"a redirect built by a substitution into a role file: {RETRO_ROLE_WHY}")
        nbodies = self.pt.bodies if self.pt else []
        seen = len(nbodies)
        slots = []
        text, extras = preprocess(text, self.hq, nbodies, slots)
        if self.role in NO_PUSH_ROLES and not self.hq:
            self.call_assign(text, [b for _, b in nbodies[seen:]])
        for kind, body in extras:
            if kind == "sh":
                self.soft(self.script, body, depth + 1)
            elif kind == "icode":
                hit = priv_in_code(body)
                if hit:
                    raise PrivBlock(hit)
                if not self.hq and persist_code(body):
                    raise Block(f"interpreter code naming a protected config/init file: {PERSIST_WHY}")
            elif not self.hq and HOST_CLI_WORD.search(body):
                raise Block("herdr in an interpreter heredoc")
        try:
            pre = re.sub(r"(?<![\w$])\d+(?=[<>])", " ", text)
            marked, pwords = (mark_punct_words(pre) if "find" in text else (pre, {}))
            lex = shlex.shlex(marked, posix=True, punctuation_chars=True)
            lex.whitespace_split = True
            lex.commenters = ""
            toks = fold_mktemp(list(lex))
        except ValueError as e:
            raise Block(f"cannot parse command ({e})")
        for sub in extract_subs(text):
            self.soft(self.script, sub, depth + 1)
        cmds, cur, piped, i, hs = [], [], False, 0, []
        reds = []  # (index of the command it belongs to, redirect target word)
        hds = {}  # command index -> heredoc bodies (T-0882: they join the here-string stdin channel)
        stack, dollar = [], False  # saved (cur, piped, hs) of the commands that open a $( )
        while i < len(toks):
            tok = toks[i]
            nxt = toks[i + 1] if i + 1 < len(toks) else None
            if tok in pwords:  # a quoted or escaped word made only of punctuation: data to find, as before for the rest
                tok = pwords[tok]
                if any(os.path.basename(w) == "find" for w in cur):
                    cur.append(tok)  # find's expression goes on: `-exec ls {} ';' -delete`, `\\( -name x \\) -delete`
                    i += 1
                    continue
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
                        elif tok == "<<":
                            slot = slots.pop(0) if slots else [None, False]
                            b = slot[0]
                            if b is not None:
                                hds.setdefault(len(cmds), []).append(b)
                            if slot[1] and nxt == "-" and i + 2 < len(toks) and not is_punct(toks[i + 2]):
                                i += 1  # `<<- EOF`
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
                    self.prot_redirect(target)
                    self.persist_redirect(target)
            k = n - 1
            while k > 0 and cmds[k][1] and cmds[k][0] and os.path.basename(cmds[k][0][0]) in ("tee", "cat"):
                k -= 1  # pass-through stages (`echo ... | tee f | python3`) keep the literal
            prev = cmds[k][0] if piped and k >= 0 else []
            self.pipe_src = " ".join(prev[1:]) if prev and os.path.basename(prev[0]) in ("echo", "printf") else None
            if prev and os.path.basename(prev[0]) == "cat" and (hds.get(k) or cmds[k][2]):
                self.pipe_src = "\n".join([*hds.get(k, []), *cmds[k][2]])
            self.soft(self.cmd, words, depth, piped, False, hstrs, (), (piped, [*hstrs, *hds.get(n, [])], self.pipe_src))
            if any(r[0] == n and r[1] != "/dev/null" for r in reds):
                self.fs_changed = True

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
        elif name == "gh":
            hit = gh_repo_change(rest)
            if hit:
                raise RepoProtBlock(hit)
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

    def interp_inline_push(self, name, rest):
        """T-0789: NO_PUSH_ROLES may not run inline interpreter code whose text names `git` and `push` as words, or a
        gh write (`gh pr merge|create`, `gh repo ...`, `gh api -X POST|PUT|PATCH|DELETE`). Other inline code (python3 -c
        "import json") and script files stay allowed. Incomplete defense in depth: see INLINE_CODE_INTERPS."""
        if not INLINE_CODE_INTERPS.match(name):
            return
        _, ci, unknown = interp_args("node" if name == "nodejs" else name, rest)
        if unknown:  # cannot tell which word is the code: look at every code-looking word (as interp_priv does)
            texts = [a for a in rest if re.search(r"[\s\"'();]", a)]
        else:
            texts = [rest[ci]] if ci is not None else []
        # attached code (`perl -e'CODE'`, `node --eval=CODE`) is not a separate word: scan option words that look like code
        texts += [a for a in rest if a.startswith("-") and re.search(r"[\s\"'();]", a)]
        for t in texts:
            self.scan_inline_text(name, t, "inline code")

    def scan_inline_text(self, name, t, what):
        if all(p.search(t) for p in INLINE_GIT_PUSH):
            raise Block(f"{name} {what} that runs `git push` (no-push roles never push; ask your lead)")
        if INLINE_GH_WRITE.search(t) or inline_gh_api_body(t):
            raise Block(f"{name} {what} that runs a gh write (pr merge/create, repo, api with a write method or body, release write); "
                        "no-push roles never write to GitHub; ask your lead")

    def stdin_program(self, name, rest):
        """T-0882: True when the interpreter command reads its program from stdin: no script file (or `-`) and no -c/-e code.
        An option this hook does not model counts as stdin too (scanning only ever refuses a push/gh write)."""
        if not INLINE_CODE_INTERPS.match(name):
            return False
        n = "node" if name.startswith("nodejs") else name
        si, ci, unknown = interp_args(n, rest)
        if unknown:
            return True
        if ci is not None:
            return False
        if si is not None and PT_STDIN_PATH.match(os.path.normpath(rest[si])):
            return True
        return si is None or rest[si] == "-" or bool(PT_STDIN_PATH.match(os.path.normpath(rest[si])))

    def stdin_scan(self, name, text):
        """T-0882: scan code fed to an interpreter on stdin (heredoc body, here-string, echo/printf piped in) with the T-0789
        scan, plus the T-0808 F4 fail-closed rule for archive extraction from python. NOT covered, by design: a piped
        non-literal source (`cat file | python3`, `curl ... | python3`), uv run wrappers, obfuscated code. Defense in depth only;
        the sandbox + worker PAT are the boundary (M15349)."""
        self.scan_inline_text(name, text, "stdin code")
        if re.match(r"^python[\d.]*$", name) and re.search(r"extractall|extract\s*\(|unpack_archive", text):
            raise Block(f"`{name}` stdin code that extracts an archive cannot be bounded (it may write .git/config or hooks); "
                        "use unzip -d or tar -C")

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
        self.cwd_unres = False
        if name == "popd":
            self.ccwd = None
        elif not args:
            self.ccwd = PW_DIR
        elif args[0] == "-":
            self.ccwd = None
        elif (hf := cd_home_factory_word(args[0])) is not None:
            self.ccwd = hf
        elif self.dynamic_dir(args[0]):
            self.ccwd = None
        else:
            p = self.cfg_expand(args[0])
            if tilde_unres(args[0], self.ccwd):  # `cd ~nosuchuser`, `cd ~-`: as on main, a path relative to the known cwd
                self.cwd_unres = True  # the extractor / downloader rules still refuse a cwd that is not really known
            self.ccwd = p if os.path.isabs(p) or self.ccwd is None else os.path.join(self.ccwd, p)

    def cfg_expand(self, w):
        return tilde_expand(w, self.ccwd)

    @staticmethod
    def cfg_code(word):
        """True when interpreter code / an editor argument spells a protected path, also split by string
        concatenation or joined pieces (`'live_'+'home'`): quotes, `+`, `,` and spaces are dropped before matching."""
        return bool(CFG_CODE.search(word) or CFG_CODE.search(re.sub(r"[\s'\"+,]", "", word)))

    def export_glob(self, t):
        """T-0883: True when a glob word (`g?ard-export.json`, `guard-*`, `run/*`) can expand to the export file or its
        temp sibling: `echo x > gua*` truncates it without the word ever spelling the name."""
        if any(c in t for c in "$`") or not any(c in t for c in "*?["):
            return False
        if not os.path.isabs(t):
            if self.ccwd is None:
                return False
            t = os.path.join(self.ccwd, t)
        return any(fnmatch.fnmatchcase(d + sfx, pat) for pat in brace_expand(os.path.normpath(t)) for d in RUN_EXPORT
                   for sfx in ("", ".tmp1"))

    def cfg_hit(self, word, ancestor=False):
        """True when `word` (or the value of a --opt=value / of=value word) names CFG_DIR or <live>/run/settings or
        something under them (or, with ancestor, a directory that contains one). A word with an expansion we cannot
        resolve counts when it spells `.config/factory` or `run/settings`."""
        for w in {word, word.split("=", 1)[-1]}:
            w = re.sub(r"\$\{?(\w+)\}?", lambda m: self.vars.get(m.group(1), m.group(0)), w)
            t = self.cfg_expand(w)
            if tilde_unres(w, self.ccwd) and (".config/factory" in w or ".local/lib/factory" in w or "run/settings" in w
                                              or "run/guard_impl" in w or "run/cli_impl" in w or "guard-export" in w
                                              or re.search(r"\.local/bin(?![A-Za-z0-9_])", w)):
                return True  # rule A: an unresolvable `~name/...` that spells a protected name counts
            if any(c in t for c in "$`*?[{"):
                if (".config/factory" in t or ".local/lib/factory" in t or "run/settings" in t
                        or "run/guard_impl" in t or "run/cli_impl" in t or "guard-export" in t or re.search(r"\.local/bin(?![A-Za-z0-9_])", t)):
                    return True
                if self.export_glob(t):
                    return True
                continue
            if not t or (not os.path.isabs(t) and self.ccwd is None):
                continue
            full = t if os.path.isabs(t) else os.path.join(self.ccwd, t)
            p = os.path.realpath(full)
            for d in {CFG_DIR, LIB_DIR} | RUN_SETTINGS | RUN_GUARD_IMPL | RUN_CLI_IMPL:
                if p == d or under(p, d) or (ancestor and under(d, p)):
                    return True
            for q in (p, os.path.normpath(full)):  # T-0957: the spelled path too (a symlink in ~/.local/bin)
                for d in BIN_DIRS:
                    if q == d or under(q, d) or (ancestor and under(d, q)):
                        return True
            for d in RUN_EXPORT:  # the file itself and its temp sibling <file>.tmp<pid>
                if p == d or p.startswith(d + ".") or (ancestor and under(d, p)):
                    return True
        return False

    def cfg_redirect(self, target):
        if self.cfg_hit(target):
            raise Block("a redirect into ~/.config/factory (it holds live_home, which decides what counts as the live "
                        "home), <live>/run/settings (the rendered role settings) or <live>/run/guard-export.json "
                        "(the guard's own decisions), <live>/run/guard_impl or ~/.local/lib/factory (the guard implementation "
                        "flag and binary) or ~/.local/bin (first on the tick's PATH); only the owner and the tick change them")

    def cfg_path(self, w):
        """Realpath of a word for the export checks, None when it has an expansion we cannot resolve."""
        t = self.cfg_expand(re.sub(r"\$\{?(\w+)\}?", lambda m: self.vars.get(m.group(1), m.group(0)), w))
        if not t or any(c in t for c in "$`*?[{") or (not os.path.isabs(t) and self.ccwd is None):
            return None
        return os.path.realpath(t if os.path.isabs(t) else os.path.join(self.ccwd, t))

    def in_guard_impl(self, w):
        """Message only (the block fires on cfg_hit): w resolves to run/guard_impl or to LIB_DIR or a file under it."""
        p = self.cfg_path(w)
        return p is not None and (p in RUN_GUARD_IMPL or p in RUN_CLI_IMPL or under(p, LIB_DIR))

    def in_bin_dir(self, w):
        """Message only (the block fires on cfg_hit): w, as spelled or resolved, is ~/.local/bin or under it."""
        t = self.cfg_expand(re.sub(r"\$\{?(\w+)\}?", lambda m: self.vars.get(m.group(1), m.group(0)), w))
        if not t or any(c in t for c in "$`*?[{") or (not os.path.isabs(t) and self.ccwd is None):
            return bool(re.search(r"\.local/bin(?![A-Za-z0-9_])", t))
        full = t if os.path.isabs(t) else os.path.join(self.ccwd, t)
        return any(under(q, d) or q == d for q in (os.path.realpath(full), os.path.normpath(full)) for d in BIN_DIRS)

    def export_rule(self, name, args):
        """T-0883 rule R, position independent (no guess which argument is the destination): True when ANY argument of a
        verb in EXPORT_VERBS names the export, its temp sibling or one of their ancestor directories (R1), is an option
        whose value does (R2: --target=X, of=X, -tX, -rtX), is a glob that can expand to one of them (R3), or is an
        expansion we cannot resolve that spells guard-export or a /run directory (R4). Reads use cat/jq/grep/head."""
        if name not in EXPORT_VERBS:
            return False
        for a in args:
            words = {a, a.split("=", 1)[-1]}
            m = re.match(r"^-[A-Za-z]*?[tC](.+)$", a)
            if m:
                words.add(m.group(1))
            for w in words:
                sub = re.sub(r"\$\{?(\w+)\}?", lambda k: self.vars.get(k.group(1), k.group(0)), w)
                for e in brace_expand(self.cfg_expand(sub)):
                    if any(c in e for c in "$`"):
                        if "guard-export" in e or re.search(r"/run(?:/|$)", e):
                            return True
                        continue
                    if not e or (not os.path.isabs(e) and self.ccwd is None):
                        continue
                    full = e if os.path.isabs(e) else os.path.join(self.ccwd, e)
                    if any(c in e for c in "*?["):
                        pat = os.path.normpath(full)
                        if any(fnmatch.fnmatchcase(d + sfx, pat) for d in RUN_EXPORT | EXPORT_ANCESTORS
                               for sfx in ("", ".tmp1")):
                            return True
                        continue
                    p = os.path.realpath(full)
                    if p in EXPORT_ANCESTORS or any(p == d or p.startswith(d + ".") for d in RUN_EXPORT):
                        return True
        return False

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
        if self.export_rule(name, [a for a in rest if a != "--"]):
            raise Block(f"`{name}` on <live>/run/guard-export.json: it holds the guard's own decisions; only the "
                        "tick writes it")
        if name in CFG_WRITERS:
            args = [a for a in rest if a != "--"]
            checked = [(j, a) for j, a in enumerate(args)]
            if name in ("cp", "install", "rsync", "dd"):  # reading FROM the config dir is fine: only the destination counts
                checked = [(j, a) for j, a in checked if j == len(args) - 1 or a.startswith("of=")
                           or (j > 0 and args[j - 1] in ("-t", "--target-directory")) or a.startswith("--target-directory=")]
            for j, a in checked:
                anc = name in CFG_REMOVERS or (name == "mv" and j < len(args) - 1)
                if self.cfg_hit(a, anc):
                    if "guard-export" in a:
                        raise Block(f"`{name}` on <live>/run/guard-export.json: it holds the guard's own decisions; "
                                    "only the tick writes it")
                    if "guard_impl" in a or "cli_impl" in a or ".local/lib/factory" in a or self.in_guard_impl(a):
                        raise Block(f"`{name}` on <live>/run/guard_impl or ~/.local/lib/factory: they pick and hold the "
                                    "guard implementation; only the owner and `factory` change them")
                    if self.in_bin_dir(a):
                        raise Block(f"`{name}` on ~/.local/bin: it is first on the tick's PATH, so a file there runs with "
                                    "the tick's credentials; only the owner changes it")
                    raise Block(f"`{name}` on ~/.config/factory: {why}")
        elif (name in ("sed", "perl", "gawk") or name in AWKS) and (name in AWKS or any(
                re.match(r"^-[A-Za-z]*i", a) or a.startswith("--in-place") or a == "inplace" for a in rest)):
            if any(self.cfg_hit(a) or self.cfg_code(a) for a in rest):
                raise Block(f"`{name}` writing ~/.config/factory: {why}")
        elif INTERPRETERS.match(name) or name in EDITORS - {"less", "more", "man", "view"}:
            if any(self.cfg_hit(a) or self.cfg_code(a) for a in rest):
                raise Block(f"`{name}` naming ~/.config/factory (code there cannot be inspected; read it with cat): {why}")

    def prot_hit(self, word, ancestor=False, base="cwd"):
        """T-0925: True when `word` (or the value of --opt=value / of=value) names a protected tree (prot_trees), something
        inside one (a glob that can match one counts) or, with ancestor, a directory holding one. A relative word is
        joined to `base` (default: the tracked cwd; unknown = not decidable = False, like cfg_hit). An expansion we cannot
        resolve counts when it spells a protected tree name."""
        base = self.ccwd if base == "cwd" else base
        for w in {word, word.split("=", 1)[-1]}:
            w = re.sub(r"\$\{?(\w+)\}?", lambda m: self.vars.get(m.group(1), m.group(0)), w)
            t = self.cfg_expand(w)
            unres = tilde_unres(w, base)
            for e in brace_expand(t):
                if unres and (any(m in e for m in PROT_MARKERS) or re.search(r"\.local/bin(?![A-Za-z0-9_])", e)):
                    return True  # rule A: an unresolvable `~name/...` that spells a protected tree name counts
                if any(c in e for c in "$`"):
                    if any(m in e for m in PROT_MARKERS) or re.search(r"\.local/bin(?![A-Za-z0-9_])", e):
                        return True
                    continue
                if not e or e == "-" or (not os.path.isabs(e) and base is None):
                    continue
                full = e if os.path.isabs(e) else os.path.join(base, e)
                if GLOB.search(e):
                    if prot_glob(full, ancestor):
                        return True
                elif prot_path(full, ancestor):
                    return True
        return False

    def prot_redirect(self, target):
        if self.prot_hit(target):
            raise Block(f"a redirect into a protected tree: {PROT_WHY}")

    def prot_write(self, name, rest, assigns=()):
        """T-0925 (gaps a, b, c, e, f, g): Bash writes into a protected tree. The writers cfg_write knows (now also with a
        glob target and for the new trees), the writer flags curl/wget/go/cc/tar/unzip/7z/git clone have, and an extractor
        or downloader whose target is the cwd after a `cd` into a tree. `go env -w|-u` writes ~/.config/go/env.
        T-0988: gzip -d, cpio -id, GOBIN= go install, gcc -MF, cargo --target-dir."""
        args = [a for a in rest if a != "--"]

        def block(w, what=None):
            raise Block(f"`{name}` on {what or repr(w)}: {PROT_WHY}")

        def hit(w, anc=False, base="cwd"):
            if w and self.prot_hit(w, anc, base):
                block(w, "a protected tree")

        def cwd_write(strict, base="cwd", anc=False):  # the command writes into its working directory
            b = self.ccwd if base == "cwd" else base
            if strict and base == "cwd" and self.cwd_unres:
                block(None, "a directory known only at run time (a `cd` into an unresolved path, then an extractor)")
            if b is None:
                if strict:
                    block(None, "a directory known only at run time (a `cd` into an unresolved path, then an extractor)")
            elif prot_path(b, anc):
                block(b, "the working directory, which is in a protected tree" + (" or holds one (the archive content is unknown)" if anc else ""))

        if name in CFG_WRITERS:
            dest_only = name in ("cp", "install", "rsync", "dd") or (
                name == "ln" and any(long_opt(a, "--symbolic") or (re.match(r"^-[A-Za-z]+$", a) and "s" in a) for a in args))
            tvals = target_dir_values(args) if name in ("cp", "install", "ln") and dest_only else []
            if tvals:  # N2: -t names the destination; the words after it are sources (reads)
                for v in tvals:
                    hit(v)
                self.prot_parent_copy(name, args, tvals, block)
                return
            dests = prot_dest_indexes(args) if dest_only else None
            for j, a in enumerate(args):
                if dest_only and not (j in dests or a.startswith("of=") or (j > 0 and args[j - 1] in ("-t", "--target-directory"))
                                      or (long_opt(a.partition("=")[0], "--target-directory") and "=" in a)):
                    continue
                hit(a, name in CFG_REMOVERS or (name == "mv" and j < len(args) - 1))
            if name in ("cp", "mv", "ln", "install", "rsync"):
                self.prot_parent_copy(name, args, tvals, block)
        elif name in ("sed", "perl", "gawk") and any(re.match(r"^-[A-Za-z]*i", a) or a.startswith("--in-place") for a in args):
            for a in args:
                hit(a)
        elif name == "curl":
            for v in opt_values(args, "oDc", {"--output", "--dump-header", "--cookie-jar", "--trace", "--trace-ascii", "--stderr"}):
                hit(v)
            dirs = opt_values(args, "", {"--output-dir"})
            for d in dirs:
                hit(d)
            if not dirs and (any(re.match(r"^-[A-Za-z]*[OJ]", a) for a in args) or any(
                    a in ("--remote-name", "--remote-name-all", "--remote-header-name") for a in args)):
                cwd_write(False)
        elif name == "wget":
            outs = opt_values(args, "O", {"--output-document"})
            for v in outs + opt_values(args, "oa", {"--output-file", "--append-output", "--save-cookies"}):
                hit(v)
            prefix = opt_values(args, "P", {"--directory-prefix"})
            for v in prefix:
                hit(v)
            if not outs and not prefix:
                cwd_write(False)
        elif name == "go":
            sub, k = None, 0
            while k < len(args):  # the first word that is not an option (or the value of -C)
                if args[k] in ("-C", "--C"):
                    k += 1
                elif not args[k].startswith("-"):
                    sub = args[k]
                    break
                k += 1
            if sub == "env" and any(re.match(r"^--?[wu](=|$)", a) for a in args):
                block(None, "`go env -w|-u` (it writes ~/.config/go/env)")
            cdir = (opt_values(args, "", {"-C", "--C"}) or [None])[-1]
            hit(cdir)
            cdir = None if cdir is None else self.cfg_expand(cdir)
            if cdir is None:
                base = self.ccwd
            elif os.path.isabs(cdir):
                base = cdir
            else:
                base = None if self.ccwd is None else os.path.join(self.ccwd, cdir)
            if sub in ("build", "test", "install"):
                for v in opt_values(args, "", {"-o", "--o"}):
                    hit(v, base=base)
                if sub == "install":
                    for a in assigns:
                        if a.startswith("GOBIN="):
                            hit(a.split("=", 1)[1], base=base)
        elif name in ("gcc", "g++", "cc", "c++", "clang", "clang++", "ld", "as", "rustc"):
            for k, a in enumerate(args):
                if (a == "-o" or long_opt(a, "--output")) and k + 1 < len(args):
                    hit(args[k + 1])
                elif long_opt(a.partition("=")[0], "--output") and "=" in a:
                    hit(a)
                elif a.startswith("-o") and len(a) > 2:
                    hit(a[2:])
                elif name == "gcc":
                    if a == "-MF" and k + 1 < len(args):
                        hit(args[k + 1])
                    elif a.startswith("-MF") and len(a) > 3:
                        hit(a[3:])
        elif name == "gzip":
            stdout = any(a in ("-c", "--stdout", "--to-stdout") or (re.match(r"^-[A-Za-z]+$", a) and "c" in a)
                         for a in args)
            decompress = any(a in ("-d", "--decompress") or long_opt(a.partition("=")[0], "--decompress") for a in args) or \
                any(re.match(r"^-[A-Za-z]+$", a) and "d" in a for a in args)
            if decompress:
                if stdout:
                    return
                pos = [a for a in args if not a.startswith("-")]
                for f in pos:
                    hit(f)
                    if f.endswith(".gz"):
                        hit(f[:-3])
                if not pos:
                    return
        elif name == "cpio":
            extract, list_mode, dirs = False, False, []
            for j, a in enumerate(args):
                short = re.match(r"^-[A-Za-z]+$", a)
                if a == "--extract" or (short and "i" in a):
                    extract = True
                if a in ("-t", "--list", "--to-stdout", "--only-verify-crc") or (short and "t" in a):
                    list_mode = True
                if (a == "-D" or a == "--directory") and j + 1 < len(args):
                    dirs.append(args[j + 1])
                elif a.startswith("--directory="):
                    dirs.append(a.split("=", 1)[1])
            if extract and not list_mode:
                for d in dirs:
                    hit(d, anc=True)
                if not dirs:
                    cwd_write(True, anc=True)
        elif name == "cargo":
            sub, k = None, 0
            while k < len(args):
                if not args[k].startswith("-"):
                    sub = args[k]
                    break
                k += 1
            if sub in ("build", "install", "run", "doc", "test", "bench"):
                for v in opt_values(args, "", {"--target-dir"}) + [a.partition("=")[2] for a in args
                                                                   if long_opt(a.partition("=")[0], "--target-dir") and "=" in a]:
                    hit(v, anc=True)
        elif name in ("tar", "gtar", "bsdtar"):
            targs = list(args)
            old = bool(targs) and re.fullmatch(r"[A-Za-z]+", targs[0]) is not None
            if old:
                targs[0] = "-" + targs[0]  # the old style `tar xf a.tar`
            extract = any(a.startswith("-") and not a.startswith("--") and "x" in tar_mode_letters(a, old and i == 0)
                          for i, a in enumerate(targs)) or any(
                long_opt(a.partition("=")[0], "--extract", "--get") for a in targs)
            # B3: only create, append and update (and --catenate) write the archive; list and diff only read it
            writes_archive = any(
                a.startswith("-") and not a.startswith("--") and any(c in tar_mode_letters(a, old and i == 0) for c in "cruA")
                for i, a in enumerate(targs)) or any(
                long_opt(a.partition("=")[0], "--create", "--append", "--update", "--catenate", "--concatenate") for a in targs)
            dirs = tar_dirs(targs)
            top = [a.partition("=")[2] for a in targs if long_opt(a.partition("=")[0], "--one-top-level")]
            if extract:
                base, unres = self.ccwd, self.cwd_unres
                for d in dirs:  # -C chains: each relative one is relative to the one before
                    hit(d, True, base=base)  # T-0983: an ancestor of a tree too (the archive content is unknown)
                    d = self.cfg_expand(d)
                    unres = unres and not os.path.isabs(d)
                    base = d if os.path.isabs(d) else (None if base is None else os.path.join(base, d))
                for v in top:
                    hit(v, True, base=base)
                if base is None or unres:
                    block(None, "a directory known only at run time (a `cd` into an unresolved path, then an extractor)")
                hit(base, True)  # no -C: the cwd; with -C: where the last one leads
            elif writes_archive:
                for v in opt_values(targs[1:] if old else targs, "f", {"--file"}):  # create/append/update: the archive is written
                    hit(v)
                if old and "f" in targs[0]:  # `tar cfb a.tar 20 x`: the value letters take the next words
                    n = sum(1 for ch in targs[0] if ch in TAR_VALUE_LETTERS)
                    for v in targs[1:1 + n]:
                        if not v.startswith("-"):
                            hit(v)
        elif name == "unzip":
            dirs = unzip_dirs(args)
            if not unzip_listing(list(rest)):
                for d in dirs:
                    hit(d, True)
                if not dirs:
                    cwd_write(True, anc=True)
        elif name == "env":
            for v in opt_values(args, "C", {"--chdir"}):
                hit(v)
            gobin, i = [], 0
            while i < len(args):
                if ASSIGN.match(args[i]):
                    if args[i].startswith("GOBIN="):
                        gobin.append(args[i].split("=", 1)[1])
                    i += 1
                elif args[i] == "--":
                    i += 1
                    break
                elif args[i].startswith("-"):
                    i += 1
                else:
                    break
            sub = args[i:]
            if gobin and len(sub) >= 2 and os.path.basename(sub[0]) == "go" and sub[1] == "install":
                for g in gobin:
                    hit(g)
        elif name == "find":
            self.prot_find(args, hit, lambda: cwd_write(False, anc=True))
        elif name in ("7z", "7za", "7zr"):
            if [a for a in args if not a.startswith("-")][:1] in (["x"], ["e"]):
                outs = [a[2:] for a in args if a.startswith("-o") and len(a) > 2]
                for v in outs:
                    hit(v, True)
                if not outs:
                    cwd_write(True, anc=True)
        elif name == "git":
            base, k = self.ccwd, 0
            while k < len(args) and args[k].startswith("-"):
                a = args[k]
                k += 1
                if a == "-C" and k < len(args):
                    args[k] = self.cfg_expand(args[k])
                    base = None if base is None and not os.path.isabs(args[k]) else (
                        args[k] if os.path.isabs(args[k]) else os.path.join(base, args[k]))
                    k += 1
                elif a in ("-c", "--attr-source", "--git-dir", "--work-tree", "--namespace", "--exec-path", "--super-prefix") and "=" not in a:
                    k += 1
            if k < len(args) and args[k] == "init":  # G1: `git init <dir>` creates a repository (and its hooks) there
                valued = {"--template", "-b", "--initial-branch", "--object-format", "--ref-format", "--separate-git-dir"}
                pos, j = [], k + 1
                while j < len(args):
                    a = args[j]
                    j += 1
                    if a in valued:
                        if a == "--separate-git-dir" and j < len(args):
                            hit(args[j], base=base)
                        j += 1
                    elif long_opt(a.partition("=")[0], "--separate-git-dir") and "=" in a:
                        hit(a, base=base)
                    elif not a.startswith("-"):
                        pos.append(a)
                for d in pos:
                    hit(d, base=base)
                if not pos:
                    cwd_write(False, base)
            elif k + 1 < len(args) and args[k] == "worktree" and args[k + 1] in ("add", "move", "repair"):
                pos, j = [], k + 2  # G1: the new worktree's directory (and the existing ones `move`/`repair` name)
                while j < len(args):
                    a = args[j]
                    j += 1
                    if a in ("-b", "-B", "--reason"):
                        j += 1
                    elif not a.startswith("-"):
                        pos.append(a)
                for d in pos:
                    hit(d, base=base)
            if k < len(args) and args[k] == "clone":
                pos, j, unknown = [], k + 1, False
                valued = {"-b", "--branch", "--depth", "-o", "--origin", "--template", "--reference", "--reference-if-able",
                          "-c", "--config", "-j", "--jobs", "--filter", "--separate-git-dir", "-u", "--upload-pack",
                          "--server-option", "--shallow-since", "--shallow-exclude", "--bundle-uri"}
                while j < len(args):
                    a = args[j]
                    j += 1
                    if a in valued or long_opt(a, "--separate-git-dir"):
                        if long_opt(a, "--separate-git-dir") and j < len(args):
                            hit(args[j], base=base)
                        j += 1
                    elif long_opt(a.partition("=")[0], "--separate-git-dir") and "=" in a:
                        hit(a, base=base)
                    elif not a.startswith("-"):
                        pos.append(a)
                    elif a.startswith("--") and "=" not in a:
                        unknown = True  # N1: a `--word` we do not know may take a value word
                for d in pos[1:]:  # N1: pos[0] is the repository; a value word of an unknown option may precede it
                    hit(d, base=base)
                if len(pos) < 2 or unknown:
                    cwd_write(False, base)

    def prot_parent_copy(self, name, args, tvals, block):
        """T-0983 form B: cp/mv/ln/install/rsync into a directory that holds a protected tree. The entry the source makes
        there is the source's basename; it can be, or contain, a protected tree when that name is the next component
        toward one (`cp -r /tmp/bin ~/.local`). A basename that cannot be known (glob, expansion, `-T`, `--parents`,
        `/.` or a trailing `/` on an rsync source, `.`/`..`) counts. Any other name keeps main's decision (allowed)."""
        nonopt = [j for j, a in enumerate(args) if not (a.startswith("-") and len(a) > 1)]
        if tvals:
            dest_words, src_idx = tvals, [j for j in nonopt if args[j] not in tvals]
        elif len(nonopt) >= 2:
            di = prot_dest_indexes(args)  # an option value may trail the destination (`cp a dest -S .bak`)
            dest_words, src_idx = [args[j] for j in sorted(di)], [j for j in nonopt if j not in di]
        else:
            return
        notarget = any(long_opt(a, "--no-target-directory", "--parents", "--relative") or (
            re.match(r"^-[A-Za-z]+$", a) and ("T" in a or (name == "rsync" and "R" in a))) for a in args if a.startswith("-"))
        for d in dest_words:
            if not d or d.startswith("of="):
                continue
            t = self.cfg_expand(re.sub(r"\$\{?(\w+)\}?", lambda m: self.vars.get(m.group(1), m.group(0)), d))
            for t in brace_expand(t):
                if any(c in t for c in "$`") or (not os.path.isabs(t) and self.ccwd is None):
                    continue  # the unresolvable destination keeps main's rule (prot_hit above)
                full = t if os.path.isabs(t) else os.path.join(self.ccwd, t)
                nxt = prot_next_names(full)
                if nxt is not None and not nxt:
                    continue  # not a strict ancestor of any protected tree
                for j in src_idx:
                    src = self.cfg_expand(re.sub(r"\$\{?(\w+)\}?", lambda m: self.vars.get(m.group(1), m.group(0)), args[j]))
                    base = os.path.basename(src.rstrip("/"))
                    unknown = (notarget or nxt is None or not base or base in (".", "..") or src.endswith("/.") or any(
                        c in src for c in "$`*?[{") or (name == "rsync" and (src.endswith("/") or "/./" in src)))
                    if unknown or base in nxt:
                        block(d, f"a directory that holds a protected tree, with the source {args[j]!r} (it is, or can contain, "
                                 "one of them: the name of what it creates there is " + ("not known" if unknown else repr(base)) + ")")

    def prot_find(self, args, hit, cwd_hit):
        """T-0983 form C: `find` that deletes (-delete), runs a writer or deleter (-exec, -execdir, -ok, -okdir) or writes
        a file (-fprint, -fprint0, -fprintf, -fls) writes to every start path: blocked when one is a protected tree, inside
        one or holds one. No start path: the cwd."""
        i = 0
        while i < len(args) and (args[i] in ("-H", "-L", "-P") or args[i].startswith("-O") or args[i].startswith("-D")):
            i += 2 if args[i] == "-D" else 1
        starts = []
        while i < len(args) and not (args[i].startswith("-") or args[i] in ("(", "!", ")", ",")):
            starts.append(args[i])
            i += 1
        expr, writes, files = args[i:], False, []
        for k, a in enumerate(expr):
            if a == "-delete":
                writes = True
            elif a in ("-fprint", "-fprint0", "-fls", "-fprintf") and k + 1 < len(expr):
                writes = True
                files.append(expr[k + 1])
            elif a in ("-exec", "-execdir", "-ok", "-okdir") and k + 1 < len(expr):
                # B1: against a protected tree any command counts as a writer unless it is on the closed read-only list
                # N2: the whole word: a bare listed name or its /usr/bin or /bin path (./cat and /tmp/grep are not readers)
                if expr[k + 1] not in FIND_READERS and not any(expr[k + 1] in (f"/usr/bin/{n}", f"/bin/{n}") for n in FIND_READERS):
                    writes = True
        if not writes:
            return
        for w in starts + files:
            hit(w, True)
        if not starts:
            cwd_hit()

    def persist_hit(self, word, ancestor=False, gitfile=False):
        """True when `word` (or the value of --opt=value / of=value) names a protected persistence path (T-0334). A
        word with an expansion we cannot resolve counts when it spells a protected name. T-0783: gitfile=True (a write
        target) also counts a path named `.git` for NO_PUSH_ROLES."""
        for w in {word, word.split("=", 1)[-1]}:
            t = re.sub(r"\$\{?(\w+)\}?", lambda m: self.vars.get(m.group(1), m.group(0)), w)
            t = self.cfg_expand(t)
            if any(c in t for c in "$`"):
                # an expansion we cannot resolve: refuse when it spells a protected name or sits under the home
                if self.role == RETRO and not self.hq and retro_roles_subst(t, self.ccwd):
                    return True
                if persist_code(t) or t.startswith(("~", PW_DIR)) or "~" in t or "HOME" in t:
                    return True
                continue
            if not t:
                continue
            if not os.path.isabs(t):
                if self.ccwd is None:
                    if persist_code(t):
                        return True
                    if self.role == RETRO and not self.hq and retro_roles_path(t, spelled=True):
                        return True
                    continue
                t = os.path.join(self.ccwd, t)
            if self.role == RETRO and not self.hq and retro_roles_path(t):
                return True
            nopush = self.role in NO_PUSH_ROLES and not self.hq  # T-0770: no scratchpad exemption for git config/hooks
            if persist_path(t, ancestor, not nopush) or (nopush and git_hooks_path(t)):
                return True
            if nopush and gitfile and gitfile_path(t):
                return True
        return False

    def persist_redirect(self, target):
        if self.persist_hit(target, gitfile=True):
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
                if self.persist_hit(a, name in ("ln", "mv"), gitfile=True):
                    raise Block(f"`{name}` on a protected config/init file: {PERSIST_WHY}")
            if name in ("cp", "mv", "install", "ln", "rsync"):  # a directory destination: the file lands as dest/<source name>
                opts = [a for a in args if not a.startswith("-")]
                dests = [a for j, a in checked if j == len(args) - 1 or (j > 0 and args[j - 1] in ("-t", "--target-directory"))
                         ] + [a.split("=", 1)[1] for a in args if a.startswith("--target-directory=")]
                for d in dests:
                    for src in opts:
                        if src != d and self.persist_hit(d.rstrip("/") + "/" + os.path.basename(src.rstrip("/")), gitfile=True):
                            raise Block(f"`{name}` into a protected directory under a protected file name: {PERSIST_WHY}")
        elif (name in ("sed", "perl", "gawk") or name in AWKS) and (name in AWKS or any(
                re.match(r"^-[A-Za-z]*i", a) or a.startswith("--in-place") or a == "inplace" for a in rest)):
            cand = rest
            if name in AWKS and not any(a == "inplace" for a in rest):  # operands are data files: reading one is fine
                prog = [a for a in rest if not a.startswith("-")][:1] if not any(a.startswith("-f") or a == "--file" for a in rest) else []
                cand = prog
            if any(self.persist_hit(a, gitfile=True) or persist_code(a) for a in cand):
                raise Block(f"`{name}` writing a protected config/init file: {PERSIST_WHY}")
        elif INTERPRETERS.match(name) or name in EDITORS - {"less", "more", "man", "view"}:
            if any(self.persist_hit(a, gitfile=not INTERPRETERS.match(name)) or persist_code(a) for a in rest):
                raise Block(f"`{name}` naming a protected config/init file (code there cannot be inspected; read it "
                            f"with cat): {PERSIST_WHY}")

    def archive_target(self, name, d):
        """T-0783: NO_PUSH_ROLES extract only into a directory inside their scratchpad that is not under a .git dir and
        holds no .git (file or dir) at any depth. A target the hook cannot resolve, outside the scratchpad, or whose walk
        hits ARCHIVE_WALK_MAX entries fails closed."""
        t = self.cfg_expand(re.sub(r"\$\{?(\w+)\}?", lambda m: self.vars.get(m.group(1), m.group(0)), d))
        if any(c in t for c in "$`*?[{") or (not os.path.isabs(t) and self.ccwd is None):
            raise Block(f"`{name}` into {d!r}: the target cannot be resolved, so it may be a .git dir; "
                        "give a literal directory in your scratchpad")
        t = os.path.realpath(t if os.path.isabs(t) else os.path.join(self.ccwd, t))
        if not in_scratchpad(t):
            raise Block(f"`{name}` into {d!r}: extraction may only target a directory inside your scratchpad (an archive "
                        f"member .git/config or hook elsewhere runs as git): {PERSIST_WHY}")
        if self.fs_changed:
            raise Block(f"`{name}` into {d!r}: an earlier command on this line may have planted a symlink or .git under the "
                        "target (ln, mv, cp, install, chmod, a write or redirect); extract in its own command, first")
        if ".git" in t.split(os.sep) or self.has_git_below(t):
            raise Block(f"`{name}` into {d!r}: a .git dir (or a dir holding one at any depth) would receive config/hooks "
                        f"that run as git: {PERSIST_WHY}")

    @staticmethod
    def has_git_below(top):
        """True when `top` or anything below it is named .git or is a symlink (never followed: it may point at a .git
        dir), the walk fails (unreadable dir) or exceeds ARCHIVE_WALK_MAX entries (unknown)."""
        seen, failed = 0, []
        for root, dirs, files in os.walk(top, followlinks=False, onerror=failed.append):
            seen += len(dirs) + len(files)
            if failed or ".git" in dirs or ".git" in files or seen > ARCHIVE_WALK_MAX:
                return True
            if any(os.path.islink(os.path.join(root, n)) for n in dirs + files):
                return True
        return bool(failed)

    def archive_extract(self, name, rest):
        """T-0783: NO_PUSH_ROLES extract tar/bsdtar/unzip/cpio/rsync only into a named directory that is not, under or
        holding a .git dir; an extraction with no named directory cannot be bounded and fails closed. Listing stays allowed."""
        if name in ("tar", "gtar", "bsdtar", "unzip"):
            return self.archive_rule_e(name, list(rest))  # `--` kept: P1 refuses it in extract mode
        args = [a for a in rest if a != "--"]
        dirs, extract, why = [], False, "name the target (tar -C, unzip -d, cpio -D) as a scratchpad dir that has no .git"
        if name == "cpio":
            pos = [a for a in args if not a.startswith("-")]
            for j, a in enumerate(args):
                short = re.match(r"^-[A-Za-z]+$", a)
                if a == "--extract" or (short and "i" in a and "t" not in a):
                    extract = True
                if a == "--pass-through" or (short and "p" in a):
                    extract = True
                    dirs += pos[-1:]
                if a in ("-D", "--directory") and j + 1 < len(args):
                    dirs.append(args[j + 1])
                elif a.startswith("--directory="):
                    dirs.append(a.split("=", 1)[1])
        elif name == "rsync":
            pos = [a for a in args if not a.startswith("-")]
            if len(pos) >= 2:
                extract, dirs = True, pos[-1:]
        elif name in ("7z", "7za", "7zr"):
            extract = bool(args[:1]) and args[0] in ("x", "e")
            dirs = [a[2:] for a in args if a.startswith("-o") and len(a) > 2]
        elif re.match(r"^python[\d.]*$", name):
            mod, k, code = None, 0, None
            while k < len(args) and args[k].startswith("-") and len(args[k]) > 1:
                cl = args[k]
                k += 1
                if cl.startswith("--"):  # T-0808 F2: skip a known long option (and its value); an unknown one fails closed
                    base, eq, _ = cl.partition("=")
                    if base in PY_LONG_VALUE:
                        k += 0 if eq else 1  # `--opt=value` carries its value; `--opt value` takes the next argument
                    elif base not in PY_LONG_FLAG:
                        # unknown arity: block only when a later -m names an archive module (`--bogus -m pytest` stays allowed)
                        if any(a.endswith(("zipfile", "tarfile", "zipfile.__main__", "tarfile.__main__")) for a in args[k:]):
                            raise Block(f"`{name}` with the unknown option {cl!r} before -m zipfile/tarfile: the target cannot "
                                        "be bounded; drop the option or extract with unzip -d")
                        break
                    continue
                # a short-flag cluster: -I, -Bm, -Im zipfile, -mzipfile. T-0808 F1: walk it char by char; only a bare c/m ends
                # the loop, and -W/-X (value-taking) consume the rest of the cluster or the next argument as their value
                for i, ch in enumerate(cl[1:], 1):
                    tail = cl[i + 1:]
                    if ch == "m":
                        mod = tail or (args[k] if k < len(args) else None)
                        k += 0 if tail else 1
                        break
                    if ch == "c":
                        code = tail or (args[k] if k < len(args) else "")
                        break
                    if ch in "WXQ":  # takes a value
                        k += 0 if tail else 1
                        break
                if mod is not None or code is not None:
                    break
            if code is not None and re.search(r"extractall|extract\s*\(|unpack_archive", code):
                raise Block(f"`{name} -c` code that extracts an archive cannot be bounded (it may write .git/config or hooks): "
                            f"{why}; use unzip -d or tar -C")
            if mod in ("zipfile", "tarfile", "zipfile.__main__", "tarfile.__main__"):  # T-0945: `-m zipfile.__main__` runs the same CLI
                for j, a in enumerate(args[k:], k):
                    if a in ("-e", "--extract"):
                        extract = True
                        dirs += args[j + 2:j + 3]
        elif name == "ar":
            ops, k, outs, opseen = [], 0, [], False
            for a in args:
                if a.startswith("@"):  # T-0808 F3: a response file adds options the hook cannot see, even as an option value
                    raise Block("`ar @file`: the response file's options cannot be inspected; pass the options on the command line")
            while k < len(args):
                a = args[k]
                k += 1
                if a in ("--plugin", "--target", "--output"):
                    if a == "--output" and k < len(args):
                        outs.append(args[k])
                    k += 1
                elif a.startswith("--output="):
                    outs.append(a.split("=", 1)[1])
                elif a == "--extract":
                    ops.append("x")
                elif a.startswith("--"):
                    continue
                elif not opseen:  # -x, -xv, -X32_64: any cluster naming x counts; the first non-option argument is the operation
                    ops.append(a)
                    opseen = not a.startswith("-")
            if any("x" in o for o in ops):
                extract = True
                dirs += outs
                if not dirs:  # members land in the current directory
                    dirs = [self.ccwd] if self.ccwd else []
        elif name == "pax":
            extract = any(a == "-r" or (re.match(r"^-[A-Za-z]+$", a) and "r" in a) for a in args)
        elif name == "jar":
            extract = bool(args[:1]) and "x" in args[0]
            if extract:
                raise Block("`jar` extraction writes into the current directory under names the hook cannot see: "
                            f"{why.replace('(tar -C, unzip -d, cpio -D) ', '')}; use unzip -d")
        if not extract:
            return False
        if not dirs:
            raise Block(f"`{name}` extracting without a target directory cannot be bounded (an archive may carry .git/config "
                        f"or hooks): {why}")
        for d in dirs:
            self.archive_target(name, d)
        return True

    @staticmethod
    def listing_only(words):
        """T-0805: True for a command that cannot create a file: cd and plain readers, tar -t, unzip -l."""
        name, args = os.path.basename(words[0]), words[1:]
        if name in ("cd", "pushd", "popd", "ls", "cat", "echo", "printf", "true", "pwd", "test", "[", "file", "stat", "head"):
            return not any(a in (">", ">>") for a in args)
        if name in ("tar", "gtar", "bsdtar"):
            return any(re.match(r"^-?[A-Za-z]*t[A-Za-z]*$", a) for a in args[:1]) and not any(
                re.match(r"^-?[A-Za-z]*[cxru][A-Za-z]*$", a) for a in args[:1]) or "--list" in args
        if name == "unzip":
            return unzip_listing(args)
        return False

    def archive_rule_e(self, name, args):
        """T-0805 rule E for tar/gtar/bsdtar/unzip in extract mode, whatever the argument order: E1 every option is in a
        fixed allowlist (exact match); E2 the archive resolves to one regular file; E3 its member list (never extracted by
        the hook) has no .git member. Returns True for an extraction, False for a listing."""
        why = ("name a scratchpad target (tar -C, unzip -d), use only -x -f -v -z -j -J -C -o -p -k (unzip -d -o -n -q) "
               "and an archive that has no .git member")
        why0 = "name the target (tar -C, unzip -d, cpio -D) as a scratchpad dir that has no .git"  # the T-0783 text (Go mirror)
        zipped = name == "unzip"
        if zipped:
            extract = not unzip_listing(args)
        else:
            extract, tarch, tdirs, tproblems = parse_tar(args)
        if not extract:
            return False
        if ARCHIVE_ENV_RE.search(re.sub(r"[\'\"\\]", "", self.text)) or any(n in os.environ for n in ARCHIVE_ENV_NAMES):
            raise Block(f"`{name}` extraction with {'/'.join(ARCHIVE_ENV_NAMES)} on the line or in the environment: they change "
                        f"the options or names tar/unzip use, so the listing the hook read may not match; unset it. {why}")
        if not zipped and any(
                a in ("--absolute-names", "--transform", "--xform", "--to-command", "--one-top-level")
                or a.startswith(("--transform=", "--xform=", "--to-command=")) or
                (re.match(r"^-?[A-Za-z]+$", a) and "P" in a and not a.startswith("--")) for a in args):
            raise Block(f"`{name}` option that rewrites member names (-P, --transform, --to-command) can escape the -C target "
                        f"into a .git dir: {why0}")

        def bad(what):
            raise Block(f"`{name}` extraction with {what}: names on disk may differ from the listing the hook read, so a "
                        f".git/config member cannot be ruled out; {why}")

        dirs, arch, pos, k = [], None, [], 0
        if not zipped:
            if tproblems:
                bad(tproblems[0])
            dirs, arch = tdirs, tarch[0] if tarch else None
        while zipped and k < len(args):
            a = args[k]
            k += 1
            if a == "--":
                bad("a literal `--` (the hook and unzip would disagree on what follows)")
            elif a == "-d":
                if k >= len(args):
                    bad("-d without a directory")
                dirs.append(args[k])
                k += 1
            elif a.startswith("-d") and len(a) > 2:  # -dDIR: the same target as -d DIR
                dirs.append(a[2:])
            elif a.startswith("-") and len(a) > 1:
                if a not in UNZIP_FLAG_OK:
                    bad(f"option {a!r}")
            else:
                pos.append(a)
        if zipped:
            arch = pos[0] if pos else None
        if not dirs:
            raise Block(f"`{name}` extracting without a target directory cannot be bounded (an archive may carry .git/config "
                        f"or hooks): {why0}")
        for d in dirs:
            self.archive_target(name, d)
        self.archive_members(name, arch)
        return True

    def archive_path(self, arch):
        """The absolute path of the archive named `arch`, or None when it is stdin, a glob, built from an expansion, a
        remote name (host:path) or relative with an unknown cwd."""
        if not arch or arch == "-" or any(c in arch for c in "$`*?[{~\\") or (not os.path.isabs(arch) and self.ccwd is None):
            return None
        if ":" in arch.split("/", 1)[0]:
            return None
        return arch if os.path.isabs(arch) else os.path.join(self.ccwd, arch)

    @staticmethod
    def archive_names(path, zipped):
        """(member name, link target or None) for each member of the archive at `path`, read with the stdlib (never
        extracted); raises OSError/tarfile.TarError/zipfile.BadZipFile/ValueError/RuntimeError when it cannot be listed or
        holds more than ARCHIVE_WALK_MAX members."""
        import tarfile  # lazy: the hook starts once per command, and only an extraction needs these
        import zipfile
        out = []
        if zipped:
            with zipfile.ZipFile(path) as z:
                infos = z.infolist()
                if len(infos) > ARCHIVE_WALK_MAX:
                    raise RuntimeError("too many members")
                out = [(i.filename, None) for i in infos]
        else:
            with tarfile.open(path, "r|*", ignore_zeros=True) as t:  # a concatenated archive: list every part, as tar -i extracts
                for m in t:
                    out.append((m.name, m.linkname if (m.issym() or m.islnk()) else None))
                    if len(out) > ARCHIVE_WALK_MAX:
                        raise RuntimeError("too many members")
            if not out:  # ignore_zeros skips junk blocks: an archive with no readable member is not a tar the hook can vouch for
                raise ValueError("no readable member")
        return out

    def archive_members(self, name, arch):
        """T-0805: a NO_PUSH_ROLES extraction (tar, bsdtar, unzip) reads the member list first and refuses an archive with
        a `.git` path component (also as a link target) or a directory holding both `config` and `HEAD` (a bare repo whose
        config `git -C <dir>` would run). A list that cannot be obtained (stdin, no such file, unreadable, unknown format,
        too many members) fails closed. The archive is only listed, never extracted by the hook."""
        why = "extract an archive you have listed (tar -tf, unzip -l) that has no .git member and no config+HEAD pair"
        arch = self.archive_path(arch)
        if arch is None:
            raise Block(f"`{name}` extracting from stdin, an expansion or an unresolvable archive name cannot be listed first "
                        f"(it may carry .git/config): {why}")
        zipped = name == "unzip"
        import tarfile
        import zipfile
        # unzip opens <name>, then <name>.zip, then <name>.ZIP: list every candidate that exists, not just the first
        cands = [arch] + ([arch + ".zip", arch + ".ZIP"] if zipped else [])
        found = [c for c in cands if os.path.lexists(c)]
        members = []
        for arch in found or [arch]:
            if os.path.exists(arch) and (not os.path.isfile(arch) or os.path.getsize(arch) > ARCHIVE_SIZE_MAX):
                raise Block(f"`{name}` archive {arch!r} is not a regular file under {ARCHIVE_SIZE_MAX >> 20} MB, so it is not "
                            f"listed (a FIFO or a bomb would hang the hook): {why}")
            try:
                members += self.archive_names(arch, zipped)
            except FileNotFoundError:
                if not self.may_create_before and not self.fs_changed:
                    return  # the extraction is the first command on the line and the file does not exist: it extracts nothing
                raise Block(f"`{name}` archive {arch!r} does not exist yet; an earlier command on this line may create it, so "
                            f"its members cannot be listed: run the extraction in its own command, after the archive exists")
            except (OSError, tarfile.TarError, zipfile.BadZipFile, ValueError, RuntimeError, EOFError, NotImplementedError) as e:
                raise Block(f"`{name}` archive {arch!r} cannot be listed ({type(e).__name__}), so a .git/config member cannot be "
                            f"ruled out: {why}")
        dirs = collections.defaultdict(set)
        for mem, link in members:
            parts = [c for c in re.split(r"[/\\]", mem) if c not in ("", ".")]
            if any(c.lower() == ".git" for c in parts) or (
                    link and any(c.lower() == ".git" for c in re.split(r"[/\\]", link))):
                raise Block(f"`{name}` archive {arch!r} has a member {mem!r} under a .git path: its config/hooks would run as "
                            f"git ({PERSIST_WHY}); {why}")
            if parts and parts[-1] in ("config", "HEAD"):
                dirs[tuple(parts[:-1])].add(parts[-1])
        if any(v >= {"config", "HEAD"} for v in dirs.values()):
            raise Block(f"`{name}` archive {arch!r} has config and HEAD in one directory (a bare repo layout whose config "
                        f"git runs: core.fsmonitor, core.hooksPath); {why}")

    @staticmethod
    def config_args_write(args):
        """True when `git config <args>` writes: anything config_is_read cannot prove a read."""
        return not config_is_read(args)

    @staticmethod
    def config_worktree_scope(args):
        """True when --worktree is a scope flag, not a value option's value or a legacy value-pattern (T-1200)."""
        skip = False
        seen_key = False
        for a in args:
            if skip:
                skip = False
                continue
            abbrev = len(a) >= 3 and a.startswith("--") and "=" not in a and any(
                o.startswith(a) for o in ("--type", "--default", "--comment", "--blob", "--value"))
            if a in CONFIG_VALUE_OPTS or abbrev:
                skip = True
                continue
            if not a.startswith("-") or a == "-":
                seen_key = True
            if a == "--worktree" or a.startswith("--worktree="):
                if not seen_key:
                    return True
                continue
        return False

    def config_expansion_refuse(self, toks):
        """T-1063: a raw `git config` token built by an expansion (dollar forms, backtick, brace list, or a glob before
        any read action) could expand into a write the parser cannot see. A dollar ending a token is regexp text."""
        if self.hq:
            return
        read = unsure = skip = False
        key = None
        for n, t in enumerate(toks):
            if "`" in t or re.search(r"\$.", t, re.S) or "{" in t or (GLOB.search(t) and not read):
                raise Block(f"git config: argument {t} is built from an expansion, so it could be a write; "
                            "write the arguments literally (T-1063)")
            if skip:  # T-1097: the value of a value option (`--comment --get`) is never the action flag
                skip = False
                if t in CONFIG_FLAGS or t in CONFIG_WORD_WRITE:
                    raise Block(f"git config: `{t}` follows a value option, so git takes it as the option's value and "
                                "not as an action; put the action flag first, or write the arguments literally "
                                "(T-1063, T-1097)")
                continue
            name = t.partition("=")[0]
            if t in CONFIG_PARSE_VALUE_OPTS or (len(t) >= 3 and t.startswith("--") and "=" not in t and any(
                    o.startswith(t) for o in ("--type", "--default", "--comment", "--blob", "--value", "--file"))):
                skip = True  # also a unique abbreviation (`--com --get al*`), which git accepts
            elif t.startswith("-") and t != "-" and t not in CONFIG_FLAGS and t not in CONFIG_ACTION_FLAGS \
                    and name not in CONFIG_PARSE_VALUE_OPTS:
                unsure = True  # an option outside the table before the action flag: fail closed, no read proven
            elif (t in CONFIG_ACTION_FLAGS or (n == 0 and t in ("get", "list"))) and not unsure:
                read = True
            elif key is None and not t.startswith("-"):
                key = t
        if read:
            return
        # T-1097: the parser drops a zsh glob such as (a|b) or <-> and the extendedglob forms (^z, a~b, al#) as
        # separate commands or redirections, so the command's own args are checked: no value, or no literal key.
        if skip:
            raise Block("git config: a value option has no value, so zsh may have expanded a glob away from it and "
                        "the command could be a write; write the arguments literally (T-1063, T-1097)")
        if (not unsure and key is None) or (key is not None and re.search(r"[()|<>^~#]", key)):
            scope = next((t for t in toks if t in ("--global", "--system")), None)
            if scope:  # same refusal as the write path (and the Go mirror) gives a scoped write
                raise Block(f"`git config {scope}` write: {PERSIST_WHY}")
            raise Block("git config: no read action flag is proven and no literal key is given (a zsh glob such as "
                        "(a|b), <->, ^z, a~b or al# expands into more arguments, or the key is missing), so it could "
                        "be a write; write the arguments literally (T-1063, T-1097)")

    def git_persist(self, rest, depth):
        """T-0334: `git config --global|--system|-f <protected file>` writes, also behind `git -c ... config` and an
        alias. Read-only forms (`--global --get`, `--global --list`) and repo-local writes stay allowed."""
        i, cwd, alias_map, git_dir, work_tree, persist_opts = 0, self.ccwd, {}, None, None, []
        while i < len(rest) and rest[i].startswith("-"):
            o = rest[i]
            val = None
            raw_tok = o
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
                name, body = val[6:].split("=", 1)
                alias_map[name] = body
            elif o == "-C" and val is not None:
                persist_opts.extend(["-C", val])
                if cwd is not None:
                    cwd = None if self.dynamic_dir(val) else os.path.join(cwd, os.path.expanduser(val))
            elif o == "--git-dir":
                persist_opts.append(f"--git-dir={val}" if "=" in raw_tok else "--git-dir")
                if "=" not in raw_tok and val is not None:
                    persist_opts.append(val)
                git_dir = val
            elif o == "--work-tree":
                persist_opts.append(f"--work-tree={val}" if "=" in raw_tok else "--work-tree")
                if "=" not in raw_tok and val is not None:
                    persist_opts.append(val)
                work_tree = val
        for alias, body in alias_map.items():
            used = rest[i + 1:] if i < len(rest) and rest[i] == alias else []
            if body.startswith("!"):
                self.script(" ".join([body[1:], *map(shlex.quote, used)]), depth + 1)
            else:
                try:
                    words = shlex.split(body)
                except ValueError as e:
                    raise Block(f"cannot parse git alias ({e})")
                inner = [*persist_opts]
                if words[:1] == ["config"]:
                    self.git_persist([*inner, "config", *words[1:], *used], depth)
                elif words[:1] == ["remote"]:
                    self.git_persist([*inner, "remote", *words[1:], *used], depth)
        if i < len(rest) and rest[i] == "remote":
            remote_args = rest[i + 1:]
            if not self.git_remote_read(remote_args):
                hit = next((a for a in remote_args if a in REMOTE_WRITE), None)
                if hit is not None:
                    self.repo_cfg_refuse(f"git remote {hit}", cwd, git_dir, work_tree, [])
            return
        if i >= len(rest) or rest[i] != "config":
            return
        args, scope, files, k = [], None, [], i + 1
        rest = rest[:]
        # T-1064 v3: git's legacy form (bare key, no action flag, no leading subcommand word) takes every token after the
        # key as the VALUE, so scope/file options are only stripped while they are real options; a value option's value
        # (`--comment --global k v`) is never one.
        stripping, first, action = True, None, False
        while k < len(rest):
            a = rest[k]
            k += 1
            name, eq, val = a.partition("=")
            if not stripping:
                args.append(a)
                continue
            # git accepts unique abbreviations (`--typ bool`): a prefix of a value option eats the next token too
            abbrev = len(a) >= 3 and a.startswith("--") and "=" not in a and any(
                o.startswith(a) for o in ("--type", "--default", "--comment", "--blob", "--value"))
            if (a in CONFIG_PARSE_VALUE_OPTS and a not in ("--file", "-f")) or abbrev:
                args.append(a)
                if k < len(rest):
                    args.append(rest[k])
                    k += 1
                continue
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
            if a in CONFIG_ACTION_FLAGS:
                action = True
            elif first is None and (not a.startswith("-") or a == "-"):
                first = a
                if not action and a not in ("set", "unset", "edit", "rename-section", "remove-section", "get", "list"):
                    stripping = False
        raw = rest[i + 1:]
        if not self.config_args_write(args):
            self.config_expansion_refuse(raw)
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
        # T-1200: --worktree writes only affect this worktree's config.worktree; they do not touch <common-dir>/config.
        worktree_scope = self.config_worktree_scope(args)
        if worktree_scope and self.uses_live_common(cwd, git_dir, work_tree):
            if self.config_targets_live_common_file(files, cwd, git_dir, work_tree):
                raise GitBlock(f"`git config --file <common-dir>/config` write: {REPO_CFG_WHY}")
            return
        self.repo_cfg_refuse("git config", cwd, git_dir, work_tree, files)
        self.config_expansion_refuse(raw)

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
        common = self.git_common_dir(cwd, git_dir, work_tree)
        if self.is_live_git_common(common):
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

    def git_common_dir(self, cwd, git_dir=None, work_tree=None):
        """Absolute git common dir for the repository git would use from cwd, or None when it cannot be told (no git is run)."""
        if cwd is None:
            return None
        if git_dir is not None:
            gd = os.path.realpath(os.path.join(cwd, os.path.expanduser(git_dir)))
            comm = os.path.join(gd, "commondir")
            if os.path.isfile(comm):
                try:
                    with open(comm) as f:
                        rel = f.read().strip()
                    return os.path.realpath(os.path.join(gd, rel))
                except OSError:
                    return None
            return gd
        p = os.path.realpath(os.path.join(cwd, os.path.expanduser(work_tree))) if work_tree is not None else os.path.realpath(cwd)
        while True:
            g = os.path.join(p, ".git")
            bare = os.path.isfile(os.path.join(p, "HEAD")) and os.path.isdir(os.path.join(p, "objects"))
            if os.path.isdir(g):
                return os.path.realpath(g)
            if bare and not os.path.exists(os.path.join(p, "commondir")):
                return os.path.realpath(p)
            if os.path.isfile(g):
                try:
                    with open(g) as f:
                        first = f.readline().strip()
                    if not first.lower().startswith("gitdir:"):
                        return None
                    gitdir = os.path.realpath(os.path.join(p, first[7:].strip()))
                    comm = os.path.join(gitdir, "commondir")
                    if os.path.isfile(comm):
                        with open(comm) as f:
                            return os.path.realpath(os.path.join(gitdir, f.read().strip()))
                    return gitdir
                except OSError:
                    return None
            parent = os.path.dirname(p)
            if parent == p:
                return None
            p = parent

    def is_live_git_common(self, common):
        if common is None:
            return False
        common = os.path.realpath(common)
        return any(common == os.path.realpath(os.path.join(h, ".git")) for h in self.git_homes())

    def uses_live_common(self, cwd, git_dir, work_tree):
        return self.is_live_git_common(self.git_common_dir(cwd, git_dir, work_tree))

    def live_common_config_path(self, cwd, git_dir, work_tree):
        common = self.git_common_dir(cwd, git_dir, work_tree)
        return os.path.join(common, "config") if common else None

    def config_targets_live_common_file(self, files, cwd, git_dir, work_tree):
        target = self.live_common_config_path(cwd, git_dir, work_tree)
        if not target:
            return False
        saved = self.ccwd
        self.ccwd = cwd
        try:
            for f in files:
                if not f:
                    continue
                t = os.path.expanduser(re.sub(r"\$\{?(\w+)\}?", lambda m: self.vars.get(m.group(1), m.group(0)),
                                            self.cfg_expand(f)))
                if not t or any(c in t for c in "$`*?[{") or (not os.path.isabs(t) and self.ccwd is None):
                    return True
                if os.path.realpath(t if os.path.isabs(t) else os.path.join(self.ccwd, t)) == os.path.realpath(target):
                    return True
        finally:
            self.ccwd = saved
        return False

    def factory_remote_url(self):
        """Config factory_remote_url from the factory DB; empty when unset or unreadable (fail closed for URL allow)."""
        try:
            con = sqlite3.connect(f"file:{self.db_path()}?mode=ro", uri=True, timeout=2)
            try:
                row = con.execute("SELECT value FROM config WHERE key = 'factory_remote_url'").fetchone()
            finally:
                con.close()
        except sqlite3.Error:
            return ""
        url = (row[0] if row and row[0] else "").strip()
        return url if FACTORY_URL_SHAPE.fullmatch(url) else ""

    def git_live_push_guard_push_args(self, args, remote_cfg):
        """T-1200: shared checks for `git push` and alias-expanded push on the live common dir."""
        if remote_cfg:
            raise GitBlock("`git -c remote.<name>.url=... push` overrides the shared origin from a worktree; push with "
                           "origin or the plain factory URL only (T-1200)")
        pos, k = [], 0
        while k < len(args):
            a = args[k]
            if a in ("-o", "--push-option", "--receive-pack", "--exec") or (a.startswith("-") and not a.startswith("--repo=")):
                k += 2 if a in ("-o", "--push-option", "--receive-pack", "--exec", "--repo") else 1
                continue
            if a.startswith("--repo="):
                pos.append(a.split("=", 1)[1])
            elif not a.startswith("-"):
                pos.append(a)
            k += 1
        remote = pos[0] if pos else None
        if remote and push_url_has_credentials(remote):
            raise GitBlock("git push: a remote URL must not embed credentials; use origin or the plain https remote (T-1200)")
        fac = self.factory_remote_url()
        if remote and re.match(r"[\w.-]+://", remote):
            if not fac or normalize_push_url(remote) != normalize_push_url(fac):
                raise GitBlock("git push: only the configured factory origin URL or the `origin` remote is allowed from a "
                               "worktree of the live checkout (T-1200)")

    def git_live_push_guard(self, rest):
        """T-1200: refuse push URL credentials and -c remote URL overrides when the git common dir is the live checkout."""
        if self.hq:
            return
        i, cwd, git_dir, work_tree, remote_cfg, alias_map = 0, self.ccwd, None, None, False, {}
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
                name, body = val[6:].split("=", 1)
                alias_map[name] = body
            elif o == "-C" and val is not None and cwd is not None:
                cwd = None if self.dynamic_dir(val) else os.path.join(cwd, os.path.expanduser(val))
            elif o == "--git-dir":
                git_dir = val
            elif o == "--work-tree":
                work_tree = val
            elif o == "-c" and val and LIVE_REMOTE_CFG.match(val):
                remote_cfg = True
        if i >= len(rest) or not self.uses_live_common(cwd, git_dir, work_tree):
            return
        sub, push_args = rest[i], None
        if sub in ("push", "send-pack"):
            push_args = rest[i + 1:]
        else:
            body = alias_map.get(sub)
            if body is not None:
                try:
                    aw = shlex.split(body)
                except ValueError:
                    return
                if aw and aw[0] in ("push", "send-pack"):
                    push_args = [*aw[1:], *rest[i + 1:]]
        if push_args is None:
            return
        self.git_live_push_guard_push_args(push_args, remote_cfg)

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
        return not config_is_read(args)

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

    def git_push_pin(self, rest):
        """T-0689: a coder's only allowed push is `git push origin HEAD:factory/<own task id>`; the task id comes from
        FACTORY_AGENT ([claude-|cursor-]coder-t-NNNN). No other remote, URL, refspec, force/--all/--mirror/--tags/--delete or option;
        no -c / --git-dir overrides (push.default, remote URLs); an unknown own task id refuses."""
        if self.role != "coder":
            return
        i = 0
        while i < len(rest) and rest[i].startswith("-"):
            if rest[i] != "-C" and "push" in rest[i + 1:]:
                raise Block(f"git {rest[i]} before push: coder pushes take no git option except -C")
            cv = rest[i + 1] if rest[i] == "-c" and i + 1 < len(rest) else (rest[i][2:] if re.match(r"-c.", rest[i]) else None)
            if cv is not None and re.match(r"(?i)alias\.[^=]*=.*(?:push|send-pack)", cv):
                raise Block("git -c alias.<x>=...push/send-pack...: an alias can hide a push; coders push only `git push origin HEAD:factory/<task id>`")
            if cv is not None and LIVE_REMOTE_CFG.match(cv):
                raise Block("git -c remote.<name>.url=...: cannot override the shared origin for push (T-1200)")
            ce = rest[i][len("--config-env="):] if rest[i].startswith("--config-env=") else (
                rest[i + 1] if rest[i] == "--config-env" and i + 1 < len(rest) else None)
            if ce is not None and re.match(r"(?i)alias\.", ce):
                raise Block("git --config-env alias.<x>=<VAR>: the alias value comes from the environment and could be a push or send-pack; "
                            "coders push only `git push origin HEAD:factory/<task id>`")
            o = rest[i]
            if "=" not in o and not re.match(r"-c.", o) and o not in GIT_PIN_FLAGS \
                    and o not in ("-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path"):
                raise Block(f"git {o}: coders may use only known git options here; write the command without it")
            i += 2 if rest[i] in ("-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path") else 1
        if i < len(rest) and (rest[i] in ("send-pack", "http-push") or (rest[i] == "subtree" and "push" in rest[i + 1:])):
            raise Block(f"git {rest[i]} is a push by another name: coders push only `git push origin HEAD:factory/<task id>`")
        if i < len(rest) and not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", rest[i]):
            raise Block(f"git {rest[i]}: coders may not hide the git subcommand behind a variable, expansion, brace or "
                        "glob word, array, $@/$1 or command substitution; write the literal subcommand (git status, git log ...)")
        if i >= len(rest) or rest[i] != "push":
            return
        args = rest[i + 1:]
        m = re.fullmatch(r"(?:claude-|cursor-)?coder-(t-\d+)(?:-a\d+)?", os.environ.get("FACTORY_AGENT", ""))
        if not m:
            raise Block("git push: cannot tell this pane's task id (FACTORY_AGENT); ask your lead")
        ref = f"HEAD:factory/{m.group(1)}"
        fac = self.factory_remote_url()
        ok = args == ["origin", ref] or (fac and len(args) == 2 and args[1] == ref
                                         and normalize_push_url(args[0]) == normalize_push_url(fac))
        if not ok:
            raise Block(f"coders push exactly `git push origin HEAD:factory/{m.group(1)}` (no other remote, refspec, "
                        "force, --all/--mirror/--tags/--delete or option)")
        # fail closed: the whole Bash call must be the one push (optionally `-C <static path>`). Anything around it
        # (env assigns, export/printf -v/set -a, functions, bash -c, BASH_ENV, HOME=) could redirect it unseen.
        url_pat = re.escape(fac) if fac else ""
        remote = rf"(?:origin|{url_pat})" if url_pat else "origin"
        w = re.fullmatch(rf"(?:uv run (?:-- )?)?git(?: -C ([\w./-]+))? push {remote} HEAD:factory/{m.group(1)}",
                         self.text.strip())
        if not w:
            raise Block(f"coders push exactly `git push origin HEAD:factory/{m.group(1)}` as the whole command "
                        "(optionally `git -C <own worktree> push ...`): no prefix, env assignment, export, chain, wrapper or quoting")
        # T-0743: the own worktree is the one recorded for this task in the factory DB, not whatever dir the pane sits in;
        # both the hook cwd and any -C target must be inside / equal to it. Unresolvable -> refuse (fail closed).
        own = self.task_worktree(m.group(1))
        if own is None:
            raise Block(f"git push: cannot resolve the worktree recorded for {m.group(1)} in the factory DB; ask your lead")
        cwd = os.path.realpath(self.cwd)
        if cwd != own and not cwd.startswith(own + os.sep):
            raise Block(f"coders push exactly `git push origin HEAD:factory/{m.group(1)}` from their own worktree {own}; "
                        f"this pane is in {cwd}: cd back to your worktree")
        if w.group(1):
            tgt = os.path.realpath(w.group(1) if os.path.isabs(w.group(1)) else os.path.join(self.cwd, w.group(1)))
            if tgt != own:
                raise Block(f"coders push exactly `git push origin HEAD:factory/{m.group(1)}`: `-C` must be this task's own "
                            f"worktree {own}, not {w.group(1)}; drop -C or fix the path")

    def git_runners(self, rest, depth):
        """T-0908: a coder's git may run a command through submodule foreach, rebase -x/--exec, bisect run and difftool
        -x/--extcmd. The inner command is checked as `bash -c <inner>` would be (self.script, one level deeper); a missing,
        dynamic or unparseable inner command is refused. Coder only."""
        if self.role != "coder":
            return
        i = 0
        while i < len(rest) and rest[i].startswith("-"):
            i += 2 if rest[i] in ("-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path") else 1
        if i >= len(rest):
            return
        sub, args = rest[i], rest[i + 1:]
        if sub in ("submodule", "bisect"):
            j = 0
            while j < len(args) and args[j].startswith("-"):
                j += 1
            if j >= len(args):
                return
            word = args[j]
            if is_dynamic(word, True) or GLOB.search(word):
                raise Block(f"git {sub} {word}: the {sub} subcommand is dynamic and could run a command; write it literally")
            if word != ("foreach" if sub == "submodule" else "run"):
                return
            inner = args[j + 1:]
            if sub == "submodule":
                while inner and inner[0] in ("--recursive", "--quiet", "-q"):
                    inner = inner[1:]
            if inner and inner[0] == "--":
                inner = inner[1:]  # git's option parser consumes a leading `--`; the command follows it
            self.git_inner(f"git {sub} {word}", inner, depth, quote=sub == "bisect")
        elif sub == "rebase":
            self.git_exec_opts(sub, args, "x", "--exec", "XsCS", depth)
        elif sub == "difftool":
            self.git_exec_opts(sub, args, "x", "--extcmd", "tOSG", depth)

    def git_inner(self, what, words, depth, quote=False):
        if not words:
            raise Block(f"{what} without a command: the inner command cannot be inspected; give it a literal command")
        dyn = [w for w in words if is_dynamic(w, True)]
        if dyn:
            raise Block(f"{what} {dyn[0]!r}: the inner command is built from an expansion and cannot be inspected")
        try:
            self.script(" ".join(shlex.quote(w) for w in words) if quote else " ".join(words), depth + 1)
        except ValueError as e:
            raise Block(f"{what}: the inner command cannot be parsed ({e})")

    def git_exec_opts(self, sub, args, short, long, valued, depth):
        """Every -<short>/--<long> (or git-accepted prefix of it) value of a git subcommand: separate, =value, attached,
        or at the end of a short flag cluster; options in `valued` swallow the rest of their cluster."""
        k = 0
        while k < len(args):
            a = args[k]
            k += 1
            if a == "--":
                return
            if a.startswith("--"):
                name, eq, val = a.partition("=")
                if len(name) > 2 and long.startswith(name):
                    if not eq:
                        val = args[k] if k < len(args) else None
                        k += 1
                    self.git_inner(f"git {sub} {long}", [val] if val else [], depth)
                continue
            if a.startswith("-") and len(a) > 1:
                for n, c in enumerate(a[1:], 1):
                    if c == short:
                        val = a[n + 1:]
                        if not val:
                            val = args[k] if k < len(args) else None
                            k += 1
                        self.git_inner(f"git {sub} -{short}", [val] if val else [], depth)
                        break
                    if c in valued:
                        if n == len(a) - 1 and c != "S":
                            k += 1
                        break

    def git_deny(self, why):
        raise Block(f"{self.role} may not run git push, or any git form outside the read-only allow-list ({why}): "
                    "read-only roles only inspect (status, log, diff, show, fetch ...); report results with factory done / "
                    "factory msg, or ask your lead")

    @staticmethod
    def git_abbrev(args, longs):
        """First option in `args` that is, or git would accept as an abbreviation of, one of `longs` (parse-options
        takes any unambiguous prefix); stops at `--`."""
        for a in args:
            if a == "--":
                return None
            if a.startswith("--") and len(a) > 2 and any(x.startswith(a.split("=", 1)[0]) for x in longs):
                return a
        return None

    def git_launch(self, assigns, words):
        """T-0755: NO_PUSH_ROLES start git only as the literal `git` (or the real /usr/bin/git), with no env assignment
        before it; git-* helpers and lookalike paths are refused. xargs and find -exec never start git."""
        if not words:
            return
        raw = words[0]
        base = os.path.basename(re.sub(r"[\\'\"]", "", raw))
        if base == "git" or base.startswith("git-"):
            if raw != "git" and not (os.path.isabs(raw) and os.path.realpath(raw) == os.path.realpath("/usr/bin/git")):
                self.git_deny(f"{raw!r} is not the literal `git`: no git-* helpers, relative or lookalike paths")
            if assigns:
                self.git_deny("an environment assignment before git can point it at another config")
        elif base in ("xargs", "find"):
            rest = words[1:]
            if base == "find":
                hit = any(a in ("-exec", "-execdir", "-ok", "-okdir") for a in rest) and any(GIT_WORD.search(a) for a in rest)
            else:
                hit = any(GIT_WORD.search(a) for a in rest)
            if hit:
                self.git_deny(f"{base} would start git with arguments this hook cannot see")

    def git_allow(self, rest):
        """T-0755: NO_PUSH_ROLES (qa, reviewer, pm, staff-engineer, scout, evaluator) may run only read-only git, fail
        closed: global options, subcommand and arguments are matched against an allow-list; a dynamic word, an alias, an
        unknown builtin and every push form are refused. Scope: this closes push from git invoked by shell."""
        i = 0
        used_c = False
        while i < len(rest) and rest[i].startswith("-"):
            o = rest[i]
            if is_dynamic(o, True):
                self.git_deny(f"dynamic word {o!r} before the subcommand")
            if o == "-C" and i + 1 < len(rest) and not is_dynamic(rest[i + 1], True):
                used_c = True
                i += 2
            elif o in GIT_OPTS_ALLOWED:
                i += 1
            else:
                self.git_deny(f"global option {o!r} could change config, aliases or the repository")
        if i >= len(rest):
            return
        sub, args = rest[i], rest[i + 1:]
        if is_dynamic(sub, True) or GLOB.search(sub) or "{" in sub:
            self.git_deny(f"the subcommand word {sub!r} is dynamic, so it could be push")
        dyn = [a for a in args if is_dynamic(a, True)]
        if dyn:
            self.git_deny(f"argument {dyn[0]!r} is built from an expansion and could be an option (--output, --ext-diff ...)")
        if sub in ("fetch", "ls-remote"):
            self.git_fetch_args(sub, args)
        elif sub == "clone" and self.role == RESEARCHER:
            self.git_clone(args, used_c)
        elif sub in GIT_ALLOW_PLAIN:
            if self.git_abbrev(args, GIT_FORBID_LONG):
                self.git_deny(f"{self.git_abbrev(args, GIT_FORBID_LONG)!r} runs a program or writes a file")
            if sub == "grep":
                if self.git_abbrev(args, ("--open-files-in-pager",)) or any(re.match(r"^-[^-]*O", a) for a in args):
                    self.git_deny("git grep -O runs a program")
                if any(is_dynamic(a, True) for a in args):
                    self.git_deny("git grep with an expansion in its arguments")
        elif sub == "branch" and not self.branch_mutates(args):
            return
        elif sub == "remote" and self.git_remote_read(args):
            return
        elif sub == "worktree" and args[:1] == ["list"]:
            return
        elif sub == "config" and self.git_config_read(args):
            return
        else:
            self.git_deny(f"`git {sub}` is not on the read-only list (aliases, push, send-pack, subtree, rebase --exec, "
                          "bisect run, submodule foreach, archive --remote ... are refused)")

    def nopush_gh_write(self, rest):
        """T-0881: NO_PUSH_ROLES may not run `gh api` with a body flag (-f -F --field --raw-field --input: gh then uses
        POST) unless the method is GET, nor `gh api` with any -X/--method other than GET (T-0987; HEAD, OPTIONS and an
        empty method are refused too, only GET is a read), nor `gh api -H 'X-HTTP-Method-Override: POST|PUT|PATCH|DELETE'`
        (T-1030), nor `gh release create|new|edit|delete|upload|delete-asset` (`new` is gh's alias of `create`). Defense in depth only,
        no obfuscation hunting: the boundary is the G-010 sandbox plus the worker PAT (M15349)."""
        headers = []
        pos, method, body = gh_parse(rest, headers)
        if pos[:1] == ["api"] and body and method != "GET":
            raise Block("gh api with -f/-F/--field/--raw-field/--input is a write (POST); no-push roles never write to "
                        "GitHub; use -X GET for a read with fields, or ask your lead")
        if pos[:1] == ["api"] and method not in (None, "GET"):  # T-0987: a non-GET method is a write even without a body
            raise Block(f"gh api -X/--method {method!r} is not GET; no-push roles only read GitHub through gh api "
                        "(GET); use GET, or ask your lead")
        if pos[:1] == ["api"]:  # T-1030: a method-override header turns a POST into a write on the server side
            for h in headers:
                name, _, val = h.partition(":")
                if name.strip().lower() == "x-http-method-override" and val.strip().upper() in GH_MUTATING:
                    raise Block(f"gh api -H X-HTTP-Method-Override: {val.strip()} is a write; no-push roles never write "
                                "to GitHub; drop the header, or ask your lead")
        if pos[:1] == ["release"] and pos[1:2] and pos[1] in ("create", "new", "edit", "delete", "upload", "delete-asset"):
            raise Block(f"gh release {pos[1]} is a GitHub write; no-push roles never write to GitHub; ask your lead")
        # T-0995: issue, workflow and pr writes (narrow verb list; reads stay allowed). Same limits: a verb known only at
        # run time (`gh issue $X`) and other subcommand groups (run, gist, cache, alias, label edit|delete|clone, secret|variable delete|remove, repo deploy-key|autolink ...) are not seen; repo create|new|fork|sync, label create, secret set and variable set are (T-1156).
        if len(pos) >= 2 and pos[1] in GH_NOPUSH_WRITE_VERBS.get(pos[0], ()):
            if not (pos[:2] == ["pr", "comment"] and self.role in GH_PR_COMMENT_ROLES):
                raise Block(f"gh {pos[0]} {pos[1]} is a GitHub write; no-push roles never write to GitHub"
                            + ("" if pos[0] != "pr" or pos[1] != "comment" else " (only reviewer and qa post a PR comment)")
                            + "; ask your lead")

    def researcher_cmd(self, name, rest):
        """G-029 T-0846: a researcher runs `gh` only to read and `factory` only to report (whoami, inbox, msg, done,
        report, task show|note, goal show); merge, PR, judge, spawn and every other form is refused."""
        pos = [a for a in rest if not a.startswith("-")]
        if name == "gh":
            ok = (pos[:1] == ["search"] or (pos[:1] and pos[0] in RESEARCH_GH_GROUPS and pos[1:2] and pos[1] in RESEARCH_GH_READ
                                            and rest[:2] == pos[:2])
                  or (pos[:1] == ["api"] and gh_api_is_get(rest)))
            if not ok:
                raise Block("a researcher may run gh only to read (search, <pr|issue|release|repo|run> view|list|diff|status|"
                            "checks, api that resolves to GET: no method other than GET, no -f/-F/--field/--raw-field/--input)")
        elif name == "factory":
            ok = bool(pos) and (pos[0] in RESEARCH_FACTORY or pos[:2] in (["task", "show"], ["task", "note"], ["goal", "show"]))
            if not ok:
                raise Block("a researcher runs only factory whoami, inbox, msg, done, report, task show|note, goal show")

    def git_clone(self, args, used_c):
        """G-029 T-0846: the one write a researcher may run: a public https clone into its research dir or scratchpad.
        Fixed option list, a literal https URL (no credentials, helper, file, ssh or local path) and a literal destination
        inside the allowed area; anything else is refused."""
        if used_c or self.gdyn:
            self.git_deny("git clone with -C or after a cd whose target is only known at run time")
        pos, ended, i = [], False, 0
        while i < len(args):
            a = args[i]
            nxt = args[i + 1] if i + 1 < len(args) else ""
            if not ended and a == "--":
                ended = True
            elif not ended and a.startswith("-"):
                if a in CLONE_FLAGS or re.fullmatch(r"--depth=[0-9]+", a) or (
                        a.startswith("--branch=") and CLONE_BRANCH.fullmatch(a[9:])):
                    pass
                elif (a == "--depth" and re.fullmatch(r"[0-9]+", nxt)) or (
                        a in ("--branch", "-b") and CLONE_BRANCH.fullmatch(nxt)):
                    i += 1
                else:
                    self.git_deny(f"git clone option {a!r} can run a program, read a config or name another source")
            else:
                pos.append(a)
            i += 1
        if self.fs_changed:
            self.git_deny("git clone after a command on this line that may have planted a symlink or moved a directory "
                          "(ln, mv, cp, a write or redirect, another clone); clone in its own command, first")
        if len(pos) != 2:
            self.git_deny("git clone needs exactly a literal https URL and a destination directory")
        url, dest = pos
        if not CLONE_URL.fullmatch(url):
            self.git_deny(f"git clone source {url!r} is not a plain https://<host>/<path> URL")
        if "~" in dest or "\\" in dest or ".." in dest.split("/") or any(c in dest for c in "*?[{"):
            self.git_deny(f"git clone destination {dest!r} is not a plain path inside the research dir or scratchpad")
        full = dest if os.path.isabs(dest) else os.path.join(self.gcwd, dest)
        if not (researcher_area(self.gcwd, self.gcwd) and researcher_area(full, self.gcwd)):
            self.git_deny(f"git clone destination {dest!r} (or the current directory) is outside your research dir "
                          "<state>/factory/research/<name> and your scratchpad")
        self.fs_changed = True  # the clone itself creates a tree: a later clone on this line must not follow it

    @staticmethod
    def git_remote_read(args):
        """Read-only `git remote` forms; every word must match, so -v cannot hide set-url/add/rename/rm."""
        def name(a):
            return len(a) == 1 and bool(a[0]) and not a[0].startswith("-")
        if args in ([], ["-v"], ["--verbose"]):
            return True
        if args[:1] == ["get-url"]:
            rest = args[1:]
            if rest[:1] in (["--push"], ["--all"]):
                rest = rest[1:]
            return name(rest)
        if args[:1] in (["-v"], ["--verbose"]):
            args = args[1:]
        if args[:1] == ["show"]:
            rest = args[1:]
            if rest[:1] == ["-n"]:
                rest = rest[1:]
            return name(rest)
        return False

    @staticmethod
    def git_config_read(args):
        parsed = config_parse(args)
        return bool(parsed) and any(f in GIT_CONFIG_READ_ONLY for f in parsed[0]) and config_is_read(args)

    def git_fetch_args(self, sub, args):
        ok_flags = GIT_FETCH_FLAGS | (GIT_LSREMOTE_FLAGS if sub == "ls-remote" else set())
        ended = False
        for a in args:
            if is_dynamic(a, True):
                self.git_deny(f"git {sub} argument {a!r} is dynamic")
            if not ended and a == "--":
                ended = True
            elif not ended and a.startswith("-"):
                if a not in ok_flags and not re.fullmatch(r"--(depth|deepen)=\d+", a):
                    self.git_deny(f"git {sub} option {a!r} can run a program or name another remote")
            elif "::" in a or "://" in a or a.startswith(("/", ".", "~")) or re.match(r"^[\w.-]+@[\w.-]+:", a):
                self.git_deny(f"git {sub} argument {a!r} is a URL, helper or path, not origin or a refspec")
            elif ":" in a and not a.split(":", 1)[1].startswith("refs/remotes/"):
                self.git_deny(f"git {sub} refspec {a!r} would write a local ref other than refs/remotes/*")

    def git_config_env(self, assigns, words):
        """T-0755: NO_PUSH_ROLES never set GIT_CONFIG_COUNT/KEY_*/VALUE_*/PARAMETERS (env-injected alias hides a push), nor
        HOME, XDG_CONFIG_HOME, PATH, PAGER or any GIT_* variable (another config file or helper), by assignment, export,
        declare or unset."""
        names = list(assigns)
        if words and os.path.basename(words[0]) in DECLARERS | {"unset"}:
            names += words[1:]
        if any(GIT_CONFIG_ENV.match(x) for x in names):
            raise Block(f"{self.role} may not set GIT_CONFIG_COUNT/GIT_CONFIG_KEY_*/GIT_CONFIG_VALUE_*/GIT_CONFIG_PARAMETERS: "
                        "env-injected git config can alias a push; ask your lead")
        if any(GIT_SENSITIVE_ENV.match(x) for x in names):
            raise Block(f"{self.role} may not set, export or unset HOME, XDG_CONFIG_HOME, PATH, PAGER or GIT_*: they select "
                        "the git config, helpers and pager (an alias there hides a push); ask your lead")

    def call_assign(self, text, bodies):
        """T-0771 (attempt 3): whole-call checks for NO_PUSH_ROLES: a `{NAME}>f` named-fd redirect assigns NAME; a call that
        declares a nameref (-n) may not name a protected variable anywhere (the split `declare -n r; r=HOME; read r` form);
        ${VAR:=x} in a heredoc body is scanned like one in a word."""
        for m in re.finditer(r"(?<![\w$\\])\{([A-Za-z_]\w*|[^\s{}]*\$[^\s{}]*)\}(?=[<>])", text):
            name = m.group(1)
            if PROTECTED_NAME.match(name) or "$" in name:
                raise Block(f"{self.role} may not assign HOME, XDG_CONFIG_HOME, PATH, PAGER or GIT_* with a named-fd redirect "
                            f"({{{name}}}>: it assigns the variable, or the name is dynamic); use a plain variable name, "
                            "or ask your lead")
        for m in re.finditer(r"(?<![\w-])coproc\s+([^\s{(;|&<>]+)\s+(?=[^\s;|&)])", text):  # coproc is stripped by cmd()
            name = m.group(1).strip("'\"")
            if PROTECTED_NAME.match(name) or re.search(r"[$`!*?{\[]", name):
                raise Block(f"{self.role} may not assign HOME, XDG_CONFIG_HOME, PATH, PAGER or GIT_* with coproc NAME "
                            f"({name!r}: it assigns the variable, or the name is dynamic); use a plain variable name, "
                            "or ask your lead")
        if re.search(r"(?<![\w-])(declare|typeset|local)\b[^;&|\n]*\s-[A-Za-z]*n", text) and \
                re.search(r"(?<![\w])(HOME|XDG_CONFIG_HOME|PATH|path|PAGER|GIT_\w*)(?![\w])", text):
            raise Block(f"{self.role} may not assign HOME, XDG_CONFIG_HOME, PATH, PAGER or GIT_* ... in a call that declares a "
                        "nameref (-n): the nameref could assign it later; declare the nameref in a call that does not name "
                        "them, or ask your lead")
        for body in bodies:
            self.builtin_assign([], [], (body,))

    def builtin_assign(self, assigns, words, extra=()):
        """T-0771: NO_PUSH_ROLES may not assign HOME, XDG_CONFIG_HOME, PATH, PAGER or GIT_* through a builtin either (read,
        printf -v, mapfile/readarray, for/select, getopts, let, declare-like, `${VAR:=x}` / `${VAR=x}`), and fail closed
        when the target name is dynamic."""
        def bad(name):
            return bool(PROTECTED_NAME.match(re.sub(r"\[\d+\]$", "", name)))  # HOME[0] is HOME

        def dyn(name):
            # expansion/glob/brace/subscript other than [digits] can name HOME at run time: fail closed
            return bool(re.search(r"[$`!*?{]", name)) or bool(re.search(r"\[", re.sub(r"\[\d+\]$", "", name)))

        def deny(what, name, verb="assign"):
            why = "dynamic target name" if dyn(name) else "it selects the git config, helpers or pager"
            raise Block(f"{self.role} may not {verb} HOME, XDG_CONFIG_HOME, PATH, PAGER or GIT_* (or zsh path) with {what} ({name!r}: "
                        f"{why}); use a plain variable name, or ask your lead")

        for w in [*assigns, *words, *extra]:
            m = PARAM_DEFAULT.search(w)
            if m and m.group(1).startswith("!"):
                deny("an indirect ${!VAR:=...} default", m.group(1))
            if m and (bad(m.group(1)) or dyn(m.group(1))):
                deny("a ${VAR:=...} default", m.group(1))
        if not words:
            return
        m = re.match(r"([A-Za-z_]\w*)\[[^\]]*\]\+?=", words[0])  # T-0788: `path[1]=x` / `PATH[1]=x` is an assignment
        if m and bad(m.group(1)):
            deny("a subscript assignment", m.group(1))
        name, rest = os.path.basename(words[0]), words[1:]
        targets = []
        if name == "env":  # T-0788: removing a protected name (env -u, env -i) strips the pane's git isolation
            for kind, v in env_options(rest):  # T-0964: shared parser; a dash word is never a value
                if kind == "clear":
                    deny("env " + v, "the whole environment", "clear or unset")
                if kind == "unknown":
                    deny("env " + v, "an env option this guard does not model", "run")
                if kind == "unset" and (bad(v) or dyn(v)):
                    deny("env -u", v, "unset")
        elif name == "unset":
            for a in rest:
                if not a.startswith("-") and (bad(a) or dyn(a)):
                    deny("unset", a, "unset")
        elif name in ("read", "mapfile", "readarray"):
            argopt = set("padnNtui") if name == "read" else set("dnOsuCc")
            if name == "read":
                argopt.add("A")
            skip = None
            for a in rest:
                if skip is not None:
                    if skip in "aA":
                        targets.append(a)
                    skip = None
                elif a.startswith("-") and len(a) > 1:
                    for k, c in enumerate(a[1:], 1):
                        if c in argopt:  # the rest of the cluster (-aHOME) or the next word is its argument
                            if a[k + 1:]:
                                if c in "aA":
                                    targets.append(a[k + 1:])
                            else:
                                skip = c
                            break
                else:
                    targets.append(a)
        elif name in ("printf", "print"):
            for j, a in enumerate(rest):
                if a == "-v" and j + 1 < len(rest):
                    targets.append(rest[j + 1])
                elif a.startswith("-v") and len(a) > 2:
                    targets.append(a[2:])
                elif name == "print" and re.fullmatch(r"-[A-Za-z]*v", a) and j + 1 < len(rest):  # T-0788: cluster -rv NAME
                    targets.append(rest[j + 1])
        elif name in ("for", "select", "foreach") and rest:
            for a in rest:  # T-0826: zsh assigns every name before `(` or `in`
                if a == "in" or a.startswith("("):
                    break
                targets.append(a)
        elif name == "vared":  # T-0826: vared NAME edits (assigns) the parameter; options take values, so fail closed on every word
            targets += [a for a in rest if not a.startswith("-")]
        elif name in ("integer", "float"):  # T-0826: zsh declarers: `integer path` re-types the PATH-tied array
            targets += [a.split("=", 1)[0] for a in rest if not a.startswith("-") and not a.startswith("+")]
        elif name == "set":  # T-0788: `set -A path ...` assigns the array
            for j, a in enumerate(rest):
                if a in ("-A", "+A", "-a", "+a") and j + 1 < len(rest):
                    targets.append(rest[j + 1])
        elif name == "zparseopts":  # T-0788: -a ARRAY / -A ASSOC name the variable they assign
            for j, a in enumerate(rest):
                if a in ("-a", "-A") and j + 1 < len(rest):
                    targets.append(rest[j + 1])
                elif a[:2] in ("-a", "-A") and len(a) > 2:
                    targets.append(a[2:])
        elif name == "wait":
            for j, a in enumerate(rest):
                if a.startswith("-") and not a.startswith("--") and "p" in a:
                    tail = a[a.index("p") + 1:]  # -pNAME, or the next word
                    if tail:
                        targets.append(tail)
                    elif j + 1 < len(rest):
                        targets.append(rest[j + 1])
        elif name == "getopts":
            targets += [a for a in rest if not a.startswith("-")][:2]  # optstring too: fail closed
        elif name == "let":
            for a in rest:
                targets += re.findall(r"[A-Za-z_]\w*|\$\S*", a.split("=", 1)[0] if "=" in a else a)
        elif name in DECLARERS:
            targets += [a.split("=", 1)[0] for a in rest if not a.startswith("-")]
            if any(a.startswith("-") and not a.startswith("--") and "n" in a for a in rest):
                for a in rest:  # nameref: declare -n r=HOME makes `read r` assign HOME
                    if not a.startswith("-") and "=" in a:
                        v = a.split("=", 1)[1]
                        if bad(v) or dyn(v):
                            deny(name + " -n", v)
        for t in targets:
            if bad(t) or dyn(t):
                deny(name, t)

    def task_worktree(self, task):
        """T-0743: realpath of tasks.worktree for the task (read-only DB); None on a missing DB/row/path (fail closed)."""
        try:
            con = sqlite3.connect(f"file:{self.db_path()}?mode=ro", uri=True, timeout=2)
            try:
                row = con.execute("SELECT worktree FROM tasks WHERE id = ?", (task.upper(),)).fetchone()
            finally:
                con.close()
        except sqlite3.Error:
            return None
        return os.path.realpath(row[0]) if row and row[0] else None

    def cmd(self, words, depth, piped, in_loose, hstrs=(), env_assigns=(), stdin=None):
        words = list(words)
        # T-0882: a wrapped interpreter keeps the wrapper's stdin (here-strings, the echo/printf literal piped in)
        stdin_piped, stdin_hs, stdin_pipe = stdin if stdin else (piped, hstrs, getattr(self, "pipe_src", None))
        assigns = list(env_assigns)
        stripped = strip_prefix(words)
        assigns += [x for x in words[:len(words) - len(stripped)] if ASSIGN.match(x)]
        words = stripped
        while words and (words[0] in ("noglob", "nocorrect", "-") or words[0] == "repeat" and len(words) > 1):
            if words[0] == "repeat":  # T-0826: zsh evaluates the count as arithmetic (`repeat PATH=1 ls` assigns PATH)
                if not re.fullmatch(r"[0-9]+", words[1]) and self.role in NO_PUSH_ROLES and not self.hq:
                    raise Block(f"{self.role} may not use a non-literal repeat count ({words[1]!r}: zsh evaluates it as "
                                "arithmetic and can assign PATH or GIT_*); use a plain integer, or ask your lead")
                words = words[2:]
            else:
                words = words[1:]  # T-0826: zsh precommand modifiers and the `-` separator run the next command word
            stripped = strip_prefix(words)
            assigns += [x for x in words[:len(words) - len(stripped)] if ASSIGN.match(x)]
            words = stripped
        self.may_create_before = self.may_create
        if words and not self.listing_only(words):
            self.may_create = True
        if self.role in NO_PUSH_ROLES and not self.hq:
            # T-0331: a one-command `HOME=<scratch> FACTORY_HOME=<scratch> factory init|bash install.sh --dry-run`
            chk = [x for x in assigns if not (x.startswith("HOME=") and self.scratch_form(words, assigns))]
            self.git_config_env(chk, words)
            self.builtin_assign(chk, words, hstrs)
            self.git_launch(chk, words)
        for x in assigns if not words else []:
            if ASSIGN.match(x) and "+=" not in x.split("=", 1)[0] + "=":
                self.vars[x.split("=", 1)[0]] = x.split("=", 1)[1]
        if not words:
            if not self.hq:
                self.check_identity(assigns)
            return
        if os.path.basename(words[0]) == "git":
            self.git_live_push_guard(words[1:])  # T-1200: before the push pin; shared-origin overrides and credentialed URLs
            self.git_push_pin(words[1:])  # before the public-text push check: names the one allowed push
            self.git_runners(words[1:], depth)
        if self.pt:
            self.pt.command(words, self.hq)
        if not in_loose:
            self.priv(words, depth, piped, hstrs)
            if self.hq and not (self.role == "lead" and self._lead_factory_goal_words(words)):
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
            raise DynamicBlock(f"dynamic command word {w!r} could expand to herdr, hc or claude")
        if is_herdr(w):
            raise Block("direct herdr invocation")
        if is_claude(w):
            raise ClaudeBlock("direct claude invocation")
        name = os.path.basename(w)
        if name in ("cd", "pushd", "popd") and not in_loose:
            self.git_cd(name, rest)
            self.cfg_cd(name, rest)
        self.claude_launchers(name, rest)
        self.sandbox_route(name, rest)
        self.cfg_write(name, rest)
        self.prot_write(name, rest, assigns)
        self.persist_write(name, rest)
        if self.role in NO_PUSH_ROLES and not self.hq:
            if self.archive_extract(name, rest) or name in FS_MUTATORS or inline_interpreter(name, rest):
                self.fs_changed = True
        self.factory_unit(name, rest, assigns)
        if self.role == RESEARCHER and not self.hq:
            self.researcher_cmd(name, rest)
        if self.role in NO_PUSH_ROLES and not self.hq and name == "gh":
            self.nopush_gh_write(rest)
        if name in COPIERS:  # R3: no copying/encoding/reading the real herdr binary
            for a in rest:
                a = a.split("=", 1)[1] if re.match(r"^(if|of)=", a) else a
                if is_herdr(a) or "HERDR_BIN_PATH" in a:
                    raise Block(f"{name} on the herdr binary")
        if name in SHELLS:
            saved = (self.gcwd, self.gdyn, self.ccwd, self.cwd_unres)  # a cd inside `sh -c` does not move the caller
            self.shell_assigns = assigns
            try:
                self.shell(rest, depth, piped, hstrs)
            finally:
                self.gcwd, self.gdyn, self.ccwd, self.cwd_unres = saved
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
            if self.role in NO_PUSH_ROLES and not self.hq:
                self.interp_inline_push(name, rest)
                if self.stdin_program(name, rest):  # T-0882: here-string, heredoc, echo|printf piped in
                    for h in list(stdin_hs) + ([stdin_pipe] if stdin_piped and stdin_pipe else []):
                        self.stdin_scan(name, h)
            self.unittest_full(name, rest)
            self.pytest_full(name, rest)
            for j, a in enumerate(rest[:-1]):
                if (re.match(r"^-[A-Za-z]*[ceEr]$", a) or a == "--eval") and HOST_CLI_WORD.search(rest[j + 1]):
                    raise Block("herdr in inline interpreter code")
        elif name in ("pytest", "py.test"):
            self.pytest_full(name, rest)
        elif name in AWKS or name in EDITORS:
            if any(HOST_CLI_WORD.search(a) for a in rest):
                raise Block(f"herdr in {name} program text (can run shell commands)")
        elif name == "sed":
            for a in rest:
                if HOST_CLI_WORD.search(a) and re.search(r"(?:^|[\s;{}\d$,])e\b|/[gIpmM]*e[gIpmM]*(?:\s|;|$)", a):
                    raise Block("herdr in a sed e command")
        elif name == "git":
            self.git_shared(words, assigns)  # first: its reason names the own-worktree advice
            self.git_delete(rest)
            if self.role in NO_PUSH_ROLES and not self.hq:
                self.git_allow(rest)  # T-0755: after git_delete, whose reason names the branch-deletion rule
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
        if name in ("uv", "uvx"):  # T-0880: `uv run CMD` / `uvx CMD` is checked like CMD run directly
            self.uv_check(name, rest, depth, piped, in_loose, hstrs, assigns)
        if name == "command" and rest[:1] in (["-v"], ["-V"], ["-pv"]):
            return
        if name == "find":
            for j, a in enumerate(rest):
                if a in ("-exec", "-execdir", "-ok", "-okdir"):
                    self.loose(rest[j + 1:], depth)
        elif name in WRAPPERS and not in_loose:
            if name == "xargs":
                self.xargs(rest)
            self.loose(rest, depth, assigns, (stdin_piped, stdin_hs, stdin_pipe))

    def test_sandbox_on(self):
        """T-0675: config test_sandbox is 1, read-only from the factory DB. Fails open (False) on a missing or unreadable DB or table."""
        try:
            con = sqlite3.connect(f"file:{self.db_path()}?mode=ro", uri=True, timeout=2)
            try:
                row = con.execute("SELECT value FROM config WHERE key = 'test_sandbox'").fetchone()
            finally:
                con.close()
        except sqlite3.Error:
            return False
        return bool(row) and str(row[0]).strip() == "1"

    @staticmethod
    def python_m_module(rest):
        """T-0675: walk CPython argv like the interpreter. Returns the -m module name, None when the command runs a
        script, stdin or -c (or has no -m), or "?" for an option this walker does not know."""
        return Analyzer.python_m_split(rest)[0]

    @staticmethod
    def python_m_split(rest):
        """T-0735: the one CPython argv walker. Returns (module, args after the module); (None, []) when the command
        runs a script, stdin or -c (or has no -m); ("?", tokens after the unknown option) for an option it does not know."""
        i = 0
        while i < len(rest):
            tok = rest[i]
            i += 1
            if tok == "--" or tok == "-" or not tok.startswith("-"):
                return None, []
            if tok.startswith("--"):
                if tok == "--check-hash-based-pycs":
                    i += 1
                elif not (tok.startswith("--check-hash-based-pycs=") or tok == "--version" or tok.startswith("--help")):
                    return "?", rest[i:]
                continue
            for k, ch in enumerate(tok[1:], 1):
                if ch in "bBdEhiIOPqRsSuvVx?":
                    continue
                if ch in "WX":
                    if k == len(tok) - 1:
                        i += 1  # value is the next token, even one that starts with "-"
                    break
                if ch == "m":
                    if tok[k + 1:]:
                        return tok[k + 1:], rest[i:]
                    return (rest[i], rest[i + 1:]) if i < len(rest) else (None, [])
                if ch == "c":
                    return None, []
                return "?", rest[i:]
        return None, []

    def sandbox_route(self, name, rest):
        """T-0675 (ADR M11673 T-d): with test_sandbox=1, coder and qa run test runners through `factory test <cmd>`,
        not bare. Runners: python -m unittest/pytest, pytest, go test. `factory test ...` never reaches here as a
        runner: its own words are `factory`, and its arguments are not parsed as commands."""
        if self.role not in ("coder", "qa"):
            return
        runner = None
        if re.fullmatch(r"python[\d.]*", name):
            mod = self.python_m_module(rest)
            if mod == "?":  # unknown interpreter option: fail closed on any later -m naming a runner
                mod = next((m for m in (t[2:] if t.startswith("-m") and len(t) > 2 else rest[j + 1] if t == "-m" and rest[j + 1:j + 2] else None
                                        for j, t in enumerate(rest)) if m and m.split(".")[0] in ("unittest", "pytest")), None)
            if mod and mod.split(".")[0] in ("unittest", "pytest"):
                runner = f"python3 -m {mod}"
            else:  # T-0745: python3 -m coverage run -m pytest
                cmod, cargs = self.python_m_split(rest)
                while cmod == "?":
                    cmod, cargs = self.python_m_split(cargs)
                if cmod == "coverage" and cargs[:1] == ["run"]:
                    cov = self.coverage_run_module(cargs[1:])
                    if cov:
                        runner = f"python3 -m coverage run -m {cov}"
        elif name in ("pytest", "py.test"):
            runner = name
        elif name == "go" and self.go_subcommand(rest) == "test":
            runner = "go test"
        elif name == "tox":
            runner = "tox"
        elif name == "coverage" and rest[:1] == ["run"]:
            mod = self.coverage_run_module(rest[1:])
            if mod:
                runner = f"coverage run -m {mod}"
        elif name == "uv" and rest[:1] == ["run"]:
            runner = self.uv_run_runner(rest[1:])
        elif name == "uv" and "tool" in rest:  # T-0745, T-0748: global options may sit between tool and run
            runner = self.uvx_scan(rest, "uv tool run")
        elif name == "uvx":  # T-0745, T-0748
            runner = self.uvx_scan(rest, "uvx")
        if runner and self.test_sandbox_on():
            raise Block(f"bare test runner `{runner}` with test_sandbox=1: run it as `factory test {name} {' '.join(rest)}` "
                        "(runs under the test sandbox, T-0675)")

    @staticmethod
    def coverage_run_module(args):
        """T-0745: fail closed. Any word after `coverage run` naming pytest/py.test/unittest is the runner;
        no option model, so flags, valued options and clusters cannot hide it. Never returns early on a non-match.
        Accepted false positive: `coverage run app.py pytest` goes to the test slot."""
        for t in args:
            if t.startswith("--module="):
                cand = t[9:]
            elif t.startswith("-") and not t.startswith("--"):
                cand = t[t.index("m") + 1:] if "m" in t else ""   # -mpytest, -pmpytest
            elif t.startswith("-"):
                cand = t.split("=", 1)[1] if "=" in t else ""
            else:
                cand = t
            base = os.path.basename(cand)
            if base == "py.test" or base.split(".")[0] in ("pytest", "unittest"):
                return cand
        return None

    @staticmethod
    def go_subcommand(rest):
        """T-0735: first go word that is not an option; `-C <dir>` / `--C <dir>` takes a value."""
        i = 0
        while i < len(rest):
            a = rest[i]
            i += 2 if a in ("-C", "--C") else 1
            if not a.startswith("-"):
                return a
        return None

    @staticmethod
    def uvx_scan(words, prefix):
        """T-0748: fail closed. Any word after uvx / `uv [global opts] tool run` naming pytest/py.test/unittest
        is the runner, after stripping extras ([..]) and version suffixes (at-sign, ==). No option model, so index
        options, valued flags and global options cannot hide it. Accepted false positive: `uvx ruff pytest`."""
        if prefix == "uv tool run":
            words = words[words.index("tool") + 1:]
        for t in words:
            if t.startswith("-"):
                t = t.split("=", 1)[1] if "=" in t else ""
            base = re.split(r"[\s\[@=<>~!;(]", os.path.basename(t), maxsplit=1)[0].lower()  # uvx PyTest is pytest
            if base in ("pytest", "py.test", "unittest"):
                return f"{prefix} {base}"
        return None

    @staticmethod
    def uv_inner(name, rest):
        """T-0880: the words after `uv [opts] run` / `uv [opts] tool run` / `uvx`, or [] when uv runs nothing."""
        if name == "uvx":
            return rest
        return rest[rest.index("run") + 1:] if "run" in rest else []

    UV_VALUED = {"--with", "--with-requirements", "-p", "--python", "--project", "--directory", "--env-file",
                 "--extra", "--group", "--package", "--only-group", "--no-group", "--with-editable", "-w", "--index",
                 "--config-file", "--from"}

    def uv_check(self, name, rest, depth, piped, in_loose, hstrs, assigns):
        """T-0880: `uv run [opts] [--] CMD...` / `uvx [opts] CMD...` is checked like CMD run directly, for the coder and
        the no-push roles only (cleaner, judge and HQ stay as before). Only the first word after the uv options gets
        the full command check (exactly the bare command's rules). Later words are a closed predicate: a word whose
        basename is git followed by the word push (coder and no-push roles), or, for no-push roles, a gh word matching
        INLINE_GH_WRITE or inline interpreter code (T-0789). No later word reaches exec_path, kill or any other rule
        (accepted false positive: `uv run python3 x.py git push`). Defense in depth only; the sandbox + worker PAT are the boundary (M15349)."""
        if self.hq or not (self.role == "coder" or self.role in NO_PUSH_ROLES):
            return
        words = self.uv_inner(name, rest)
        i = 0
        while i < len(words):
            a = words[i]
            if a == "--":
                i += 1
                break
            if not a.startswith("-"):
                break
            i += 2 if a in self.UV_VALUED else 1
        for k in range(i, len(words)):  # git / inline code: later words only, so values of known options are never scanned
            cand = words[k:]
            base = os.path.basename(cand[0])
            if base == "git" and k > i and "push" in cand[1:]:
                raise Block(f"{name} running git push behind an unknown uv option; push only your own task branch "
                            "with a plain `git push origin HEAD:factory/<task>`")
            if self.role in NO_PUSH_ROLES:
                if base == "gh" and INLINE_GH_WRITE.search(" ".join(cand)):  # item 1: includes the command word
                    raise Block(f"{name} running a gh write (pr merge/create, repo, api with a write method); "
                                "no-push roles never write to GitHub; ask your lead")
                if k > i:
                    self.interp_inline_push(base, cand[1:])  # inline code after an unknown valued option (T-0789 scan)
        if i < len(words):
            self.cmd(words[i:], depth + 1, piped, in_loose, hstrs, assigns)

    def uv_run_runner(self, rest, prefix="uv run"):
        """T-0735: `uv run [opts] pytest ...` or `uv run [opts] python -m pytest ...`; returns the runner text or None."""
        valued = {"--with", "--with-requirements", "-p", "--python", "--project", "--directory", "--extra", "--group",
                  "--env-file", "--package", "--only-group", "--no-group", "--with-editable", "-w", "--index", "--config-file"}
        i = 0
        while i < len(rest):
            a = rest[i]
            i += 1
            if prefix != "uv run" and a == "--from":  # uvx / uv tool run: --from <spec> takes a value
                i += 1
                continue
            if prefix != "uv run" and not a.startswith("-"):
                a = a.split("@", 1)[0]  # drop a version suffix after the at-sign
            if a == "-m" and i < len(rest):
                a, rest = "python", ["-m"] + rest[i:]
                i = 0
            elif a in valued:
                i += 1
                continue
            elif a.startswith("-"):
                continue
            if a in ("pytest", "py.test"):
                return f"{prefix} {a}"
            if re.fullmatch(r"python[\d.]*", a):
                mod = self.python_m_module(rest[i:])
                if mod and mod.split(".")[0] in ("unittest", "pytest"):
                    return f"{prefix} {a} -m {mod}"
            return None
        return None

    def unittest_full(self, name, rest):
        """T-0642: coder, qa and reviewer run no full suite locally (CI is the full gate). Blocked: `python -m unittest`
        with no test argument, `... discover` unless -p/--pattern names one module (no wildcard). Allowed:
        `python -m unittest tests.test_x` and `unittest discover -s tests -p test_x.py`."""
        if self.role not in ("coder", "qa", "reviewer") or not re.fullmatch(r"python[\d.]*", name):
            return
        mod, args = self.python_m_split(rest)
        while mod == "?":  # unknown interpreter option: keep looking for a later -m
            mod, args = self.python_m_split(args)
        if mod not in ("unittest", "unittest.__main__"):
            return
        advice = FULL_SUITE_ADVICE
        if args[:1] == ["discover"]:
            pats = [args[i + 1] for i, a in enumerate(args[:-1]) if a in ("-p", "--pattern")]
            pats += [a.split("=", 1)[1] for a in args if a.startswith("--pattern=")]
            if pats and all(re.fullmatch(r"test_[\w]+\.py", x) for x in pats):  # the last pattern wins, so all must be single-module
                return
            if "-k" in args[1:]:  # T-0435: a -k name filter is targeted
                return
            raise Block("`python3 -m unittest discover` without a single-module -p pattern runs the full suite" + advice)
        mods = []
        skip = False
        for a in args:
            if skip:
                skip = False
            elif a in ("-k", "--start-directory", "-s", "-t", "-p"):
                skip = True
                if a == "-k":
                    mods.append(a)  # T-0435: -k name filters the run
            elif not a.startswith("-"):
                mods.append(a)
        if not mods:
            raise Block("`python3 -m unittest` with no test module runs the full suite" + advice)

    PYTEST_VALUED = frozenset(("-m", "-c", "-p", "-o", "-W", "-n", "--rootdir", "--maxfail", "--deselect", "--ignore",
                               "--ignore-glob", "--confcutdir", "--basetemp", "--junitxml", "--durations", "--tb",
                               "--log-level", "--cov", "--cov-report", "--import-mode", "--color", "--capture",
                               "--override-ini", "--config-file", "--junit-xml", "--log-file"))

    @staticmethod
    def pytest_target(a):
        """A token is a pytest target only if its part before the first `::` ends in .py and has no glob char."""
        f = a.split("::", 1)[0]
        return f.endswith(".py") and not any(c in f for c in "*?[")

    def pytest_full(self, name, rest):
        """T-0435: coder, qa and reviewer run no full pytest suite locally. Blocked: pytest with no path and no -k, or
        with tests/ (or .) as the only path. Allowed: a file or node path, a -k expression."""
        if self.role not in ("coder", "qa", "reviewer") or self.hq:
            return
        if name in ("pytest", "py.test"):
            args = rest
        elif re.fullmatch(r"python[\d.]*", name):
            mod, args = self.python_m_split(rest)
            while mod == "?":
                mod, args = self.python_m_split(args)
            if mod not in ("pytest", "pytest.__main__"):
                return
        else:
            return
        skip = False
        for a in args:
            if skip:
                skip = False
            elif a == "-k" or (a.startswith("-k") and not a.startswith("--")):
                return  # a -k filter is targeted ("--keep-duplicates" is not -k)
            elif a in self.PYTEST_VALUED:
                skip = True
            elif not a.startswith("-") and self.pytest_target(a):
                return  # a test file or node id; a directory or an option value is never a target
        raise Block("pytest with no test file or -k runs the full suite" + FULL_SUITE_ADVICE)

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

    def loose(self, rest, depth, env_assigns=(), stdin=None):
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
                     env_assigns=[*env_assigns, *(x for x in rest[:k] if x.startswith("FACTORY_HOME="))], stdin=stdin)

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
            elif a in WRAP_VALUE_OPTS["xargs"]:
                j += 2
                continue
            elif not a.startswith("-"):
                first = a
                break
            j += 1
        if repl is not None and first == repl:
            raise Block("xargs replacement string used as the command word")
        if self.role == "coder" and not self.hq:
            n = sum(1 for w in rest if XARGS_GIT_TOKEN.search(w))
            if n:
                # fail closed: allowed only as exactly one git word that is the command word itself (no launcher between)
                if not (n == 1 and first is not None and os.path.basename(first) == "git"):
                    raise Block("xargs with git: the command or its arguments could come from stdin; run git directly, "
                                "not through xargs")
                words = rest[j:]
                if repl is not None and any(repl in w for w in words[1:]):
                    raise Block("xargs git: the replacement string stands in for part of git's arguments (an alias or "
                                "subcommand could come from stdin); coders push only `git push origin HEAD:factory/<task id>`")
                self.xargs_git(words[1:])

    def xargs_git(self, gargs):
        """T-0799: `xargs git ...` takes part of its argv from stdin, so the subcommand may be `push` unseen. A coder may
        run xargs git only with a literal subcommand word in the command line itself, never a replacement/stdin-supplied
        one, and the usual push pin and alias refusal apply to the words that are visible."""
        i = 0
        while i < len(gargs) and gargs[i].startswith("-"):
            cv = gargs[i + 1] if gargs[i] == "-c" and i + 1 < len(gargs) else (gargs[i][2:] if re.match(r"-c.", gargs[i]) else None)
            if cv is not None and re.match(r"(?i)alias\.", cv):
                raise Block("xargs git -c alias.<x>=...: the alias could expand to config or push with arguments from stdin; "
                            "run git directly")
            i += 2 if gargs[i] in ("-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path") else 1
        if i >= len(gargs) or not re.fullmatch(r"[A-Za-z][A-Za-z0-9-]*", gargs[i]) or gargs[i] in ("push", "send-pack", "http-push", "subtree"):
            raise Block("xargs git: the subcommand would come from stdin or could be push; coders push only "
                        "`git push origin HEAD:factory/<task id>` as the whole command: write `git <subcommand> ...` directly")
        if gargs[i] == "config":  # T-1035 + #371: refuse unless BOTH main's read-flag rule and the explicit-action exact-read rule hold
            cargs = gargs[i + 1:]
            parsed = config_parse(cargs)
            explicit = bool(parsed) and (any(f in CONFIG_ACTION_FLAGS for f in parsed[0]) or parsed[1][:1] in (["get"], ["list"]))
            main_ok = any(a in GIT_CONFIG_READ_ONLY for a in cargs) and not any(a in CONFIG_WRITE for a in cargs)
            if not (main_ok and explicit and config_is_read(cargs)):
                raise Block("xargs git config: stdin adds arguments, so only a read with an explicit action (`--get`, "
                            "`--get-all`, `--get-regexp`, `--list`, `-l`) and exact options is allowed; run `git config` directly")
        sub = gargs[i]
        if sub in ("rebase", "difftool"):
            raise Block(f"xargs git {sub}: stdin adds arguments, so the -x/--exec command cannot be inspected; run git {sub} directly")
        if sub in ("submodule", "bisect"):
            word, run = None, "foreach" if sub == "submodule" else "run"
            for w in gargs[i + 1:]:
                if not w.startswith("-"):
                    word = w
                    break
            if word is None or not re.fullmatch(r"[A-Za-z][A-Za-z0-9-]*", word) or word == run:
                raise Block(f"xargs git {sub}: stdin adds arguments, so without a literal subcommand other than {run} the inner "
                            f"command cannot be inspected; run git {sub} directly")
        self.git_push_pin(gargs)

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
            if i or not self.install_dry_run(rest, self.shell_assigns):  # T-0331: the one unparsed script (no shell options)
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
        elif self.role in NO_PUSH_ROLES and not self.hq and not head.startswith(b"\x7fELF"):
            if first.startswith(b"#!"):
                self.git_deny(f"{path} is a script for another interpreter, whose body this hook does not inspect")
            self.script(self.read(path), depth + 1)  # no shebang: the kernel hands it to sh, so read it as sh

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
            if re.match(r"^(factory|herdr|hc-)", u) or GLOB.search(u) or "$" in u or "`" in u:
                raise Block(f"systemctl {verbs[0]} {u}")


FULL_SUITE_ADVICE = ("; CI runs the full suite (sharded). Locally run `python3 ci/affected.py --base origin/main --quick --run`, "
                     "or one module: `python3 -m unittest tests.test_X` or `python3 -m unittest discover -s tests -p test_X.py`"
                     " (T-0642). QA that needs a merged-tree full run asks its lead (T-0435)")


PUNCT_CHARS = "();<>|&"


def mark_punct_words(text):
    """T-0983 (find): shlex in posix mode drops quote state, so a word the shell passes as data (`';'`, `\\;`, `"("`,
    `\\(`) comes out as a bare operator token and is_punct cuts the command there. This walks the real quote state word
    by word (words end at whitespace outside quotes): a word that holds a quote or a backslash, whose dequoted value is
    made only of `();<>|&` and whose every such character is quoted or escaped, becomes a sentinel word. Returns
    (text, {sentinel: value}). An unquoted `;` stays a separator; every other word is untouched."""
    sent = "__PUNCT_WORD__"
    while sent in text:
        sent += "_"
    out, vals, i, n = [], {}, 0, len(text)
    while i < n:
        if text[i] in " \t\r\n":
            out.append(text[i])
            i += 1
            continue
        j, q, val, quoted, bare_punct = i, None, [], False, False
        while j < n and (q or text[j] not in " \t\r\n"):
            c = text[j]
            if q == "'":
                if c == "'":
                    q = None
                else:
                    val.append(c)
            elif q == '"':
                if c == '"':
                    q = None
                elif c == "\\" and j + 1 < n and text[j + 1] in '"\\$`':
                    val.append(text[j + 1])
                    j += 1
                else:
                    val.append(c)
            elif c in "'\"":
                q, quoted = c, True
            elif c == "\\" and j + 1 < n:
                quoted = True
                val.append(text[j + 1])
                j += 1
            else:
                val.append(c)
                bare_punct = bare_punct or c in PUNCT_CHARS
            j += 1
        v = "".join(val)
        if q is None and quoted and v and not bare_punct and all(c in PUNCT_CHARS for c in v):
            key = f"{sent}{len(vals)}__"
            vals[key] = v
            out.append(key)
        else:
            out.append(text[i:j])
        i = j
    return "".join(out), vals


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


R2_ASSIGN_WORDS = frozenset({"export", "readonly", "declare", "typeset"})
R2_ASSIGN_WRAPPERS = frozenset({"eval", "command", "builtin"})


def r2_segment_words(seg):
    """Words of one segment with bash quote removal; segments() mask blanks quoted spans for splitting only."""
    seg = seg.strip()
    if not seg:
        return []
    try:
        lex = shlex.shlex(seg, posix=True)
        lex.whitespace_split = True
        return list(lex)
    except ValueError:
        return seg.split()


def r2_assign_builtin_words(cmd):
    """True when a simple-command starts with export/readonly/declare/typeset (not mere mention in data args)."""
    for start, end, _ in segments(cmd):
        rest = r2_segment_words(cmd[start:end])
        while rest and (rest[0] in RESERVED or ASSIGN.match(rest[0])):
            kw = rest.pop(0)
            if kw == "time":
                while rest and rest[0].startswith("-"):
                    rest.pop(0)
        if not rest:
            continue
        head = rest[0]
        if head in R2_ASSIGN_WORDS:
            return True
        if head in R2_ASSIGN_WRAPPERS and len(rest) > 1 and rest[1] in R2_ASSIGN_WORDS:
            return True
    return False


def r2_set_hit(cmd):
    """R2 socket/session assignment: in data-only commands, VAR=value in arguments is text, not a retarget."""
    for start, end, words in segments(cmd):
        seg = cmd[start:end]
        if not words or os.path.basename(words[0]) not in DATA_CMDS:
            if R2_SET.search(seg):
                return True
        else:
            if R2_SET_SHELL.search(seg):
                return True
            rest = list(words)
            while rest and ASSIGN.match(rest[0]):
                if R2_SET_VAR.search(rest[0]):
                    return True
                rest = rest[1:]
    if r2_assign_builtin_words(cmd) and R2_SET_VAR.search(cmd):
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


def unzip_listing(args):
    """T-0805: unzip reads options only before the zip name (a later -l is a member pattern, not a listing flag)."""
    k = 0
    while k < len(args):
        a = args[k]
        if a in UNZIP_LISTING:
            return True
        if a == "-d":
            k += 1
        elif not a.startswith("-") or a == "-":
            return False  # the zip name: the rest are member patterns
        k += 1
    return False


def parse_tar(args):
    """T-0805 P1: the one grammar for tar/gtar/bsdtar arguments. Returns (extract, archives, dirs, problems). A short word
    (single leading `-`, or the first word without one) is classified only when it is `-C<dir>` (C first) or letters only,
    every letter in TAR_SHORT_OK or `t` (g, K, N, T, I, s ... are unclassifiable), with at most one value letter (f or C) and that letter last; anything else (glued `-xf<path>`, `-xfC`, digits, `/`) is
    unclassifiable and counts as extract mode, so E1 refuses it (unknown = extract, fail closed). `problems` lists every
    reason the argv is outside the E1 allowlist (non-empty with extract = refuse)."""
    extract, archives, dirs, problems, marks = False, [], [], [], []
    k = 0
    while k < len(args):
        a, j = args[k], k
        k += 1
        if a == "--":
            problems.append("a literal `--` (the hook and tar would disagree on what follows)")
        elif a.startswith("--"):
            opt, eq, val = a.partition("=")
            extract = extract or (len(opt) >= 5 and "--extract".startswith(opt)) or (len(opt) >= 4 and "--get".startswith(opt))
            if opt in TAR_LONG_VALUE:
                if not eq:
                    if k >= len(args):
                        problems.append(f"{opt} without a value")
                        continue
                    val, k = args[k], k + 1
                (archives if opt == "--file" else dirs).append(val)
            elif opt not in TAR_LONG_FLAG or eq:
                problems.append(f"option {a!r}")
        elif a.startswith("-C") and len(a) > 2:  # -CDIR: the same target as -C DIR
            dirs.append(a[2:])
        elif (a.startswith("-") and len(a) > 1) or (j == 0 and re.match(r"^[A-Za-z]", a)):
            letters = a.lstrip("-")
            values = [i for i, ch in enumerate(letters) if ch in "fC"]
            if (not re.fullmatch(r"[A-Za-z]+", letters) or any(ch not in TAR_SHORT_OK | {"t"} for ch in letters)
                    or len(values) > 1 or (values and values[0] != len(letters) - 1)):
                extract = True  # unclassifiable: assume tar extracts
                problems.append(f"option {a!r}")
                continue
            extract = extract or "x" in letters
            problems += [f"option {a!r}" for ch in letters if ch not in TAR_SHORT_OK]
            if values:
                if k >= len(args):
                    problems.append(f"-{letters[-1]} without a value")
                    continue
                (archives if letters[-1] == "f" else dirs).append(args[k])
                k += 1
        else:
            marks.append(len(dirs))  # a member name: -C only applies to the names after it
    if len(archives) > 1:
        problems.append("more than one -f/--file")
    if any(m < len(dirs) for m in marks):
        problems.append("a member name before -C (GNU tar applies -C only to later names)")
    if len(dirs) > 1:
        problems.append("more than one -C/--directory (GNU tar chains them)")
    return extract, archives, dirs, problems


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
PT_GH_WRITES = {"pr": {"create", "new", "edit", "comment", "review", "close", "merge"},
                "issue": {"create", "new", "edit", "comment", "close"}, "release": {"create", "new", "edit"}}
PT_GIT_SUBS = {"commit", "tag", "push", "merge", "commit-tree", "notes"}
PT_DYNAMIC = re.compile(r"`|\$[A-Za-z_{(@*#?!0-9$-]")
PT_CAT_HEREDOC = re.compile(r"\A\$\(\s*cat\s+<<-?\s*(['\"]?)(\w+)\1[ \t]*\n(.*?)\n[ \t]*\2[ \t]*\n?\s*\)\Z", re.S)
PT_WRITE_HINT = re.compile(r"\b(?:git\s+(?:-\S+\s+\S+\s+)*(?:commit|tag|push|merge)|gh\s+(?:(?:-R|--repo)\s+\S+\s+)?(?:pr|issue|release|api))\b")
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
        self.nopush = False  # T-0961: set by check() for a NO_PUSH_ROLES role (not HQ): push() refuses before scanning
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
        if self.nopush:  # T-0961: refuse by role before the (slow) base..HEAD scan; NO_PUSH_ROLES never push
            raise PublicBlock("this role may not push (no-push roles only read; ask your lead). "
                              "The push was refused before any public-text scan")
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
        # T-1156: the value of a separate -R/--repo is not a subcommand word (`gh -R x pr create` reads like `gh pr create -R x`)
        pos = [x for k, x in enumerate(a) if not x.startswith("-") and not (k and a[k - 1] in ("-R", "--repo"))]
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



def lead_hq_goal_show_guard(an, cmd):
    """HQ lead panes: team goal-show allow/deny before the HQ-only return (T-0822)."""
    real_cmd = an.cmd
    real_soft = an.soft

    def soft(fn, *args):
        try:
            fn(*args)
        except Block:
            raise
        except (PrivBlock, PublicBlock):
            raise

    def cmd_goal_only(words, depth, piped, in_loose, hstrs=(), env_assigns=(), stdin=None):
        orig_words = list(words)
        words = list(words)
        stripped = strip_prefix(words)
        assigns = list(env_assigns)
        assigns += [x for x in words[:len(words) - len(stripped)] if ASSIGN.match(x)]
        words = stripped
        while words and (words[0] in ("noglob", "nocorrect", "-") or words[0] == "repeat" and len(words) > 1):
            if words[0] == "repeat":
                words = words[2:]
            else:
                words = words[1:]
            stripped = strip_prefix(words)
            assigns += [x for x in words[:len(words) - len(stripped)] if ASSIGN.match(x)]
            words = stripped
        if not words:
            return
        if not in_loose:
            saved_hq = an.hq
            an.hq = True
            try:
                an.priv(words, depth, piped, hstrs)
            finally:
                an.hq = saved_hq
        w, rest = words[0], words[1:]
        name = os.path.basename(w)
        if name == "command" and rest[:1] in (["-v"], ["-V"], ["-pv"]):
            return
        if name in SHELLS:
            saved = (an.gcwd, an.gdyn, an.ccwd)
            an.shell_assigns = assigns
            try:
                an.shell(rest, depth, piped, hstrs)
            finally:
                an.gcwd, an.gdyn, an.ccwd = saved
            return
        if name == "eval":
            if "$" in " ".join(rest):
                raise Block("eval of an expansion: content cannot be inspected")
            an.script(" ".join(rest), depth + 1)
            return
        if name in ("source", ".") and rest:
            an.file(rest[0], depth)
            return
        if name == "find":
            for j, a in enumerate(rest):
                if a in FIND_EXEC:
                    an.loose(rest[j + 1:], depth)
            return
        if name in WRAPPERS and not in_loose:
            if name == "xargs":
                an.xargs(rest)
            an.loose(rest, depth, assigns, stdin)
            return
        if "/" in w and not in_loose:
            an.exec_path(w, depth)
        if an._lead_factory_goal_show_words(words):
            rest_cmd = an._factory_rest_cmd(words)
            show_i = an._lead_goal_show_index(rest_cmd)
            gid = None
            if show_i is not None and show_i + 1 < len(rest_cmd) and not rest_cmd[show_i + 1].startswith("-"):
                gid = rest_cmd[show_i + 1]
            team_goals = an.lead_team_goals()
            if gid and team_goals is not None and gid in team_goals:
                return
            real_cmd(orig_words, depth, piped, in_loose, hstrs, env_assigns, stdin)
            return

    an.soft = soft
    an.cmd = cmd_goal_only
    try:
        an.script(cmd)
    finally:
        an.soft = real_soft
        an.cmd = real_cmd


def check(cmd, cwd=None, role=None, hq=False):
    pt = PublicText(cwd, Analyzer(cwd).db_path())
    pt.raw = cmd
    pt.nopush = role in NO_PUSH_ROLES and not hq
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
    if hq and role == "lead":
        try:
            lead_hq_goal_show_guard(Analyzer(cwd, role, hq=False), cmd)
        except Block:
            raise
    if hq:
        return
    if role in TERMS_ROLES:
        terms_bash(cmd, cwd)
    if role == "evaluator":
        evaluator_home_text(cmd)
    if role == RESEARCHER and RESEARCH_SECRET_TEXT.search(cmd):
        raise Block("a researcher may not name credentials, tokens or the factory/gh config dirs")
    # layer 2: raw text, independent of the parser
    if r2_set_hit(cmd) or r2_path_hit(cmd):
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


def terms_ids(paths):
    """(st_dev, st_ino) of the terms file under every spelling; None when a spelling cannot be stat'ed (fail closed)."""
    ids = set()
    for p in paths:
        try:
            st = os.stat(p)
        except OSError:
            return None
        ids.add((st.st_dev, st.st_ino))
    return ids


def terms_token_hits(tok, cwd, paths, ids):
    """True when a shell token (after quote removal) names the terms file: same inode after realpath and glob expansion,
    or, when the file cannot be stat'ed, the same basename or a glob next to it."""
    names = {os.path.basename(p) for p in paths}
    cands = {tok}
    if "=" in tok:
        cands.add(tok.split("=", 1)[1])
    for c in cands:
        c = c.lstrip("<>(")
        if not c:
            continue
        full = os.path.join(cwd, os.path.expanduser(c))
        try:
            found = glob.glob(full) or [full]
        except (OSError, re.error):
            found = [full]
        for f in found:
            real = os.path.realpath(f)
            if real in paths or os.path.basename(real) in names:
                return True
            try:
                st = os.stat(real)
            except OSError:
                continue
            if ids is not None and (st.st_dev, st.st_ino) in ids:
                return True
        if ids is None and glob.has_magic(c) and any(
                fnmatch.fnmatch(os.path.basename(p), os.path.basename(c)) for p in paths):
            return True
    return False


TERMS_RECURSIVE = {"rg", "find", "tar", "bsdtar", "zip", "rsync", "ag", "ack"}
TERMS_GREPS = {"grep", "egrep", "fgrep", "cp", "scp"}
TERMS_REC_FLAG = re.compile(r"^(?:-[A-Za-z]*[rRa][A-Za-z]*|--recursive|--dereference-recursive|--archive)$")
TERMS_WRAPPERS = {"env", "command", "nice", "time", "nohup", "xargs"}
TERMS_BRACE = re.compile(r"\{[^{}\s]*,[^{}\s]*\}|\{\w+\.\.\w+\}")


def terms_recursive_hits(toks, cwd, paths, home=None):
    """True when a recursive/archive command (grep -r, rg, find, tar, zip, cp -r, rsync) targets the terms file's directory
    or any ancestor of it; without a path argument the target is cwd."""
    toks = list(toks)
    xargs = False
    while toks and (TERMS_ENV_ASSIGN.match(toks[0]) or os.path.basename(toks[0]) in TERMS_WRAPPERS):
        xargs = xargs or os.path.basename(toks[0]) == "xargs"
        toks.pop(0)
    if not toks:
        return False
    name = os.path.basename(toks[0])
    if not (name in TERMS_RECURSIVE or (name in TERMS_GREPS and (any(TERMS_REC_FLAG.match(t) for t in toks[1:])
                                                                 or terms_rec_dirflag(toks[1:])))):
        return False
    if xargs:  # the paths arrive on stdin, so the target cannot be judged: fail closed
        return True
    reals = {os.path.realpath(p) for p in paths}
    targets, any_path = [], False
    for t in toks[1:]:
        if t.startswith("-") and name != "tar":
            continue
        for c in {t, t.split("=", 1)[-1]}:
            full = os.path.join(cwd, os.path.expanduser(c))
            try:
                found = glob.glob(full) or [full]
            except (OSError, re.error):
                found = [full]
            for f in found:
                if os.path.lexists(f):
                    any_path = True
                    targets.append(os.path.realpath(f))
    if not any_path:
        targets.append(os.path.realpath(cwd))
    return any(r.startswith(t.rstrip("/") + "/") for t in targets for r in reals)


TERMS_SHELLS = {"bash", "sh", "zsh", "dash", "ksh"}
TERMS_VARREF = re.compile(r"\$(?:\{(\w+)\}|(\w+))")


def terms_expand(text, env):
    """Expand $VAR/${VAR} from same-command assignments and $HOME; ~ is expanded by the callers. Unknown names stay."""
    def sub(m):
        name = m.group(1) or m.group(2)
        return env.get(name, os.environ.get(name, m.group(0)) if name == "HOME" else m.group(0))
    return TERMS_VARREF.sub(sub, text)


def terms_rec_dirflag(toks):
    """grep -d recurse / --directories=recurse is -r in another spelling."""
    return any(t in ("--directories=recurse",) or (t == "recurse" and i and toks[i - 1] in ("-d", "--directories"))
               or t == "-drecurse" for i, t in enumerate(toks))


def terms_split(cmd):
    """Split a command line on ; & | and newlines that sit outside quotes, so sh -c "cd d && grep -r x" stays one segment."""
    segs, cur, quote, i = [], [], None, 0
    while i < len(cmd):
        c = cmd[i]
        if quote:
            if c == "\\" and quote == '"' and i + 1 < len(cmd):
                cur.append(c)
                i += 1
                c = cmd[i]
            elif c == quote:
                quote = None
        elif c in "'\"":
            quote = c
        elif c == "\\" and i + 1 < len(cmd):
            cur.append(c)
            i += 1
            c = cmd[i]
        elif c in ";&|\n":
            segs.append("".join(cur))
            cur = []
            i += 1
            continue
        cur.append(c)
        i += 1
    segs.append("".join(cur))
    return segs


TERMS_BRACE_HEADS = ("grep", "egrep", "rg", "jq", "gh")
TERMS_BRACE_GH = {("pr", "list"), ("pr", "view"), ("pr", "checks"), ("pr", "status"), ("issue", "list"), ("issue", "view"),
                  ("issue", "status"), ("run", "list"), ("run", "view"), ("repo", "view")}
TERMS_BRACE_FLAGS = {"grep": re.compile(r"-[EFGHIPRUZachilnoqrsvwxz]+"), "egrep": re.compile(r"-[EFGHIPRUZachilnoqrsvwxz]+"),
                     "rg": re.compile(r"-[FHILPSUchilnopqsuvwxz]+"), "jq": re.compile(r"-[CMRSaceJjnrst]+")}
TERMS_BRACE_DOLLAR = re.compile(r"\$[A-Za-z_][A-Za-z0-9_]*(?![^ \t;&|])")


def terms_brace_view(cmd):
    """T-1121: the text TERMS_BRACE is searched in (depth 0 only). Gives up (returns cmd) unless the whole command is a plain
    one-line chain of grep/egrep/rg/jq/gh read-only segments; then blanks only the single-quoted pattern/program/template word."""
    t = cmd.strip(" \t\n")
    if not t or any((ord(c) < 32 and c not in " \t") or ord(c) == 127 for c in t):
        return cmd
    segs, words = [], []  # a word is (start, end, number of quoted parts)
    ws, parts, i, n = None, 0, 0, len(t)

    def end_word(at):
        nonlocal ws, parts
        if ws is not None:
            words.append((ws, at, parts))
        ws, parts = None, 0

    while i < n:
        c = t[i]
        if c == "'":
            j = t.find("'", i + 1)
            if j < 0:
                return cmd
            ws = i if ws is None else ws
            parts += 1
            i = j + 1
        elif c in " \t":
            end_word(i)
            i += 1
        elif c in ";&|":
            end_word(i)
            two = t[i:i + 2]
            if (c == "&" and two != "&&") or two == "|&":
                return cmd
            segs.append(words)
            words = []
            i += 2 if two in ("&&", "||") else 1
        elif c in "\"\\`(){}#<>" or ord(c) > 126:
            return cmd
        elif c == "$":
            m = TERMS_BRACE_DOLLAR.match(t, i)
            if not m:
                return cmd
            ws = i if ws is None else ws
            i = m.end()
        else:
            ws = i if ws is None else ws
            i += 1
    end_word(n)
    segs.append(words)
    blank = []
    for words in segs:
        if not words:
            return cmd
        txt = [t[a:b] if q == 0 else None for a, b, q in words]  # None: the word has a quoted part
        head = txt[0]
        if head in ("cd", "pushd"):
            continue
        if head not in TERMS_BRACE_HEADS:
            return cmd
        if head == "gh" and (len(txt) < 3 or (txt[1], txt[2]) not in TERMS_BRACE_GH):
            return cmd

        def whole(k):
            a, b, q = words[k]
            return q == 1 and t[a] == "'" and t[b - 1] == "'"

        if head == "gh":
            for k in range(1, len(words) - 1):
                if txt[k] in ("--jq", "-q", "--template") and whole(k + 1):
                    blank.append(words[k + 1][:2])
        else:
            k = 1
            while k < len(words) and txt[k] is not None and TERMS_BRACE_FLAGS[head].fullmatch(txt[k]):
                k += 1
            if head != "jq" and k < len(words) and txt[k] in ("-e", "--regexp"):
                k += 1
            if k < len(words) and whole(k) and t[words[k][0] + 1] != "-":  # a quoted word starting with a dash may be an option (rg '--glob=...'): blank nothing
                blank.append(words[k][:2])
    out = t
    for a, b in sorted(blank, reverse=True):
        out = out[:a] + "''" + out[b:]
    return out


def terms_bash(cmd, cwd, env=None, depth=0):
    paths = terms_paths(cwd)
    if not paths:
        return
    ids = terms_ids(paths)
    cwd = cwd or os.getcwd()
    env = {} if env is None else env
    home = os.environ.get("HOME") or os.path.expanduser("~")
    names = {"scrub", "terms"} | {n.lower() for p in paths for n in (os.path.basename(p), os.path.basename(os.path.dirname(p))) if n}
    brace_view = terms_brace_view(cmd) if depth == 0 else cmd
    if ("${!" in cmd or TERMS_BRACE.search(brace_view)) and any(n in cmd.lower() for n in names):
        raise PublicBlock(f"variable indirection or brace expansion next to the owner-terms file is refused: {TERMS_WHY}")
    for raw_seg in terms_split(re.sub(r"(?:\d*>&\d*-?|&>)", " ", cmd)):
        if (TERMS_VAR in raw_seg or any(p in raw_seg for p in paths)) and not terms_segment_ok(raw_seg):
            raise PublicBlock(f"reading the owner-terms file is refused: {TERMS_WHY}")
        seg = terms_expand(raw_seg, env)
        try:
            toks = shlex.split(re.sub(r"\$\(|[`()]", " ", seg))
        except ValueError:  # unbalanced quote from a split inside quotes: judge the raw words instead
            toks = re.sub(r"\$\(|[`()'\"\\]", " ", seg).split()
        while toks and toks[0] == "export" and len(toks) > 1:
            toks.pop(0)
        for t in toks:  # VAR=value (also as a command prefix) feeds later expansion
            if not TERMS_ENV_ASSIGN.match(t):
                break
            k, v = t.split("=", 1)
            env[k] = os.path.expanduser(v)
        if toks and toks[0] in ("cd", "pushd"):
            cwd = os.path.realpath(os.path.join(cwd, os.path.expanduser(toks[1]) if len(toks) > 1 else home))
            continue
        if terms_segment_ok(seg):
            continue
        core = list(toks)
        while core and (TERMS_ENV_ASSIGN.match(core[0]) or os.path.basename(core[0]) in TERMS_WRAPPERS - {"xargs"}):
            core.pop(0)
        if core and core[0] == "eval" and depth < 4:
            terms_bash(" ".join(core[1:]), cwd, dict(env), depth + 1)
        if core and os.path.basename(core[0]) in TERMS_SHELLS and depth < 4:  # bash -c 'payload' is its own script
            for i, t in enumerate(core[1:], 1):
                if re.fullmatch(r"-[A-Za-z]*c[A-Za-z]*", t) and i + 1 < len(core):
                    terms_bash(core[i + 1], cwd, dict(env), depth + 1)
                    break
        if terms_recursive_hits(toks, cwd, paths, home):
            raise PublicBlock(f"recursive or archive reads of the directory holding the owner-terms file are refused: {TERMS_WHY}")
        if any(terms_token_hits(t, cwd, paths, ids) for t in toks):
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


# G-029 T-0846: researcher. Writes only inside its research dir <state>/factory/research/<name> (the directory its cwd is
# in) and its session scratchpad; never a secret. WORKER_GUARD_STATE (the hook's own env, tests only) replaces <state>
# (default ~/.local/state).
RESEARCH_SECRET_TEXT = re.compile(
    r"oauth-token|rw-token|\.credentials\.json|(?:^|[/\s'\"=~:])\.(?:ssh|gnupg|netrc|aws)(?:[/\s'\";]|$)|\.config/(?:factory|gh)\b")
RESEARCH_SECRETS = (".ssh", ".gnupg", ".aws", ".netrc", ".git-credentials", ".npmrc", ".docker", ".kube", ".config/gcloud", ".config/factory",
                    ".config/gh", ".claude/.credentials.json")


def research_root():
    base = os.environ.get("WORKER_GUARD_STATE") or os.path.join(PW_DIR, ".local", "state")
    return os.path.realpath(os.path.join(base, "factory", "research"))


def research_dir(cwd):
    """The agent's research dir (root/<first component of cwd below root>), or None when cwd is not strictly below root."""
    root = research_root()
    c = os.path.realpath(cwd)
    if c == root or not under(c, root):
        return None
    return os.path.join(root, c[len(root) + 1:].split(os.sep)[0])


def researcher_area(path, cwd):
    """True when `path` (real and lexical) lies in cwd's research dir or in the session scratchpad."""
    rd = research_dir(cwd)
    for p in (os.path.realpath(path), os.path.normpath(path)):
        if not ((rd is not None and under(p, rd)) or in_scratchpad(p)):
            return False
    return True


def researcher_secret_path(p):
    for t in RESEARCH_SECRETS:
        for base in (PW_DIR, os.path.realpath(PW_DIR)):
            s = os.path.join(base, t)
            if under(p, s) or under(s, p):
                return True
    return os.path.basename(p).endswith(("-token", ".credentials.json")) or (p.startswith("/proc/") and p.endswith("/environ"))


def researcher_tool(data):
    """Edit/Write/MultiEdit/NotebookEdit: only inside the research dir or scratchpad. Read/Grep: no secret."""
    ti = data.get("tool_input")
    ti = ti if isinstance(ti, dict) else {}
    cwd = data.get("cwd") if isinstance(data.get("cwd"), str) else os.getcwd()
    tool = data.get("tool_name")
    if tool in PERSIST_EDIT_TOOLS:
        raw = ti.get("file_path") or ti.get("notebook_path")
    elif tool == "Read":
        raw = ti.get("file_path")
    else:
        raw = ti.get("path") or cwd
    if not isinstance(raw, str) or not raw:
        sys.stderr.write("worker-guard: blocked (tool call without a readable path, failing closed)\n")
        return 2
    p = re.sub(r"^(~|\$HOME|\$\{HOME\})(?=/|$)", lambda m: PW_DIR, raw)
    if not os.path.isabs(p):
        p = os.path.join(cwd, p)
    if tool in PERSIST_EDIT_TOOLS:
        if "$" in p or not researcher_area(p, cwd):
            sys.stderr.write(f"worker-guard: blocked ({raw!r}): a researcher writes only inside its research dir "
                             "<state>/factory/research/<name> and its scratchpad. Tell your lead if this is wrong.\n")
            return 2
    elif "$" in p or any(researcher_secret_path(q) for q in (os.path.realpath(p), os.path.normpath(p))):
        sys.stderr.write(f"worker-guard: blocked ({raw!r}): a researcher may not read credentials or tokens\n")
        return 2
    return 0


EXPORT_WHY = ("run/guard-export.json holds the guard's own decisions (own goal, push pin, sandbox, terms file); only "
              "the tick writes it")


def export_path(p):
    """T-0883: True when `p` (absolute; `..` and symlinks resolved) is run/guard-export.json of a live home or its
    atomic-rename temp sibling guard-export.json.tmp<pid>."""
    return any(c == d or c.startswith(d + ".") for c in {os.path.normpath(p), os.path.realpath(p)} for d in RUN_EXPORT)


def persist_edit(data, role=None):
    """T-0334: an Edit/Write/MultiEdit/NotebookEdit call on a protected config/init file is refused. A payload without a
    readable path cannot be decided and fails closed."""
    ti = data.get("tool_input")
    ti = ti if isinstance(ti, dict) else {}
    path = ti.get("file_path") or ti.get("notebook_path")
    if not isinstance(path, str) or not path:
        sys.stderr.write("worker-guard: blocked (edit tool call without a readable file_path, failing closed)\n")
        return 2
    cwd = data.get("cwd") if isinstance(data.get("cwd"), str) else os.getcwd()
    p = tilde_expand(path, cwd)
    unres = tilde_unres(path, cwd)  # rule A: an unresolvable `~name/...` is judged by the protected names it spells
    if not os.path.isabs(p):
        p = os.path.join(cwd, p)
    if export_path(p) or ("$" in p and "guard-export" in p):
        sys.stderr.write(f"worker-guard: blocked ({path!r}): {EXPORT_WHY}. Tell your lead if this is wrong.\n")
        return 2
    if role == RETRO and (retro_roles_path(p) or (unres and retro_roles_path(path, spelled=True))
                          or (any(c in path for c in "$`") and retro_roles_subst(path))):
        sys.stderr.write(f"worker-guard: blocked ({path!r}): {RETRO_ROLE_WHY}. Tell your lead if this is wrong.\n")
        return 2
    if prot_edit_path(p) or (unres and prot_edit_path(path, spelled=True)):  # T-0925 gap g: the Edit tools were checked against the persist paths only
        sys.stderr.write(f"worker-guard: blocked ({path!r}): {PROT_WHY}. Tell your lead if this is wrong.\n")
        return 2
    # T-0957: ~/.local/bin is first on the tick's PATH; matched as written and resolved (its entries are symlinks), and
    # a path with an unexpanded `$` that names .local/bin cannot be decided, so it is refused too
    if any(q == d or under(q, d) for q in (os.path.normpath(p), os.path.realpath(p)) for d in BIN_DIRS) or (
            ("$" in p or unres) and re.search(r"\.local/bin(?![A-Za-z0-9_])", path if unres else p)):
        sys.stderr.write(f"worker-guard: blocked ({path!r}): ~/.local/bin is first on the tick's PATH, so a file "
                         "planted there runs with the tick's credentials; only the owner changes it. "
                         "Tell your lead if this is wrong.\n")
        return 2
    nopush = role in NO_PUSH_ROLES  # T-0770: no scratchpad exemption for git config/hooks/info (never reached for HQ)
    if persist_path(p, scratch_ok=not nopush) or (nopush and (git_hooks_path(p) or gitfile_path(p))) or (
            "$" in p and persist_code(p)):
        why = REPO_CFG_WHY if repo_cfg_path(os.path.realpath(p)) or repo_cfg_path(os.path.normpath(p)) else PERSIST_WHY
        sys.stderr.write(f"worker-guard: blocked ({path!r}): {why}. Tell your lead if this is wrong.\n")
        return 2
    return 0


def main():
    global SESSION_ID
    role = None
    hq = "--hq" in sys.argv[1:]
    if "--role" in sys.argv[1:-1]:
        role = sys.argv[sys.argv.index("--role") + 1]
    claude = dynamic = False
    try:
        data = json.load(sys.stdin)
        if not isinstance(data, dict):
            raise ValueError("not an object")
        if role == RESEARCHER and not hq and (data.get("tool_name") in PERSIST_EDIT_TOOLS
                                              or data.get("tool_name") in ("Read", "Grep")):
            SESSION_ID = data.get("session_id") if isinstance(data.get("session_id"), str) else None
            rc = researcher_tool(data)
            if rc == 0 and data.get("tool_name") in PERSIST_EDIT_TOOLS:
                rc = persist_edit(data, role)
            return rc
        if data.get("tool_name") in PERSIST_EDIT_TOOLS and not hq:
            return persist_edit(data, role)
        if data.get("tool_name") in ("Read", "Grep") and not hq:
            return terms_tool(data) if role in TERMS_ROLES else 0
        if data.get("tool_name") != "Bash":
            return 0
        cmd = (data.get("tool_input") or {}).get("command")
        if not isinstance(cmd, str):
            raise ValueError("no command string")
        SESSION_ID = data.get("session_id") if isinstance(data.get("session_id"), str) else None
        check(cmd, data.get("cwd"), role, hq)
        return 0
    except UnitBlock as e:
        sys.stderr.write(f"worker-guard: blocked (`{e}`): mutating systemctl/systemd-run is not allowed for "
                         "factory agents; the owner manages units (M2108)\n")
        return 2
    except RepoProtBlock as e:
        sys.stderr.write(f"worker-guard: blocked (`{e}`): branch protection and repository settings are changed only by "
                         "the owner (ci/publish_repo.py, rule R-a); agents may only read them with a plain GET. "
                         "Tell your lead if this is wrong.\n")
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
