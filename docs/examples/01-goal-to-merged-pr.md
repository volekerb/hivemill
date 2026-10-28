# Example 1: from an owner request to a merged PR

This walkthrough follows one goal, G-001 "Launch the public landing page", from a plain-English request to `done`.
Every output block is real: it was copied from a demo home, never from a live factory.

Reproduce it (`<tmp>` is any empty scratch directory outside your factory home):

```
python3 ci/demo_seed.py --home <tmp>/demo --now 2026-10-20T12:00:00
HOME=<tmp>/demo/home FACTORY_HOME=<tmp>/demo bin/factory goal show G-001
```

Seed with `--now 2026-10-20T12:00:00`; the `HOME=` prefix is mandatory (without it some views read your real
`~/.claude`). Below, commands are shown as plain `factory …`. All names are fictional (see
[../demo-data.md](../demo-data.md)).

The demo seeds its rows straight into the database, with realistic specs, acceptance criteria and verdicts, so the
tasks read like a real team's. Steps that only a role can run (the boss creates goals, the director staffs teams,
the pm stores plans) are shown as that role would type them, with the result read back from the demo data and
marked "(from demo data)".

## 1. The owner talks to the boss

The owner never edits the factory. They say what they want, in plain English, to the boss:

> We have a small web shop called Acme. I want a simple landing page: a hero section, a pricing table and a contact
> form that actually delivers messages. Nothing fancy, but it has to be live this week.

The boss turns this into one goal with success criteria (more examples in [README.md](README.md)).

## 2. The boss creates the goal

```
factory goal new --title "Launch the public landing page" --project acme-web \
  --intent "Ship a simple landing page for the Acme web shop." \
  --success "1. Hero section is live.
2. Pricing table is live.
3. Contact form delivers messages." --priority 2
```

Only the boss runs this (as the owner in the demo home it is refused with `only the boss creates goals`). The stored result, read with
`factory goal show G-001` (from demo data):

```
# G-001 — Launch the public landing page
status=done priority=2 project=acme-web

## intent
Ship a simple landing page for the Acme web shop.

## success_criteria
1. Hero section is live.
2. Pricing table is live.
3. Contact form delivers messages.
```

## 3. The director staffs a team

The director gives the goal a team (the id `g-001-a` is generated from the goal) and spawns its lead:

```
factory team new --goal G-001 --scope "Landing page sections and contact form"
```

Run `factory team list` to see it (real output, demo home):

```
ID       GOAL_ID  STATUS  LEAD_AGENT    SCORE  SCOPE                                      
g-001-a  G-001    closed  lead-g-001-a  5.0    Landing page sections and contact form     
g-002-a  G-002    active  lead-g-002-a  4.0    Cart, shipping, payment and confirmation   
g-003-a  G-003    active  lead-g-003-a  4.0    Profile, password and notification settings
g-004-a  G-004    active  lead-g-004-a         Mobile shell prototype                     
g-005-a  G-005    closed  lead-g-005-a  5.0    CSV export endpoint and filters            
g-006-a  G-006    active  lead-g-006-a  4.0    Rate limiter, headers, metrics and docs    
g-007-a  G-007    active  lead-g-007-a  4.0    New theme, navigation and dark mode        
g-008-a  G-008    closed  lead-g-008-a  4.0    Search index, box and ranking              
g-009-a  G-009    active  lead-g-009-a         Translations, locale switcher and emails   
```

The other teams belong to the other goals of the demo. `g-001-a` is `closed` because its goal is finished; the SCORE is the director's 1-5 evaluation (see
[example 3](03-digest-and-scorecard.md)). The lead (`lead-g-001-a`) now runs the team toward the goal.

## 4. The plan

The pm (or the lead) stores a plan against the goal:

```
factory plan G-001 - <<'EOF2'
1. Specify
2. Build
3. Verify
EOF2
```

Plans are versioned. `factory goal show G-001` prints the latest (from demo data):

```
## plan v1 (lead-g-001-a, 2026-10-06T04:48:00+00:00)
1. Specify
2. Build
3. Verify
```

## 5. Tasks with acceptance

The lead turns the plan into tasks. Each has a role, a spec, acceptance criteria and dependencies
(`factory task add --goal G-001 --role coder --title … --spec - --acceptance … --after T-0001`). Real output of
`factory goal show G-001` (the `[SC1]` tags tie a task to success criterion 1):

```
## tasks
ID      STATUS  ROLE     ASSIGNEE        DEPENDS_ON  PR_URL                                            TITLE                               
T-0001  done    pm       pm-t-0001       []                                                            Write landing page requirements     
T-0002  done    coder    coder-t-0002    ["T-0001"]  https://github.com/example-org/acme-web/pull/101  Add hero section [SC1]              
T-0003  done    coder    coder-t-0003    ["T-0001"]  https://github.com/example-org/acme-web/pull/102  Add pricing table [SC2]             
T-0004  done    coder    coder-t-0004    ["T-0001"]  https://github.com/example-org/acme-web/pull/103  Add contact form [SC3]              
T-0005  done    cleaner  cleaner-t-0005  ["T-0004"]                                                    Remove merged landing page worktrees
```

T-0001 is the pm's requirements task; the three coder tasks wait for it (`DEPENDS_ON`), then run in parallel. T-0005
is the cleaner removing the merged worktrees.

## 6. The coder opens a PR

`factory task show T-0002` (real output) shows one task from start to finish. The coder moved it to `in_progress`
(`factory task update T-0002 --status in_progress`), implemented it in its own worktree on branch `factory/t-0002`,
opened PR #101 with the body from [../../.github/pull_request_template.md](../../.github/pull_request_template.md)
(Summary, Why, Changes, Test evidence, Risk / rollback, and the Acceptance walk) and ran
`factory task update T-0002 --status in_review --pr <url>`:

```
id: T-0002
goal_id: G-001
team_id: g-001-a
title: Add hero section [SC1]
role: coder
status: done
assignee: coder-t-0002
depends_on: ["T-0001"]
branch: factory/t-0002
pr_url: https://github.com/example-org/acme-web/pull/101
attempts: 1
created: 2026-10-08T00:42:00+00:00
updated: 2026-10-08T06:00:00+00:00

## spec
Add a hero section to the landing page: headline, one-line subtitle and a 'Shop now' button.
The button links to /shop. Use the existing page layout.

## acceptance
1. GET / renders an h1 with the headline text.
2. The 'Shop now' button links to /shop.
3. At 375 px width the headline does not overflow.
```

The pm's acceptance list is what the reviewer and QA check the PR against. The verdicts and history of this task
follow in steps 7 and 8.

`attempts: 1` means the coder got it right the first time (the counter is explained in
[example 2](02-handling-a-rejection.md)).

## 7. Reviewer and QA verdicts

The lead spawns a reviewer (reads the diff against the spec) and a QA (runs the change against the acceptance
criteria). Each records a verdict with `factory verdict T-0002 --pass|--fail --summary "…" [--as reviewer|qa]`. The
values are `pass` or `fail`. Each verdict is bound to the PR head SHA it judged (the `@…` value). The PR was not pushed to between the two
verdicts, so both name the same head. The tail of `factory task show T-0002` (real output):

```
## verdicts
- 2026-10-08T04:30:00+00:00 reviewer PASS @80173ed9 by reviewer-t-0002: Checked the diff against the spec; the button target and the heading markup match.
- 2026-10-08T05:30:00+00:00 qa PASS @80173ed9 by qa-t-0002: Ran 3 of 3 acceptance items on a staging build, including the 375 px width check.
```

## 8. The judge merges

When both pass, the judge runs `factory judge --merge --reason "…" T-0002`. The CLI re-checks that the head SHA is still the one both verdicts name, that
CI is green, that the PR is open and mergeable and that `origin/main` is merged in, and only then merges. Nobody else
can merge: `gh pr merge` is blocked for every role.

```
- 2026-10-08T06:00:00+00:00 judge MERGE @80173ed9 by judge-t-0002: Reviewer and QA PASS at 80173ed9; checks green; merging.

## history
- 2026-10-08T00:42:00+00:00 lead-g-001-a add: Add hero section [SC1]
- 2026-10-08T01:00:00+00:00 coder-t-0002 update: status=in_progress
- 2026-10-08T03:30:00+00:00 coder-t-0002 update: status=in_review pr=https://github.com/example-org/acme-web/pull/101
- 2026-10-08T06:00:00+00:00 judge-t-0002 merged: @80173ed9
- 2026-10-08T06:00:00+00:00 judge-t-0002 update: status=done
```

## 9. The goal is done

When every build task is `done`, the lead has the pm audit the goal against its success criteria
(`factory goal audit G-001 --ready`), the director signs the audit off (`factory goal signoff`) and the goal is
closed (`factory goal close`; see [../goal-lifecycle.md](../goal-lifecycle.md)). The boss then reports to the
owner and the director closes the team. The header of `factory goal show G-001`, seen in step 2, reads
`status=done` because the demo goal is already finished, and the `## teams` block shows the closed team (real output):

```
## teams
ID       LEAD_AGENT    STATUS  SCOPE                                 
g-001-a  lead-g-001-a  closed  Landing page sections and contact form
```
