#!/usr/bin/env python3
"""Risk tier of a PR diff (G-022 #1, staff-engineer ADR M10886). Stdlib only.

  tier(diff, paths, lines, rules) -> "T0" | "T1" | "T2" | "T3"

  T3  any sensitive path or sensitive top-level def (rules `sensitive_*`), or more than `large_lines` changed lines
  T0  docs only (rules `docs_*`)
  T1  tests only, or at most `small_lines` changed lines
  T2  everything else
A tier only ever goes up (see TIERS / max_tier); the caller keeps max(old, new). The rules are read from the MAIN checkout
(bin/factory loads this module and ci/tier_rules.json from HOME), so a PR cannot edit the rules that classify itself.
Fails closed: a sensitive file that cannot be parsed is T3.
"""
import fnmatch
import importlib.util
import json
import os
import subprocess

HERE = os.path.dirname(os.path.abspath(__file__))
RULES_FILE = "tier_rules.json"
TIERS = ("T0", "T1", "T2", "T3")


class TierError(Exception):
    pass


def max_tier(a, b):
    """The higher of two tiers; a missing one counts as lowest."""
    return max((x for x in (a, b) if x), key=TIERS.index, default=None)


def load_rules(path=None):
    path = path or os.path.join(HERE, RULES_FILE)
    try:
        with open(path, encoding="utf-8") as f:
            rules = json.load(f)
        for k in ("docs_globs", "docs_excluded_prefixes", "tests_globs", "sensitive_paths", "sensitive_defs",
                  "small_lines", "large_lines"):
            rules[k]
    except (OSError, ValueError, KeyError) as e:
        raise TierError("cannot load tier rules %s: %s; restore ci/tier_rules.json on the main checkout" % (path, e)) from e
    return rules


def _affected():
    spec = importlib.util.spec_from_file_location("affected_main", os.path.join(HERE, "affected.py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def _match(path, pat):
    if pat.endswith("/**"):
        return path.startswith(pat[:-2])
    return fnmatch.fnmatchcase(path, pat)


def _any(path, pats):
    return any(_match(path, p) for p in pats)


def sensitive_defs(diff, rules):
    """Sorted 'path:def' for each changed top-level def (ci/affected.py ast diff) matching rules['sensitive_defs'].
    `diff` is {path: (base_src, head_src)}; a source that does not parse counts as a hit on '<unparsable>'."""
    hits = set()
    aff = None
    for path, pats in rules["sensitive_defs"].items():
        if path not in diff:
            continue
        aff = aff or _affected()
        base, head = diff[path]
        try:
            names = aff.changed_defs(base, head)
        except SyntaxError:
            hits.add("%s:<unparsable>" % path)
            continue
        hits.update("%s:%s" % (path, n) for n in names if _any(n, pats))
    return sorted(hits)


def reasons(diff, paths, lines, rules):
    """Why the diff is T3: list of strings, empty when it is not."""
    out = ["sensitive path %s" % p for p in sorted(paths) if _any(p, rules["sensitive_paths"])]
    out += ["sensitive def %s" % d for d in sensitive_defs(diff, rules)]
    if lines > rules["large_lines"]:
        out.append("%d changed lines (> %d)" % (lines, rules["large_lines"]))
    return out


def tier(diff, paths, lines, rules):
    """Classify. diff: {path: (base_src, head_src)} for the files named in rules['sensitive_defs'] (others ignored);
    paths: every changed path (renames as delete + add); lines: added + deleted lines."""
    paths = list(paths)
    if reasons(diff, paths, lines, rules):
        return "T3"
    def docs(p):
        return _any(p, rules["docs_globs"]) and not any(p.startswith(x) for x in rules["docs_excluded_prefixes"])
    if paths and all(docs(p) for p in paths):
        return "T0"
    if (paths and all(_any(p, rules["tests_globs"]) for p in paths)) or lines <= rules["small_lines"]:
        return "T1"
    return "T2"


def _git(root, *args):
    p = subprocess.run(["git", "-C", root, *args], capture_output=True, text=True)
    if p.returncode:
        raise TierError("git %s failed: %s" % (" ".join(args), p.stderr.strip()))
    return p.stdout


def _show(root, rev, path):
    """File text at rev, or '' when the file does not exist there (added or deleted by the diff)."""
    p = subprocess.run(["git", "-C", root, "show", "%s:%s" % (rev, path)], capture_output=True, text=True)
    return p.stdout if p.returncode == 0 else ""


def classify_range(root, base, head, rules):
    """(tier, reasons) for `git diff base...head` in the repo at `root`."""
    for ref in (base, head):
        if not ref or ref.startswith("-"):
            raise TierError("bad ref %r: a ref must not start with '-'" % ref)
    rng = "%s...%s" % (base, head)
    paths, lines = [], 0
    # -z: NUL-separated, paths unquoted (a quoted non-ASCII path would match no sensitive glob)
    for ln in _git(root, "diff", "--numstat", "--no-renames", "-z", rng).split("\0"):
        if not ln:
            continue
        add, dele, path = ln.split("\t", 2)
        paths.append(path)
        lines += (int(add) + int(dele)) if add != "-" else rules["large_lines"] + 1  # binary file: size unknown, treat as large
    diff = {}
    merge_base = None
    for path in rules["sensitive_defs"]:
        if path in paths:
            merge_base = merge_base or _git(root, "merge-base", base, head).strip()
            diff[path] = tuple(_show(root, rev, path) for rev in (merge_base, head))
    return tier(diff, paths, lines, rules), reasons(diff, paths, lines, rules)
