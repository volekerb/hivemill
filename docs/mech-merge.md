# Mechanical merger (G-022 #4)

The tick can merge a PR of risk tier T0 to T2 without an LLM judge. It calls `judge_gate_and_merge`, the same function
`factory judge --merge` runs (verdicts and carry, head contains main, PR state, CI, push-policy freeze, red-main freeze,
public-text guard, noreply author, `auto_merge`, `--match-head-commit`). Nothing is re-implemented or skipped.
Design: staff-engineer ADR M10886.

Config `mech_merge` (default `off`):

| value | effect |
|---|---|
| `off` | nothing runs. This is the rollback. |
| `shadow` | the full gate runs, nothing is merged. Event `mech-would-merge` or `mech-would-block` (with the reasons) per PR head. The LLM judge works as before. Compare for one working day before `on`. |
| `on` | one T0-T2 PR is merged per run. Event `merged` carries `by=merger:` and the reason list. |

Needs `risk_tiers=on`: the merger classifies the exact PR head itself (fetch, rules from the main checkout) and stores
max(old, new); with `risk_tiers` off, or a tier it cannot decide, it records a block and leaves the PR to the judge.

**Candidates** (oldest first): `in_review`, tier T2 or lower, latest reviewer and QA verdict PASS, no live/starting/queued
judge on the task, no merger event for it in the last `mech_merge_retry_s` (600), no merge freeze.
**To the LLM judge**: T3, a dispute (reviewer and QA disagree on one head, two or more FAIL verdicts, a verdict text line
starting `judge:`), or any gate refusal (`mech-blocked` in `on`, the judge's normal path then applies).
The merger never refreshes a stale head (`STALE-ONLY` block); refresh stays with the judge or `factory refresh`.

**Runner**: the tick launches at most one detached `factory mech-merge` (systemd unit `factory-mech-merge`; the tick never
waits on gh). Merges are serial: a second run exits 4 while `run/mech-merge.lock` is held. Manual run (owner/director):
`factory mech-merge [--task T-0001]`.

Rollback: `factory config mech_merge off`. Tests: `tests/test_mech_merge.py`.
