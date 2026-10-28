---
name: plan-quality
description: Rules for plans, build tasks and acceptance criteria that touch shared contracts or guards. Use when writing a plan, filing build tasks, writing a spec or acceptance criteria, or when a task changes a CLI output or exit code, a DB column or enum value, a settings or deny entry, a role prompt, a message format, shared files or docs, or a hook, guard or CI check.
---

# Plan quality

Eight rules (P1-P8) to apply before you file or approve a build task.

Follow these steps in order.

1. Shared contracts (P1). If the task changes something other code or roles depend on (CLI output or exit codes, a DB column, a settings or deny-list entry, a role prompt step, a message format), the spec must list every consumer: files, roles, and open tasks of other goals. For each consumer it says how it keeps working and the command that checks it. Find consumers with `grep -rn` over the repo and `factory task list` for open tasks.
2. Guards in acceptance (P2). Every hook, deny rule, CLI refusal or CI check gets acceptance criteria covering both outcomes: a case that gets blocked and a case that is let through. Each case runs through the real entry point (the actual hook, command or workflow), not only a helper function.
3. Shared files (P3). Tasks that edit the same file run in sequence: add them with `--after <earlier task>`. `--after` only works inside one goal. When another goal has an open task on the same file, name that task and file in the spec so your lead can agree an order with the other lead.
4. Merge conditions are acceptance (P4). Anything the lead or pm will hold the merge to is a numbered acceptance item, never only a note, a message or a reviewer nit.
5. New state values (P5). A spec that adds a status or enum value, or changes what one means, lists every code path that reads that column (`grep -rn`), with the expected behaviour for each.
6. Amended rules (P6). When a rule changes after a FAIL, restate the whole rule with at least one input -> expected output example, including the case that broke.
7. Shared docs (P7). Parallel tasks of one goal never edit the same docs page or page list. Give the shared edit to one task, or chain the tasks with `--after` (P3 applied to docs/).
8. Prompt budget (P8). A task that adds text to a role prompt states the byte delta and the current headroom (_common.md + role <= 12000 B). New rule text goes into a plugin skill when headroom < delta + 200 B. Never compress existing rule wording to fit.
9. Re-read each task spec against P1-P8 before you report the plan ready. A task that fails a rule is fixed in the spec, not left for the reviewer.

Escalate to your lead when a consumer list cannot be completed (unknown owner, unclear impact) instead of guessing.

Source: coding-policy ideas, our wording (docs/planning-rules.md).
