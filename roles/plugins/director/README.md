# Director plugin

`factory-director` is loaded (via `--plugin-dir roles/plugins/director`) only for agents with role `director`.
`factory spawn` passes the directory when `roles/plugins/<role>/` exists, so the director picks it up when it is relaunched.

All text here is factory-authored; nothing is copied from third parties.

## Skills
- `owner-decisions`: what the director settles, what goes to the owner, and the 2-daytime-hour default (M833).
- `utilization`: measure utilization, check goal starvation, report the utilization line (G-018 rules R1-R6).
- `cycle-procedures`: detail text moved verbatim out of director.md to fit the role budget.
- `restriction-check`: list every automated writer before proposing a restriction (M733).

## Adding a skill
Add `factory-director/skills/<name>/SKILL.md` with front matter `name:` (equal to the directory name) and `description:` (what it does, plus a "Use when ..." trigger).
Keep it imperative with exact commands, under about 60 lines.
Text only: no hooks, MCP config, scripts, executables or HTML. `tests/test_plugins.py` enforces this.
