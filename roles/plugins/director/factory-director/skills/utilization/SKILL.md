---
name: utilization
description: How to measure and raise worker utilization, check goal starvation and report the utilization line (director rules R1-R6, G-018). Use when running the cycle utilization check, when workers are under target, a P1 goal has no worker, a lead is idle, or when writing a report to the boss.
---

# Utilization

Purpose: keep real workers busy and every active goal progressing (rules R1-R6 in director.md).

Follow these steps in order.

1. Read the line from `factory res`: `workers live N · leads live L/4 (parked P, ...)` gives the live worker and lead counts. Only if it is missing, count with `factory agents` (live/starting rows by role; queued ones are listed too).
2. Compute: W = coder+qa+reviewer+pm live, L = leads, J = judges, HQ = boss+director, Q = queued. Target W >= min(10, 60% of `max_live_agents`); L + J <= 1/3 of live slots.
3. Write the line: `live 22: workers 11 / leads 4 / judges 3 / HQ 2 / queued 5; P1 goals without workers: none`.
4. Starvation (R2): list active goals with `factory goal list`; for each P1 goal check that a worker is live or queued on it. A starved goal: `factory task ready --goal G-xxx`, then tell its lead or spawn the worker.
5. Idle lead with no work: ask it to `factory park` (works when config `lead_parking=1`), or close its team with `factory team close <id>` if the goal is done.
6. Judge waiting on CI (R5): `gh pr checks <url>`; if not all green on the current head, `factory stop <judge> --reason "CI pending"`. Never more than 3 judges.
7. Spawn queued workers, P1 goals first: `factory spawn <role> --goal G-xxx --task T-xxxx`. Never change a cap to make room (R4).
8. Lead with ready tasks and no action for 30 min: `factory msg lead:<team> "nudge" "ready tasks: T-..., act now" --kind request`. At 45 min: `factory relaunch <lead> --fresh`.
9. Keep the queue full (R3): when a goal has fewer than 2 ready tasks, ask its lead for specced tasks.
10. Start every report to the boss with the line from step 3.

Stop and escalate: a cap or setting change seems needed (cite the owner M-id and ask the boss); a breach happened (report it to the boss the same cycle).
Source: owner order 2026-10-27, M6373, M6377.
