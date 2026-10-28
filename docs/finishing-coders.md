# Finishing (coders)

`factory done` for a coder checks its worktree first, using local git refs only (no fetch):

- Modified or staged tracked files, or HEAD commits not on `refs/remotes/origin/<branch>`, are refused with exit 3 and nothing changes. A coder with no commits beyond the base may finish. Untracked files only produce a warning.
- Red or pending CI on the task's PR only warns, and the lead's summary gets a `CI at done: <state>` line; an unreachable `gh` prints `CI state unknown`.
- `factory done --force` skips the dirty/unpushed refusal, records a `done-forced-unclean` event and tells the lead `closed with --force: <details>`.

Source: Coder done guard: idea from gAmUssA/coding-policy stop-handoff-hygiene @7a25169 (declares MIT, no LICENSE file); our own code.
