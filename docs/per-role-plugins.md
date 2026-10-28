# Per-role plugins

`factory spawn` passes `--plugin-dir roles/plugins/<role>` to an agent when that directory exists, so a role loads only its own text-only skills (today `coder`, `director`, `lead`, `pm`, `reviewer` and `staff-engineer`). Plugins contain no hooks, MCP config, scripts or HTML; `tests/test_plugins.py` enforces this. Persistent agents pick up a new plugin when they are relaunched. To add a skill, follow `roles/plugins/AUTHORING.md`; `tests/test_skill_authoring.py` lints every SKILL.md front matter.
