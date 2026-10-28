# Goal lifecycle

The pm owns a goal's tickets end to end, but never writes code or git, merges or records verdicts.
- **Audit.** When every build task is done or cancelled, the lead creates an audit pm task and spawns the pm. The pm checks each task against its acceptance list and each success criterion against evidence, then records `factory goal audit G --ready|--gaps --report -`. At most 2 audit rounds; a third goes to the director.
- **Follow-up.** For a gap in merged work: `factory task add … --follow-up-of T-x` (the parent must be a done task of the same goal). Merged (PR) tasks are never reopened.
- **Reopen.** `factory task reopen T --reason TEXT` sends a done task without a PR back to `todo` (pm in its own goal, lead, director, owner). A pm marks a task done only for a non-coder task without a PR, with `--note`; a coder-role task without a PR is closed only by the lead, director or owner.
- **Sign-off.** `factory goal signoff G --audit N --reason TEXT`: director (or owner) only, never a worker process, on the latest `ready` audit.
- **Close.** `factory goal close G --reason TEXT` (pm of the goal, director or owner) needs a ready audit, a sign-off for it, and every task done or cancelled and unchanged since the sign-off (exit 4 otherwise). The old status-to-done shortcut is refused except for the owner, whose override is logged as `owner-override-close`.
- **Reopen a goal.** `factory goal status G active --note TEXT`: director or owner only.
