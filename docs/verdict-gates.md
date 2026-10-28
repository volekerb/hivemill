# Verdict gates

A `factory verdict <T> --pass --sha <sha> --summary ...` summary can carry a machine-checked `## Gates` section: one line per command that must succeed on the PR head.

```
## Gates
- unittest exit=0 cmd=python3 -m unittest discover -s tests -v
- compile exit=0 cmd=python3 -m py_compile bin/factory
```

- The section starts at a line that is exactly `## Gates` and ends at the next line starting with `#` (or the end). Blank lines are ignored. Each line is `- <name> exit=<code|unknown> cmd=<command>`; the command may contain spaces and `=`.
- **qa** (and owner `--as qa`): exactly one section with at least one gate, every line well formed, names unique, every exit `0`. **reviewer** (and owner `--as reviewer`): the section is optional; if present, the same checks apply.
- A refused PASS exits 5, records no verdict row and no event, and prints the reason plus the format. `--fail` is never checked.
- A PASS needs `--sha <full or >=7-char sha you checked>`. It exits 5 and records nothing when `--sha` is missing or is not a prefix of the current PR head (the head moved after your check: re-check it, or rely on carry-over at judge time). The recorded head sha is the full head. `--fail` takes `--sha` optionally; a `--sha` that is not the head records the FAIL at that sha with a warning.
- Gates are commands that must succeed (suite, build, lint, compile). An acceptance check whose correct result is a non-zero exit (e.g. "the hook blocks with exit 2") goes in the per-criterion list, not in Gates.
- Tests that record a QA PASS use `harness.gates_summary()`.

Source: Verdict Gates lines: idea from gAmUssA/coding-policy ADR 0005 @7a25169 (declares MIT, no LICENSE file); our own code and wording.

## Pending CI and merge races

`factory judge --merge` no longer refuses just because CI is still running.

- If the only non-green checks are pending (`PENDING`, `IN_PROGRESS`, `QUEUED`, `WAITING`, `REQUESTED`, `EXPECTED`), the judge polls the PR every 30 s (one progress line per poll) until nothing is pending or `judge_ci_wait_s` has passed. The config key defaults to `2400` seconds (40 min, so the 35-min CI limit of #117 fits; the judge runs `--merge` in the background, T-0149) (`factory config judge_ci_wait_s <seconds>`). A failing check refuses at once, with no waiting.
- After the wait, the **whole gate is re-run on the fresh PR state**: head SHA, both verdicts (or a carry across pure `origin/main` merges), mergeable, required checks, auto_merge and the stale-base check below. If the head moved while waiting, the verdicts are checked against the new head, so a verdict for the old head still refuses. On timeout the refusal reads `checks not green: <check>=IN_PROGRESS (waited Ns)`.
- If `gh pr merge` fails with `Base branch was modified` (another PR merged at the same moment), the judge re-reads the PR and re-runs the **full gate** (including the stale-base check with a fresh verified `origin/main` fetch). If main moved, the head no longer contains it, so the judge treats it as stale (next section: it refreshes the branch itself, up to twice). Only when the head SHA is unchanged and the whole gate passes does it retry the same `gh pr merge ... --match-head-commit <sha>`, once. A second failure, a changed head or any other merge error is recorded as `merge-failed`, as before.
- **Stale base.** The merge is also refused (exit 4, `judge-blocked`, reason `STALE-ONLY: head does not contain origin/main`) unless `origin/main` is an ancestor of the PR head. It uses the same verified fetch as the verdict carry (private `refs/factory-judge/*`, replace refs ignored, main compared with GitHub's answer). Both checks, and `verified_main` (the landed-PR and record-merged checks), run every git call with the same hardened flags (`-c core.commitGraph=false -c core.attributesFile=/dev/null`). If the fetch or any git step fails, the merge is refused with `cannot verify head contains origin/main: ...`.
- **A lost merge answer (T-0288).** A `gh pr merge` that fails with a 5xx or times out may still have landed. The judge then looks up to 6 times (5 s apart; `FACTORY_JUDGE_MERGE_POLLS` / `FACTORY_JUDGE_MERGE_POLL_S`) at the PR state and at origin/main for a commit whose subject ends with `(#<PR>)` and whose tree equals the gated head. If either shows the merge, it is recorded as merged against the real main commit (a still-OPEN PR is closed with a pointer comment); otherwise it is `merge-failed` as before. The same main check runs before any refresh, so an already-squashed PR is never refreshed or squashed again. To record such a merge by hand: `factory judge <task> --record-merged <sha> --reason ...` (judge or director). It refuses unless `<sha>` is on origin/main, its subject ends with `(#<PR>)` and `git diff <sha> <PR head>` is empty or the PR head is in main; it then closes the PR with a comment naming `<sha>` and records done.

## Stale head: the judge refreshes it itself (T-0230)

When the **only** gate problem is `STALE-ONLY` (the head does not contain the verified `origin/main`; GitHub's own
`PR has conflicts` next to it does not count) and `git merge-tree --write-tree <head> <main>` is clean, `factory judge --merge`
does what `factory refresh` does (docs/refresh.md) in-process, then keeps going:

1. refresh: verified fetch, pure main merge commit (parents: old head, verified main sha), push with `--force-with-lease` on the old head; event `refreshed` (actor = the judge, detail has `"how": "by judge"`) and a `note`;
2. wait for GitHub to report the new head and for CI on it (`judge_ci_wait_s`; an empty check list right after the push is not green);
3. re-run the **whole gate** on fresh PR state: both verdicts through the T-0095 carry-over walk to the new head, CI green, mergeable, head contains `origin/main`, required checks, auto_merge;
4. `gh pr merge --match-head-commit <new head>`.

If main moved again before step 4 and the head is still clean, steps 1-4 repeat. **At most `judge_max_refreshes` refreshes per judge run** (config, default 4; invalid or below 1 means 4); one more
stale result exits 4 with `judge-blocked` reason `STALE-LOOP: main moved N times during refresh; ...`. The lead then runs
`factory refresh` by hand. A real conflict exits 4 with `CONFLICT-ONLY: <files>` and pushes nothing. Any other problem
(a missing or failed verdict, a non-pure commit after the PASS, red CI, a base other than `main`, cannot verify) refuses exactly
as before, with no refresh. A PR whose base is not `main`, or whose head branch is not the task branch, is never refreshed.

The gate is not weakened: the merged head always contains the verified `origin/main`, has green CI on that exact head, and carries
verdicts only by the machine rule (pure `origin/main` merges). There is no file-overlap exception. Fetch, git and `gh` errors
refuse with `cannot verify`, and fetched main must equal `gh api .../commits/main`.
