# Judge batch train (T1: dry-run planner)

`factory judge --train T-a T-b [T-c T-d] --dry-run` plans a batch merge train and changes nothing. It is the first
step of G-018's throughput work: a train merges up to N PRs per CI cycle while keeping "merged main tree == tested tree".
Pushing the train ref and merging are later steps; `--train` without `--dry-run` is refused.

- Takes 2 to 4 distinct task ids (exit 2 otherwise; duplicates refused). No task argument or `--reason`. Judge role only. `--dry-run` on any other `judge` call is refused (exit 2).
- Per task, the same checks as `judge --merge` except staleness: PR open, not a draft, base `main`; reviewer and QA
  PASS at the current head or carried (`verdict_carries`); CI green on the head and required checks present.
  A task that fails is printed as `DROPPED T-x: <reasons>` and left out.
- Prefix trees: starting at the verified `origin/main` (`verified_main`, compared with `gh api`), each remaining PR is
  merged on top of the previous step with `git merge-tree --write-tree` and the hardened git flags. A PR that
  conflicts with the train so far is dropped, the rest continue. Each step is chained through a throwaway
  `commit-tree` object (no ref) so the next merge has the right base.
- Output: the main sha, then per step `k. <task> head=<sha> T_k=<tree> ref=factory-train/<ids>-<k>`.
- Writes: nothing on the remote and no branch or tag. Like the merge gate it fetches into the private
  `refs/factory-judge/*` refs and writes git objects locally. Exit 4 when no task is ready.

Tests: `tests/test_judge_train.py` (all ready, one not ready, stale verdict, conflict drop, more than four refused, no write).
