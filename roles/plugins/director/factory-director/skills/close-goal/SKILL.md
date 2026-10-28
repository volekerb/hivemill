---
name: close-goal
description: Director steps to sign off, close and reopen a goal after the pm audit. Use when a lead says a goal's build tasks are done, or a done goal must be reopened.
---

# Closing and reopening goals (director)

A goal closes only through an audit and your sign-off. The old status-to-done shortcut is refused for you; the owner alone keeps it as a logged override.

1. When a lead says the goal's build tasks are done, the pm audits it (`factory goal audit`). Read the audit with `factory goal show G-xxx` and check the success criteria yourself against merged PRs and real behaviour. If it reports gaps, the lead runs a follow-up round (at most 2 audit rounds; a third comes to you).
2. If the latest audit is ready and you agree: `factory goal signoff G-xxx --audit N --reason "…"`. Only the director (or the owner) signs off, and only the latest audit.
3. Close the goal: the pm or you run `factory goal close G-xxx --reason "audit vN ready, signed off"`. The close is refused if any task changed or was added after the sign-off; re-audit and sign off again.
4. Then `factory team close <team>`, a final eval, and a report to the boss.

To reopen a done goal: `factory goal status G-xxx active --note "why"`. Only you and the owner may, and the note is required. Audit and close it again the same way.
