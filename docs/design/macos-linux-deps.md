# Linux-only dependency inventory for the macOS port (G-049 SC1)

Status: inventory, no code change. Measured on `origin/main` at `94bd93d9e` plus the merge of the T-1535 branch (docs only).
Owner: G-049 (Hivemill runs on macOS). The sandbox *choice* is not made here; it belongs to the ADR (PR 699, `docs/adr/0003-*`, section 3.x). This file is the full, reproducible list of what that port has to touch, with the macOS replacement for each row or an explicit "no replacement".

## How to read this

- A row is one file plus one mechanism. `lines` are the first matching lines of the grep in the "Reproduce" column (at most 6, then `…`); the full list is the command's output. A row's file:line pairs are exact on this commit.
- Every row ends with a macOS replacement, or **no replacement** and a note.
- Evidence tags: **[build]** = proven by `GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -gcflags=-e ./...` (see "Go compile check"); **[grep]** = found by the grep only; **[macOS ok]** = the call exists on macOS, listed so nobody re-checks it. Behaviour on a real Mac was **not** run here (this task is read-only on a Linux box); "verify on Mac" marks every claim that must be checked in the SC2 owner window.
- Counts per area are at the end.

## Reproduce

Run from the repo root on `origin/main`. Pathspecs:

```sh
# CODE = product code: everything except docs, test data, tests, web, ci and workflows
CODE=(. ':!docs' ':!**/testdata/**' ':!tests' ':!**/*_test.go' ':!web' ':!ci' ':!.github')
# TESTS = Python and Go tests;  CIX = ci scripts and workflows;  DOCS = the setup docs
TESTS=(tests '**/*_test.go' ':!**/testdata/**'); CIX=(ci .github); DOCS=(docs/SETUP.md README.md CONTRIBUTING.md)
```

| id | command (append `-- "${CODE[@]}"`, `"${TESTS[@]}"`, `"${CIX[@]}"` or `"${DOCS[@]}"`) |
|---|---|
| G1 boot | `git grep -nIE 'systemctl\|systemd-run\|journalctl\|loginctl\|systemd-analyze\|\.service\b\|\.timer\b\|\.slice\b\|WantedBy\|linger'` |
| G2 sandbox | `git grep -nIE 'unshare\|bwrap\|CLONE_NEW\|nsenter\|newuidmap\|newgidmap\|MS_BIND\|pivot_root\|setns\|subuid\|PR_SET_NO_NEW_PRIVS\|seccomp\|prctl\|setpriv\|capsh\|O_PATH'` |
| G3 binaries | `git grep -nIE 'herdr-linux\|hc-linux\|linux-amd64\|linux_amd64\|x86_64-linux\|aarch64-linux\|GOOS=linux\|runtime\.GOOS\|platform\.system\|sys\.platform\|uname -s\|uname -m\|\.AppImage\|ELF'` |
| G4 gates | `git grep -nIE 'tmpfs\|/dev/shm\|statvfs\|/proc/mounts\|/proc/meminfo\|/proc/loadavg\|/proc/uptime\|MemAvailable\|SwapTotal\|os\.getloadavg\|loadavg'` |
| G5 procfs/ps | `git grep -nIE 'pgrep\|pkill\|"ps"\|/proc/[^ ]*(stat\|comm\|status\|cmdline\|cgroup\|environ\|fd)\|_proc_start\|starttime\|PR_SET_PDEATHSIG\|setsid\|killpg\|\bfuser\b\|\blsof\b'` |
| G6 gnu tools | `git grep -nIE 'stat -c\|date -d\|readlink -f\|realpath -\|sed -i\|sed -r\|grep -P\|-printf\|xargs -r\|sort -V\|--sort=\|sha256sum\|sha1sum\|md5sum\|/bin/true\|timeout -k\|numfmt\|truncate\|nproc\|free -\|xdg-open'` |
| G7 paths | `git grep -nIE 'XDG_[A-Z_]+\|~/\.config\|\.config/\|\.local/share\|\.local/bin\|/run/user\|\.cache/\|/etc/(os-release\|passwd\|resolv\|hosts\|ssl\|ca-)\|/sys/fs\|/dev/(null\|tty\|pts\|stdin)\|/lib64'` |
| G8 install/pkg | `git grep -nIE '\bapt(-get)?\b\|\bdpkg\b\|\bsudo\b\|/etc/os-release\|debian\|ubuntu\|\bflock\b\|fcntl\.flock\|LOCK_EX\|syscall\.Flock\|inotify\|fsnotify\|userns\|apparmor\|setcap\|sysctl'` |
| G9 hardcoded tmp | `git grep -nE '"/tmp\|/tmp/'` (pathspec: `bin go scout roles/hooks roles/settings plugins systemd ':!**/*_test.go' ':!**/testdata/**'`) |
| G10 hardcoded PATH | `git grep -nE '"/usr/bin\|"/bin/(sh\|bash\|true\|env)'` (pathspec: `bin go scout ':!**/*_test.go' ':!**/testdata/**'`) |
| G11 Go syscalls | `git grep -nE 'syscall\.[A-Z][A-Za-z_]+\|golang.org/x/sys\|unix\.[A-Z]' -- 'go/**.go' ':!**/*_test.go'` |
| G12 go build | `cd go && GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -gcflags=-e ./...` |
| G13 shells | `git grep -nIE 'mapfile\|readarray\|declare -A\|sha256sum' -- systemd plugins/hivemill/scripts/install roles/hooks/worker-guard` |

G1-G8 are broad on purpose: they over-match (for example `.config/` hits in test fixtures, `XDG_` in guard rules). Rows keep only real dependencies; hits that are text in a test fixture or a deny-list are in the "noise" notes of each area, not rows. `ci/guard_corpus.json` is a data corpus (244 `XDG`/`.config` hits, 83 GNU-flag hits): data, not a dependency, no row.

### Go compile check (G12) - the hard evidence

Output on `origin/main` (Linux/amd64 host, darwin/arm64 target). These are the only packages that do not compile; every importer of them (the `cli`, `spawn` and `cmd/*` binaries) is shadowed behind them, so the real count of packages that cannot link for darwin is larger than three:

```
internal/sandbox/setup.go:26:22: undefined: syscall.MS_BIND          (also :27 MS_REMOUNT, :28 MS_RDONLY)
internal/sandbox/setup.go:36:20: undefined: syscall.Mount
internal/sandbox/setup.go:48:36: undefined: syscall.AF_NETLINK       (also SOCK_CLOEXEC, NETLINK_ROUTE, :56 RTM_NEWLINK, :57 NLM_F_*, :62 SockaddrNetlink, :70 NLMSG_ERROR)
internal/sandbox/launch.go:84:43: unknown field Cloneflags in struct literal of type syscall.SysProcAttr
internal/sandbox/launch.go:84:63: undefined: syscall.CLONE_NEWUSER
internal/sandbox/workerbox.go:516:38: st.Mtim undefined (type syscall.Stat_t ...)   (also :547)
internal/gate/proc.go:164:41: v.Frsize undefined (type syscall.Statfs_t ...)
```

No file in `go/` carries a `//go:build` line (`git grep -nE '^//go:build' -- go` is empty), so the port needs build-tag splits (`*_linux.go` / `*_darwin.go`), not just edits.

## Area 1 - boot (systemd, linger, transient units, cgroups)

macOS has no systemd. Boot = launchd. `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/<label>.plist`. A LaunchAgent starts at *login* of that user, not at machine boot; "starts after reboot with nobody logged in" (what `--linger` gives on Linux) needs either auto-login on the Mac mini or a LaunchDaemon with `UserName`. Verify on Mac; the owner decides.

| id | file:lines | uses | macOS replacement |
|---|---|---|---|
| B1 | `systemd/herdr-server.service`: 6,7,12 | user unit, `WantedBy=default.target`, `%h` paths | `~/Library/LaunchAgents/dev.hivemill.herdr-server.plist`: `RunAtLoad`, `KeepAlive`, `ProgramArguments` absolute (no `%h`; the installer writes the real home). |
| B2 | `systemd/factory-up.service`: 3,4,9-11,15 | `Type=oneshot`, `RemainAfterExit`, `Wants/After=herdr-server`, `ExecStartPre` bash loop `seq 1 60` polling `herdr workspace list` | plist with `RunAtLoad` and a wrapper script that does the same wait loop (`seq` exists on macOS). launchd has no `After=`; the loop is the ordering. `RemainAfterExit` has no analogue: plist exits 0, set `KeepAlive` false. |
| B3 | `systemd/factory-tick.service`: 3,6,7; `systemd/factory-tick.timer`: 8 | oneshot service + timer (the once-a-minute tick, SC3) | one plist with `StartInterval` 60 (or `StartCalendarInterval`). launchd skips a start while the previous run is alive, which matches `flock` single-flight in the tick (bin/factory_cli.py:15714). |
| B4 | `systemd/factory-serve.service`: 7,30 | opt-in web UI unit | plist, `RunAtLoad`+`KeepAlive`; only installed with `--with-serve`. |
| B5 | `systemd/factory-tests.service`: 9,10,14; `systemd/factory-tests.slice` | delegated cgroup parent for `factory test`: `Slice=`, `Delegate=cpu memory pids`, `ExecStart=/bin/true` | **no replacement** (no cgroups). `test_cgroup` defaults to `0` (bin/factory_cli.py:217), so the feature is off by default; on macOS refuse `test_cgroup=1` with a clear message and fall back to `ulimit -v`/RSS-watchdog. `/bin/true` is `/usr/bin/true` on macOS: moot once the unit is dropped. |
| B6 | `systemd/install.sh`: 3,5,21-26,37,68-70,79,85,91-93 | symlinks units into `${XDG_CONFIG_HOME:-~/.config}/systemd/user`, `systemctl --user daemon-reload/enable`, `loginctl show-user -p Linger` | `launchd/install.sh` writing plists to `~/Library/LaunchAgents` and `launchctl bootstrap`; linger check becomes "auto-login or LaunchDaemon" advice. macOS ships bash 3.2: the script uses plain arrays only (no `mapfile`/`declare -A`: G13 clean), so `#!/usr/bin/env bash` works; keep it that way. `date`/`readlink` calls there use no GNU flags. |
| B7 | `bin/factory_cli.py`: 6834,14484,14487 | `systemd-run --user --collect --unit factory-mech/factory-gate-<id>` launches detached mech-merge and gate jobs | `launchctl submit -l <label> -- cmd` or a detached `subprocess.Popen(start_new_session=True)` with the existing `flock` lock (bin/factory_cli.py:6806). `--collect` (unit auto-removed) = `launchctl remove` after exit. |
| B8 | `bin/factory_cli.py`: 8234,8239,8247,8254,8257,8474 | `systemctl --user show factory-serve.service -p MainPID`, then scanning `/proc/*/cgroup` for the unit's processes, to protect serve from the orphan reap | `launchctl print gui/$UID/<label>` (PID field) or a pidfile written by the serve wrapper. The `/proc/*/cgroup` scan has **no replacement**; the pid from launchd is enough because launchd keeps children in the job. |
| B9 | `bin/factory_cli.py`: 9249,9254,9256,9261,9278 | serve-unit drift check: compares `~/.config/systemd/user/factory-serve.service` with `systemd/factory-serve.service` | compare `~/Library/LaunchAgents/<label>.plist` with the repo plist; same logic, different path and file name. |
| B10 | `bin/factory_cli.py`: 2768,2770 | comments tie the build timeouts to `factory-up.service` / `factory-tick.service` `TimeoutStartSec` | launchd has `TimeoutStartSec` nowhere; `ExitTimeOut` only bounds stop. Keep the timeouts in code; no replacement for the unit-side limit. |
| B11 | `scout/testcg.py`: 4,14,23 | writes cgroupfs `/sys/fs/cgroup/user.slice/user-<uid>.slice/user@<uid>.service/factory-tests.slice/...` for per-run caps | **no replacement**; see B5. `factory test` runs uncapped or under the watchdog. |
| B12 | `bin/factory_cli.py`: 217,218,14286-14339 | `test_cgroup`, `test_cgroup_parent` config and the `into_cgroup` preexec that moves the child into the cgroup | same as B5/B11: gate on `sys.platform`, default `0`, refuse `1` on darwin. |
| B13 | `go/internal/install/install.go`: 242,710,712,715,716,893; `go/internal/install/args.go`: 39,63,101 | installer runs `systemctl --user show-environment`, `loginctl enable-linger`, `systemctl --user start herdr-server.service factory-tick.timer factory-serve.service factory-up.service`; flags `--no-systemd`, `--linger` | `launchctl print gui/$UID` probe replaces `show-environment`; `--linger` becomes `--boot-without-login` (LaunchDaemon) or is refused; `--no-systemd` renamed `--no-services` (keep the old name as an alias). |
| B14 | `plugins/hivemill/scripts/install.py`: 6,326,589-594,692,900 | Python twin of B13 (`systemctl`, `loginctl`, `--linger`) | same changes as B13; the Go and Python installers must change together (they are differential-tested: `go/cmd/hivemill-install/differential_test.go`). |
| B15 | `go/internal/guard/commands.go`: 846,847,1333-1360; `go/internal/guard/analyzer.go`: 42,589-595; `go/internal/guard/errors.go`: 19,71; `go/internal/guard/frontend.go`: 38-40; `roles/hooks/worker-guard.py`: 25,890,1087,1730,2271,2272,… | worker guard deny rules for `systemctl`, `systemd-run`, `loginctl`, `unshare` | add `launchctl` (`load/unload/bootstrap/bootout/submit/kickstart`) and `sandbox-exec` to the deny rules; keep the Linux names denied. Every new guard needs the C3 accept/reject pair. |
| B16 | `roles/_common.md`: 68; `plugins/hivemill/skills/setup/SKILL.md`: 30 | role text and setup skill name systemd units | text mentions `launchctl` as well. |
| B17 | `plugins/hivemill/scripts/ios-release/asc.mjs`: 242,309; `plugins/hivemill/scripts/ios-release/what-to-test.mjs`: 14 | `.service` matches are App Store Connect field names, not systemd | none (false positive, listed so the grep is auditable). |

False positives in G1 (listed so the grep is auditable): `plugins/hivemill/herdr-app/mods/hivemill/main.js` (46,63,100,249,306,375,…) and `data.js` (58,98,301) match `.timer.` / `.slice(` in JavaScript, not units.

Noise in G1 on tests: `tests/test_worker_guard*.py`, `go/internal/guard/commands_test.go` (deny-rule fixtures), `tests/test_serve_unit_drift.py`, `tests/test_testcg.py`: see Area 7.

## Area 2 - gates (resources, processes, tmp)

The tick's resource gates read Linux `/proc` and assume `/tmp` is a size-limited tmpfs.

| id | file:lines | uses | macOS replacement |
|---|---|---|---|
| R1 | `bin/factory_cli.py`: 2409,2410,14579 | `/proc/meminfo` MemAvailable, SwapTotal/SwapFree for the min-mem gate and `factory res` | `vm_stat` (`Pages free + inactive + speculative` x page size) for available, `sysctl -n hw.memsize` for total, `sysctl -n vm.swapusage` for swap. Python: `subprocess` of those; same value semantics as MB. |
| R2 | `go/internal/cli/cmd_res.go`: 175,179,418; `go/internal/gate/proc.go`: 73,74,76 (`MemAvailableMB`), 91,96 (`SwapUsedMB`) | Go twin of R1: parses meminfo text | Go has no `golang.org/x/sys` (stdlib-only, `docs/go-toolchain.md`): `syscall.Sysctl("hw.memsize")` works on darwin, `vm_stat`/`sysctl vm.swapusage` via exec for the rest. Put it behind `meminfo_linux.go` / `meminfo_darwin.go`. |
| R3 | `go/internal/cli/cmd_res.go`: 216; `go/internal/gate/proc.go`: 109 (`Load1`) | `/proc/loadavg` first field -> `gate_max_load_ratio x CPUs` | `sysctl -n vm.loadavg` (`{ 1.2 1.1 1.0 }`, strip braces) or Python `os.getloadavg()` [macOS ok]. Go: exec `sysctl`. |
| R4 | `plugins/hivemill/herdr-app/mods/hivemill/data.js`: 281-295 | probe snippet: Linux branch (290-292) reads `/proc/meminfo`, `/proc/loadavg`, `/proc/uptime` | **already has a macOS branch** (283-288: `sysctl -n hw.memsize`, `vm_stat` free+inactive+speculative x `hw.pagesize`, `sysctl -n vm.loadavg`, `vm.swapusage`, `kern.boottime`). Reuse these exact commands for R1-R3; verify on Mac. |
| R5 | `bin/factory_cli.py`: 2426,2437,2447 | tmp gate: `os.statvfs(/tmp)` plus `/proc/mounts` to report the fstype (the "tmpfs" assumption in the message and in `tmp_max_pct`) | `os.statvfs` [macOS ok]; fstype from `os.statvfs`-less `mount -t` parse or Go `Statfs_t.Fstypename`. `/tmp` on macOS is `/private/tmp` on APFS (the data volume shared with everything), not a tmpfs: `tmp_max_pct` then measures the whole disk, so the gate needs a `tmp_dir` under a dedicated directory or a different threshold. Verify on Mac. |
| R6 | `go/internal/gate/proc.go`: 160-164 (`TmpUsage`, `syscall.Statfs` + `Frsize`) **[build]**; `go/internal/cli/cmd_res.go`: 198,200,1005,1007; `go/internal/gate/gate.go`: 28,94,322,323,328 | Go tmp gate: `Statfs_t.Frsize` does not exist on darwin | use `Bsize` (darwin `Statfs_t` has `Bsize`, `Blocks`, `Bavail`, `Fstypename`), behind `tmp_linux.go`/`tmp_darwin.go`. |
| R7 | `bin/factory_cli.py`: 2426,2644,7649,7650,7652; `go/internal/cli/cmd_src.go`: 41; `go/internal/guard/factory.go`: 91; `roles/hooks/worker-guard.py`: 1787,2937,2989; `roles/settings/cleaner.json.in`: 91 | hardcoded `/tmp`, `/tmp/qa-*`, `/tmp/zz-test-factory-*` sweep roots and guard allow rules | macOS `$TMPDIR` is `/var/folders/<xx>/<hash>/T/` per user, and `/tmp` is a symlink to `/private/tmp`. Resolve with `tempfile.gettempdir()` / `os.TempDir()` everywhere and compare **realpaths** (the guard's `/tmp/` prefix rules must also accept `/private/tmp/`). |
| R8 | `bin/factory_cli.py`: 14636; `go/internal/cli/cmd_res.go`: 36,481 | `ps -eo rss,args --sort=-rss` (top RSS) in `factory res` | `ps -Ao rss,args` piped through a Python/Go sort by RSS; `--sort` is a procps GNU option and macOS `ps` rejects it. |
| R9 | `bin/factory_cli.py`: 2293,8064,8369; `_proc_start` at 2273 | process identity = `/proc/<pid>/stat` field 22 start time (clock ticks), `/proc/<pid>/comm`; used by the orphan reap, the idle-coder reap and the "is this still the same process" check | `ps -o lstart= -p <pid>` (string; compare as an opaque token) and `ps -o comm= -p <pid>`; or `sysctl kern.proc.pid.<pid>`. The unit is different (seconds, not ticks), so persisted `start` values from Linux must not be compared with Mac values: namespaced by platform in the DB row. |
| R10 | `go/internal/sandbox/egress.go`: 64; `scout/egress.py`: 32 | reads `/proc/<pid>/stat` to find a child's parent/start for the egress proxy | same as R9. |
| R11 | `go/internal/skilleval/stopproxy.go`: 22 | `/proc/<pid>/cmdline` check before killing a proxy | `ps -o args= -p <pid>` (macOS `ps` truncates by tty width unless `-ww`). |
| R12 | `scout/nsbox.py`: 640; `scout/gatebox.py`: 121 | `/proc/<pid>/cmdline` / process probes inside the namespace box | **no replacement** while the namespace box itself has none (Area 3); revisit once the sandbox mechanism is chosen. |
| R13 | `bin/factory_cli.py`: 2264,2289,2290 (`_userns_link`) | compares `/proc/<pid>/ns/user` links of self and pid 1 to decide "am I inside a user namespace" | **no replacement**; on macOS this is a constant "no namespace". The question becomes "am I under Seatbelt / as the sandbox user", chosen with the sandbox. |
| R14 | `go/internal/tokens/index.go`: 275 (`os.Readlink("/proc/self/fd/N")`), 257,266,270-272 | resolve the real path of an open fd to reject a swapped file; `Stat_t.Nlink` | `fcntl(fd, F_GETPATH)` (syscall.Syscall on darwin, constant 50); `Nlink` is [macOS ok]. Go build is fine here (`Readlink` just fails at run time) **[grep]**; the failure becomes an error that blocks token use, so this one is a runtime hazard. |
| R15 | `go/internal/skilleval/results.go`: 153; `go/internal/guard/ptrule.go`: 324,443; `go/internal/guard/researcher.go`: 72; `roles/hooks/worker-guard.py`: 5554,6438 | `/proc/self/fd/N` and `/proc/*/environ` named in guard rules and in the result writer (`fdPath`) | `/dev/fd/N` exists on macOS; `/proc/<pid>/environ` does not, but `ps eww`/`ps -E` exposes the environment of same-user processes, so the guard rule becomes "deny `ps` with `e`/`-E`/`eww`" for workers. `fdPath` -> `/dev/fd/N` or `F_GETPATH`. |
| R16 | `bin/factory_cli.py`: 927; `go/internal/herdr/herdr.go`: 30; `go/internal/sandbox/sandbox.go`: 23; `scout/sandbox.py`: 36; `roles/settings/{cleaner,coder,evaluator,judge,pm,qa,researcher,retro,reviewer,scout,staff-engineer}.json.in`: `HERDR_SOCKET_PATH` = `/proc/nonexistent/worker-no-herdr.sock` | "dead socket": a path under a directory that cannot exist, so a worker's herdr calls fail closed | works unchanged on macOS (`/proc` does not exist, so the path stays unreachable) **[macOS ok]**; rename to a neutral `/nonexistent/...` so the intent does not rely on procfs. 14 rows collapsed to one: the same literal, 14 places. |
| R17 | `bin/factory_cli.py`: 2531,2541,2891,5464,5471,6228,6233,6806,14647,14651,15710,15714,15718; `go/internal/cli/cmd_res.go`: 277,294,298; `tests/_slot.py` | `fcntl.flock` / `syscall.Flock`: test slots, merge locks, tick single-flight | BSD `flock(2)` exists on macOS [macOS ok]; semantics match for these (non-blocking exclusive/shared on regular files). Not a port item; listed because it is a classic "Linux-only" suspect. One difference to verify: macOS flock on NFS/SMB volumes. |
| R18 | `bin/factory_cli.py`: 2898 | `/usr/bin/nice -n 10` if present | `/usr/bin/nice` exists on macOS [macOS ok]. |

## Area 3 - sandbox (namespaces, mounts, credentials)

This whole area is Linux kernel namespaces. **No replacement for the mechanism**; the replacement is a design choice (ADR 0003: Seatbelt `sandbox-exec`, separate macOS user, Apple Virtualization/containers). Each row says what it *needs from* the replacement.

| id | file:lines | uses | macOS replacement / requirement |
|---|---|---|---|
| S1 | `go/internal/sandbox/launch.go`: 17,18,25-27,35,37,43,84,129,156 | `exec.LookPath("unshare"/"setpriv")`, `SysProcAttr.Cloneflags: CLONE_NEWUSER` **[build]** | **no replacement** (no clone flags on darwin). The launcher interface needs a darwin implementation that execs `sandbox-exec -f <profile>` or `sudo -u <workeruser>`; split into `launch_linux.go` / `launch_darwin.go`. |
| S2 | `go/internal/sandbox/setup.go`: 20-33,36,48,56,57,62,70,121,192,210,215,240,246,249,304,309,345,447,456,503 | the in-namespace setup: `syscall.Mount` bind/remount-ro, tmpfs over `$HOME`, `/proc/self/fd/<n>` no-follow mounts (the "allow-list home view"), netlink `RTM_NEWLINK` to bring loopback up **[build]** | **no replacement**. Seatbelt gives a path allow-list by `(allow file-read* (subpath ...))` instead of a mount view: a different guarantee (deny, not hide). A separate user gives the home view by file permissions. Whichever is chosen, the SC4 tests (G-010: worker credentials isolated, per-role token, home allow-list) must be re-proved on a Mac. |
| S3 | `go/internal/sandbox/nsbox.go`: 41,42,73,86,89,94,137,155,158,161,170,171,192,194,234,237,243,247,297-305 | builds the `unshare -r -n -m --map-user ... setpriv ...` argv, TmpfsSize, resolv.conf keep | **no replacement** (needs both `unshare` and `setpriv`, util-linux only; neither exists on macOS and Homebrew's `util-linux` does not ship them). Argv builder becomes `sandbox-exec -p/-f` plus profile text, or a `sudo -u` wrapper. |
| S4 | `go/internal/sandbox/gatebox.go`: 19,27,29,82-84,97,98,106-112,146,149,173,248 | gate box: nested userns for gate runs, `FACTORY_GATEBOX`, tmp layout, `unshare` binary pick | **no replacement**; gates that run test code need the same isolation as workers (decision in ADR). |
| S5 | `go/internal/sandbox/run.go`: 34,44,72,76,77,90,118,122,127,129,134,179,215; `go/internal/sandbox/sandbox.go`: 44,45,53,54,67,69,72,74,80,88,105-108 | probe `unshare -r -n -m true` ("userns available"), owner checks via `Stat_t.Uid`, `Setsid` | the userns probe has **no replacement**; replace with a Seatbelt probe (`sandbox-exec -p '(version 1)(allow default)' true`) or user-exists check. `Setsid` and `Stat_t.Uid` [macOS ok]. |
| S6 | `go/internal/sandbox/workerbox.go`: 23,31,49,57,124,146,155,158,190,215-232,303,402-407,488,516,543,547,654,685 | worker home build: copies the allowed tree to tmpfs, reads mtimes via `Stat_t.Mtim` **[build]** | `Stat_t.Mtimespec` on darwin (field name differs); the tmpfs home becomes a directory under the sandbox user's home (macOS has no tmpfs; `hdiutil attach -nomount ram://` RAM disk is possible but needs root). |
| S7 | `go/internal/sandbox/ghcred.go`: 23,61,71,200,206,224,248,276,283,398 | per-role GitHub token files, `Stat_t.Uid` ownership checks, git credential helper; hardcoded `PATH=/usr/bin:/bin` | token handling is POSIX [macOS ok] but the PATH fallback misses `/opt/homebrew/bin` (see Area 4 row P4). The file-mode/ownership checks are the part SC4 tests. |
| S8 | `scout/nsbox.py`: 3,4,33,36,40,42,52,64,65,68,75,78,92,95,98,132,146,151,312,333,392,427,430,436,468,483,488,519,528,564,567,640 (65 hits); `scout/gatebox.py`: 4,22,25,58,63-69,74,82,84,106; `scout/workerbox.py`: 6,7,37-39,52,55,66,106,137,151,164,167; `scout/sandbox.py`: 8,36,70,71,83,85,121,125,126,130,139,143,148,150,187; `scout/testbox.py`: 4 | the Python original of S1-S6 (ctypes `mount`, `unshare`, `MS_BIND`, `/proc/self/fd`, tmpfs); the Go port (G-041) is replacing it | **no replacement**; do **not** port the Python side. Because the Go port is mid-migration, macOS work targets Go only and Python stays Linux-only until deleted. |
| S9 | `scout/skilleval.py`: 226,228,236,248,259,262,…(12), 615,616; `go/internal/skilleval/builder.go`: 70,71,163,167,171,211,219,252,289; `go/internal/skilleval/collect.go`: 206,211,212,216,478,481,486,488,509,515-519,536,568,569; `go/internal/skilleval/ops.go`: 39,271,272,281,376; `go/internal/skilleval/trial.go`: 86; `go/internal/skilleval/results.go`: 153,158-174 | skill-eval trial sandbox (nsbox over a trial home), `Stat_t`, `/proc/self/fd`, hardcoded PATH | **no replacement** until the sandbox is chosen; skilleval is not on the owner's macOS critical path, so gate it with `unsupported on darwin` and skip its tests. |
| S10 | `go/internal/scout/risk.go`: 85,87; `scout/risk.py`: 88,90,103 | risk heuristics that name `unshare`/`nsenter`/`apt` as dangerous in third-party skills | add macOS names (`sandbox-exec`, `launchctl`, `osascript`, `brew`, `defaults write`, `sudo`) to the pattern list; the Linux entries stay. |
| S11 | `go/internal/guard/frontend.go`: 32,37-41,55,64,65,99,100; `go/internal/guard/commands.go`: 741,742,915,1303; `roles/hooks/worker-guard.py`: 888-890,926,5115 | command classes the guard knows (`unshare`, `nsenter`, `ps`, `apt`), ELF magic check `\x7fELF` for "worker pushes a binary" | add Mach-O magics (`\xcf\xfa\xed\xfe`, `\xca\xfe\xba\xbe`) next to ELF so the no-push check still fires; add macOS command classes. |
| S12 | `bin/factory_cli.py`: 222,9103,9106,10291,10293,14311-14314,19379,19380,19434; `bin/flags.json`: 935,938; `go/internal/cli/defaults_gen.go`: 80; `go/internal/cli/cmd_skilleval.go`: 100 | config/flag plumbing: `sandbox_required` refusal reason `userns`, `NSBOX_TEST_UNSHARE` seam | the refusal reason set gets a `platform` reason; message must say what to do next (C7). |
| S13 | `go/internal/sandbox/util.go`: 5; `go/internal/sandbox/setup.go`: 21-33 | design comment: "bind from `/proc/self/fd` survives the tmpfs" (TOCTOU defence) | **no replacement** of the technique: macOS has no bind mount; Seatbelt resolves paths at open time, so symlink-swap defence moves to the profile (`(subpath)` on a realpath'd directory, plus `no-follow` checks in the helper). |

## Area 4 - terminal backend and binaries (herdr, hc, PATH)

`hc` (the herdr client name some docs use) has **no** separate Linux artefact in this repo: `git grep -nE 'hc-linux'` is empty. Only `herdr-linux-{x86_64,aarch64}` is pinned.

| id | file:lines | uses | macOS replacement |
|---|---|---|---|
| P1 | `go/internal/install/install.go`: 42,43; `plugins/hivemill/scripts/install.py`: 39,41; `ci/freshbox_test.py`: 39,41; `docs/SETUP.md`: 32,38 | pinned herdr release `herdr-linux-x86_64` / `-aarch64` with size and sha256 | a `herdr` macOS arm64 asset: check the upstream release list (`herdrdev/herdr`, tag v0.9.5) for a darwin asset; if there is none, **no replacement** and herdr must be built from source on the Mac. Pin its sha256 the same way. Not verified here (no network use in this task). |
| P2 | `go/internal/install/install.go`: 46,438; `plugins/hivemill/scripts/install.py`: 48 | pinned `gh_<ver>_linux_amd64.tar.gz`; `linuxOnly()` refuses the pinned download off Linux | `gh_<ver>_macOS_arm64.zip` from the same release, or `brew install gh`. |
| P3 | `go/internal/install/host.go`: 123,129; `plugins/hivemill/scripts/install`: 15-19; `go/internal/install/testdata/install-shim`: 15-19; `.github/workflows/release.yml`: 31,38 | OS/arch detect: Linux amd64/arm64 and **Darwin arm64 already supported** by the shim and the release matrix | done for the installer binary (`hivemill-install-darwin-arm64`); Intel Macs (`Darwin-x86_64`) are refused (`die`): add if the owner's Macs need it. |
| P4 | `bin/factory_cli.py`: 1465,2831,5250,12205,19399; `go/internal/cli/cmd_src.go`: 44; `go/internal/factorytest/env.go`: 165; `go/internal/sandbox/ghcred.go`: 224,398; `go/internal/skilleval/builder.go`: 51; `go/internal/skilleval/collect.go`: 536; `go/internal/skilleval/ops.go`: 281; `scout/ghcred.py`: 135; `scout/sandbox.py`: 83,93; `scout/skilleval.py`: 235 | hardcoded `PATH=/usr/bin:/bin` (+ `~/.local/bin`, `/usr/local/bin`) for child processes | on macOS git/gh/go/node from Homebrew live in `/opt/homebrew/bin` (Apple Silicon), which is not on that PATH, so child `git`/`gh` calls resolve to the Xcode CLT stubs or fail. Add `/opt/homebrew/bin:/opt/homebrew/sbin` (and `/usr/local/bin`) when `sys.platform == "darwin"`. 15 files, one fix: a shared `defaultPath()` helper. |
| P5 | `go/internal/factorytest/stub/herdr.go`: 151 | test stub runs hooks with `/bin/sh -c` | `/bin/sh` exists on macOS (bash 3.2 in posix mode): fine for plain sh. [macOS ok] |
| P6 | `go/internal/herdr/herdr.go`: 29,169; `docs/SETUP.md`: 181 | default herdr socket `~/.config/herdr/herdr.sock`, XDG-style path | herdr's macOS config dir is unknown here; native macOS would be `~/Library/Application Support/herdr/`. **verify on Mac** what herdr itself uses, then make `herdr_socket` default per platform. |
| P7 | `plugins/hivemill/herdr-app/mods/hivemill/main.js`: 20; `plugins/hivemill/herdr-app/mods/hivemill/data.js`: 3,281-295; `go/internal/install/herdrapp.go`: 15,16,140 | the dashboard mod: `open` vs `xdg-open` (main.js:20 already switches on `platform === "macos"`), the probe (R4), install under `~/.config/herdr-app/mods/hivemill` | **already macOS-aware** (herdrapp.go:15 says "macOS or Linux"); `df -Pk /` at data.js:295 is POSIX [macOS ok]. Reference for P6: the Herdr app itself uses `~/.config/` on both OSes. |
| P8 | `go/internal/agentruntime/agentruntime.go`: 76,349; `go/internal/cursorharness/cursorharness.go`: 216,218,246,275,279; `scout/cursor_harness.py`: 152,153,187,197,219,225 | agent runtime launch (`Setsid`), cursor harness config under `~/.config/` | `Setsid` [macOS ok]; Cursor's config dir on macOS is `~/Library/Application Support/Cursor` and `~/.cursor`: verify on Mac. |
| P9 | `roles/hooks/worker-guard`: 13,27,44,79,83,85,87,92,… | the POSIX `sh` wrapper of the guard: `sha256sum` (missing on stock macOS), `date -u +%Y-%m-%dT%H:%M:%SZ` (fine), `mktemp` (fine) | `shasum -a 256` fallback exactly as `plugins/hivemill/scripts/install:22-24` already does. This runs on every tool call, so it is the first thing that breaks on a Mac. |
| P10 | `go/internal/guard/nopush.go`: 123; `go/internal/guard/prot.go`: 940 | guard rule treats only `/usr/bin/git` (and `/usr/bin/`,`/bin/` prefixes) as git/system binaries | add `/opt/homebrew/bin/git`, `/usr/local/bin/git`, `/Library/Developer/CommandLineTools/usr/bin/git`; accept+reject pair per C3. |

## Area 5 - install (docs/SETUP.md and installers)

| id | file:lines | uses | macOS replacement |
|---|---|---|---|
| I1 | `docs/SETUP.md`: 14 | gh install link `install_linux.md` | `brew install gh` or `install_macos.md`. |
| I2 | `docs/SETUP.md`: 18,76,310-331 | "systemd user session with linger", `--no-systemd`, `--linger`, `systemd/install.sh`, `systemd-analyze --user verify` | launchd section (see B6): `plutil -lint` replaces `systemd-analyze verify`. |
| I3 | `docs/SETUP.md`: 19,21,339,378 | util-linux `unshare`+`setpriv`, Ubuntu 24.04 AppArmor `sysctl kernel.apparmor_restrict_unprivileged_userns` | whole prerequisite row replaced by the chosen macOS sandbox prerequisite; AppArmor note is Linux-only. |
| I4 | `docs/SETUP.md`: 32,33,38,62 | `curl herdr-linux-x86_64`, `sha256sum -c`, aarch64 variant | macOS asset + `shasum -a 256 -c`. |
| I5 | `docs/SETUP.md`: 44,90,116 | "fresh Debian or Ubuntu machine"; installs into `~/.local/bin`; "make sure `~/.local/bin` is on PATH; the systemd units add it themselves" | zsh is the macOS login shell: add to `~/.zprofile`; launchd plists must set `EnvironmentVariables.PATH` themselves (like `Environment=PATH=%h/.local/bin:...` in `systemd/factory-up.service:9`). |
| I6 | `docs/SETUP.md`: 124,181,345-347 | `~/.config/factory/live_home`, `worker-oauth-token`, `gh-worker-{ro,rw}-token` | `~/.config` works on macOS (nothing forces `~/Library`), so **keep** it for tokens: a single path scheme on both OSes is simpler and the guard rules (R7-style prefix rules) already know it. XDG env (`XDG_CONFIG_HOME`) is honoured only if set. |
| I7 | `docs/SETUP.md`: 169,171 | `/tmp` sweep description | see R5/R7. |
| I8 | `docs/SETUP.md`: 308 | `runuser -u <user> -- env -i HOME=... XDG_RUNTIME_DIR=...` for the test user | **no replacement** (`runuser` is util-linux). macOS: `sudo -u <user> -H env -i HOME=...`; `XDG_RUNTIME_DIR` does not exist on macOS, drop it. |
| I9 | `docs/SETUP.md`: 370 | `systemctl --user start herdr-server` in troubleshooting | `launchctl kickstart -k gui/$UID/<label>`. |
| I10 | `go/internal/install/install.go`: 29; `plugins/hivemill/scripts/install.py`: 59; `CONTRIBUTING.md`: 35 | `sudo apt-get install -y git python3` hint | `xcode-select --install` (git) and `brew install python@3` hint; Python 3 ships only as an Xcode CLT stub on macOS. |
| I11 | `go/internal/install/host.go`: 137-161; `plugins/hivemill/scripts/install.py`: 118,122 | installer RAM check reads `/proc/meminfo` MemTotal | `sysctl -n hw.memsize` (bytes). |
| I12 | `go/internal/install/install.go`: 419,439,588; `plugins/hivemill/scripts/install.py`: 12,436,527,722,723,766; `plugins/hivemill/scripts/install`: 22,23,25,36 | `~/.local/bin`, `~/.config/...` targets; `sha256sum` or `shasum` | `~/.local/bin` is fine (not on macOS PATH by default: I5); the `shasum` fallback already exists. |
| I13 | `go/internal/install/host.go`: 256,463 | `syscall.Setsid` for detached start | [macOS ok]. |
| I14 | `plugins/hivemill/skills/setup/SKILL.md`: 24,30 | setup skill text names `apt` and `systemctl` | add macOS branches to the skill text. |
| I15 | `SECURITY.md`: 45; `CHANGELOG.md`: 29,315 | prose names `~/.config`/unshare | text only; update when macOS ships. |

## Area 6 - CI

CI is GitHub-hosted Linux; nothing there blocks macOS *use*, but SC2/SC5 need a macOS job or a Mac window.

| id | file:lines | uses | macOS replacement |
|---|---|---|---|
| C1 | `.github/workflows/tests.yml`: 29,70,98,156,219,255,274,356,395,452,541,559,653 | `runs-on: ubuntu-latest` for every job | add a `macos-14` (arm64) job running the quick Go + Python subset; `runs-on: macos-14` hosted runners exist (no sandbox/unshare inside). |
| C2 | `.github/workflows/tests.yml`: 134,237,288,370,409,517,620 | `sudo apt-get install -y zsh` | zsh is preinstalled on macOS runners: skip the step on darwin. |
| C3 | `.github/workflows/install.yml`: 27,41,42,49,56,62,63 | builds `hivemill-install-linux-amd64`, `sha256sum`, container matrix `debian:12`, `ubuntu:24.04`, `apt-get` | macOS job builds `darwin-arm64` and uses `shasum -a 256`; containers are Linux-only: **no replacement** for the container fresh-install test, the Mac fresh install is the SC5 manual window. |
| C4 | `.github/workflows/release.yml`: 13,31,38,41 | cross-builds `linux/amd64 linux/arm64 darwin/arm64` already; `sha256sum` on the runner | already cross-compiles darwin **only for `hivemill-install`**; `go build ./...` for darwin fails (G12), so any release that ships the factory CLI for darwin needs Area 3 first. |
| C5 | `ci/install_container_test.py`: 32,33,96,117,118,142,160,224 | fresh-install test in a Docker container; reads `/proc/net/tcp` for listeners | **no replacement** (no Linux containers semantic, no `/proc/net/tcp`); Mac: `lsof -nP -iTCP:<port> -sTCP:LISTEN` or `netstat -an`. |
| C6 | `ci/freshbox_test.py`: 4,39,41,129,142,263,313,318,359,364,448,449,562,566,614-616 | "fresh box" test: pinned herdr Linux asset, `/proc/<pid>/comm|status` scans, systemd checks | needs a darwin variant (P1) and `ps`-based scans (R9). |
| C7 | `ci/demo_video.py`: 40,105,115,116,119,122,124; `ci/demo_record.py`: 22,91 | demo recorder: `/proc/meminfo` MemAvailable gate (115), refuses `/tmp` and `/dev/shm` as "RAM-backed tmpfs" (105,124; wrong on macOS where /tmp is APFS), `apt install ffmpeg` hint (40) | R1 for the meminfo read; drop the tmpfs refusal on darwin; `brew install ffmpeg` hint. Demo tooling, not on the macOS critical path. |
| C8 | `ci/profile_baseline.py`: 31,110,127,131,133 | tick profile baseline reads `journalctl --user -u factory-tick.service -o short-unix` | **no replacement** (launchd logs to the file named in the plist `StandardOutPath`): read the tick log file instead; skip on darwin until B3 exists. (`:60` `os.getloadavg` is portable.) |
| C9 | `ci/screenshots.py`: 78,191,200,203; `ci/affected.py`: 304 | `FONT_HELP` suggests `sudo apt install fontconfig`; `killpg` process-group cleanup | hint text becomes `brew install fontconfig`; `os.killpg` [macOS ok]. |
| C10 | `ci/freshbox_test.py` (see C6): 562,566 `sudo` hint; `ci/guard_golden.py`: 53,68-71,89; `ci/fresh_export.py`: 52; `ci/state_load.py`: 20; `ci/demo_tour.py`: 36 | `<home>/.config/factory/live_home` and `XDG_CACHE_HOME`/`~/.cache` defaults in comments and fixtures | `~/.config` is kept (I6); `~/.cache` exists on macOS but `~/Library/Caches` is the native one: harmless. |

## Area 7 - tests (harness and test files)

Tests do not ship, but SC4 says "tests prove each guarantee on a Mac", so every Linux-only test must either skip on darwin with a reason or gain a darwin twin. Counted per file, not per hit.

| id | file:lines | uses | macOS replacement |
|---|---|---|---|
| T1 | `tests/envcheck.py`: 1,11,14; `tests/harness.py`: 276,383,420,421,467,471,659; `tests/_slot.py`: 1,17,21,71,81,90,124,129 | harness: `/proc` read, `XDG_*` isolation, `stat`/GNU flag, `flock` slots | harness sets `XDG_*`; those are inert on macOS and harmless. `/proc` use at `harness.py:276` and the GNU flag at `:467,:471` need darwin branches. |
| T2 | `go/internal/sandbox/{gatebox,golden,nsbox,plan,workerbox,ghcred}_test.go`; `tests/test_{gatebox,gate_sandbox,nsbox_allow,nsbox_resolv_keep,workerbox,workerbox_clone,testbox,sandbox_spawn,sandbox_required,researcher_sandbox,worker_gate}.py` | sandbox tests: `unshare`, `/proc`, tmpfs, golden argv containing `/usr/bin/unshare`, `/usr/bin/setpriv` (`go/internal/sandbox/testdata/argv_golden.json`) | skip on darwin (`runtime.GOOS`/`sys.platform`), plus new darwin tests as part of SC4. The golden JSON is Linux argv: keep as the Linux golden, add a darwin golden. 30 files. |
| T3 | `tests/test_skilleval_{nsbox,collect,sandbox,startlog,startlog_safety,tick,guard_copy,vendor,control}.py`; `go/internal/skilleval/skilleval_test.go` | skill-eval trial sandbox tests | skip on darwin (S9). 10 files. |
| T4 | `go/internal/gate/{gate,fuzz}_test.go`; `go/internal/cli/cmd_res_test.go`; `go/internal/install/host_test.go`; `tests/test_{gate,gate_integration,gate_cursor_pane_limit,gate_cursor_usage,load_gate,judge_load_exempt,lead_load_exempt,tmp_sweep,tmp_leftover_sweep,tmp_watch,profile_baseline,orphan_reap,serve_reap,idle_coder_reap}.py` | gate tests with fake `/proc/meminfo`, `/proc/mounts`, `/proc/<pid>` trees and `FACTORY_TMP_STATVFS` | the fakes are injected files, so most run on macOS as is **[grep]** (they never read the real `/proc`); the ones that stat a real tmpfs (`tests/test_tmp_watch.py:33,34,112,126,138`) need `/private/tmp` handling (R7). Verify on Mac. 16 files. |
| T5 | `tests/test_{testcg,serve_unit_drift,ci_tier,gate_integration,hivemill_install,install,freshbox_test,worker_guard_units}.py`; `go/internal/install/{install,fakehost,fuzz,shim}_test.go`; `go/cmd/hivemill-install/differential_test.go` | units, `systemctl`, `loginctl`, `apt` in fakes and fixtures | fakes pass on macOS if the product code is OS-switched through the same seam (`FakeHost`); add darwin cases beside each Linux one. 14 files. |
| T6 | `go/internal/guard/*_test.go` (commands, factory, fuzz, lookaround, nopush, persist, prot, prot_parent, zshpath_envunset, archive_rule, frontend, hc_parity); `tests/test_worker_guard*.py`; `tests/test_priv_guard.py`; `tests/test_guard_*.py` | guard rule tests that assert `/proc/...`, `/usr/bin/...`, `~/.config` paths and `sudo`/`systemctl` strings | pure string rules, so they run on macOS **[macOS ok]**; add the darwin rules from B15, P10, S11 with their accept/reject pairs (C3). 60+ files, no per-file rows: same data. |
| T7 | `tests/test_live_home.py`: 2,153,158,196,294-326; `tests/test_cursor_*`; `tests/test_herdr_socket.py`: 18-35; `tests/test_researcher_*`; `tests/test_workerbox_clone.py` | `XDG_CONFIG_HOME`, `~/.config` path assertions | portable if the product keeps `~/.config` (I6); otherwise update the expected paths. |
| T8 | `tests/test_screenshots.py`: 103,145,159,472; `tests/test_demo_video.py`: 172,183,220,229; `tests/test_web_*.py`: 144,211,214,511 | browser/video/psutil paths, `/proc/meminfo` fake | skip heavy browser tests on darwin until a Mac CI job exists (C9). |

## Area 8 - coreutils and shell flags (portability)

Product code is mostly clean here, which is why this area is short. The GNU-only items that remain are the rows above (R8 `ps --sort`, P9 `sha256sum`, B5 `/bin/true`). `readlink` in `systemd/install.sh:79` and `date +%Y%m%d%H%M%S` at `:42,85` use no GNU flags. No `sed -i`, `grep -P`, `find -printf`, `stat -c`, `date -d`, `readlink -f`, `xargs -r`, `numfmt`, `xdg-open` appear in product code: `git grep -nIE 'sed -i|grep -P|find .*-printf|stat -c|date -d|readlink -f|xargs -r|numfmt|xdg-open' -- "${CODE[@]}"` returns only `plugins/hivemill/herdr-app/mods/hivemill/main.js` snippets (P7) and the HTML/CSV in `roles/plugins/goals/G-008/ui-ux-pro-max/` (documentation text, not run). Python `os.statvfs`, `os.getloadavg`, `subprocess` are portable.

## Counts per area

Rows are those in the tables above; "files" is the number of distinct files named in them (tests grouped by the lists written in the row).

| area | rows | no replacement (needs design) | works as is on macOS | replacement exists |
|---|---|---|---|---|
| 1 boot | 17 | 5 (B5, B8 cgroup scan, B10, B11, B12) | 1 (B17 false positive) | 11 |
| 2 gates | 18 | 3 (R12, R13, R9 value compatibility is a data issue) | 3 (R16, R17, R18) | 12 |
| 3 sandbox | 13 | 9 (S1-S4, S8, S9, S13 and the probe in S5, S6 home) | 1 (S7 partly) | 3 |
| 4 terminal backend + binaries | 10 | 2 (P1 if no darwin herdr asset, P6 unknown herdr dir) | 3 (P3 done, P5, P7 already macOS-aware) | 5 |
| 5 install | 15 | 2 (I3, I8) | 2 (I6, I13) | 11 |
| 6 CI | 10 | 2 (C3 container test, C5) | 0 | 8 |
| 7 tests | 8 | 2 groups (T2, T3 skip on darwin) | 2 (T4 mostly, T6) | 4 |
| 8 coreutils | prose (4 items counted in rows R8, P9, B5, P7) | 0 | n/a | n/a |
| **total** | **91 rows** | **25** | **12** | **54** |

(Totals recomputed with `grep -c '^| [A-Z]\+[0-9]\+ |' docs/design/macos-linux-deps.md`; the per-column split is by judgement and is the part a reviewer should challenge.)

## What this means for the plan (short)

1. Nothing here needs the Linux sandbox to be *replaced one-for-one*: Area 3 is the only "no replacement" cluster that blocks SC4, and it is exactly the ADR's sandbox decision.
2. The cheapest first slice that makes the CLI build and run on a Mac: build-tag splits for `internal/gate/proc.go` and `internal/sandbox/{launch,setup,workerbox}.go` (G12), `sys.platform` branches for `/proc` reads (R1-R5, R9), PATH default (P4), `shasum` in the guard wrapper (P9). Without P9 and P4 every worker tool call fails or mis-resolves.
3. launchd units (Area 1) are independent of the sandbox choice and can proceed in parallel.
4. Python `scout/` sandbox code is being replaced by Go (G-041): do not port it.
