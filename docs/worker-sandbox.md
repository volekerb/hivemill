# Worker sandbox

Goal G-010. Workers of the listed roles run `claude` inside an unprivileged user and mount namespace with an allow-list view of the owner's home. They log in with a worker-only token and a per-role GitHub token, so a worker cannot reach the owner's Claude login, ssh keys, gh login, systemd user bus or multiplexer socket. It is behind one config flag and is **not a full security boundary** (section 6).

Code: `scout/nsbox.py` (allow mode, see [scout-nsbox.md](scout-nsbox.md)), `scout/workerbox.py` (the worker profile and shim), `bin/factory` (`sandbox_roles`, preflight, spawn wiring). Tests: `tests/test_nsbox_allow.py`, `tests/test_workerbox.py`, `tests/test_sandbox_spawn.py`.

Status at the time of writing (checked against the code on main): present are T-0159 (DB in its own dir), T-0160 (nsbox allow mode), T-0161 (worker profile), T-0162 (flag, preflight, golden test), T-0225 (workerbox nits), T-0209 (tmpfs sizes) and T-0174 (sandbox escape detection). Still **pending**, and marked where it matters below: T-0165 (per-task `git clone --shared`) and T-0167 (qa and coder rw on clones).

## 1. What it is

**View.** Stage 1 (`unshare -r -m`) puts an empty tmpfs over the real home, `/run/user/<uid>`, `/tmp` and `/dev/shm`, binds back only what the worker profile lists, drops every capability, sets no_new_privs and execs the real `claude` by absolute path. Everything is exec-only, so claude keeps the pid, process group and session of the pane's foreground process.

| | what |
|---|---|
| Hidden (empty tmpfs) | the whole of `$HOME`: owner Claude OAuth (`~/.claude`), `~/.ssh`, the gh config and the git credential helper in `~/.gitconfig`, `~/.zshrc`, `~/.local/bin` (except the two links below); `/run/user/<uid>`: the systemd user bus and the ssh-agent proxy socket; the multiplexer control socket and agent socket (they live under `~/.config`); `/tmp` and `/dev/shm`. `SSH_AUTH_SOCK` and `DBUS_SESSION_BUS_ADDRESS` are not in the env either |
| Visible read-only | `~/factory` (code, `roles/`, `run/`, `docs/`, `.git`), the claude link and its install dir under `~/.local/share/claude`, the `~/.local/bin/factory` link, the agent's own `bin/` and `gitconfig`, system dirs |
| Visible read-write | `~/factory/db/` (the DB, WAL needs the whole dir), the agent's `claude/` config dir, and the cwd for qa and coder (read-only for the reviewer) |
| Masked | `~/factory/worktrees/` is an empty tmpfs; only the agent's own cwd is bound back, so a worker does not see other tasks' worktrees |
| Network | ON. Workers need the Claude API, GitHub and the local factory server. No egress filter |
| Pid namespace | none (`pid=False`) |

Why no pid namespace: a non-init user namespace already denies `/proc/<pid>/environ`, `root` and `fd` of processes outside it, so a pid namespace adds no credential protection (`test_outside_process_environ_is_denied`). Without one the chain is exec-only: no fork, no reaping, no signal forwarding, and the multiplexer still sees a claude with the same pid and argv. Cost: a worker sees the pids and command lines of every owner process and can signal them (section 6). Because of that, **owner-side tools must never put secrets in argv.**

**Per-agent dir** `~/.local/state/factory/workers/<agent>/` (written by `workerbox.prepare_agent` in HQ, mode 0700): `bin/claude` (the shim, 0755, contains no secret), `gitconfig` (user.name and user.email copied from the owner's global git config, plus a credential helper that hands git `$GH_TOKEN`), and `claude/` (the persistent `CLAUDE_CONFIG_DIR`, seeded once with onboarding done and the cwd trusted, so `--resume` finds the session after a relaunch). `bin/` and `gitconfig` are read-only binds inside; `claude/` is worker-writable and therefore never followed through a symlink by HQ.

**Shim.** The pane's `PATH` starts with `<agent dir>/bin`, so the pane's `claude` is the shim. It runs outside the namespace as the owner, reads the two token files, builds the env from an allow-list (`workerbox.WORKER_ENV_PASS` plus `LC_*`: `PATH`, `LANG`, `TERM`, the three claude knobs of `nsbox.CLAUDE_ENV_PASS`, `FACTORY_AGENT`, `FACTORY_ROLE`, `FACTORY_HOME`, `HERDR_PANE_ID`), sets `HOME`, `CLAUDE_CONFIG_DIR`, `GIT_CONFIG_GLOBAL`, `DISABLE_AUTOUPDATER=1`, `CLAUDE_CODE_OAUTH_TOKEN` and `GH_TOKEN`, and execs stage 1. Tokens are env only, never argv, never the shim file. Missing, symlinked, wrong-mode, foreign-owned or empty token: exit 3, claude never starts.

**Spawn.** `factory spawn` reads `sandbox_roles` at every spawn. For a listed role it runs the preflight (section 5), writes the agent dir, puts the agent's `bin/` first on the pane `PATH` and records `sandbox: true` in the agent's args. `staff-engineer` and `evaluator` are never sandboxed by this flag (the evaluator has its own sandbox, [skill-scout.md](skill-scout.md#eval-sandbox)).

**tmpfs sizes** (T-0209, merged). `/tmp` is 512m and home 64m (runtime dir and `/dev/shm` stay 8m; other hide tmpfs default to 8m); memory-backed, only what is written is used, worst case about 576 MB per worker on this box.

**DB.** The live DB is `~/factory/db/factory.db` ([db-layout.md](db-layout.md)). Inside, `~/factory` is read-only and `~/factory/db/` read-write, so the worker's `factory` commands (`inbox`, `msg`, `task`, `done`) work, and `bin/` and `roles/hooks` (code HQ runs) are not writable. The hidden control socket breaks nothing: no worker-callable command needs it. Plan said a `FACTORY_DB` override; the merged code has none (only `FACTORY_HOME` picks the DB, see db-layout.md).

### qa/coder profile (T-0167)

qa and coder run with `rw_cwd=True` on a per-task clone (`git clone --shared`, objects borrowed from `<factory>/.git` through alternates, `origin` = the real remote). `tests/test_workerbox_clone.py` proves it from inside a real sandbox: `git status/log/diff/commit`, `git fetch origin` and `git merge origin/main`, `git push -u origin HEAD`, a qa fetch of `refs/pull/<n>/head` checked out detached, the `GH_TOKEN` credential helper, and `factory done` (refused with an unpushed commit, accepted after the push). No change to `scout/workerbox.py` was needed.

| | inside the sandbox |
|---|---|
| Read-write | the task's own clone (its `.git` included: commits, branches, `FETCH_HEAD`), `~/factory/db/`, the agent's `claude/` config dir, the private `$HOME` and `/tmp` tmpfs |
| Read-only | `~/factory` including `~/factory/.git` (`config`, `hooks/`, `info/attributes`, `refs/`, `objects/`: every write fails with EROFS), the claude install, the agent's `bin/` and `gitconfig` |
| Not visible | other tasks' clones (`~/factory/worktrees/` is masked; only the own clone is bound back), the owner's home, the user bus, the ssh-agent and multiplexer sockets (these are tested in `tests/test_workerbox.py`) |

The clone reads the shared objects through `<clone>/.git/objects/info/alternates`, so `git cat-file` of an object that exists only in `~/factory/.git/objects` works. New objects are written to the clone, never to `~/factory/.git`. `git push` and `git fetch` authenticate with the `credential.https://github.com.helper` of the agent's `gitconfig` (`username=x-access-token`, `password=$GH_TOKEN`: the rw token for coder, the read-only one for qa). The reviewer profile is unchanged: its cwd is read-only.

## 2. Credentials

All three files are owner-created, regular files, mode 0600, owned by the owner, not empty, never a symlink (`workerbox` reads them with `nsbox.read_token`; the preflight checks the metadata only). Never paste a token into a message, a task, a PR or a log.

| file | what | used by | permissions | expiry |
|---|---|---|---|---|
| `~/.config/factory/worker-oauth-token` | Claude setup-token (`claude setup-token`), inference only | every sandboxed role | inference only; revoke at claude.ai settings | long-lived; revoke and recreate to rotate |
| `~/.config/factory/gh-worker-ro-token` | fine-grained PAT, repo `volekerb/hivemill` only | reviewer, qa | Contents: Read; Pull requests: Read and write; Actions: Read; Commit statuses: Read; Metadata: Read (implicit). Cannot push or merge | 90 days at most |
| `~/.config/factory/gh-worker-rw-token` | fine-grained PAT, repo `volekerb/hivemill` only | coder | Contents: Read and write; Pull requests: Read and write; Actions: Read; Commit statuses: Read; Metadata: Read (implicit). **No Workflows permission** | 90 days at most |

Because the rw PAT has no Workflows permission, a push that changes `.github/workflows` is rejected. Tasks that edit CI files run unsandboxed or are done by the owner.

**Known limit.** `main` is protected (PR plus the `unittest` check required, `enforce_admins`, 0 required approvals), so a direct push or force-push to `main` is refused even with the rw PAT. But the rw PAT can **merge a green PR through the API, bypassing `factory judge`**. The owner's own token can do that and far more, so this is not a regression. The real fix is required approvals plus a separate judge identity (a machine user or GitHub App); that is a later step, outside G-010.

The `judge` role is not sandboxed: it merges by design and would need the rw PAT or the owner token inside, which isolates nothing.

## 3. Owner steps (O1-O4)

The director routes these to the boss; the owner runs them in a plain shell on the factory host. The factory does not run them.

**O1, after T-0159 merged (done by the tick, then optional restart).** The DB migration is automatic at the next tick (event `db-migrated`). Check: `ls ~/factory/db/factory.db` and a `factory.db.migrated-<ts>` file next to the old path. Since T-0235, `factory serve` re-executes itself when `web/`, `scout/` or `bin/factory` change, and it resolves the DB path on every request, so a restart is only a belt and braces step: `systemctl --user restart factory-serve`.

**O2, after T-0161 and T-0162 merged, before the step-0 spike.** Worker setup-token:

```
claude setup-token                       # in a plain owner shell; copy the token it prints
umask 077; mkdir -p ~/.config/factory
cat > ~/.config/factory/worker-oauth-token    # paste the token, press Ctrl-D
chmod 600 ~/.config/factory/worker-oauth-token
```

Inference only. To revoke: claude.ai settings, then recreate the file.

**O3, before the reviewer step (step 1).** GitHub, Settings, Developer settings, Fine-grained tokens, Generate new token. Resource owner `volekerb`; Repository access "Only select repositories: hivemill"; Expiration 90 days; permissions Contents: Read, Pull requests: Read and write, Actions: Read, Commit statuses: Read (Metadata Read is added automatically). Save it:

```
umask 077; cat > ~/.config/factory/gh-worker-ro-token    # paste, Ctrl-D
chmod 600 ~/.config/factory/gh-worker-ro-token
```

The reviewer preflight needs this file, so O3 is also needed before the step-0 spike if the spike uses a reviewer (it does). Do O3 together with O2 unless you want the spike to prove the refusal first.

**O4, before the coder step (step 3).** Same as O3 with Contents: Read and write (and still no Workflows), saved to `~/.config/factory/gh-worker-rw-token` (0600). Accept the known limit in section 2.

**Renewal (every 90 days).** Put a reminder at day 80. The PATs are not validated at spawn: an expired one shows up as gh/git auth errors inside the worker (HTTP 401 on `gh pr view`, `git push` asking for credentials). Renew: generate a new token with the same permissions, overwrite the file with the same `umask 077; cat >` and `chmod 600`, no restart needed (the shim reads it at each start; a running worker keeps the old one until its next relaunch). The director may bridge an expiry with `factory config sandbox_roles ''` (section 5).

## 4. Rollout

Order: **0 spike, 1 reviewer, 2 qa, 3 coder, 4 pm**, then the later judge and cleaner steps. The director sets the flag with `factory config` (director only); `factory config` with no arguments prints every key, including `sandbox_roles`. Run each step in a quiet window. Do not go to the next step before the hold time is over and every check is ticked. Roll back at any step with the command in section 5.

Every step: before it, the event log shows no `sandbox-refused` or `start-uncertain` events from an earlier step that you do not understand; after it, none new.

### Step 0: spike (T-0168)
- Preconditions: T-0159, T-0161, T-0162 merged; O1 and O2 done and O3 (the dummy is a reviewer); `db/factory.db` exists; T-0174 (escape detection) and T-0209 (#152) merged. A dummy reviewer task with a task worktree.
- Command: `factory config sandbox_roles reviewer`, then spawn one dummy reviewer.
- Checks:
  1. `factory agents` and the multiplexer's agent list show it as a claude agent that goes working to idle; the agent session id matches the DB session id;
  2. the tick's flagless/claude audit raises no alert (`flagless_state` is `ok`);
  3. esc and `factory stop` work; closing the pane kills it;
  4. `factory relaunch --when-idle` resumes it from the per-agent config dir;
  5. `factory done` closes it;
  6. inside: reading `~/.claude/.credentials.json` fails and `ls ~` shows only the allowed entries; never print secrets;
  7. `readlink /proc/<claude pid>/ns/user` differs from the pane shell's;
  8. `factory config sandbox_roles ''`, then the next spawn is unsandboxed (same user ns as the pane shell).
- Hold: until all eight are ticked (same day). If the multiplexer does not detect the shimmed claude (R1), stop: the fallback is to run the shim as the pane command, which is a new task.

### Step 1: reviewer (T-0169, success criterion 2)
- Preconditions: step 0 passed; O3 done.
- Command: `factory config sandbox_roles reviewer`.
- Checks: a real PR review end to end: `gh pr view`, `gh pr diff`, `gh pr checks` work with the ro PAT; the verdict is recorded; the judge proceeds; no `sandbox-refused`, `start-uncertain` or `sandbox-escaped` event (the last one: T-0174).
- Hold: at least 24 h and 3 reviews with none of those events.

### Step 2: qa (T-0170)
- Preconditions: step 1 held; T-0165, T-0166 (clean for clones) and T-0167 (qa and coder rw on clones) merged: **pending**. Until then the preflight refuses qa and coder with reason `clone`. A task whose cwd is a per-task clone (`.git` is a directory).
- Command: `factory config sandbox_roles reviewer,qa`.
- Checks: QA checks out the PR head, runs the tests and records PASS or FAIL; no write outside the clone; the test count equals an unsandboxed run of the same PR; `factory clean` removes the clone after the merge.
- Hold: 24 h and 3 QA runs with no refused or escape events.

### Step 3: coder (T-0171)
- Preconditions: step 2 held; O4 done; `main` protection still PR plus `unittest` plus `enforce_admins`.
- Command: `factory config sandbox_roles reviewer,qa,coder`.
- Checks: a small real task: commit, `git push` of `factory/<task>`, `gh pr create`, CI green, judge merges. The lead confirms the main protection with `gh api repos/volekerb/hivemill/branches/main/protection`; the rw PAT has no admin and no Workflows; the done guard works (`factory done` refuses uncommitted or unpushed work). Never test by pushing to `main`.
- Hold: 3 tasks merged with no refused or escape events before more coders are sandboxed.

### Step 4: pm (documented, not built)
`pm` is not in `workerbox.SANDBOXABLE`, so `factory config sandbox_roles pm` is refused with exit 2. A pm runs in `~/factory`, which the cwd rule refuses. It would use the reviewer profile with `~/factory` read-only, and `git fetch` inside would fail because `FETCH_HEAD` sits in the read-only `.git`, so pm reads `origin/main` as fetched by HQ. It needs a small follow-up to the cwd rule. Check when built: one plan task end to end. Enable after step 3 is stable.

### Later, not in G-010
- judge: stays outside until a separate judge identity with required approvals exists; then sandbox it with that identity.
- cleaner: needs read-write over `worktrees/` and branch deletion; enable after the legacy worktrees are gone, with a profile whose only rw path is `~/factory/worktrees`.

Each later step is a new task with the same check list.

## 5. Flag and rollback

The sandbox is behind one config key, `sandbox_roles`: a comma list of roles whose `claude` runs in the namespace sandbox. Empty (the default) means off, which is exactly today's behaviour.

- Turn on: `factory config sandbox_roles reviewer` (later `reviewer,qa,coder`). Only roles in `scout.workerbox.SANDBOXABLE` (reviewer, qa, coder) are accepted. Any other role (boss, director, lead, pm, judge, cleaner, staff-engineer, scout, evaluator, unknown) is refused with exit 2 and the stored value stays as it was. Whoever may run `factory config` today may set it; nothing new.
- Turn off (rollback): `factory config sandbox_roles ''`.
- The value is read from the DB at every spawn. Agents that are already running keep their sandbox (or lack of one) until they are done; the change applies to the next spawn and the next relaunch of each agent. A relaunch of a sandboxed agent reuses its agent dir, so `--resume` finds the session.
- The golden test is `tests/test_sandbox_spawn.py` (`GoldenTests`). It compares the stubbed multiplexer calls and claude arguments of a lead, reviewer, qa and coder spawn with `tests/fixtures/spawn_golden_main.json`, recorded on origin/main before the flag existed: with the flag unset, empty, or set and cleared again, they are byte-identical; with `reviewer` set, only the reviewer's pane `PATH` differs.

### Fail-closed preflight

A role that is listed never starts unsandboxed. Before any pane is placed, the spawn checks: user namespaces work; the DB is at `<FACTORY_HOME>/db/factory.db`; the worker oauth token and the role's gh token are regular files, mode 0600, owned by the owner, not empty (only their metadata is read); a real `claude` is on the PATH; qa and coder run in a per-task clone (`.git` is a directory, G-010 T-G); the cwd is one the sandbox accepts (a reviewer with no task worktree is refused: reason `cwd`).

When a check fails, no pane and no live row is created, an `sandbox-refused` event is logged with the reason, the director gets one message per reason per hour, and the spawn exits 3. Fix the cause, or roll back with the command above.

A queued sandboxed spawn that preflight refuses is cancelled by the tick (agents row `cancelled`, event `dequeued` with `by=sandbox-preflight`), never started unsandboxed, and the spawning team's lead (the director when there is no team) gets one message, `sandbox refused queued spawn <name>`, with the reason. Every refusal reason needs a human fix, not time: fix it and spawn again.

### What a running agent does on rollback
Nothing visible: it finishes inside its sandbox. The flag is read at spawn and at `factory relaunch` (which goes through spawn); a relaunch after the flag was cleared starts that agent unsandboxed with its old session (`--resume`). There is no restart step. To force the change now, relaunch it with `factory relaunch --when-idle`.

### DB layout rollback
The DB move has its own rollback (stop the tick and serve, fold the WAL, move the file back, revert T-0159): see [db-layout.md](db-layout.md#rollback). It is independent of the flag: with the flag empty nothing reads `db/`-specific paths except the CLI itself.

### Live rollback check (T-0168)
With a sandboxed dummy reviewer idle, run `factory config sandbox_roles ''`, spawn another dummy reviewer, and compare `readlink /proc/<pid>/ns/user` of its claude with its pane shell's: equal means no namespace. The golden test above is the offline half.

## 6. Residual risk

The sandbox hides the owner's credentials and keeps code HQ runs read-only. It is not a full boundary. A deliberate worker can still:

1. write `factory.db` (impersonate agents, change tasks and verdict rows): `~/factory/db/` is read-write;
2. push any branch other than `main`, and, with the rw PAT, merge a green PR through the API (section 2);
3. exfiltrate its own worker OAuth token and its PAT (revocable and scoped; they are in its environment);
4. see the pids and command lines of outside processes and kill same-uid processes (denial of service; no pid namespace, section 1);
5. reach localhost and LAN TCP services (today DNS and `factory serve` on `203.0.113.5:8765`, which shows DB content the worker can read anyway) and the internet (network is on);
6. read world-readable files outside `$HOME`;
7. plant hooks or config in the git repository it works in. Until per-task clones (T-0165) exist and are used, the worktree shares `~/factory/.git`, which is read-only inside the reviewer sandbox. A lead or the owner who runs plain `git` in a worker's clone would run what the worker planted there: see section 7.

A worker also cannot be assumed to be honest about its own state: the spawn refuses to start a listed role unsandboxed (fail closed), and escape detection (T-0174, merged) closes a sandboxed role's claude found in the host user namespace with event `sandbox-escaped` and alerts the director, so a bare `claude --resume` pane started by the multiplexer after a restart is caught.

## 7. Operating notes

- **Status line.** The role settings' `statusLine` script lives under the hidden `~/.claude`, so inside the sandbox the status line is blank. Accepted.
- **pm and `git fetch`.** A sandboxed worker with a read-only `.git` cannot `git fetch`: it writes `FETCH_HEAD` into `.git`. Reviewers read `origin/main` as HQ last fetched it. qa and coder fetch into their own clone.
- **Never run plain `git` in a worker's clone** or worktree. Its config and hooks are worker-controlled. Use factory commands, or `git -c core.hooksPath=/dev/null -c core.fsmonitor=false <cmd>` (see [git-hardening.md](git-hardening.md)).
- **Where things live.** Agent dirs: `~/.local/state/factory/workers/<agent>/`, mode 0700, kept across relaunches. Inside, `/tmp` and the Claude scratchpad are private tmpfs and are lost when the agent exits. Cleanup: nothing removes the agent dirs yet; T-0166 (`factory clean` for clones and stale agent dirs older than 7 days) is pending. Until then the owner removes dirs of finished agents by hand: only when the agent's row is `done` and its pane is gone.
- **Refusals.** `sandbox-refused` events carry the reason: `role`, `code-root`, `userns`, `db-layout`, `token`, `claude`, `cwd`, `clone`, `prepare`. The director gets one message per reason per hour, the spawn exits 3, and a queued row stays queued and is retried by the tick. Fix the cause or roll back. The refusal text never names a token path.
- **Tests.** The userns tests skip with the reason from `userns_available()` (GitHub runners often block userns). Acceptance for any change here needs a run on the factory host of `test_nsbox_allow`, `test_workerbox` and `test_sandbox_spawn` with 0 skipped.

### Rollout: sandbox one agent (`spawn --sandbox`)

`sandbox_roles` is factory-wide: listing `reviewer` sandboxes every reviewer, including other teams' queued ones. For a spike or a canary use the per-spawn opt-in instead:

    factory spawn reviewer --task T-0168 --goal G-010 --team g-010-a --sandbox

- Only that agent is sandboxed. Other spawns of the same role stay as they are, and `sandbox_roles` is not touched.
- Only `reviewer`, `qa` and `coder` accept it. Any other role exits 2 (`--sandbox: <role> cannot be sandboxed`) before anything is placed.
- The same fail-closed preflight applies as for a listed role (see above): a failed check exits 3 and creates no pane.
- The choice is stored in the agent's `spawn_args` (`sandbox_optin: true`) at spawn time. A spawn that is queued (capacity gate) keeps it when the tick starts it later, and `factory relaunch` (also `--fresh`, `--when-idle`) keeps it too.
- Who may set it: only `factory spawn`, which is already refused to workers (`refuse_worker`) and limited to director, lead and boss. No config key, message or env var can set it.
- Why a flag and not agent names in `sandbox_roles`: names there would turn the role-checked list into a mixed list, and `check_sandbox_roles` and every reader would have to tell roles from names. A flag keeps the decision on the one spawn, recorded in that spawn's own row.
- Staged rollout: canary with `--sandbox` on a single agent, then `factory config sandbox_roles reviewer` for the role, then `reviewer,qa,coder`. Rollback of a canary: stop the agent and spawn it again without the flag (a running agent keeps its sandbox until then).

## factory test

`factory test [--] <cmd...>` runs a test command in the current directory (a coder's or qa's clone or worktree) under the same profile as the gate sandbox (`scout/testbox.py` over `scout/gatebox.py`): own network namespace (only `lo`), own pid namespace, capabilities dropped, `$HOME`, the runtime dir, `/tmp`, `/dev/shm` and `/var/tmp` empty, and an env allow-list (no `GH_TOKEN`, `GITHUB_TOKEN`, `CLAUDE_CODE_OAUTH_TOKEN`, `SSH_AUTH_SOCK`, `GIT_*`, `FACTORY_AGENT`). The cwd tree is writable; the repo's git common dir and `FACTORY_MAIN` are read-only, and the `FACTORY_MAIN` state dirs (`db`, `run`, `logs`, `worktrees`, `exports`, `state`) are masked. `TMPDIR` and `FACTORY_HOME` point at a per-run scratch dir `<cwd>/.factory-test-*` that is removed when the command ends. When the tree is a git worktree, its top-level `.git` pointer file is bound read-only too: a writable pointer would let the run redirect `gitdir` to a directory with `core.hooksPath`/`core.fsmonitor`, which the next git command outside the box would execute. The exit code of `<cmd>` is the exit code of `factory test`.

Config flag `test_sandbox` (director only, default `0`):

- `0`: the command runs directly, as before.
- `1`: the command runs sandboxed. If the sandbox cannot be built (no user namespaces, bad cwd), the command is not run and `factory test` exits 3 with an error naming the rollback. It never falls back to running unsandboxed.
- anything else: config error, exit 3, the command is not run.

Rollback: `factory config test_sandbox 0`. Nested use: tests that need a user namespace skip inside the boxed gate (`FACTORY_GATEBOX=1`).

The scratch dir sits inside the tree as an untracked `.factory-test-*`: it is removed on exit and on SIGINT/SIGTERM/SIGHUP, but a SIGKILL of `factory test` leaves it behind, so delete a stale one before `git add -A` (`.factory-test-*` is gitignored).

See also: [ADR 0002, sandbox runtime v3](adr/0002-sandbox-runtime-v3.md).

### Subordinate uid (`test_sandbox_subuid`, T-0682)

Config flag `test_sandbox_subuid` (director only, default `0`; only meaningful with `test_sandbox 1`). With `1` the box does not run as the owner's host uid. `scout/nsbox.py --launch` creates the user namespace, `newuidmap`/`newgidmap` map inside uid 0 to the owner (stage-1 setup only) and inside uids 1.. to the owner's `/etc/subuid` range, and the final process is dropped to inside uid 1 (a host uid such as 100000) with inside gid 0 (the owner's primary group), no supplementary groups and no capabilities. Owner-only (0600) files are unreadable, the engine socket and the docker group are out of reach even without the `/run` mask, and `kill` of an owner pid fails with EPERM even in a shared pid namespace.

- Fail closed: no `newuidmap`/`newgidmap` (install the `uidmap` package), no `/etc/subuid` or `/etc/subgid` range for the owner, or a failing probe means `factory test` exits 3 and the command is not run. It never falls back to the owner uid.
- Known limit: the tree must be writable through the owner's group (mode `g+w`; the scratch and gate dirs are chmod 0770 by the profile). Files the run creates are owned by the subordinate uid and group-owned by the owner's group. Owner files that are group-readable stay readable to the box; 0600 ones do not.
- Rollback: `factory config test_sandbox_subuid 0` (back to the owner uid with the same namespaces), or `factory config test_sandbox 0`.
