---
name: owner-decisions
description: Decide what the director settles itself, what goes to the owner and what defaults after 2 daytime hours (owner standing rule M833). Use when an owner question, approval, cap or default comes up, or before sending anything to the boss for the owner.
---

# Owner decisions

Owner standing rule M833 comes first.

1. **Internal designs: you approve them.** Architecture, task splits, test plans and role-prompt wording are yours. Do not ask the owner.
2. **Owner-reserved: NEVER defaulted.** The question stays open until the owner answers:
   - third-party code
   - security changes
   - new recurring cost
   - anything irreversible or on the owner's machine/accounts
   - changes to how the owner works
3. **Everything else sent to the owner:** the recommended option applies if there is no answer within 2 daytime hours. The boss tells the owner it was applied.

## Routing
- Send owner questions to the boss (`factory msg boss "<subject>" "<body>" --kind request`). Never message the owner.
- Batch questions, one message per decision. Each has options, a recommendation, and whether it is owner-reserved.
- Quote owner approvals verbatim with their M-id. Never restate a number or scope from memory (M121, M125).
- Never invent an "owner exception". It exists only as a boss `--kind decision` message quoting the owner (M347).
- Never stall unrelated work while waiting for an answer. Keep every other ready task moving (M404).

## Commands for the owner or boss (M10432)
Before any message asks the owner or boss to run a command, check it on current main and quote the check:
1. `factory <cmd> <sub> --help` lists the flag for that exact subcommand (a parent `--help` listing all subcommands does not prove it).
2. Required args are valid, and a `--dry-run` was run where one exists.
3. If the command cannot work yet (flag unmerged, PR blocked by a guard), say so instead of asking the owner to run it. The boss rejects requests without the quoted check.

Sources: M833, M407.
