# Tests & CI

The suite is offline: it runs `bin/factory` as a subprocess against a throwaway `FACTORY_HOME` with `herdr` and `gh` stubbed (`tests/stubs/`), and never touches the live DB.

```
python3 -m py_compile bin/factory bin/factory_cli.py
python3 -m unittest discover -s tests -v
```

`.github/workflows/tests.yml` runs the same commands on every pull request to `main` and on manual dispatch. It does not run on push, because the tick pushes state commits to `main` every minute.

The workflow splits the suite into 10 parallel `unittest-shard (N)` jobs (30 min timeout each; see `.github/workflows/tests.yml`; the docs previously said 20 min). `ci/shard.py` packs the modules of `tests/test_*.py` onto the shards greedily, longest first onto the lightest shard (ties: lowest shard, then module name), using the per-module seconds in `ci/shard_timings.json`; a module missing from the file gets the median duration, so new test files need no edit to the timing file. **T-1168:** if one module’s timing alone exceeds the usual 15% bound used in `tests/test_shard.py` and is listed in `SPLIT_TEST_IDS_MODULES`, it is still named on every shard (so every job runs a slice), but the runner picks unittest **test ids** on that module only, round-robin on the sorted id list (same rule as guard-unittest T-1039). The report JSON carries `test_ids` for that module; `shard.py verify` checks the union and disjointness. Other modules stay whole (one `discover` per module per shard). Each shard uploads a JSON report (its modules and test count). The final job `unittest` (5 min timeout, `needs: unittest-shard`, `if: always()`) is the required check: it fails unless every shard succeeded, verifies with `python ci/shard.py verify` that every module is covered exactly as specified, puts the total test count in the job summary, and runs the test-count gate below. The shard checks are extra and not required. To change the shard count, edit both `SHARDS` and the matrix (a test checks they agree). The single-job command for local runs stays `python3 -m unittest discover -s tests -v`; one shard locally: `python3 ci/shard.py run --shard 1 --total 10 --out /tmp/s1.json`.

#### Shard timing file

`ci/shard_timings.json` is `{module: seconds}`. `shard.py run` writes each module's seconds into its report (`seconds`), and the job summary shows the per-shard total. To regenerate after a big change, use only a clean, all-green run (every shard and `unittest` passed; a run with a failed shard has truncated or missing timings): open a PR run, download the `shard-*` artifacts into one directory (`gh run download <run-id> -p 'shard-*' -D d && mkdir d/all && cp d/shard-*/*.json d/all/`), then `python3 ci/shard.py timings --dir d/all` and commit the file. A test checks that the checked-in timings pack within 15% of total/10 for the modules that stay **whole** (greedy pack only). If one module alone exceeds that bound *and* it is in `SPLIT_TEST_IDS_MODULES` (T-1168), it is named on every shard but only a subset of its unittest ids runs per shard, so wall time can stay under the cap; if that list is empty, the old rule still applies: the oversize module must sit alone on a shard and the other nine must pack within 15% of their own mean. Locally, `python3 tests/timing_runner.py` lists the slowest individual tests, not modules.

The judge only treats a missing CI report as green until `required_checks` is set. After this workflow has merged, the director enables the gate with `factory config required_checks unittest`; from then on `factory judge --merge` refuses with "required check unittest missing" until that check is present and green.

The repo hygiene tests (`tests/test_repo_hygiene.py`, no workflow change) run three offline checks over tracked files:
- Conflict markers: no line starts with a seven-character conflict marker (`=======` is allowed, it is a markdown underline).
- Secret patterns: GitHub, Anthropic, AWS, Slack tokens and PEM private-key headers are flagged by path:line and pattern name, never the value. A line with `secret-scan: allow` is exempt.
- Role-prompt budget: `roles/_common.md` plus any one `roles/<role>.md` must stay within `ROLE_PROMPT_BUDGET_BYTES` (12000). Raising it is its own PR; prefer moving detail into a per-role skill under `roles/plugins/<role>/`.
- Role-prompt target: the same pair should stay within `ROLE_PROMPT_TARGET_BYTES` (10800); `tests/test_repo_hygiene.py` enforces it.
`exports/` and `state/` are skipped by the marker and secret scans.

### CI test-count gate

Inside the `unittest` aggregator job, `ci/test_count_gate.py` counts the unittest tests discovered in the PR tree and in its base commit (`tests/` of each, counted the same way) and prints `base=<X> pr=<Y>`. The job fails if the PR has fewer tests than base, or if discovery hits an import error. To drop tests on purpose, put a line `tests-removed: <reason>` (non-empty reason, line start, case-insensitive) in the PR body; the job then passes and logs the drop. The workflow does not trigger on `pull_request` `edited`: an edit-only run would put a second `unittest` check (a skipped job counts as passing for a required check) on a head SHA whose full run is pending or red, and its concurrency group would cancel that run. So editing the body alone re-runs nothing: after adding `tests-removed:`, re-run the failed `unittest` job (the gate reads the live body via `gh`, not the stale event payload) or push a commit.

Config `max_total_claude` (default `10`, see `factory config`): the spawn gate also closes when the host runs that many claude processes in total, the owner's own panes included (top-level ones only; a spawn with no process yet still counts).

Source: Repo hygiene tests: idea from gAmUssA/coding-policy @7a25169 (declares MIT, no LICENSE file); our own code.

### Generic scrub check (T-0392)

`tests/test_scrub_clean.py` runs `ci/scrub_scan.py --tree .` with `SCRUB_TERMS_FILE` unset (generic patterns only: home paths, LAN addresses, non-example emails, token shapes) and fails on any hit, printing it. A new hit is fixed by rewriting the line to a neutral placeholder (`/home/user`, `example.com`) or, when the literal is the point (vendored attribution, guard-test data), by adding an exact `path:pattern-id:regex  # reason` line to `ci/scrub_allowlist.txt` (format in `docs/scrub-scan.md`; write `hom[e]` instead of `home` in the regex so the allowlist is not a hit itself). Owner terms are never in CI: they live only in the owner's local terms file and are never allowlisted.

### Running tests on your-box (T-0284)

Run only the test modules your change touches, e.g. `python3 -m unittest discover -s tests -p test_gate.py`; the full suite is CI's job (the sharded `unittest-shard` jobs and the `unittest` aggregator). A local full run (more than 50 test modules in one process) takes one of `test_slots` (default 2) host-wide flock slots under `~/.cache/factory-test-slots/` (`tests/_slot.py`); with none free it prints `waiting for a test slot` every 30 s, and after 30 min it runs anyway with a warning. The affected-tests step of `factory prcheck` also takes a slot (T-0876): it prints `waiting for a test slot (N/M busy)` while all are busy, and after 15 min (`FACTORY_PRCHECK_SLOT_WAIT_S`) fails with `no test slot free: ... NOT a pass; CI will verify` without running. While load1 is at or above `gate_max_load_ratio` x CPUs (owner M16816) only one local suite runs box-wide: the prcheck affected step is then refused at once with `load1 X >= cap Y: one local suite box-wide (M16816); rely on PR CI` when another suite holds a slot, and that step passes if CI is green on the PR head (T-0873 rule), else fails until CI is. Other targeted runs and `CI=true` take no slot. `factory res` shows `test runs: <n> full suites running (slots <used>/<N>)`. The spawn gate also holds new spawns while load1 is above `gate_max_load_ratio` (default 1.25) x the CPU count; `factory res` shows it and a queued spawn names it as the reason. Persistent HQ relaunches (boss, director, leads) and judges are exempt from the load cap (judges still obey every other cap, including `max_live_judges`).

### Tiered testing: ci/affected.py (T-0519)

`python3 ci/affected.py --base origin/main [--head HEAD] [--list|--run]` selects the test modules a diff can affect (stdlib only; `--list` is the default, `--run` runs the selection with the shard.py loader and prints `affected: Ran N tests in M modules: OK`). Rules on `git diff --name-only --no-renames base...head`:

1. `tests/test_X.py` selects X (a deleted module is not selected; a rename selects the new name). `tests/harness.py`, `tests/_slot.py` and `tests/stubs/**` mean `FULL`.
2. `ci/X.py` selects modules that mention `ci/X` or `X.py`, plus `tests/test_X.py`.
3. `bin/factory`: base and head are parsed with `ast` and the top-level units whose source changed are collected. `SCHEMA`, `DEFAULT_CONFIG`, `main`, `db`, a changed top-level statement, or any def with 15 or more call sites means `FULL`. Otherwise modules that mention a changed name as a whole word are selected (`cmd_judge` also selects modules that mention `judge`).
4. `roles/**` selects modules that mention `roles/` or the role file name. `docs/**` and `*.md` add nothing beyond the smoke set.
5. The smoke set is always added: config key `affected_smoke` (space-separated module names; default `test_db_layout.py test_inbox.py test_judge.py`). A selection above 40% of the modules means `FULL`.

Any other changed path (web/, ci/*.json, ...) selects the modules that mention its path, its file name or its dotted module path; if none does, it means `FULL`. On `FULL` the output is `FULL (<triggering rule>)`; `--run` then runs every module. Coders, reviewers and QA run `--quick --run` (below) and quote its output instead of the full suite; the full suite runs only in the judge's local gate, which catches a missed dependency before merge (the cost is one rejection, not a broken main).

#### Quick mode and the local-fast policy (T-0642)

`--quick` (or `--budget SECONDS`, default 120) is the form the role files use: affected modules plus the smoke set, one process, time printed (`affected: elapsed N s of 120 s budget`). A selection that would be `FULL` prints `FULL needed: rely on CI` and runs nothing (exit 0). If the budget is spent before every module ran, the rest are listed on stderr and the exit code is 4. The whole policy (what runs locally, what CI runs, the guard, gate fast mode) is in `docs/testing.md`.

### CI tiers: ci/ci_tier.py (T-0658)

On a pull request each job of `tests.yml` (`unittest-shard (1..10)`, `go`, `unittest`; names and required contexts unchanged) first classifies the diff with `ci/ci_tier.py classify`. The script and `ci/affected.py` are taken from the **base commit** (`git archive`), so a PR cannot change its own tier. If the base has no `ci/ci_tier.py`, or the classifier errors, the run is full.

| tier | when | what runs |
|---|---|---|
| docs | only `*.md` outside `roles/`, or files under `docs/` (if a test module mentions a changed file, e.g. `CHANGELOG.md`, those modules run as the affected set instead) | `ci-tier` classifies once; `docs-check` runs `ci_tier.py docs-check` and `test_docs_index.py`; the shard, `go`, and guard jobs are not scheduled; `unittest` is the fan-in (same required check name; passes when `docs-check` succeeds) |
| normal | anything else | each shard runs only its share of the `ci/affected.py` modules (a shard with none reports success); `unittest` verifies exactly that set ran; `go` builds only if `go/**` changed |
| risky | `.github/**`, `roles/hooks/**`, `roles/**/settings*`, `ci/public_text_guard.py`, `ci/publish_repo.py`, `ci/scrub_*`, `ci/guard_*`, the CI gates (`ci_tier.py`, `affected.py`, `shard.py`, `test_count_gate.py`), `scout/nsbox.py`, `gatebox.py`, `ghcred.py`, `egress.py`, `sandbox.py`, `workerbox.py`, `systemd/*`; a change to `cmd_judge`, `judge_*`, `gate*`, `verdict_carries`, `head_has_main`, `refresh_branch`, `git_config_guard`, `noreply_email`, `_push_policy`, `OWNER_CAP_KEYS`, `DEFAULT_CONFIG`, `SCHEMA`, `sandbox_*`, or a refusal/guard def (see `RISKY_DEFS`) in `bin/factory`; more than 600 changed lines; a changed file whose `git diff --numstat` line count is not numeric (e.g. binary under `docs/`) — see module docstring in `ci/ci_tier.py` (T-1166) | full suite and `go`, as before |

A push to `main` and `workflow_dispatch` always run the full suite. When `ci/affected.py` answers `FULL`, a normal PR runs the full suite too. The test-count gate runs in every tier. Change the rules in `ci/ci_tier.py` (itself risky); `python3 ci/ci_tier.py classify --base origin/main` shows the tier of your branch locally.

### Go

The `go` job in `tests.yml` (`actions/setup-go@v5`, `go-version-file: go/go.mod`, `GOTOOLCHAIN=local`) runs in `go/`: a gofmt check (`test -z "$(gofmt -l .)"`, lists the unformatted files on failure), `go vet ./...`, `go test -race -count=1 ./...`, `go build ./...`. The `unittest` aggregator has `needs: [unittest-shard, go]` and fails unless both results are `success`, so a red `go` job turns the required `unittest` check red; the check name and branch protection are unchanged. Run locally (toolchain: `docs/go-toolchain.md`):

```
cd go && test -z "$(gofmt -l .)" && go vet ./... && go test -race -count=1 ./... && go build ./...
```

The `guard-equiv` job (T-0355) is the Go/Python equivalence gate and a sub-check of `unittest`, split in two jobs since T-1009 to stay under 15 minutes each: `guard-equiv` runs only the strict guard_diff step and `guard-equiv-rest` runs the routed modules and REAL_GO tests with the same path gating (the description below covers both) (`needs: [unittest-shard, go, guard-unittest, guard-diff, guard-equiv, guard-equiv-rest]`, results checked as `GUARD_EQUIV_RESULT` and `GUARD_EQUIV_REST_RESULT`; the required check name stays `unittest`). It builds `go/cmd/worker-guard` once per shard and runs `python3 ci/guard_diff.py --impl go --go-bin "$GUARD_GO_BIN" --roles all --role-shard N --role-shards 4 --mode both|worker --strict` (zero differences; four shards; shard 1 runs both modes, the others worker mode; the check name stays `guard-equiv`), then `GUARD_IMPL=diff python3 ci/guard_impl_unittest.py run --routed-rest`: the hook_run-routed test modules that `guard-diff` does not run, listed at runtime by `ci/guard_impl_unittest.py list --routed-rest` (never named in the YAML). It also links `~/.local/go` to the setup-go toolchain and runs `tests/test_guard_build.py`, so its REAL_GO tests run instead of skipping. The real steps run on guard-path PRs (the `guard-diff` path list plus `ci/guard_impl_unittest.py`) and on a push to main that changes a guard path (a push with no parent still runs); the path step also runs when a guard test source changed (`tests/guard_impl.py`, the listed modules), and fails open to run=true if either `list` command fails or prints nothing; otherwise the job succeeds as skipped. Timeout 30 minutes; no allowlist and no `continue-on-error`. Measured hook latency: `docs/worker-guard-go.md` ("Latency").

Cost and fork-safety policy for these workflows (free runners only, `tests/test_ci_policy.py`): [ci-cost.md](ci-cost.md).
