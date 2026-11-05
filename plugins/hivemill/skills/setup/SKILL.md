---
name: setup
description: Guided install of Hivemill through its installer script. Use when the user runs /hivemill:setup or asks to install or set up Hivemill.
disable-model-invocation: true
argument-hint: "[--dry-run]"
allowed-tools: Bash(python3 ${CLAUDE_PLUGIN_ROOT}/scripts/install.py *)
---

# Hivemill setup

You are a thin guide over one script: `python3 ${CLAUDE_PLUGIN_ROOT}/scripts/install.py`. Call it `install.py` below.

## Rules

- Never ask for, accept or repeat a token, password or API key in this chat.
- If the user pastes one, do not use it. Tell them to revoke it and create a new one in their own terminal.
- Do not run the setup steps yourself. install.py does them; if it fails, stop and report.
- Every login happens in the user's own terminal, never through `!` here: output of a `!` command enters this conversation.
- Ask one question at a time. Plain words, no jargon.

## Steps, in order

1. Run `python3 ${CLAUDE_PLUGIN_ROOT}/scripts/install.py --check`. Explain the report in plain words: `ok` is fine, `warn` is a note, `missing` needs action.
2. If git or python is `missing`: tell the user the apt line is not in the `--check` report (only `--dry-run` and `--yes` print it): run `--dry-run` and show the apt line it prints, ask the user to run it in their own terminal, and stop until they say it is done. Then run `--check` again.
3. If `gh-auth` is missing: ask the user to open a separate terminal and run `gh auth login` there. If `claude-auth` is missing: ask them to run `claude` in a separate terminal and complete `/login`. Wait until they say it is done, then run `--check` again.
   `herdr` or `gh` `missing` (not installed yet) is expected on a fresh box: go on, the installer installs them. If `gh` is not installed, `gh-auth` cannot be checked yet; the installer asks for `gh auth login` later (exit 3, step 7). Any other `missing` line: show it and stop. If only `systemd-user` is missing, offer `--no-systemd`.
4. Ask only the real choices:
   - the project repo (optional): local path and GitHub owner/name, passed as `--project-id`, `--project-repo`, `--project-gh`;
   - the agent cap: offer the `suggested-max-agents` value from the report, passed as `--max-agents`;
   - start at boot, passed as `--linger`: default no;
   - scout, the skills triage helper: default off, so pass `--no-scout` unless the user asks for scout.
5. Run `python3 ${CLAUDE_PLUGIN_ROOT}/scripts/install.py --dry-run <flags>` and show the plan. Say that it installs user-local tools with pinned checksums, and that starting agents uses the user's Claude usage. Get an explicit yes before going on.
6. Run `python3 ${CLAUDE_PLUGIN_ROOT}/scripts/install.py --yes <flags>`. Read only the exit code and the `RESULT` line.
7. Act on the exit code:
   - 0 with `RESULT PASS url=...`: go to step 8.
   - 3: show the printed command for the user to run in their own terminal, then stop. When they are done, re-run step 6 with `--resume` if the message says so.
   - 2: show the message and fix the flags.
   - 4: say Hivemill is already installed. Offer `--resume` only if the user says a previous run failed.
   - 1: show the failing step from `RESULT FAIL step=...`. Do not improvise around it. Point to `docs/SETUP.md` in https://github.com/volekerb/hivemill.
8. Print the UI URL from the RESULT line and say: "Open the boss's pane in herdr and describe what you want built."
