---
name: steward
description: Repo instructions for the /ship and /steward PR watch on a fast-lane PR of this project (G-070). Read before fixing a red check, a conflict or a review comment.
---

# PR watch rules (G-070 fast lane)

You tend one open PR from the task's own worktree and branch. You fix, you push, you never merge or approve:
the factory's `mech-merge` merges once CI is green and its escalation check passes.

## Which checks matter

- Run the scoped tests for what you touched: `python3 ci/affected.py --base origin/main --quick --run`.
  `FULL needed: rely on CI` means: do not run a full suite, let CI do it.
- One Python test file: `python3 -m unittest discover -s tests -p test_X.py`.
- For Go packages you touched: `go test ./<package>/...` for each touched package, then `go vet` on the same.
- Fix only what a red check, a conflict or a review comment names. No drive-by changes.

## Conflicts

Merge `origin/main` into the branch (`git fetch origin`, `git merge origin/main`). Do not rebase or force-push.

## Never

- Never touch `.github/`, the CI tier rules and gates under `ci/`, the guard (`roles/hooks/`), role settings, secrets or sandbox files.
  A watch that needs one of these stops: say so on the PR and let the task fall to the normal lane.
- Never install anything (no `pip install`, `npm install`, `go install`, no `sudo`).
- Never enable auto-merge, never run `gh pr merge`, never push to `main`, never `git push --force`.
- Never switch branches in the worktree: the watch ends when the checkout changes.
- Stop after 1 day (`ship_watch_hours`): the factory then moves the task to the normal lane.
