# ADR 0005: Self-hosted CI runners for trusted PRs

## Status

Proposed, 2026-11-15. Design only: nothing is installed, no runner is registered, no workflow, policy text or policy test changes with this ADR. It goes to the owner for review. It becomes Accepted only with the owner's approving message id recorded here. The install is task T-1606, which is blocked on that approval. Goal: G-058. Task: T-1605.

## Context

CI capacity is the largest cause of slow PRs in the G-058 diagnosis (report M37505, 12 to 15 November 2026, 145 PRs): 202 PR-hours lost in 3 days. A merged PR costs about 11 job-hours, and the free plan's 20 concurrent hosted jobs carry about 45 merges a day (20 x 24 = 480 job-hours a day). The owner's target is 100 merges a day.

Owner decisions (director M37547 item 3, director M37570 D1 and D2): stay on the free plan; run the full suite on our own runners before merge; run only affected tests on GitHub-hosted runners.

Today `docs/ci-cost.md` forbids self-hosted runners and `tests/test_ci_policy.py` rule (a) enforces it. ADR 0002 already records the owner's "yes in principle" to a Proxmox runner VM (M11845), deferred at the time for host RAM.

Facts this design rests on (measured on 2026-11-15, commands in the PR):

- `volekerb/hivemill` is **public** and owned by a **user account**, not an organization (`gh api repos/volekerb/hivemill`: `visibility=public`, `owner.type=User`). Runner groups are an organization feature ("All organizations have a single default runner group", [manage-access](https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/manage-access)), so no runner group can restrict a runner on this repository.
- All of the last 300 PRs are same-repository PRs by one author (`gh pr list --state all --limit 300`: 300 x `volekerb`, 0 cross-repository). Fork PRs are rare but must stay possible.
- One full run of `tests.yml` is 2.4 to 2.6 job-hours on hosted runners (run 43744198175: 8,631 job-seconds; run 43735600579: 9,233). So 11 job-hours per merged PR is about 4.4 runs per merged PR (heads pushed, plus the run on main).
- GitHub's own guidance: "self-hosted runners should almost never be used for public repositories on GitHub, because any user can open pull requests against the repository and compromise the environment" ([secure use](https://docs.github.com/en/actions/reference/security/secure-use)), and "We recommend that you only use self-hosted runners with private repositories" ([manage-access](https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/manage-access)).

### Owner hard rules

Verbatim from director M37547, except one host name, replaced by the words in square brackets because the public-text guard refuses that name. In this ADR "the HQ VM" and "the second worker VM" are the factory's two worker hosts, and pve2 is the virtualization host. The design below carries every one of the rules.

> hivemill is PUBLIC, so self-hosted runners NEVER run fork/external PRs; fork PRs stay on GitHub-hosted runners, with approval required for outside contributors; ephemeral, non-root, sandboxed runner users; no factory secrets on the runner; runner labels chosen by trusted branch/author only; placement: a runner VM on pve2 or [the second worker VM] spare cores, capped so it never starves workers; coordinate with G-057 (T-1585 placement); GitHub-hosted CI stays minimal (lint/smoke + the external-contributor CI); anything needing an owner login/token (runner registration) goes to the director as an exact command, dry-run checked on main; the ADR goes to the owner for review before install (file the owner-blocked install follow-up task, status blocked, waits owner).

| hard rule | where it is designed |
|---|---|
| self-hosted runners NEVER run fork/external PRs | Trust boundary: no runner is registered on the public repository at all |
| fork PRs stay on GitHub-hosted runners, with approval required for outside contributors | What runs where; Owner steps 1 |
| ephemeral, non-root, sandboxed runner users | Runner host |
| no factory secrets on the runner | Runner host (must-not-mount list; the registration token stays on HQ) |
| runner labels chosen by trusted branch/author only | Trust boundary (the label exists only in the private CI repository; the factory decides what is sent there) |
| a runner VM on pve2 or [the second worker VM] spare cores, capped so it never starves workers; coordinate with G-057 (T-1585 placement) | Placement and caps |
| GitHub-hosted CI stays minimal (lint/smoke + the external-contributor CI) | What runs where, with one named exception: the fallback when the runner is offline or full |
| owner login/token steps as exact commands, dry-run checked | Owner steps |
| owner review before install; install task blocked | Status; T-1606 |

## Decision

Run the full suite for trusted PR heads on our own ephemeral runner, and register that runner on a **private CI repository** (`volekerb/hivemill-ci`), never on the public one. The factory pushes a trusted head to the private repository; the suite runs there; the judge reads the result for that exact commit. The public repository keeps hosted runners only.

In one picture:

```
public volekerb/hivemill                      private volekerb/hivemill-ci (no collaborators, no forks)
  pull_request run, hosted:                     push to ci-full/<task>-<sha>, by the factory only
    lint/smoke + affected tests (trusted PR)      -> full-suite.yml on label hm-trusted
    full current suite (fork PR, after approval)  -> one ephemeral runner per job, in the runner VM
  no self-hosted runner registered here         result read by the judge for that head sha
```

Names used below are proposals for T-1606: workflow file `.github/workflows/full-suite.yml`, check name `full-suite`, runner label `hm-trusted`, config key `ci_full_runner` (`off`, `self`, `hosted`), tick unit `ci_full`.

### Trust boundary

**How a job is routed.** Routing is not done inside any workflow file, because a pull request runs the workflow file from its own head: a `runs-on` value computed in a job is computed by code the PR author controls. Routing is done by the factory, outside GitHub:

1. A new tick unit (`ci_full`) decides per PR head. A head is trusted only if all hold: the PR's head repository is `volekerb/hivemill` (not cross-repository), the PR author is in the allow-list `ci_trusted_authors` (initially `volekerb`; never a bot, so Dependabot PRs are not trusted), and the branch is `factory/*`. It reads these from the GitHub API and the factory DB, never from PR-controlled text.
2. For a trusted head `H` it pushes `H` to `refs/heads/ci-full/<task>-<sha12>` of the private repository. Only that push starts `full-suite.yml` there (trigger `push: branches: ['ci-full/**']`).
3. It then creates exactly as many just-in-time runners as that run has queued jobs, up to the free job slots, and hands each one-time runner configuration to the runner VM together with the expected run id.
4. A job-started hook inside the sandbox refuses any job that is not that run (below).

**GitHub mechanisms relied on, each with the page and the sentence:**

| # | mechanism | sentence relied on | page |
|---|---|---|---|
| G1 | A repository-level runner serves one repository | "Repository-level runners are dedicated to a single repository, while organization-level runners can process jobs for multiple repositories in an organization." | https://docs.github.com/en/actions/concepts/runners/self-hosted-runners |
| G2 | A job with no matching runner only waits, then fails | "If GitHub doesn't find an online and idle runner that matches the job's runs-on labels and groups, then the job will remain queued until a runner comes online. If the job remains queued for more than 24 hours, the job will fail." | https://docs.github.com/en/actions/reference/runners/self-hosted-runners |
| G3 | Fork PR runs wait for approval | "Require approval for all external contributors": "All users that are not a member or owner of this repository and not a member of the organization will require approval to run workflows." | https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/enabling-features-for-your-repository/managing-github-actions-settings-for-a-repository |
| G4 | Approval is not a sandbox | "If you are using self-hosted runners, potentially malicious user-controlled workflow code will execute automatically if the user is allowed to bypass approval in the set approval policy or if the pull request is approved." | same page as G3 |
| G5 | Fork PR runs get no secrets and a read-only token | "With the exception of GITHUB_TOKEN, secrets are not passed to the runner when a workflow is triggered from a forked repository. The GITHUB_TOKEN has read-only permissions in pull requests from forked repositories." | https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows |
| G6 | A just-in-time runner takes one job | "These self-hosted runners perform at most one job before being automatically removed from the repository, organization, or enterprise." and "With ephemeral runners, this can be guaranteed because GitHub only assigns one job to a runner." | https://docs.github.com/en/actions/reference/security/secure-use and https://docs.github.com/en/actions/reference/runners/self-hosted-runners |
| G7 | A pre-job hook can refuse a job before any step runs | "The script defined in this environment variable is triggered when a job has been assigned to a runner, but before the job starts running." and "If there is any other exit code, the job will not run and will be marked as failed." and "Scripts have access to the default variables." | https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/run-scripts |
| G8 | Creating a runner needs admin on that repository only | "The authenticated user must have admin access to the repository." Fine-grained tokens: "Administration" repository permissions (write). | https://docs.github.com/en/rest/actions/self-hosted-runners |
| G9 | A cache written by a PR run cannot reach other runs | "When a cache is created by a workflow run triggered on a pull request, the cache is created for the merge ref (refs/pull/.../merge). Because of this, the cache will have a limited scope and can only be restored by re-runs of the pull request. It cannot be restored by the base branch or other pull requests targeting that base branch." | https://docs.github.com/en/actions/reference/workflows-and-actions/dependency-caching |
| G10 | Self-hosted minutes cost nothing | "The following usage remains free independently and does not reduce the included compute usage: Standard GitHub-hosted runners in public repositories. [...] Self-hosted runners." | https://docs.github.com/en/billing/concepts/product-billing/github-actions |

G3 names the setting as GitHub shows it today. `docs/ci-cost.md` calls it "Require approval for all outside collaborators", the older label for the same option.

**The job-started hook.** The sandbox image sets `ACTIONS_RUNNER_HOOK_JOB_STARTED` to a read-only script (G7). It exits non-zero unless `GITHUB_REPOSITORY` is `volekerb/hivemill-ci`, `GITHUB_EVENT_NAME` is `push`, `GITHUB_REF` starts with `refs/heads/ci-full/`, and `GITHUB_RUN_ID` equals the run id the factory passed when it started the sandbox. These are default variables set by GitHub from the job message before any workflow step runs. So a runner serves the one run it was created for, or nothing.

**Attacks considered.**

| attack | what stops it | how it will be tested (in T-1606, before `ci_full_runner=self`) |
|---|---|---|
| A fork PR that changes runs-on to the self-hosted label | Structural: the public repository has no self-hosted runner (G1), so the job has nothing to run on; it waits and fails after 24 hours (G2). Before that, the run waits for approval (G3), and the approver refuses any fork PR that touches `.github/`. The policy test keeps the label out of every workflow that has a `pull_request` trigger. | Drill from a throwaway fork: a PR that sets `runs-on: hm-trusted` in `tests.yml`. Expected: run "awaiting approval"; after a drill approval the job stays queued and `gh api repos/volekerb/hivemill/actions/runners --jq .total_count` prints 0. Policy test: a must-reject fixture with the label in a `pull_request` workflow, a must-accept fixture for `full-suite.yml`. |
| A fork PR that changes the routing job | There is no routing job in any workflow. Routing is the tick unit, which reads the PR's head repository and author from the GitHub API. A fork author cannot push to the private repository (no collaborators), and nothing a fork PR writes is read as a routing input. | Unit tests of the tick unit through the CLI: a cross-repository PR, a non-allow-listed author, a Dependabot PR and a non-`factory/*` branch are each never pushed to the private repository; a trusted head is. Each test removed-fix checked (C4). |
| A trusted branch that pulls untrusted code (an agent merges outside commits, a test downloads something, a prompt-injected change) | It cannot be prevented, so every job is treated as hostile and contained: fresh non-root sandbox per job, no factory secrets, no LAN, egress to GitHub only, and nothing survives the job (Runner host). The job's `GITHUB_TOKEN` belongs to the private repository only. It is not assumed read-only: the workflow file in the pushed head can ask for write permissions. That is accepted because the private repository holds nothing but copies of public commits, the token ends with the job, it cannot reach the public repository, and the judge trusts only the run id the tick unit recorded for the head it pushed itself. The hook binds the runner to the one expected run. | Canary job in the private repository, run at every image rebuild and daily: it tries to read each must-not-mount path, reach HQ, the LAN and a non-allow-listed host, become root, and write outside the workspace. Every attempt must fail, and a canary that succeeds at any of them pages the director and sets `ci_full_runner=hosted`. |
| A poisoned cache or workspace left for the next job | One job per runner registration (G6); the sandbox root file system comes from a read-only image and the workspace is deleted with the sandbox; `full-suite.yml` uses no `actions/cache` and no `cache:` option (toolchains are in the image); the image is built on the runner VM from a pinned definition on `main`, never by a job. On the public side G9 already scopes PR caches. | Two consecutive jobs: the first writes marker files to the workspace, `$HOME`, `/tmp` and the tool directories; the second asserts none exists. After each job the private repository's runner count returns to 0. Policy test rejects `actions/cache` and `cache: true` in `full-suite.yml`. |

Two more cases were checked. A same-repository PR that adds the label to `tests.yml` gets a queued job with no runner (G1, G2) and a red policy test. A worker that pushes to the private repository by hand cannot: the worker guard allows a coder only `git push origin HEAD:factory/<task-id>`, and the private repository's token is readable by the tick unit only.

**What the design does not claim.** The suite runs code written by agents, so a full-suite job can fake its own result, exactly as a hosted job can today. That is why reviewer, QA and judge stay, and why a change under `.github/` stays in the risky tier.

Two residual risks are accepted and named. First, a pushed head may contain another workflow with an unfiltered `push` trigger; it would start in the private repository on a hosted runner. The cost is bounded by the private repository's free minutes with a $0 budget, it never reaches our runner (the hook), and the canary reports any run in the private repository that is not `full-suite.yml`. Second, "no runner on the public repository" is checked at install and by Owner steps 5, not continuously, because the factory token cannot read that list. Only the owner can break it ("To add a self-hosted runner to a user repository, you must be the repository owner", https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/add-runners); Owner steps 5 is repeated after every change to the runner setup.

### Runner host

- **Ephemeral.** One job per registration: a just-in-time runner configuration (G6) is used once. Each job gets a new sandbox and a new, empty workspace; both are destroyed when the job ends, whatever its result.
- **Non-root.** The runner process runs as the unprivileged user `ci-run` inside the sandbox, with no sudo, no setuid binaries and no added capabilities.
- **Sandbox layer.** Two layers: a dedicated KVM virtual machine (its own kernel, shared with no worker and no HQ process), and inside it one rootless Podman container per job with a read-only root file system, a tmpfs workspace, and per-job CPU, memory and process caps. If the suite's own namespace tests cannot run inside a rootless container (to be measured in the pilot), the fallback is one short-lived VM per job cloned from a template snapshot; the trust boundary does not change.
- **No factory secrets, tokens or DB on the runner.** The runner VM holds only one-time runner configurations and the image. It must NOT mount or contain: `~/factory/db`, `~/.claude`, `~/.cursor`, gh credentials (`~/.config/gh`, `GH_TOKEN`), ssh keys (`~/.ssh`), the NFS export of `~/factory` that the second worker VM mounts, the owner-terms file, any `.env`. The token that creates runners (G8) lives on HQ in a 0600 file used by the tick unit; it is scoped to the private repository only and cannot change anything on the public one. HQ reaches the VM over ssh with a key that is forced to one command (start a job sandbox); the VM has no key that reaches HQ.
- **Network.** Outbound only, to the hosts GitHub lists as required for a runner: `github.com`, `api.github.com`, `*.actions.githubusercontent.com`, `codeload.github.com`, `results-receiver.actions.githubusercontent.com`, `*.blob.core.windows.net` (https://docs.github.com/en/actions/reference/runners/self-hosted-runners). Runner self-update is off (the image pins the version), so the update hosts are not opened. Everything else is dropped, including all private address ranges (HQ, the second worker VM, the Proxmox hosts, NFS). Inbound: ssh from HQ only. The suite needs no other network: Python and Go are standard-library only and the toolchains are in the image.

### Placement and caps

**Where.** A dedicated VM on pve2 (recommended). A VM has its own memory and its own kernel, so the caps are the VM size and no worker shares a user id or a file system with a job. The alternative, a cgroup slice on the second worker VM's spare cores, is weaker on both counts and smaller (below).

**Caps, pilot size.**

| cap | value | reasoning |
|---|---|---|
| VM size | 8 vCPU, 16 GB RAM, 60 GB disk | 4 job slots plus 4 GB for the OS, the image and the page cache |
| parallel jobs | 4 parallel jobs | 8 vCPU / 2 vCPU per job |
| per job | 2 vCPU, 3 GB RAM, 4096 processes | container limits; a job over 3 GB is killed and fails visibly. 3 GB is twice the 1500 MB worker floor of T-1585; the real peak is measured in the pilot |
| CPU | 8 vCPU is the hard cap; `cpuunits 50` | the VM can never use more than its 8 vCPU; half the default weight of 100, so when pve2 is contended the worker VMs win |
| memory | fixed 16 GB, ballooning off | the runner can never take memory from another VM. Precondition: pve2 has 16 GB unallocated after every existing VM, plus 2 GB host reserve. pve2 was not measured for this ADR (the task forbids ssh to it); Owner steps 4 checks it |

**Coexistence with G-057 T-1585.** The runner VM is not a worker machine: it is never listed in `worker_machines`, so placement never puts an agent on it and its load never counts against a worker share. Because its memory is fixed and separate, `MemAvailable` on the HQ VM and the second worker VM does not move when CI runs, so `machine_mem_floor_mb` (1500 MB) and the worker shares by nproc (8 and 16) keep their meaning. Nothing in T-1585 changes.

**Why not the second worker VM's spare cores.** That VM has 16 vCPU and 11.9 GB RAM (measured). With T-1585 it carries two thirds of the worker slots; 16 workers at 500 to 900 MB each already reach 8 to 14 GB. Cores are spare there, memory is not. A slice on the second worker VM would be capped at `CPUQuota=400%` (4 cores), `MemoryMax=8G` (2 jobs of 3 GB plus 2 GB for the runner processes and page cache), `CPUWeight=20`, 2 parallel jobs, and T-1585's floor would then refuse workers while CI runs. It also mounts `~/factory` over NFS, which is on the must-not-mount list. It stays the fallback only if pve2 has no room.

**Capacity against demand.** A slot gives 24 job-hours a day. A full run is 2.5 job-hours (measured on hosted runners; assumption A1: a 2 vCPU slot of ours is no slower, measured in the pilot). The factory starts a full run when a head enters review with green hosted checks, merge candidates first, and cancels runs of superseded heads; assumption A2: 1.3 full runs per merged PR, so 3.25 job-hours per merge.

| size | job-hours a day | full runs a day | merges a day it carries |
|---|---|---|---|
| slice on the second worker VM, 2 parallel jobs | 48 job-hours | 19 | 14 |
| pilot VM, 4 parallel jobs (8 vCPU, 16 GB) | 96 job-hours | 38 | 29 |
| target VM, 14 parallel jobs (28 vCPU, 48 GB) | 336 job-hours | 134 | 103 |

Against the 11 job-hours per merged PR of M37505: with the skip set on (T-1608) a PR costs about 4.4 job-hours on hosted runners (the plan's 2.5x estimate), so the 480 hosted job-hours carry about 109 PRs a day, if the full run is somewhere else. The pilot VM carries the full run of 29 of them; the rest overflow to hosted (What runs where) at 3.25 job-hours each. Solving 4.4x + 3.25(x - 29) = 480 gives about **75 merges a day with the pilot VM**, against about 45 today. **100 a day needs about 14 parallel jobs**, or a faster suite, or one full run per batch of candidates instead of per PR. The pilot's first job is to replace A1 and A2 by measurements; the size for 100 a day is an owner decision (Open questions 2).

### What runs where

| | GitHub-hosted (public repository) | self-hosted (private CI repository) |
|---|---|---|
| trusted PR, every head | `tests.yml`: lint/smoke and the affected tests (the skip set of T-1607, switched on by T-1608) | nothing |
| trusted PR, head in review | nothing more | `full-suite.yml`: the full suite, before merge (owner D2) |
| fork PR, after approval | `tests.yml`: the full current suite (the skip set never applies to a cross-repository PR; T-1608 must carry that condition) | never |
| Dependabot PR | `tests.yml`: the full current suite | never |
| push to main | `tests.yml` as decided in T-1592 and T-1608 | never |

GitHub-hosted CI stays minimal: lint/smoke plus the external-contributor CI.

**Required checks.** The required check names do not change: `unittest`, `unittest-shard (1)` to `unittest-shard (10)`, `go`, and the `guard-*` jobs that feed `unittest`. They keep coming from the hosted `tests.yml` run on the public repository, with step-level skips so every name still reports. Branch protection is not edited.

The full run reports under one new name, `full-suite`, a fan-in over jobs named `full-*`. The names are new on purpose: a second set of jobs with the required names on the same commit would make the required result ambiguous. `full-suite` is required by the factory judge (`required_checks`, per-PR: same-repository PRs only), not by GitHub: the judge reads the private run for the exact head sha and refuses to merge without a green one. `full-suite.yml` must run the same commands as the push-to-main run of `tests.yml`; a parity test in T-1606 compares the two command lists so they cannot drift.

**When the runner is offline** (or full): fall back to hosted, never skip. The tick unit keeps its own queue and pushes a head to the private repository only when a job slot is free, so a run never waits inside GitHub behind another run. Two timers, both measured by the tick unit: a head that has waited 30 minutes in the factory queue, or a pushed run of which no job has started 10 minutes after the push, falls back. Falling back is, in this order: cancel the private run and wait until it reports cancelled, delete the private branch and the unused just-in-time runners, push the same head to `ci-hosted/<task>-<sha12>` on the public repository, where `full-suite.yml` runs the same jobs on `ubuntu-latest`, and raise a named alert (`ci-runner: offline or saturated since <time>`). Only one full run per head is live at a time. The judge accepts a green `full-suite` for the head sha from the run the tick unit recorded as current; a cancelled run counts as no result, and a failed one refuses the merge until a new head or an explicit re-run.

This fallback is a deliberate exception to "GitHub-hosted CI stays minimal": while the runner is down or full, and for Dependabot PRs, the full suite runs on hosted runners. The other choice is to hold (`ci_full_fallback hold`, proposed key), which keeps hosted CI minimal and stops trusted merges until the runner is back. The owner may pick it when accepting this ADR; the default proposed here is fall back. Justification: a hold would stop every merge on one VM's health, and a queue waits up to 24 hours (G2); falling back costs only speed (it is today's hosted capacity) and keeps the gate identical. A missing `full-suite` result is a refusal, never a pass.

**Gate not weakened.** Judge, reviewer, QA and CI all stay. Every head that merges has a green full suite on that exact commit, run on a machine the PR author cannot reach, in a sandbox that keeps nothing from earlier jobs.

### Policy changes

This ADR changes none of these. T-1606 makes them, in the same PR that adds `full-suite.yml`, after the owner accepts this ADR.

`docs/ci-cost.md`, section Policy, first bullet. From:

> GitHub Actions runs on **free standard hosted runners only** (`ubuntu-latest`, `ubuntu-24.04`, `ubuntu-22.04`). No self-hosted, larger or paid runners, no paid features. The paid Actions budget is **$0**.

to:

> GitHub Actions runs on **free standard hosted runners** (`ubuntu-latest`, `ubuntu-24.04`, `ubuntu-22.04`). One exception, ADR 0005: `.github/workflows/full-suite.yml` may run on the factory's own ephemeral runner (label `hm-trusted`), which is registered only on the private CI repository. No self-hosted runner is ever registered on this repository. No larger or paid runners, no paid features. The paid Actions budget is **$0**.

Same file, Owner steps 2: rename the option to "Require approval for all external contributors". Same file, rule table row (a): "every `runs-on:` is `ubuntu-latest`, `ubuntu-24.04` or `ubuntu-22.04`; only `full-suite.yml` may instead use the one exact expression below, and only while its triggers are exactly `push` on `ci-full/**` and `ci-hosted/**`".

`tests/test_ci_policy.py` rule (a): `ALLOWED_RUNNERS` stays. Add one file name (`full-suite.yml`) and one exact string, `${{ startsWith(github.ref, 'refs/heads/ci-hosted/') && 'ubuntu-latest' || 'hm-trusted' }}`, accepted as `runs-on` in that file only. In that file also require: no `pull_request`, `pull_request_target`, `workflow_dispatch` or `workflow_call` trigger; push branches exactly `ci-full/**` and `ci-hosted/**`; no `actions/cache` step and no `cache:` option. Every other workflow keeps today's rule unchanged. The branch check needs a small new parser: today `_triggers()` returns trigger and child key names, not the branch values. New fixtures, each through `check_dir()`: must reject the label in `tests.yml`, any other expression, a `pull_request` trigger in `full-suite.yml`, an extra push branch, a cache step; must accept the exact form.

### Owner steps

These go to the director, who sends them to the owner as written. `<...>` marks a value the owner fills in. The factory token cannot read repository administration, so a checked read returns HTTP 403 naming the documented endpoint; that confirms the path, and the owner's own login returns the value.

1. Confirm fork approval on the public repository. UI: `volekerb/hivemill` > Settings > Actions > General > "Approval for running fork pull request workflows from contributors" > "Require approval for all external contributors" > Save. Read back: `gh api repos/volekerb/hivemill/actions/permissions/fork-pr-contributor-approval --jq .approval_policy` (expected `all_external_contributors`). DRY-RUN CHECKED: the read-back command, run on main with the factory token, returned 403 with `documentation_url` ending `#get-fork-pr-contributor-approval-permissions-for-a-repository`.
2. Create the private CI repository: `gh repo create volekerb/hivemill-ci --private --disable-issues --disable-wiki`. DRY-RUN CHECKED: `gh repo view volekerb/hivemill-ci --json name` returned "Could not resolve to a Repository" (the name is free), and `gh repo create --help` lists `--private`, `--disable-issues`, `--disable-wiki`.
3. Create the factory's token for that repository. UI: account Settings > Developer settings > Personal access tokens > Fine-grained tokens > Generate new token; name `hm-ci-runner`; expiration 90 days; Repository access "Only select repositories": `volekerb/hivemill-ci`; Repository permissions: Administration read and write, Contents read and write, Workflows read and write, Actions read and write; no other permission, no other repository. Hand it to the director for the 0600 file named in T-1606. NOT CHECKABLE: creating a token is UI-only and makes a secret; the Workflows permission is confirmed by the first pilot push.
4. Create the runner VM on pve2. First read the free memory: `pvesh get /nodes/pve2/status` (needs 18 GB free). Then: `qm create <vmid> --name hm-ci-runner --cores 8 --memory 16384 --balloon 0 --cpuunits 50 --net0 virtio,bridge=<bridge>,firewall=1 --scsi0 <storage>:60`. NOT CHECKABLE: this task may not ssh to pve2 and the factory worker host has no Proxmox tool (`qm` not found); the director checks the flags with `qm create --help` on pve2 before sending. The egress rules of Runner host are a rules file delivered and tested by T-1606 (the canary); the owner applies it to this VM's firewall.
5. Verify that no runner is registered on the public repository, now and after the install: `gh api repos/volekerb/hivemill/actions/runners --jq .total_count` (expected `0`). DRY-RUN CHECKED: run on main with the factory token, returned 403 with `documentation_url` ending `#list-self-hosted-runners-for-a-repository`.

No step changes branch protection, the public repository's secrets or its Actions permissions.

### Rollback

One step: `factory config set ci_full_runner hosted`. From the next tick every full run goes to the hosted fallback path on the public repository and no runner is created; the gate is unchanged. Just-in-time runners that never ran a job are removed by GitHub by themselves ("If JIT runners never run a job, they will automatically be removed", https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/remove-runners). The VM can then be stopped at any time; the owner may also delete the token and the private repository. `ci_full_runner off` together with the T-1608 switch off returns CI to exactly today's layout.

### Open questions for the owner

1. **Is a private CI repository acceptable?** Options: (a) yes, as designed (recommended: it follows GitHub's own advice and makes "never a fork PR" structural); (b) register the runner on the public repository and rely on approval plus the hook (see Alternatives; not recommended); (c) no self-hosted runner.
2. **How large may the runner be?** Options: (a) pilot VM on pve2, 8 vCPU and 16 GB, about 75 merges a day, then grow after two weeks of measurements (recommended); (b) go straight to 28 vCPU and 48 GB for about 100 a day, if pve2 has it; (c) the slice on the second worker VM, 4 cores and 8 GB, about 68 a day and less isolation.
3. **When does the full run start?** Options: (a) when a head enters review with green hosted checks, candidates first (recommended: failures surface during review, about 1.3 runs per merge); (b) only after reviewer and QA PASS (about 1.0 run per merge, but adds the run's wall time before every merge); (c) on every pushed head (about 3.4 runs per merge; needs about 35 parallel jobs).
4. **Should GitHub itself require `full-suite`?** Options: (a) no, the judge requires it and branch protection stays as it is (recommended for the first two weeks; nothing merges outside the judge); (b) yes, the factory posts a commit status `full-suite` on the public head and branch protection requires it (needs a "Commit statuses: write" permission on the factory's public token, and fork PRs then need the hosted fallback path).
5. **Who approves fork PR runs, and by what rule?** Options: (a) the owner only, and never a fork PR that touches `.github/` without reading the diff (recommended); (b) the director on the owner's behalf with the same rule.

## Consequences

- The 20-job ceiling stops being the limit for trusted PRs. With the pilot size the expected ceiling moves from about 45 to about 75 merges a day; 100 needs the larger VM or fewer full runs.
- The public repository's security posture does not change: no runner, no new secret, no new permission, no settings change beyond confirming fork approval.
- New parts to own: a private repository, one token (90-day expiry, on HQ), one VM, one tick unit, one image. The token expiring or the VM failing degrades to hosted speed with a named alert; it never opens the gate.
- The full-suite result of a trusted PR is not shown on the public PR page (Open questions 4). The judge's merge record names the run.
- `full-suite.yml` duplicates the command list of `tests.yml`; the parity test is what keeps them equal.
- Private repositories on the free plan have small artifact storage, so `full-suite.yml` uploads no artifacts and every job there is self-hosted (zero hosted minutes; G10).
- A fork PR costs the same as today and waits for approval as today.
- Follow-ups: T-1606 (install, blocked on the owner; the pm re-specs it from this ADR), T-1607 (skip set, off), T-1608 (skip set on, `full-suite` required by the judge, only after T-1606), G-050 SC5 (CI layout: `tests.yml` single-writer order ends with T-1606 and T-1608).

## Alternatives

- **Runner registered on the public repository, routed by a `runs-on` expression or a `workflow_dispatch` run, guarded by fork approval and the job-started hook.** Rejected as the main design. With a user-owned repository there is no runner group to restrict it; approval is a human step and GitHub states approved code "will execute automatically" on the runner (G4); the token that creates runners would need Administration write on the public repository (G8), which also covers its branch protection. Every one of these is avoided by the private repository. The hook is kept as a second layer.
- **Move the repository to an organization for runner groups.** Rejected for now: additional groups need a paid plan ("Organization owners using the GitHub Team plan can create additional organization-level runner groups", https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/manage-access), the default group refuses public repositories unless overridden ("By default, only private repositories can access runners in a runner group, but you can override this", same page), and a transfer changes every URL.
- **A persistent runner cleaned between jobs.** Rejected: GitHub's own page says of destroy-after-job schemes without one-job registration that "there is no way to guarantee that a self-hosted runner only runs one job" (https://docs.github.com/en/actions/reference/security/secure-use). Just-in-time runners give that guarantee (G6).
- **Runner on the second worker VM as a cgroup slice.** Rejected as the first choice: memory, not CPU, is the scarce resource there, workers share the user id, and `~/factory` is mounted. Kept as the fallback (Placement and caps).
- **Stay on hosted runners only, with the skip set.** Possible, but then the only full run of a normal PR's tree is the run on main after the merge. The owner chose a full run before merge (D2).
- **A paid plan or larger hosted runners.** Rejected by owner decision D1 (free plan, budget $0).
