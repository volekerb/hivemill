# Authoring per-role skills and role prompts

Factory-authored guide. Follow it when you add or change a skill under `roles/plugins/` or a role prompt under `roles/`.

## 1. Where skills live

- Layout: `roles/plugins/<role>/<plugin>/.claude-plugin/plugin.json` and `roles/plugins/<role>/<plugin>/skills/<skill>/SKILL.md`.
- `factory spawn` passes `--plugin-dir roles/plugins/<role>` for that role only, so a skill loads only for its role.
- Plugins are text only: no hooks, MCP config, scripts, executables or HTML (`tests/test_plugins.py` enforces this).
- A directory directly under `roles/plugins/` that is not a role name (for example a future `goals/` tree) is not a role and is never loaded by spawn.

## 2. Front matter

Every SKILL.md starts with a `---` block. `tests/test_skill_authoring.py` checks it.

- `name`: lowercase letters, digits and single hyphens, at most 64 characters, equal to the skill's directory name.
- `description`: at most 1024 characters. Say what the skill does, then give trigger phrases that start with "Use when" (or "Use whenever") and name the concrete situations, commands or words that should load it.

Example:

```
---
name: spec-change
description: Stop-and-respawn procedure for changing a running task's spec. Use when a running worker's spec, acceptance or scope must change.
---
```

Values may be bare, quoted on one line, or a `>` / `|` block scalar.

## 3. Body

1. Write a one-line purpose.
2. Write "Follow these steps in order."
3. Write flat numbered steps. One action per step, with the exact `factory`, `gh` or `git` command.
4. End with the stop and escalate conditions, and the lesson or message ids the skill comes from.
5. Keep SKILL.md under about 60 lines. Put long reference material in a sibling `.md` file that the skill names.

## 4. Script delegation

- Work with exactly one right answer (parsing, counting, validating, comparing, changing DB state) belongs in a `factory` subcommand in `bin/factory` with unittests. It does not belong in skill prose.
- Never put a script under `roles/plugins/` (banned there).
- The skill calls the command and interprets its output. Judgement stays in the skill.
- If the command does not exist yet, ask your lead for a coder task. Do not write the logic out as prose steps.

## 5. Role prompt style

`roles/_common.md` and `roles/<role>.md` are loaded into every spawn of the role.

- Write short imperative rules, one line each, with the source id in parentheses. No justification prose.
- Move long guidance, examples and checklists into a per-role skill.
- Size budget: bytes(`_common.md`) + bytes(`<role>.md`) must be at most `ROLE_PROMPT_BUDGET_BYTES` (12000). `tests/test_repo_hygiene.py` (G-007) enforces it.
- Raising the budget is its own PR. Prefer moving detail into a skill.

## 6. Provenance

- All skill text is factory-authored.
- Third-party text needs owner approval through the boss (G-002) and a vendoring table in that role tree's README, as `roles/plugins/staff-engineer/README.md` has.
- Vendored files stay byte-for-byte. Do not edit them.

## 7. Checklist for adding a skill

1. Create the directories.
2. Write the front matter.
3. Write the steps.
4. Run `python3 -m unittest discover -s tests -p "test_plugins.py"` and the same with `-p "test_skill_authoring.py"`.
5. If it is a new role tree, add the expected child list in `tests/test_plugins.py`.
6. Open the PR. It goes through reviewer, QA and judge.
7. Link the lesson it applies with `factory lesson link` when that command exists.
