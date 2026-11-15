# macOS facts, MacBook run of 2026-11-15 (ADR 0003 stage M0)

Source: the owner ran the read-only `macos-facts` tool (T-1565) on the MacBook at 2026-11-15T20:45:15Z, binary
sha256 `b42ce31f…3488`, verified by the owner. Nothing was changed on the Mac and no agent ran anything on it.

**This is a redacted summary, not the raw `facts.md`.** The raw file stays on the owner's Mac. The boss removed
process command lines and ids, and the username, home paths and host name are replaced here (`$HOME`, `<user>`).
Boss relay: T-1575 note, 2026-11-15T21:45:58+01:00 (boss M37862 announced the run). Because the raw command
output is not in the repository, the exact text of `sw_vers`, `vm_stat` and `vm.swapusage` is not recorded here;
the darwin test fixtures that need that text stay synthetic (see the PR).

## Facts (each has an id; ADR 0003 cites them as `[fact M0 Fn]`)

| id | fact |
|---|---|
| F1 | macOS 15.6.1 (build 24G90), arm64 (Apple silicon). 12 CPUs (6 performance, 6 efficiency) |
| F2 | RAM 18 GiB (`hw.memsize` 19327352832). Swap: 6144M total, 5070M used |
| F3 | `memorystatus_vm_pressure_level` was 2 (warn) with normal desktop apps running (Chrome, Spotify the largest by RSS) |
| F4 | Load average 3.7 / 4.0 / 3.7 |
| F5 | `/usr/bin/sandbox-exec` exists. `sandbox-exec -p '(version 1)(allow default)' /usr/bin/true` exits 0 |
| F6 | Inside `sandbox-exec` with `(allow default)`, `/bin/ps` fails with "Operation not permitted" (exit 126): `ps` cannot run inside Seatbelt even with allow default |
| F7 | A nested `sandbox-exec` inside a `sandbox-exec` fails with "sandbox_apply: Operation not permitted" (exit 71): no nested Seatbelt. A sandboxed worker cannot start a sandboxed child (this is the S16-relevant result) |
| F8 | `/tmp` is a symlink to `private/tmp`, on APFS |
| F9 | Permissions: `$HOME` is `drwxr-xr-x+` (755 plus ACL); `~/.claude` is `drwxr-xr-x` (755, world-readable); `~/.ssh` is 700; `~/Library/Keychains` is 711; `/Users/Shared` is 1777 |
| F10 | `lsof` 4.91 (libproc) at `/usr/sbin/lsof` |
| F11 | The Claude login is in the macOS Keychain (item "Claude Code-credentials" exists); there is no `~/.claude/.credentials.json`. `gh` is logged in with keyring storage |
| F12 | Local users with uid >= 500: only the owner (uid 501) |
| F13 | `pmset`: `sleep 1` but "sleep prevented by powerd"; `displaysleep 0`; `disksleep 10` |
| F14 | Tools: python3 3.13.0 at `/usr/local/bin`; git 2.50.1 (Apple); go 1.23.3 darwin/**amd64** at `/usr/local/bin/go` (an Intel build under Rosetta, and older than the 1.27.3 the factory pins); herdr 0.9.0 and hc 0.1.21 in `~/.local/bin` |
| F15 | `launchctl print gui/501` exits 0 |

## Not measured by this run

`vm_stat`, `vm.loadavg`, `vm.swapusage` and `ps` output text; the exact `sw_vers` text; herdr's detection of
`sudo -u <self> claude`; the process-tree behaviour of `sandbox-exec`; `statfs` and APFS free-space behaviour;
whether an installer download is quarantined; the macOS arm64 `gh` asset. These ADR claims stay `[untested]`.
