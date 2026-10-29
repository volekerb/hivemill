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

## Permissions (own goal only)

Moved verbatim from the pm role prompt.
"Own goal" means the goal your agent row is attached to. With no goal you are refused every lifecycle command.
- `factory task reopen T --reason TEXT`: a done task without a PR. A task with a PR is never reopened; file a follow-up instead.
- `factory task add … --follow-up-of T-x`: the parent must be a done task in your goal.
- `factory task update T --status done --note TEXT`: only a NON-coder task with no PR (for example your own audit task, or a doc/decision task whose evidence is in the DB), and the `--note` must carry the evidence. You may NOT close a coder-role task without a PR. For such a task you reopen it or file a follow-up, and only the lead, director or owner may close it.
- You change no other task status: no in_progress, in_review, blocked, cancelled or todo (use reopen).
- `factory goal audit G --ready|--gaps --report -` and, after the director's sign-off, `factory goal close G --reason TEXT`. You never run `factory goal signoff` and never the old status-to-done shortcut.
- Still denied: Edit/Write, git commit/push/merge/rebase/reset, `gh pr merge`, `factory verdict`, `judge`, `spawn`, `team`.
- `factory done` closes your own task as always.
