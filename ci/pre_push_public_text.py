#!/usr/bin/env python3
"""git pre-push hook body: refuses a push to a public_text_repos remote when ci/public_text_guard.py finds private data
in the pushed range (ref names, commit messages, identities, paths, added lines of every new commit).

Installed by `factory spawn` as <git-common-dir>/hooks/pre-push (a two-line shell wrapper that runs this file with the
factory home baked in). Python 3 stdlib. Fails closed: unreadable config, unparsable remote while a public repo is
configured, missing terms file or any check error refuses the push. Owner terms are never printed.
Usage (by git): pre_push_public_text.py --home DIR REMOTE URL < "<local ref> <local sha> <remote ref> <remote sha>" lines.
See docs/public-text-guard.md.
"""
import importlib.util
import os
import re
import sqlite3
import subprocess
import sys
from pathlib import Path

GUARD = Path(__file__).resolve().parent / "public_text_guard.py"
ZERO = re.compile(r"^0+$")


def refuse(msg):
    print(f"pre-push: refused ({msg})", file=sys.stderr)
    return 1


def repo_slug(url):
    """OWNER/NAME (lower case) of a remote URL or local path ending OWNER/NAME(.git); None when it holds no two-part name."""
    u = re.sub(r"[?#].*$", "", (url or "").strip())
    u = re.sub(r"^[A-Za-z][A-Za-z0-9+.-]*://[^/]*/", "", u)
    u = re.sub(r"^(?:[^/@:]*@)?[^/@:]+:", "", u)  # scp-style [user@]host:path
    parts = [x for x in u.rstrip("/").split("/") if x]
    if len(parts) < 2:
        return None
    owner, name = parts[-2], re.sub(r"\.git$", "", parts[-1], flags=re.I)
    if not owner or not name or owner in (".", "..") or name in (".", ".."):
        return None
    return f"{owner}/{name}".lower()


def read_config(home):
    db = Path(home) / "db" / "factory.db"
    con = sqlite3.connect(f"file:{db}?mode=ro", uri=True, timeout=5)
    try:
        return dict(con.execute("SELECT key, value FROM config WHERE key IN ('public_text_repos','scrub_terms_file')"))
    finally:
        con.close()


def git(*args):
    r = subprocess.run(["git", *args], capture_output=True, text=True, timeout=30)
    return r.stdout.strip() if r.returncode == 0 else ""


def has_commit(sha):
    return subprocess.run(["git", "cat-file", "-e", sha + "^{commit}"], capture_output=True, timeout=30).returncode == 0


def load_guard():
    spec = importlib.util.spec_from_file_location("public_text_guard_prepush", GUARD)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def main(argv, stdin):
    if len(argv) < 4 or argv[0] != "--home":
        return refuse("usage: pre_push_public_text.py --home DIR REMOTE URL")
    home, url = argv[1], argv[3]
    try:
        cfg = read_config(home)
    except sqlite3.Error as e:
        return refuse(f"factory config unreadable ({type(e).__name__}); retry, or tell your lead")
    public = {s for s in (repo_slug(x) for x in (cfg.get("public_text_repos") or "").split(",")) if s}
    if not public:
        return 0
    slug = repo_slug(url)
    if slug is not None and slug not in public:
        return 0
    lines = [ln.split() for ln in stdin.read().splitlines() if ln.strip()]
    try:
        mod = load_guard()
    except (OSError, ImportError, SyntaxError) as e:
        return refuse(f"public-text-guard cannot be loaded ({type(e).__name__}); fix ci/public_text_guard.py")
    try:
        terms = mod.load_terms_or_fail(os.environ, cfg.get("scrub_terms_file") or None)
        hits = []
        for ref, sha, remote_ref, remote_sha in (ln[:4] for ln in lines if len(ln) >= 4):
            if ZERO.match(sha):
                continue  # a delete publishes no commits
            hits += mod.check_ref(re.sub(r"^refs/(?:heads|tags)/", "", remote_ref), terms)
            base = remote_sha if not ZERO.match(remote_sha) and has_commit(remote_sha) else ""
            if not base:
                base = git("merge-base", sha, "refs/remotes/origin/main")
            if not base:
                return refuse("no base to check the push against: run git fetch origin so origin/main exists")
            hits += mod.check_push(".", base, sha, terms)
    except mod.UsageError as e:
        return refuse(str(e))
    if hits:
        ids = sorted({h[0] for h in hits})
        return refuse(f"public text: {len(hits)} hits: {', '.join(ids)}; reword or drop the commits (git rebase -i), then push again")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:], sys.stdin))
