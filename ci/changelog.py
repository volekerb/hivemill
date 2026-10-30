#!/usr/bin/env python3
"""Generate CHANGELOG.md from the squash-merged PRs on first-parent history.

Python 3 stdlib only, no network, no gh. Reads `git log --first-parent` of a ref (default origin/main), keeps the
commits whose subject ends with `(#N)`, drops the PR number and groups the rest into Keep a Changelog sections by
keyword. Subjects that ci/scrub_scan.py would flag are dropped; their count is printed to stderr.

  changelog.py                 print the changelog to stdout
  changelog.py --write         write it to CHANGELOG.md
  changelog.py --check         exit 1 if CHANGELOG.md differs from what would be generated
  changelog.py --since SHA     only commits after SHA (default: the previous release commit, SINCE)
  changelog.py --release-notes VERSION  print the curated ci/release-notes/VERSION.md
  changelog.py --prerelease VERSION     print "true" for 0.x or -suffixed versions, else "false"
  changelog.py --check-version VERSION  exit 1 unless the VERSION file equals VERSION, ci/release-notes/VERSION.md exists
                                        with a "### Upgrade" line and CHANGELOG.md has "## [VERSION]"
Exit codes: 0 ok, 1 --check or --check-version fails, 2 usage, git error or missing release notes.
"""
import argparse
import re
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import scrub_scan  # noqa: E402

PR_SUFFIX = re.compile(r"\s*\(#\d+\)$")
DATE = re.compile(r"\s*\(?\b\d{4}-\d{2}-\d{2}\b\)?")  # entries carry no dates
NOTES_DIR = Path(__file__).resolve().parent / "release-notes"
VERSION_HEADING = re.compile(r"^## \[([^\]]+)\]", re.M)
SECTIONS = ("Added", "Changed", "Fixed", "Security")
# First match wins, in this order: fix/bug, then security words, then add/new, else Changed.
RULES = (
    ("Fixed", re.compile(r"\b(fix|bug)", re.I)),
    ("Security", re.compile(r"\b(guard|deny|harden|secret|scrub)", re.I)),
    ("Added", re.compile(r"\b(add|new)\b", re.I)),
)
HEADER = """# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

v0.1.0 summarises the changes merged during private development.

## [0.3.0] - 2026-10-30
"""
# The previous release (tag v0.2.0); the generated list covers the commits after it. Update it with HEADER at each release.
SINCE = "3b1d99516"
# Older releases sit below the generated lists of the newest one; each carries only its curated notes.
OLDER = """
## [0.2.0] - 2026-10-30

## [0.1.0] - Initial public release

First public release of the factory, published from a fresh repository.
"""


class ChangelogError(Exception):
    pass


def release_notes_path(version):
    return NOTES_DIR / f"{version}.md"


def release_notes(version):
    path = release_notes_path(version)
    try:
        return path.read_text(encoding="utf-8").strip() + "\n"
    except OSError as e:
        raise ChangelogError(f"cannot read ci/release-notes/{version}.md: {e.strerror}. Write the curated notes for {version} first.")


def is_prerelease(version):
    return "-" in version or version.split(".", 1)[0] == "0"


def with_curated_notes(header):
    """Insert ci/release-notes/<version>.md under each `## [<version>]` heading of the header."""
    out, pos = [], 0
    for m in VERSION_HEADING.finditer(header):
        if not release_notes_path(m.group(1)).is_file():
            continue
        # keep the heading and its intro paragraph, then the curated block
        nxt = header.find("\n## ", m.end())
        stop = len(header) if nxt < 0 else nxt
        out.append(header[pos:stop].rstrip("\n") + "\n\n" + release_notes(m.group(1)))
        pos = stop
    out.append(header[pos:])
    return "".join(out)


def known_commit(sha, repo):
    return subprocess.run(["git", "-C", repo, "cat-file", "-e", f"{sha}^{{commit}}"], capture_output=True).returncode == 0


def merged_subjects(ref, since=None, repo="."):
    if since is None and known_commit(SINCE, repo):  # a repo without the release commit (a test repo) lists its whole history
        since = SINCE
    rng = f"{since}..{ref}" if since else ref
    proc = subprocess.run(["git", "-C", repo, "log", "--first-parent", "--format=%H%x09%cs%x09%s", rng],
                          capture_output=True, text=True)
    if proc.returncode != 0:
        raise ChangelogError(f"git log {rng} failed: {proc.stderr.strip()}. Fetch the ref or pass --ref/--since a valid one.")
    subjects = []
    for line in proc.stdout.splitlines():
        parts = line.split("\t", 2)
        if len(parts) == 3 and PR_SUFFIX.search(parts[2]):
            subjects.append(DATE.sub("", PR_SUFFIX.sub("", parts[2])).strip())
    return subjects


def section_of(subject):
    for name, rx in RULES:
        if rx.search(subject):
            return name
    return "Changed"


def render(subjects):
    """Return (markdown, number of subjects dropped by the scrub patterns)."""
    scanner = scrub_scan.Scanner([])
    groups = {s: [] for s in SECTIONS}
    dropped = 0
    for subj in subjects:
        if any(True for _ in scanner.scan_line(subj)):
            dropped += 1
            continue
        groups[section_of(subj)].append(f"- {subj}")
    out = [with_curated_notes(HEADER)]
    out.append("### All merged changes since v0.2.0\n")
    for name in SECTIONS:
        if groups[name]:
            out.append(f"#### {name}\n\n" + "\n".join(groups[name]) + "\n")
    out.append(with_curated_notes(OLDER).lstrip("\n"))
    return "\n".join(out), dropped


def version_problems(version, root):
    """One message per failed release check of VERSION in the repo at root (empty list: all pass)."""
    problems = []
    try:
        have = (root / "VERSION").read_text(encoding="utf-8").strip()
    except (OSError, UnicodeDecodeError):
        have = None
    if have != version:
        problems.append(f"VERSION file is {have!r}, not {version!r}. Set it to {version} in VERSION.")
    notes = root / "ci" / "release-notes" / f"{version}.md"
    try:
        text = notes.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError):
        problems.append(f"ci/release-notes/{version}.md is missing. Write the curated notes.")
    else:
        if not re.search(r"^### Upgrade\s*$", text, re.M):
            problems.append(f'ci/release-notes/{version}.md has no "### Upgrade" line. Add the upgrade steps.')
    try:
        log = (root / "CHANGELOG.md").read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError):
        log = ""
    if not re.search(rf"^## \[{re.escape(version)}\]", log, re.M):
        problems.append(f'CHANGELOG.md has no "## [{version}]" heading. Run: python3 ci/changelog.py --write')
    return problems


def build_parser():
    p = argparse.ArgumentParser(description="Generate CHANGELOG.md from first-parent squash-merge commits.")
    p.add_argument("--ref", default="origin/main", help="ref to read (default origin/main)")
    p.add_argument("--since", metavar="SHA", help=f"only commits after SHA (default {SINCE} when the repo has it)")
    p.add_argument("--repo", default=".", help="git repository (default .)")
    p.add_argument("--file", default="CHANGELOG.md", help="changelog file for --write/--check (default CHANGELOG.md)")
    p.add_argument("--release-notes", metavar="VERSION", help="print the curated notes of VERSION and exit")
    p.add_argument("--prerelease", metavar="VERSION", help='print "true" if VERSION is a pre-release (0.x or -suffix), else "false"')
    p.add_argument("--check-version", metavar="VERSION", help="exit 1 unless VERSION file, release notes and CHANGELOG all match VERSION")
    mode = p.add_mutually_exclusive_group()
    mode.add_argument("--write", action="store_true", help="write the changelog to --file")
    mode.add_argument("--check", action="store_true", help="exit 1 if --file differs from the generated text")
    return p


def run(argv):
    args = build_parser().parse_args(argv)
    if args.release_notes is not None:
        sys.stdout.write(release_notes(args.release_notes))
        return 0
    if args.prerelease is not None:
        print("true" if is_prerelease(args.prerelease) else "false")
        return 0
    if args.check_version is not None:
        problems = version_problems(args.check_version, NOTES_DIR.parent.parent)
        for msg in problems:
            print(f"changelog: check-version: {msg}", file=sys.stderr)
        return 1 if problems else 0
    text, dropped = render(merged_subjects(args.ref, args.since, args.repo))
    print(f"changelog: {dropped} subject(s) dropped by scrub patterns", file=sys.stderr)
    if args.check:
        try:
            current = Path(args.file).read_text(encoding="utf-8")
        except OSError as e:
            raise ChangelogError(f"cannot read {args.file}: {e.strerror}. Run changelog.py --write first.")
        if current != text:
            print(f"changelog: {args.file} is out of date. Run: python3 ci/changelog.py --write", file=sys.stderr)
            return 1
        return 0
    if args.write:
        Path(args.file).write_text(text, encoding="utf-8")
    else:
        sys.stdout.write(text)
    return 0


def main(argv=None):
    try:
        return run(sys.argv[1:] if argv is None else argv)
    except ChangelogError as e:
        print(f"changelog: error: {e}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
