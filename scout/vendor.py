"""Skill scout vendoring (plan G-009 §7.1-§7.3): copy ONLY allow-listed files of a prompt-only candidate, at its
pinned sha, into the skilleval root; re-check what was written; pre-screen it for exfiltration hints.

Everything comes from `gh api` (git/trees and git/blobs at the pinned sha). There is no clone, no download tool,
and nothing fetched is executed. `assert_prompt_only_tree` and `is_eval_allowed` are shared with the spawn
and queue code, which must re-check before a vendored copy is handed to an agent.
"""
import base64
import hashlib
import collections
import ipaddress
import json
import os
import posixpath
import re
import shutil
import unicodedata
from pathlib import Path
from urllib.parse import unquote, urlsplit

from . import risk
from . import sources
from .sources import GhError

DEFAULT_ROOT = "~/.cache/factory-skilleval"
MAX_FILES = 200
MAX_BYTES = 2 * 1024 * 1024
NEEDS_OWNER = "needs-owner"
NOT_EVALUABLE = {NEEDS_OWNER, "dismissed"}
BAD_NAMES = {"hooks", "hooks.json", ".mcp.json"}
SHA_RE = re.compile(r"[0-9a-f]{40}")
MAX_HITS_DETAIL = 20

EXIT_REFUSED = 2
EXIT_PRESCREEN = 4


class VendorError(Exception):
    """Refusal; `code` is the CLI exit status (2 refused, 4 pre-screen hits)."""

    def __init__(self, msg, code=EXIT_REFUSED, fresh=None):
        super().__init__(msg)
        self.code, self.fresh = code, fresh   # fresh: the risk.Result when the stored install_risk was stale


# -- config ---------------------------------------------------------------------------------------
def resolve_root(value, factory_home=None):
    """The skilleval root as a resolved Path. It must lie outside ~/factory, ~/.claude and the factory home
    (neither inside them nor above them): the eval sandbox hides those, and vendored text must not mix with them."""
    value = (value or DEFAULT_ROOT).strip() or DEFAULT_ROOT
    p = Path(os.path.expanduser(value))
    if not p.is_absolute():
        raise VendorError(f"skilleval_root {value!r} is not an absolute path; set it to e.g. {DEFAULT_ROOT}")
    root = p.resolve()
    forbidden = [Path(os.path.expanduser("~/factory")), Path(os.path.expanduser("~/.claude"))]
    if factory_home:
        forbidden.append(Path(factory_home))
    for f in forbidden:
        f = f.resolve()
        if root == f or f in root.parents or root in f.parents:
            raise VendorError(f"skilleval_root {value!r} resolves to {root}, which overlaps {f}; "
                              f"choose a directory outside ~/factory and ~/.claude, e.g. {DEFAULT_ROOT}")
    return root


# -- is the written tree still prompt-only? -------------------------------------------------------
def tree_problems(path):
    """Every reason the tree under `path` is not a prompt-only plugin dir (empty list = fine). Checks: symlinks and
    other non-regular files, exec bits, hooks dirs/hooks.json/.mcp.json, non-allow-listed extensions, shell lines in
    markdown, and plugin.json with hooks, mcpServers or commands that point at scripts."""
    root = Path(path)
    if not root.is_dir():
        return [f"{root}: not a directory"]
    bad = []
    for cur, dirs, files in os.walk(root, followlinks=False):
        for n in dirs + files:
            full = Path(cur) / n
            rel = full.relative_to(root).as_posix()
            if full.is_symlink():
                bad.append(f"{rel}: symlink")
            elif n.lower() in BAD_NAMES:
                bad.append(f"{rel}: hooks/mcp file")
        for n in files:
            full = Path(cur) / n
            rel = full.relative_to(root).as_posix()
            if full.is_symlink():
                continue
            st = full.lstat()
            if not os.path.isfile(full):
                bad.append(f"{rel}: not a regular file")
                continue
            if st.st_mode & 0o111:
                bad.append(f"{rel}: executable bit")
            if not risk._is_allowed(rel):
                bad.append(f"{rel}: not an allow-listed file type")
                continue
            if n.lower() == "plugin.json":
                bad += _plugin_json_problems(full, rel)
            elif n.lower().endswith(".md") and st.st_size > risk.READ_CAP:
                bad.append(f"{rel}: markdown over the {risk.READ_CAP}-byte read cap (cannot be checked)")
            elif n.lower().endswith(".md") and _read_text(full, risk.READ_CAP) and \
                    risk.SHELL_BANG.search(_read_text(full, risk.READ_CAP)):
                bad.append(f"{rel}: shell command in markdown")
    return bad


def _plugin_json_problems(full, rel):
    try:
        data = json.loads(_read_text(full).lstrip("﻿"))
    except ValueError:
        return [f"{rel}: plugin.json is not valid JSON"]
    if not isinstance(data, dict):
        return [f"{rel}: plugin.json is not an object"]
    out = [f"{rel}: plugin.json has {k}" for k in ("hooks", "mcpServers") if k in data]
    for k in ("commands", "agents", "skills"):
        refs = data.get(k)
        refs = list(refs.values()) if isinstance(refs, dict) else [refs] if isinstance(refs, str) else refs
        for r in refs if isinstance(refs, list) else []:
            r = r.get("source") if isinstance(r, dict) else r
            if isinstance(r, str) and os.path.splitext(r.rstrip("/"))[1].lower() not in ("", ".md"):
                out.append(f"{rel}: plugin.json {k} points at {r!r}")
    return out


def assert_prompt_only_tree(path):
    """Raise VendorError (exit 2) naming the offenders unless the tree is prompt-only. Importable by spawn."""
    bad = tree_problems(path)
    if bad:
        shown = "; ".join(bad[:5]) + (f"; and {len(bad) - 5} more" if len(bad) > 5 else "")
        raise VendorError(f"{path} is not prompt-only: {shown}")


def _read_text(p, cap=None):
    with open(p, "rb") as fh:
        return fh.read(cap).decode("utf-8", "replace")


# -- exfil pre-screen -----------------------------------------------------------------------------
B64_RUN = re.compile(r"[A-Za-z0-9+/=_-]{200,}")
# a blob wrapped over lines or split by spaces: 8+ char chunks joined by whitespace
# anchored to the start of a run (lookbehind) so one long run is scanned once, not once per offset
B64_WRAPPED = re.compile(r"(?<![A-Za-z0-9+/=_-])[A-Za-z0-9+/=_-]{8,}(?:\s+[A-Za-z0-9+/=_-]{8,})+")
URL = re.compile(r"""https?://[^\s<>"'`)\]]+""", re.I)
# host-like token without a scheme: evil.example/x, //evil.example, host:8080
BARE_HOST = re.compile(r"""(?<![\w.-])(?://)?((?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,24}(?::\d+)?(?:/[^\s<>"'`)\]]*)?)(?![\w-])""", re.I)
# bare IPv4 / IPv6 literal (validated with ipaddress), with or without scheme, port or brackets
IPV4_TOKEN = re.compile(r"(?<![\w.])(?:\d{1,3}\.){3}\d{1,3}(?![\w])")
IPV6_TOKEN = re.compile(r"(?<![\w:.])\[?([0-9a-f:]{2,}(?:\d{1,3}(?:\.\d{1,3}){3})?)\]?(?![\w])", re.I)
# an integer-form IP right after a scheme or `@`: 0x7f000001, 2130706433 (the dotted forms are caught above)
INT_IP = re.compile(r"(?://|@)(0x0*[0-9a-f]{1,8}|\d{8,10})(?!\w)(?!\.\d)", re.I)
# separators that can sit between wrapped base64 chunks: quote marks, quote prefixes, comment marks, a lone `+`
CHUNK_SEP = re.compile(r"""["'`,;>|#*()<>:.\\!?\[\]{}]+|(?<=\s)\+(?=\s)|^\+(?=\s)""")
B64_TOKEN = re.compile(r"[A-Za-z0-9+/=_-]+")
# a chunk with real base64 traits: mixed case plus a digit or symbol. Prose, versions, dates and plain numbers lack one.
B64_ISH = re.compile(r"(?=.*[0-9+=])(?=.*[a-z])(?=.*[A-Z])")
HEX_COLOR = re.compile(r"#[0-9a-f]{3,8}\b", re.I)  # css colours are not wrapped blobs
LINE_LABEL = re.compile(r"^\s*[A-Za-z_]{1,12}:\s+")  # `data: <chunk>`: a one-word label per line is not blob content
MAX_PROSE_GAP = 3  # prose lines tolerated inside one wrapped blob
DOTS = {0x3002: ".", 0xFF0E: ".", 0xFF61: "."}  # ideographic and fullwidth full stops
FILE_EXTS = {"tsx", "jsx", "java", "cpp", "c", "h", "hpp", "php", "rst", "mdx", "webp", "gz", "tgz", "env", "xml", "sql", "bat", "ps1", "lua", "swift", "kt", "png", "jpg", "jpeg", "gif", "svg", "pdf", "md", "txt", "py", "sh", "json", "yml", "yaml", "js", "cjs", "mjs", "ts", "csv", "toml", "html", "css", "rb", "go", "rs", "ini", "cfg", "lock", "log"}
DOC_HOSTS = {"docs.anthropic.com", "docs.claude.com", "code.claude.com", "anthropic.com"}
# (id, regex): one table, one id per pattern. Hits are cheap evidence for the owner, not a verdict.
PATTERNS = (
    ("sk-ant", re.compile(r"sk-ant-")),
    ("curl", re.compile(r"\bcurl\b", re.I)),
    ("wget", re.compile(r"\bwget\b", re.I)),
    ("urllib", re.compile(r"\burllib\b", re.I)),
    ("requests", re.compile(r"\brequests\.", re.I)),
    ("nc", re.compile(r"\bnc\b")),
    ("netcat", re.compile(r"\bnetcat\b", re.I)),
    ("printenv", re.compile(r"\bprintenv\b", re.I)),
    ("environ", re.compile(r"\benviron\b", re.I)),
    ("env-cmd", re.compile(r"^\s*env\b|\benv\s*[|>]")),
    ("credentials-file", re.compile(r"\.credentials\.json", re.I)),
    ("credentials", re.compile(r"\bcredentials\b", re.I)),
    ("api-key", re.compile(r"\bapi[ _]key\b|\bANTHROPIC_API_KEY\b", re.I)),
)


def _fold(text):
    """NFKC-normalise and drop every format character (Unicode Cf: zero-width, soft hyphen, bidi controls,
    BOM) so fullwidth or split keywords match the patterns."""
    if text.isascii():
        return text
    return "".join(c for c in unicodedata.normalize("NFKC", text) if unicodedata.category(c) != "Cf").translate(DOTS)


def _random_case(joined):
    """True when `joined` has the letter mix of random base64 (about 41% upper, 41% lower, 16% digits), which
    prose, code, version lists and timestamp lists lack."""
    n = len(joined)
    up = sum(c.isupper() for c in joined)
    low = sum(c.islower() for c in joined)
    dig = sum(c.isdigit() for c in joined)
    return up * 10 >= n * 2.5 and low * 10 >= n * 2.5 and dig * 20 >= n


PLAIN_TOKEN = re.compile(r"[A-Z]?[a-z]+|[A-Z]+|\d+")


def _wordy(run):
    """True when over a third of the chunks are plain words or numbers (`word`, `Word`, `WORD`, `123`): code,
    prose and version lists. Random base64 chunks of any width rarely look like that (about 10% at 3-7 chars),
    so the check does not depend on how the attacker cuts the blob."""
    long = [t for t in run if len(t) > 1]  # a single character is plain whatever it is
    return sum(bool(PLAIN_TOKEN.fullmatch(t)) for t in long) * 3 > len(long)


def _small_chunk_blobs(lines):
    """0-based first line of each blob split into short chunks (under 8 chars) or wrapped with per-line
    prefixes (`> `, quoted string concatenation). Linear: one pass over the tokens. Each stretch of base64-alphabet
    tokens on a line is scored on its own: a prose stretch is skipped (it neither joins the run nor, up to MAX_PROSE_GAP in a row, ends it), so
    adjacent prose cannot dilute the blob; a token outside the alphabet ends the run."""
    out = set()
    run = []  # chunks of the current run
    start = gap = 0  # gap: prose stretches skipped since the last blob stretch

    def close():
        nonlocal run, gap
        gap = 0
        joined = "".join(run)
        if len(joined) >= 200 and len(set(joined)) > 8 and _random_case(joined) and not _wordy(run):
            out.add(start)
        run = []

    def take(seg, i):
        nonlocal start, gap
        if not seg:
            return
        joined = "".join(seg)
        if len(joined) < 8 * len(seg):  # chunks too short to show word shape: ask for random-looking case
            blobby = sum(c.isupper() for c in joined) * 4 >= len(joined) or \
                (sum(c.isupper() for c in joined) * 7 >= len(joined) and bool(B64_ISH.match(joined)))
        else:
            blobby = sum(bool(B64_ISH.match(t)) for t in seg) * 10 >= len(seg) * 3
        if blobby:
            if not run:
                start = i
            run.extend(seg)
            gap = 0
        elif run:
            gap += 1
            if gap > MAX_PROSE_GAP:  # a long stretch of prose: not one blob (keeps scattered code lines apart)
                close()

    for i, line in enumerate(lines):
        seg = []
        for tok in CHUNK_SEP.sub(" ", HEX_COLOR.sub(" ", LINE_LABEL.sub("", line, 1))).split():
            if len(tok) > 1 and len(set(tok)) == 1:
                continue  # a rule of dashes or equals
            if B64_TOKEN.fullmatch(tok):
                seg.append(tok)
                continue
            take(seg, i)
            seg = []
            close()
        take(seg, i)
    close()
    return out


def _is_ip(host):
    try:
        ipaddress.ip_address(host)
        return True
    except ValueError:
        return False


def _url_allowed(url, own_repo):
    try:
        parts = urlsplit(url)
        host = (parts.hostname or "").lower()
    except ValueError:
        return False
    if parts.scheme.lower() not in ("http", "https"):
        return False
    if "@" in parts.netloc or _is_ip(host):  # userinfo (user@host) and IP literals are never docs
        return False
    if host in DOC_HOSTS:
        return True
    if host == "github.com":
        path = posixpath.normpath("/" + unquote(parts.path).replace("\\", "/"))  # resolves /anthropics/../evil
        segs = [s for s in path.split("/") if s and s != "."]
        if segs and segs[0].lower() == "anthropics":
            return True
        own = (own_repo or "").lower().split("/")
        return len(own) == 2 and [s.lower() for s in segs[:2]] == own
    return False


def _bare_host_bad(line, own_repo):
    """True if `line` has a host-like token outside an http(s) URL that is not allowed: schemeless
    (evil.example/x, //evil.example), after another scheme (ftp://, wss://, file://), after `@` (userinfo),
    or a bare IPv4/IPv6 literal. Fails closed: anything not clearly a file name or an allowed docs host hits."""
    line = URL.sub(" ", line)
    for m in IPV4_TOKEN.finditer(line):
        if _is_ip(m.group()):
            return True
    for m in IPV6_TOKEN.finditer(line):
        if m.group(1).count(":") >= 2 and _is_ip(m.group(1)):
            return True
    for m in INT_IP.finditer(line):
        tok = m.group(1)
        if int(tok, 16 if tok[:2].lower() == "0x" else 10) <= 0xFFFFFFFF:
            return True
    for m in BARE_HOST.finditer(line):
        tok = m.group(1).rstrip(".,;:")
        host = re.split(r"[/:]", tok, maxsplit=1)[0]
        before = line[:m.start()]
        if before.endswith("@"):
            return True  # userinfo or e-mail: never docs
        scheme_ctx = before.endswith("//") or m.group().startswith("//")
        if host.rsplit(".", 1)[1].lower() in FILE_EXTS and not scheme_ctx and "/" not in tok and ":" not in tok:
            continue  # SKILL.md, run.py, images/logo.png: a file name (last segment), not a host
        if not _url_allowed("https://" + tok, own_repo):
            return True
    return False


# Narrow allowances for false positives on every real plugin (T-0728). Each is scoped by file kind AND exact pattern;
# anything outside the scope keeps hitting.
APACHE_URL = re.compile(r"https?://www\.apache\.org/licenses/(?:LICENSE-2\.0)?")  # Apache-2.0 boilerplate, exactly
EMAIL = re.compile(r"[A-Za-z0-9._+-]+@([A-Za-z0-9.-]+)")
PROSE_EXTS = {".md", ".txt"}
LICENSE_NAME = re.compile(r"(?:license|notice)(?:[-_.][^/]*)?", re.I)  # LICENSE, LICENSE.txt, LICENSE-APACHE, NOTICE.md


def _doc_email(addr):
    m = EMAIL.fullmatch(addr) if isinstance(addr, str) else None
    host = m.group(1).lower() if m else ""
    return bool(m) and (host in DOC_HOSTS or (host.startswith("www.") and host[4:] in DOC_HOSTS))


def _plugin_json_emails(text):
    """Addresses a plugin.json may carry without a hit: the `author.email`, `support` or `support.email` value, at a
    DOC_HOSTS host, written on one line and nowhere else in the file. Anything unparseable or ambiguous gives none."""
    # ASCII + no backslash: raw == folded == parsed literals. `text` must be the raw file; never parse the folded text.
    if not text.isascii() or "\\" in text:
        return set()
    try:
        data = json.loads(text)
    except ValueError:
        return set()
    if not isinstance(data, dict):
        return set()
    author, support = data.get("author"), data.get("support")
    cands = [author.get("email") if isinstance(author, dict) else None, support,
             support.get("email") if isinstance(support, dict) else None]
    return {a for a in cands if _doc_email(a) and text.count(a) == 1}


def prescreen(path, own_repo=None):
    """Scan every text file under `path`. Returns hits [{file, line, pattern}] (file relative to `path`)."""
    root = Path(path)
    hits = []
    for cur, _dirs, files in os.walk(root, followlinks=False):
        for n in sorted(files):
            full = Path(cur) / n
            if full.is_symlink():
                continue
            rel = full.relative_to(root).as_posix()
            lines = [_fold(x) for x in _read_text(full).splitlines()]
            wrapped = set()  # 0-based first line of each multi-chunk blob
            for m in B64_WRAPPED.finditer("\n".join(lines)):
                joined = re.sub(r"\s+", "", m.group())
                if len(joined) >= 200 and len(set(joined)) > 8 and re.search(r"[0-9+/=]", joined):
                    wrapped.add(m.string.count("\n", 0, m.start()))
            wrapped |= _small_chunk_blobs(lines)
            license_file = bool(LICENSE_NAME.fullmatch(n)) and full.suffix.lower() in ("", ".md", ".txt")
            prose = full.suffix.lower() in PROSE_EXTS
            emails = _plugin_json_emails(_read_text(full)) if n == "plugin.json" else set()
            for ln, line in enumerate(lines, 1):
                found = [pid for pid, rx in PATTERNS if rx.search(line) and not (pid == "requests" and prose)]
                if any(len(set(m.group())) > 8 for m in B64_RUN.finditer(line)) or ln - 1 in wrapped:
                    found.append("base64-run")  # not a rule of dashes/equals
                bare = line
                for addr in emails:  # only the whole `"email": "addr"` / `"support": "addr"` line is exempt
                    if re.fullmatch(r'\s*"(?:email|support)"\s*:\s*"' + re.escape(addr) + r'"\s*,?\s*', line):
                        bare = ""
                if any(not _url_allowed(m.group().rstrip(".,;:"), own_repo) and
                       not (license_file and APACHE_URL.fullmatch(m.group().rstrip(".,;:")))
                       for m in URL.finditer(line)) or _bare_host_bad(bare, own_repo):
                    found.append("url")
                hits += [{"file": rel, "line": ln, "pattern": pid} for pid in found]
    hits.sort(key=lambda h: (h["file"], h["line"], h["pattern"]))
    return hits


def rescreen(root, cand):
    """Re-run the current prescreen on the kept copy of `cand` (same sha). Returns (meta, hits, copy dir); a missing or
    invalid copy raises VendorError. Writes nothing: `clear_hits` records a clean result."""
    d = vendor_dir(root, cand["id"], cand["head_sha"])
    meta = vendored(root, cand["id"], cand["head_sha"])
    if not meta:
        _raise_missing_file(d, cand)
        raise VendorError(f"{cand['id']}: no valid vendored copy at {cand['head_sha']}; run `factory skilleval vendor` first")
    _check_files(d, meta, cand["id"])
    return meta, prescreen(d / meta["plugin"], meta.get("repo")), d


def _raise_missing_file(d, cand):
    """The copy failed `vendored`: if VENDOR.json records files that are gone, name the first one."""
    try:
        meta = json.loads((d / "VENDOR.json").read_text())
        plugin = d / meta["plugin"]
        for rel in sorted(meta["files"]):
            if not (plugin / rel).is_file():
                raise VendorError(f"{cand['id']}: recorded file {rel} is missing from the kept copy; "
                                  f"re-vendor it with `factory skilleval vendor {cand['id']}` (after removing the copy)")
    except (OSError, ValueError, KeyError, TypeError):
        return


def _check_files(d, meta, cid):
    """Refuse unless every kept file has a recorded sha256 in VENDOR.json and still matches it."""
    files = meta.get("files")
    redo = f"re-vendor the candidate (remove the kept copy, then `factory skilleval vendor {cid}`)"
    if not isinstance(files, dict) or not files:
        raise VendorError(f"{cid}: VENDOR.json has no per-file sha256 (vendored before shas were recorded); {redo}")
    plugin = d / meta["plugin"]
    for rel in sorted(meta.get("copied", [])):
        if rel not in files:
            raise VendorError(f"{cid}: {rel} has no recorded sha256 in VENDOR.json; {redo}")
    for rel, want in sorted(files.items()):
        try:
            got = hashlib.sha256((plugin / rel).read_bytes()).hexdigest()
        except OSError:
            raise VendorError(f"{cid}: recorded file {rel} is missing from the kept copy; {redo}") from None
        if got != want:
            raise VendorError(f"{cid}: {rel} differs from the sha256 recorded at vendor time (modified after vendoring); {redo}")


def clear_hits(d, meta):
    """Record 0 pre-screen hits in VENDOR.json of the copy at `d`."""
    _write(d / "VENDOR.json", (json.dumps({**meta, "prescreen_hits": []}, indent=2) + "\n").encode())


# -- eval gate ------------------------------------------------------------------------------------
def is_eval_allowed(cand, root=None):
    """True only for a candidate that is exactly prompt-only and not held back (needs-owner, dismissed).
    With `root`, the vendored copy at the candidate's head sha must also exist, be prompt-only and carry no hits."""
    get = (lambda k: cand[k]) if not isinstance(cand, dict) else cand.get
    if (get("install_risk") or "") != "prompt-only" or (get("status") or "") in NOT_EVALUABLE:
        return False
    if root is None:
        return True
    meta = vendored(root, get("id"), get("head_sha"))
    return bool(meta) and not meta.get("prescreen_hits")


def vendor_dir(root, cid, sha):
    return Path(root) / "vendor" / f"{cid}@{sha}"


def vendored(root, cid, sha):
    """VENDOR.json of a valid existing copy of C@sha, else None."""
    d = vendor_dir(root, cid, sha)
    try:
        meta = json.loads((d / "VENDOR.json").read_text())
        plugin = d / meta["plugin"]
        assert_prompt_only_tree(plugin)
        on_disk = sorted(p.relative_to(plugin).as_posix() for p in plugin.rglob("*") if p.is_file())
    except (OSError, ValueError, KeyError, TypeError, VendorError):
        return None
    ok = meta.get("sha") == sha and meta.get("candidate") == cid and on_disk == sorted(meta.get("copied", []))
    return meta if ok else None


# -- vendoring ------------------------------------------------------------------------------------
def _safe_name(s, default="plugin"):
    s = re.sub(r"[^A-Za-z0-9._-]+", "-", s or "").strip(".-")
    return s[:64] or default


def _git_blob_sha(data):
    return hashlib.sha1(b"blob %d\0" % len(data) + data).hexdigest()


class Blobs:
    """Blob contents by blob sha through gh, decoded from base64 and checked against their git hash."""

    def __init__(self, gh, repo):
        self.gh, self.repo, self.cache = gh, repo, {}

    def get(self, entry):
        sha = entry.get("sha") or ""
        if sha not in self.cache:
            if not SHA_RE.fullmatch(sha):
                raise VendorError(f"tree entry {entry.get('path')!r} has no valid blob sha")
            try:
                data = self.gh.json(f"repos/{self.repo}/git/blobs/{sha}")
                body = base64.b64decode(data["content"]) if data.get("encoding") == "base64" else None
            except (GhError, KeyError, TypeError, ValueError) as e:
                raise VendorError(f"cannot fetch blob of {entry.get('path')!r}: {e}") from e
            if body is None:
                raise VendorError(f"blob of {entry.get('path')!r} is not base64; refusing")
            if _git_blob_sha(body) != sha:
                raise VendorError(f"blob of {entry.get('path')!r} does not match its sha {sha}; refusing")
            self.cache[sha] = body
        return self.cache[sha]

    def text(self, entry):
        try:
            return self.get(entry).decode("utf-8", "replace")
        except VendorError as e:
            raise risk.ReadError(str(e)) from e


def _plan(items):
    """Split scoped tree items into (plan [(rel, entry)] to copy, skipped [{path, reason}])."""
    plan, skipped = [], []
    for rel, e in items:
        mode, typ = str(e.get("mode", "")), e.get("type")
        if typ == "tree":
            continue
        segs = rel.split("/")
        if any(s in ("", ".", "..") for s in segs) or rel.startswith("/") or "\\" in rel or "\0" in rel:
            raise VendorError(f"unsafe path in tree: {rel!r}")
        if mode in ("120000", "160000") or typ == "commit":
            skipped.append({"path": rel, "reason": "symlink" if mode == "120000" else "submodule"})
        elif mode == "100755":
            skipped.append({"path": rel, "reason": "executable mode"})
        elif typ != "blob" or not risk._is_allowed(e["path"]):
            skipped.append({"path": rel, "reason": "not allow-listed"})
        else:
            plan.append((rel, e))
    return plan, skipped


def _wrap_dest(rel, skill_name, root_skill):
    low = rel.lower()
    if low.split("/")[0] in ("skills", "commands", "agents"):
        return rel
    return f"skills/{skill_name}/{rel}" if root_skill else f"skills/{rel}"


def vendor(gh, cand, root):
    """Vendor candidate row `cand` (a dict with id, repo, path, head_sha, licence, install_risk, status) into
    `root`. Returns {"dir", "plugin", "meta", "reclassified", "reused"}. Raises VendorError."""
    cid, repo, sha = cand["id"], cand.get("repo") or "", cand.get("head_sha") or ""
    path = (cand.get("path") or "").strip("/")
    if not sources.REPO_RE.match(repo) or not SHA_RE.fullmatch(sha) or (path and not sources.PATH_RE.match(path)):
        raise VendorError(f"{cid}: needs a GitHub repo and a full head sha (have repo={repo!r}, sha={sha!r}); "
                          "run `factory scout fetch` again")
    if cand.get("status") == NEEDS_OWNER:
        raise VendorError(f"{cid} is {NEEDS_OWNER}: pre-screen hits wait for the owner; nothing is vendored", EXIT_PRESCREEN)
    if (cand.get("install_risk") or "") != "prompt-only":
        raise VendorError(f"{cid}: install_risk is {cand.get('install_risk')!r}, not prompt-only; refusing")
    root = Path(root)
    done = vendored(root, cid, sha)
    if done:  # the tree at a sha never changes, so a valid copy needs no refetch
        return {"dir": vendor_dir(root, cid, sha), "plugin": done["plugin"], "meta": done, "reclassified": None, "reused": True}

    try:
        data = gh.json(f"repos/{repo}/git/trees/{sha}?recursive=1")
    except GhError as e:
        raise VendorError(f"{cid}: cannot list the tree at {sha}: {e}") from e
    tree = data.get("tree") if isinstance(data, dict) else None
    if not isinstance(tree, list):
        raise VendorError(f"{cid}: tree response at {sha} has no tree list")
    blobs = Blobs(gh, repo)
    fresh = risk.classify(tree, path, blobs.text, bool(data.get("truncated")))
    if fresh.risk != "prompt-only":
        raise VendorError(f"{cid}: fresh install risk at {sha[:12]} is {fresh.risk} (DB said prompt-only): "
                          + "; ".join(fresh.detail[:5]) + "; refusing", fresh=fresh)

    items = risk._scoped(tree, path)
    plan, skipped = _plan(items)
    wrapped = not any(r.lower() == ".claude-plugin/plugin.json" for r, _ in plan)
    if len(plan) > MAX_FILES:
        raise VendorError(f"{cid}: {len(plan)} files exceed the cap of {MAX_FILES}; refusing")
    if sum(int(e.get("size") or 0) for _r, e in plan) > MAX_BYTES:
        raise VendorError(f"{cid}: files exceed the {MAX_BYTES} byte cap; refusing")

    name_hint = _safe_name(path.rsplit("/", 1)[-1] if path else repo.split("/")[1])
    if wrapped:
        plugin_name = f"cand-{cid.lower()}"
        root_skill = any(r.lower() == "skill.md" for r, _ in plan)
        dests = [(_wrap_dest(r, name_hint, root_skill), e) for r, e in plan]
    else:
        pj_entry = next(e for r, e in plan if r.lower() == ".claude-plugin/plugin.json")
        try:
            plugin_name = _safe_name(json.loads(blobs.text(pj_entry).lstrip("﻿")).get("name"), name_hint)
        except (ValueError, AttributeError, risk.ReadError):
            plugin_name = name_hint
        dests = [(r, e) for r, e in plan]

    made_root, made_vendor = not root.exists(), not (root / "vendor").exists()
    out = vendor_dir(root, cid, sha)
    stage = out.with_name(out.name + ".partial")
    shutil.rmtree(stage, ignore_errors=True)
    try:
        plugin = stage / plugin_name
        total = 0
        copied = []
        for dest, e in dests:
            body = blobs.get(e)
            total += len(body)
            if total > MAX_BYTES:
                raise VendorError(f"{cid}: files exceed the {MAX_BYTES} byte cap; refusing")
            _write(plugin / dest, body)
            copied.append(dest)
        if wrapped:
            gen = {"name": plugin_name, "description": f"G-009 eval copy of {repo}@{sha}"}
            _write(plugin / ".claude-plugin" / "plugin.json", (json.dumps(gen, indent=2) + "\n").encode())
            copied.append(".claude-plugin/plugin.json")
        assert_prompt_only_tree(plugin)
        hits = prescreen(plugin, repo)
        meta = {"candidate": cid, "repo": repo, "path": path, "sha": sha, "licence": cand.get("licence"),
                "plugin": plugin_name, "wrapped": wrapped, "copied": sorted(copied), "skipped": skipped,
                "files": {rel: hashlib.sha256((plugin / rel).read_bytes()).hexdigest() for rel in sorted(copied)},
                "prescreen_hits": hits}
        _write(stage / "VENDOR.json", (json.dumps(meta, indent=2) + "\n").encode())
        shutil.rmtree(out, ignore_errors=True)
        os.replace(stage, out)
    except BaseException:
        shutil.rmtree(stage, ignore_errors=True)
        _prune(root, made_root, made_vendor)
        raise
    return {"dir": out, "plugin": plugin_name, "meta": meta, "reclassified": fresh, "reused": False}


def _write(p, body):
    p.parent.mkdir(parents=True, exist_ok=True)
    fd = os.open(p, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o644)
    with os.fdopen(fd, "wb") as fh:
        fh.write(body)
    os.chmod(p, 0o644)


def _prune(root, made_root, made_vendor):
    """Remove the vendor/ and root dirs this call created, so a refusal leaves nothing behind."""
    for d, made in ((root / "vendor", made_vendor), (root, made_root)):
        if made:
            try:
                d.rmdir()
            except OSError:
                break


def hit_lines(hits):
    return [f"prescreen {h['pattern']}: {h['file']}:{h['line']}" for h in hits]
