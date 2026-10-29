# factory refresh

`factory refresh <T-xxxx> [--dry-run]` brings a clean-but-stale PR branch up to `origin/main` with one mechanical
merge commit. No coder is spawned and no working tree is used. Allowed roles: lead, director (owner too); any other
role exits 2.

Why: since T-0152 `factory judge --merge` refuses a head that does not contain `origin/main` (STALE-ONLY). With
several PRs in flight each merge makes the others stale, and a coder spawn per refresh is too costly (G-013, M2632).

## Usage

```
factory refresh T-0123 --dry-run   # verify + merge-tree only; prints what it would push
factory refresh T-0123             # build the merge commit and push it to the PR branch
```

The task needs a `pr_url` and a `branch`; the PR must be OPEN and its head branch must be the task branch.
After a refresh wait for CI to go green on the new head, then spawn the judge. A later coder must `git pull` the
branch first (the task gets the note `refreshed <old> -> <new> by <agent>`).

## Exit codes

| code | meaning | event |
|---|---|---|
| 0 | refreshed, or the head already contains origin/main (nothing pushed) | `refreshed` |
| 1 | task has no PR/branch, PR not open, head branch differs from the task branch | none |
| 2 | caller's role is not lead/director | none |
| 3 | merge conflict: prints `CONFLICT-ONLY: <files>`; spawn a coder with the merge-only prompt (fix-loop) | `refresh-conflict` |
| 4 | PR base is not `main` (`PR base is <x>, not main`) | `refresh-failed` |
| 4 | cannot verify (fetch failed, fetched main != `gh api` main, git config refused) or the push failed | `refresh-failed` |

## How the commit is built

1. `head_has_main` does the verified fetch the judge uses: explicit repo URL from `gh repo view`, private refs
   `refs/factory-judge/*`, replace refs and grafts ignored, fetched main compared with `gh api .../commits/main`,
   fetched head compared with the PR's `headRefOid`. All git calls go through the hardened `_git_rc` helper and
   `git_config_guard`. No new ref namespace.
2. `git merge-tree --write-tree --name-only <head> <main>`; a conflict stops here (exit 3).
3. `git commit-tree <tree> -p <head> -p <main>` as `factory refresh <factory-refresh@localhost>`. On a public repo
   (the PR repo or the project's `gh_repo` is in `public_text_repos`, T-0604) the email is a GitHub noreply address
   instead: `factory config noreply_email` (must end in `@users.noreply.github.com`), else the worktree's
   `git config user.email` when it is one. With neither, the refresh is refused (`REFRESH-IDENT: ...`) and nothing is
   pushed: `factory refresh` exits 4 (`refresh-failed`), the judge blocks (`judge-blocked`), the spawn pre-check logs
   `precheck-skipped`. This applies to all three, since they share `refresh_branch`.
   The commit is exactly a pure main merge by the T-0095 rule (docs/verdict-carry-over.md): two parents, first
   parent = old head, second = origin/main, tree = the merge-tree output.
4. `git push --force-with-lease=refs/heads/<branch>:<old head> <repo url> <commit>:refs/heads/<branch>`.

The shared checkout and task worktrees are never checked out, reset or modified.

## The lease

The push is a fast-forward of the old head, so the lease only matters when someone moved the branch after the
fetch (a coder pushing at the same moment). Then the remote ref no longer equals `<old head>`, git rejects the
push, and the command exits 4 with `refresh-failed`. Nothing is overwritten.

## The gate is not weakened

The refresh writes no verdict and skips none. It only produces a pure main merge. The T-0095 machine rule still
decides whether the reviewer/QA PASS carries to the new head; if the merge were not pure, the judge would refuse.
CI must be green on the new head, and `factory judge --merge` re-runs its fail-closed checks (STALE-ONLY, CI,
carry-over, mergeable) at merge time.

## The judge does it too (T-0230)

`factory judge --merge` runs the same core (`refresh_branch`) when a stale-only clean head is the judge's only gate
problem: refresh, wait for CI on the new head (`judge_ci_wait_s`, default 2400 s, enough for the 35-min CI limit), re-run
the full gate with the T-0095 carry-over walk, merge with `--match-head-commit <new head>`. Main merges faster than a hand
refresh plus CI plus judge, so the judge repeats the refresh when main moved again, at most **`judge_max_refreshes` refreshes per run** (config, default 4; invalid or below 1 means 4); then it
exits 4 with `STALE-LOOP: main moved N times during refresh; ...` and the lead refreshes by hand. A conflict exits 4 with
`CONFLICT-ONLY: <files>`, nothing pushed. See docs/verdict-gates.md for the gate-not-weakened argument. Events per
judge refresh: `refreshed` (actor = the judge, `"how": "by judge"`) and a `note`; refusals are `judge-blocked`.

The merge commit's second parent is always the **verified** main sha returned by `head_has_main`, never a re-read of the
shared `refs/factory-judge/main` (T-0176 review nit n1).

## One refresh per PR at a time (T-0260)

`refresh_branch` takes a non-blocking exclusive `flock` on `db/refresh-pr-<N>.lock` (one file per PR number), held from
the verified fetch through the push, so a head is never refreshed twice at once. If the lock is held it returns
`in-flight` and pushes nothing. The lock file is opened without truncation and its holder writes its own agent name into it, so
a refused caller can say who holds it (`... another refresh is pushing (lock held by <agent>); ...`). `factory refresh --dry-run`
pushes nothing and takes no lock, so it never makes a real refresh or the judge wait.
- `factory refresh`: exits 4 with `refresh in flight for PR #N: another refresh is pushing; wait for it`; no event.
- `factory judge --merge`: no refresh, none counted; it waits (`judge_ci_wait_s`) for the PR head to change, waits for CI
  on it, and re-runs the full gate (carry-over included). If the head never changes: `STALE-ONLY: refresh in flight did not land within <s>s`.
- A lost lease in the judge path is treated the same when the new head is a pure main merge of the old one; otherwise it refuses as before.
