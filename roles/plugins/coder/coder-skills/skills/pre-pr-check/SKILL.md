---
name: pre-pr-check
description: Self-check that catches the misses a reviewer or QA would otherwise find (unmet spec item, red CI, duplicate def, flaky test, missing PR-body item, unmeasured limit). Use when about to run factory task update --status in_review, before factory done, or before pushing a retry.
---

Purpose: find cheaply, before the PR, the defects that cost a rejection round (G-013 class C5s, T-0147). Builds on coder.md C1-C10; does not restate them.

Follow these steps in order.

1. Acceptance walk: for EVERY numbered spec item and EVERY acceptance item, write one line `N. <how met> - <command> -> <key output>`. An item you cannot evidence means the PR is not ready: fix it or tell your lead. The lines go in the PR body under a heading that is exactly `## Acceptance walk` (`factory task update --status in_review` refuses a coder without it).
   PR body size: `gh pr view <url> --json body -q .body | perl -0pe 's/<details.*?<\/details>//gs; s/<!--.*?-->//gs' | wc -w` must print at most 300 (target 150-250); `in_review` refuses more, and also refuses a body missing a template heading (.github/pull_request_template.md) or a walk outside <details>.
2. Measure every quantitative limit in the spec (sentences, bytes, lines, counts, time) with a command such as `wc -c`, `wc -l`, `grep -c`, and quote the number in the PR body.
3. Copy every "PR body must ..." requirement of the spec into the PR body. Then run `gh pr view <url> --json body -q .body | grep -n '<phrase>'` for each one and confirm a hit.
4. Run each new or changed test file 3 times, one file per run: `python3 -m unittest discover -s tests -p test_<name>.py` (repeat 3x; a wildcard pattern is a full run and the guard refuses it). Then `git fetch origin && git merge origin/main` and run `python3 ci/affected.py --base origin/main --quick --run` on that tree and quote its output (under 2 min; `FULL needed: rely on CI` means CI runs the rest; never a full suite locally). Any intermittent failure is a bug (coder.md C2): fix it, do not retry it away.
   Test count: count `def test_` on the tree of `git merge-tree --write-tree origin/main HEAD` (the PR merged into current main) and on origin/main; the merged count must be >= the main count. Also confirm the PR removes or renames no `def test_` relative to its merge-base (`git diff $(git merge-base origin/main HEAD)..HEAD`). Put both numbers in the PR body.
5. Run `python3 -m unittest discover -s tests -p test_no_duplicate_defs.py`. Fix a duplicate def or dead code you introduced.
6. Before `factory task update <T> --status in_review`, wait for every check to be terminal and green with skill `coder-skills:ci-wait` (one background watch, no foreground polling). A red check on your head is yours until shown otherwise; check `gh pr view <url> --json mergeable` first (L-013).
7. Repeat steps 1-6 before pushing a retry, and list which findings from the earlier round each line answers.

Stop and tell your lead if a spec item is impossible or ambiguous, or if a check fails for a reason outside your diff (name the check and the output).

Sources: T-0147 retro notes 1-4, L-013.
