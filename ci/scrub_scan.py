#!/usr/bin/env python3
"""Own scrub scanner: finds owner-specific and sensitive data in a git tree, in git history, or in plain files.

Python 3 stdlib only, no network, no writes outside stdout/stderr. Generic shapes (home paths, LAN addresses,
emails, token shapes) are built in. Owner-specific literal terms are never committed: they come from the file in
$SCRUB_TERMS_FILE and/or --terms-file (one term per line, case-insensitive) and only their count is ever printed.
See docs/scrub-scan.md. Exit codes: 0 no hits, 1 hits, 2 usage or runtime error.
"""
import argparse
import fnmatch
import hashlib
import os
import re
import subprocess
import sys
import tempfile
from collections import Counter
from pathlib import Path

DEFAULT_ALLOWLIST = Path(__file__).resolve().parent / "scrub_allowlist.txt"
HOME_OK = {"user", "runner", "example", "placeholder"}
EMAIL_OK_DOMAIN = re.compile(r"example\.(com|org|invalid)", re.I)
EMAIL_OK_FULL = {"noreply@anthropic.com", "noreply@github.com"}
EMAIL_OK_NOREPLY = re.compile(r"(?:\d{1,20}\+)?[a-z0-9](?:[a-z0-9-]{0,38})@users\.noreply\.github\.com")
MARK = "\x01" + os.urandom(8).hex() + "\x01"  # git log field marker: random per run, so no commit message or tag can forge it
BINARY_PROBE = 8000
# git run for --history ignores user/system config, replace refs and the caller's GIT_* redirects: they change what log -p prints
HARDEN_ENV = {"GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_NOSYSTEM": "1", "GIT_NO_REPLACE_OBJECTS": "1"}
LOG_FLAGS = ["--all", "--root", "-m", "-p", "--text", "--no-textconv", "--no-ext-diff", "--no-renames", "--no-color",
             "--no-use-mailmap", "--src-prefix=a/", "--dst-prefix=b/"]
ENV_OK = {".env.example", ".env.sample"}

HOME_RE = re.compile("/ho" + "me/" + r"([^/\s'\"`]+)/")  # split so this line is not a hit itself
# Bounded so a long run without "@" scans in linear time: local part <= 64, labels <= 63, <= 8 labels.
EMAIL_RE = re.compile(r"[A-Za-z0-9._%+-]{1,64}@[A-Za-z0-9-]{1,63}(?:\.[A-Za-z0-9-]{1,63}){1,7}")

OCT = r"(?:25[0-5]|2[0-4]\d|[01]?\d?\d)"  # one IPv4 octet, 0-255, leading zeros allowed
# the key words of a secret assignment (generic-secret)
KEY_WORDS = r"(?:secret|token|api[_-]?key|passw(?:or)?d|private[_-]?key)"
PLACEHOLDER_RE = re.compile(r"xxx|changeme|example|dummy|test", re.I)
RAILWAY_UUID = re.compile(r"\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b", re.I)


CODE_REF_RE = re.compile(r"[A-Za-z_]\w*(?:\.\w+)+")  # `tokens = api.session_tokens`: code, not a value


def _real_value(m):
    """A secret-looking value is not a placeholder such as <...>, ${...}, $VAR, xxx, changeme, example, dummy, test."""
    v = m.group("v")
    return not v.startswith(("<", "$")) and not PLACEHOLDER_RE.search(v) and not CODE_REF_RE.fullmatch(v)


# (pattern id, regex, optional predicate on the match deciding whether it is a real hit)
GENERIC = (
    ("home-path", HOME_RE, lambda m: not _placeholder_home(m.group(1))),
    ("rfc1918", re.compile(r"(?<![\d.])(?:192\.168\.{o}\.{o}|10\.{o}\.{o}\.{o}|172\.0*(?:1[6-9]|2\d|3[01])\.{o}\.{o})(?![\d])"
                           .replace("{o}", OCT)), None),
    ("cgnat", re.compile(r"(?<![\d.])100\.0*(?:6[4-9]|[7-9]\d|1[01]\d|12[0-7])\.{o}\.{o}(?![\d])".replace("{o}", OCT)), None),
    ("email", EMAIL_RE, lambda m: not _allowed_email(m.group(0))),
    ("github-token", re.compile(r"\bgh[pousr]_[A-Za-z0-9]{36,}"), None),
    ("github-pat", re.compile(r"\bgithub_pat_[A-Za-z0-9_]{22,}"), None),
    ("anthropic-key", re.compile(r"\bsk-ant-[A-Za-z0-9]+-[A-Za-z0-9_-]{20,}"), None),
    ("aws-access-key-id", re.compile(r"\bAKIA[0-9A-Z]{16}"), None),
    ("private-key", re.compile(r"-----BEGIN [A-Z ]*PRIVATE KEY-----"), None),
    ("railway-token", re.compile(r"\bRAILWAY_(?:API_)?TOKEN\b[\"']?\s*[:=]\s*[\"']?(?P<v>[^\s\"'`]{16,})"), _real_value),
    ("railway-token", RAILWAY_UUID, lambda m: "railway" in m.string.lower()),
    ("oauth-secret", re.compile(r"\b(?:client_secret|refresh_token|access_token)\b[\"']?\s*[:=]\s*[\"']?(?P<v>[A-Za-z0-9._~+/=-]{20,})", re.I), None),
    ("generic-secret", re.compile(KEY_WORDS + r"[\w.-]{0,64}[\"']?\s*[:=]\s*[\"']?(?P<v>[^\s\"'`(){}\[\]<>,;$]{16,})", re.I), _real_value),
)


OWNER_TERM = "owner-term"  # never allowlisted
SHORT_TERM = 4  # owner terms this short or shorter match whole words only
LEGACY_OK = {"home-path", "rfc1918", "email"}  # a legacy path:regex entry may only suppress these
ENV_FILE = "env-file"  # a path check, not a regex: tracked .env files
PATTERN_IDS = {pid for pid, _, _ in GENERIC} | {OWNER_TERM, ENV_FILE}


class UsageError(Exception):
    pass


def _placeholder_home(name):
    return name.lower() in HOME_OK or (name.startswith("<") and name.endswith(">"))


def _allowed_email(addr):
    low = addr.lower()
    return low in EMAIL_OK_FULL or bool(EMAIL_OK_NOREPLY.fullmatch(low)) or bool(EMAIL_OK_DOMAIN.fullmatch(low.rsplit("@", 1)[1]))


def is_env_file(path):
    name = os.path.basename(path)
    name = name.lower()
    return name not in ENV_OK and (fnmatch.fnmatchcase(name, "*.env") or fnmatch.fnmatchcase(name, ".env.*"))


def mask(text):
    """Keep at most the first 4 characters (fewer for short matches); the rest is hidden."""
    return text[:min(4, len(text) // 2)] + "***"


def load_terms(paths):
    terms = []
    for p in paths:
        try:
            text = Path(p).read_text(encoding="utf-8", errors="replace")
        except OSError as e:
            raise UsageError(f"cannot read terms file {p!r}: {e.strerror}. Check SCRUB_TERMS_FILE / --terms-file.")
        terms += [t.strip() for t in text.splitlines() if t.strip()]
    return sorted(set(terms), key=str.lower)


class Scanner:
    def __init__(self, terms):
        self.terms = terms
        # one whole-text, case-insensitive matcher per term; its 1-based index is printed instead of any term text
        self.term_fulls = [re.compile(re.escape(t), re.I) for t in terms]
        # terms of <= SHORT_TERM chars match only as a whole word; longer terms match as substrings
        alts = [(r"(?<!\w)%s(?!\w)" if len(t) <= SHORT_TERM else "%s") % re.escape(t)
                for t in sorted(terms, key=len, reverse=True)]
        self.term_re = re.compile("|".join(alts), re.I) if terms else None

    def matches(self, line):
        """Yield (pattern id, matched text) for every hit on one line."""
        for pid, rx, keep in GENERIC:
            for m in rx.finditer(line):
                if keep is None or keep(m):
                    yield pid, m
        if self.term_re:
            for m in self.term_re.finditer(line):
                yield OWNER_TERM, m

    def label(self, m):
        """What is printed for an owner-term match: its index, never any of its text."""
        text = m.group(0)
        for i, rx in enumerate(self.term_fulls, 1):  # fullmatch, not a lower() lookup: re.I folds more than str.lower()
            if rx.fullmatch(text):
                return f"{OWNER_TERM}#{i}"
        return f"{OWNER_TERM}#?"

    def scan_line(self, line):
        """Yield (pattern id, masked text) for every hit on one line; an owner-term hit prints owner-term#<n> only."""
        for pid, m in self.matches(line):
            if pid == OWNER_TERM:
                yield pid, self.label(m)
                continue
            text = self.hide_terms(m.group(0))  # a hit overlapping a term must not show a term prefix
            yield pid, mask(text)

    def hide_terms(self, text):
        """Paths are printed, so owner terms inside them print as owner-term#<n>."""
        return self.term_re.sub(self.label, text) if self.term_re else text

    def hide_all(self, text):
        """Mask every hit (not only owner terms) in text that is about to be printed, e.g. a path name."""
        spans = sorted(((m.span(), pid == OWNER_TERM and self.label(m)) for pid, m in self.matches(text)), key=lambda x: x[0])
        out, pos = [], 0
        for (a, b), term in spans:
            if a < pos:
                continue
            out.append(text[pos:a] + (term or mask(self.hide_terms(text[a:b]))))
            pos = b
        return "".join(out) + text[pos:]


class Allowlist:
    """Lines `path:pattern-id:regex-of-line  # reason`; applies to --tree and --path only.

    An entry suppresses only hits of its pattern id. The legacy `path:regex` form is still read (with a warning) and
    suppresses only the home-path, rfc1918 and email shapes. owner-term and token-shape hits need an exact id entry,
    and owner-term hits are never suppressed."""

    def __init__(self, entries=()):
        self.entries = list(entries)  # (path, pattern id or None for legacy, compiled regex)

    @classmethod
    def load(cls, path):
        entries = []
        try:
            raw = Path(path).read_text(encoding="utf-8").splitlines()
        except OSError as e:
            raise UsageError(f"cannot read allowlist {str(path)!r}: {e.strerror}")
        for n, line in enumerate(raw, 1):
            if not line.strip() or line.lstrip().startswith("#"):
                continue
            body, sep, reason = line.rpartition(" # ")
            if not sep or not reason.strip():
                raise UsageError(f"{path}:{n}: allowlist line needs a '  # reason' comment. See docs/scrub-scan.md.")
            apath, colon, rest = body.partition(":")
            if not colon or not apath.strip() or not rest.strip():
                raise UsageError(f"{path}:{n}: expected 'path:pattern-id:regex  # reason'. See docs/scrub-scan.md.")
            pid, colon, rx = rest.strip().partition(":")
            if colon and pid in PATTERN_IDS:
                if pid == OWNER_TERM:
                    raise UsageError(f"{path}:{n}: owner-term hits can never be allowlisted. Remove this line.")
                if not rx.strip():
                    raise UsageError(f"{path}:{n}: expected 'path:pattern-id:regex  # reason'. See docs/scrub-scan.md.")
            else:
                pid, rx = None, rest.strip()
                print(f"scrub-scan: warning: {path}:{n}: legacy 'path:regex' allowlist entry is deprecated; "
                      "use 'path:pattern-id:regex'. See docs/scrub-scan.md.", file=sys.stderr)
            try:
                entries.append((apath.strip(), pid, re.compile(rx.strip())))
            except re.error as e:
                raise UsageError(f"{path}:{n}: bad regex: {e}")
        return cls(entries)

    def allows(self, names, line, pid):
        if pid == OWNER_TERM:
            return False
        return any(p in names and rx.search(line) and (e == pid or (e is None and pid in LEGACY_OK))
                   for p, e, rx in self.entries)


class HistoryAllowlist:
    """Lines `path:pattern-id:sha256hex  # reason` for --history-allowlist.

    path is exact (as git prints it, C-unquoted); sha256hex is the SHA-256 of the raw bytes of one added or removed
    diff line without its +/- marker and trailing LF. An entry suppresses only a blob-content hit of that pattern id
    at that path; it never suppresses author, committer, message, path-name, ref or tag hits."""
    ENTRY = re.compile(r"(?P<path>.+?):(?P<pid>[a-z0-9-]+):(?P<sha>[0-9a-f]{64})[ \t]+#[ \t]*(?P<reason>\S.*)")

    def __init__(self, keys=()):
        self.used = {k: False for k in keys}

    @classmethod
    def load(cls, path):
        try:
            raw = Path(path).read_bytes().decode("utf-8", "surrogateescape")
        except OSError as e:
            raise UsageError(f"cannot read history allowlist {str(path)!r}: {e.strerror}")
        keys = []
        for n, line in enumerate(raw.split("\n"), 1):
            if not line.strip() or line.lstrip().startswith("#"):
                continue
            m = cls.ENTRY.fullmatch(line.rstrip("\r"))
            if not m:
                why = ("needs a '  # reason' comment" if " # " not in line and "\t# " not in line
                       else "expected 'path:pattern-id:sha256  # reason' with a 64-character lowercase hex sha256")
                raise UsageError(f"{path}:{n}: history allowlist line {why}. See docs/scrub-scan.md.")
            pid = m.group("pid")
            if pid == OWNER_TERM:
                raise UsageError(f"{path}:{n}: owner-term hits can never be allowlisted. Remove this line.")
            if pid not in PATTERN_IDS:
                raise UsageError(f"{path}:{n}: unknown pattern id {pid!r}. Use one of the ids in docs/scrub-scan.md.")
            key = (m.group("path"), pid, m.group("sha"))
            if key in keys:
                raise UsageError(f"{path}:{n}: duplicate history allowlist entry. Remove one of them.")
            keys.append(key)
        return cls(keys)

    def allows(self, path, pid, sha):
        if (path, pid, sha) in self.used:
            self.used[(path, pid, sha)] = True
            return True
        return False

    def unused(self):
        return [k for k, used in self.used.items() if not used]


class Report:
    def __init__(self, scanner):
        self.scanner = scanner
        self.counts = Counter()
        self.allowlisted = 0
        self.skipped = []

    def hit(self, pid, loc, masked, extra=""):
        self.counts[pid] += 1
        print(f"{pid} {self.scanner.hide_terms(loc)} {masked}{extra}")

    def path_hit(self, pid, rel, line, allow):
        """A hit on a file name alone: the content is never read or printed."""
        if allow is not None and allow.allows({rel}, line, pid):
            self.allowlisted += 1
        else:
            self.hit(pid, f"{rel}:0", mask(self.scanner.hide_terms(os.path.basename(rel))))

    def scan_text(self, text, path, allow, names):
        for n, line in enumerate(text.splitlines(), 1):
            for pid, masked in self.scanner.scan_line(line):
                if allow is not None and allow.allows(names, line, pid):
                    self.allowlisted += 1
                else:
                    self.hit(pid, f"{path}:{n}", masked)

    def scan_file(self, fs_path, shown, allow, names):
        try:
            data = Path(fs_path).read_bytes()
        except OSError as e:
            raise UsageError(f"cannot read {shown!r}: {e.strerror}")
        if b"\0" in data[:BINARY_PROBE]:
            self.skipped.append(shown)
            return
        self.scan_text(data.decode("utf-8", errors="replace"), shown, allow, names)

    def finish(self, mode, with_allowlist=False):
        for pid in sorted(self.counts):
            print(f"  {pid}: {self.counts[pid]}")
        total = sum(self.counts.values())
        extra = f" ({self.allowlisted} allowlisted)" if mode != "history" or with_allowlist else ""
        print(f"{total} hits{extra}")
        if self.skipped:
            print(f"scrub-scan: skipped {len(self.skipped)} binary file(s): not text-scannable", file=sys.stderr)
        return 1 if total else 0


def git(directory, *args):
    try:
        return subprocess.run(["git", "-C", directory, *args], capture_output=True, check=True)
    except FileNotFoundError:
        raise UsageError("git not found on PATH")
    except subprocess.CalledProcessError as e:
        msg = e.stderr.decode("utf-8", "replace").strip()
        raise UsageError(f"git {args[0]} failed in {directory!r}: {msg}. Pass a directory inside a git repository.")


def scan_tree(directory, report, allow):
    out = git(directory, "ls-files", "-z").stdout
    for rel in filter(None, out.decode("utf-8", "surrogateescape").split("\0")):
        full = os.path.join(directory, rel)
        if os.path.islink(full) or not os.path.isfile(full):
            continue
        if is_env_file(rel):
            report.path_hit(ENV_FILE, rel, rel, allow)
        report.scan_file(full, rel, allow, {rel})


def git_env():
    """The caller's environment minus git's own variables (GIT_DIR and friends), plus the hardening above."""
    env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
    env.update(HARDEN_ENV)
    return env


_ESCAPES = {ord(c): v for c, v in zip('abfnrtv\\"', (7, 8, 12, 10, 13, 9, 11, 92, 34))}


def c_unquote(data):
    """Bytes of a git C-quoted name (surrounding quotes included) -> raw bytes."""
    if len(data) < 2 or data[:1] != b'"' or data[-1:] != b'"':
        raise UsageError("cannot parse a quoted path in the git diff header. Report this to the scanner owner.")
    inner, out, i = data[1:-1], bytearray(), 0
    while i < len(inner):
        c = inner[i]
        i += 1
        if c != 0x5C:
            out.append(c)
        elif i < len(inner) and 0x30 <= inner[i] <= 0x37:
            j = i
            while j < len(inner) and j < i + 3 and 0x30 <= inner[j] <= 0x37:
                j += 1
            out.append(int(inner[i:j], 8) & 0xFF)
            i = j
        elif i < len(inner) and inner[i] in _ESCAPES:
            out.append(_ESCAPES[inner[i]])
            i += 1
        else:
            raise UsageError("cannot parse a quoted path in the git diff header. Report this to the scanner owner.")
    return bytes(out)


def diff_header_path(body):
    """Path of a `diff --git a/P b/P` line (rename detection is off, so both sides are the same path), as raw bytes."""
    rest = body[len(b"diff --git "):]
    if rest.startswith(b'"'):
        i = 1
        while i < len(rest) and rest[i] != 0x22:
            i += 2 if rest[i] == 0x5C else 1
        first = c_unquote(rest[:i + 1])
        second = rest[i + 1:].lstrip(b" ")
        if first.startswith(b"a/") and second.startswith(b'"') and c_unquote(second) == b"b/" + first[2:]:
            return first[2:]
    elif rest.startswith(b"a/"):
        rest = rest[2:]
        n = (len(rest) - 3) // 2
        if n > 0 and rest[n:n + 3] == b" b/" and rest[n + 3:] == rest[:n]:
            return rest[:n]
    raise UsageError("cannot parse a path from a git diff header. Report this to the scanner owner.")


def shown(scanner, path):
    """A raw path as one line of printable text: every hit in it masked, control characters escaped."""
    text = scanner.hide_all(path.decode("utf-8", "replace"))
    return re.sub(r"[\x00-\x1f\x7f]", lambda m: "\\x%02x" % ord(m.group()), text)


def history_hits(directory, scanner, excludes):
    """Yield (pattern id, location, masked, entry) from `git log -p --all` plus author/committer identities, path names,
    ref names and annotated tags. entry is (path, sha256 of the raw line) for a blob-content hit, else None."""
    fmt = f"--format={MARK}H%H%n{MARK}A%an <%ae>%n{MARK}C%cn <%ce>%n{MARK}M%n%B{MARK}E"
    cmd = ["git", "-C", directory, "-c", "core.quotepath=off", "-c", "i18n.logOutputEncoding=UTF-8", "-c", "log.showSignature=false", "log"]
    cmd += [f"--exclude={r}" for r in excludes]
    cmd += LOG_FLAGS + [fmt]
    # stderr goes to a temp file: reading two pipes one after the other deadlocks once git fills the stderr pipe
    with tempfile.TemporaryFile() as errf:
        try:
            proc = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=errf, env=git_env())
        except FileNotFoundError:
            raise UsageError("git not found on PATH")
        yield from _history_lines(proc, directory, scanner, errf)
    yield from _header_hits(directory, scanner, excludes)
    yield from _ref_hits(directory, scanner, excludes)


def _header_hits(directory, scanner, excludes):
    """Extra commit headers (mergetag, gpgsig, encoding, ...) from the raw commit object of every commit: git log does not print them."""
    cmd = ["git", "-C", directory, "rev-list"] + [f"--exclude={r}" for r in excludes] + ["--all"]
    try:
        ids = subprocess.run(cmd, capture_output=True, check=True, env=git_env()).stdout.decode("ascii", "replace").split()
        if not ids:
            return
        # --batch has no header-only mode, so all commit objects are held in memory at once (fine at this repo's size)
        raw = subprocess.run(["git", "-C", directory, "cat-file", "--batch"], capture_output=True, check=True, env=git_env(),
                             input="".join(i + "\n" for i in ids).encode()).stdout
    except FileNotFoundError:
        raise UsageError("git not found on PATH")
    except subprocess.CalledProcessError as e:
        raise UsageError(f"git {e.cmd[3]} failed in {directory!r}: {e.stderr.decode('utf-8', 'replace').strip()[-2000:]}")
    pos = 0
    for commit in ids:
        nl = raw.index(b"\n", pos)
        size = int(raw[pos:nl].split()[2])
        head = raw[nl + 1:nl + 1 + size].split(b"\n\n", 1)[0].decode("utf-8", "replace")
        pos = nl + 1 + size + 1
        name = ""
        for line in head.split("\n"):
            if not line.startswith(" "):
                name = line.split(" ", 1)[0]
            if name in ("tree", "parent", "author", "committer"):  # the log scan covers these
                continue
            for pid, masked in scanner.scan_line(line):
                yield pid, f"{commit}:header {shown(scanner, name.encode())}", masked, None  # the name is attacker-controlled text


def _ref_hits(directory, scanner, excludes):
    sep = "\x01" + os.urandom(8).hex() + "\x01"  # random per run: tag text cannot forge a field boundary
    fmt = f"{sep}R%(refname){sep}K%(objecttype){sep}N%(tag){sep}G%(taggername) %(taggeremail){sep}C%(contents)"
    try:
        out = subprocess.run(["git", "-C", directory, "for-each-ref", f"--format={fmt}"], capture_output=True,
                             check=True, env=git_env()).stdout.decode("utf-8", "replace")
    except FileNotFoundError:
        raise UsageError("git not found on PATH")
    except subprocess.CalledProcessError as e:
        raise UsageError(f"git for-each-ref failed in {directory!r}: {e.stderr.decode('utf-8', 'replace').strip()}")
    for rec in out.split(sep + "R")[1:]:
        fields = {}
        name, *rest = rec.split(sep)
        for part in rest:
            fields[part[:1]] = part[1:]
        if any(fnmatch.fnmatchcase(name, pat) for pat in excludes):
            continue
        for pid, masked in scanner.scan_line(name):
            yield pid, f"ref:{scanner.hide_all(name)}", masked, None
        if fields.get("K") != "tag":  # a branch's %(contents) is its tip commit, which the log scan covers
            continue
        for text in [fields.get("N", ""), fields.get("G", "")] + fields.get("C", "").split("\n"):
            for pid, masked in scanner.scan_line(text):
                yield pid, f"tag:{scanner.hide_all(name)}", masked, None


HISTORY_ALLOWLIST_PATH = b"ci/scrub_history_allowlist.txt"


def _is_allowlist_entry(scanner, text):
    """A well-formed `path:secret-id:sha256hex  # reason` line of the committed history allowlist: its own `generic-secret:<hex>`
    would otherwise match the generic-secret pattern, so no entry for that pattern could ever be added. The reason must be clean."""
    m = HistoryAllowlist.ENTRY.fullmatch(text)
    if not m or not re.fullmatch(r"[\w./-]+", m.group("path")):  # a colon or `=` in the path field could carry a secret
        return False
    return not any(pid == "generic-secret" for field in ("path", "reason") for pid, _ in scanner.scan_line(m.group(field)))


def _history_lines(proc, directory, scanner, errf):
    commit, state, path, in_header = "", "msg", b"", False
    for raw in proc.stdout:  # git's output is split on LF only (not str.splitlines): the line hash is over these bytes
        body = raw[:-1] if raw.endswith(b"\n") else raw
        line = body.decode("utf-8", errors="replace")
        if line.startswith(MARK + "H"):
            commit, state, path, in_header = line[len(MARK) + 1:], "head", b"", False
            continue
        if state == "head" and line.startswith(MARK + "A"):
            who = ("author", line[len(MARK) + 1:])
        elif state == "head" and line.startswith(MARK + "C"):
            who = ("committer", line[len(MARK) + 1:])
        else:
            who = None
        if who:
            for pid, masked in scanner.scan_line(who[1]):
                yield pid, f"{commit}:{who[0]}", masked, None
            continue
        if state == "head" and line.startswith(MARK + "M"):
            state = "msg"
            continue
        if state == "msg":
            end = line.find(MARK + "E")
            for pid, masked in scanner.scan_line(line if end < 0 else line[:end]):
                yield pid, f"{commit}:message", masked, None
            if end >= 0:
                state = "diff"
            continue
        if body.startswith(b"diff --git "):
            path, in_header = diff_header_path(body), True
            text = path.decode("utf-8", "replace")
            for pid, masked in scanner.scan_line(text):
                yield pid, f"{commit}:{shown(scanner, path)} (path name)", masked, None
            if is_env_file(text):
                yield ENV_FILE, f"{commit}:{shown(scanner, path)} (path name)", mask(scanner.hide_terms(os.path.basename(text))), None
            continue
        if in_header:
            in_header = not body.startswith(b"@@")
            continue
        if body[:1] not in (b"+", b"-"):
            continue
        content = body[1:]  # the diff marker would otherwise join the local part of an email
        entry = (path.decode("utf-8", "surrogateescape"), hashlib.sha256(content).hexdigest())
        text = content.decode("utf-8", "replace")
        for pid, masked in scanner.scan_line(text):
            if pid == "generic-secret" and path == HISTORY_ALLOWLIST_PATH and _is_allowlist_entry(scanner, text):
                continue
            yield pid, f"{commit}:{shown(scanner, path)}", masked, entry
    if proc.wait() != 0:
        errf.seek(0)
        err = errf.read().decode("utf-8", "replace").strip()[-2000:]
        raise UsageError(f"git log failed in {directory!r}: {err}. Pass a directory inside a git repository.")


def build_parser():
    p = argparse.ArgumentParser(
        prog="scrub_scan.py", allow_abbrev=False,
        description="Scan for owner-specific data and secrets. Exit 0 = no hits, 1 = hits, 2 = usage error.",
        epilog="pattern ids: " + ", ".join(sorted(PATTERN_IDS)))
    mode = p.add_mutually_exclusive_group(required=True)
    mode.add_argument("--tree", nargs="?", const=".", metavar="DIR", help="tracked files (git ls-files in DIR, default .)")
    mode.add_argument("--history", nargs="?", const=".", metavar="DIR", help="git log -p --all plus author/committer identities")
    mode.add_argument("--path", nargs="+", metavar="FILE", help="arbitrary files, git not needed")
    p.add_argument("--exclude-ref", action="append", nargs="+", metavar="REF", default=[],
                   help="--history only: leave this ref out (repeatable)")
    p.add_argument("--history-allowlist", metavar="FILE",
                   help="--history only: exact reviewed blob lines, 'path:pattern-id:sha256  # reason' (default: none)")
    p.add_argument("--show-line-hash", action="store_true",
                   help="--history only: print sha256=<hex> of the raw line on each blob-content hit (to write allowlist entries)")
    p.add_argument("--terms-file", action="append", default=[], metavar="FILE",
                   help="owner terms, one per line (also read from $SCRUB_TERMS_FILE)")
    p.add_argument("--allowlist", metavar="FILE", default=str(DEFAULT_ALLOWLIST),
                   help="reviewed hits for --tree/--path (default ci/scrub_allowlist.txt)")
    return p


def run(argv):
    args = build_parser().parse_args(argv)
    excludes = [r for group in args.exclude_ref for r in group]
    if excludes and args.history is None:
        raise UsageError("--exclude-ref only applies to --history")
    term_files = list(args.terms_file)
    if os.environ.get("SCRUB_TERMS_FILE"):
        term_files.append(os.environ["SCRUB_TERMS_FILE"])
    terms = load_terms(term_files)
    if terms:
        print(f"scrub-scan: {len(terms)} owner term(s) loaded", file=sys.stderr)
    else:
        print("scrub-scan: warning: no owner terms loaded (set SCRUB_TERMS_FILE or pass --terms-file); generic patterns only",
              file=sys.stderr)
    scanner = Scanner(terms)
    report = Report(scanner)
    if args.history is None and (args.history_allowlist or args.show_line_hash):
        raise UsageError("--history-allowlist and --show-line-hash only apply to --history")
    if args.history is not None:
        hallow = HistoryAllowlist.load(args.history_allowlist) if args.history_allowlist else None
        for pid, loc, masked, entry in history_hits(args.history, scanner, excludes):
            if entry and hallow and hallow.allows(entry[0], pid, entry[1]):
                report.allowlisted += 1
                continue
            report.hit(pid, loc, masked, f" sha256={entry[1]}" if entry and args.show_line_hash else "")
        for apath, pid, sha in (hallow.unused() if hallow else ()):
            print(f"scrub-scan: warning: unused history allowlist entry {shown(scanner, apath.encode('utf-8', 'surrogateescape'))}:{pid}:{sha[:12]}...",
                  file=sys.stderr)
        return report.finish("history", with_allowlist=hallow is not None)
    allow = Allowlist.load(args.allowlist)
    if args.tree is not None:
        scan_tree(args.tree, report, allow)
        return report.finish("tree")
    cwd = os.getcwd()
    for f in args.path:
        norm = os.path.normpath(f)
        report.scan_file(f, f, allow, {norm, os.path.relpath(os.path.abspath(f), cwd)})
    return report.finish("path")


def main(argv=None):
    try:
        return run(sys.argv[1:] if argv is None else argv)
    except UsageError as e:
        print(f"scrub-scan: error: {e}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
