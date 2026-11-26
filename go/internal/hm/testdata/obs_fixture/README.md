# Synthetic obs fixture

Made-up event logs for `hivemill obs`: every id, machine, project and number is invented.

- `events.jsonl`: the local log of `box-a`.
- `box-b/events.jsonl`: the log pulled from `box-b`.

All dates are in January 2026. `ci/obs_screens.sh` moves them to the last days, so they fall in the page's window.
It shows all three banners: polling share, 2+ review rounds and one dominant stage (ci).
