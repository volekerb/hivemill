---
name: restriction-check
description: List every automated writer of a target before proposing a restriction, and say how each is affected. Use when proposing or approving branch protection, deny rules, caps, permission narrowing or any other repo or infra restriction.
---

# Restriction check

A restriction that blocks an automated writer breaks the factory silently (M733). Before you propose or approve one, write this list.

## Automated writers to check
- the tick: exports/state commits and pulls
- the judge: `gh pr merge` through `factory judge`
- the cleaner: branch and worktree deletes
- the systemd units: `factory-up`, `factory-tick`, `herdr-server`
- coders' pushes to `factory/<task>` branches
- any CLI path: `factory` subcommands that write to the target

## Steps
1. Name the target (branch, file, directory, permission, cap).
2. For each writer above, say whether it writes to the target and how the restriction affects it: unaffected, needs an exception, or breaks.
3. If any writer breaks, change the proposal or add the exception first. Do not send the restriction until every writer still works.
4. Put the list in the proposal to the boss or lead.

## Worked example
Branch protection on `main` would have broken the tick's state commits and the exports it pushes (M729, M733). The list shows that at once.

Sources: M729, M733.
