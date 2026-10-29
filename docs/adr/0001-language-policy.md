# ADR 0001: Language policy

## Status

Accepted (2026-10-27). Director approval recorded on T-0344 (Gate A).

Origin: goal G-016, plan v1 (staff-engineer review, director approval 2026-10-26). This ADR records plan v1; it does
not re-decide it. Task: T-0344.

## Context

The owner asked for more performant and safer code, "go or rust". The factory is Python today (`bin/factory`,
`factory/`, `roles/hooks/worker-guard.py`). Two things motivate a policy:

- The worker-guard hook runs on every agent Bash call, and its Python bash parsing (regexes, `shlex`) has had
  repeated security gaps.
- Nobody had measured where time goes. The numbers are in [Profiling report](#profiling-report), produced by
  `ci/profile_baseline.py` (see [docs/profiling.md](../profiling.md)).

## Language policy

- **Go** is the primary language for performance-critical, long-running and security-sensitive parsing components.
- Rust only through an exception ADR. That ADR must justify why Go cannot do the job (a measured need, such as
  no GC pauses or a library only Rust has), name the toolchain install without root, the CI job (`cargo fmt --check`,
  `clippy -D warnings`, `cargo test`), and the dependency pins.
- **Python** stays for the CLI, the tick, `serve`, scout, and glue or one-off scripts.

| component | language | why |
|---|---|---|
| worker-guard hook (`roles/hooks/worker-guard.py`) | Go port behind a flag, Python stays default and fallback | runs on every Bash call; parser with repeated security gaps; see [Port decision](#port-decision) |
| CLI `bin/factory` | Python | startup cost is mostly imports; fixed with lazy imports (T-0345), no rewrite |
| tick | Python | I/O bound (git, gh, sqlite, journald); profile with cProfile before any port; not ported |
| `factory serve` | Python | not measured as hot; not ported |
| scout | Python | glue around fetch and sandbox; not hot |
| CI scripts, tests, one-off tools | Python | glue; the test suite is the behaviour contract |
| future parsers or long-running daemons | Go | memory safety, static types, one static binary |

## Safety

- Memory safety: Go is memory safe. No `unsafe`, no cgo.
- Unicode vs ASCII regex semantics: Python `str` regex `\w`, `\b`, `\d`, `\s` are Unicode; Go RE2 `\w` is ASCII.
  Ports use explicit Unicode classes (`\p{L}` and friends) or hand-written matchers, and every ported pattern has a
  test with non-ASCII input.
- Static types: Go code passes `go vet`; no `interface{}` plumbing for parsed data.
- No shell injection: Go starts processes with `exec.Command` and an argv slice only. Never `sh -c` with a built
  string.
- Fail closed for guards: a panic or internal error in a guard exits 2 (block). Unported or unknown paths also exit
  2. The sh shim maps every exit status other than 0 and 2 (126, 127, signals) to 2, because Claude Code treats
  other non-zero exits as non-blocking.
- Input size limits: guards read stdin with a bounded reader and block, not truncate, on oversize input.

## Toolchain

See [docs/go-toolchain.md](../go-toolchain.md). Summary: the pinned version is in `go/go.mod` (`go` and `toolchain`
lines; currently go1.27.3); the tarball from go.dev is sha256-verified; it is installed in `~/.local/go`; builds
use `GOTOOLCHAIN=local` so Go never downloads another toolchain; no root and no distro package. Coders do not install
toolchains; a missing `go` is reported to the lead.

## CI

- Job `go` (T-0342): `gofmt -l` (must print nothing), `go vet`, `go test -race`, `go build`, using `actions/setup-go`
  with `go-version-file: go/go.mod`. It sits under the `needs:` of `unittest`, which checks its result.
- Branch protection stays `['unittest']`; no new required check names, so no GitHub settings change.
- Planned job `guard-equiv` (T-0355), wired the same way: the differential gate against the Python guard.

## Building and shipping binaries

- No binaries in git.
- `factory up` and the tick build into `~/.local/lib/factory` (to a `.new` file, then rename). A stamp holding the
  git tree id of `go/` (`git rev-parse HEAD:go`, the srchash) is written last.
- The flag `guard_impl` is a director-only DB config key: `factory config guard_impl python|go|shadow`, default
  `python`. The shim cannot read sqlite, so the CLI materialises it into `$FACTORY_HOME/run/guard_impl` as
  `<impl> <srchash>` on every set, after every build and on every tick.
- The sh shim (`roles/hooks/worker-guard`) reads that file on every call and uses Go only when the impl is `go` or
  `shadow` and the srchash equals the binary stamp. A missing or stale binary, a failed build, or a missing flag file
  means Python.
- `run/guard_impl` and `~/.local/lib/factory` are guard-protected from workers (in both implementations), so a
  worker cannot flip the flag or swap the binary.
- Shadow mode: Python decides; Go runs on the same payload. Exit-code disagreements are logged to
  `$FACTORY_HOME/logs/guard-shadow.log` with the command's sha256 and both exit codes, never the command text.

## Dependencies

- stdlib only in phase 1. `go/go.mod` has no `require` lines and there is no `go.sum`.
- Any external module needs owner approval. Dependency rules for other languages: pin versions, list them here.

| dependency | pinned version | status |
|---|---|---|
| Go toolchain | go1.27.3 (`go/go.mod`) | in use |
| `mvdan.cc/sh/v3` (shell AST pre-check, phase 2) | not pinned | OWNER-GATED, not approved; no task is filed until the owner approves |

## How agents test Go code

| kind | rule |
|---|---|
| table tests | default form; one table per function, named cases |
| race | `go test -race ./...` in CI and before the PR |
| fuzz | `go test -fuzz` for parsers; for the guard the oracle is only no-panic and no-hang (differential checks cover correctness) |
| differential | for ports: `python3 ci/guard_diff.py --impl go` ([docs/guard-diff.md](../guard-diff.md)); zero loosened AND zero tightened against Python |
| guard unittests | `GUARD_IMPL=go` runs the existing guard unittests against the Go binary (T-0352) |
| test-count rule | the test count on the branch is at least that on `origin/main` (`ci/test_count_gate.py`); a run with 0 tests fails |

Hook and settings changes also follow [docs/test-first-pilot.md](../test-first-pilot.md).

## Profiling report

Run on the dev box with `python3 ci/profile_baseline.py --runs 20` (T-0341, [docs/profiling.md](../profiling.md)), against
main at the sha below. The box was not idle (load 5.7 on 8 vCPU, other agents running), so treat absolute values as
upper bounds; min vs median shows the jitter.

```
meta: git_sha=06a28ae6 python=3.13.5 nproc=8 loadavg=[5.67, 4.42, 2.84] date=2026-10-27T13:00:57+01:00
(ms)                                         runs       min    median       p95
python_start python3 -c pass                   20      9.76     10.22     13.73
python_start python3 -S -c pass                20      7.04      7.37       8.5
guard        coder                             60     37.69     40.37     49.12  exit={'allowed': 0, 'blocked': 2, 'heredoc': 0}
guard        qa                                60     38.17      40.8     52.58
guard        reviewer                          60     38.28     40.17     48.16
guard        judge                             60     37.84     40.12     49.82
guard        cleaner                           60     37.73     40.03     50.29
guard        evaluator                         60     38.07     41.76     52.91
guard        hq                                60     38.16     40.44     52.25  exit={'allowed': 0, 'blocked': 0, 'heredoc': 0}
cli          bin/factory --help                20    138.07    146.59    173.89
cli          import total: 53.7 ms; scout.* total: 5.9 ms
tick         wall time                         10   2853.73  11835.84 110589.14  [journald factory-tick.service, last 10 runs, whole-second stamps]
```

(The exit codes of the other roles equal the coder's; `exit_codes` confirms the hook really allowed and blocked.)

CLI import breakdown (cumulative ms): `scout.sources` 13.7, `urllib.request` 11.0, `http.client` 9.6, `argparse` 6.7,
`scout.sandbox` 5.2, `re` 5.0, `scout.nsbox` 4.9, `ctypes` 4.2. The `scout.*` modules together take 5.9 ms of the
53.7 ms import total (about 11% of imports, 4% of the 146.6 ms `--help`). Imports are about a third of CLI startup;
the rest of the 146.6 ms is not import time and was not broken down. T-0345 (lazy imports, PR #214) had not merged at
this sha, so these are the before numbers.

Ranking of hot paths:

1. **worker-guard**: 40 ms per call at every role against a 10 ms interpreter floor, so about 30 ms is the hook's own
   imports, regexes and parsing. It runs on every agent Bash call and has the repeated parsing gaps. Reason to
   look at it: call frequency and security.
2. **CLI startup**: 147 ms per `factory` call, 54 ms of it imports, of which scout is 6 ms. Python fixes this with
   lazy imports (T-0345), no new language.
3. **tick**: median 11.8 s, p95 110.6 s per run. I/O bound (git, gh, sqlite); the spread points at waiting, not CPU.
   cProfile first; not ported.
4. **serve**: not measured as hot by these numbers; not ported.

## Port decision

Director decision. Inputs: the pasted run above.

Measured cost of the guard (ms, median / p95): coder 40.4 / 49.1, qa 40.8 / 52.6, reviewer 40.2 / 48.2, judge 40.1 /
49.8, cleaner 40.0 / 50.3, evaluator 41.8 / 52.9, hq 40.4 / 52.3. All roles are the same within noise. Bash calls per
agent-hour: unknown; T-0341 does not measure it. For scale: 1,000 calls per agent-hour would be 40 s of CPU per hour
per agent, and it adds to the latency of each tool call by 40 ms, small against model turn times of seconds.

Expected gain of a Go port: estimated 30 ms to 35 ms per call (about 40 ms down to 5 ms to 10 ms). Basis: a static Go
binary starts in a few ms and has no import phase, versus 10 ms Python start plus about 30 ms of hook imports and
regex compilation. This is an estimate, not a measurement; T-0355 measures it through the shim.

Port cost:
- A byte-exact Go port of CPython `shlex` (posix, `punctuation_chars`; T-0358), because every token comes from it.
- Python Unicode `\w` vs Go RE2 ASCII `\w`: explicit classes and non-ASCII tests for each pattern.
- About 14 regex lookarounds that RE2 lacks, rewritten by hand with table tests (T-0347 to T-0351).
- The settings switch to the shim (T-0356), a planned SETTINGS-LOOSENED swap needing director approval.
- Eleven wave 2 tasks, each one guard PR at a time, queued behind Gate B.

Assessment, with the two arguments kept separate:
- Speed: the guard is 4x the interpreter floor, but 30 ms to 35 ms per Bash call is not a measured hot path in
  absolute terms. It does not pay for the cost by itself.
- Safety: the repeated gaps are logic gaps in hand-written pattern matching, not memory errors, so Go does not fix
  them on its own. A byte-exact port reproduces the same logic; the value comes from the differential gate (zero
  loosened and zero tightened against Python on a corpus, plus every guard test running in both implementations),
  which would also harden the Python guard's tests. That is a real but unquantified gain.

Verdict: shrink to T-0358, T-0347, T-0348 (shlex port, fail-closed Go skeleton, differential harness); hold T-0349 to
T-0357 until the director sees the harness diff results and decides if the safety gain justifies the rest.

## Port plan and rollback

Worker-guard phase 1, per plan v1 and v2 (the tasks that remain after the verdict above):

- Go binary built from `go/`; sections ported in order, unported sections exit 2.
- `own_goal` does not open sqlite: Go re-runs the payload through the Python hook (`--py-hook <path>`).
- Lookarounds are hand-written matchers with Python-derived table tests.
- The flag `guard_impl` defaults to `python`. Shim, build and stamp are described in
  [Building and shipping binaries](#building-and-shipping-binaries).
- Gate before any default flip: zero loosened AND zero tightened on the corpus, `GUARD_IMPL=diff` over every guard
  test, and 7 days of clean shadow mode (no lines in `guard-shadow.log`). The director decides the flip.
- Rollback: `factory config guard_impl python`. The shim reads the flag file on every call, so no relaunch is
  needed. A missing or stale binary also means Python.

## Not doing

- No CLI rewrite. If the numbers ever justify one, it comes back to the boss as a proposal with numbers.
- tick and `serve` are not ported.
- Phase 2 (`mvdan.cc/sh/v3`) is owner-gated; nothing is built before the owner approves the dependency.
- No Rust code without an exception ADR.

## Consequences

- New Go components follow this ADR; roles and skills link to this path (T-0346).
- The guard gets a measured, reversible path to Go, and the Python guard stays the default and the fallback.
- Two implementations of the guard exist during the port; the differential gate keeps them in sync.

## Alternatives

- Rust as the primary language: rejected; heavier toolchain and slower builds, and no need that Go cannot meet.
- Python only, with better tests: kept for everything not listed above.
- Distro `golang` package: rejected; needs root and its version drifts. `GOTOOLCHAIN=auto`: rejected; silent downloads.
- A flag file in `~/.config/factory`: rejected; a second source of truth outside the DB.
- New required CI checks: rejected; would need an owner change to GitHub settings.
