# Evaluator trials: `factory skilleval try`

`factory skilleval try <task> [--candidate C-xxxx|none] [--condition with|without]` (roles director, lead, owner) starts one evaluator on one task, with or without one vendored skill, inside the eval sandbox of [scout-nsbox.md](scout-nsbox.md). `factory skilleval collect` ends a trial and records its result (below).

## What `try` does, in order

1. Checks the task (`skilleval/tasks/<task>/`) and the candidate. `is_eval_allowed` must be true: only prompt-only candidates that are not `needs-owner` or `dismissed`. Otherwise exit 2, nothing created.
   `--candidate none` (or no `--candidate`) is the baseline smoke: it uses the reserved candidate `C-0000`, created on demand, and the condition is always `without`.
2. Fail closed (exit 3, nothing created, no multiplexer call, no agent row): the token file `~/.config/factory/eval-oauth-token` must be a regular file, mode 0600, owned by you (message `owner decision D3 pending: no eval token`); user namespaces must work (`unshare -r -n -m`). There is no fallback to an unsandboxed claude.
3. For condition `with`, vendors the candidate if needed (`factory skilleval vendor`) and re-checks the gate against the vendored copy.
4. Inserts a `skill_trials` row (an existing `error`, `cancelled` or `voided` row for the same candidate, sha, task and condition is reused; any other existing row is refused) and builds `<skilleval_root>/trials/trNNNN/`.
5. Runs a preflight: the real namespace view with no proxy and no token, around the claude version command. A mount or bind failure ends in exit 3, the trial dir is removed and the row is `error`.
6. Spawns `eval-trNNNN` through the normal gate (it may queue) and sets the row to `running`.

The token is stat-ed by `try`, never opened. Only the shim reads it, inside the pane, at start time. It is not in the multiplexer `--env`, `env_extra`, `spawn_args`, events or messages.

## Trial directory

| path | content |
|---|---|
| `work/` | copy of the task's `repo/`, `git init`, one commit as `eval <eval@localhost>`, no remote. The evaluator's cwd. |
| `fh/` | temp `FACTORY_HOME`: initialised DB, `herdr_socket` = a dead path, `git_push` = 0, goal `G-001`, task `T-0001` (the task text), the evaluator agent row and the kickoff message |
| `home/` | empty, used as `HOME` |
| `cfg/` | `CLAUDE_CONFIG_DIR` with `.claude.json` from one constant (`scout/sandbox.py`): onboarding done, theme, the realpath of `work/` trusted |
| `role/` | read-only copies of `roles/settings/evaluator.json.in` and the system prompt (the live checkout is hidden inside), written by spawn |
| `bin/claude`, `bin/factory`, `scout/` | the nsbox shim and a read-only snapshot of the factory CLI |

Inside the namespace the real home, `/tmp`, `/dev/shm` and the runtime dir are hidden (nsbox allow-list mode). Bound back: the trial dir read-write (`bin/`, `scout/`, `role/` read-only), the one vendored plugin dir read-only, and the claude install read-only.

## Evaluator spawn

`spawn(..., env_extra, plugin_dirs, add_home_dir)` takes the three new arguments for the role `evaluator` only; any other role passing them dies. They are stored in `spawn_args` and applied again when the tick dequeues a queued evaluator. A queued evaluator whose sandbox no longer validates (`check_evaluator_spawn` refuses it, for example the trial dir is gone or the shim is not an executable regular file owned by the user) is cancelled by the tick instead of failing every tick, and its `running` trial becomes `cancelled` with `passed` 0, `ended` and a reason in `check_output`. Any other spawn failure (a transient multiplexer error) leaves the agent queued and the trial `running`; the next tick retries.

For the evaluator, `spawn` dies (exit 3, before the gate and before any multiplexer call) unless:

- `env_extra` sets only `FACTORY_HOME`, `HOME`, `CLAUDE_CONFIG_DIR`, `PATH`, `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` and `DISABLE_AUTOUPDATER`, and `FACTORY_HOME`, `HOME` and `CLAUDE_CONFIG_DIR` are the `fh`, `home` and `cfg` of one trial directly under `<skilleval_root>/trials/`;
- `PATH` starts with `<trial>/bin`, which holds the claude shim; the rest is the factory and claude binary dirs, then `/usr/local/bin:/usr/bin:/bin`;
- the cwd is `<trial>/work`;
- every plugin dir resolves under `<skilleval_root>/vendor/` and passes the prompt-only tree check (no scripts, hooks or MCP files).

The claude command line has no `--add-dir` of the factory home, never `roles/plugins/<role>`, one `--plugin-dir` per plugin dir, and `--settings` and `--append-system-prompt-file` point into `<trial>/role/`. No kickoff message is left in the live DB (the evaluator reads mail from its temp DB); the agent still gets one prompt telling it to run `factory task show T-0001`.

## The shim's environment

The shim passes through only `PATH`, `LANG`, `TERM`, `TMPDIR`, `LC_*`, the three claude knobs and `FACTORY_AGENT`. `HOME`, `FACTORY_HOME`, `FACTORY_ROLE` and `CLAUDE_CONFIG_DIR` are baked into the shim and win over whatever the pane has. Anything else is dropped.

## Tests

`tests/test_skilleval_sandbox.py`: layout, spawn argv/env for both conditions, refusals, the token rule and the queue path run everywhere (multiplexer stub, dummy token). `NamespaceTests` need user namespaces and skip with the reason where they are restricted.

## Collecting a trial: `factory skilleval collect <trial> [--reason timeout|over-budget|cancelled]`

Roles director, lead, owner; every worker role is refused. Code: `scout/skilleval.py` (`collect_trial`). Only a `running` trial is collected; a trial already in a terminal status is returned unchanged (idempotent, nothing is stopped or run twice).

Order of work:

1. End state. Without `--reason` the trial must be finished: its temp DB (`<trial>/fh`) shows the evaluator `done`, or the live agent row is lost or no longer `live` (then the trial is `error`: the agent exited without finishing, and the checker does not run). A still-live agent with no `--reason` is refused (exit 2). On every path a still-live agent is stopped first (stop flag set, then `force_stop`, confirmed `stopped`) before anything is staged or run; if the stop fails, collect exits 2, runs no checker, and leaves the trial running and the agent live for a retry. Concurrent collects of one trial are refused by a per-trial lock.
2. The trial CONNECT proxy is stopped (`nsbox.stop_proxy`), and the live agent row is set to `done` so the tick closes the pane.
3. `hidden/` and `check.py` of the task are copied into `work/` (a symlink the evaluator planted at a target path or on its parent chain is replaced, never followed). `check.py` runs **only** through `nsbox.run_contained`: no network, no token, the nsbox allow-list view (only the trial dir is visible under `$HOME`; `~/.claude`, `~/factory` and every other home path are absent), cwd `work/`, env `HOME=<trial>/home` and `PATH` only, 60 s timeout. Exit 0 is `passed`. A run over 60 s is killed and recorded as `failed` with a timeout note. Without user namespaces the trial is `error` (`check_output` names the reason) and `check.py` is not even copied: there is no uncontained fallback.
4. Metrics on `skill_trials`: `status` (`passed`, `failed`, `timeout`, `over-budget`, `error`, `cancelled`), `passed`, `ended`, `wall_s` (live agent `started` to the temp-DB done time, or now), `tokens_in`, `tokens_out`, `cache_read`, `cache_write` (summed from `<trial>/cfg/projects/**/*.jsonl` by `web/tokens.py` `usage_totals`, the same parser as the dashboard), `skill_invoked` (1 if a transcript has a `Skill` tool call naming a skill of the candidate or a `Read` of its `SKILL.md`, else 0; NULL for `without` trials) and `check_output` (last 2 KB). With `timeout` or `over-budget` the checker still runs but `passed` stays 0. With `cancelled` it does not run. The transcripts are written by the evaluator: symlinks and other non-regular files under `cfg/projects` are removed before they are read.
5. The sandbox is deleted for `passed`, `failed` and `cancelled`. It is kept for `error`, `timeout` and `over-budget` (a later cleanup task removes those). A `cancelled` trial whose agent could not be stopped also keeps its sandbox.

## Results

`factory skilleval list [--candidate C]` prints one line per trial; `factory skilleval results [--candidate C]` prints one summary line per candidate after its trial lines (`list` prints them too):

```
C-0001 fix-bug with: trial 1 status=passed passed=1 tokens=165 wall_s=300 skill_invoked=1
C-0001 summary: with: passed 1/2 tasks, median tokens 382, median wall_s 450; without: passed 1/2 tasks, median tokens 165, median wall_s 300
```

`tokens` is `tokens_in + tokens_out`; `-` means not recorded. Only trials in a terminal status other than `voided` count in the with/without figures; open trials are added as `; pending N` and `; running N`.

### Start log

When a trial's `claude` never reaches a session, read `<trial>/log/start.log`; `factory skilleval list|results` print its last rc and last 20 lines under every `error` or `lost` trial and every `running` trial with no tokens yet. The shim (`<trial>/bin/claude`) writes it: a timestamped traceback for any failure before stage 1 starts (token, proxy), a `stage1 exec` line (argv[0] only, never env), then the stage-1 stderr (teed, so the pane still shows it) and `stage1 exited rc=N`. The shim stays the parent of stage 1 so it can record the exit status. The log stays inside the trial dir, stops appending at 64 KiB (one `[truncated]` marker; stderr is still drained), strips control characters when printing, and redacts the OAuth token and anything shaped like `sk-ant-...`.

Tests: `tests/test_skilleval_collect.py` (fixtures in `tests/fixtures/skilleval/`). The contained-checker integration test needs user namespaces and skips with the reason elsewhere.

## Queue and the tick scheduler: `factory skilleval queue|cancel`

`factory skilleval queue C-xxxx [--tasks all|task1,task2]` (roles director, lead, boss, owner; workers, including scout and evaluator, are refused) queues a candidate for evaluation:

1. Refuses (exit 2, nothing queued) unless the candidate is `prompt-only` and not `needs-owner` or `dismissed`, then vendors it (`factory skilleval vendor`); a vendor refusal or a pre-screen hit propagates.
2. Inserts one `pending` trial per task and condition (`with`, `without`; a task's two trials are adjacent). `--tasks all` (the default) is every task under `skilleval/tasks/`. The candidate becomes `queued-eval`.
3. The key `(candidate, vendor_sha, task, condition)` is unique, so a second `queue` adds nothing, says `already queued` and prints the existing trials.

`factory skilleval cancel <trial id|trNNNN|C-xxxx>` (same roles) turns pending trials into `cancelled` and stops running ones through `collect` with reason `cancelled` (the checker does not run). If a stop fails, the command exits 2 and that trial stays running for a retry.

### Voiding a trial: `factory skilleval void <trial id|trNNNN> --reason infra --why "<text>"`

Roles director and lead only (workers and the boss are refused). For a finished trial whose result says nothing about the skill because the harness failed (for example the `timeout` of trial 1 caused by the T-0718 hook bug). Code: `scout/skilleval.py` (`void`).

- Allowed on `passed`, `failed`, `timeout`, `over-budget` and `error`. `pending`, `starting` and `running` trials are refused (use `cancel`); so are `cancelled` and already `voided` ones. Exit 2, row unchanged.
- `--reason infra` and a non-empty `--why` are both required; any other reason is refused. (`collect --reason` does not accept `infra`.)
- The status becomes `voided`. The row is never deleted and keeps `passed`, `wall_s`, tokens and the rest; `void_reason` and `void_why` (two nullable `skill_trials` columns, added to an existing DB by `migrate`) hold the reason. One `skilleval` event `void` records the old status, the figures and the why.
- `list` and `results` still print the trial line, with `status=voided void_reason=infra void_why='...'`. The with/without figures (passed x/y, median tokens, median wall_s) ignore voided trials; the summary adds `; voided N`.
- `factory skilleval try` treats a `voided` trial like `error` or `cancelled`: it reuses the row (the key is unique) and starts it again, clearing `void_reason` and `void_why`. The void event keeps the old figures.
- Sandboxes of voided trials are cleaned like `error` ones (`skilleval_keep_failed_h`).

Tests: `VoidCliTests` in `tests/test_skilleval_tick.py`; the re-run in `tests/test_skilleval_sandbox.py`.

`cmd_tick` calls `skilleval_tick` right after `scout_tick`, on the normal path only. It is wrapped like `scout_tick`: an exception is recorded as one `skilleval-failed` event per local day and never breaks the tick. Each pass (`scout/skilleval.py`, `tick`):

1. Collects every `running` trial: the temp DB shows the evaluator done, or the live agent row is lost or no longer live, then `collect_trial` (a lost agent ends as `error`). Otherwise a live agent over `skilleval_trial_minutes` of wall time is collected with reason `timeout`, and one over `skilleval_trial_max_tokens` (input + output + cache creation of `<trial>/cfg/projects`) with reason `over-budget`. An agent still queued at the gate has no wall clock and no tokens yet: the wall is measured from the moment the evaluator agent goes live (`agents.started`), never from when the trial was queued or launched. A `CollectError` (for example a failed stop) leaves the trial running, records a `collect-retry` event and is retried on the next tick.
2. Starts at most one trial: the oldest `pending` one, only if fewer than `skilleval_max_concurrent` trials are `running` or `starting` (a trial whose evaluator waits at a closed gate counts as running) and fewer than `skilleval_max_trials_per_day` trials were started on the current local day. Start is the same code as `try` (sandbox, preflight, `spawn` through the gate). Both first claim the row atomically (`BEGIN IMMEDIATE`, `UPDATE ... WHERE status='pending'`, rowcount 1) as `starting`, so two starters can never spawn the same `eval-trNNNN` and a cancelled trial is never revived; `cancel` uses the same guard and refuses a `starting` trial (retry in a minute). A `starting` trial older than 15 min (its process died) becomes `error`. An unexpected start failure (for example an OSError while building the sandbox) marks that trial `error` and the queue moves on. If the token file or user namespaces are missing, nothing starts: the trials stay pending and one `skilleval-blocked` event says why (again if the reason changes, or returns after a trial started in between). A refused start marks that trial `error`.
3. Cleans up: sandboxes of `error`, `timeout` and `over-budget` trials older than `skilleval_keep_failed_h` are deleted, and so is any `trNNNN` directory under `<skilleval_root>/trials` that has no `skill_trials` row. Other names are left alone.
4. Sets a candidate from `queued-eval` to `evaluated` once all its trials are terminal.

| config key | default | meaning |
|---|---|---|
| `skilleval_max_trials_per_day` | 6 | trials started per local day (`0` stops new starts) |
| `skilleval_max_concurrent` | 1 | running or gate-queued trials at once |
| `skilleval_trial_minutes` | 15 | wall budget per trial |
| `skilleval_trial_max_tokens` | 300000 | token budget per trial |
| `skilleval_keep_failed_h` | 48 | hours a failed sandbox is kept |

Six trials per candidate is one candidate per day by default; the director may raise the daily cap temporarily with `factory config`.

Tests: `tests/test_skilleval_tick.py` (injected clock, a fake launch, the checker mocked; the CLI tests run `bin/factory` as a subprocess).

## Scoped allowances and `--rescreen` (T-0728)

Three false positives hit every real plugin, so the pre-screen has three allowances. Each is scoped by file kind and exact pattern; the same text anywhere else still hits, and no other rule changed.

| allowance | clean only when | still hits |
|---|---|---|
| Apache-2.0 boilerplate URL | the file is named `LICENSE` or `NOTICE` (any case, optional `-`/`_`/`.` suffix such as `LICENSE-APACHE`, `NOTICE.md`) with no extension or `.md`/`.txt` and the URL is exactly `http(s)://www.apache.org/licenses/` or `…/licenses/LICENSE-2.0` | the same URL in any other file (`.py`, `.md`, `plugin.json`); any other URL, extra path or host in a LICENSE |
| doc-host author e-mail | the file's basename is `plugin.json`, the address is the value of `author.email`, `support` or `support.email`, its host is in `DOC_HOSTS` (or the `www.` form), it sits alone on its line and occurs once in the file | the same address in code, `.md`, another `plugin.json` key or a second place; any other domain; `https://user@host` userinfo; any other `@host` |
| `requests.` in prose | the file extension is `.md` or `.txt` (the pattern itself is unchanged) | every other file (code, `.json`, `.sh`, no extension): `requests.get(`, `import requests`, bare `requests.` |

`factory skilleval vendor C-xxxx --rescreen` (director or lead; workers are refused, exit 3) re-runs the current pre-screen on the kept copy (same sha) of a `needs-owner` candidate whose hold came from pre-screen hits:

- 0 hits: `VENDOR.json` records no hits, status goes back to `triaged`, event `vendor-rescreen-clear` names the old hit count and the rules that cleared (e.g. `url x3, requests x1`).
- any hit: status stays `needs-owner`, the hits are printed, exit 4. A rescreen never clears a hit by itself and never partially clears.
- Before the scan the kept files are checked against the per-file sha256 recorded in `VENDOR.json` at vendor time. A file that differs, has no recorded sha, or is missing is refused (exit 2, the file is named); so is a legacy `VENDOR.json` with no `files` map. Remove the kept copy and run `factory skilleval vendor C-xxxx` again.
- refused with exit 2, status unchanged: a candidate that is not `needs-owner` (dismissed, new, …), one held for another reason (`risk_detail` is not a list of pre-screen hits), or one with no valid vendored copy.
