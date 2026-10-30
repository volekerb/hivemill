# Guard diff (ci/guard_diff.py)

`ci/guard_diff.py` compares the worker-guard hook on a base ref (normally `origin/main`) with the hook in your working tree. It answers one question mechanically: does this PR silently loosen a block that main has, or add a false positive?

## Usage

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

The tool also diffs the permission rules of `roles/settings/<role>.json.in` against `--base` (a base that still has the old `roles/settings/<role>.json` is compared after mapping the owner's paths to the template tokens). A removed `deny` rule, an added `allow` rule or a removed hook command prints `SETTINGS-LOOSENED <role> <kind> removed|added: <rule>` and counts as LOOSENED (listable with `--allow-loosen`, one `<role> <kind> removed: <rule>` text per line); the reverse prints `SETTINGS-TIGHTENED`. See `docs/settings-templates.md`.

## Exit codes

- `0`: no LOOSENED line outside `--allow-loosen`, and no timeout.
- `1`: at least one unplanned LOOSENED line, or a TIMEOUT (a hook that cannot decide is not a pass).
- `2`: usage or git error, such as an unreadable base, hook or corpus. It fails closed: an unreadable base is never "no diff".

## Corpus format

`ci/guard_corpus.json` is a JSON object with two lists:

- `must_block`: commands the hook must refuse (exit 2). A string is checked in worker mode (`--role coder`). An object `{"cmd": "...", "modes": ["worker", "hq"]}` is checked in each listed mode (for example privilege-escalation forms, which HQ also blocks).
- Role tag: an object `{"cmd": "...", "role": "evaluator"}` is checked in worker mode only, under that role, with a temp `FACTORY_HOME` in the hook environment. This is how the evaluator class lives in the same corpus: `FACTORY_HOME` writers (`export`, `declare -x`, `typeset`, `readonly`, `read <<<`, `printf -v`, `for`, `mapfile`/`readarray`, `getopts`), `env -u`, `unset`, and the allowed `FACTORY_HOME=<temp> factory ...` prefix. The default `--mode both` run therefore does not skip evaluator-only rules.
- `must_allow`: realistic commands that must keep working (exit 0 in both modes): commit and PR heredoc bodies that mention herdr, claude or the privilege-escalation words, grep/rg, git log with PAGER, factory msg and verdict.

- Config seed: an object `{"cmd": "uvx pytest", "config": {"test_sandbox": "1"}}` (a non-empty object of string keys to string values; any other shape is a usage error, exit 2) runs both hook sides with `FACTORY_HOME` pointing at a fresh temp home whose `db/factory.db` holds a `config(key TEXT PRIMARY KEY, value TEXT NOT NULL)` table with exactly those rows. This makes rules gated by a DB config key (the `test_sandbox` bare-runner rule, incl. `uvx` and `uv tool run`) visible to the diff. A report line for a seeded entry names the seed, e.g. `LOOSENED base=2 pr=0 worker config=test_sandbox:1 uvx pytest`. Unseeded entries keep their environment unchanged. Rules gated by DB rows (own goal, `tasks.worktree`) and the `public_text_repos`/`frozen_repos` keys (which also depend on the hook's cwd) are not seeded yet.

`tests/test_guard_diff.py` runs every entry against the current hook (evaluator-tagged entries under `--role evaluator` with a temp `FACTORY_HOME`), so a corpus entry that the hook no longer treats as listed fails the suite. Add a new bypass variant to `must_block` and a new look-alike to `must_allow` in the same PR as the guard change.

## How reviewers use it

1. Run `git fetch origin`, then the command above on the PR head.
2. Check that the PR body pastes the summary and every LOOSENED/TIGHTENED line.
3. Reject an unplanned LOOSENED line. A planned loosening must be named in the spec and ships as its own task (L-026).
4. Read the TIGHTENED lines for false positives: each should be a form the PR meant to block.
5. For a retry, check that the variants listed per class include the sibling forms of the reported bypass.

The coder procedure is the `guard-change` skill in `roles/plugins/coder/`.

## Go implementation (--impl go)

`--impl go` is the G-016 differential gate for the Go port of the hook (`go/cmd/worker-guard`). The reference is the Python hook in the working tree (`--hook`); the candidate is the Go binary, built once per run with `go build ./cmd/worker-guard` in `go/`, or taken from `--go-bin PATH`. The same corpus, tokens, environment and modes apply, and the candidate is invoked as `worker-guard --role <role>|--hq --py-hook <abs path of the reference hook>`.

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
GUARD_IMPL=diff python3 -m unittest tests.test_worker_guard tests.test_priv_guard tests.test_guard_diff
GUARD_IMPL=go python3 -m unittest tests.test_worker_guard_git
```

`tests/test_guard_impl_coverage.py` scans `tests/test_*.py`: a module that names `worker-guard` must import `hook_run`, set a module-level `GUARD_IMPL_PYTHON_ONLY = "<reason>"` (it tests Python-only machinery, such as a pwd patch or the copied hook of a skill-eval trial), or be in the test's `ALLOWLIST` (it only mentions the path as text). It also pins the set of guard test modules: a new one fails until it uses the helper and is added to `KNOWN`.
