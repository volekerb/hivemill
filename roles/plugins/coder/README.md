# Coder plugin

`coder-skills` is loaded (via `--plugin-dir roles/plugins/coder`) only for agents with role `coder`.
`factory spawn` passes the directory when `roles/plugins/<role>/` exists.

All text here is factory-authored; nothing is copied from third parties.

## Skills
- `guard-change`: fix the bypass class (not the instance) when changing a hook, deny rule or CLI refusal, and attach a main-vs-PR `ci/guard_diff.py` run to the PR.
- `pre-pr-check`: before in_review, `factory done` or a retry push: evidence every spec and acceptance item in the PR body, measure numeric limits, 3x test runs plus the affected-tests run (ci/affected.py) on the merged tree, CI green, no duplicate defs.
- `pr-description`: concrete PR body in the five-section template (150-250 words, exact test command, risk/rollback); no Gates block.

## Adding a skill
Add `coder-skills/skills/<name>/SKILL.md` with front matter `name:` (equal to the directory name) and `description:` (what it does, plus a "Use when ..." trigger).
Text only: no hooks, MCP config, scripts, executables or HTML. `tests/test_plugins.py` enforces this.
