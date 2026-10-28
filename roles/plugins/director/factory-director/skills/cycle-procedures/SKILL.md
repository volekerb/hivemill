---
name: cycle-procedures
description: Detail text moved out of director.md to fit the role budget (staffing, judging leads, reporting, scout reports, escalations). Use when running the director cycle steps on staffing several teams, chasing status, scoring leads, housekeeping, reporting to the boss, or recording decisions.
---

# Cycle procedures

These blocks are moved verbatim from director.md; the rules stay in force. Use them in the cycle steps that point here.

3. **Staff the goals** (`factory goal list`).
   - A goal in `proposed` or `planning` with no team gets one: `factory team new --goal G-xxx --scope "…"`. This spawns its lead.
   - Large goals may get several teams with non-overlapping scopes, but only if capacity allows. The tick parks idle leads and wakes them on new work.
   - Order by priority. Do not start a new team when priority-1 work is queued behind the gate.
   - Leads are optional (D1): you may staff a goal's workers yourself; see the delivery skill.

Resources (step 2):
- If swap is climbing or MemAvailable is under 1.5 GB, pause the lowest-priority team: tell its lead to finish the current step and not spawn more.
- Weekly usage over 75%: run only priority-1 goals and tell the boss.

Steps 4-8:
4. **Chase status.** If a team hasn't reported in about 30 minutes, ask its lead: `factory msg lead:<team> "status?" "progress, blockers, next step, ETA" --kind request`. If a team has no movement after two requests, investigate (`factory goal show`, `herdr agent read <lead> --lines 40`) and intervene.
5. **Unblock.** Resolve cross-team conflicts, such as two teams touching the same files, and clear resource contention. Agent health problems (blocked >10 minutes, lost panes) arrive as alerts; respawn or reassign.
6. **Judge the leads.** After each merged task or closed goal, score the team: `factory eval <team> --score 1-5 --notes "…"`. Score outcomes: merged and working, low rejection and retry counts (`factory scorecard`), honest reporting, low waste. Give the lead concrete feedback. A lead who scores ≤2 twice is replaced: close the team and staff a new one with a better brief.
7. **Housekeeping.** When `factory res` shows many worktrees, when tasks were merged since the last clean, or once a day, spawn a cleaner: `factory spawn cleaner --prompt "clean merged worktrees and branches"`.
8. **Report up.** When something meaningful changes (goal planned, task merged, goal blocked or done), send the boss one concise report: `factory msg boss "…" "…" --kind report --goal G-xxx`. Do not report on every cycle when nothing changed.

Daily digest:
A daily digest (done, in progress, blocked, team scores per goal) is sent from you to the boss each morning by the tick, with no action needed from you. `factory digest` shows it at any time.

Skill scout reports:
Skill scout reports ("skill scout <date> <slot>") arrive after each scout run (09, 18): fold the latest into the 18:00 owner report to the boss; adopt and executable-part items go to the owner as decisions. (G-009)

Escalations (the owner-call list is in the owner-decisions skill):
Pass to the boss only what is genuinely the owner's call; see the boss role for the list. Everything else you decide, and you record the decision: `factory msg <lead> "decision: …" --kind decision`.
