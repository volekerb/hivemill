# Scout timestamps and input rules

## Timestamps are instants, not text

After a DST switch the factory writes stamps with two offsets (`02:30:00+02:00`, then an hour later `02:10:00+01:00`). As text the second sorts first, so scout code never orders or compares ISO stamps as strings:

- `scout_stars` snapshots are read with `ORDER BY julianday(ts), ts`; `stars_per_day` is computed from the parsed instants, so two snapshots straddling the switch give the true elapsed time.
- `factory scout pending` orders by `julianday(first_seen)`; `factory scout list --since T` compares `julianday(first_seen) >= julianday(T)`.
- `factory scout show` lists star snapshots in time order.
- Ids and autoincrement counters (`ORDER BY id`) are unaffected.

`snapshot_stars` stores every timestamp as aware ISO text. A naive stamp is read as local time; an unparseable one is rejected. `--since` must be an ISO 8601 date or timestamp, else exit 2.

## Input rules

- `--limit` on `factory scout pending` must be at least 1 (exit 2 otherwise); a negative value no longer means "no limit".
- In `factory scout triage`, `--purpose`, `--overlap` and `--why` accept `-` (stdin) or `@path`. The `@path` file must resolve, after following symlinks, to a location inside the current directory; otherwise exit 2 and nothing is written. Other commands keep the unrestricted `@path`.
- Only the scout role (and director, lead, boss, owner for the reads) may run `factory scout`; any other worker is refused with exit 3, tested without the stub multiplexer exemption.
