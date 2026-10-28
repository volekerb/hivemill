#!/usr/bin/env python3
"""Demo scrub check (G-020 SC5/SC6): demo sources and sampled frame text must be clean.

Runs ci/scrub_scan.py --path as a subprocess (it inherits SCRUB_TERMS_FILE) over docs/demo/ (except out/ and
binary files), ci/demo_*.py and, when given, every *.txt under a recorder text dir. Also checks statically that the
demo pulls in nothing from outside: no http(s) URL other than http://127.0.0.1, no @font-face with url(, no
<script src=> pointing outside docs/demo. Exit codes: 0 clean, 1 hits, 2 usage error.
"""
import argparse
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
BINARY_EXT = {".png", ".webp", ".gif", ".mp4", ".webm", ".woff", ".woff2", ".jpg", ".jpeg", ".ico"}
LICENSE_RE = re.compile(r"OFL.*\.txt", re.I)  # bundled font licences name their upstream URLs; exempt from SC6 only
URL_RE = re.compile(r"https?://[^\s\"'<>)]*", re.I)
# 127.0.0.1 with an optional port and nothing that continues the authority: a userinfo form (loopback, then an at sign and another host) is external
LOCAL_RE = re.compile(r"http://127\.0\.0\.1(?::\d+)?(?![\w.:@%-])", re.I)
# protocol-relative reference (//host/x) in an attribute or CSS: it loads from whatever origin serves the page
PROTO_REL_RE = re.compile(r"""(?:\b(?:src|href|action|poster)\s*=\s*|url\(\s*|@import\s+)["']?//[\w.-]""", re.I)
FONT_FACE_RE = re.compile(r"@font-face\s*\{[^}]*\}", re.I | re.S)
CSS_URL_RE = re.compile(r"url\(\s*[\"']?([^\"')\s]+)", re.I)
SCRIPT_SRC_RE = re.compile(r"<script\b[^>]*\bsrc\s*=\s*[\"']?([^\s\"'>]+)", re.I)


def has_nul(path):
    """True when the head of a file has a NUL byte. scrub_scan.py skips such a file as binary, so for a source file
    (not a known binary extension) that is a hole in the scrub: scan() reports it instead of skipping it silently."""
    try:
        return b"\0" in path.read_bytes()[:8000]
    except OSError:
        return False


def demo_sources(root):
    demo, out = root / "docs" / "demo", root / "docs" / "demo" / "out"
    files = [p for p in sorted(demo.rglob("*")) if p.is_file() and out not in p.parents]
    files += sorted((root / "ci").glob("demo_*.py"))
    return [p for p in files if p.suffix.lower() not in BINARY_EXT]


def inside_demo(path, ref, root):
    """True when a relative reference from `path` resolves to a file location under docs/demo."""
    if re.match(r"([a-z][a-z0-9+.-]*:|/)", ref, re.I):
        return False
    target = (path.parent / ref.split("?")[0].split("#")[0]).resolve()
    return (root / "docs" / "demo").resolve() in target.parents


def static_hits(path, root):
    """SC6 problems in one docs/demo file, as 'file: problem' lines."""
    text = path.read_text(encoding="utf-8", errors="replace")
    name = path.relative_to(root).as_posix()
    hits = []
    for m in URL_RE.finditer(text):
        if not LOCAL_RE.match(m.group(0)):
            hits.append(f"{name}: external URL {m.group(0)[:60]!r}. Serve it from docs/demo or drop it.")
    for m in PROTO_REL_RE.finditer(text):
        hits.append(f"{name}: protocol-relative reference {m.group(0)[-24:]!r} loads from outside. Use a file under docs/demo.")
    for block in FONT_FACE_RE.finditer(text):
        for m in CSS_URL_RE.finditer(block.group(0)):
            if not inside_demo(path, m.group(1), root):
                hits.append(f"{name}: @font-face url({m.group(1)[:60]}) is not a file under docs/demo. Bundle the font there.")
    for m in SCRIPT_SRC_RE.finditer(text):
        src = m.group(1)
        if not inside_demo(path, src, root):
            hits.append(f"{name}: <script src={src[:60]!r}> points outside docs/demo. Bundle it in docs/demo.")
    return hits


def scan(text_dir=None, root=None):
    """Scrub and offline-check the demo under `root` (default: this checkout; tests pass a temp tree). Returns the
    exit code."""
    root = Path(root) if root else ROOT
    files = demo_sources(root)
    rc = 0
    for p in [p for p in files if has_nul(p)]:
        print(f"{p.relative_to(root).as_posix()}: has a NUL byte, so the scrub cannot read it as text. "
              "Remove the byte, or give the file a binary extension if it is binary.", file=sys.stderr)
        files.remove(p)
        rc = 1
    if text_dir is not None:
        tdir = Path(text_dir)
        if not tdir.is_dir():
            print(f"demo_scrub: text dir {str(tdir)!r} is not a directory. Pass the recorder's text output dir.", file=sys.stderr)
            return 2
        files += sorted(p for p in tdir.rglob("*.txt") if p.is_file())
    scanner = ROOT / "ci" / "scrub_scan.py"
    if files:
        r = subprocess.run([sys.executable, str(scanner), "--path", *map(str, files)], cwd=root)  # inherits SCRUB_TERMS_FILE
        if r.returncode == 2:
            return 2
        rc = 1 if r.returncode else rc
    demo, out = root / "docs" / "demo", root / "docs" / "demo" / "out"
    for p in files:
        if demo in p.parents and out not in p.parents and not LICENSE_RE.fullmatch(p.name):
            for line in static_hits(p, root):
                print(line, file=sys.stderr)
                rc = 1
    return rc


def main(argv=None):
    ap = argparse.ArgumentParser(description="Scrub + offline check of the demo sources and frame text dumps.")
    ap.add_argument("--text-dir", help="directory of recorder text dumps (*.txt) to scrub too")
    args = ap.parse_args(argv)
    return scan(text_dir=args.text_dir)


if __name__ == "__main__":
    sys.exit(main())
