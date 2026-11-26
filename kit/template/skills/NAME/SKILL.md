---
name: NAME
description: One sentence saying what NAME does and when Claude should use it.
---

# NAME

Replace this paragraph with what the skill does, for whom, and what it does not do.

## Find the binary

The skill drives the `hm-NAME` binary. Look for it in this order and use the first one found:

1. `$HIVEMILL_BIN_DIR/hm-NAME`
2. `${CLAUDE_PLUGIN_ROOT}/bin/hm-NAME` (the verifier shim that ships with the plugin)
3. `~/.local/share/hivemill/bin/hm-NAME` (installed by `npx hivemill add NAME`)

If none exists, tell the user to install it with `/plugin install hivemill-NAME@hivemill` or `npx hivemill add NAME`.
Run `hm-NAME --version` once and report the version.

## Rules

- Never ask the user for a token, password or key, and never repeat one you see. The tools use the user's own
  login (for example `gh auth login`); if a login is missing, tell the user to run it themselves.
- Run only the documented `hm-NAME` commands. Do not edit the config by hand; use the binary's config commands.
- Config lives in one JSON file with an integer `schemaVersion`; see `NAME.example.json`.

## Steps

1. Replace this list with the steps Claude follows, one command per step.
