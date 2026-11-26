# Warm standby

One machine runs the runner (the primary). A second machine (the standby) can restore the primary's newest DB snapshot and take over when the primary is down. There is never more than one active runner: the runner assumes it is the only writer of its SQLite DB, and the standby does no runner work until you fail over.

Replace `PRIMARY` below with the primary's ssh name. It may hold only letters, digits, `-`, `_` and `.`, so put a user name or a port in `~/.ssh/config`.

## Set up

On the primary, turn on snapshots:

```
hivemill config backup_interval 1h
```

Everything after the newest snapshot is lost in a failover, so the interval is the most work you accept to lose.

A dead machine cannot be read over ssh. Keep a copy of the snapshots somewhere the standby can still reach: `hivemill config backup_target file:///mnt/share/hivemill` on the primary (a share both machines mount), or a scheduled copy from the primary to the standby.

On the standby:

- Install `hivemill`, `claude`, `gh` and `hc`, and log in to each.
- Create the home with `hivemill init`. Put the project checkouts at the same paths as on the primary (`hivemill project list` shows them), and the files that config keys name (for example `scrub_terms_file`).
- Do not set `projects`: the standby home must not start tasks of its own.
- Run `hivemill config standby_from PRIMARY` and start the daemon (see [option-b-runner.md](option-b-runner.md#supervision)). It runs the monthly check.

## The standby check

```
hivemill standby check --from PRIMARY
```

It copies the newest `factory-*.db` snapshot, restores it into a temporary home, runs `hivemill run status` and `hivemill goal list` against that home, and prints for example `OK: factory-20261101T060000Z.db restored and read, snapshot age 42m0s`. The temporary home is removed, and the standby's own DB is not touched.

`--from` takes:

| Form | Reads |
|---|---|
| `PRIMARY` | `factory/state/hivemill/backups` under the remote user's home, over ssh |
| `PRIMARY:DIR` | `DIR` on that machine (relative to the remote home, or absolute) |
| a path that starts with `/`, `.` or `~/` | a local directory, for example the mounted share |

Point `standby_from` at the place you would really restore from.

The daemon runs the check every `standby_check_days` (default 30; `0` turns it off) when `standby_from` is set. A failure raises a `hivemill-standby` owner alert and writes a `standby.check` event. The daemon waits the full period after a failure too, so run the command by hand once you have fixed the cause, with `--from` exactly as `standby_from` has it: an OK then closes the alert and starts a new period.

Read the age in the OK line: the check passes with an old snapshot. It also does not prove that `claude`, `gh` or the project checkouts work on the standby.

## Fail over

The `systemctl` commands below are for Linux. On macOS, to stop the daemon and keep it stopped after a reboot or login:

```
launchctl disable gui/$(id -u)/hivemill.daemon
launchctl bootout gui/$(id -u)/hivemill.daemon
```

To start it again (this replaces `enable --now`):

```
launchctl enable gui/$(id -u)/hivemill.daemon
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/hivemill.daemon.plist
```

1. **Make sure the primary daemon is stopped and stays stopped.** On the primary: `systemctl --user disable --now hivemill.service`. Stopping the daemon does not stop the task, judge and planner sessions it started: end their terminals too, or they keep writing and pushing to the branches the standby resumes. If you cannot reach it, keep it powered off or off the network until you can do this. The two machines have separate homes, so the daemon lock cannot stop a primary that comes back.
2. On the standby, stop its daemon: `systemctl --user disable --now hivemill.service`.
3. Restore the newest snapshot (copy it to the standby first when it is not on a share):

   ```
   hivemill restore /path/to/factory-<stamp>.db --dry-run
   hivemill restore /path/to/factory-<stamp>.db
   ```

   Write down the `<db>.pre-restore-<ts>` path it prints: that is the standby's own DB, and you need it to hand back.
4. `hivemill drain`, so nothing new starts while you look.
5. Start the daemon: `systemctl --user enable --now hivemill.service`.
6. Check `hivemill run status` and `hivemill report`, then `hivemill resume`.

The snapshot holds the primary's runs, but not their terminals or Claude session files. The daemon recreates missing clones from pushed branches. A session it cannot resume ends as FAIL `session lost`; `hivemill run stop T-1234` puts such a task back to `todo`. Commits that were never pushed from the primary are not on the standby.

## Hand back

1. On the standby: `hivemill planner stop` (the restored snapshot carries the planner setting, so the standby daemon may have started a planner, and drain does not stop it), then `hivemill drain`, wait until `hivemill drain --status` shows no active run, then `systemctl --user disable --now hivemill.service` (disabled, so a reboot cannot start it again).
2. On the standby: `hivemill backup`, and copy the snapshot it prints to the primary.
3. On the primary, with its daemon still stopped: `hivemill restore <snapshot> --dry-run`, then without `--dry-run`.
4. On the primary: `systemctl --user enable --now hivemill.service`, check `hivemill run status`, then `hivemill resume`. If you use the planner, `hivemill planner start`: the restored DB has it off since step 1.
5. On the standby, put its own DB back **before** its daemon starts again, or it would be a second writer of the same work: `hivemill restore <db>.pre-restore-<ts>`, then `systemctl --user enable --now hivemill.service`.

## The daemon lock

Every daemon writes `state/hivemill/daemon.lock` in its home: its host name and the time, refreshed every pass and every minute. A daemon refuses to start while that file names another host and the time is newer than 15 minutes (or 3 passes, when that is longer). This protects a home that two machines can reach, and a home copied whole to another machine. A clean stop removes the file. A daemon on the same host is held off by the local lock in `state/hivemill/locks/`, so a restart after a crash is not delayed, unless the host name changed in between (a rename, or macOS on another network): the old record then looks like another machine's, so wait until it is stale or use `--take-over`.

When you know the other daemon is dead and do not want to wait:

```
hivemill daemon --take-over
```

A daemon that finds another host in the file stops with an error instead of writing on.

On a home that two machines share, disable the other machine's unit: a clean stop of the active daemon removes the file, and a unit that retries would claim the home during a restart. The age of a heartbeat compares the two machines' clocks, so keep both synchronised (NTP). The lock tells machines apart by host name only, so two machines with the same name (a cloned VM) are not held off. The lock is a guard against mistakes, not a fence: it cannot interrupt a pass that is already running, it reads and then writes the file without a cross-machine lock (two daemons that start at the same moment, or a heartbeat that crosses a `--take-over`, can leave the wrong one running, so use `--take-over` only when the other daemon is dead), and it knows nothing about a home it cannot see. Do not share a home over a network filesystem to get a standby; SQLite is not safe there.
