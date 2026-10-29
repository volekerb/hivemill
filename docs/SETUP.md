# Setup

How to install the factory on a Linux box and bring up its first agents. If you would rather have your own Claude Code do it for you, give it [adopt/ADOPT.md](adopt/ADOPT.md). Boot, reboot recovery and identity rules are in [operations.md](operations.md).

Every command here was run on this branch under a temporary `HOME` and `FACTORY_HOME`, except those marked **not run in CI**: they need a real herdr server, a real systemd user session, or a logged-in Claude Code, none of which a CI job has.

## Prerequisites

| Tool | Check | Notes |
|---|---|---|
| Claude Code | `claude --version`, and you are logged in | Every agent is a Claude Code session. **Not run in CI.** |
| herdr | `herdr --version` | The terminal multiplexer that hosts the agent panes. Install: see [Install herdr](#install-herdr). **Not run in CI.** |
| gh | `gh auth status` | Used for PRs, CI status and merges. Install: <https://github.com/cli/cli/blob/trunk/docs/install_linux.md>. **Not run in CI.** |
| git | `git --version` | Each task gets its own worktree. |
| Python | `python3 --version` | **3.11 or newer.** See below. Standard library only: there is nothing to `pip install`. |
| sqlite3 CLI | `sqlite3 --version` | Optional. Only for restoring a state dump by hand. |
| systemd user session with linger | `loginctl show-user $USER -p Linger` prints `Linger=yes` | Only needed for the boot units (see [systemd](#systemd)). **Not run in CI.** |

**Python minimum.** `web/tokens.py` calls `datetime.fromisoformat` on Claude log timestamps that end in `Z`, which only parses on Python 3.11 or newer; on 3.9 and 3.10 every token row would land in day `unknown`. Elsewhere `bin/factory` uses `Path.is_relative_to` (3.9) and SQLite's `ON CONFLICT ... DO UPDATE` upsert (SQLite 3.24+), so 3.11 is the true floor. CI runs Python 3.13 only.

### Install herdr

The official source is <https://github.com/herdrdev/herdr> (Apache-2.0, homepage <https://herdr.dev>); releases are at <https://github.com/herdrdev/herdr/releases>. Other GitHub repositories named herdr are forks or mirrors: do not use them. The release assets are plain binaries, no archive. Download one, verify it, then install it. This is the version the [fresh-install test](#test-a-fresh-install) pins:

```
mkdir -p ~/.local/bin
HB=$(mktemp)
curl -fsSL -o "$HB" https://github.com/herdrdev/herdr/releases/download/v0.9.5/herdr-linux-x86_64
echo "14d9e8bc8b4a9e0118d77c32266f76d461f09b40342eb23752960bd89f59929d  $HB" | sha256sum -c -
install -m 755 "$HB" ~/.local/bin/herdr
herdr --version
```

On ARM use `herdr-linux-aarch64` and sha256 `17d7536f2da50859523680ef279dffcac8e4a235f4e46fe8da6c2ec44141897c`. Do not install if `sha256sum -c` does not print `OK`. Newer releases are fine for normal use; only the test tool pins v0.9.5. Homebrew, mise and Nix packages exist upstream: see the herdr README. **Not run in CI.**

The pinned release page: <https://github.com/herdrdev/herdr/releases/tag/v0.9.5>.

## Install

1. Clone the repository. The default location is `~/factory`; any other place works if you set `FACTORY_HOME` (below).

   ```
   git clone https://github.com/example-org/factory.git ~/factory
   cd ~/factory
   ```

2. Put the CLI on your `PATH`:

   ```
   mkdir -p ~/.local/bin
   ln -s "$PWD/bin/factory" ~/.local/bin/factory
   factory --help
   ```

   Make sure `~/.local/bin` is on `PATH`; the systemd units add it themselves.

3. Pick the home. The factory keeps its state in one directory, `FACTORY_HOME` (default `~/factory`, the clone). The database is `<FACTORY_HOME>/db/factory.db` and there is no separate variable for it. If you want the home somewhere other than `~/factory`, export `FACTORY_HOME` for every shell and unit that runs `factory`, and record it once as a live home (the tools that guard agents recognise `~/factory` and this recorded path as "the real factory"):

   ```
   factory init --live-home /srv/factory
   ```

   `--live-home` takes one absolute path, is write-once (a different value is refused until you delete `~/.config/factory/live_home` by hand), and is refused for agents. **Not run in CI**: it writes under your real passwd home, never `$HOME`. Skip it for the default `~/factory`.

4. Create the database:

   ```
   factory init
   ```

   Output: `initialised <FACTORY_HOME>/db/factory.db`. It is safe to run again: existing config values are kept. `init` also renders the per-role Claude settings files from the templates `roles/settings/<role>.json.in` into `<FACTORY_HOME>/run/settings/<role>.json`. Every spawn and relaunch renders them again.

5. Register the repository the factory will work on:

   ```
   factory project add myapp --name "My app" --repo ~/src/myapp --gh example-org/myapp
   factory project list
   ```

   `--base` is the base branch (default `main`); `--no-auto-merge` makes the judge stop before merging. Only the owner, the boss and the director may add projects.

   A project whose CI runs different checks than the global `required_checks` (see Configuration) gets its own list: `factory project add myapp ... --required-checks "build,stale-ci"`, or on an existing project `factory project update myapp --required-checks "build"` (an empty value goes back to the global list). A non-empty project list replaces the global one, it is not added to it. The judge merge gate and the tick's judge-ready check use it for the project of the task's goal (or of the PR's repository). `factory project list` shows the column.

## Configuration

`factory config` prints every key with its current value; `factory config <key> <value>` sets one (owner or director only). Unknown keys are refused. `max_live_judges` (default 3, 0 = no cap) caps live judges; see [spawn-queue.md](spawn-queue.md). Values are strings in the database; numbers are read as integers or floats where the code needs them.

| Key | Default | Meaning |
|---|---|---|
| `max_live_agents` | `6` | Cap on live factory agents that herdr also lists. |
| `max_total_claude` | `10` | Cap on all `claude` processes on the host, yours included. The spawn gate closes at this number. |
| `gate_max_load_ratio` | `1.25` | The gate closes while load1 is above this times the CPU count. |
| `test_slots` | `2` | Host-wide slots for full test-suite runs. |
| `min_mem_available_mb` | `1024` | The gate closes below this much available RAM. |
| `tmp_sweep_pct` | `80` | Over this, the tick deletes `/tmp` scratch dirs and `/tmp/qa-*` homes of agents that all finished (done/stopped/lost/exited) 30+ min ago; unknown dirs stay. One `tmp-swept` event per sweep, shown in `factory res`; if `/tmp` is still over `tmp_max_pct` the director is alerted once. |
| `tmp_max_pct` | `90` | The gate closes when the filesystem holding `/tmp` is this full. The cleaner role is exempt from this check only (it frees `/tmp`); every other cap still applies to it. |
| `max_five_hour_pct` | `92` | The gate closes above this share of the five-hour Claude usage window. |
| `max_seven_day_pct` | `92` | Same, for the seven-day window. |
| `nudge_cooldown_s` | `120` | Minimum seconds between nudges to one agent about unread mail. |
| `director_cycle_s` | `900` | How often the tick gives the director a planning cycle. |
| `blocked_alert_s` | `600` | How long an agent may sit on a dialog before it is reported blocked. |
| `hq_unblock_s` | `900` | After this long the tick denies (Esc) a permission dialog on a boss, director or lead pane. |
| `git_push` | `1` | `1` = exports are pushed to origin; `0` = commit locally only. |
| `push_interval_s` | `600` | The tick commits every export change but pushes at most this often. |
| `required_checks` | empty | Comma-separated CI check names the judge needs green before it merges. A non-empty per-project `required_checks` (`factory project update`) replaces it for that project. |
| `herdr_socket` | `~/.config/herdr/herdr.sock` | The herdr socket every `herdr` call is pinned to. |
| `stop_timeout_s` | `300` | `factory stop`: after this long the tick force-closes the pane of a stopped agent that is still live. |
| `sandbox_roles` | empty | Comma list of roles (from `reviewer`, `qa`, `coder`) whose Claude runs in the namespace sandbox. Empty = off. Read at every spawn. |
| `task_clones` | `0` | `1` = a new coder task gets a standalone `git clone --shared` instead of a worktree (docs/git-hardening.md, "Per-task clones"). Only `0` or `1`. |
| `serve_bind` | empty | `factory serve` listen address. Empty = the source address of the default route. |
| `serve_port` | `8765` | `factory serve` TCP port. |
| `serve_hostnames` | empty | Extra `Host` names `factory serve` accepts besides its bind address, comma separated. For example a LAN name such as `factory.example`. |
| `factory_remote_url` | empty | The one `origin` URL the git guard allows for the factory checkout. Empty = learned once from the live checkout; see [Identity and remote](#identity-and-remote). |
| `state_push` | `0` | `1` = also push the `state` branch (a full database dump) to `origin`; needs `git_push` `1`; see [Identity and remote](#identity-and-remote). |
| `opus_effort` | `medium` | `--effort` level for Opus agents. Empty leaves Claude's default. Sonnet agents are never touched. |
| `judge_max_refreshes` | `4` | Refreshes one `factory judge --merge` run does before it gives up with `STALE-LOOP`. Invalid or below 1 falls back to 4. |
| `judge_ci_wait_s` | `2400` | `factory judge --merge` waits this long for pending CI before refusing. |
| `skilleval_root` | `~/.cache/factory-skilleval` | Where vendored third-party skills are kept for evaluation. Must lie outside the factory home and `~/.claude`. |
| `digest_hour` | `8` | Local hour from which the tick sends the daily digest (director to boss), once a day. |
| `scout_enabled` | `1` | `1` = the tick starts the skill scout at each slot; `0` = no new runs. |
| `scout_slots` | `09,18` | Local hours at which the scout runs, comma separated. |
| `scout_max_minutes` | `20` | Wall-clock budget of one scout run. |
| `scout_max_tokens` | `300000` | Token budget of one scout run. |
| `recycle_context_tokens` | `250000` | A director or lead above this context size is relaunched fresh once idle. `0` = never. |
| `scout_triage_per_run` | `8` | `factory scout pending` lists at most this many candidates (1 to 50). |
| `scout_queue_max_h` | `6` | A scout queued longer than this many hours is cancelled. |
| `skilleval_max_trials_per_day` | `6` | The tick starts at most this many skill trials per local day. `0` = none. |
| `skilleval_max_concurrent` | `1` | Skill trials running (or queued at the gate) at once. |
| `skilleval_trial_minutes` | `15` | Wall-clock budget of one trial. |
| `skilleval_trial_max_tokens` | `300000` | Token budget of one trial. |
| `skilleval_keep_failed_h` | `48` | Sandboxes of failed trials are deleted after this many hours. |

## First run

These steps need real herdr and Claude Code. **Not run in CI.** For a run that needs neither, see [Try it without herdr](#try-it-without-herdr).

1. Start the herdr server, either in the foreground (`herdr server`) or through its unit (see [systemd](#systemd)). Check it: `herdr workspace list` exits 0.
2. Bring up the headquarters: `factory up`. It starts the boss and the director (plus the scout), resumes every active team's lead, and skips agents herdr already runs. If the spawn gate is closed (RAM, load, usage window, too many Claude processes) it prints `QUEUED <agent>: <reason>` and the tick starts the agent when capacity frees up. Check with `factory agents`.
3. Talk to the boss. Open its pane in herdr and describe what you want built; the boss turns it into a goal, and the director staffs a team. The boss is the only agent that talks to you.
4. Open the read-only web UI. It has no writes and no authentication, so bind it to an address you trust:

   ```
   factory serve --bind 127.0.0.1
   ```

   It prints `factory serve: http://127.0.0.1:8765/ (read-only, LAN only, no auth)`; open that URL. `--port` overrides `serve_port`.

## Try it without herdr

This mode runs the real CLI and web UI against the fake `herdr` in `tests/stubs/`, under a throwaway home. **Nothing real runs**: no agent is started, no pane exists, no Claude session is used. It shows you the database, the commands and the web UI, and it is what the fresh-box test follows.

```
export TRIAL=$(mktemp -d)                       # everything lives here; delete it when done
export HOME=$TRIAL/home FACTORY_HOME=$TRIAL/fh
mkdir -p "$HOME" "$FACTORY_HOME"
export FAKE_HERDR_STATE=$TRIAL/herdr.json FAKE_CALLS=$TRIAL/calls.jsonl
export PATH=$PWD/tests/stubs:$PWD/bin:$PATH      # run from the repository root

factory init
factory project add demo --name Demo --repo "$TRIAL/repo" --gh example-org/demo
factory up
factory agents
factory serve --bind 127.0.0.1 --port 18623 &
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18623/
kill %1
```

Run on this branch, the output is:

```
initialised <tmp>/fh/db/factory.db
demo
QUEUED director: N/10 claude processes on host (incl. non-factory). The tick starts it when capacity frees up.
QUEUED boss: N/10 claude processes on host (incl. non-factory). The tick starts it when capacity frees up.
QUEUED scout: N/10 claude processes on host (incl. non-factory). The tick starts it when capacity frees up.
NAME      ROLE      STATUS  FLAGLESS  RELAUNCH  GOAL_ID  TEAM_ID  TASK_ID  PANE_ID  MODEL  STARTED
director  director  queued  ...
factory serve: http://127.0.0.1:18623/ (read-only, LAN only, no auth)
200
```

The `QUEUED` lines are the spawn gate looking at the host the run was made on, which already had more than ten Claude processes. On a quiet box `factory up` instead places the three agents in the fake herdr and `factory agents` lists them as live; either way nothing runs. A herdr that is down is simulated with `echo '{"fail": true}' > "$FAKE_HERDR_STATE"`, after which `factory up` exits 1 with `herdr is unreachable (is herdr-server running?); nothing started`.

Commands run as yourself count as the owner. If you run this from inside a factory agent's pane, the identity guard treats you as that agent, and `project add` and `up` are refused; run it from a plain shell.

For data to look at, seed a fictional home instead (see [demo-data.md](demo-data.md)):

```
python3 ci/demo_seed.py --home "$TRIAL/demo" --now 2026-10-25T12:00:00+01:00
HOME=$TRIAL/demo/home FACTORY_HOME=$TRIAL/demo factory serve --bind 127.0.0.1 --port 18624
```

The `HOME=` prefix is mandatory: some pages read `~/.claude`, and without it they show your real usage. Stop the server with Ctrl-C, then `rm -rf "$TRIAL"`.

## Test a fresh install

`ci/freshbox_test.py` proves that this document works on a clean machine, in one command. It is for you and for contributors who change install or setup. **Not run in CI.**

```
sudo python3 ci/freshbox_test.py --repo https://github.com/<you>/factory.git --ref <branch>
```

Run it in your own terminal (it needs a TTY) on a Linux box with `sudo`. What it does, in order:

1. Refuses to start unless it runs as root with a TTY, the throwaway user does not exist yet (default `fbtest`; it never touches an existing user and never uses your own account) and the port is free.
2. Creates the user, installs herdr (the pinned release, size and sha256 verified, or the binary you pass with `--herdr-bin`) and Claude Code (the official installer, downloaded to a file, sha256 logged, run as that user, never piped into a shell).
3. Checks the prerequisites above as that user. Linger is not enabled: `factory up` does not need it, only the boot units do.
4. Pauses for **two interactive logins**: Claude Code (`/login`, then `/exit`), then `gh auth login` and `gh auth setup-git`. Their output is never logged. With `--gh-token-file` the GitHub login is not interactive: the token goes to `gh auth login --with-token` on stdin.
5. Follows this document: clones `--repo` at `--ref`, links the CLI, `factory init`, checks the role settings exist, adds a throwaway demo project (a fresh git repo in the user's home), sets `max_total_claude`, starts the herdr server, runs `factory up` and waits for the boss and director, then starts `factory serve` and checks that `/` and `/goals` answer HTTP 200.
6. **Always** cleans up, also after a failure, Ctrl-C or an error: stops serve, herdr and the user's processes, ends the user's session, deletes the user and its home (only a user this run created), checks that the user is gone and removes its own temp directory.

> **Warning: never revoke the GitHub CLI OAuth app on a box where `gh` is also logged in for the same account.** GitHub keeps one authorization per app per user, so revoking it revokes every `gh` token of that account, including your own. The tool logs the test user out itself; there is nothing to revoke. Prefer `--gh-token-file` with a fine-grained token: it creates no OAuth grant at all.

**Costs and side effects.** It starts real Claude agents for about two minutes and so uses your Claude usage. It creates and deletes a Linux user. The tool logs the test user out of GitHub itself (`gh auth logout`) before it deletes the user. It writes nothing to your home except the results file, and talks to nothing of your own factory or herdr.

**Results file.** `~/factory-freshbox-<UTC timestamp>.log` (override with `--out`), mode 600, owned by you. It has one `[PASS|FAIL|INCONCLUSIVE] <n> <name>: <detail>` line per step, the tail of each step's output with token-shaped strings redacted, and a last line `RESULT PASS|FAIL|INCONCLUSIVE steps=<n> failed=<names>` (plus `inconclusive=<names>`). Exit code: 0 PASS, 1 FAIL, 3 INCONCLUSIVE (the host was too busy to decide; re-run). Paste that line into your pull request.

| Flag | Default | Meaning |
|---|---|---|
| `--repo URL` | the project's public repository | What to clone. A private repository works through `gh auth setup-git`. |
| `--ref REF` | the remote default branch | Branch, tag, sha or `refs/pull/N/head` to test. |
| `--user NAME` | `fbtest` | Throwaway user to create and delete. |
| `--port N` | `8799` | Web UI port for the check. |
| `--out PATH` | `~/factory-freshbox-<ts>.log` | Results file. |
| `--herdr-bin PATH` | pinned download | Copy this binary instead of downloading v0.9.5. It is not checked against the pin (its sha256 is logged). Required on an architecture other than x86_64 and aarch64. |
| `--max-total-claude N` | Claude processes running right before `factory up`, plus 4 | The `max_total_claude` set for the test factory. If the box is busy and `factory up` is queued on the claude cap, the step reports `INCONCLUSIVE (host busy)`, not `FAIL`; re-run when the box is quieter or pass a larger N. |
| `--gh-token-file PATH` | interactive `gh auth login` | Recommended. File with a short-lived token for the test user (`-` reads stdin, which must still be your terminal: paste the token, press Enter; input is hidden; a piped stdin is refused because the Claude login needs the TTY). It is fed to `gh auth login --with-token` on stdin only and never logged. Use a fine-grained personal access token with read access to the repository and a short expiry, and delete it afterwards. |
| `--dry-run` | off | Needs no root and no TTY. Prints the ordered plan with the exact command of every step and of the cleanup; creates, downloads and runs nothing. |

Every command for the test user runs as `runuser -u <user> -- env -i HOME=... XDG_RUNTIME_DIR=...`: a clean environment, no `FACTORY_HOME`, so it cannot reach your factory. `runuser` is enough because nothing here needs a user systemd session.

## systemd

Units in `systemd/` bring the factory back after a reboot. Linger must be on, or user units do not start at boot: `loginctl enable-linger $USER` (this changes your account; do it on purpose).

1. See what the installer would do. It changes nothing:

   ```
   bash systemd/install.sh --dry-run
   ```

   **Not run in CI.** Run it from the main checkout, not from a worktree, because the installed units are symlinks into the checkout.
2. Install: `bash systemd/install.sh`. It links the units into `${XDG_CONFIG_HOME:-~/.config}/systemd/user`, reloads and enables them. It never uses `--now` and never stops or restarts herdr or the tick, so nothing starts until the next boot or until you start a unit yourself. **Not run in CI.**
3. Verify: `systemd-analyze --user verify systemd/*.service systemd/*.timer` prints nothing when the units are fine. **Not run in CI.**

What the units do (details in [operations.md](operations.md)):

| Unit | Does |
|---|---|
| `herdr-server.service` | Runs `herdr server`, the headless server. |
| `factory-up.service` | Waits up to 120 s for herdr, then runs `factory up`. |
| `factory-tick.timer` / `factory-tick.service` | Runs `factory tick` every minute: reaps finished workers, starts queued spawns, nudges agents with unread mail, exports state. |
| `factory-serve.service` | The read-only web UI. Installed only with `bash systemd/install.sh --with-serve`. |

The units assume the factory lives in `~/factory` and that `factory` and `herdr` are reachable at `~/.local/bin` (symlink herdr there if it is installed elsewhere). If you chose another `FACTORY_HOME`, add an `Environment=FACTORY_HOME=...` line to each unit before you install.

## Identity and remote

- **Remote.** The factory pushes its exports and creates PR branches through `origin`. `factory_remote_url` is empty by default. The first time the git guard needs it, the factory reads `remote.origin.url` from the live checkout, keeps it if it looks like `https://github.com/<owner>/<repo>.git` (trust on first use), and stores it. The guard then accepts only that URL for the factory checkout. Set the key yourself with `factory config factory_remote_url <url>` to pin another one.
- **LAN name.** `serve_hostnames` defaults to empty, so the web UI answers only to its bind address. To reach it by a name on your LAN, set `factory config serve_hostnames factory.example` (several names, comma separated) and point your DNS at the box. Any other `Host` header gets `421 misdirected request`.
- **State branch.** `state_push` is off by default. With `factory config state_push 1` (and `git_push` `1`) the tick pushes the `state` branch, including a full database dump, to `origin`. **Privacy warning:** those files contain every goal, plan, message and verdict. Turn it on only when the remote is private and everyone who can read it may read all of that. See [state-branch.md](state-branch.md).
- **Agent identity** (`FACTORY_AGENT`, fail-closed rules): see the Identity section of [operations.md](operations.md#identity).

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `factory: herdr is unreachable (is herdr-server running?); nothing started` (exit 1) | The herdr server is not running or the socket differs. Start it (`herdr server`, or `systemctl --user start herdr-server`) and check `herdr workspace list`. The socket is the `herdr_socket` config key. **Not run in CI** (the message was produced with the fake herdr in failure mode). |
| Browser shows `421 misdirected request` | You opened the UI by a name or address it does not serve. Use the bind address it printed, or add the name to `serve_hostnames` (see above). Reproduced: `curl -H 'Host: evil.example' http://127.0.0.1:<port>/` returns 421. |
| After a reboot an agent runs without role isolation (`FLAGLESS` in `factory agents`) | herdr resumed the pane as a bare `claude --resume`. See "Post-reboot role flags" in [operations.md](operations.md#post-reboot-role-flags-flagless-agents); relaunch with `factory relaunch <name>`. |
| A worker shows as `lost` | Workers are not resumed after a reboot or crash. The director respawns them; there is nothing to do by hand. |
| `QUEUED <agent>: ...` from `factory up` | The spawn gate is closed. `factory res` shows which limit and the current numbers; the tick starts the agent when it opens, or raise the matching config key. |
| `factory: factory.db moved to db/factory.db; rerun the command` | A tick migrated an old-layout home. Rerun the command. |
| `unknown`/permission errors for commands you run | You are inside an agent's pane; run from a plain shell (see [operations.md](operations.md#identity)). |

Where things are: the database is `<FACTORY_HOME>/db/factory.db`, moved only by `FACTORY_HOME`, exports are in `<FACTORY_HOME>/exports/`, and the audit log is `<FACTORY_HOME>/state/events.jsonl`.
