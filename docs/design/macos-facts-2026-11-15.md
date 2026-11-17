# macOS facts, MacBook run of 2026-11-15 (ADR 0003 stage M0)

## sw_vers (raw)

Command: `sw_vers`  Exit code: 0
ProductName:        macOS
ProductVersion:        15.6.1
BuildVersion:        24G90

## uname_m (raw)

Command: `uname -m`  Exit code: 0
arm64

Source: the owner ran the read-only `macos-facts` tool (T-1565) on the MacBook at 2026-11-15T20:45:15Z, binary
sha256 `b42ce31f…3488`, verified by the owner. Nothing was changed on the Mac and no agent ran anything on it.

Boss relay: T-1575 task note 2026-11-15T21:45:58+01:00; owner window announced in **M37862**. Raw tool output was pasted to
the boss at ~21:47 (boss note 2026-11-16T00:26:59). Usernames, home paths and long browser command lines are redacted
(`<user>`, app name only in `ps_rss_top3`). After the run the owner ran `chmod 700 ~/.claude`.

## Facts (each has an id; ADR 0003 cites them as `[fact M0 Fn]`)

| id | fact |
|---|---|
| F1 | macOS 15.6.1 (build 24G90), arm64 (Apple silicon). 12 CPUs (6 performance, 6 efficiency) |
| F2 | RAM 18 GiB (`hw.memsize` 19327352832). Swap: 6144M total, 5070M used (see raw `vm.swapusage` below) |
| F3 | `memorystatus_vm_pressure_level` was 2 (warn) with normal desktop apps running (Chrome, Spotify the largest by RSS) |
| F4 | Load average 3.71 / 3.99 / 3.66 (`sysctl -n vm.loadavg`) |
| F5 | `/usr/bin/sandbox-exec` exists. `sandbox-exec -p '(version 1)(allow default)' /usr/bin/true` exits 0 |
| F6 | Inside `sandbox-exec` with `(allow default)`, `/bin/ps` fails with "Operation not permitted" (exit 126): `ps` cannot run inside Seatbelt even with allow default |
| F7 | A nested `sandbox-exec` inside a `sandbox-exec` fails with "sandbox_apply: Operation not permitted" (exit 71): no nested Seatbelt. A sandboxed worker cannot start a sandboxed child (S16) |
| F8 | `/tmp` is a symlink to `private/tmp`, on APFS |
| F9 | Permissions: `$HOME` is `drwxr-xr-x+` (755 plus ACL); `~/.claude` was `drwxr-xr-x` (755, world-readable) until the owner `chmod 700` it; `~/.ssh` is 700; `~/Library/Keychains` is 711; `/Users/Shared` is 1777 |
| F10 | `lsof` 4.91 (libproc) at `/usr/sbin/lsof` |
| F11 | The Claude login is in the macOS Keychain (item "Claude Code-credentials" exists); there is no `~/.claude/.credentials.json`. `gh` is logged in with keyring storage |
| F12 | Local users with uid >= 500: only the owner (uid 501) |
| F13 | `pmset`: `sleep 1` but "sleep prevented by powerd"; `displaysleep 0`; `disksleep 10` |
| F14 | Tools: python3 3.13.0 at `/usr/local/bin`; git 2.50.1 (Apple); go 1.23.3 darwin/**amd64** at `/usr/local/bin/go` (Intel build under Rosetta, older than factory-pinned 1.27.3); herdr 0.9.0 and hc 0.1.21 in `~/.local/bin` |
| F15 | `launchctl print gui/501` exits 0 |

## vm_stat (raw)

```
Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                               11361.
Pages active:                            205257.
Pages inactive:                          205358.
Pages speculative:                          980.
Pages throttled:                              0.
Pages wired down:                        192790.
Pages purgeable:                           2805.
"Translation faults":               28822129786.
Pages copy-on-write:                  975803135.
Pages zero filled:                   9821281992.
Pages reactivated:                   5552109517.
Pages purged:                         746384006.
File-backed pages:                       147152.
Anonymous pages:                         264443.
Pages stored in compressor:             1768462.
Pages occupied by compressor:            510048.
Decompressions:                      5356209763.
Compressions:                        5790343887.
Pageins:                              757174494.
Pageouts:                              12863672.
Swapins:                              200745985.
Swapouts:                             208705111.
```

## sysctl samples (raw)

```
sysctl -n vm.loadavg exit 0: { 3.71 3.99 3.66 }
sysctl -n vm.swapusage exit 0: total = 6144.00M  used = 5070.19M  free = 1073.81M  (encrypted)
sysctl -n hw.memsize exit 0: 19327352832
sysctl -n hw.ncpu exit 0: 12
sysctl -n hw.perflevel0.logicalcpu exit 0: 6
sysctl -n hw.perflevel1.logicalcpu exit 0: 6
sysctl -n kern.memorystatus_vm_pressure_level exit 0: 2
```

## sandbox-exec (raw summary)

Nested `sandbox-exec` → exit 71 `sandbox_apply: Operation not permitted` (F7). Inside `(allow default)`, `/bin/ps` → exit 126 "Operation not permitted" (F6).

## Not measured by this run

herdr's detection of `sudo -u <self> claude`; whether an installer download is quarantined; the macOS arm64 `gh` asset; pressure levels 1 and 4 (only level 2 was observed). See ADR 0003 for remaining `[untested]` claims.