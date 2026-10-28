---
name: pr-triage
description: Find the real reason a PR is stuck or red before blaming CI. Use when a PR is not merging, CI shows no checks or red checks, or a judge reports a PR as blocked.
---

# PR triage

Check mergeability before reading CI (M514). A conflicting PR gets no CI run, so "CI is stuck" is usually a merge conflict.

1. `gh pr view <url> --json mergeable,mergeStateStatus,headRefOid`
2. If `mergeable` is `CONFLICTING`: tell the coder to merge origin/main, keep both sides of any conflict, rerun the tests and push. Do not touch CI.
3. Only when `mergeable` is `MERGEABLE`: `gh pr checks <url>`.
4. Confirm the checks belong to the current head: compare their commit with `headRefOid` from step 1.
5. A real red check goes back to the coder with the failing test name and log excerpt.
