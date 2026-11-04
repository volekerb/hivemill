"""T-0991: the judge's merged-tree check (roles/judge.md) must be runnable under worker-guard from the shared checkout.

The commands are read from the "Merged-tree check" line of roles/judge.md, so doc and test cannot drift. The old route
(`T=$(git merge-tree ...)`, `git commit-tree`) is refused by the hook rule git_shared (merge-tree/commit-tree are not in
GIT_READ_ONLY, and the git directory is the shared checkout)."""
import os
import re
import subprocess
import tempfile
import unittest

from test_worker_guard_git import GitGuardBase

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SHA = "a" * 40
TREE = "b" * 40


def route():
    """The backticked commands of the judge.md Merged-tree check line, placeholders still in <>."""
    with open(os.path.join(ROOT, "roles", "judge.md")) as f:
        line = next(x for x in f if x.lstrip().startswith("Merged-tree check"))
    return line, re.findall(r"`([^`]+)`", line)


class JudgeMergedTreeRoute(GitGuardBase):
    def fill(self, cmd, scratch):
        return (cmd.replace("<task-worktree>", self.wt).replace("<pr-head>", SHA).replace("<tree-id>", TREE)
                .replace("<scratchpad>", scratch))

    def route_cmds(self):
        line, cmds = route()
        steps = [c for c in cmds if c.startswith(("git -C ", "factory src ", "rm -rf "))]
        self.assertEqual([c.split()[0] for c in steps], ["git", "factory", "rm"], line)
        return steps

    def test_every_documented_command_is_allowed_for_judge_from_the_shared_checkout(self):
        self.scratch = os.path.join(self.tmp, "scratchpad")
        os.makedirs(self.scratch)
        for cmd in self.route_cmds():
            with self.subTest(cmd=cmd):
                self.allowed(self.fill(cmd, self.scratch), cwd=self.home, role="judge")

    def test_doc_names_no_command_substitution_or_commit_tree(self):
        line, cmds = route()
        steps = [c for c in cmds if c.startswith(("git -C ", "factory src ", "rm -rf "))]
        self.assertEqual(len(steps), 3, cmds)
        for c in steps:
            self.assertNotIn("$(", c)
            self.assertNotIn("commit-tree", c)
        self.assertTrue(steps[0].startswith("git -C ") and "merge-tree" in steps[0], cmds)

    def test_old_route_is_refused_in_the_shared_checkout(self):
        old = [f"git merge-tree --write-tree origin/main {SHA}", f"git commit-tree {TREE} -p origin/main -p {SHA} -m tmp",
               f"T=$(git merge-tree --write-tree origin/main {SHA})"]
        for cmd in old:
            with self.subTest(cmd=cmd):
                p = self.blocked(cmd, cwd=self.home, role="judge")
                self.assertIn("shared checkout", p.stderr)

    def test_merge_tree_in_the_shared_checkout_is_refused_even_with_a_literal_id(self):
        p = self.blocked(f"git merge-tree --write-tree origin/main {SHA}", cwd=self.home, role="judge")
        self.assertIn("merge-tree", p.stderr)


class JudgeMergedTreeResidue(unittest.TestCase):
    """The documented git step leaves refs, HEAD and the working tree alone; its only residue is objects."""

    def git(self, *a, cwd):
        return subprocess.run(["git", *a], cwd=cwd, capture_output=True, text=True, env=os.environ.copy())

    def test_no_ref_or_worktree_change(self):
        with tempfile.TemporaryDirectory() as td:
            r = os.path.realpath(td)
            g = lambda *a: self.git(*a, cwd=r)
            g("init", "-q", "-b", "main")
            for name, text in (("a", "1\n"),):
                open(os.path.join(r, name), "w").write(text)
            g("add", "."); g("commit", "-qm", "base")
            g("checkout", "-qb", "pr")
            open(os.path.join(r, "b"), "w").write("2\n")
            g("add", "."); g("commit", "-qm", "pr")
            g("checkout", "-q", "main")
            open(os.path.join(r, "c"), "w").write("3\n")
            g("add", "."); g("commit", "-qm", "main moved")
            head = g("rev-parse", "pr").stdout.strip()
            before = (g("for-each-ref").stdout, g("rev-parse", "HEAD").stdout, g("status", "--porcelain").stdout)
            _line, cmds = route()
            mt = next(c for c in cmds if c.startswith("git -C ")).replace("<task-worktree>", r).replace("<pr-head>", head)
            mt = mt.replace("origin/main", "main")
            p = subprocess.run(mt.split(), capture_output=True, text=True, env=os.environ.copy())
            self.assertEqual(p.returncode, 0, p.stderr)
            tree = p.stdout.split()[0]
            self.assertEqual(len(tree), 40)
            self.assertEqual(g("cat-file", "-t", tree).stdout.strip(), "tree")
            names = g("ls-tree", "--name-only", tree).stdout.split()
            self.assertEqual(sorted(names), ["a", "b", "c"])
            after = (g("for-each-ref").stdout, g("rev-parse", "HEAD").stdout, g("status", "--porcelain").stdout)
            self.assertEqual(before, after)


if __name__ == "__main__":
    unittest.main()
