---
name: diff-checklist
description: Run only the diff-checklist items whose triggers match your diff and record their results in the PR body. Use when about to run factory task update --status in_review, before factory done, or before pushing a retry.
---

Purpose: catch the defect classes the checklist encodes, cheaply, before review. Items always come from origin/main; never edit ci/diff-checklist.json to change what runs.

Follow these steps in order.

1. `git fetch origin`, then from the worktree: `cd go && GOTOOLCHAIN=local ~/.local/go/bin/go run ./cmd/diff-checklist run --repo-dir ..` (it prints the matching items as a block, or `nothing to run` while ci/diff-checklist.json is not on main). If the worker guard refuses `--repo-dir` (its go flag allow-list), run the same command without it from `go/`; the repo defaults to the worktree.
2. For each listed item, do its check and replace TODO with `PASS <evidence: command -> key output>`, `FAIL <evidence>` (then fix the code and rerun) or `N/A <reason>`.
3. Paste the whole block, unchanged apart from the results, into the PR body as its own details element after the Acceptance walk details element.
4. Verify: `cd go && GOTOOLCHAIN=local ~/.local/go/bin/go run ./cmd/diff-checklist body --pr <PR url> --repo-dir ..` must print no MISSING/TODO lines (same fallback). In observe mode it exits 0 even with problems: fix them anyway.
5. `- none matched` is a valid block; still paste it.
6. If the tool cannot run (missing go, exit 2), write `- not run: <reason>` in the block and tell your lead. Never skip silently.
