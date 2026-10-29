# Merge freeze on non-PR pushes to public main (T-0639, owner rule R-d)

When `origin` is listed in `public_text_repos`, the tick (inside `export`, at most every 50 s) fetches `origin/main` and checks every commit that is new since the last verified sha. A commit passes only if it is the GitHub squash of a merged PR:

- committer email is `noreply@github.com`,
- the subject ends with `(#N)`,
- `gh api repos/<repo>/pulls/N` says `merged` and `merge_commit_sha` equals the commit.

The first commit that fails:

- sets the merge freeze (`config` key `main_freeze` = the sha); `factory judge` (merge) refuses with `non-PR push on main: <sha>`,
- sends an escalation to the boss and to the director at once (any hour) and records a `main-freeze` event,
- stops the live `~/factory` checkout from syncing: it only ever fast-forwards to `main_verified:<repo>` (the last sha whose commits all passed), and not at all while frozen.

A check that cannot decide (gh or fetch failing) does not freeze and does not advance the verified sha, so nothing new is synced; it records `public-main-check-failed` at most once an hour.

On the first run the baseline is the live checkout's own `main`.

## Clearing the freeze

Only the director or boss, after verifying main (`git log`, `git diff`):

    factory main-unfreeze --reason "<what was verified>"

This clears the freeze and moves the verified sha to the current `origin/main`. Neither a tick nor `factory config` can clear it.
