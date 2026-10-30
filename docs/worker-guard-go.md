# worker-guard in Go (G-016 SC4, in progress)

`go/cmd/worker-guard` is the Go port of `roles/hooks/worker-guard.py`. It is built in four section tasks and is
**not wired anywhere**: no settings file, shim or flag calls it, and Python stays the default (`guard_impl=python`).
The Python hook on `origin/main` is the behaviour contract.

## Status

| section | task | state |
|---|---|---|
| I/O contract, shell front end, lookaround matchers | T-0347 | merged |
| command analysis (priv, shells, launchers, kill, systemctl, `check`) | T-0349 | merged |
| git rules | T-0350 | here |
| config, persist, factory rules | T6d | not started |

A rule that belongs to a section not yet ported is a fail-closed stub: the command exits 2 with
`worker-guard(go): not ported: <rule>`. A partial port can never allow more than Python. Payloads Python allows without
analysis (other tools, `--hq` with a non-Bash tool) are allowed here too. Edit/Write/MultiEdit/NotebookEdit calls and
Read/Grep by a terms role still exit 2 `not ported` (T6d).

## Command analyser (T-0349)

`analyzer.go` (`Analyzer`, `script`, `priv`), `commands.go` (`cmd`, `kill`, `shell`, `loose`, `xargs`, `systemctl`,
file reads), `check.go` (`Check`, the R1/R2/R3 raw-text layer) and `terms.go` (`terms_bash`) port the functions named in
their doc comments ("port of worker-guard.py:<func>"). The Python exception classes are Go error types (`Block`,
`PrivBlock`, `UnitBlock`, `ClaudeBlock`, `DynamicBlock`, `PublicBlock`) and `Explain` prints the same stderr text.
`NotPorted` is the stub error: unlike a plain `Block` it is never swallowed by HQ mode's `soft`.

Stubs, by the section that owns them (a command that reaches one exits 2 where Python may allow it):

| section | stub fires for | Python function |
|---|---|---|
| T6c gh | every `gh` command word (also behind `env` etc. in HQ mode) | `PublicText.command` (gh), `gh_repo_change` |
| T6d git | a coder's `git push`/`send-pack`/`http-push`; a `git commit/tag/push/merge/commit-tree/notes` or `-c alias.*` in a pass where the public-text rule runs (worker pass, HQ check) | `git_push_pin`, `PublicText.git` (both read the DB) |
| T6d git | a `git config` write that is not `--global/--system` or a `GIT_CONFIG*/HOME/XDG_CONFIG_HOME` assignment; `--file`/`-f` writes; `git remote add/set-url/...` | `persist_hit`, `repo_cfg_refuse`, `cfg_in_scratch` |
| T6d config/persist | writers (`rm`, `cp`, `mv`, `tee`, ...), `sed -i`, awk, every interpreter (`python3 x.py`), editors | `cfg_write`, `persist_write` |
| T6d config/persist | any redirect target (worker mode) | `cfg_redirect`, `persist_redirect` |
| T6d factory | identity-variable assignments; `factory spawn/up/tick/relaunch/clean`; broad reads that reach a temp-home or own-goal decision; the evaluator role | `check_identity`, `factory_unit`, `broad_read`, `evaluator_factory` |
| T6d sandbox | a test runner (`python3 -m unittest`, `pytest`, `go test`, ...) run by coder or qa | `sandbox_route` (reads `test_sandbox` from the DB) |

The Go port cannot read the factory DB (stdlib only), so `terms_bash` takes the owner-terms path from the exported
terms-file environment variable only. Python also reads the `scrub_terms_file` config key; reading it is part of T6d.
HQ mode's public-text write hint (a command that cannot be parsed and looks like a git/gh write) is a stub for any HQ
command with a parse error that contains `git` or `gh`.

## Git rules (T-0350)

`git.go` ports `git_homes`, `dynamic_dir`, `git_cd`, `git_join`, `git_top_shared`, `git_is_shared`, `git_shared`,
`git_outside_shared`, `branch_mutates`, `config_mutates`, `git_mutates`, `git_cleaner_ok`, `git_delete`,
`config_args_write`, `git_persist` (up to the file rules), the tail of `cmd`'s git branch and `derive_live_homes`.
`GitBlock` mirrors Python's class (a `Block` for `errors.As`). The tests override the hook's own environment exactly as
the Python tests do: `WORKER_GUARD_LIVE_HOME` replaces the shared checkout, `WORKER_GUARD_SCRATCH` names the worker's
scratchpad; without it the scratchpad comes from the payload `session_id` (`guard.SessionID`).

The stubs of the table above are ordered after the git rules, so a real `GitBlock` (shared checkout, branch deletion,
`git config --global`) is reported as in Python; a stub only fires for a command the Go rules let through. Tests:
`git_test.go` (`go test ./internal/guard -run Git`) ports `tests/test_worker_guard_git.py`; where Python allows a command
that a stub still covers, the test accepts exit 0 or the `not ported` stub and refuses every other block.

Tests: `commands_test.go` replays `testdata/commands_cases.json` (every Bash command the Python test modules
`test_worker_guard`, `test_priv_guard`, `test_worker_guard_heredoc`, `test_worker_guard_units` and
`test_worker_guard_reads` send to the hook, with the exit of the Python hook) and `testdata/helpers_cases.json` (the
hand-written matchers against the Python regexes). A Go result may differ from Python only as a `NotPorted` stub in the
blocking direction. `FuzzCheck` (`go test ./internal/guard -run XXX -fuzz FuzzCheck`) checks for no panic and no hang.

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
