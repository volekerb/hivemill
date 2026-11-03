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

## Decisions that read the factory DB (T-0351, T-0767)

Go is stdlib only and opens no sqlite. Five decisions of the hook read the factory DB (read-only, `FACTORY_HOME` or the
live home): the agent's own goal (`own_goal`, for `factory goal show <own goal>`), the `test_sandbox` config row
(`sandbox_route`), the `public_text_repos` / `frozen_repos` rows (`PublicText`, git and gh writes; decided in Go since T-0887), the task worktree of a
coder's `git push` (`git_push_pin`) and the `scrub_terms_file` row (`terms_paths`).

### The guard export (T-0767)

Every tick (`export()` in `bin/factory_cli.py`, also `factory export`) writes `<FACTORY_HOME>/run/guard-export.json`
(mode 0600, replaced atomically):

```json
{"version": 1, "generated_at": "2026-11-02T07:00:00+00:00",
 "config": {"public_text_repos": "", "frozen_repos": "", "scrub_terms_file": "", "test_sandbox": "0"},
 "agent_goals": {"claude-coder-t-0767": "G-016"}, "task_worktrees": {"T-0767": "/path/to/worktree"}}
```

It is in `run/` (git-ignored), not `state/`: `state/` is committed and pushed, and a timestamp that changes every tick
would add a commit per tick. The Go binary (`internal/guard/guardexport.go`) decides from this file:

Workers cannot forge it (T-0883): every `roles/settings/*.json.in` denies Edit/Write/MultiEdit/NotebookEdit on
`run/guard-export.json*` (the file and its `.tmp<pid>` sibling), and both guards refuse Bash writes, moves, links,
removals and chmods of it and Edit-class calls on it (realpath and symlink forms included), through the
`run/settings` machinery (`RUN_EXPORT`, `CFG_CODE`). Reads stay allowed. The exporter does not run under the hook.

1. No DB file (`db_path()`: `<home>/db/factory.db`, else the legacy `<home>/factory.db`): Python's own "no DB" branch is
   taken without the export: own goal `None`, `test_sandbox` off, public text off, the push has no worktree (refused).
2. A DB file: the export is read. **Fail closed**: a missing, unreadable, unparsable file, a `version` other than 1 or a
   `generated_at` more than `GuardExportMaxAge` (15 minutes) away from now exits 2 for any decision that needs a value,
   with a message that names the file and says to run `factory tick`. A decision that reads none of these values never
   looks at the file.
3. With the export: `factory goal show <id>` is allowed for `agent_goals[$FACTORY_AGENT]`; a coder's push needs
   `task_worktrees[<TASK>]` to hold the pane's cwd and equal the `-C` target (realpaths); `test_sandbox` = `1` blocks
   bare runners; the `scrub_terms_file` row joins the exported terms variable in `terms_paths` (f1: it is honoured with
   the variable unset). No Python is started for any of these.
4. **Public text (T-0887)**: when `public_text_repos` or `frozen_repos` names a repo the public-text rule is on and a git/gh
   write (`commit|tag -a|push|merge|commit-tree|notes`, `-c alias.*`, `gh pr|issue|release <write>`, a `gh api` write) is
   decided in Go as well (`ptrule.go`, the checker in `scrub.go` / `ptcheck.go`): remote lookups and the push range use
   `git` subprocesses as Python does, the owner terms come from the exported terms-file variable or the export's
   `scrub_terms_file` row. Nothing is re-run through the Python hook for any decision any more. The refusal text is
   Python's (`public text: N hits: <ids>`, `repo X is frozen, G-021`, ...), so exit code and stderr are the same.
   `--py-hook` is no longer needed to decide anything, but it still names `HOOK_REAL` (the checkout the hook belongs
   to): from it the rule reads `ci/scrub_allowlist.txt` for a push and checks that `ci/public_text_guard.py` and
   `ci/scrub_scan.py` exist (Python's "cannot be loaded" refusal). A push with no `--py-hook` is refused (the allowlist
   cannot be located); text checks do not need it. The delegation machinery (`Delegate`, `RunPython`) is still in the
   package, now used by no rule, so a future DB read without an export row can use it.

   The checker port: the patterns of `scrub_scan.py` (GENERIC) and `public_text_guard.py` (EXTRA, image metadata readers,
   `check_push`) are RE2 expressions plus hand-written lookbehind/`\b` assertions (`scanRule`); owner terms are matched
   rune by rune with Python's `re.IGNORECASE` folding (`foldKey`: simple lower case plus CPython's extra cases such as
   U+0130/U+0131/U+017F/U+212A). Only the hit count and ids matter to the hook (the refusal never prints matched
   text), so masking and labels are not ported. Known difference: Python prints a deprecation warning on stderr for a
   legacy `path:regex` line of `ci/scrub_allowlist.txt` on every push check; Go does not (there is none today).
   Tests: `internal/guard/scrub_diff_test.go` and `scrub_push_test.go` compare the Go scanner and `checkPush` with the
   Python module on a corpus (case, Unicode, boundaries, images, merges, allowlist) and on fixed-seed mixes;
   `cmd/worker-guard/publictext_diff_test.go` runs ~350 git/gh commands through the binary (with a `--py-hook` that fails if
   started) and the Python hook and requires the same exit and stderr; `publictext_test.go` asserts that nothing is
   delegated and that the rule fails closed.

`terms_paths` takes the union of the export's `scrub_terms_file` row and the exported terms-file variable (as Python
does), so the export is read for every terms-role Bash call; an unusable export exits 2. A coder just spawned is
missing from `agent_goals` until the next tick (up to a minute): `factory goal show <own goal>` is refused until then
(the safe side), `factory task show` is not. `factory spawn` does not rewrite the export.

A `git`/`gh` command reads the export for the public-text rule only when it can reach the rule (the write forms above); a
shell payload in HQ mode is analysed only when it may hold a `git`/`gh` word once its quoting is resolved.

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
  `factory_unit`, `broad_read`; `gh.go`: `gh_repo_change`; `publictext.go` / `ptrule.go`: `PublicText` (T-0887: the whole rule);
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

## Shim and flag

The shim finds its own directory from `$0`; the live home is the parent of `roles/`.

**Flag.** `<live home>/run/guard_impl` holds one line, `<impl> <srchash>`, with impl `python`, `go` or `shadow`. The
factory CLI writes it (the DB key is director-only); the shim cannot read sqlite. A missing, unreadable, empty,
multi-line or malformed file, or any other impl, means `python`.

**Binary.** `~/.local/lib/factory/worker-guard` with the stamp `~/.local/lib/factory/worker-guard.stamp`, one line: the
tree id of `go/` it was built from. The shim uses Go only when impl is `go` or `shadow`, the binary is executable and
the stamp equals `<srchash>` (a string compare; the shim runs no extra process for it). Otherwise it runs Python, so a
stale or missing binary never runs old rules. The binary is called as `worker-guard --py-hook <dir>/worker-guard.py <args>`.

**Modes.** `python`: `python3 worker-guard.py <args>`. `go`: the binary. Both get stdin as is.

**Fail closed.** The shim never `exec`s. Every exit status other than 0 and 2 (including 126/127 when python3 or the
binary cannot run, and signals such as SIGKILL = 137) becomes exit 2 with
`worker-guard shim: <impl> failed (<status>); blocking`. Exit 1 and 126/127 would otherwise be non-blocking for Claude Code.

**Protected.** `<live home>/run/guard_impl` and the `~/.local/lib/factory` tree are write-protected from workers' Bash
in both the Python and the Go guard (writers, redirects, `cd` + write); reads (`ls`, `cat` of the stamp) stay allowed.

**Rollback.** Write `python` (or remove `run/guard_impl`): the next call uses Python, no relaunch.

**Debugging.** `WORKER_GUARD_SHIM_TRACE=1` prints `impl=<python|go|shadow>` to stderr (used by tests/test_guard_shim.py).

## Shadow mode

Impl `shadow` runs both on the same payload (read once into a temp file removed on exit). Python is authoritative: its
stdout, stderr and exit status are returned. Go runs with a 2 s timeout and its output is discarded. When the two exit
statuses differ the shim appends one JSON line to `$FACTORY_HOME/logs/guard-shadow.log` (`FACTORY_HOME` defaults to the
live home):

    {"ts":"2026-01-02T03:04:05Z","role_args":"--role coder","sha256":"<64 hex>","py":0,"go":2}

`sha256` is the hash of the command text (of the whole payload if the command cannot be parsed); the command text itself
is never logged. A timeout shows as `go: 124`. A logging failure never changes the decision. The clean-week criterion
before any default flip is no lines in this log.

The shim locates the binary under `$HOME`; the guard protects the passwd home. They are equal for the factory user, and a worker cannot change the hook's environment.

## Build and stamp

No binary is in git. `factory up` and every tick call `build_guard_go()` (T-0354), which builds `go/cmd/worker-guard`
into `~/.local/lib/factory/worker-guard`:

1. `go` is looked up on `PATH`, then `~/.local/go/bin/go`. Missing: one stderr line `guard go build: skipped (no go toolchain)`, no stamp, no error.
2. `srchash` = `git -C <live home> rev-parse HEAD:go`. When `worker-guard.stamp` already equals it and the binary is
   executable the call is a no-op (one git call, well under 50 ms).
3. Otherwise, under `flock` on `~/.local/lib/factory/.build.lock` (a second builder just skips), it runs
   `GOTOOLCHAIN=local nice go build -trimpath -o worker-guard.new ./cmd/worker-guard` in `<live home>/go` (timeout 300 s),
   renames the result to `worker-guard`, and only then writes the stamp (to `.new`, then rename).
4. A failed or timed-out build deletes the stamp, leaves any old binary alone, logs `guard go build: failed (...)` and
   never raises: the shim sees no matching stamp and runs Python.

The tick skips the build (not the flag refresh) while load1 is above `gate_max_load_ratio` x CPUs.

## guard_impl config

`factory config guard_impl <python|go|shadow>` (director only; anything else exits 2). Default `python`. The key is
mirrored to `$FACTORY_HOME/run/guard_impl` as `<impl> <srchash>` by `write_guard_impl()`, which runs after the key is
set, after `factory up`'s build and once per tick, so the file always follows the DB. `srchash` is `HEAD:go` of the live
home; with no `go/` tree or a git error the file holds `python -`. A pull that changes `go/` makes `srchash` differ from the
stamp until the rebuild, and the shim uses Python meanwhile. `factory config` ends with `guard binary: ok|missing|stale`;
setting `go` or `shadow` while it is not `ok` prints a warning (the key is still set). Rollback: `factory config guard_impl python`.
