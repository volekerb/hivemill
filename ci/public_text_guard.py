#!/usr/bin/env python3
"""Public text guard: refuses private data in text that is about to become public (PR/commit text, ref names, pushed diffs).

Python 3 stdlib only, no network. The patterns are ci/scrub_scan.py's (imported by path, not copied) plus two that only
matter for text in public: any non-documentation IPv4 address and private host-name suffixes. Owner terms come only from
the terms file; with none loaded the guard fails closed (exit 2). See docs/public-text-guard.md.
Exit codes: 0 clean, 1 hits, 2 usage or runtime error.
"""
import argparse
import functools
import importlib.util
import os
import re
import subprocess
import sys
from pathlib import Path

SCAN = Path(__file__).resolve().parent / "scrub_scan.py"
NO_TERMS = "public-text-guard: no owner terms loaded"


def _load_scrub():
    spec = importlib.util.spec_from_file_location("scrub_scan_mod", SCAN)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


scrub = _load_scrub()
UsageError = scrub.UsageError
mask = scrub.mask

_OCT = r"(?:25[0-5]|2[0-4]\d|1\d\d|0?\d?\d)"  # leading zeros too: inet_aton reads 010 as octal
# Not glued to a longer dotted number on either side; a sentence-ending "." after the address is fine.
IPV4_RE = re.compile(r"(?<!\d)(?<!\d\.)" + _OCT + r"(?:\." + _OCT + r"){3}(?!\d)(?!\.\d)")
HOST_RE = re.compile(r"(?<![A-Za-z0-9_-])(?:[A-Za-z0-9_-]{1,63}\.){1,8}(?:local|lan|internal|home\.arpa|ts\.net)(?![A-Za-z0-9_-])(?!\.[A-Za-z0-9])", re.I)
_DOC_NETS = ((192, 0, 2), (198, 51, 100), (203, 0, 113))


def _real_ipv4(m):
    parts = m.group(0).split(".")
    if any(len(o) > 1 and o[0] == "0" for o in parts):  # octal-looking form: never exempt
        return True
    octets = tuple(int(o) for o in parts)
    if octets[0] == 127 or octets in ((0, 0, 0, 0), (255, 255, 255, 255)):
        return False
    return octets[:3] not in _DOC_NETS


EXTRA = (("ipv4", IPV4_RE, _real_ipv4), ("private-host", HOST_RE, None))
PATTERN_IDS = sorted({pid for pid, _, _ in scrub.GENERIC} | {pid for pid, _, _ in EXTRA} | {scrub.OWNER_TERM})


class NoTermsError(UsageError):
    pass


class GuardScanner(scrub.Scanner):
    """scrub_scan's Scanner plus the extra pattern ids; hide_all/hide_terms/scan_line see them too."""

    def matches(self, line):
        yield from super().matches(line)
        for pid, rx, keep in EXTRA:
            for m in rx.finditer(line):
                if keep is None or keep(m):
                    yield pid, m


@functools.lru_cache(maxsize=8)
def _scanner(terms):
    return GuardScanner(list(terms))


def load_terms_or_fail(env, config_path=None, extra_paths=()):
    """Owner terms from --terms-file paths and $SCRUB_TERMS_FILE, else config_path. None loaded -> NoTermsError (fail closed)."""
    paths = [str(p) for p in extra_paths]
    if env.get("SCRUB_TERMS_FILE"):
        paths.append(env["SCRUB_TERMS_FILE"])
    elif not paths and config_path:
        paths.append(str(config_path))
    terms = scrub.load_terms(paths)
    if not terms:
        raise NoTermsError(f"{NO_TERMS}. Set SCRUB_TERMS_FILE (or the scrub_terms_file config) to the owner terms file.")
    return terms


def check_text(text, terms):
    """[(pattern id, masked text)] for every hit in text (line by line)."""
    sc = _scanner(tuple(terms))
    return [hit for line in text.splitlines() for hit in sc.scan_line(line)]


def check_ref(name, terms):
    """check_text for one branch or tag name."""
    return check_text(name, terms)


def _git(repo, *args):
    cmd = ["git", "-C", str(repo), "-c", "core.quotepath=off", "-c", "i18n.logOutputEncoding=UTF-8",
           "-c", "log.showSignature=false", *args]
    try:
        return subprocess.run(cmd, capture_output=True, check=True, env=scrub.git_env()).stdout
    except FileNotFoundError:
        raise UsageError("git not found on PATH")
    except subprocess.CalledProcessError as e:
        raise UsageError(f"git {args[0]} failed in {str(repo)!r}: {e.stderr.decode('utf-8', 'replace').strip()}. "
                         "Pass --repo DIR inside a git repository and BASE/HEAD that exist there.")


HUNK_RE = re.compile(rb"@@ -\d+(?:,\d+)? \+(\d+)")
CC_HUNK_RE = re.compile(rb"(@@@+)(?: -\d+(?:,\d+)?)+ \+(\d+)")


def check_push(repo, base, head, terms, allowlist=None):
    """[(pattern id, location, masked)] for what pushing HEAD publishes beyond BASE: commit messages, identities,
    path names and added diff lines of every commit in BASE..HEAD. allowlist (scrub_scan.Allowlist, default the repo's
    ci/scrub_allowlist.txt) applies to added lines only, by path and pattern id; messages, identities, paths have none."""
    for rev in (base, head):
        if rev.startswith("-"):
            raise UsageError(f"revision {rev!r} looks like an option. Pass a commit id or branch name.")
    if allowlist is None:
        allowlist = scrub.Allowlist.load(scrub.DEFAULT_ALLOWLIST)
    sc = _scanner(tuple(terms))
    mark = scrub.MARK
    fmt = f"--format={mark}H%H%n{mark}A%an <%ae>%n{mark}C%cn <%ce>%n{mark}M%n%B{mark}E"
    # A merge is diffed with --cc (lines new against every parent), never against its first parent alone, so what a
    # merge of main brings in from BASE is not reported as added.
    flags = [f for f in scrub.LOG_FLAGS if f not in ("--all", "-m")]
    out = _git(repo, "log", "--cc", *flags, f"{base}..{head}", fmt, "--")
    hits = []
    commit, state, path, newline, in_header, width = "", "msg", b"", 0, False, 1
    for raw in out.split(b"\n"):
        line = raw.decode("utf-8", "replace")
        if line.startswith(mark + "H"):
            commit, state, path, in_header = line[len(mark) + 1:][:8], "head", b"", False
            continue
        if state == "head" and line.startswith((mark + "A", mark + "C")):
            who = "author" if line.startswith(mark + "A") else "committer"
            hits += [(pid, f"commit:{commit}:{who}", m) for pid, m in sc.scan_line(line[len(mark) + 1:])]
            continue
        if state == "head" and line.startswith(mark + "M"):
            state = "msg"
            continue
        if state == "msg":
            end = line.find(mark + "E")
            hits += [(pid, f"commit:{commit}:message", m) for pid, m in sc.scan_line(line if end < 0 else line[:end])]
            if end >= 0:
                state = "diff"
            continue
        if raw.startswith((b"diff --git ", b"diff --cc ")):
            if raw.startswith(b"diff --cc "):  # combined (merge) diff: one path, no a/ b/ prefix
                rest = raw[len(b"diff --cc "):]
                path = scrub.c_unquote(rest) if rest.startswith(b'"') else rest
            else:
                path = scrub.diff_header_path(raw)
            in_header = True
            hits += [(pid, f"commit:{commit}:path", m) for pid, m in sc.scan_line(path.decode("utf-8", "replace"))]
            continue
        cc = CC_HUNK_RE.match(raw) if raw.startswith(b"@@@") else None
        hunk = cc or (HUNK_RE.match(raw) if raw.startswith(b"@@") else None)
        if in_header and not hunk:
            continue
        if hunk:
            in_header = False
            newline = int(hunk.group(2 if cc else 1))
            width = len(cc.group(1)) - 1 if cc else 1
            continue
        if raw[:width] == b"+" * width:
            content = raw[width:].decode("utf-8", "replace")
            names = {path.decode("utf-8", "surrogateescape")}
            for pid, m in sc.scan_line(content):
                if not allowlist.allows(names, content, pid):
                    hits.append((pid, f"{scrub.shown(sc, path)}:{newline}", m))
            newline += 1
        elif b"-" not in raw[:width]:
            newline += 1
    return hits


def build_parser():
    common = argparse.ArgumentParser(add_help=False)
    common.add_argument("--terms-file", action="append", default=argparse.SUPPRESS, metavar="FILE",
                        help="owner terms, one per line (also read from $SCRUB_TERMS_FILE); repeatable")
    common.add_argument("--no-terms", action="store_true", default=argparse.SUPPRESS,
                        help="run without owner terms (tests/CI only; without this the guard fails closed)")
    p = argparse.ArgumentParser(prog="public_text_guard.py", allow_abbrev=False, parents=[common],
                                description="Refuse private data in text that becomes public. Exit 0 clean, 1 hits, 2 error.")
    sub = p.add_subparsers(dest="mode", required=True)
    t = sub.add_parser("text", parents=[common], help="scan text from FILE or stdin")
    t.add_argument("file", nargs="?", default="-", metavar="FILE|-")
    r = sub.add_parser("ref", parents=[common], help="scan branch/tag names")
    r.add_argument("names", nargs="+", metavar="NAME")
    s = sub.add_parser("push", parents=[common], help="scan what pushing HEAD publishes beyond BASE")
    s.add_argument("--repo", default=".", metavar="DIR")
    s.add_argument("--allowlist", metavar="FILE", default=str(scrub.DEFAULT_ALLOWLIST),
                   help="reviewed added-line hits (default ci/scrub_allowlist.txt)")
    s.add_argument("base")
    s.add_argument("head")
    return p


def run(argv):
    args = build_parser().parse_args(argv)
    files = getattr(args, "terms_file", [])
    if getattr(args, "no_terms", False):
        terms = scrub.load_terms(files + ([os.environ["SCRUB_TERMS_FILE"]] if os.environ.get("SCRUB_TERMS_FILE") else []))
    else:
        terms = load_terms_or_fail(os.environ, None, files)
    if terms:
        print(f"public-text-guard: {len(terms)} owner term(s) loaded", file=sys.stderr)
    hits = []  # (pattern id, location, masked)
    sc = _scanner(tuple(terms))
    if args.mode == "text":
        try:
            data = sys.stdin.buffer.read() if args.file == "-" else Path(args.file).read_bytes()
        except OSError as e:
            raise UsageError(f"cannot read {args.file!r}: {e.strerror}")
        for n, line in enumerate(data.decode("utf-8", "replace").splitlines(), 1):
            hits += [(pid, f"line {n}", m) for pid, m in sc.scan_line(line)]
    elif args.mode == "ref":
        for name in args.names:
            hits += [(pid, f"ref:{sc.hide_all(name)}", m) for pid, m in check_ref(name, terms)]
    else:
        hits = check_push(args.repo, args.base, args.head, terms, scrub.Allowlist.load(args.allowlist))
    for pid, loc, masked in hits:
        print(f"{pid} {sc.hide_terms(loc)} {masked}")
    print(f"{len(hits)} hits")
    return 1 if hits else 0


def main(argv=None):
    try:
        return run(sys.argv[1:] if argv is None else argv)
    except UsageError as e:
        msg = str(e)
        print(msg if msg.startswith("public-text-guard:") else f"public-text-guard: error: {msg}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
