# Coder self-review

Skill `coder-skills:self-review` (roles/plugins/coder/coder-skills/skills/self-review/SKILL.md).

## What it does
A coder runs exactly one fresh Sonnet subagent on its own diff
(`git diff origin/main...HEAD`) with the Steps 2-3 checklist of roles/reviewer.md
(correctness, security, spec fit, maintainability, tests; R1 blocking/nit rule).
The subagent reviews only and never edits files. The coder verifies every finding,
fixes each verified blocking one, adds a test where behaviour changed and reruns tests.

## When
After pre-pr-check and before `factory task update <T> --status in_review`.
On a retry after a FAIL it runs again on the new diff.

## PR-body line
`Self-review: N findings, fixed/not`, then one line per finding:
`- <blocking|nit> <file:line> fixed|not fixed: <reason>`.

## Why it does not replace reviewer or QA
The subagent shares the coder's task framing and runs no gates. Its findings are
a cheap early read that removes obvious defects. The reviewer and QA verdicts and
the judge's merge decision are unchanged and still required.

## Guard/sandbox/merge-gate diffs (3-angle)
Step 6 of the skill replaces the single subagent with the Workflow tool on the one
saved script `<factory-root>/roles/workflows/multi-angle-review.js`
(`args.mode = "coder"`). It applies when the diff touches roles/hooks/, roles/settings/,
ci/, .github/, scout/nsbox.py, scout/workerbox.py, has a `bin/factory` hunk in
cmd_judge, refresh_branch, cmd_refresh, head_has_main or verdict_carries, or is over 500 lines.
Rule: exactly 3 agents (security, spec, tests), all Sonnet, no verifiers, never Opus or Fable.
Findings come back as `unverified`; the coder checks each one (steps 3-5).
PR-body line: `Self-review: N findings, fixed/not (3-angle)`. The reviewer path is unchanged.
