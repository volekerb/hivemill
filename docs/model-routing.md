# Model routing

Each role starts on one model, set by `MODELS` in `bin/factory`. `role_model(con, role)` is the only resolver: `_spawn_start` and the `factory relaunch` plan both call it.

| role | default model | config key |
|---|---|---|
| boss | claude-opus-5-5 | `model_boss` |
| director | claude-opus-5-5 | `model_director` |
| lead | claude-opus-5-5 | `model_lead` |
| pm | claude-sonnet-5-5 | `model_pm` |
| coder | claude-sonnet-5-5 | `model_coder` |
| qa | claude-sonnet-5-5 | `model_qa` |
| reviewer | claude-opus-5-5 | `model_reviewer` |
| judge | claude-sonnet-5-5 | `model_judge` |
| cleaner | claude-haiku-5-5 (G-011 trial, director decision) | `model_cleaner` |
| staff-engineer | claude-opus-5-5 | `model_staff-engineer` |
| researcher | claude-sonnet-5-5 | `model_researcher` |
| scout, retro, evaluator | claude-sonnet-5-5 | `model_scout`, `model_retro`, `model_evaluator` |

G-064 SC3 moved pm, judge and researcher from Opus to Sonnet. The reviewer stays on Opus until the risk-tier rule lands. The merge gate (`factory judge`) is unchanged: the judge still needs a reviewer PASS and a QA PASS. Rollback: `factory config model_judge claude-opus-5-5`.

Fable is never used. Only Opus models get `--effort` (config `opus_effort`), so Haiku and Sonnet start without it.

## Override and rollback

Every role has a config key `model_<role>` (table above). Empty (the default) means the `MODELS` entry. It applies to the claude org only; other orgs use their own model map. The director sets it; the next spawn of that role uses it, no code change or deploy:

    factory config model_judge claude-opus-5-5

Allowed values: `claude-opus-5-5`, `claude-sonnet-5-5`, `claude-haiku-5-5`, or empty to go back to the default. Anything else (any Fable id, a typo) is refused with exit 2 and the stored value stays as it was.

## Trial stop rule

Any wrong deletion by a Haiku cleaner (a worktree, branch or file outside the verified merged set) ends the trial: the director reverts at once with the command above. The cleaner guard is unchanged and still applies.

## Where the model shows up

- `factory agents`: the model column is written at spawn.
- `web/prices.py` prices `claude-haiku-5-5`; `web/tokens.py` reads the model from each transcript row.
- `factory config` lists every `model_<role>` key.
