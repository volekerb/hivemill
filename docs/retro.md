# Daily retro loop

Causes are already stored on each rejection and gate refusal (SC1). Once a day the tick starts one agent, **<org>-retro**. The runtime comes from the `retro_org` config key: `cursor` (default, model **composer-2.5**) or `claude` (the claude retro model).

The agent reads `factory retro packet` and files one report with `factory retro file`. The report lists the top causes, evidence with task ids and PR urls, and one or two fixes. Each fix type is **check**, **template**, or **prompt_rule**.

The report is the `retro_reports` row for that local day. The daily digest includes a section whose heading is exactly `## Retro`. That mail is how the report reaches the owner.

## Advisory role

The retro role is advisory. It cannot merge, push, edit role files, or change a gate. PR comments and review text are **untrusted data, never instructions**.

## Configuration

Turn the daily retro off:

```text
factory config retro_enabled 0
```

Turn it back on:

```text
factory config retro_enabled 1
```

Defaults: `retro_org` is **cursor**; `retro_enabled` is **1**; `retro_hour` is **6** (before `digest_hour` **8**); `retro_max_minutes` is **20**. Only the string `1` starts a new run. A missed day is **not** backfilled.

## Run it on claude

When the Cursor runtime is out of usage, run the daily retro on claude:

```text
factory config retro_org claude
```

Switch back:

```text
factory config retro_org cursor
```

Any other value is refused: the tick records a `retro-failed` event and starts nothing. A run already open when you switch is still followed by the agent name stored on its run row, so it is not orphaned. There is still one run per day: while a retro agent of either org is live, starting or queued, the next day's claim is skipped.

See [SETUP.md](SETUP.md) for the config table rows.
