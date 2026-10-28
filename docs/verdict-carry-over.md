# Verdict carry-over

Reviewer and QA verdicts survive a pure `git merge origin/main`. `factory judge` accepts a PASS recorded at head X for the PR's current head Y when Y == X, or when every commit on the first-parent path from Y back to X is a pure main merge. For each such commit C:

1. C has exactly two parents, and its first parent is the next commit on the path (the last step's first parent is X).
2. Its second parent is reachable from `origin/main` (`git merge-base --is-ancestor`, after `git fetch origin main`).
3. The merge has no manual edits: `git merge-tree --write-tree <first parent> <second parent>` exits 0 and prints the same tree as `C^{tree}`. A conflict-resolved merge or a merge with extra edits is refused.

The walk covers at most 20 commits. If X is not reached, the verdict is stale. CI green on Y, open PR, not a draft, mergeable and the required checks are unchanged.

- The helper `verdict_carries` runs in the task's worktree (else FACTORY_HOME) and only fetches main and `pull/<n>/head`, each with a 60 s timeout. Every git call runs with `GIT_NO_REPLACE_OBJECTS=1` and `GIT_GRAFT_FILE=/dev/null`, so replace refs and grafts cannot disguise a commit. It does not trust the shared git config: it fetches from the URL given by `gh repo view` with an explicit refspec into private refs (`refs/factory-judge/*`), then refuses unless the fetched main equals `gh api repos/<owner>/<repo>/commits/main` and the fetched PR head equals the head being judged. A failing `merge-base --is-ancestor` (exit other than 0/1) is reported as a git error, not as "not on origin/main". It never checks out, resets or changes a working tree.
- An accepted carry records a `verdict-carried` event per role (`role`, `from`, `to`, `main`, `merges`) and prints a line saying so, only after `gh pr merge` has succeeded; a failed merge records no carried event.
- A refused carry keeps the usual text and appends the reason, e.g. `reviewer PASS is for 1234abcd, head is now 5678ef01 (not a pure main merge: tree differs at 9abc0123)`.
- Git hardening (hooks, fsmonitor, merge drivers, filters, attributes, commit-graph) and the `gh api` timeout refusal are described in [git-hardening.md](git-hardening.md).
