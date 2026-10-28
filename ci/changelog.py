#!/usr/bin/env python3
"""Generate CHANGELOG.md from the squash-merged PRs on first-parent history.

Python 3 stdlib only, no network, no gh. Reads `git log --first-parent` of a ref (default origin/main), keeps the
commits whose subject ends with `(#N)`, drops the PR number and groups the rest into Keep a Changelog sections by
keyword. Subjects that ci/scrub_scan.py would flag are dropped; their count is printed to stderr.

  changelog.py                 print the changelog to stdout
  changelog.py --write         write it to CHANGELOG.md
  changelog.py --check         exit 1 if CHANGELOG.md differs from what would be generated
  changelog.py --since SHA     only commits after SHA
Exit codes: 0 ok, 1 --check differs, 2 usage or git error.
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

## [0.1.0] - Initial public release

First public release of the factory, published from a fresh repository.
"""


class ChangelogError(Exception):
    pass


def merged_subjects(ref, since=None, repo="."):
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
    out = [HEADER]
    for name in SECTIONS:
        if groups[name]:
            out.append(f"### {name}\n\n" + "\n".join(groups[name]) + "\n")
    return "\n".join(out), dropped


def build_parser():
    p = argparse.ArgumentParser(description="Generate CHANGELOG.md from first-parent squash-merge commits.")
    p.add_argument("--ref", default="origin/main", help="ref to read (default origin/main)")
    p.add_argument("--since", metavar="SHA", help="only commits after SHA")
    p.add_argument("--repo", default=".", help="git repository (default .)")
    p.add_argument("--file", default="CHANGELOG.md", help="changelog file for --write/--check (default CHANGELOG.md)")
    mode = p.add_mutually_exclusive_group()
    mode.add_argument("--write", action="store_true", help="write the changelog to --file")
    mode.add_argument("--check", action="store_true", help="exit 1 if --file differs from the generated text")
    return p


def run(argv):
    args = build_parser().parse_args(argv)
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
