# worker-guard in Go (G-016 SC4, in progress)

`go/cmd/worker-guard` is the Go port of `roles/hooks/worker-guard.py`. It is built in four section tasks and is
**not wired anywhere**: no settings file, shim or flag calls it, and Python stays the default (`guard_impl=python`).
The Python hook on `origin/main` is the behaviour contract.

## Status

| section | task | state |
|---|---|---|
| I/O contract, shell front end, lookaround matchers | T-0347 | here |
| command analysis | T6b | not started |
| git rules | T6c | not started |
| config, persist, factory rules | T6d | not started |

Until the last section lands every Bash command, and every Edit/Write/MultiEdit/NotebookEdit call and every
Read/Grep by a terms role, exits 2 with `worker-guard(go): not ported: <section>`. A partial port can never allow more than
Python. Payloads Python allows without analysis (other tools, `--hq` with a non-Bash tool) are allowed here too.

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
