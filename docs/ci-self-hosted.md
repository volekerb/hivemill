# Self-hosted CI runners (`hm-ci`)

CI for our own pull requests and pushes runs on four self-hosted runners with the label `hm-ci` (owner decision, 2026-11-26): Debian 13, 16 cores, shared with task sessions.

## Which jobs run where

Every job in `.github/workflows/tests.yml` uses one `runs-on` expression: a pull request from this repository, a push to `main` and a manual run go to `hm-ci`; a pull request from a fork goes to `ubuntu-latest`. `tests/test_ci_policy.py` allows exactly that expression and no other self-hosted form. The other workflows stay on `ubuntu-latest`.

The expression is routing, not a security boundary: a fork pull request can edit the workflow and ask for `hm-ci`. The boundary is the approval of fork runs (below). Read the workflow changes of a fork pull request before you approve its run.

## One-time machine setup

The owner session applies this and keeps it so. Workflows must not depend on `sudo`.

Runner user and service:

- A dedicated user with no `sudo`, not in the `docker` group, and none of the owner's credentials.
- The runner services run at low CPU priority (for example `Nice=` or `CPUWeight=` in the unit), so task sessions win.
- Fork pull requests need approval before any workflow runs: set the repository's `fork-pr-contributor-approval` policy to `all_external_contributors`.

OS packages:

- `python3` 3.13 at `/usr/bin/python3` (the Debian 13 default).
- `zsh`: the workflow only calls `apt-get` when it is missing, which fails without `sudo`.
- `git`, `bash`, `perl`, `gh` (the test-count step reads the live PR body).
- `gcc` and `libc6-dev`: the manual deep run uses `go test -race`.
- `nodejs`, version 20 or newer: with `CI` set, the ios-release node tests fail without it.
- `util-linux` (`unshare`, `setpriv`) and `uidmap` (`newuidmap`, `newgidmap`).

Kernel and user settings:

- Unprivileged user namespaces enabled, including new mount, PID and network namespaces. Check as the runner user: `unshare -r -n -m --pid --fork --mount-proc true` must exit 0.
- A subordinate id range of at least 2 ids for the runner user in both `/etc/subuid` and `/etc/subgid` (`usermod --add-subuids`, `--add-subgids`).

## Each runner needs its own tool cache

`actions/setup-go` installs into the runner's tool cache, and the install is not safe when two runners share one: jobs that start together delete each other's half-copied files and fail with `Failed to download version ... ENOENT ... copyfile`, or find a Go that does not run. This happens when jobs ask for a Go version the cache does not hold yet, for example after a Go version bump in `go/go.mod`. `tests.yml` sets `GOTOOLCHAIN: local` for every `setup-go` step, so a cached Go matching the `go` line is enough and the `toolchain` line alone causes no install.

For each runner, set `AGENT_TOOLSDIRECTORY=<that runner's own directory>/toolcache` in the `.env` file in the runner's install directory. Remove any other `AGENT_TOOLSDIRECTORY`, `RUNNER_TOOLSDIRECTORY` or `RUNNER_TOOL_CACHE` setting that points at a shared directory (in `.env` and in the service unit), reload systemd if you edited a unit (`systemctl daemon-reload`, or `systemctl --user daemon-reload`, run as the runner user, for a user unit), wait until the runner has no job, restart the runner service, and check that the next `setup-go` log names a path under that runner's own directory.

Until that is applied, re-run the failed jobs after the run has finished: the last install of a burst usually leaves a good copy in the cache. If a re-run still fails, wait until no runner has a job, stop the runners, delete the broken version directory under the tool cache's `go/` together with its `x64.complete` marker, then start one runner, re-run the failed jobs once their run has finished, wait until a `setup-go` step for that Go version succeeds, and only then start the others.

## Python must live outside the runner's home

The test sandboxes hide the user's home directory. A Python installed there (as `actions/setup-python` does on a self-hosted runner) does not exist inside the box, and sandbox tests fail with `No such file or directory` or `Permission denied`. So `tests.yml` runs `setup-python` only on GitHub-hosted runners, and the self-hosted runners use `/usr/bin/python3`. Workflow commands say `python3`.

## Test time limits

The machine is busy, so `tests.yml` sets `HM_TEST_TIME_SCALE`, which multiplies test time limits. Set it the same way when reproducing a timing failure on a loaded machine.

## Tests that skip when a capability is absent

Each skip prints its reason. A skip for the first three rows on `hm-ci` means the setup above is broken: fix the machine.

| Missing | Skipped |
|---|---|
| unprivileged user namespaces | the namespace cases of the sandbox tests, for example `test_nsbox_allow`, `test_gatebox`, `test_gate_sandbox`, `test_workerbox`, `test_testbox`, `test_skilleval_nsbox`, `test_skilleval_collect`, and their Go ports |
| subordinate id ranges or `uidmap` | the subuid cases of `test_testbox` and their Go ports |
| `zsh` | nothing in CI: with `CI` set the `.zshenv` tests in `test_worker_ghcred` and `test_sandbox_spawn` fail |
| a host socket the runner user may connect to (expected: it is not in the `docker` group) | the control case "without the `/run` mask the host socket is reachable" in `test_nsbox_allow` and its Go port. The cases asserting the socket is blocked inside the box still run |
| a delegated cgroup under the user's systemd service | `test_testcg.Enforced` |
