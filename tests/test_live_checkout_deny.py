"""T-0175: coder and qa may not Edit/Write/NotebookEdit the live checkout top level; worktrees stay writable for coder."""
import json
import os
import pwd
import subprocess
import unittest

from harness import rendered_settings_dir
import harness

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
LIVE = os.path.join(pwd.getpwuid(os.getuid()).pw_dir, "factory")  # T-0212
TOOLS = ("Edit", "Write", "NotebookEdit")
EXTRA_ENTRIES = (".git",)  # not in git ls-tree, but live and must be denied
# G-012 top-level OSS files, denied ahead of their PRs so those PRs stay out of the settings files
PLANNED_ENTRIES = ("LICENSE", "THIRD_PARTY.md", "CONTRIBUTING.md", "SECURITY.md", "CODE_OF_CONDUCT.md", "CHANGELOG.md", ".editorconfig")


def _in_git_work_tree():
    p = subprocess.run(["git", "rev-parse", "--show-toplevel"], cwd=ROOT, capture_output=True, text=True)
    return p.returncode == 0 and os.path.realpath(p.stdout.strip()) == os.path.realpath(ROOT)


IN_GIT_WORK_TREE = _in_git_work_tree()


def git(*args):
    return harness.git(*args, cwd=ROOT)


def top_level(ref):
    return git("ls-tree", "--name-only", ref).split()


def settings(role):
    with open(os.path.join(str(rendered_settings_dir()), f"{role}.json")) as f:
        return json.load(f)


def denied(deny, tool, path):
    """True if a deny rule for `tool` covers absolute `path` (exact, or X/** prefix, or bare tool name)."""
    for rule in deny:
        if rule == tool:
            return True
        if not rule.startswith(tool + "(") or not rule.endswith(")"):
            continue
        pat = rule[len(tool) + 1:-1]
        if pat.startswith("//"):
            pat = pat[1:]
        if pat.endswith("/**"):
            base = pat[:-3]
            if path == base or path.startswith(base + "/"):
                return True
        elif pat.endswith("*"):
            if path.startswith(pat[:-1]):
                return True
        elif path == pat:
            return True
    return False


class LiveCheckoutDeny(unittest.TestCase):
    def entries(self):
        # HEAD always exists (in a shallow CI checkout it is the PR merge commit: main's top level plus
        # anything the PR adds); origin/main is added when present. Never skip: failing to list is an error.
        names = set(top_level("HEAD"))
        if subprocess.run(["git", "rev-parse", "--verify", "-q", "origin/main"], cwd=ROOT, capture_output=True).returncode == 0:
            names |= set(top_level("origin/main"))
        self.assertTrue(names, "git ls-tree returned no entries")
        return sorted(names) + list(EXTRA_ENTRIES)

    @unittest.skipUnless(IN_GIT_WORK_TREE, "ROOT is not a git work tree (archive tree); the top-level listing needs git")
    def test_every_top_level_entry_denied_for_coder_and_qa(self):
        for role in ("coder", "qa"):
            deny = settings(role)["permissions"]["deny"]
            for entry in self.entries():
                for tool in TOOLS:
                    for path in (f"{LIVE}/{entry}", f"{LIVE}/{entry}/x/y"):
                        self.assertTrue(denied(deny, tool, path), f"{role}: {tool} not denied for {path}")

    def test_planned_oss_entries_denied_before_they_exist(self):
        for role in ("coder", "qa"):
            deny = settings(role)["permissions"]["deny"]
            for entry in PLANNED_ENTRIES:
                for tool in TOOLS:
                    for path in (f"{LIVE}/{entry}", f"{LIVE}/{entry}/x/y"):
                        self.assertTrue(denied(deny, tool, path), f"{role}: {tool} not denied for {path}")

    def test_planned_oss_entries_have_literal_exact_and_subtree_rules(self):
        # denied() treats X/** as covering X, so assert both literal rules to catch a dropped one
        for role in ("coder", "qa"):
            deny = settings(role)["permissions"]["deny"]
            for entry in PLANNED_ENTRIES:
                for tool in TOOLS:
                    if tool in deny:
                        continue
                    for rule in (f"{tool}(/{LIVE}/{entry})", f"{tool}(/{LIVE}/{entry}/**)"):
                        self.assertIn(rule, deny, f"{role}: missing deny rule {rule}")

    def test_new_top_level_entry_would_fail(self):
        deny = settings("coder")["permissions"]["deny"]
        self.assertFalse(denied(deny, "Write", f"{LIVE}/brand-new-dir/f.py"))

    def test_coder_worktrees_not_denied(self):
        deny = settings("coder")["permissions"]["deny"]
        for tool in TOOLS:
            for path in (f"{LIVE}/worktrees/G-005-T-0175/bin/factory", f"{LIVE}/worktrees/G-005-T-0175/tests/t.py"):
                self.assertFalse(denied(deny, tool, path), f"coder: {tool} wrongly denied for {path}")


if __name__ == "__main__":
    unittest.main()
