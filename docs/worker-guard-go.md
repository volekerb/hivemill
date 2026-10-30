# worker-guard in Go (G-016 SC4)

`go/cmd/worker-guard` is the Go port of `roles/hooks/worker-guard.py`. It was built in four section tasks and is
**not wired anywhere**: no settings file, shim or flag calls it, and Python stays the default (`guard_impl=python`).
The Python hook on `origin/main` is the behaviour contract.

## Status

| section | task | state |
|---|---|---|
| I/O contract, shell front end, lookaround matchers | T-0347 | merged |
| command analysis (priv, shells, launchers, kill, systemctl, `check`) | T-0349 | merged |
| git rules | T-0350 | merged |
| config, persist, factory rules, Edit/Write and Read/Grep tools, gh rules, the decisions that read the DB | T-0351 | here |

Every rule is ported; no command exits 2 with a "not ported" message. Payloads Python allows without analysis (other
tools, `--hq` with a non-Bash tool) are allowed here too.

## Decisions that read the factory DB (T-0351)

Go is stdlib only and opens no sqlite. Five decisions of the hook read the factory DB (read-only, `FACTORY_HOME` or the
live home): the agent's own goal (`own_goal`, for `factory goal show <own goal>`), the `test_sandbox` config row
(`sandbox_route`), the `public_text_repos` / `frozen_repos` rows (`PublicText`, git and gh writes), the task worktree of a
coder's `git push` (`git_push_pin`) and the `scrub_terms_file` row (`terms_paths`). The binary decides them like this:

1. Python's own "no DB" branch is taken in Go when the DB file does not exist (`db_path()`: `<home>/db/factory.db`, else
   the legacy `<home>/factory.db`): own goal `None`, `test_sandbox` off, public text off, the push has no worktree (refused).
2. Otherwise the whole payload is re-run through the Python hook: `python3 <py-hook> <argv without --py-hook>` with the
   same stdin; its exit code and stderr are the binary's. The path comes from `--py-hook <abs path of
   roles/hooks/worker-guard.py>`, which the shim passes (the binary lives outside the repo). If the path is missing or
   unreadable, `python3` is missing, the run times out (120 s) or the hook exits with anything but 0 or 2, the binary
   exits 2 (`owngoal.go:RunPython`). Delegation is never swallowed by HQ mode's `soft`.
3. A command that cannot reach one of these decisions never delegates (no `factory goal show <id>`, no runner, no git/gh
   write, a terms role with an exported terms file).

Only what Python's early returns skip is decided in Go: a `git`/`gh` command reaches the public-text rule only for
`commit|tag -a|push|merge|commit-tree|notes`, `-c alias.*`, `gh pr|issue|release <write>` and a `gh api` write.
`terms_paths` unions the DB key with the exported terms-file variable. Go takes the variable alone while it is
exported (spawn exports the configured path to every worker) and delegates only when it is not exported and a DB file
exists; a path changed in the DB after a worker was spawned is therefore not seen by that worker's Go guard.

Consequence for the hot path: on a box whose DB file exists, a terms role (coder, qa, reviewer, judge, pm) without an
exported terms file, and every git/gh write, costs a Python run. Exporting the four config rows and the agent goals to a
plain file the binary can read would remove it (follow-up, not part of this task).

## Command analyser (T-0349)

`analyzer.go` (`Analyzer`, `script`, `priv`), `commands.go` (`cmd`, `kill`, `shell`, `loose`, `xargs`, `systemctl`,
file reads), `check.go` (`Check`, the R1/R2/R3 raw-text layer) and `terms.go` (`terms_bash`) port the functions named in
their doc comments ("port of worker-guard.py:<func>"). The Python exception classes are Go error types (`Block`,
`PrivBlock`, `UnitBlock`, `ClaudeBlock`, `DynamicBlock`, `PublicBlock`) and `Explain` prints the same stderr text.

`NotPorted` no longer exists; its place is taken by `Delegate` (owngoal.go), the error that means "the Python hook
decides this payload". `RepoProtBlock` (a `PrivBlock`) is the `gh` R-a refusal.

## Git rules (T-0350)

`git.go` ports `git_homes`, `dynamic_dir`, `git_cd`, `git_join`, `git_top_shared`, `git_is_shared`, `git_shared`,
`git_outside_shared`, `branch_mutates`, `config_mutates`, `git_mutates`, `git_cleaner_ok`, `git_delete`,
`config_args_write`, `git_persist` (up to the file rules), the tail of `cmd`'s git branch and `derive_live_homes`.
`GitBlock` mirrors Python's class (a `Block` for `errors.As`). The tests override the hook's own environment exactly as
the Python tests do: `WORKER_GUARD_LIVE_HOME` replaces the shared checkout, `WORKER_GUARD_SCRATCH` names the worker's
scratchpad; without it the scratchpad comes from the payload `session_id` (`guard.SessionID`).

Tests: `git_test.go` (`go test ./internal/guard -run Git`) ports `tests/test_worker_guard_git.py`; where the decision
reads the DB and a DB file exists, the test accepts the delegation.

## Config, persist and factory rules (T-0351)

- `host.go`: the module-level state that comes from the passwd entry: `derive_live_homes`, `LIVE_HOME(S)`, `PW_DIR`,
  `CFG_DIR`, `PERSIST_FILES/TREES/ANCESTORS`, `RUN_SETTINGS`, `HOOK_REAL` (from `--py-hook`), `Analyzer.live_homes`.
  `$HOME` and every env var are never read for it.
- `config.go`: `cfg_cd`, `cfg_expand`, `cfg_hit`, `cfg_redirect`, `cfg_write` (the `~/.config/factory` and `run/settings`
  write detector); `persist.go`: `brace_expand`, `repo_cfg_path`, `persist_path`, `persist_hit`, `persist_redirect`,
  `persist_write`; `git.go`: `git_persist` (with `-C`, `--git-dir`, `--work-tree`), `repo_cfg_refuse`, `cfg_in_scratch`,
  `git_push_pin`; `factory.go`: `temp_home`, `outside_live`, `agent_temp_home`, `check_identity`, `evaluator_factory`,
  `factory_unit`, `broad_read`; `gh.go`: `gh_repo_change`; `publictext.go`: the entry conditions of `PublicText`;
  `tools.go`: `persist_edit` and `terms_tool` (Edit/Write/MultiEdit/NotebookEdit and Read/Grep payloads).
- Tests: `persist_test.go` ports `tests/test_worker_guard_persist.py` and `tests/test_worker_guard_repocfg.py`;
  `factory_test.go` ports `tests/test_worker_guard_repoprot.py`, `tests/test_worker_guard_data.py`, the
  identity/units/evaluator/config cases of `tests/test_worker_guard.py` and compares `derive_live_homes` with the Python
  function for the same passwd directory; `cmd/worker-guard/section4_test.go` runs payloads through the real binary and
  the Python hook and requires the same exit and stderr (Edit/Write, terms tools, own goal, `test_sandbox` with the
  `uv`/`uvx` runner forms of `tests/test_worker_guard_uv_runner.py`, public text, push pin, the terms key in the DB).

Tests: `commands_test.go` replays `testdata/commands_cases.json` (every Bash command the Python test modules
`test_worker_guard`, `test_priv_guard`, `test_worker_guard_heredoc`, `test_worker_guard_units` and
`test_worker_guard_reads` send to the hook, with the exit of the Python hook) and `testdata/helpers_cases.json` (the
hand-written matchers against the Python regexes). A Go result must equal the recorded Python exit unless it is a
`Delegate` (the replay runs with a DB file present, so the DB-reading decisions delegate). `FuzzCheck` (`go test ./internal/guard -run XXX -fuzz FuzzCheck`) checks for no panic and no hang.

## I/O contract

Same as the hook: `--role ROLE` or `--hq` in argv, a PreToolUse JSON object on stdin, exit 0 = allow, exit 2 = block
with the reason on stderr. Bad JSON, a non-object, a non-string command, an unhashable `tool_name` and invalid UTF-8
all exit 2, as in Python. A panic is recovered in `main`, prints `worker-guard(go): internal error: <msg>` and exits 2.

One deliberate difference: stdin is capped at 1 MiB and a larger payload exits 2 (Python has no cap). This is a
TIGHTENED case for the T7 differential gate.

## Package layout (`go/internal/guard`)

- `frontend.go`: `Preprocess`, `HeredocDelim`, `ExtractSubs`, `MaskQuotes`, `Segments`, `LauncherInCommandPosition`,
  `ScriptTokens` (uses `go/internal/shlex`), the small predicates and the module constants. Each function's doc comment
  names the Python function it ports.
- `lookaround.go`: hand-written matchers for the Python regexes with a lookaround (RE2 has none). Python's `\w`, `\d`,
  `\s` and `\b` are Unicode-aware, so the matchers use explicit Unicode helpers (`pyutil.go`), never Go's ASCII classes.
  `Segments` offsets are character (code point) offsets, like Python; `RuneSlice` cuts text with them.
- `testdata/frontend_golden.json`: what the Python front end returns for every command in `ci/guard_corpus.json` and a
  list of edge cases, plus the module constants the port repeats.

## Golden data

```
python3 ci/guard_golden.py          # regenerate (needs CPython 3.13)
python3 ci/guard_golden.py --check  # exit 1 if stale; run by tests/test_guard_golden.py
cd go && go test ./internal/guard -run Golden -v
```

A later guard PR that changes `preprocess`, `extract_subs`, `mask_quotes`, `segments` or a ported constant makes
`tests/test_guard_golden.py` fail. That PR regenerates the golden file and updates the Go port in the same change.

## Lookaround map (`grep -nE '\(\?<?[=!]' roles/hooks/worker-guard.py`)

| Python line | Go | test |
|---|---|---|
| 99 `PRIV_CODE` | `FindPrivCode` | `TestLookaroundPrivCode` |
| 151-152 `PERSIST_CODE` | `hasPersistCode`, `PersistCode` (219-220) | `TestLookaroundPersistCode`, `TestLookaroundPersistCodeFn` |
| 153 `CFG_ENV_ASSIGN` | `HasCfgEnvAssign` | `TestLookaroundCfgEnvAssign` |
| 225 `CFG_CODE` | `hasCfgCode`, `CfgCode` (1232-1235) | `TestLookaroundCfgCode`, `TestLookaroundCfgCodeWord` |
| 254 `R1` | `MatchR1` | `TestLookaroundR1` |
| 267 `R3_REDIR` | `MatchR3Redir` | `TestLookaroundR3Redir` |
| 268 `HERDR_WORD` | `HasHerdrWord` | `TestLookaroundHerdrWord` |
| 397 `priv_subst` | `FindPrivSubst` | `TestLookaroundPrivSubst` |
| 989 digits before a redirect | `BlankRedirectFds` | `TestLookaroundBlankRedirectFds` |
| 1229, 3055 `~` / `$HOME` prefix | `ExpandHome` | `TestLookaroundExpandHome` |
| 2243 `EVAL_HOME_PREFIX` | `MatchEvalHomePrefix` | `TestLookaroundEvalHomePrefix` |

Expected values in the table tests come from running the Python regex (one-liners in the test file header).
