# Examples: working with Hivemill

Three walkthroughs with real command output, captured against a fictional demo home built by `ci/demo_seed.py`
([../demo-data.md](../demo-data.md)), never a live factory. All three use the same seed:

```
python3 ci/demo_seed.py --home <tmp>/demo --now 2026-10-20T12:00:00
```

Run any command in them with `HOME=<tmp>/demo/home FACTORY_HOME=<tmp>/demo bin/factory …`. The documents show
plain `factory …`.

| example | what it shows |
|---|---|
| [01 From a goal to a merged PR](01-goal-to-merged-pr.md) | The whole life of goal G-001: request, goal, team, plan, tasks with acceptance, PR, reviewer and QA verdicts, judge merge, goal done. |
| [02 Handling a rejection](02-handling-a-rejection.md) | A QA fail and a judge reject on T-0004, the attempts counter, how the lead routes the fix loop, verdict carry-over and stale bases. |
| [03 The digest and the scorecard](03-digest-and-scorecard.md) | `factory digest` and `factory scorecard` output explained section by section, and where the same data shows in the web UI. |

## Owner-to-boss requests

You only ever talk to the boss, in plain English. The boss decides the project, writes the intent and the success
criteria and creates one goal; the director then staffs a team. Three examples:

**1. A small, well-bounded feature.**
"Add a dark mode toggle to the Acme shop header. The choice should survive a reload."
The boss creates one goal in project `acme-web`, priority 2, with two success criteria: "A toggle is in the header" and
"The choice survives a reload". Small enough for one team and about three tasks.

**2. A fuzzy wish.**
"Checkout feels slow and confusing."
The boss does not guess. It asks you one or two questions (what is slow, which step loses people) or makes
the goal's first task a pm investigation: the criteria then read "Findings are written down with measurements" before any
build criteria exist. The goal is created once the problem is concrete.

**3. Several things at once.**
"We need a mobile app, and also fix the password reset email."
The boss splits this into two goals with different priorities (the email fix is small, so priority 1; the app is a
prototype, priority 3) instead of one big goal, so the director can staff them separately and a blocked app does not
hold up the fix. In the demo home G-004, the mobile app shell, is exactly such a separate, lower-priority goal.

In each case the boss shows you the goal it created (`factory goal show G-…`) and reports back when it is done. You
can check progress at any time with [`factory digest`](03-digest-and-scorecard.md) or the web UI.
