# Example 2: handling a failed review and a judge reject

Not every PR merges on the first try. This walkthrough follows T-0004 "Add contact form" (goal G-001 of the demo
home): a QA FAIL, a judge REJECT, three coder attempts and a final MERGE. All output is real, from the demo home
seeded as in [example 1](01-goal-to-merged-pr.md):

```
python3 ci/demo_seed.py --home <tmp>/demo --now 2026-10-20T12:00:00
HOME=<tmp>/demo/home FACTORY_HOME=<tmp>/demo bin/factory task show T-0004
```

Seed with `--now 2026-10-20T12:00:00`. In the factory, verdicts are `pass` / `fail` (reviewer and QA) and
`merge` / `reject` (judge). Commands below are shown as `factory …`.

## The task after it merged

`factory task show T-0004` (real output; the verdicts and history follow step by step below):

```
id: T-0004
goal_id: G-001
team_id: g-001-a
title: Add contact form [SC3]
role: coder
status: done
assignee: coder-t-0004
depends_on: ["T-0001"]
branch: factory/t-0004
pr_url: https://github.com/example-org/acme-web/pull/103
attempts: 3
created: 2026-10-10T14:24:00+00:00
updated: 2026-10-11T04:00:00+00:00

## spec
Add a contact form (name, email, message) that posts to /api/contact and sends the text to the shop inbox.
Reject empty or malformed input.

## acceptance
1. A valid post returns 200 and queues one email.
2. A missing email returns 422 with a field error.
3. A message over 2000 characters is rejected.
```

## Step by step

### 1. First PR, QA FAIL

The coder opened PR #103 (`status=in_review`). The record holds one verdict for this first head: a QA `fail`.
It lists the finding with file, line, input and expected result, so the coder can fix it in one pass:

```
- 2026-10-10T18:12:00+00:00 qa FAIL @ba49e234 by qa-t-0004: Changes needed: POST /api/contact with email 'a@' returned 200, expected 422 (api/contact.py:31 only checks for an @ sign).
```

A verdict records the PR head SHA it judged (`@ba49e234`). The lead sees the FAIL in its inbox and reads
`factory task show T-0004`.

### 2. The lead routes the fix: attempt 2

The lead follows the `fix-loop` skill (`roles/plugins/lead/factory-lead/skills/fix-loop/SKILL.md`): it triages the
cause, writes a retry brief that says "attempt 2 of 3", lists every open blocking finding with its source and
spawns a coder (`factory spawn coder --task T-0004 --prompt -`). The coder moves the task back to `in_progress`:

```
- 2026-10-10T18:42:00+00:00 coder-t-0004 update: status=in_progress
- 2026-10-10T20:42:00+00:00 coder-t-0004 update: status=in_review pr=https://github.com/example-org/acme-web/pull/103
```

**The attempts counter.** `attempts` goes up by one every time a task enters `in_progress` from another status.
The first start gives 1, every retry adds one. The rule is three attempts: after the second failed attempt the lead
asks the staff-engineer why the work is not converging and puts the answer in the third brief, and a third failure
goes to the director. Stalls that produced nothing, nit-only findings, verifier respawns and stale-base merges do
not count as attempts (fix-loop L1, L4, L6 and step 6).

### 3. Both verdicts pass, the judge REJECTS

The coder fixed the email check and pushed head `@3bb1e7aa`. Reviewer and QA both passed it. The judge still
rejected, for a reason neither verifier covers: the diff carried a change no acceptance item asks for:

```
- 2026-10-10T21:42:00+00:00 reviewer PASS @3bb1e7aa by reviewer-t-0004: Checked input validation in api/contact.py and that the mail queue call is made once per request.
- 2026-10-10T22:12:00+00:00 qa PASS @3bb1e7aa by qa-t-0004: Posted a valid, an invalid-email and a 2001-character message; got 200, 422 and 422 as specified.
- 2026-10-10T22:42:00+00:00 judge REJECT @3bb1e7aa by judge-t-0004: Rejected: the diff edits src/cart.js:88, which no acceptance item of this task covers. Remove it or split it into its own task.
- 2026-10-10T22:42:00+00:00 judge-t-0004 judge-reject: unrelated file touched
```

(the last line is the `history` entry). The judge checks the diff against the spec's scope; a PR that carries an
unrelated file is rejected even when the code works. The reject sends the task back to the lead, who writes the
attempt 3 brief ("attempt 3 of 3: remove src/cart.js, change nothing else") and spawns a coder:

```
- 2026-10-10T23:12:00+00:00 coder-t-0004 update: status=in_progress
- 2026-10-11T01:12:00+00:00 coder-t-0004 update: status=in_review pr=https://github.com/example-org/acme-web/pull/103
```

### 4. Re-review, QA, and a gate that was not green yet

The coder pushed to the same PR, so the head SHA changed (`@a6bd606d`). Verdicts are bound to the head they judged,
so the three records on `@3bb1e7aa` say nothing about the new head and reviewer and QA run again:

```
- 2026-10-11T02:12:00+00:00 reviewer PASS @a6bd606d by reviewer-t-0004: Checked input validation in api/contact.py and that the mail queue call is made once per request.
- 2026-10-11T03:12:00+00:00 qa PASS @a6bd606d by qa-t-0004: Posted a valid, an invalid-email and a 2001-character message; got 200, 422 and 422 as specified.
```

The judge's first run on the new head was held at the gate, not rejected:

```
- 2026-10-11T03:30:00+00:00 judge-t-0004 judge-blocked: checks not green: build pending
```

`judge-blocked` means the merge gate refused (here CI was still running). It is not a verdict and not an attempt.
The judge waits for CI and tries again.

### 5. MERGE

```
- 2026-10-11T04:00:00+00:00 judge MERGE @a6bd606d by judge-t-0004: Reviewer and QA PASS at a6bd606d; checks green; merging.
- 2026-10-11T04:00:00+00:00 judge-t-0004 merged: @a6bd606d
- 2026-10-11T04:00:00+00:00 judge-t-0004 update: status=done
```

The task ends `done` with `attempts: 3`. The scorecard keeps score: team g-001-a has 3 merged tasks, 3 rejections
(every `fail` or `reject` verdict) and 3 retries. See [example 3](03-digest-and-scorecard.md).

## Verdicts after main moves: stale base

While a PR waits for review, other PRs merge and the base goes stale. A stale base alone is not a content failure,
and the lead does not burn an attempt on it:

- A head that is only behind `origin/main` is refreshed mechanically with a merge (`factory refresh T-0004`, see
  [../refresh.md](../refresh.md)), and `factory judge --merge` does the same itself, at most twice.
- Reviewer and QA PASS verdicts survive a pure `git merge origin/main`: the judge accepts a PASS recorded at an earlier
  head when every commit between the two heads is a pure main merge with no manual edits. See
  [../verdict-carry-over.md](../verdict-carry-over.md). In step 4 above the head changed because the coder changed
  code, so new verdicts were needed. Each attempt of T-0004 has one head SHA, and every verdict of that attempt names it. A pure main merge would have kept the old ones.
- A merge that conflicts (`CONFLICT-ONLY: <files>`) gets a merge-only coder and a delta review of the conflict hunks.
  The protocol and its limits are in [../staleness.md](../staleness.md).

## What the lead does, in short

| result | lead's move |
|---|---|
| reviewer or QA `fail` with findings | retry brief, new coder (attempt +1) |
| only nit findings | respawn the verifier, no coder, no attempt |
| judge `reject` for scope or content | retry brief listing the judge's reason, new coder |
| judge `reject` for stale base | `factory refresh T`, then the judge again, no attempt |
| attempt 2 failed | ask the staff-engineer for one remedy, then attempt 3 |
| attempt 3 failed | escalate to the director |
