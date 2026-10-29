# Tests & CI

The suite is offline: it runs `bin/factory` as a subprocess against a throwaway `FACTORY_HOME` with `herdr` and `gh` stubbed (`tests/stubs/`), and never touches the live DB.

```
python3 -m py_compile bin/factory
python3 -m unittest discover -s tests -v
```

`.github/workflows/tests.yml` runs the same commands on every pull request to `main` and on manual dispatch. It does not run on push, because the tick pushes state commits to `main` every minute.

The workflow splits the suite into 10 parallel `unittest-shard (N)` jobs (20 min timeout each; the slowest shard must stay at most 12 min wall time: with 5 shards the slowest took 13m17 on run 40565301275, so there are 10; 6 gave about 11m50 on run 40636918368). `ci/shard.py` packs the modules of `tests/test_*.py` onto the shards greedily, longest first onto the lightest shard (ties: lowest shard, then module name), using the per-module seconds in `ci/shard_timings.json`; a module missing from the file gets the median duration, so new test files need no edit to the timing file; each shard uploads a JSON report (its modules and test count). The final job `unittest` (5 min timeout, `needs: unittest-shard`, `if: always()`) is the required check: it fails unless every shard succeeded, verifies with `python ci/shard.py verify` that every module ran exactly once, puts the total test count in the job summary, and runs the test-count gate below. The shard checks are extra and not required. To change the shard count, edit both `SHARDS` and the matrix (a test checks they agree). The single-job command for local runs stays `python3 -m unittest discover -s tests -v`; one shard locally: `python3 ci/shard.py run --shard 1 --total 10 --out /tmp/s1.json`.

#### Shard timing file

`ci/shard_timings.json` is `{module: seconds}`. `shard.py run` writes each module's seconds into its report (`seconds`), and the job summary shows the per-shard total. To regenerate after a big change, use only a clean, all-green run (every shard and `unittest` passed; a run with a failed shard has truncated or missing timings): open a PR run, download the `shard-*` artifacts into one directory (`gh run download <run-id> -p 'shard-*' -D d && mkdir d/all && cp d/shard-*/*.json d/all/`), then `python3 ci/shard.py timings --dir d/all` and commit the file. A test checks that the checked-in timings pack within 15% of total/10. Locally, `python3 tests/timing_runner.py` lists the slowest individual tests, not modules.

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

Run only the test modules your change touches, e.g. `python3 -m unittest discover -s tests -p test_gate.py`; the full suite is CI's job (the sharded `unittest-shard` jobs and the `unittest` aggregator). A local full run (more than 50 test modules in one process) takes one of `test_slots` (default 2) host-wide flock slots under `~/.cache/factory-test-slots/` (`tests/_slot.py`); with none free it prints `waiting for a test slot` every 30 s, and after 30 min it runs anyway with a warning. Runs with `CI=true` and targeted runs take no slot. `factory res` shows `test runs: <n> full suites running (slots <used>/<N>)`. The spawn gate also holds new spawns while load1 is above `gate_max_load_ratio` (default 1.25) x the CPU count; `factory res` shows it and a queued spawn names it as the reason. Persistent HQ relaunches (boss, director, leads) and judges are exempt from the load cap (judges still obey every other cap, including `max_live_judges`).

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

### Go

The `go` job in `tests.yml` (`actions/setup-go@v5`, `go-version-file: go/go.mod`, `GOTOOLCHAIN=local`) runs in `go/`: a gofmt check (`test -z "$(gofmt -l .)"`, lists the unformatted files on failure), `go vet ./...`, `go test -race -count=1 ./...`, `go build ./...`. The `unittest` aggregator has `needs: [unittest-shard, go]` and fails unless both results are `success`, so a red `go` job turns the required `unittest` check red; the check name and branch protection are unchanged. Run locally (toolchain: `docs/go-toolchain.md`):

```
cd go && test -z "$(gofmt -l .)" && go vet ./... && go test -race -count=1 ./... && go build ./...
```

Cost and fork-safety policy for these workflows (free runners only, `tests/test_ci_policy.py`): [ci-cost.md](ci-cost.md).
