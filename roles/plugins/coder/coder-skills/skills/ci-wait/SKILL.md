---
name: ci-wait
description: Wait for PR CI in a background Bash task instead of foreground polling. Use when a PR is pushed and its checks must reach a terminal state (before in_review, after a fix push, before factory done).
---

Purpose: CI takes up to 35 min and a foreground Bash call times out at 10 min, so a foreground `--watch` is re-run several times and every re-run is a turn that re-reads the whole context. One background watch costs one turn.

Follow these steps in order.

1. Start exactly one watch with the Bash tool, with `run_in_background: true`:
   `Bash(command: "gh pr checks <url> --watch --fail-fast", run_in_background: true, description: "Watch CI for <T>")`
2. End your turn. Do not poll, do not `sleep` (foreground sleep is blocked), do not re-read the output file, do not start other long jobs while waiting. You are re-invoked when the background-task notification arrives.
3. On the notification, read the result once: `gh pr checks <url>`. All green: continue (in_review, then `factory done`).
4. On red: check `gh pr view <url> --json mergeable` first (L-013), fix what your head caused, commit, push, then repeat from step 1 with one new watch. A red check outside your diff: tell your lead with the check name and output.

Local = fast, CI = full (docs/testing.md): CI is where the full suite, go and integration run. Locally you ran only lint and `ci/affected.py --quick`. Read a red check with `gh run view <run-id> --log-failed` (run id from `gh pr checks <url>`), reproduce with only the failing module (`python3 -m unittest discover -s tests -p test_<name>.py`), never the full suite. Never export GIT_* env (L-050).

Rules:
- Never more than one watch per PR at a time; wait for the notification of the old one before starting a new one.
- Kill only your own background job (`kill %job` / `kill $!`), never `pkill` or `killall` (L-022).
- `gh pr merge` stays denied; merging is the judge's job.
