# Multi-angle review workflow

Risky PRs get a deterministic multi-angle review through the Workflow tool before the reviewer writes its verdict. Other PRs keep the single-pass review.

## Gate

The workflow runs only when the PR diff touches `roles/hooks/`, `roles/settings/`, `ci/` or `.github/`, or has more than 500 changed lines (additions + deletions). `bin/factory` alone does not gate. The lead puts this line into the reviewer brief of a gated PR (lead skill `worker-brief`), because the Workflow tool only runs when asked:

`Gated PR: run the multi-angle-review workflow (reviewer skill multi-angle-review) before your verdict.`

The reviewer skill `multi-angle-review` (`roles/plugins/reviewer/reviewer-skills/`) re-checks the gate and refuses to run on an ungated PR.

## What runs

`roles/workflows/multi-angle-review.js`, run by absolute path from the main checkout (never from the PR worktree, so a PR cannot weaken its own review). Args: `{pr, base, spec_task}`.

- Review: 3 Sonnet agents in parallel: correctness, guard/security, spec-fit.
- Verify: up to 3 Opus agents in parallel, one per angle that has findings; each tries to disprove its angle's findings and drops unconfirmed ones.
- Caps: at most 6 agents in total (guideline < 10), at most 3 in parallel.
- Agents are read-only: their prompts forbid edits, git writes, `gh pr merge|review|comment` and every factory command. They run under the normal worker guard.
- Returns `{confirmed, unverified, dropped, angles}`. If an agent dies (null result), its angle gets `error: "review"` or `"verify"` in `angles`; findings of a dead verifier go to `unverified` (never `dropped`) and the reviewer checks them by hand.

The reviewer still owns `factory verdict`; the output is input to it.

## Cost

Per gated PR about 3 x 60-100k Sonnet input plus 3 x 20-40k Opus input, roughly +250-400k tokens, about $0.5-1.5 (see `web/prices.py`). Ungated PRs add nothing. The gate hit 8 of 20 recent merges (40%) in the T-0241 baseline window.

## Off switch

Delete `roles/plugins/reviewer/reviewer-skills/skills/multi-angle-review/` (or the whole `roles/plugins/reviewer/` directory; then update `WITH_PLUGINS` in `tests/test_plugins.py`). The lead brief line is then ignored by reviewers.
