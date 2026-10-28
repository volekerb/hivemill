# Planning and retry rules

Two per-role skills carry the planning and retry rules. They are loaded on demand, so the always-loaded role prompts stay inside the byte budget.

## `plan-quality` (pm)
File: `roles/plugins/pm/pm-skills/skills/plan-quality/SKILL.md`. Applies when writing a plan, build tasks, a spec or acceptance criteria.
- P1 Shared contracts: a task that changes something others depend on lists every consumer, how each keeps working, and the check command.
- P2 Guards in acceptance: every hook, deny rule, CLI refusal or CI check gets one test it must reject and one it must accept, each run through its real entry point.
- P3 Shared files: tasks on the same file run in sequence (`--after`); cross-goal files are named in the spec.
  `factory task add --spec` and `task update --spec` now warn on stderr when another open task names the same repo files (same goal: chain with `--after`; other goal: name it in the spec); the CLI never refuses.
  A named file is a tracked file or a new path in a tracked directory whose last component has an extension (leading `./` ignored); prefixes and globs like `tests/test_` are skipped, so an extensionless new file such as `bin/newtool` is not counted.
- P4 Merge conditions are acceptance: anything the merge is held to is a numbered acceptance item, never only a note or reviewer nit.
- P5 New state values: a spec that adds or re-means a status/enum value lists every code path that reads that column, with expected behaviour.
- P6 Amended rules: restate the whole rule with an input -> expected output example, including the case that broke.
- P7 Shared docs: parallel tasks of one goal never edit the same docs page; give it to one task or chain with `--after`.
- P8 Prompt budget: a task adding role-prompt text states the byte delta and headroom; rule text goes into a skill when headroom < delta + 200 B; never compress existing wording.

## `fix-loop` (lead)
File: `roles/plugins/lead/factory-lead/skills/fix-loop/SKILL.md`. Applies to FAIL or reject results, retries, lost workers and nit-only findings. `roles/lead.md` has one pointer line to it.
- L1 No-work stalls are respawned and not counted as an attempt; partial work is described to the next coder.
- L2 Retry briefs say "attempt N of 3", list every open blocking finding with its source, and give the coder ownership.
- L3 Before the third attempt the lead asks the staff-engineer for one remedy (continue, restructure, stop) and puts it in the brief.
- L4 Nit-only findings and evidence-only judge rejects never earn a coder round.
- Nit-only FAIL: the lead respawns the same verifier to re-verdict; that is not a coder and not an attempt (fix-loop L4).

## Reach
The skills load for new pm and lead spawns. A running persistent lead sees them after a relaunch.

## Tests
`tests/test_prompt_rules_planning.py` checks one marker per rule and the `lead.md` pointer.

Source: pm/lead rules: ideas from gAmUssA/coding-policy (cross-boundary-changes, testing-standards, herdr-teamlead team contract) @7a25169 (declares MIT, no LICENSE file); our wording.
