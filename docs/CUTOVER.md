# Cutover: moving factory development to the public repository

The owner decided (2026-10-28) that the factory is open-sourced and that all further factory work happens in the public repository `volekerb/hivemill` (see [PUBLISHING.md](PUBLISHING.md)). This is the runbook for that move. It is one sequence with the public-repository checklist in PUBLISHING.md: phases 2 and 4 below are checklist steps handled by the G-012 lead.

Replace <private-repo> with the private repository slug (owner knows it).

**Actors.** OWNER is the human. DIRECTOR is the factory director. BOSS is the boss agent. G012 is `lead-g-012-b`. No agent changes a git remote or a GitHub setting except where a step says the DIRECTOR runs a `factory` command; every GitHub-side step is an OWNER step.

Nothing here is run by the person who reads it first. Each step names who runs it, in order.

## Before you start

Phase 0, prerequisites. All three must hold before F1.

- **P0.1** The public-text and cutover work (G-021 T1 to T5) is merged on `<private-repo>` main. The local gate (G-005 T-0515, T-0516) is merged and switched on for project `factory` (`projects.gate_cmd` is set). T-0507 (`factory project update`) is merged.
- **P0.2** OWNER: GitHub > Settings > Emails: turn on "Keep my email addresses private" and "Block command line pushes that expose my email".
- **P0.3** OWNER: the owner terms file exists outside the repository (the same file G-012 uses). Note its path (`<owner terms file>`); step C2 needs it.

## Phase 1: freeze the private repository

Freeze before the final export, so the export holds every merged change and nothing has to be ported back.

- **F1** DIRECTOR: stop dispatching new coder tasks for project `factory`.
- **F2** DIRECTOR: `gh pr list -R <private-repo> --state open`. For each open PR decide merge-before-freeze, port, or close. Record the decision as a task note on the PR's task.
- **F3** DIRECTOR: wait until no `factory` project task is `in_progress` or `in_review` and `git -C ~/factory worktree list` shows only the main checkout (the cleaner is done).
- **F4** DIRECTOR: `FREEZE_SHA` = `git -C ~/factory rev-parse origin/main`. Store it as a note on T-0531. No merges to `<private-repo>` after this.

## Phase 2: export and create

Steps 1 and 2 of the public-repository checklist in [PUBLISHING.md](PUBLISHING.md).

- **L1** G012: `python3 ci/fresh_export.py --out <export dir> --author "volekerb <11260070+volekerb@users.noreply.github.com>" --ref <FREEZE_SHA> --public-repo volekerb/hivemill`. It must report 0 hits. The director signs off.
- **L2** OWNER: create the empty repository `volekerb/hivemill` (private for now). Settings > Actions > General > "Disable actions" **before the first push** (no paid minutes are spent while it is private; see [ci-cost.md](ci-cost.md)). Then push from the export directory only.

## Phase 3: cutover

The factory keeps running in `~/factory`; the database, `state/`, `exports/`, `run/` and `worktrees/` are untouched. Only the git remote and a few config values change.

- **C1** OWNER: `systemctl --user stop factory-tick.timer` (pause the tick).
- **C2** DIRECTOR:
  ```
  factory config state_push                                    # must print 0
  factory config public_text_repos volekerb/hivemill
  factory config frozen_repos <private-repo>
  factory config scrub_terms_file <owner terms file>
  ```
  From here the public text guard applies to `volekerb/hivemill` and nothing may push to or merge into `<private-repo>`.
- **C3** OWNER (repo-local, untracked; worktrees share it):
  ```
  git -C ~/factory config user.name volekerb
  git -C ~/factory config user.email 11260070+volekerb@users.noreply.github.com
  ```
  The previous repo-local identity may hold the owner's real name and email. The guard checks commit identities on push, so this must be done before any push.
- **C4** OWNER:
  ```
  git -C ~/factory branch -m main private-main
  git -C ~/factory remote set-url origin https://github.com/volekerb/hivemill.git
  git -C ~/factory fetch --prune origin
  git -C ~/factory switch -c main --track origin/main
  ```
  Ignored directories (`db/`, `state/`, `exports/`, `run/`, `worktrees/`) are untouched. `private-main` and the local `state` branch stay local only.
- **C5** DIRECTOR: `factory project update factory --gh volekerb/hivemill`.
- **C6** DIRECTOR, verify:
  - `git -C ~/factory remote -v` shows hivemill only.
  - `git -C ~/factory log --oneline origin/main | wc -l` prints 1 (or the number of commits pushed since).
  - `SCRUB_TERMS_FILE=<owner terms file> python3 ci/scrub_scan.py --tree ~/factory` reports 0 hits (SC5).
  - `git -C ~/factory status --porcelain` shows no tracked changes.
  - `factory export` reports the state branch is not pushed.
- **C7** OWNER: `systemctl --user start factory-tick.timer`.
- **C8** DIRECTOR: release T-0532 (contributor flow). A coder worktree is created from hivemill: `git -C <worktree> remote -v` shows hivemill.
- **C9** Evidence (SC1): T-0532 goes spec, PR on hivemill, reviewer, QA, judge merge on the local gate. Record the PR URL and merge sha as a note on T-0532.

## Rollback (before the flip only)

C1, `git -C ~/factory switch private-main`, set `origin` back to `https://github.com/<private-repo>.git`, `factory config public_text_repos ""`, `factory config frozen_repos ""`, `factory project update factory --gh <private-repo>`, then C7.

After the repository is public there is no rollback of published text. Fix forward.

## Phase 4: launch

Steps 3 to 11 of the public-repository checklist in [PUBLISHING.md](PUBLISHING.md).

- **P1** BOSS: go/no-go message to the owner (G-012 SC8).
- **P2** OWNER: flip `volekerb/hivemill` to public.
- **P3** OWNER: Billing > Budgets: Actions budget `$0` with "Stop usage when budget limit is reached". Settings > Actions > General: enable Actions; fork pull request workflows: "Require approval for all outside collaborators" (see [ci-cost.md](ci-cost.md)).
- **P4** G012: checklist steps 4 to 11 (`publish_repo.py` dry run and apply, labels, social preview, v0.1.0, README check, starter issues, features).

## Phase 5: port and archive

- **P5** DIRECTOR: for each PR marked "port" in F2, file a follow-up task in hivemill whose spec says: `gh pr diff <N> -R <private-repo> > <scratch>/p.diff`, `git apply` in the hivemill worktree, and the public text guard must pass. Then OWNER or DIRECTOR closes the old PR with the note "moved to volekerb/hivemill#<M>". PRs marked "close" are closed with "superseded by volekerb/hivemill".
- **P6** BOSS to OWNER: cutover report (C6 output, C9 PR URL). The OWNER confirms the cutover (SC7) and OKs the archive. The OWNER archives `<private-repo>` (Settings > Danger Zone > Archive).

## Local-only config (SC5)

Every owner-specific setting stays on this machine. None of it is in the repository: **never committed**.

| setting | where it lives | notes |
|---|---|---|
| `public_text_repos` | DB config (`factory config`) | repos where the public text rule applies; never committed |
| `frozen_repos` | DB config (`factory config`) | repos nothing may push to or merge into; never committed |
| `noreply_email` | DB config (`factory config`) | author email for judge merges into a public repo; empty = the git `user.email` of `~/factory`; must end `@users.noreply.github.com`; never committed |
| `scrub_terms_file` | DB config (`factory config`) | path of the owner terms file; the env var `SCRUB_TERMS_FILE` wins; never committed |
| other DB config (`state_push`, `git_push`, caps) | the live database under `db/` | gitignored; never committed |
| owner terms file | a path outside the repository | holds names, handle, hostnames, private project names; never committed |
| git identity | repo-local `git config user.name` / `user.email` in `~/factory` | set at C3 to the handle and the noreply address; never committed |
| `gh` auth | the `gh` credential store of the owner's user | token never in the repo or in agent text; never committed |
| systemd user units | the owner's user systemd directory (`factory-tick.timer` and friends) | never committed |
| herdr socket | herdr runtime directory of the owner's user | never committed |

The public slug `volekerb/hivemill` and the noreply identity are not private and appear in this repository.

## What the guards enforce

- [public-text-guard.md](public-text-guard.md): the checker that refuses private data in text that becomes public, and its fail-closed rules.
- [worker-guard.md](worker-guard.md) ("Public text (G-021)"): the hook that runs it on every agent `git` and `gh` write and refuses pushes and writes to a frozen repository.
- [state-branch.md](state-branch.md): the `state` branch is never pushed to a public or frozen origin, whatever `state_push` says.
- [ci-cost.md](ci-cost.md): free hosted runners only, a `$0` Actions budget, fork pull requests without secrets, and the kill switch.
- Merge email guard (T-0602), two layers so no real email lands on public `main` again (a web merge once created a merge commit with the owner's real email):
  - Layer 1, `factory judge --merge` into a `public_text_repos` repo: the squash is made with `gh pr merge --author-email <noreply>` (`-A`; checked on gh 2.103.0). The address is config `noreply_email`, else the git `user.email` of `~/factory`, and must end `@users.noreply.github.com`; with none the judge refuses to merge (exit 4, nothing merged). After the merge it reads the merge commit with `gh api repos/<repo>/commits/<sha>`; an author or committer email that is not noreply (GitHub's own `noreply@github.com` committer is allowed) or a commit it cannot read gives an event `merge-email-leak`, an escalation to the director and exit 6. The task is already `done` then: the change is on `main`.
  - Layer 2, tick: for the `origin` of `~/factory` when it is in `public_text_repos`, each tick checks the commits new on `origin/main` (up to the last 200 when there is no earlier check) and sends the boss one escalation per sha whose author or committer email is not noreply. It also catches commits made outside the factory (web UI, owner pushes). The message masks the address.
