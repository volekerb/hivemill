# PR descriptions

Source: owner request M2502; G-007, our own wording.

## Standard

A PR body is short, concrete and checkable. Template: `.github/pull_request_template.md`. Coder skill: `pr-description`.

- The template's five sections, in this order: `## Summary`, `## Why`, `## Changes`, `## Test evidence`, `## Risk / rollback`. Then a collapsed `<details>` block with the `## Acceptance walk` line required by pre-pr-check. No "Generated with Claude Code" footer (owner M11175; the settings templates set `attribution.pr` to empty).
- 150-250 words in the five sections. Text inside `<details>` blocks (acceptance walk, audit tables, long evidence) is outside the count.
- Summary: what changed and the effect. Why: task id and one line. Changes: 3-6 bullets of behaviour, not a restatement of the diff.
- Test evidence: exact command plus key output line; test count on main and on the branch.
- Risk / rollback: what could break; rollback = revert the PR; note a relaunch when role files, plugins or settings change.
- No marketing tone. The `Co-Authored-By` trailer lives in commits. The `## Gates` block lives in QA and reviewer verdicts, not in PR bodies.
- Enforcement: the reviewer treats a non-conforming body as a blocking finding; the judge does not merge one. The merge gate itself is not changed.

## Example 1: a long body (#110, T-0159, 1783 words)

Before (shape): a one-paragraph intro, then "What changed" with five dense bullets, a 13-item "Acceptance criteria" list (item 11 alone is a nested list of eight consumers), "C4 evidence", "Notes for the reviewer", "follow-ups", and three appended rounds (Round 2, Attempt 3, RESTRUCTURE) that supersede each other, so the reader must replay history to learn the final state.

After:

> ## Summary
> The live DB moves from `~/factory/factory.db` to `~/factory/db/factory.db`, alone in its directory, so the worker sandbox can bind `db/` read-write and the rest of `~/factory` read-only.
>
> ## Why
> T-0159 (G-010 plan v1 section 3.3).
>
> ## Changes
> - One resolver, `web/dbpath.py`, picks `<FACTORY_HOME>/db/factory.db`, else the legacy file until migration. There is no `FACTORY_DB` override.
> - `factory tick` migrates once under the tick lock: backup, row-count check, fsync, rename. Abort triggers on the old file reject late writers; the old file is renamed `factory.db.migrated-<ts>`.
> - A failed migration keeps the legacy file authoritative and alerts the director once an hour.
> - `db()` opens with `mode=rw`; only `factory init` creates a file.
> - Deny rules for `db/**` in all 12 settings files; worker-guard and web follow the new path.
>
> ## Test evidence
> `python3 -m unittest discover -s tests` -> `Ran 1583 ... OK`; main 1557 (`ci/test_count_gate.py count`). With the ABORT triggers or `mode=rw` reverted, the late-writer and no-create tests fail.
>
> ## Risk / rollback
> A reader holding the old file delays the exclusive lock up to 30 s; the tick retries. Rollback: stop tick and serve, checkpoint, move `db/factory.db` back, delete `-wal`/`-shm`, revert the PR (steps in `docs/db-layout.md`). Settings apply after relaunch.
>
> (Per-criterion lines, round history and the consumer list go in a collapsed `<details>` block, or in the QA and reviewer verdicts.)

## Example 2: a typical body (#113, T-0181, 306 words)

Before (shape): "What changed", "Evidence (Claude Code flag exists; claude was NOT executed)" quoting binary strings and a version path, "Acceptance" items 1-6 with test names, and an inline reviewer-finding fix.

After:

> ## Summary
> Opus agents now spawn with `--effort medium`. Sonnet spawns are unchanged.
>
> ## Why
> T-0181, owner request M2741: cut Opus cost.
>
> ## Changes
> - New config key `opus_effort` (default `medium`); an empty value disables the flag, an invalid level makes spawn refuse with a fix hint.
> - `effort_args()` runs first in `_spawn_start`, so a bad value creates no worktree, pane or agent row.
> - Only model ids starting `claude-opus-` get the flag.
> - Override: `factory config opus_effort high`. Documented in `docs/opus-effort.md`.
>
> ## Test evidence
> `python3 -m unittest discover -s tests -p test_opus_effort.py` -> `Ran 8 ... OK`. Full suite: branch 1577, main 1569. With `bin/factory` reverted, 3 of the new tests fail. `claude` was not executed; the `--effort` flag was confirmed in the installed binary's help text.
>
> ## Risk / rollback
> A wrong level string would break every Opus spawn; it is validated before anything is created. Rollback: revert the PR. Persistent roles pick it up at their next relaunch.
