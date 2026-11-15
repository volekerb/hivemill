# macOS branches of the Python CLI (T-1568, G-049 SC3)

Until the Go cut-over (G-048) the live tick, spawn and serve are `bin/factory_cli.py`. On macOS (`sys.platform == "darwin"`) it
has small platform branches; Linux code paths are unchanged. ADR 0003 sections 4.1-4.4.

| Area | Linux | macOS |
|---|---|---|
| Detached runs (mech-merge, gate runner) | `systemd-run --user` under systemd | no systemd-run: `Popen(start_new_session=True)` |
| Serve protection | `systemctl --user show` MainPID + `/proc/*/cgroup` | `<FACTORY_HOME>/state/serve.pid` (`pid start-time`), written by `factory serve`, removed on clean exit; a pid whose start time changed is not the serve |
| Serve unit drift check | tick alert | skipped; `factory res` prints a one-line note |
| Memory | `/proc/meminfo` MemAvailable | `vm_stat` (free+inactive+speculative pages x page size); `kern.memorystatus_vm_pressure_level` != 1 closes the gate |
| tmp gate | `/proc/mounts` fs type | `stat -f %T /private/tmp`; a non-tmpfs `/tmp` needs 10240 MB free (same floor as `go/internal/gate`) |
| Top memory in `factory res` | `ps -eo rss,args --sort=-rss` | `ps -axo rss=,command= -m` |
| Process identity | `/proc/<pid>/stat, comm, cmdline` | one `LC_ALL=C ps -axo pid=,ppid=,uid=,lstart=,command=` per call; start identity is `lstart` |
| User namespace link | `/proc/<pid>/ns/user` | none ("not in a namespace"); escape detection by uid comes in M6b |

Unknown means closed: a failing `vm_stat`, `sysctl` or `stat` closes the gate; an unreadable `ps` keeps the pid (never reaped).
The serve and orphan reapers do nothing on macOS (cwd, environ and open fds are not readable without `/proc`), and
`_proc_holds` returns "unknown" so no temp dir is deleted while it might be in use.

`factory tick --deadline <duration>` (`240s`, `4m`, `1h`, bare seconds): once the deadline has passed, the tick starts no
further phase, records event `tick-deadline` and exits 0. The launchd job passes `--deadline 240s`; without the flag nothing
changes. Tests: `tests/test_darwin_branches.py`.
