---
name: worker-brief
description: Checklist to run before every worker spawn so the brief is complete and safe. Use when about to spawn a pm, coder, qa, reviewer, judge or cleaner, or when writing or editing a worker's task spec.
---

# Worker brief checklist

Run through this before every `factory spawn`.

1. The full spec and acceptance are stored in the DB. `factory task show T` must show both. /tmp and scratch files vanish on reboot (M451).
2. The brief lists the only live factory commands a worker may run:
   - own `factory task show`, `factory task update`, `factory task note`
   - `factory inbox`, `factory msg`, `factory done`
   - `factory verdict` (qa and reviewer only)
   - read-only `factory goal show <own goal>`

   All other factory commands run only against a temp FACTORY_HOME.
3. Coder specs say: "merge origin/main before opening or updating the PR" (M605) and "test count >= main; renames must not strip test_ prefixes" (M640).
4. Never grant an "owner exception". Only a boss `--kind decision` message that quotes the owner authorises one; cite its M-id in the brief (M343/M347).
5. Coder briefs say: a denied command stays denied in every form; stop and report (L-023).
6. Spawn: `factory spawn <role> --task T`.
7. Merge conditions you will enforce are numbered acceptance items in the DB before spawn (P4); a condition added later goes through the spec-change skill.
8. Spawn the judge only after `gh pr checks <url>` shows no pending check.
9. Test-first pilot (pilot-test-first): if the build task's FILES touch roles/hooks/, roles/settings/, scout/nsbox.py, scout/workerbox.py, or bin/factory cmd_judge / refresh_branch / cmd_refresh / head_has_main / verdict_carries, and fewer than 5 pilot tasks exist, follow docs/test-first-pilot.md.
10. Reviewer brief for a gated PR (diff touches roles/hooks/, roles/settings/, ci/ or .github/, or is over 500 changed lines): include the line "Gated PR: run the multi-angle-review workflow (reviewer skill multi-angle-review) before your verdict." The Workflow tool runs only when asked. Ungated PR: leave it out.
11. QA brief for a guard or workflow task: ask in so many words for the 1-agent guard-probe workflow, naming the denied and the allowed command to probe.
