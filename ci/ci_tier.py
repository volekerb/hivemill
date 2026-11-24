#!/usr/bin/env python3
"""CI tiers: classify a PR diff as docs, normal or risky and pick how much of the suite runs (T-0658).

  Binary (T-1166): a changed file whose `git diff --numstat` added or deleted field is not a decimal count (e.g. `-`
  for binary) is not docs-only: it is classified risky and the reason names that path before the all-docs return.

  ci_tier.py classify --base BASE [--head HEAD] [--root DIR] [--only-file F] [--github-output]
  ci_tier.py docs-check --base BASE [--head HEAD] [--root DIR]
  ci_tier.py escalate --base BASE [--head HEAD] [--root DIR]   (G-070 fast lane: lane=fast|docs exit 0; exit 1 plus reasons = normal lane)

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
    """Added + deleted lines of `git diff base...head` (binary files count 0 in the sum; classify handles them separately)."""
    out = affected.git(root, "diff", "--numstat", "--no-renames", "%s...%s" % (base, head))
    total = 0
    for line in out.splitlines():
        a, d, _ = line.split("\t", 2)
        total += (int(a) if a.isdigit() else 0) + (int(d) if d.isdigit() else 0)
    return total


def numstat_non_line_paths(root, base, head):
    """Paths in `git diff --numstat` where added or deleted is not a decimal line count (binary and similar)."""
    out = affected.git(root, "diff", "--numstat", "--no-renames", "%s...%s" % (base, head))
    hits = []
    for line in out.splitlines():
        parts = line.split("\t", 2)
        if len(parts) < 3:
            continue
        a, d, path = parts
        if not (a.isdigit() and d.isdigit()):
            hits.append(path)
    return hits


def base_top_level_names(root, base):
    """Top-level entry names in `base` (git ls-tree --name-only)."""
    out = affected.git(root, "ls-tree", "--name-only", base)
    return {line for line in out.splitlines() if line}


def new_top_level_risk(root, base, files):
    """The first path component absent from the base tree, else None."""
    known = base_top_level_names(root, base)
    for path in files:
        top = path.split("/", 1)[0]
        if top not in known:
            return top
    return None


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
    non_line = numstat_non_line_paths(root, base, head)
    if non_line:
        p = sorted(non_line)[0]
        return "risky", "binary or non-text diff (numstat not line counts): %s" % p
    for p in files:
        if risky_path(p):
            return "risky", "%s is on the risky path list" % p
    top = new_top_level_risk(root, base, files)
    if top:
        return "risky", "new top-level entry %s" % top
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


# G-070 fast lane: a fast-lane PR moves to the normal lane when its diff touches any of these. The ci tier rules above are
# reused (RISKY_PATHS, RISKY_DEFS, binary files, new top-level entries) and widened; MAX_LINES does not apply (the fast
# lane relaxes the size limit). bin/factory_cli.py is read for risky defs too (the ci tier reads only bin/factory).
FAST_LANE_PATHS = [
    "ci/*",                                                          # every CI gate, not only the listed ones
    "roles/*",                                                       # role prompts, settings and hooks
    "go/internal/guard/*", "go/cmd/worker-guard/*", "go/internal/identity/*",   # the worker guard
    "go/internal/sandbox/*", "scout/*box*.py", "scout/sandbox*",     # sandboxes
    "go/internal/gate/*", "go/internal/holdexpiry/*",                # merge gate code
    "go/internal/factorydb/*", "db/*", "*.sql", "*migration*",       # DB schema and migrations
    "*secret*", "*token*", "*credential*", "*.pem", "*.key", ".env*", "*/.env*",   # secrets and tokens
    "go/cmd/ci-*", "go/internal/citools/*",                          # the Go CI gates
    "go/internal/tick/*", "go/internal/spawn/*", "go/internal/prflow/*", "go/internal/cli/*",   # Go tick, spawn, merge, CLI
    "go/go.mod", "go/go.sum",                                        # Go dependencies
    "tests/test_*guard*", "tests/test_*gate*", "tests/test_*sandbox*",   # the tests that pin guards and gates
    "*SKILL.md", "plugins/*", "skilleval/*", "*prompt*",             # agent instructions: .md, but not docs
]
FAST_LANE_DEFS = {"migrate", "SCHEMA_VERSION", "record_merged", "run_after_merge", "red_main_check", "ROLES",
                  "FASTCHECK_ROLE", "cmd_config", "cmd_verdict", "cmd_spawn", "spawn", "cmd_merge", "cmd_done",
                  "require_role", "actor_role", "fastcheck", "_config_value_rules",
                  "<stmt>"}                                          # module-level statements: constants, imports, tables
FAST_LANE_DEF_PREFIXES = ("mech_", "fast_lane", "fast_check", "lane_", "_judge", "red_main", "_red_main", "merge_",
                          "migrate", "verdict", "green_", "token", "_token", "secret", "settings_", "guard_",
                          "is_fastcheck", "spawn_", "_spawn", "_config", "require_", "refuse")
FAST_LANE_DEF_FILES = (BIN, "bin/factory_cli.py")


def fast_lane_path(path):
    return risky_path(path) or any(fnmatch.fnmatchcase(path, pat) for pat in FAST_LANE_PATHS)


def fast_lane_def(name):
    return risky_def(name) or name in FAST_LANE_DEFS or name.startswith(FAST_LANE_DEF_PREFIXES)


def deleted_test(path):
    name = os.path.basename(path)
    return (path.startswith("tests/") and name.startswith("test_")) or name.endswith("_test.go")


def escalations(root, base, head):
    """Reasons the diff base...head must leave the fast lane (G-070), [] when it may stay. Raises affected.AffectedError
    when git cannot answer: the caller escalates then (an undecidable diff never stays fast)."""
    changed = affected.changed_files(root, base, head)
    why = ["binary or non-text diff: %s" % p for p in sorted(numstat_non_line_paths(root, base, head))]
    why += ["deleted test %s" % p for p, exists in changed if not exists and deleted_test(p)]
    why += ["%s is a guarded path" % p for p, _ in changed if fast_lane_path(p)]
    top = new_top_level_risk(root, base, [p for p, _ in changed])
    if top:
        why.append("new top-level entry %s" % top)
    paths = {p for p, _ in changed}
    for f in FAST_LANE_DEF_FILES:
        if f not in paths:
            continue
        src = [affected.git(root, "show", "%s:%s" % (r, f)) if affected._exists(root, r, f) else "" for r in (base, head)]
        try:
            names = affected.changed_defs(*src)
        except SyntaxError as e:
            why.append("%s does not parse (%s)" % (f, e.msg))
            continue
        why += ["%s changed in %s" % (n, f) for n in sorted(names) if fast_lane_def(n)]
    return why


def lane(root, base, head):
    """(lane, reasons) of the diff base...head for the G-070 fast lane: ("normal", escalations) when any rule hits,
    ("docs", ...) when every file is docs (no agent check; MAX_LINES does not apply), else ("fast", ...). Raises
    affected.AffectedError like escalations()."""
    why = escalations(root, base, head)
    if why:
        return "normal", why
    files = [p for p, _ in affected.changed_files(root, base, head)]
    if not files:
        return "normal", ["empty diff"]
    if all(is_docs(p) and p.endswith(".md") for p in files):    # docs/ images or scripts still get the fast check
        return "docs", ["only docs files changed"]
    return "fast", ["%d files, no guarded path or def" % len(files)]


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
    for name in ("classify", "docs-check", "escalate"):
        p = sub.add_parser(name)
        p.add_argument("--base", required=True)
        p.add_argument("--head", default="HEAD")
        p.add_argument("--root", default=".")
    cl = sub.choices["classify"]
    cl.add_argument("--only-file", help="write the affected modules here, one per line")
    cl.add_argument("--github-output", action="store_true", help="also append the result to $GITHUB_OUTPUT")
    a = ap.parse_args(argv)
    if a.cmd == "escalate":
        try:
            name, why = lane(a.root, a.base, a.head)
        except affected.AffectedError as e:
            print("escalate: cannot decide (%s); the PR leaves the fast lane" % e)
            return 2
        for w in why if name == "normal" else ():
            print("escalate: %s" % w)
        print("lane=%s" % name)
        return 1 if name == "normal" else 0
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
