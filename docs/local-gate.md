# Local merge gate (steps 1-2: the runner and the judge wiring)

Owner rule (M8698): no paid GitHub Actions, every test runs on your own machine. The gate is a plain python process, not an agent:
`factory gate run` runs a project's gate command on the exact PR head (or on main) and records the result in the
`gate_runs` table. Step 1 (T-0515) builds and records; step 2 (T-0516, below) makes the judge, `judge_pr_ready` and the tick read it.

## Commands

| command | who | what |
|---|---|---|
| `factory gate run <task>` | boss, director, lead, owner (never a worker role) | gate the task's PR head |
| `factory gate run --main <project>` | same | gate the verified current main of the project |
| `factory gate show <task or run-id>` | anyone | one run (a task id shows its latest run) |
| `factory gate list [--project P] [--limit N]` | anyone | recent runs, newest first |
| `factory project update <id> --gate-cmd C --gate-count-cmd C --gate-timeout-s N` | director, owner | set the project's gate |

Exit code of `gate run`: 0 pass, 1 fail (the command exited non-zero), 5 error (infrastructure: no slot, timeout, head
mismatch, unreadable count). A project is in local mode once `gate_cmd` is set.

## What a run does

1. Row in `gate_runs` (status `queued`, runner pid recorded).
2. Verified fetch, as in `head_has_main`: private refs under `refs/factory-judge/`, hardened git flags, main compared with
   `gh api repos/<r>/commits/main`. The fetched PR head must equal the head GitHub reports, else `error` (also when `gh pr view` fails).
3. Result cache: a `pass` row of the same (project, head sha, main sha, `gate_cmd`) is reused (new row, note `cached: pass of run #N`,
   no slot, no run); fail/error are never reused. `gate run --no-cache` runs anyway.
4. Waits until fewer than `gate_max_parallel` runs are `running` (default 1, so one of the two test slots stays free for workers),
   then for a `tests/_slot.py` slot with `acquire(require=True)`: it never runs slot-less. No slot in time: status `error`, note
   `no slot`, and the command is not started.
5. `git worktree add --detach ~/.cache/factory-gate/<id>/head <sha>`; HEAD is asserted to be exactly that sha.
6. `projects.gate_cmd` (from the DB, never from a script in the PR tree) runs through `bash -c` in that worktree in its own
   process group, output to `logs/gate/<id>.log`. After `gate_timeout_s` (default 3600) the whole group is killed: status
   `error`. The child gets no `FACTORY_AGENT`, no gh/ssh credentials, a scratch empty `FACTORY_HOME`, `FACTORY_MAIN` (the checkout
   running `bin/factory`, for `$FACTORY_MAIN/ci/test_count_gate.py`) and `FACTORY_TEST_SLOT_HELD=1` (the suite must not take a
   second slot).
7. If `gate_count_cmd` is set, it prints the test count as its last line; it runs on the head and on the verified main (a second
   worktree; cached per main sha). An unreadable count is `error`, never `pass`. Without it, `gate show` says "not counted".
8. Record `pass`/`fail`/`error`, exit code, counts, log path. Worktrees are removed on every path (also SIGTERM and failures).
   A `queued` or `running` row whose runner pid is gone is marked `error` (`runner died`) by the next run.

A head that does not contain the verified main still runs; the row's note says the tested tree is not the merge result.

## Sharded suite

`tests/gate_shards.py` runs the full suite as N parallel `ci/shard.py` processes in the one worktree and under the one slot,
verifies that every module ran once, sums the test count and prints wall and CPU seconds (also in `$GATE_REPORT_DIR/summary.json`,
which the runner copies into the row's note and into `test_count` when there is no `gate_count_cmd`). Use it in the gate command
from the main checkout, so a PR cannot edit it: `python3 $FACTORY_MAIN/tests/gate_shards.py`. N is `gate_shards` (default 4)
capped by the CPU the load gate leaves free; the runner passes it as `$GATE_SHARDS`. Example for factory:
`python3 -m py_compile bin/factory && python3 $FACTORY_MAIN/tests/gate_shards.py && <go vet/test/build>`.

Known limit: the shards run the PR tree's own `ci/shard.py` (only the wrapper and the command are fixed), so the gate trusts the
PR's test runner, as CI did. The cache key is (project, head sha, main sha, `gate_cmd`).

## Config

| key | default | meaning |
|---|---|---|
| `gate_max_parallel` | 1 | runs at once |
| `gate_shards` | 4 | parallel shard processes per run (capped by free CPU) |
| `gate_post_status` | 0 | 1: mirror each PR result as commit status `factory/local-gate` (write only, never read back) |
| `gate_retry_error` | 1 | error (infra) runs of one head retried this many times by `judge_pr_ready` |

`factory res` shows the running gate runs. A run's log is `logs/gate/<id>.log` under the factory home.

## Judge wiring (step 2, T-0516)

A project is in **local mode** iff `projects.gate_cmd` is non-empty; every other project keeps the GitHub-checks path unchanged.
In local mode:

- `factory judge --merge` (`local_gate_problems`): the latest `gate_runs` row of this exact PR head decides. No row, `queued`,
  `running`, `fail`, `error` refuse; a pass never carries across a head change. A pass is also refused when its test count is below
  main's and the PR body has no `tests-removed: <reason>` line (same bypass line as `ci/test_count_gate.py`). A failing GitHub
  check still refuses (`checks_ok`), but `required_checks` is not asked for. With no run yet the judge enqueues one and waits
  (`judge_ci_wait_s`) for it, like it waits for CI.
- A stale head is refreshed by the judge as before; it then enqueues a gate run for the new head and exits `REQUEUED` without
  waiting (exit 0, nothing merged). The tick spawns a judge again once the run passes.
- `judge_pr_ready` (the tick's hold on a queued judge) reads `gate_runs` of the PR's current head: pass = ready, queued/running =
  not ready, none = enqueue one and not ready, fail = not ready plus one message to the team lead per run, error = retried up to
  `gate_retry_error` times, then like fail.
- The tick (`tick_gate`) reaps dead runners and launches queued runs that have no runner yet (`gate_runs.pid` NULL) as detached
  `factory gate run --run-id N` processes while fewer than `gate_max_parallel` are active. Enqueue is deduped per PR.

## Sandbox (`gate_sandbox`, T-0632)

`factory gate run` runs PR code as the owner uid. With `factory config gate_sandbox 1` the gate command and `gate_count_cmd`
run under `scout/gatebox.py` (the nsbox allow-list mode) instead of plain `bash -c`:

- `$HOME`, `$XDG_RUNTIME_DIR`, `/tmp`, `/var/tmp` and `/dev/shm` are empty tmpfs (credentials, the user bus, ssh-agent are not
  there). `/var/tmp` is disk-backed on the host, so it is hidden too: a PR must not leave files there for a later run.
- The checkout running `bin/factory` (`$FACTORY_MAIN`) and the repo's git common dir (hooks, config, refs, objects) are
  read-only; `$FACTORY_MAIN`'s `db`, `run`, `logs`, `worktrees`, `exports`, `state` are masked.
- Writable: the gate tree itself, `<wt_root>/reports` (`$GATE_REPORT_DIR`), `<wt_root>/factory-home` (scratch `$FACTORY_HOME`)
  and `<wt_root>/tmp` (`$TMPDIR`, disk-backed). The gate tree is rw because the suite writes into its own tree (`docs/demo/out`,
  rendered settings); that is acceptable because it is a throwaway detached checkout: every run makes a fresh
  `<gate_dir>/<run id>/head` with `git worktree add --detach` and removes it afterwards, so a write never reaches a later run
  or the result cache. The git common dir stays read-only even though the tree's `.git` file points into it.
- The box sets `FACTORY_GATEBOX=1`. The two nested-user-namespace tests that cannot observe what they assert inside the outer
  box (`test_skilleval_nsbox`: own pid namespace, hiding `.claude`) skip on it with the reason; unboxed runs and CI run them.
- No network (own net namespace, loopback only), own pid namespace, capabilities dropped. Only an allow-list of environment
  variables passes (no `GH_TOKEN`, no agent identity). The scratch `$FACTORY_HOME` is always the run's own; a caller's
  `FACTORY_HOME` never overrides it.
- The memory-backed tmpfs sizes are small (`$HOME` 8m, `/tmp` 64m, `/var/tmp` and `/dev/shm` 16m): anything the suite writes
  under `$HOME` is lost at the end of the run.

Fail closed: when user namespaces are unavailable, the sandbox cannot be built, or `gate_sandbox` is neither `0` nor `1`, the run
ends `error` with a note saying the command was **not run unsandboxed**; the command never runs outside the box. A boxed run's
note says `sandboxed`. The result cache is unchanged (same head, main and `gate_cmd`), so a `pass` from before the flag was
turned on is still reused; `gate run --no-cache` forces a boxed run.

Rollback: `factory config gate_sandbox 0` (the default). The next run is byte-identical to the unsandboxed gate.
