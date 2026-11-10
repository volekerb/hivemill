# worker-guard in Go (G-016 SC4)

`go/cmd/worker-guard` is the Go port of `roles/hooks/worker-guard.py`. It was built in four section tasks and is
wired only behind the shim: the 10 worker settings templates call `roles/hooks/worker-guard` (T-0356), which picks Go or Python from the `guard_impl` flag; Python stays the default (`guard_impl=python`). HQ (boss, director, lead) calls the Python hook directly.
The Python hook on `origin/main` is the behaviour contract.

## Status

| section | task | state |
|---|---|---|
| I/O contract, shell front end, lookaround matchers | T-0347 | merged |
| command analysis (priv, shells, launchers, kill, systemctl, `check`) | T-0349 | merged |
| git rules | T-0350 | merged |
| config, persist, factory rules, Edit/Write and Read/Grep tools, gh rules, the decisions that read the DB | T-0351 | here |

Every rule is ported except forms still covered only by the Python hook in follow-up tasks (T-1210 `--repo` space form,
T-1211 `--config-env` remote URL, T-1212 `http-push`). The live-checkout common-dir rules of T-1200
(`tests/test_worker_guard_live_remote.py`) are ported. Payloads Python allows without analysis (other tools, `--hq` with
a non-Bash tool) are allowed here too.

## Decisions that read the factory DB (T-0351, T-0767)

Go is stdlib only and opens no sqlite. Six decisions of the hook read the factory DB (read-only, `FACTORY_HOME` or the
live home): the agent's own goal (`own_goal`, for `factory goal show <own goal>`), the `test_sandbox` config row
(`sandbox_route`), the `public_text_repos` / `frozen_repos` rows (`PublicText`, git and gh writes; decided in Go since T-0887), the task worktree of a
coder's `git push` (`git_push_pin`), the `scrub_terms_file` row (`terms_paths`) and `factory_remote_url` (T-1200 push URL allow).

### The guard export (T-0767)

Every tick (`export()` in `bin/factory_cli.py`, also `factory export`) writes `<FACTORY_HOME>/run/guard-export.json`
(mode 0600, replaced atomically):

```json
{"version": 1, "generated_at": "2026-11-02T07:00:00+00:00",
 "config": {"public_text_repos": "", "frozen_repos": "", "scrub_terms_file": "", "test_sandbox": "0", "factory_remote_url": ""},
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
   package. One rule uses it today: the member list of a no-push `tar`/`unzip` extraction (T-1001, see below); a
   future DB read without an export row can use it too.

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

`live_remote.go` (T-1200) ports `uses_live_common`, `git_common_dir`, `git_live_push_guard`, `factory_remote_url` (from
the export), repo-config refuses on the live common dir and live push URL checks. `git.go` ports `git_homes`, `dynamic_dir`, `git_cd`, `git_join`, `git_top_shared`, `git_is_shared`, `git_shared`,
`git_outside_shared`, `branch_mutates`, `config_mutates`, `git_mutates`, `git_cleaner_ok`, `git_delete`,
`config_args_write`, `git_persist` (up to the file rules), the tail of `cmd`'s git branch and `derive_live_homes`.
`GitBlock` mirrors Python's class (a `Block` for `errors.As`). The tests override the hook's own environment exactly as
the Python tests do: `WORKER_GUARD_LIVE_HOME` replaces the shared checkout, `WORKER_GUARD_SCRATCH` names the worker's
scratchpad; without it the scratchpad comes from the payload `session_id` (`guard.SessionID`).

Tests: `git_test.go` (`go test ./internal/guard -run Git`) ports `tests/test_worker_guard_git.py`; where the decision
reads the DB and a DB file exists, the test accepts the delegation.

## No-push gh writes (T-0881, T-0993)

For the no-push roles (not HQ) `nopush.go:noPushGhWrite` mirrors `Analyzer.nopush_gh_write`: a direct `gh api` with a body
flag (`-f -F --field --raw-field --input`) is refused unless the parsed method is GET (`ghParse`: `--method=GET`, `-XGET`,
global flags such as `-R`), and so is `gh release create|new|edit|delete|upload|delete-asset`, plus the narrowed
`gh issue|workflow|pr` write verbs in `GH_NOPUSH_WRITE_VERBS` (`gh pr comment` stays allowed for reviewer and qa only).
Inline interpreter code and
`bash -c` go through `nopush_inline.go`: `INLINE_GH_WRITE` gained the `release` alternative, and `inlineGhAPIBody` ports
`inline_gh_api_body` (the text is cut at each `gh` word, so a `-X GET` on another gh call exempts nothing). RE2 has no
lookbehind, so `(?<![\w-])` before the flag and `(?![\w-])` after it are hand-coded (`bodyFlagAt`, `matchInlineGhGet`).
Messages keep Python's `gh api with -f/...` and `gh release <verb>` text. Tests: `nopush_gh_api_release_test.go`; corpus
entries sit next to the gh release lines in `ci/guard_corpus.json`.

## Config, persist and factory rules (T-0351)

- `host.go`: the module-level state that comes from the passwd entry: `derive_live_homes`, `LIVE_HOME(S)`, `PW_DIR`,
  `CFG_DIR`, `PERSIST_FILES/TREES/ANCESTORS`, `RUN_SETTINGS`, `HOOK_REAL` (from `--py-hook`), `Analyzer.live_homes`.
  `$HOME` and every env var are never read for it.
- `config.go`: `cfg_cd`, `cfg_expand`, `cfg_hit`, `cfg_redirect`, `cfg_write` (the `~/.config/factory` and `run/settings`
  write detector); `persist.go`: `brace_expand`, `repo_cfg_path`, `persist_path`, `persist_hit`, `persist_redirect`,
  `persist_write`; `git.go`: `git_persist` (with `-C`, `--git-dir`, `--work-tree`), `repo_cfg_refuse`, `cfg_in_scratch`,
  `git_push_pin`; `factory.go`: `temp_home`, `outside_live`, `agent_temp_home`, `check_identity`, `evaluator_factory`,
  `factory_unit`, `broad_read`, `scratch_homes`, `install_dry_run`, `scratch_form` (T-0331); `gh.go`: `gh_repo_change`; `publictext.go` / `ptrule.go`: `PublicText` (T-0887: the whole rule);
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

**Who calls the shim (T-0356, M22079).** The 10 worker templates call `sh <live home>/roles/hooks/worker-guard --role <role>` (27 hook lines; was `python3 <live home>/roles/hooks/worker-guard.py --role <role>`). The 3 HQ templates (boss, director, lead) stay on `python3 <live home>/roles/hooks/worker-guard.py --hq`. Why: HQ repairs a broken hook. A direct python3 crash exits non-2, which does not block, so HQ can still fix it. The shim maps every crash to exit 2, so a broken hook under the shim would lock boss, director and leads out of Bash with no agent able to repair it (M20449). Moving HQ onto the shim is decided at the Go flip, not before. The sandboxed evaluator rewrites whichever form its template has to `python3 <trial copy of worker-guard.py> <same args>` (the shim is not copied into the trial). `ci/guard_diff.py --roles all` reads both forms.

**Fail closed.** The shim never `exec`s. Every exit status other than 0 and 2 (including 126/127 when python3 or the
binary cannot run, and signals such as SIGKILL = 137) becomes exit 2 with
`worker-guard shim: <impl> failed (<status>); blocking`. Exit 1 and 126/127 would otherwise be non-blocking for Claude Code.

**Protected.** `<live home>/run/guard_impl` and the `~/.local/lib/factory` tree are write-protected from workers' Bash
in both the Python and the Go guard (writers, redirects, `cd` + write); reads (`ls`, `cat` of the stamp) stay allowed.

**Protected trees for workers (T-0925).** One list, in both guards (`prot_trees` / `protTrees`): `~/.local/lib/factory`,
`~/.config/factory`, `<live>/run/settings`, `<live>/run/guard_impl`, `~/.local/go` (the Go toolchain: its `go.env` applies
`GOFLAGS` even with `GOENV=off`), `~/.config/go` (`go env -w` writes it), and four trees inside the live checkout:
`<live>/go`, `<live>/bin`, `<live>/roles/hooks`, `<live>/roles/settings`. Not the whole live checkout, and never the
hook's own repo root (a worktree stays writable). A Bash command is refused when it is a redirect, a writer
(`cp mv ln rm tee install touch dd ...`, `sed -i`), a writer flag (`curl -o/-O`, `wget -O/-P`, `go build -o`, `gcc -o`,
`tar -C/-f/--one-top-level`, `unzip -d`, `7z -o`, `git clone <dir>`), `go env -w|-u`, or an extractor/downloader with no
target after a `cd` into a tree, and the target is a tree, inside one, or a glob that can match one (`~/.local/fact*/x`).
A `cd` target that is only known at run time followed by an extractor is refused too. Write/Edit/MultiEdit/NotebookEdit
on a path in a tree are refused the same way. Reads, runs and look-alikes (`~/.local/gox`, `<live>/binx`) stay allowed.

**Tilde names, copy into a parent, `find`, `git init` (T-0983).** The same list, tightened in both guards (nothing removed):
(A) a write or `cd` target that starts with `~name` is resolved with the passwd entry (Python `pwd.getpwnam`, Go
`os/user.Lookup`) and then checked like any path; `~+` is the tracked cwd; a name with no entry and `~-` are not decidable,
so they follow the unresolved-expansion rule (refused when the word spells a protected tree name, like `$X`); `~` and `~/`
keep their decisions; the Write/Edit tools get the same expansion. (B) `cp mv ln install rsync` into a directory that is a
strict ancestor of a protected tree (`~/.local`, `~/.local/lib`, `<live>`, `<live>/roles`) is refused when a source basename
equals the next component toward a tree (`cp -r /tmp/bin ~/.local`), or when the basename cannot be known (glob, `$`,
`-T`, `--parents`, a trailing `/.`, `.`/`..`, an rsync source with a trailing `/` or `-R`); any other name keeps its
decision (`cp /tmp/x ~/.local/` is allowed). `tar`, `unzip` and `7z` whose target or `cd` directory is such an ancestor
are refused, because the archive content is unknown. A worktree is not an ancestor, so it stays writable. (C) `find` with
`-delete`, `-exec|-execdir|-ok|-okdir` running a writer, deleter, shell or interpreter, or `-fprint|-fprint0|-fprintf|-fls`
is a write to each start path (the cwd when there is none): refused when a start path is a tree, inside one or holds one.
(G1) `git init <dir>` and `git worktree add|move|repair <dir>` into a tree are refused like `git clone`.

**Closed-list writers (T-0988).** Both guards also refuse the remaining lead-listed writer forms into any protected tree:
`patch` (every output spelling), `gzip -d` (not `gzip -dc` / `--to-stdout` to stdout; `gunzip` is a known gap), `cpio -id` (into `-D` or the cwd; `-i -t` list stays allowed),
`GOBIN=<dir> go install` and `env GOBIN=<dir> go install`, `gcc -MF` (including glued `-MF<path>`), and
`cargo build --target-dir`. Similar spellings outside that list are known gaps (see `docs/worker-guard.md`).

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

No binary is in git. `factory up` and the tick call `build_guard_go()` (T-0354), which builds `go/cmd/worker-guard`
into `~/.local/lib/factory/worker-guard`, **only when `guard_impl` is not `python`**. At the default no Go toolchain runs
in the tick or in `factory up`; `write_guard_impl()` runs in every case. After a flip to `go` or `shadow` the next tick
(or `factory up`) builds, and the shim stays on Python until the stamp matches. Rolling back to `python` also stops builds.

1. The toolchain is `~/.local/go/bin/go` only, `nice` is `/usr/bin/nice`; neither is
   looked up on `PATH`. A missing toolchain: one stderr line `guard go build: skipped (no go toolchain)`, no stamp, no error.
2. `srchash` = `git -C <live home> rev-parse HEAD:go`. When `worker-guard.stamp` already equals it and the binary is
   executable the call is a no-op (one git call, well under 50 ms).
3. Otherwise, under `flock` on `~/.local/lib/factory/.build.lock` (a second builder just skips), it runs
   `/usr/bin/nice -n 10 go build -buildvcs=false -trimpath -o worker-guard.new ./cmd/worker-guard` in `<live home>/go`
   (timeout 300 s for `factory up`, 120 s for the tick; see the budget below), renames the result to `worker-guard`, and
   only then writes the stamp (to a unique temp file, then rename).
4. A failed or timed-out build deletes the stamp, leaves any old binary alone, logs `guard go build: failed (...)` and
   never raises: the shim sees no matching stamp and runs Python.

The tick skips the build (not the flag refresh) while load1 is above `gate_max_load_ratio` x CPUs.

**Build environment.** The build gets exactly this environment and nothing from the caller (so a caller's `GOFLAGS`,
`GOENV`, `GOROOT`, `GOEXPERIMENT`, `GODEBUG` or `~/.config/go/env` cannot reach the compiler): `PATH=/usr/bin:/bin`,
`HOME=<real home>`, `GOTOOLCHAIN=local`, `GOENV=off`, `GOFLAGS=`, `GOWORK=off`, `GOPROXY=off`, `GOSUMDB=off`,
`CGO_ENABLED=0`, and a private tree under the lib dir: `GOCACHE=<lib>/go/cache`, `GOMODCACHE=<lib>/go/mod`,
`GOPATH=<lib>/go/path`, `GOTMPDIR=<lib>/go/tmp` (created before the build). `-buildvcs=false` keeps `go build` from running
`git status` and `git log` in the checkout (measured: without it both run, with it none).

**Cold-cache build time.** Measured with an empty private cache, the real `./cmd/worker-guard`, this environment and nice:
4.2 s at load1 4.1. That is far under the 120 s tick budget, so the first build after a flip may run from the tick. If a
future toolchain or tree makes a cold build exceed 120 s, the first build after a flip must run from `factory up` (300 s).

**Toolchain tree.** `$GOROOT/go.env` still applies: a `GOFLAGS` value in it takes effect even with `GOFLAGS=` in the
environment and `GOENV=off` (measured with a scratch GOROOT of symlinks plus its own `go.env`). The toolchain tree under
`~/.local/go` is therefore a dependency for the T-0925 follow-up, not something this build can close.

**Precondition for flipping `guard_impl` away from `python`.** T-0957 and T-0925 are merged, and either the coder role is
in `sandbox_roles` or the director has explicitly accepted the same-uid risk (a worker that can write `~/.local/go` or the
lib dir can influence the binary the shim runs).

## guard_impl config

`factory config guard_impl <python|go|shadow>` (director only; anything else exits 2). Default `python`. The key is
mirrored to `$FACTORY_HOME/run/guard_impl` as `<impl> <srchash>` by `write_guard_impl()`, which runs after the key is
set, after `factory up`'s build and once per tick, so the file always follows the DB. `srchash` is `HEAD:go` of the live
home; with no `go/` tree or a git error the file holds `python -`. A pull that changes `go/` makes `srchash` differ from the
stamp until the rebuild, and the shim uses Python meanwhile. `factory config` ends with `guard binary: ok|missing|stale`;
setting `go` or `shadow` while it is not `ok` prints a warning (the key is still set). Rollback: `factory config guard_impl python`.

### Tick budget, order and failure marker

- **Live home only.** `build_guard_go()` without an explicit lib dir does nothing (`skipped`) unless `FACTORY_HOME` is a live home, so a tick in a test or scratch home never writes the real `~/.local/lib/factory`; it only refreshes `run/guard_impl`.
- **Budget.** `factory up` builds with a 300 s timeout (`factory-up.service` allows 600 s). The tick builds with 120 s (`factory-tick.service` has `TimeoutStartSec=240`). The build is synchronous and runs in its own process group; on timeout the whole group is killed, so no compile child survives the tick.
- **Order.** The tick writes `run/guard_impl` first, builds, then writes it again. After a pull that changes `go/` the flag already holds the new `srchash` during the build, so the shim uses Python even if the tick is killed mid-build.
- **Failure marker.** A failed or timed-out build writes `~/.local/lib/factory/worker-guard.failed` holding the `srchash`. The tick does not rebuild while the marker holds the current `srchash` and is under 30 minutes old; a new `srchash` builds at once, `factory up` ignores the marker, and a successful build removes it.
- The tick path stays silent when `go` is missing (it would log every minute); `factory up` logs the `skipped` line, and `factory config` shows `guard binary: missing`.
- The flag and the stamp are written through a unique temp file in the same directory, never a fixed `.new` name, so a planted symlink is not followed.

## Latency

Per-call latency of the shim `sh roles/hooks/worker-guard` through the real entry point (T-0355, SC4), python vs go. Command:
`python3 ci/profile_baseline.py --runs 50 --hook-cmd 'sh roles/hooks/worker-guard'` run twice with the flag file `run/guard_impl` = `python <hash>`, then `go <hash>`; temp `HOME` (holding the built binary and its stamp) and temp `FACTORY_HOME`, on the factory box. Each row is 150 calls per implementation (50 runs of an allowed, a blocked and a heredoc command, exit codes as expected: 0, 2, 0; hq blocks nothing).

| role | python min / median / p95 (ms) | go min / median / p95 (ms) | median ratio (python / go) |
|---|---|---|---|
| coder | 63.4 / 68.0 / 71.1 | 7.4 / 8.0 / 9.5 | 8.5x |
| qa | 63.2 / 69.1 / 76.6 | 7.4 / 8.0 / 9.7 | 8.7x |
| reviewer | 64.5 / 68.9 / 76.0 | 7.2 / 8.0 / 9.4 | 8.6x |
| judge | 65.8 / 69.1 / 80.4 | 7.3 / 8.1 / 9.3 | 8.6x |
| cleaner | 66.1 / 71.3 / 87.0 | 7.2 / 8.0 / 9.5 | 8.9x |
| evaluator | 67.1 / 70.3 / 81.8 | 7.2 / 7.8 / 9.0 | 9.0x |
| hq | 64.1 / 68.9 / 81.1 | 7.2 / 7.9 / 9.4 | 8.7x |

meta (python run): git_sha=1a1111fc3 python=3.13.5 nproc=8 loadavg=[4.92, 2.6, 2.23] date=2026-11-04T01:34:17+01:00
meta (go run): git_sha=1a1111fc3 python=3.13.5 nproc=8 loadavg=[3.17, 2.55, 2.24] date=2026-11-04T01:35:35+01:00

Go is faster at the median for every role (about 8.5x to 9x): about 8 ms against about 69 ms, and the Go p95 (9.7 ms at most) is below the Python minimum (63 ms). The Go figure is close to the cost of `sh` plus one process start; Python pays for interpreter start and the hook module import. Python stays the default; this table is the measured gain for a later flip decision.

## No-push `tar` / `unzip` extraction (T-1001, mirror of T-0805)

Go ports the `parse_tar` grammar and rule E1/E2: the option allowlist, the `TAR_OPTIONS`-style environment variables,
the name-rewrite options, the `-C`/`-d` target checks and the stdin / glob / expansion archive names. An unclassifiable
tar word counts as extract mode (fail closed), as in Python. The reason text is the Python text, byte for byte.

Go does **not** read archives. Rule E3 (the member list has no `.git` member and no `config`+`HEAD` pair) needs
`tarfile` and `zipfile` as the reference, so an extraction that passes every Go check is delegated: `archiveMembers`
returns `Delegate`, before any `Lstat` or `Stat`, and the Python hook answers (missing file, FIFO, oversize and
unreadable archive included). Go therefore cannot disagree with Python on a crafted archive (NUL names, the 0x7075
Unicode path extra, truncated or padded tars, any bzip2 level).

**Python stays required for this path after any flip to `impl=go`.** `RunPython` exits 2 when `--py-hook` is not
given, the hook file is unreadable, `python3` is missing or the hook crashes: the extraction is refused, never
allowed. Measuring how often a no-push extraction reaches the list step is T-0357.
