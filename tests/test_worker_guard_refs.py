"""T-0336: non-coder roles must not create commits or move refs (ref-move guard)."""
import json
import os
import subprocess
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from guard_impl import hook_run  # noqa: E402
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
HOOK = os.path.join(ROOT, "roles", "hooks", "worker-guard.py")

NON_CODERS = ("qa", "reviewer", "judge", "cleaner", "pm", "scout", "evaluator", "staff-engineer")
BYPASS = (
    "cd /tmp ; git merge origin/main",
    "cd /tmp && git commit -m x",
    "git -C /tmp rebase main",
    "git -c a.b=1 reset --hard",
    'bash -c "git pull"',
    "timeout 5 git cherry-pick abc",
    "git revert abc",
    "git am x.patch",
    "git update-ref refs/heads/x HEAD",
    "git symbolic-ref HEAD refs/heads/x",
    "git stash",
    "S=merge; git $S x",
)
ALLOW_QA = (
    "git merge-tree --write-tree origin/main HEAD",
    "git merge-base HEAD origin/main",
    "git commit-tree abc -p HEAD -p origin/main -m m",
    "git worktree add --detach /tmp/x abc",
    "git fetch origin",
    "grep -n 'git merge' docs/x.md",
)
CODER_OK = (
    "git commit -m x",
    "git merge origin/main",
    "git rebase origin/main",
    "git reset --hard HEAD",
)


def g_hook(cmd, role):
    payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}})
    env = {k: v for k, v in os.environ.items() if k != "GUARD_IMPL"}
    env["GUARD_IMPL"] = "python"
    return hook_run(payload, ["--role", role], env=env).returncode


COMMIT_REFUSED = tuple(r for r in NON_CODERS if r != "judge")


class RefMoveNonCoderTests(unittest.TestCase):
    def test_plain_merge_and_commit_refused(self):
        for role in NON_CODERS:
            with self.subTest(role=role, cmd="git merge origin/main"):
                self.assertEqual(g_hook("git merge origin/main", role), 2)
        for role in COMMIT_REFUSED:
            with self.subTest(role=role, cmd="git commit -m x"):
                self.assertEqual(g_hook("git commit -m x", role), 2)

    def test_qa_bypass_forms_refused(self):
        for cmd in BYPASS:
            with self.subTest(cmd=cmd):
                self.assertEqual(g_hook(cmd, "qa"), 2)

    def test_qa_reviewer_merged_tree_recipe_allowed(self):
        for role in ("qa", "reviewer"):
            for cmd in ALLOW_QA:
                with self.subTest(role=role, cmd=cmd):
                    self.assertEqual(g_hook(cmd, role), 0, cmd)

    def test_coder_unchanged(self):
        for cmd in CODER_OK:
            with self.subTest(cmd=cmd):
                self.assertEqual(g_hook(cmd, "coder"), 0)


class RefMoveCleanerTests(unittest.TestCase):
    def test_cleaner_branch_delete_and_worktree_remove(self):
        """Matches acceptance 5 (no WORKER_GUARD_LIVE_HOME; same rc as on main hook)."""
        self.assertEqual(g_hook("git branch -D factory/t-0001", "cleaner"), 0)
        self.assertEqual(g_hook("git worktree remove /tmp/x/factory/worktrees/G-1-T-1", "cleaner"), 0)


class RefMoveDiscriminatesTests(unittest.TestCase):
    def test_without_git_ref_move_call_judge_merge_is_allowed(self):
        """C4: with the git_ref_move invocation removed, judge merge is no longer refused."""
        copy = os.path.join(ROOT, "roles", "hooks", ".worker-guard-t0336-discriminate.py")
        with open(HOOK, encoding="utf-8") as f:
            text = f.read()
        text = text.replace("self.git_ref_move(rest)", "pass  # git_ref_move stripped for test")
        try:
            with open(copy, "w", encoding="utf-8") as f:
                f.write(text)
            payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": "git merge origin/main"}})
            p = subprocess.run(
                [sys.executable, copy, "--role", "judge"],
                input=payload,
                cwd=ROOT,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(p.returncode, 0, p.stderr)
        finally:
            try:
                os.remove(copy)
            except OSError:
                pass
        self.assertEqual(g_hook("git merge origin/main", "judge"), 2)


if __name__ == "__main__":
    unittest.main()