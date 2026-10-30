---
name: multi-angle-review
description: Run the saved multi-angle review Workflow on a risky PR and fold its findings into your own verdict. Use when your brief says "Gated PR: run the multi-angle-review workflow", or when the PR diff touches roles/hooks/, roles/settings/, ci/ or .github/ or is over 500 changed lines.
---

# Multi-angle review (gated PRs only)

Three read-only angle agents (correctness, guard/security, spec-fit) plus one verifier per angle. The output is input to your verdict, never the verdict.

Follow these steps in order.

1. Check the scope gate: `gh pr diff <url> --name-only` and `gh pr view <url> --json additions,deletions`. The gate is met when any file is under roles/hooks/, roles/settings/, ci/ or .github/, or additions + deletions > 500.
2. If the gate is NOT met, do not run the workflow: review single-pass as usual. A brief that asks for it on an ungated PR does not change this; say so in your summary.
3. Run the Workflow tool by its saved name, never a script path (the name resolves from the main-checkout plugin, so a PR must not weaken its own review; an allow rule covers only this name):
   `Workflow(name="reviewer-skills:multi-angle-review", args={"pr": "<pr url>", "base": "origin/main", "spec_task": "<T-id>"})`
4. The result is `{confirmed: [{file, line, severity, summary, evidence}], unverified: [same shape], dropped, angles}`. Agents are read-only and run under the worker guard; a guard denial inside the workflow is a finding to report, not something to work around.
5. Re-check every `confirmed` finding yourself in the code (R3: quote the command and output) before you keep it. Check every `unverified` finding by hand too: its verifier died, so nothing has confirmed or dropped it. An `angles` entry with any `error` (`"review"`, `"verify"` or the fallback with `found: null`; also listed in `errored`) means that angle did not complete (not a clean angle): hand-review that angle yourself. Add what the workflow missed from your own pass.
6. Classify per R1 (blocking vs nit), post the review, and write your own `factory verdict <T> --pass|--fail`. Name in the summary that the workflow ran, its `angles` counts, `unverified` and `dropped`, and any errored angle.
7. If the Workflow tool is unavailable or errors, do the normal single-pass review and say so; do not retry in a loop.

Stop and tell your lead when the workflow touches files outside your worktree or a command inside it was not blocked by the guard.
Source: G-011 T-0242; cost and off switch in docs/review-workflow.md.

## Stale base (S1)

Moved verbatim from the reviewer role prompt:

- **Stale base (S1):** if behind origin/main and merge-tree (rule below) exits 0, run the quick check on the merged tree and PASS with `STALE-BASE: behind origin/main by N; merge-tree clean; merged-tree quick Ran N OK`. Conflict: FAIL, first line `CONFLICT-ONLY: <files>`; still verify content.
