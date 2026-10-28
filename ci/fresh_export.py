#!/usr/bin/env python3
"""Dry-run of the fresh public repo: export a ref as ONE commit in a new directory and scan it.

Python 3 stdlib only. `git archive <ref>` (tracked files only) is extracted into --out, committed as the only commit of
a new `main` branch under --author with a fixed UTC date, checked structurally (one commit, only refs/heads/main,
commit headers exactly tree/author/committer, only modes 100644/100755/120000, no control characters or invalid UTF-8
in paths), and then gated by `ci/scrub_scan.py --history <out> --history-allowlist <file>`: blobs as committed (symlink
targets, binary files), path names, author, committer, message, ref names and tags. It never adds a remote, never
pushes and never touches the source repository. Exit 0 = gate clean, 1 = hits, 2 = usage error or refusal (--out left
empty). See docs/PUBLISHING.md. SCRUB_TERMS_FILE is passed through to the scanner via the environment.
"""
import argparse
import datetime
import importlib.util
import os
import pwd
import re
import shutil
import stat
import subprocess
import sys
import tarfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
SCAN = HERE / "scrub_scan.py"
DEFAULT_HISTORY_ALLOWLIST = HERE / "scrub_history_allowlist.txt"
DATE_RE = re.compile(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\+0000")
MODES_OK = {"100644", "100755", "120000"}
CONTROL_RE = re.compile(r"[\x00-\x1f\x7f]")
AUTHOR_RE = re.compile(r"\s*([^<>]+?)\s*<([^<>\s]+@[^<>\s]+)>\s*")
MESSAGE = "Initial public release"
# Generated state that must never be in an export; tracked files cannot contain it, so this is an assertion.
FORBIDDEN_TOP = {"exports", "state", "run", "worktrees", "db"}
FORBIDDEN_NAMES = {"factory.db"}
# Slug tokens of the tree, substituted by --public-repo (built at runtime so this file is not rewritten by its own export).
TOKEN_REPO = "__PUBLIC_" + "REPO__"
TOKEN_OWNER = "__PUBLIC_" + "OWNER__"
OVERLAY = "ci/public-overlay"
OWNER_RE = re.compile(r"[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})")
NAME_RE = re.compile(r"[A-Za-z0-9._-]{1,100}")
ORIGIN_RE = re.compile(r"(?i:https?://(?:[^/@\s]*@)?github\.com/|git@github\.com:|ssh://git@github\.com/)([^/\s]+)/([^/\s]+?)(?i:\.git)?/?")
FILTER = {"filter": "data"} if hasattr(tarfile, "data_filter") else {}


class Refused(Exception):
    pass


def live_homes():
    """The live factory home(s), derived from the passwd entry (never $HOME): <pw_dir>/factory, plus the one extra
    home recorded in <pw_dir>/.config/factory/live_home when that file is a safe single absolute path (as bin/factory)."""
    uid = os.getuid()
    base = pwd.getpwuid(uid).pw_dir
    homes = {os.path.realpath(os.path.join(base, "factory"))}
    cfgdir = os.path.join(base, ".config", "factory")
    path = os.path.join(cfgdir, "live_home")
    try:
        st, pst = os.lstat(path), os.lstat(cfgdir)
        if (stat.S_ISREG(st.st_mode) and stat.S_ISDIR(pst.st_mode) and st.st_uid == uid
                and not st.st_mode & (stat.S_IWGRP | stat.S_IWOTH)):
            lines = Path(path).read_text().splitlines()
            if len(lines) == 1 and os.path.isabs(lines[0]) and "\0" not in lines[0]:
                homes.add(os.path.realpath(lines[0]))
    except (OSError, UnicodeDecodeError):
        pass  # no file (the usual case) or unreadable: only the default is live
    return homes


def check_out(out):
    """Resolved --out; raises Refused when it is inside the live home or exists and is not empty."""
    target = Path(out).expanduser().resolve()
    for live in live_homes():
        if target == Path(live) or Path(live) in target.parents:
            raise Refused(f"{target} is inside the live factory home {live}. Choose a directory outside it, e.g. a new dir under /tmp.")
    if target.exists():
        if not target.is_dir():
            raise Refused(f"{target} exists and is not a directory. Choose a new path.")
        if any(target.iterdir()):
            raise Refused(f"{target} exists and is not empty. Choose a new or empty directory.")
    return target


def parse_author(text):
    m = AUTHOR_RE.fullmatch(text)
    if not m:
        raise Refused(f'--author {text!r} is not of the form "Name <email>".')
    return m.group(1), m.group(2)


def load_scanner():
    """ci/scrub_scan.py as a module (its hardened git env, patterns and owner terms), without editing or shelling out to it."""
    spec = importlib.util.spec_from_file_location("scrub_scan_mod", SCAN)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


SCANNER = load_scanner()


def clean_env(extra=None):
    """The scanner's hardened git environment (no user/system config, no replace objects, no GIT_DIR and friends) plus `extra`."""
    env = SCANNER.git_env()
    env.update(extra or {})
    return env


def git(args, cwd, env=None, check=True):
    p = subprocess.run(["git", *args], cwd=str(cwd), capture_output=True, env=clean_env(env))
    out, err = p.stdout.decode("utf-8", "surrogateescape"), p.stderr.decode("utf-8", "replace")
    if check and p.returncode != 0:
        raise RuntimeError(f"git {' '.join(args)} failed in {cwd}: {err.strip()[-500:]}. Check --repo and --ref.")
    return subprocess.CompletedProcess(p.args, p.returncode, out, err)


def extract(repo, ref, target):
    """git archive REF | tar -x into target, member by member (a member leaving target is refused)."""
    git(["rev-parse", "--verify", "--quiet", ref + "^{commit}"], repo)
    target.mkdir(parents=True, exist_ok=True)
    proc = subprocess.Popen(["git", "archive", "--format=tar", ref], cwd=str(repo), stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            env=clean_env())  # user config (core.autocrlf, ...) must not change the archived bytes
    root = str(target)
    with tarfile.open(fileobj=proc.stdout, mode="r|") as tar:
        for m in tar:
            dest = os.path.realpath(os.path.join(root, m.name))
            if dest != root and not dest.startswith(root + os.sep):
                raise RuntimeError(f"archive member {m.name!r} escapes {root}")
            try:
                tar.extract(m, root, **FILTER)
            except getattr(tarfile, "FilterError", ()):  # (no filters on an old Python: nothing to catch) # its text can hold a symlink target: say only the kind of member and a generic reason
                kind = "symlink" if m.issym() else "hardlink" if m.islnk() else "directory" if m.isdir() else "file"
                raise RuntimeError(f"archive {kind} member refused by the tar safety filter (link target, path or mode not allowed): "
                                   f"remove or fix that entry in {ref} and rerun. Find it with: git ls-tree -r {ref}") from None
    proc.stdout.close()
    err = proc.stderr.read().decode("utf-8", "replace").strip()
    proc.stderr.close()
    if proc.wait() != 0:
        raise RuntimeError(f"git archive {ref} failed in {repo}: {err[-500:]}. Check --repo and --ref.")


def assert_no_generated_state(target):
    bad = [n for n in os.listdir(target) if n in FORBIDDEN_TOP]
    for dirpath, _dirs, files in os.walk(target):
        if ".git" in Path(dirpath).relative_to(target).parts:
            continue
        bad += [os.path.join(dirpath, f) for f in files if f in FORBIDDEN_NAMES]
    if bad:
        raise RuntimeError(f"export contains generated state {bad}: remove it from the tracked tree before publishing.")


def check_entries(repo, ref, what):
    """Refuse gitlinks and other unexpected modes, and paths with control characters or invalid UTF-8, in `ref`'s tree.
    Returns the number of blobs. Names are not echoed: they may themselves be what the scan is looking for."""
    bad_mode = bad_name = count = 0
    for rec in filter(None, git(["ls-tree", "-r", "-z", ref], repo).stdout.split("\0")):
        meta, path = rec.split("\t", 1)
        count += 1
        bad_mode += meta.split()[0] not in MODES_OK
        try:
            path.encode("utf-8")
        except UnicodeEncodeError:  # surrogateescape'd bytes that are not UTF-8
            bad_name += 1
        else:
            bad_name += bool(CONTROL_RE.search(path))
    if bad_mode:
        raise RuntimeError(f"{what} holds {bad_mode} entries with a mode other than 100644, 100755 or 120000 (gitlink or "
                           f"submodule): remove them from the source tree and rerun. Find them with: git ls-tree -r {ref}")
    if bad_name:
        raise RuntimeError(f"{what} holds {bad_name} paths with control characters or invalid UTF-8: rename them in the source "
                           f"tree and rerun. Find them with: git ls-tree -r -z {ref} | grep -zaP '[\\x00-\\x1f\\x7f\\x80-\\xff]'")
    return count


def assert_structure(target):
    """The commit is the whole publication: one commit, one ref, a bare commit object, and the identity that was asked for."""
    count = git(["rev-list", "--all", "--count"], target).stdout.strip()
    if count != "1":
        raise RuntimeError(f"export has {count} commits, expected 1: rerun into a new --out.")
    refs = git(["for-each-ref", "--format=%(refname)"], target).stdout.split()
    if refs != ["refs/heads/main"]:
        raise RuntimeError(f"export has refs {refs}, expected only refs/heads/main: rerun into a new --out.")
    raw = git(["cat-file", "commit", "HEAD"], target).stdout.split("\n\n", 1)[0]
    headers = [ln.split(" ", 1)[0] for ln in raw.split("\n") if not ln.startswith(" ")]
    if headers != ["tree", "author", "committer"]:
        raise RuntimeError(f"commit headers are {headers}, expected tree, author, committer only: rerun into a new --out.")
    check_entries(target, "HEAD", "the export commit")


def count_binary_blobs(target):
    """NUL-containing blobs of HEAD, for the summary (the gate scans them too, with --text)."""
    ids = [rec.split()[2] for rec in git(["ls-tree", "-r", "-z", "HEAD"], target).stdout.split("\0") if rec]
    if not ids:
        return 0
    proc = subprocess.run(["git", "cat-file", "--batch"], cwd=str(target), env=clean_env(), capture_output=True,
                          input="".join(i + "\n" for i in ids).encode())
    if proc.returncode != 0:
        raise RuntimeError(f"git cat-file failed in {target}: {proc.stderr.decode('utf-8', 'replace').strip()[-500:]}")
    data, pos, binary = proc.stdout, 0, 0
    for _ in ids:
        nl = data.index(b"\n", pos)
        size = int(data[pos:nl].split()[2])
        binary += b"\0" in data[nl + 1:nl + 1 + size]
        pos = nl + 1 + size + 1
    return binary


def parse_public_repo(text):
    owner, sep, name = text.partition("/")
    if not (sep and OWNER_RE.fullmatch(owner) and NAME_RE.fullmatch(name) and name not in (".", "..")):
        raise Refused(f"--public-repo {text!r} is not OWNER/NAME (OWNER: letters, digits, '-', at most 39; NAME: letters, digits, "
                      "'.', '_', '-', at most 100, not '.' or '..'). Example: --public-repo example-org/factory.")
    return owner, name


def private_slug(repo):
    """OWNER/NAME of the source repo's GitHub origin, or None (a note is printed) when there is no origin to compare with.

    An origin that is not a GitHub URL is refused. The URL (it may carry credentials) is never stored beyond this call
    and never printed: only the derived OWNER/NAME leaves this function."""
    p = git(["remote", "get-url", "origin"], repo, check=False)
    if p.returncode != 0:
        print("fresh_export: note: the source repository has no GitHub origin; the private-slug comparison is skipped.")
        return None
    m = ORIGIN_RE.fullmatch(p.stdout.strip())
    if not m:
        raise Refused("origin is not a GitHub URL the export can check; set a plain https://github.com/OWNER/NAME origin or remove it.")
    owner, name = m.group(1), m.group(2)
    if not (OWNER_RE.fullmatch(owner) and NAME_RE.fullmatch(name) and name not in (".", "..")):
        raise Refused("origin is not a GitHub URL the export can check; set a plain https://github.com/OWNER/NAME origin or remove it.")
    return f"{owner}/{name}"


def tree_files(target):
    """Regular (non-symlink) files of the extracted export, outside .git."""
    for dirpath, dirs, files in os.walk(target):
        if dirpath == str(target):
            dirs[:] = [d for d in dirs if d != ".git"]
        for f in files:
            path = Path(dirpath, f)
            if not path.is_symlink():
                yield path


def apply_overlay(target):
    """Move every file of ci/public-overlay/ to the same relative path at the export root, then drop the overlay dir."""
    base = target / OVERLAY
    if not base.is_dir():
        return
    for path in sorted(tree_files(base)):
        dest = target / path.relative_to(base)
        if dest.exists() or dest.is_symlink():
            raise Refused(f"overlay file {path.relative_to(target)} would overwrite {dest.relative_to(target)}, which is already in "
                          f"the tree. Remove one of them in the source.")
        dest.parent.mkdir(parents=True, exist_ok=True)
        os.replace(path, dest)
    shutil.rmtree(base)


def substitute(target, public):
    """Replace the slug tokens in every UTF-8 text file. Returns {relative path: occurrences}; binary files are left alone."""
    counts = {}
    for path in sorted(tree_files(target)):
        data = path.read_bytes()
        if b"\0" in data:
            continue
        try:
            text = data.decode("utf-8")
        except UnicodeDecodeError:
            continue
        n = text.count(TOKEN_REPO) + text.count(TOKEN_OWNER)
        if n:
            if public is None:
                raise Refused(f"The tree names the public repository ({TOKEN_REPO}); pass --public-repo OWNER/NAME.")
            owner = public.split("/")[0]
            path.write_bytes(text.replace(TOKEN_REPO, public).replace(TOKEN_OWNER, owner).encode("utf-8"))
            counts[str(path.relative_to(target))] = n
    return counts


def private_slug_re(private):
    """Private slug on a slug boundary, case-insensitive: not inside a longer name (acme/private-public). Any '.'
    after the slug is a boundary (fail-safe), so acme/private.git, .wiki, .zip and a sentence-ending dot are refused.
    The '/' may be percent-encoded (%2F or %2f, as in an API URL); a preceding %2F also counts as a boundary."""
    slug = rb"(?:/|%2F)".join(re.escape(part.encode()) for part in private.split("/"))
    return re.compile(rb"(?:(?<![A-Za-z0-9-])|(?<=%2F))" + slug + rb"(?![A-Za-z0-9_-])", re.IGNORECASE)


def check_substituted(target, private):
    """After substitution: no token anywhere, and no text file naming the private slug (paths and a count only)."""
    left, named = [], []
    named_re = private_slug_re(private) if private else None
    for path in sorted(tree_files(target)):
        data = path.read_bytes()
        rel = str(path.relative_to(target))
        if TOKEN_REPO.encode() in data or TOKEN_OWNER.encode() in data:
            left.append(rel)
        if named_re and b"\0" not in data and named_re.search(data):
            named.append(rel)
    if left:
        raise Refused(f"{len(left)} files still hold a public slug token after substitution: {', '.join(left)}. Fix the token spelling.")
    if named:
        raise Refused(f"{len(named)} files name the private repository: {', '.join(named)}. Replace the private slug with the "
                      f"{TOKEN_REPO} token (or remove it) in the source and rerun.")


def gate(target, allowlist):
    """The only pass/fail: scrub_scan --history over the export repo. Returns its exit code (0 clean, 1 hits)."""
    p = subprocess.run([sys.executable, str(SCAN), "--history", str(target), "--history-allowlist", str(allowlist)],
                       capture_output=True, text=True, env=clean_env())
    sys.stdout.write(p.stdout)
    if p.stderr:
        sys.stderr.write(p.stderr)
    if p.returncode not in (0, 1):
        raise RuntimeError(f"history scan failed (exit {p.returncode}); fix the scanner error above and rerun.")
    return p.returncode


def masked(text):
    """Text for the summary line with every scanner hit in it masked (an --author may itself be a leak)."""
    files = [os.environ["SCRUB_TERMS_FILE"]] if os.environ.get("SCRUB_TERMS_FILE") else []
    try:
        return SCANNER.Scanner(SCANNER.load_terms(files)).hide_all(text)
    except SCANNER.UsageError as e:
        raise RuntimeError(str(e))


def date_arg(text):
    if not DATE_RE.fullmatch(text):
        raise argparse.ArgumentTypeError(f"{text!r} is not YYYY-MM-DDTHH:MM:SS+0000 (the timezone is fixed to UTC on purpose)")
    return text


def build_parser():
    p = argparse.ArgumentParser(
        prog="fresh_export.py", allow_abbrev=False,
        description="Export a ref as a single-commit repo in a new dir and scan it. Never adds a remote, never pushes.")
    p.add_argument("--out", required=True, metavar="DIR", help="new or empty directory for the export (not inside the live home)")
    p.add_argument("--author", required=True, metavar='"Name <email>"', help="author and committer of the single commit")
    p.add_argument("--ref", default="origin/main", help="ref to export (default origin/main)")
    p.add_argument("--date", type=date_arg, metavar="YYYY-MM-DDT00:00:00+0000",
                   help="author and committer date (default: today, UTC, at 00:00:00+0000)")
    p.add_argument("--history-allowlist", metavar="FILE", default=str(DEFAULT_HISTORY_ALLOWLIST),
                   help="reviewed fixture lines for the gate (default ci/scrub_history_allowlist.txt)")
    p.add_argument("--public-repo", metavar="OWNER/NAME",
                   help="slug of the new public repository, substituted for the slug tokens of the tree (required when the tree has tokens)")
    p.add_argument("--repo", default=".", metavar="DIR", help="source repository (default .)")
    return p


def run(argv):
    args = build_parser().parse_args(argv)
    name, email = parse_author(args.author)
    if args.ref.startswith("-"):
        raise Refused(f"--ref {args.ref!r} starts with '-' and would be read as a git option. Pass a branch, tag or commit.")
    public = args.public_repo
    if public is not None:
        parse_public_repo(public)
    target = check_out(args.out)
    repo = Path(args.repo).resolve()
    try:
        return export(args, repo, target, name, email)
    except BaseException:  # any failure, an interrupt included, leaves --out empty
        for child in target.iterdir() if target.is_dir() else ():  # --out was new or empty: leave it empty again
            shutil.rmtree(child) if child.is_dir() and not child.is_symlink() else child.unlink()
        raise


def export(args, repo, target, name, email):
    date = args.date or datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT00:00:00+0000")
    private = private_slug(repo)
    if private and args.public_repo and args.public_repo.lower() == private.lower():
        raise Refused("--public-repo names the source repository; the public repository must be a new one.")
    check_entries(repo, args.ref + "^{tree}", f"{args.ref}")
    extract(repo, args.ref, target)
    assert_no_generated_state(target)
    apply_overlay(target)
    substituted = substitute(target, args.public_repo)
    check_substituted(target, private)
    env = dict(GIT_AUTHOR_NAME=name, GIT_AUTHOR_EMAIL=email, GIT_COMMITTER_NAME=name, GIT_COMMITTER_EMAIL=email,
               GIT_AUTHOR_DATE=date, GIT_COMMITTER_DATE=date)
    git(["init", "-q", "-b", "main"], target)
    git(["add", "-A"], target)
    git(["-c", "commit.gpgsign=false", "commit", "-q", "--no-verify", "-m", MESSAGE], target, env=env)
    assert_structure(target)
    if args.public_repo:
        print(f"fresh_export: public repo {args.public_repo}; substituted {sum(substituted.values())} occurrences in {len(substituted)} files")
        for rel, n in substituted.items():
            print(f"  {rel}: {n}")
    files = len(git(["ls-tree", "-r", "-z", "--name-only", "HEAD"], target).stdout.split("\0")) - 1
    print(f"== gate: scrub_scan --history {target} (blobs as committed, path names, author, committer, message, refs, tags)")
    rc = gate(target, args.history_allowlist)
    print(f"fresh_export: ref={args.ref} out={target} files={files} binary_blobs={count_binary_blobs(target)} commits=1 "
          f"date={date} author={masked(f'{name} <{email}>')}")
    print(f"fresh_export: history gate {'clean' if rc == 0 else 'HITS'}. Publish only with `git push <new-remote> main` from the "
          "export directory; never tar or copy it.")
    if rc:
        print("fresh_export: FAIL - do not publish this export. Fix the hits in the source, then rerun into a new --out.")
        return 1
    print("fresh_export: OK - 0 hits. Nothing was pushed and no remote was added.")
    return 0


def main(argv=None):
    try:
        return run(sys.argv[1:] if argv is None else argv)
    except Refused as e:
        print(f"fresh_export: refused: {e}", file=sys.stderr)
        return 2
    except (RuntimeError, OSError, tarfile.TarError, ValueError, IndexError) as e:  # never a traceback with exit 1 (= hits)
        print(f"fresh_export: error: {e}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
