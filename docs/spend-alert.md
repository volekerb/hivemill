# Token-spend spike alert

Owner order (M41393): know about token-spend spikes so they can be fixed. The alert is deterministic: no LLM, no network.

## What runs

The tick's `util` unit runs `factory-go spend-alert` (Go package `go/internal/spendalert`). At most every 15 minutes it runs
`hm-spend report --hours 1 --json` and `hm-spend report --hours 24 --json` (found in `$HIVEMILL_BIN_DIR`,
`~/.local/share/hivemill/bin`, then `PATH`).

## When it alerts

- the last hour cost more than **$20**, or
- the last hour cost more than **2x** the trailing 24 h hourly average (24 h total / 24), and at least $1 (an idle box does not alert).

One alert per clock hour (UTC): throttle key `spend-alert:hour:<YYYY-MM-DDTHH>`. The alert is a `kind=escalation` message to
`claude-boss`. Its body gives the numbers, the top 3 sessions with their agent (matched by `agents.session_id`; "unknown
agent" for a subagent or an unmatched session), a per-session finding (`big-context`, `chatter`, `cache writes`, same
thresholds as hm-spend), and hm-spend's own findings with their advice.

## When hm-spend fails

hm-spend missing, a non-zero exit, a timeout or bad JSON writes one `spend-alert-failed` event per clock hour and sends no
alert. Install it: `cd go && go build -o ~/.local/share/hivemill/bin/hm-spend ./cmd/hm-spend`.

See [token-discipline.md](token-discipline.md) for the findings and the fixes.
