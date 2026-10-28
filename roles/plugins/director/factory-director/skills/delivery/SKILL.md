---
name: delivery
description: How the director owns goal delivery (rules D1-D5, owner M7486): direct worker dispatch, per-goal delivery plan, daily delivery report with slips and recovery actions. Use when staffing a goal without its lead, when a lead is parked or idle with ready tasks, when writing or updating a goal's delivery plan, or when sending the daily delivery report.
---

# Delivery (director)

Purpose: every active goal is delivered on time (D1-D5 in director.md). Owner order M7486, verbatim: "i want goals to be conpleted. Director shall find out and be accoontable for the delivery in time. It has all resources management and can spawn any agents needed to accomplish work either coding or qa or review or making pm work"

Direct dispatch (D2, D5):
1. Find ready work: `factory task ready --goal G-xxx`.
2. Spawn the worker yourself, P1 goals first: `factory spawn <role> --goal G-xxx --task T-xxxx` (pm, coder, qa, reviewer). A coder or qa only gets a task with spec and acceptance; a task without them gets a pm first.
3. Stay inside the spawn gate and owner caps (factory config max_live_agents / max_live_leads; M5894). Never change a cap to make room (R4).
4. If the goal has a lead, tell it: `factory msg lead:<team> "dispatched T-xxxx" "<role> <agent>" --kind report`. The lead keeps sequencing (deps, --after, priorities).
5. When the tick's lead-less dispatch (T-0467, M7462) is merged, turn on its config switch instead of spawning by hand for parked teams.

Delivery plan (D3):
6. One per active goal: remaining tasks, target date, critical path, risks. Record it in the DB: `factory msg boss "delivery plan G-xxx vN" - --kind report --goal G-xxx` with the plan as a heredoc body. Send a new version when the date, the critical path or a risk changes.

Daily delivery report (D4):
7. Once a day, with the 18:00 report to the boss: `factory msg boss "delivery YYYY-MM-DD" - --kind report`, body = the R6 utilization line first, then one line per active goal:
   `G-xxx done D/T, plan D'/T by YYYY-MM-DD; slip: <what>, recovery: <action, owner, new date>`
   D/T = tasks done / tasks not cancelled (`factory goal show G-xxx`).
8. Every slip (behind plan or past its date) names a recovery action. A slip without one counts against R7.

Stop and escalate: a goal cannot make its date inside the caps -> tell the boss with options and a recommendation.
