# Scout roles

Part of the [skill scout](skill-scout.md).

- `scout` triages fetched candidates with `factory scout` (prompt `roles/scout.md`, settings `roles/settings/scout.json.in`). It is read-only: no Edit/Write, no web tools, no `gh pr`/`gh issue`, no git commit/push; `gh api` is allowed for facts. `factory spawn scout` works for the director and the owner only (the tick calls `spawn()` itself); a lead or the boss gets exit 3. It has no task and no goal, and `factory done` still works for it.
- `evaluator` is the subject of a skill eval in a sandbox (`roles/evaluator.md`, `roles/settings/evaluator.json.in`). `factory spawn evaluator` is refused for everyone (exit 3, names `factory skilleval try`); only the skilleval harness spawns it internally. It may write in its sandbox but not under `~/factory` or `~/.claude`, and has no web, `gh`, `pip`, `npm`, `ssh`.
- Both are worker roles: the worker guard blocks herdr and `claude`, and both are refused `spawn`, `tick`, `judge`, `verdict`, `plan`, `task add`, `lesson add` and `goal new`. The guard lets the evaluator run only `factory whoami|task show|inbox|msg|done`, and only against its temp `FACTORY_HOME`, never the live home.
- Both settings files carry the qa deny baseline plus the PRIV deny rules from PR #52 (`sudo`, `su`, `doas`, `pkexec`, `docker`) and run the worker-mode `worker-guard.py` hook (they are workers, not `--hq`).
- The evaluator's `Edit`/`Write` deny rules are tool-level only and do not cover Bash redirects; its real write isolation is the T-0118 sandbox, which hides `~/factory`. The guard is closed by default for the evaluator: any command whose text contains `FACTORY_HOME` is refused, except one leading prefix `FACTORY_HOME=<temp dir> factory <allowed subcommand> ...`. `bin/factory` adds a second layer: with `FACTORY_ROLE=evaluator`, an unset or empty `FACTORY_HOME`, or one that resolves (symlinks, trailing slash) to the live home, exits 3 before any DB is opened. Both are tripwires against accidental or cooperative misuse, not the security boundary.

### Known limits (out of scope, by design)

The boundary is the T-0118 sandbox, which hides `~/factory` so the live DB is not in the evaluator's namespace, plus the T-0111 fail-closed spawn. The guard and the `bin/factory` check do not try to stop arbitrary code:

- `python3 -c` (or any interpreter) that sets or unsets environment variables before calling `factory`;
- string-built variable names such as `export ${X}HOME=...`;
- unsetting or changing `FACTORY_ROLE` / `FACTORY_AGENT`;
- invoking `bin/factory` by path, which skips the `factory` word match.
