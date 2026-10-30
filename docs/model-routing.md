# Model routing

Each role starts on one model, set by `MODELS` in `bin/factory`. `role_model(con, role)` is the only resolver: `_spawn_start` and the `factory relaunch` plan both call it.

| role | model |
|---|---|
| boss, director, lead, pm, reviewer, judge, staff-engineer | claude-opus-5-5 |
| coder, qa, scout, evaluator | claude-sonnet-5-5 |
| cleaner | claude-haiku-5-5 (G-011 trial, director decision) |

Fable is never used. Only Opus models get `--effort` (config `opus_effort`), so Haiku and Sonnet start without it.

## Override and rollback

The cleaner has a config key, `model_cleaner`. Empty (the default) means the `MODELS` entry. The director sets it; the next cleaner spawn uses it, no code change or deploy:

    factory config model_cleaner claude-sonnet-5-5

Allowed values: `claude-opus-5-5`, `claude-sonnet-5-5`, `claude-haiku-5-5`, or empty to go back to the default. Anything else (any Fable id, a typo) is refused with exit 2 and the stored value stays as it was.

## Trial stop rule

Any wrong deletion by a Haiku cleaner (a worktree, branch or file outside the verified merged set) ends the trial: the director reverts at once with the command above. The cleaner guard is unchanged and still applies.

## Where the model shows up

- `factory agents`: the model column is written at spawn.
- `web/prices.py` prices `claude-haiku-5-5`; `web/tokens.py` reads the model from each transcript row.
- `factory config` lists `model_cleaner`.
