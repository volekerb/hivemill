# Testing policy: local = fast, CI = full

Owner decision 2026-10-29 (T-0642): agents run only fast checks locally and take the rest from CI. Full local suites held the load gate for hours.

## Locally (coder, reviewer, QA)

- Lint: `python3 -m py_compile` on the touched Python files.
- `python3 ci/affected.py --base origin/main --quick --run`: the smoke set (config key `affected_smoke`) plus the modules the diff can affect, in one process, under 2 minutes. It prints the elapsed time. When the diff needs the whole suite it prints `FULL needed: rely on CI` and runs nothing; stop there.
- A failing test: rerun only that module, `python3 -m unittest discover -s tests -p test_X.py` (or `python3 -m unittest tests.test_X`).
- Never `python3 -m unittest discover` over the whole tree, a wildcard `-p`, or `python3 -m unittest` with no module. The worker guard (`roles/hooks/worker-guard.py`, `unittest_full`) refuses these for the coder, qa and reviewer roles with a message pointing to `--quick`.
- Never export `GIT_DIR`, `GIT_WORK_TREE` or other `GIT_*` variables for a test run (lesson L-050): the harness git calls would hit the real repo. Before a run, `env | grep ^GIT_ | grep -v ^GIT_EDITOR=true` must print nothing.

## In CI

CI runs the full sharded suite, go and integration (`docs/tests-and-ci.md`). Agents wait with `gh pr checks <pr> --watch` (coders: skill `coder-skills:ci-wait`) and read a red check with `gh run view <run> --log-failed`. Reviewers and QA judge the diff and the CI result; QA runs targeted checks for its acceptance criteria, not the suite.

## The merge gate

For projects listed in config `gate_fast_projects` (default `factory`), `factory gate run` runs fast mode: `py_compile` + `ci/affected.py --quick --budget 600 --run` against the verified main. The judge still needs the GitHub checks green, so CI is the full gate. Rollback: `factory config gate_fast_projects ''` restores the project's `gate_cmd`.
