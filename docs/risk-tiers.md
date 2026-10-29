# Risk tiers (G-022 #1)

`factory task tier` stores a risk tier T0 to T3 on a task. This step only classifies and stores: no merge, review or
spawn decision reads the tier yet (later G-022 tasks do), so turning it on changes nothing else. Design: staff-engineer
ADR M10886, section "Risk tiers".

| tier | rule |
|---|---|
| T3 | any sensitive path or sensitive top-level def, or more than 600 changed lines |
| T0 | docs only: `docs/**` or `*.md`, none under `roles/`, `ci/`, `.github/`, `bin/` |
| T1 | tests only (`tests/test_*.py`, `tests/fixtures/**`), or at most 150 changed lines |
| T2 | everything else |

Rules are checked in the order T3, T0, T1, T2. The lists (sensitive paths, sensitive defs of `bin/factory`, globs, line
limits) are in `ci/tier_rules.json`; the code is `ci/risk_tier.py` (pure `tier(diff, paths, lines, rules)`). Def-level
detection reuses the ast diff of `ci/affected.py`; an unparsable `bin/factory` or an edited unnamed top-level statement
counts as sensitive (fail closed). Both files are sensitive paths themselves.

**The PR cannot edit its own tier.** `bin/factory` loads `ci/risk_tier.py` and `ci/tier_rules.json` from the main checkout
(`$FACTORY_HOME`), never from the PR tree, the same way it loads `ci/public_text_guard.py`.

## Commands

- `factory task tier T-0001`: print the stored tier (`-` when unset). `factory task show` prints `tier: T2` once set.
- `factory task tier T-0001 up T3` (the word `up` is optional): raise. Reviewer, QA, lead, director, boss, judge and the
  owner may. A lower value is refused (exit 2): a tier is only ever raised.
- `factory task tier T-0001 --classify [--base origin/main] [--head origin/<branch>]`: classify `base...head` in the
  main checkout and store max(old, new), so a later, smaller commit never lowers it. Each raise is a `tier` event.
  Needs config `risk_tiers` = `on` (default `off`).

Rollback: `factory config risk_tiers off`; nothing consumes the stored tier.

Tests: `tests/test_risk_tier.py` (each rule, sensitive defs), `tests/test_task_tier.py` (CLI, escalate-only, rules from HOME).
