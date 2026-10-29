# Per-run cgroup caps for `factory test` (T-0681)

ADR 0002 T-c, owner yes M11793. `factory test` can put each run in its own child cgroup with a CPU, memory and task cap, so a runaway test cannot starve the panes. It needs one user unit that the **owner installs**; nothing here installs it.

## Unit files to install (owner step, not done by the PR)

The same files are in `systemd/factory-tests.slice` and `systemd/factory-tests.service` (owner approved the values, M13102); copy them to `~/.config/systemd/user/`.

`~/.config/systemd/user/factory-tests.slice`:

```ini
[Unit]
Description=Factory test runs (parent for per-run cgroups)

[Slice]
# Hard ceiling for all concurrent test runs together; per-run caps are set by `factory test` below this.
CPUQuota=600%
MemoryMax=4G
TasksMax=2048
```

`~/.config/systemd/user/factory-tests.service`:

```ini
[Unit]
Description=Delegated cgroup for factory test runs
Documentation=file:%h/factory/docs/test-cgroups.md

[Service]
# Holds no process: an empty, delegated cgroup that `factory test` makes per-run children in.
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/true
Slice=factory-tests.slice
Delegate=cpu memory pids

[Install]
WantedBy=default.target
```

Install and check (owner):

```
systemctl --user daemon-reload
systemctl --user enable --now factory-tests.service
cat /sys/fs/cgroup/user.slice/user-$(id -u).slice/user@$(id -u).service/factory-tests.slice/factory-tests.service/cgroup.controllers
```

The last command must list `cpu memory pids` (available controllers; `cgroup.subtree_control` stays empty until the first `factory test` run enables them, because systemd does not do that for a delegatee). Why a oneshot service: Delegate= is documented for services and scopes, not slices, and a cgroup that delegates controllers to children must hold no process itself (kernel no-internal-process rule), which `RemainAfterExit` with a no-op `ExecStart` gives. Agents cannot run `systemd-run` (M2108), so `factory test` makes the child cgroups by writing cgroupfs directly inside this delegated parent.

Rollback: `factory config test_cgroup 0`, then `systemctl --user disable --now factory-tests.service` and remove the two files.

## Config (director only)

| key | default | meaning |
|---|---|---|
| `test_cgroup` | `0` | `1` = every `factory test` run gets a child cgroup with the caps below. |
| `test_cgroup_parent` | `` | Absolute cgroupfs dir of the delegated parent. Empty = derived from the uid as in the unit above. |
| `test_cpu_quota` | `400` | CPU cap in percent of one CPU (`cpu.max`). |
| `test_mem_max` | `3G` | `memory.max`; `memory.swap.max` is set to 0 so the cap is a real one. |
| `test_tasks_max` | `512` | `pids.max`. |

Behaviour with `test_cgroup=1`: the parent must exist, be writable, and list `cpu`, `memory` and `pids` in its `cgroup.controllers` (Delegate=); `factory test` then writes `+cpu +memory +pids` to the parent's `cgroup.subtree_control` itself, and refuses if that write fails; otherwise `factory test` exits 3 and the command is **not run** (never uncapped). The run's process tree is moved into the child before the command starts; when the command ends the child cgroup is killed (`cgroup.kill`) and removed. A kernel OOM kill inside the cap shows as exit 137. Works with `test_sandbox` 0 or 1.
