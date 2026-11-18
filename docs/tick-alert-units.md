# Go tick alert units 5-8 and go/internal/digest

Slice A of the hygiene block of the Go tick (G-048 stage P1c). It ports four units of `bin/factory_cli.py` to
`go/internal/tick` and moves the digest builder out of `go/internal/cli`.

| Unit | Python | Go |
|---|---|---|
| `digest` | `morning_digest` | `go/internal/tick/alert_digest.go`, builder in `go/internal/digest` |
| `tmp_alert` | `tmp_alert` | `alert_tmp.go` |
| `remote_url_alert` | `tmp_sweep` (lapless), then `remote_url_alert` | `alert_tmp.go` (sweep), `alert_remote.go` |
| `main_protection` | `main_protection_alert`, `main_protection_problem` | `alert_remote.go` |

Every unit keeps the Python lap name, counters, `events` rows, alert texts and the try/except wrapping: a digest failure is one
`digest-failed` event per day, a `tmp_sweep` failure a `tmp-sweep-failed` event, a gh or git that cannot answer a
`*-check-failed` event and never an alert.

## Off by default

The units are registered with `registerGo`, like `judge_ci_prune`. They run in Go only where the Go tick unit set lists
them (the enable step is task T-1470); `tick_impl python` stays the rollback.

## go/internal/digest

`digest.Build(store, env, goal, date, hours)` is `build_digest`; `digest.Send` is the store-only send of the tick. The package
imports `go/internal/store` and `go/internal/identity` only, never `go/internal/cli` or `go/internal/tick` (that import
direction is what made the first attempt a cycle). The cli commands `digest` and `digest --send` are thin wrappers around it;
their output, exit codes and DB rows are the golden cases `digest`, `digestall`, `digestall_empty`, `digestall_retrobad`
and `digestall_send`.

An input the Go builder cannot reproduce byte for byte returns `identity.ErrDelegate`. The cli then runs the command in
Python. The tick unit has no Python to fall back to inside a unit, so it reports that as the `digest-failed` event
("set tick_impl to python") instead of sending a different digest.

## Known differences from Python

- Error texts that Python takes from its own libraries (`json` decode errors inside an object, sqlite messages) are
  approximated; `str(e)` of a missing tool, a timeout and a bad `gh` exit is exact.
- `tmp_sweep` removes a scratch dir with `os.RemoveAll`: after a partial failure it keeps going, where `shutil.rmtree`
  stops at the first error. The dir is still counted as failed.
- Off Linux `tmp_usage` reads "unknown" (no `statvfs` port): the units do nothing.
