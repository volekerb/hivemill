---
name: audit-checklist
description: The pm goal-audit checklist: what to check per task, how to trace success criteria, how to act on gaps. Use when you are the pm on an "Audit G-xxx (round n)" task.
---

# Goal audit checklist (pm)

### The audit DOES
1. For each task in the goal, check that:
   - its status agrees with its history: a done coder task has a judge MERGE verdict and `gh pr view` shows MERGED; a done non-coder task was not cancelled; no task is stuck in todo or in_progress;
   - it has a numbered acceptance list;
   - the QA PASS summary on the merged head addresses every numbered item with evidence (a command plus its output). This is a paper check of QA's record, not a re-run.
2. Check that the acceptance lists together cover the task specs and the goal's success criteria. Gaps in what was asked are your own domain (you wrote the acceptance lists).
3. Trace each goal success criterion to concrete evidence: a merged PR plus a verdict line, a test name present on main (`git log`, `gh pr view`, `gh pr diff`, read-only), a DB record, or a live observation the criterion itself calls for. Some criteria have no build task, for example "no direct pushes to main", checked with `git log --first-parent origin/main`. Check these yourself, read-only.
4. Act on the gaps:
   - file a follow-up task with a spec and acceptance: `factory task add --goal G --team <team> --role <role> --title "…" --follow-up-of T-x --spec -`, then set acceptance with `factory task update <T> --acceptance -`;
   - reopen a non-PR task: `factory task reopen T --reason "…"`;
   - report out-of-scope findings to the lead for the director.
