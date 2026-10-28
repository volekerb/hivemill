---
name: fix-loop
description: Run retries, stalls and the third attempt on a task. Use when a reviewer or QA FAIL or a judge reject comes back, when respawning a coder, when a task reaches attempt 2 or attempt 3, when a worker is lost, stopped or stalled, or when the findings are only nits.
---

# Fix loop

Rules for the build loop's failure paths. Follow these steps in order.

1. Triage the cause first (L1, L4). Run `factory task show T` and read the latest verdicts.
   - Worker lost or stopped, worktree clean, no commits beyond the base: it produced nothing (L1). Respawn it for the same task: `factory spawn <role> --task T`. This is not an attempt.
   - Partial or unpushed work is NOT no-work. Describe it in the next coder's `--prompt`, or ask that coder to push it first.
   - Only nit findings: do not respawn a coder (L4). Respawn the same verifier, not a coder (L6): `factory spawn reviewer|qa --task T --prompt -`, with a note to re-verdict under the severity rule. This is not an attempt. Its pane is closed after `factory done`, so nothing re-verdicts unless you spawn it. Park the nits: `--follow-up-of T` is refused until T is done (merged), so before the merge file a plain `factory task add` in the same goal and cite T in the title and spec; after the merge use `--follow-up-of T`.
   - Judge reject for "insufficient evidence" and no code change asked: spawn a fresh reviewer or QA on the same head with the judge's reason in `--prompt`, then the judge again. No coder (L4).
2. Write the retry brief (L2). The coder's `--prompt` says "attempt N of 3", lists every open blocking finding with its source (reviewer, QA or judge), and says the coder owns the task now. Then `factory spawn coder --task T --prompt -`.
3. Diagnose before the third attempt (L3). When attempt 2 fails, ask the staff-engineer why the attempts are not converging:
   `factory msg staff-engineer "<question>" "<task id, verdicts, diffs>" --kind request --task T`
   Ask for exactly one remedy:
   - continue: same approach plus one specific fix.
   - restructure: change the spec or split the task. Use the spec-change skill (stop, update, respawn); do not repeat its steps here.
   - stop: escalate to the director now.
4. Put the remedy in the third coder's `--prompt`, or in the escalation. Never send a third attempt with the same brief as the second.
5. A third failure, or a "stop" remedy, goes to the director with `factory task show T` history (see the three-attempts rule in your role prompt).
6. Stale base is not a content FAIL (S1/J4). A PASS carrying `STALE-BASE`, a FAIL whose only finding is `STALE-BASE` or `CONFLICT-ONLY`, or a judge reject starting `STALE-ONLY:`/`STALE-LOOP:`/`CONFLICT-ONLY:` is handled as below. This is not an attempt (like L1/L5/L6).
   - Clean stale head: no coder. `factory judge --merge` refreshes it itself (docs/staleness.md), at most 2 times. After a `STALE-LOOP` reject, run `factory refresh T` by hand, wait for CI green on the new head, then spawn the judge about 2 min after the push. A pure main merge keeps the PASS verdicts (docs/verdict-carry-over.md), so no reviewer or QA respawn.
   - Do the refresh BEFORE spawning the judge whenever a PASS says `STALE-BASE` (optional pre-work to the first bullet, which otherwise leaves it to the judge): `factory refresh T`, wait for CI green, then the judge. The judge then finds the head already contains main.
   - `CONFLICT-ONLY` (judge reject or `factory refresh` exit 3): spawn a coder with the merge-only prompt, which starts with `git pull` of its own branch (a refresh may have moved it): "git pull && git fetch origin && git merge origin/main; resolve conflicts only; no other edits; affected --run; push". After the conflict-resolved merge, respawn reviewer and QA for a delta re-verdict of the conflict hunks only, then the judge.

Every coder run counts as an attempt except a no-work stall (L1). Evidence-only rejects (L5) and verifier respawns (L6) are not coder runs, so they do not count. The merge-only staleness coder (step 6) is not an attempt either.
