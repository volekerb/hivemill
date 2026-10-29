#!/usr/bin/env python3
"""CI tiers: classify a PR diff as docs, normal or risky and pick how much of the suite runs (T-0658).

  ci_tier.py classify --base BASE [--head HEAD] [--root DIR] [--only-file F] [--github-output]
  ci_tier.py docs-check --base BASE [--head HEAD] [--root DIR]

classify reads `git diff BASE...HEAD` and prints `tier=`, `mode=`, `go=`, `reason=` (and `modules=` in affected mode):
  docs    only *.md outside roles/, or files under docs/        -> mode docs     (link check + docs index test only)
  risky   RISKY_PATHS / RISKY_DEFS below, or > MAX_LINES changed -> mode full
  normal  everything else                                        -> mode affected (ci/affected.py selection; FULL -> full)
`go=true` when go/** changed or the mode is full. Any error prints mode=full: an undecidable diff gets the whole suite.
tests.yml runs this file from the BASE commit (git archive), never from the PR, so a PR cannot loosen its own tier.
docs-check fails on a broken relative link or an unbalanced code fence in a changed markdown file. Stdlib only.
"""
import argparse
import fnmatch
import os
import re
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import affected  # noqa: E402  (the sibling copy: from the base commit when run by CI)

MAX_LINES = 600
BIN = "bin/factory"
RISKY_PATHS = [
    "ci/public_text_guard.py", "ci/publish_repo.py", "ci/scrub_*", "ci/guard_*",
    "ci/ci_tier.py", "ci/affected.py", "ci/shard.py", "ci/test_count_gate.py",  # the CI gates themselves
    "roles/hooks/*", "roles/*settings*",
    "scout/nsbox.py", "scout/gatebox.py", "scout/ghcred.py", "scout/egress.py", "scout/sandbox.py", "scout/workerbox.py",
    "systemd/*",
    ".github/*",
]
RISKY_DEFS = {"cmd_judge", "verdict_carries", "head_has_main", "refresh_branch", "git_config_guard", "noreply_email",
              "_push_policy", "OWNER_CAP_KEYS", "DEFAULT_CONFIG", "SCHEMA",
              # guards and refusals no test module names, so ci/affected.py would not select a test for them
              "_identity", "_state_push_refusal", "refuse_worker", "refuse_on_demand", "check_sandbox_roles",
              "_token_file_ok", "settings_tokens", "git_config_refusal", "_public_guard", "evaluator_home_guard",
              "verify_merged_externally"}
RISKY_DEF_PREFIXES = ("judge_", "gate", "sandbox_")


class TierError(Exception):
    """Classification failed; the message says what to do next."""


def is_docs(path):
    if path.startswith("docs/"):
        return True
    return path.endswith(".md") and not path.startswith("roles/")


def risky_path(path):
    return any(fnmatch.fnmatchcase(path, pat) for pat in RISKY_PATHS)


def risky_def(name):
    return name in RISKY_DEFS or name.startswith(RISKY_DEF_PREFIXES)


def changed_lines(root, base, head):
    """Added + deleted lines of `git diff base...head` (binary files count 0; their path rules still apply)."""
    out = affected.git(root, "diff", "--numstat", "--no-renames", "%s...%s" % (base, head))
    total = 0
    for line in out.splitlines():
        a, d, _ = line.split("\t", 2)
        total += (int(a) if a.isdigit() else 0) + (int(d) if d.isdigit() else 0)
    return total


def bin_factory_risk(root, base, head):
    """The first risky top-level name changed in bin/factory, else None."""
    base_src = affected.git(root, "show", "%s:%s" % (base, BIN)) if affected._exists(root, base, BIN) else ""
    head_src = affected.git(root, "show", "%s:%s" % (head, BIN)) if affected._exists(root, head, BIN) else ""
    try:
        names = affected.changed_defs(base_src, head_src)
    except SyntaxError as e:
        return "%s does not parse (%s)" % (BIN, e.msg)
    hit = sorted(n for n in names if risky_def(n))
    return "%s changed in %s" % (hit[0], BIN) if hit else None


def classify(root, base, head):
    """(tier, reason) for the diff base...head."""
    files = [p for p, _ in affected.changed_files(root, base, head)]
    if not files:
        return "normal", "empty diff"
    for p in files:
        if risky_path(p):
            return "risky", "%s is on the risky path list" % p
    if BIN in files:
        why = bin_factory_risk(root, base, head)
        if why:
            return "risky", why
    n = changed_lines(root, base, head)
    if n > MAX_LINES:
        return "risky", "%d changed lines (> %d)" % (n, MAX_LINES)
    if all(is_docs(p) for p in files):
        return "docs", "only docs files changed"
    return "normal", "%d files, %d changed lines" % (len(files), n)


def plan(root, base, head):
    """Dict of outputs: tier, mode, go, reason, modules (affected mode only)."""
    tier, reason = classify(root, base, head)
    out = {"tier": tier, "reason": reason, "modules": ""}
    if tier == "docs":
        out["mode"] = "docs"
        # tests that read a changed file (CHANGELOG.md, README.md, docs assets...) still run, as an affected selection
        hit = set()
        mods = affected.all_modules(os.path.join(root, "tests"))
        for p, exists in affected.changed_files(root, base, head):
            if p == "docs/README.md":
                continue  # the docs index test runs in docs mode anyway
            for needle in {p, os.path.basename(p)}:
                hit |= affected.mentions(os.path.join(root, "tests"), mods, needle)
        if hit:
            out["mode"], out["modules"] = "affected", " ".join(sorted(hit))
            out["reason"] += "; tests mention the changed files"
    elif tier == "risky":
        out["mode"] = "full"
    else:
        mods, note = affected.select(root, base, head)
        if mods == "FULL":
            out["mode"], out["reason"] = "full", "affected.py: %s" % note
        else:
            out["mode"], out["modules"] = "affected", " ".join(mods)
    files = [p for p, _ in affected.changed_files(root, base, head)]
    out["go"] = "true" if out["mode"] == "full" or any(p.startswith("go/") for p in files) else "false"
    return out


FALLBACK = {"tier": "risky", "mode": "full", "go": "true", "modules": ""}

LINK = re.compile(r"\]\(([^)\s]+)")


def docs_problems(root, base, head):
    """Problems in the changed markdown files: relative links that do not resolve, unbalanced code fences."""
    problems = []
    for path, exists in affected.changed_files(root, base, head):
        if not exists or not path.endswith(".md"):
            continue
        full = os.path.join(root, path)
        with open(full, encoding="utf-8") as f:
            text = f.read()
        if sum(1 for l in text.splitlines() if l.lstrip().startswith(("```", "~~~"))) % 2:
            problems.append("%s: unbalanced code fence; close it" % path)
        body = re.sub(r"(?s)(```|~~~).*?\1", "", text)
        for target in LINK.findall(body):
            if re.match(r"([a-z][a-z0-9+.-]*:|#|/)", target, re.I):
                continue
            rel = target.split("#", 1)[0]
            if rel and not os.path.exists(os.path.normpath(os.path.join(os.path.dirname(full), rel))):
                problems.append("%s: link target %s does not exist; fix the path" % (path, target))
    return problems


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    sub = ap.add_subparsers(dest="cmd", required=True)
    for name in ("classify", "docs-check"):
        p = sub.add_parser(name)
        p.add_argument("--base", required=True)
        p.add_argument("--head", default="HEAD")
        p.add_argument("--root", default=".")
    cl = sub.choices["classify"]
    cl.add_argument("--only-file", help="write the affected modules here, one per line")
    cl.add_argument("--github-output", action="store_true", help="also append the result to $GITHUB_OUTPUT")
    a = ap.parse_args(argv)
    if a.cmd == "docs-check":
        try:
            probs = docs_problems(a.root, a.base, a.head)
        except affected.AffectedError as e:
            print("ci_tier: %s" % e, file=sys.stderr)
            return 2
        for p in probs:
            print("docs-check: %s" % p, file=sys.stderr)
        print("docs-check: %s" % ("FAILED" if probs else "OK"))
        return 1 if probs else 0
    try:
        res = plan(a.root, a.base, a.head)
    except (affected.AffectedError, OSError, ValueError) as e:
        print("::warning::ci_tier could not classify (%s); running the full suite" % e, file=sys.stderr)
        res = dict(FALLBACK, reason="classifier error: %s" % e)
    lines = ["%s=%s" % (k, res[k]) for k in ("tier", "mode", "go", "reason", "modules")]
    print("\n".join(lines))
    if a.only_file:
        with open(a.only_file, "w") as f:
            f.write("\n".join(res["modules"].split()) + ("\n" if res["modules"] else ""))
    if a.github_output:
        with open(os.environ["GITHUB_OUTPUT"], "a") as f:
            f.write("\n".join(lines) + "\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
