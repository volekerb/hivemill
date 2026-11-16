# ADR 0006: Worker sandbox assessment (why go test fails for QA)

## Status

Proposed, 2026-11-16. Assessment only: nothing is installed, no settings file, hook, namespace profile or gate changes with this ADR. It goes to the owner for review. It becomes Accepted only with the owner's approving message id recorded here. Goal: G-058. Task: T-1614.

This ADR **keeps** [ADR 0002](0002-sandbox-runtime-v3.md). It does not amend it and it does not supersede it. ADR 0002 stays the decision for the runtime: hardened nsbox testbox, no microVMs for tests. The new fact is which layer denied `go test` on PRs 687, 690 and 691. That layer is the agent-runtime permission check, which ADR 0002 does not specify.

## Context

Owner, verbatim (M37570, item 4): "ok we need a good solution. Maybe sandbox we use is shit?" The director's reading of that item is this assessment: the current worker sandbox, why `go test` failed for QA on PRs 687, 690 and 691 (those PRs held for a later re-QA), the alternatives, and one recommended fix. Expected gain of the fix, not of this ADR: about 15 PR-hours a day on Go PRs, and no QA item answered with "sandbox denied".

Diagnosis M37505 is the source of that hold account. This ADR does not remeasure the hold clocks. It remeasures the denial.

## Current design

Three layers sit on a worker. They are not one sandbox.

**1. Namespace box.** `scout/nsbox.py` is the unprivileged user and mount namespace. `scout/workerbox.py` is the worker profile. `scout/sandbox.py` is a different box: the evaluator trial, not a coder or QA pane. The Go port of the worker profile is `go/internal/sandbox` (`go/internal/sandbox/workerbox.go`). The macOS profile is `go/internal/macbox` and is not what runs on this Linux host (ADR 0003).

Who enters the worker profile: `scout/workerbox.py:32` `SANDBOXABLE` is reviewer, qa, coder, researcher. The same set is `go/internal/sandbox/workerbox.go:27`. Researcher is always sandboxed. The other three enter when `sandbox_roles` lists the role, when the spawn used `--sandbox`, or when the task has `sandbox_required` for that same role. The code default of `sandbox_roles` is empty. This pane could not read the live value: `factory config` was denied (quoted below). `docs/worker-sandbox.md` last records step 3 (coder) as in progress and `sandbox_roles` unchanged, on 2026-11-11. That sentence is not a live read.

What the profile mounts, from `scout/workerbox.py:140` `make_cfg` and `scout/workerbox.py:162`: allow mode, `net=True`, `pid=False`. Network stays the host network. There is no pid namespace. Home, the runtime dir, `/tmp` and `/dev/shm` are hidden under tmpfs; the factory checkout is read-only; `~/factory/db/` and the agent config dir are read-write; `~/factory/worktrees/` is masked and only this agent's cwd is bound back. QA, coder and researcher get that cwd read-write; the reviewer gets it read-only (`bin/factory` passes `role in ("qa", "coder", "researcher")` as the rw flag at `bin/factory_cli.py:10770`).

Go inside that profile, `scout/workerbox.py:126` `go_env_for` and the same keys at `go/internal/sandbox/workerbox.go:146`: `GOCACHE` and `GOTMPDIR` under the per-agent `go/` dir (mode 0700, on disk), `GOMODCACHE` the host module cache read-only, `GOPROXY=off`, `GOSUMDB=off`, `GOFLAGS=-mod=readonly`, `GOTOOLCHAIN=local`, `GOENV=off`, `GOWORK=off`. The toolchain directory is bound read-only when it sits under home. Read-only binds are re-mounted with the source's locked flags (`scout/nsbox.py:335`). On this host `/` is ext4 without `noexec`, and `/tmp` is tmpfs `rw,nosuid,nodev` (no `noexec`); see the measurements below. A test binary on the agent `GOTMPDIR` or on `/tmp` is not blocked by a noexec mount.

Stage 1 is `unshare` (`scout/nsbox.py:247` `compose_stage1`). `-n` is added only when `net` is false (`scout/nsbox.py:248`). A pid namespace is added only when `pid` is true (`scout/nsbox.py:249`); if the key is missing the default is true. The worker profile passes `net=True` and `pid=False`, so its argv is `unshare -r -m` with neither `-n` nor `--pid`. The test profile passes `net=False` and `pid=True`, so it gets both.

**2. Settings sandbox of the agent runtime.** Claude is started with `--permission-mode auto` (`scout/agent_runtime.py:137`). T-1518 states the rule this assessment relies on: an allow entry skips the auto-mode classifier for that command shape, and a command that is not in the allow list is decided by the classifier. QA's allow list is `roles/settings/qa.json.in:212` and the eight entries at `roles/settings/qa.json.in:213` through `roles/settings/qa.json.in:220`: `factory`, three `gh pr` reads, three `git` reads, and one Workflow. None of them is `go` or `gofmt`. The deny list is the long block above that (merge, sqlite3, herdr, force-push, writes to the live checkout's `bin/`, `roles/`, `db/`, and so on). The same file wires the command guard at `roles/settings/qa.json.in:245`: `sh <live>/roles/hooks/worker-guard --role qa`. Coder and reviewer templates have the same shape: a short allow list, no `go` entry. Cursor does not take `--permission-mode`. It translates those allow and deny strings into `Shell(...)` tokens (`scout/cursor_harness.py:47` `translate_permission_token`) and can add `--sandbox enabled` (`scout/cursor_harness.py:365`). That flag is the product sandbox, not nsbox (`scout/cursor_harness.py:396`).

**3. Command guard hook.** `roles/hooks/worker-guard` is the shim; `roles/hooks/worker-guard.py` is the Python hook; `go/cmd/worker-guard` is the Go port, used only when the guard-impl flag says so. For coder and qa, `roles/hooks/worker-guard.py:4743` `sandbox_route` treats `go test` as a bare test runner, and `roles/hooks/worker-guard.py:4781` blocks it only when config `test_sandbox` is `1`, telling the agent to run `factory test` instead. `factory test` uses `scout/testbox.py` over `scout/gatebox.py` only when config `test_sandbox` is `1`: own net namespace, loopback only (`scout/gatebox.py:104` `net=False`), own pid namespace. The code default is `0` (`bin/factory_cli.py:216`). At `0`, `factory test` replaces itself with the command on the host (`bin/factory_cli.py:14701` `os.execvp`). The live value was not readable from this pane. Writes into `~/.local/go` are blocked (`roles/hooks/worker-guard.py:372` and the tests in `tests/test_worker_guard_prot_trees.py`). Exec of `~/.local/go/bin/go version` and `go build ./...` is allowed by that same test module. A missing or unreadable DB makes `test_sandbox_on` return false (fail open on that one check).

| layer | reviewer | qa | coder | researcher | other roles |
|---|---|---|---|---|---|
| namespace box | opt-in, cwd read-only | opt-in, own clone read-write | opt-in, own clone read-write | always, research dir read-write | not in `SANDBOXABLE` |
| settings allow list | no `go` entry | no `go` entry | no `go` entry | its own template | each role has a template |
| command guard | hook runs; `go test` route is coder/qa only | hook runs; bare `go test` blocked only if `test_sandbox=1` | same as qa | hook runs | hook runs |

## Why go test fails for QA

This pane is `cursor-coder-t-1614`, role coder. It is not a Claude QA pane and it does not start with `--permission-mode auto` or `roles/settings/qa.json.in`. The Claude auto-mode classifier therefore cannot be entered from here. Where a verdict does not quote a classifier stderr or an exit code, this section says **not reproduced** and quotes the task record. The command guard and the namespace box were entered.

Commands the records name, and what this pane ran. The hook was the real shim, `sh roles/hooks/worker-guard --role qa`, with a Bash PreToolUse payload whose `cwd` was this worktree. `factory res` was denied before any of this, so no full `go test -race` of the `go/` tree was started:

```
worker-guard: blocked (workers may not run broad factory reads (`factory res`) against the live home; use factory task show <id> / goal show <own goal>, or set a temp FACTORY_HOME in the same command).
```

`factory config` was denied the same way (`factory config` named in the block text). The live `sandbox_roles` and `test_sandbox` values were not read. A script that only checked whether `docker` was on `PATH` was denied and was not retried:

```
worker-guard: blocked: privilege escalation (`docker`) is not allowed for factory agents (owner M803)
```

**PR 687 (T-1475).** QA verdict `claude-qa-t-1475`, 2026-11-15T09:32:11+01:00, also PR comment 6891732922. Two commands:

- `go test -race -count=1 ./internal/web/...` — the verdict says this passed (`ok` data, `ok` views). It was not the denial.
- `go test -run '^$' -fuzz '^FuzzNormalizeDOM$' -fuzztime 20s ./internal/web/views` — the verdict's words are "was denied by the auto-mode classifier in my sandbox (twice); not worked around." The verdict does not quote the classifier's stderr. The classifier's exit code is **not reproduced**.

The same fuzz command through the QA hook on this tree: exit code 0, empty stderr. The race command through the same hook: exit code 0, empty stderr. The denying layer in the verdict is the auto-mode classifier, which runs because the command is not in `roles/settings/qa.json.in:212`. It is not the command guard on this tree, and it is not a GOCACHE path: the verdict records that the race test ran.

**PR 690 (T-1460).** QA comment 6892934015, 2026-11-15T10:23:42Z: "Could not verify (go denied by classifier / worker-guard): AC1 (gofmt, go vet, go test -race), ..." plus the other Go items listed there. The comment does not quote a shell command, a stderr line, or an exit code. Reviewer comment 6892922905: "The local go test run was not permitted in this pane." No exit code. The exact classifier stderr and its exit code are **not reproduced**. The PR body's acceptance item 1 is "gofmt -l, go vet, go test -race", and the body also has `go test -race -count=1 ./internal/spawn/... ./internal/cli/...`.

Hook, role qa, command `cd go && gofmt -l . && go vet ./... && go test -race -count=1 ./...`: exit code 0, empty stderr. On this tree the worker-guard does not deny that shape. The "worker-guard" half of the QA sentence is not what the hook does today. The classifier half matches the allow list, and that half's own error text is **not reproduced**.

**PR 691 (T-1524).** No QA verdict body is stored. The task history at 2026-11-15T12:20:14 is the director's judge hold: "QA could not run go test (classifier); reviewer PASS, CI green, static checks verified. Re-QA after T-1498 (PR 661) + T-1518 merge." The acceptance item 1 command is `cd go && gofmt -l . && go vet ./... && go test -race -count=1 ./internal/factorytest/...`. The classifier stderr and its exit code are **not reproduced**.

Hook, role qa, that command: exit code 0, empty stderr.

**Namespace box, QA Go env.** `python3 -m unittest tests.test_workerbox.GoRealToolchainTests.test_go_gofmt_vet_and_race_test_work_inside_offline` runs `go version`, `go env GOPROXY GOTOOLCHAIN`, `gofmt -l`, `go vet ./...` and `go test -race ./...` inside the worker profile, with the real toolchain, `GOPROXY=off`, and a fresh per-agent cache. The helper's default role is coder. QA uses the same `go_env_for` and the same read-write cwd bit. Result: `Ran 1 test in 10.956s` / `OK`. So a cold scratch cache inside the namespace runs `go test -race` of a tiny module. That does not time `go test -race -count=1 ./...` of the `go/` tree (not started; `factory res` denied).

What was ruled out, because it was run or read, not guessed:

- GOCACHE or GOMODCACHE outside the writable set: the profile already sets a per-agent writable `GOCACHE` and `GOTMPDIR` (`scout/workerbox.py:128`). The tiny in-namespace race test wrote its cache there and passed.
- `GOFLAGS=-mod=readonly` with `GOPROXY=off`: the same test prints `off` and `local` and the race test exits 0 for a module with no missing dependencies. A missing module would fail closed, which is the no-network keep, not a broken cache.
- A write under a read-only checkout: QA's own clone is read-write. PR 687's race test ran.
- Exec from a noexec mount: `/tmp` has no `noexec`; the agent `GOTMPDIR` is a normal directory bind.
- The command guard: the three PR command shapes exit code 0 through `roles/hooks/worker-guard --role qa`.
- A blocked network lookup: not the recorded error. The verdicts name the classifier. Worker panes have host network (`scout/workerbox.py:162`).

The denying layer for the three PRs, as far as the records and this tree agree, is the Claude auto-mode classifier in front of `roles/settings/qa.json.in`. T-1518, still blocked on T-1498, states the same mechanism. T-1498's premise that the guard refuses exec of `~/.local/go/bin/go` is not what `tests/test_worker_guard_prot_trees.py` or this hook run shows for `go test`.

## Requirements

The owner's two keeps, verbatim: "no network" and "a scratch cache for go test in QA".

Also required:

- QA never modifies product code. QA's writable tree is its own clone (`<goal>-<task>-qa`), not the coder clone and not `~/factory` outside `db/`.
- The PR head is checked out read-only (reviewer) or as a throwaway copy (the QA clone). A `factory test` run copies or binds that tree and discards the scratch dir on exit.
- No factory secrets readable: the worker profile hides the owner home, `~/.ssh`, the owner `~/.config` except the one scrub-terms file, and injects only the worker token.
- It has to work on the 5.7 GB box named in the task. Measured on this host, 2026-11-16, from `/proc/meminfo` and `os.cpu_count()`: MemTotal 12183124 kB (11.6 GiB), MemAvailable 7648956 kB, SwapTotal 4194300 kB, 16 CPUs, load1 1.33 at the time of the unshare runs. ADR 0002 on 2026-10-29 measured 11.9 GB and already said that was not 5.7. The design budget stays 5.7 GB: the fix adds no guest RAM. Start-time budget: the sandbox itself stays under 50 ms. Measured `unshare -r -m true`, 20 runs, all exit code 0: minimum 0.000912 s, median 0.001558 s, maximum 0.004421 s. Measured `unshare -r -n -m --pid --fork --mount-proc true`, 10 runs, all exit code 0: minimum 0.001335 s, median 0.001855 s. A cold tiny `go test -race` inside the worker profile was 10.956 s wall, almost all of it the Go compile, not `unshare`.

## Alternatives

Tools present: `/usr/bin/unshare`, `/usr/bin/newuidmap`, `/usr/bin/newgidmap`. `bwrap`, `nsjail`, `podman` and `firecracker` are absent (`shutil.which` printed `absent`). `/dev/kvm` exists and this user cannot read, write or execute it. Nothing was installed.

| | (a) current sandbox, minimal fix | (b) bubblewrap | (c) nsjail | (d) rootless podman | (e) firecracker |
|---|---|---|---|---|---|
| isolation: what a hostile test can reach | same kernel, same uid unless `test_sandbox_subuid` is on; host network in the pane; test profile has its own net namespace. Secrets hidden by the worker mounts. A test can still dial out if it runs in the pane | same kernel and user namespaces. Isolation is whatever the argv binds. README: "bubblewrap is not a complete, ready-made sandbox" | same kernel; namespaces plus a seccomp policy and rlimits. A missed bind is still same-kernel | same kernel, subordinate uids, its own rootfs. Rootless still maps host paths that are mounted in | own kernel via KVM. Strongest of the five. This user cannot open `/dev/kvm` |
| no-network enforcement | pane: not enforced (host net). Test profile: `-n` on `unshare` (`scout/nsbox.py:248`), loopback only. `GOPROXY=off` blocks module download and does not block a dial | `--unshare-net`: own net namespace with only loopback. Not measured (not installed). https://github.com/containers/bubblewrap/blob/main/README.md not measured | `clone_newnet: true` in the sample config. Not measured (not installed). https://github.com/google/nsjail/blob/master/README.md not measured | `--network=none`: "Create a network namespace for the container but do not configure network interfaces for it, thus the container has no network connectivity." Not measured (not installed). https://manpages.debian.org/testing/podman/podman-run.1.en.html not measured | "Firecracker does not perform any network traffic filtering." No NIC means no network; a TAP means host filtering is required. Not measured (not installed). https://github.com/firecracker-microvm/firecracker/blob/main/docs/design.md not measured |
| go test works | yes, once the classifier allow list lets the command start. Scratch `GOCACHE`/`GOTMPDIR` already exist. Command `python3 -m unittest tests.test_workerbox.GoRealToolchainTests.test_go_gofmt_vet_and_race_test_work_inside_offline` -> `Ran 1 test in 10.956s` / `OK` (cold cache, tiny module) | yes if the argv bind-mounts the toolchain read-only, a writable scratch cache, and the module cache read-only, and does not use a noexec tmpfs for `GOTMPDIR`. Not measured | yes with the same binds (`-R` toolchain, `-B` scratch, `-T` tmpfs not used for the test binary). Not measured | yes with a volume for the scratch cache and `--network=none`. Image upkeep on every Go bump. Not measured | yes inside the guest if the rootfs contains the toolchain and a scratch disk. Not measured |
| start time and RAM | namespace start median 0.001558 s, command `unshare -r -m true` (20 runs, all exit 0); with a net and pid namespace median 0.001855 s, command `unshare -r -n -m --pid --fork --mount-proc true` (10 runs, all exit 0). Host `/tmp` tmpfs size 6091564k, read from `/proc/mounts`. No guest RAM | not measured. The README states no startup time and no RSS. https://github.com/containers/bubblewrap/blob/main/README.md not measured | not measured. The README states no startup time and no RSS. https://github.com/google/nsjail/blob/master/README.md not measured | not measured. The rootless tutorial states no startup time and no RSS. https://github.com/containers/podman/blob/main/docs/tutorials/rootless_tutorial.md not measured | not measured. Design doc: a minimal microVM is "a minimal Linux kernel, single-core CPU, and 128 MiB of RAM" and "5 microVMs per host core per second". That is a throughput sentence, not a measured latency on this box. https://github.com/firecracker-microvm/firecracker/blob/main/docs/design.md not measured |
| install and upkeep | none. Settings text and a regression test. `newuidmap` is already installed; this ADR does not turn subuid on | install `bubblewrap`, then reimplement the worker binds as `bwrap` flags. Two sandboxes to keep if nsbox stays for panes | build from source (the README's package list includes a compiler and protobuf). Not installed. A second profile language (protobuf) | install podman and passt, keep an image, `/etc/subuid` already required. Heavier than a settings change | install a VMM, a kernel and a rootfs; grant kvm; a TAP needs a privileged helper. Does not fit a 5.7 GB box at 10 to 15 parallel guests |
| what ADR 0002 decided | recommended: keep nsbox, harden the test path (A+), no microVMs for tests | option B, not now. Revisit only if nsbox upkeep is the bottleneck | not a named option. Same class as B: another same-kernel wrapper | option B, with bubblewrap. Not now | option C, rejected for tests on RAM, kvm and the rootfs pipeline |

## Decision

Keep the nsbox worker profile and the nsbox test profile. Do not replace them with bubblewrap, nsjail, rootless podman or Firecracker. The recorded QA failures are the auto-mode classifier in front of an allow list that has no `go` entry. A different kernel sandbox would still stop at that classifier, and it would violate the "no install" constraint until the owner asked for the install.

## Recommendation

Option (a): the current sandbox, with a minimal fix.

The scratch cache in the parenthetical of option (a) is already implemented (`scout/workerbox.py:128`, `go/internal/sandbox/workerbox.go:146`). Building it again does not change the PR 687, 690 or 691 denials. The minimal fix is the allow list T-1518 already specifies, so the classifier does not decide those commands, while the command guard still blocks network and write shapes.

What changes, in a later task, not in this ADR:

- `roles/settings/qa.json.in`, `roles/settings/reviewer.json.in`, `roles/settings/coder.json.in`: allow only the exec shapes T-1518 names (`~/.local/go/bin/go` with `test`, `vet`, `build`, `run`, `list`, `env`, `version`; `gofmt` with `-l` and `-d`; the `GOTOOLCHAIN=local GOFLAGS=-mod=mod GOPROXY=off GOMODCACHE=...` prefix if the settings format can say it). Cursor copies those strings through `translate_permission_token` on the next materialize.
- `tests/test_settings_templates.py`: each allowed shape present, and `go mod download`, `go get`, `go install`, `go env -w`, `go clean`, `gofmt -w` absent from the allow list.
- No edit to `scout/nsbox.py`, `scout/workerbox.py`, `scout/gatebox.py` or `scout/testbox.py` unless a later run shows the scratch dir is missing. It is not missing.
- T-1498's exec half is already true here: the QA hook exits 0 for `go test`. Its network half is not. The same hook, role qa, this worktree, exits 0 for `go mod download`, `~/.local/go/bin/go mod download`, `go get` of one module version, and `go install ./...`. `go env -w GOFLAGS=-mod=mod` exits 2. Do not add a wider exec allow. Do add the missing block for those three network subcommands.

What this does not loosen:

- The allow-list entries do not add a network permission and do not remove a deny entry. They also do not create a network block the hook lacks today. `go mod download`, `go get` and `go install` exit 0 through the QA hook; the settings allow list must not gain those shapes. The hook block is follow-up 1.
- No network for the test run is the test profile (`scout/gatebox.py:104` `net=False`). `factory test` uses that profile only when `test_sandbox` is `1`. At the code default `0` the command runs on the host (`bin/factory_cli.py:14701`). Turning the flag on is a tightening, done by the director, not by this ADR. `GOPROXY=off` inside the worker profile stays. It does not stop a dial, and it is set only inside that profile.
- QA does not gain write access to product code. The allow entries are exec of the toolchain, not writes. The QA clone stays the only writable tree. The reviewer cwd stays read-only. The deny list for `bin/`, `roles/`, `db/` and the live checkout stays.
- Factory secrets stay hidden. The worker token stays the only GitHub credential inside the profile.
- The worker pane keeps host network, because the agent needs the API and `gh`. That split is ADR 0002. `GOPROXY=off` is not a substitute for the net namespace: a test binary can still dial. The no-network keep for the test itself is the test profile, not a pane that is offline.

How to test the fix, after it lands:

- Blocked case, after `test_sandbox` is `1`: inside `factory test` (net namespace, loopback only), a dial fails. Example shape: a one-file Go test that calls `net.DialTimeout` to `192.0.2.1` port 80 and expects a failure, and `go env GOPROXY` prints `off`. After follow-up 1, `go mod download` through the hook exits 2. Today that command exits 0, so it is not the blocked case yet.
- Allowed case: `go test -race -count=1 ./...` of the `go/` tree, inside that sandbox, once with a cold scratch cache (empty `GOCACHE`) and once with a warm scratch cache (the same `GOCACHE` left in place). Record both wall times. One run at a time, after `factory res`.

Rollback: revert the settings PR. No migration. Relaunch QA, reviewer and coder so they read the old allow list. The namespace profile is untouched, so reverting settings returns today's classifier behavior and nothing else.

ADR 0003 and `go/internal/macbox`: the recommendation does not carry a runtime change onto macOS. `go/internal/macbox/roles.go:36` already sets the gate role to no network. The settings allow list is the piece that carries over, because both Claude and Cursor load `roles/settings`. Seatbelt stays as ADR 0003 wrote it. There is no Mac in this task, so no Seatbelt number is claimed.

G-057: workers on the second worker host use this same sandbox. The settings change is in the repo, so it applies on that host at the next deploy of `roles/settings`. No second sandbox design.

## Consequences

- QA can run the Go acceptance commands without a classifier denial, and without a hold for "sandbox denied".
- A test that needs the network fails inside `factory test` only when `test_sandbox` is `1`. At the code default `0` it runs on the host. Suites that dial out have to be hermetic or move to CI on purpose once that flag is on.
- Same kernel remains the trust base, which is the ADR 0002 acceptance. This ADR does not reopen microVMs.
- `test_sandbox` stays the switch for "bare `go test` must go through `factory test`". If it is off, the allow list lets `go test` run in the pane, where the network is on and only `GOPROXY=off` limits module fetch. Open question 3 asks the owner which of those is the keep.

## Interim rule until the fix lands

T-1597 rule H5, verbatim: "H5 QA could not run a command (sandbox denial): do not hold the PR for a later re-QA. Decide in the same cycle: the CI job is the proof for that item, or another QA runs it."

What QA does today: for a Go acceptance item, quote the CI `go` job on that head SHA, including the package line and the time, or have another QA run the command. Do not file a verdict that only says the command was denied and then wait. What the director does today: do not set a judge hold whose reason is "QA could not run go test". H5 does not waive the item. Reviewer PASS, QA PASS and green CI stay required. The CI job is the proof for the item that the sandbox denied, or a second QA run is.

## Follow-up tasks

This task files none. After the owner accepts this ADR, the pm files or retargets, in this order:

1. Title: "Block go mod download, go get and go install in the worker-guard; keep go test exec allowed." Files: `roles/hooks/worker-guard.py`, `go/internal/guard`, and the existing guard test module. Through the real hook, role qa, `go test` exits 0 and `go mod download`, `go get` and `go install` exit 2. Order: 1. This is the network half of T-1498. Do not land the exec refusal: `~/.local/go/bin/go test` already exits 0. Retarget T-1498, do not file a second guard change.
2. Title: "qa, reviewer and coder settings: allow the Go toolchain read-only commands so the auto-mode classifier skips them." Files: `roles/settings/qa.json.in`, `roles/settings/reviewer.json.in`, `roles/settings/coder.json.in`, `tests/test_settings_templates.py`. Order: 2. This is T-1518, already filed and blocked on T-1498. Unblock it when order 1 says the guard already allows exec. Do not file a duplicate.
3. Title: "One QA run of go test -race of the go/ tree inside factory test, cold scratch cache and warm scratch cache, with the dial-out case failing." Files: none in the product tree if the timings are a task note; a test lands only if the dial-out case is missing from `tests/test_workerbox.py` or the testbox tests. Order: 3, after order 2 is merged and the director has relaunched QA.

## Open questions for the owner

1. Does "no network" apply to the go test run, or to the whole QA pane? Option A: the test run only; the pane keeps the API and `gh`. Option B: the whole QA pane is offline. Recommendation: A. ADR 0002 already split the pane from the test run, and a pane with no network cannot `gh pr view`.
2. T-1498 assumes the guard refuses `~/.local/go/bin/go`. This tree's hook allows `go test`. Option A: retarget T-1498 to a regression test and do not add a wider exec allow. Option B: land T-1498 as written. Recommendation: A. A wider allow is a loosening this assessment does not support.
3. When `test_sandbox` is off, an allow-listed `go test` runs in the pane, which has host network. Option A: turn `test_sandbox` on so bare `go test` is forced through `factory test` (loopback only). Option B: leave `test_sandbox` off and rely on `GOPROXY=off`. Recommendation: A. `GOPROXY=off` does not stop a test from dialing. The live value was not readable from this pane (`factory config` denied); the director should confirm it before the switch.
4. Is the 5.7 GB figure still the budget, given this host measured 11.6 GiB and 16 CPUs? Option A: keep 5.7 GB as the budget, so Firecracker stays rejected. Option B: plan for this host's 11.6 GiB and revisit microVMs. Recommendation: A. The recommended fix does not need the extra RAM, and ADR 0002 already rejected microVMs for parallel tests.
5. Should the macOS port (ADR 0003) change in the same fix? Option A: no; only the shared settings allow list, which macOS agents will load too. Option B: add a Seatbelt rule for a scratch `GOCACHE` now. Recommendation: A. The gate profile already has no network, and no Mac was available to measure.
