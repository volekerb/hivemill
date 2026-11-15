# ADR 0003: macOS support (worker sandbox mechanism and port plan)

## Status

Proposed (2026-11-15). Goal G-049, success criterion SC1. Task T-1536. An Opus reviewer checks it; it becomes
Accepted only with the approving message id recorded here.

Written by the Fable model on the owner's explicit request, relayed as boss decision M36804 (owner, 2026-11-15:
"For Mac I think we need to find the best way to get sandboxes running there and get fable model to design this").
The exception covers this design only. Every build task below uses Opus or Sonnet as usual.

**Nothing in this document was run on a Mac.** It was written on the Linux factory host, where no macOS mechanism
can be tried. Every claim about macOS therefore carries one of two tags:

- `[doc Dn]`: stated by a document that was read during this task. The documents, their URLs and the quoted text
  are in [Evidence](#evidence).
- `[untested]`: untested on a Mac. This is the author's expectation from general knowledge, with no document read
  for it in this task. Each one must be proven or corrected by a build task (section 5) before anything depends on it.

## 1. Context

Hivemill is Linux-only today: systemd user units, Linux herdr binaries, gates that read `/proc`, and a worker
sandbox built on `unshare` user and mount namespaces (`docs/worker-sandbox.md`, ADR 0002). The owner wants it to
run on the MacBook and the Mac mini, with a worker sandbox that works there and is not merely refused.

The guarantees to keep are those of G-010, as the T-1536 spec and `docs/worker-sandbox.md` state them (the G-010
goal record itself is not readable from a worker pane):

| id | guarantee | what it means on macOS |
|---|---|---|
| G1 | The owner's Claude login is out of reach | On macOS the login is in the Keychain, not only in a file `[doc D6]`. Both must be unreachable |
| G2 | The owner's gh token is out of reach | `gh` stores it in the system credential store, with a plain-text fallback `[doc D7]`. Both must be unreachable |
| G3 | The owner's ssh keys and agent are out of reach | `~/.ssh`, the `SSH_AUTH_SOCK` agent socket, and key passphrases kept in the Keychain `[untested]` |
| G4 | The owner's Keychain is out of reach | New on macOS. One default keychain per user `[doc D14]`; access is decided by the `securityd` daemon, not by file permissions alone `[doc D14]` |
| G5 | A per-role GitHub token | Among the roles that can be sandboxed: read-only PAT for reviewer, qa and researcher; read-write PAT for coder (pm, judge and cleaner are not sandboxed, as on Linux). A read-only role must not be able to read the read-write token or write into a read-write role's work tree |
| G6 | An allow-list view of home | The worker sees only listed paths of the owner's home; everything else is denied by default |

What the Linux sandbox also gives, and what macOS must match or list as a limit: HQ-run code (`bin/`,
`roles/hooks`) is read-only for the worker, other tasks' work trees are hidden, the owner's service manager bus and
the multiplexer socket are hidden, the launch is fail closed, and a sandboxed role found outside its sandbox is
detected (`sandbox-escaped`).

Hardware facts that shape the choice: the Mac mini is shared with another team of the owner, so every run there
needs the owner's OK at that time, and a machine-level change (a new account, a sudoers file) is the owner's
action, never an agent's. The MacBook needs a test window from the owner.

## 2. Sandbox mechanisms compared

Four mechanisms:

- **A. Seatbelt** (`sandbox-exec` with an SBPL profile). Same user, kernel-enforced rules on files, Mach services
  and network.
- **B. A separate unprivileged macOS user** for workers (`sysadminctl` or `dscl` to create it, `sudo -u` to enter
  it, ACLs for the shared paths).
- **C. A Linux virtual machine** (Apple Virtualization.framework, through Apple's `container` tool or a tool such
  as Lima), with the existing Linux sandbox inside it.
- **D. App Sandbox** (a code-signing entitlement).

### 2.1 Guarantees

| | A. Seatbelt | B. Separate user | C. Linux VM | D. App Sandbox |
|---|---|---|---|---|
| G1 Claude login | Met only if the profile denies both the credentials file and every Mach service that reaches the Keychain. A deny-default profile does that on paper; one wrong allow rule exposes the login, because the worker is still the owner's user `[untested]` | Met by the operating system: the worker user has its own keychain `[doc D14]` and cannot read the owner's 0600 file `[untested]`. The worker logs in with `CLAUDE_CODE_OAUTH_TOKEN`, which ranks above the stored login `[doc D6]` | Met: the owner's Keychain does not exist inside a Linux guest `[untested]`. Only directories shared into the guest are visible `[untested]` | Met in principle (no Keychain or home access without an entitlement) `[untested]` |
| G2 gh token | As G1: same Keychain, same condition `[untested]`. Worker `gh` uses `GH_TOKEN` `[doc D7]` | Met as G1. Worker `gh` uses `GH_TOKEN` `[doc D7]` | Met as G1 | As G1 `[untested]` |
| G3 ssh keys, agent | Deny `~/.ssh` and the agent socket path in the profile; unset `SSH_AUTH_SOCK` `[untested]` | Met: `~/.ssh` is 0700 and the agent socket belongs to the owner `[untested]` | Met: not shared into the guest `[untested]` | As G1 `[untested]` |
| G4 Keychain | Depends on the profile, as G1. The sandbox-runtime project warns that even its narrower Mach allowance for TLS trust "opens a potential data exfiltration vector through the trustd service" `[doc D5]` | Met by the operating system: a different user, a different keychain `[doc D14]` | Met: no Keychain in the guest `[untested]` | Met in principle `[untested]` |
| G5 per-role token | Works (token in the environment, as on Linux), but a read-only worker runs as the same user as a read-write worker and can read its environment unless the profile denies process inspection `[untested]` | Met with one account per token class: a process cannot read the environment of another user's process `[untested]` | Met: one guest, or one Linux sandbox inside the guest, per worker; the Linux mechanism is already proven | Works through the environment `[untested]` |
| G6 allow-list view | Met by rules: deny the home subtree, allow listed paths `[untested]`. Paths are denied, not hidden: the worker gets a permission error where Linux shows an empty directory `[untested]` | Partly: Unix permissions are deny-by-default only where the owner's files are not readable by others. Anything in the home that is world-readable stays readable unless the home directory itself is closed and the allowed paths are opened by ACL `[untested]` | Met: only the shared directories exist in the guest `[untested]` | Not usable: the view is the app's fixed container plus entitlements, not a per-worker list `[untested]` |

### 2.2 Cost, risk and fit

| | A. Seatbelt | B. Separate user | C. Linux VM | D. App Sandbox |
|---|---|---|---|---|
| Needs root | No `[untested]` | Yes, once: create the accounts and one sudoers file. Creating a user needs administrator rights `[untested]` | `container`: an administrator password at install `[doc D12]`. Lima-style tools: no `[untested]` | No root, but needs an entitlement `[doc D13]`, which is applied by code signing `[untested]` |
| Setup cost | Medium to high: one profile per role, written and kept working by us. The profile language has no Apple reference `[untested]`; the working examples are other projects' profiles `[doc D5]` | Medium: an owner-run script (accounts, sudoers, ACLs) plus the launcher. Standard Unix tools `[untested]` | High: guest image, Claude Code and the Go toolchain for Linux arm64 in the guest, file sharing, and a way to show a guest process in a host pane | Very high: a signed app bundle that hosts the worker; `claude` and every tool it starts would have to run as its helpers `[untested]` |
| Deprecation | `sandbox-exec` "is DEPRECATED" `[doc D1]`, and so is `sandbox_init` `[doc D3]`. The man pages are dated 2017 `[doc D1]`. It is still what Claude Code itself uses on macOS today `[doc D4]`, so it is in wide use, but Apple can remove it | None. User accounts, `sudo` and ACLs are core system features `[untested]` | Documented by Apple as a current framework `[doc D11]`; no deprecation notice was seen `[untested]`. `container` "is designed to run on macOS 26 or later" and needs Apple silicon `[doc D12]` | Documented as current and required for the Mac App Store `[doc D13]` |
| Performance | No measurable start cost expected; files are used in place `[untested]` | A `sudo` call per launch; files are used in place `[untested]` | A guest boot per worker or one long-lived guest; reserved guest RAM; shared-directory file access is slower for git and Go builds `[untested]` | In place `[untested]` |
| claude and cursor CLIs inside | The CLIs run in place. Claude Code's own Bash sandbox also uses Seatbelt `[doc D4]`; whether a sandbox can be applied inside a sandbox is not known, so that feature may have to be off inside `[untested]` | The CLIs run in place as the worker user, with their own config directory. Nothing nested | The Linux builds run in the guest. Work for macOS-only tool chains (Xcode) is impossible in a Linux guest `[untested]` | Not practical |
| herdr and hc | `sandbox-exec` is expected to replace itself with the command, so the pane's foreground process is still `claude` `[untested]` | `sudo` stays as the parent of `claude` `[untested]`. herdr's agent detection must be tested with that process tree. The `hc` backend reads the status the program reports itself (`docs/terminal-backends.md`), which should not depend on the tree `[untested]` | The pane's foreground process is the tool that attaches to the guest, not `claude`. Detection, pid checks and escape detection all need new code `[untested]` | Not practical |
| Failure modes | A profile hole is silent and exposes the owner's own credentials. A macOS update can change which Mach services a tool needs and break the profile `[untested]`. Tools that start their own sandbox (browsers for UI tests) may fail inside `[untested]` | Files shared between two users: ownership, ACL inheritance, git's "dubious ownership" refusal, SQLite WAL files written by two users `[untested]`. The owner's files that are world-readable stay readable unless the preflight catches them | RAM pressure on a shared Mac mini; a second operating system to patch; results cross a VM boundary | Not practical |
| Sandbox escape seen from outside | No supported way to ask whether a pid is sandboxed `[untested]` | The process's user id, read with `ps`. Simple and reliable `[untested]` | The process is in the guest or it is not | - |

Other options looked at and dropped: a **macOS guest VM** per worker (several GB of RAM and tens of GB of disk
each, and Apple's licence limits the number of macOS guests `[untested]`); **running the whole factory in one Linux
VM** on the Mac (works with today's code, but it is "Linux on a Mac", not the macOS support the owner asked for:
no launchd, no macOS herdr build, no macOS tool chains).

## 3. Decision

### 3.1 The pick: a separate worker user as the boundary, Seatbelt inside it as the view (B + A)

The two mechanisms fail in different places, and each covers the other's weak point.

- **The user boundary (B) carries the credential guarantees G1 to G5.** The things that are hardest to fence with
  rules on macOS are not files: the Keychain, the owner's launchd domain, Apple Events to the owner's apps, signals
  to the owner's processes, the environment of the owner's processes. A different user id is expected to put all of
  them out of reach by the operating system's own access control, with no rule list to get wrong, using features
  that are not deprecated `[untested]`. One open point: `sudo -u` changes the user but may leave the worker in the
  owner's login session, so services that are scoped to the session and not to the user (the pasteboard, screen
  capture) may stay reachable `[untested]`. Tests S9 and S15 decide this; until then it is limit 11.
- **Seatbelt (A) carries the allow-list view G6 and the write limits.** Unix permissions cannot express "deny all
  of the home except these paths" when some files are world-readable, and cannot hide other tasks' work trees from
  a worker of the same class. A deny-by-default profile can `[untested]`. Because it runs inside the worker user, a hole in the
  profile exposes at most what the worker user could reach anyway, never the owner's credentials.
- **If Apple removes `sandbox-exec`**, G1 to G5 still hold. G6 falls back to file permissions checked by the
  preflight (3.3), a weaker but honest state, and this ADR is reopened.

Seatbelt alone (A without B) is **not accepted** as the worker sandbox for SC4: it needs no root, which is
attractive, but it keeps the worker in the owner's user, so every guarantee rests on a profile in an undocumented
language that this task could not test. The profile is still the first sandbox stage to be built (M4, before the
accounts of M5), because it needs no owner action as root and is needed for the pick anyway.

The Linux VM (C) is not the default: it costs RAM on a shared machine, cannot run macOS tool chains, and breaks
the "the pane's process is claude" assumption that herdr detection and escape detection are built on. It stays the
candidate for hostile third-party code (the skill evaluator), as microVMs did in ADR 0002.

App Sandbox (D) is rejected: it is designed for a signed app bundle, not for wrapping third-party command-line
tools with a per-worker file list.

### 3.2 Shape of the macOS worker sandbox ("macbox")

Every item is `[untested]` design until its stage in section 5 proves it.

**Accounts.** Two hidden, unprivileged, no-login accounts created once by the owner: `_hivemill_ro` (roles that get
the read-only PAT) and `_hivemill_rw` (roles that get the read-write PAT), in one group `_hivemill`. Two accounts
and not one, because a process can inspect the environment of processes of its own user, and G5 says a read-only
role must not get the read-write token. A larger pool (one account per live worker) is a later option, not needed
for the guarantees: workers of one class hold identical tokens.

**Owner steps (root, once per Mac).** The factory never runs `sudo` for setup. `factory` prints a script; the
owner reads and runs it:

1. create the two accounts and the group;
2. install the launcher as a root-owned file outside the owner's home (not writable by the owner or a worker), and
   `/etc/sudoers.d/hivemill`: the owner may run exactly that launcher as `_hivemill_ro` or `_hivemill_rw` without a
   password, with the `SETENV` tag so that a fixed list of environment variables can be preserved. Nothing lets a
   worker account run anything as the owner;
3. set ACLs: the worker group may traverse (not list) the owner's home, read `~/factory` and write `~/factory/db`.

**Work trees are opened per task, not per group.** At spawn the factory (as the owner, who owns the directory, so
no root is needed) adds an ACL entry on that task's clone for the one account of the agent that works in it:
read-write for a coder or qa, read-only for a reviewer, with inheritance so that files the worker creates stay
writable by the owner. The read-only account therefore gets no write access to a coder's clone, and so cannot plant
a git hook that would later run with the read-write token. The token files in `~/.config/factory` stay 0600 and
owned by the owner; no worker account can read them.

**Launch chain.** As on Linux the pane's `PATH` starts with the agent's `bin/`, so the pane's `claude` is a shim
running as the owner. The shim reads the token files, builds the environment from the same allow-list as
`workerbox`, and runs:

    sudo -n -u <worker account> --preserve-env=<fixed list> <launcher> -- sandbox-exec -f <agent dir>/profile.sb <real claude> ...

Tokens travel in the environment only, never in an argument (other users can see arguments with `ps`). The
launcher sets `HOME`, `CLAUDE_CONFIG_DIR`, `GIT_CONFIG_GLOBAL`, `GIT_CONFIG_NOSYSTEM=1` (the system git config on
macOS may name the Keychain credential helper `[untested]`) and `TMPDIR` to per-agent directories owned by the worker account.

**Profile.** Generated per agent from the same inputs as the Linux worker profile: deny by default; read-only for
system paths, the Claude install, the Go toolchain and `~/factory`; read-write for the agent's own directories,
`~/factory/db` and the task clone (read-only for the reviewer); no Keychain, launchd, Apple Events or
process-inspection services; network on, as on Linux.

**Preflight, fail closed.** A listed role never starts unsandboxed. Before any pane is placed the spawn checks:
the accounts exist; `sudo -n -u <account> true` works; `sandbox-exec` exists and a self-test passes (a canary file
in the owner's home must be unreadable through the full chain, and a file in the task clone must be writable); a
probe run as the worker account finds no readable entry in the owner's home outside the allow-list; the token
files pass the same checks as on Linux. New `sandbox-refused` reasons: `os`, `user`, `sudo`, `seatbelt`, `view`.
Until macbox is merged, a sandbox request on macOS is refused with reason `os`; the researcher, which is always
sandboxed, therefore cannot be spawned on a Mac until then.

**Escape detection.** The Linux check compares `/proc/<pid>/ns/user`. On macOS the tick compares the user id of
the agent's `claude` process with the expected worker account. A sandboxed role whose `claude` runs as the owner
is closed and reported as `sandbox-escaped`, as today.

**Tests and the gate sandbox.** `factory test` and the HQ gate run on macOS use the same chain with a stricter
profile (no network, no tokens in the environment, writes only in the tree). cgroup caps have no macOS
counterpart; `test_cgroup` is refused on macOS and `test_slots` remains the brake.

### 3.3 Honest limits

1. **Not a full security boundary**, as on Linux. A worker can still write the factory DB, push branches other
   than `main`, exfiltrate its own tokens, and reach the network and local services.
2. **Root once.** The owner must create two accounts and a sudoers file on each Mac. On the shared Mac mini that
   is a visible machine-level change.
3. **World-readable files outside the allow-list.** Anything on the Mac that every user may read (other users'
   public folders, `/Users/Shared`, Homebrew) is readable by workers; the profile narrows this, file permissions
   alone do not. This is residual risk 6 of `docs/worker-sandbox.md`.
4. **Denied, not hidden.** A worker can learn that a path exists from the error it gets. Linux shows an empty home.
5. **Process list.** A worker sees the owner's process names and arguments (as on Linux). Unlike Linux it cannot
   signal them, because it is another user.
6. **Shared `/tmp`.** There is no private tmpfs. Workers get a private `TMPDIR`, and the profile denies the rest of
   `/tmp`; without the profile, `/tmp` is shared with every user.
7. **Deprecated layer.** The view (G6) rests on a deprecated tool with an undocumented rule language. A macOS
   update may break a profile; the self-test turns that into a refused spawn, not a silent hole.
8. **Same class, same user.** Two coders run as the same account. Only the profile separates their work trees.
9. **HQ is not sandboxed**, as on Linux. Boss, director, leads and the judge run as the owner.
10. **Claude Code's own Bash sandbox** may not work inside the profile (a sandbox inside a sandbox); it is turned
    off for sandboxed workers if the test shows that.
11. **Session services.** Until S9 shows otherwise, assume a worker can reach what belongs to the owner's login
    session and not to the owner's user: the pasteboard (which may hold a copied secret) and possibly screen
    capture. The profile denies the services it knows of; the user boundary alone may not.
12. **Shared DB directory.** Both worker accounts can write `~/factory/db`, as every worker can on Linux
    (residual risk 1 of `docs/worker-sandbox.md`).

Limits 3 to 6 and 11 describe expected macOS behaviour and are `[untested]`; M0 and M7 confirm or correct them.

### 3.4 What must be proven on a Mac (the SC4 tests)

Each test runs through the real launch chain as a sandboxed reviewer (read-only class) and a sandboxed coder
(read-write class), prints no secret, and is recorded with its command and output.

| # | proves | test, from inside the sandbox unless noted | must |
|---|---|---|---|
| S1 | G1 | read the owner's `~/.claude/.credentials.json`; ask the Keychain for the owner's Claude Code entry with `security find-generic-password` | both fail |
| S2 | G1 | start `claude` with only the worker token and send one prompt | answers; `/status` shows the token login, not the owner's |
| S3 | G2 | read `~/.config/gh/hosts.yml`; `security find-internet-password -s github.com`; `gh auth status` with `GH_TOKEN` unset | all fail or report not logged in |
| S4 | G3 | read `~/.ssh`; connect to the owner's ssh-agent socket; `ssh-add -l` | all fail |
| S5 | G4 | read the owner's `~/Library/Keychains`; `security dump-keychain` on the owner's login keychain | fail |
| S6 | G5 | as the read-only class: `git push` of a test branch; read the environment of a read-write worker's process; read the read-write token file; create a file in a coder's clone and in its `.git/hooks` | push refused by GitHub; no environment shown; read and both writes fail |
| S7 | G5 | as the read-write class: push a test branch, open and close a PR | works; never push to `main` |
| S8 | G6 | list the owner's home; read each allow-listed path; write to `~/factory/bin`; read another task's clone | list fails; reads work; write fails; other clone unreadable |
| S9 | no way out | `launchctl bootstrap gui/<owner uid>` with a test job; `osascript` telling Terminal to run a command; `kill -0` of an owner process; connect to the herdr socket; `pbpaste` after the owner copied a marker string; `screencapture` to a file | all fail, and `pbpaste` does not print the marker. Run once with and once without the Seatbelt layer; a pass only with Seatbelt stays limit 11 |
| S10 | it still works | `git fetch`, `git merge origin/main`, `go vet`, `go test -race` on `go/`, `gh pr view`, `factory inbox`, `factory done` | all work; DB written by owner and worker without a lock or permission error |
| S11 | multiplexer | herdr and `hc`: the agent shows as a Claude agent, goes working to idle; Esc, `factory stop`, `factory relaunch --when-idle` | all work |
| S12 | fail closed | outside: remove the sudoers file, then each of the other preflight inputs, and spawn | exit 3, no pane, `sandbox-refused` with the reason |
| S13 | escape detection | outside: start a plain `claude --resume` as the owner for a sandboxed agent | tick closes it, event `sandbox-escaped` |
| S14 | profile self-test | outside: break the profile (allow the home) and spawn | refused with reason `seatbelt` or `view` |
| S15 | layer independence | run S1 to S7 once with the Seatbelt layer off (user only) | S1 to S7 still pass: the credential guarantees do not depend on the deprecated layer |
| S16 | nested sandbox | turn Claude Code's Bash sandbox on inside | record the result; decide the setting |

S15 is the test that justifies the layering. If it fails, the decision in 3.1 is wrong and this ADR is reopened.

## 4. Port plan

### 4.1 Linux-only inventory

`docs/design/macos-linux-deps.md` (task T-1535) was not on `origin/main` when this was written (checked at
`587491961`). T-1535 owns the complete file-and-line list for SC1; when it merges, it replaces this summary. The
summary below is this task's own `git grep` over non-test code, grouped by dependency.

| dependency | where (file:line at `587491961`) | macOS replacement |
|---|---|---|
| systemd user units, timer | `systemd/*.service`, `systemd/factory-tick.timer`, `systemd/install.sh:68,91,93` | launchd agents (4.2) |
| `systemctl`, `loginctl` in the installer | `go/internal/install/install.go:242,710,715,893`; `plugins/hivemill/scripts/install.py:326,590,593,692` | `launchctl bootstrap`, `kickstart`, `print` `[doc D10]`; no linger (4.2) |
| `systemd-run --user` for detached runs | `bin/factory_cli.py:6833,14483` | a detached child in its own session (the code already has that fallback at `:6835,14490`) |
| serve unit pid from systemd and cgroup | `bin/factory_cli.py:8238,8246-8253` | a pid file written by `factory serve`; `launchctl print` output is not stable enough to parse `[doc D10]` |
| cgroup caps for tests | `scout/testcg.py`, `systemd/factory-tests.service`, `systemd/factory-tests.slice` | none; refused on macOS (3.2) |
| memory gate, `/proc/meminfo` | `bin/factory_cli.py:2409,14578`; `go/internal/cli/cmd_res.go:175,418`; `go/internal/gate/proc.go:74,93`; `web/views/resources.py:146`; `go/internal/install/host.go:139` | `vm_stat`, `sysctl hw.memsize`, `sysctl vm.swapusage` (4.3) |
| load gate, `/proc/loadavg`, CPU count from `/sys` | `go/internal/cli/cmd_res.go:38,204,216`; `go/internal/gate/proc.go:113,557` (Python uses `os.getloadavg`, `bin/factory_cli.py:2527`) | `sysctl vm.loadavg`, `runtime.NumCPU` (4.3) |
| process scan, `/proc/<pid>/stat`, `cmdline` | `bin/factory_cli.py:935-1005,2009-2093,2246,2277,8193,8370`; `go/internal/gate/proc.go:437`; `go/internal/cli/worker.go:99-104`; `web/views/agents.py:60-108`; `scout/egress.py:32`; `go/internal/sandbox/egress.go:64` | one `ps` call or the `kern.proc` sysctls (4.3) |
| process cwd, open files, environment, comm | `bin/factory_cli.py:8063-8086,8279-8326,8385,8428` | `lsof`; arguments and cwd instead of the environment (4.3) |
| user-namespace identity for escape detection | `bin/factory_cli.py:2265` | process user id (3.2) |
| tmp gate file-system type, `/proc/mounts` | `bin/factory_cli.py:2447`; `go/internal/cli/cmd_res.go:200,1007`; `go/internal/gate/proc.go:146-173` | the type name from `statfs` (4.4) |
| path of an open file, `/proc/self/fd` | `web/tokens.py:133`; `go/internal/tokens/index.go:275` | `fcntl` with `F_GETPATH` `[untested]` |
| namespaces: `unshare`, `setpriv`, `newuidmap`, tmpfs mounts, `/proc/self/fd` binds | `scout/nsbox.py`, `scout/workerbox.py`, `scout/gatebox.py`, `scout/testbox.py`, `scout/sandbox.py`; `go/internal/sandbox/*.go` (`run.go:118-129`, `launch.go:25-97`, `setup.go:21-33,249,456`, `gatebox.go:27-29,146-149`, `workerbox.go:158,587,635`) | macbox (3.2); Linux code stays behind a build tag |
| runtime dir `/run/user/<uid>`, `/dev/shm` | runtime dir: `bin/factory_cli.py:13985,14330`, `scout/nsbox.py:33,167`, `scout/gatebox.py:74`; `/dev/shm`: `scout/gatebox.py:22`, `go/internal/sandbox/gatebox.go:27-29`, `go/internal/sandbox/workerbox.go:158` | do not exist on macOS; macbox does not use them |
| herdr and gh pins are Linux builds | `go/internal/install/install.go:41-47,438`; `plugins/hivemill/scripts/install.py:39-41`; `ci/freshbox_test.py:39-41` | macOS assets (4.5) |
| GNU `ps` flags | `bin/factory_cli.py:14635` (`--sort=-rss`) | BSD form, `ps -axo rss=,command= -m` `[untested]` |
| fresh-install test creates a Linux user | `ci/freshbox_test.py:265,347,615,630`; `ci/install_container_test.py:143,168` | a macOS variant with `sysadminctl`, `sudo -u`, `launchctl bootout` (stage M8) |
| machine probe in the Herdr app mod | `plugins/hivemill/herdr-app/mods/hivemill/data.js:281-293` | already has a Darwin branch (`sysctl`, `vm_stat`); reuse its formulas |
| installer shim platform switch | `plugins/hivemill/scripts/install:15-24` | already accepts `Darwin-arm64` and `shasum`; `.github/workflows/release.yml:31,38` already builds `darwin/arm64` |

Needs no change: the dead-socket paths under `/proc/nonexistent` (`bin/factory_cli.py:927`,
`go/internal/herdr/herdr.go:30`, `roles/settings/*.json.in`, `scout/sandbox.py:36`) only have to not exist, and `/proc` does not exist on macOS
`[untested]`; `fcntl.flock` and `start_new_session` are POSIX and exist on macOS `[untested]`.

Scope decisions: **Apple silicon only** (the release already builds `darwin/arm64` and nothing else for macOS).
New code is Go behind `//go:build darwin` and `linux` files with one shared interface (ADR 0001). Python paths
that are still live when a stage is built get the same behaviour through the Go binary or a small platform branch;
the lead decides per task, depending on how far the Go migration is.

### 4.2 Boot with launchd

Per-user agents in `~/Library/LaunchAgents`, loaded with `launchctl bootstrap gui/<uid> <plist>` and removed with
`launchctl bootout`; `load` and `unload` are the legacy forms `[doc D10]`.

| systemd unit | launchd job | keys |
|---|---|---|
| `herdr-server.service` | `hivemill.herdr-server` | `ProgramArguments`, `RunAtLoad`, `KeepAlive` `[doc D8]` |
| `factory-up.service` | `hivemill.up` | `RunAtLoad`; the wait-for-herdr loop moves into `factory up` or a small wrapper |
| `factory-tick.timer` + `.service` | `hivemill.tick` | `StartInterval` 60 `[doc D8]` |
| `factory-serve.service` | `hivemill.serve` | `RunAtLoad`, `KeepAlive` |

Differences that need code or a written limit:

- **Plists are generated, not linked.** systemd units use `%h`; a plist needs absolute paths, and an agent's plist
  must be owned by the user and not group or world writable `[doc D9]`. The installer renders them from templates
  (with `FACTORY_HOME` and `PATH` in `EnvironmentVariables` `[doc D8]`) and has a `--dry-run`.
- **Once a minute.** `StartInterval` skips a firing while the job still runs ("that interval firing will likewise
  be missed" `[doc D8]`), which matches the one-shot tick. It fires on a fixed interval, not 60 s after the last
  run ended; a tick of 50 s is followed by the next one 10 s later. Jobs are also not started more often than
  every 10 s by default `[doc D8]`. Accepted.
- **Sleep.** A firing during sleep is missed `[doc D8]`. A Mac that runs the factory must not sleep. How (a power
  setting by the owner, or the herdr job under `caffeinate`) is decided in stage M2 `[untested]`.
- **No tick timeout.** `TimeoutStartSec=240` has no launchd counterpart `[untested]`; the tick gets its own
  deadline.
- **No linger.** An agent "executes only while that user is logged in" `[doc D9]`. After a reboot the factory
  starts when the owner logs in, or at boot if the owner enables automatic login `[untested]`. The alternative, a
  system daemon with `UserName` (which only works in the privileged system domain `[doc D8]`), needs root and runs
  without the owner's unlocked Keychain, where the owner's Claude login lives `[doc D6]`; HQ agents would not be
  logged in. Decision: user agents; `--linger` prints this limit on macOS and does nothing.
- **Logs.** No journal: `StandardOutPath` and `StandardErrorPath` `[doc D8]` point into `<FACTORY_HOME>/logs/`.
- **Keychain over ssh.** A Claude login made in an ssh session falls back to the credentials file because the
  Keychain is locked there `[doc D6]`. Setup on a headless Mac mini must say which of the two the owner ends up
  with, since G1 covers both.

### 4.3 Gates without /proc

One host-facts interface in `go/internal/gate` with a Linux and a macOS implementation; `Decide` stays the pure
function it is today. Every macOS reader keeps the Linux rule "a value that cannot be read closes the gate or
reports unknown, never open".

| fact | macOS source | notes |
|---|---|---|
| load1 | `sysctl -n vm.loadavg` | same formula as the Herdr app probe (`data.js:285`) `[untested]` |
| CPU count | `runtime.NumCPU` | Apple silicon mixes fast and slow cores; whether `1.25 x all cores` is the right cap is measured in M3 `[untested]` |
| available memory | `vm_stat` pages (free + inactive + speculative) times the page size | the formula of `data.js:284`. macOS has no `MemAvailable`; it compresses memory and swaps to dynamic files, so this number is a guess. M3 also reads the kernel's memory-pressure level and closes the gate when it is not normal `[untested]` |
| swap used | `sysctl -n vm.swapusage` | display only `[untested]` |
| claude and cursor process count, parents, sessions | one `ps -axo pid=,ppid=,uid=,lstart=,command=` per tick, or the `kern.proc` sysctls through `golang.org/x/sys` (already an indirect dependency, `go/go.mod:15`) | arguments of other users' processes are visible, which the count needs once workers run as another user `[untested]` |
| process start time | `lstart` from the same `ps` call | replaces field 22 of `/proc/<pid>/stat` `[untested]` |
| process cwd and open files (tmp sweep, serve scan) | `lsof` | the owner cannot inspect another user's process without root `[untested]`: for worker-user processes the sweep keeps everything it cannot prove unused, as it does today for an unreadable pid |
| process environment (job identity) | not available for another user's process `[untested]` | identify by arguments, cwd and the DB row |

### 4.4 The tmp gate on APFS

- On Linux `/tmp` is a RAM-backed tmpfs, so the gate protects memory. On macOS `/tmp` is a link to `/private/tmp`
  on the main APFS volume `[untested]`: the same code then measures how full the **disk** is. `statfs` still gives
  total and free space, and the file-system type name comes from the same call, so `/proc/mounts` is not needed
  `[untested]`.
- Keep `tmp_max_pct` and `tmp_sweep_pct`, and add an absolute free-space floor for macOS: 90% of a large disk
  leaves tens of GB free, and of a small one almost nothing. The value is chosen in M3 from the Mac's real numbers.
- APFS volumes share their container's free space, and purgeable space and local snapshots make "free" move
  without the factory writing anything `[untested]`. The gate text must name the volume and the numbers.
- Keep `/tmp`, not `$TMPDIR`, as the swept directory: Claude's scratch dirs are expected under `/tmp/claude-<uid>`
  as on Linux `[untested]`, and `$TMPDIR` on macOS is a per-user directory elsewhere. The sweep's ownership check
  must accept the worker accounts' entries once macbox is on.
- macOS may delete old files from `/tmp` on its own schedule `[untested]`. A long-idle agent's scratch dir can
  disappear; nothing durable may live there (it does not today).

### 4.5 herdr and hc builds

- **herdr.** Release v0.9.5, the version the installer pins, has `herdr-macos-aarch64` (22,536,176 bytes) and
  `herdr-macos-x86_64` `[doc D15]`. The pin table gets a second key (operating system) and the sha256 computed
  from the downloaded asset in stage M1. Whether the binary is signed so that macOS runs it without a prompt, and
  whether a download made by the installer is quarantined, is checked in M1 `[untested]`.
- **hc (herdr-core).** No published release was found (the GitHub releases list for `herdrdev/herdr-core` was
  empty on 2026-11-15 `[doc D15]`). Until one exists, macOS starts with the herdr backend (`terminal_backend
  herdr`), and M1 records how `hc` is obtained on a Mac (a source build, or a binary the owner supplies).
- **gh.** A macOS arm64 release asset is expected `[untested]`; pin it like the Linux one, or accept an existing
  `gh` on `PATH` (Homebrew).

### 4.6 Install path

- The shim (`plugins/hivemill/scripts/install`) and the release already cover `darwin-arm64`. In
  `hivemill-install`, the "pinned release is a Linux build" refusal (`install.go:438`) becomes a macOS pin table,
  and the systemd step becomes the launchd step of 4.2 (`--no-systemd` gets a neutral alias).
- Prerequisites that differ: Python 3.11 or newer is not part of macOS `[untested]`, so until the Python removal
  lands the owner installs it (Homebrew or python.org) and `--check` reports it; `git` comes with the Xcode
  command-line tools `[untested]`. The installer still never uses `sudo` and never edits dotfiles.
- Logins: `gh auth login` and `claude` `/login` in the owner's own terminal, as on Linux. On macOS both end up in
  the Keychain `[doc D6]` `[doc D7]`, which is exactly what G1, G2 and G4 protect.
- `docs/SETUP.md` gets a macOS section in which every command was run on a Mac (SC2).

## 5. Staged build tasks

Each stage is a candidate factory task. "Mac window" means the step needs a Mac and therefore the owner's OK at
that time (MacBook: a test window; Mac mini: an OK for each run, because another team shares it). Stages
without it are built and tested on the Linux host and in CI with fakes, and prove nothing about macOS by
themselves.

| stage | task | acceptance | Mac window |
|---|---|---|---|
| M0 | Facts spike on a Mac: a read-only script that records the macOS version, `sandbox-exec` presence, home and `~/.claude` permissions, where `/tmp` points, `vm_stat`, `sysctl` and `ps` output shapes, where the Claude and gh logins are stored, and herdr's detection of `sudo -u <self> claude` | A report file with each `[untested]` claim of sections 2 and 4 marked confirmed or corrected; this ADR updated in the same PR. Changes nothing on the Mac | **Yes** (MacBook first). No root |
| M1 | Installer: macOS pins for herdr and gh, operating-system key in the pin table, launchd step behind a fake host | `hivemill-install --check` and `--dry-run` pass on `darwin/arm64` in CI (macOS runner or fake host); Linux output is byte-identical to before; sha256 of each macOS asset recorded with the command that produced it | Only to compute and confirm the pins if no macOS CI runner is used |
| M2 | launchd units: plist templates, `factory` subcommand or installer step to render, bootstrap and bootout them, tick deadline, sleep decision | On a Mac: `factory up`, `tick` and `serve` run from launchd; the event log shows a tick in each of 10 consecutive minutes; a reboot followed by a login brings HQ back; bootout removes everything (SC3) | **Yes** |
| M3 | Gates: host-facts interface, macOS readers for load, CPU, memory, swap, processes; tmp gate with the free-space floor | Unit tests with recorded macOS command output (from M0) for each reader, including an unreadable value closing the gate; on a Mac `factory res` prints sane numbers next to Activity Monitor's; no `/proc` path is opened on macOS | **Yes**, for the final check only |
| M4 | Seatbelt profile generator for reviewer, coder and qa, plus the self-test | Golden-file tests of the generated profile; on a Mac, same user: a canary in the home is unreadable, the clone is writable, `claude`, `gh`, `git` and `go test` work inside. Recorded as not sufficient for SC4 | **Yes**. No root |
| M5 | Owner setup script for the accounts, sudoers file and ACLs; `factory` prints it and has a matching uninstall script | The script is reviewed by the owner before it runs; after it, `sudo -n -u _hivemill_ro true` works and the worker account cannot list the owner's home; the uninstall script restores the previous state | **Yes, with root, the owner runs it.** MacBook first; the Mac mini only after M7 passed on the MacBook |
| M6 | macbox launcher, shim, preflight reasons, escape detection by user id, spawn wiring behind `sandbox_roles` | Linux golden spawn tests unchanged; unit tests for each new refusal reason through the CLI as a subprocess (one refused and one accepted case each); on macOS without M5 a sandboxed spawn exits 3 with reason `user` and places no pane | No (fakes); a short one to confirm the refusal |
| M7 | SC4 proof: run S1 to S16 of section 3.4 | A PR that quotes each command and its output; every row meets its "must" column, or the limit is added to 3.3 and the owner accepts it; `docs/worker-sandbox.md` gets a macOS section (SC4) | **Yes** (needs M5 done) |
| M8 | Fresh-install test for macOS and the `docs/SETUP.md` macOS section | The test creates and removes a throwaway macOS user and passes; every command of the new section was run on a Mac and its output is quoted in the PR (SC2, SC5) | **Yes, with root** |
| M9 | Rollout on the Mac mini: one sandboxed reviewer canary, then qa, then coder, with the hold times of `docs/worker-sandbox.md` section 4 | No `sandbox-refused` or `sandbox-escaped` event during each hold; rollback is `factory config sandbox_roles ''` | **Yes, the owner's OK for each run** |
| M10 | Later: Linux VM profile for the skill evaluator on macOS | Separate design; until then skill trials are refused on macOS with a clear message | Later |

Order: M0 first, because it turns the `[untested]` tags into facts cheaply and may change later stages. M1 to M4
are independent of each other after M0. M5 is the only stage that changes a Mac at the machine level; it blocks
M7. Release text claims macOS support only after M2, M3, M7 and M8 (SC5).

## 6. Consequences

- The Linux sandbox is unchanged. macOS gets a second implementation behind the same config key, preflight and
  events, so operators learn one set of commands.
- Two sandbox implementations must be kept working. The shared part (profile inputs, token handling, preflight,
  refusal reasons) must live in one place so that a change to the allow-list reaches both.
- macOS support needs an owner action as root on each Mac. Without it the factory still runs on macOS, with
  workers unsandboxed as on a Linux box where `sandbox_roles` is empty, except the researcher, which is always
  sandboxed and is refused there. The release notes must say so.
- Part of the design rests on a tool Apple calls deprecated. The layering and test S15 limit what its removal
  would cost.
- Until M0 runs, most of sections 2 to 4 is informed expectation. The staged plan is built so that no stage
  depends on an untested claim that an earlier stage did not check.

## 7. Alternatives

Covered in section 2 and decided in 3.1: Seatbelt alone (not accepted for SC4, built as a layer), a Linux VM per
worker (kept for hostile code), App Sandbox (rejected), a macOS guest VM per worker and the whole factory in one
Linux VM (dropped).

## Evidence

Read on 2026-11-15. Quotes are exact. The man pages are Apple's, as mirrored from an Xcode installation.

| id | source | quoted or recorded |
|---|---|---|
| D1 | `sandbox-exec(1)`, <https://keith.github.io/xcode-man-pages/sandbox-exec.1.html> | The command "is DEPRECATED"; the page points developers to App Sandbox. Options: `-f` profile file, `-n` profile name, `-p` profile string, `-D` key=value. Dated March 9, 2017 |
| D2 | `sandbox(7)`, <https://keith.github.io/xcode-man-pages/sandbox.7.html> | "The sandbox facility allows applications to voluntarily restrict their access to operating system resources." "New processes inherit the sandbox of their parent." |
| D3 | `sandbox_init(3)`, <https://keith.github.io/xcode-man-pages/sandbox_init.3.html> | "The sandbox_init() and sandbox_free_error() functions are DEPRECATED." |
| D4 | Claude Code, Sandboxing, <https://code.claude.com/docs/en/sandboxing> | "macOS: uses Seatbelt for sandbox enforcement". "The sandbox covers shell commands only." It is built on the open source `@anthropic-ai/sandbox-runtime` package |
| D5 | sandbox-runtime README, <https://github.com/anthropic-experimental/sandbox-runtime> | "macOS: Uses `sandbox-exec` with dynamically generated Seatbelt profiles". "It can be used to sandbox the behaviour of agents, local MCP servers, bash commands and arbitrary processes." On its weaker network mode: "enabling this opens a potential data exfiltration vector through the trustd service." On Apple Events: "enabling this means the sandbox no longer provides code-execution isolation." "As this is an early research preview, APIs and configuration formats may evolve." |
| D6 | Claude Code, Authentication, <https://code.claude.com/docs/en/authentication> | "On macOS, credentials are stored in the encrypted macOS Keychain. When the Keychain rejects the write, such as when it's locked in an SSH session, Claude Code stores your login in `~/.claude/.credentials.json` with file mode `0600` instead". With `CLAUDE_CONFIG_DIR` set, Claude Code "keys the macOS Keychain entry to that directory too". Precedence: `CLAUDE_CODE_OAUTH_TOKEN` is item 5, above "Subscription OAuth credentials from `/login`" (item 7). The `claude setup-token` token "can only make model requests" |
| D7 | `gh auth login`, <https://cli.github.com/manual/gh_auth_login> | "After completion, an authentication token will be stored securely in the system credential store." "gh will fallback to writing the token to a plain text file." "Favour setting `GH_TOKEN` for fine-grained personal access token usage." |
| D8 | `launchd.plist(5)`, <https://keith.github.io/xcode-man-pages/launchd.plist.5.html> | `StartInterval`: if asleep, "that interval will be missed due to shortcomings in kqueue(3)"; "If the job is running during an interval firing, that interval firing will likewise be missed." `ThrottleInterval`: "by default, jobs will not be spawned more than once every 10 seconds." `UserName`: "This key is only applicable for services that are loaded into the privileged system domain." `KeepAlive`: "The value may be set to true to unconditionally keep the job alive." Also `RunAtLoad`, `EnvironmentVariables`, `StandardOutPath` |
| D9 | Apple, Creating Launch Daemons and Agents, <https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html> | "A user agent is essentially identical to a daemon, but is specific to a given logged-in user and executes only while that user is logged in." "Agents installed for the current user must be owned by that user. All daemons and agents must not be group writable or world writable." |
| D10 | `launchctl(1)`, <https://keith.github.io/xcode-man-pages/launchctl.1.html> | "bootstrap \| bootout domain-target [service-path service-path2 ...] \| service-target"; domains `system/`, `user/<uid>`, `gui/<uid>`; `kickstart` "Instructs launchd to run the specified service immediately"; for `load` and `unload`: "Recommended alternative subcommands: bootstrap \| bootout \| enable \| disable". The page says the output of `print` is not a stable format |
| D11 | Apple, Virtualization framework, <https://developer.apple.com/documentation/virtualization> | "Create virtual machines and run macOS and Linux-based operating systems." "The Virtualization framework provides high-level APIs for creating and managing virtual machines (VM) on Apple silicon and Intel-based Mac computers." Entitlement `com.apple.security.virtualization`: "A Boolean value that indicates whether your app can use the Virtualization framework." |
| D12 | apple/container README, <https://github.com/apple/container> | "`container` is a tool that you can use to create and run Linux containers as lightweight virtual machines on your Mac." "You need a Mac with Apple silicon to run `container`." "`container` is designed to run on macOS 26 or later." Install: "Enter your administrator password when prompted." |
| D13 | Apple, App Sandbox, <https://developer.apple.com/documentation/security/app-sandbox> | "Restrict access to system resources and user data in macOS apps to contain damage if an app becomes compromised." "App Sandbox provides protection to system resources and user data by limiting your app's access to resources requested through entitlements." "To distribute a macOS app through the Mac App Store, you must enable the App Sandbox capability." Entitlement `com.apple.security.app-sandbox` |
| D14 | Apple Platform Security, Keychain data protection, <https://support.apple.com/guide/security/keychain-data-protection-secb0694df1a/web> | "An initial default keychain is created for each Mac user". "the securityd daemon determines which keychain items each process or app can access." "Keychains can use access control lists (ACLs) to set policies for accessibility and authentication requirements." |
| D15 | GitHub API, <https://api.github.com/repos/herdrdev/herdr/releases/tags/v0.9.5> and <https://api.github.com/repos/herdrdev/herdr-core/releases> | herdr v0.9.5 assets: `herdr-linux-aarch64`, `herdr-linux-x86_64`, `herdr-macos-aarch64` (22,536,176 bytes), `herdr-macos-x86_64` (24,367,600 bytes), `herdr-windows-x86_64.zip`. The herdr-core releases list was empty |

Not found in any document read here, and so tagged `[untested]` throughout: the SBPL rule language itself (Apple
publishes no reference), whether `sandbox-exec` replaces itself with the command, nested Seatbelt behaviour, the
default permissions of a macOS home directory, how `sudo` on macOS handles the terminal and the process tree, and
every statement about `ps`, `lsof`, `vm_stat`, `sysctl` and APFS behaviour.
