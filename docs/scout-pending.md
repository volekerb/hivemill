# Scout pending order

`factory scout pending` (`scout/db.py` `pending()`) lists untriaged candidates (status `new`, no triage row) up to the
cap: `--limit`, or `scout_triage_per_run` when no limit is given. The order decides what fits inside the cap:

1. `kind` skill, collection or agent before plugin, mcp, hook, unknown or NULL.
2. `install_risk` exactly `prompt-only` before any flagged, unknown or NULL risk.
3. Candidates seen in any source other than `marketplace` (the `sources` JSON list) before marketplace-only ones.
4. Higher `stars_per_day` first, NULL last; then oldest `first_seen` (compared as an instant); then `id`.

Nothing is filtered out. A triaged candidate leaves the queue, so every candidate is reached in later runs.
The marketplace source adds many vendor plugins; this ordering keeps them from starving skills of triage slots.
