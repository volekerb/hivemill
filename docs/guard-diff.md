# Guard diff (ci/guard_diff.py)

`ci/guard_diff.py` compares the worker-guard hook on a base ref (normally `origin/main`) with the hook in your working tree. It answers one question mechanically: does this PR silently loosen a block that main has, or add a false positive?

## In CI

The `guard-diff` job holds the real-corpus step in `.github/workflows/tests.yml` (its own 30 min cap; the hook unittests run in the separate `guard-unittest-shard` jobs, 3 matrix shards of the test ids (round-robin on the sorted ids, T-1039, `ci/guard_impl_unittest.py run --shard I --shards 3`), each with its own 30 min cap; the `guard-unittest` fan-in job fails unless every shard succeeded, so the two no longer share one cap). The separate `guard-self-compare` job runs the full corpus under `--mode worker --role evaluator` with `--base HEAD` (hook against itself) on every PR, push to main, and workflow_dispatch, without path gating. The step runs both commands below on every PR that changes `roles/hooks/**`, `roles/settings/**`, `ci/guard_corpus.json`, `ci/guard_diff.py`, `ci/public_text_guard.py`, `ci/scrub_scan.py` (the hook loads them), `.github/workflows/tests.yml` (so a gate change proves itself) or `go/internal/guard/**`, `go/cmd/worker-guard/**` or `go/internal/shlex/**`, with `--base` set to the PR base sha. The `guard-unittest`, `guard-diff`, and `guard-self-compare` jobs all feed the required `unittest` aggregate (any result but success in either, including cancelled or timed out, fails it; `unittest` stays the required check, and `guard-unittest` is added to `required_checks` in `ci/publish_metadata.json` for the public repo), so rc!=0 (a LOOSENED line) fails the PR. The tool (`ci/guard_diff.py`, `tests/_slot.py`) is extracted from the base commit, so a PR cannot weaken its own gate; a base without it falls back to the PR copy. The corpus is passed twice on both runs, `--corpus <base ci/guard_corpus.json> --corpus ci/guard_corpus.json`, so deleting a `must_block` entry in the PR still shows as LOOSENED. The job fails if `ci/guard_corpus.json` has 100 lines or fewer (not the real corpus). A PR that adds `ci/allow_loosen_*.json` has it merged into one `--allow-loosen` list and printed in the job log for the reviewer. Coders do not run the tool locally (load, M16816); they read this job's log.

Go-only PRs: a PR that changes only `go/internal/guard/**`, `go/cmd/worker-guard/**` or `go/internal/shlex/**` triggers this step, but the step runs the tool with its default `--impl python`, which compares the Python hooks only, so that run proves nothing about Go. Go is covered elsewhere: by the `GUARD_IMPL=diff` step in the separate `guard-unittest-shard` jobs (no path condition, runs on every PR; the `guard-unittest` job is only the fan-in that fails unless every shard succeeded), and by the `guard-equiv` job, which on guard-path PRs and pushes to main (and when a guard test source changed) builds `worker-guard` and runs the strict Go-vs-Python gate `guard_diff.py --impl go --roles all --mode both --strict`; its sibling `guard-equiv-rest` runs the routed unittest modules under `GUARD_IMPL=diff` and the REAL_GO build tests with the same path gating.

## Usage (manual run, needs a named authorization)

CI is the normal route: the `guard-diff` job runs these commands and reviewers read its log. Both commands are `--impl python` only: they compare the Python hooks and prove nothing about Go, so a green `guard-diff` is not Go coverage (see `guard-unittest-shard` and `guard-equiv` above and below). A local run is not allowed by default (the reviewer and coder rules permit only `ci/affected.py --quick` locally, and the tool is heavy); run it only when a task, the owner or the boss names the authorization to do so.

```
git fetch origin
python3 ci/guard_diff.py --base origin/main --mode both
python3 ci/guard_diff.py --base origin/main --mode worker --role evaluator
```

The first run covers every untagged entry as `--role coder` and `--hq`, plus the role-tagged entries (below), which it runs under their own role. The second run replays the whole corpus under the evaluator role as a belt-and-braces check; both must report 0 loosened.

Options:

- Corpus token `@SHARED@`: a temp stand-in shared checkout (`$TMPDIR/guard-diff-shared-<uid>/factory`, holds `.git`). A command naming it runs with `WORKER_GUARD_LIVE_HOME` set to it, so shared-checkout git rules are exercised on any machine (CI has no ~/factory).
- Corpus token `@SCRATCH@`: a temp stand-in worker scratchpad (`$TMPDIR/guard-diff-scratch-<uid>`). A command naming it runs with `WORKER_GUARD_SCRATCH` set to it. `@SHARED@` and `@LIVE_HOME@` cannot appear in one command (the first replaces the live home for the hook).
- `--base REF` (required): git ref that holds the base hook. It is read with `git show --end-of-options <base>:<hook>` into a temp dir, at a path that mirrors the repo layout (`roles/hooks/worker-guard.py`), because the hook derives its live home from its own path. A ref starting with `-` is refused (exit 2).
- `--hook PATH`: hook under test, default `roles/hooks/worker-guard.py`. A copy outside the repo is compared with main's `roles/hooks/worker-guard.py` (handy for "delete one rule and see what changes").
- `--corpus FILE` (repeatable): corpus files, default `ci/guard_corpus.json`.
- `--mode worker|hq|both` (default `both`): `worker` runs `--role <role>`, `hq` runs `--hq`, exactly as `roles/settings/*.json.in` invoke the hook.
- `--role ROLE`: role for worker mode, default `coder`. Under `--role evaluator` the tool sets `FACTORY_HOME` to a fresh temp dir in the environment of both hooks, as the evaluator sandbox does.
- `--allow-loosen FILE`: commands whose LOOSENED line is planned, one exact command per line (or a `.json` list of strings). A command that spans several lines can only be listed in a `.json` list.
- `--timeout SECONDS`: per hook call, default 5.

Every corpus command is sent to both hooks as PreToolUse JSON on stdin (`{"tool_name":"Bash","tool_input":{"command":...}}`). A command is never executed.

## Output

One line per command whose exit code differs, then a summary:

```
LOOSENED base=2 pr=0 worker <command>
TIGHTENED base=0 pr=2 hq <command>
TIMEOUT base=2 pr=timeout worker <command>
guard-diff: 490 commands, 0 loosened, 0 tightened
```

`CHANGED` is printed when both exit codes differ but neither is a block (2). A crash of the PR hook (exit 1) against a block on main shows as LOOSENED.

## Settings templates

The tool also diffs `roles/settings/<role>.json.in` against `--base` (a base that still has the old `roles/settings/<role>.json` is compared after mapping the owner's paths to the template tokens), over the union of the roles on base and in the tree. It covers:

- rules: `permissions.allow` and `permissions.deny`;
- hooks, for every event (not only `PreToolUse`): each hook is compared as `<event>|<matcher>|<type>|<command>` (a missing matcher or type is an empty field), so deleting a matcher group or narrowing a matcher is a removal plus an addition;
- `permissions.ask`: a removed rule is LOOSENED, an added one TIGHTENED (T-0944);
- `autoMode`: each entry of a list value as `<key>: <text>` (a non-list value as `<key>=<json>`); any change prints `SETTINGS-LOOSENED <role> autoMode removed: ...` for the old value and `SETTINGS-CHANGED <role> autoMode added: ...` for the new one (fail closed; plan it in `--allow-loosen`). An added entry whose key has no removed entry in that role (a pure addition under that key, even when an entry under another key was removed) is also counted as LOOSENED, as `<role> autoMode added: ...`; a same-key swap is counted only through the removed old value and relies on a human reading the CHANGED line;
- hook `timeout`, as its own `hook-timeout` line `<event>|<matcher>|<type>|<command>|timeout=<N>` (empty when unset), so the hook text above stays unchanged for consumers; a changed or removed timeout prints `SETTINGS-LOOSENED <role> hook-timeout removed: ...` plus `SETTINGS-CHANGED <role> hook-timeout added: ...`. A hook present on only one side reports only its hook line, not a timeout line;
- the `env` block, as `K=V`;
- the template set: a role whose template is on base but gone from the tree.

A removed `deny` or `ask` rule, an added `allow` rule, a removed hook, a removed or changed `env` entry or a removed template prints `SETTINGS-LOOSENED <role> <kind> removed|added: <rule>` (for a removed template: `SETTINGS-LOOSENED <role> template removed`) and counts as LOOSENED. A hook line reads for example `SETTINGS-LOOSENED qa hook removed: PreToolUse|Edit|Write|MultiEdit|NotebookEdit|command|python3 ...`. `--allow-loosen` takes the same text without the prefix, one `<role> <kind> removed: <rule>` or `<role> template removed` per line. The base role names are listed with `git ls-tree -z`, so a role name with a non-ASCII character is not missed. The reverse of a loosening prints `SETTINGS-TIGHTENED`; a new `env` key (or the new value of a changed key) prints `SETTINGS-CHANGED <role> env added: K=V` and is not counted. See `docs/settings-templates.md`.

## Exit codes

- `0`: no LOOSENED line outside `--allow-loosen`, and no timeout.
- `1`: at least one unplanned LOOSENED line, or a TIMEOUT (a hook that cannot decide is not a pass).
- `2`: usage or git error, such as an unreadable base, hook or corpus. It fails closed: an unreadable base is never "no diff".

## Corpus format

`ci/guard_corpus.json` is a JSON object with two lists:

- `must_block`: commands the hook must refuse (exit 2). A string is checked in worker mode (`--role coder`) by `tests/test_guard_diff.py`. An object `{"cmd": "...", "modes": ["worker", "hq"]}` may carry a `modes` list, but `ci/guard_diff.py` never reads it (`entry_item` takes only `cmd`, `role`, `config` and `env`). Only `CorpusSanityTests.check` in `tests/test_guard_diff.py` reads `modes`, and only for an untagged `must_block` entry (default `["worker"]`); a role tag overrides it, and an untagged `must_allow` entry is always checked in `["worker", "hq"]` whatever `modes` says. So `modes` never changes where `guard_diff` or `guard-equiv` runs an entry.
- Where an entry runs in `guard_diff`: an untagged entry (a string, or an object with no `role`) runs in every mode of `--mode`, and with `--roles all` (what `guard-equiv` runs) it runs in worker mode once per role plus in hq; without `--roles` its worker run uses the default `--role`. A role-tagged entry runs in worker mode only, under that role, and never in hq.
- Role tag: an object `{"cmd": "...", "role": "evaluator"}` is checked in worker mode only, under that role, with a temp `FACTORY_HOME` in the hook environment. This is how the evaluator class lives in the same corpus: `FACTORY_HOME` writers (`export`, `declare -x`, `typeset`, `readonly`, `read <<<`, `printf -v`, `for`, `mapfile`/`readarray`, `getopts`), `env -u`, `unset`, and the allowed `FACTORY_HOME=<temp> factory ...` prefix. The default `--mode both` run therefore does not skip evaluator-only rules.
- `must_allow`: realistic commands that must keep working (exit 0 in both modes): commit and PR heredoc bodies that mention herdr, claude or the privilege-escalation words, grep/rg, git log with PAGER, factory msg and verdict.

- Config seed: an object `{"cmd": "uvx pytest", "config": {"test_sandbox": "1"}}` (a non-empty object of string keys to string values; any other shape is a usage error, exit 2) runs both hook sides with `FACTORY_HOME` pointing at a fresh temp home whose `db/factory.db` holds a `config(key TEXT PRIMARY KEY, value TEXT NOT NULL)` table with exactly those rows. This makes rules gated by a DB config key (the `test_sandbox` bare-runner rule, incl. `uvx` and `uv tool run`) visible to the diff. A report line for a seeded entry names the seed, e.g. `LOOSENED base=2 pr=0 worker config=test_sandbox:1 uvx pytest`. Unseeded entries keep their environment unchanged. Rules gated by DB rows (own goal, `tasks.worktree`) and the `public_text_repos`/`frozen_repos` keys (which also depend on the hook's cwd) are not seeded yet. Each seed home also gets `run/guard-export.json` (what the Go guard reads). During `--impl go`, compare() refreshes that file's `generated_at` when it would exceed the Go guard's export age limit, so a long corpus run does not false-tighten on stale-export fail-closed.
- Env: an object `{"cmd": "git push origin HEAD:factory/t-1", "role": "coder", "env": {"FACTORY_AGENT": "claude-coder-t-1"}}` (a non-empty object of string keys to string values; any other shape is a usage error, exit 2) adds those variables to the hook environment on both sides (the Python hook and, with `--impl go`, the Go guard). It combines with `config`. When `FACTORY_AGENT` carries a task id (`t-N`, matched loosely so look-alike names such as `claude-coder-t-1x` still get a row), the entry also runs from a stand-in own worktree: the hook gets that directory as the payload `cwd` and a temp `FACTORY_HOME` whose `tasks` row for that task points at it (the Go guard reads the same row from `run/guard-export.json`). That is what the coder push pin (T-0689/T-0743) needs to be exercised, e.g. `claude-coder-t-1`, `cursor-coder-t-1` and `coder-t-1` may push `HEAD:factory/t-1` while `evil-coder-t-1`, `Claude-coder-t-1` and `claude-coder-t-1x` may not. A report line names the env, e.g. `LOOSENED base=2 pr=0 worker env=FACTORY_AGENT:evil-coder-t-1 git push ...`. Entries without `env` get no injected `FACTORY_AGENT` and keep their environment unchanged.

`tests/test_guard_diff.py` runs every entry against the current hook (evaluator-tagged entries under `--role evaluator` with a temp `FACTORY_HOME`), so a corpus entry that the hook no longer treats as listed fails the suite. Add a new bypass variant to `must_block` and a new look-alike to `must_allow` in the same PR as the guard change.

## How reviewers use it

1. Read the `guard-diff` job's real-corpus step log for the PR head (do not run the tool locally).
2. Check that the PR body pastes the summary and every LOOSENED/TIGHTENED line.
3. Reject an unplanned LOOSENED line. A planned loosening must be named in the spec and ships as its own task (L-026).
4. Read the TIGHTENED lines for false positives: each should be a form the PR meant to block.
5. For a retry, check that the variants listed per class include the sibling forms of the reported bypass.

The coder procedure is the `guard-change` skill in `roles/plugins/coder/`.

## Go implementation (--impl go)

`--impl go` is the G-016 differential gate for the Go port of the hook (`go/cmd/worker-guard`). The reference is the Python hook in the working tree (`--hook`); the candidate is the Go binary, built once per run with `go build ./cmd/worker-guard` in `go/`, or taken from `--go-bin PATH`. The same corpus, tokens, environment and modes apply, and the candidate is invoked as `worker-guard --role <role>|--hq --py-hook <abs path of the reference hook>`.

CI is the normal route: it covers Go through the `GUARD_IMPL=diff` step of the `guard-unittest-shard` jobs (no path condition; `guard-unittest` is the fan-in) and through the `guard-equiv` job, which runs exactly the command below with `--go-bin` on guard-path PRs and pushes to main. The `guard-diff` job runs `--impl python` only. A local `--impl go` run needs a named authorization from the task, the owner or the boss, like the manual run above.

```
python3 ci/guard_diff.py --impl go --roles all --mode both --strict
```

- `--base` is not needed; the settings diff is skipped and the output says `settings diff: skipped (--impl go)`. In the lines, `base=` is the Python hook and `pr=` the Go binary.
- `--roles all`: worker mode runs under every role whose `roles/settings/<role>.json.in` calls the hook with `--role` (derived from the templates, not a list in the tool), plus `hq`. Lines read `worker role=<role>`.
- `--strict`: TIGHTENED and CHANGED lines also exit 1, because the gate is zero in both directions. Without it, only LOOSENED and TIMEOUT exit 1.
- `--allow-loosen` is refused together with `--impl go` (exit 2, `no --allow-loosen with --impl go`); so are `--go-bin` and `--strict` without `--impl go`.
- A missing `go`, a failed build or an unusable `--go-bin` exits 2 and names the cause.
- Known TIGHTENED cases that are not behaviour bugs: a payload above 1 MiB, NaN/Infinity and lone-surrogate JSON inputs. Differences in stderr text are not behaviour; the exit code decides.

## GUARD_IMPL in the test suite

Every unit test that runs the worker-guard hook goes through `tests/guard_impl.py` (`hook_run(payload, argv_tail, env=None, cwd=None, timeout=30)`), which picks the implementation from the `GUARD_IMPL` environment variable:

- unset or `python`: `roles/hooks/worker-guard.py`, as before (the default; nothing here flips it).
- `go`: the Go binary from `GUARD_GO_BIN`, else built once per process from `go/cmd/worker-guard` into a temp dir (called with `--py-hook <the Python hook>`). A missing `go` or a failed build raises, so the test errors; it never skips.
- `diff`: runs both. Differing exit codes, or differing stderr, raise `AssertionError` naming the arguments and payload. Stderr must be identical except that Go's internal-error lines start `worker-guard(go)` where Python's start `worker-guard`; only that prefix is normalized. (`ci/guard_diff.py` stays exit-code only.) The Python result is returned, so the test's own assertions still run.
- A test helper that runs the hook in-process (loads the hook module and calls `main()`) must branch on `guard_impl.impl()` and call `hook_run` when it is not `python`; `tests/test_guard_impl_coverage.py` fails a guard test module that calls `main()` in-process without it.

```
GUARD_IMPL=diff python3 -m unittest tests.test_worker_guard tests.test_priv_guard tests.test_guard_diff tests.test_worker_guard_public_text
GUARD_IMPL=go python3 -m unittest tests.test_worker_guard_git
```

`tests/test_guard_impl_coverage.py` scans `tests/test_*.py`: a module that names `worker-guard` must import `hook_run`, set a module-level `GUARD_IMPL_PYTHON_ONLY = "<reason>"` (it tests Python-only machinery, such as a pwd patch or the copied hook of a skill-eval trial), or be in the test's `ALLOWLIST` (it only mentions the path as text). It also pins the set of guard test modules: a new one fails until it uses the helper and is added to `KNOWN`.

## Python-only guard PRs under guard-equiv

Since T-0355 the `guard-equiv` job runs on PRs that touch `roles/hooks/` and compares Go with Python strictly, stderr included. A guard PR that changes only the Python hook goes red before merge, and a corpus line can go red because of a different Python-only rule. Pick the rule that fits your PR:

- (a) You change the reason text of an existing rule in the Python guard: the same PR changes the Go string to match, text only.
- (b) You add a new Python-only rule: its test module carries `GUARD_IMPL_PYTHON_ONLY = "<reason>"`, its forms stay out of `ci/guard_corpus.json` until the Go mirror lands, and the Go mirror task is filed at merge. Every Python-only rule needs its mirror merged before any `impl=go` or shadow flip (M21163).
- (c) A corpus line is red only because of a different Python-only rule: tag the line with the role where both guards agree (for example `"role": "coder"`), or leave it out, and hand the form to that rule's mirror task. Never allowlist it, never drop `--strict`. Tagging a line with a role also drops its hq and other-role coverage (a tagged line runs in worker mode under that one role only), so the Go mirror task must restore the line to untagged. `"modes"` cannot be used to narrow a line: `guard_diff` ignores it (see "Corpus format"), so an untagged line with `"modes": ["hq"]` still runs under every role and hq, which is how T-0995 attempt 2 went red (5157 commands, 54 loosened).
