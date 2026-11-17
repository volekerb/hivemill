---
name: lanes-and-holds
description: Rules L1-L4 for file lanes and H1-H5 for judge holds (G-058, owner decision M37547). Use when about to block a task for a file lane, before setting or keeping a judge hold, and at every cycle for PRs open over 4 h.
---

# Lanes and holds (director)

File lanes and judge holds were the two largest waits that are decisions, not machinery (diagnosis M37505). Apply these rules before you block a task or hold a PR.

## Lanes

L1 No lane by file name. Two tasks that edit the same file run in parallel unless `lane-check` shows a real conflict (docs/lanes.md).
Why: the single-writer lane on bin/factory_cli.py was set by message (M33724, M35023, M37042); PR 629 sat blocked 25.8 h.

L2 Before blocking a task for a file, run `lane-check plan <branch> --against-open`. CLEAN: start it. CONFLICT: order only the conflicting pair; the later task merges origin/main after the earlier PR merges.
Why: 30 tasks and 505 task-hours waited in 3 days behind the lane, PR 629 among them, mostly on pairs git merges cleanly.

L3 A task not yet started that will edit a large shared file starts anyway. Its coder merges origin/main before the PR and again when a conflicting PR lands first. The stuck-PR program refreshes or wakes the coder on conflict.
Why: tasks queued behind PR 629 did not start at all; a refresh costs minutes, a queue cost a day.

L4 A lane block that remains must name the one PR it waits for and is re-checked with lane-check every cycle. No chains: a task never waits for a task that itself waits for a lane.
Why: chains behind PR 629 turned one 25.8 h block into many.

## Holds

H1 A judge hold names one unblocking event that someone is working on now, and who. No hold without both.
Why: PRs 627, 630, 631 and 632 were PASS/PASS and held 22 h each for evidence nobody was producing.

H2 Never hold a PASS/PASS green PR for post-merge evidence, for a release note, or for merge order with a PR it does not conflict with. Merge order is only for pairs where lane-check shows CONFLICT.
Why: the holds on PRs 627, 630, 631 and 632 were for evidence later ruled post-merge, then for "train order".

H3 Never set one hold on many PRs. A red main already stops merges; a hold on top hides which PR waits for what.
Why: "hold until PR 703 merges" was set on 15 open PRs (M37392).

H4 A hold on a reviewer+QA PASS PR expires after 2 h (tick unit hold_expiry, docs/judge-hold-expiry.md). To hold longer, get a boss decision and put `boss-approved M<id>` in the hold text. Any other hold older than 4 h is cleared or re-justified in a task note at the next cycle.
Why: manual holds cost 150 PR-hours; PRs 627, 630, 631 and 632 waited 22 h each with no expiry.

H5 QA could not run a command (sandbox denial): do not hold the PR for a later re-QA. Decide in the same cycle: the CI job is the proof for that item, or another QA runs it.
Why: part of the 22 h holds on PRs 627, 630, 631 and 632 was waiting for a re-QA that could not run either.

## Check

- `cd go && go run ./cmd/lane-check plan <branch> --against-open` (CLEAN or CONFLICT per pair).
- `factory task show <T>` for the hold text and its age (`judge-hold-set` event).
- `factory task update <T> --judge-hold-clear` to clear a hold that breaks H1-H5.

Judge, reviewer PASS, QA PASS and green CI on the merged head stay required; no rule here waives them. A conflicting PR still cannot merge until it is refreshed and green.
