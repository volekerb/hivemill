# Worker sandbox

Status: stub. T-F fills in the rest (threat model, rollout order per role, owner steps). This file only has the section the spawn wiring (G-010 T-0162) needs.

## Flag and rollback

The sandbox is behind one config key, `sandbox_roles`: a comma list of roles whose `claude` runs in the namespace sandbox. Empty (the default) means off, which is exactly today's behaviour.

- Turn on: `factory config sandbox_roles reviewer` (later `reviewer,qa,coder`). Only roles in `scout.workerbox.SANDBOXABLE` (reviewer, qa, coder) are accepted. Any other role (boss, director, lead, pm, judge, cleaner, staff-engineer, scout, evaluator, unknown) is refused with exit 2 and the stored value stays as it was. Whoever may run `factory config` today may set it; nothing new.
- Turn off (rollback): `factory config sandbox_roles ''`.
- The value is read from the DB at every spawn. Agents that are already running keep their sandbox (or lack of one) until they are done; the change applies to the next spawn and the next relaunch of each agent. A relaunch of a sandboxed agent reuses its agent dir, so `--resume` finds the session.
- The golden test is `tests/test_sandbox_spawn.py` (`GoldenTests`). It compares the stubbed multiplexer calls and claude arguments of a lead, reviewer, qa and coder spawn with `tests/fixtures/spawn_golden_main.json`, recorded on origin/main before the flag existed: with the flag unset, empty, or set and cleared again, they are byte-identical; with `reviewer` set, only the reviewer's pane `PATH` differs.

### Fail-closed preflight

A role that is listed never starts unsandboxed. Before any pane is placed, the spawn checks: user namespaces work; the DB is at `<FACTORY_HOME>/db/factory.db`; the worker oauth token and the role's gh token are regular files, mode 0600, owned by the owner, not empty (only their metadata is read); a real `claude` is on the PATH; qa and coder run in a per-task clone (`.git` is a directory, G-010 T-G); the cwd is one the sandbox accepts (a reviewer with no task worktree is refused: reason `cwd`).

When a check fails, no pane and no live row is created, an `sandbox-refused` event is logged with the reason, the director gets one message per reason per hour, and the spawn exits 3. Fix the cause, or roll back with the command above.

A queued sandboxed spawn that preflight refuses is cancelled by the tick (agents row `cancelled`, event `dequeued` with `by=sandbox-preflight`), never started unsandboxed, and the spawning team's lead (the director when there is no team) gets one message, `sandbox refused queued spawn <name>`, with the reason. Every refusal reason needs a human fix, not time: fix it and spawn again.

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
