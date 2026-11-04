"""T-0991: the judge's merged-tree check (roles/judge.md) must be runnable under worker-guard from the shared checkout.

The commands are read from the "Merged-tree check" line of roles/judge.md, so doc and test cannot drift. The old route
(`T=$(git merge-tree ...)`, `git commit-tree`) is refused by the hook rule git_shared (merge-tree/commit-tree are not in
GIT_READ_ONLY, and the git directory is the shared checkout)."""
import os
import re
import shutil
import subprocess
import tempfile
import unittest

from guard_impl import hook_run
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
        self.assertEqual([c.split()[0] for c in steps], ["git", "git", "factory", "rm"], line)
        self.assertIn(" fetch origin", steps[0], line)
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
        self.assertEqual(len(steps), 4, cmds)
        for c in steps:
            self.assertNotIn("$(", c)
            self.assertNotIn("commit-tree", c)
        self.assertTrue(steps[0].startswith("git -C ") and steps[0].endswith("fetch origin"), cmds)
        self.assertTrue(steps[1].startswith("git -C ") and "merge-tree" in steps[1], cmds)

    def test_doc_states_precondition_and_exit_1_defers_to_j4(self):
        line, _ = route()
        self.assertIn("task_clones=1", line)
        self.assertIn("insufficient evidence:", line)
        self.assertIn("J4 decides", line)
        self.assertNotIn("reject as CONFLICT-ONLY", line)
        self.assertNotIn("command substitution is refused", line)

    def test_hook_run_entry_point_allows_factory_src_of_a_tree_id(self):
        import json
        env = {**os.environ, "HOME": self.tmp, "FACTORY_HOME": self.home, "WORKER_GUARD_LIVE_HOME": self.home}
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": f"factory src {TREE} {self.tmp}/merged"},
                              "cwd": self.home})
        self.assertEqual(hook_run(payload, ["--role", "judge"], env=env).returncode, 0)

    def test_old_route_is_refused_in_the_shared_checkout(self):
        old = [f"git commit-tree {TREE} -p origin/main -p {SHA} -m tmp",
               f"T=$(git merge-tree --write-tree origin/main {SHA})"]
        for cmd in old:
            with self.subTest(cmd=cmd):
                p = self.blocked(cmd, cwd=self.home, role="judge")
                self.assertIn("shared checkout", p.stderr)

    def test_merge_tree_in_the_shared_checkout_is_refused_even_with_a_literal_id(self):
        p = self.blocked(f"git merge-tree --write-tree origin/main {SHA}", cwd=self.home, role="judge")
        self.assertIn("merge-tree", p.stderr)


class JudgeMergedTreeResidue(unittest.TestCase):
    """The documented route, run through a real linked worktree of a temp repo: the tree written with git -C <worktree>
    reads back from the main checkout, exports (git archive stands in for factory src, which needs the live DB), is
    removed; refs other than refs/remotes/origin/*, HEAD and the working trees are unchanged."""

    def git(self, *a, cwd):
        env = {**os.environ, "GIT_AUTHOR_NAME": "zz", "GIT_AUTHOR_EMAIL": "zz@example.invalid",
               "GIT_COMMITTER_NAME": "zz", "GIT_COMMITTER_EMAIL": "zz@example.invalid"}
        return subprocess.run(["git", *a], cwd=cwd, capture_output=True, text=True, env=env)

    def test_no_ref_or_worktree_change(self):
        with tempfile.TemporaryDirectory() as td:
            t = os.path.realpath(td)
            origin, main, wt, scratch = (os.path.join(t, n) for n in ("origin.git", "main", "wt", "scratch"))
            os.makedirs(scratch)
            g = lambda *a, cwd=main: self.git(*a, cwd=cwd)
            self.assertEqual(g("init", "-q", "--bare", "-b", "main", origin, cwd=t).returncode, 0)
            g("clone", "-q", origin, main, cwd=t)
            g("checkout", "-qb", "main")
            for name, text in (("a", "1\n"),):
                open(os.path.join(main, name), "w").write(text)
            g("add", "."); g("commit", "-qm", "base"); g("push", "-q", "origin", "main")
            g("worktree", "add", "-q", "-b", "pr", wt)
            open(os.path.join(wt, "b"), "w").write("2\n")
            g("add", ".", cwd=wt); g("commit", "-qm", "pr", cwd=wt)
            head = g("rev-parse", "pr").stdout.strip()
            # origin/main moves after the worktree was made: only the documented fetch makes it visible
            other = os.path.join(t, "other")
            g("clone", "-q", origin, other, cwd=t)
            open(os.path.join(other, "c"), "w").write("3\n")
            g("add", ".", cwd=other); g("commit", "-qm", "main moved", cwd=other); g("push", "-q", "origin", "main", cwd=other)

            def state():
                refs = [l for l in g("for-each-ref").stdout.splitlines() if "refs/remotes/origin/" not in l]
                return (refs, g("rev-parse", "HEAD").stdout, g("status", "--porcelain").stdout,
                        g("rev-parse", "HEAD", cwd=wt).stdout, g("status", "--porcelain", cwd=wt).stdout)
            before = state()
            _line, cmds = route()
            gits = [c.replace("<task-worktree>", wt).replace("<pr-head>", head).split()
                    for c in cmds if c.startswith("git -C ")]
            fetch = subprocess.run(gits[0], capture_output=True, text=True)
            self.assertEqual(fetch.returncode, 0, fetch.stderr)
            p = subprocess.run(gits[1], capture_output=True, text=True)
            self.assertEqual(p.returncode, 0, p.stderr)
            tree = p.stdout.split()[0]
            self.assertEqual(len(tree), 40)
            # read back from the main checkout, not from the worktree that wrote it
            self.assertEqual(g("cat-file", "-t", tree).stdout.strip(), "tree")
            self.assertEqual(sorted(g("ls-tree", "--name-only", tree).stdout.split()), ["a", "b", "c"])
            dest = os.path.join(scratch, "merged")
            os.makedirs(dest)
            arc = subprocess.run(["git", "archive", tree], cwd=main, capture_output=True)
            self.assertEqual(arc.returncode, 0, arc.stderr)
            subprocess.run(["tar", "-x", "-C", dest], input=arc.stdout, check=True)
            self.assertEqual(sorted(os.listdir(dest)), ["a", "b", "c"])
            shutil.rmtree(dest)
            self.assertFalse(os.path.exists(dest))
            self.assertEqual(before, state())


if __name__ == "__main__":
    unittest.main()
