# ADR 0002: Sandbox runtime v3 (tests + workers)

## Status

Accepted (A+), director M11695; owner decisions M11793, M11838, M11845.

Proposed by the staff-engineer on 2026-10-29 (M11673). Owner decisions: (2) the cgroup unit with `Delegate=yes`: yes
(M11793); (3) install `uidmap`: yes, installed (M11838); (4) a Proxmox runner VM: yes in principle, deferred until the
host RAM upgrade (M11845). Items marked OWNER below needed the boss or owner. Goal: G-010. Task: T-0688.

Owner ask (M11620): "improve how we run virtual machines for the tests and everything. How we want to implement
sandboxes?"

## 1. Context

- Today: v2 = nsbox (`scout/nsbox.py`), unprivileged user+mount namespace, same uid, network on, no pid ns, no
  CPU/mem caps (docs/worker-sandbox.md section 6). Worker profile (`scout/workerbox.py`) is merged but NOT live:
  T-0168 spike is blocked on owner steps O2/O3. Gate profile (`scout/gatebox.py`, T-0632: net ns lo-only, pid ns, git
  common dir ro, env allow-list) is written, wiring T-0652 in progress, not live.
- Incident 2026-10-29 12:05: test fixtures pushed 60+ commits to public main via an inherited GIT_DIR plus the owner gh
  login; the tick then synced the live checkout to that tree. The test run had (a) a real repo reachable through GIT_*
  env, (b) owner credentials in reach, (c) network. Any one of those removed would have stopped it. No sandbox was
  active, so this was a rollout gap, not an nsbox failure.
- Box (measured at the time): 8 vCPU, 11.9 GB RAM (not 5.7), 4.9 GB available, 3.0 GB swap used, /tmp tmpfs 88% full,
  load1 5.3, 13 agents live. `/dev/kvm` present, nested=Y, but the owner uid is NOT in group kvm. No podman, bwrap,
  firecracker, qemu, pasta, slirp4netns, newuidmap. Present: unshare, runc, docker. /etc/subuid exists;
  the owner user has a subuid range of 65536. cgroup v2, the systemd user manager of uid 1000 delegates cpu, memory, pids.

## 2. New finding (fix first, independent of the option chosen)

The owner uid is in group `docker` (gid 989). `/run/docker.sock` is root:docker 0660 and is NOT hidden by any nsbox
profile (workerbox hides home, /run/user/<uid>, /tmp, /dev/shm; gatebox the same plus a net ns). A unix socket path is
not affected by a net namespace, and supplementary groups survive `unshare -r`. Measured:

    $ unshare -r -m -n python3 -c 'import socket;s=socket.socket(socket.AF_UNIX);s.connect("/run/docker.sock");print("connect OK")'
    connect OK inside unshare -r -m -n

So every nsbox profile (worker, gate, eval) can reach the Docker API, which is root on the host (bind-mount /, read
every credential). This defeats the whole G-010 credential boundary for a deliberate worker. Only connect() was
tested; no API request was sent.

Fix (coder task, small, risky tier): in nsbox allow mode, mask `/run` with an empty tmpfs and bind back only what is
needed (likely `/run/systemd/resolve` for DNS, nothing else), or at minimum hide `/run/docker.sock` and
`/var/run/docker.sock`; add a test that connect() to docker.sock fails inside each profile. Also seen:
`/run/ssh-unix-local/socket` (0666, local sshd; low risk because keys are hidden, but masking /run covers it).
OWNER: decide whether the owner uid needs the docker group at all; removing it closes this for unsandboxed agents too
(worker-guard blocks the `docker` word, which is a hook, not a boundary).

## 3. Options

| | A: nsbox today | A+: hardened nsbox "testbox" (recommended) | B: rootless podman/bwrap | C: microVMs (Firecracker/CH) | D: Proxmox runner VM |
|---|---|---|---|---|---|
| Isolation | same uid, same kernel, net on, no pid ns; docker.sock reachable | same kernel; tests: no net, pid ns, env allow-list, ro git dir, /run masked, cgroup caps. Phase 2: subuid (different host uid) | same kernel, subuid by default, seccomp profile, own rootfs | own kernel; strongest | own kernel and own host identity; no owner creds exist there |
| Startup | measured 1.6 ms (`unshare -r -m`), 2.0 ms with net+pid+mount-proc (20 runs: 0.031 s / 0.040 s); python3 start is 12 ms on top | same, ~2 ms; + cgroup scope setup ~tens of ms (not measured, systemd-run is blocked for agents) | not installed; typical 150-400 ms | not installed; published ~125 ms to guest userspace, realistic 0.3-1 s with rootfs, slower under nested virt | persistent runner: 0 per run + repo fetch 1-3 s; clone-per-run 10-60 s |
| RAM per sandbox at 10-15 parallel | ~0 overhead; tmpfs only what is written (but worker /tmp tmpfs up to 512 MB each, and /tmp is already 88%) | ~0; test scratch on disk (gatebox uses <wt>/tmp), per-run MemoryMax | +5-15 MB (conmon, pasta); shared page cache | guest RAM 256-512 MB each, no shared page cache: 15 x 384 MB = 5.6 GB > 4.9 GB available. Fits 2-3, not 10-15 | 0 on this box; costs Proxmox host RAM (headroom unknown, OWNER) |
| CPU caps vs load gate | none; load gate (load1 cap 10) is the only brake | per-run cgroup (CPUQuota, MemoryMax, TasksMax) under one `factory-tests` parent (e.g. 400% CPU, 3 GB) so tests never starve agent panes; load gate stays as the spawn brake | same via systemd cgroups | vCPU count per VM; host still sees load | moves full-suite CPU off this box entirely |
| Network (github+pypi only) | open | tests: none (lo only; tests must be hermetic, deps preinstalled). Worker panes: extend `scout/egress.py` CONNECT proxy (today ALLOW=api.anthropic.com:443 only) to a host set: api.anthropic.com, github.com, api.github.com, codeload/objects.githubusercontent.com, pypi.org, files.pythonhosted.org | --network=none or pasta + same proxy | TAP + host proxy; needs CAP_NET_ADMIN/root helper | VM firewall, simplest to reason about |
| Credentials | worker panes get worker OAuth + PAT (O3 ro / O4 rw) in env | panes: unchanged (O3/O4). Tests: NONE, ever. Env allow-list as gatebox.make_env: no GH_TOKEN, no CLAUDE_CODE_OAUTH_TOKEN, no GIT_* | same split | same split | runner holds at most a read-only deploy key / fetch-only PAT; no push rights |
| Effort | done | S: /run mask. M: generalize gatebox into `factory test` + hook routing + cgroup parent. 4-6 coder tasks | M-L + OWNER install (podman, uidmap, passt) + image upkeep | L-XL: kernel+rootfs pipeline, TAP/root helper, kvm group, vsock result channel | M infra (OWNER) + M dispatch/result code |
| Migration from nsbox | - | in place; same code, flags | rewrite profiles as podman flags; keep nsbox as fallback | tests only; panes stay nsbox | tests only; panes stay nsbox |

## 4. Recommendation

Split the problem in two. Agent panes (claude) need the API, GitHub and the factory DB; test execution needs none of
that. Most of today's risk is in test execution, and the incident was a test run.

1. Now, S: mask /run in nsbox allow mode (section 2). Blocks the docker.sock root path in all profiles. OWNER: drop
   the docker group if unused.
2. Now, M: A+ "testbox". Promote the gatebox profile to the one way tests run: a `factory test <cmd>` runner used by
   coder, qa and the local gate. It applies: own net ns (lo only), pid ns, env allow-list (no tokens, no GIT_*), ro
   git common dir, masked FACTORY_MAIN state dirs, disk-backed scratch, and a per-run cgroup under a `factory-tests`
   parent with CPU/mem/pids caps. worker-guard steers bare `python -m unittest`, `pytest`, `go test` in coder/qa panes
   to `factory test` (a hook is not a boundary, but the default path becomes safe; the boundary is that the pane
   itself is nsboxed). Fail closed like gatebox (GateboxError, never run unsandboxed). This alone would have stopped
   the incident three ways (no GIT_DIR, no gh login, no network).
   cgroup caps need a delegated subtree the factory owns: OWNER one-time change (a user unit with Delegate=yes, e.g.
   the existing tick/serve service, or a dedicated `factory-tests.slice`); agents cannot run systemd-run (worker-guard
   M2108), so HQ code creates the child cgroups.
3. Keep G-010 worker rollout as planned (T-0168 spike is waiting on owner O2/O3; that is the real blocker for pane
   isolation). Add next: egress allow-list for panes via the existing egress proxy with a host set. Note nsbox refuses
   forward=True with pid=False because only --kill-child reaps the forwarder; fix with PR_SET_PDEATHSIG or a pid ns
   with signal forwarding (which the herdr pid/session detection must survive; spike first).
4. Next, OWNER install of `uidmap` (newuidmap/newgidmap; subuid range already exists): run testbox (later panes) as a
   subordinate uid. This removes the same-uid residuals in one step: no docker group, no owner-owned files, no signals
   to owner processes, owner 0600 files unreadable even if a mount is missed. Near-zero RAM and startup cost. This is
   the single biggest isolation gain per unit of effort.
5. Not now: B (podman) gives the same kernel primitives plus images; after 4 its extra value is a default seccomp
   profile and a separate rootfs. Revisit only if nsbox upkeep becomes the bottleneck. C (microVMs) rejected for tests
   at 10-15 parallel: does not fit in RAM, needs root for TAP and the kvm group, plus a kernel/rootfs pipeline, under
   nested virt. Keep C as a candidate for G-009 untrusted-skill evals (1-2 runs at a time, hostile code, strongest
   boundary wanted). D is the right shape if the owner wants a hard kernel boundary for full suites AND the Proxmox
   host has spare RAM (it would add capacity instead of using this box's 4.9 GB). OWNER question, not urgent once A+
   is in.

## 5. Consequences

- Tests lose network: any test that fetches must be fixed or marked; deps come from the preinstalled env. Expect a few
  failures on first enable; run behind a `test_sandbox` config flag with the same canary/rollback pattern as
  `sandbox_roles`.
- Same kernel remains the trust base (kernel userns bugs = escape). Acceptable for our threat (accidental or confused
  agents, limited deliberate misuse); not acceptable for hostile code, which is why C stays on the table for G-009.
- /tmp pressure: worker tmpfs /tmp (512 MB cap each) plus 88% full host /tmp is a memory risk at 10-15 workers;
  testbox scratch should stay disk-backed.
- Load gate: cgroup CPU caps keep test bursts from starving panes; the load1 gate keeps working as the spawn brake.
  Consider PSI (cpu.pressure of the tests cgroup) later as a better signal than load1.

## 6. Proposed tasks (for the lead/director to staff)

- T-a (coder, risky tier, S, URGENT): nsbox allow mode masks /run (tmpfs) with an explicit ro bind-back list; tests:
  connect() to /run/docker.sock and /run/ssh-unix-local/socket fails in worker, gate and eval profiles; DNS still
  resolves in the worker profile. Acceptance run on the factory host with 0 skipped userns tests.
- T-b (coder, M): `factory test` runner = gatebox profile generalized for coder/qa cwd (per-task clone rw, everything
  else as gatebox), fail closed; behind config `test_sandbox`.
- T-c (coder, M): per-run cgroup under an owner-delegated parent (CPUQuota, MemoryMax, TasksMax); refuse to run if the
  parent is missing when the flag is on. Needs OWNER unit change first.
- T-d (coder, S): worker-guard routes bare test-runner commands in coder/qa panes to `factory test` when
  `test_sandbox` is on.
- T-e (coder, M): egress proxy host-set allow-list for worker panes; spike pid-death handling for pid=False.
- OWNER via boss: (1) docker group needed? (2) unit with Delegate=yes for factory-tests; (3) install `uidmap`
  (phase 2); (4) D only if Proxmox host has spare RAM. Plus the existing O2/O3 for the G-010 spike.

Owner answers: (2) yes (M11793); (3) yes, installed (M11838); (4) yes in principle, deferred until the host RAM
upgrade (M11845).

Not measured (needs install, out of scope here): podman and Firecracker startup/RAM (numbers above are typical
published values), cgroup scope setup time, per-run RAM of the full suite inside testbox. If wanted: a measurement
task after uidmap/podman are installed in a scratch VM, not on this box.
