# Lead plugin

`factory-lead` is loaded (via `--plugin-dir roles/plugins/lead`) only for agents with role `lead`.
`factory spawn` passes the directory when `roles/plugins/<role>/` exists, so persistent leads pick it up when they are relaunched.

All text here is factory-authored; nothing is copied from third parties.

## Skills
- `spec-change`: change a running worker's spec, acceptance or scope.
- `worker-brief`: checklist before every spawn.
- `pr-triage`: what to check before blaming CI for a stuck or red PR.
- `fix-loop`: retries, no-work stalls, nit-only findings and diagnosis before the third attempt.
- `blocked-worker`: answering a worker stuck on a permission dialog (L-021).
- `park`: hand the pane back with `factory park` when the next step is queued or the goal waits; what to check first and what to write.

## Adding a skill
Add `factory-lead/skills/<name>/SKILL.md` with front matter `name:` (equal to the directory name) and `description:` (what it does, plus a "Use when ..." trigger).
Keep it imperative with exact commands, under about 60 lines.
Text only: no hooks, MCP config, scripts, executables or HTML. `tests/test_plugins.py` enforces this.
