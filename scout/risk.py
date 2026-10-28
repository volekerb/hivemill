"""Skill scout install-risk classification (plan G-009 §7.1). Pure: works on a git tree listing and,
for markdown, on text handed in by a `read` callback. Nothing is fetched, executed or written here.

A tree entry is a dict {path, mode, type, sha} as returned by `git/trees/<sha>?recursive=1`.
`classify()` returns a Result whose `risk` is a comma list in FLAG_ORDER, or 'prompt-only' when no
flag applies. A check that cannot decide (unreadable markdown, truncated tree, nothing under the
path) adds 'unknown' instead of passing.
"""
import json
import re
from collections import namedtuple

FLAG_ORDER = ("scripts", "hooks", "mcp", "symlinks", "shell-in-markdown", "network", "unknown")
MAX_MD_READS = 10        # SKILL.md / commands/*.md blobs read per candidate
MAX_JSON_READS = 3       # plugin.json blobs read per candidate
MAX_DETAIL = 20          # offending paths recorded
READ_CAP = 65536         # bytes of one blob that are scanned

ALLOWED_EXT = {".md", ".txt", ".csv"}
ALLOWED_JSON_NAMES = {"plugin.json", "marketplace.json"}
ALLOWED_BARE = ("LICENSE", "NOTICE", "README")

# These are only words searched for in fetched markdown; this module never runs them.
_NET_TOOLS = "curl|wget"
_SHELL_NET_TOOLS = "curl|wget|nc|netcat|ssh|scp|ftp"

SHELL_BANG = re.compile(r"(?m)(?:^|[\s(])!`[^`\n]+`|^\s*```!")
NET_BODY = re.compile(rf"\b(?:{_NET_TOOLS}|WebFetch)\b|(?i:\b(?:fetch|download|retrieve)\b[^\n]{{0,40}}https?://)")
# MCP set up by instruction or declaration in text: `claude mcp add ...`, `mcp add --transport ...`, an
# `mcpServers` key, or a URL whose host or path has "mcp" (https://mcp.asana.com/v2/mcp). Fail closed: prose that
# only says "MCP" does not match, but an endpoint URL in prose does.
MCP_ADD = re.compile(r"(?i)\bmcp\s+add(?:-json|-from-claude-desktop)?\b")
MCP_KEY = re.compile(r"mcpServers")
_URL = re.compile(r"(?i)\bhttps?://([^\s/?#)>\]\"'`]+)([^\s)>\]\"'`]*)")
FM_NET = re.compile(r"\b(?:WebFetch|WebSearch)\b")
FM_BASH_NET = re.compile(rf"Bash\([^)]*\b(?:{_SHELL_NET_TOOLS})\b")
FM_HOOKS = re.compile(r"(?m)^hooks\s*:")
FM_TOOL_KEY = re.compile(r"(?m)^(?:allowed-tools|tools)\s*:[^\n]*(?:\n(?:[ \t]+[^\n]*|-[^\n]*))*")
BASH_UNRESTRICTED = re.compile(r"(?<![\w-])Bash(?!\s*\(|[\w-])")
BASH_SCOPED = re.compile(r"(?<![\w-])Bash\s*\(([^)]*)\)")
# A Bash(<cmd>:...) scope whose command runs arbitrary code is as open as bare Bash.
# Matched on the basename of the command; any glob in the name is open (see _unrestricted_bash).
_OPEN_CMD = re.compile(
    r"(?i)(?:ba|z|da|k|c|tc|fi)?sh|fish|pwsh|(?:python|ruby|perl|node|php)[\d.]*|env|eval|exec|source|\."
    r"|xargs|sudo|doas|timeout|nohup|command|nice|stdbuf|setsid"
    r"|npx|uv|uvx|deno|bun|awk|gawk|find|git|make"
    r"|ssh|scp|rsync|busybox|chroot|unshare|nsenter|script|expect|tclsh|lua|R|Rscript|julia")
# git stays scoped only for these read-only subcommands, and only when the scope has no option or shell
# metacharacter (`Bash(git status:*)`). Everything else is open: `git grep -O<cmd>`, `git -c`, `git rebase -x`,
# `git bisect run`, `git submodule foreach`. make has no exception (`--eval`, `-f`, `SHELL=`).
_GIT_SAFE_SUB = {"status", "log", "diff", "show", "rev-parse", "ls-files", "blame"}
_ENV_ASSIGN = re.compile(r"[A-Za-z_]\w*=")
_SHELL_META = re.compile(r"[;&|<>$`()\\*?\n]")
_WORD_META = re.compile(r"[$`\[\]{}~*?()]")
# the only characters a command word may have; anything else (`=sh`, `!`, `{`, `$x`, newline) is shell syntax
_WORD_OK = re.compile(r"[A-Za-z0-9._/+-]+")
# shell keywords and precommand words: the real command is the next word, so the scope is open
_KEYWORD_CMD = frozenset(
    "time noglob nocorrect builtin command exec coproc if then while until for case select function eval source "
    "nohup env sudo doas - ! { [[ .".split())

Result = namedtuple("Result", "risk detail kind")


class ReadError(Exception):
    """A blob could not be read; the check that needed it reports 'unknown'."""


def _scoped(tree, prefix):
    """Entries under prefix as (relative path, entry). prefix '' means the whole tree."""
    prefix = (prefix or "").strip("/")
    out = []
    for e in tree:
        p = e["path"]
        if not prefix:
            out.append((p, e))
        elif p.startswith(prefix + "/"):
            out.append((p[len(prefix) + 1:], e))
    return out


def has_manifest(tree, prefix=""):
    """True when the (scoped) tree holds a plugin manifest, a SKILL.md, an agent or a command file."""
    for p, e in _scoped(tree, prefix):
        if e.get("type") == "tree":
            continue
        segs = p.split("/")
        base = segs[-1]
        parent = segs[-2] if len(segs) > 1 else ""
        if base.lower() == "skill.md" or p.lower().endswith(".claude-plugin/plugin.json") or p.lower() == "plugin.json":
            return True
        if parent.lower() in ("agents", "commands") and base.lower().endswith(".md"):
            return True
    return False


def _is_allowed(full):
    base = full.rsplit("/", 1)[-1]
    low = base.lower()
    dot = low.rfind(".")
    ext = low[dot:] if dot > 0 else ""
    if ext in ALLOWED_EXT or low in ALLOWED_JSON_NAMES:
        return True
    if ext == ".json":  # data json only inside skills/<name>/
        return re.search(r"(?:^|/)skills/[^/]+/.+", full) is not None
    return ext == "" and base.upper().startswith(ALLOWED_BARE)


def _unrestricted_bash(tools):
    """True for bare `Bash`, `Bash(*)`, `Bash()` and scopes on a shell or interpreter (`Bash(sh:*)`)."""
    if BASH_UNRESTRICTED.search(tools):
        return True
    for m in BASH_SCOPED.finditer(tools):
        raw = m.group(1).strip()
        arg = re.sub(r"[\\'\"]", "", raw)  # quotes and backslashes do not change the command word
        words = arg.split(":", 1)[0].split()
        if not words or _ENV_ASSIGN.match(words[0]):
            return True
        raw_words = raw.split(":", 1)[0].split()
        # a command word built by expansion, globbing or quoting (`$'\x73h'`, `$SHELL`, `s[h]`, `{sh,}`) is unknowable
        if any(_WORD_META.search(w) for w in (raw_words[0], words[0])):
            return True
        if "\n" in raw or not _WORD_OK.fullmatch(words[0]):
            return True
        cmd = words[0].rsplit("/", 1)[-1]
        if not cmd or cmd in _KEYWORD_CMD:
            return True
        after = arg.split(":", 1)[1].split() if ":" in arg else []
        if cmd == "git" and words[0] == "git" and len(words) > 1 and words[1] in _GIT_SAFE_SUB and not any(
                w.startswith("-") or _SHELL_META.search(w) for w in words[2:]) and all(
                w == "*" or not (w.startswith("-") or _SHELL_META.search(w)) for w in after):
            continue
        if _OPEN_CMD.fullmatch(cmd):
            return True
    return False


def _mcp_text(text):
    """(declares/instructs MCP, names a remote MCP endpoint URL) for fetched text."""
    remote = any("mcp" in (m.group(1) + m.group(2)).lower() for m in _URL.finditer(text))
    return bool(MCP_ADD.search(text) or MCP_KEY.search(text)) or remote, remote


def _normalize(text):
    """Drop a BOM, zero-width space and leading (Unicode) whitespace so the `---` fence is found at the start."""
    return re.sub(r"\A[\s\ufeff\u200b]+", "", text)


def _frontmatter(text):
    """Frontmatter block of normalized text; None when the text opens a fence it never closes."""
    m = re.match(r"\A---[ \t]*\r?\n(.*?)\r?\n---[ \t]*(?:\r?\n|\Z)", text, re.S)
    if m:
        return m.group(1)
    return None if text.startswith("---") else ""


def detect_kind(rels):
    rels = [r.lower() for r in rels]
    skills = [r for r in rels if r.rsplit("/", 1)[-1] == "skill.md"]
    if any(r.endswith("plugin.json") and ".claude-plugin" in r for r in rels):
        return "plugin"
    if len(skills) > 1:
        return "collection"
    if skills:
        return "skill"
    if any(r.startswith("agents/") or "/agents/" in r for r in rels if r.endswith(".md")):
        return "agent"
    if any("hooks" in r.split("/")[:-1] or r.endswith("hooks.json") for r in rels):
        return "hook"
    if any(r.endswith(".mcp.json") for r in rels):
        return "mcp"
    if any(r.startswith("commands/") or "/commands/" in r for r in rels if r.endswith(".md")):
        return "plugin"
    return "unknown"


def classify(tree, prefix="", read=None, truncated=False):
    """Install risk of the files under `prefix` in `tree`. `read(entry) -> str` supplies blob text
    (it may raise ReadError); without it markdown is not inspected and the result says so."""
    items = _scoped(tree, prefix)
    flags = {f: [] for f in FLAG_ORDER}
    rels = [rel for rel, e in items if e.get("type") != "tree"]
    md_reads, json_reads = [], []
    for rel, e in items:
        mode, typ = str(e.get("mode", "")), e.get("type")
        if typ == "tree":
            continue
        segs = rel.split("/")
        base = segs[-1]
        if mode in ("120000", "160000") or typ == "commit":
            flags["symlinks"].append(rel)
            continue
        lsegs = [x.lower() for x in segs[:-1]]
        if "hooks" in lsegs or base.lower() == "hooks.json":
            flags["hooks"].append(rel)
        if base.lower() == "hooks.json":
            continue
        if base.lower() == ".mcp.json":
            flags["mcp"].append(rel)
            continue
        if mode == "100755" or not _is_allowed(e["path"]):
            flags["scripts"].append(rel)
            continue
        if base.lower() == "plugin.json":
            json_reads.append((rel, e))
        elif base.lower() == "skill.md" or (base.lower().endswith(".md") and ({"commands", "agents"} & set(lsegs))):
            md_reads.append((rel, e))

    unread = 0
    for rel, e in json_reads[:MAX_JSON_READS]:
        text = _read(read, e, flags, rel)
        if text is None:
            continue
        try:
            data = json.loads(text.lstrip("\ufeff"))
        except ValueError:
            data = None
        if isinstance(data, dict):
            keys = set(data)
        else:
            keys = {k for k in ("hooks", "mcpServers") if f'"{k}"' in text}
            flags["unknown"].append(f"unparseable json: {rel}")
        if "hooks" in keys:
            flags["hooks"].append(rel)
        is_mcp, remote = _mcp_text(text)
        if "mcpServers" in keys or is_mcp:
            flags["mcp"].append(rel)
        if remote:
            flags["network"].append(rel)
    unread += max(0, len(json_reads) - MAX_JSON_READS)
    md_reads.sort(key=lambda t: (t[0].rsplit("/", 1)[-1].lower() != "skill.md", t[0]))  # SKILL.md first
    for rel, e in md_reads[:MAX_MD_READS]:
        text = _read(read, e, flags, rel)
        if text is None:
            continue
        text = _normalize(text)
        fm = _frontmatter(text)
        if fm is None:
            flags["unknown"].append(f"unclosed frontmatter: {rel}")
            fm = body = text  # scan the whole text as both rather than nothing
        else:
            body = text[len(fm):] if fm else text
        is_mcp, remote = _mcp_text(text)
        if is_mcp:
            flags["mcp"].append(rel)
        if remote:
            flags["network"].append(rel)
        if SHELL_BANG.search(text) or FM_BASH_NET.search(fm):
            flags["shell-in-markdown"].append(rel)
        if FM_HOOKS.search(fm):
            flags["hooks"].append(rel)
        tools = "\n".join(FM_TOOL_KEY.findall(fm))
        open_bash = _unrestricted_bash(tools)
        if FM_NET.search(fm) or NET_BODY.search(body) or open_bash:
            flags["network"].append(rel)
        if open_bash and not BASH_UNRESTRICTED.search(tools):
            flags["shell-in-markdown"].append(rel)
    unread += max(0, len(md_reads) - MAX_MD_READS)
    if unread:
        flags["unknown"].append(f"unread: {unread} file(s) over the read limit")
    if truncated:
        flags["unknown"].append("tree truncated by the API")
    if not rels:
        flags["unknown"].append("no files under path")

    present = [f for f in FLAG_ORDER if flags[f]]
    detail = [f"{f}: {p}" for f in present for p in sorted(set(flags[f]))][:MAX_DETAIL]
    return Result(",".join(present) or "prompt-only", detail, detect_kind(rels))


def _read(read, entry, flags, rel):
    if read is None:
        flags["unknown"].append(f"not inspected: {rel}")
        return None
    try:
        text = read(entry)
        if len(text) > READ_CAP or (entry.get("size") or 0) > READ_CAP:
            flags["unknown"].append(f"over {READ_CAP} bytes, not fully scanned: {rel}")
        return text[:READ_CAP]
    except ReadError as e:
        flags["unknown"].append(f"unreadable: {rel} ({str(e)[:80]})")
        return None
