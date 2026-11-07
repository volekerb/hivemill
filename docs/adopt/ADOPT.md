---
name: adopt-factory
description: Use when setting up this factory (the multi-agent Claude Code factory in this repository) for a user on their own machine: install, configure, first run, optional systemd boot units. Asks the user before anything outward-facing.
---

# Adopt Hivemill

This file is written to you, a Claude Code session on the user's machine. The user asked you to set the factory up for them. Follow the steps in order. It mirrors [../SETUP.md](../SETUP.md), which has the full reference (config keys, troubleshooting); read it when a step fails.

It lives here, and not in a `.claude/skills` directory or a `CLAUDE.md` of this repository, on purpose: every factory agent works in this repository and would load it automatically. If the user wants it as a skill, they copy it to `~/.claude/skills/adopt-factory/SKILL.md` themselves.

## Rules

- Run each command yourself and check its output against the **Verify** line before the next step. If the output differs, stop and tell the user what you saw. Do not improvise around it.
- Make no machine-level change that a step does not list: no `sudo`, no package installs, no edits to dotfiles.
- Ask the user, in plain words and one question at a time, whenever a step says **ASK**. Do not guess a default for them.

## Ask the user before

Never do any of these without an explicit yes in this conversation, even if the user's first message sounded like a general go-ahead:

- `gh repo create`, or any other action that creates or changes something on GitHub.
- `git push`, or anything that sets or changes a git remote or git config.
- `systemctl --user enable` or `bash systemd/install.sh` without `--dry-run`.
- `loginctl enable-linger`.
- Turning `state_push` on (it publishes every goal, plan and message to the remote).
- Binding the web UI to anything other than `127.0.0.1`, or setting `serve_hostnames`.
- Choosing an install location other than `~/factory`, or writing `factory init --live-home`.

## Steps

### 1. Check the prerequisites

Run, and report which are missing:

```
claude --version
herdr --version
gh auth status
git --version
python3 --version
loginctl show-user $USER -p Linger
```

**Verify:** each prints a version, except `gh auth status` (prints the login state; it must say you are logged in) and the linger check (`Linger=yes` or `Linger=no`; `no` only matters for step 8). Python must be 3.11 or newer. If `herdr` or `gh` is missing or not logged in, tell the user the install or login command and stop: you do not install tools. The sqlite3 CLI is optional.

### 2. ASK: where to install

Ask: "Install the factory in `~/factory` (the default), or somewhere else?" Recommend the default. For another path, explain that `FACTORY_HOME` must then be set for every shell and unit, and ask for an absolute path.

### 3. Get the code

If the repository is already checked out where the user wants it, use it. Otherwise ask for the repository URL and run `git clone <url> <location>`, then check out the latest release tag:

```
cd <location>
git checkout "$(git tag --list 'v*' --sort=-v:refname | head -n 1)"
```

**Verify:** `git -C <location> rev-parse --short HEAD` prints a commit id and `ls <location>/bin/factory` exists, and `factory --version` (once the CLI is on `PATH`, step 4) prints `hivemill X.Y.Z`.

### 4. Put the CLI on PATH

```
mkdir -p ~/.local/bin
ln -s "<location>/bin/factory" ~/.local/bin/factory
factory --help
```

**Verify:** `factory --help` prints a usage text. If it says `command not found`, `~/.local/bin` is not on the user's `PATH`: tell them how to add it, do not edit their dotfiles.

If the location is not `~/factory`: after the user agrees, export `FACTORY_HOME=<location>` for the session and run `factory init --live-home <location>`. **Verify:** it prints `wrote live home <location> to ...` (or `live home already set to ... nothing changed` on a rerun).

### 5. Create the database

```
factory init
```

**Verify:** it prints `initialised <FACTORY_HOME>/db/factory.db`, and `factory config` lists the keys (for example `max_live_agents = 6`).

### 6. ASK: which repository to manage

Ask: "Which repository should the factory work on? Give me the local path and its GitHub `owner/name`." If it has no GitHub repository yet, say so and offer to continue without one, or let the user create it themselves. **Do not run `gh repo create` yourself** (see the list above).

```
factory project add <id> --name "<name>" --repo <path> --gh <owner/name>
```

**Verify:** `factory project list` shows the row. Ask whether the judge may merge automatically; if not, add `--no-auto-merge`.

### 7. Configuration

Show the user the config table in [../SETUP.md](../SETUP.md#configuration) in short. Change nothing unless they ask. Two questions to ask anyway:

- **ASK:** "Should the factory push its state (every goal, plan and message) to a `state` branch on the remote? Default is no." Set `factory config state_push 1` only on an explicit yes, and only after confirming the remote is private. See [../state-branch.md](../state-branch.md).
- **ASK:** "Do you want to reach the web UI by a name on your LAN?" If yes, ask for the name and run `factory config serve_hostnames <name>`; if no, leave it empty.

**Verify:** `factory config` prints the value you set.

### 8. ASK: boot units (optional)

Ask: "Should the factory start again by itself after a reboot? That installs systemd user units and may need linger enabled." If no, skip to step 9.

If yes:

1. `bash systemd/install.sh --dry-run` from the main checkout (not a worktree). **Verify:** every action line starts with `[dry-run]` and nothing is changed. Lines starting `ok:` or `WARNING:` and the final `done.` line are also normal in a dry run. Show the user the output.
2. If it warned that linger is off, ask before running `loginctl enable-linger $USER`. **Verify:** `loginctl show-user $USER -p Linger` prints `Linger=yes`.
3. Ask for a final yes, then `bash systemd/install.sh`. Add `--with-serve` only if the user wants the web UI as a service. **Verify:** it ends with `done. Units are enabled for the next boot; nothing was started or restarted.` and `systemd-analyze --user verify systemd/*.service systemd/*.timer` prints nothing.

### 9. First run

Start herdr if it is not running (`herdr server` in the background, or the user's own session; ask which). **Verify:** `herdr workspace list` exits 0. Then:

```
factory up
```

**Verify:** exit code 0. Each of boss, director and scout is either started or reported as `QUEUED <agent>: <reason>` (the spawn gate is closed; the tick starts it later). `factory agents` lists them. Exit 1 with `herdr is unreachable` means herdr is not running.

### 10. Open the web UI

Use `127.0.0.1` unless the user agreed to something else in the list above.

```
factory serve --bind 127.0.0.1
```

Run it in the background, or tell the user to run it in a spare terminal.

**Verify:** it prints `factory serve: http://127.0.0.1:8765/ (read-only, LAN only, no auth)` and `curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8765/` prints `200`. Stop it again if you started it only to check.

### 11. Hand over

Tell the user to open the boss's pane in herdr and describe what they want built: the boss is their only point of contact. Tell them that reboot recovery and the identity rules are in [../operations.md](../operations.md).

## Done when

- [ ] `claude`, `herdr`, `gh`, `git` and `python3` (3.11 or newer) passed the step 1 checks.
- [ ] `factory --help` works from a new shell.
- [ ] `factory init` printed `initialised ...` and `factory project list` shows the user's repository.
- [ ] `factory up` exited 0 and `factory agents` lists boss and director (started or queued).
- [ ] `curl` of the serve URL returned `200`.
- [ ] Every item in the "Ask the user before" list that you did, you did after an explicit yes. Tell the user which ones you did and which you left alone (for example "systemd units: not installed").
- [ ] You told the user what to do next (talk to the boss) and where the docs are.

## Trying it without herdr

If herdr is not installed and the user just wants a look, use the trial mode in [../SETUP.md](../SETUP.md#try-it-without-herdr). Nothing real runs there; say so.
