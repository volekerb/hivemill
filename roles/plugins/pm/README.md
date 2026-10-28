# PM plugin

`pm-skills` is loaded (via `--plugin-dir roles/plugins/pm`) only for agents with role `pm`.
`factory spawn` passes the directory when `roles/plugins/<role>/` exists.

All text here is factory-authored; nothing is copied from third parties.

## Skills
- `audit-scope`: what the goal audit must not redo (moved out of `roles/pm.md`).
- `plan-quality`: shared-contract consumers, two-way guard acceptance and shared-file ordering in plans and specs.

## Adding a skill
Add `pm-skills/skills/<name>/SKILL.md` with front matter `name:` (equal to the directory name) and `description:` (what it does, plus a "Use when ..." trigger).
Text only: no hooks, MCP config, scripts, executables or HTML. `tests/test_plugins.py` enforces this.
