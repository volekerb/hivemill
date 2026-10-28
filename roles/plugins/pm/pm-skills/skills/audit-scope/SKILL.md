---
name: audit-scope
description: What the pm goal audit must not redo, so it stays a paper check of records. Use when you are about to run tests, open a PR head, re-verify QA, check CI or touch git during a goal audit.
---

# Audit scope: what the audit does NOT redo

The audit reads records (DB, `gh pr view`, `git log`, `git show`). If you feel the need to do any of these, stop.

1. Run the test suite or the build, at any SHA.
2. Re-run QA's per-criterion commands for a criterion QA PASSed with evidence. If the evidence is missing or hand-waved, file a follow-up or reopen; do not re-verify.
3. Check out a PR head, or check a head-SHA binding.
4. Review a diff for correctness, security or style (the reviewer's job).
5. Check CI status, mergeability or required checks (the judge gate does this).
6. Record any verdict (`factory verdict`/`factory judge` stay qa/reviewer/judge only) or merge or reject a PR.
7. Reopen a merged task to send it back to a coder. Merged work only gets a follow-up task.
8. Touch code, git (beyond read-only log, diff and show), worktrees or branches.

If evidence is missing or hand-waved, file a follow-up or reopen a non-PR task. Do not re-verify.
