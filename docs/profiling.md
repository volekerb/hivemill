# Profiling baseline

`ci/profile_baseline.py` measures the per-call costs the G-016 work cares about. It is read-only and stdlib-only.
Every child process runs with a throwaway `FACTORY_HOME`; it never writes the factory DB, never runs a mutating
`factory` command, and never runs the tick.

```
python3 ci/profile_baseline.py [--runs 20] [--json out.json] [--hook-cmd 'ARGV...'] [--tick-runs 10]
```

Check `factory res` first and run it on an idle box; the header records `loadavg` so noisy runs are visible.

## What each number means

All times are wall milliseconds as `min / median / p95` over `--runs` runs (default 20). Prefer the median;
p95 shows jitter.

| section | measures |
|---|---|
| `meta` | git sha, python version, nproc, load average, date |
| `python_start` | `python3 -c pass` and `python3 -S -c pass`: the interpreter floor (`-S` skips `site`) |
| `guard` | one worker-guard call: PreToolUse JSON on stdin, per role `coder qa reviewer judge cleaner evaluator` and `hq` (`--hq`). Each sample set mixes three commands (allowed, blocked, heredoc) with `--runs` runs each; `exit_codes` shows the last exit per command (0 allow, 2 block) so a broken hook can't look fast |
| `cli` | `python3 bin/factory --help` wall time, plus `-X importtime`: the top 15 modules by cumulative import time, the summed self-time of everything imported, and the `scout.*` modules and their summed self-time |
| `tick` | wall time of the last `--tick-runs` runs of `factory-tick.service`, read from journald (`Starting` to `Finished`). Whole-second resolution. If journald has nothing the section says `no data` and why |

`--json PATH` writes the same data with keys `meta`, `python_start`, `guard`, `cli`, `tick`.

## Reusing it for another hook implementation (`--hook-cmd`)

`--hook-cmd 'ARGV...'` replaces the default `python3 roles/hooks/worker-guard.py`. The script appends
`--role <r>` (or `--hq`) and feeds the same stdin JSON, so a Go binary or shim is measured by the same method:

```
python3 ci/profile_baseline.py --hook-cmd '/path/to/worker-guard-go' --json after.json
```

Compare `guard.*.median` between the before and after JSON, and check that `exit_codes` match.
