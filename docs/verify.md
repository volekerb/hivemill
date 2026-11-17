# verify: run a change and see it working

`verify` is one fast harness that coder and QA both run to see a change working. It prints a step trace, and "could not run it" counts as a FAIL with the reason. It never opens the live factory home.

```
cd go && go run ./cmd/verify MODE [flags] [-- FACTORY ARGS...]
```

Modes: `cli` and `mod` work today. `web` is reserved for a follow-up task and exits 1 with a reason until then.

## Common flags

| flag | meaning |
|---|---|
| `--tree DIR` | the code under test (default: git toplevel of the current directory) |
| `--out DIR` | artefact directory (default: a new `verify-out-*` temp dir; kept, and printed in step 1) |
| `--timeout D` | limit for the whole run (default 120s for `cli`, 60s for `mod`) |

OUT holds `trace.txt` (same bytes as stdout), `stdout.txt` and `stderr.txt` of the run step (cli), and one `.log` per helper step.

## cli mode

```
verify cli [--impl python|go] [--seed empty|demo] [--setup 'FACTORY ARGS']... \
           [--expect-exit N] [--expect-stdout RE]... [--expect-stderr RE]... -- FACTORY ARGS...
```

Steps: `tempdir`, `tree`, `seed`, `impl`, one `setup-N` per `--setup`, `run`, `check-exit`, one `check-stdout-N` / `check-stderr-N` per expectation, `cleanup`.

- `seed empty` (default) runs `bin/factory init` in the temp home, so the DB schema exists and nothing else is in it. `seed demo` runs `python3 ci/demo_seed.py --home TMP --force` (the script has no executable bit, so it goes through python3).
- `impl python` (default) runs `TREE/bin/factory` with `FACTORY_CLI_IMPL=python`. `impl go` builds `./cmd/factory` in `TREE/go` (`GOTOOLCHAIN=local GOFLAGS=-mod=readonly GOPROXY=off`) and runs that; a build failure is a FAIL.
- Each `--setup` string is split with the factory shell-lexer and must exit 0.
- `--expect-exit` defaults to 0. The regular expressions are Go RE2 and match anywhere in the stream.
- The child sees `FACTORY_AGENT=owner`, so the CLI does not refuse in an agent pane; the home is the throwaway one.

## mod mode

`verify mod` is the cheapest check of the dashboard mod: `node` must be on PATH; `node --check` on every `.js` file in `plugins/hivemill/herdr-app/mods/hivemill/`; `mod.json` there parses as JSON; `node --test plugins/hivemill/herdr-app/tests/dashboard.test.mjs` (run in TREE, the file named, not the directory) exits 0. Steps: `tempdir`, `tree`, `node`, `node-check`, `mod-json`, `dashboard-test`, `cleanup`.

## Trace format (a contract)

One line per step, then one result line, to stdout and to `OUT/trace.txt`:

```
verify: step N NAME ok|fail MSms DETAIL
verify: result PASS|FAIL mode=cli|mod|web steps=N ms=MS [reason=ONE LINE]
```

Details are single-line. `reason=` appears on FAIL only. A bad mode or bad flags print only the result line (`steps=0`, `mode=cli` when the mode itself was bad). Example:

```
verify: step 1 tempdir ok 0ms home=/tmp/verify-home-123 out=/tmp/verify-out-456
verify: step 5 run ok 63ms exit=0
verify: result PASS mode=cli steps=7 ms=134
verify: result FAIL mode=cli steps=7 ms=419 reason=exit code 1, expected 0
```

## Exit codes

0 = PASS, 1 = FAIL. Nothing else. Every failure is a FAIL with a reason: bad flags, a missing tool, a setup error (reason starts `could not run: `), a timeout (reason contains `timeout`), a cleanup that leaves a child alive.

## Temp-home rule

Every run creates `TMP` (`verify-home-*` under `$TMPDIR`). Children get `FACTORY_HOME=TMP`, `HOME=TMP/home`, and no other inherited `FACTORY_*` variable; the inherited `FACTORY_HOME` is ignored. If the temp base is, or is inside, `$HOME/factory` or the inherited `FACTORY_HOME`, the run stops with `could not run: live home` before creating anything. TMP is removed at the end, PASS or FAIL. Children run in their own process group; the group is killed at timeout and after each command, and `cleanup` fails if any is still alive. There are no retries and no sleeps as waits.

## Examples

```
cd go && go run ./cmd/verify cli --tree .. -- goal list
cd go && go run ./cmd/verify cli --tree .. --seed demo --expect-stdout 'G-' -- goal list
cd go && go run ./cmd/verify cli --tree .. --expect-exit 3 -- goal show NOPE
cd go && go run ./cmd/verify mod --tree ..
```
