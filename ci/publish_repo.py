#!/usr/bin/env python3
"""Apply the public-repository metadata (ci/publish_metadata.json) to the NEW public repository, by name.

Python 3 stdlib only. `python3 ci/publish_repo.py --repo OWNER/NAME [--apply] [--gh PATH]`.

The default is a DRY RUN: it prints every `gh api` call it would make, with its JSON body, and starts no process
(neither gh nor anything else that talks to GitHub). `--apply` runs the calls with `--gh` (default "gh"), in order,
and stops at the first failure. Exit 0 = done, 1 = a call failed (the calls already made are listed), 2 = refusal.

Refused before any call, in both modes: --repo that is not OWNER/NAME; --repo equal to the GitHub origin slug of a live
factory home or to --private-repo; metadata that still names such a slug or that ci/scrub_scan.py flags (SCRUB_TERMS_FILE
is honoured). With --apply the repository must already be public and not a fork: branch protection is for the public
repository only, the private repository's main stays unprotected. See docs/PUBLISHING.md.
"""
import argparse
import importlib.util
import json
import os
import re
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
METADATA = HERE / "publish_metadata.json"
SCAN = HERE / "scrub_scan.py"
SOCIAL = "Upload docs/images/social-preview.png by hand: Settings > General > Social preview."
# Slug tokens of the metadata, built at runtime so this file is not rewritten by its own export (as fresh_export.py does).
TOKEN_REPO = "__PUBLIC_" + "REPO__"
TOKEN_OWNER = "__PUBLIC_" + "OWNER__"
KEYS = ("description", "homepage", "topics", "required_checks")


class Refused(Exception):
    pass


def load_fresh_export():
    """ci/fresh_export.py as a module: its slug regexes and live_homes() are reused, not copied."""
    spec = importlib.util.spec_from_file_location("fresh_export_mod", HERE / "fresh_export.py")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


FX = load_fresh_export()


def parse_slug(text, flag):
    try:
        owner, name = FX.parse_public_repo(text)
    except FX.Refused:
        raise Refused(f"{flag} {text!r} is not OWNER/NAME (letters, digits, '-', '.', '_'; exactly one '/'). "
                      "Example: --repo volekerb/hivemill.") from None
    if name.lower().endswith(".git"):
        name = name[:-4]  # the origin slugs are compared without .git
        if not name or name in (".", ".."):
            raise Refused(f"{flag} {text!r} is not OWNER/NAME. Example: --repo volekerb/hivemill.")
    return owner, name


def live_origin_slugs():
    """Lower-cased OWNER/NAME of the GitHub origin of every live factory home that has one."""
    slugs = set()
    for home in sorted(FX.live_homes()):
        if not os.path.isdir(home):
            continue
        p = FX.git(["remote", "get-url", "origin"], home, check=False)
        if p.returncode != 0:
            continue  # no origin: nothing to compare with
        m = FX.ORIGIN_RE.fullmatch(p.stdout.strip())
        if not m:
            raise Refused("a live factory home has an origin that is not a plain github.com URL, so the private slug cannot be "
                          "derived. Set a plain https://github.com/OWNER/NAME origin, or pass --private-repo OWNER/NAME and fix the origin.")
        slugs.add(f"{m.group(1)}/{m.group(2)}".lower())
    return slugs


def load_metadata(path):
    try:
        meta = json.loads(Path(path).read_text())
    except (OSError, ValueError) as e:
        raise Refused(f"cannot read metadata {path}: {e}. Check --metadata.") from None
    if not isinstance(meta, dict) or any(k not in meta for k in KEYS):
        raise Refused(f"metadata {path} must be an object with the keys {', '.join(KEYS)}.")
    if not (isinstance(meta["description"], str) and isinstance(meta["homepage"], str)
            and all(isinstance(meta[k], list) and meta[k] and all(isinstance(x, str) for x in meta[k]) for k in ("topics", "required_checks"))):
        raise Refused(f"metadata {path}: description and homepage are strings, topics and required_checks non-empty lists of strings.")
    return meta


def render(meta, owner, name):
    text = json.dumps(meta)
    return json.loads(text.replace(TOKEN_REPO, f"{owner}/{name}").replace(TOKEN_OWNER, owner))


def scrub_check(rendered):
    """Run the scanner on a temp copy of the rendered metadata; a hit (or a scanner failure) is a refusal."""
    with tempfile.TemporaryDirectory() as tmp:
        f = Path(tmp) / "publish_metadata.json"
        f.write_text(json.dumps(rendered, indent=2))
        p = subprocess.run([sys.executable, str(SCAN), "--path", str(f)], capture_output=True, text=True, env=dict(os.environ))
    if p.returncode == 1:
        raise Refused("the rendered metadata has scrub-scan hits (masked below); remove them from the metadata:\n"
                      + (p.stdout + p.stderr).strip())
    if p.returncode != 0:
        raise Refused(f"ci/scrub_scan.py failed (exit {p.returncode}): {p.stderr.strip()[-300:]}. Fix the scanner error and rerun.")


def plan(repo, meta):
    """The six calls, in order: (method, path, body or None). The comment above each names the REST page it was checked against."""
    r = f"repos/{repo}"
    return [
        # https://docs.github.com/en/rest/repos/repos#get-a-repository ("private", "fork")
        ("GET", r, None),
        # https://docs.github.com/en/rest/repos/repos#update-a-repository (description, homepage, has_issues, has_discussions, has_wiki, has_projects)
        ("PATCH", r, {"description": meta["description"], "homepage": meta["homepage"], "has_issues": True,
                      "has_discussions": True, "has_wiki": False, "has_projects": False}),
        # https://docs.github.com/en/rest/repos/repos#replace-all-repository-topics (body {"names": [...]}, at most 20)
        ("PUT", f"{r}/topics", {"names": meta["topics"]}),
        # https://docs.github.com/en/rest/repos/repos#enable-vulnerability-alerts (no body, 204)
        ("PUT", f"{r}/vulnerability-alerts", None),
        # https://docs.github.com/en/rest/repos/repos#enable-private-vulnerability-reporting-for-a-repository (no body, 204)
        ("PUT", f"{r}/private-vulnerability-reporting", None),
        # https://docs.github.com/en/rest/branches/branch-protection#update-branch-protection (all four top-level keys are required)
        ("PUT", f"{r}/branches/main/protection",
         {"required_status_checks": {"strict": True, "contexts": meta["required_checks"]}, "enforce_admins": False,
          "required_pull_request_reviews": None, "restrictions": None}),
    ]


# The social preview image has no REST endpoint (it is a manual upload):
# https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/customizing-your-repositorys-social-media-preview


def show(method, path, body):
    print(f"gh api -X {method} {path}")
    if body is not None:
        print(f"  body: {json.dumps(body)}")


def run_call(gh, method, path, body):
    argv = [gh, "api", "-X", method, path] + (["--input", "-"] if body is not None else [])
    try:
        return subprocess.run(argv, input=json.dumps(body) if body is not None else "", capture_output=True, text=True)
    except OSError as e:
        return subprocess.CompletedProcess(argv, 127, "", f"cannot run {gh}: {e}")


def apply(gh, calls):
    done = []
    for i, (method, path, body) in enumerate(calls):
        label = f"{method} {path}"
        p = run_call(gh, method, path, body)
        if p.returncode != 0:
            print(f"publish_repo: FAILED {label} (exit {p.returncode}): {p.stderr.strip()[-300:]}", file=sys.stderr)
            print("publish_repo: calls already made: " + (", ".join(done) or "none") + ". Fix the cause and rerun; every call is idempotent.", file=sys.stderr)
            return 1
        if i == 0:
            try:
                info = json.loads(p.stdout)
                private, fork = info["private"], info["fork"]
            except (ValueError, KeyError, TypeError):
                print(f"publish_repo: cannot read private/fork from the answer of {label}; no change was made.", file=sys.stderr)
                return 1
            if private is not False or fork is not False:
                print("publish_repo: refused: the repository is private or a fork. Make the repository public first; "
                      "branch protection is only for the public repository. No change was made.", file=sys.stderr)
                return 2
        done.append(label)
        print(f"ok: {label}")
    return 0


def build_parser():
    ap = argparse.ArgumentParser(prog="publish_repo.py", description="Apply public-repo metadata (dry run unless --apply).")
    ap.add_argument("--repo", required=True, help="the PUBLIC repository, OWNER/NAME")
    ap.add_argument("--apply", action="store_true", help="make the calls (default: print them only)")
    ap.add_argument("--gh", default="gh", help="gh executable (default: gh); tests pass a stub")
    ap.add_argument("--private-repo", help="the private repository OWNER/NAME, refused as --repo")
    ap.add_argument("--metadata", default=str(METADATA), help=argparse.SUPPRESS)
    return ap


def run(args):
    owner, name = parse_slug(args.repo, "--repo")
    repo = f"{owner}/{name}"
    forbidden = live_origin_slugs()
    if args.private_repo:
        forbidden.add("/".join(parse_slug(args.private_repo, "--private-repo")).lower())
    if repo.lower() in forbidden:
        raise Refused(f"--repo {repo} is the private repository. Never run this against the private repository; its main must stay unprotected.")
    raw = load_metadata(args.metadata)
    low = json.dumps(raw).lower()  # before the tokens are replaced, so the public slug itself cannot match
    for slug in sorted(forbidden):
        if re.search(r"(?<![a-z0-9._-])" + re.escape(slug) + r"(?![a-z0-9._-])", low):
            raise Refused("the metadata still contains the private repository slug; remove it from the metadata.")
    meta = render(raw, owner, name)
    scrub_check(meta)
    calls = plan(repo, meta)
    if not args.apply:
        print(f"publish_repo: DRY RUN for {repo}; nothing is called. Add --apply to make these calls.")
        for call in calls:
            show(*call)
        print(SOCIAL)
        return 0
    rc = apply(args.gh, calls)
    if rc == 0:
        print(SOCIAL)
    return rc


def main(argv=None):
    args = build_parser().parse_args(argv)
    try:
        return run(args)
    except Refused as e:
        print(f"publish_repo: refused: {e}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
