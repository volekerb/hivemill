# Old-chain deletion map

What depends on the old chain, and the order in which it can be deleted. This is a map, not a change: nothing here deletes a file. It answers one question for each unit of the old chain: what imports it, executes it or tests it, and which part of the kept set needs it.

**Old chain** (to delete): the Python `bin/factory` CLI and tick, the Python web UI (`web/`), `scout/`, `skilleval/`, `retro/`, the Go port of the CLI (`go/cmd/factory`, `go/internal/cli`, `go/internal/tick`, `go/internal/factorytest/*` and the packages only they use), the `factory-*` systemd units, and the chat-agent files in `roles/`.

**Kept set**: `hivemill` (`go/cmd/hivemill`, `go/internal/hm`), `hivemill-install` (`go/internal/install`), `worker-guard` and its hook (`go/internal/guard`, `roles/hooks/worker-guard*`), the `ci-*` commands (`go/internal/citools/*`) and the remaining `ci/*.py`.

Counts come from `git ls-files` at the commit this page was written. Line estimates count lines changed that are not whole-file deletions.

## How the kept set reaches the old chain

Go imports: `go list -deps` over every kept `go/cmd/*` shows **no** import of `cmd/factory`, `cli`, `tick`, `factorytest`, `spawn`, `web`, `scout`, `skilleval`, `retro`, `factorydb` or `store`. The Go side of the kept set cannot be broken at build time by deleting those packages, with one exception (a test import, below).

The kept set still reaches the old chain by seven other routes. These are the edits the steps below have to make:

| # | Kept part | Reaches the old chain by | Needed edit |
|---|---|---|---|
| 1 | `hivemill-install` (`go/internal/install/install.go`, `plugins/hivemill/scripts/install.py`) | Symlinks `bin/factory` into `~/.local/bin`; runs `factory init`, `config`, `project`, `serve`, `up`, `agents`, `--version`; starts the `herdr-server`, `factory-tick`, `factory-serve` and `factory-up` units | Rewrite (about 250 lines; may be two steps) |
| 2 | `go/internal/macbox/envlist_test.go` | Imports `go/internal/sandbox` for `WorkerEnvPass`, `WorkerKeepEnv` | About 25 lines: inline the constants |
| 3 | `ci/release_check.py`, `go/internal/citools/releasecheck` and `tests/test_release_check.py` | Import or list `scout/agent_runtime.py`, `scout/cursor_harness.py`, `roles/hooks/cursor-guard-adapter.py`; `pythonRuleDirs` names `bin/`, `web/`, `scout/`, `retro/`; the smoke test runs `bin/factory` | About 60 lines |
| 4 | `ci/affected.py`, `ci/ci_tier.py`, `ci/tier_rules.json` (and its copies `go/internal/hm/assets/tier_rules.json`, `go/internal/citools/risktier/testdata/tier_rules.json`), `go/internal/citools/{affected,tier,risktier}` | Constants `bin/factory`, `bin/factory_cli.py`; tier globs for `scout/`, `systemd/*`, `roles/hooks/*`, `roles/*settings*` | About 40 lines plus matching tests |
| 5 | `.github/workflows/tests.yml`, `ci/remote_gate.sh`, `ci/remote_gate_driver.sh` | `py_compile bin/factory bin/factory_cli.py` and `bin/factory --help`; `tests.yml` archives `systemd/install.sh`; `install.yml` triggers on `systemd/**` | Under 30 lines; **T3** (workflow files) |
| 6 | `ci/guard_diff.py` and `tests/test_guard_diff.py` | Diff the allow, deny and hook rules in `roles/settings/*.json.in` and derive `--roles all` from them | About 150 lines; `roles/settings` cannot go first |
| 7 | `go/internal/hm/guardexport.go`, `go/internal/tokens`, `go/internal/kit/spend/findings.go` | `os.Stat(<home>/bin/factory_cli.py)` decides whether the Python tick writes the guard export; strings and comments name `web/prices.py`, `web/tokens.py`, `web/dbpath.py` | About 30 lines; check behaviour for `guardexport.go` |

Also coupled: `launchd/hivemill.serve.plist.in` runs `{{.FactoryBin}} serve`, and the `herdr-server`, `tick` and `up` plists in `launchd/` start old-chain commands. `go/internal/launchd` and the `hivemill launchd` command in `go/cmd/hivemill/main.go` install them. `tests/harness.py` runs `bin/factory` as a subprocess and copies `scout/__init__.py`; 44 kept-code test files name `bin/factory`, `factory_cli` or `systemd` (list under "Tests" below).

### Literal strings: kept, or a one-line edit

These name the old chain only as text. They do not import or run it.

| Where | Status |
|---|---|
| `ci/guard_corpus.json` (34 `bin/factory` command strings), `ci/allow_loosen_t0331.json` (`systemd/install.sh --dry-run`) | **Kept**: fixtures for guard rules |
| `go/internal/guard/testdata/commands_cases.json`, `frontend_golden.json`; `guard/commands.go:21` (`-mfactory_cli`), `guard/frontend.go:62` (`FactoryNames`), `guard/factory.go`, `guardexport.go` | **Kept**: the rules still block those spellings on old checkouts |
| `roles/hooks/worker-guard.py` (`FACTORY_NAMES`, lines 34, 36, 121, 348, 924, 1879, 1943, 1989, 2030) | **Kept**: guard rule text |
| `go/internal/hm`: `factorykeys.go`, `prcreate.go`, `scope.go`, `init.go` (comments), `prbody.go` (runner allowlist has `factory`), `risk.go` (project name), `config.go` (`~/factory` home) | **Kept**: names of the project and its checkout, not the CLI |
| `go/internal/tokens/index.go:149` (`~/.local/state/factory/workers`) | **Kept**: data path |
| `ci/risk_tier.py`, `ci/fresh_export.py`, `ci/state_load.py` | **One-line edit**: comments and docstrings only |
| `ci/shard_timings.json`, `ci/scrub_allowlist.txt`, `ci/scrub_history_allowlist.txt` | **Mechanical**: remove the entries of deleted tests |
| `go/internal/tokens` (`prices.go`, `attribute.go`, `tokens_test.go`), `kit/spend/findings.go:26` | **One-line edit** each: drop the `web/…` pointers |

## Units

### `bin/` (4 files)

`bin/factory`, `bin/factory_cli.py` (about 25,000 lines), `bin/flags.json` and one more helper.

- Imported or executed by: `go/cmd/factory` (Go port, same commands), `tests/harness.py` (subprocess), about 110 Python tests, `ci/demo_seed.py`, `ci/screenshots.py`, `ci/freshbox_test.py`, `ci/install_container_test.py`, `ci/release_check.py`, `ci/remote_gate*.sh`, `tests.yml`, and `hivemill-install` (route 1).
- Kept set depends on it: routes 1, 4, 5, 7. Kept guard code only names it (literal strings).
- Delete after: routes 1, 4, 5 are cut, and the tests that run it are gone.

### `web/` (33 files) and `go/internal/web` (215 files)

- Imports or executes: `web/` is imported by `bin/factory_cli.py` (serve, home) and `ci/web_render_bench.py`; the Go `internal/web` is imported only by `factorytest/scout`.
- Tested by: 35 Python tests (`test_web_*`, `test_adoption`, `test_goal_tokens`, `test_runtime_display`, `test_utilization`, `test_db_layout` and others), 28 files under `tests/fixtures` for the web, and the Go tests inside `internal/web`.
- Kept set depends on it: only the string pointers in route 7. `ci/affected.py` mentions `web.views` in a docstring.
- Related CI tools that go with it (10 files): `ci/web_render_bench.py`, `visual_qa.py`, `screenshots.py`, `demo_cdp.py`, `demo_record.py`, `demo_scrub.py`, `demo_seed.py`, `demo_stills.py`, `demo_tour.py`, `demo_video.py`.

### `scout/` (19 files), `go/internal/scout` (27 files)

- Imported by: `bin/factory_cli.py`, `web/`, `tests/harness.py`, `ci/release_check.py`, `factorytest/scout`, `factorytest/cursor`.
- Tested by: 21 Python tests (`test_scout_*`, `test_cursor_*`, `test_nsbox_*`, `test_orgs`, `test_research*`, `test_testbox`, `test_workerbox*`) and 11 fixture files under `tests/fixtures` (scout and skilleval together).
- Kept set depends on it: route 3 (`release_check`, `releasecheck.go`, `test_release_check`) and `tests/harness.py`.

### `skilleval/` (27 files), `go/internal/skilleval` (9 files)

- Imported by: `bin/factory_cli.py` and `factorytest/skilleval1`, `skilleval2`.
- Tested by: 10 Python tests, plus the entries in `ci/scrub_allowlist.txt` and `scrub_history_allowlist.txt` for `tests/test_skilleval_vendor.py`.
- Kept set depends on it: nothing except the scrub allowlist entries.

### `retro/` (3 files), `go/internal/retro` (5 files)

- Imported by: `bin/factory_cli.py`, `go/internal/tick`.
- Tested by: `tests/test_retro_role.py`, `tests/test_retro_schedule.py`; `roles/retro.md` and `roles/hooks/worker-guard.py` mirror tests (`test_worker_guard_retro_mirror`).
- Kept set depends on it: `pythonRuleDirs` (route 3); the mirror test is a kept-edit.

### `go/cmd/factory` (3 files) and `go/internal/cli` (4,368 files, mostly testdata)

- Imported by: `factorytest/s2cli`, `cli/pydefaults`, `store`. Built by `factorytest/build.go:137` as the harness binary.
- Tested by: its own tests, and every test in `factorytest/*`.
- Kept set depends on it: nothing at build time. `tests.yml` runs `go build ./...` and `go test ./...`, so it needs no edit. `tests/test_guard_build.py` asserts `CLI_GO_CMDS_KNOWN` against it (kept-edit).

### `go/cmd/factory-forward` (2 files), `go/internal/forward` (5), `go/cmd/factory-go-version` (1), `go/internal/buildinfo` (3)

Self-contained leaves. `factory-forward` is named only in `bin/factory_cli.py`, `tests/test_spawn_precheck.py`, `tests/test_worker_gate.py` and `factorytest/s2cli/worker_gate_test.go`. `factory-go-version` and `buildinfo` use only each other. The kept set does not use them.

### `go/internal/tick` (123 files) and the packages only the old chain uses

- `tick` is imported by `cli` and itself.
- Old-only packages (files): `spawn` 112, `factorydb` 4, `store` 10, `digest` 7, `schedule` 6, `herdr` 4, `holdexpiry` 4, `identity` 3, `stuckpr` 3, `spendalert` 2, `agentruntime` 2, `cursorharness` 2, `launchd` 2, `sandbox` 26, `kit/clean` 24 (`fsx` 9, `procx` 15).
- Kept set depends on them: only `macbox` through `sandbox` (route 2) and `hivemill launchd` through `launchd` (see the launchd note above).
- Kept internal packages that stay: `blockcond`, `cdp`, `checklist`, `citools/*`, `freshdarwin`, `gate`, `ghx`, `gitx`, `gowrap`, `guard`, `hm`, `install`, `kit/spend`, `lanecheck`, `langshare`, `macbox`, `macfacts`, `machinesync`, `nonewpy`, `prflow`, `shlex`, `tokens`.

### `go/internal/factorytest/*` (221 files)

Root files (`build.go`, `doc.go`, `env.go`, `ported.go`, `ported_*.txt`, `testdata`) plus these subpackages (files): `cursor` 11, `guard1` 42, `guard2` 10, `guardtools` 4, `installer` 11, `s2cli` 50 with `msgs` 14, `restier` 4 and `tasks` 17, `sandbox1` 12, `sandbox2` 10, `scout` 12, `skilleval1` 19, `skilleval2` 11, `stub` 4.

- They import `cli`, `tick`, `spawn`, `web` and `scout`. `cli`, `tick`, `holdexpiry` and `spendalert` tests import them back, which makes a test-import cycle: these packages go in one step.
- Kept set depends on it: **coverage only**. `guard1`, `guard2`, `guardtools` and `installer` are Go ports of Python tests for `internal/guard` and `internal/install`, run through the `cmd/factory` harness. Deleting them removes that coverage for the kept guard and installer. Their useful cases must be moved into `go/internal/guard` and `go/internal/install` tests first, or accepted as lost. This is the largest judgement call in the plan.

### `systemd/` (15 files) and `launchd/` (7 files)

- Delete: `factory-serve.service`, `factory-tests.service`, `factory-tests.slice`, `factory-tick.service`, `factory-tick.timer`, `factory-up.service`, `herdr-server.service` (7 files); `launchd/hivemill.serve.plist.in`, `hivemill.herdr-server.plist.in` (and, once checked, `tick` and `up`).
- Keep: `hivemill.service`, `hivemill-digest.service` and `.timer`, `hivemill-health.service` and `.timer`, `hivemill-litestream.service`, `litestream.yml.in`, `install.sh`.
- `install.sh` stays as a file but loses the factory unit lines. Its kept consumers: the guard `install_dry_run` rule (T-0331), `ci/guard_diff.py`, `ci/remote_gate_driver.sh`, `tests.yml`, `install.yml`, `test_install`, `test_hivemill_install`, `test_worker_guard`.

### `roles/` (156 files)

- Delete: the role prompts `roles/{boss,cleaner,coder,_common,director,evaluator,judge,lead,pm,qa,researcher,retro,reviewer,scout,staff-engineer}.md` (15), `roles/plugins/*` (114), `roles/settings/*.json.in` (14), `roles/cursor/coder.md` (1), `roles/workflows/*.js` (2), `roles/hooks/boss-inbox.py` and `cursor-guard-adapter.py` (2).
- Keep: `roles/hivemill/*` (6), `roles/hooks/worker-guard` and `worker-guard.py`.
- Kept set depends on it: `roles/settings` (route 6), tier globs (route 4), and the guard Go package, which holds ports of `cursor-guard-adapter.py` and `boss-inbox.py` and lists `roles/settings` in its protected trees. Those Go ports must be checked before the Python files go.

### Tests (`tests/`, 407 Python files)

150 files name an old unit. By group: about 35 web, 21 scout, 10 skilleval, 2 retro, 38 other old-chain (`test_cli_*`, `test_serve_*`, `test_gate*`, `test_task_*`, `test_judge_*`, `test_hc_*`, `test_tick_phase_timers` and others), and 44 kept-code tests that need a small edit rather than deletion: `harness.py`, `_slot.py`, `test_guard_impl`, `test_guard_build`, `test_guard_diff`, `test_guard_factory_cli`, `test_worker_guard*`, `test_affected`, `test_ci_tier`, `test_risk_tier`, `test_release_check`, `test_publish_repo`, `test_install`, `test_hivemill_install`, `test_shard`, `test_scrub_scan`, `test_public_repo_safety`, `test_no_duplicate_defs`, `test_git_env_hermetic`, `test_live_checkout_deny`, `test_settings_templates`, `test_role_settings`, `test_gate_shards`, `test_slots`, `test_workflow_by_name`, `test_prcheck`, `test_priv_guard`, `test_flags`, `test_version`. Stubs for `gh`, `hc`, `herdr` and `ssh` are 4 files under `tests/stubs`. Confirm the groups with `git grep` before each step; they are by name.

## Deletion steps

Each step leaves `main` green: it deletes a layer only after everything that imports or runs it is already gone or edited. "Edits" is the estimate of changed lines that are not whole-file deletions. Only step 4 touches `.github/workflows` (**T3**).

1. **Leaves.** Delete `go/cmd/factory-forward`, `go/internal/forward`, `go/cmd/factory-go-version`, `go/internal/buildinfo`. Files: 10. Edits: about 20 (drop the forward cases from `test_spawn_precheck`, `test_worker_gate`, `worker_gate_test.go`). Workflows: no.
2. **Web.** Delete `web/` (33), `go/internal/web` (215), the 35 web tests, the web fixtures (28), the 10 related CI tools, `go/internal/factorytest/scout` (12). Files: about 333. Edits: about 20 (`tokens` and `kit/spend` strings, the `ci/affected.py` docstring, `ci/shard_timings.json` entries). Workflows: no.
3. **Scout, skilleval, retro (Python and Go).** Delete `scout/` (19), `skilleval/` (27), `retro/` (3), `go/internal/scout` (27), `go/internal/skilleval` (9), `go/internal/retro` (5), their tests (33) and fixtures (11), `factorytest/skilleval1`, `skilleval2` (30), role files `roles/scout.md`, `roles/retro.md`, `roles/researcher.md`. Files: about 205. Edits: about 150 (route 3: `ci/release_check.py`, `releasecheck.go` and its test, `tests/harness.py` scout copy, `test_release_check`; the scrub allowlists). Workflows: no.
4. **Cut the kept set from `bin/factory`.** Remove `bin/factory` from `tests.yml` (py-compile, `--help`), `remote_gate.sh` and `remote_gate_driver.sh`; replace the `BIN` constants in `ci/affected.py`, `ci/ci_tier.py`, `tier.go`, `affected.go`, `risktier`, and the three `tier_rules.json` copies; edit `tests/harness.py`'s `FACTORY_IMPL` switch and `guardexport.go`. Files: about 20 edited, 0 deleted. Edits: about 200. Workflows: **yes, T3** (`.github/workflows/tests.yml`).
5. **Rewrite `hivemill-install`.** Remove the `factory` symlink, the `factory init|config|project|serve|up|agents` calls and the old unit starts from `go/internal/install/install.go` and `plugins/hivemill/scripts/install.py`; remove the factory unit lines from `systemd/install.sh`; fix `install.yml` triggers; adjust `test_install`, `test_hivemill_install`, `ci/freshbox_test.py`, `ci/install_container_test.py`. Files: about 10 edited. Edits: about 280. Split in two if it passes 300: (5a) Go install and its tests, (5b) the shell, Python shim and the CI scripts. Workflows: **yes, T3** if `install.yml` is edited, otherwise no.
6. **Go outer layer.** Delete `go/cmd/factory` (3), `go/internal/cli` (4,368), `go/internal/factorytest` (about 209 left after steps 2 and 3). Before this step, move or drop the `guard1`, `guard2`, `guardtools`, `installer` coverage as decided above. Files: about 4,580. Edits: about 40 (`test_guard_build`'s `CLI_GO_CMDS_KNOWN`, `tests/test_tmp_leftover_sweep.py`). Workflows: no (`go build ./...` and `go test ./...` need no edit).
7. **Go inner packages.** Delete `go/internal/tick` (123), `spawn` (112), `factorydb` (4), `store` (10), `digest` (7), `schedule` (6), `herdr` (4), `holdexpiry` (4), `identity` (3), `stuckpr` (3), `spendalert` (2), `agentruntime` (2), `cursorharness` (2), `sandbox` (26), `kit/clean` (24), and `launchd` (2) with the matching `hivemill launchd` command and the `launchd/` plists, after checking the daemon does not need them. Files: about 335. Edits: about 40 (`macbox/envlist_test.go`, `go/cmd/hivemill/main.go`). Workflows: no.
8. **Python chain, role files, units.** Delete `bin/` (4), the 38 other old-chain tests, the 7 `factory-*`/`herdr-server` units, the roles files (`roles/*.md` 15, `roles/plugins` 114, `roles/cursor` 1, `roles/workflows` 2, `roles/hooks/boss-inbox.py`, `cursor-guard-adapter.py`), the old CI tools (`demo_seed.py`, `freshbox_test.py`, `install_container_test.py`, `profile_baseline.py`), `tests/stubs` if unused. Files: about 190. Edits: about 100 (`tests/test_guard_factory_cli.py`, `test_worker_guard*`, scrub allowlists, `ci/shard_timings.json`). Workflows: no.
9. **Settings templates.** Delete `roles/settings/*.json.in` (14) after replacing the `--roles all` derivation in `ci/guard_diff.py` and its tests (route 6). Files: 14 deleted, about 4 edited. Edits: about 150. Workflows: no (check `tests.yml` guard jobs that call `guard_diff`; if one lists a `roles/settings` path, mark this step **T3**).
10. **Docs.** Delete or archive the pages under "Legacy pipeline (paused)" in `docs/README.md` and fix the index. A separate pass; it does not change code.

Order is forced by three rules: delete a layer only after its importers; delete `factorytest` as one unit (test-import cycle); do routes 1, 4 and 5 before `bin/` and the units go. Steps 1 to 3 are independent of each other and can run in parallel; 4 and 5 must come before 8; 6 before 7.
