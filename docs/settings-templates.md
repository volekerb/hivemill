# Role settings templates (G-012 T-B2)

Claude Code permission rules are prefix strings with no `~` or env expansion, so the per-role settings need absolute paths. They are no longer tracked with the owner's paths: `roles/settings/<role>.json.in` are templates, and `factory` renders them.

## Tokens

| Token | Rendered as |
|---|---|
| `@FACTORY_LIVE_HOME@` | the live factory home (`LIVE_HOME`, derived from the passwd entry, see `docs/worker-guard.md`) |
| `@FACTORY_HOME_DIR@` | the passwd home directory of the user running factory |
| `@FACTORY_USER_CONFIG@` | `<home dir>/.config`; `<user config>/factory/live_home` is the file `factory init --live-home` writes |

The values are absolute, so an Edit/Write/Read rule is written `Edit(/@FACTORY_LIVE_HOME@/db/**)`: one leading slash plus the absolute path gives the `//abs/path` form Claude Code wants. A Bash rule uses the token without the extra slash.

## Rendering

`render_settings(role)` in `bin/factory` substitutes the tokens, checks that the result is valid JSON with no `@FACTORY_` token left (otherwise it exits 3 and nothing starts), and writes `<run>/settings/<role>.json` atomically (tmp file, rename, mode 0644). It runs on every spawn and relaunch, in `factory up` and in `factory init`, so a tampered rendered file is overwritten at the next launch. The `--settings` value of every agent is that path. It still ends in `/settings/<role>.json`, which is what the `/proc` role detection (`_ancestry_worker`) and the flagless check match on.

Every template denies Edit/Write on `<live home>/run/settings/**`, so no agent can edit the rendered files. Every worker role also denies Edit/Write on `<user config>/factory`, `<user config>/factory/**` and `<user config>/factory/live_home`.

The worker-guard hook command stays an absolute path inside the live checkout (`python3 <live home>/roles/hooks/worker-guard.py ...`): the hook trusts the root it was started from, so it is never a worktree or a relative path.

## Legacy path (compat)

Panes started before this change run `claude --settings <live>/roles/settings/<role>.json`. The sync pull deletes those tracked files, so the tick (every run) and the sync pull also render each role to that legacy path. The files are gitignored. This is marked `# compat: legacy --settings path (G-012 T-B2)` in `bin/factory` and goes in a later cleanup task.

## Reviewing a settings change

Edit the `.json.in` file. `python3 ci/guard_diff.py --base origin/main` prints `SETTINGS-LOOSENED <role> deny removed: <rule>` for a removed deny rule, an added allow rule or a removed hook, and `SETTINGS-TIGHTENED` for the reverse (see `docs/guard-diff.md`). Tests that need real settings use `harness.rendered_settings(role)`; `tests/test_settings_templates.py` covers the templates.
