# Lead-less dispatch

When a team's lead is parked, its ready tasks used to sit idle until something woke the lead. With `lead_less_dispatch=1` the tick starts the worker itself and only informs the parked lead. Leads keep sequencing (`--after`, priorities); they stop being the only path to a spawn. It is off by default.

## What qualifies

A task is a candidate when all of these hold (`dispatch_candidates`, DB only):

- status `todo` and every task in `depends_on` is `done` (the `factory task ready` rule);
- role is `pm`, `coder`, `qa`, `reviewer` or `cleaner` (never `judge`: leads and the merge queue own judges);
- the spec is non-empty; for `coder` and `qa` the acceptance is non-empty too (the same refusal as `factory spawn`);
- the task has a team, the team is `active` and parked, and its lead agent is not live, starting or queued (a lead that is waking sequences itself);
- the goal is `active`;
- no agent on the task is live, starting or queued.

Order: goal priority, then task priority (lower first, unset last), then task id.

## Modes (`factory config lead_less_dispatch <mode>`)

- `0` (default, also any unknown value): off. Nothing runs, `factory res` is unchanged.
- `shadow`: records one `dispatch-shadow` event per candidate (at most once per task per hour). No spawn, no mail.
- `1`: the tick (step 2b, after the queue is drained, before nudges) spawns workers through the normal `spawn()` and its gate, named `<role>-<task id>` (e.g. `coder-t-0123`).

## Caps

- `dispatch_max_per_tick` (default 2): lead-less spawns per tick run.
- `dispatch_max_team_workers` (default 2): no spawn while the team already has this many worker agents live, starting or queued.
- The gate is checked before each spawn; a closed gate, or a spawn that ends up queued, stops the loop. Dispatch never creates queue rows itself, and queued agents always start first.
- A refused spawn logs a `dispatch-error` event for that task and the next candidate is tried. Any other failure logs `dispatch-error` under entity `tick`; the tick always continues.
- A bad integer in these keys falls back to the default.

## The fyi mail

After each spawn the parked lead gets one message of kind `fyi`, subject `dispatch: <name> started on <task>`. `factory inbox` shows it like any other mail, but `tick_wake_leads` ignores `fyi` when looking for unread mail, so it does not wake the lead (that would defeat parking). Real mail (reports, requests) still wakes it, and the unread count in the wake kickoff still includes the fyi. `fyi` is a tick-only kind and is not accepted by `factory msg --kind`.

## Turning it on and off

The director runs `factory config lead_less_dispatch shadow`, checks the `dispatch-shadow` events and the `lead-less dispatch:` line in `factory res`, then `factory config lead_less_dispatch 1`. Setting it back to `0` stops dispatch at the next tick. `factory stop <name>` undoes a single dispatch.

## Spec fill

When a goal has nothing ready left but has todo tasks without a spec, nothing refills the queue until a lead wakes. With `lead_less_dispatch=1` the tick (start of step 2b, `spec_fill_tick`) files one `pm` task to write those specs; the normal dispatch above then starts the pm in the same tick run. `shadow` only records a `spec-fill-shadow` event (goal entity, detail `team` and `tasks`).

A goal gets a pm task when all of these hold:

- (a) the goal is `active` and `dispatch_candidates` returns no task of it (the caps are ignored);
- (b) it has todo `coder`, `qa`, `reviewer` or `cleaner` tasks with an empty spec (for `coder` and `qa` also: an empty acceptance), whose team is active and parked with its lead not live, starting or queued;
- (c) no `pm` task of the goal is todo, in_progress or in_review, and no `pm` agent on the goal is live, starting or queued;
- (d) the throttle key `spec-fill:<goal>` is older than 3600 s (set when acting or shadowing): one pm task per goal per hour.

The team of the lowest-id task from (b) is used; its tasks from (b) are listed, ready ones (deps done) first, then by id, at most 5. The task is `Spec ready queue <G>: <T-a, T-b, ...>`, role `pm`, no deps. Its spec asks the pm to write spec and acceptance for the listed ids and not to change status, deps or priorities; its acceptance has three items. The parked lead gets one `fyi` mail `spec-fill: <T> filed for <T-a, T-b>`; the events are `add` (actor `tick`) on the task and `spec-fill` on the goal. A failure rolls the insert back and logs `dispatch-error`.

## What leads still own

Specs, acceptance, dependencies (`--after`), priorities, judges and everything after the worker starts. A lead that does not want a task dispatched sets `--after` on it or keeps it out of `todo`.
